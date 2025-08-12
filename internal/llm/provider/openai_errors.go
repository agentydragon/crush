package provider

import (
	"fmt"
	"strings"

	"github.com/openai/openai-go"
)

func isOpenAIContextLengthExceeded(err *openai.Error) bool {
	if err == nil {
		return false
	}
	t := strings.ToLower(err.Type)
	c := strings.ToLower(err.Code)
	m := strings.ToLower(err.Message)
	if (t == "invalid_request_error" || t == "invalid_request") && (c == "context_length_exceeded" || strings.Contains(m, "context_length_exceeded") || strings.Contains(m, "exceeds the context window")) {
		return true
	}
	return false
}

func wrapOpenAIContextLengthExceeded(err *openai.Error, modelID string) error {
	return fmt.Errorf("context window exceeded for model %s: %s", modelID, err.Message)
}
