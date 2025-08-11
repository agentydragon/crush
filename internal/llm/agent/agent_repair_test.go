package agent

import (
	"testing"
	"github.com/charmbracelet/crush/internal/message"
)

func TestRepairOrphanedToolCalls_InsertsStubForMissingToolResults(t *testing.T) {
	msgs := []message.Message{
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hi"}}},
		{Role: message.Assistant, Parts: []message.ContentPart{message.ToolCall{ID: "call_1", Name: "bash", Input: "{}", Type: "function", Finished: true}}},
	}
	repaired := repairOrphanedToolCalls(msgs)
	if len(repaired) != 3 {
		t.Fatalf("expected 3 messages after repair, got %d", len(repaired))
	}
	toolMsg := repaired[2]
	if toolMsg.Role != message.Tool {
		t.Fatalf("expected a Tool message at index 2, got %v", toolMsg.Role)
	}
	trs := toolMsg.ToolResults()
	if len(trs) != 1 {
		t.Fatalf("expected 1 ToolResult, got %d", len(trs))
	}
	if trs[0].ToolCallID != "call_1" {
		t.Fatalf("expected ToolResult.ToolCallID=call_1, got %s", trs[0].ToolCallID)
	}
	if !trs[0].IsError {
		t.Fatalf("expected stub ToolResult IsError=true")
	}
}
