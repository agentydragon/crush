package e2e

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/llm/provider"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

// TestReasoningToNonReasoning_Live exercises:
// 1) gpt-5 (reasoning) with high effort produces a reasoning block
// 2) switch to gpt-4.1 (non-reasoning) and ensures the turn succeeds (reasoning omitted)
// Skips unless E2E_LIVE=1 and OPENAI_API_KEY are set.
func TestReasoningToNonReasoning_Live(t *testing.T) {
	if os.Getenv("E2E_LIVE") == "" || os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("live test disabled")
	}
	// Adjust models here if you use different IDs
	const reasoningModel = "gpt-5"
	const nonReasoningModel = "gpt-4.1"

	agentSvc, sessions, messages, artifactDir, cleanup := SetupServices(t, "https://api.openai.com/v1", []string{}, "")
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	sess, err := sessions.Create(ctx, "live-reasoning-switch")
	require.NoError(t, err)

	// Force providers/models: OpenAI Responses + register model IDs locally if catalog lacks them
	cfg := config.Get()
	pc, _ := cfg.Providers.Get("openai")
	pc.GenerationAPI = "responses"
	// Ensure both models exist in provider catalog for lookup
	// Ensure the model IDs exist in the provider catalog; construct minimal entries if missing
	// Rebuild models explicitly
	addIfMissing := func(id string, canReason bool, maxTok int64, defEffort string) {
		for _, m := range pc.Models {
			if m.ID == id {
				return
			}
		}
		pc.Models = append(pc.Models, pc.Models[0])
		last := len(pc.Models) - 1
		pc.Models[last].ID = id
		pc.Models[last].Name = id
		pc.Models[last].CanReason = canReason
		pc.Models[last].DefaultMaxTokens = maxTok
		pc.Models[last].DefaultReasoningEffort = defEffort
	}
	addIfMissing(reasoningModel, true, 4096, "high")
	addIfMissing(nonReasoningModel, false, 4096, "")
	cfg.Providers.Set("openai", pc)
	cfg.Models[config.SelectedModelTypeLarge] = config.SelectedModel{Provider: "openai", Model: reasoningModel, ReasoningEffort: "high", MaxTokens: 1024}
	cfg.Models[config.SelectedModelTypeSmall] = config.SelectedModel{Provider: "openai", Model: nonReasoningModel, MaxTokens: 256}
	cfg.SetupAgents()

	// Try a couple of prompts likely to trigger encrypted reasoning
	prompts := []string{
		"Solve this 3x3 logic grid puzzle:\nPeople: Alice,Bob,Carol. Pets: cat,dog,fish. Houses: red,blue,green.\nConstraints: (1) Alice does not own the dog. (2) The fish owner lives in the green house. (3) Bob lives in the red house. (4) Carol does not live in the blue house. (5) The cat's owner is not Bob.\nReturn only JSON mapping each person to {pet, house} with no explanation.",
		"Compute 9876543210987^2 - 1234567890123^2 exactly; output only the integer, no commas, no spaces, no explanation.",
		// SAT CNF: list all satisfying assignments
		"List ALL satisfying assignments for this CNF over variables x1..x8. Output each assignment as a single line 'x1=0 x2=1 ... x8=0' with no extra text.\nCNF in DIMACS-like form (space-separated literals, 0 at end of clause; negative means negation):\n(x1 x2 -x3 0)\n(-x1 x4 x5 0)\n(-x2 -x4 x6 0)\n(x3 -x5 x7 0)\n(-x6 x8 -x1 0)\n(x7 -x2 0)\n(x8 x3 0)\n(-x7 -x3 0)\n(x4 -x5 0)\n(-x4 x5 0)\n(x6 -x8 0)\n(-x6 x8 0)",
	}
	var hasEncrypted bool
	for _, prompt1 := range prompts {
		events, err := agentSvc.Run(ctx, sess.ID, prompt1)
		require.NoError(t, err)
		for range events {
		}
		msgs, err := messages.List(ctx, sess.ID)
		require.NoError(t, err)
		require.GreaterOrEqual(t, len(msgs), 2)
		// Log assistant completions for inspection
		for _, m := range msgs {
			if m.Role == message.Assistant {
				t.Logf("assistant: finished=%v len=%d", m.IsFinished(), len(m.Content().Text))
				if s := m.Content().Text; s != "" {
					if len(s) > 400 {
						t.Logf("assistant.head: %s", s[:400])
					} else {
						t.Logf("assistant.text: %s", s)
					}
				}
				if rc := m.ReasoningSummary(); rc.EncryptedContent != "" || rc.Summary != "" {
					t.Logf("assistant.reasoning: enc=%d summary.len=%d", len(rc.EncryptedContent), len(rc.Summary))
				}
			}
			if m.Role == message.Assistant && m.ReasoningSummary().EncryptedContent != "" {
				hasEncrypted = true
				break
			}
		}
		if hasEncrypted {
			break
		}
	}
	msgsAfterFirst, _ := messages.List(ctx, sess.ID)
	_ = saveJSON(filepath.Join(artifactDir, "timeline1.json"), snapshot("after_turn_1", msgsAfterFirst))
	// Dump provider wire log tail for inspection
	wirePath := provider.CurrentWireLogPath()
	t.Logf("artifact_dir=%s wire_log=%s", artifactDir, wirePath)
	if f, err := os.Open(wirePath); err == nil {
		scanner := bufio.NewScanner(f)
		lines := []string{}
		for scanner.Scan() {
			lines = append(lines, scanner.Text())
		}
		_ = f.Close()
		start := 0
		if len(lines) > 200 {
			start = len(lines) - 200
		}
		for i := start; i < len(lines); i++ {
			t.Log(lines[i])
		}
	}
	require.True(t, hasEncrypted, "expected encrypted reasoning to be present from reasoning model turn")

	// Switch to gpt-4.1 for a follow-up; client should drop reasoning when sending to 4.1
	cfg.Models[config.SelectedModelTypeLarge] = config.SelectedModel{Provider: "openai", Model: nonReasoningModel, MaxTokens: 256}
	cfg.SetupAgents()
	_ = agentSvc.UpdateModel()
	prompt2 := "Now summarize the previous result into one sentence."
	events2, err := agentSvc.Run(ctx, sess.ID, prompt2)
	require.NoError(t, err)
	for range events2 {
	}

	msgs2, err := messages.List(ctx, sess.ID)
	require.NoError(t, err)
	_ = saveJSON(filepath.Join(artifactDir, "timeline2.json"), snapshot("after_turn_2", msgs2))
	require.GreaterOrEqual(t, len(msgs2), 3)
	last := msgs2[len(msgs2)-1]
	require.Equal(t, message.Assistant, last.Role)
	require.True(t, last.IsFinished())
}
