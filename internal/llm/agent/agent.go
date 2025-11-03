// Package agent contains the implementation of the AI agent service.
package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/catwalk/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/event"
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

// AgentEventType 定义了事件类型
type AgentEventType string

const (
	// AgentEventTypeError 表示错误
	AgentEventTypeError AgentEventType = "error"

	// AgentEventTypeResponse 响应
	AgentEventTypeResponse AgentEventType = "response"

	// AgentEventTypeSummarize 总结
	AgentEventTypeSummarize AgentEventType = "summarize"
)

// AgentEvent ai agent 事件
type AgentEvent struct {
	Type    AgentEventType  // 事件类型
	Message message.Message // 消息
	Error   error           // 错误

	// When summarizing
	SessionID string // 会话ID
	Progress  string // 进度
	Done      bool   // 是否完成
}

type Service interface {
	// Suscriber 订阅事件
	pubsub.Suscriber[AgentEvent]

	// Model 获取模型
	Model() catwalk.Model

	// Run 运行agent ai
	Run(ctx context.Context, sessionID string, content string, attachments ...message.Attachment) (<-chan AgentEvent, error)

	// Cancel 取消一个会话
	Cancel(sessionID string)

	// CancelAll 取消所有会话
	CancelAll()

	// IsSessionBusy 检测一个会话是否正在运行
	IsSessionBusy(sessionID string) bool

	// IsBusy 检测所有会话是否正在运行
	IsBusy() bool

	// Summarize 总结一个会话的摘要
	Summarize(ctx context.Context, sessionID string) error

	// UpdateModel 更新模型
	UpdateModel() error

	// QueuedPrompts 获取一个会话的队列中的提示词数
	QueuedPrompts(sessionID string) int

	// ClearQueue 清空一个会话的队列
	ClearQueue(sessionID string)
}

// agent ai agent
type agent struct {
	*pubsub.Broker[AgentEvent]                                    // ai agent 事件
	agentCfg                   config.Agent                       // ai agent 配置
	sessions                   session.Service                    // 会话管理
	messages                   message.Service                    // 消息管理
	permissions                permission.Service                 // 权限管理
	baseTools                  *csync.Map[string, tools.BaseTool] // 基础工具
	mcpTools                   *csync.Map[string, tools.BaseTool] // mcp 工具
	lspClients                 *csync.Map[string, *lsp.Client]    // LSP 客户端

	// 我们需要它才能在模型更改时更新它
	agentToolFn  func() (tools.BaseTool, error)
	cleanupFuncs []func()

	provider   provider.Provider // 模型提供者
	providerID string            // 模型提供者ID

	titleProvider       provider.Provider // 标题提供者
	summarizeProvider   provider.Provider // 摘要提供者
	summarizeProviderID string            // 摘要提供者ID

	activeRequests *csync.Map[string, context.CancelFunc] // 正在活动的请求请求id => cancel ctx 取消上文去终止所有活跃的协程
	promptQueue    *csync.Map[string, []string]           // 提示词队列
}

// agentPromptMap 提示词映射
var agentPromptMap = map[string]prompt.PromptID{
	"coder": prompt.PromptCoder,
	"task":  prompt.PromptTask,
}

// NewAgent 创建一个ai agent
func NewAgent(
	ctx context.Context,
	agentCfg config.Agent,
	// These services are needed in the tools
	permissions permission.Service,
	sessions session.Service,
	messages message.Service,
	history history.Service,
	lspClients *csync.Map[string, *lsp.Client],
) (Service, error) {

	// 获取配置
	cfg := config.Get()

	// 创建agent tool
	var agentToolFn func() (tools.BaseTool, error)

	// 如果ai agent是coder，并且agent允许coder工具，则创建coder tool
	if agentCfg.ID == "coder" && slices.Contains(agentCfg.AllowedTools, AgentToolName) {
		// 创建coder agent tool
		agentToolFn = func() (tools.BaseTool, error) {
			// 创建task agent
			taskAgentCfg := config.Get().Agents["task"]
			if taskAgentCfg.ID == "" {
				return nil, fmt.Errorf("task agent not found in config")
			}

			// 创建task agent
			taskAgent, err := NewAgent(ctx, taskAgentCfg, permissions, sessions, messages, history, lspClients)
			if err != nil {
				return nil, fmt.Errorf("failed to create task agent: %w", err)
			}

			// 创建coder agent tool,他传入task agent，他是一个处理调用工具的agent
			return NewAgentTool(taskAgent, sessions, messages), nil
		}
	}

	// 获取模型的提供者配置
	providerCfg := config.Get().GetProviderForModel(agentCfg.Model)
	if providerCfg == nil {
		return nil, fmt.Errorf("provider for agent %s not found in config", agentCfg.Name)
	}

	// 获取模型信息
	model := config.Get().GetModelByType(agentCfg.Model)
	if model == nil {
		return nil, fmt.Errorf("model not found for agent %s", agentCfg.Name)
	}

	// 获取ai agent 的提示词id
	// 根据配置里面的id获取提示词id
	promptID := agentPromptMap[agentCfg.ID]
	if promptID == "" {
		// 默认使用默认的提示词
		promptID = prompt.PromptDefault
	}

	// 构建模型提供者客户端选型
	opts := []provider.ProviderClientOption{
		provider.WithModel(agentCfg.Model),
		provider.WithSystemMessage(prompt.GetPrompt(promptID, providerCfg.ID, config.Get().Options.ContextPaths...)),
	}

	// 创建agent模型提供者
	agentProvider, err := provider.NewProvider(*providerCfg, opts...)
	if err != nil {
		return nil, err
	}

	// 创建小模型配置
	smallModelCfg := cfg.Models[config.SelectedModelTypeSmall]
	var smallModelProviderCfg *config.ProviderConfig
	if smallModelCfg.Provider == providerCfg.ID {
		// 如果小模型配置的提供者与ai agent的提供者相同，则使用ai agent的提供者
		smallModelProviderCfg = providerCfg
	} else {
		// 否则，使用小模型配置的提供者
		smallModelProviderCfg = cfg.GetProviderForModel(config.SelectedModelTypeSmall)

		if smallModelProviderCfg.ID == "" {
			return nil, fmt.Errorf("provider %s not found in config", smallModelCfg.Provider)
		}
	}

	// 获取小模型
	smallModel := cfg.GetModelByType(config.SelectedModelTypeSmall)
	if smallModel.ID == "" {
		return nil, fmt.Errorf("model %s not found in provider %s", smallModelCfg.Model, smallModelProviderCfg.ID)
	}

	// 创建标题提供者
	titleOpts := []provider.ProviderClientOption{
		provider.WithModel(config.SelectedModelTypeSmall),
		provider.WithSystemMessage(prompt.GetPrompt(prompt.PromptTitle, smallModelProviderCfg.ID)),
	}
	// 创建标题提供者
	titleProvider, err := provider.NewProvider(*smallModelProviderCfg, titleOpts...)
	if err != nil {
		return nil, err
	}

	// 创建摘要提供者配置选项
	summarizeOpts := []provider.ProviderClientOption{
		provider.WithModel(config.SelectedModelTypeLarge),
		provider.WithSystemMessage(prompt.GetPrompt(prompt.PromptSummarizer, providerCfg.ID)),
	}

	// 创建摘要提供者
	summarizeProvider, err := provider.NewProvider(*providerCfg, summarizeOpts...)
	if err != nil {
		return nil, err
	}

	// 创建基础工具
	baseToolsFn := func() map[string]tools.BaseTool {
		slog.Debug("Initializing agent base tools", "agent", agentCfg.ID)
		defer func() {
			slog.Debug("Initialized agent base tools", "agent", agentCfg.ID)
		}()

		// 所有代理均可使用的基本工具
		// 获取工作目录
		cwd := cfg.WorkingDir()
		result := make(map[string]tools.BaseTool)

		toolList := []tools.BaseTool{
			tools.NewBashTool(permissions, cwd, cfg.Options.Attribution),
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

		// 遍历工具对象列表
		for _, tool := range toolList {
			result[tool.Name()] = tool
		}
		return result
	}

	// 创建mcp工具
	mcpToolsFn := func() map[string]tools.BaseTool {
		slog.Debug("Initializing agent mcp tools", "agent", agentCfg.ID)
		defer func() {
			slog.Debug("Initialized agent mcp tools", "agent", agentCfg.ID)
		}()

		mcpToolsOnce.Do(func() {
			doGetMCPTools(ctx, permissions, cfg)
		})

		return maps.Collect(mcpTools.Seq2())
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
		agentToolFn:         agentToolFn,
		activeRequests:      csync.NewMap[string, context.CancelFunc](),
		mcpTools:            csync.NewLazyMap(mcpToolsFn),
		baseTools:           csync.NewLazyMap(baseToolsFn),
		promptQueue:         csync.NewMap[string, []string](),
		permissions:         permissions,
		lspClients:          lspClients,
	}
	a.setupEvents(ctx)
	return a, nil
}

// Model 获取模型
func (a *agent) Model() catwalk.Model {
	return *config.Get().GetModelByType(a.agentCfg.Model)
}

// Cancel 取消请求
func (a *agent) Cancel(sessionID string) {
	// 取消常规请求
	if cancel, ok := a.activeRequests.Take(sessionID); ok && cancel != nil {
		slog.Info("Request cancellation initiated", "session_id", sessionID)
		cancel()
	}

	// 取消摘要请求
	if cancel, ok := a.activeRequests.Take(sessionID + "-summarize"); ok && cancel != nil {
		slog.Info("Summarize cancellation initiated", "session_id", sessionID)
		cancel()
	}

	// 检查是否有排队的提示提示词
	if a.QueuedPrompts(sessionID) > 0 {
		slog.Info("Clearing queued prompts", "session_id", sessionID)
		a.promptQueue.Del(sessionID)
	}
}

// IsBusy 是否正在处理请求
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

// IsSessionBusy 获取指定session是否正在处理请求
func (a *agent) IsSessionBusy(sessionID string) bool {
	_, busy := a.activeRequests.Get(sessionID)
	return busy
}

// QueuedPrompts 获取指定session的排队提示词队列数量
func (a *agent) QueuedPrompts(sessionID string) int {
	l, ok := a.promptQueue.Get(sessionID)
	if !ok {
		return 0
	}
	return len(l)
}

// generateTitle 生成标题并将标题保存到session中
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

	// 使用摘要等流式方法
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

	// 流式返回，每一次都是一个最近的整体，所以这里一最后一次的结果就是整体内容
	// 第一次响应： "Hel"
	// 第二次响应： "Hello"
	// 第三次响应： "Hello wor"
	// 最终响应： "Hello world"
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

	// 格式化标题
	title := strings.ReplaceAll(finalResponse.Content, "\n", " ")

	if idx := strings.Index(title, "</think>"); idx > 0 {
		title = title[idx+len("</think>"):]
	}

	title = strings.TrimSpace(title)
	if title == "" {
		return nil
	}

	// 保存标题
	session.Title = title
	_, err = a.sessions.Save(ctx, session)
	return err
}

// err 创建一个错误事件
func (a *agent) err(err error) AgentEvent {
	return AgentEvent{
		Type:  AgentEventTypeError,
		Error: err,
	}
}

// Run 运行代理
func (a *agent) Run(ctx context.Context, sessionID string, content string, attachments ...message.Attachment) (<-chan AgentEvent, error) {
	// 检查是否支持图片
	if !a.Model().SupportsImages && attachments != nil {
		attachments = nil
	}

	// 创建agent事件通道
	events := make(chan AgentEvent, 1)

	// 检查是否正在处理请求
	if a.IsSessionBusy(sessionID) {
		// 添加到队列
		existing, ok := a.promptQueue.Get(sessionID)
		if !ok {
			existing = []string{}
		}
		existing = append(existing, content)
		a.promptQueue.Set(sessionID, existing)
		return nil, nil
	}

	// 创建取消函数
	genCtx, cancel := context.WithCancel(ctx)

	// 添加到正在处理的请求中map中
	a.activeRequests.Set(sessionID, cancel)

	// 统计请求时间
	startTime := time.Now()

	// 异步流式处理，通过chan发送结果
	go func() {
		slog.Debug("Request started", "sessionID", sessionID)
		defer log.RecoverPanic("agent.Run", func() {
			// 捕获panic
			events <- a.err(fmt.Errorf("panic while running the agent"))
		})

		// 创建附件内容消息数据结构
		var attachmentParts []message.ContentPart
		for _, attachment := range attachments {
			attachmentParts = append(attachmentParts, message.BinaryContent{Path: attachment.FilePath, MIMEType: attachment.MimeType, Data: attachment.Content})
		}

		// agent流式生成响应
		result := a.processGeneration(genCtx, sessionID, content, attachmentParts)
		if result.Error != nil {
			if isCancelledErr(result.Error) {
				slog.Error("Request canceled", "sessionID", sessionID)
			} else {
				slog.Error("Request errored", "sessionID", sessionID, "error", result.Error.Error())
				event.Error(result.Error)
			}
		} else {
			slog.Debug("Request completed", "sessionID", sessionID)
		}

		// 发送已响应事件
		a.eventPromptResponded(sessionID, time.Since(startTime).Truncate(time.Second))

		// 删除正在处理的请求, 让出当前请求会话占用，允许下一个请求开始
		a.activeRequests.Del(sessionID)

		// 触发取消信号，可能是结束a.activeRequests.Set(sessionID, cancel)的某些逻辑
		cancel()

		// 发送创建事件, 让外部知道请求已经创建，并且返回响应内容
		a.Publish(pubsub.CreatedEvent, result)

		// 将响应发送给订阅者
		events <- result

		// 关闭事件通道,通知订阅者，响应已经完成
		close(events)
	}()

	// 发送已发送事件
	a.eventPromptSent(sessionID)

	// 返回事件通道，订阅者可以通过这个通道获取响应
	return events, nil
}

// processGeneration 处理生成，优化消息历史，使用消息摘要，减少token，并且自动加载提示词队列中的等待消息，最后返回一个AgentEvent
func (a *agent) processGeneration(ctx context.Context, sessionID, content string, attachmentParts []message.ContentPart) AgentEvent {
	// 获取配置
	cfg := config.Get()

	// 列出现有会话消息；如果没有，则异步开始标题生成。
	msgs, err := a.messages.List(ctx, sessionID)
	if err != nil {
		return a.err(fmt.Errorf("failed to list messages: %w", err))
	}
	if len(msgs) == 0 {
		go func() {
			defer log.RecoverPanic("agent.Run", func() {
				slog.Error("panic while generating title")
			})
			titleErr := a.generateTitle(ctx, sessionID, content)
			if titleErr != nil && !errors.Is(titleErr, context.Canceled) && !errors.Is(titleErr, context.DeadlineExceeded) {
				slog.Error("failed to generate title", "error", titleErr)
			}
		}()
	}

	// 获取会话
	session, err := a.sessions.Get(ctx, sessionID)
	if err != nil {
		return a.err(fmt.Errorf("failed to get session: %w", err))
	}

	// 核心逻辑：用摘要消息替代冗长的历史对话,大幅减少token使用量
	// 这种设计是现代AI对话系统的核心优化技术，让系统能够"记住"重要信息而不会遗忘上下文
	// 原始消息: [消息1, 消息2, 消息3, ..., 消息20] (可能很长)
	// 摘要处理: [摘要消息, 消息18, 消息19, 消息20] (大幅缩短)
	//示例：
	//原始对话（30条）：
	//用户: 帮我写个Python函数计算阶乘
	//AI: def factorial(n):...
	//用户: 能加个缓存优化吗？
	//...（25条优化讨论）...
	//用户: 现在如何添加类型提示？ ← 当前问题
	//
	//摘要处理：
	//摘要消息: "用户要求编写带缓存的阶乘函数，已完成基础实现"
	//当前消息: "现在如何添加类型提示？"
	//
	//AI基于完整上下文提供类型提示方案

	if session.SummaryMessageID != "" {
		summaryMsgInex := -1
		// 步骤1：查找摘要消息在历史中的位置
		for i, msg := range msgs {
			// 消息id和会话摘要匹配，标记该消息
			if msg.ID == session.SummaryMessageID {
				// 找到摘要位置
				summaryMsgInex = i
				break
			}
		}
		if summaryMsgInex != -1 {
			// 保留从摘要开始的所有消息
			msgs = msgs[summaryMsgInex:]
			// 步骤3：将摘要消息角色改为用户
			msgs[0].Role = message.User
		}
	}

	// 创建用户消息
	userMsg, err := a.createUserMessage(ctx, sessionID, content, attachmentParts)
	if err != nil {
		return a.err(fmt.Errorf("failed to create user message: %w", err))
	}
	// 将新用户消息追加到对话历史记录中
	msgHistory := append(msgs, userMsg)

	for {
		// 每次迭代前检查取消情况
		select {
		case <-ctx.Done(): // 检查取消情况，这里应该是存在规则：如果取消，则返回取消错误
			return a.err(ctx.Err())
		default:
			// Continue processing
		}

		// 将整个历史记录发送给模型进行生成， 返回ai agent 的结果和工具执行结果
		agentMessage, toolResults, err := a.streamAndHandleEvents(ctx, sessionID, msgHistory)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				// Canceled
				agentMessage.AddFinish(message.FinishReasonCanceled, "Request cancelled", "")

				// Update the message
				a.messages.Update(context.Background(), agentMessage)
				return a.err(ErrRequestCancelled)
			}
			return a.err(fmt.Errorf("failed to process events: %w", err))
		}

		if cfg.Options.Debug {
			// 配置debug模式
			// 输出结果
			slog.Info("Result", "message", agentMessage.FinishReason(), "toolResults", toolResults)
		}

		// 如果agent结束内容生成的原因是因为需要使用工具，并且工具结果不为空
		if (agentMessage.FinishReason() == message.FinishReasonToolUse) && toolResults != nil {
			// 我们还没有完成，我们需要用工具回应响应
			msgHistory = append(msgHistory, agentMessage, *toolResults)

			// 获取当前会话的全部提示词，并且将其该会话的提示词从map中移除
			nextPrompt, ok := a.promptQueue.Take(sessionID)
			if ok {
				// 遍历提示词对啦，构建消息
				for _, prompt := range nextPrompt {
					// 为排队提示词创建新用户消息
					userMsg, err := a.createUserMessage(ctx, sessionID, prompt, nil)
					if err != nil {
						return a.err(fmt.Errorf("failed to create user message for queued prompt: %w", err))
					}
					// 将新用户消息追加到对话历史记录中
					msgHistory = append(msgHistory, userMsg)
				}
			}

			continue
		} else if agentMessage.FinishReason() == message.FinishReasonEndTurn { // 模型结束，并且需要结束当前轮次
			// 自然结束 模型认为自己已经完成了当前回合的回复 典型场景：问答完成后模型主动结束对话轮次 处理建议：这是最理想的结束状态，可继续后续对话
			// 完成当前一轮对话后，检查是否有排队的提示词，如果有需要拿出提示词继续对话
			queuePrompts, ok := a.promptQueue.Take(sessionID)
			if ok {
				for _, prompt := range queuePrompts {
					if prompt == "" {
						continue
					}
					userMsg, err := a.createUserMessage(ctx, sessionID, prompt, nil)
					if err != nil {
						return a.err(fmt.Errorf("failed to create user message for queued prompt: %w", err))
					}
					msgHistory = append(msgHistory, userMsg)
				}
				continue
			}
		}

		// agent 没有结束原因
		if agentMessage.FinishReason() == "" {
			// Kujtim: 无法追踪发生这种情况的位置，但这意味着它被取消了
			agentMessage.AddFinish(message.FinishReasonCanceled, "Request cancelled", "")
			_ = a.messages.Update(context.Background(), agentMessage)
			return a.err(ErrRequestCancelled)
		}

		// 整个对话结束，返回agent事件
		return AgentEvent{
			Type:    AgentEventTypeResponse,
			Message: agentMessage,
			Done:    true,
		}
	}
}

// createUserMessage 创建用户消息
func (a *agent) createUserMessage(ctx context.Context, sessionID, content string, attachmentParts []message.ContentPart) (message.Message, error) {
	parts := []message.ContentPart{message.TextContent{Text: content}}
	parts = append(parts, attachmentParts...)
	return a.messages.Create(ctx, sessionID, message.CreateMessageParams{
		Role:  message.User,
		Parts: parts,
	})
}

// getAllTools 获取所有工具
func (a *agent) getAllTools() ([]tools.BaseTool, error) {
	var allTools []tools.BaseTool

	// 获取基本工具
	for tool := range a.baseTools.Seq() {
		if a.agentCfg.AllowedTools == nil || slices.Contains(a.agentCfg.AllowedTools, tool.Name()) {
			allTools = append(allTools, tool)
		}
	}

	// 获取agent cmp 工具
	if a.agentCfg.ID == "coder" {
		allTools = slices.AppendSeq(allTools, a.mcpTools.Seq())
		if a.lspClients.Len() > 0 {
			allTools = append(allTools, tools.NewDiagnosticsTool(a.lspClients), tools.NewReferencesTool(a.lspClients))
		}
	}

	// 获取代理工具
	if a.agentToolFn != nil {
		agentTool, agentToolErr := a.agentToolFn()
		if agentToolErr != nil {
			return nil, agentToolErr
		}
		allTools = append(allTools, agentTool)
	}
	return allTools, nil
}

// streamAndHandleEvents 获取模型生成的结果，并返回结果和工具执行结果
func (a *agent) streamAndHandleEvents(ctx context.Context, sessionID string, msgHistory []message.Message) (message.Message, *message.Message, error) {
	// 将会话id写入ctx
	ctx = context.WithValue(ctx, tools.SessionIDContextKey, sessionID)

	// 首先创建助理消息，以便微调器立即显示
	// 创建一个助理消息，用于后续结构化处理
	assistantMsg, err := a.messages.Create(ctx, sessionID, message.CreateMessageParams{
		Role:     message.Assistant,
		Parts:    []message.ContentPart{},
		Model:    a.Model().ID,
		Provider: a.providerID,
	})
	if err != nil {
		return assistantMsg, nil, fmt.Errorf("failed to create assistant message: %w", err)
	}

	// 获取当前agent的所有工具
	allTools, toolsErr := a.getAllTools()
	if toolsErr != nil {
		return assistantMsg, nil, toolsErr
	}
	// Now collect tools (which may block on MCP initialization)
	// 现在收集工具，但这可能会导致MCP初始化
	// 模型提供者做流式响应
	eventChan := a.provider.StreamResponse(ctx, msgHistory, allTools)

	// 如果工具需要，请将会话和消息 ID 添加到上下文中
	ctx = context.WithValue(ctx, tools.MessageIDContextKey, assistantMsg.ID)

loop:
	for {
		select {
		case event, ok := <-eventChan: // 订阅监听模型处理消息chan，接收到模型提供者事件
			if !ok { // chan已关闭
				// 跳出循环
				break loop
			}
			// 处理模型提供者流式返回的各种事件，并且将其事件消息格式化，然后更新到message中
			if processErr := a.processEvent(ctx, sessionID, &assistantMsg, event); processErr != nil {
				if errors.Is(processErr, context.Canceled) { // 上下文被取消
					a.finishMessage(context.Background(), &assistantMsg, message.FinishReasonCanceled, "Request cancelled", "")
				} else { // API 错误
					a.finishMessage(ctx, &assistantMsg, message.FinishReasonError, "API Error", processErr.Error())
				}

				return assistantMsg, nil, processErr
			}
		case <-ctx.Done(): // 上下文被取消
			a.finishMessage(context.Background(), &assistantMsg, message.FinishReasonCanceled, "Request cancelled", "")
			return assistantMsg, nil, ctx.Err()
		}
	}

	// 初始化一个工具执行结果队列
	toolResults := make([]message.ToolResult, len(assistantMsg.ToolCalls()))

	// 获取当前辅助器的全部工具调用
	toolCalls := assistantMsg.ToolCalls()
	for i, toolCall := range toolCalls { // 遍历当前助手的全部工具调用
		select {
		case <-ctx.Done(): // 上下文被取消
			a.finishMessage(context.Background(), &assistantMsg, message.FinishReasonCanceled, "Request cancelled", "")
			// 取消所有未来的工具调用
			for j := i; j < len(toolCalls); j++ {
				toolResults[j] = message.ToolResult{
					ToolCallID: toolCalls[j].ID,
					Content:    "Tool execution canceled by user",
					IsError:    true,
				}
			}
			goto out // 跳转到out
		default:
			// 继续处理
			var tool tools.BaseTool

			// 获取agent的所有工具
			allTools, _ = a.getAllTools()
			for _, availableTool := range allTools {
				// 检查工具是否属于agent的工具
				if availableTool.Info().Name == toolCall.Name {
					tool = availableTool
					break
				}
			}

			// 找不到工具
			if tool == nil {
				// 将当前工具结果写入具体工具处理结果
				toolResults[i] = message.ToolResult{
					ToolCallID: toolCall.ID,
					Content:    fmt.Sprintf("Tool not found: %s", toolCall.Name),
					IsError:    true,
				}
				continue
			}

			// 在 goroutine 中运行工具以允许取消
			type toolExecResult struct {
				response tools.ToolResponse
				err      error
			}

			// 用于同步工具执行结果
			resultChan := make(chan toolExecResult, 1)

			go func() {
				// 执行工具
				response, err := tool.Run(ctx, tools.ToolCall{
					ID:    toolCall.ID,
					Name:  toolCall.Name,
					Input: toolCall.Input,
				})

				// 将执行结果写入chan
				resultChan <- toolExecResult{response: response, err: err}
			}()

			var toolResponse tools.ToolResponse
			var toolErr error

			// 等待工具执行结果或者上下文状态
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
				goto out // 跳转到out
			case result := <-resultChan: // 监听工具执行结果
				toolResponse = result.response
				toolErr = result.err
			}

			// 工具执行错误
			if toolErr != nil {
				slog.Error("Tool execution error", "toolCall", toolCall.ID, "error", toolErr)
				if errors.Is(toolErr, permission.ErrorPermissionDenied) {
					// 检查是否权限导致的问题
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

					// 发送调用结束消息
					a.finishMessage(ctx, &assistantMsg, message.FinishReasonPermissionDenied, "Permission denied", "")

					// 停止后续工具执行
					break
				}
			}

			// 记录当前工具执行结果
			toolResults[i] = message.ToolResult{
				ToolCallID: toolCall.ID,
				Content:    toolResponse.Content,
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
	// 创建一个工具消息
	msg, err := a.messages.Create(context.Background(), assistantMsg.SessionID, message.CreateMessageParams{
		Role:     message.Tool,
		Parts:    parts,
		Provider: a.providerID,
	})
	if err != nil {
		return assistantMsg, nil, fmt.Errorf("failed to create cancelled tool message: %w", err)
	}

	// 返回优化的消息和工具消息
	return assistantMsg, &msg, err
}

// finishMessage 结束消息
func (a *agent) finishMessage(ctx context.Context, msg *message.Message, finishReason message.FinishReason, message, details string) {
	msg.AddFinish(finishReason, message, details)
	_ = a.messages.Update(ctx, *msg)
}

// processEvent 处理各种事件，并且将内容更新到消息中
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
		assistantMsg.FinishThinking()
		assistantMsg.AppendContent(event.Content)
		return a.messages.Update(ctx, *assistantMsg)
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
		// 结束推理
		assistantMsg.FinishThinking()
		// 设置工具调用
		assistantMsg.SetToolCalls(event.Response.ToolCalls)
		assistantMsg.AddFinish(event.Response.FinishReason, "", "")
		if err := a.messages.Update(ctx, *assistantMsg); err != nil {
			return fmt.Errorf("failed to update message: %w", err)
		}
		return a.trackUsage(ctx, sessionID, a.Model(), event.Response.Usage)
	}

	return nil
}

// trackUsage 跟踪模型的使用情况，并更新会话的消耗和令牌使用情况。
func (a *agent) trackUsage(ctx context.Context, sessionID string, model catwalk.Model, usage provider.TokenUsage) error {
	sess, err := a.sessions.Get(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("failed to get session: %w", err)
	}

	// 计算模型消耗
	cost := model.CostPer1MInCached/1e6*float64(usage.CacheCreationTokens) +
		model.CostPer1MOutCached/1e6*float64(usage.CacheReadTokens) +
		model.CostPer1MIn/1e6*float64(usage.InputTokens) +
		model.CostPer1MOut/1e6*float64(usage.OutputTokens)

	// 创建token使用事件
	a.eventTokensUsed(sessionID, usage, cost)

	// 统计模型消耗
	sess.Cost += cost

	// 统计完成的消耗
	sess.CompletionTokens = usage.OutputTokens + usage.CacheReadTokens

	// 统计输入消耗
	sess.PromptTokens = usage.InputTokens + usage.CacheCreationTokens

	// 保存到会话中
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
			Type:     AgentEventTypeSummarize,
			Progress: "Starting summarization...",
		}

		a.Publish(pubsub.CreatedEvent, event)
		// Get all messages from the session
		msgs, err := a.messages.List(summarizeCtx, sessionID)
		if err != nil {
			event = AgentEvent{
				Type:  AgentEventTypeError,
				Error: fmt.Errorf("failed to list messages: %w", err),
				Done:  true,
			}
			a.Publish(pubsub.CreatedEvent, event)
			return
		}
		summarizeCtx = context.WithValue(summarizeCtx, tools.SessionIDContextKey, sessionID)

		if len(msgs) == 0 {
			event = AgentEvent{
				Type:  AgentEventTypeError,
				Error: fmt.Errorf("no messages to summarize"),
				Done:  true,
			}
			a.Publish(pubsub.CreatedEvent, event)
			return
		}

		event = AgentEvent{
			Type:     AgentEventTypeSummarize,
			Progress: "Analyzing conversation...",
		}
		a.Publish(pubsub.CreatedEvent, event)

		// Add a system message to guide the summarization
		summarizePrompt := "Provide a detailed but concise summary of our conversation above. Focus on information that would be helpful for continuing the conversation, including what we did, what we're doing, which files we're working on, and what we're going to do next."

		// Create a new message with the summarize prompt
		promptMsg := message.Message{
			Role:  message.User,
			Parts: []message.ContentPart{message.TextContent{Text: summarizePrompt}},
		}

		// Append the prompt to the messages
		msgsWithPrompt := append(msgs, promptMsg)

		event = AgentEvent{
			Type:     AgentEventTypeSummarize,
			Progress: "Generating summary...",
		}

		a.Publish(pubsub.CreatedEvent, event)

		// Send the messages to the summarize provider
		response := a.summarizeProvider.StreamResponse(
			summarizeCtx,
			msgsWithPrompt,
			nil,
		)
		var finalResponse *provider.ProviderResponse
		for r := range response {
			if r.Error != nil {
				event = AgentEvent{
					Type:  AgentEventTypeError,
					Error: fmt.Errorf("failed to summarize: %w", r.Error),
					Done:  true,
				}
				a.Publish(pubsub.CreatedEvent, event)
				return
			}
			finalResponse = r.Response
		}

		summary := strings.TrimSpace(finalResponse.Content)
		if summary == "" {
			event = AgentEvent{
				Type:  AgentEventTypeError,
				Error: fmt.Errorf("empty summary returned"),
				Done:  true,
			}
			a.Publish(pubsub.CreatedEvent, event)
			return
		}
		shell := shell.GetPersistentShell(config.Get().WorkingDir())
		summary += "\n\n**Current working directory of the persistent shell**\n\n" + shell.GetWorkingDir()
		event = AgentEvent{
			Type:     AgentEventTypeSummarize,
			Progress: "Creating new session...",
		}

		a.Publish(pubsub.CreatedEvent, event)
		oldSession, err := a.sessions.Get(summarizeCtx, sessionID)
		if err != nil {
			event = AgentEvent{
				Type:  AgentEventTypeError,
				Error: fmt.Errorf("failed to get session: %w", err),
				Done:  true,
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
				Type:  AgentEventTypeError,
				Error: fmt.Errorf("failed to create summary message: %w", err),
				Done:  true,
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
				Type:  AgentEventTypeError,
				Error: fmt.Errorf("failed to save session: %w", err),
				Done:  true,
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

// ClearQueue 清空队列
func (a *agent) ClearQueue(sessionID string) {
	if a.QueuedPrompts(sessionID) > 0 {
		slog.Info("Clearing queued prompts", "session_id", sessionID)
		a.promptQueue.Del(sessionID)
	}
}

// CancelAll 取消所有活动请求
func (a *agent) CancelAll() {
	if !a.IsBusy() {
		// 不存在请求，直接返回
		return
	}
	for key := range a.activeRequests.Seq2() {
		a.Cancel(key) // key is sessionID
	}

	for _, cleanup := range a.cleanupFuncs {
		if cleanup != nil {
			// 执行所有清理函数
			cleanup()
		}
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

func (a *agent) UpdateModel() error {
	cfg := config.Get()

	// Get current provider configuration
	currentProviderCfg := cfg.GetProviderForModel(a.agentCfg.Model)
	if currentProviderCfg == nil || currentProviderCfg.ID == "" {
		return fmt.Errorf("provider for agent %s not found in config", a.agentCfg.Name)
	}

	// 检查提供商是否已更改
	if string(currentProviderCfg.ID) != a.providerID {
		// 提供商更改，需要重新创建主提供商
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

		// 更新提供商和提供商 ID
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

	var maxTitleTokens int64 = 40

	// if the max output is too low for the gemini provider it won't return anything
	if smallModelCfg.Provider == "gemini" {
		maxTitleTokens = 1000
	}
	// Recreate title provider
	titleOpts := []provider.ProviderClientOption{
		provider.WithModel(config.SelectedModelTypeSmall),
		provider.WithSystemMessage(prompt.GetPrompt(prompt.PromptTitle, smallModelProviderCfg.ID)),
		provider.WithMaxTokens(maxTitleTokens),
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

// SubscribeMCPEvents 订阅MCP事件
func (a *agent) setupEvents(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)

	go func() {
		// 订阅MCP事件
		subCh := SubscribeMCPEvents(ctx)

		for {
			select {
			case event, ok := <-subCh: // 监听MCP事件
				if !ok {
					slog.Debug("MCPEvents subscription channel closed")
					return
				}
				switch event.Payload.Type {
				case MCPEventToolsListChanged: // MCP工具列表已更改
					name := event.Payload.Name

					// 获取MCP客户端
					c, ok := mcpClients.Get(name)
					if !ok {
						slog.Warn("MCP client not found for tools update", "name", name)
						continue
					}

					// 获取配置
					cfg := config.Get()

					// 从配置中获取工具
					tools, err := getTools(ctx, name, a.permissions, c, cfg.WorkingDir())
					if err != nil {
						slog.Error("error listing tools", "error", err)
						updateMCPState(name, MCPStateError, err, nil, 0)
						_ = c.Close()
						continue
					}

					// 更新MCP工具
					updateMcpTools(name, tools)

					// 重置MCP工具
					a.mcpTools.Reset(maps.Collect(mcpTools.Seq2()))

					// 更新MCP状态
					updateMCPState(name, MCPStateConnected, nil, c, a.mcpTools.Len())
				default:
					continue
				}
			case <-ctx.Done(): // 监听取消信号，终止订阅
				slog.Debug("MCPEvents subscription cancelled")
				return
			}
		}
	}()

	// 将订阅函数添加到清理函数列表中，以便在取消时取消订阅
	a.cleanupFuncs = append(a.cleanupFuncs, cancel)
}
