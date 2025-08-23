package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/responses"
	"github.com/openai/openai-go/shared"
)

// TestResponsesSSE_Smoke ensures the SDK can decode a minimal SSE two-event stream
// (response.created -> response.completed) without any Crush wiring.
func TestResponsesSSE_Smoke(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/responses", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		// Minimal well-formed SSE stream with created + completed
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		flusher, _ := w.(http.Flusher)

		// Read body to simulate server behavior
		_, _ = io.ReadAll(r.Body)
		_ = r.Body.Close()

		created := "event: response.created\n" +
			"data: {\"type\":\"response.created\",\"sequence_number\":1,\"response\":{\"id\":\"resp_1\",\"created_at\":0,\"error\":{\"code\":\"\",\"message\":\"\"},\"incomplete_details\":{\"reason\":\"\"},\"instructions\":\"\",\"metadata\":{},\"model\":\"gpt-4o-mini\",\"object\":\"response\",\"output\":[],\"parallel_tool_calls\":false,\"temperature\":0,\"tool_choice\":\"auto\",\"tools\":[],\"top_p\":1}}\n\n"
		w.Write([]byte(created))
		if flusher != nil { flusher.Flush() }

		completed := "event: response.completed\n" +
			"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"created_at\":0,\"error\":{\"code\":\"\",\"message\":\"\"},\"incomplete_details\":{\"reason\":\"\"},\"instructions\":\"\",\"metadata\":{},\"model\":\"gpt-4o-mini\",\"object\":\"response\",\"output\":[],\"parallel_tool_calls\":false,\"temperature\":0,\"tool_choice\":\"auto\",\"tools\":[],\"top_p\":1,\"usage\":{\"input_tokens\":0,\"output_tokens\":0}}}\n\n"
		w.Write([]byte(completed))
		if flusher != nil { flusher.Flush() }
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client := openai.NewClient(option.WithBaseURL(server.URL))
	// Minimal params via SDK types (user message with text content)
	content := responses.ResponseInputMessageContentListParam{
		responses.ResponseInputContentParamOfInputText("hi"),
	}
	inputItem := responses.ResponseInputItemParamOfInputMessage(content, "user")
	params := responses.ResponseNewParams{Model: shared.ResponsesModel("gpt-4o-mini")}
	params.Input = responses.ResponseNewParamsInputUnion{OfInputItemList: []responses.ResponseInputItemUnionParam{inputItem}}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream := client.Responses.NewStreaming(ctx, params)
	count := 0
	for stream.Next() {
		count++
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("stream error: %v", err)
	}
	if count < 2 {
		t.Fatalf("expected >=2 events, got %d", count)
	}
}
