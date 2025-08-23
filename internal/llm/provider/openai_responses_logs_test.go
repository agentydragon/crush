package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/catwalk/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/testutil/testenv"
)

func TestOpenAIResponsesRequestIncludesReasoning(t *testing.T) {
	// Isolate from user environment
	_, cleanup := testenv.MustSetup(t)
	defer cleanup()
	if _, err := config.Init(".", true); err != nil {
		t.Fatalf("failed to init config: %v", err)
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
				message.ReasoningEncryptedContent{ID: "r1", EncryptedContent: "abc"},
				message.ReasoningSummaryContent{ID: "r1", Summary: "THINKING_LOGS"},
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

	if !strings.Contains(captured, "\"type\":\"reasoning\"") || !strings.Contains(captured, "reasoning.encrypted_content") {
		t.Fatalf("request body did not contain required reasoning fields, got: %s", captured)
	}
}
