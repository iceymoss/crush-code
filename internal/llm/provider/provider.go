package provider

import (
	"context"
	"fmt"

	"github.com/charmbracelet/catwalk/pkg/catwalk"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/llm/tools"
	"github.com/charmbracelet/crush/internal/message"
)

// EventType 定义大模型流式响应的事件类型
type EventType string

// maxRetries 定义最大重试次数，用于API调用失败时的重试机制
const maxRetries = 3

// 大模型流式响应事件类型常量定义
const (
	// EventContentStart 内容生成开始事件
	// 触发时机：大模型开始生成回复内容时
	// 典型用途：初始化UI状态，显示"正在输入"指示器
	EventContentStart EventType = "content_start"

	// EventToolUseStart 工具调用开始事件
	// 触发时机：大模型开始调用外部工具/函数时
	// 典型用途：准备工具执行环境，显示工具加载状态
	EventToolUseStart EventType = "tool_use_start"

	// EventToolUseDelta 工具调用过程更新事件
	// 触发时机：工具调用过程中有增量数据返回时
	// 典型用途：实时显示工具执行进度或中间结果
	EventToolUseDelta EventType = "tool_use_delta"

	// EventToolUseStop 工具调用结束事件
	// 触发时机：工具调用完成时
	// 典型用途：清理工具资源，更新调用结果状态
	EventToolUseStop EventType = "tool_use_stop"

	// EventContentDelta 内容增量更新事件
	// 触发时机：大模型生成回复内容的每个增量片段时
	// 典型用途：实时显示生成的文本（打字机效果）
	EventContentDelta EventType = "content_delta"

	// EventThinkingDelta 思维链更新事件
	// 触发时机：大模型展示内部推理过程时
	// 典型用途：显示模型的思考过程（增强可解释性）
	EventThinkingDelta EventType = "thinking_delta"

	// EventSignatureDelta 签名信息更新事件
	// 触发时机：生成数字签名或验证信息时
	// 典型用途：安全验证、内容完整性检查
	EventSignatureDelta EventType = "signature_delta"

	// EventContentStop 内容生成结束事件
	// 触发时机：大模型完成当前轮次的内容生成时
	// 典型用途：标记内容生成完成，可以进行后续处理
	EventContentStop EventType = "content_stop"

	// EventComplete 请求完全结束事件
	// 触发时机：整个API请求处理完成时
	// 典型用途：清理请求资源，触发回调函数
	EventComplete EventType = "complete"

	// EventError 错误事件
	// 触发时机：处理过程中发生错误时
	// 典型用途：错误处理、重试机制、用户提示
	EventError EventType = "error"

	// EventWarning 警告事件
	// 触发时机：处理过程中出现非致命性问题时
	// 典型用途：警告提示、日志记录、降级处理
	EventWarning EventType = "warning"
)

// 事件流典型处理流程示例：
//
// 正常对话流程：
// EventContentStart → EventContentDelta(多次) → EventContentStop → EventComplete
//
// 工具调用流程：
// EventContentStart → EventToolUseStart → EventToolUseDelta → EventToolUseStop
// → EventContentDelta → EventContentStop → EventComplete
//
// 错误处理流程：
// EventContentStart → EventError → (根据maxRetries重试) → EventComplete

// TokenUsage 定义大模型生成的内容消耗情况
type TokenUsage struct {
	InputTokens         int64
	OutputTokens        int64
	CacheCreationTokens int64
	CacheReadTokens     int64
}

// ProviderResponse 描述大模型生成的内容、工具调用、消耗情况、完成原因
type ProviderResponse struct {
	Content      string               // 生成的内容
	ToolCalls    []message.ToolCall   // 工具调用
	Usage        TokenUsage           // 内容消耗情况
	FinishReason message.FinishReason // 完成原因
}

// ProviderEvent 模型提供者事件
type ProviderEvent struct {
	Type      EventType         // 事件类型
	Content   string            // 事件内容
	Thinking  string            // 思维ing
	Signature string            // 签名信息
	Response  *ProviderResponse // 模型提供者响应
	ToolCall  *message.ToolCall // 工具调用
	Error     error             // 错误信息
}

// Provider 描述大模型提供者接口, 为agent或者其他模块提供调用
type Provider interface {
	// SendMessages 发送消息并返回响应
	SendMessages(ctx context.Context, messages []message.Message, tools []tools.BaseTool) (*ProviderResponse, error)

	// StreamResponse 模型提供者流式响应
	StreamResponse(ctx context.Context, messages []message.Message, tools []tools.BaseTool) <-chan ProviderEvent

	// Model 获取模型信息
	Model() catwalk.Model
}

// ProviderClientOptions 描述模型提供者客户端选项
type providerClientOptions struct {
	baseURL            string                                       // 基础URL
	config             config.ProviderConfig                        // 配置
	apiKey             string                                       // API密钥
	modelType          config.SelectedModelType                     // 模型类型
	model              func(config.SelectedModelType) catwalk.Model // 模型
	disableCache       bool                                         // 禁用缓存
	systemMessage      string                                       // 系统提示
	systemPromptPrefix string                                       // 系统提示前缀
	maxTokens          int64                                        // 最大Tokens
	extraHeaders       map[string]string                            // 额外HTTP头
	extraBody          map[string]any                               // 额外的请求体
	extraParams        map[string]string                            // 额外的参数
}

// ProviderClientOption 描述模型提供者客户端选项
type ProviderClientOption func(*providerClientOptions)

// ProviderClient 描述模型提供者客户端接口
type ProviderClient interface {
	// send 发送消息并返回响应
	send(ctx context.Context, messages []message.Message, tools []tools.BaseTool) (*ProviderResponse, error)

	// stream 模型提供者流式响应
	stream(ctx context.Context, messages []message.Message, tools []tools.BaseTool) <-chan ProviderEvent

	// Model 获取模型信息
	Model() catwalk.Model
}

type baseProvider[C ProviderClient] struct {
	options providerClientOptions
	client  C
}

func (p *baseProvider[C]) cleanMessages(messages []message.Message) (cleaned []message.Message) {
	for _, msg := range messages {
		// The message has no content
		if len(msg.Parts) == 0 {
			continue
		}
		cleaned = append(cleaned, msg)
	}
	return cleaned
}

func (p *baseProvider[C]) SendMessages(ctx context.Context, messages []message.Message, tools []tools.BaseTool) (*ProviderResponse, error) {
	messages = p.cleanMessages(messages)
	return p.client.send(ctx, messages, tools)
}

func (p *baseProvider[C]) StreamResponse(ctx context.Context, messages []message.Message, tools []tools.BaseTool) <-chan ProviderEvent {
	messages = p.cleanMessages(messages)
	return p.client.stream(ctx, messages, tools)
}

func (p *baseProvider[C]) Model() catwalk.Model {
	return p.client.Model()
}

// WithModel 描述模型提供者客户端选项, 指定模型
func WithModel(model config.SelectedModelType) ProviderClientOption {
	return func(options *providerClientOptions) {
		options.modelType = model
	}
}

// WithDisableCache 描述模型提供者客户端选项, 禁用缓存
func WithDisableCache(disableCache bool) ProviderClientOption {
	return func(options *providerClientOptions) {
		options.disableCache = disableCache
	}
}

// WithSystemMessage 描述模型提供者客户端选项, 指定系统提示
func WithSystemMessage(systemMessage string) ProviderClientOption {
	return func(options *providerClientOptions) {
		options.systemMessage = systemMessage
	}
}

// WithMaxTokens 描述模型提供者客户端选项, 指定最大Tokens
func WithMaxTokens(maxTokens int64) ProviderClientOption {
	return func(options *providerClientOptions) {
		options.maxTokens = maxTokens
	}
}

// NewProvider 创建模型提供者
//   - cfg: 模型提供者配置
//   - opts: 模型提供者选项
func NewProvider(cfg config.ProviderConfig, opts ...ProviderClientOption) (Provider, error) {
	restore := config.PushPopCrushEnv()
	defer restore()
	resolvedAPIKey, err := config.Get().Resolve(cfg.APIKey)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve API key for provider %s: %w", cfg.ID, err)
	}

	// Resolve extra headers
	resolvedExtraHeaders := make(map[string]string)
	for key, value := range cfg.ExtraHeaders {
		resolvedValue, err := config.Get().Resolve(value)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve extra header %s for provider %s: %w", key, cfg.ID, err)
		}
		resolvedExtraHeaders[key] = resolvedValue
	}

	clientOptions := providerClientOptions{
		baseURL:            cfg.BaseURL,
		config:             cfg,
		apiKey:             resolvedAPIKey,
		extraHeaders:       resolvedExtraHeaders,
		extraBody:          cfg.ExtraBody,
		extraParams:        cfg.ExtraParams,
		systemPromptPrefix: cfg.SystemPromptPrefix,
		model: func(tp config.SelectedModelType) catwalk.Model {
			return *config.Get().GetModelByType(tp)
		},
	}
	for _, o := range opts {
		// 设置模型选项
		o(&clientOptions)
	}

	switch cfg.Type { // 根据模型类型创建模型提供者
	case catwalk.TypeAnthropic:
		return &baseProvider[AnthropicClient]{
			options: clientOptions,
			client:  newAnthropicClient(clientOptions, AnthropicClientTypeNormal),
		}, nil
	case catwalk.TypeOpenAI:
		return &baseProvider[OpenAIClient]{
			options: clientOptions,
			client:  newOpenAIClient(clientOptions),
		}, nil
	case catwalk.TypeGemini:
		return &baseProvider[GeminiClient]{
			options: clientOptions,
			client:  newGeminiClient(clientOptions),
		}, nil
	case catwalk.TypeBedrock:
		return &baseProvider[BedrockClient]{
			options: clientOptions,
			client:  newBedrockClient(clientOptions),
		}, nil
	case catwalk.TypeAzure:
		return &baseProvider[AzureClient]{
			options: clientOptions,
			client:  newAzureClient(clientOptions),
		}, nil
	case catwalk.TypeVertexAI:
		return &baseProvider[VertexAIClient]{
			options: clientOptions,
			client:  newVertexAIClient(clientOptions),
		}, nil
	}
	return nil, fmt.Errorf("provider not supported: %s", cfg.Type)
}
