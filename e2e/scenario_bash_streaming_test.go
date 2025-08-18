package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/llm/agent"
	"github.com/stretchr/testify/require"
)

// TestScenario_BashStreaming_Real verifies that a real bash command streams output
// and that the pending overlay shows the invoked command immediately.
func TestScenario_BashStreaming_Real(t *testing.T) {
	sc, providerEvents, cleanup := NewScenario(t, t.Name(), "", "Run a streaming bash command", NewMockOrchestrator(nil), []string{"bash"}, 20*time.Second)
	defer cleanup()
	// Subscribe to agent events (tool_state, etc.)
	agentEvents := sc.Agent.Subscribe(sc.Ctx)

	// Create a stepper script in the working dir for this scenario.
	workdir := config.Get().WorkingDir()
	script := "#!/usr/bin/env bash\n" +
		"set -euo pipefail\n" +
		"printf \"step:1\\n\"\n" +
		fmt.Sprintf("while [ ! -f '%s' ]; do sleep 0.05; done\n", filepath.Join(workdir, "go2")) +
		"printf \"step:2\\n\"\n" +
		fmt.Sprintf("while [ ! -f '%s' ]; do sleep 0.05; done\n", filepath.Join(workdir, "go3")) +
		"printf \"step:3\\n\"\n" +
		fmt.Sprintf("while [ ! -f '%s' ]; do sleep 0.05; done\n", filepath.Join(workdir, "done")) +
		"printf \"done\\n\"\n"
	scriptPath := filepath.Join(workdir, "stepper.sh")
	require.NoError(t, os.WriteFile(scriptPath, []byte(script), 0o755))

	// Emit a single bash tool call that runs the stepper.
	RunSteps(sc,
		StepAssistantCreated(),
		ScenarioStep{
			Name: "emit bash call",
			Act: func(c *ScenarioCtx) {
				mock := c.Orch.(*MockOrchestrator).srv
				mock.Enqueue(Step{Do: []Action{
					actionEmit(
						SSE{Data: map[string]any{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{"type": "function_call", "id": "toolA", "name": "bash"}}},
						SSE{Data: map[string]any{"type": "response.function_call_arguments.delta", "item_id": "toolA", "delta": "{\"command\":\"bash -lc './stepper.sh'\",\"timeout\":600000}"}},
						SSE{Data: map[string]any{"type": "response.function_call_arguments.done", "item_id": "toolA"}},
						SSE{Data: map[string]any{"type": "response.completed", "response": map[string]any{
							"status":             "incomplete",
							"incomplete_details": map[string]any{"reason": "tool_use"},
							"output": []any{map[string]any{"type": "function_call", "id": "toolA", "name": "bash", "arguments": "{\"command\":\"./stepper.sh\",\"timeout\":600000}"}},
						}}},
					),
				}})
				mock.Enqueue(Step{Do: []Action{actionClose()}})
			},
			Assert: func(t *testing.T, c *ScenarioCtx) {
				// The pending UI should show the exact command immediately from ToolCall.Input.
				c.Eventually("pending shows command", func() bool {
					// Ensure the assistant tool call is persisted with input first
					msgs := mustList(c)
					seen := false
					for _, m := range msgs {
						if m.Role != "assistant" {
							continue
						}
						for _, tc := range m.ToolCalls() {
							if strings.Contains(tc.Input, "stepper.sh") {
								seen = true
								break
							}
						}
					}
					if !seen {
						return false
					}
					view := renderChatView(t, c)
					return strings.Contains(view, "./stepper.sh")
				})
			},
		},
	)

	// Helper to wait for a streaming detail substring in ToolState events.
	waitToolStateDetail := func(sub string) {
		deadline := time.After(10 * time.Second)
		for {
			select {
			case <-deadline:
				t.Fatalf("timeout waiting for tool_state detail containing %q", sub)
			case e, ok := <-agentEvents:
				if !ok {
					t.Fatalf("agent events channel closed before seeing %q", sub)
				}
				ev := e.Payload
				if ev.Type == agent.AgentEventTypeToolState && strings.Contains(ev.State.Detail, sub) {
					return
				}
			}
		}
	}

	// Step through the script by creating marker files.
	// Create markers in the workdir used by the script and bash tool
	m := func(name string) string { return filepath.Join(workdir, name) }
	waitToolStateDetail("step:1")
	require.NoError(t, os.WriteFile(m("go2"), []byte(""), 0o644))
	waitToolStateDetail("step:2")
	require.NoError(t, os.WriteFile(m("go3"), []byte(""), 0o644))
	waitToolStateDetail("step:3")
	require.NoError(t, os.WriteFile(m("done"), []byte(""), 0o644))

	// Finally assert that a tool result message was persisted and contains the last line.
	sc.Eventually("tool_result persisted", func() bool {
		msgs := mustList(sc)
		for _, m := range msgs {
			for _, tr := range m.ToolResults() {
				if strings.Contains(tr.Content, "done") {
					return true
				}
			}
		}
		return false
	})

	// Instruct the mock server to emit a final assistant message and close the stream.
	if mock, ok := sc.Orch.(*MockOrchestrator); ok {
		mock.srv.Enqueue(Step{WaitUntil: []Condition{{Kind: CondRequestBodyContains, Name: "function_call_output"}}, Do: []Action{
			actionEmit(sseTextDelta("Done", "out1"), sseTextDone(), sseCompletedText("Done", "out1")),
			actionClose(),
		}})
	}
	// Drain provider events until the final response arrives or the channel closes.
	for {
		select {
		case <-sc.Ctx.Done():
			return
		case ev, ok := <-providerEvents:
			if !ok {
				return
			}
			if ev.Type == agent.AgentEventTypeResponse && ev.Done {
				return
			}
		}
	}
}
