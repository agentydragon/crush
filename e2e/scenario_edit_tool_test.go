package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/llm/agent"
	"github.com/stretchr/testify/require"
)

func TestScenario_EditTool_Simple_Mock(t *testing.T) {
	// Only allow view and edit
	// TODO(mpokorny): Decide whether to switch this test to explicit permission prompt assertions (pre/post)
	// instead of allowlisting tools for auto-approval, to cover permission UI for edit/write flows.
	sc, events, cleanup := NewScenario(t, t.Name(), "", "View then edit a file, then say Done", NewMockOrchestrator(nil), []string{"view", "edit"}, 10*time.Second)
	defer cleanup()

	workdir := config.Get().WorkingDir()
	fileRel := "test_edit.txt"
	filePath := filepath.Join(workdir, fileRel)

	// Initial file
	require.NoError(t, os.WriteFile(filePath, []byte("hello world\n"), 0o644))

	RunSteps(sc,
		StepAssistantCreated(),
		ScenarioStep{
			Name: "emit view tool call",
			Act: func(c *ScenarioCtx) {
				mock := c.Orch.(*MockOrchestrator).srv
				mock.Enqueue(Step{Do: []Action{actionEmit(
					SSE{Data: map[string]any{"type": "response.output_item.added", "item": map[string]any{"type": "function_call", "id": "view1", "name": "view"}}},
					SSE{Data: map[string]any{"type": "response.function_call_arguments.delta", "item_id": "view1", "delta": "{\"file_path\":\"" + fileRel + "\"}"}},
					SSE{Data: map[string]any{"type": "response.function_call_arguments.done", "item_id": "view1"}},
					SSE{Data: map[string]any{"type": "response.completed", "response": map[string]any{
						"status":             "incomplete",
						"incomplete_details": map[string]any{"reason": "tool_use"},
						"output":             []any{map[string]any{"type": "function_call", "id": "view1", "name": "view", "arguments": "{\"file_path\":\"" + fileRel + "\"}"}},
					}}},
				)}})
				mock.Enqueue(Step{Do: []Action{actionClose()}})
			},
		},
		ScenarioStep{
			Name: "emit edit tool call",
			Act: func(c *ScenarioCtx) {
				mock := c.Orch.(*MockOrchestrator).srv
				oldStr := "hello world"
				newStr := "hello Crush"
				args := "{\"file_path\":\"" + fileRel + "\",\"old_string\":\"" + oldStr + "\",\"new_string\":\"" + newStr + "\"}"
				mock.Enqueue(Step{WaitUntil: []Condition{{Kind: CondRequestBodyContains, Name: "function_call_output"}}, Do: []Action{actionEmit(
					SSE{Data: map[string]any{"type": "response.output_item.added", "item": map[string]any{"type": "function_call", "id": "edit1", "name": "edit"}}},
					SSE{Data: map[string]any{"type": "response.function_call_arguments.delta", "item_id": "edit1", "delta": args}},
					SSE{Data: map[string]any{"type": "response.function_call_arguments.done", "item_id": "edit1"}},
					SSE{Data: map[string]any{"type": "response.completed", "response": map[string]any{
						"status":             "incomplete",
						"incomplete_details": map[string]any{"reason": "tool_use"},
						"output":             []any{map[string]any{"type": "function_call", "id": "edit1", "name": "edit", "arguments": args}},
					}}},
				)}})
				mock.Enqueue(Step{Do: []Action{actionClose()}})
			},
			Assert: func(t *testing.T, c *ScenarioCtx) {
				// Wait for disk change (tool executed)
				c.Eventually("file edited on disk", func() bool {
					b, err := os.ReadFile(filePath)
					return err == nil && strings.Contains(string(b), "hello Crush")
				})
				// UI shows Edit block and file name
				c.Eventually("ui renders edit block", func() bool {
					view := renderChatView(t, c)
					return strings.Contains(view, "Edit") && strings.Contains(view, fileRel)
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
				StepExpectFinalText("Done").Assert(t, c)
			},
		},
	)

	// Drain until done
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
