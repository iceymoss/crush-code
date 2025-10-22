package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/db"
	"github.com/charmbracelet/crush/internal/format"
	"github.com/charmbracelet/crush/internal/history"
	"github.com/charmbracelet/crush/internal/llm/agent"
	"github.com/charmbracelet/crush/internal/log"
	"github.com/charmbracelet/crush/internal/lsp"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/x/ansi"
)

type App struct {
	Sessions    session.Service    // 会话管理
	Messages    message.Service    // 消息管理
	History     history.Service    // 历史管理
	Permissions permission.Service // 权限管理

	CoderAgent agent.Service // ai agent 管理

	LSPClients *csync.Map[string, *lsp.Client] // 语言服务协议：主要用于处理代码规范性，代码提示等等

	config *config.Config // 配置

	serviceEventsWG *sync.WaitGroup // 服务所有事件同步
	eventsCtx       context.Context // 事件处理的上下文
	events          chan tea.Msg    // 事件消息同步通道
	tuiWG           *sync.WaitGroup // 终端界面同步

	// global context and cleanup functions
	globalCtx    context.Context // 全局上下文
	cleanupFuncs []func() error  // 清理功能
}

// New initializes a new applcation instance.
func New(ctx context.Context, conn *sql.DB, cfg *config.Config) (*App, error) {
	// 创建db
	q := db.New(conn)

	// 将db连接保存在会话中使用
	// 创建会话
	sessions := session.NewService(q)

	// 创建消息管理
	messages := message.NewService(q)

	// 创建历史管理
	files := history.NewService(q, conn)

	// 是否跳过权限直接请求
	skipPermissionsRequests := cfg.Permissions != nil && cfg.Permissions.SkipRequests

	// 授权的工具
	allowedTools := []string{}
	if cfg.Permissions != nil && cfg.Permissions.AllowedTools != nil {
		allowedTools = cfg.Permissions.AllowedTools
	}

	// 实例化app管理对象
	app := &App{
		Sessions:    sessions,
		Messages:    messages,
		History:     files,
		Permissions: permission.NewPermissionService(cfg.WorkingDir(), skipPermissionsRequests, allowedTools),
		LSPClients:  csync.NewMap[string, *lsp.Client](),

		globalCtx: ctx,

		config: cfg,

		events:          make(chan tea.Msg, 100),
		serviceEventsWG: &sync.WaitGroup{},
		tuiWG:           &sync.WaitGroup{},
	}

	// 设置事件
	app.setupEvents()

	// Initialize LSP clients in the background.
	app.initLSPClients(ctx)

	// cleanup database upon app shutdown
	app.cleanupFuncs = append(app.cleanupFuncs, conn.Close)

	// TODO: remove the concept of agent config, most likely.
	if cfg.IsConfigured() {
		// 初始化coder agent
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
	messageReadBytes := make(map[string]int)

	defer fmt.Printf(ansi.ResetProgressBar)
	for {
		// HACK: add it again on every iteration so it doesn't get hidden by
		// the terminal due to inactivity.
		fmt.Printf(ansi.SetIndeterminateProgressBar)
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
			readBts := messageReadBytes[result.Message.ID]

			if len(msgContent) < readBts {
				slog.Error("Non-interactive: message content is shorter than read bytes", "message_length", len(msgContent), "read_bytes", readBts)
				return fmt.Errorf("message content is shorter than read bytes: %d < %d", len(msgContent), readBts)
			}
			fmt.Println(msgContent[readBts:])
			messageReadBytes[result.Message.ID] = len(msgContent)

			slog.Info("Non-interactive: run completed", "session_id", sess.ID)
			return nil

		case event := <-messageEvents:
			msg := event.Payload
			if msg.SessionID == sess.ID && msg.Role == message.Assistant && len(msg.Parts) > 0 {
				stopSpinner()

				content := msg.Content().String()
				readBytes := messageReadBytes[msg.ID]

				if len(content) < readBytes {
					slog.Error("Non-interactive: message content is shorter than read bytes", "message_length", len(content), "read_bytes", readBytes)
					return fmt.Errorf("message content is shorter than read bytes: %d < %d", len(content), readBytes)
				}

				part := content[readBytes:]
				fmt.Print(part)
				messageReadBytes[msg.ID] = len(content)
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
	// 为事件上下文添加取消功能
	app.eventsCtx = ctx
	setupSubscriber(ctx, app.serviceEventsWG, "sessions", app.Sessions.Subscribe, app.events)                                  // 订阅会话
	setupSubscriber(ctx, app.serviceEventsWG, "messages", app.Messages.Subscribe, app.events)                                  // 订阅消息
	setupSubscriber(ctx, app.serviceEventsWG, "permissions", app.Permissions.Subscribe, app.events)                            // 订阅权限
	setupSubscriber(ctx, app.serviceEventsWG, "permissions-notifications", app.Permissions.SubscribeNotifications, app.events) // 订阅权限通知
	setupSubscriber(ctx, app.serviceEventsWG, "history", app.History.Subscribe, app.events)                                    // 订阅历史
	setupSubscriber(ctx, app.serviceEventsWG, "mcp", agent.SubscribeMCPEvents, app.events)                                     // 订阅MCP
	setupSubscriber(ctx, app.serviceEventsWG, "lsp", SubscribeLSPEvents, app.events)                                           // 订阅LSP
	cleanupFunc := func() error {                                                                                              // 清理函数
		cancel()
		app.serviceEventsWG.Wait() // 等待所有订阅者完成
		return nil
	}

	// 添加清理函数
	app.cleanupFuncs = append(app.cleanupFuncs, cleanupFunc)
}

// setupSubscriber 设置订阅者
func setupSubscriber[T any](
	ctx context.Context,
	wg *sync.WaitGroup,
	name string,
	subscriber func(context.Context) <-chan pubsub.Event[T],
	outputCh chan<- tea.Msg,
) {
	// 创建一个 goroutine 监听订阅者的事件
	wg.Go(func() {
		subCh := subscriber(ctx)
		for {
			select {
			case event, ok := <-subCh: // 监听订阅者的事件
				if !ok {
					// chan is closed
					slog.Debug("subscription channel closed", "name", name)
					return
				}
				var msg tea.Msg = event
				select {
				case outputCh <- msg: // 发送事件给输出通道
				case <-time.After(2 * time.Second): // 2秒内没有处理事件则丢弃
					slog.Warn("message dropped due to slow consumer", "name", name)
				case <-ctx.Done(): // 上下文被cancel，终止订阅，退出循环
					slog.Debug("subscription cancelled", "name", name)
					return
				}
			case <-ctx.Done(): // 上下文被cancel，终止订阅，退出循环
				slog.Debug("subscription cancelled", "name", name)
				return
			}
		}
	})
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

	// 订阅coder agent事件
	setupSubscriber(app.eventsCtx, app.serviceEventsWG, "coderAgent", app.CoderAgent.Subscribe, app.events)
	return nil
}

// Subscribe 将事件作为 tea.Msgs 发送到 TUI。
func (app *App) Subscribe(program *tea.Program) {
	defer log.RecoverPanic("app.Subscribe", func() {
		slog.Info("TUI subscription panic: attempting graceful shutdown")
		program.Quit()
	})

	app.tuiWG.Add(1)
	tuiCtx, tuiCancel := context.WithCancel(app.globalCtx)

	// 添加清理函数
	app.cleanupFuncs = append(app.cleanupFuncs, func() error {
		slog.Debug("Cancelling TUI message handler")
		tuiCancel()
		app.tuiWG.Wait()
		return nil
	})

	defer app.tuiWG.Done()

	for {
		select {
		case <-tuiCtx.Done(): // 上下文被cancel，退出循环
			slog.Debug("TUI message handler shutting down")
			return
		case msg, ok := <-app.events: // 订阅者事件 => app.events => TUI
			if !ok {
				slog.Debug("TUI message channel closed")
				return
			}

			// 发送事件给TUI
			program.Send(msg)
		}
	}
}

// Shutdown performs a graceful shutdown of the application.
func (app *App) Shutdown() {
	if app.CoderAgent != nil {
		app.CoderAgent.CancelAll()
	}

	// Shutdown all LSP clients.
	for name, client := range app.LSPClients.Seq2() {
		shutdownCtx, cancel := context.WithTimeout(app.globalCtx, 5*time.Second)
		if err := client.Close(shutdownCtx); err != nil {
			slog.Error("Failed to shutdown LSP client", "name", name, "error", err)
		}
		cancel()
	}

	// Call call cleanup functions.
	for _, cleanup := range app.cleanupFuncs {
		if cleanup != nil {
			if err := cleanup(); err != nil {
				slog.Error("Failed to cleanup app properly on shutdown", "error", err)
			}
		}
	}
}
