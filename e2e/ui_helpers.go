package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/llm/agent"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/app"
	"github.com/charmbracelet/crush/internal/session"
	chat "github.com/charmbracelet/crush/internal/tui/components/chat"
	"github.com/charmbracelet/x/ansi"
)

const (
	// Safety cap so we never spin indefinitely on animation ticks
	pumpMaxSteps      = 8
	pumpMaxTotalSleep = 750 * time.Millisecond
)

// BuildChatCmp creates a MessageListCmp bound to the scenario's services.
func BuildChatCmp(c *ScenarioCtx) chat.MessageListCmp {
	appMinimal := &app.App{Messages: c.Messages, Permissions: c.Permissions}
	cmp := chat.New(appMinimal)
	_ = cmp.SetSize(100, 80)
	_ = cmp.SetSession(session.Session{ID: c.SessionID})
	return cmp
}

// SetupLiveUI ensures a single live chat component is created and wired for the scenario.
func SetupLiveUI(c *ScenarioCtx) chat.MessageListCmp {
	if c.LiveCmp != nil {
		return c.LiveCmp
	}
	cmp := BuildChatCmp(c)
	PipeEventsToComponent(c, cmp)
	c.LiveCmp = cmp
	return cmp
}

// SnapshotView renders the current chat view to a ui_<step>.txt file under artifacts.
func SnapshotView(c *ScenarioCtx, step string) error {
	viewPath := filepath.Join(c.ArtifactDir, "ui_"+sanitize(step)+".txt")
	_ = os.MkdirAll(filepath.Dir(viewPath), 0o755)
	// Build a fresh component from DB state to mirror prod snapshot behavior
	appMinimal := &app.App{Messages: c.Messages, Permissions: c.Permissions}
	cmp := chat.New(appMinimal)
	_ = cmp.SetSize(100, 30)
	_ = cmp.SetSession(session.Session{ID: c.SessionID})
	return os.WriteFile(viewPath, []byte(ansi.Strip(cmp.View())), 0o644)
}

// WaitForViewContains polls cmp.View() for a substring up to timeout.
func WaitForViewContains(cmp chat.MessageListCmp, substr string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	lastPrint := time.Time{}
	pump := func(cmd tea.Cmd) {
		steps := 0
		for cmd != nil && steps < 4 {
			msg := cmd()
			var next tea.Cmd
			_, next = cmp.Update(msg)
			cmd = next
			steps++
		}
	}
	for time.Now().Before(deadline) {
		// Keep viewport pinned to bottom so newest tool state is visible.
		pump(cmp.GoToBottom())
		raw := ansi.Strip(cmp.View())
		// Normalize: strip leading spaces on each line so substring checks are robust to UI padding.
		lines := strings.Split(raw, "\n")
		for i, ln := range lines {
			lines[i] = strings.TrimLeft(ln, " \t")
		}
		view := strings.Join(lines, "\n")
		if strings.Contains(view, substr) {
			return true
		}
		if time.Since(lastPrint) > 250*time.Millisecond {
			// Print a short preview to aid debugging on timeouts
			preview := view
			if len(preview) > 240 {
				preview = preview[:240] + "…"
			}
			fmt.Printf("[WaitForViewContains] looking for %q; current view head:\n%s\n---\n", substr, preview)
			lastPrint = time.Now()
		}
		time.Sleep(15 * time.Millisecond)
	}
	return false
}

// PipeEventsToComponent wires agent and message events to a chat component and
// executes returned commands to ensure re-render ticks are processed.
func PipeEventsToComponent(c *ScenarioCtx, cmp chat.MessageListCmp) {
	execCmdBounded := func(cmd tea.Cmd) {
		steps := 0
		start := time.Now()
		for cmd != nil && steps < pumpMaxSteps && time.Since(start) < pumpMaxTotalSleep {
			msg := cmd()
			var next tea.Cmd
			_, next = cmp.Update(msg)
			cmd = next
			steps++
		}
	}
	// Agent events
	evs := c.Agent.Subscribe(c.Ctx)
	go func() {
		for e := range evs {
			pe := pubsub.Event[agent.AgentEvent]{Type: pubsub.UpdatedEvent, Payload: e.Payload}
			_, cmd := cmp.Update(pe)
			execCmdBounded(cmd)
		}
	}()
	// Message events
	msgEvents := c.Messages.Subscribe(c.Ctx)
	go func() {
		for ev := range msgEvents {
			_, cmd := cmp.Update(ev)
			execCmdBounded(cmd)
		}
	}()
}
