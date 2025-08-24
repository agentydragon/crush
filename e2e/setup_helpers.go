package e2e

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/db"
	"github.com/charmbracelet/crush/internal/history"
	"github.com/charmbracelet/crush/internal/llm/agent"
	"github.com/charmbracelet/crush/internal/logging"
	"github.com/charmbracelet/crush/internal/lsp"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/stretchr/testify/require"
)

// SetupServicesWithConfigAndSkip is the base helper that allows controlling whether
// permission prompts are skipped (auto-approved) or shown.
func SetupServicesWithConfigAndSkip(t *testing.T, baseURL string, allowedTools []string, artifactDir string, skipPermissions bool, customize func(*config.Config), agentOpts ...agent.AgentOption) (agent.Service, session.Service, message.Service, permission.Service, string, func()) {
	t.Helper()
	work := t.TempDir()
	oldHome := os.Getenv("HOME")
	oldXDGData := os.Getenv("XDG_DATA_HOME")
	oldXDGConfig := os.Getenv("XDG_CONFIG_HOME")
	os.Setenv("HOME", work)
	os.Setenv("XDG_DATA_HOME", filepath.Join(work, ".local", "share"))
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(work, ".config"))
	if artifactDir == "" {
		artifactDir = MakeArtifactDir(t, t.Name())
	}
	if strings.Contains(baseURL, "api.openai.com") {
		if os.Getenv("OPENAI_API_KEY") == "" {
			os.Setenv("OPENAI_API_KEY", "placeholder_live_key")
		}
	} else {
		os.Setenv("OPENAI_API_KEY", "mock")
	}
	// Defer logging platform init until after cfg.Init + ApplyCommonOptions so DataDirectory is final.
	cfg, err := config.Init(work, true)
	require.NoError(t, err)
	if cfg.Options == nil {
		cfg.Options = &config.Options{}
	}
	cfg.Options.DisableTitleGeneration = true
	(&ScenarioCtx{ArtifactDir: artifactDir}).ApplyCommonOptions(cfg)
	// Initialize logging once for this scenario
	_ = os.MkdirAll(filepath.Join(artifactDir, "logs"), 0o755)
	_, _ = logging.NewLoggerPlatform(logging.LoggingConfig{
		Level:      slog.LevelDebug,
		AppLogPath: filepath.Join(artifactDir, "logs", "crush.log"),
		Console:    true,
		JSON:       true,
		WireLogs: []logging.WireSinkConfig{
			{Name: "mcp", Path: filepath.Join(artifactDir, "logs", "mcp", "mcp-wire.log"), Rotate: logging.RotationConfig{MaxSizeMB:250, MaxBackups:10, MaxAgeDays:30, Compress:true}},
			{Name: "provider", Path: filepath.Join(artifactDir, "logs", "provider", "provider-wire.log"), Rotate: logging.RotationConfig{MaxSizeMB:250, MaxBackups:10, MaxAgeDays:30, Compress:true}},
		},
	})
	// Force-create per-test log directories and wire logging retention.
	_ = os.MkdirAll(filepath.Join(artifactDir, "logs", "ui"), 0o755)
	_ = os.MkdirAll(filepath.Join(artifactDir, "logs", "mcp"), 0o755)
	if cfg.Options.Wire == nil {
		cfg.Options.Wire = &config.WireOptions{}
	}
	cfg.Options.Wire.MaxSizeMB = 250
	cfg.Options.Wire.MaxBackups = 10
	cfg.Options.Wire.MaxAgeDays = 30
	b := true
	cfg.Options.Wire.Compress = &b
	pc, _ := cfg.Providers.Get("openai")
	pc.BaseURL = baseURL
	pc.GenerationAPI = "responses"
	cfg.Providers.Set("openai", pc)
	cfg.Models[config.SelectedModelTypeLarge] = config.SelectedModel{Provider: "openai", Model: "gpt-4o-mini", ReasoningEffort: "low", MaxTokens: 512}
	cfg.Models[config.SelectedModelTypeSmall] = config.SelectedModel{Provider: "openai", Model: "gpt-4o-mini", ReasoningEffort: "low", MaxTokens: 64}
	if customize != nil {
		customize(cfg)
	}
	cfg.SetupAgents()

	ctx := context.Background()
	// Use the artifact directory as the data directory so each run persists DB + logs under artifacts
	dbBase := filepath.Join(artifactDir, ".crush")
	dbConn, err := db.Connect(ctx, dbBase)
	require.NoError(t, err)
	q, err := db.Prepare(ctx, dbConn)
	require.NoError(t, err)

	sessionsSvc := session.NewService(q)
	messagesSvc := message.NewService(q)
	perms := permission.NewPermissionService(work, skipPermissions, allowedTools)

	agCfg := cfg.Agents["coder"]
	agCfg.AllowedTools = allowedTools

	agentSvc, err := agent.NewAgent(ctx, agCfg, perms, sessionsSvc, messagesSvc, history.NewService(q, dbConn), map[string]*lsp.Client{}, agentOpts...)
	require.NoError(t, err)

	cleanup := func() {
		_ = q.Close()
		_ = dbConn.Close()
		os.Setenv("HOME", oldHome)
		if oldXDGData == "" {
			os.Unsetenv("XDG_DATA_HOME")
		} else {
			os.Setenv("XDG_DATA_HOME", oldXDGData)
		}
		if oldXDGConfig == "" {
			os.Unsetenv("XDG_CONFIG_HOME")
		} else {
			os.Setenv("XDG_CONFIG_HOME", oldXDGConfig)
		}
	}
	return agentSvc, sessionsSvc, messagesSvc, perms, artifactDir, cleanup
}

// Backward-compatible helper: defaults to skipping permission prompts (auto-approve).
func SetupServicesWithConfig(t *testing.T, baseURL string, allowedTools []string, artifactDir string, customize func(*config.Config), agentOpts ...agent.AgentOption) (agent.Service, session.Service, message.Service, permission.Service, string, func()) {
	return SetupServicesWithConfigAndSkip(t, baseURL, allowedTools, artifactDir, true, customize, agentOpts...)
}

func SetupServices(t *testing.T, baseURL string, allowedTools []string, artifactDir string, agentOpts ...agent.AgentOption) (agent.Service, session.Service, message.Service, permission.Service, string, func()) {
	return SetupServicesWithConfig(t, baseURL, allowedTools, artifactDir, nil, agentOpts...)
}
