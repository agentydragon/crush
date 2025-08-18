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
)

func TestOpenAIResponses_OmitsReasoningForNonReasoningModel(t *testing.T) {
	// Ensure config is initialized
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
					CanReason:        false,
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
				message.ReasoningSummaryContent{ID: "r1", EncryptedContent: "abc", Summary: "THINK"},
			},
		},
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "Next"}}},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = client.send(ctx, messages, nil)

	if strings.Contains(captured, "reasoning.encrypted_content") || strings.Contains(captured, "\"type\":\"reasoning\"") {
		t.Fatalf("request must not include reasoning fields for non-reasoning model, got: %s", captured)
	}
	if strings.Contains(captured, "\"reasoning\":") {
		t.Fatalf("request must not include top-level reasoning param for non-reasoning model, got: %s", captured)
	}
}
