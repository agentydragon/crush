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

// E2E workflow template:
// - Live test runs against the real provider with wire logging enabled.
// - We set the config data directory to the per-test artifact dir so provider wire logs
//   and timelines land in an easy-to-inspect place.
// - After live run, we read the provider wire log and use it to refine the mock server
//   so the mock sequence mirrors production behavior.
// - Mock test then validates UI/state evolution deterministically.
// This isolates tests from user config and makes captured artifacts first-class.
func setupServices(t *testing.T, baseURL string, allowedTools []string, dataDir string) (agent.Service, session.Service, message.Service, func()) {
	t.Helper()
	work := t.TempDir()
	// Sandbox the environment to avoid touching user dirs
	oldHome := os.Getenv("HOME")
	oldXDGData := os.Getenv("XDG_DATA_HOME")
	oldXDGConfig := os.Getenv("XDG_CONFIG_HOME")
	os.Setenv("HOME", work)
	os.Setenv("XDG_DATA_HOME", filepath.Join(work, ".local", "share"))
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(work, ".config"))
	// Ensure API key resolves for provider only for mock baseURL
	if !strings.Contains(baseURL, "api.openai.com") {
		os.Setenv("OPENAI_API_KEY", "mock")
	}
	restore := writeProvidersCache(t, baseURL)
	cfg, err := config.Init(work, true)
	require.NoError(t, err)
	// Place all runtime data (including provider wire logs) under the test artifact dir
	if cfg.Options == nil {
		cfg.Options = &config.Options{}
	}
	cfg.Options.DataDirectory = dataDir
	cfg.Options.DebugProviderWire = true
	// Configure provider to point to baseURL
	pc, _ := cfg.Providers.Get("openai")
	pc.BaseURL = baseURL
	pc.GenerationAPI = "responses"
	cfg.Providers.Set("openai", pc)
	// Force large/small to gpt-4o-mini in-memory (no global writes)
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
		// restore env
		os.Setenv("HOME", oldHome)
		if oldXDGData == "" { os.Unsetenv("XDG_DATA_HOME") } else { os.Setenv("XDG_DATA_HOME", oldXDGData) }
		if oldXDGConfig == "" { os.Unsetenv("XDG_CONFIG_HOME") } else { os.Setenv("XDG_CONFIG_HOME", oldXDGConfig) }
		restore()
	}
	return agentSvc, sessions, messages, cleanup
}

func TestAgentResponsesScenarioBasic_Mock(t *testing.T) {
	timer := time.AfterFunc(30*time.Second, func() { t.Fatalf("test timeout (30s)") })
	defer timer.Stop()
	artifactDir := filepath.Join("e2e", "_artifacts", t.Name(), strconv.FormatInt(time.Now().UnixNano(), 10))
	_ = os.MkdirAll(artifactDir, 0o755)
	mock := &mockResponsesServer{}
	ts := httptest.NewServer(mock)
	defer ts.Close()

	agentSvc, sessions, messages, cleanup := setupServices(t, ts.URL+"/v1", []string{"bash"}, artifactDir)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	sess, err := sessions.Create(ctx, "e2e")
	require.NoError(t, err)

	events, err := agentSvc.Run(ctx, sess.ID, "Run bash to echo hi")
	require.NoError(t, err)
	// Assert spinner visible before any events
	var assistant0 message.Message
	for {
		msgs0, err := messages.List(ctx, sess.ID)
		require.NoError(t, err)
		if len(msgs0) >= 2 && msgs0[len(msgs0)-1].Role == message.Assistant {
			assistant0 = msgs0[len(msgs0)-1]
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timeout waiting for assistant message creation: %v", ctx.Err())
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
	require.Empty(t, assistant0.Content().Text)
	require.Empty(t, assistant0.ToolCalls())
	// Emit stage1 (tool call + args + incomplete completion)
	mock.Enqueue(Step{
		Do: []Action{
			{
				Emit: []SSE{
					{Data: map[string]any{"type": "response.output_item.added", "item": map[string]any{"type": "function_tool_call", "id": "toolA", "name": "bash"}}},
					{Data: map[string]any{"type": "response.function_call_arguments.delta", "item_id": "toolA", "delta": "{\"command\":\"echo hi\"}"}},
					{Data: map[string]any{"type": "response.function_call_arguments.done", "item_id": "toolA"}},
					{Data: map[string]any{"type": "response.completed", "response": map[string]any{
						"status": "incomplete",
						"incomplete_details": map[string]any{"reason": "tool_use"},
						"output": []any{
							map[string]any{"type": "function_tool_call", "id": "toolA", "name": "bash", "arguments": "{\"command\":\"echo hi\"}"},
						},
					}}},
				},
			},
		},
	})
	// Wait until tool_calls appear on assistant
	for {
		msgs1, err := messages.List(ctx, sess.ID)
		require.NoError(t, err)
		if len(msgs1) >= 2 && len(msgs1[len(msgs1)-1].ToolCalls()) > 0 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timeout waiting for tool_calls to appear")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
	// Await tool output then complete
	mock.Enqueue(Step{
		WaitUntil: []Condition{{Kind: CondRequestBodyContains, Name: "function_call_output"}},
		Do: []Action{
			{
				Emit: []SSE{
					{Data: map[string]any{
						"type": "response.completed",
						"response": map[string]any{
							"status": "completed",
							"output": []any{
								map[string]any{
									"type": "message",
									"role": "assistant",
									"content": []any{map[string]any{"type": "output_text", "text": "Done"}},
								},
							},
							"usage": map[string]any{"input_tokens": 12, "output_tokens": 2},
						},
					}},
				},
			},
			{Close: true},
		},
	})
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
	require.NotEmpty(t, last.Content().Text)
	require.NoError(t, saveJSON(filepath.Join(artifactDir, "timeline.json"), snapshot("final", msgs)))
}


func TestAgentResponsesScenarioBasic_Live(t *testing.T) {
	timer := time.AfterFunc(30*time.Second, func() { t.Fatalf("test timeout (30s)") })
	defer timer.Stop()
	artifactDir := filepath.Join("e2e", "_artifacts", t.Name(), strconv.FormatInt(time.Now().UnixNano(), 10))
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
	// Assert provider wire log exists for blueprinting the mock
	wireLog := filepath.Join(artifactDir, "logs", "provider-wire.log")
	fi, err := os.Stat(wireLog)
	require.NoError(t, err)
	require.Greater(t, fi.Size(), int64(0))
}
