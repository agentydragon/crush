package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/app"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/llm/agent"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/session"
	chatcmp "github.com/charmbracelet/crush/internal/tui/components/chat"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func renderChatView(t *testing.T, c *ScenarioCtx) string {
	t.Helper()
	appMinimal := &app.App{
		Messages:    c.Messages,
		Permissions: permission.NewPermissionService(config.Get().WorkingDir(), true, []string{}),
	}
	cmp := chatcmp.New(appMinimal)
	_ = cmp.SetSize(100, 30)
	_ = cmp.SetSession(session.Session{ID: c.SessionID})
	return ansi.Strip(cmp.View())
}

func TestScenario_ParallelToolCalls_Mock(t *testing.T) {
	sc, events, cleanup := NewScenario(t, t.Name(), "", "Use two tools in parallel, then say Done", NewMockOrchestrator(nil), []string{"bash"}, 5*time.Second)
	defer cleanup()
	RunSteps(sc,
		StepAssistantCreated(),
		ScenarioStep{
			Name: "emit two parallel tool calls + args",
			Act: func(c *ScenarioCtx) {
				mock := c.Orch.(*MockOrchestrator).srv
				mock.Enqueue(Step{Do: []Action{
					actionEmit(
						SSE{Data: map[string]any{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{"type": "function_call", "id": "toolA", "name": "bash"}}},
						SSE{Data: map[string]any{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{"type": "function_call", "id": "toolB", "name": "bash"}}},
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
									map[string]any{"type": "function_call", "id": "toolA", "name": "bash", "arguments": "{\"command\":\"echo A\"}"},
									map[string]any{"type": "function_call", "id": "toolB", "name": "bash", "arguments": "{\"command\":\"echo B\"}"},
								},
							},
						}},
					),
					actionClose(),
				}})
			},
			Assert: func(t *testing.T, c *ScenarioCtx) {
				// UI should show waiting state for tool response
				c.Eventually("ui shows pending tool", func() bool {
					view := renderChatView(t, c)
					return strings.Contains(view, "Bash")
				})
			},
		},
		ScenarioStep{
			Name: "final text",
			Act: func(c *ScenarioCtx) {
				mock := c.Orch.(*MockOrchestrator).srv
				mock.Enqueue(Step{WaitUntil: []Condition{{Kind: CondRequestBodyContains, Name: "function_call_output"}}, Do: []Action{
					actionEmit(sseTextDelta("Done", "out1"), sseTextDone(), sseCompletedText("Done", "out1")),
					actionClose(),
				}})
			},
			Assert: func(t *testing.T, c *ScenarioCtx) {
				// existing DB assertion
				StepExpectFinalText("Done").Assert(t, c)
				// UI shows final text and tool success indicator
				c.Eventually("ui shows final text and success", func() bool {
					view := renderChatView(t, c)
					return strings.Contains(view, "Done") && strings.Contains(view, "✓")
				})
			},
		},
	)
	for {
		select {
		case <-sc.Ctx.Done():
			t.Fatalf("mock test timed out: %v", sc.Ctx.Err())
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
	require.False(t, sc.Agent.IsBusy())
}
