package e2e

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/charmbracelet/crush/internal/llm/tools"
)

type fakeBashTool struct {
	mu   sync.Mutex
	wait map[string]chan struct{}
}

func newFakeBashTool() *fakeBashTool {
	return &fakeBashTool{wait: make(map[string]chan struct{})}
}

func (f *fakeBashTool) Name() string { return "bash" }

func (f *fakeBashTool) Info() tools.ToolInfo {
	return tools.ToolInfo{
		Name:        "bash",
		Description: "fake bash tool for e2e",
		Parameters: map[string]any{
			"command": map[string]any{"type": "string"},
		},
		Required: []string{"command"},
	}
}

func (f *fakeBashTool) unblock(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch, ok := f.wait[id]
	if ok {
		close(ch)
	}
}

func (f *fakeBashTool) Run(ctx context.Context, call tools.ToolCall) (tools.ToolResponse, error) {
	// parse minimally to simulate work
	var p struct{ Command string `json:"command"` }
	_ = json.Unmarshal([]byte(call.Input), &p)
	f.mu.Lock()
	ch, exists := f.wait[call.ID]
	if !exists {
		ch = make(chan struct{})
		f.wait[call.ID] = ch
	}
	f.mu.Unlock()

	select {
	case <-ctx.Done():
		return tools.NewTextErrorResponse("canceled"), ctx.Err()
	case <-ch:
		// return a canned response
		return tools.WithResponseMetadata(
			tools.NewTextResponse("hi\n\n<cwd>/tmp</cwd>"),
			map[string]any{"end_time": time.Now().UnixMilli()},
		), nil
	}
}
