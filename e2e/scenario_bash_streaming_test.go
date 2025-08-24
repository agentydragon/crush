package e2e

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/llm/agent"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// TestScenario_BashStreaming_Real verifies that a real bash command streams output
// and that the pending overlay shows the invoked command immediately.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n... [clipped]"
}

func tailFile(path string, maxLines int) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	lines := []string{}
	for s.Scan() {
		lines = append(lines, s.Text())
	}
	if len(lines) <= maxLines {
		return lines
	}
	return lines[len(lines)-maxLines:]
}

func TestScenario_BashStreaming_Real(t *testing.T) {
	sc, providerEvents, cleanup := NewScenario(t, t.Name(), "", "Run a streaming bash command", NewMockOrchestrator(nil), nil, 30*time.Second)
	defer cleanup()
	// Subscribe to agent events (tool_state, etc.)
	_ = sc.Agent.Subscribe(sc.Ctx)

	// Create a stepper script in the working dir for this scenario.
	workdir := config.Get().WorkingDir()
	// Ensure this scenario does NOT auto-approve permissions; we want to see the prompt.
	sc.Permissions.SetSkipRequests(false)
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
					actionClose(),
				}})
			},
			Assert: func(t *testing.T, c *ScenarioCtx) {
				// Wait for a PermissionNotification (prompt visible)
				notifs := c.Permissions.SubscribeNotifications(c.Ctx)
				deadline := time.After(30 * time.Second)
				for {
					select {
					case <-deadline:
						t.Fatalf("timeout waiting for PermissionNotification")
					case ev, ok := <-notifs:
						if !ok {
							t.Fatalf("permission notifications closed before prompt")
						}
						if ev.Payload.ToolCallID == "toolA" && !ev.Payload.Granted && !ev.Payload.Denied {
							// Log current UI and wire tail for visibility
							view := renderChatView(t, c)
							t.Logf("[UI pre-prompt]\n%s", clip(view, 1200))
							wire := filepath.Join(c.ArtifactDir, "logs", "provider-wire.log")
							if lines := tailFile(wire, 40); len(lines) > 0 {
								t.Logf("[wire tail]\n%s", strings.Join(lines, "\n"))
							}
							goto haveNotif
						}
					}
				}
			haveNotif:
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
		ScenarioStep{
			Name: "allow bash execute",
			Assert: func(t *testing.T, c *ScenarioCtx) {
				if pr, ok := permission.GetActiveRequest(c.Permissions); ok {
					c.Permissions.Grant(pr)
				}
				c.Eventually("prompt dismissed", func() bool {
					view := renderChatView(t, c)
					t.Logf("[UI post-grant]\n%s", clip(view, 1200))
					return !strings.Contains(view, "Permission Required") && !strings.Contains(view, "Requesting for permission")
				})
			},
		},
	)

	// Step through the script by creating marker files.
	m := func(name string) string { return filepath.Join(workdir, name) }

	// Spin up a live UI to assert progressive visibility in the detail block under anchor
	cmp := SetupLiveUI(sc)
	anchor := "bash: ./stepper.sh"

	// 1: wait for 'step:1' and ensure 'step:2' not present yet
	sc.Eventually("tool item visible (real)", func() bool {
		view := normalizeView(ansi.Strip(cmp.View()))
		return strings.Contains(view, anchor)
	})
	start := time.Now()
	if !WaitForToolDetailContains(cmp, anchor, "step:1", 1500*time.Millisecond) {
		t.Fatalf("timeout waiting for 'step:1' in detail")
	}
	t.Logf("[timing] step:1 visible at +%s", time.Since(start).Round(time.Millisecond))
	if ToolDetailContainsNow(cmp, anchor, "step:2") {
		t.Fatalf("unexpected next line present prematurely in detail: 'step:2'")
	}
	require.NoError(t, os.WriteFile(m("go2"), []byte(""), 0o644))

	// 2: advance to step 2
	if !WaitForToolDetailContains(cmp, anchor, "step:2", 1500*time.Millisecond) {
		t.Fatalf("timeout waiting for 'step:2' in detail")
	}
	t.Logf("[timing] step:2 visible at +%s", time.Since(start).Round(time.Millisecond))
	if ToolDetailContainsNow(cmp, anchor, "step:3") {
		t.Fatalf("unexpected next line present prematurely in detail: 'step:3'")
	}
	require.NoError(t, os.WriteFile(m("go3"), []byte(""), 0o644))

	// 3: advance to step 3 then done
	if !WaitForToolDetailContains(cmp, anchor, "step:3", 1500*time.Millisecond) {
		t.Fatalf("timeout waiting for 'step:3' in detail")
	}
	t.Logf("[timing] step:3 visible at +%s", time.Since(start).Round(time.Millisecond))
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
