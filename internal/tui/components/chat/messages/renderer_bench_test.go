package messages

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/message"
)

func makeToolCall(content string) *toolCallCmp {
	call := message.ToolCall{ID: "tc1", Name: "fetch", Input: `{"url":"http://example.com","format":"html"}`}
	meta, _ := json.Marshal(map[string]any{"file_path": "fetch.html"})
	res := message.ToolResult{ToolCallID: call.ID, Content: content, Metadata: string(meta)}
	cmp := &toolCallCmp{width: 120}
	cmp.SetToolCall(call)
	cmp.SetToolResult(res)
	return cmp
}

func BenchmarkToolRender_Code_LongLines(b *testing.B) {
	line := strings.Repeat("<span>", 2000) + strings.Repeat("a", 30000)
	content := strings.Repeat(line+"\n", 50)
	cmp := makeToolCall(content)
	_ = cmp.View() // warm up
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = cmp.View()
	}
}

func BenchmarkToolRender_Plain_LongLines(b *testing.B) {
	call := message.ToolCall{ID: "tc2", Name: "download", Input: `{"url":"http://example.com","file_path":"/tmp/x"}`}
	res := message.ToolResult{ToolCallID: call.ID, Content: strings.Repeat(strings.Repeat("x", 40000)+"\n", 50)}
	cmp := &toolCallCmp{width: 120}
	cmp.SetToolCall(call)
	cmp.SetToolResult(res)
	_ = cmp.View()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = cmp.View()
	}
}
