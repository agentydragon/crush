package e2e

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/llm/agent"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

func TestSequenceTransformer_TextOnlyNextResample(t *testing.T) {
	// Setup mock provider server
	mock := &mockResponsesServer{}
	ts := httptest.NewServer(mock)
	defer ts.Close()
	agent.ResetMCPForTests()
	agentSvc, sessionsSvc, messagesSvc, perms, artifactDir, cleanup := SetupServicesWithConfig(t, ts.URL+"/v1", nil, "", func(cfg *config.Config) {
		if cfg.Options.Wire == nil { cfg.Options.Wire = &config.WireOptions{} }
		cfg.Options.Wire.MCP.Enabled = true
		cfg.MCP["inproc_text"] = config.MCPConfig{Type: config.MCPStdio, HandlesHook: true}
	}, WithInprocHookMCPText(t))
	defer cleanup()
	// Enable wire logs and apply common options
	cfg := config.Get()
	(&ScenarioCtx{ArtifactDir: artifactDir}).ApplyCommonOptions(cfg)

	ctx := context.Background()
	s, err := sessionsSvc.Create(ctx, "hook-text-only-next")
	require.NoError(t, err)
	perms.AutoApproveSession(s.ID)

	cfg.MCP["inproc_text"] = config.MCPConfig{Type: config.MCPStdio, HandlesHook: true}

	// Orchestrate provider: 1st stream (assistant), then 2nd stream (resample) after hook triggers next=assistant_sampling
	mock.Enqueue(Step{Do: []Action{actionEmit(sseResponseCreated())}})
	mock.Enqueue(Step{Do: []Action{actionEmit(sseTextDelta("first", "out1"), sseTextDone(), sseCompletedText("first", "out1")), actionClose()}})
	// Allow hook to run and agent to set resample flag, then emit a new response.created and completion
	mock.Enqueue(Step{Do: []Action{actionEmit(sseResponseCreated())}})
	mock.Enqueue(Step{Do: []Action{actionEmit(sseTextDelta("second", "out2"), sseTextDone(), sseCompletedText("second", "out2"))}})
	mock.Enqueue(Step{Do: []Action{actionClose()}})

	evCh, err := agentSvc.Run(ctx, s.ID, "trigger resample")
	require.NoError(t, err)

	deadline := time.After(10 * time.Second)
	var sawSecond bool
	for !sawSecond {
		select {
		case <-deadline:
			t.Fatalf("timeout waiting for resample second stream")
		case <-evCh:
			// ignore live; verify by DB snapshot
		default:
		}
		// We observe resample by either (a) mock saw two streams or (b) DB shows a second assistant
		if mock.streamsStarted.Load() >= 2 {
			sawSecond = true
		} else {
			msgs, _ := messagesSvc.List(ctx, s.ID)
			if len(msgs) >= 3 {
				last := msgs[len(msgs)-1]
				if last.Role == message.Assistant && last.IsFinished() {
					sawSecond = true
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
}
