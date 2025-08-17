package provider

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/charmbracelet/catwalk/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/config"
	llmtools "github.com/charmbracelet/crush/internal/llm/tools"
	"github.com/charmbracelet/crush/internal/message"
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

func (o *openaiResponsesClient) logWire(ctx context.Context, direction string, payload any, attempt int) {
	if !wireEnabled() {
		return
	}
	sessionID, messageID := llmtools.GetContextValues(ctx)
	getWireLogger().logJSONL(wireEntry{TS: wireNow(), Provider: string(o.providerOptions.config.ID), Direction: direction, Attempt: attempt, SessionID: sessionID, MessageID: messageID, Payload: payload})
}

func normalizeFunctionSchema(info llmtools.ToolInfo) map[string]any {
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

func buildResponsesTools(ts []llmtools.BaseTool) []responses.ToolUnionParam {
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
	// Track valid function call IDs seen from assistant messages so we only send
	// function_call_output entries that reference a known prior function call.
	validCallIDs := make(map[string]struct{})
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
			if rc.ID != "" {
				reas := responses.ResponseReasoningItemParam{ID: rc.ID, Type: "reasoning"}
				if rc.EncryptedContent != "" {
					reas.EncryptedContent = param.NewOpt(rc.EncryptedContent)
				}
				if rc.Summary != "" {
					reas.Summary = []responses.ResponseReasoningItemSummaryParam{{Text: rc.Summary, Type: "summary_text"}}
				}
				input = append(input, responses.ResponseInputItemUnionParam{OfReasoning: &reas})
			}
			if s := m.Content().String(); s != "" {
				input = append(input, responses.ResponseInputItemParamOfMessage(s, responses.EasyInputMessageRoleAssistant))
			}
			for _, tc := range m.ToolCalls() {
				// Record valid call IDs as we see them
				if tc.ID != "" {
					validCallIDs[tc.ID] = struct{}{}
				}
				input = append(input, responses.ResponseInputItemParamOfFunctionCall(tc.Input, tc.ID, tc.Name))
			}
		case message.Tool:
			for _, r := range m.ToolResults() {
				if r.ToolCallID == "" {
					// Malformed entry; ignore to avoid 400 from provider
					slog.Warn("dropping function_call_output with empty tool_call_id")
					continue
				}
				if _, ok := validCallIDs[r.ToolCallID]; !ok {
					// Guard: drop stray outputs that don't match any prior function_call
					slog.Warn("dropping stray function_call_output without matching function_call", "tool_call_id", r.ToolCallID)
					continue
				}
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
	if cfg := config.Get(); cfg != nil {
		reasoning := shared.ReasoningParam{}
		switch cfg.Models[config.SelectedModelTypeLarge].ReasoningEffort {
		case "low":
			reasoning.Effort = shared.ReasoningEffortLow
		case "medium":
			reasoning.Effort = shared.ReasoningEffortMedium
		case "high":
			reasoning.Effort = shared.ReasoningEffortHigh
		}
		if cfg.Options != nil {
			switch cfg.Options.EffectiveReasoningSummary() {
			case "auto":
				reasoning.Summary = shared.ReasoningSummaryAuto
			case "concise":
				reasoning.Summary = shared.ReasoningSummaryConcise
			case "detailed":
				reasoning.Summary = shared.ReasoningSummaryDetailed
			}
		}
		p.Reasoning = reasoning
	}
	return p
}

func mapFinishReason(resp responses.Response, hasToolCalls bool) message.FinishReason {
	finish := message.FinishReasonEndTurn
	if resp.Status == "incomplete" {
		reason := resp.IncompleteDetails.Reason
		if reason == "max_output_tokens" {
			return message.FinishReasonMaxTokens
		}
		if reason == "tool_use" {
			return message.FinishReasonToolUse
		}
	} else if hasToolCalls {
		return message.FinishReasonToolUse
	}
	return finish
}

func (o *openaiResponsesClient) send(ctx context.Context, messages []message.Message, tools []llmtools.BaseTool) (*ProviderResponse, error) {
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
		reasoning := make([]message.ReasoningSummaryContent, 0)
		for _, out := range req.Output {
			item := out
			switch v := item.AsAny().(type) {
			case responses.ResponseOutputMessage:
				for _, c := range v.Content {
					if t, ok := c.AsAny().(responses.ResponseOutputText); ok {
						content += t.Text
					}
				}
			case responses.ResponseFunctionToolCall:
				// IMPORTANT: Always use the function_call call_id (fc_…)
				// for ToolCall.ID because Responses requires function_call_output
				// to reference the call_id. Using item.ID here breaks the linkage
				// and yields 400 "No tool output found for function call fc_…".
				// Never send item.ID back to the API; only use it for local streaming bookkeeping.
				id := v.CallID
				if id == "" {
					id = item.ID
				}
				toolCalls = append(toolCalls, message.ToolCall{ID: id, Name: v.Name, Input: v.Arguments, Type: "function", Finished: true})
			case responses.ResponseReasoningItem:
				rs := message.ReasoningSummaryContent{ID: item.ID, EncryptedContent: v.EncryptedContent}
				for _, s := range v.Summary {
					if s.Type == "summary_text" {
						rs.Summary += s.Text
					}
				}
				reasoning = append(reasoning, rs)
			}
		}
		usage := TokenUsage{InputTokens: req.Usage.InputTokens, OutputTokens: req.Usage.OutputTokens}
		finish := mapFinishReason(*req, len(toolCalls) > 0)
		return &ProviderResponse{Content: content, ToolCalls: toolCalls, Usage: usage, FinishReason: finish, ReasoningSumm: reasoning}, nil
	}
}

func (o *openaiResponsesClient) stream(ctx context.Context, messages []message.Message, tools []llmtools.BaseTool) <-chan ProviderEvent {
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
			o.logWire(ctx, "request", params, attempts)
			currentContent := ""
			var toolCalls []message.ToolCall
			// Track started tool calls by stable id (call_id when available)
			seenToolCalls := make(map[string]bool)
			// Map streaming output item IDs to stable function call IDs (fc_…)
			itemToCallID := make(map[string]string)
			for stream.Next() {
				ev := stream.Current()
				o.logWire(ctx, "inbound", ev, attempts)
				switch ev.Type {
				case "response.output_text.delta":
					v := ev.AsResponseOutputTextDelta()
					slog.Info("provider delta", "item_id", v.ItemID, "content_index", v.ContentIndex, "output_index", v.OutputIndex, "delta", v.Delta)
					eventChan <- ProviderEvent{Type: EventContentDelta, Content: v.Delta}
					currentContent += v.Delta
				case "response.reasoning_summary_text.delta":
					v := ev.AsResponseReasoningSummaryTextDelta()
					eventChan <- ProviderEvent{Type: EventThinkingDelta, Thinking: v.Delta}
				case "response.output_text.done":
					// End of content text segment
					eventChan <- ProviderEvent{Type: EventContentStop}
				case "response.output_item.added":
					v := ev.AsResponseOutputItemAdded()
					slog.Info("provider item added", "item_id", v.Item.ID, "type", v.Item.Type)
					itemID := v.Item.ID
					switch x := v.Item.AsAny().(type) {
					case responses.ResponseFunctionToolCall:
						id := x.CallID
						if id == "" {
							id = itemID
						}
						itemToCallID[itemID] = id
						// Always emit a start with name using stable call_id when available
						eventChan <- ProviderEvent{Type: EventToolUseStart, ToolCall: &message.ToolCall{ID: id, Name: x.Name, Finished: false, Type: "function"}}
						seenToolCalls[id] = true
					}
				case "response.function_call_arguments.delta":
					v := ev.AsResponseFunctionCallArgumentsDelta()
					mapped, ok := itemToCallID[v.ItemID]
					if !ok || mapped == "" {
						// Assert: arguments delta must not precede output_item.added for this item
						eventChan <- ProviderEvent{Type: EventError, Error: fmt.Errorf("protocol violation: function_call_arguments.delta before output_item.added (item_id=%s)", v.ItemID)}
						close(eventChan)
						return
					}
					eventChan <- ProviderEvent{Type: EventToolUseDelta, ToolCall: &message.ToolCall{ID: mapped, Finished: false, Input: v.Delta}}
				case "response.function_call_arguments.done":
					v := ev.AsResponseFunctionCallArgumentsDone()
					mapped, ok := itemToCallID[v.ItemID]
					if !ok || mapped == "" {
						// Assert: arguments done must not precede output_item.added for this item
						eventChan <- ProviderEvent{Type: EventError, Error: fmt.Errorf("protocol violation: function_call_arguments.done before output_item.added (item_id=%s)", v.ItemID)}
						close(eventChan)
						return
					}
					eventChan <- ProviderEvent{Type: EventToolUseStop, ToolCall: &message.ToolCall{ID: mapped}}
				case "response.completed":
					v := ev.AsResponseCompleted()
					slog.Info("provider completed", "outputs", len(v.Response.Output), "status", v.Response.Status)
					// Collect any finalized tool calls from the completed response output
					toolCalls = nil
					finalContent := currentContent
					for _, out := range v.Response.Output {
						item := out
						switch x := item.AsAny().(type) {
						case responses.ResponseFunctionToolCall:
							// IMPORTANT: Use function_call call_id (fc_…) as ToolCall.ID.
							// The next turn will send function_call_output referencing this id.
							// Avoid item.ID in API payloads; it is for local stream tracking only.
							id := x.CallID
							if id == "" {
								id = item.ID
							}
							toolCalls = append(toolCalls, message.ToolCall{ID: id, Name: x.Name, Input: x.Arguments, Type: "function", Finished: true})
						case responses.ResponseOutputMessage:
							if finalContent == "" {
								for _, c := range x.Content {
									if t, ok := c.AsAny().(responses.ResponseOutputText); ok {
										finalContent += t.Text
									}
								}
							}
						}
					}
					usage := TokenUsage{InputTokens: v.Response.Usage.InputTokens, OutputTokens: v.Response.Usage.OutputTokens}
					finish := mapFinishReason(v.Response, len(toolCalls) > 0)
					// Emit stop for any tool items that started but did not send .done
					for id := range seenToolCalls {
						found := false
						for _, tc := range toolCalls {
							if tc.ID == id {
								found = true
								break
							}
						}
						if !found {
							slog.Warn("Forcibly ending tool call without explicit .done", "item_id", id)
							eventChan <- ProviderEvent{Type: EventToolUseStop, ToolCall: &message.ToolCall{ID: id}}
						}
					}
					eventChan <- ProviderEvent{Type: EventComplete, Response: &ProviderResponse{Content: finalContent, ToolCalls: toolCalls, Usage: usage, FinishReason: finish}}
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
		var apiErr *openai.Error
		if errors.As(err, &apiErr) && apiErr != nil {
			return false, 0, fmt.Errorf("maximum retry attempts reached for rate limit: %d retries; last error (%d %s): %s", maxRetries, apiErr.StatusCode, apiErr.Type, apiErr.Message)
		}
		return false, 0, fmt.Errorf("maximum retry attempts reached for rate limit: %d retries; last error: %v", maxRetries, err)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false, 0, err
	}
	var apiErr *openai.Error
	retryMs := 0
	retryAfterValues := []string{}
	if errors.As(err, &apiErr) && apiErr != nil {
		// Treat context_length_exceeded as non-retriable invalid request, not a quota/rate-limit error
		if isOpenAIContextLengthExceeded(apiErr) {
			return false, 0, wrapOpenAIContextLengthExceeded(apiErr, o.Model().ID)
		}
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
