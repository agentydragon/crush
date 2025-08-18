package e2e

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/app"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/llm/agent"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/session"
	chat "github.com/charmbracelet/crush/internal/tui/components/chat"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

type ScenarioCtx struct {
	T           *testing.T
	Ctx         context.Context
	Agent       agent.Service
	Sessions    session.Service
	Messages    message.Service
	Permissions permission.Service
	SessionID   string
	ArtifactDir string
	Orch        Orchestrator

	PerStepBudget time.Duration
}

func (c *ScenarioCtx) ApplyCommonOptions(cfg *config.Config) {
	if cfg.Options == nil {
		cfg.Options = &config.Options{}
	}
	cfg.Options.DebugProviderWire = true
	if cfg.Options.Wire == nil {
		cfg.Options.Wire = &config.WireOptions{}
	}
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
		// Also dump the UI view for ease of debugging
		dumpUI(ctx, s.Name)
	}
}

func dumpUI(ctx *ScenarioCtx, step string) {
	// Render chat view from the same isolated DB (cfg.Options.DataDirectory)
	viewPath := filepath.Join(ctx.ArtifactDir, "ui_"+sanitize(step)+".txt")
	// Minimal app using existing services
	appMinimal := &app.App{Messages: ctx.Messages, Permissions: ctx.Permissions}
	cmp := chat.New(appMinimal)
	_ = cmp.SetSize(100, 30)
	_ = cmp.SetSession(session.Session{ID: ctx.SessionID})
	_ = os.WriteFile(viewPath, []byte(ansi.Strip(cmp.View())), 0o644)
}

func sanitize(s string) string {
	b := make([]rune, 0, len(s))
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b = append(b, r)
		} else {
			b = append(b, '_')
		}
	}
	return string(b)
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

func MakeArtifactDir(t *testing.T, name string) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	baseDir := filepath.Dir(file)
	dir := filepath.Join(baseDir, "_artifacts", name, strconv.FormatInt(time.Now().UnixNano(), 10))
	_ = os.MkdirAll(filepath.Join(dir, "logs"), 0o755)
	return dir
}

func NewScenario(t *testing.T, name, baseURL, userPrompt string, orch Orchestrator, allowedTools []string, perStep time.Duration) (*ScenarioCtx, <-chan agent.AgentEvent, func()) {
	t.Helper()
	artifactDir := MakeArtifactDir(t, name)
	var ts *httptest.Server
	var mock *mockResponsesServer
	if srv, ok := orch.(*MockOrchestrator); ok && srv.srv == nil {
		mock = &mockResponsesServer{}
		srv.srv = mock
		ts = httptest.NewServer(mock)
		baseURL = ts.URL + "/v1"
	}
	agentSvc, sessions, messages, perms, artifactDir, cleanup := SetupServices(t, baseURL, allowedTools, "")
	ctx, cancel := context.WithTimeout(context.Background(), perStep)
	sess, err := sessions.Create(ctx, name)
	require.NoError(t, err)
	sc := &ScenarioCtx{T: t, Ctx: ctx, Agent: agentSvc, Sessions: sessions, Messages: messages, Permissions: perms, SessionID: sess.ID, ArtifactDir: artifactDir, Orch: orch, PerStepBudget: perStep}
	_ = messages.Subscribe(ctx)
	events, err := agentSvc.Run(ctx, sess.ID, userPrompt)
	require.NoError(t, err)
	cleanupAll := func() {
		cancel()
		cleanup()
		if ts != nil {
			ts.Close()
		}
	}
	return sc, events, cleanupAll
}

func StepAssistantCreated() ScenarioStep {
	return ScenarioStep{
		Name: "assistant created",
		Act:  func(c *ScenarioCtx) { c.Orch.EmitCreated() },
		Assert: func(t *testing.T, c *ScenarioCtx) {
			c.Eventually("assistant exists", func() bool {
				ms := mustList(c)
				return len(ms) >= 2 && ms[len(ms)-1].Role == message.Assistant
			})
		},
	}
}

func StepTextSequence(text string) ScenarioStep {
	return ScenarioStep{
		Name: "text delta + completion",
		Act: func(c *ScenarioCtx) {
			c.Orch.EmitOutputMessageSequence("out1", text)
			c.Orch.Close()
		},
		Assert: func(t *testing.T, c *ScenarioCtx) {
			c.Eventually("assistant finished with text", func() bool {
				ms := mustList(c)
				if len(ms) == 0 {
					return false
				}
				last := ms[len(ms)-1]
				return last.Role == message.Assistant && last.IsFinished() && last.Content().Text == text
			})
		},
	}
}

func StepExpectFinalText(text string) ScenarioStep {
	return ScenarioStep{
		Name: "expect final text",
		Assert: func(t *testing.T, c *ScenarioCtx) {
			c.Eventually("assistant finished with expected text", func() bool {
				ms := mustList(c)
				if len(ms) == 0 {
					return false
				}
				last := ms[len(ms)-1]
				return last.Role == message.Assistant && last.IsFinished() && last.Content().Text == text
			})
		},
	}
}
