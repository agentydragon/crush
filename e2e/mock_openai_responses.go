package e2e

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
)

type mockResponsesServer struct {
	// observed flag when function_call_output was sent by the client
	sawFunctionCallOutput atomic.Bool
}

func (m *mockResponsesServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/responses") {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	// Streaming endpoint
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, _ := w.(http.Flusher)

	bodyBytes, _ := io.ReadAll(r.Body)
	_ = r.Body.Close()
	bodyStr := string(bodyBytes)
	if strings.Contains(bodyStr, "function_call_output") {
		m.sawFunctionCallOutput.Store(true)
		m.emitStage2(w, flusher)
		return
	}
	if strings.Contains(bodyStr, "parallel") {
		m.emitStage1Parallel(w, flusher)
		return
	}
	m.emitStage1(w, flusher)
}

func writeSSE(w http.ResponseWriter, flusher http.Flusher, v any) {
	enc, _ := json.Marshal(v)
	bw := bufio.NewWriter(w)
	_, _ = bw.WriteString("data: ")
	_, _ = bw.Write(enc)
	_, _ = bw.WriteString("\n\n")
	_ = bw.Flush()
	flusher.Flush()
}

func (m *mockResponsesServer) emitStage1(w http.ResponseWriter, flusher http.Flusher) {
	// 0) optional created
	writeSSE(w, flusher, map[string]any{
		"type":     "response.created",
		"response": map[string]any{"id": "resp_123"},
	})
	// 0.5) optional reasoning summary text delta
	writeSSE(w, flusher, map[string]any{
		"type":  "response.reasoning_summary_text.delta",
		"delta": "Considering a bash command…",
	})
	// 1) function tool call added
	writeSSE(w, flusher, map[string]any{
		"type": "response.output_item.added",
		"item": map[string]any{
			"type": "function_tool_call",
			"id":   "toolA",
			"name": "bash",
		},
	})
	// 2) arguments delta
	writeSSE(w, flusher, map[string]any{
		"type":    "response.function_call_arguments.delta",
		"item_id": "toolA",
		"delta":   "{\"command\":\"echo hi\"}",
	})
	// 3) arguments done
	writeSSE(w, flusher, map[string]any{
		"type":    "response.function_call_arguments.done",
		"item_id": "toolA",
	})
	// 4) completed with tool call present
	writeSSE(w, flusher, map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"status":             "incomplete",
			"incomplete_details": map[string]any{"reason": "tool_use"},
			"output": []any{
				map[string]any{
					"type":      "function_tool_call",
					"id":        "toolA",
					"name":      "bash",
					"arguments": "{\"command\":\"echo hi\"}",
				},
			},
			"usage": map[string]any{"input_tokens": 10, "output_tokens": 1},
		},
	})
}

func (m *mockResponsesServer) emitStage1Parallel(w http.ResponseWriter, flusher http.Flusher) {
	// two parallel function calls A and B
	writeSSE(w, flusher, map[string]any{"type": "response.output_item.added", "item": map[string]any{"type": "function_tool_call", "id": "toolA", "name": "bash"}})
	writeSSE(w, flusher, map[string]any{"type": "response.output_item.added", "item": map[string]any{"type": "function_tool_call", "id": "toolB", "name": "bash"}})
	writeSSE(w, flusher, map[string]any{"type": "response.function_call_arguments.delta", "item_id": "toolA", "delta": "{\"command\":\"echo A\"}"})
	writeSSE(w, flusher, map[string]any{"type": "response.function_call_arguments.delta", "item_id": "toolB", "delta": "{\"command\":\"echo B\"}"})
	writeSSE(w, flusher, map[string]any{"type": "response.function_call_arguments.done", "item_id": "toolA"})
	writeSSE(w, flusher, map[string]any{"type": "response.function_call_arguments.done", "item_id": "toolB"})
	writeSSE(w, flusher, map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"status":             "incomplete",
			"incomplete_details": map[string]any{"reason": "tool_use"},
			"output": []any{
				map[string]any{"type": "function_tool_call", "id": "toolA", "name": "bash", "arguments": "{\"command\":\"echo A\"}"},
				map[string]any{"type": "function_tool_call", "id": "toolB", "name": "bash", "arguments": "{\"command\":\"echo B\"}"},
			},
		},
	})
}

func (m *mockResponsesServer) emitStage2(w http.ResponseWriter, flusher http.Flusher) {
	// 1) text delta
	writeSSE(w, flusher, map[string]any{
		"type":    "response.output_text.delta",
		"delta":   "Done",
		"item_id": "out1",
	})
	// 2) text done
	writeSSE(w, flusher, map[string]any{
		"type": "response.output_text.done",
	})
	// 3) completed (include a reasoning item to exercise UI paths)
	writeSSE(w, flusher, map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"status": "completed",
			"output": []any{
				map[string]any{
					"type": "message",
					"role": "assistant",
					"content": []any{
						map[string]any{"type": "output_text", "text": "Done"},
					},
				},
				map[string]any{
					"type":               "reasoning",
					"id":                 "rsn_123",
					"encrypted_content":  "enc:abc123",
					"summary":            []any{map[string]any{"type": "summary_text", "text": "Ran bash as requested."}},
				},
			},
			"usage": map[string]any{"input_tokens": 12, "output_tokens": 2},
		},
	})
}
