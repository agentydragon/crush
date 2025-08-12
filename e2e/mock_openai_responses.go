package e2e

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

type ConditionKind int

const (
	CondNone ConditionKind = iota
	CondRequestBodyContains
	CondSignal
	CondSleep
)

type Condition struct {
	Kind     ConditionKind
	Name     string
	Duration time.Duration
}

type SSE struct{ Data any }

type Action struct {
	Emit  []SSE
	Close bool
}

type Step struct {
	WaitUntil []Condition
	Do        []Action
}

type mockResponsesServer struct {
	// observed flag when function_call_output was sent by the client
	sawFunctionCallOutput atomic.Bool
	// optional initial delay to allow tests to observe assistant pre-event state
	initialDelay time.Duration
	// steps to gate SSE emissions
	steps chan Step
	// observed request bodies
	reqObs chan string
	// manual signals
	signals map[string]chan struct{}
}

func (m *mockResponsesServer) initOnce() {
	if m.steps == nil {
		m.steps = make(chan Step, 16)
	}
	if m.reqObs == nil {
		m.reqObs = make(chan string, 8)
	}
	if m.signals == nil {
		m.signals = map[string]chan struct{}{}
	}
}

func (m *mockResponsesServer) Enqueue(step Step) { m.initOnce(); m.steps <- step }
func (m *mockResponsesServer) Signal(name string) {
	m.initOnce()
	ch, ok := m.signals[name]
	if !ok {
		ch = make(chan struct{}, 1)
		m.signals[name] = ch
	}
	select {
	case ch <- struct{}{}:
	default:
	}
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
	if m.initialDelay > 0 {
		time.Sleep(m.initialDelay)
	}

	bodyBytes, _ := io.ReadAll(r.Body)
	_ = r.Body.Close()
	bodyStr := string(bodyBytes)
	// Record all request bodies so conditions can match them
	m.initOnce()
	select {
	case m.reqObs <- bodyStr:
	default:
	}
	if hasFunctionCallOutputJSON(bodyStr) {
		m.sawFunctionCallOutput.Store(true)
		// Do not return early; continue into step-driven SSE so tests can emit the final response
	}
	// Note: disable auto-parallel shortcut; rely on step-driven emissions to keep tests deterministic
	// step-driven streaming
	for {
		step, ok := <-m.steps
		if !ok {
			return
		}
		// Wait for all conditions
		for _, c := range step.WaitUntil {
			switch c.Kind {
			case CondSignal:
				ch, ok := m.signals[c.Name]
				if !ok {
					ch = make(chan struct{}, 1)
					m.signals[c.Name] = ch
				}
				<-ch
			case CondSleep:
				time.Sleep(c.Duration)
			case CondNone:
				// no-op
			case CondRequestBodyContains:
				for {
					body := <-m.reqObs
					if c.Name == "function_call_output" {
						if hasFunctionCallOutputJSON(body) {
							m.sawFunctionCallOutput.Store(true)
							break
						}
						continue
					}
					if c.Name == "" || strings.Contains(body, c.Name) {
						break
					}
				}
			}
		}
		// Execute actions
		for _, a := range step.Do {
			for _, e := range a.Emit {
				writeSSE(w, flusher, e.Data)
			}
			if a.Close {
				return
			}
		}
	}
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

func hasFunctionCallOutputJSON(body string) bool {
	var v any
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		return false
	}
	return hasFunctionCallOutputValue(v)
}

func hasFunctionCallOutputValue(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		if t, ok := x["type"].(string); ok && t == "function_call_output" {
			return true
		}
		if _, hasCallID := x["call_id"]; hasCallID {
			if _, hasOutput := x["output"]; hasOutput {
				return true
			}
		}
		for _, vv := range x {
			if hasFunctionCallOutputValue(vv) {
				return true
			}
		}
		return false
	case []any:
		for _, vv := range x {
			if hasFunctionCallOutputValue(vv) {
				return true
			}
		}
		return false
	default:
		return false
	}
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
			"type": "function_call",
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
					"type":    "function_call",
					"id":      "toolA",
					"name":    "bash",
					"call_id": "toolA", "arguments": "{\"command\":\"echo hi\"}",
				},
			},
			"usage": map[string]any{"input_tokens": 10, "output_tokens": 1},
		},
	})
}

func (m *mockResponsesServer) emitStage1Parallel(w http.ResponseWriter, flusher http.Flusher) {
	// two parallel function calls A and B
	writeSSE(w, flusher, map[string]any{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{"type": "function_call", "id": "toolA", "name": "bash", "status": "in_progress"}})
	writeSSE(w, flusher, map[string]any{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{"type": "function_call", "id": "toolB", "name": "bash"}})
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
				map[string]any{"type": "function_call", "id": "toolA", "name": "bash", "arguments": "{\"command\":\"echo A\"}"},
				map[string]any{"type": "function_call", "id": "toolB", "name": "bash", "arguments": "{\"command\":\"echo B\"}"},
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
					"type":              "reasoning",
					"id":                "rsn_123",
					"encrypted_content": "enc:abc123",
					"summary":           []any{map[string]any{"type": "summary_text", "text": "Ran bash as requested."}},
				},
			},
			"usage": map[string]any{"input_tokens": 12, "output_tokens": 2},
		},
	})
}
