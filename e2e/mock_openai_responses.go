package e2e

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log/slog"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	types "github.com/charmbracelet/crush/e2e/types"
)

type mockResponsesServer struct {
	// observed flag when function_call_output was sent by the client
	sawFunctionCallOutput atomic.Bool
	// optional initial delay to allow tests to observe assistant pre-event state
	initialDelay time.Duration
	// steps to gate SSE emissions
	steps chan types.Step
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
		m.steps = make(chan types.Step, 16)
	}
	if m.reqObs == nil {
		m.reqObs = make(chan string, 8)
	}
	if m.signals == nil {
		m.signals = map[string]chan struct{}{}
	}
}

func (m *mockResponsesServer) Enqueue(step types.Step) {
	m.initOnce()
	m.steps <- step
	slog.Info("mock_sse.enqueue", "ptr", fmt.Sprintf("%p", m), "len", len(m.steps))
}
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
	slog.Info("mock_sse.open", "path", "/v1/responses", "ptr", fmt.Sprintf("%p", m))
	// Responses API always begins with response.created; emit immediately to avoid client retry
	writeSSE(w, flusher, sseResponseCreated().Data)
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
		// Second-turn stream carrying final assistant text: emit completion inline and close.
		for _, e := range []SSE{
			// We already emitted response.created at open on non-title streams in some flows; emit again harmlessly.
			sseResponseCreated(),
			sseOutputItemAdded("out1"),
			sseContentPartAdded("out1"),
			sseTextDelta("Done", "out1"),
			sseTextDone(),
			sseContentPartDone("out1", "Done"),
			sseOutputItemDone("out1", "Done"),
			sseCompletedText("Done", "out1"),
		} {
			writeSSE(w, flusher, e.Data)
		}
		return
	}
	// Uniform step-driven streaming: always honor WaitUntil before emitting, including the first step.
	rctx := r.Context()
	slog.Info("mock_sse.queue_len", "steps", len(m.steps))
	for {
		select {
		case <-rctx.Done():
			slog.Info("mock_sse.client_ctx_done")
			return
		case step, ok := <-m.steps:
			if !ok {
				return
			}
			slog.Info("mock_sse.step", "wait_conditions", len(step.WaitUntil), "actions", len(step.Do))
			// Wait for all conditions
			for _, c := range step.WaitUntil {
				switch c.Kind {
				case types.CondSignal:
					ch, ok := m.signals[c.Name]
					if !ok {
						ch = make(chan struct{}, 1)
						m.signals[c.Name] = ch
					}
					select {
					case <-rctx.Done():
						return
					case <-ch:
					}
				case types.CondSleep:
					select {
					case <-rctx.Done():
						return
					case <-time.After(c.Duration):
					}
				case types.CondNone:
					// no-op
				case types.CondRequestBodyContains:
					for {
						select {
						case <-rctx.Done():
							return
						case body := <-m.reqObs:
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
}

func writeSSE(w http.ResponseWriter, flusher http.Flusher, v any) {
	enc, _ := json.Marshal(v)
	bw := bufio.NewWriter(w)
	// If payload includes a top-level "type" field, emit it as the SSE event name and log
	if m, ok := v.(map[string]any); ok {
		if t, ok := m["type"].(string); ok && t != "" {
			slog.Info("mock_sse.send", "type", t)
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
