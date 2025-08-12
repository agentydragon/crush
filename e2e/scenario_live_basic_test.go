package e2e

import (
	"os"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/llm/agent"
	"github.com/stretchr/testify/require"
)

func TestScenario_Basic_Live(t *testing.T) {
	if os.Getenv("E2E_LIVE") == "" || os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("live test disabled")
	}
	sc, events, cleanup := NewScenario(
		t,
		t.Name(),
		"https://api.openai.com/v1",
		"Please respond with exactly: ok",
		LiveOrchestrator{},
		[]string{},
		15*time.Second,
	)
	defer cleanup()
	RunSteps(sc, StepAssistantCreated(), StepExpectFinalText("ok"))
	for {
		select {
		case <-sc.Ctx.Done():
			t.Fatalf("live test timed out: %v", sc.Ctx.Err())
		case ev, ok := <-events:
			if !ok {
				goto done
			}
			if ev.Type == agent.AgentEventTypeResponse && ev.Done {
				goto done
			}
		}
	}

done:
	require.False(t, sc.Agent.IsBusy())
	// provider-wire.log should be present under this test's artifact dir
	if st, err := os.Stat(sc.ArtifactDir + "/logs/provider-wire.log"); err == nil {
		require.Greater(t, st.Size(), int64(0))
	}
}
