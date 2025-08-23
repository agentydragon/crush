package agent

import (
	"context"
	"testing"

	"github.com/charmbracelet/catwalk/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/llm/provider"
	"github.com/charmbracelet/crush/internal/llm/tools"
	"github.com/charmbracelet/crush/internal/message"
)

// mockProviderPartialTool simulates: ToolUseStart + some deltas, then EventError before ToolUseStop.
type mockProviderPartialTool struct{}

func (m *mockProviderPartialTool) Model() catwalk.Model { return catwalk.Model{ID: "mock"} }
func (m *mockProviderPartialTool) StreamResponse(ctx context.Context, msgs []message.Message, tools []tools.BaseTool) <-chan provider.ProviderEvent {
	ch := make(chan provider.ProviderEvent, 16)
	go func() {
		defer close(ch)
		// Start a tool call (C1), but never stop it
		ch <- provider.ProviderEvent{Type: provider.EventToolUseStart, ToolCall: &message.ToolCall{ID: "C1", Name: "multiedit", Type: "function"}}
		ch <- provider.ProviderEvent{Type: provider.EventToolUseDelta, ToolCall: &message.ToolCall{ID: "C1", Input: "{\"file_path\":"}}
		// Error out before ToolUseStop
		ch <- provider.ProviderEvent{Type: provider.EventError, Error: context.DeadlineExceeded}
		return
	}()
	return ch
}

// This is pseudocode-ish structure to illustrate assertions rather than a fully wired test harness.
func TestAgent_PartialToolCall_YieldsRecoveredToolResult(t *testing.T) {
	// Arrange a minimal agent with our mock provider and in-memory services (omitted: setup code)
	// a := newTestAgentWithProvider(&mockProviderPartialTool{})
	// sess := newTestSession()

	// Act: run one turn that triggers the mock provider behavior
	// evts, err := a.Run(context.Background(), sess.ID, "please do a multiedit")
	// if err != nil { t.Fatalf("Run error: %v", err) }
	// <-evts // drain final event

	// Assert (pseudostructure):
	// - assistant message has ToolCall ID=C1 with Finished=false
	// - a Tool message exists with ToolResult { ToolCallID=C1, IsError=true, Recovered=true }
	// - no toolstate.update events logged for C1 (we did not execute)
	// - UI would not be pending: presence of ToolResult clears spinner

	// This test is a scaffold. The real test should use the existing agent test helpers
	// to create in-memory services and retrieve messages for assertions.
}
