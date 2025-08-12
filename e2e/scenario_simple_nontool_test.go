package e2e

import (
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/llm/agent"
	"github.com/stretchr/testify/require"
)

func TestScenario_Simple_NoTool_Mock(t *testing.T) {
	sc, events, cleanup := NewScenario(t, t.Name(), "", "Say ok", NewMockOrchestrator(nil), []string{}, 5*time.Second)
	defer cleanup()
	RunSteps(sc, StepAssistantCreated(), StepTextSequence("ok"))
	for {
		select {
		case <-sc.Ctx.Done():
			t.Fatalf("mock test timed out: %v", sc.Ctx.Err())
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
}
