package e2e

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/db"
	"github.com/charmbracelet/crush/internal/history"
	"github.com/charmbracelet/crush/internal/llm/agent"
	crushlog "github.com/charmbracelet/crush/internal/log"
	"github.com/charmbracelet/crush/internal/lsp"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/stretchr/testify/require"
)

func SetupServices(t *testing.T, baseURL string, allowedTools []string, artifactDir string, agentOpts ...agent.AgentOption) (agent.Service, session.Service, message.Service, string, func()) {
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
	cfg, err := config.Init(work, true)
	require.NoError(t, err)
	if cfg.Options == nil {
		cfg.Options = &config.Options{}
	}
	cfg.Options.DisableTitleGeneration = true
	crushlog.Setup(filepath.Join(artifactDir, "logs", "crush.log"), true)
	(&ScenarioCtx{ArtifactDir: artifactDir}).ApplyCommonOptions(cfg)
	pc, _ := cfg.Providers.Get("openai")
	pc.BaseURL = baseURL
	pc.GenerationAPI = "responses"
	cfg.Providers.Set("openai", pc)
	cfg.Models[config.SelectedModelTypeLarge] = config.SelectedModel{Provider: "openai", Model: "gpt-4o-mini", ReasoningEffort: "low", MaxTokens: 512}
	cfg.Models[config.SelectedModelTypeSmall] = config.SelectedModel{Provider: "openai", Model: "gpt-4o-mini", ReasoningEffort: "low", MaxTokens: 64}
	cfg.SetupAgents()

	ctx := context.Background()
	dbConn, err := db.Connect(ctx, filepath.Join(work, ".crush"))
	require.NoError(t, err)
	q, err := db.Prepare(ctx, dbConn)
	require.NoError(t, err)

	sessionsSvc := session.NewService(q)
	messagesSvc := message.NewService(q)
	perms := permission.NewPermissionService(work, true, allowedTools)

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
	return agentSvc, sessionsSvc, messagesSvc, artifactDir, cleanup
}
