package agent

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/home"
	"github.com/charmbracelet/crush/internal/llm/tools"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/version"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCPState 表示 MCP 客户端的当前状态
type MCPState int

const (
	// MCPStateDisabled MCP已禁用
	MCPStateDisabled MCPState = iota

	// MCPStateStarting MCP正在启动
	MCPStateStarting

	// MCPStateConnected MCP已连接
	MCPStateConnected

	// MCPStateError MCP发生错误
	MCPStateError
)

// String 返回MCP状态的字符串描述
func (s MCPState) String() string {
	switch s {
	case MCPStateDisabled: // 禁用
		return "disabled"
	case MCPStateStarting: // 开始
		return "starting"
	case MCPStateConnected: // 已连接
		return "connected"
	case MCPStateError: // 错误
		return "error"
	default: // 未知
		return "unknown"
	}
}

// MCPEventType 代表MCP事件的类型
type MCPEventType string

const (
	// MCPEventStateChanged MCP状态已改变
	MCPEventStateChanged MCPEventType = "state_changed"

	// MCPEventToolsListChanged MCP工具列表已改变
	MCPEventToolsListChanged MCPEventType = "tools_list_changed"
)

// MCPEvent 表示MCP系统中的一个事件结构
type MCPEvent struct {
	Type      MCPEventType // mcp事件类型
	Name      string       // 事件名称
	State     MCPState     // 事件状态
	Error     error        // 错误信息
	ToolCount int          // 工具数量
}

// MCPClientInfo 描述和记录有关 MCP 客户端状态的信息
type MCPClientInfo struct {
	Name        string             // MCP名称
	State       MCPState           // MCP状态
	Error       error              // 错误信息
	Client      *mcp.ClientSession // MCP会话
	ToolCount   int                // 工具数量
	ConnectedAt time.Time          // 连接时间
}

var (
	// mcpToolsOnce 确保工具只被加载一次
	mcpToolsOnce sync.Once

	// mcpTools 保存MCP工具：工具名称 -> 工具对象
	mcpTools = csync.NewMap[string, tools.BaseTool]()

	// mcpClient2Tools 保存MCP客户端和工具之间的映射： MCP名称 -> 工具列表
	mcpClient2Tools = csync.NewMap[string, []tools.BaseTool]()

	// mcpClients 存储MCP客户端会话： MCP名称 -> MCP会话
	mcpClients = csync.NewMap[string, *mcp.ClientSession]()

	// mcpStates 存储MCP客户端信息 ： MCP名称 -> MCP客户端信息
	mcpStates = csync.NewMap[string, MCPClientInfo]()

	// mcpBroker 订阅MCP事件
	mcpBroker = pubsub.NewBroker[MCPEvent]()
)

// McpTool MCP工具
type McpTool struct {
	// mcpName MCP名称
	mcpName string

	// tool MCP工具
	tool *mcp.Tool

	// permissions 权限服务
	permissions permission.Service

	// workingDir 工具工作目录
	workingDir string
}

// Name 返回工具名称
func (b *McpTool) Name() string {
	return fmt.Sprintf("mcp_%s_%s", b.mcpName, b.tool.Name)
}

// Info 返回工具信息
func (b *McpTool) Info() tools.ToolInfo {
	parameters := make(map[string]any)
	required := make([]string, 0)

	if input, ok := b.tool.InputSchema.(map[string]any); ok {
		if props, ok := input["properties"].(map[string]any); ok {
			parameters = props
		}
		if req, ok := input["required"].([]any); ok {
			// Convert []any -> []string when elements are strings
			for _, v := range req {
				if s, ok := v.(string); ok {
					required = append(required, s)
				}
			}
		} else if reqStr, ok := input["required"].([]string); ok {
			// Handle case where it's already []string
			required = reqStr
		}
	}

	return tools.ToolInfo{
		Name:        fmt.Sprintf("mcp_%s_%s", b.mcpName, b.tool.Name),
		Description: b.tool.Description,
		Parameters:  parameters,
		Required:    required,
	}
}

// runTool 运行MCP工具, 返回结构化工具执行数据结构
func runTool(ctx context.Context, name, toolName string, input string) (tools.ToolResponse, error) {
	var args map[string]any
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return tools.NewTextErrorResponse(fmt.Sprintf("error parsing parameters: %s", err)), nil
	}

	// 获取MCP会话
	c, err := getOrRenewClient(ctx, name)
	if err != nil {
		return tools.NewTextErrorResponse(err.Error()), nil
	}

	// 调用MCP工具
	result, err := c.CallTool(ctx, &mcp.CallToolParams{
		Name:      toolName,
		Arguments: args,
	})
	if err != nil {
		return tools.NewTextErrorResponse(err.Error()), nil
	}

	output := make([]string, 0, len(result.Content))
	for _, v := range result.Content {
		if vv, ok := v.(*mcp.TextContent); ok {
			output = append(output, vv.Text)
		} else {
			output = append(output, fmt.Sprintf("%v", v))
		}
	}
	return tools.NewTextResponse(strings.Join(output, "\n")), nil
}

// getOrRenewClient 获取或重新创建MCP会话
//   - name MCP名称
func getOrRenewClient(ctx context.Context, name string) (*mcp.ClientSession, error) {
	sess, ok := mcpClients.Get(name)
	if !ok {
		return nil, fmt.Errorf("mcp '%s' not available", name)
	}

	cfg := config.Get()
	m := cfg.MCP[name]
	state, _ := mcpStates.Get(name)

	timeout := mcpTimeout(m)
	pingCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	err := sess.Ping(pingCtx, nil)
	if err == nil {
		return sess, nil
	}

	// 错误处理, 重新创建会话
	updateMCPState(name, MCPStateError, maybeTimeoutErr(err, timeout), nil, state.ToolCount)

	// 创建MCP会话，并且解析配置中的mcp工具
	sess, err = createMCPSession(ctx, name, m, cfg.Resolver())
	if err != nil {
		return nil, err
	}

	// 更新MCP状态
	updateMCPState(name, MCPStateConnected, nil, sess, state.ToolCount)

	// 写入新会话
	mcpClients.Set(name, sess)
	return sess, nil
}

// Run 执行当前工具
func (b *McpTool) Run(ctx context.Context, params tools.ToolCall) (tools.ToolResponse, error) {
	// 获取会话id和消息id
	sessionID, messageID := tools.GetContextValues(ctx)
	if sessionID == "" || messageID == "" {
		return tools.ToolResponse{}, fmt.Errorf("session ID and message ID are required for creating a new file")
	}
	permissionDescription := fmt.Sprintf("execute %s with the following parameters:", b.Info().Name)
	// 获取执行权限
	p := b.permissions.Request(
		permission.CreatePermissionRequest{
			SessionID:   sessionID,
			ToolCallID:  params.ID,
			Path:        b.workingDir,
			ToolName:    b.Info().Name,
			Action:      "execute",
			Description: permissionDescription,
			Params:      params.Input,
		},
	)
	if !p {
		return tools.ToolResponse{}, permission.ErrorPermissionDenied
	}

	// 执行，返回结果
	runResp, err := runTool(ctx, b.mcpName, b.tool.Name, params.Input)
	if err != nil {
		return runResp, err
	}

	return runResp, nil
}

// getTools 获取MCP工具
//   - name: MCP名称
//   - permissions: 权限服务
//   - c MCP会话
//   - workingDir: 工作目录
func getTools(ctx context.Context, name string, permissions permission.Service, c *mcp.ClientSession, workingDir string) ([]tools.BaseTool, error) {
	// 获取会话中的工具
	result, err := c.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		return nil, err
	}

	// 将工具转为基础工具数据结构
	mcpTools := make([]tools.BaseTool, 0, len(result.Tools))
	for _, tool := range result.Tools {
		mcpTools = append(mcpTools, &McpTool{
			mcpName:     name,
			tool:        tool,
			permissions: permissions,
			workingDir:  workingDir,
		})
	}
	return mcpTools, nil
}

// SubscribeMCPEvents 返回 MCP 事件的通道
func SubscribeMCPEvents(ctx context.Context) <-chan pubsub.Event[MCPEvent] {
	return mcpBroker.Subscribe(ctx)
}

// GetMCPStates 返回所有 MCP 客户端的当前状态
func GetMCPStates() map[string]MCPClientInfo {
	return maps.Collect(mcpStates.Seq2())
}

// GetMCPState 返回特定 MCP 客户端的状态
func GetMCPState(name string) (MCPClientInfo, bool) {
	return mcpStates.Get(name)
}

// updateMCPState 更新 MCP 客户端的状态并发布事件
//   - state: MCP 状态
//   - err: 错误
//   - client: MCP 客户端会话
//   - toolCount: MCP 工具数量
func updateMCPState(name string, state MCPState, err error, client *mcp.ClientSession, toolCount int) {
	// 实例化一个客户端信息对象
	info := MCPClientInfo{
		Name:      name,
		State:     state,
		Error:     err,
		Client:    client,
		ToolCount: toolCount,
	}
	switch state {
	case MCPStateConnected: // MCP 已连接
		info.ConnectedAt = time.Now()
	case MCPStateError: // MCP 错误
		// 删除所有 MCP 工具
		updateMcpTools(name, nil)

		// 删除当前 MCP 客户端会话
		mcpClients.Del(name)
	}

	// 设置当前cmp的客户端信息
	mcpStates.Set(name, info)

	// 发布状态改变事件
	mcpBroker.Publish(pubsub.UpdatedEvent, MCPEvent{
		Type:      MCPEventStateChanged,
		Name:      name,
		State:     state,
		Error:     err,
		ToolCount: toolCount,
	})
}

// CloseMCPClients 关闭所有 MCP 客户端。这应该在应用程序关闭期间调用。
func CloseMCPClients() error {
	var errs []error
	for name, c := range mcpClients.Seq2() {
		if err := c.Close(); err != nil &&
			!errors.Is(err, io.EOF) &&
			!errors.Is(err, context.Canceled) &&
			err.Error() != "signal: killed" {
			errs = append(errs, fmt.Errorf("close mcp: %s: %w", name, err))
		}
	}
	mcpBroker.Shutdown()
	return errors.Join(errs...)
}

// getMCPTools 获取所有 MCP 工具
//   - cfg: 配置
//   - permissions: 权限服务
//   - workingDir: 配置
func doGetMCPTools(ctx context.Context, permissions permission.Service, cfg *config.Config) {
	var wg sync.WaitGroup
	// 初始化所有配置的 MCP 的状态
	for name, m := range cfg.MCP {
		if m.Disabled {
			// 禁用的 MCP
			updateMCPState(name, MCPStateDisabled, nil, nil, 0)
			slog.Debug("skipping disabled mcp", "name", name)
			continue
		}

		// 设置初始启动状态
		updateMCPState(name, MCPStateStarting, nil, nil, 0)

		wg.Add(1)
		go func(name string, m config.MCPConfig) {
			defer func() {
				wg.Done()
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
					updateMCPState(name, MCPStateError, err, nil, 0)
					slog.Error("panic in mcp client initialization", "error", err, "name", name)
				}
			}()

			// 设置ctx超时及其取消功能
			ctx, cancel := context.WithTimeout(ctx, mcpTimeout(m))
			defer cancel()

			// 创建MCP会话，并解析配置中的mcp工具
			c, err := createMCPSession(ctx, name, m, cfg.Resolver())
			if err != nil {
				return
			}

			// 保存mcp会话
			mcpClients.Set(name, c)

			// 获取所有MCP工具
			tools, err := getTools(ctx, name, permissions, c, cfg.WorkingDir())
			if err != nil {
				slog.Error("error listing tools", "error", err)
				updateMCPState(name, MCPStateError, err, nil, 0)
				c.Close()
				return
			}

			// 将mcp工具保存为全局
			updateMcpTools(name, tools)
			mcpClients.Set(name, c)

			// 更新MCP状态
			updateMCPState(name, MCPStateConnected, nil, c, len(tools))
		}(name, m)
	}
	wg.Wait()
}

// updateMcpTools 更新全局 mcpTools 和 mcpClient2Tools 映射
func updateMcpTools(mcpName string, tools []tools.BaseTool) {
	if len(tools) == 0 {
		// 工具为0，删除当前名称mcp工具集
		mcpClient2Tools.Del(mcpName)
	} else {
		// 将当前名称mcp工具集设置为全局mcp工具集
		mcpClient2Tools.Set(mcpName, tools)
	}
	for _, tools := range mcpClient2Tools.Seq2() {
		for _, t := range tools {
			// 设置全局mcp工具
			mcpTools.Set(t.Name(), t)
		}
	}
}

// createMCPSession 创建 MCP 会话
//   - name: MCP名称
//   - m: MCP配置
//   - resolver: 变量解析器，用于解析变量，具体由使用者实现解析功能
func createMCPSession(ctx context.Context, name string, m config.MCPConfig, resolver config.VariableResolver) (*mcp.ClientSession, error) {
	// 获取配中创建 MCP会话的超时时间
	timeout := mcpTimeout(m)
	mcpCtx, cancel := context.WithCancel(ctx)

	// 添加一个延迟取消函数
	cancelTimer := time.AfterFunc(timeout, cancel)

	// 创建 MCP 转换，将配置转为cmd执行的命令
	transport, err := createMCPTransport(mcpCtx, m, resolver)
	if err != nil {
		updateMCPState(name, MCPStateError, err, nil, 0)
		slog.Error("error creating mcp client", "error", err, "name", name)
		cancel()
		cancelTimer.Stop()
		return nil, err
	}

	// 创建 MCP 客户端
	client := mcp.NewClient(
		&mcp.Implementation{
			Name:    "crush",
			Version: version.Version,
			Title:   "Crush",
		},
		&mcp.ClientOptions{
			ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) {
				// 发布工具列表改变事件
				mcpBroker.Publish(pubsub.UpdatedEvent, MCPEvent{
					Type: MCPEventToolsListChanged,
					Name: name,
				})
			},
			KeepAlive: time.Minute * 10,
		},
	)

	// 连接到 MCP,并且返回会话
	session, err := client.Connect(mcpCtx, transport, nil)
	if err != nil {
		err = maybeStdioErr(err, transport)
		updateMCPState(name, MCPStateError, maybeTimeoutErr(err, timeout), nil, 0)
		slog.Error("error starting mcp client", "error", err, "name", name)
		cancel()
		cancelTimer.Stop()
		return nil, err
	}

	cancelTimer.Stop()
	slog.Info("Initialized mcp client", "name", name)
	return session, nil
}

// maybeStdioErr 如果 stdio mcp 以非 json 格式打印错误，它将失败
// 解析，然后 cli 将关闭它，导致 EOF 错误。
// 所以，如果我们遇到 EOF 错误，并且传输是 STDIO，我们会尝试执行它
// 再次超时并收集输出，以便我们可以向其中添加详细信息
// 错误。
// 当使用 npx 启动时尤其会发生这种情况，例如如果节点不能
// 被发现或类似的其他错误。
func maybeStdioErr(err error, transport mcp.Transport) error {
	if !errors.Is(err, io.EOF) {
		return err
	}
	ct, ok := transport.(*mcp.CommandTransport)
	if !ok {
		return err
	}

	// 检查标准输入输出
	if err2 := stdioMCPCheck(ct.Command); err2 != nil {
		err = errors.Join(err, err2)
	}
	return err
}

// maybeTimeoutErr 如果错误是 context.Canceled，则返回超时错误
func maybeTimeoutErr(err error, timeout time.Duration) error {
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("timed out after %s", timeout)
	}
	return err
}

// createMCPTransport 为给定的 mcp 配置创建cmp transport。
//   - m: MCP配置
//   - resolver: 配置变量解析器,交给使用者实现解析功能
func createMCPTransport(ctx context.Context, m config.MCPConfig, resolver config.VariableResolver) (mcp.Transport, error) {
	switch m.Type { // 选择连接MCP服务端的方式
	case config.MCPStdio: // 标准 io
		// 解析命令，方便后续使用mcp时，执行mcp服务端启动命令
		command, err := resolver.ResolveValue(m.Command)
		if err != nil {
			return nil, fmt.Errorf("invalid mcp command: %w", err)
		}
		if strings.TrimSpace(command) == "" {
			return nil, fmt.Errorf("mcp stdio config requires a non-empty 'command' field")
		}
		cmd := exec.CommandContext(ctx, home.Long(command), m.Args...)
		cmd.Env = append(os.Environ(), m.ResolvedEnv()...)
		return &mcp.CommandTransport{
			Command: cmd,
		}, nil
	case config.MCPHttp: // http
		if strings.TrimSpace(m.URL) == "" {
			return nil, fmt.Errorf("mcp http config requires a non-empty 'url' field")
		}
		client := &http.Client{
			Transport: &headerRoundTripper{
				headers: m.ResolvedHeaders(),
			},
		}
		return &mcp.StreamableClientTransport{
			Endpoint:   m.URL,
			HTTPClient: client,
		}, nil
	case config.MCPSSE: // sse
		if strings.TrimSpace(m.URL) == "" {
			return nil, fmt.Errorf("mcp sse config requires a non-empty 'url' field")
		}
		client := &http.Client{
			Transport: &headerRoundTripper{
				headers: m.ResolvedHeaders(),
			},
		}
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

// RoundTrip 实现 http.RoundTripper
func (rt headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	for k, v := range rt.headers {
		req.Header.Set(k, v)
	}
	return http.DefaultTransport.RoundTrip(req)
}

// mcpTimeout 获取 mcp 超时时间
func mcpTimeout(m config.MCPConfig) time.Duration {
	return time.Duration(cmp.Or(m.Timeout, 15)) * time.Second
}

// stdioMCPCheck 检查命令是否正常
func stdioMCPCheck(old *exec.Cmd) error {
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
