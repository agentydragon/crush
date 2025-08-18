package provider

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/charmbracelet/catwalk/pkg/catwalk"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/llm/tools"
	"github.com/charmbracelet/crush/internal/message"
)

type EventType string

const maxRetries = 8

const (
	EventContentStart   EventType = "content_start"
	EventToolUseStart   EventType = "tool_use_start"
	EventToolUseDelta   EventType = "tool_use_delta"
	EventToolUseStop    EventType = "tool_use_stop"
	EventContentDelta   EventType = "content_delta"
	EventThinkingDelta  EventType = "thinking_delta"
	EventSignatureDelta EventType = "signature_delta"
	EventContentStop    EventType = "content_stop"
	EventComplete       EventType = "complete"
	EventError          EventType = "error"
	EventWarning        EventType = "warning"
)

type TokenUsage struct {
	InputTokens         int64
	OutputTokens        int64
	CacheCreationTokens int64
	CacheReadTokens     int64
}

type ProviderResponse struct {
	Content       string
	ToolCalls     []message.ToolCall
	Usage         TokenUsage
	FinishReason  message.FinishReason
	ReasoningSumm []message.ReasoningSummaryContent
	ReasoningEnc  []message.ReasoningEncryptedContent
}

type ProviderEvent struct {
	Type EventType

	Content   string
	Thinking  string
	Signature string
	Response  *ProviderResponse
	ToolCall  *message.ToolCall
	Error     error
}
type Provider interface {
	SendMessages(ctx context.Context, messages []message.Message, tools []tools.BaseTool) (*ProviderResponse, error)

	StreamResponse(ctx context.Context, messages []message.Message, tools []tools.BaseTool) <-chan ProviderEvent

	Model() catwalk.Model
}

type providerClientOptions struct {
	baseURL            string
	config             config.ProviderConfig
	apiKey             string
	modelType          config.SelectedModelType
	model              func(config.SelectedModelType) catwalk.Model
	disableCache       bool
	systemMessage      string
	systemPromptPrefix string
	maxTokens          int64
	extraHeaders       map[string]string
	extraBody          map[string]any
	extraParams        map[string]string
	wire               ProviderWireLogger
}

type ProviderClientOption func(*providerClientOptions)

func WithWireLogger(w ProviderWireLogger) ProviderClientOption {
	return func(options *providerClientOptions) { options.wire = w }
}

type ProviderClient interface {
	send(ctx context.Context, messages []message.Message, tools []tools.BaseTool) (*ProviderResponse, error)
	stream(ctx context.Context, messages []message.Message, tools []tools.BaseTool) <-chan ProviderEvent

	Model() catwalk.Model
}

type ProviderWireLogger interface {
	Enabled() bool
	LogJSONL(e any)
}

type baseProvider[C ProviderClient] struct {
	options providerClientOptions
	client  C
	wire    ProviderWireLogger
}

func (p *baseProvider[C]) cleanMessages(messages []message.Message) (cleaned []message.Message) {
	for _, msg := range messages {
		// The message has no content
		if len(msg.Parts) == 0 {
			continue
		}
		cleaned = append(cleaned, msg)
	}
	return
}

func (p *baseProvider[C]) SendMessages(ctx context.Context, messages []message.Message, tools []tools.BaseTool) (*ProviderResponse, error) {
	messages = p.cleanMessages(messages)
	return p.client.send(ctx, messages, tools)
}

func (p *baseProvider[C]) StreamResponse(ctx context.Context, messages []message.Message, tools []tools.BaseTool) <-chan ProviderEvent {
	messages = p.cleanMessages(messages)
	return p.client.stream(ctx, messages, tools)
}

func (p *baseProvider[C]) Model() catwalk.Model {
	return p.client.Model()
}

func calcMaxTokens(opts providerClientOptions, model catwalk.Model) int64 {
	cfg := config.Get()
	modelConfig := cfg.Models[config.SelectedModelTypeLarge]
	if opts.modelType == config.SelectedModelTypeSmall {
		modelConfig = cfg.Models[config.SelectedModelTypeSmall]
	}
	maxTokens := model.DefaultMaxTokens
	if modelConfig.MaxTokens > 0 {
		maxTokens = modelConfig.MaxTokens
	}
	if opts.maxTokens > 0 {
		maxTokens = opts.maxTokens
	}
	return maxTokens
}

func WithModel(model config.SelectedModelType) ProviderClientOption {
	return func(options *providerClientOptions) {
		options.modelType = model
	}
}

func WithDisableCache(disableCache bool) ProviderClientOption {
	return func(options *providerClientOptions) {
		options.disableCache = disableCache
	}
}

func WithSystemMessage(systemMessage string) ProviderClientOption {
	return func(options *providerClientOptions) {
		options.systemMessage = systemMessage
	}
}

func WithMaxTokens(maxTokens int64) ProviderClientOption {
	return func(options *providerClientOptions) {
		options.maxTokens = maxTokens
	}
}

func NewProvider(cfg config.ProviderConfig, opts ...ProviderClientOption) (Provider, error) {
	restore := config.PushPopCrushEnv()
	defer restore()
	resolvedAPIKey, err := config.Get().Resolve(cfg.APIKey)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve API key for provider %s: %w", cfg.ID, err)
	}

	// Resolve extra headers
	resolvedExtraHeaders := make(map[string]string)
	for key, value := range cfg.ExtraHeaders {
		resolvedValue, err := config.Get().Resolve(value)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve extra header %s for provider %s: %w", key, cfg.ID, err)
		}
		resolvedExtraHeaders[key] = resolvedValue
	}

	clientOptions := providerClientOptions{
		baseURL:            cfg.BaseURL,
		config:             cfg,
		apiKey:             resolvedAPIKey,
		extraHeaders:       resolvedExtraHeaders,
		extraBody:          cfg.ExtraBody,
		extraParams:        cfg.ExtraParams,
		systemPromptPrefix: cfg.SystemPromptPrefix,
		model: func(tp config.SelectedModelType) catwalk.Model {
			return *config.Get().GetModelByType(tp)
		},
	}
	for _, o := range opts {
		o(&clientOptions)
	}
	switch cfg.Type {
	case catwalk.TypeAnthropic:
		return &baseProvider[AnthropicClient]{
			options: clientOptions,
			client:  newAnthropicClient(clientOptions, AnthropicClientTypeNormal),
		}, nil
	case catwalk.TypeOpenAI:
		// Honor explicit generation API selection; default to chat for back-compat.
		if cfg.GenerationAPI == "responses" {
			slog.Info("provider.openai.client", "api", "responses", "provider_id", cfg.ID, "base_url", cfg.BaseURL)
			return &baseProvider[OpenAIClient]{
				options: clientOptions,
				client:  newOpenAIResponsesClient(clientOptions),
			}, nil
		}
		slog.Info("provider.openai.client", "api", "chat", "provider_id", cfg.ID, "base_url", cfg.BaseURL)
		return &baseProvider[OpenAIClient]{
			options: clientOptions,
			client:  &openaiClient{providerOptions: clientOptions, client: createOpenAIClient(clientOptions)},
		}, nil
	case catwalk.TypeGemini:
		return &baseProvider[GeminiClient]{
			options: clientOptions,
			client:  newGeminiClient(clientOptions),
		}, nil
	case catwalk.TypeBedrock:
		return &baseProvider[BedrockClient]{
			options: clientOptions,
			client:  newBedrockClient(clientOptions),
		}, nil
	case catwalk.TypeAzure:
		return &baseProvider[AzureClient]{
			options: clientOptions,
			client:  newAzureClient(clientOptions),
		}, nil
	case catwalk.TypeVertexAI:
		return &baseProvider[VertexAIClient]{
			options: clientOptions,
			client:  newVertexAIClient(clientOptions),
		}, nil
	}
	return nil, fmt.Errorf("provider not supported: %s", cfg.Type)
}
