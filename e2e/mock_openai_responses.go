package e2e

import (
	"bufio"
	"encoding/json"
	"log/slog"
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

func (m *mockResponsesServer) Enqueue(step Step) { m.initOnce(); slog.Info("mock_sse.enqueue"); m.steps <- step }
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
	slog.Info("mock_sse.open", "path", "/v1/responses")
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
	// Uniform step-driven streaming: always honor WaitUntil before emitting, including the first step.
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

// deadcode pruned: emitStage1 was unused


