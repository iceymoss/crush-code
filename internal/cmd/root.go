package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/crush/internal/app"
	"github.com/charmbracelet/crush/internal/client"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/db"
	"github.com/charmbracelet/crush/internal/event"
	"github.com/charmbracelet/crush/internal/log"
	"github.com/charmbracelet/crush/internal/projects"
	"github.com/charmbracelet/crush/internal/proto"
	"github.com/charmbracelet/crush/internal/server"
	"github.com/charmbracelet/crush/internal/ui/common"
	ui "github.com/charmbracelet/crush/internal/ui/model"
	"github.com/charmbracelet/crush/internal/version"
	"github.com/charmbracelet/fang"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/charmtone"
	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"
)

var clientHost string

func init() {
	rootCmd.PersistentFlags().StringP("cwd", "c", "", "Current working directory")
	rootCmd.PersistentFlags().StringP("data-dir", "D", "", "Custom crush data directory")
	rootCmd.PersistentFlags().BoolP("debug", "d", false, "Debug")
	rootCmd.Flags().BoolP("help", "h", false, "Help")
	rootCmd.Flags().BoolP("yolo", "y", false, "Automatically accept all permissions (dangerous mode)")

	rootCmd.Flags().StringVarP(&clientHost, "host", "H", server.DefaultHost(), "Connect to a specific crush server host (for advanced users)")

	rootCmd.AddCommand(
		runCmd,
		dirsCmd,
		projectsCmd,
		updateProvidersCmd,
		logsCmd,
		schemaCmd,
		loginCmd,
		statsCmd,
	)
}

var rootCmd = &cobra.Command{
	Use:   "crush",
	Short: "A terminal-first AI assistant for software development",
	Long:  "A glamorous, terminal-first AI assistant for software development and adjacent tasks",
	Example: `
# Run in interactive mode
crush

# Run non-interactively
crush run "Guess my 5 favorite Pokémon"

# Run a non-interactively with pipes and redirection
cat README.md | crush run "make this more glamorous" > GLAMOROUS_README.md

# Run with debug logging in a specific directory
crush --debug --cwd /path/to/project

# Run in yolo mode (auto-accept all permissions; use with care)
crush --yolo

# Run with custom data directory
crush --data-dir /path/to/custom/.crush
  `,
	RunE: func(cmd *cobra.Command, args []string) error {
		hostURL, err := server.ParseHostURL(clientHost)
		if err != nil {
			return fmt.Errorf("invalid host URL: %v", err)
		}

		if err := ensureServer(cmd, hostURL); err != nil {
			return err
		}

		appInstance, err := setupAppWithProgressBar(cmd)
		if err != nil {
			return err
		}
		defer appInstance.Shutdown()

		// Register the workspace with the server so it tracks active
		// clients and auto-shuts down when the last one exits.
		cwd, _ := ResolveCwd(cmd)
		dataDir, _ := cmd.Flags().GetString("data-dir")
		debug, _ := cmd.Flags().GetBool("debug")
		yolo, _ := cmd.Flags().GetBool("yolo")

		c, err := client.NewClient(cwd, hostURL.Scheme, hostURL.Host)
		if err != nil {
			return fmt.Errorf("failed to create client: %v", err)
		}

		ws, err := c.CreateWorkspace(cmd.Context(), proto.Workspace{
			Path:    cwd,
			DataDir: dataDir,
			Debug:   debug,
			YOLO:    yolo,
			Version: version.Version,
		})
		if err != nil {
			return fmt.Errorf("failed to register workspace: %v", err)
		}
		defer func() { _ = c.DeleteWorkspace(cmd.Context(), ws.ID) }()

		event.AppInitialized()

		// Set up the TUI.
		var env uv.Environ = os.Environ()

		com := common.DefaultCommon(appInstance)
		model := ui.New(com)

		program := tea.NewProgram(
			model,
			tea.WithEnvironment(env),
			tea.WithContext(cmd.Context()),
			tea.WithFilter(ui.MouseEventFilter), // Filter mouse events based on focus state
		)
		go appInstance.Subscribe(program)

		if _, err := program.Run(); err != nil {
			event.Error(err)
			slog.Error("TUI run error", "error", err)
			return errors.New("Crush crashed. If metrics are enabled, we were notified about it. If you'd like to report it, please copy the stacktrace above and open an issue at https://github.com/charmbracelet/crush/issues/new?template=bug.yml") //nolint:staticcheck
		}
		return nil
	},
}

var heartbit = lipgloss.NewStyle().Foreground(charmtone.Dolly).SetString(`
    ▄▄▄▄▄▄▄▄    ▄▄▄▄▄▄▄▄
  ███████████  ███████████
████████████████████████████
████████████████████████████
██████████▀██████▀██████████
██████████ ██████ ██████████
▀▀██████▄████▄▄████▄██████▀▀
  ████████████████████████
    ████████████████████
       ▀▀██████████▀▀
           ▀▀▀▀▀▀
`)

// copied from cobra:
const defaultVersionTemplate = `{{with .DisplayName}}{{printf "%s " .}}{{end}}{{printf "version %s" .Version}}
`

func Execute() {
	// NOTE: very hacky: we create a colorprofile writer with STDOUT, then make
	// it forward to a bytes.Buffer, write the colored heartbit to it, and then
	// finally prepend it in the version template.
	// Unfortunately cobra doesn't give us a way to set a function to handle
	// printing the version, and PreRunE runs after the version is already
	// handled, so that doesn't work either.
	// This is the only way I could find that works relatively well.
	if term.IsTerminal(os.Stdout.Fd()) {
		var b bytes.Buffer
		w := colorprofile.NewWriter(os.Stdout, os.Environ())
		w.Forward = &b
		_, _ = w.WriteString(heartbit.String())
		rootCmd.SetVersionTemplate(b.String() + "\n" + defaultVersionTemplate)
	}
	if err := fang.Execute(
		context.Background(),
		rootCmd,
		fang.WithVersion(version.Version),
		fang.WithNotifySignal(os.Interrupt),
	); err != nil {
		os.Exit(1)
	}
}

// supportsProgressBar tries to determine whether the current terminal supports
// progress bars by looking into environment variables.
func supportsProgressBar() bool {
	if !term.IsTerminal(os.Stderr.Fd()) {
		return false
	}
	termProg := os.Getenv("TERM_PROGRAM")
	_, isWindowsTerminal := os.LookupEnv("WT_SESSION")

	return isWindowsTerminal || strings.Contains(strings.ToLower(termProg), "ghostty")
}

func setupAppWithProgressBar(cmd *cobra.Command) (*app.App, error) {
	app, err := setupApp(cmd)
	if err != nil {
		return nil, err
	}

	// Check if progress bar is enabled in config (defaults to true if nil)
	progressEnabled := app.Config().Options.Progress == nil || *app.Config().Options.Progress
	if progressEnabled && supportsProgressBar() {
		_, _ = fmt.Fprintf(os.Stderr, ansi.SetIndeterminateProgressBar)
		defer func() { _, _ = fmt.Fprintf(os.Stderr, ansi.ResetProgressBar) }()
	}

	return app, nil
}

// setupApp handles the common setup logic for both interactive and non-interactive modes.
// It returns the app instance, config, cleanup function, and any error.
func setupApp(cmd *cobra.Command) (*app.App, error) {
	debug, _ := cmd.Flags().GetBool("debug")
	yolo, _ := cmd.Flags().GetBool("yolo")
	dataDir, _ := cmd.Flags().GetString("data-dir")
	ctx := cmd.Context()

	cwd, err := ResolveCwd(cmd)
	if err != nil {
		return nil, err
	}

	store, err := config.Init(cwd, dataDir, debug)
	if err != nil {
		return nil, err
	}

	cfg := store.Config()
	if cfg.Permissions == nil {
		cfg.Permissions = &config.Permissions{}
	}
	cfg.Permissions.SkipRequests = yolo

	if err := createDotCrushDir(cfg.Options.DataDirectory); err != nil {
		return nil, err
	}

	// Register this project in the centralized projects list.
	if err := projects.Register(cwd, cfg.Options.DataDirectory); err != nil {
		slog.Warn("Failed to register project", "error", err)
		// Non-fatal: continue even if registration fails
	}

	// Connect to DB; this will also run migrations.
	conn, err := db.Connect(ctx, cfg.Options.DataDirectory)
	if err != nil {
		return nil, err
	}

	appInstance, err := app.New(ctx, conn, store)
	if err != nil {
		slog.Error("Failed to create app instance", "error", err)
		return nil, err
	}

	if shouldEnableMetrics(cfg) {
		event.Init()
	}

	return appInstance, nil
}

// setupClientApp sets up a client-based workspace via the server. It
// auto-starts a detached server process if the socket does not exist.
func setupClientApp(cmd *cobra.Command, hostURL *url.URL) (*client.Client, *proto.Workspace, error) {
	debug, _ := cmd.Flags().GetBool("debug")
	yolo, _ := cmd.Flags().GetBool("yolo")
	dataDir, _ := cmd.Flags().GetString("data-dir")
	ctx := cmd.Context()

	cwd, err := ResolveCwd(cmd)
	if err != nil {
		return nil, nil, err
	}

	c, err := client.NewClient(cwd, hostURL.Scheme, hostURL.Host)
	if err != nil {
		return nil, nil, err
	}

	ws, err := c.CreateWorkspace(ctx, proto.Workspace{
		Path:    cwd,
		DataDir: dataDir,
		Debug:   debug,
		YOLO:    yolo,
		Env:     os.Environ(),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create workspace: %v", err)
	}

	cfg, err := c.GetGlobalConfig(cmd.Context())
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get global config: %v", err)
	}

	if shouldEnableMetrics(cfg) {
		event.Init()
	}

	return c, ws, nil
}

// ensureServer auto-starts a detached server if the socket file does not
// exist. When connecting to an existing server, it waits for the health
// endpoint to respond.
func ensureServer(cmd *cobra.Command, hostURL *url.URL) error {
	switch hostURL.Scheme {
	case "unix", "npipe":
		_, err := os.Stat(hostURL.Host)
		if err != nil && errors.Is(err, fs.ErrNotExist) {
			if err := startDetachedServer(cmd); err != nil {
				return err
			}
		}

		for range 10 {
			_, err = os.Stat(hostURL.Host)
			if err == nil {
				break
			}
			select {
			case <-cmd.Context().Done():
				return cmd.Context().Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
		if err != nil {
			return fmt.Errorf("failed to initialize crush server: %v", err)
		}
	}

	return nil
}

// waitForHealth polls the server's health endpoint until it responds.
func waitForHealth(ctx context.Context, c *client.Client) error {
	var err error
	for range 10 {
		err = c.Health(ctx)
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return fmt.Errorf("failed to connect to crush server: %v", err)
}

// streamEvents forwards SSE events from the client to the TUI program.
func streamEvents(ctx context.Context, evc <-chan any, p *tea.Program) {
	defer log.RecoverPanic("app.Subscribe", func() {
		slog.Info("TUI subscription panic: attempting graceful shutdown")
		p.Quit()
	})

	for {
		select {
		case <-ctx.Done():
			slog.Debug("TUI message handler shutting down")
			return
		case ev, ok := <-evc:
			if !ok {
				slog.Debug("TUI message channel closed")
				return
			}
			p.Send(ev)
		}
	}
}

var safeNameRegexp = regexp.MustCompile(`[^a-zA-Z0-9._-]`)

func startDetachedServer(cmd *cobra.Command) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %v", err)
	}

	safeClientHost := safeNameRegexp.ReplaceAllString(clientHost, "_")
	chDir := filepath.Join(config.GlobalCacheDir(), "server-"+safeClientHost)
	if err := os.MkdirAll(chDir, 0o700); err != nil {
		return fmt.Errorf("failed to create server working directory: %v", err)
	}

	cmdArgs := []string{"server"}
	if clientHost != server.DefaultHost() {
		cmdArgs = append(cmdArgs, "--host", clientHost)
	}

	c := exec.CommandContext(cmd.Context(), exe, cmdArgs...)
	stdoutPath := filepath.Join(chDir, "stdout.log")
	stderrPath := filepath.Join(chDir, "stderr.log")
	detachProcess(c)

	stdout, err := os.Create(stdoutPath)
	if err != nil {
		return fmt.Errorf("failed to create stdout log file: %v", err)
	}
	defer stdout.Close()
	c.Stdout = stdout

	stderr, err := os.Create(stderrPath)
	if err != nil {
		return fmt.Errorf("failed to create stderr log file: %v", err)
	}
	defer stderr.Close()
	c.Stderr = stderr

	if err := c.Start(); err != nil {
		return fmt.Errorf("failed to start crush server: %v", err)
	}

	if err := c.Process.Release(); err != nil {
		return fmt.Errorf("failed to detach crush server process: %v", err)
	}

	return nil
}

func shouldEnableMetrics(cfg *config.Config) bool {
	if v, _ := strconv.ParseBool(os.Getenv("CRUSH_DISABLE_METRICS")); v {
		return false
	}
	if v, _ := strconv.ParseBool(os.Getenv("DO_NOT_TRACK")); v {
		return false
	}
	if cfg.Options.DisableMetrics {
		return false
	}
	return true
}

func MaybePrependStdin(prompt string) (string, error) {
	if term.IsTerminal(os.Stdin.Fd()) {
		return prompt, nil
	}
	fi, err := os.Stdin.Stat()
	if err != nil {
		return prompt, err
	}
	// Check if stdin is a named pipe ( | ) or regular file ( < ).
	if fi.Mode()&os.ModeNamedPipe == 0 && !fi.Mode().IsRegular() {
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

func createDotCrushDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("failed to create data directory: %q %w", dir, err)
	}

	gitIgnorePath := filepath.Join(dir, ".gitignore")
	if _, err := os.Stat(gitIgnorePath); os.IsNotExist(err) {
		if err := os.WriteFile(gitIgnorePath, []byte("*\n"), 0o644); err != nil {
			return fmt.Errorf("failed to create .gitignore file: %q %w", gitIgnorePath, err)
		}
	}

	return nil
}
