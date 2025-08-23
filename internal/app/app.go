package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/db"
	"github.com/charmbracelet/crush/internal/format"
	"github.com/charmbracelet/crush/internal/history"
	"github.com/charmbracelet/crush/internal/llm/agent"
	"github.com/charmbracelet/crush/internal/logging"
	"github.com/charmbracelet/crush/internal/pubsub"

	"github.com/charmbracelet/crush/internal/lsp"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/session"
	"gopkg.in/natefinch/lumberjack.v2"
)

type App struct {
	Sessions    session.Service
	Messages    message.Service
	History     history.Service
	Permissions permission.Service

	CoderAgent agent.Service

	LSPClients map[string]*lsp.Client

	clientsMutex sync.RWMutex

	watcherCancelFuncs *csync.Slice[context.CancelFunc]
	lspWatcherWG       sync.WaitGroup

	config *config.Config
	db     *sql.DB

	serviceEventsWG *sync.WaitGroup
	eventsCtx       context.Context
	events          chan tea.Msg
	tuiWG           *sync.WaitGroup

	// global context and cleanup functions
	globalCtx    context.Context
	cleanupFuncs []func()

	// debug UI logger (JSONL), enabled when options.debug
	uiLogger *lumberjack.Logger
}

// New initializes a new applcation instance.
func New(ctx context.Context, conn *sql.DB, cfg *config.Config) (*App, error) {
	// Set pubsub broker buffer size early so all services use it
	if cfg != nil && cfg.Options != nil && cfg.Options.BrokerBufferSize > 0 {
		pubsub.SetDefaultBufferSize(cfg.Options.BrokerBufferSize)
		slog.Info("pubsub.buffer", "default_size", cfg.Options.BrokerBufferSize)
	}
	q := db.New(conn)
	sessions := session.NewService(q)
	baseMessages := message.NewService(q)
	// Serialize writes per session to guarantee ordering across assistant updates
	// and tool results, then debounce high-frequency deltas before enqueuing.
	serialized := agent.NewSessionSerializedMessageService(baseMessages)
	messages := agent.NewDebouncedMessageService(serialized, 30*time.Millisecond)
	files := history.NewService(q, conn)
	skipPermissionsRequests := cfg.Permissions != nil && cfg.Permissions.SkipRequests
	allowedTools := []string{}
	if cfg.Permissions != nil && cfg.Permissions.AllowedTools != nil {
		allowedTools = cfg.Permissions.AllowedTools
	}

	app := &App{
		Sessions:    sessions,
		Messages:    messages,
		History:     files,
		Permissions: permission.NewPermissionService(cfg.WorkingDir(), skipPermissionsRequests, allowedTools),
		LSPClients:  make(map[string]*lsp.Client),

		globalCtx: ctx,

		config: cfg,
		db:     conn,

		watcherCancelFuncs: csync.NewSlice[context.CancelFunc](),

		// Increase UI event queue to reduce drops under bursty traffic
		events:          make(chan tea.Msg, 1000),
		serviceEventsWG: &sync.WaitGroup{},
		tuiWG:           &sync.WaitGroup{},
	}

	// Initialize UI event logger when debug is enabled
	if cfg.Options != nil && cfg.Options.Debug {
		_ = os.MkdirAll(filepath.Join(cfg.Options.DataDirectory, "logs", "ui"), 0o755)
		maxSize := 250
		maxBackups := 10
		maxAge := 30
		compress := true
		if cfg.Options.Wire != nil {
			if cfg.Options.Wire.MaxSizeMB > 0 {
				maxSize = cfg.Options.Wire.MaxSizeMB
			}
			if cfg.Options.Wire.MaxBackups > 0 {
				maxBackups = cfg.Options.Wire.MaxBackups
			}
			if cfg.Options.Wire.MaxAgeDays > 0 {
				maxAge = cfg.Options.Wire.MaxAgeDays
			}
			if cfg.Options.Wire.Compress != nil {
				compress = *cfg.Options.Wire.Compress
			}
		}
		app.uiLogger = &lumberjack.Logger{Filename: filepath.Join(cfg.Options.DataDirectory, "logs", "ui", "ui.log"), MaxSize: maxSize, MaxBackups: maxBackups, MaxAge: maxAge, Compress: compress}
	}

	app.setupEvents()

	// Initialize LSP clients in the background.
	app.initLSPClients(ctx)

	// Start MCP clients early so TUI shows live status before any message is sent.
	if cfg.IsConfigured() {
		go func() {
			if err := agent.DefaultMCPManager().StartAll(ctx, app.Permissions, cfg); err != nil {
				slog.Error("mcp.start_all", "error", err)
			}
		}()
	}

	// TODO: remove the concept of agent config, most likely.
	if cfg.IsConfigured() {
		if err := app.InitCoderAgent(); err != nil {
			return nil, fmt.Errorf("failed to initialize coder agent: %w", err)
		}
	} else {
		slog.Warn("No agent configuration found")
	}
	return app, nil
}

// Config returns the application configuration.
func (app *App) Config() *config.Config {
	return app.config
}

// RunNonInteractive handles the execution flow when a prompt is provided via
// CLI flag.
func (app *App) RunNonInteractive(ctx context.Context, prompt string, quiet bool) error {
	slog.Info("Running in non-interactive mode")

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Start spinner if not in quiet mode.
	var spinner *format.Spinner
	if !quiet {
		spinner = format.NewSpinner(ctx, cancel, "Generating")
		spinner.Start()
	}

	// Helper function to stop spinner once.
	stopSpinner := func() {
		if !quiet && spinner != nil {
			spinner.Stop()
			spinner = nil
		}
	}
	defer stopSpinner()

	const maxPromptLengthForTitle = 100
	titlePrefix := "Non-interactive: "
	var titleSuffix string

	if len(prompt) > maxPromptLengthForTitle {
		titleSuffix = prompt[:maxPromptLengthForTitle] + "..."
	} else {
		titleSuffix = prompt
	}
	title := titlePrefix + titleSuffix

	sess, err := app.Sessions.Create(ctx, title)
	if err != nil {
		return fmt.Errorf("failed to create session for non-interactive mode: %w", err)
	}
	slog.Info("Created session for non-interactive run", "session_id", sess.ID)

	// Automatically approve all permission requests for this non-interactive session
	app.Permissions.AutoApproveSession(sess.ID)

	done, err := app.CoderAgent.Run(ctx, sess.ID, prompt)
	if err != nil {
		return fmt.Errorf("failed to start agent processing stream: %w", err)
	}

	messageEvents := app.Messages.Subscribe(ctx)
	readBts := 0

	for {
		select {
		case result := <-done:
			stopSpinner()

			if result.Error != nil {
				if errors.Is(result.Error, context.Canceled) || errors.Is(result.Error, agent.ErrRequestCancelled) {
					slog.Info("Non-interactive: agent processing cancelled", "session_id", sess.ID)
					return nil
				}
				return fmt.Errorf("agent processing failed: %w", result.Error)
			}

			msgContent := result.Message.Content().String()
			if len(msgContent) < readBts {
				slog.Error("Non-interactive: message content is shorter than read bytes", "message_length", len(msgContent), "read_bytes", readBts)
				return fmt.Errorf("message content is shorter than read bytes: %d < %d", len(msgContent), readBts)
			}
			fmt.Println(msgContent[readBts:])

			slog.Info("Non-interactive: run completed", "session_id", sess.ID)
			return nil

		case event := <-messageEvents:
			msg := event.Payload
			if msg.SessionID == sess.ID && msg.Role == message.Assistant && len(msg.Parts) > 0 {
				stopSpinner()
				part := msg.Content().String()[readBts:]
				fmt.Print(part)
				readBts += len(part)
			}

		case <-ctx.Done():
			stopSpinner()
			return ctx.Err()
		}
	}
}

func (app *App) UpdateAgentModel() error {
	return app.CoderAgent.UpdateModel()
}

func (app *App) setupEvents() {
	ctx, cancel := context.WithCancel(app.globalCtx)
	app.eventsCtx = ctx
	setupSubscriber(ctx, app.serviceEventsWG, "sessions", app.Sessions.Subscribe, app.events, app.uiLogger)
	setupSubscriber(ctx, app.serviceEventsWG, "messages", app.Messages.Subscribe, app.events, app.uiLogger)
	setupSubscriber(ctx, app.serviceEventsWG, "permissions", app.Permissions.Subscribe, app.events, app.uiLogger)
	setupSubscriber(ctx, app.serviceEventsWG, "permissions-notifications", app.Permissions.SubscribeNotifications, app.events, app.uiLogger)
	setupSubscriber(ctx, app.serviceEventsWG, "history", app.History.Subscribe, app.events, app.uiLogger)
	setupSubscriber(ctx, app.serviceEventsWG, "mcp", agent.SubscribeMCPEvents, app.events, app.uiLogger)
	setupSubscriber(ctx, app.serviceEventsWG, "lsp", SubscribeLSPEvents, app.events, app.uiLogger)
	cleanupFunc := func() {
		cancel()
		app.serviceEventsWG.Wait()
	}
	app.cleanupFuncs = append(app.cleanupFuncs, cleanupFunc)
}

func setupSubscriber[T any](
	ctx context.Context,
	wg *sync.WaitGroup,
	name string,
	subscriber func(context.Context) <-chan pubsub.Event[T],
	outputCh chan<- tea.Msg,
	uiLogger *lumberjack.Logger,
) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		subCh := subscriber(ctx)
		for {
			select {
			case event, ok := <-subCh:
				if !ok {
					slog.Debug("subscription channel closed", "name", name)
					return
				}
				if uiLogger != nil {
					entry := map[string]any{
						"ts":      time.Now().UTC().Format(time.RFC3339Nano),
						"unix_ms": time.Now().UTC().UnixMilli(),
						"topic":   name,
						"type":    string(event.Type),
						"payload": event.Payload,
					}
					if b, err := json.Marshal(entry); err == nil {
						_, _ = uiLogger.Write(append(b, '\n'))
					}
				}
				var msg tea.Msg = event
				select {
				case outputCh <- msg:
				case <-time.After(2 * time.Second):
					slog.Warn("message dropped due to slow consumer", "name", name)
					// Derive a more specific topic when possible (e.g., mcp:<server>)
					topic := name
					if name == "mcp" {
						v := reflect.ValueOf(event.Payload)
						if v.Kind() == reflect.Struct {
							if f := v.FieldByName("Name"); f.IsValid() && f.Kind() == reflect.String {
								if s, ok := f.Interface().(string); ok && s != "" {
									topic = "mcp:" + s
								}
							}
						}
					}
					pubsub.IncDrop(topic, "slow_consumer")
				case <-ctx.Done():
					slog.Debug("subscription cancelled", "name", name)
					return
				}
			case <-ctx.Done():
				slog.Debug("subscription cancelled", "name", name)
				return
			}
		}
	}()
}

func (app *App) InitCoderAgent() error {
	coderAgentCfg := app.config.Agents["coder"]
	if coderAgentCfg.ID == "" {
		return fmt.Errorf("coder agent configuration is missing")
	}
	var err error
	app.CoderAgent, err = agent.NewAgent(
		app.globalCtx,
		coderAgentCfg,
		app.Permissions,
		app.Sessions,
		app.Messages,
		app.History,
		app.LSPClients,
	)
	if err != nil {
		slog.Error("Failed to create coder agent", "err", err)
		return err
	}

	// Add MCP client cleanup to shutdown process
	app.cleanupFuncs = append(app.cleanupFuncs, agent.CloseMCPClients)

	setupSubscriber(app.eventsCtx, app.serviceEventsWG, "coderAgent", app.CoderAgent.Subscribe, app.events, app.uiLogger)
	return nil
}

// Subscribe sends events to the TUI as tea.Msgs.
func (app *App) Subscribe(program *tea.Program) {
	defer logging.RecoverPanic("app.Subscribe", func() {
		slog.Info("TUI subscription panic: attempting graceful shutdown")
		program.Quit()
	})

	app.tuiWG.Add(1)
	tuiCtx, tuiCancel := context.WithCancel(app.globalCtx)
	app.cleanupFuncs = append(app.cleanupFuncs, func() {
		slog.Debug("Cancelling TUI message handler")
		tuiCancel()
		app.tuiWG.Wait()
	})
	defer app.tuiWG.Done()

	for {
		select {
		case <-tuiCtx.Done():
			slog.Debug("TUI message handler shutting down")
			return
		case msg, ok := <-app.events:
			if !ok {
				slog.Debug("TUI message channel closed")
				return
			}
			program.Send(msg)
		}
	}
}

// Shutdown performs a graceful shutdown of the application.
func (app *App) Shutdown() {
	if app.CoderAgent != nil {
		app.CoderAgent.CancelAll()
	}

	for cancel := range app.watcherCancelFuncs.Seq() {
		cancel()
	}

	// Wait for all LSP watchers to finish.
	app.lspWatcherWG.Wait()

	// Get all LSP clients.
	app.clientsMutex.RLock()
	clients := make(map[string]*lsp.Client, len(app.LSPClients))
	maps.Copy(clients, app.LSPClients)
	app.clientsMutex.RUnlock()

	// Shutdown all LSP clients.
	for name, client := range clients {
		shutdownCtx, cancel := context.WithTimeout(app.globalCtx, 5*time.Second)
		if err := client.Shutdown(shutdownCtx); err != nil {
			slog.Error("Failed to shutdown LSP client", "name", name, "error", err)
		}
		cancel()
	}

	// Call call cleanup functions.
	for _, cleanup := range app.cleanupFuncs {
		if cleanup != nil {
			cleanup()
		}
	}

	// Close DB connection last.
	if app.db != nil {
		if err := app.db.Close(); err != nil {
			slog.Error("Failed to close DB", "error", err)
		}
	}
}
