package e2e

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/db"
	"github.com/charmbracelet/crush/internal/history"
	"github.com/charmbracelet/crush/internal/llm/agent"
	"github.com/charmbracelet/crush/internal/lsp"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/stretchr/testify/require"
)

func TestScenario_Simple_NoTool_Mock(t *testing.T) {
	timer := time.AfterFunc(30*time.Second, func() { t.Fatalf("test timeout (30s)") })
	defer timer.Stop()
	artifactDir := filepath.Join("e2e", "_artifacts", t.Name(), strconv.FormatInt(time.Now().UnixNano(), 10))
	_ = os.MkdirAll(artifactDir, 0o755)
	mock := &mockResponsesServer{}
	ts := httptest.NewServer(mock)
	defer ts.Close()
	agentSvc, sessions, messages, cleanup := setupServices(t, ts.URL+"/v1", []string{}, artifactDir)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sess, err := sessions.Create(ctx, "e2e-scenario")
	require.NoError(t, err)

	scenario := &ScenarioCtx{
		T:             t,
		Ctx:           ctx,
		Agent:         agentSvc,
		Sessions:      sessions,
		Messages:      messages,
		SessionID:     sess.ID,
		ArtifactDir:   artifactDir,
		Orch:          NewMockOrchestrator(mock),
		PerStepBudget: 3 * time.Second,
	}

	_ = messages.Subscribe(ctx)
	events, err := agentSvc.Run(ctx, sess.ID, "Say ok")
	require.NoError(t, err)

	RunSteps(scenario,
		ScenarioStep{
			Name: "assistant created",
			Act:  func(c *ScenarioCtx) { mock.Enqueue(Step{Do: []Action{actionEmit(sseResponseCreated())}}) },
			Assert: func(t *testing.T, c *ScenarioCtx) {
				c.Eventually("assistant exists", func() bool {
					ms, _ := c.Messages.List(context.Background(), c.SessionID)
					return len(ms) >= 2 && ms[len(ms)-1].Role == message.Assistant
				})
			},
		},
		ScenarioStep{
			Name: "text delta + completion",
			Act: func(c *ScenarioCtx) {
				NewMockOrchestrator(mock).EmitOutputMessageSequence("out1", "ok")
			},
			Assert: func(t *testing.T, c *ScenarioCtx) {
				c.Eventually("assistant finished with ok", func() bool {
					ms, _ := c.Messages.List(context.Background(), c.SessionID)
					last := ms[len(ms)-1]
					return last.IsFinished() && last.Content().Text == "ok"
				})
			},
		},
	)

	// Drain events until done
	for {
		select {
		case <-ctx.Done():
			t.Fatalf("mock test timed out: %v", ctx.Err())
		case ev, ok := <-events:
			if !ok { goto done }
			if ev.Type == agent.AgentEventTypeResponse && ev.Done { goto done }
		}
	}

done:
	require.False(t, agentSvc.IsBusy())
}

// Removed helper setupServicesNoServer in favor of using setupServices with httptest.Server
