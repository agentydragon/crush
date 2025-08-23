package cmd

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"

	tea "github.com/charmbracelet/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/app"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/db"
	"github.com/charmbracelet/crush/internal/tui"
	"github.com/charmbracelet/crush/internal/version"
	"github.com/charmbracelet/fang"
	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"
	"github.com/charmbracelet/crush/internal/profile"
	"github.com/charmbracelet/crush/internal/logging"
)

func init() {
	rootCmd.PersistentFlags().StringP("cwd", "c", "", "Current working directory")
	rootCmd.PersistentFlags().BoolP("debug", "d", false, "Debug")
	rootCmd.PersistentFlags().Bool("dump-config", false, "Print the effective configuration (redacted) and exit")

	rootCmd.Flags().BoolP("help", "h", false, "Help")
	rootCmd.Flags().BoolP("yolo", "y", false, "Automatically accept all permissions (dangerous mode)")

	rootCmd.AddCommand(runCmd)
}

var rootCmd = &cobra.Command{
	Use:   "crush",
	Short: "Terminal-based AI assistant for software development",
	Long: `Crush is a powerful terminal-based AI assistant that helps with software development tasks.
It provides an interactive chat interface with AI capabilities, code analysis, and LSP integration
to assist developers in writing, debugging, and understanding code directly from the terminal.`,
	Example: `
# Run in interactive mode
crush

# Run with debug logging
crush -d

# Run with debug logging in a specific directory
crush -d -c /path/to/project

# Print version
crush -v

# Run a single non-interactive prompt
crush run "Explain the use of context in Go"

# Run in dangerous mode (auto-accept all permissions)
crush -y
  `,
	RunE: func(cmd *cobra.Command, args []string) error {
		if dump, _ := cmd.Flags().GetBool("dump-config"); dump {
			cwd, err := ResolveCwd(cmd)
			if err != nil {
				return err
			}
			debug, _ := cmd.Flags().GetBool("debug")
			if _, err := config.Init(cwd, debug); err != nil {
				return err
			}
			// Print layering trace to stderr first
			cfg := config.Get()
			fmt.Fprintln(os.Stderr, "Configuration load sequence:")
			for _, p := range cfg.LoadPathsConsidered {
				mark := "-"
				if slices.Contains(cfg.LoadPathsLoaded, p) {
					mark = "+"
				}
				fmt.Fprintf(os.Stderr, "  [%s] %s\n", mark, p)
			}
			bts, err := cfg.EffectiveJSON(true)
			if err != nil {
				return err
			}
			fmt.Println(string(bts))
			return nil
		}

		app, err := setupApp(cmd)
		if err != nil {
			return err
		}
		defer app.Shutdown()

		// Start pprof in debug mode (or if CRUSH_PROFILE set); shows in status bar.
		if app.Config().Options != nil {
			profile.MaybeStart(app.Config().Options.Debug)
		}

		// Set up the TUI.
		program := tea.NewProgram(
			tui.New(app),
			tea.WithAltScreen(),
			tea.WithContext(cmd.Context()),
			tea.WithMouseCellMotion(),            // Use cell motion instead of all motion to reduce event flooding
			tea.WithFilter(tui.MouseEventFilter), // Filter mouse events based on focus state
		)

		go app.Subscribe(program)

		if _, err := program.Run(); err != nil {
			slog.Error("TUI run error", "error", err)
			return fmt.Errorf("TUI error: %v", err)
		}
		return nil
	},
}

func Execute() {
	if err := fang.Execute(
		context.Background(),
		rootCmd,
		fang.WithVersion(version.Version),
		fang.WithNotifySignal(os.Interrupt),
	); err != nil {
		os.Exit(1)
	}
}

// setupApp handles the common setup logic for both interactive and non-interactive modes.
// It returns the app instance, config, cleanup function, and any error.
func setupApp(cmd *cobra.Command) (*app.App, error) {
	debug, _ := cmd.Flags().GetBool("debug")
	yolo, _ := cmd.Flags().GetBool("yolo")
	ctx := cmd.Context()

	cwd, err := ResolveCwd(cmd)
	if err != nil {
		return nil, err
	}

	// Initialize logging once using the final data directory; resolve after config.Init sets defaults.
	cfg, err := config.Init(cwd, debug)
	if err != nil { return nil, err }
	dataDir := cfg.Options.DataDirectory
	if _, logInitErr := logging.NewLoggerPlatform(logging.LoggingConfig{
		Level:      func() slog.Level { if debug { return slog.LevelDebug }; return slog.LevelInfo }(),
		AppLogPath: dataDir + "/logs/crush.log",
		Console:    false, // prevent slog JSON to stderr; avoid clobbering TUI
		JSON:       true,
		WireLogs: []logging.WireSinkConfig{
			{Name: "mcp", Path: dataDir + "/logs/mcp/mcp-wire.log", Rotate: logging.RotationConfig{MaxSizeMB:250, MaxBackups:10, MaxAgeDays:30, Compress:true}},
			{Name: "provider", Path: dataDir + "/logs/provider/provider-wire.log", Rotate: logging.RotationConfig{MaxSizeMB:250, MaxBackups:10, MaxAgeDays:30, Compress:true}},
		},
	}); logInitErr != nil {
		return nil, logInitErr
	}
	if cfg.Options != nil && cfg.Options.Debug {
		// Force-enable provider/MCP wire logs in debug
		cfg.Options.DebugProviderWire = true
		if cfg.Options.Wire == nil {
			cfg.Options.Wire = &config.WireOptions{}
		}
	}

	if cfg.Permissions == nil {
		cfg.Permissions = &config.Permissions{}
	}
	cfg.Permissions.SkipRequests = yolo

	// Connect to DB; this will also run migrations.
	conn, err := db.Connect(ctx, cfg.Options.DataDirectory)
	if err != nil {
		return nil, err
	}

	appInstance, err := app.New(ctx, conn, cfg)
	if err != nil {
		slog.Error("Failed to create app instance", "error", err)
		return nil, err
	}

	return appInstance, nil
}

func MaybePrependStdin(prompt string) (string, error) {
	if term.IsTerminal(os.Stdin.Fd()) {
		return prompt, nil
	}
	fi, err := os.Stdin.Stat()
	if err != nil {
		return prompt, err
	}
	if fi.Mode()&os.ModeNamedPipe == 0 {
		return prompt, nil
	}
	bts, err := io.ReadAll(os.Stdin)
	if err != nil {
		return prompt, err
	}
	return string(bts) + "\n\n" + prompt, nil
}

func ResolveCwd(cmd *cobra.Command) (string, error) {
	cwd, _ := cmd.Flags().GetString("cwd")
	if cwd != "" {
		err := os.Chdir(cwd)
		if err != nil {
			return "", fmt.Errorf("failed to change directory: %v", err)
		}
		return cwd, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to get current working directory: %v", err)
	}
	return cwd, nil
}
