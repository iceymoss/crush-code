// Package app wires together services, coordinates agents, and manages
// application lifecycle.
package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/agent"
	"github.com/charmbracelet/crush/internal/agent/notify"
	"github.com/charmbracelet/crush/internal/agent/tools/mcp"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/db"
	"github.com/charmbracelet/crush/internal/event"
	"github.com/charmbracelet/crush/internal/filetracker"
	"github.com/charmbracelet/crush/internal/format"
	"github.com/charmbracelet/crush/internal/history"
	"github.com/charmbracelet/crush/internal/log"
	"github.com/charmbracelet/crush/internal/lsp"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/shell"
	"github.com/charmbracelet/crush/internal/ui/anim"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/crush/internal/update"
	"github.com/charmbracelet/crush/internal/version"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/charmtone"
	"github.com/charmbracelet/x/term"
)

// UpdateAvailableMsg is sent when a new version is available.
type UpdateAvailableMsg struct {
	CurrentVersion string
	LatestVersion  string
	IsDevelopment  bool
}

type App struct {
	Sessions    session.Service     // 会话服务，管理会话的创建、获取、删除等操作
	Messages    message.Service     // 消息服务，管理agent的各种消息的创建、获取、删除等操作
	History     history.Service     // 历史服务，管理用户的历史会话记录的创建、获取、删除等操作
	Permissions permission.Service  // 权限服务，用于管理agent各种权限能力
	FileTracker filetracker.Service // 文件跟踪服务，管理文件的创建、获取、删除等操作

	AgentCoordinator agent.Coordinator // agent协调器，用于协调agent的各种操作

	LSPManager *lsp.Manager // LSP管理器，用于管理LSP的各种操作, SLP是Language Server Protocol的缩写，是一种用于代码编辑器的协议

	config *config.ConfigStore // 配置存储，用于存储配置信息

	serviceEventsWG *sync.WaitGroup // 服务事件等待组，用于等待服务事件的完成
	eventsCtx       context.Context // 事件上下文，用于存储事件信息
	events          chan tea.Msg    // 事件通道，用于传递事件信息
	tuiWG           *sync.WaitGroup // TUI等待组，用于等待TUI的完成

	// global context and cleanup functions
	globalCtx          context.Context                     // 全局上下文，用于存储全局信息
	cleanupFuncs       []func(context.Context) error       // 清理函数，用于清理资源
	agentNotifications *pubsub.Broker[notify.Notification] // agent通知，用于通知agent的各种操作
}

// New initializes 初始化一个新应用程序实例。
func New(ctx context.Context, conn *sql.DB, store *config.ConfigStore) (*App, error) {
	// 初始化数据库
	q := db.New(conn)
	// 初始化会话服务
	sessions := session.NewService(q, conn)
	// 初始化消息服务
	messages := message.NewService(q)
	// 初始化历史服务
	files := history.NewService(q, conn)
	// 获取配置
	cfg := store.Config()
	// 初始化权限服务
	skipPermissionsRequests := cfg.Permissions != nil && cfg.Permissions.SkipRequests

	// 初始化允许的工具
	var allowedTools []string
	if cfg.Permissions != nil && cfg.Permissions.AllowedTools != nil {
		allowedTools = cfg.Permissions.AllowedTools
	}

	// 初始化app
	app := &App{
		Sessions:    sessions,
		Messages:    messages,
		History:     files,
		Permissions: permission.NewPermissionService(store.WorkingDir(), skipPermissionsRequests, allowedTools), // 初始化权限服务
		FileTracker: filetracker.NewService(q),                                                                  // 初始化文件跟踪服务
		LSPManager:  lsp.NewManager(store),                                                                      // 初始化LSP管理器

		globalCtx: ctx, // 全局上下文

		config: store, // 配置存储

		events:             make(chan tea.Msg, 100),                 // 事件通道, 用于传递事件信息
		serviceEventsWG:    &sync.WaitGroup{},                       // 服务事件等待组, 用于等待服务事件的完成
		tuiWG:              &sync.WaitGroup{},                       // TUI并发同步, 用于等待TUI的完成
		agentNotifications: pubsub.NewBroker[notify.Notification](), // 初始化agent通知, 用于通知agent的各种操作
	}

	app.setupEvents()

	// 检查更新，开启一个goroutine，用于检查更新
	go app.checkForUpdates(ctx)

	// 初始化MCP，开启一个goroutine，用于初始化MCP
	go mcp.Initialize(ctx, app.Permissions, store)

	// cleanup database upon app shutdown
	// 将app自动更新检查和MCP客户端关闭的清理函数添加到清理函数列表中
	app.cleanupFuncs = append(
		app.cleanupFuncs,
		func(context.Context) error { return conn.Close() },
		func(ctx context.Context) error { return mcp.Close(ctx) },
	)

	// TODO: remove the concept of agent config, most likely.
	// TODO：极有可能会在未来移除 'agent config'（代理配置）这个概念。

	// 查系统是否具备最起码的运行条件（至少配了一个能用的提供商
	if !cfg.IsConfigured() {
		slog.Warn("No agent configuration found")
		return app, nil
	}

	// 初始化Coder Agent
	if err := app.InitCoderAgent(ctx); err != nil {
		return nil, fmt.Errorf("failed to initialize coder agent: %w", err)
	}

	// Set up callback for LSP state updates.
	// 设置LSP状态更新回调
	app.LSPManager.SetCallback(func(name string, client *lsp.Client) {
		if client == nil {
			// lsp客户端未启动，更新LSP状态为未启动状态
			updateLSPState(name, lsp.StateUnstarted, nil, nil, 0)
			return
		}
		// 设置诊断回调
		client.SetDiagnosticsCallback(updateLSPDiagnostics)
		// 更新LSP状态
		updateLSPState(name, client.GetServerState(), nil, client, 0)
	})
	// 子协程跟踪配置的LSP服务器
	go app.LSPManager.TrackConfigured()

	return app, nil
}

// Config returns the pure-data configuration.
func (app *App) Config() *config.Config {
	return app.config.Config()
}

// Store returns the config store.
func (app *App) Store() *config.ConfigStore {
	return app.config
}

// AgentNotifications returns the broker for agent notification events.
func (app *App) AgentNotifications() *pubsub.Broker[notify.Notification] {
	return app.agentNotifications
}

// resolveSession 决定在非交互式运行中应该使用哪个会话 (session)
// 如果传入了 continueSessionID，它会通过该 ID 查找对应的历史会话
// 如果设置了 useLast 为 true，它会返回系统里最近更新的、且处于顶层的会话
// 如果以上条件都不满足，它会创建一个全新的会话
func (app *App) resolveSession(ctx context.Context, continueSessionID string, useLast bool) (session.Session, error) {
	switch {
	// 情况 1：指定了具体的会话 ID
	case continueSessionID != "":
		// 安全检查：不允许恢复 Agent 工具专用的会话
		if app.Sessions.IsAgentToolSession(continueSessionID) {
			return session.Session{}, fmt.Errorf("cannot continue an agent tool session: %s", continueSessionID)
		}

		// 尝试根据 ID 从数据库/内存中获取会话
		sess, err := app.Sessions.Get(ctx, continueSessionID)
		if err != nil {
			return session.Session{}, fmt.Errorf("session not found: %s", continueSessionID) // 找不到会话则报错
		}

		// 规则检查：只能恢复主会话，如果这个会话有父级 ID（说明是子会话），则拒绝恢复
		if sess.ParentSessionID != "" {
			return session.Session{}, fmt.Errorf("cannot continue a child session: %s", continueSessionID)
		}

		// 所有检查通过，返回找到的会话
		return sess, nil

	// 情况 2：要求使用上一次的会话
	case useLast:
		// 获取最近更新的一个会话
		sess, err := app.Sessions.GetLast(ctx)
		if err != nil {
			return session.Session{}, fmt.Errorf("no sessions found to continue") // 如果没有任何历史记录则报错
		}
		return sess, nil

	// 情况 3：默认行为（没给 ID 且 useLast 为 false）
	default:
		// 创建一个全新的会话，并使用系统默认名称
		return app.Sessions.Create(ctx, agent.DefaultSessionName)
	}
}

// RunNonInteractive 在非交互模式下运行应用程序，
// 使用给定的提示词 (prompt) 并将结果打印到标准输出 (stdout)
func (app *App) RunNonInteractive(ctx context.Context, output io.Writer, prompt, largeModel, smallModel string, hideSpinner bool, continueSessionID string, useLast bool) error {
	slog.Info("Running in non-interactive mode")

	// 创建一个取消上下文，用于取消非交互模式
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// 模型覆盖设置：如果用户通过命令行参数指定了特定模型，则覆盖默认配置
	if largeModel != "" || smallModel != "" {
		if err := app.overrideModelsForNonInteractive(ctx, largeModel, smallModel); err != nil {
			return fmt.Errorf("failed to override models: %w", err)
		}
	}

	// 变量声明：用于判断当前是否在真实的终端 (TTY) 环境中运行
	var (
		spinner   *format.Spinner
		stdoutTTY bool
		stderrTTY bool
		stdinTTY  bool
		progress  bool
	)

	// 终端检测：检查输入、输出和错误流是否连接到了真实的终端
	if f, ok := output.(*os.File); ok {
		stdoutTTY = term.IsTerminal(f.Fd())
	}
	stderrTTY = term.IsTerminal(os.Stderr.Fd())
	stdinTTY = term.IsTerminal(os.Stdin.Fd())

	// 读取配置决定是否显示进度条
	progress = app.config.Config().Options.Progress == nil || *app.config.Config().Options.Progress

	// UI 动画设置：如果不隐藏加载动画，且标准错误输出是终端，则初始化加载动画 (Spinner)
	if !hideSpinner && stderrTTY {
		t := styles.DefaultStyles()

		// Detect background color to set the appropriate color for the
		// spinner's 'Generating...' text. Without this, that text would be
		// unreadable in light terminals.
		// 侦测终端背景色（深色或浅色），以确保 "Generating..." 文字不会因为背景色相同而看不见
		hasDarkBG := true
		if f, ok := output.(*os.File); ok && stdinTTY && stdoutTTY {
			hasDarkBG = lipgloss.HasDarkBackground(os.Stdin, f)
		}
		defaultFG := lipgloss.LightDark(hasDarkBG)(charmtone.Pepper, t.FgBase)

		// 配置并启动加载动画
		spinner = format.NewSpinner(ctx, cancel, anim.Settings{
			Size:        10,
			Label:       "Generating",
			LabelColor:  defaultFG,
			GradColorA:  t.Primary,
			GradColorB:  t.Secondary,
			CycleColors: true,
		})
		spinner.Start()
	}

	// 定义一个停止加载动画的辅助函数
	stopSpinner := func() {
		if !hideSpinner && spinner != nil {
			spinner.Stop()
			spinner = nil
		}
	}

	// 等待MCP初始化完成，再读取MCP工具
	if err := mcp.WaitForInit(ctx); err != nil {
		return fmt.Errorf("failed to wait for MCP initialization: %w", err)
	}

	// 强制更新 AI 代理的模型状态，确保刚才加载的 MCP 工具生效
	app.AgentCoordinator.UpdateModels(ctx)

	// 确保函数退出时无论如何都会停止动画
	defer stopSpinner()

	// 会话管理：调用 resolveSession 决定使用哪个会话
	sess, err := app.resolveSession(ctx, continueSessionID, useLast)
	if err != nil {
		return fmt.Errorf("failed to create session for non-interactive mode: %w", err)
	}

	if continueSessionID != "" || useLast {
		slog.Info("Continuing session for non-interactive run", "session_id", sess.ID)
	} else {
		slog.Info("Created session for non-interactive run", "session_id", sess.ID)
	}

	// Automatically approve all permission requests for this non-interactive
	// session.
	// 非常重要：自动批准该非交互会话的所有权限请求，因为无人值守模式下用户无法手动点击同意
	app.Permissions.AutoApproveSession(sess.ID)

	// 定义一个用于接收 AI 执行结果的channel
	type response struct {
		result *fantasy.AgentResult
		err    error
	}

	done := make(chan response, 1)

	// 启动一个goroutine，用于执行AI代理，并接收结果
	go func(ctx context.Context, sessionID, prompt string) {
		// 调用agent协调器执行AI代理，并返回结果
		result, err := app.AgentCoordinator.Run(ctx, sess.ID, prompt)
		if err != nil {
			// 如果执行失败，则将错误信息发送给done channel
			done <- response{
				err: fmt.Errorf("failed to start agent processing stream: %w", err),
			}
			return
		}

		// 将结果发送给done channel
		done <- response{
			result: result,
		}
	}(ctx, sess.ID, prompt)

	// 事件流监听与流式输出:
	// 订阅系统消息事件，用来捕捉 AI 实时生成的一字一句
	messageEvents := app.Messages.Subscribe(ctx)

	// 记录每条消息已经打印了多少个字节，防止重复打印
	messageReadBytes := make(map[string]int)

	// 记录是否已经开始打印有效内容
	var printed bool

	// 退出时的清理工作
	defer func() {
		if progress && stderrTTY {
			_, _ = fmt.Fprintf(os.Stderr, ansi.ResetProgressBar)
		}

		// Always print a newline at the end. If output is a TTY this will
		// prevent the prompt from overwriting the last line of output.
		// 打印一个换行符，防止最后一行输出和用户的终端提示符挤在一起
		_, _ = fmt.Fprintln(output)
	}()

	// 主循环：持续监听事件，直到收到退出信号或发生错误
	for {
		if progress && stderrTTY {
			// Hack 技巧：不断重置终端的不确定进度条，防止它因为终端静止而被隐藏
			_, _ = fmt.Fprintf(os.Stderr, ansi.SetIndeterminateProgressBar)
		}

		select {
		case result := <-done: // 收到AI代理执行结果
			// 停止加载动画
			stopSpinner()
			// 如果执行失败，则返回错误
			if result.err != nil {
				// 如果执行被取消，则返回nil
				if errors.Is(result.err, context.Canceled) || errors.Is(result.err, agent.ErrRequestCancelled) {
					slog.Debug("Non-interactive: agent processing cancelled", "session_id", sess.ID)
					return nil
				}
				return fmt.Errorf("agent processing failed: %w", result.err)
			}
			// 如果执行成功，则返回nil
			return nil

		case event := <-messageEvents: // 收到 AI 的流式消息更新
			msg := event.Payload // 获取消息内容
			if msg.SessionID == sess.ID && msg.Role == message.Assistant && len(msg.Parts) > 0 {
				// 停止加载动画
				stopSpinner()

				content := msg.Content().String()     // 获取消息内容
				readBytes := messageReadBytes[msg.ID] // 获取消息已经打印了多少个字节

				// 如果消息内容长度小于已经打印的字节数，则返回错误
				if len(content) < readBytes {
					// 记录错误日志
					slog.Error("Non-interactive: message content is shorter than read bytes", "message_length", len(content), "read_bytes", readBytes)
					return fmt.Errorf("message content is shorter than read bytes: %d < %d", len(content), readBytes)
				}

				part := content[readBytes:]
				// Trim leading whitespace. Sometimes the LLM includes leading
				// formatting and intentation, which we don't want here.
				if readBytes == 0 {
					part = strings.TrimLeft(part, " \t")
				}
				// Ignore initial whitespace-only messages.
				if printed || strings.TrimSpace(part) != "" {
					printed = true
					fmt.Fprint(output, part)
				}
				messageReadBytes[msg.ID] = len(content)
			}

		case <-ctx.Done():
			stopSpinner()
			return ctx.Err()
		}
	}
}

func (app *App) UpdateAgentModel(ctx context.Context) error {
	if app.AgentCoordinator == nil {
		return fmt.Errorf("agent configuration is missing")
	}
	return app.AgentCoordinator.UpdateModels(ctx)
}

// overrideModelsForNonInteractive parses the model strings and temporarily
// overrides the model configurations, then rebuilds the agent.
// Format: "model-name" (searches all providers) or "provider/model-name".
// Model matching is case-insensitive.
// If largeModel is provided but smallModel is not, the small model defaults to
// the provider's default small model.
func (app *App) overrideModelsForNonInteractive(ctx context.Context, largeModel, smallModel string) error {
	providers := app.config.Config().Providers.Copy()

	largeMatches, smallMatches, err := findModels(providers, largeModel, smallModel)
	if err != nil {
		return err
	}

	var largeProviderID string

	// Override large model.
	if largeModel != "" {
		found, err := validateMatches(largeMatches, largeModel, "large")
		if err != nil {
			return err
		}
		largeProviderID = found.provider
		slog.Info("Overriding large model for non-interactive run", "provider", found.provider, "model", found.modelID)
		app.config.Config().Models[config.SelectedModelTypeLarge] = config.SelectedModel{
			Provider: found.provider,
			Model:    found.modelID,
		}
	}

	// Override small model.
	switch {
	case smallModel != "":
		found, err := validateMatches(smallMatches, smallModel, "small")
		if err != nil {
			return err
		}
		slog.Info("Overriding small model for non-interactive run", "provider", found.provider, "model", found.modelID)
		app.config.Config().Models[config.SelectedModelTypeSmall] = config.SelectedModel{
			Provider: found.provider,
			Model:    found.modelID,
		}

	case largeModel != "":
		// No small model specified, but large model was - use provider's default.
		smallCfg := app.GetDefaultSmallModel(largeProviderID)
		app.config.Config().Models[config.SelectedModelTypeSmall] = smallCfg
	}

	return app.AgentCoordinator.UpdateModels(ctx)
}

// GetDefaultSmallModel returns the default small model for the given
// provider. Falls back to the large model if no default is found.
func (app *App) GetDefaultSmallModel(providerID string) config.SelectedModel {
	cfg := app.config.Config()
	largeModelCfg := cfg.Models[config.SelectedModelTypeLarge]

	// Find the provider in the known providers list to get its default small model.
	knownProviders, _ := config.Providers(cfg)
	var knownProvider *catwalk.Provider
	for _, p := range knownProviders {
		if string(p.ID) == providerID {
			knownProvider = &p
			break
		}
	}

	// For unknown/local providers, use the large model as small.
	if knownProvider == nil {
		slog.Warn("Using large model as small model for unknown provider", "provider", providerID, "model", largeModelCfg.Model)
		return largeModelCfg
	}

	defaultSmallModelID := knownProvider.DefaultSmallModelID
	model := cfg.GetModel(providerID, defaultSmallModelID)
	if model == nil {
		slog.Warn("Default small model not found, using large model", "provider", providerID, "model", largeModelCfg.Model)
		return largeModelCfg
	}

	slog.Info("Using provider default small model", "provider", providerID, "model", defaultSmallModelID)
	return config.SelectedModel{
		Provider:        providerID,
		Model:           defaultSmallModelID,
		MaxTokens:       model.DefaultMaxTokens,
		ReasoningEffort: model.DefaultReasoningEffort,
	}
}

// setupEvents 设置事件
func (app *App) setupEvents() {
	// 封装cxt和cancel函数，用于取消事件
	ctx, cancel := context.WithCancel(app.globalCtx)

	// 设置事件
	app.eventsCtx = ctx

	// 设置会话事件订阅, 用于订阅会话事件
	setupSubscriber(ctx, app.serviceEventsWG, "sessions", app.Sessions.Subscribe, app.events)

	// 设置消息事件订阅, 用于订阅消息事件
	setupSubscriber(ctx, app.serviceEventsWG, "messages", app.Messages.Subscribe, app.events)

	// 设置权限事件订阅, 用于订阅权限事件
	setupSubscriber(ctx, app.serviceEventsWG, "permissions", app.Permissions.Subscribe, app.events)

	// 设置权限通知事件订阅, 用于订阅权限通知事件
	setupSubscriber(ctx, app.serviceEventsWG, "permissions-notifications", app.Permissions.SubscribeNotifications, app.events)

	// 设置历史事件订阅, 用于订阅历史事件
	setupSubscriber(ctx, app.serviceEventsWG, "history", app.History.Subscribe, app.events)

	// 设置agent通知事件订阅, 用于订阅agent通知事件
	setupSubscriber(ctx, app.serviceEventsWG, "agent-notifications", app.agentNotifications.Subscribe, app.events)

	// 设置MCP事件订阅, 用于订阅MCP事件
	setupSubscriber(ctx, app.serviceEventsWG, "mcp", mcp.SubscribeEvents, app.events)

	// 设置LSP事件订阅, 用于订阅LSP事件
	setupSubscriber(ctx, app.serviceEventsWG, "lsp", SubscribeLSPEvents, app.events)

	// 设置清理函数, 用于清理事件,serviceEventsWG用于控制这些订阅的协程
	cleanupFunc := func(context.Context) error {
		cancel()
		app.serviceEventsWG.Wait()
		return nil
	}

	// 添加清理函数, 用于清理事件,serviceEventsWG用于控制这些订阅的协程
	app.cleanupFuncs = append(app.cleanupFuncs, cleanupFunc)
}

const subscriberSendTimeout = 2 * time.Second

// setupSubscriber 设置订阅者，开启一个goroutine，用于订阅事件，用于监控订阅的消息，然后发送给输出通道, serviceEventsWG的协程可以被上游控制
func setupSubscriber[T any](
	ctx context.Context, // 上下文
	wg *sync.WaitGroup, // 并发同步
	name string, // 名称
	subscriber func(context.Context) <-chan pubsub.Event[T], // 订阅者处理函数
	outputCh chan<- tea.Msg, // 输出通道
) {
	// 启动一个goroutine，用于订阅事件
	wg.Go(func() { // 启动一个goroutine，用于订阅事件
		// subscriber 返回对应类型的一个事件通道
		subCh := subscriber(ctx)

		// 这里创建了一个立刻到期的定时器，并马上读取了 <-sendTimer.C 将其排空。
		// 这样做的目的是为了后面在循环中可以安全地重用这个 timer
		// 而不需要在循环内部频繁地分配新的 Timer 对象（节省内存和 GC 开销
		sendTimer := time.NewTimer(0)

		// 马上读取了 <-sendTimer.C 将其排空
		<-sendTimer.C
		// 停止定时器
		defer sendTimer.Stop()

		// 这个 select 语句是整个定时器存在的唯一目的。它让程序同时等待两个事情：
		// 要么把消息成功塞进 outputCh，要么 2 秒钟超时（sendTimer.C 触发）。如果超时触发，说明下游消费者处理太慢
		// 系统为了自保，宁可丢弃这条消息（记录一条 Message dropped due to slow consumer 的 debug 日志）
		// 也不能让当前的 Goroutine 卡死在这里
		for {
			select {
			case event, ok := <-subCh: // 监控事件通道
				if !ok {
					// chan被关闭，直接结束订阅
					slog.Debug("Subscription channel closed", "name", name)
					return
				}

				// 创建一个tea.Msg类型的消息
				var msg tea.Msg = event
				// 优雅且安全地停止旧的 timer 并排空通道
				if !sendTimer.Stop() {
					select {
					case <-sendTimer.C: // 排空定时器通道
					default:
					}
				}
				// 将定时器重置为 2 秒
				sendTimer.Reset(subscriberSendTimeout)

				select {
				case outputCh <- msg: // 将msg发送给输出通道， 如果在 2 秒内发送成功，皆大欢喜
				case <-sendTimer.C: // 如果 2 秒到了 outputCh 还没收下这个 msg
					// 丢弃消息，打印日志，然后继续下一次 for 循环
					slog.Debug("Message dropped due to slow consumer", "name", name)
				case <-ctx.Done(): // 如果上下文取消，则结束订阅
					slog.Debug("Subscription cancelled", "name", name)
					return
				}
			case <-ctx.Done(): // 如果上下文取消，则结束订阅
				slog.Debug("Subscription cancelled", "name", name)
				return
			}
		}
	})
}

// InitCoderAgent 初始化Coder Agent
func (app *App) InitCoderAgent(ctx context.Context) error {
	// 获取Coder Agent配置
	coderAgentCfg := app.config.Config().Agents[config.AgentCoder]
	if coderAgentCfg.ID == "" {
		// 提前返回错误，说明Coder Agent配置缺失
		return fmt.Errorf("coder agent configuration is missing")
	}
	// 创建Coder Agent协调器
	var err error
	app.AgentCoordinator, err = agent.NewCoordinator(
		ctx,
		app.config,
		app.Sessions,
		app.Messages,
		app.Permissions,
		app.History,
		app.FileTracker,
		app.LSPManager,
		app.agentNotifications,
	)
	if err != nil {
		slog.Error("Failed to create coder agent", "err", err)
		return err
	}
	return nil
}

// Subscribe sends events to the TUI as tea.Msgs.
func (app *App) Subscribe(program *tea.Program) {
	defer log.RecoverPanic("app.Subscribe", func() {
		slog.Info("TUI subscription panic: attempting graceful shutdown")
		program.Quit()
	})

	app.tuiWG.Add(1)
	tuiCtx, tuiCancel := context.WithCancel(app.globalCtx)
	app.cleanupFuncs = append(app.cleanupFuncs, func(context.Context) error {
		slog.Debug("Cancelling TUI message handler")
		tuiCancel()
		app.tuiWG.Wait()
		return nil
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

// Shutdown 执行优雅的应用程序关闭
func (app *App) Shutdown() {
	start := time.Now()
	defer func() { slog.Debug("Shutdown took " + time.Since(start).String()) }()

	// 首先，取消所有agent，等他们完成。这必须完成
	// 在关闭数据库之前，这样代理人才能完成写入他们的状态。
	if app.AgentCoordinator != nil {
		app.AgentCoordinator.CancelAll()
	}

	// Now run remaining cleanup tasks in parallel.
	var wg sync.WaitGroup

	// 所有超时范围内清理的共享关机上下文。
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(app.globalCtx), 5*time.Second)
	defer cancel()

	// 发送退出事件
	wg.Go(func() {
		event.AppExited()
	})

	// Kill all background shells.
	wg.Go(func() {
		shell.GetBackgroundShellManager().KillAll(shutdownCtx)
	})

	// Shutdown all LSP clients.
	wg.Go(func() {
		app.LSPManager.KillAll(shutdownCtx)
	})

	// 调用所有清理函数.
	for _, cleanup := range app.cleanupFuncs {
		if cleanup != nil {
			wg.Go(func() {
				if err := cleanup(shutdownCtx); err != nil {
					slog.Error("Failed to cleanup app properly on shutdown", "error", err)
				}
			})
		}
	}
	wg.Wait()
}

// checkForUpdates 检查更新，开启一个goroutine，用于检查更新
func (app *App) checkForUpdates(ctx context.Context) {
	// 封装cxt和cancel函数，用于取消检查更新
	checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// 检查更新
	info, err := update.Check(checkCtx, version.Version, update.Default)
	if err != nil || !info.Available() {
		return
	}

	// 发送更新事件
	app.events <- UpdateAvailableMsg{
		CurrentVersion: info.Current,
		LatestVersion:  info.Latest,
		IsDevelopment:  info.IsDevelopment(),
	}
}
