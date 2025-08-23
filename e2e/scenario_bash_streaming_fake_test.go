package e2e

// WARNING: This test MUST exercise streaming state step-by-step.
//
// Required behavior (non-negotiable):
// - Bash prints 1 → the UI must show "1".
// - Step forward → Bash prints 2 → the UI must show "1\n2".
// - Step forward → Bash prints 3 → the UI must show "1\n2\n3".
// - Step forward → Bash prints 4 → the UI must show "1\n2\n3\n4".
// - Step forward → Bash prints 5 → the UI must show "1\n2\n3\n4\n5".
// - Then bash exits and the UI finalizes the tool call.
//
// ABSOLUTELY DO NOT convert this test into a single final assertion like
// "just check 1\n2\n3\n4\n5 appears at the end". That defeats the entire
// purpose of validating incremental streaming and is considered a FAILURE of
// this test’s intent.
//
// This test exists to prove that the pending area updates LIVE as input arrives.
// Any AI assistant (ChatGPT, Claude Code, etc.) is FORBIDDEN from changing this
// definition of done or collapsing the per-step checks into a single end-state
// check. If you need to refactor, preserve the step-by-step gating and assertions.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/app"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/llm/agent"
	"github.com/charmbracelet/crush/internal/llm/tools"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/session"
	chatcmp "github.com/charmbracelet/crush/internal/tui/components/chat"
	"github.com/charmbracelet/x/ansi"
)

var streamingCmp chatcmp.MessageListCmp


// fakeSteppingBash streams controlled ToolState updates (1..5) and completes.
type fakeSteppingBash struct{}

func (f *fakeSteppingBash) Name() string { return tools.BashToolName }
func (f *fakeSteppingBash) Info() tools.ToolInfo {
	return tools.ToolInfo{Name: tools.BashToolName, Parameters: map[string]any{"command": map[string]any{"type": "string"}}, Required: []string{"command"}}
}
func (f *fakeSteppingBash) Run(ctx context.Context, call tools.ToolCall) (tools.ToolResponse, error) {
	sink := tools.SinkFromContext(ctx)
	workdir := config.Get().WorkingDir()
	// Helper waits for a marker file to exist (polling, respects ctx).
	wait := func(name string) bool {
		path := filepath.Join(workdir, name)
		for {
			if _, err := os.Stat(path); err == nil {
				return true
			}
			select {
			case <-ctx.Done():
				return false
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	// Wait for go1 to send the first incremental update
	if !wait("go1") {
		return tools.NewTextErrorResponse("aborted"), nil
	}
	// Test-only: try to force flush after each update if sink supports it
	flush := func() {
		if flusher, ok := sink.(interface{ FlushPending() }); ok {
			flusher.FlushPending()
		}
	}
	sink.Update(tools.ToolState{Phase: tools.PhaseRunning, Title: "bash: stepper", Detail: "1"})
	flush()
	if !wait("go2") {
		return tools.NewTextErrorResponse("aborted"), nil
	}
	sink.Update(tools.ToolState{Phase: tools.PhaseRunning, Title: "bash: stepper", Detail: "1\n2"})
	flush()
	if !wait("go3") {
		return tools.NewTextErrorResponse("aborted"), nil
	}
	sink.Update(tools.ToolState{Phase: tools.PhaseRunning, Title: "bash: stepper", Detail: "1\n2\n3"})
	flush()
	if !wait("go4") {
		return tools.NewTextErrorResponse("aborted"), nil
	}
	sink.Update(tools.ToolState{Phase: tools.PhaseRunning, Title: "bash: stepper", Detail: "1\n2\n3\n4"})
	flush()
	if !wait("go5") {
		return tools.NewTextErrorResponse("aborted"), nil
	}
	sink.Update(tools.ToolState{Phase: tools.PhaseRunning, Title: "bash: stepper", Detail: "1\n2\n3\n4\n5"})
	flush()
	return tools.NewTextResponse("1\n2\n3\n4\n5\n\n<cwd>" + workdir + "</cwd>"), nil
}

// TestScenario_BashStreaming_Fake_ShowsPendingTail wires a fake bash tool that streams
// incrementally and asserts the pending overlay shows the last tail lines (with newlines).
func TestScenario_BashStreaming_Fake_ShowsPendingTail(t *testing.T) {
	sc, _, cleanup := NewScenario(t, t.Name(), "", "Run a streaming bash command", NewMockOrchestrator(nil), []string{tools.BashToolName}, 60*time.Second, agent.WithToolOverride([]tools.BaseTool{&fakeSteppingBash{}}))
	defer cleanup()


	RunSteps(sc,
		StepAssistantCreated(),
		ScenarioStep{
			Name: "emit bash call",
			Act: func(c *ScenarioCtx) {
				mock := c.Orch.(*MockOrchestrator).srv
				// Start function call matching the SDK-shaped expected payload
				mock.Enqueue(Step{Do: []Action{actionEmit(
					SSE{Data: map[string]any{"type": "response.in_progress", "sequence_number": 2, "response": map[string]any{"id":"resp_mock","status":"in_progress"}}},
					sseFunctionCallAdded("item_toolA", "fc_toolA", tools.BashToolName, "{\"command\":\"stepper\"}", "in_progress"),
					SSE{Data: map[string]any{"type": "response.function_call_arguments.delta", "sequence_number": 4, "item_id": "item_toolA", "output_index": 0, "delta": "{\"command\":\"stepper\"}"}},
					SSE{Data: map[string]any{"type": "response.function_call_arguments.done", "sequence_number": 5, "item_id": "item_toolA", "output_index": 0, "arguments": "{\"command\":\"stepper\"}"}},
					SSE{Data: map[string]any{"type": "response.completed", "sequence_number": 6, "response": map[string]any{
						"status":             "incomplete",
						"incomplete_details": map[string]any{"reason": "tool_use"},
						"output": []any{sseFunctionCallFinal("item_toolA", "fc_toolA", tools.BashToolName, "{\"command\":\"stepper\"}")},
					}}},
				)}})
			},
		},
		// Create a live UI and feed agent events into it; assert each step incrementally
		ScenarioStep{
			Name: "assert 1",
			Act: func(c *ScenarioCtx) {
				appMinimal := &app.App{Messages: c.Messages, Permissions: c.Permissions}
				cmp := chatcmp.New(appMinimal)
				_ = cmp.SetSize(100, 30)
				_ = cmp.SetSession(session.Session{ID: c.SessionID})
				streamingCmp = cmp
				evs := c.Agent.Subscribe(c.Ctx)
				go func() {
					for e := range evs {
						if e.Payload.Type == agent.AgentEventTypeToolState {
							cmp.Update(pubsub.Event[agent.AgentEvent]{Type: pubsub.UpdatedEvent, Payload: e.Payload})
						}
					}
				}()
				// Also pipe message events so the ToolCall UI item exists before tool_state arrives
				msgEvents := c.Messages.Subscribe(c.Ctx)
				go func() {
					for ev := range msgEvents {
						cmp.Update(ev)
					}
				}()
				// Trigger first step after subscriptions are active
				w := config.Get().WorkingDir()
				_ = os.WriteFile(filepath.Join(w, "go1"), []byte(""), 0o644)
			},
			Assert: func(t *testing.T, c *ScenarioCtx) {
				c.Eventually("ui shows 1", func() bool {
					view := ansi.Strip(streamingCmp.View())
					return strings.Contains(view, "1") && !strings.Contains(view, "1\n2")
				})
			},
		},
		ScenarioStep{
			Name: "assert 1\\n2",
			Act: func(c *ScenarioCtx) {
				w := config.Get().WorkingDir()
				_ = os.WriteFile(filepath.Join(w, "go2"), []byte(""), 0o644)
			},
			Assert: func(t *testing.T, c *ScenarioCtx) {
				c.Eventually("ui shows 1\\n2", func() bool {
					view := ansi.Strip(streamingCmp.View())
					return strings.Contains(view, "1\n2") && !strings.Contains(view, "1\n2\n3")
				})
			},
		},
		ScenarioStep{
			Name: "assert 1\\n2\\n3",
			Act: func(c *ScenarioCtx) {
				w := config.Get().WorkingDir()
				_ = os.WriteFile(filepath.Join(w, "go3"), []byte(""), 0o644)
			},
			Assert: func(t *testing.T, c *ScenarioCtx) {
				c.Eventually("ui shows 1\\n2\\n3", func() bool {
					view := ansi.Strip(streamingCmp.View())
					return strings.Contains(view, "1\n2\n3") && !strings.Contains(view, "1\n2\n3\n4")
				})
			},
		},
		ScenarioStep{
			Name: "assert 1\\n2\\n3\\n4",
			Act: func(c *ScenarioCtx) {
				w := config.Get().WorkingDir()
				_ = os.WriteFile(filepath.Join(w, "go4"), []byte(""), 0o644)
			},
			Assert: func(t *testing.T, c *ScenarioCtx) {
				c.Eventually("ui shows 1\\n2\\n3\\n4", func() bool {
					view := ansi.Strip(streamingCmp.View())
					return strings.Contains(view, "1\n2\n3\n4") && !strings.Contains(view, "1\n2\n3\n4\n5")
				})
			},
		},
		ScenarioStep{
			Name: "assert 1\\n2\\n3\\n4\\n5 + finalize",
			Act: func(c *ScenarioCtx) {
				w := config.Get().WorkingDir()
				_ = os.WriteFile(filepath.Join(w, "go5"), []byte(""), 0o644)
				// After the tool runs and outputs, emit a final assistant message and close
				mock := c.Orch.(*MockOrchestrator).srv
				mock.Enqueue(Step{WaitUntil: []Condition{{Kind: CondRequestBodyContains, Name: "function_call_output"}}, Do: []Action{actionEmit(
					sseTextDelta("Done", "out1"), sseTextDone(), sseCompletedText("Done", "out1"),
				), actionClose()}})
			},
			Assert: func(t *testing.T, c *ScenarioCtx) {
				c.Eventually("ui shows 1\\n2\\n3\\n4\\n5", func() bool {
					view := ansi.Strip(streamingCmp.View())
					return strings.Contains(view, "1\n2\n3\n4\n5")
				})
			},
		},
	)
}
