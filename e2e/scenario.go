package e2e

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/llm/agent"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/session"
)

type ScenarioStep struct {
	Name   string
	Act    func(ctx *ScenarioCtx)
	Assert func(t *testing.T, ctx *ScenarioCtx)
}

type Orchestrator interface {
	Advance()
	EmitTextDelta(text string)
	EmitCompleted(content string)
	Close()
}

type ScenarioCtx struct {
	T            *testing.T
	Deadline     time.Time
	PerStep      time.Duration
	Agent        agent.Service
	Sessions     session.Service
	Messages     message.Service
	SessionID    string
	Orchestrator Orchestrator
	ArtifactsDir string
}

func (c *ScenarioCtx) Eventually(msg string, fn func() bool) {
	deadline := time.Now().Add(c.PerStep)
	if deadline.After(c.Deadline) {
		deadline = c.Deadline
	}
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	c.T.Fatalf("timeout: %s", msg)
}

func (c *ScenarioCtx) Snapshot(label string) {
	ctx := context.Background()
	msgs, _ := c.Messages.List(ctx, c.SessionID)
	_ = saveJSON(filepath.Join(c.ArtifactsDir, label+".json"), snapshot(label, msgs))
}

func RunSteps(ctx *ScenarioCtx, steps ...ScenarioStep) {
	for _, st := range steps {
		if st.Act != nil { st.Act(ctx) }
		if st.Assert != nil { st.Assert(ctx.T, ctx) }
		ctx.Snapshot(st.Name)
	}
}
