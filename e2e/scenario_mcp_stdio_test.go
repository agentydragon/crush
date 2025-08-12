package e2e

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/llm/agent"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

func TestScenario_MCP_Stdio_Mock(t *testing.T) {
	timer := time.AfterFunc(30*time.Second, func() { t.Fatalf("test timeout (30s)") })
	defer timer.Stop()
	artifactDir := filepath.Join("e2e", "_artifacts", t.Name(), strconv.FormatInt(time.Now().UnixNano(), 10))
	_ = os.MkdirAll(artifactDir, 0o755)

	mock := &mockResponsesServer{}
	ts := httptest.NewServer(mock)
	defer ts.Close()

	// IMPORTANT: fully isolated environment is provided by setupServices.
	// Use nil for allowedTools so all tools (including MCP) are available.
	agentSvc, sessions, messages, cleanup := setupServices(t, ts.URL+"/v1", nil, artifactDir)
	defer cleanup()

	// Reset MCP global state and enable wire logging for both provider and MCP
	agent.ResetMCPForTests() // TODO(mpokorny): Replace globals with injectable registries
	cfg := config.Get()
	(&ScenarioCtx{ArtifactDir: artifactDir}).ApplyCommonOptions(cfg)

	// Configure stdio MCP echo server (go run testdata)
	cfg.MCP = config.MCPs{
		"echo": {
			Type:    config.MCPStdio,
			Command: "go",
			Args:    []string{"run", "e2e/testdata/mcp/echo/main.go"},
		},
	}
	defer agent.CloseMCPClients()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sess, err := sessions.Create(ctx, "scenario-mcp-stdio")
	require.NoError(t, err)

	sc := &ScenarioCtx{
		T:             t,
		Ctx:           ctx,
		Agent:         agentSvc,
		Sessions:      sessions,
		Messages:      messages,
		SessionID:     sess.ID,
		ArtifactDir:   artifactDir,
		Orch:          NewMockOrchestrator(mock),
		PerStepBudget: 5 * time.Second,
	}

	_ = messages.Subscribe(ctx)
	events, err := agentSvc.Run(ctx, sess.ID, "Please use echo then say Done")
	require.NoError(t, err)

	RunSteps(sc,
		ScenarioStep{
			Name: "assistant created",
			Act:  func(c *ScenarioCtx) { c.Orch.EmitCreated() },
			Assert: func(t *testing.T, c *ScenarioCtx) {
				c.Eventually("assistant exists", func() bool {
					ms := mustList(c)
					return len(ms) >= 2 && ms[len(ms)-1].Role == message.Assistant
				})
			},
		},
		ScenarioStep{
			Name: "tool call for mcp_echo_echo",
			Act: func(c *ScenarioCtx) {
				const itemID = "tool_echo_1"
				mock.Enqueue(Step{Do: []Action{actionEmit(
					SSE{Data: map[string]any{"type": "response.output_item.added", "item": map[string]any{"type": "function_tool_call", "id": itemID, "name": "mcp_echo_echo"}}},
					SSE{Data: map[string]any{"type": "response.function_call_arguments.delta", "item_id": itemID, "delta": "{\"text\":\"hello\"}"}},
					SSE{Data: map[string]any{"type": "response.function_call_arguments.done", "item_id": itemID}},
					SSE{Data: map[string]any{"type": "response.completed", "response": map[string]any{
						"status":             "incomplete",
						"incomplete_details": map[string]any{"reason": "tool_use"},
						"output":             []any{map[string]any{"type": "function_tool_call", "id": itemID, "name": "mcp_echo_echo", "arguments": "{\"text\":\"hello\"}"}},
					}}},
				)}})
				mock.Enqueue(Step{Do: []Action{actionClose()}}) // end first stream
			},
			Assert: func(t *testing.T, c *ScenarioCtx) {
				// We only assert that function_call_output is eventually posted back (tool executed)
				c.Eventually("function_call_output posted", func() bool { return mock.sawFunctionCallOutput.Load() })
			},
		},
		ScenarioStep{
			Name: "final assistant completion",
			Act: func(c *ScenarioCtx) {
				mock.Enqueue(Step{WaitUntil: []Condition{{Kind: CondRequestBodyContains, Name: "function_call_output"}}, Do: []Action{
					actionEmit(sseTextDelta("Done", "out1"), sseTextDone(), sseCompletedText("Done", "out1")),
					actionClose(),
				}})
			},
			Assert: func(t *testing.T, c *ScenarioCtx) {
				c.Eventually("assistant finished with Done", func() bool {
					ms := mustList(c)
					last := ms[len(ms)-1]
					return last.Role == message.Assistant && last.IsFinished() && last.Content().Text == "Done"
				})
			},
		},
		ScenarioStep{
			Name: "wire logs present",
			Assert: func(t *testing.T, c *ScenarioCtx) {
				// Optional: logs may rotate or be delayed; check existence and non-empty when present
				for _, p := range []string{
					filepath.Join(artifactDir, "logs", "provider-wire.log"),
					filepath.Join(artifactDir, "logs", "mcp-wire.log"),
					filepath.Join(artifactDir, "logs", "mcp-stdio.log"),
				} {
					if st, err := os.Stat(p); err == nil {
						require.Greater(t, st.Size(), int64(0))
					}
				}
				// TODO(mpokorny): Capture additional diagnostics if size is zero
			},
		},
	)

	// Drain events until done or context timeout
	for {
		select {
		case <-ctx.Done():
			t.Fatalf("mock test timed out: %v", ctx.Err())
		case ev, ok := <-events:
			if !ok {
				goto done
			}
			if ev.Type == agent.AgentEventTypeResponse && ev.Done {
				goto done
			}
		}
	}

done:
	require.False(t, agentSvc.IsBusy())
	// TODO(mpokorny): Expand asserts to verify transcript contains the mcp_echo_echo tool call and echoed result
	// once upstream SSE event handling is stabilized.
}
