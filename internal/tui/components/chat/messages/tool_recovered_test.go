package messages

import (
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/llm/tools"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/x/ansi"
)

// Ensures that when a recovered ToolResult is present, the UI is not shown as pending/spinning.
func TestRecoveredToolResultClearsPending(t *testing.T) {
	perm := permission.NewPermissionService(t.TempDir(), true, []string{})
	call := message.ToolCall{ID: "call_recovered_1", Name: "multiedit", Input: `{"file_path":"/abs","edits":[]}`, Type: "function", Finished: false}
	cmp := NewToolCallCmp("msg_parent_1", call, perm)
	_ = cmp.SetSize(100, 0)
	// Initialize to start animation state transitions
	_ = cmp.Init()

	// Simulate live state arriving before result (pending view)
	cmp.(*toolCallCmp).SetLiveToolState(tools.ToolState{Title: "Applying edits…", Detail: "edits=1/2"})
	pending := ansi.Strip(cmp.View())
	if pending == "" {
		t.Fatalf("expected non-empty pending view")
	}
	if !strings.Contains(strings.ToLower(pending), "apply") {
		t.Fatalf("expected live state in pending view; got: %q", pending)
	}
	if !cmp.Spinning() {
		t.Fatalf("expected spinner active before result")
	}

	// Now set a recovered ToolResult as produced by the agent in crash/error recovery
	res := message.ToolResult{
		ToolCallID: call.ID,
		Name:       call.Name,
		Content:    "Recovered from crash: tool input incomplete; please re-issue this function call.",
		IsError:    true,
		Recovered:  true,
	}
	cmp.SetToolResult(res)

	view := ansi.Strip(cmp.View())
	if view == "" {
		t.Fatalf("expected non-empty final view")
	}
	// Should no longer be spinning/pending
	if cmp.Spinning() {
		t.Fatalf("spinner should be stopped after recovered ToolResult; view=%q", view)
	}
	if strings.Contains(strings.ToLower(view), "waiting for tool") || strings.Contains(strings.ToLower(view), "pending") {
		t.Fatalf("view should not show pending/waiting after ToolResult; got: %q", view)
	}
	// Should surface the recovered message content to the user
	if !strings.Contains(strings.ToLower(view), "recovered") {
		t.Fatalf("expected recovered hint in view; got: %q", view)
	}
}
