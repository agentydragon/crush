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
	"github.com/charmbracelet/crush/internal/log"
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
	httpClient := log.NewHTTPClient()
	openaiClientOptions = append(openaiClientOptions, option.WithHTTPClient(httpClient))

	for key, value := range opts.extraHeaders {
		openaiClientOptions = append(openaiClientOptions, option.WithHeader(key, value))
	}

	for extraKey, extraValue := range opts.extraBody {
		openaiClientOptions = append(openaiClientOptions, option.WithJSONSet(extraKey, extraValue))
	}

	return openai.NewClient(openaiClientOptions...)
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
	params := o.preparedParams(o.convertMessages(messages), o.convertTools(tools))
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
	params := o.preparedParams(o.convertMessages(messages), o.convertTools(tools))
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
				if err != nil { errStr = err.Error() }
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
		return false, 0, fmt.Errorf("maximum retry attempts reached for rate limit: %d retries", maxRetries)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false, 0, err
	}
	var apiErr *openai.Error
	retryMs := 0
	retryAfterValues := []string{}
	if errors.As(err, &apiErr) {
		// Check for token expiration (401 Unauthorized)
		if apiErr.StatusCode == 401 {
			o.providerOptions.apiKey, err = config.Get().Resolve(o.providerOptions.config.APIKey)
			if err != nil {
				return false, 0, fmt.Errorf("failed to resolve API key: %w", err)
			}
			o.client = createOpenAIClient(o.providerOptions)
			return true, 0, nil
		}

		if apiErr.StatusCode != 429 && apiErr.StatusCode != 500 {
			return false, 0, err
		}

		retryAfterValues = apiErr.Response.Header.Values("Retry-After")
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
