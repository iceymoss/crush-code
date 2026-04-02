// Package mcp provides functionality for managing Model Context Protocol (MCP)
// clients within the Crush application.
package mcp

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/home"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/version"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// parseLevel 解析MCP日志级别
func parseLevel(level mcp.LoggingLevel) slog.Level {
	switch level {
	case "info": // 信息级别
		return slog.LevelInfo
	case "notice": // 通知级别
		return slog.LevelInfo
	case "warning": // 警告级别
		return slog.LevelWarn
	default: // 默认级别为调试级别
		return slog.LevelDebug
	}
}

// ClientSession 包装了一个mcp.ClientSession，并添加了一个取消上下文的功能，以便在会话建立期间创建的上下文在关闭时被正确清理
// ClientSession wraps an mcp.ClientSession with a context cancel function so
// that the context created during session establishment is properly cleaned up
// on close.
type ClientSession struct {
	*mcp.ClientSession                    // 底层mcp.ClientSession
	cancel             context.CancelFunc // 取消上下文函数
}

// Close 取消会话上下文并关闭底层会话
func (s *ClientSession) Close() error {
	// 取消会话上下文,清理被ctx关联的所有协程
	s.cancel()
	// 关闭底层会话
	return s.ClientSession.Close()
}

var (
	// sessions 存储所有MCP客户端会话, 客户端名称 => 客户端会话
	sessions = csync.NewMap[string, *ClientSession]()
	// states 存储所有MCP客户端状态, 客户端名称 => 客户端状态
	states = csync.NewMap[string, ClientInfo]()
	// broker 创建一个MCPEvent发布者/订阅者模式中的发布者，用于MCP事件
	broker = pubsub.NewBroker[Event]()
	// initOnce 用于确保只初始化一次
	initOnce sync.Once
	// initDone 用于确保只初始化一次
	initDone = make(chan struct{})
)

// State 表示MCP客户端的当前状态
type State int

const (
	// StateDisabled 表示MCP客户端被禁用
	StateDisabled State = iota
	// StateStarting 表示MCP客户端正在启动
	StateStarting
	// StateConnected 表示MCP客户端已连接
	StateConnected
	// StateError 表示MCP客户端发生错误
	StateError
)

// String 返回MCP客户端状态的字符串表示
func (s State) String() string {
	switch s {
	case StateDisabled:
		return "disabled"
	case StateStarting:
		return "starting"
	case StateConnected:
		return "connected"
	case StateError:
		return "error"
	default:
		return "unknown"
	}
}

// EventType 表示MCP事件的类型
type EventType uint

const (
	// EventStateChanged 表示MCP客户端状态发生变化
	EventStateChanged EventType = iota
	// EventToolsListChanged 表示MCP客户端工具列表发生变化
	EventToolsListChanged
	// EventPromptsListChanged 表示MCP客户端提示词列表发生变化
	EventPromptsListChanged
	// EventResourcesListChanged 表示MCP客户端资源列表发生变化
	EventResourcesListChanged
)

// Event 表示MCP系统中的事件
type Event struct {
	Type   EventType // 事件类型
	Name   string    // 客户端名称
	State  State     // 状态
	Error  error     // 错误
	Counts Counts    // 可用工具、提示词、资源等数量
}

// Counts 表示MCP客户端的可用工具、提示词、资源等数量
type Counts struct {
	Tools     int // 工具数量
	Prompts   int // 提示词数量
	Resources int // 资源数量
}

// ClientInfo 表示MCP客户端的状态信息
type ClientInfo struct {
	Name        string         // 客户端名称
	State       State          // 状态
	Error       error          // 错误
	Client      *ClientSession // 客户端会话
	Counts      Counts         // 可用工具、提示词、资源等数量
	ConnectedAt time.Time      // 连接时间
}

// SubscribeEvents returns a channel for MCP events
func SubscribeEvents(ctx context.Context) <-chan pubsub.Event[Event] {
	return broker.Subscribe(ctx)
}

// GetStates returns the current state of all MCP clients
func GetStates() map[string]ClientInfo {
	return states.Copy()
}

// GetState returns the state of a specific MCP client
func GetState(name string) (ClientInfo, bool) {
	return states.Get(name)
}

// Close closes all MCP clients. This should be called during application shutdown.
func Close(ctx context.Context) error {
	var wg sync.WaitGroup
	for name, session := range sessions.Seq2() {
		wg.Go(func() {
			done := make(chan error, 1)
			go func() {
				done <- session.Close()
			}()
			select {
			case err := <-done:
				if err != nil &&
					!errors.Is(err, io.EOF) &&
					!errors.Is(err, context.Canceled) &&
					err.Error() != "signal: killed" {
					slog.Warn("Failed to shutdown MCP client", "name", name, "error", err)
				}
			case <-ctx.Done():
			}
		})
	}
	wg.Wait()
	broker.Shutdown()
	return nil
}

// Initialize 根据提供的配置初始化MCP客户端
//
//	ctx 上下文
//	permissions 权限服务
//	cfg 配置存储
func Initialize(ctx context.Context, permissions permission.Service, cfg *config.ConfigStore) {
	slog.Info("Initializing MCP clients")

	// 创建一个同步原语
	var wg sync.WaitGroup
	// 初始化所有配置的MCP客户端状态
	for name, m := range cfg.Config().MCP {
		if m.Disabled {
			// 如果配置的MCP客户端被禁用，则更新MCP客户端状态为已禁用
			updateState(name, StateDisabled, nil, nil, Counts{})
			slog.Debug("Skipping disabled MCP", "name", name)
			continue
		}

		// 设置初始启动状态，MCP客户端正在启动状态
		updateState(name, StateStarting, nil, nil, Counts{})

		wg.Add(1)
		// 启动一个goroutine，用于初始化MCP客户端
		go func(name string, m config.MCPConfig) {
			defer func() {
				wg.Done()
				// 如果panic，则更新MCP客户端状态为错误状态
				if r := recover(); r != nil {
					var err error
					switch v := r.(type) {
					case error:
						err = v
					case string:
						err = fmt.Errorf("panic: %s", v)
					default:
						err = fmt.Errorf("panic: %v", v)
					}
					updateState(name, StateError, err, nil, Counts{})
					slog.Error("Panic in MCP client initialization", "error", err, "name", name)
				}
			}()

			// createSession handles its own timeout internally.
			// 创建MCP客户端会话
			session, err := createSession(ctx, name, m, cfg.Resolver())
			if err != nil {
				return
			}

			// 获取MCP客户端工具列表
			tools, err := getTools(ctx, session)
			if err != nil {
				slog.Error("Error listing tools", "error", err)
				updateState(name, StateError, err, nil, Counts{})
				session.Close()
				return
			}

			// 获取MCP客户端提示词列表
			prompts, err := getPrompts(ctx, session)
			if err != nil {
				slog.Error("Error listing prompts", "error", err)
				updateState(name, StateError, err, nil, Counts{})
				session.Close()
				return
			}

			// 获取MCP客户端资源列表
			resources, err := getResources(ctx, session)
			if err != nil {
				slog.Error("Error listing resources", "error", err)
				updateState(name, StateError, err, nil, Counts{})
				session.Close()
				return
			}

			// 更新MCP客户端工具列表
			toolCount := updateTools(cfg, name, tools)

			// 更新MCP客户端提示词列表
			updatePrompts(name, prompts)

			// 更新MCP客户端资源列表
			resourceCount := updateResources(name, resources)

			// 设置MCP客户端会话
			sessions.Set(name, session)

			// 更新MCP客户端状态为已连接
			updateState(name, StateConnected, nil, session, Counts{
				Tools:     toolCount,
				Prompts:   len(prompts),
				Resources: resourceCount,
			})
		}(name, m)
	}

	// 等待所有MCP客户端初始化完成
	wg.Wait()

	//向程序的其他部分发送一个“所有 MCP 客户端都已彻底初始化完毕”的信号，并且绝对保证这个信号只发送一次
	initOnce.Do(func() { close(initDone) })
}

// WaitForInit blocks until MCP initialization is complete.
// If Initialize was never called, this returns immediately.
func WaitForInit(ctx context.Context) error {
	select {
	case <-initDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func getOrRenewClient(ctx context.Context, cfg *config.ConfigStore, name string) (*ClientSession, error) {
	sess, ok := sessions.Get(name)
	if !ok {
		return nil, fmt.Errorf("mcp '%s' not available", name)
	}

	m := cfg.Config().MCP[name]
	state, _ := states.Get(name)

	timeout := mcpTimeout(m)
	pingCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	err := sess.Ping(pingCtx, nil)
	if err == nil {
		return sess, nil
	}
	updateState(name, StateError, maybeTimeoutErr(err, timeout), nil, state.Counts)

	sess, err = createSession(ctx, name, m, cfg.Resolver())
	if err != nil {
		return nil, err
	}

	updateState(name, StateConnected, nil, sess, state.Counts)
	sessions.Set(name, sess)
	return sess, nil
}

// updateState 更新MCP客户端状态并发布事件
//
//	name 客户端名称
//	state 状态
//	err 错误
//	client 客户端会话
//	counts 计数
func updateState(name string, state State, err error, client *ClientSession, counts Counts) {
	// 创建客户端状态信息
	info := ClientInfo{
		Name:   name,
		State:  state,
		Error:  err,
		Client: client,
		Counts: counts,
	}
	switch state {
	case StateConnected: // 状态为已连接，则更新连接时间
		info.ConnectedAt = time.Now()
	case StateError: // 状态为错误，则删除客户端会话
		sessions.Del(name)
	}

	// 更新客户端状态信息
	states.Set(name, info)

	// 发布MCP客户端状态变化事件
	broker.Publish(pubsub.UpdatedEvent, Event{
		Type:   EventStateChanged,
		Name:   name,
		State:  state,
		Error:  err,
		Counts: counts,
	})
}

// createSession 创建MCP客户端会话
//
//	ctx 上下文
//	name 客户端名称
//	m MCP配置
//	resolver 变量解析器
//	返回MCP客户端会话和错误
func createSession(ctx context.Context, name string, m config.MCPConfig, resolver config.VariableResolver) (*ClientSession, error) {
	// 获取MCP客户端的超时时间
	timeout := mcpTimeout(m)

	// 封装ctx
	mcpCtx, cancel := context.WithCancel(ctx)

	// 这里利用 time.AfterFunc 设定了一个倒计时。如果在 timeout 规定的时间内（比如 10 秒）没有人来阻止它，它就会自动执行 cancel 函数。
	// 一旦 cancel 被调用，底层的 mcpCtx 就会被取消，所有依赖这个 Context 的后续操作（如启动子进程、建立网络连接等）都会立刻被迫中断。
	cancelTimer := time.AfterFunc(timeout, cancel)

	// 根据配置创建MCP客户端传输
	transport, err := createTransport(mcpCtx, m, resolver)
	if err != nil {
		// 如果创建传输失败，则更新MCP客户端状态为错误状态
		updateState(name, StateError, err, nil, Counts{})
		slog.Error("Error creating MCP client", "error", err, "name", name)
		cancel()
		cancelTimer.Stop()
		return nil, err
	}

	// 创建MCP客户端
	client := mcp.NewClient(
		&mcp.Implementation{
			Name:    "crush",         // 客户端名称
			Version: version.Version, // 客户端版本
			Title:   "Crush",         // 客户端标题
		},
		&mcp.ClientOptions{
			ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) { // 工具列表变化处理函数, 这里发布一个工具列表变化事件
				broker.Publish(pubsub.UpdatedEvent, Event{
					Type: EventToolsListChanged,
					Name: name,
				})
			},
			PromptListChangedHandler: func(context.Context, *mcp.PromptListChangedRequest) { // 提示词列表变化处理函数, 这里发布一个提示词列表变化事件
				broker.Publish(pubsub.UpdatedEvent, Event{
					Type: EventPromptsListChanged,
					Name: name,
				})
			},
			ResourceListChangedHandler: func(context.Context, *mcp.ResourceListChangedRequest) { // 资源列表变化处理函数, 这里发布一个资源列表变化事件
				broker.Publish(pubsub.UpdatedEvent, Event{
					Type: EventResourcesListChanged,
					Name: name,
				})
			},
			LoggingMessageHandler: func(ctx context.Context, req *mcp.LoggingMessageRequest) { // 日志消息处理函数, 这里打印日志
				// 解析日志级别
				level := parseLevel(req.Params.Level)
				// 打印日志
				slog.Log(ctx, level, "MCP log", "name", name, "logger", req.Params.Logger, "data", req.Params.Data)
			},
		},
	)

	// 连接MCP客户端
	session, err := client.Connect(mcpCtx, transport, nil)
	if err != nil {
		// maybeStdioErr 的作用是排查子进程异常退出的真实原因
		err = maybeStdioErr(err, transport)
		// 如果连接失败，则更新MCP客户端状态为错误状态
		updateState(name, StateError, maybeTimeoutErr(err, timeout), nil, Counts{})
		slog.Error("MCP client failed to initialize", "error", err, "name", name)
		// 取消MCP客户端会话
		cancel()
		// 停止定时器
		cancelTimer.Stop()
		return nil, err
	}

	// 停止定时器
	// 如果 client.Connect 在规定的超时时间内顺利完成了，代码会立刻执行 cancelTimer.Stop()
	// 一旦连接成功建立，这个 MCP Session 的生命周期就交由外部的父级 ctx 来控制了，不再受这个 timeout 的限制
	cancelTimer.Stop()
	slog.Debug("MCP client initialized", "name", name)
	// 返回MCP客户端会话
	return &ClientSession{session, cancel}, nil
}

// maybeStdioErr 的作用是排查子进程异常退出的真实原因。
// 如果基于标准输入输出 (stdio) 的 MCP 客户端打印了非 JSON 格式的错误，
// 就会导致解析失败，随后 CLI 会关闭连接并引发一个无意义的 EOF 错误。
// 因此，如果我们捕获到了 EOF 错误，并且确认当前的传输层使用的是 STDIO（命令行），
// 我们会尝试（带有超时控制地）重新执行该命令并收集它的终端输出，以此来给错误补充细节。
// 这种情况在通过 npx 启动服务时尤为常见，例如找不到 node 环境或类似的系统错误。
func maybeStdioErr(err error, transport mcp.Transport) error {
	// 1. 检查错误类型：如果不是 io.EOF（意味着不是连接意外断开），
	// 说明是一个明确的其他错误，直接返回即可，不需要特殊处理。
	if !errors.Is(err, io.EOF) {
		return err
	}

	// 2. 检查传输层类型：尝试将 transport 断言为底层的命令行传输类型（*mcp.CommandTransport）
	ct, ok := transport.(*mcp.CommandTransport)
	if !ok {
		return err
	}

	// 3. 深入诊断：既然确定是命令行子进程意外断开，调用 stdioCheck 去捕获真实的错误输出。
	// (stdioCheck 内部通常会去抓取 stderr 的输出，或者短暂重试命令来获取报错文本)
	if err2 := stdioCheck(ct.Command); err2 != nil {
		// 4. 错误合并：如果成功抓取到了底层的真实报错 (err2)，
		// 使用 errors.Join 将原有的 EOF 错误和真实报错信息拼接在一起，提供给上层调用者。
		err = errors.Join(err, err2)
	}
	return err
}

func maybeTimeoutErr(err error, timeout time.Duration) error {
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("timed out after %s", timeout)
	}
	return err
}

// createTransport 根据配置创建MCP客户端传输
//
//	ctx 上下文
//	m MCP配置
//	resolver 变量解析器
//	返回MCP客户端传输和错误
func createTransport(ctx context.Context, m config.MCPConfig, resolver config.VariableResolver) (mcp.Transport, error) {
	switch m.Type { // 根据MCP配置的类型创建不同的传输
	case config.MCPStdio: // 标准输入输出模式
		// 解析mcp命令
		command, err := resolver.ResolveValue(m.Command)
		if err != nil {
			return nil, fmt.Errorf("invalid mcp command: %w", err)
		}
		// 如果命令为空，则返回错误
		if strings.TrimSpace(command) == "" {
			return nil, fmt.Errorf("mcp stdio config requires a non-empty 'command' field")
		}

		// 创建一个命令，用于执行MCP客户端命令
		cmd := exec.CommandContext(ctx, home.Long(command), m.Args...)

		// 设置环境变量
		// 这行代码在准备子进程的运行环境。它首先拉取了宿主机当前所有的环境变量（os.Environ()）
		// 然后再把 MCP 配置里独有的环境变量追加进去。这样子进程既能感知系统的基础配置，又能拿到专属的变量
		cmd.Env = append(os.Environ(), m.ResolvedEnv()...)
		return &mcp.CommandTransport{
			Command: cmd,
		}, nil
	case config.MCPHttp: // HTTP模式
		// 如果URL为空，则返回错误
		if strings.TrimSpace(m.URL) == "" {
			return nil, fmt.Errorf("mcp http config requires a non-empty 'url' field")
		}

		// 创建一个HTTP客户端，用于执行MCP客户端HTTP请求
		client := &http.Client{
			Transport: &headerRoundTripper{
				headers: m.ResolvedHeaders(), // 设置HTTP头
			},
		}
		return &mcp.StreamableClientTransport{
			Endpoint:   m.URL,
			HTTPClient: client,
		}, nil
	case config.MCPSSE: // SSE模式
		// 如果URL为空，则返回错误
		if strings.TrimSpace(m.URL) == "" {
			return nil, fmt.Errorf("mcp sse config requires a non-empty 'url' field")
		}

		// 创建一个HTTP客户端，用于执行MCP客户端HTTP请求
		client := &http.Client{
			Transport: &headerRoundTripper{
				headers: m.ResolvedHeaders(),
			},
		}

		// 创建一个SSE客户端传输，用于执行MCP客户端SSE请求
		return &mcp.SSEClientTransport{
			Endpoint:   m.URL,
			HTTPClient: client,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported mcp type: %s", m.Type)
	}
}

type headerRoundTripper struct {
	headers map[string]string
}

func (rt headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	for k, v := range rt.headers {
		req.Header.Set(k, v)
	}
	return http.DefaultTransport.RoundTrip(req)
}

// mcpTimeout 返回MCP客户端的超时时间
//
//	m MCP配置
//	返回MCP客户端的超时时间
func mcpTimeout(m config.MCPConfig) time.Duration {
	return time.Duration(cmp.Or(m.Timeout, 15)) * time.Second
}

func stdioCheck(old *exec.Cmd) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()
	cmd := exec.CommandContext(ctx, old.Path, old.Args...)
	cmd.Env = old.Env
	out, err := cmd.CombinedOutput()
	if err == nil || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil
	}
	return fmt.Errorf("%w: %s", err, string(out))
}
