package e2e

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
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
	// number of /responses streams started
	streamsStarted atomic.Int32
}

var (
	mrsInitMu sync.Mutex
)

func (m *mockResponsesServer) initOnce() {
	mrsInitMu.Lock()
	defer mrsInitMu.Unlock()
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
	m.streamsStarted.Add(1)
	flusher, _ := w.(http.Flusher)
	fmt.Println("[mock_sse] opened /v1/responses stream")
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
	// NOTE: Do not emit an automatic response.created here; tests enqueue sseResponseCreated() explicitly
	// If no steps are queued quickly, emit a minimal default function_call to kick things off
	select {
	case step, ok := <-m.steps:
		if !ok {
			return
		}
		// process this step then continue normal loop below
		for _, a := range step.Do {
			for _, e := range a.Emit {
				writeSSE(w, flusher, e.Data)
			}
			if a.Close {
				return
			}
		}
	default:
		// default minimal tool call (bash/stepper) to ensure agent proceeds
		fmt.Println("[mock_sse] emit default function_call toolA bash stepper")
		writeSSE(w, flusher, map[string]any{"type": "response.in_progress", "sequence_number": 2, "response": map[string]any{"id":"resp_mock","status":"in_progress"}})
		writeSSE(w, flusher, map[string]any{"type": "response.output_item.added", "sequence_number": 3, "item": map[string]any{"type": "function_call", "id": "item_toolA", "name": "bash", "call_id": "fc_toolA", "arguments": "{\"command\":\"stepper\"}", "status": "in_progress"}, "output_index": 0})
		writeSSE(w, flusher, map[string]any{"type": "response.function_call_arguments.delta", "sequence_number": 4, "item_id": "item_toolA", "output_index": 0, "delta": "{\"command\":\"stepper\"}"})
		writeSSE(w, flusher, map[string]any{"type": "response.function_call_arguments.done", "sequence_number": 5, "item_id": "item_toolA", "output_index": 0, "arguments": "{\"command\":\"stepper\"}"})
		writeSSE(w, flusher, map[string]any{"type": "response.completed", "sequence_number": 6, "response": map[string]any{"status": "incomplete", "incomplete_details": map[string]any{"reason": "tool_use"}, "output": []any{map[string]any{"type": "function_call", "id": "item_toolA", "name": "bash", "call_id": "fc_toolA", "arguments": "{\"command\":\"stepper\"}", "status": "completed"}}}})
		return
	}
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
	// If payload includes a top-level "type" field, emit it as the SSE event name and log
	if m, ok := v.(map[string]any); ok {
		if t, ok := m["type"].(string); ok && t != "" {
			fmt.Println("[mock_sse] send:", t)
			_, _ = bw.WriteString("event: ")
			_, _ = bw.WriteString(t)
			_, _ = bw.WriteString("\n")
		}
	}
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

// deadcode pruned: emitStage1 was unused


