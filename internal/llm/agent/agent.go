package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/catwalk/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/history"
	"github.com/charmbracelet/crush/internal/llm/prompt"
	"github.com/charmbracelet/crush/internal/llm/provider"
	"github.com/charmbracelet/crush/internal/llm/tools"
	"github.com/charmbracelet/crush/internal/log"
	"github.com/charmbracelet/crush/internal/lsp"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/shell"
)

// Common errors
var (
	ErrRequestCancelled = errors.New("request canceled by user")
	ErrSessionBusy      = errors.New("session is currently processing another request")
)

type AgentEventType string

const (
	AgentEventTypeError     AgentEventType = "error"
	AgentEventTypeResponse  AgentEventType = "response"
	AgentEventTypeSummarize AgentEventType = "summarize"
	AgentEventTypeToolState AgentEventType = "tool_state"
)

type AgentEvent struct {
	Type    AgentEventType
	Message message.Message
	Error   error

	// When summarizing
	SessionID   string
	Progress    string
	PreviewTail string
	TotalBytes  int
	TotalRunes  int
	Done        bool

	// Tool state events
	ToolCallID string
	State      tools.ToolState
}

type Service interface {
	pubsub.Suscriber[AgentEvent]
	Model() catwalk.Model
	Run(ctx context.Context, sessionID string, content string, attachments ...message.Attachment) (<-chan AgentEvent, error)
	Cancel(sessionID string)
	CancelAll()
	IsSessionBusy(sessionID string) bool
	IsBusy() bool
	Summarize(ctx context.Context, sessionID string) error
	UpdateModel() error
}

type agent struct {
	*pubsub.Broker[AgentEvent]
	agentCfg config.Agent
	sessions session.Service
	messages message.Service
	mcpTools []McpTool

	tools *csync.LazySlice[tools.BaseTool]

	provider   provider.Provider
	providerID string

	titleProvider       provider.Provider
	summarizeProvider   provider.Provider
	summarizeProviderID string

	activeRequests   *csync.Map[string, context.CancelFunc]
	mcpClientFactory MCPClientFactory
	mcpWireLogger    MCPWireLogger

	redactions []string
}

// toolStateSink is the real sink used by tools to publish intermediate state.
// Coalesces and redacts; emits agent events; can persist later.
type toolStateSink struct {
	a          *agent
	sessionID  string
	messageID  string
	toolCallID string
	last       tools.ToolState
	lastEmit   time.Time
}

func newToolStateSink(a *agent, sessionID, messageID, toolCallID string) *toolStateSink {
	return &toolStateSink{a: a, sessionID: sessionID, messageID: messageID, toolCallID: toolCallID}
}

func (s *toolStateSink) Update(state tools.ToolState) {
	state.UpdatedAt = tools.NowMillis()
	// Coalesce identical state
	if s.last.Phase == state.Phase && s.last.Title == state.Title && s.last.Detail == state.Detail {
		return
	}
	// Throttle to max one emit every 50ms per tool_call_id
	if !s.lastEmit.IsZero() && time.Since(s.lastEmit) < 50*time.Millisecond {
		return
	}
	state.Title = redactText(state.Title, s.a.redactions)
	state.Detail = redactText(state.Detail, s.a.redactions)
	s.last = state
	s.lastEmit = time.Now()
	slog.Info("toolstate.update", "session_id", s.sessionID, "message_id", s.messageID, "tool_call_id", s.toolCallID, "phase", state.Phase, "title", state.Title, "detail", state.Detail)
	s.a.Publish(pubsub.UpdatedEvent, AgentEvent{Type: AgentEventTypeToolState, SessionID: s.sessionID, ToolCallID: s.toolCallID, State: state})
}

func (s *toolStateSink) Final(result tools.ToolResponse) { /* reserved for future persistence */ }
func (s *toolStateSink) Error(err error)                 { /* reserved for future */ }

var agentPromptMap = map[string]prompt.PromptID{
	"coder": prompt.PromptCoder,
	"task":  prompt.PromptTask,
}

func NewAgent(
	ctx context.Context,
	agentCfg config.Agent,
	// These services are needed in the tools
	permissions permission.Service,
	sessions session.Service,
	messages message.Service,
	history history.Service,
	lspClients map[string]*lsp.Client,
	optsAgent ...AgentOption,
) (Service, error) {
	cfg := config.Get()

	var agentTool tools.BaseTool
	if agentCfg.ID == "coder" {
		taskAgentCfg := config.Get().Agents["task"]
		if taskAgentCfg.ID == "" {
			return nil, fmt.Errorf("task agent not found in config")
		}
		taskAgent, err := NewAgent(ctx, taskAgentCfg, permissions, sessions, messages, history, lspClients)
		if err != nil {
			return nil, fmt.Errorf("failed to create task agent: %w", err)
		}

		agentTool = NewAgentTool(taskAgent, sessions, messages)
	}

	providerCfg := config.Get().GetProviderForModel(agentCfg.Model)
	if providerCfg == nil {
		return nil, fmt.Errorf("provider for agent %s not found in config", agentCfg.Name)
	}
	model := config.Get().GetModelByType(agentCfg.Model)

	if model == nil {
		return nil, fmt.Errorf("model not found for agent %s", agentCfg.Name)
	}

	promptID := agentPromptMap[agentCfg.ID]
	if promptID == "" {
		promptID = prompt.PromptDefault
	}
	opts := []provider.ProviderClientOption{
		provider.WithModel(agentCfg.Model),
		provider.WithSystemMessage(prompt.GetPrompt(promptID, providerCfg.ID, config.Get().Options.ContextPaths...)),
	}
	agentProvider, err := provider.NewProvider(*providerCfg, opts...)
	if err != nil {
		return nil, err
	}

	smallModelCfg := cfg.Models[config.SelectedModelTypeSmall]
	var smallModelProviderCfg *config.ProviderConfig
	if smallModelCfg.Provider == providerCfg.ID {
		smallModelProviderCfg = providerCfg
	} else {
		smallModelProviderCfg = cfg.GetProviderForModel(config.SelectedModelTypeSmall)

		if smallModelProviderCfg.ID == "" {
			return nil, fmt.Errorf("provider %s not found in config", smallModelCfg.Provider)
		}
	}
	smallModel := cfg.GetModelByType(config.SelectedModelTypeSmall)
	if smallModel.ID == "" {
		return nil, fmt.Errorf("model %s not found in provider %s", smallModelCfg.Model, smallModelProviderCfg.ID)
	}

	titleOpts := []provider.ProviderClientOption{
		provider.WithModel(config.SelectedModelTypeSmall),
		provider.WithSystemMessage(prompt.GetPrompt(prompt.PromptTitle, smallModelProviderCfg.ID)),
	}
	titleProvider, err := provider.NewProvider(*smallModelProviderCfg, titleOpts...)
	if err != nil {
		return nil, err
	}

	summarizeOpts := []provider.ProviderClientOption{
		provider.WithModel(config.SelectedModelTypeLarge),
		provider.WithSystemMessage(prompt.GetPrompt(prompt.PromptSummarizer, providerCfg.ID)),
	}
	summarizeProvider, err := provider.NewProvider(*providerCfg, summarizeOpts...)
	if err != nil {
		return nil, err
	}

	var assignedFactory MCPClientFactory

	toolFn := func() []tools.BaseTool {
		slog.Info("Initializing agent tools", "agent", agentCfg.ID)
		defer func() {
			slog.Info("Initialized agent tools", "agent", agentCfg.ID)
		}()

		cwd := cfg.WorkingDir()
		allTools := []tools.BaseTool{
			tools.NewBashTool(permissions, cwd),
			tools.NewDownloadTool(permissions, cwd),
			tools.NewEditTool(lspClients, permissions, history, cwd),
			tools.NewMultiEditTool(lspClients, permissions, history, cwd),
			tools.NewFetchTool(permissions, cwd),
			tools.NewGlobTool(cwd),
			tools.NewGrepTool(cwd),
			tools.NewLsTool(permissions, cwd),
			tools.NewSourcegraphTool(),
			tools.NewViewTool(lspClients, permissions, cwd),
			tools.NewWriteTool(lspClients, permissions, history, cwd),
		}

		mcpToolsOnce.Do(func() {
			mcpTools = doGetMCPTools(ctx, permissions, cfg, assignedFactory)
		})
		allTools = append(allTools, mcpTools...)

		if len(lspClients) > 0 {
			allTools = append(allTools, tools.NewDiagnosticsTool(lspClients))
		}

		if agentTool != nil {
			allTools = append(allTools, agentTool)
		}

		if agentCfg.AllowedTools == nil {
			return allTools
		}

		var filteredTools []tools.BaseTool
		for _, tool := range allTools {
			if slices.Contains(agentCfg.AllowedTools, tool.Name()) {
				filteredTools = append(filteredTools, tool)
			}
		}
		return filteredTools
	}

	a := &agent{
		Broker:              pubsub.NewBroker[AgentEvent](),
		agentCfg:            agentCfg,
		provider:            agentProvider,
		providerID:          string(providerCfg.ID),
		messages:            messages,
		sessions:            sessions,
		titleProvider:       titleProvider,
		summarizeProvider:   summarizeProvider,
		summarizeProviderID: string(providerCfg.ID),
		activeRequests:      csync.NewMap[string, context.CancelFunc](),
		tools:               csync.NewLazySlice(toolFn),
	}
	// Build redaction list once
	a.redactions = buildRedactions()
	for _, opt := range optsAgent {
		opt(a)
	}
	assignedFactory = a.mcpClientFactory
	return a, nil
}

func (a *agent) buildRedactions() []string {
	var out []string
	cfg := config.Get()
	for p := range cfg.Providers.Seq() {
		// API key (resolved at provider creation; still useful to scrub literals)
		if p.APIKey != "" {
			if v, err := cfg.Resolve(p.APIKey); err == nil && v != "" {
				out = append(out, v)
				out = append(out, "Bearer "+v)
			}
		}
		for k, v := range p.ExtraHeaders {
			// Include header values that are likely to contain secrets
			keyLower := strings.ToLower(k)
			if strings.Contains(keyLower, "authorization") || strings.Contains(keyLower, "api") || strings.Contains(keyLower, "token") || strings.Contains(keyLower, "key") || strings.Contains(keyLower, "secret") {
				if v != "" {
					if resolved, err := cfg.Resolve(v); err == nil && resolved != "" {
						out = append(out, resolved)
					}
				}
			}
		}
	}
	return out
}

type toolStateNoop struct{}

func (t *toolStateNoop) Update(state tools.ToolState)    {}
func (t *toolStateNoop) Final(result tools.ToolResponse) {}
func (t *toolStateNoop) Error(err error)                 {}

func (a *agent) Model() catwalk.Model {
	return *config.Get().GetModelByType(a.agentCfg.Model)
}

func (a *agent) Cancel(sessionID string) {
	// Cancel regular requests
	if cancel, ok := a.activeRequests.Take(sessionID); ok && cancel != nil {
		slog.Info("Request cancellation initiated", "session_id", sessionID)
		cancel()
	}

	// Also check for summarize requests
	if cancel, ok := a.activeRequests.Take(sessionID + "-summarize"); ok && cancel != nil {
		slog.Info("Summarize cancellation initiated", "session_id", sessionID)
		cancel()
	}
}

func (a *agent) IsBusy() bool {
	var busy bool
	for cancelFunc := range a.activeRequests.Seq() {
		if cancelFunc != nil {
			busy = true
			break
		}
	}
	return busy
}

func (a *agent) IsSessionBusy(sessionID string) bool {
	_, busy := a.activeRequests.Get(sessionID)
	return busy
}

func (a *agent) generateTitle(ctx context.Context, sessionID string, content string) error {
	if content == "" {
		return nil
	}
	if a.titleProvider == nil {
		return nil
	}
	session, err := a.sessions.Get(ctx, sessionID)
	if err != nil {
		return err
	}
	parts := []message.ContentPart{message.TextContent{
		Text: fmt.Sprintf("Generate a concise title for the following content:\n\n%s", content),
	}}

	// Use streaming approach like summarization
	response := a.titleProvider.StreamResponse(
		ctx,
		[]message.Message{
			{
				Role:  message.User,
				Parts: parts,
			},
		},
		nil,
	)

	var finalResponse *provider.ProviderResponse
	for r := range response {
		if r.Error != nil {
			return r.Error
		}
		finalResponse = r.Response
	}

	if finalResponse == nil {
		return fmt.Errorf("no response received from title provider")
	}

	title := strings.TrimSpace(strings.ReplaceAll(finalResponse.Content, "\n", " "))
	if title == "" {
		return nil
	}

	session.Title = title
	_, err = a.sessions.Save(ctx, session)
	return err
}

func (a *agent) err(err error) AgentEvent {
	return AgentEvent{
		Type:  AgentEventTypeError,
		Error: err,
	}
}

func (a *agent) Run(ctx context.Context, sessionID string, content string, attachments ...message.Attachment) (<-chan AgentEvent, error) {
	if !a.Model().SupportsImages && attachments != nil {
		attachments = nil
	}
	events := make(chan AgentEvent)
	if a.IsSessionBusy(sessionID) {
		return nil, ErrSessionBusy
	}

	genCtx, cancel := context.WithCancel(ctx)

	a.activeRequests.Set(sessionID, cancel)
	go func() {
		slog.Debug("Request started", "sessionID", sessionID)
		defer log.RecoverPanic("agent.Run", func() {
			events <- a.err(fmt.Errorf("panic while running the agent"))
		})
		var attachmentParts []message.ContentPart
		for _, attachment := range attachments {
			attachmentParts = append(attachmentParts, message.BinaryContent{Path: attachment.FilePath, MIMEType: attachment.MimeType, Data: attachment.Content})
		}
		result := a.processGeneration(genCtx, sessionID, content, attachmentParts)
		if result.Error != nil && !errors.Is(result.Error, ErrRequestCancelled) && !errors.Is(result.Error, context.Canceled) {
			slog.Error(result.Error.Error())
		}
		slog.Debug("Request completed", "sessionID", sessionID)
		a.activeRequests.Del(sessionID)
		cancel()
		a.Publish(pubsub.CreatedEvent, result)
		events <- result
		close(events)
	}()
	return events, nil
}

func (a *agent) processGeneration(ctx context.Context, sessionID, content string, attachmentParts []message.ContentPart) AgentEvent {
	cfg := config.Get()
	// List existing messages; if none, start title generation asynchronously.
	msgs, err := a.messages.List(ctx, sessionID)
	if err != nil {
		return a.err(fmt.Errorf("failed to list messages: %w", err))
	}
	if len(msgs) == 0 {
		cfg := config.Get()
		if cfg.Options == nil || !cfg.Options.DisableTitleGeneration {
			go func() {
				defer log.RecoverPanic("agent.Run", func() {
					slog.Error("panic while generating title")
				})
				titleErr := a.generateTitle(context.Background(), sessionID, content)
				if titleErr != nil && !errors.Is(titleErr, context.Canceled) && !errors.Is(titleErr, context.DeadlineExceeded) {
					slog.Error("failed to generate title", "error", titleErr)
				}
			}()
		}
	}
	session, err := a.sessions.Get(ctx, sessionID)
	if err != nil {
		return a.err(fmt.Errorf("failed to get session: %w", err))
	}
	if session.SummaryMessageID != "" {
		summaryMsgInex := -1
		for i, msg := range msgs {
			if msg.ID == session.SummaryMessageID {
				summaryMsgInex = i
				break
			}
		}
		if summaryMsgInex != -1 {
			msgs = msgs[summaryMsgInex:]
			msgs[0].Role = message.User
		}
	}

	userMsg, err := a.createUserMessage(ctx, sessionID, content, attachmentParts)
	if err != nil {
		return a.err(fmt.Errorf("failed to create user message: %w", err))
	}
	// Append the new user message to the conversation history.
	msgHistory := append(msgs, userMsg)

	for {
		// Check for cancellation before each iteration
		select {
		case <-ctx.Done():
			return a.err(ctx.Err())
		default:
			// Continue processing
		}
		agentMessage, toolResults, err := a.streamAndHandleEvents(ctx, sessionID, msgHistory)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				agentMessage.AddFinish(message.FinishReasonCanceled, "Request cancelled", "")
				a.messages.Update(context.Background(), agentMessage)
				return a.err(ErrRequestCancelled)
			}
			return a.err(fmt.Errorf("failed to process events: %w", err))
		}
		if cfg.Options.Debug {
			slog.Info("Result", "message", agentMessage.FinishReason(), "toolResults", toolResults)
		}
		if (agentMessage.FinishReason() == message.FinishReasonToolUse) && toolResults != nil {
			// We are not done, we need to respond with the tool response
			msgHistory = append(msgHistory, agentMessage, *toolResults)
			continue
		}
		if agentMessage.FinishReason() == "" {
			// Kujtim: could not track down where this is happening but this means its cancelled
			agentMessage.AddFinish(message.FinishReasonCanceled, "Request cancelled", "")
			_ = a.messages.Update(context.Background(), agentMessage)
			return a.err(ErrRequestCancelled)
		}
		return AgentEvent{
			Type:    AgentEventTypeResponse,
			Message: agentMessage,
			Done:    true,
		}
	}
}

func (a *agent) createUserMessage(ctx context.Context, sessionID, content string, attachmentParts []message.ContentPart) (message.Message, error) {
	parts := []message.ContentPart{message.TextContent{Text: content}}
	parts = append(parts, attachmentParts...)
	return a.messages.Create(ctx, sessionID, message.CreateMessageParams{
		Role:  message.User,
		Parts: parts,
	})
}

func repairOrphanedToolCalls(msgs []message.Message) []message.Message {
	resultsByID := map[string]string{}
	for _, m := range msgs {
		if m.Role != message.Tool {
			continue
		}
		for _, tr := range m.ToolResults() {
			if tr.ToolCallID != "" {
				resultsByID[tr.ToolCallID] = tr.Content
			}
		}
	}
	out := make([]message.Message, 0, len(msgs)+2)
	for i := 0; i < len(msgs); i++ {
		m := msgs[i]
		out = append(out, m)
		if m.Role != message.Assistant || len(m.ToolCalls()) == 0 {
			continue
		}
		covered := 0
		if i+1 < len(msgs) && msgs[i+1].Role == message.Tool {
			idSet := map[string]bool{}
			for _, tc := range m.ToolCalls() {
				idSet[tc.ID] = true
			}
			for _, tr := range msgs[i+1].ToolResults() {
				if idSet[tr.ToolCallID] {
					covered++
				}
			}
		}
		if covered == len(m.ToolCalls()) {
			continue
		}
		var parts []message.ContentPart
		for _, tc := range m.ToolCalls() {
			if c, ok := resultsByID[tc.ID]; ok {
				parts = append(parts, message.ToolResult{ToolCallID: tc.ID, Content: c})
			} else {
				parts = append(parts, message.ToolResult{ToolCallID: tc.ID, Content: "Recovered from crash: tool output not available. Please re-issue this function call.", IsError: true, Recovered: true})
			}
		}
		out = append(out, message.Message{Role: message.Tool, Parts: parts})
	}
	return out
}

func projectForSummarization(msgs []message.Message) []message.Message {
	out := make([]message.Message, 0, len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case message.Assistant, message.User:
			parts := make([]message.ContentPart, 0, len(m.Parts))
			for _, p := range m.Parts {
				if _, ok := p.(message.ToolCall); ok {
					continue
				}
				parts = append(parts, p)
			}
			out = append(out, message.Message{Role: m.Role, Parts: parts})
		case message.Tool:
			var b strings.Builder
			for _, tr := range m.ToolResults() {
				if tr.Name != "" {
					b.WriteString("Tool ")
					b.WriteString(tr.Name)
					b.WriteString(" result (id=")
					b.WriteString(tr.ToolCallID)
					b.WriteString("):\n")
					b.WriteString(tr.Content)
					b.WriteString("\n\n")
				} else {
					b.WriteString("Tool result (id=")
					b.WriteString(tr.ToolCallID)
					b.WriteString("):\n")
					b.WriteString(tr.Content)
					b.WriteString("\n\n")
				}
			}
			if b.Len() > 0 {
				out = append(out, message.Message{Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: b.String()}}})
			}
		default:
			out = append(out, m)
		}
	}
	return out
}

func (a *agent) streamAndHandleEvents(ctx context.Context, sessionID string, msgHistory []message.Message) (message.Message, *message.Message, error) {
	ctx = context.WithValue(ctx, tools.SessionIDContextKey, sessionID)

	// Create the assistant message first so the spinner shows immediately
	assistantMsg, err := a.messages.Create(ctx, sessionID, message.CreateMessageParams{
		Role:     message.Assistant,
		Parts:    []message.ContentPart{},
		Model:    a.Model().ID,
		Provider: a.providerID,
	})
	slog.Info("agent: assistant message created", "session_id", sessionID, "message_id", assistantMsg.ID, "model", assistantMsg.Model, "provider", assistantMsg.Provider)
	if err != nil {
		return assistantMsg, nil, fmt.Errorf("failed to create assistant message: %w", err)
	}

	// Now collect tools (which may block on MCP initialization)
	repairedHistory := repairOrphanedToolCalls(msgHistory)
	// Attach a tool state sink into context so tools can stream state without changing BaseTool.Run signature yet.
	ctxWithSink := context.WithValue(ctx, tools.SessionIDContextKey, sessionID)
	ctxWithSink = context.WithValue(ctxWithSink, tools.MessageIDContextKey, assistantMsg.ID)
	// For now, provide a no-op sink to maintain plumbing; future PRs will supply a real sink impl and UI.
	ctxWithSink = tools.WithSink(ctxWithSink, &toolStateNoop{})

	eventChan := a.provider.StreamResponse(ctxWithSink, repairedHistory, slices.Collect(a.tools.Seq()))

	// Add the session and message ID into the context if needed by tools.
	ctx = context.WithValue(ctx, tools.MessageIDContextKey, assistantMsg.ID)

	// Process each event in the stream.
	for event := range eventChan {
		if processErr := a.processEvent(ctx, sessionID, &assistantMsg, event); processErr != nil {
			if errors.Is(processErr, context.Canceled) {
				a.finishMessage(context.Background(), &assistantMsg, message.FinishReasonCanceled, "Request cancelled", "")
			} else {
				a.finishMessage(ctx, &assistantMsg, message.FinishReasonError, "API Error", processErr.Error())
			}
			return assistantMsg, nil, processErr
		}
		if ctx.Err() != nil {
			a.finishMessage(context.Background(), &assistantMsg, message.FinishReasonCanceled, "Request cancelled", "")
			return assistantMsg, nil, ctx.Err()
		}
	}

	toolResults := make([]message.ToolResult, len(assistantMsg.ToolCalls()))
	toolCalls := assistantMsg.ToolCalls()
	for i, toolCall := range toolCalls {
		select {
		case <-ctx.Done():
			a.finishMessage(context.Background(), &assistantMsg, message.FinishReasonCanceled, "Request cancelled", "")
			// Make all future tool calls cancelled
			for j := i; j < len(toolCalls); j++ {
				toolResults[j] = message.ToolResult{
					ToolCallID: toolCalls[j].ID,
					Content:    "Tool execution canceled by user",
					IsError:    true,
				}
			}
			goto out
		default:
			// Continue processing
			var tool tools.BaseTool
			for availableTool := range a.tools.Seq() {
				if availableTool.Info().Name == toolCall.Name {
					tool = availableTool
					break
				}
			}

			// Tool not found
			if tool == nil {
				toolResults[i] = message.ToolResult{
					ToolCallID: toolCall.ID,
					Content:    fmt.Sprintf("Tool not found: %s", toolCall.Name),
					IsError:    true,
				}
				continue
			}

			// Run tool in goroutine to allow cancellation
			type toolExecResult struct {
				response tools.ToolResponse
				err      error
			}
			resultChan := make(chan toolExecResult, 1)

			sink := newToolStateSink(a, sessionID, assistantMsg.ID, toolCall.ID)
			go func() {
				ctxTool := tools.WithSink(ctx, sink)
				response, err := tool.Run(ctxTool, tools.ToolCall{
					ID:    toolCall.ID,
					Name:  toolCall.Name,
					Input: toolCall.Input,
				})
				if err != nil {
					sink.Error(err)
				}
				sink.Final(response)
				resultChan <- toolExecResult{response: response, err: err}
			}()

			var toolResponse tools.ToolResponse
			var toolErr error
			truncate := func(s string) string {
				lim := 0
				if cfg := config.Get(); cfg != nil && cfg.Options != nil && cfg.Options.MaxToolOutputBytes > 0 {
					lim = cfg.Options.MaxToolOutputBytes
				}
				if lim <= 0 {
					lim = 100 * 1024
				}
				if len(s) <= lim {
					return s
				}
				banner := "\n\n... [output truncated to %d bytes; %d lines omitted]\n\nConsider narrowing the command or query (use path/include filters, Glob+Grep, or save large output into a file)."
				// reserve space for banner
				b := fmt.Sprintf(banner, lim, 0)
				reserve := len(b)
				if reserve >= lim {
					// if banner itself exceeds lim, hard cut
					return s[:lim]
				}
				headTail := (lim - reserve) / 2
				start := s[:headTail]
				end := s[len(s)-headTail:]
				mid := s[headTail : len(s)-headTail]
				lines := 0
				for i := 0; i < len(mid); i++ {
					if mid[i] == '\n' {
						lines++
					}
				}
				return start + fmt.Sprintf(banner, lim, lines) + end
			}

			select {
			case <-ctx.Done():
				a.finishMessage(context.Background(), &assistantMsg, message.FinishReasonCanceled, "Request cancelled", "")
				// Mark remaining tool calls as cancelled
				for j := i; j < len(toolCalls); j++ {
					toolResults[j] = message.ToolResult{
						ToolCallID: toolCalls[j].ID,
						Content:    "Tool execution canceled by user",
						IsError:    true,
					}
				}
				goto out
			case result := <-resultChan:
				toolResponse = result.response
				toolErr = result.err
			}

			if toolErr != nil {
				slog.Error("Tool execution error", "toolCall", toolCall.ID, "error", toolErr)
				if errors.Is(toolErr, permission.ErrorPermissionDenied) {
					toolResults[i] = message.ToolResult{
						ToolCallID: toolCall.ID,
						Content:    "Permission denied",
						IsError:    true,
					}
					for j := i + 1; j < len(toolCalls); j++ {
						toolResults[j] = message.ToolResult{
							ToolCallID: toolCalls[j].ID,
							Content:    "Tool execution canceled by user",
							IsError:    true,
						}
					}
					a.finishMessage(ctx, &assistantMsg, message.FinishReasonPermissionDenied, "Permission denied", "")
					break
				}
			}
			toolResults[i] = message.ToolResult{
				ToolCallID: toolCall.ID,
				Content:    truncate(toolResponse.Content),
				Metadata:   toolResponse.Metadata,
				IsError:    toolResponse.IsError,
			}
		}
	}
out:
	if len(toolResults) == 0 {
		return assistantMsg, nil, nil
	}
	parts := make([]message.ContentPart, 0)
	for _, tr := range toolResults {
		parts = append(parts, tr)
	}
	msg, err := a.messages.Create(context.Background(), assistantMsg.SessionID, message.CreateMessageParams{
		Role:     message.Tool,
		Parts:    parts,
		Provider: a.providerID,
	})
	if err != nil {
		return assistantMsg, nil, fmt.Errorf("failed to create cancelled tool message: %w", err)
	}

	return assistantMsg, &msg, err
}

func (a *agent) finishMessage(ctx context.Context, msg *message.Message, finishReason message.FinishReason, message, details string) {
	msg.AddFinish(finishReason, message, details)
	_ = a.messages.Update(ctx, *msg)
}

func (a *agent) processEvent(ctx context.Context, sessionID string, assistantMsg *message.Message, event provider.ProviderEvent) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		// Continue processing.
	}

	switch event.Type {
	case provider.EventThinkingDelta:
		assistantMsg.AppendReasoningContent(event.Thinking)
		return a.messages.Update(ctx, *assistantMsg)
	case provider.EventSignatureDelta:
		assistantMsg.AppendReasoningSignature(event.Signature)
		return a.messages.Update(ctx, *assistantMsg)
	case provider.EventContentDelta:
		slog.Info("agent: content delta", "message_id", assistantMsg.ID, "delta_len", len(event.Content))
		assistantMsg.FinishThinking()
		assistantMsg.AppendContent(event.Content)
		if err := a.messages.Update(ctx, *assistantMsg); err != nil {
			return err
		}
		slog.Info("agent: content appended", "message_id", assistantMsg.ID, "text_len", len(assistantMsg.Content().Text))
		return nil
	case provider.EventToolUseStart:
		assistantMsg.FinishThinking()
		slog.Info("Tool call started", "toolCall", event.ToolCall)
		assistantMsg.AddToolCall(*event.ToolCall)
		return a.messages.Update(ctx, *assistantMsg)
	case provider.EventToolUseDelta:
		assistantMsg.AppendToolCallInput(event.ToolCall.ID, event.ToolCall.Input)
		return a.messages.Update(ctx, *assistantMsg)
	case provider.EventToolUseStop:
		slog.Info("Finished tool call", "toolCall", event.ToolCall)
		assistantMsg.FinishToolCall(event.ToolCall.ID)
		return a.messages.Update(ctx, *assistantMsg)
	case provider.EventError:
		return event.Error
	case provider.EventComplete:
		slog.Info("agent: complete event", "message_id", assistantMsg.ID, "content_len", len(event.Response.Content), "tools", len(event.Response.ToolCalls), "finish", event.Response.FinishReason)
		assistantMsg.FinishThinking()
		if event.Response != nil {
			if event.Response.Content != "" && assistantMsg.Content().Text == "" {
				assistantMsg.AppendContent(event.Response.Content)
			}
			// Persist reasoning parts when present
			if len(event.Response.ReasoningEnc) > 0 {
				for _, re := range event.Response.ReasoningEnc {
					assistantMsg.Parts = append(assistantMsg.Parts, re)
				}
			}
			if len(event.Response.ReasoningSumm) > 0 {
				for _, rs := range event.Response.ReasoningSumm {
					assistantMsg.Parts = append(assistantMsg.Parts, rs)
				}
			}
		}
		assistantMsg.SetToolCalls(event.Response.ToolCalls)
		assistantMsg.AddFinish(event.Response.FinishReason, "", "")
		if err := a.messages.Update(ctx, *assistantMsg); err != nil {
			return fmt.Errorf("failed to update message: %w", err)
		}
		slog.Info("agent: message finalized", "message_id", assistantMsg.ID, "text_len", len(assistantMsg.Content().Text))
		return a.TrackUsage(ctx, sessionID, a.Model(), event.Response.Usage)
	}

	return nil
}

func (a *agent) TrackUsage(ctx context.Context, sessionID string, model catwalk.Model, usage provider.TokenUsage) error {
	sess, err := a.sessions.Get(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("failed to get session: %w", err)
	}

	cost := model.CostPer1MInCached/1e6*float64(usage.CacheCreationTokens) +
		model.CostPer1MOutCached/1e6*float64(usage.CacheReadTokens) +
		model.CostPer1MIn/1e6*float64(usage.InputTokens) +
		model.CostPer1MOut/1e6*float64(usage.OutputTokens)

	sess.Cost += cost
	sess.CompletionTokens = usage.OutputTokens + usage.CacheReadTokens
	sess.PromptTokens = usage.InputTokens + usage.CacheCreationTokens

	_, err = a.sessions.Save(ctx, sess)
	if err != nil {
		return fmt.Errorf("failed to save session: %w", err)
	}
	return nil
}

func (a *agent) Summarize(ctx context.Context, sessionID string) error {
	if a.summarizeProvider == nil {
		return fmt.Errorf("summarize provider not available")
	}

	// Check if session is busy
	if a.IsSessionBusy(sessionID) {
		return ErrSessionBusy
	}

	// Create a new context with cancellation
	summarizeCtx, cancel := context.WithCancel(ctx)

	// Store the cancel function in activeRequests to allow cancellation
	a.activeRequests.Set(sessionID+"-summarize", cancel)

	go func() {
		defer a.activeRequests.Del(sessionID + "-summarize")
		defer cancel()
		event := AgentEvent{
			Type:      AgentEventTypeSummarize,
			SessionID: sessionID,
			Progress:  "Starting summarization...",
		}

		a.Publish(pubsub.CreatedEvent, event)
		// Get all messages from the session
		msgs, err := a.messages.List(summarizeCtx, sessionID)
		if err != nil {
			event = AgentEvent{
				Type:      AgentEventTypeError,
				SessionID: sessionID,
				Error:     fmt.Errorf("failed to list messages: %w", err),
				Done:      true,
			}
			a.Publish(pubsub.CreatedEvent, event)
			return
		}
		summarizeCtx = context.WithValue(summarizeCtx, tools.SessionIDContextKey, sessionID)

		if len(msgs) == 0 {
			event = AgentEvent{
				Type:      AgentEventTypeError,
				SessionID: sessionID,
				Error:     fmt.Errorf("no messages to summarize"),
				Done:      true,
			}
			a.Publish(pubsub.CreatedEvent, event)
			return
		}

		event = AgentEvent{
			Type:      AgentEventTypeSummarize,
			SessionID: sessionID,
			Progress:  "Preparing summarization prompt...",
		}
		a.Publish(pubsub.CreatedEvent, event)

		// Add a system message to guide the summarization
		summarizePrompt := "Provide a detailed, concise summary of our conversation that preserves key context required to continue the work: what we did, what we're doing, files touched, decisions, next steps."

		// Create a new message with the summarize prompt
		promptMsg := message.Message{
			Role:  message.User,
			Parts: []message.ContentPart{message.TextContent{Text: summarizePrompt}},
		}

		// Append the prompt to the messages
		clean := projectForSummarization(msgs)
		msgsWithPrompt := append(clean, promptMsg)

		event = AgentEvent{
			Type:      AgentEventTypeSummarize,
			SessionID: sessionID,
			Progress:  "Summarizing conversation...",
		}

		a.Publish(pubsub.CreatedEvent, event)

		// Send the messages to the summarize provider
		response := a.summarizeProvider.StreamResponse(
			summarizeCtx,
			msgsWithPrompt,
			nil,
		)
		var finalResponse *provider.ProviderResponse
		var partial strings.Builder
		for r := range response {
			if r.Error != nil {
				event = AgentEvent{
					Type:      AgentEventTypeError,
					SessionID: sessionID,
					Error:     fmt.Errorf("failed to summarize: %w", r.Error),
					Done:      true,
				}
				a.Publish(pubsub.CreatedEvent, event)
				return
			}
			switch r.Type {
			case provider.EventContentDelta:
				partial.WriteString(r.Content)
				cur := partial.String()
				a.Publish(pubsub.CreatedEvent, AgentEvent{
					Type:        AgentEventTypeSummarize,
					SessionID:   sessionID,
					Progress:    "Summarizing conversation...",
					PreviewTail: tail(cur, 160),
					TotalBytes:  len(cur),
					TotalRunes:  utf8.RuneCountInString(cur),
				})
			case provider.EventComplete:
				finalResponse = r.Response
			default:
				// ignore other event types
			}
		}

		summary := strings.TrimSpace(finalResponse.Content)
		if summary == "" {
			event = AgentEvent{
				Type:      AgentEventTypeError,
				SessionID: sessionID,
				Error:     fmt.Errorf("empty summary returned"),
				Done:      true,
			}
			a.Publish(pubsub.CreatedEvent, event)
			return
		}
		shell := shell.GetPersistentShell(config.Get().WorkingDir())
		summary += "\n\n**Current working directory of the persistent shell**\n\n" + shell.GetWorkingDir()
		event = AgentEvent{
			Type:      AgentEventTypeSummarize,
			SessionID: sessionID,
			Progress:  "Saving summary...",
		}

		a.Publish(pubsub.CreatedEvent, event)
		oldSession, err := a.sessions.Get(summarizeCtx, sessionID)
		if err != nil {
			event = AgentEvent{
				Type:      AgentEventTypeError,
				SessionID: sessionID,
				Error:     fmt.Errorf("failed to get session: %w", err),
				Done:      true,
			}

			a.Publish(pubsub.CreatedEvent, event)
			return
		}
		// Create a message in the new session with the summary
		msg, err := a.messages.Create(summarizeCtx, oldSession.ID, message.CreateMessageParams{
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.TextContent{Text: summary},
				message.Finish{
					Reason: message.FinishReasonEndTurn,
					Time:   time.Now().Unix(),
				},
			},
			Model:    a.summarizeProvider.Model().ID,
			Provider: a.summarizeProviderID,
		})
		if err != nil {
			event = AgentEvent{
				Type:      AgentEventTypeError,
				SessionID: sessionID,
				Error:     fmt.Errorf("failed to create summary message: %w", err),
				Done:      true,
			}

			a.Publish(pubsub.CreatedEvent, event)
			return
		}
		oldSession.SummaryMessageID = msg.ID
		oldSession.CompletionTokens = finalResponse.Usage.OutputTokens
		oldSession.PromptTokens = 0
		model := a.summarizeProvider.Model()
		usage := finalResponse.Usage
		cost := model.CostPer1MInCached/1e6*float64(usage.CacheCreationTokens) +
			model.CostPer1MOutCached/1e6*float64(usage.CacheReadTokens) +
			model.CostPer1MIn/1e6*float64(usage.InputTokens) +
			model.CostPer1MOut/1e6*float64(usage.OutputTokens)
		oldSession.Cost += cost
		_, err = a.sessions.Save(summarizeCtx, oldSession)
		if err != nil {
			event = AgentEvent{
				Type:      AgentEventTypeError,
				SessionID: sessionID,
				Error:     fmt.Errorf("failed to save session: %w", err),
				Done:      true,
			}
			a.Publish(pubsub.CreatedEvent, event)
		}

		event = AgentEvent{
			Type:      AgentEventTypeSummarize,
			SessionID: oldSession.ID,
			Progress:  "Summary complete",
			Done:      true,
		}
		a.Publish(pubsub.CreatedEvent, event)
		// Send final success event with the new session ID
	}()

	return nil
}

func (a *agent) CancelAll() {
	if !a.IsBusy() {
		return
	}
	for key := range a.activeRequests.Seq2() {
		a.Cancel(key) // key is sessionID
	}

	timeout := time.After(5 * time.Second)
	for a.IsBusy() {
		select {
		case <-timeout:
			return
		default:
			time.Sleep(200 * time.Millisecond)
		}
	}
}

func tail(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[len(r)-maxRunes:])
}

func (a *agent) UpdateModel() error {
	cfg := config.Get()

	// Get current provider configuration
	currentProviderCfg := cfg.GetProviderForModel(a.agentCfg.Model)
	if currentProviderCfg == nil || currentProviderCfg.ID == "" {
		return fmt.Errorf("provider for agent %s not found in config", a.agentCfg.Name)
	}

	// Check if provider has changed
	if string(currentProviderCfg.ID) != a.providerID {
		// Provider changed, need to recreate the main provider
		model := cfg.GetModelByType(a.agentCfg.Model)
		if model.ID == "" {
			return fmt.Errorf("model not found for agent %s", a.agentCfg.Name)
		}

		promptID := agentPromptMap[a.agentCfg.ID]
		if promptID == "" {
			promptID = prompt.PromptDefault
		}

		opts := []provider.ProviderClientOption{
			provider.WithModel(a.agentCfg.Model),
			provider.WithSystemMessage(prompt.GetPrompt(promptID, currentProviderCfg.ID, cfg.Options.ContextPaths...)),
		}

		newProvider, err := provider.NewProvider(*currentProviderCfg, opts...)
		if err != nil {
			return fmt.Errorf("failed to create new provider: %w", err)
		}

		// Update the provider and provider ID
		a.provider = newProvider
		a.providerID = string(currentProviderCfg.ID)
	}

	// Check if providers have changed for title (small) and summarize (large)
	smallModelCfg := cfg.Models[config.SelectedModelTypeSmall]
	var smallModelProviderCfg config.ProviderConfig
	for p := range cfg.Providers.Seq() {
		if p.ID == smallModelCfg.Provider {
			smallModelProviderCfg = p
			break
		}
	}
	if smallModelProviderCfg.ID == "" {
		return fmt.Errorf("provider %s not found in config", smallModelCfg.Provider)
	}

	largeModelCfg := cfg.Models[config.SelectedModelTypeLarge]
	var largeModelProviderCfg config.ProviderConfig
	for p := range cfg.Providers.Seq() {
		if p.ID == largeModelCfg.Provider {
			largeModelProviderCfg = p
			break
		}
	}
	if largeModelProviderCfg.ID == "" {
		return fmt.Errorf("provider %s not found in config", largeModelCfg.Provider)
	}

	// Recreate title provider
	titleOpts := []provider.ProviderClientOption{
		provider.WithModel(config.SelectedModelTypeSmall),
		provider.WithSystemMessage(prompt.GetPrompt(prompt.PromptTitle, smallModelProviderCfg.ID)),
		provider.WithMaxTokens(40),
	}
	newTitleProvider, err := provider.NewProvider(smallModelProviderCfg, titleOpts...)
	if err != nil {
		return fmt.Errorf("failed to create new title provider: %w", err)
	}
	a.titleProvider = newTitleProvider

	// Recreate summarize provider if provider changed (now large model)
	if string(largeModelProviderCfg.ID) != a.summarizeProviderID {
		largeModel := cfg.GetModelByType(config.SelectedModelTypeLarge)
		if largeModel == nil {
			return fmt.Errorf("model %s not found in provider %s", largeModelCfg.Model, largeModelProviderCfg.ID)
		}
		summarizeOpts := []provider.ProviderClientOption{
			provider.WithModel(config.SelectedModelTypeLarge),
			provider.WithSystemMessage(prompt.GetPrompt(prompt.PromptSummarizer, largeModelProviderCfg.ID)),
		}
		newSummarizeProvider, err := provider.NewProvider(largeModelProviderCfg, summarizeOpts...)
		if err != nil {
			return fmt.Errorf("failed to create new summarize provider: %w", err)
		}
		a.summarizeProvider = newSummarizeProvider
		a.summarizeProviderID = string(largeModelProviderCfg.ID)
	}

	return nil
}
