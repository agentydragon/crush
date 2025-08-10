package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/catwalk/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
)

func TestMain(m *testing.M) {
	_, err := config.Init(".", true)
	if err != nil {
		panic("Failed to initialize config: " + err.Error())
	}

	os.Exit(m.Run())
}

func TestOpenAIClientStreamChoices(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)

		emptyChoicesChunk := map[string]any{
			"id":      "chat-completion-test",
			"object":  "chat.completion.chunk",
			"created": time.Now().Unix(),
			"model":   "test-model",
			"choices": []any{},
		}

		jsonData, _ := json.Marshal(emptyChoicesChunk)
		w.Write([]byte("data: " + string(jsonData) + "\n\n"))
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := &openaiResponsesClient{
		providerOptions: providerClientOptions{
			modelType:     config.SelectedModelTypeLarge,
			apiKey:        "test-key",
			systemMessage: "test",
			model: func(config.SelectedModelType) catwalk.Model {
				return catwalk.Model{
					ID:   "test-model",
					Name: "test-model",
				}
			},
		},
		client: openai.NewClient(
			option.WithAPIKey("test-key"),
			option.WithBaseURL(server.URL),
		),
	}

	messages := []message.Message{
		{
			Role:  message.User,
			Parts: []message.ContentPart{message.TextContent{Text: "Hello"}},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventsChan := client.stream(ctx, messages, nil)

	for event := range eventsChan {
		t.Logf("Received event: %+v", event)
		if event.Type == EventError || event.Type == EventComplete {
			break
		}
	}
}

func TestOpenAIClientCarriesForwardReasoning(t *testing.T) {
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
		client: openai.NewClient(
			option.WithAPIKey("test-key"),
			option.WithBaseURL(server.URL),
		),
	}

	messages := []message.Message{
		{
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.ReasoningSummaryContent{Summary: "THINKING123"},
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

	if !strings.Contains(captured, "reasoning.encrypted_content") {
		t.Fatalf("expected include reasoning.encrypted_content in request: %s", captured)
	}
	if !strings.Contains(captured, "\"type\":\"reasoning\"") {
		t.Fatalf("expected a reasoning input item in request: %s", captured)
	}
	if !strings.Contains(captured, "THINKING123") {
		t.Fatalf("expected reasoning summary text to include assistant thinking: %s", captured)
	}
}
