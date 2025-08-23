package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/responses"
	"github.com/openai/openai-go/shared"
)

// Minimal e2e-mock-equivalent to ensure our decoder works with that server shape.
func TestResponsesSSE_WithLocalMockServer(t *testing.T) {
	// Inline a small SSE server similar to e2e/mock_openai_responses.go
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || !strings.HasSuffix(r.URL.Path, "/responses") {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		w.Write([]byte("event: response.created\n" +
			"data: {\"type\":\"response.created\",\"sequence_number\":1,\"response\":{\"id\":\"resp_local\",\"created_at\":0,\"error\":{\"code\":\"\",\"message\":\"\"},\"incomplete_details\":{\"reason\":\"\"},\"instructions\":\"\",\"metadata\":{},\"model\":\"gpt-4o-mini\",\"object\":\"response\",\"output\":[],\"parallel_tool_calls\":false,\"temperature\":0,\"tool_choice\":\"auto\",\"tools\":[],\"top_p\":1}}\n\n"))
		if fl != nil { fl.Flush() }
		w.Write([]byte("event: response.completed\n" +
			"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_local\",\"created_at\":0,\"error\":{\"code\":\"\",\"message\":\"\"},\"incomplete_details\":{\"reason\":\"\"},\"instructions\":\"\",\"metadata\":{},\"model\":\"gpt-4o-mini\",\"object\":\"response\",\"output\":[],\"parallel_tool_calls\":false,\"temperature\":0,\"tool_choice\":\"auto\",\"tools\":[],\"top_p\":1,\"usage\":{\"input_tokens\":0,\"output_tokens\":0}}}\n\n"))
		if fl != nil { fl.Flush() }
	}))
	defer srv.Close()

	client := openai.NewClient(option.WithBaseURL(srv.URL))
	content := responses.ResponseInputMessageContentListParam{responses.ResponseInputContentParamOfInputText("hi")}
	item := responses.ResponseInputItemParamOfInputMessage(content, "user")
	params := responses.ResponseNewParams{Model: shared.ResponsesModel("gpt-4o-mini")}
	params.Input = responses.ResponseNewParamsInputUnion{OfInputItemList: []responses.ResponseInputItemUnionParam{item}}

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
		t.Fatalf("expected >=2 events from local mock, got %d", count)
	}
}
