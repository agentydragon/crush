package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"time"

	llmtools "github.com/charmbracelet/crush/internal/llm/tools"

	"github.com/charmbracelet/catwalk/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/llm/tools"
	"github.com/charmbracelet/crush/internal/logging"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/packages/param"
	"github.com/openai/openai-go/shared"
)

type openaiClient struct {
	providerOptions providerClientOptions
	client          openai.Client
}

type OpenAIClient ProviderClient

func createOpenAIClient(opts providerClientOptions) openai.Client {
	openaiClientOptions := []option.RequestOption{}
	if opts.apiKey != "" {
		openaiClientOptions = append(openaiClientOptions, option.WithAPIKey(opts.apiKey))
	}
	if opts.baseURL != "" {
		resolvedBaseURL, err := config.Get().Resolve(opts.baseURL)
		if err == nil {
			openaiClientOptions = append(openaiClientOptions, option.WithBaseURL(resolvedBaseURL))
		}
	}

	// Always use our HTTP client with request/response logging; log verbosity is controlled by slog level.
	httpClient := logging.NewHTTPClient()
	openaiClientOptions = append(openaiClientOptions, option.WithHTTPClient(httpClient))

	for key, value := range opts.extraHeaders {
		openaiClientOptions = append(openaiClientOptions, option.WithHeader(key, value))
	}

	for extraKey, extraValue := range opts.extraBody {
		openaiClientOptions = append(openaiClientOptions, option.WithJSONSet(extraKey, extraValue))
	}

	return openai.NewClient(openaiClientOptions...)
}

// sanitizeChatHistory ensures tool messages only appear immediately after an assistant
// message that contains tool_calls, and only for the matching tool_call IDs. It does not
// persist any rewrites; it only adjusts the outbound sequence for Chat API requirements.
func sanitizeChatHistory(msgs []message.Message) []message.Message {
	sanitized := make([]message.Message, 0, len(msgs))
	// expectedIDs tracks tool_call IDs from the most recent assistant tool_calls
	expectedIDs := map[string]bool{}
	remaining := 0
	expecting := false
	reset := func() {
		for k := range expectedIDs {
			delete(expectedIDs, k)
		}
		remaining = 0
		expecting = false
	}

	for idx, m := range msgs {
		switch m.Role {
		case message.Assistant:
			// Append assistant as-is
			sanitized = append(sanitized, m)
			calls := m.ToolCalls()
			if len(calls) > 0 {
				reset()
				for _, c := range calls {
					expectedIDs[c.ID] = true
				}
				remaining = len(calls)
				expecting = true
			} else {
				reset()
			}
		case message.Tool:
			if !expecting || remaining == 0 {
				slog.Debug("sanitizeChatHistory: dropping orphaned tool message", "index", idx)
				continue
			}
			// Filter tool results to only those matching still-expected IDs; one per ID
			used := map[string]bool{}
			filtered := []message.ToolResult{}
			for _, tr := range m.ToolResults() {
				if expectedIDs[tr.ToolCallID] && !used[tr.ToolCallID] {
					filtered = append(filtered, tr)
					used[tr.ToolCallID] = true
					delete(expectedIDs, tr.ToolCallID)
					remaining--
				} else {
					// drop mismatched/duplicate result
				}
			}
			if len(filtered) == 0 {
				slog.Debug("sanitizeChatHistory: dropped tool message with no matching tool_call_id", "index", idx)
				continue
			}
			mm := m
			mm.Parts = make([]message.ContentPart, 0, len(filtered))
			for _, fr := range filtered {
				mm.Parts = append(mm.Parts, fr)
			}
			sanitized = append(sanitized, mm)
			if remaining == 0 {
				reset()
			}
		default:
			// Any other role breaks the contiguity requirement
			reset()
			sanitized = append(sanitized, m)
		}
	}
	return sanitized
}

func (o *openaiClient) convertMessages(messages []message.Message) (openaiMessages []openai.ChatCompletionMessageParamUnion) {
	isAnthropicModel := o.providerOptions.config.ID == string(catwalk.InferenceProviderOpenRouter) && strings.HasPrefix(o.Model().ID, "anthropic/")
	// Add system message first
	systemMessage := o.providerOptions.systemMessage
	if o.providerOptions.systemPromptPrefix != "" {
		systemMessage = o.providerOptions.systemPromptPrefix + "\n" + systemMessage
	}

	system := openai.SystemMessage(systemMessage)
	if isAnthropicModel && !o.providerOptions.disableCache {
		systemTextBlock := openai.ChatCompletionContentPartTextParam{Text: systemMessage}
		systemTextBlock.SetExtraFields(
			map[string]any{
				"cache_control": map[string]string{
					"type": "ephemeral",
				},
			},
		)
		var content []openai.ChatCompletionContentPartTextParam
		content = append(content, systemTextBlock)
		system = openai.SystemMessage(content)
	}
	openaiMessages = append(openaiMessages, system)

	for i, msg := range messages {
		cache := false
		if i > len(messages)-3 {
			cache = true
		}
		switch msg.Role {
		case message.User:
			var content []openai.ChatCompletionContentPartUnionParam

			textBlock := openai.ChatCompletionContentPartTextParam{Text: msg.Content().String()}
			content = append(content, openai.ChatCompletionContentPartUnionParam{OfText: &textBlock})
			hasBinaryContent := false
			for _, binaryContent := range msg.BinaryContent() {
				hasBinaryContent = true
				imageURL := openai.ChatCompletionContentPartImageImageURLParam{URL: binaryContent.String(catwalk.InferenceProviderOpenAI)}
				imageBlock := openai.ChatCompletionContentPartImageParam{ImageURL: imageURL}

				content = append(content, openai.ChatCompletionContentPartUnionParam{OfImageURL: &imageBlock})
			}
			if cache && !o.providerOptions.disableCache && isAnthropicModel {
				textBlock.SetExtraFields(map[string]any{
					"cache_control": map[string]string{
						"type": "ephemeral",
					},
				})
			}
			if hasBinaryContent || (isAnthropicModel && !o.providerOptions.disableCache) {
				openaiMessages = append(openaiMessages, openai.UserMessage(content))
			} else {
				openaiMessages = append(openaiMessages, openai.UserMessage(msg.Content().String()))
			}

		case message.Assistant:
			assistantMsg := openai.ChatCompletionAssistantMessageParam{
				Role: "assistant",
			}

			hasContent := false
			if msg.Content().String() != "" {
				hasContent = true
				textBlock := openai.ChatCompletionContentPartTextParam{Text: msg.Content().String()}
				if cache && !o.providerOptions.disableCache && isAnthropicModel {
					textBlock.SetExtraFields(map[string]any{
						"cache_control": map[string]string{
							"type": "ephemeral",
						},
					})
				}
				assistantMsg.Content = openai.ChatCompletionAssistantMessageParamContentUnion{
					OfArrayOfContentParts: []openai.ChatCompletionAssistantMessageParamContentArrayOfContentPartUnion{
						{
							OfText: &textBlock,
						},
					},
				}
				if !isAnthropicModel {
					assistantMsg.Content = openai.ChatCompletionAssistantMessageParamContentUnion{
						OfString: param.NewOpt(msg.Content().String()),
					}
				}
			}

			if len(msg.ToolCalls()) > 0 {
				hasContent = true
				assistantMsg.ToolCalls = make([]openai.ChatCompletionMessageToolCallParam, len(msg.ToolCalls()))
				for i, call := range msg.ToolCalls() {
					assistantMsg.ToolCalls[i] = openai.ChatCompletionMessageToolCallParam{
						ID:   call.ID,
						Type: "function",
						Function: openai.ChatCompletionMessageToolCallFunctionParam{
							Name:      call.Name,
							Arguments: call.Input,
						},
					}
				}
			}
			if !hasContent {
				slog.Warn("There is a message without content, investigate, this should not happen")
				continue
			}

			openaiMessages = append(openaiMessages, openai.ChatCompletionMessageParamUnion{
				OfAssistant: &assistantMsg,
			})

		case message.Tool:
			for _, result := range msg.ToolResults() {
				openaiMessages = append(openaiMessages,
					openai.ToolMessage(result.Content, result.ToolCallID),
				)
			}
		}
	}

	return
}

func (o *openaiClient) convertTools(tools []tools.BaseTool) []openai.ChatCompletionToolParam {
	openaiTools := make([]openai.ChatCompletionToolParam, len(tools))

	for i, tool := range tools {
		info := tool.Info()
		openaiTools[i] = openai.ChatCompletionToolParam{
			Function: openai.FunctionDefinitionParam{
				Name:        info.Name,
				Description: openai.String(info.Description),
				Parameters: openai.FunctionParameters{
					"type":       "object",
					"properties": info.Parameters,
					"required":   info.Required,
				},
			},
		}
	}

	return openaiTools
}

func (o *openaiClient) finishReason(reason string) message.FinishReason {
	switch reason {
	case "stop":
		return message.FinishReasonEndTurn
	case "length":
		return message.FinishReasonMaxTokens
	case "tool_calls":
		return message.FinishReasonToolUse
	default:
		return message.FinishReasonUnknown
	}
}

func (o *openaiClient) preparedParams(messages []openai.ChatCompletionMessageParamUnion, tools []openai.ChatCompletionToolParam) openai.ChatCompletionNewParams {
	model := o.providerOptions.model(o.providerOptions.modelType)
	cfg := config.Get()

	modelConfig := cfg.Models[config.SelectedModelTypeLarge]
	if o.providerOptions.modelType == config.SelectedModelTypeSmall {
		modelConfig = cfg.Models[config.SelectedModelTypeSmall]
	}

	reasoningEffort := modelConfig.ReasoningEffort

	params := openai.ChatCompletionNewParams{
		Model:    openai.ChatModel(model.ID),
		Messages: messages,
		Tools:    tools,
	}

	maxTokens := model.DefaultMaxTokens
	if modelConfig.MaxTokens > 0 {
		maxTokens = modelConfig.MaxTokens
	}

	// Override max tokens if set in provider options
	if o.providerOptions.maxTokens > 0 {
		maxTokens = o.providerOptions.maxTokens
	}
	if model.CanReason {
		params.MaxCompletionTokens = openai.Int(maxTokens)
		switch reasoningEffort {
		case "low":
			params.ReasoningEffort = shared.ReasoningEffortLow
		case "medium":
			params.ReasoningEffort = shared.ReasoningEffortMedium
		case "high":
			params.ReasoningEffort = shared.ReasoningEffortHigh
		case "minimal":
			params.ReasoningEffort = shared.ReasoningEffort("minimal")
		default:
			params.ReasoningEffort = shared.ReasoningEffort(reasoningEffort)
		}
	} else {
		params.MaxTokens = openai.Int(maxTokens)
	}

	return params
}

func (o *openaiClient) send(ctx context.Context, messages []message.Message, tools []tools.BaseTool) (response *ProviderResponse, err error) {
	params := o.preparedParams(o.convertMessages(sanitizeChatHistory(messages)), o.convertTools(tools))
	attempts := 0
	for {
		attempts++
		openaiResponse, err := o.client.Chat.Completions.New(
			ctx,
			params,
		)
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
		if len(openaiResponse.Choices) == 0 {
			return nil, fmt.Errorf("received empty response from OpenAI API - check endpoint configuration")
		}
		content := ""
		if openaiResponse.Choices[0].Message.Content != "" {
			content = openaiResponse.Choices[0].Message.Content
		}
		toolCalls := o.toolCalls(*openaiResponse)
		finishReason := o.finishReason(string(openaiResponse.Choices[0].FinishReason))
		if len(toolCalls) > 0 {
			finishReason = message.FinishReasonToolUse
		}
		return &ProviderResponse{Content: content, ToolCalls: toolCalls, Usage: o.usage(*openaiResponse), FinishReason: finishReason}, nil
	}
}

func (o *openaiClient) stream(ctx context.Context, messages []message.Message, tools []tools.BaseTool) <-chan ProviderEvent {
	params := o.preparedParams(o.convertMessages(sanitizeChatHistory(messages)), o.convertTools(tools))
	params.StreamOptions = openai.ChatCompletionStreamOptionsParam{
		IncludeUsage: openai.Bool(true),
	}
	attempts := 0
	eventChan := make(chan ProviderEvent)
	go func() {
		for {
			attempts++
			if len(params.Tools) == 0 {
				params.Tools = nil
			}
			openaiStream := o.client.Chat.Completions.NewStreaming(
				ctx,
				params,
			)
			if wireEnabled() {
				sessionID, messageID := llmtools.GetContextValues(ctx)
				getWireLogger().logJSONL(wireEntry{TS: wireNow(), Provider: string(o.providerOptions.config.ID), Model: o.Model().ID, Direction: "request", EventType: "chat.completions.new_streaming", Attempt: attempts, SessionID: sessionID, MessageID: messageID, Payload: params})
			}
			acc := openai.ChatCompletionAccumulator{}
			currentContent := ""
			toolCalls := make([]message.ToolCall, 0)
			var msgToolCalls []openai.ChatCompletionMessageToolCall
			bufferedArgs := map[int]string{}
			startedByIndex := map[int]bool{}
			idByIndex := map[int]string{}
			nameByIndex := map[int]string{}
			for openaiStream.Next() {
				chunk := openaiStream.Current()
				if wireEnabled() {
					sessionID, messageID := llmtools.GetContextValues(ctx)
					getWireLogger().logJSONL(wireEntry{TS: wireNow(), Provider: string(o.providerOptions.config.ID), Model: o.Model().ID, Direction: "inbound", EventType: "chat.delta", Attempt: attempts, SessionID: sessionID, MessageID: messageID, Payload: chunk})
				}
				if len(chunk.Choices) > 0 && len(chunk.Choices[0].Delta.ToolCalls) > 0 && chunk.Choices[0].Delta.ToolCalls[0].Index == -1 {
					chunk.Choices[0].Delta.ToolCalls[0].Index = 0
				}
				acc.AddChunk(chunk)
				for i, choice := range chunk.Choices {
					reasoning, ok := choice.Delta.JSON.ExtraFields["reasoning"]
					if ok && reasoning.Raw() != "" {
						reasoningStr := ""
						json.Unmarshal([]byte(reasoning.Raw()), &reasoningStr)
						if reasoningStr != "" {
							eventChan <- ProviderEvent{Type: EventThinkingDelta, Thinking: reasoningStr}
						}
					}
					if choice.Delta.Content != "" {
						eventChan <- ProviderEvent{Type: EventContentDelta, Content: choice.Delta.Content}
						currentContent += choice.Delta.Content
					} else if len(choice.Delta.ToolCalls) > 0 {
						tc := choice.Delta.ToolCalls[0]
						idx := int(tc.Index)
						if tc.Function.Name != "" {
							nameByIndex[idx] = tc.Function.Name
						}
						if tc.Function.Arguments != "" {
							bufferedArgs[idx] = bufferedArgs[idx] + tc.Function.Arguments
						}
						if tc.ID != "" && idByIndex[idx] == "" {
							idByIndex[idx] = tc.ID
							if !startedByIndex[idx] {
								eventChan <- ProviderEvent{Type: EventToolUseStart, ToolCall: &message.ToolCall{ID: tc.ID, Name: nameByIndex[idx], Finished: false}}
								startedByIndex[idx] = true
							}
							if bufferedArgs[idx] != "" {
								eventChan <- ProviderEvent{Type: EventToolUseDelta, ToolCall: &message.ToolCall{ID: tc.ID, Finished: false, Input: bufferedArgs[idx]}}
								msgToolCalls = append(msgToolCalls, openai.ChatCompletionMessageToolCall{ID: tc.ID, Type: "function", Function: openai.ChatCompletionMessageToolCallFunction{Name: nameByIndex[idx], Arguments: bufferedArgs[idx]}})
								bufferedArgs[idx] = ""
							} else {
								msgToolCalls = append(msgToolCalls, openai.ChatCompletionMessageToolCall{ID: tc.ID, Type: "function", Function: openai.ChatCompletionMessageToolCallFunction{Name: nameByIndex[idx], Arguments: ""}})
							}
						} else if startedByIndex[idx] && idByIndex[idx] != "" && tc.Function.Arguments != "" {
							eventChan <- ProviderEvent{Type: EventToolUseDelta, ToolCall: &message.ToolCall{ID: idByIndex[idx], Finished: false, Input: tc.Function.Arguments}}
							if idx < len(msgToolCalls) {
								msgToolCalls[idx].Function.Arguments += tc.Function.Arguments
							}
						}
					}
					acc.Choices[i].Message.ToolCalls = slices.Clone(msgToolCalls)
				}
			}
			err := openaiStream.Err()
			if wireEnabled() {
				sessionID, messageID := llmtools.GetContextValues(ctx)
				errStr := ""
				if err != nil {
					errStr = err.Error()
				}
				getWireLogger().logJSONL(wireEntry{TS: wireNow(), Provider: string(o.providerOptions.config.ID), Model: o.Model().ID, Direction: "complete", EventType: "stream_end", Attempt: attempts, SessionID: sessionID, MessageID: messageID, Error: errStr})
			}
			if err == nil || errors.Is(err, io.EOF) {
				if len(acc.Choices) == 0 {
					eventChan <- ProviderEvent{Type: EventError, Error: fmt.Errorf("received empty streaming response from OpenAI API - check endpoint configuration")}
					return
				}
				resultFinishReason := acc.Choices[0].FinishReason
				if resultFinishReason == "" {
					resultFinishReason = "stop"
				}
				finishReason := o.finishReason(resultFinishReason)
				if len(acc.Choices[0].Message.ToolCalls) > 0 {
					toolCalls = append(toolCalls, o.toolCalls(acc.ChatCompletion)...)
				}
				if len(toolCalls) > 0 {
					finishReason = message.FinishReasonToolUse
				}
				for idx, started := range startedByIndex {
					if started {
						id := idByIndex[idx]
						if id != "" {
							eventChan <- ProviderEvent{Type: EventToolUseStop, ToolCall: &message.ToolCall{ID: id}}
						}
					}
				}
				eventChan <- ProviderEvent{Type: EventComplete, Response: &ProviderResponse{Content: currentContent, ToolCalls: toolCalls, Usage: o.usage(acc.ChatCompletion), FinishReason: finishReason}}
				close(eventChan)
				return
			}
			retry, after, retryErr := o.shouldRetry(attempts, err)
			if retryErr != nil {
				eventChan <- ProviderEvent{Type: EventError, Error: retryErr}
				close(eventChan)
				return
			}
			if retry {
				slog.Warn("Retrying due to rate limit", "attempt", attempts, "max_retries", maxRetries)
				// Surface retry to UI via warning event so callers (e.g., summarization dialog) can display it
				eventChan <- ProviderEvent{Type: EventWarning, Content: fmt.Sprintf("Provider retry: %v; waiting %dms (attempt %d/%d)", err, after, attempts, maxRetries)}
				select {
				case <-ctx.Done():
					if ctx.Err() == nil {
						eventChan <- ProviderEvent{Type: EventError, Error: ctx.Err()}
					}
					close(eventChan)
					return
				case <-time.After(time.Duration(after) * time.Millisecond):
					continue
				}
			}
			eventChan <- ProviderEvent{Type: EventError, Error: retryErr}
			close(eventChan)
			return
		}
	}()
	return eventChan
}

func (o *openaiClient) shouldRetry(attempts int, err error) (bool, int64, error) {
	if attempts > maxRetries {
		var apiErr *openai.Error
		if errors.As(err, &apiErr) && apiErr != nil {
			return false, 0, fmt.Errorf("maximum retry attempts reached for rate limit: %d retries; last error (%d %s): %s", maxRetries, apiErr.StatusCode, apiErr.Type, apiErr.Message)
		}
		if err != nil {
			return false, 0, fmt.Errorf("maximum retry attempts reached for rate limit: %d retries; last error: %v", maxRetries, err)
		}
		return false, 0, fmt.Errorf("maximum retry attempts reached for rate limit: %d retries", maxRetries)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false, 0, err
	}
	var apiErr *openai.Error
	retryMs := 0
	retryAfterValues := []string{}
	if errors.As(err, &apiErr) {
		// Treat context_length_exceeded as non-retriable invalid request, not a quota/rate-limit error
		if isOpenAIContextLengthExceeded(apiErr) {
			return false, 0, wrapOpenAIContextLengthExceeded(apiErr, o.Model().ID)
		}
		// 401: allow one refresh/rotate attempt upstream; here we re-resolve and recreate once
		if apiErr.StatusCode == 401 {
			o.providerOptions.apiKey, err = config.Get().Resolve(o.providerOptions.config.APIKey)
			if err != nil {
				return false, 0, fmt.Errorf("failed to resolve API key: %w", err)
			}
			o.client = createOpenAIClient(o.providerOptions)
			return true, 0, nil
		}
		// Non-retryable classes by type/code
		lcType := strings.ToLower(apiErr.Type)
		lcCode := strings.ToLower(apiErr.Code)
		if lcType == "invalid_request_error" || lcType == "invalid_request" {
			return false, 0, err
		}
		if lcType == "content_policy_violation" || lcCode == "content_policy_violation" {
			return false, 0, err
		}
		if lcType == "insufficient_quota" || lcCode == "insufficient_quota" || lcCode == "billing_not_active" {
			return false, 0, err
		}
		// Retryable statuses
		if apiErr.StatusCode == 429 || apiErr.StatusCode == 500 || apiErr.StatusCode == 502 || apiErr.StatusCode == 503 || apiErr.StatusCode == 504 || apiErr.StatusCode == 408 {
			retryAfterValues = apiErr.Response.Header.Values("Retry-After")
			goto doRetry
		}
		return false, 0, err
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

	// Unknown error: treat as transient only if clearly a server condition; otherwise bubble
	slog.Error("OpenAI API error", "error", err.Error(), "attempt", attempts, "max_retries", maxRetries)

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

func (o *openaiClient) toolCalls(completion openai.ChatCompletion) []message.ToolCall {
	var toolCalls []message.ToolCall

	if len(completion.Choices) > 0 && len(completion.Choices[0].Message.ToolCalls) > 0 {
		for _, call := range completion.Choices[0].Message.ToolCalls {
			toolCall := message.ToolCall{
				ID:       call.ID,
				Name:     call.Function.Name,
				Input:    call.Function.Arguments,
				Type:     "function",
				Finished: true,
			}
			toolCalls = append(toolCalls, toolCall)
		}
	}

	return toolCalls
}

func (o *openaiClient) usage(completion openai.ChatCompletion) TokenUsage {
	cachedTokens := completion.Usage.PromptTokensDetails.CachedTokens
	inputTokens := completion.Usage.PromptTokens - cachedTokens

	return TokenUsage{
		InputTokens:         inputTokens,
		OutputTokens:        completion.Usage.CompletionTokens,
		CacheCreationTokens: 0,
		CacheReadTokens:     cachedTokens,
	}
}

func (o *openaiClient) Model() catwalk.Model {
	return o.providerOptions.model(o.providerOptions.modelType)
}
