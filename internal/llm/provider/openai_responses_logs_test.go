package provider

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/catwalk/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/message"
)

func TestOpenAIResponsesLogsIncludeReasoning(t *testing.T) {
	// Ensure config (and debug logging) is initialized
	if config.Get() == nil {
		if _, err := config.Init(".", true); err != nil {
			t.Fatalf("failed to init config: %v", err)
		}
	}

	var captured string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/responses") {
			b, _ := io.ReadAll(r.Body)
			captured = string(b)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error": {"message": "bad request"}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	client := &openaiResponsesClient{
		providerOptions: providerClientOptions{
			modelType:     config.SelectedModelTypeLarge,
			apiKey:        "test-key",
			baseURL:       server.URL,
			systemMessage: "test",
			model: func(config.SelectedModelType) catwalk.Model {
				return catwalk.Model{
					ID:               "test-model",
					Name:             "test-model",
					CanReason:        true,
					DefaultMaxTokens: 128,
				}
			},
		},
		client: createOpenAIClient(providerClientOptions{apiKey: "test-key", baseURL: server.URL}),
	}

	messages := []message.Message{
		{
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.ReasoningSummaryContent{Summary: "THINKING_LOGS"},
			},
		},
		{
			Role:  message.User,
			Parts: []message.ContentPart{message.TextContent{Text: "Next"}},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = client.send(ctx, messages, nil)

	if !strings.Contains(captured, "\"type\":\"reasoning\"") || !strings.Contains(captured, "reasoning.encrypted_content") || !strings.Contains(captured, "THINKING_LOGS") {
		t.Fatalf("request body did not contain reasoning fields, got: %s", captured)
	}

	logPath := filepath.Join(config.Get().Options.DataDirectory, "logs", "crush.log")
	deadline := time.Now().Add(2 * time.Second)
	var found bool
	for time.Now().Before(deadline) && !found {
		fd, err := os.Open(logPath)
		if err == nil {
			s := bufio.NewScanner(fd)
			for s.Scan() {
				line := s.Bytes()
				var rec map[string]any
				if json.Unmarshal(line, &rec) == nil {
					if msg, _ := rec["msg"].(string); msg == "HTTP Request" {
						if body, _ := rec["body"].(string); strings.Contains(body, "\"type\":\"reasoning\"") && strings.Contains(body, "reasoning.encrypted_content") && strings.Contains(body, "THINKING_LOGS") {
							found = true
							break
						}
					}
				}
			}
			fd.Close()
		}
		if !found {
			time.Sleep(50 * time.Millisecond)
		}
	}
	if !found {
		t.Fatalf("did not find reasoning fields in HTTP Request logs at %s", logPath)
	}
}
