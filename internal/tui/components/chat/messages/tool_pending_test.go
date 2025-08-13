package messages

import (
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/x/ansi"
)

func TestPendingViewShowsLiveStateUntilResultArrives(t *testing.T) {
	perm := permission.NewPermissionService(t.TempDir(), true, []string{})
	call := message.ToolCall{ID: "call_1", Name: "mcp_openai_research_ask", Input: `{"user_message":"pong"}`, Type: "function", Finished: true}
	m := NewToolCallCmp("msg_1", call, perm)
	_ = m.SetSize(100, 0)
	m.(*toolCallCmp).SetLiveState("Waiting for MCP server response…", "server=openai_research tool=ask")

	view := ansi.Strip(m.View())
	if view == "" {
		t.Fatalf("expected non-empty view")
	}
	if strings.Contains(view, "Waiting for tool response...") {
		t.Fatalf("should not show generic 'Waiting for tool response...' while ToolResult not yet arrived; got: %q", view)
	}
	// Expect live title to be present while executing; spinner label may not be rendered here.
	if !strings.Contains(view, "Waiting for MCP server response") {
		t.Fatalf("should show live MCP waiting status; got: %q", view)
	}
}
