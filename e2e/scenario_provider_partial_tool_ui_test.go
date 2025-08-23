package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/tui/components/chat/messages"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

/* (spec block omitted for brevity; see file top) */

func TestProviderPartialTool_FailsStream_UIUnblocked(t *testing.T) {
	orch := &MockOrchestrator{}
	sc, events, cleanup := NewScenario(t, "TestProviderPartialTool_FailsStream_UIUnblocked", "", "please multiedit", orch, []string{"multiedit"}, 5*time.Second)
	t.Cleanup(cleanup)

	// Step 1: assistant created
	RunSteps(sc, StepAssistantCreated())

	// Step 2: emit partial tool call then error (no stop/complete)
	// Minimal OpenAI Responses-style sequence for one function tool call "C1":
	// - output_item.added: function_call item with call_id=C1
	// - function_call_arguments.delta (partial JSON) for that item_id
	// - then the stream closes (error path)
	toolItemAdded := map[string]any{
		"type": "response.output_item.added",
		"item": map[string]any{
			"id":   "item_1",
			"type": "function_call",
			"name": "multiedit",
			"call_id": "C1",
			"arguments": "",
		},
	}
	argsDelta := map[string]any{
		"type":    "response.function_call_arguments.delta",
		"item_id": "item_1",
		"delta":   "{\"file_path\":\"/abs\"",
	}
	sc.Orch.(*MockOrchestrator).srv.Enqueue(Step{Do: []Action{{Emit: []SSE{{Data: toolItemAdded}, {Data: argsDelta}}, Close: true}}})

	// Drain events until close so the agent processes the error path.
	for range events { /* wait */ }

	// Assert assistant has ToolCall C1 Finished=false
	msgs := mustList(sc)
	require.NotEmpty(t, msgs)
	var assistant message.Message
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == message.Assistant {
			assistant = msgs[i]
			break
		}
	}
	require.NotEmpty(t, assistant.ID, "assistant message not found")
	var call *message.ToolCall
	for _, c := range assistant.ToolCalls() {
		if c.ID == "C1" {
			cc := c
			call = &cc
			break
		}
	}
	require.NotNil(t, call, "tool call C1 not found on assistant")
	require.False(t, call.Finished, "tool call C1 should be unfinished")

	// Assert tool-role message exists with recovered ToolResult for C1
	var toolMsg *message.Message
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == message.Tool {
			tm := msgs[i]
			toolMsg = &tm
			break
		}
	}
	require.NotNil(t, toolMsg, "tool message not found")
	var tr *message.ToolResult
	for _, r := range toolMsg.ToolResults() {
		if r.ToolCallID == "C1" { rr := r; tr = &rr; break }
	}
	require.NotNil(t, tr, "ToolResult for C1 not found")
	require.True(t, tr.IsError, "ToolResult must be error")
	require.True(t, tr.Recovered, "ToolResult must be marked recovered")
	low := strings.ToLower(tr.Content)
	require.Contains(t, low, "incomplete", "ToolResult content should hint incomplete input")

	// UI render for that ToolCall should not be pending/spinning
	perm := permission.NewPermissionService(t.TempDir(), true, []string{})
	cmp := messages.NewToolCallCmp(assistant.ID, *call, perm)
	_ = cmp.SetSize(100, 0)
	_ = cmp.Init()
	cmp.SetToolResult(*tr)
	view := ansi.Strip(cmp.View())
	require.NotEmpty(t, view)
	require.False(t, cmp.Spinning(), "spinner should be stopped after recovered ToolResult")
	lv := strings.ToLower(view)
	require.NotContains(t, lv, "waiting")
	require.NotContains(t, lv, "pending")
	require.Contains(t, lv, "recovered")
}
