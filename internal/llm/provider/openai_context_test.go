package provider

import (
	"strings"
	"testing"

	"github.com/charmbracelet/catwalk/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/openai/openai-go"
)

func TestOpenAIChat_ContextLengthExceeded_NotRetriable(t *testing.T) {
	c := &openaiClient{providerOptions: providerClientOptions{
		modelType: config.SelectedModelTypeLarge,
		model: func(config.SelectedModelType) catwalk.Model {
			return catwalk.Model{ID: "test-model"}
		},
	}}
	err := &openai.Error{Type: "invalid_request_error", Code: "context_length_exceeded", Message: "Your input exceeds the context window.", StatusCode: 400}
	retry, after, out := c.shouldRetry(1, err)
	if retry {
		t.Fatalf("expected not retry, got retry=true")
	}
	if after != 0 {
		t.Fatalf("expected after=0, got %d", after)
	}
	if out == nil {
		t.Fatalf("expected error, got nil")
	}
	if strings.Contains(out.Error(), "rate limit") || strings.Contains(out.Error(), "quota") {
		t.Fatalf("expected non-quota error, got: %v", out)
	}
	if !strings.Contains(out.Error(), "context window exceeded for model test-model") {
		t.Fatalf("unexpected error: %v", out)
	}
}

func TestOpenAIResponses_ContextLengthExceeded_NotRetriable(t *testing.T) {
	c := &openaiResponsesClient{providerOptions: providerClientOptions{
		modelType: config.SelectedModelTypeLarge,
		model: func(config.SelectedModelType) catwalk.Model {
			return catwalk.Model{ID: "test-model"}
		},
	}}
	err := &openai.Error{Type: "invalid_request_error", Code: "context_length_exceeded", Message: "Your input exceeds the context window.", StatusCode: 400}
	retry, after, out := c.shouldRetry(1, err)
	if retry {
		t.Fatalf("expected not retry, got retry=true")
	}
	if after != 0 {
		t.Fatalf("expected after=0, got %d", after)
	}
	if out == nil {
		t.Fatalf("expected error, got nil")
	}
	if strings.Contains(out.Error(), "rate limit") || strings.Contains(out.Error(), "quota") {
		t.Fatalf("expected non-quota error, got: %v", out)
	}
	if !strings.Contains(out.Error(), "context window exceeded for model test-model") {
		t.Fatalf("unexpected error: %v", out)
	}
}
