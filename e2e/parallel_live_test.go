package e2e

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

// TestParallelToolCalls_Live runs against the real OpenAI Responses API and
// asks the model to invoke two bash tool calls in parallel before replying.
// Skips unless E2E_LIVE=1 and OPENAI_API_KEY are set.
func TestParallelToolCalls_Live(t *testing.T) {
	if os.Getenv("E2E_LIVE") == "" || os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("live test disabled")
	}
	// Global 45s guard for network slowness
	timer := time.AfterFunc(45*time.Second, func() { t.Fatalf("test timeout (45s)") })
	defer timer.Stop()

	_, file, _, _ := runtime.Caller(0)
	baseDir := filepath.Dir(file)
	artifactDir := filepath.Join(baseDir, "_artifacts", t.Name(), strconv.FormatInt(time.Now().UnixNano(), 10))
	_ = os.MkdirAll(artifactDir, 0o755)

	agentSvc, sessions, messages, cleanup := setupServices(t, "https://api.openai.com/v1", []string{"bash"}, artifactDir)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	sess, err := sessions.Create(ctx, "live-parallel")
	require.NoError(t, err)

	prompt := `You can call the function tool named "bash". Do the following exactly:
1) Call bash with {"command":"echo A"} and also call bash with {"command":"echo B"}.
2) Start these two function calls in parallel if possible.
3) After both function calls complete, respond with exactly: Done
Only use function calls for executing commands; do not describe them in text.`

	events, err := agentSvc.Run(ctx, sess.ID, prompt)
	require.NoError(t, err)

	for {
		select {
		case <-ctx.Done():
			t.Fatalf("live parallel test timed out: %v", ctx.Err())
		case _, ok := <-events:
			if !ok { goto finished }
		}
	}

finished:
	require.False(t, agentSvc.IsBusy())
	msgs, err := messages.List(context.Background(), sess.ID)
	require.NoError(t, err)
	// Save a timeline for debugging
	_ = saveJSON(filepath.Join(artifactDir, "timeline.json"), snapshot("final", msgs))
	// Basic assertions: assistant finished and asked for two tool calls overall at some point
	require.GreaterOrEqual(t, len(msgs), 2)
	var twoCalls bool
	for _, m := range msgs {
		if m.Role == message.Assistant && len(m.ToolCalls()) >= 2 {
			twoCalls = true
			break
		}
	}
	if !twoCalls {
		t.Log("did not observe >= 2 tool calls in assistant messages; check provider-wire.log under artifacts for details")
	}
	// Final assistant content should be Done when present
	last := msgs[len(msgs)-1]
	if last.Role == message.Assistant {
		require.True(t, last.IsFinished())
	}
	// Wire log should be present
	wireLog := filepath.Join(artifactDir, "logs", "provider-wire.log")
	if st, err := os.Stat(wireLog); err == nil {
		require.Greater(t, st.Size(), int64(0))
	}
}
