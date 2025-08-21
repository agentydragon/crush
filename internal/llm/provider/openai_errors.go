package provider

import (
	"encoding/json"
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

// parseOpenAIJSONError attempts to extract {type, code, message} from an error string
// that may contain a JSON payload, including nested {"error": {...}} shapes as
// seen with Responses API streaming. Returns lower-cased type/code.
func parseOpenAIJSONError(raw string) (typ, code, msg string, ok bool) {
	if raw == "" {
		return "", "", "", false
	}
	// Find a JSON object substring
	s := strings.TrimSpace(raw)
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end <= start {
		return "", "", "", false
	}
	sub := s[start : end+1]
	// Try common shapes
	type inner struct {
		Type    string `json:"type"`
		Code    string `json:"code"`
		Message string `json:"message"`
		Param   string `json:"param"`
		Error   *struct {
			Type    string `json:"type"`
			Code    string `json:"code"`
			Message string `json:"message"`
			Param   string `json:"param"`
		} `json:"error"`
	}
	var v inner
	if err := json.Unmarshal([]byte(sub), &v); err != nil {
		return "", "", "", false
	}
	if v.Error != nil {
		return strings.ToLower(v.Error.Type), strings.ToLower(v.Error.Code), v.Error.Message, true
	}
	return strings.ToLower(v.Type), strings.ToLower(v.Code), v.Message, true
}
