package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/fang/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/crush/internal/app"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/db"
	"github.com/charmbracelet/crush/internal/event"
	"github.com/charmbracelet/crush/internal/projects"
	"github.com/charmbracelet/crush/internal/ui/common"
	ui "github.com/charmbracelet/crush/internal/ui/model"
	"github.com/charmbracelet/crush/internal/version"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/charmtone"
	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.PersistentFlags().StringP("cwd", "c", "", "Current working directory")
	rootCmd.PersistentFlags().StringP("data-dir", "D", "", "Custom crush data directory")
	rootCmd.PersistentFlags().BoolP("debug", "d", false, "Debug")
	rootCmd.Flags().BoolP("help", "h", false, "Help")
	rootCmd.Flags().BoolP("yolo", "y", false, "Automatically accept all permissions (dangerous mode)")

	rootCmd.AddCommand(
		runCmd,
		dirsCmd,
		projectsCmd,
		updateProvidersCmd,
		logsCmd,
		schemaCmd,
		loginCmd,
		statsCmd,
		sessionCmd,
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
		// 设置app, 并且添加进度条
		app, err := setupAppWithProgressBar(cmd)
		if err != nil {
			return err
		}

		// 退出时释放各种资源
		defer app.Shutdown()

		// 发送App initialized事件
		event.AppInitialized()

		// Set up the TUI，环境变量捕获.
		var env uv.Environ = os.Environ()

		// 创建common,返回默认的通用界面配置
		com := common.DefaultCommon(app)

		// 创建终端
		model := ui.New(com)

		// 配置 Bubble Tea 引擎
		program := tea.NewProgram(
			model,
			tea.WithEnvironment(env),
			tea.WithContext(cmd.Context()),
			tea.WithFilter(ui.MouseEventFilter), // Filter mouse events based on focus state
		)

		// 订阅程序，app可以订阅program，向program发送处理时间
		go app.Subscribe(program)

		// Bubble Tea 引擎自动监控键盘输入和鼠标点击，并且处理用户输入。
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

// setupAppWithProgressBar 设置app，会在应用中添加进度条。
func setupAppWithProgressBar(cmd *cobra.Command) (*app.App, error) {
	// 处理交互式和非交互式模式的通用设置逻辑。
	// 它会返回应用实例、配置、清理功能以及任何错误。
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

// setupApp 处理交互式和非交互式模式的通用设置逻辑。
// 它会返回应用实例、配置、清理功能以及任何错误。
// 💡 架构设计：这是一个典型的“工厂方法”或“依赖注入容器”的雏形。
func setupApp(cmd *cobra.Command) (*app.App, error) {
	// ---------------------------------------------------------
	// 1. 提取命令行运行时参数 (Flags)
	// ---------------------------------------------------------
	debug, _ := cmd.Flags().GetBool("debug")
	yolo, _ := cmd.Flags().GetBool("yolo")
	dataDir, _ := cmd.Flags().GetString("data-dir")

	// 继承 Cobra 框架的上下文 (能够捕获用户的 Ctrl+C 退出信号)
	ctx := cmd.Context()

	// ---------------------------------------------------------
	// 2. 确立阵地 (工作目录)
	// ---------------------------------------------------------
	// 解析当前的工作目录。如果用户传了 `--cwd /path/to/project`，
	// 这里就会把进程的工作目录强行切换过去，让 AI 认为自己就在那个目录里。
	cwd, err := ResolveCwd(cmd)
	if err != nil {
		return nil, err
	}

	// ---------------------------------------------------------
	// 3. 加载核心配置大脑
	// ---------------------------------------------------------
	// 这里面会执行查找全局配置、读取环境变量、合并工作区配置等一系列复杂操作。
	store, err := config.Init(cwd, dataDir, debug)
	if err != nil {
		return nil, err
	}

	// 获取纯数据配置指针
	cfg := store.Config()

	// ---------------------------------------------------------
	// 4. 安全权限覆写
	// ---------------------------------------------------------
	// 兜底初始化
	if cfg.Permissions == nil {
		cfg.Permissions = &config.Permissions{}
	}

	// 如果运行时带了这个参数，这个字段就会变成 true。此时 AI 拥有最高神权
	// 调用任何工具、执行任何删库跑路的命令都不再弹窗问你，直接执行！（极其危险，谨慎使用）
	cfg.Permissions.SkipRequests = yolo

	// ---------------------------------------------------------
	// 5. 工作区脚手架建设
	// ---------------------------------------------------------
	// 在当前目录下创建 `.crush` 隐藏文件夹。
	// 并且极其贴心地往里面塞一个 `.gitignore` 文件（内容是 `*`），
	// 保证你绝对不会不小心把本地的 AI 聊天记录提交到 Git 仓库里去！
	if err := createDotCrushDir(cfg.Options.DataDirectory); err != nil {
		return nil, err
	}

	// 将该项目注册到全局的“最近使用项目”列表中。
	// 这样以后可能在 UI 里实现一个 "Open Recent" 的功能，快速切换 AI 工作区。
	if err := projects.Register(cwd, cfg.Options.DataDirectory); err != nil {
		slog.Warn("Failed to register project", "error", err)
		// 容错处理：即使注册失败也不影响当前使用，继续往下走
	}

	// ---------------------------------------------------------
	// 6. 核心存储层挂载 (SQLite)
	// ---------------------------------------------------------
	// 连接本地数据库。如果数据库文件不存在，会自动创建。
	// 并且在连接成功后，会自动执行 SQL 迁移脚本 (Migrations) 建表。
	conn, err := db.Connect(ctx, cfg.Options.DataDirectory)
	if err != nil {
		return nil, err
	}

	// ---------------------------------------------------------
	// 7. 终极装配：创建 App 实例
	// ---------------------------------------------------------
	// 将上下文 (ctx)、数据库连接池 (conn) 和 配置大管家 (store)
	// 全部注入到业务核心结构体 `app.App` 中。
	// 这个 `appInstance` 就是之后真正在后台跑大模型、发事件流的核心引擎！
	appInstance, err := app.New(ctx, conn, store)
	if err != nil {
		slog.Error("Failed to create app instance", "error", err)
		return nil, err
	}

	// ---------------------------------------------------------
	// 8. 遥测与事件总线启动
	// ---------------------------------------------------------
	// 如果用户没有禁用遥测（Metrics），就初始化事件总线，
	// 用于收集匿名崩溃日志或性能指标。
	if shouldEnableMetrics(cfg) {
		event.Init()
	}

	return appInstance, nil
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

// ResolveCwd 解析当前工作目录
func ResolveCwd(cmd *cobra.Command) (string, error) {
	cwd, _ := cmd.Flags().GetString("cwd")
	if cwd != "" {
		// 更改目录
		err := os.Chdir(cwd)
		if err != nil {
			return "", fmt.Errorf("failed to change directory: %v", err)
		}
		return cwd, nil
	}

	// 获取当前工作目录
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
