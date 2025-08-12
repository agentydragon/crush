package e2e

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/llm/agent"
	crushlog "github.com/charmbracelet/crush/internal/log"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/stretchr/testify/require"
)

func TestAgentResponsesScenario_ToolLess_Mock(t *testing.T) {
	timer := time.AfterFunc(30*time.Second, func() { t.Fatalf("test timeout (30s)") })
	defer timer.Stop()
	mock := &mockResponsesServer{}
	ts := httptest.NewServer(mock)
	defer ts.Close()

	agentSvc, sessions, messages, artifactDir, cleanup := SetupServices(t, ts.URL+"/v1", []string{}, "")
	defer cleanup()
	crushlog.Setup(filepath.Join(artifactDir, "logs", "crush.log"), true)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sess, err := sessions.Create(ctx, "e2e")
	require.NoError(t, err)

	// Subscribe before running agent to avoid missing updates
	updates := messages.Subscribe(ctx)

	events, err := agentSvc.Run(ctx, sess.ID, "Say ok")
	require.NoError(t, err)

	// Wait for assistant CreatedEvent
	createdDeadline := time.After(5 * time.Second)
	for {
		select {
		case <-createdDeadline:
			t.Fatalf("timeout waiting for assistant creation via pubsub")
		case ev := <-updates:
			if ev.Type != pubsub.CreatedEvent {
				continue
			}
			m := ev.Payload
			if m.SessionID == sess.ID && m.Role == message.Assistant {
				goto haveAssistant
			}
		}
	}
haveAssistant:

	// Emit a canonical minimal output message sequence then completed
	const itemID = "msg_out"
	mock.Enqueue(Step{Do: []Action{
		actionEmit(
			sseResponseCreated(),
			sseOutputItemAdded(itemID),
			sseContentPartAdded(itemID),
			sseTextDelta("ok", itemID),
			sseTextDone(),
			sseContentPartDone(itemID, "ok"),
			sseOutputItemDone(itemID, "ok"),
			sseCompletedText("ok", itemID),
		),
		actionClose(),
	}})

	// Wait for text via DB only (Eventually)
	textDeadline := time.After(5 * time.Second)
	for {
		msgsNow, _ := messages.List(ctx, sess.ID)
		if len(msgsNow) >= 2 {
			last := msgsNow[len(msgsNow)-1]
			if last.Role == message.Assistant && last.Content().Text == "ok" && last.IsFinished() {
				break
			}
		}
		select {
		case <-textDeadline:
			t.Fatalf("timeout waiting for assistant text in DB")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	var final message.Message
	for {
		select {
		case <-ctx.Done():
			t.Fatalf("mock test timed out: %v", ctx.Err())
		case ev, ok := <-events:
			if !ok {
				goto done
			}
			if ev.Type == agent.AgentEventTypeResponse && ev.Done {
				final = ev.Message
			}
		}
	}

done:
	require.NotEmpty(t, final.ID)
	require.False(t, agentSvc.IsBusy())

	msgs, err := messages.List(ctx, sess.ID)
	require.NoError(t, err)
	last := msgs[len(msgs)-1]
	require.Equal(t, message.Assistant, last.Role)
	require.True(t, last.IsFinished())
	require.Equal(t, "ok", last.Content().Text)
	require.NoError(t, saveJSON(filepath.Join(artifactDir, "timeline.json"), snapshot("final", msgs)))
}

func TestAgentResponsesScenarioBasic_Live(t *testing.T) {
	timer := time.AfterFunc(30*time.Second, func() { t.Fatalf("test timeout (30s)") })
	defer timer.Stop()
	if os.Getenv("E2E_LIVE") == "" || os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("live test disabled")
	}
	agentSvc, sessions, messages, artifactDir, cleanup := SetupServices(t, "https://api.openai.com/v1", []string{}, "")
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	sess, err := sessions.Create(ctx, "live-e2e")
	require.NoError(t, err)

	events, err := agentSvc.Run(ctx, sess.ID, "Please respond with exactly: ok")
	require.NoError(t, err)
	for {
		select {
		case <-ctx.Done():
			t.Fatalf("live test timed out: %v", ctx.Err())
		case _, ok := <-events:
			if !ok {
				goto liveDone
			}
		}
	}

liveDone:
	require.False(t, agentSvc.IsBusy())
	msgs, err := messages.List(ctx, sess.ID)
	require.NoError(t, err)
	require.NoError(t, saveJSON(filepath.Join(artifactDir, "timeline.json"), snapshot("final", msgs)))
	wireLog := filepath.Join(artifactDir, "logs", "provider-wire.log")
	fi, err := os.Stat(wireLog)
	require.NoError(t, err)
	require.Greater(t, fi.Size(), int64(0))
}
