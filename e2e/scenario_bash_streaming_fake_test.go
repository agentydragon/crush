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
	"fmt"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/app"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/llm/agent"
	"github.com/charmbracelet/crush/internal/llm/tools"
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
	slog.Info("fake_bash.run.start", "call_id", call.ID, "input", call.Input)

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
	// Allow production debounce/coalescing to operate; avoid test-only FlushPending
	debounceWait := 120 * time.Millisecond // 2x the 50ms debounce + buffer
	slog.Info("fake_bash.update", "detail", "1")
	sink.Update(tools.ToolState{Phase: tools.PhaseRunning, Title: "bash: stepper", Detail: "1"})
	time.Sleep(debounceWait)
	if !wait("go2") {
		return tools.NewTextErrorResponse("aborted"), nil
	}
	slog.Info("fake_bash.update", "detail", "1\n2")
	sink.Update(tools.ToolState{Phase: tools.PhaseRunning, Title: "bash: stepper", Detail: "1\n2"})
	time.Sleep(debounceWait)
	if !wait("go3") {
		return tools.NewTextErrorResponse("aborted"), nil
	}
	slog.Info("fake_bash.update", "detail", "1\n2\n3")
	sink.Update(tools.ToolState{Phase: tools.PhaseRunning, Title: "bash: stepper", Detail: "1\n2\n3"})
	time.Sleep(debounceWait)
	if !wait("go4") {
		return tools.NewTextErrorResponse("aborted"), nil
	}
	slog.Info("fake_bash.update", "detail", "1\n2\n3\n4")
	sink.Update(tools.ToolState{Phase: tools.PhaseRunning, Title: "bash: stepper", Detail: "1\n2\n3\n4"})
	time.Sleep(debounceWait)
	if !wait("go5") {
		return tools.NewTextErrorResponse("aborted"), nil
	}
	slog.Info("fake_bash.update", "detail", "1\n2\n3\n4\n5")
	sink.Update(tools.ToolState{Phase: tools.PhaseRunning, Title: "bash: stepper", Detail: "1\n2\n3\n4\n5"})
	time.Sleep(debounceWait)
	return tools.NewTextResponse("1\n2\n3\n4\n5\n\n<cwd>" + workdir + "</cwd>"), nil
}

// TestScenario_BashStreaming_Fake_ShowsPendingTail wires a fake bash tool that streams
// incrementally and asserts the pending overlay shows the last tail lines (with newlines).
func TestScenario_BashStreaming_Fake_ShowsPendingTail(t *testing.T) {
	// Build the mock server first and pre-enqueue the function_call so it's guaranteed to be first.
	srv := &mockResponsesServer{}
	// Start mock server and use its URL for the provider
	ts := httptest.NewServer(srv)
	defer ts.Close()
	// Pre-enqueue created + function_call deterministically so the mock has it before the provider opens.
	srv.Enqueue(Step{Do: []Action{actionEmit(
		sseResponseCreated(),
		SSE{Data: map[string]any{"type": "response.in_progress", "sequence_number": 2, "response": map[string]any{"id": "resp_mock", "status": "in_progress"}}},
		sseFunctionCallAdded("item_toolA", "fc_toolA", tools.BashToolName, "{\"command\":\"stepper\"}", "in_progress"),
		SSE{Data: map[string]any{"type": "response.function_call_arguments.delta", "sequence_number": 4, "item_id": "item_toolA", "output_index": 0, "delta": "{\"command\":\"stepper\"}"}},
		SSE{Data: map[string]any{"type": "response.function_call_arguments.done", "sequence_number": 5, "item_id": "item_toolA", "output_index": 0, "arguments": "{\"command\":\"stepper\"}"}},
		SSE{Data: map[string]any{"type": "response.completed", "sequence_number": 6, "response": map[string]any{
			"status":             "incomplete",
			"incomplete_details": map[string]any{"reason": "tool_use"},
			"output":            []any{sseFunctionCallFinal("item_toolA", "fc_toolA", tools.BashToolName, "{\"command\":\"stepper\"}")},
		}}},
	)}})

	sc, _, cleanup := NewScenario(t, t.Name(), ts.URL+"/v1", "Run a streaming bash command", NewMockOrchestrator(srv), []string{tools.BashToolName}, 60*time.Second, agent.WithToolOverride([]tools.BaseTool{&fakeSteppingBash{}}))
	defer cleanup()

	RunSteps(sc,
		// Only assert assistant created; do not emit again to avoid races.
		ScenarioStep{
			Name: "assistant created",
			Assert: func(t *testing.T, c *ScenarioCtx) {
				c.Eventually("assistant exists", func() bool {
					ms := mustList(c)
					return len(ms) >= 2 && ms[len(ms)-1].Role == "assistant"
				})
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
				c.LiveCmp = cmp
				// Centralized event wiring with command execution
				PipeEventsToComponent(c, cmp)
				// Wait until the tool item is visible to avoid UI-buffer races
				c.Eventually("tool item visible", func() bool {
					view := ansi.Strip(streamingCmp.View())
					return strings.Contains(view, "id=fc_toolA")
				})
				// Trigger first step after subscriptions are active and tool item exists
				w := config.Get().WorkingDir()
				p := filepath.Join(w, "go1")
				fmt.Println("[test] WRITE MARKER:", p)
				_ = os.WriteFile(p, []byte(""), 0o644)
			},
			Assert: func(t *testing.T, c *ScenarioCtx) {
				anchor := "bash: stepper"
				if !WaitForToolDetailContains(streamingCmp, anchor, "1", c.PerStepBudget) {
					t.Fatal("timeout waiting for '1' in detail")
				}
				if ToolDetailContainsNow(streamingCmp, anchor, "2") {
					t.Fatal("unexpected next line present prematurely in detail: '2'")
				}
			},
		},
		ScenarioStep{
			Name: "assert 1\\n2",
			Act: func(c *ScenarioCtx) {
				w := config.Get().WorkingDir()
				_ = os.WriteFile(filepath.Join(w, "go2"), []byte(""), 0o644)
			},
			Assert: func(t *testing.T, c *ScenarioCtx) {
				// Progressive: now '2' should appear, but not '3' yet
				anchor := "bash: stepper"
				if !WaitForToolDetailContains(streamingCmp, anchor, "2", c.PerStepBudget) {
					t.Fatal("timeout waiting for '2' in detail")
				}
				if ToolDetailContainsNow(streamingCmp, anchor, "3") {
					t.Fatal("unexpected next line present prematurely in detail: '3'")
				}
			},
		},
		ScenarioStep{
			Name: "assert 1\\n2\\n3",
			Act: func(c *ScenarioCtx) {
				w := config.Get().WorkingDir()
				_ = os.WriteFile(filepath.Join(w, "go3"), []byte(""), 0o644)
			},
			Assert: func(t *testing.T, c *ScenarioCtx) {
				anchor := "bash: stepper"
				if !WaitForToolDetailContains(streamingCmp, anchor, "3", c.PerStepBudget) {
					t.Fatal("timeout waiting for '3' in detail")
				}
				if ToolDetailContainsNow(streamingCmp, anchor, "4") {
					t.Fatal("unexpected next line present prematurely in detail: '4'")
				}
			},
		},
		ScenarioStep{
			Name: "assert 1\\n2\\n3\\n4",
			Act: func(c *ScenarioCtx) {
				w := config.Get().WorkingDir()
				_ = os.WriteFile(filepath.Join(w, "go4"), []byte(""), 0o644)
			},
			Assert: func(t *testing.T, c *ScenarioCtx) {
				anchor := "bash: stepper"
				if !WaitForToolDetailContains(streamingCmp, anchor, "4", c.PerStepBudget) {
					t.Fatal("timeout waiting for '4' in detail")
				}
				if ToolDetailContainsNow(streamingCmp, anchor, "5") {
					t.Fatal("unexpected next line present prematurely in detail: '5'")
				}
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
				anchor := "bash: stepper"
				if !WaitForToolDetailContains(streamingCmp, anchor, "5", c.PerStepBudget) {
					t.Fatal("timeout waiting for '5' in detail")
				}
				// Ensure the agent persisted the tool result before teardown to avoid DB closure races
				c.Eventually("tool results persisted", func() bool {
					ms := mustList(c)
					if len(ms) == 0 { return false }
					last := ms[len(ms)-1]
					return string(last.Role) == "tool"
				})
			},
		},
	)
}
