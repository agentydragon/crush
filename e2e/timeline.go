package e2e

import (
	"fmt"
	"encoding/json"
	"os"

	"github.com/charmbracelet/crush/internal/message"
)

type timelineEntry struct {
	Label    string         `json:"label"`
	Messages []timelineMsg  `json:"messages"`
}

type timelineMsg struct {
	Role      string            `json:"role"`
	Content   string            `json:"content"`
	Reasoning *timelineReason   `json:"reasoning,omitempty"`
	ToolCalls []timelineTool    `json:"tool_calls,omitempty"`
	Finished  bool              `json:"finished"`
}

type timelineReason struct {
	ID   string `json:"id,omitempty"`
	Enc  string `json:"enc,omitempty"`
	Text string `json:"text,omitempty"`
	Done bool   `json:"done"`
}

type timelineTool struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Input    string `json:"input"`
	Finished bool   `json:"finished"`
}

func snapshot(label string, msgs []message.Message) timelineEntry {
	out := timelineEntry{Label: label}
	for _, m := range msgs {
		tm := timelineMsg{
			Role:     string(m.Role),
			Content:  m.Content().Text,
			Finished: m.IsFinished(),
		}
		rc := m.ReasoningSummary()
		if rc.Summary != "" || rc.EncryptedContent != "" || rc.ID != "" {
			tm.Reasoning = &timelineReason{ID: rc.ID, Enc: rc.EncryptedContent, Text: rc.Summary, Done: rc.FinishedAt != 0}
		}
		for _, tc := range m.ToolCalls() {
			tm.ToolCalls = append(tm.ToolCalls, timelineTool{ID: tc.ID, Name: tc.Name, Input: tc.Input, Finished: tc.Finished})
		}
		out.Messages = append(out.Messages, tm)
	}
	return out
}

func saveJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return fmt.Errorf("failed to write JSON to %s: %w", path, err)
	}
	    return nil
}