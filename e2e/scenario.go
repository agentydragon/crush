package e2e

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/llm/agent"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/session"
)

type ScenarioCtx struct {
	T           *testing.T
	Ctx         context.Context
	Agent       agent.Service
	Sessions    session.Service
	Messages    message.Service
	SessionID   string
	ArtifactDir string
	Orch        Orchestrator

	PerStepBudget time.Duration
}

// Apply common per-test options: wire logging on, MCP wire logging on, route wire logs to this test's artifact dir.
func (c *ScenarioCtx) ApplyCommonOptions(cfg *config.Config) {
	if cfg.Options == nil { cfg.Options = &config.Options{} }
	cfg.Options.DebugProviderWire = true
	if cfg.Options.Wire == nil { cfg.Options.Wire = &config.WireOptions{} }
	trueVal := true
	cfg.Options.Wire.DebugMCPWire = &trueVal
	cfg.Options.DataDirectory = c.ArtifactDir
}

type ScenarioStep struct {
	Name   string
	Act    func(*ScenarioCtx)
	Assert func(*testing.T, *ScenarioCtx)
}

func RunSteps(ctx *ScenarioCtx, steps ...ScenarioStep) {
	for _, s := range steps {
		if s.Act != nil {
			s.Act(ctx)
		}
		if s.Assert != nil {
			s.Assert(ctx.T, ctx)
		}
		_ = saveJSON(filepath.Join(ctx.ArtifactDir, "timeline.json"), snapshot(s.Name, mustList(ctx)))
	}
}

func (c *ScenarioCtx) Eventually(name string, cond func() bool) {
	deadline := time.After(c.PerStepBudget)
	for {
		if cond() {
			return
		}
		select {
		case <-deadline:
			c.T.Fatalf("Eventually(%s) timed out after %s", name, c.PerStepBudget)
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func mustList(ctx *ScenarioCtx) []message.Message {
	msgs, err := ctx.Messages.List(context.Background(), ctx.SessionID)
	if err != nil {
		ctx.T.Fatalf("list messages: %v", err)
	}
	return msgs
}

type Orchestrator interface {
	EmitCreated()
	EmitOutputMessageSequence(itemID, text string)
	Close()
}

type MockOrchestrator struct{ srv *mockResponsesServer }

func NewMockOrchestrator(srv *mockResponsesServer) *MockOrchestrator {
	return &MockOrchestrator{srv: srv}
}

func (m *MockOrchestrator) EmitCreated() {
	m.srv.Enqueue(Step{Do: []Action{actionEmit(sseResponseCreated())}})
}

func (m *MockOrchestrator) EmitOutputMessageSequence(itemID, text string) {
	m.srv.Enqueue(Step{Do: []Action{actionEmit(
		sseOutputItemAdded(itemID),
		sseContentPartAdded(itemID),
		sseTextDelta(text, itemID),
		sseTextDone(),
		sseContentPartDone(itemID, text),
		sseOutputItemDone(itemID, text),
		sseCompletedText(text, itemID),
	)}})
}

func (m *MockOrchestrator) Close() { m.srv.Enqueue(Step{Do: []Action{actionClose()}}) }

type LiveOrchestrator struct{}

func (LiveOrchestrator) EmitCreated()                                  {}
func (LiveOrchestrator) EmitOutputMessageSequence(itemID, text string) {}
func (LiveOrchestrator) Close()                                        {}
