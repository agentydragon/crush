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

// TestResponsesSSE_FunctionCall streams a minimal function_call sequence and asserts decode succeeds.
func TestResponsesSSE_FunctionCall(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/responses", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		flusher, _ := w.(http.Flusher)
		_, _ = io.ReadAll(r.Body)
		_ = r.Body.Close()

		// created
		w.Write([]byte("event: response.created\n" +
			"data: {\"type\":\"response.created\",\"sequence_number\":1,\"response\":{\"id\":\"resp_1\",\"created_at\":0,\"error\":{\"code\":\"\",\"message\":\"\"},\"incomplete_details\":{\"reason\":\"\"},\"instructions\":\"\",\"metadata\":{},\"model\":\"gpt-4o-mini\",\"object\":\"response\",\"output\":[],\"parallel_tool_calls\":false,\"temperature\":0,\"tool_choice\":\"auto\",\"tools\":[],\"top_p\":1}}\n\n"))
		if flusher != nil { flusher.Flush() }
		// output_item.added (function_call)
		w.Write([]byte("event: response.output_item.added\n" +
			"data: {\"type\":\"response.output_item.added\",\"sequence_number\":2,\"output_index\":0,\"item\":{\"type\":\"function_call\",\"id\":\"item_toolA\",\"name\":\"bash\",\"call_id\":\"fc_toolA\",\"arguments\":\"{\\\"command\\\":\\\"stepper\\\"}\",\"status\":\"in_progress\"}}\n\n"))
		if flusher != nil { flusher.Flush() }
		// function_call_arguments.delta
		w.Write([]byte("event: response.function_call_arguments.delta\n" +
			"data: {\"type\":\"response.function_call_arguments.delta\",\"sequence_number\":3,\"item_id\":\"item_toolA\",\"output_index\":0,\"delta\":\"{\\\"command\\\":\\\"stepper\\\"}\"}\n\n"))
		if flusher != nil { flusher.Flush() }
		// function_call_arguments.done
		w.Write([]byte("event: response.function_call_arguments.done\n" +
			"data: {\"type\":\"response.function_call_arguments.done\",\"sequence_number\":4,\"item_id\":\"item_toolA\",\"output_index\":0,\"arguments\":\"{\\\"command\\\":\\\"stepper\\\"}\"}\n\n"))
		if flusher != nil { flusher.Flush() }
		// completed
		w.Write([]byte("event: response.completed\n" +
			"data: {\"type\":\"response.completed\",\"sequence_number\":5,\"response\":{\"id\":\"resp_1\",\"created_at\":0,\"error\":{\"code\":\"\",\"message\":\"\"},\"incomplete_details\":{\"reason\":\"tool_use\"},\"instructions\":\"\",\"metadata\":{},\"model\":\"gpt-4o-mini\",\"object\":\"response\",\"output\":[{\"type\":\"function_call\",\"id\":\"item_toolA\",\"name\":\"bash\",\"call_id\":\"fc_toolA\",\"arguments\":\"{\\\"command\\\":\\\"stepper\\\"}\",\"status\":\"completed\"}],\"parallel_tool_calls\":false,\"temperature\":0,\"tool_choice\":\"auto\",\"tools\":[],\"top_p\":1,\"usage\":{\"input_tokens\":0,\"output_tokens\":0}}}\n\n"))
		if flusher != nil { flusher.Flush() }
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client := openai.NewClient(option.WithBaseURL(server.URL))
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
	if count < 5 { // created + added + delta + done + completed
		t.Fatalf("expected >=5 events, got %d", count)
	}
}
