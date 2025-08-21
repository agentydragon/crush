package e2e

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/llm/agent"
	"github.com/charmbracelet/crush/internal/llm/tools"
	"github.com/charmbracelet/crush/internal/permission"
)

// fakeSteppingBash streams controlled ToolState updates (1..5) and completes.
type fakeSteppingBash struct{}

func (f *fakeSteppingBash) Name() string { return tools.BashToolName }
func (f *fakeSteppingBash) Info() tools.ToolInfo {
	return tools.ToolInfo{Name: tools.BashToolName, Parameters: map[string]any{"command": map[string]any{"type": "string"}}, Required: []string{"command"}}
}
func (f *fakeSteppingBash) Run(ctx context.Context, call tools.ToolCall) (tools.ToolResponse, error) {
	sink := tools.SinkFromContext(ctx)
	// Simulate progressive output: lines 1..5 at small intervals
	for i := 1; i <= 5; i++ {
		lines := strings.Join([]string{"1", "2", "3", "4", "5"}[:i], "\n")
		sink.Update(tools.ToolState{Phase: tools.PhaseRunning, Title: "bash: stepper", Detail: lines})
		time.Sleep(20 * time.Millisecond)
	}
	return tools.NewTextResponse("1\n2\n3\n4\n5\n\n<cwd>/tmp</cwd>"), nil
}

// TestScenario_BashStreaming_Fake_ShowsPendingTail wires a fake bash tool that streams
// incrementally and asserts the pending overlay shows the last tail lines (with newlines).
func TestScenario_BashStreaming_Fake_ShowsPendingTail(t *testing.T) {
	sc, _, cleanup := NewScenario(t, t.Name(), "", "Run a streaming bash command", NewMockOrchestrator(nil), []string{tools.BashToolName}, 5*time.Second, agent.WithToolOverride([]tools.BaseTool{&fakeSteppingBash{}}))
	defer cleanup()

	RunSteps(sc,
		StepAssistantCreated(),
		ScenarioStep{
			Name: "emit bash call",
			Act: func(c *ScenarioCtx) {
				mock := c.Orch.(*MockOrchestrator).srv
				mock.Enqueue(Step{Do: []Action{actionEmit(
					SSE{Data: map[string]any{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{"type": "function_call", "id": "toolA", "name": tools.BashToolName}}},
					SSE{Data: map[string]any{"type": "response.function_call_arguments.delta", "item_id": "toolA", "delta": "{\"command\":\"stepper\"}"}},
					SSE{Data: map[string]any{"type": "response.function_call_arguments.done", "item_id": "toolA"}},
					SSE{Data: map[string]any{"type": "response.completed", "response": map[string]any{
						"status":             "incomplete",
						"incomplete_details": map[string]any{"reason": "tool_use"},
						"output": []any{map[string]any{"type": "function_call", "id": "toolA", "name": tools.BashToolName, "arguments": "{\"command\":\"stepper\"}"}},
					}}},
				)}})
			},
		},
		// Grant permission so the tool runs and streams
		ScenarioStep{
			Name: "grant permission",
			Assert: func(t *testing.T, c *ScenarioCtx) {
				// Wait for permission prompt, then grant.
				c.Eventually("permission prompt", func() bool {
					_, ok := permission.GetActiveRequest(c.Permissions)
					return ok
				})
				if pr, ok := permission.GetActiveRequest(c.Permissions); ok {
					c.Permissions.Grant(pr)
				}
				// Eventually the UI pending view should show multi-line tail including 1..5
				c.Eventually("pending shows 1..5 tail", func() bool {
					view := renderChatView(t, c)
					return strings.Contains(view, "\n1\n") || strings.HasPrefix(view, "1\n")
				})
				c.Eventually("pending shows 2..5", func() bool {
					view := renderChatView(t, c)
					return strings.Contains(view, "\n2\n")
				})
				c.Eventually("pending shows 3..5", func() bool {
					view := renderChatView(t, c)
					return strings.Contains(view, "\n3\n")
				})
				c.Eventually("pending shows 4..5", func() bool {
					view := renderChatView(t, c)
					return strings.Contains(view, "\n4\n")
				})
				c.Eventually("pending shows 5", func() bool {
					view := renderChatView(t, c)
					return strings.Contains(view, "\n5\n") || strings.HasSuffix(view, "\n5")
				})
			},
		},
	)
}
