package e2e

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/db"
	"github.com/charmbracelet/crush/internal/history"
	"github.com/charmbracelet/crush/internal/llm/agent"
	"github.com/charmbracelet/crush/internal/lsp"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/stretchr/testify/require"
)

func writeProvidersCache(t *testing.T, catwalkURL string) func() {
	// The loader will fetch providers list from CATWALK_URL; short-circuit via env to our tiny cache
	cachePath := filepath.Join(os.Getenv("HOME"), ".local", "share", "crush", "providers.json")
	_ = os.MkdirAll(filepath.Dir(cachePath), 0o755)
	providersJSON := `[
	  {
	    "name": "OpenAI",
	    "id": "openai",
	    "api_key": "$OPENAI_API_KEY",
	    "api_endpoint": "` + catwalkURL + `",
	    "type": "openai",
	    "default_large_model_id": "gpt-4o-mini",
	    "default_small_model_id": "gpt-4o-mini",
	    "models": [
	      {"id":"gpt-4o-mini","name":"gpt-4o-mini","cost_per_1m_in":0.5,"cost_per_1m_out":1.5,"cost_per_1m_in_cached":0,"cost_per_1m_out_cached":0,"context_window":128000,"default_max_tokens":4096,"can_reason":true,"has_reasoning_efforts":true,"default_reasoning_effort":"low","supports_attachments":false}
	    ]
	  }
	]`
	require.NoError(t, os.WriteFile(cachePath, []byte(providersJSON), 0o644))
	os.Setenv("CATWALK_URL", "file://ignored")
	return func() {
		_ = os.Remove(cachePath)
		os.Unsetenv("CATWALK_URL")
	}
}

func setupServices(t *testing.T, baseURL string, allowedTools []string) (agent.Service, session.Service, message.Service, func()) {
	t.Helper()
	work := t.TempDir()
	// Ensure API key resolves for provider
	os.Setenv("OPENAI_API_KEY", "mock")
	restore := writeProvidersCache(t, baseURL)
	cfg, err := config.Init(work, true)
	require.NoError(t, err)
	// Configure provider to point to baseURL
	pc, _ := cfg.Providers.Get("openai")
	pc.BaseURL = baseURL
	cfg.Providers.Set("openai", pc)
	// Force large/small to gpt-4o-mini
	cfg.Models[config.SelectedModelTypeLarge] = config.SelectedModel{Provider: "openai", Model: "gpt-4o-mini", ReasoningEffort: "low", MaxTokens: 512}
	cfg.Models[config.SelectedModelTypeSmall] = config.SelectedModel{Provider: "openai", Model: "gpt-4o-mini", ReasoningEffort: "low", MaxTokens: 64}
	cfg.SetupAgents()

	ctx := context.Background()
	dbConn, err := db.Connect(ctx, filepath.Join(work, ".crush"))
	require.NoError(t, err)
	q, err := db.Prepare(ctx, dbConn)
	require.NoError(t, err)

	sessions := session.NewService(q)
	messages := message.NewService(q)
	perms := permission.NewPermissionService(work, true, allowedTools)

	agCfg := cfg.Agents["coder"]
	agCfg.AllowedTools = allowedTools

	agentSvc, err := agent.NewAgent(ctx, agCfg, perms, sessions, messages, history.NewService(q, dbConn), map[string]*lsp.Client{})
	require.NoError(t, err)

	cleanup := func() {
		_ = q.Close()
		_ = dbConn.Close()
		restore()
	}
	return agentSvc, sessions, messages, cleanup
}

func TestAgentResponsesScenarioBasic_Mock(t *testing.T) {
	mock := &mockResponsesServer{}
	ts := httptest.NewServer(mock)
	defer ts.Close()

	agentSvc, sessions, messages, cleanup := setupServices(t, ts.URL+"/v1", []string{"bash"})
	defer cleanup()

	ctx := context.Background()
	sess, err := sessions.Create(ctx, "e2e")
	require.NoError(t, err)

	events, err := agentSvc.Run(ctx, sess.ID, "Run bash to echo hi")
	require.NoError(t, err)
	var final message.Message
	for ev := range events {
		if ev.Type == agent.AgentEventTypeResponse && ev.Done {
			final = ev.Message
		}
	}
	require.NotEmpty(t, final.ID)

	msgs, err := messages.List(ctx, sess.ID)
	require.NoError(t, err)
	_ = os.MkdirAll("e2e/_artifacts", 0o755)
	require.NoError(t, saveJSON("e2e/_artifacts/basic.mock.json", snapshot("final", msgs)))
}

func TestAgentResponsesScenarioBasic_Live(t *testing.T) {
	if os.Getenv("E2E_LIVE") == "" || os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("live test disabled")
	}
	agentSvc, sessions, messages, cleanup := setupServices(t, "https://api.openai.com/v1", []string{"bash"})
	defer cleanup()

	ctx := context.Background()
	sess, err := sessions.Create(ctx, "live-e2e")
	require.NoError(t, err)

	events, err := agentSvc.Run(ctx, sess.ID, "say ok")
	require.NoError(t, err)
	for range events {
	}
	msgs, err := messages.List(ctx, sess.ID)
	require.NoError(t, err)
	_ = os.MkdirAll("e2e/_artifacts", 0o755)
	require.NoError(t, saveJSON("e2e/_artifacts/basic.live.json", snapshot("final", msgs)))
}
