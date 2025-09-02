package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
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

var responsesStreams atomic.Int32

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

func buildResponsesInput(opts providerClientOptions, messages []message.Message, supportsReasoning bool) []responses.ResponseInputItemUnionParam {
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
			// Only forward prior reasoning items to Responses models that support reasoning.
			if supportsReasoning {
				rc := m.EncryptedReasoning()
				if rc.ID != "" && rc.EncryptedContent != "" {
					reas := responses.ResponseReasoningItemParam{ID: rc.ID, Type: "reasoning"}
					reas.EncryptedContent = param.NewOpt(rc.EncryptedContent)
					input = append(input, responses.ResponseInputItemUnionParam{OfReasoning: &reas})
				}
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

func newResponsesParams(modelID string, input []responses.ResponseInputItemUnionParam, maxTokens int64, supportsReasoning bool) responses.ResponseNewParams {
	p := responses.ResponseNewParams{Model: shared.ResponsesModel(modelID)}
	p.Input = responses.ResponseNewParamsInputUnion{OfInputItemList: input}
	p.MaxOutputTokens = param.NewOpt(maxTokens)
	// Only request reasoning-related fields for models that support reasoning.
	if supportsReasoning {
		p.Include = []responses.ResponseIncludable{responses.ResponseIncludableReasoningEncryptedContent}
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
		supportsReasoning := model.CanReason
		maxTokens := calcMaxTokens(o.providerOptions, model)
		input := buildResponsesInput(o.providerOptions, messages, supportsReasoning)
		params := newResponsesParams(model.ID, input, maxTokens, supportsReasoning)
		params.Tools = buildResponsesTools(tools)
		if len(params.Tools) > 0 {
			params.ToolChoice = responses.ResponseNewParamsToolChoiceUnion{OfToolChoiceMode: param.NewOpt(responses.ToolChoiceOptionsAuto)}
		}
		resp, err := o.client.Responses.New(ctx, params)
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
		reasoningSumm := make([]message.ReasoningSummaryContent, 0)
		reasoningEnc := make([]message.ReasoningEncryptedContent, 0)
		for _, out := range resp.Output {
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
				reasoningEnc = append(reasoningEnc, message.ReasoningEncryptedContent{ID: item.ID, EncryptedContent: v.EncryptedContent})
				if len(v.Summary) > 0 {
					var rs message.ReasoningSummaryContent
					rs.ID = item.ID
					for _, s := range v.Summary {
						if s.Type == "summary_text" {
							rs.Summary += s.Text
						}
					}
					if rs.Summary != "" {
						reasoningSumm = append(reasoningSumm, rs)
					}
				}
			}
		}
		// TODO(mpokorny): When openai-go exposes Responses Usage.InputTokenDetails{CachedTokens, CacheCreationTokens},
		// populate cached/created here. Current SDK version in this repo does not expose them.
		usage := mapResponsesUsage(resp.Usage)
		finish := mapFinishReason(*resp, len(toolCalls) > 0)
		return &ProviderResponse{Content: content, ToolCalls: toolCalls, Usage: usage, FinishReason: finish, ReasoningSumm: reasoningSumm, ReasoningEnc: reasoningEnc}, nil
	}
}

// Centralized mapping for Responses API usage
func mapResponsesUsage(u responses.ResponseUsage) TokenUsage {
	// SDK v1.12.0 exposes InputTokensDetails.CachedTokens; CacheCreationTokens not present.
	cached := u.InputTokensDetails.CachedTokens
	// Cache creation tokens are not exposed in SDK v1.12.0.
	// Consequences:
	// - Window: session.PromptTokens (input + cache_create) undercounts by creation amount;
	//   auto-compact may trigger slightly late when creation > 0.
	// - Cost: TrackUsage() computes cost using CacheCreationTokens; until exposed here,
	//   cost will underreport cache-create input spend for Responses.
	// Mitigation: treat as 0; once SDK surfaces InputTokensDetails.CacheCreationTokens, wire it.
	created := int64(0) // TODO(mpokorny): wire CacheCreationTokens when SDK exposes it
	return TokenUsage{
		InputTokens:         u.InputTokens - cached,
		OutputTokens:        u.OutputTokens,
		CacheReadTokens:     cached,
		CacheCreationTokens: created,
	}
}

func (o *openaiResponsesClient) stream(ctx context.Context, messages []message.Message, tools []llmtools.BaseTool) <-chan ProviderEvent {
	eventChan := make(chan ProviderEvent)
	go func() {
		attempts := 0
		for {
			attempts++
			model := o.Model()
			supportsReasoning := model.CanReason
			maxTokens := calcMaxTokens(o.providerOptions, model)
			input := buildResponsesInput(o.providerOptions, messages, supportsReasoning)
			params := newResponsesParams(model.ID, input, maxTokens, supportsReasoning)
			params.Tools = buildResponsesTools(tools)
			if len(params.Tools) > 0 {
				params.ToolChoice = responses.ResponseNewParamsToolChoiceUnion{OfToolChoiceMode: param.NewOpt(responses.ToolChoiceOptionsAuto)}
			}
			n := responsesStreams.Add(1)
			slog.Info("provider.responses.new_stream", "count", n)
			stream := o.client.Responses.NewStreaming(ctx, params)
			o.logWire(ctx, "request", params, attempts)
			o.logWire(ctx, "stream_start", map[string]any{"model": model.ID}, attempts)
			slog.Info("provider.stream.start", "model", model.ID)
			currentContent := ""
			var toolCalls []message.ToolCall
			// Track started tool calls by stable id (call_id when available)
			seenToolCalls := make(map[string]bool)
			// Map streaming output item IDs to stable function call IDs (fc_…)
			itemToCallID := make(map[string]string)
			sawEvent := false
			for stream.Next() {
				sawEvent = true
				ev := stream.Current()
				o.logWire(ctx, "inbound", ev, attempts)
				slog.Info("provider.event", "type", ev.Type)
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
					slog.Info("provider item added", "item_id", v.Item.ID, "type", v.Item.Type, "output_index", v.OutputIndex, "seq", v.SequenceNumber)
					itemID := v.Item.ID
					// Deep log for function_call content if present; also dump raw JSON for diagnosis
					if fc, ok := v.Item.AsAny().(responses.ResponseFunctionToolCall); ok {
						slog.Info("provider item added:function_call", "item_id", itemID, "call_id", fc.CallID, "name", fc.Name, "args_len", len(fc.Arguments), "status", fc.Status)
						slog.Info("provider item added:function_call.raw", "raw", fc.RawJSON())
					} else {
						if any := v.Item.AsAny(); any != nil {
							if b, err := json.Marshal(any); err == nil {
								slog.Info("provider item added:raw_other", "type", v.Item.Type, "raw", string(b))
							}
						}
					}
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
					slog.Info("provider fcall.args.delta", "item_id", v.ItemID, "output_index", v.OutputIndex, "delta_len", len(v.Delta))
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
					slog.Info("provider fcall.args.done", "item_id", v.ItemID, "output_index", v.OutputIndex, "args_len", len(v.Arguments))
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
					for i, out := range v.Response.Output {
						if fc, ok := out.AsAny().(responses.ResponseFunctionToolCall); ok {
							slog.Info("provider completed:function_call", "idx", i, "item_id", out.ID, "call_id", fc.CallID, "name", fc.Name, "args_len", len(fc.Arguments), "status", fc.Status)
						}
					}
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
					// TODO(mpokorny): When openai-go exposes Responses Usage.InputTokenDetails{CachedTokens, CacheCreationTokens},
					// populate cached/created here. Current SDK version in this repo does not expose them.
					usage := mapResponsesUsage(v.Response.Usage)
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
			if !sawEvent {
				slog.Warn("provider.stream.no_events")
			}
			err := stream.Err()
			slog.Info("provider.stream.end", "err", err)
			retry, after, retryErr := o.shouldRetry(attempts, err)
			if !retry || retryErr != nil {
				eventChan <- ProviderEvent{Type: EventError, Error: retryErr}
				close(eventChan)
				return
			}
			// Surface retry to UI via warning event so callers (e.g., summarization dialog) can display it
			eventChan <- ProviderEvent{Type: EventWarning, Content: fmt.Sprintf("Provider retry: %v; waiting %dms (attempt %d/%d)", err, after, attempts, maxRetries)}
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
		if apiErr.StatusCode != 429 && apiErr.StatusCode != 500 && apiErr.StatusCode != 502 && apiErr.StatusCode != 503 && apiErr.StatusCode != 504 && apiErr.StatusCode != 408 {
			return false, 0, err
		}
		if apiErr.Response != nil {
			retryAfterValues = apiErr.Response.Header.Values("Retry-After")
		}
		goto doRetry
	}
	if apiErr != nil {
		slog.Warn("OpenAI API error", "status_code", apiErr.StatusCode, "message", apiErr.Message, "type", apiErr.Type)
		if len(retryAfterValues) > 0 {
			slog.Warn("Retry-After header", "values", retryAfterValues)
		}
	} else {
		slog.Error("OpenAI API error", "error", err.Error(), "attempt", attempts, "max_retries", maxRetries)
	}

	// Fallback: parse JSON from error text to short-circuit on invalid_request/context_length_exceeded
	if typ, code, _, ok := parseOpenAIJSONError(err.Error()); ok {
		if typ == "invalid_request_error" || typ == "invalid_request" {
			if code == "context_length_exceeded" {
				return false, 0, fmt.Errorf("context window exceeded for model %s", o.Model().ID)
			}
			return false, 0, err
		}
		if typ == "insufficient_quota" || code == "insufficient_quota" || code == "billing_not_active" {
			return false, 0, err
		}
	}

	// TODO(mpokorny): Replace with exponential backoff + jitter; honor Retry-After precisely
	doRetry:
	retryMs = 2000 * (1 << (attempts - 1))
	if len(retryAfterValues) > 0 {
		if _, err := fmt.Sscanf(retryAfterValues[0], "%d", &retryMs); err == nil {
			retryMs = retryMs * 1000
		}
	}
	return true, int64(retryMs), nil
}
