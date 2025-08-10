package provider

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/charmbracelet/catwalk/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/llm/tools"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/google/uuid"
	"github.com/openai/openai-go"
	"github.com/openai/openai-go/packages/param"
	"github.com/openai/openai-go/responses"
	"github.com/openai/openai-go/shared"
)

type openaiResponsesClient struct {
	providerOptions providerClientOptions
	client          openai.Client
}

type OpenAIResponsesClient ProviderClient

func normalizeFunctionSchema(info tools.ToolInfo) map[string]any {
	raw := info.Parameters
	if raw == nil {
		raw = map[string]any{}
	}
	if _, hasProps := raw["properties"]; !hasProps {
		props := map[string]any{}
		for k, v := range raw {
			props[k] = v
		}
		raw = map[string]any{
			"type":       "object",
			"properties": props,
		}
	} else {
		if t, ok := raw["type"].(string); !ok || t == "" {
			raw["type"] = "object"
		}
	}
	if raw["type"] == "object" {
		if _, ok := raw["additionalProperties"]; !ok {
			raw["additionalProperties"] = true
		}
	}
	if len(info.Required) > 0 {
		if _, ok := raw["required"]; !ok {
			raw["required"] = info.Required
		}
	}
	return raw
}

func buildResponsesTools(ts []tools.BaseTool) []responses.ToolUnionParam {
	if len(ts) == 0 {
		return nil
	}
	out := make([]responses.ToolUnionParam, 0, len(ts))
	for _, t := range ts {
		info := t.Info()
		schema := normalizeFunctionSchema(info)
		out = append(out, responses.ToolParamOfFunction(info.Name, schema, false))
	}
	return out
}

func newOpenAIResponsesClient(opts providerClientOptions) OpenAIClient {
	return &openaiResponsesClient{
		providerOptions: opts,
		client:          createOpenAIClient(opts),
	}
}

func (o *openaiResponsesClient) Model() catwalk.Model {
	return o.providerOptions.model(o.providerOptions.modelType)
}

func buildResponsesInput(opts providerClientOptions, messages []message.Message) []responses.ResponseInputItemUnionParam {
	var input []responses.ResponseInputItemUnionParam
	if opts.systemPromptPrefix != "" {
		input = append(input, responses.ResponseInputItemParamOfMessage(opts.systemPromptPrefix, responses.EasyInputMessageRoleSystem))
	}
	input = append(input, responses.ResponseInputItemParamOfMessage(opts.systemMessage, responses.EasyInputMessageRoleSystem))
	for _, m := range messages {
		switch m.Role {
		case message.User:
			if s := m.Content().String(); s != "" {
				input = append(input, responses.ResponseInputItemParamOfMessage(s, responses.EasyInputMessageRoleUser))
			}
			for range m.BinaryContent() {
				content := responses.ResponseInputMessageContentListParam{
					responses.ResponseInputContentParamOfInputText(""),
					responses.ResponseInputContentParamOfInputImage(responses.ResponseInputImageDetailAuto),
				}
				input = append(input, responses.ResponseInputItemParamOfInputMessage(content, string(responses.EasyInputMessageRoleUser)))
			}
		case message.Assistant:
			rc := m.ReasoningSummary()
			if rc.Summary != "" {
				reas := responses.ResponseReasoningItemParam{ID: uuid.NewString(), Type: "reasoning"}
				reas.Summary = []responses.ResponseReasoningItemSummaryParam{{Text: rc.Summary, Type: "summary_text"}}
				input = append(input, responses.ResponseInputItemUnionParam{OfReasoning: &reas})
			}
			if s := m.Content().String(); s != "" {
				input = append(input, responses.ResponseInputItemParamOfMessage(s, responses.EasyInputMessageRoleAssistant))
			}
			for _, tc := range m.ToolCalls() {
				input = append(input, responses.ResponseInputItemParamOfFunctionCall(tc.Input, tc.ID, tc.Name))
			}
		case message.Tool:
			for _, r := range m.ToolResults() {
				input = append(input, responses.ResponseInputItemParamOfFunctionCallOutput(r.ToolCallID, r.Content))
			}
		}
	}
	return input
}

func newResponsesParams(modelID string, input []responses.ResponseInputItemUnionParam, maxTokens int64) responses.ResponseNewParams {
	p := responses.ResponseNewParams{Model: shared.ResponsesModel(modelID)}
	p.Input = responses.ResponseNewParamsInputUnion{OfInputItemList: input}
	p.Include = []responses.ResponseIncludable{responses.ResponseIncludableReasoningEncryptedContent}
	p.MaxOutputTokens = param.NewOpt(maxTokens)
	return p
}

func (o *openaiResponsesClient) send(ctx context.Context, messages []message.Message, tools []tools.BaseTool) (*ProviderResponse, error) {
	attempts := 0
	for {
		attempts++
		model := o.Model()
		maxTokens := calcMaxTokens(o.providerOptions, model)
		input := buildResponsesInput(o.providerOptions, messages)
		params := newResponsesParams(model.ID, input, maxTokens)
		params.Tools = buildResponsesTools(tools)
		if len(params.Tools) > 0 {
			params.ToolChoice = responses.ResponseNewParamsToolChoiceUnion{OfToolChoiceMode: param.NewOpt(responses.ToolChoiceOptionsAuto)}
		}
		req, err := o.client.Responses.New(ctx, params)
		if err != nil {
			retry, after, retryErr := o.shouldRetry(attempts, err)
			if retryErr != nil {
				return nil, retryErr
			}
			if retry {
				slog.Warn("Retrying due to rate limit", "attempt", attempts, "max_retries", maxRetries)
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(time.Duration(after) * time.Millisecond):
					continue
				}
			}
			return nil, retryErr
		}
		content := ""
		var toolCalls []message.ToolCall
		for _, out := range req.Output {
			switch v := out.AsAny().(type) {
			case responses.ResponseOutputMessage:
				for _, c := range v.Content {
					if t, ok := c.AsAny().(responses.ResponseOutputText); ok {
						content += t.Text
					}
				}
			case responses.ResponseFunctionToolCall:
				toolCalls = append(toolCalls, message.ToolCall{ID: v.CallID, Name: v.Name, Input: v.Arguments, Type: "function", Finished: true})
			case responses.ResponseReasoningItem:
				if v.EncryptedContent != "" {
					// persist encrypted reasoning for carry-forward
				}
			}
		}
		usage := TokenUsage{InputTokens: req.Usage.InputTokens, OutputTokens: req.Usage.OutputTokens}
		finish := message.FinishReasonEndTurn
		if len(toolCalls) > 0 {
			finish = message.FinishReasonToolUse
		}
		return &ProviderResponse{Content: content, ToolCalls: toolCalls, Usage: usage, FinishReason: finish}, nil
	}
}

func (o *openaiResponsesClient) stream(ctx context.Context, messages []message.Message, tools []tools.BaseTool) <-chan ProviderEvent {
	eventChan := make(chan ProviderEvent)
	go func() {
		attempts := 0
		for {
			attempts++
			model := o.Model()
			maxTokens := calcMaxTokens(o.providerOptions, model)
			input := buildResponsesInput(o.providerOptions, messages)
			params := newResponsesParams(model.ID, input, maxTokens)
			params.Tools = buildResponsesTools(tools)
			if len(params.Tools) > 0 {
				params.ToolChoice = responses.ResponseNewParamsToolChoiceUnion{OfToolChoiceMode: param.NewOpt(responses.ToolChoiceOptionsAuto)}
			}
			stream := o.client.Responses.NewStreaming(ctx, params)
			currentContent := ""
			var toolCalls []message.ToolCall
			for stream.Next() {
				ev := stream.Current()
				switch ev.Type {
				case "response.output_text.delta":
					v := ev.AsResponseOutputTextDelta()
					eventChan <- ProviderEvent{Type: EventContentDelta, Content: v.Delta}
					currentContent += v.Delta
				case "response.reasoning_summary_text.delta":
					v := ev.AsResponseReasoningSummaryTextDelta()
					eventChan <- ProviderEvent{Type: EventThinkingDelta, Thinking: v.Delta}
				case "response.function_call_arguments.delta":
					v := ev.AsResponseFunctionCallArgumentsDelta()
					eventChan <- ProviderEvent{Type: EventToolUseDelta, ToolCall: &message.ToolCall{ID: v.ItemID, Finished: false, Input: v.Delta}}
				case "response.function_call_arguments.done":
					v := ev.AsResponseFunctionCallArgumentsDone()
					eventChan <- ProviderEvent{Type: EventToolUseStop, ToolCall: &message.ToolCall{ID: v.ItemID}}
				case "response.completed":
					v := ev.AsResponseCompleted()
					// Collect any finalized tool calls from the completed response output
					toolCalls = nil
					for _, out := range v.Response.Output {
						switch x := out.AsAny().(type) {
						case responses.ResponseFunctionToolCall:
							toolCalls = append(toolCalls, message.ToolCall{ID: x.CallID, Name: x.Name, Input: x.Arguments, Type: "function", Finished: true})
						}
					}
					usage := TokenUsage{InputTokens: v.Response.Usage.InputTokens, OutputTokens: v.Response.Usage.OutputTokens}
					finish := message.FinishReasonEndTurn
					if len(toolCalls) > 0 {
						finish = message.FinishReasonToolUse
					}
					eventChan <- ProviderEvent{Type: EventComplete, Response: &ProviderResponse{Content: currentContent, ToolCalls: toolCalls, Usage: usage, FinishReason: finish}}
					close(eventChan)
					return
				}
			}
			err := stream.Err()
			retry, after, retryErr := o.shouldRetry(attempts, err)
			if !retry || retryErr != nil {
				eventChan <- ProviderEvent{Type: EventError, Error: retryErr}
				close(eventChan)
				return
			}
			select {
			case <-ctx.Done():
				if ctx.Err() != nil {
					eventChan <- ProviderEvent{Type: EventError, Error: ctx.Err()}
				}
				close(eventChan)
				return
			case <-time.After(time.Duration(after) * time.Millisecond):
			}
		}
	}()
	return eventChan
}

func (o *openaiResponsesClient) shouldRetry(attempts int, err error) (bool, int64, error) {
	if err == nil {
		return false, 0, fmt.Errorf("stream ended without completion event")
	}
	if attempts > maxRetries {
		return false, 0, fmt.Errorf("maximum retry attempts reached for rate limit: %d retries", maxRetries)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false, 0, err
	}
	var apiErr *openai.Error
	retryMs := 0
	retryAfterValues := []string{}
	if errors.As(err, &apiErr) && apiErr != nil {
		if apiErr.StatusCode == 401 {
			o.providerOptions.apiKey, err = config.Get().Resolve(o.providerOptions.config.APIKey)
			if err != nil {
				return false, 0, fmt.Errorf("failed to resolve API key: %w", err)
			}
			o.client = createOpenAIClient(o.providerOptions)
			return true, 0, nil
		}
		if apiErr.StatusCode != 429 && apiErr.StatusCode != 500 && apiErr.StatusCode != 503 {
			return false, 0, err
		}
		if apiErr.Response != nil {
			retryAfterValues = apiErr.Response.Header.Values("Retry-After")
		}
	}
	if apiErr != nil {
		slog.Warn("OpenAI API error", "status_code", apiErr.StatusCode, "message", apiErr.Message, "type", apiErr.Type)
		if len(retryAfterValues) > 0 {
			slog.Warn("Retry-After header", "values", retryAfterValues)
		}
	} else {
		slog.Error("OpenAI API error", "error", err.Error(), "attempt", attempts, "max_retries", maxRetries)
	}
	backoffMs := 2000 * (1 << (attempts - 1))
	jitterMs := int(float64(backoffMs) * 0.2)
	retryMs = backoffMs + jitterMs
	if len(retryAfterValues) > 0 {
		if _, err := fmt.Sscanf(retryAfterValues[0], "%d", &retryMs); err == nil {
			retryMs = retryMs * 1000
		}
	}
	return true, int64(retryMs), nil
}
