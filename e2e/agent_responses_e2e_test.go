package e2e

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/db"
	"github.com/charmbracelet/crush/internal/history"
	"github.com/charmbracelet/crush/internal/llm/agent"
	"github.com/charmbracelet/crush/internal/lsp"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/stretchr/testify/require"
)

func writeProvidersCache(t *testing.T, catwalkURL string) func() {
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

func setupServices(t *testing.T, baseURL string, allowedTools []string, dataDir string) (agent.Service, session.Service, message.Service, func()) {
	t.Helper()
	work := t.TempDir()
	oldHome := os.Getenv("HOME")
	oldXDGData := os.Getenv("XDG_DATA_HOME")
	oldXDGConfig := os.Getenv("XDG_CONFIG_HOME")
	os.Setenv("HOME", work)
	os.Setenv("XDG_DATA_HOME", filepath.Join(work, ".local", "share"))
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(work, ".config"))
	if !strings.Contains(baseURL, "api.openai.com") {
		os.Setenv("OPENAI_API_KEY", "mock")
	}
	restore := writeProvidersCache(t, baseURL)
	cfg, err := config.Init(work, true)
	require.NoError(t, err)
	if cfg.Options == nil {
		cfg.Options = &config.Options{}
	}
	cfg.Options.DataDirectory = dataDir
	cfg.Options.DebugProviderWire = true
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
		os.Setenv("HOME", oldHome)
		if oldXDGData == "" { os.Unsetenv("XDG_DATA_HOME") } else { os.Setenv("XDG_DATA_HOME", oldXDGData) }
		if oldXDGConfig == "" { os.Unsetenv("XDG_CONFIG_HOME") } else { os.Setenv("XDG_CONFIG_HOME", oldXDGConfig) }
		restore()
	}
	return agentSvc, sessions, messages, cleanup
}

func TestAgentResponsesScenario_ToolLess_Mock(t *testing.T) {
	timer := time.AfterFunc(30*time.Second, func() { t.Fatalf("test timeout (30s)") })
	defer timer.Stop()
	cwd, _ := os.Getwd()
	artifactDir := filepath.Join(cwd, "_artifacts", t.Name(), strconv.FormatInt(time.Now().UnixNano(), 10))
	_ = os.MkdirAll(artifactDir, 0o755)
	mock := &mockResponsesServer{}
	ts := httptest.NewServer(mock)
	defer ts.Close()

	agentSvc, sessions, messages, cleanup := setupServices(t, ts.URL+"/v1", []string{}, artifactDir)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sess, err := sessions.Create(ctx, "e2e")
	require.NoError(t, err)

	// Subscribe before running agent to avoid missing updates
	updates := messages.Subscribe(ctx)

	events, err := agentSvc.Run(ctx, sess.ID, "Say ok")
	require.NoError(t, err)

	// Wait for assistant CreatedEvent
	createdDeadline := time.After(5 * time.Second)
	for {
		select {
		case <-createdDeadline:
			t.Fatalf("timeout waiting for assistant creation via pubsub")
		case ev := <-updates:
			if ev.Type != pubsub.CreatedEvent { continue }
			m := ev.Payload
			if m.SessionID == sess.ID && m.Role == message.Assistant {
				goto haveAssistant
			}
		}
	}
	haveAssistant:

	// Emit a canonical minimal output message sequence then completed
	const itemID = "msg_out"
	mock.Enqueue(Step{Do: []Action{
		actionEmit(
			sseOutputItemAdded(itemID),
			sseContentPartAdded(itemID),
			sseTextDelta("ok", itemID),
			sseTextDone(),
			sseContentPartDone(itemID, "ok"),
			sseOutputItemDone(itemID, "ok"),
			sseCompletedText("ok", itemID),
		),
		actionClose(),
	}})

	// Wait for text via DB only (Eventually)
	textDeadline := time.After(5 * time.Second)
	for {
		msgsNow, _ := messages.List(ctx, sess.ID)
		if len(msgsNow) >= 2 {
			last := msgsNow[len(msgsNow)-1]
			if last.Role == message.Assistant && last.Content().Text == "ok" && last.IsFinished() {
				break
			}
		}
		select {
		case <-textDeadline:
			t.Fatalf("timeout waiting for assistant text in DB")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	var final message.Message
	for {
		select {
		case <-ctx.Done():
			t.Fatalf("mock test timed out: %v", ctx.Err())
		case ev, ok := <-events:
			if !ok { goto done }
			if ev.Type == agent.AgentEventTypeResponse && ev.Done {
				final = ev.Message
			}
		}
	}

done:
	require.NotEmpty(t, final.ID)
	require.False(t, agentSvc.IsBusy())

	msgs, err := messages.List(ctx, sess.ID)
	require.NoError(t, err)
	last := msgs[len(msgs)-1]
	require.Equal(t, message.Assistant, last.Role)
	require.True(t, last.IsFinished())
	require.Equal(t, "ok", last.Content().Text)
	require.NoError(t, saveJSON(filepath.Join(artifactDir, "timeline.json"), snapshot("final", msgs)))
}

func TestAgentResponsesScenarioBasic_Live(t *testing.T) {
	timer := time.AfterFunc(30*time.Second, func() { t.Fatalf("test timeout (30s)") })
	defer timer.Stop()
	cwd, _ := os.Getwd()
	artifactDir := filepath.Join(cwd, "_artifacts", t.Name(), strconv.FormatInt(time.Now().UnixNano(), 10))
	_ = os.MkdirAll(artifactDir, 0o755)
	if os.Getenv("E2E_LIVE") == "" || os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("live test disabled")
	}
	agentSvc, sessions, messages, cleanup := setupServices(t, "https://api.openai.com/v1", []string{}, artifactDir)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	sess, err := sessions.Create(ctx, "live-e2e")
	require.NoError(t, err)

	events, err := agentSvc.Run(ctx, sess.ID, "Please respond with exactly: ok")
	require.NoError(t, err)
	for {
		select {
		case <-ctx.Done():
			t.Fatalf("live test timed out: %v", ctx.Err())
		case _, ok := <-events:
			if !ok { goto liveDone }
		}
	}

liveDone:
	require.False(t, agentSvc.IsBusy())
	msgs, err := messages.List(ctx, sess.ID)
	require.NoError(t, err)
	require.NoError(t, saveJSON(filepath.Join(artifactDir, "timeline.json"), snapshot("final", msgs)))
	wireLog := filepath.Join(artifactDir, "logs", "provider-wire.log")
	fi, err := os.Stat(wireLog)
	require.NoError(t, err)
	require.Greater(t, fi.Size(), int64(0))
}
