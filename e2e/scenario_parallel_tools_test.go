package e2e

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/llm/agent"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

func TestScenario_ParallelToolCalls_Mock(t *testing.T) {
	timer := time.AfterFunc(30*time.Second, func() { t.Fatalf("test timeout (30s)") })
	defer timer.Stop()
	artifactDir := filepath.Join("e2e", "_artifacts", t.Name(), strconv.FormatInt(time.Now().UnixNano(), 10))
	_ = os.MkdirAll(artifactDir, 0o755)
	mock := &mockResponsesServer{}
	ts := httptest.NewServer(mock)
	defer ts.Close()

	// No real tools: we only validate streaming/tool bookkeeping; agent will produce
	// synthetic tool results ("Tool not found: …") which is fine.
	agentSvc, sessions, messages, cleanup := setupServices(t, ts.URL+"/v1", []string{}, artifactDir)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	sess, err := sessions.Create(ctx, "e2e-parallel")
	require.NoError(t, err)

	scenario := &ScenarioCtx{
		T:             t,
		Ctx:           ctx,
		Agent:         agentSvc,
		Sessions:      sessions,
		Messages:      messages,
		SessionID:     sess.ID,
		ArtifactDir:   artifactDir,
		Orch:          NewMockOrchestrator(mock),
		PerStepBudget: 3 * time.Second,
	}

	_ = messages.Subscribe(ctx)
	events, err := agentSvc.Run(ctx, sess.ID, "Use two tools in parallel, then say Done")
	require.NoError(t, err)

	RunSteps(scenario,
		ScenarioStep{
			Name: "emit two parallel tool calls + args",
			Act: func(c *ScenarioCtx) {
				mock.Enqueue(Step{Do: []Action{
					actionEmit(
						SSE{Data: map[string]any{"type": "response.output_item.added", "item": map[string]any{"type": "function_tool_call", "id": "toolA", "name": "bash"}}},
						SSE{Data: map[string]any{"type": "response.output_item.added", "item": map[string]any{"type": "function_tool_call", "id": "toolB", "name": "bash"}}},
						SSE{Data: map[string]any{"type": "response.function_call_arguments.delta", "item_id": "toolA", "delta": "{\"command\":\"echo A\"}"}},
						SSE{Data: map[string]any{"type": "response.function_call_arguments.delta", "item_id": "toolB", "delta": "{\"command\":\"echo B\"}"}},
						SSE{Data: map[string]any{"type": "response.function_call_arguments.done", "item_id": "toolA"}},
						SSE{Data: map[string]any{"type": "response.function_call_arguments.done", "item_id": "toolB"}},
						SSE{Data: map[string]any{
							"type": "response.completed",
							"response": map[string]any{
								"status":             "incomplete",
								"incomplete_details": map[string]any{"reason": "tool_use"},
								"output": []any{
									map[string]any{"type": "function_tool_call", "id": "toolA", "name": "bash", "arguments": "{\"command\":\"echo A\"}"},
									map[string]any{"type": "function_tool_call", "id": "toolB", "name": "bash", "arguments": "{\"command\":\"echo B\"}"},
								},
							},
						}},
					),
					actionClose(),
				}})
			},
			Assert: func(t *testing.T, c *ScenarioCtx) {
				c.Eventually("two tool calls finished", func() bool {
					ms, _ := c.Messages.List(context.Background(), c.SessionID)
					if len(ms) == 0 { return false }
					last := ms[len(ms)-1]
					if last.Role != message.Assistant { return false }
					calls := last.ToolCalls()
					if len(calls) < 2 { return false }
					finished := 0
					for _, tc := range calls {
						if tc.Finished { finished++ }
					}
					return finished >= 2
				})
			},
		},
		ScenarioStep{
			Name: "final text",
			Act: func(c *ScenarioCtx) {
				mock.Enqueue(Step{WaitUntil: []Condition{{Kind: CondRequestBodyContains, Name: "function_call_output"}}, Do: []Action{
					actionEmit(sseTextDelta("Done", "out1"), sseTextDone(), sseCompletedText("Done", "out1")),
					actionClose(),
				}})
			},
			Assert: func(t *testing.T, c *ScenarioCtx) {
				c.Eventually("assistant finished with Done", func() bool {
					ms, _ := c.Messages.List(context.Background(), c.SessionID)
					if len(ms) == 0 { return false }
					last := ms[len(ms)-1]
					return last.Role == message.Assistant && last.IsFinished() && last.Content().Text == "Done"
				})
			},
		},
	)

	// Drain agent events until completion
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
}
