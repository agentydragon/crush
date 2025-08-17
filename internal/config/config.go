package config

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/catwalk/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/env"
	"github.com/tidwall/sjson"
)

const (
	appName              = "crush"
	defaultDataDirectory = ".crush"
)

var defaultContextPaths = []string{
	".github/copilot-instructions.md",
	".cursorrules",
	".cursor/rules/",
	"CLAUDE.md",
	"CLAUDE.local.md",
	"GEMINI.md",
	"gemini.md",
	"crush.md",
	"crush.local.md",
	"Crush.md",
	"Crush.local.md",
	"CRUSH.md",
	"CRUSH.local.md",
	"AGENTS.md",
	"agents.md",
	"Agents.md",
}

type SelectedModelType string

const (
	SelectedModelTypeLarge SelectedModelType = "large"
	SelectedModelTypeSmall SelectedModelType = "small"
)

type SelectedModel struct {
	// The model id as used by the provider API.
	// Required.
	Model string `json:"model" jsonschema:"required,description=The model ID as used by the provider API,example=gpt-4o"`
	// The model provider, same as the key/id used in the providers config.
	// Required.
	Provider string `json:"provider" jsonschema:"required,description=The model provider ID that matches a key in the providers config,example=openai"`

	// Only used by models that use the openai provider and need this set.
	ReasoningEffort string `json:"reasoning_effort,omitempty" jsonschema:"description=Reasoning effort level for OpenAI models that support it,enum=low,enum=medium,enum=high"`

	// Overrides the default model configuration.
	MaxTokens int64 `json:"max_tokens,omitempty" jsonschema:"description=Maximum number of tokens for model responses,minimum=1,maximum=200000,example=4096"`

	// Used by anthropic models that can reason to indicate if the model should think.
	Think bool `json:"think,omitempty" jsonschema:"description=Enable thinking mode for Anthropic models that support reasoning"`
}

type ProviderConfig struct {
	// The provider's id.
	ID string `json:"id,omitempty" jsonschema:"description=Unique identifier for the provider,example=openai"`
	// The provider's name, used for display purposes.
	Name string `json:"name,omitempty" jsonschema:"description=Human-readable name for the provider,example=OpenAI"`
	// The provider's API endpoint.
	BaseURL string `json:"base_url,omitempty" jsonschema:"description=Base URL for the provider's API,format=uri,example=https://api.openai.com/v1"`
	// The provider type, e.g. "openai", "anthropic", etc. if empty it defaults to openai.
	Type catwalk.Type `json:"type,omitempty" jsonschema:"description=Provider type that determines the API format,enum=openai,enum=anthropic,enum=gemini,enum=azure,enum=vertexai,default=openai"`
	// Which API to use for text generation; for OpenAI this selects between Chat Completions and Responses.
	GenerationAPI string `json:"generation_api,omitempty" jsonschema:"description=Which API to use for text generation,enum=chat,enum=responses,default=chat"`
	// The provider's API key.
	APIKey string `json:"api_key,omitempty" jsonschema:"description=API key for authentication with the provider,example=$OPENAI_API_KEY"`
	// Marks the provider as disabled.
	Disable bool `json:"disable,omitempty" jsonschema:"description=Whether this provider is disabled,default=false"`

	// Custom system prompt prefix.
	SystemPromptPrefix string `json:"system_prompt_prefix,omitempty" jsonschema:"description=Custom prefix to add to system prompts for this provider"`

	// Path to a file that, if set, will be used as the system prompt instead of the built-in prompt.
	SystemPromptPath string `json:"system_prompt_path,omitempty" jsonschema:"description=Path to a file to use as the system prompt instead of the built-in prompt"`

	// Extra headers to send with each request to the provider.
	ExtraHeaders map[string]string `json:"extra_headers,omitempty" jsonschema:"description=Additional HTTP headers to send with requests"`
	// Extra body
	ExtraBody map[string]any `json:"extra_body,omitempty" jsonschema:"description=Additional fields to include in request bodies"`

	// Used to pass extra parameters to the provider.
	ExtraParams map[string]string `json:"-"`

	// The provider models
	Models []catwalk.Model `json:"models,omitempty" jsonschema:"description=List of models available from this provider"`
}

type MCPType string

const (
	MCPStdio MCPType = "stdio"
	MCPSse   MCPType = "sse"
	MCPHttp  MCPType = "http"
)

type MCPConfig struct {
	Command  string            `json:"command,omitempty" jsonschema:"description=Command to execute for stdio MCP servers,example=npx"`
	Env      map[string]string `json:"env,omitempty" jsonschema:"description=Environment variables to set for the MCP server"`
	Args     []string          `json:"args,omitempty" jsonschema:"description=Arguments to pass to the MCP server command"`
	Type     MCPType           `json:"type" jsonschema:"required,description=Type of MCP connection,enum=stdio,enum=sse,enum=http,default=stdio"`
	URL      string            `json:"url,omitempty" jsonschema:"description=URL for HTTP or SSE MCP servers,format=uri,example=http://localhost:3000/mcp"`
	Disabled bool              `json:"disabled,omitempty" jsonschema:"description=Whether this MCP server is disabled,default=false"`

	// TODO: maybe make it possible to get the value from the env
	Headers map[string]string `json:"headers,omitempty" jsonschema:"description=HTTP headers for HTTP/SSE MCP servers"`
}

type LSPConfig struct {
	Disabled bool     `json:"enabled,omitempty" jsonschema:"description=Whether this LSP server is disabled,default=false"`
	Command  string   `json:"command" jsonschema:"required,description=Command to execute for the LSP server,example=gopls"`
	Args     []string `json:"args,omitempty" jsonschema:"description=Arguments to pass to the LSP server command"`
	Options  any      `json:"options,omitempty" jsonschema:"description=LSP server-specific configuration options"`
}

type TUIOptions struct {
	CompactMode     bool                   `json:"compact_mode,omitempty" jsonschema:"description=Enable compact mode for the TUI interface,default=false"`
	FileCompletions *FileCompletionOptions `json:"file_completions,omitempty" jsonschema:"description=Options for editor file path completions"`
	// Here we can add themes later or any TUI related options
}

type Permissions struct {
	AllowedTools []string `json:"allowed_tools,omitempty" jsonschema:"description=List of tools that don't require permission prompts,example=bash,example=view"` // Tools that don't require permission prompts
	SkipRequests bool     `json:"-"`                                                                                                                              // Automatically accept all permissions (YOLO mode)
}

type Options struct {
	ContextPaths           []string     `json:"context_paths,omitempty" jsonschema:"description=Paths to files containing context information for the AI,example=.cursorrules,example=CRUSH.md"`
	TUI                    *TUIOptions  `json:"tui,omitempty" jsonschema:"description=Terminal user interface options"`
	Debug                  bool         `json:"debug,omitempty" jsonschema:"description=Enable debug mode: UI overlays, event logs, wire logs, counters,default=false"`
	DebugLSP               bool         `json:"debug_lsp,omitempty" jsonschema:"description=Enable debug logging for LSP servers,default=false"`
	DisableAutoSummarize   bool         `json:"disable_auto_summarize,omitempty" jsonschema:"description=Disable automatic conversation summarization,default=false"`
	DisableTitleGeneration bool         `json:"disable_title_generation,omitempty" jsonschema:"description=Disable automatic session title generation,default=false"`
	DataDirectory          string       `json:"data_directory,omitempty" jsonschema:"description=Directory for storing application data (relative to working directory),default=.crush,example=.crush"` // Relative to the cwd
	ShowReasoningSummaries bool         `json:"show_reasoning_summaries,omitempty" jsonschema:"description=Show model reasoning summary text as separate items in the chat UI,default=false"`
	ReasoningSummary       string       `json:"reasoning_summary,omitempty" jsonschema:"description=Request a reasoning summary from OpenAI Responses models (o-series). One of: auto, concise, detailed.,enum=auto,enum=concise,enum=detailed"`
	DebugProviderWire      bool         `json:"debug_provider_wire,omitempty" jsonschema:"description=Enable provider wire logging (JSONL rotation+compression). Logs full request/response bodies for debugging.,default=false"`
	Wire                   *WireOptions `json:"wire,omitempty" jsonschema:"description=Provider wire logging options"`
	MCP                    *MCPOptions  `json:"mcp,omitempty" jsonschema:"description=Options for MCP (Model Context Protocol) behavior"`
	Diff                   *DiffOptions `json:"diff,omitempty" jsonschema:"description=External diff options"`

	BrokerBufferSize   int `json:"broker_buffer_size,omitempty" jsonschema:"description=Channel buffer size for internal pubsub brokers; affects event backpressure and drops,minimum=1,default=64"`
	MaxToolOutputBytes int `json:"max_tool_output_bytes,omitempty" jsonschema:"description=Hard cap on bytes for any single tool output included in a tool_result message; 0 uses default"`
}

func (o *Options) EffectiveReasoningSummary() string {
	if o == nil {
		return ""
	}
	switch o.ReasoningSummary {
	case "auto", "concise", "detailed":
		return o.ReasoningSummary
	}
	if o.ShowReasoningSummaries {
		return "auto"
	}
	return ""
}

type WireOptions struct {
	MaxSizeMB    int    `json:"max_size_mb,omitempty" jsonschema:"description=Max size in MB for wire logs before rotation,default=250"`
	MaxBackups   int    `json:"max_backups,omitempty" jsonschema:"description=Max number of rotated files to keep,default=10"`
	MaxAgeDays   int    `json:"max_age_days,omitempty" jsonschema:"description=Max days to retain rotated logs,default=30"`
	Compress     *bool  `json:"compress,omitempty" jsonschema:"description=Compress rotated logs with gzip,default=true"`
	MCPLogMode   string `json:"mcp_log_mode,omitempty" jsonschema:"description=Logging mode for MCP wire logs: single (one file) or per_server (one per MCP),enum=single,enum=per_server,default=single"`
	MCPFilename  string `json:"mcp_filename,omitempty" jsonschema:"description=Filename for MCP wire log in single mode,default=mcp-wire.log"`
	DebugMCPWire *bool  `json:"debug_mcp_wire,omitempty" jsonschema:"description=Enable MCP wire logging independent of provider wire switch,default=null"`
}

type MCPOptions struct {
	// Timeout in seconds for MCP client startup (create/start/initialize/list tools). Defaults to 10s when 0 or unset.
	InitTimeoutSecs int `json:"init_timeout_secs,omitempty" jsonschema:"description=Timeout in seconds for MCP client startup (connect+initialize); 0 uses default of 10s,minimum=0"`
	// Timeout in seconds for individual MCP tool calls.
	ToolTimeoutSecs int `json:"tool_timeout_secs,omitempty" jsonschema:"description=Timeout in seconds for MCP tool calls; 0 uses default of 120s,minimum=0"`
}

type DiffOptions struct {
	ExternalCommand     string `json:"external_command,omitempty" jsonschema:"description=Shell command template to invoke for diffs; use {old} and {new} placeholders,example=git diff --no-index --histogram --minimal -U3 -- a {old} -- b {new}"`
	ParseMode           string `json:"parse_mode,omitempty" jsonschema:"description=How to interpret the external diff output,enum=unified,enum=git_word_porcelain,enum=auto,default=unified"`
	IgnoreIndentChanges bool   `json:"ignore_indent_changes,omitempty" jsonschema:"description=Treat lines that differ only by leading whitespace as equal in diffs,default=false"`
	UseExternalForUI    bool   `json:"use_external_for_ui,omitempty" jsonschema:"description=Render diffs in the UI using the external diff command instead of the built-in renderer,default=false"`
}

type FileCompletionOptions struct {
	Enabled     bool `json:"enabled,omitempty" jsonschema:"description=Enable editor file completions,default=true"`
	MinChars    int  `json:"min_chars,omitempty" jsonschema:"description=Minimum characters after '/', before triggering file completions,minimum=0,default=2"`
	MaxResults  int  `json:"max_results,omitempty" jsonschema:"description=Maximum number of completion results to display,minimum=1,default=1000"`
	DebounceMS  int  `json:"debounce_ms,omitempty" jsonschema:"description=Debounce before starting file scan in milliseconds,minimum=0,default=150"`
	TimeLimitMS int  `json:"time_limit_ms,omitempty" jsonschema:"description=Time limit for a file scan in milliseconds,minimum=10,default=1500"`
	GitAware    bool `json:"git_aware,omitempty" jsonschema:"description=Use git for faster listing when in a repository,default=true"`
}

type MCPs map[string]MCPConfig

type MCP struct {
	Name string    `json:"name"`
	MCP  MCPConfig `json:"mcp"`
}

func (m MCPs) Sorted() []MCP {
	sorted := make([]MCP, 0, len(m))
	for k, v := range m {
		sorted = append(sorted, MCP{
			Name: k,
			MCP:  v,
		})
	}
	slices.SortFunc(sorted, func(a, b MCP) int {
		return strings.Compare(a.Name, b.Name)
	})
	return sorted
}

type LSPs map[string]LSPConfig

type LSP struct {
	Name string    `json:"name"`
	LSP  LSPConfig `json:"lsp"`
}

func (l LSPs) Sorted() []LSP {
	sorted := make([]LSP, 0, len(l))
	for k, v := range l {
		sorted = append(sorted, LSP{
			Name: k,
			LSP:  v,
		})
	}
	slices.SortFunc(sorted, func(a, b LSP) int {
		return strings.Compare(a.Name, b.Name)
	})
	return sorted
}

func (m MCPConfig) ResolvedEnv() []string {
	resolver := NewShellVariableResolver(env.New())
	for e, v := range m.Env {
		var err error
		m.Env[e], err = resolver.ResolveValue(v)
		if err != nil {
			slog.Error("error resolving environment variable", "error", err, "variable", e, "value", v)
			continue
		}
	}

	env := make([]string, 0, len(m.Env))
	for k, v := range m.Env {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}
	return env
}

func (m MCPConfig) ResolvedHeaders() map[string]string {
	resolver := NewShellVariableResolver(env.New())
	for e, v := range m.Headers {
		var err error
		m.Headers[e], err = resolver.ResolveValue(v)
		if err != nil {
			slog.Error("error resolving header variable", "error", err, "variable", e, "value", v)
			continue
		}
	}
	return m.Headers
}

type Agent struct {
	ID          string `json:"id,omitempty"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	// This is the id of the system prompt used by the agent
	Disabled bool `json:"disabled,omitempty"`

	Model SelectedModelType `json:"model" jsonschema:"required,description=The model type to use for this agent,enum=large,enum=small,default=large"`

	// The available tools for the agent
	//  if this is nil, all tools are available
	AllowedTools []string `json:"allowed_tools,omitempty"`

	// this tells us which MCPs are available for this agent
	//  if this is empty all mcps are available
	//  the string array is the list of tools from the AllowedMCP the agent has available
	//  if the string array is nil, all tools from the AllowedMCP are available
	AllowedMCP map[string][]string `json:"allowed_mcp,omitempty"`

	// The list of LSPs that this agent can use
	//  if this is nil, all LSPs are available
	AllowedLSP []string `json:"allowed_lsp,omitempty"`

	// Overrides the context paths for this agent
	ContextPaths []string `json:"context_paths,omitempty"`
}

// Config holds the configuration for crush.
type Config struct {
	Schema string `json:"$schema,omitempty"`

	// We currently only support large/small as values here.
	Models map[SelectedModelType]SelectedModel `json:"models,omitempty" jsonschema:"description=Model configurations for different model types,example={\"large\":{\"model\":\"gpt-4o\",\"provider\":\"openai\"}}"`

	// The providers that are configured
	Providers *csync.Map[string, ProviderConfig] `json:"providers,omitempty" jsonschema:"description=AI provider configurations"`

	MCP MCPs `json:"mcp,omitempty" jsonschema:"description=Model Context Protocol server configurations"`

	LSP LSPs `json:"lsp,omitempty" jsonschema:"description=Language Server Protocol configurations"`

	Options *Options `json:"options,omitempty" jsonschema:"description=General application options"`

	Permissions *Permissions `json:"permissions,omitempty" jsonschema:"description=Permission settings for tool usage"`

	// Resolution/Loading trace
	LoadPathsConsidered []string `json:"load_paths_considered,omitempty"`
	LoadPathsLoaded     []string `json:"load_paths_loaded,omitempty"`

	// Internal
	workingDir string `json:"-"`
	// TODO: most likely remove this concept when I come back to it
	Agents map[string]Agent `json:"-"`
	// TODO: find a better way to do this this should probably not be part of the config
	resolver       VariableResolver
	dataConfigDir  string             `json:"-"`
	knownProviders []catwalk.Provider `json:"-"`
}

func (c *Config) WorkingDir() string {
	return c.workingDir
}

func (c *Config) EnabledProviders() []ProviderConfig {
	var enabled []ProviderConfig
	for p := range c.Providers.Seq() {
		if !p.Disable {
			enabled = append(enabled, p)
		}
	}
	return enabled
}

// IsConfigured  return true if at least one provider is configured
func (c *Config) IsConfigured() bool {
	return len(c.EnabledProviders()) > 0
}

func (c *Config) GetModel(provider, model string) *catwalk.Model {
	if providerConfig, ok := c.Providers.Get(provider); ok {
		for _, m := range providerConfig.Models {
			if m.ID == model {
				return &m
			}
		}
	}
	return nil
}

func (c *Config) GetProviderForModel(modelType SelectedModelType) *ProviderConfig {
	model, ok := c.Models[modelType]
	if !ok {
		return nil
	}
	if providerConfig, ok := c.Providers.Get(model.Provider); ok {
		return &providerConfig
	}
	return nil
}

func (c *Config) GetModelByType(modelType SelectedModelType) *catwalk.Model {
	model, ok := c.Models[modelType]
	if !ok {
		return nil
	}
	return c.GetModel(model.Provider, model.Model)
}

func (c *Config) LargeModel() *catwalk.Model {
	model, ok := c.Models[SelectedModelTypeLarge]
	if !ok {
		return nil
	}
	return c.GetModel(model.Provider, model.Model)
}

func (c *Config) SmallModel() *catwalk.Model {
	model, ok := c.Models[SelectedModelTypeSmall]
	if !ok {
		return nil
	}
	return c.GetModel(model.Provider, model.Model)
}

func (c *Config) SetCompactMode(enabled bool) error {
	if c.Options == nil {
		c.Options = &Options{}
	}
	c.Options.TUI.CompactMode = enabled
	return c.SetConfigField("options.tui.compact_mode", enabled)
}

func (c *Config) Resolve(key string) (string, error) {
	if c.resolver == nil {
		return "", fmt.Errorf("no variable resolver configured")
	}
	return c.resolver.ResolveValue(key)
}

func (c *Config) UpdatePreferredModel(modelType SelectedModelType, model SelectedModel) error {
	c.Models[modelType] = model
	if err := c.SetConfigField(fmt.Sprintf("models.%s", modelType), model); err != nil {
		return fmt.Errorf("failed to update preferred model: %w", err)
	}
	return nil
}

func (c *Config) SetConfigField(key string, value any) error {
	// read the data
	data, err := os.ReadFile(c.dataConfigDir)
	if err != nil {
		if os.IsNotExist(err) {
			data = []byte("{}")
		} else {
			return fmt.Errorf("failed to read config file: %w", err)
		}
	}

	newValue, err := sjson.Set(string(data), key, value)
	if err != nil {
		return fmt.Errorf("failed to set config field %s: %w", key, err)
	}
	if err := os.WriteFile(c.dataConfigDir, []byte(newValue), 0o600); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}
	return nil
}

func (c *Config) SetProviderAPIKey(providerID, apiKey string) error {
	// First save to the config file
	err := c.SetConfigField("providers."+providerID+".api_key", apiKey)
	if err != nil {
		return fmt.Errorf("failed to save API key to config file: %w", err)
	}

	providerConfig, exists := c.Providers.Get(providerID)
	if exists {
		providerConfig.APIKey = apiKey
		c.Providers.Set(providerID, providerConfig)
		return nil
	}

	var foundProvider *catwalk.Provider
	for _, p := range c.knownProviders {
		if string(p.ID) == providerID {
			foundProvider = &p
			break
		}
	}

	if foundProvider != nil {
		// Create new provider config based on known provider
		providerConfig = ProviderConfig{
			ID:           providerID,
			Name:         foundProvider.Name,
			BaseURL:      foundProvider.APIEndpoint,
			Type:         foundProvider.Type,
			APIKey:       apiKey,
			Disable:      false,
			ExtraHeaders: make(map[string]string),
			ExtraParams:  make(map[string]string),
			Models:       foundProvider.Models,
		}
	} else {
		return fmt.Errorf("provider with ID %s not found in known providers", providerID)
	}
	// Store the updated provider config
	c.Providers.Set(providerID, providerConfig)
	return nil
}

func (c *Config) SetupAgents() {
	agents := map[string]Agent{
		"coder": {
			ID:           "coder",
			Name:         "Coder",
			Description:  "An agent that helps with executing coding tasks.",
			Model:        SelectedModelTypeLarge,
			ContextPaths: c.Options.ContextPaths,
			// All tools allowed
		},
		"task": {
			ID:           "task",
			Name:         "Task",
			Description:  "An agent that helps with searching for context and finding implementation details.",
			Model:        SelectedModelTypeLarge,
			ContextPaths: c.Options.ContextPaths,
			AllowedTools: []string{
				"glob",
				"grep",
				"ls",
				"sourcegraph",
				"view",
			},
			// NO MCPs or LSPs by default
			AllowedMCP: map[string][]string{},
			AllowedLSP: []string{},
		},
	}
	c.Agents = agents
}

func (c *Config) Resolver() VariableResolver {
	return c.resolver
}

func (c *ProviderConfig) TestConnection(resolver VariableResolver) error {
	testURL := ""
	headers := make(map[string]string)
	apiKey, _ := resolver.ResolveValue(c.APIKey)
	switch c.Type {
	case catwalk.TypeOpenAI:
		baseURL, _ := resolver.ResolveValue(c.BaseURL)
		if baseURL == "" {
			baseURL = "https://api.openai.com/v1"
		}
		testURL = baseURL + "/models"
		headers["Authorization"] = "Bearer " + apiKey
	case catwalk.TypeAnthropic:
		baseURL, _ := resolver.ResolveValue(c.BaseURL)
		if baseURL == "" {
			baseURL = "https://api.anthropic.com/v1"
		}
		testURL = baseURL + "/models"
		headers["x-api-key"] = apiKey
		headers["anthropic-version"] = "2023-06-01"
	case catwalk.TypeGemini:
		baseURL, _ := resolver.ResolveValue(c.BaseURL)
		if baseURL == "" {
			baseURL = "https://generativelanguage.googleapis.com"
		}
		testURL = baseURL + "/v1beta/models?key=" + url.QueryEscape(apiKey)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := &http.Client{}
	req, err := http.NewRequestWithContext(ctx, "GET", testURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create request for provider %s: %w", c.ID, err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	for k, v := range c.ExtraHeaders {
		req.Header.Set(k, v)
	}
	b, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to create request for provider %s: %w", c.ID, err)
	}
	if b.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to connect to provider %s: %s", c.ID, b.Status)
	}
	_ = b.Body.Close()
	return nil
}
