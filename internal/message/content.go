package message

import (
	"encoding/base64"
	"slices"
	"time"

	"github.com/charmbracelet/catwalk/pkg/catwalk"
)

type MessageRole string

const (
	Assistant MessageRole = "assistant"
	User      MessageRole = "user"
	System    MessageRole = "system"
	Tool      MessageRole = "tool"
)

type FinishReason string

const (
	// FinishReasonEndTurn 自然结束
	// 模型认为自己已经完成了当前回合的回复
	// 典型场景：问答完成后模型主动结束对话轮次
	// 处理建议：这是最理想的结束状态，可继续后续对话
	FinishReasonEndTurn FinishReason = "end_turn"

	// FinishReasonMaxTokens 达到token限制
	// 因达到最大生成token数限制而被强制停止
	// 典型场景：生成长文本时触发生成长度上限
	// 处理建议：可能需要调整max_tokens参数或分段生成
	FinishReasonMaxTokens FinishReason = "max_tokens"

	// FinishReasonToolUse 需要工具调用
	// 模型决定调用外部工具/函数而暂停文本生成
	// 典型场景：模型需要调用计算器、查询API等外部功能
	// 处理建议：执行相应工具后，将结果返回给模型继续生成
	FinishReasonToolUse FinishReason = "tool_use"

	// FinishReasonCanceled 请求被取消
	// 用户或系统主动取消了生成请求
	// 典型场景：用户点击"停止生成"按钮、超时取消等
	// 处理建议：清理资源，可能需要进行回滚操作
	FinishReasonCanceled FinishReason = "canceled"

	// FinishReasonError 发生错误
	// 模型推理过程中发生技术错误
	// 典型场景：模型服务内部错误、网络问题等
	// 处理建议：记录错误日志，可能需要进行重试
	FinishReasonError FinishReason = "error"

	// FinishReasonPermissionDenied 权限拒绝
	// 因权限不足而停止生成
	// 典型场景：尝试访问受限内容、API密钥无效等
	// 处理建议：检查权限设置和认证信息
	FinishReasonPermissionDenied FinishReason = "permission_denied"

	// FinishReasonUnknown 未知原因
	// 理论上不应该出现的未知结束原因
	// 典型场景：系统异常、版本不兼容等边界情况
	// 处理建议：作为兜底处理，记录详细日志供排查
	FinishReasonUnknown FinishReason = "unknown"
)

type ContentPart interface {
	isPart()
}

type ReasoningContent struct {
	Thinking   string `json:"thinking"`              // 模型的思考过程/推理链
	Signature  string `json:"signature"`             // 数字签名/验证信息，防止被篡改
	StartedAt  int64  `json:"started_at,omitempty"`  // 推理开始时间戳
	FinishedAt int64  `json:"finished_at,omitempty"` // 推理结束时间戳
}

func (tc ReasoningContent) String() string {
	return tc.Thinking
}
func (ReasoningContent) isPart() {}

type TextContent struct {
	Text string `json:"text"`
}

func (tc TextContent) String() string {
	return tc.Text
}

func (TextContent) isPart() {}

type ImageURLContent struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

func (iuc ImageURLContent) String() string {
	return iuc.URL
}

func (ImageURLContent) isPart() {}

type BinaryContent struct {
	Path     string
	MIMEType string
	Data     []byte
}

func (bc BinaryContent) String(p catwalk.InferenceProvider) string {
	base64Encoded := base64.StdEncoding.EncodeToString(bc.Data)
	if p == catwalk.InferenceProviderOpenAI {
		return "data:" + bc.MIMEType + ";base64," + base64Encoded
	}
	return base64Encoded
}

func (BinaryContent) isPart() {}

// ToolCall 工具调用请求结构体
type ToolCall struct {
	ID       string `json:"id"`       // 调用唯一标识
	Name     string `json:"name"`     // 工具函数名
	Input    string `json:"input"`    // 调用参数（通常是JSON）
	Type     string `json:"type"`     // 工具类型
	Finished bool   `json:"finished"` // 是否完成
}

func (ToolCall) isPart() {}

// ToolResult 工具调用结果结构体
type ToolResult struct {
	ToolCallID string `json:"tool_call_id"` // 关联的调用ID
	Name       string `json:"name"`         // 工具名（与ToolCall对应）
	Content    string `json:"content"`      // 工具返回结果
	Metadata   string `json:"metadata"`     // 元数据（如执行耗时、状态码）
	IsError    bool   `json:"is_error"`     // 是否出错
}

func (ToolResult) isPart() {}

type Finish struct {
	Reason  FinishReason `json:"reason"`
	Time    int64        `json:"time"`
	Message string       `json:"message,omitempty"`
	Details string       `json:"details,omitempty"`
}

func (Finish) isPart() {}

type Message struct {
	ID        string
	Role      MessageRole
	SessionID string
	Parts     []ContentPart
	Model     string
	Provider  string
	CreatedAt int64
	UpdatedAt int64
}

func (m *Message) Content() TextContent {
	for _, part := range m.Parts {
		if c, ok := part.(TextContent); ok {
			return c
		}
	}
	return TextContent{}
}

func (m *Message) ReasoningContent() ReasoningContent {
	for _, part := range m.Parts {
		if c, ok := part.(ReasoningContent); ok {
			return c
		}
	}
	return ReasoningContent{}
}

func (m *Message) ImageURLContent() []ImageURLContent {
	imageURLContents := make([]ImageURLContent, 0)
	for _, part := range m.Parts {
		if c, ok := part.(ImageURLContent); ok {
			imageURLContents = append(imageURLContents, c)
		}
	}
	return imageURLContents
}

func (m *Message) BinaryContent() []BinaryContent {
	binaryContents := make([]BinaryContent, 0)
	for _, part := range m.Parts {
		if c, ok := part.(BinaryContent); ok {
			binaryContents = append(binaryContents, c)
		}
	}
	return binaryContents
}

func (m *Message) ToolCalls() []ToolCall {
	toolCalls := make([]ToolCall, 0)
	for _, part := range m.Parts {
		if c, ok := part.(ToolCall); ok {
			toolCalls = append(toolCalls, c)
		}
	}
	return toolCalls
}

func (m *Message) ToolResults() []ToolResult {
	toolResults := make([]ToolResult, 0)
	for _, part := range m.Parts {
		if c, ok := part.(ToolResult); ok {
			toolResults = append(toolResults, c)
		}
	}
	return toolResults
}

func (m *Message) IsFinished() bool {
	for _, part := range m.Parts {
		if _, ok := part.(Finish); ok {
			return true
		}
	}
	return false
}

func (m *Message) FinishPart() *Finish {
	for _, part := range m.Parts {
		if c, ok := part.(Finish); ok {
			return &c
		}
	}
	return nil
}

func (m *Message) FinishReason() FinishReason {
	for _, part := range m.Parts {
		if c, ok := part.(Finish); ok {
			return c.Reason
		}
	}
	return ""
}

func (m *Message) IsThinking() bool {
	if m.ReasoningContent().Thinking != "" && m.Content().Text == "" && !m.IsFinished() {
		return true
	}
	return false
}

func (m *Message) AppendContent(delta string) {
	found := false
	for i, part := range m.Parts {
		if c, ok := part.(TextContent); ok {
			m.Parts[i] = TextContent{Text: c.Text + delta}
			found = true
		}
	}
	if !found {
		m.Parts = append(m.Parts, TextContent{Text: delta})
	}
}

// AppendReasoningContent 追加推理内容
func (m *Message) AppendReasoningContent(delta string) {
	// 是否追加推理结果
	found := false
	for i, part := range m.Parts {
		if c, ok := part.(ReasoningContent); ok {
			m.Parts[i] = ReasoningContent{
				Thinking:   c.Thinking + delta,
				Signature:  c.Signature,
				StartedAt:  c.StartedAt,
				FinishedAt: c.FinishedAt,
			}
			found = true
		}
	}
	if !found {
		// 创建推理结果
		m.Parts = append(m.Parts, ReasoningContent{
			Thinking:  delta,
			StartedAt: time.Now().Unix(),
		})
	}
}

// AppendReasoningSignature 追加推理签名
func (m *Message) AppendReasoningSignature(signature string) {
	for i, part := range m.Parts {
		if c, ok := part.(ReasoningContent); ok {
			m.Parts[i] = ReasoningContent{
				Thinking:   c.Thinking,
				Signature:  c.Signature + signature,
				StartedAt:  c.StartedAt,
				FinishedAt: c.FinishedAt,
			}
			return
		}
	}
	m.Parts = append(m.Parts, ReasoningContent{Signature: signature})
}

// FinishThinking 结束推理
func (m *Message) FinishThinking() {
	for i, part := range m.Parts {
		if c, ok := part.(ReasoningContent); ok {
			if c.FinishedAt == 0 {
				m.Parts[i] = ReasoningContent{
					Thinking:   c.Thinking,
					Signature:  c.Signature,
					StartedAt:  c.StartedAt,
					FinishedAt: time.Now().Unix(),
				}
			}
			return
		}
	}
}

func (m *Message) ThinkingDuration() time.Duration {
	reasoning := m.ReasoningContent()
	if reasoning.StartedAt == 0 {
		return 0
	}

	endTime := reasoning.FinishedAt
	if endTime == 0 {
		endTime = time.Now().Unix()
	}

	return time.Duration(endTime-reasoning.StartedAt) * time.Second
}

// FinishToolCall 结束工具调用
func (m *Message) FinishToolCall(toolCallID string) {
	for i, part := range m.Parts {
		if c, ok := part.(ToolCall); ok {
			if c.ID == toolCallID {
				m.Parts[i] = ToolCall{
					ID:       c.ID,
					Name:     c.Name,
					Input:    c.Input,
					Type:     c.Type,
					Finished: true,
				}
				return
			}
		}
	}
}

// AppendToolCallInput 追加工具调用参数输入
func (m *Message) AppendToolCallInput(toolCallID string, inputDelta string) {
	for i, part := range m.Parts {
		if c, ok := part.(ToolCall); ok {
			if c.ID == toolCallID {
				m.Parts[i] = ToolCall{
					ID:       c.ID,
					Name:     c.Name,
					Input:    c.Input + inputDelta,
					Type:     c.Type,
					Finished: c.Finished,
				}
				return
			}
		}
	}
}

// AddToolCall 添加工具调用请求
func (m *Message) AddToolCall(tc ToolCall) {
	for i, part := range m.Parts {
		if c, ok := part.(ToolCall); ok {
			if c.ID == tc.ID {
				m.Parts[i] = tc
				return
			}
		}
	}
	m.Parts = append(m.Parts, tc)
}

func (m *Message) SetToolCalls(tc []ToolCall) {
	// 删除任何现有的工具调用部分，它可能有多个
	parts := make([]ContentPart, 0)
	for _, part := range m.Parts {
		if _, ok := part.(ToolCall); ok {
			continue
		}
		parts = append(parts, part)
	}
	m.Parts = parts
	for _, toolCall := range tc {
		m.Parts = append(m.Parts, toolCall)
	}
}

func (m *Message) AddToolResult(tr ToolResult) {
	m.Parts = append(m.Parts, tr)
}

func (m *Message) SetToolResults(tr []ToolResult) {
	for _, toolResult := range tr {
		m.Parts = append(m.Parts, toolResult)
	}
}

func (m *Message) AddFinish(reason FinishReason, message, details string) {
	// remove any existing finish part
	for i, part := range m.Parts {
		if _, ok := part.(Finish); ok {
			m.Parts = slices.Delete(m.Parts, i, i+1)
			break
		}
	}
	m.Parts = append(m.Parts, Finish{Reason: reason, Time: time.Now().Unix(), Message: message, Details: details})
}

func (m *Message) AddImageURL(url, detail string) {
	m.Parts = append(m.Parts, ImageURLContent{URL: url, Detail: detail})
}

func (m *Message) AddBinary(mimeType string, data []byte) {
	m.Parts = append(m.Parts, BinaryContent{MIMEType: mimeType, Data: data})
}
