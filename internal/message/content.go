package message

import (
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"charm.land/fantasy/providers/anthropic"
	"charm.land/fantasy/providers/google"
	"charm.land/fantasy/providers/openai"
)

// MessageRole 消息角色
// Assistant: 助手角色，用于回复用户消息
// User: 用户角色，用于发送用户消息
// System: 系统角色，用于发送系统消息
// Tool: 工具角色，用于发送工具消息
type MessageRole string

const (
	// Assistant: 助手角色，用于回复用户消息
	Assistant MessageRole = "assistant"
	// User: 用户角色，用于发送用户消息
	User MessageRole = "user"
	// System: 系统角色，用于发送系统消息
	System MessageRole = "system"
	// Tool: 工具角色，用于发送工具消息
	Tool MessageRole = "tool"
)

// FinishReason 完成原因
type FinishReason string

const (
	// FinishReasonEndTurn: 结束回合
	FinishReasonEndTurn FinishReason = "end_turn"
	// FinishReasonMaxTokens: 超过最大令牌数
	FinishReasonMaxTokens FinishReason = "max_tokens"
	// FinishReasonToolUse: 使用工具
	FinishReasonToolUse FinishReason = "tool_use"
	// FinishReasonCanceled: 取消
	FinishReasonCanceled FinishReason = "canceled"
	// FinishReasonError: 错误
	FinishReasonError FinishReason = "error"
	// FinishReasonPermissionDenied: 权限不足
	FinishReasonPermissionDenied FinishReason = "permission_denied"

	// FinishReasonUnknown: 未知
	FinishReasonUnknown FinishReason = "unknown"
)

// ContentPart 内容部分
// 定义了消息内容的部分,例如,文本、图片、音频、视频等
type ContentPart interface {
	isPart()
}

// ReasoningContent 推理内容
// 定义了推理内容的结构,例如,思考、签名、思考签名、工具ID、响应数据、开始时间、完成时间等
type ReasoningContent struct {
	Thinking         string                             `json:"thinking"`              // 思考内容
	Signature        string                             `json:"signature"`             // 签名
	ThoughtSignature string                             `json:"thought_signature"`     // 思考签名
	ToolID           string                             `json:"tool_id"`               // 工具ID
	ResponsesData    *openai.ResponsesReasoningMetadata `json:"responses_data"`        // 响应数据
	StartedAt        int64                              `json:"started_at,omitempty"`  // 开始时间
	FinishedAt       int64                              `json:"finished_at,omitempty"` // 完成时间
}

// String 返回推理内容字符串
func (tc ReasoningContent) String() string {
	return tc.Thinking
}

// isPart 实现ContentPart接口，只是为了实现ContentPart接口，并没有实际意义，让其CreateMessageParams.Parts可以包含推理内容,其实是一种多态的体现
func (ReasoningContent) isPart() {}

type TextContent struct {
	Text string `json:"text"` // 文本内容
}

// String 返回文本内容字符串
func (tc TextContent) String() string {
	return tc.Text
}

// isPart 实现ContentPart接口，只是为了实现ContentPart接口，并没有实际意义，让其CreateMessageParams.Parts可以包含文本内容,其实是一种多态的体现
func (TextContent) isPart() {}

// ImageURLContent 图片url内容
// 定义了图片url内容的结构,例如,图片url、详情等
type ImageURLContent struct {
	URL    string `json:"url"`              // 图片url
	Detail string `json:"detail,omitempty"` // 详情
}

// String 返回图片url内容字符串
func (iuc ImageURLContent) String() string {
	return iuc.URL
}

// isPart 实现ContentPart接口，只是为了实现ContentPart接口，并没有实际意义，让其CreateMessageParams.Parts可以包含图片url内容,其实是一种多态的体现
func (ImageURLContent) isPart() {}

// BinaryContent 二进制内容
// 定义了二进制内容的结构,例如,路径、MIME类型、内容等
type BinaryContent struct {
	Path     string // 路径
	MIMEType string // MIME类型(音频、视频等)
	Data     []byte // 内容
}

// String 返回二进制内容字符串
// 将二进制内容转换为base64编码字符串
func (bc BinaryContent) String(p catwalk.InferenceProvider) string {
	base64Encoded := base64.StdEncoding.EncodeToString(bc.Data)
	if p == catwalk.InferenceProviderOpenAI {
		return "data:" + bc.MIMEType + ";base64," + base64Encoded
	}
	return base64Encoded
}

// isPart 实现ContentPart接口，只是为了实现ContentPart接口，并没有实际意义，让其CreateMessageParams.Parts可以包含二进制内容,其实是一种多态的体现
func (BinaryContent) isPart() {}

// ToolCall 工具调用内容
// 定义了工具调用内容的结构,例如,ID、名称、输入、提供者执行、完成等
type ToolCall struct {
	ID               string `json:"id"`                // 工具调用ID
	Name             string `json:"name"`              // 工具名称
	Input            string `json:"input"`             // 工具输入
	ProviderExecuted bool   `json:"provider_executed"` // 提供者执行
	Finished         bool   `json:"finished"`          // 是否完成
}

// isPart 实现ContentPart接口，只是为了实现ContentPart接口，并没有实际意义，让其CreateMessageParams.Parts可以包含工具调用内容,其实是一种多态的体现
func (ToolCall) isPart() {}

// ToolResult 工具结果内容
// 定义了工具结果内容的结构,例如,工具调用ID、名称、内容、数据、MIME类型、元数据、是否错误等
type ToolResult struct {
	ToolCallID string `json:"tool_call_id"` // 工具调用ID
	Name       string `json:"name"`         // 工具名称
	Content    string `json:"content"`      // 工具内容
	Data       string `json:"data"`         // 工具数据
	MIMEType   string `json:"mime_type"`    // MIME类型
	Metadata   string `json:"metadata"`     // 元数据
	IsError    bool   `json:"is_error"`     // 是否错误
}

// isPart 实现ContentPart接口，只是为了实现ContentPart接口，并没有实际意义，让其CreateMessageParams.Parts可以包含工具结果内容,其实是一种多态的体现
func (ToolResult) isPart() {}

// Finish 完成内容
// 定义了消息完成的内容,例如,结束回合、超过最大令牌数、使用工具、取消、错误、权限不足、未知等
type Finish struct {
	Reason  FinishReason `json:"reason"`            // 完成原因,表示消息完成的原因,例如,结束回合、超过最大令牌数、使用工具、取消、错误、权限不足、未知等
	Time    int64        `json:"time"`              // 完成时间,表示消息完成的时间,例如,2026-03-26 10:00:00
	Message string       `json:"message,omitempty"` // 完成消息,表示消息完成的消息,例如,完成消息
	Details string       `json:"details,omitempty"` // 完成详情,表示消息完成详情,例如,完成详情
}

// 实现ContentPart接口，只是为了实现ContentPart接口，并没有实际意义，让其CreateMessageParams.Parts可以包含Finish内容,其实是一种多态的体现
func (Finish) isPart() {}

// Message 消息
// 定义了消息的结构,例如,ID、角色、会话ID、内容、模型、提供者、创建时间、更新时间、是否是摘要消息等
type Message struct {
	ID               string        // 消息ID
	Role             MessageRole   // 消息角色
	SessionID        string        // 会话ID
	Parts            []ContentPart // 消息内容,表示消息的内容,例如,文本、图片、音频、视频等
	Model            string        // 模型,表示消息的模型,例如,gpt-4o、gpt-4o-mini、claude-3-5-sonnet等
	Provider         string        // 提供者,表示消息的提供者,例如,openai、anthropic、google等
	CreatedAt        int64         // 创建时间
	UpdatedAt        int64         // 更新时间
	IsSummaryMessage bool          // 是否是摘要消息,表示消息是否是摘要消息,例如,是否是系统消息、是否是用户消息等
}

// Content 断言消息内容是否为文本内容,并返回文本内容
func (m *Message) Content() TextContent {
	for _, part := range m.Parts {
		if c, ok := part.(TextContent); ok {
			return c
		}
	}
	return TextContent{}
}

// ReasoningContent 断言消息内容是否为推理内容,并返回推理内容
func (m *Message) ReasoningContent() ReasoningContent {
	for _, part := range m.Parts {
		if c, ok := part.(ReasoningContent); ok {
			return c
		}
	}
	return ReasoningContent{}
}

// ImageURLContent 断言消息内容是否为图片url内容,并返回图片url内容列表
func (m *Message) ImageURLContent() []ImageURLContent {
	imageURLContents := make([]ImageURLContent, 0)
	for _, part := range m.Parts {
		if c, ok := part.(ImageURLContent); ok {
			imageURLContents = append(imageURLContents, c)
		}
	}
	return imageURLContents
}

// BinaryContent 断言消息内容是否为二进制内容,并返回二进制内容列表
func (m *Message) BinaryContent() []BinaryContent {
	binaryContents := make([]BinaryContent, 0)
	for _, part := range m.Parts {
		if c, ok := part.(BinaryContent); ok {
			binaryContents = append(binaryContents, c)
		}
	}
	return binaryContents
}

// ToolCalls 断言消息内容是否为工具调用内容,并返回工具调用内容列表
func (m *Message) ToolCalls() []ToolCall {
	toolCalls := make([]ToolCall, 0)
	for _, part := range m.Parts {
		if c, ok := part.(ToolCall); ok {
			toolCalls = append(toolCalls, c)
		}
	}
	return toolCalls
}

// ToolResults 断言消息内容是否为工具结果内容,并返回工具结果内容列表
func (m *Message) ToolResults() []ToolResult {
	toolResults := make([]ToolResult, 0)
	for _, part := range m.Parts {
		if c, ok := part.(ToolResult); ok {
			toolResults = append(toolResults, c)
		}
	}
	return toolResults
}

// IsFinished 断言消息内容是否为完成内容,并返回是否完成
func (m *Message) IsFinished() bool {
	// 遍历消息内容,如果消息内容为完成内容,则返回true
	for _, part := range m.Parts {
		if _, ok := part.(Finish); ok {
			return true
		}
	}
	return false
}

// FinishPart 使用类型断言，提取是否为“完成内容”类型，并返回完成内容
func (m *Message) FinishPart() *Finish {
	for _, part := range m.Parts {
		if c, ok := part.(Finish); ok {
			return &c
		}
	}
	return nil
}

// FinishReason 断言消息内容是否为完成内容,并返回完成原因
func (m *Message) FinishReason() FinishReason {
	for _, part := range m.Parts {
		if c, ok := part.(Finish); ok {
			return c.Reason
		}
	}
	return ""
}

// IsThinking 断言消息内容是否为思考内容,并返回是否思考
func (m *Message) IsThinking() bool {
	if m.ReasoningContent().Thinking != "" && m.Content().Text == "" && !m.IsFinished() {
		return true
	}
	return false
}

// AppendContent 追加文本内容
// 追加文本内容到消息内容中
func (m *Message) AppendContent(delta string) {
	found := false
	// 遍历消息内容,如果消息内容为文本内容,则追加文本内容
	for i, part := range m.Parts {
		if c, ok := part.(TextContent); ok {
			m.Parts[i] = TextContent{Text: c.Text + delta}
			found = true
		}
	}
	// 如果消息内容中没有文本内容,则追加文本内容
	if !found {
		m.Parts = append(m.Parts, TextContent{Text: delta})
	}
}

// AppendReasoningContent 追加推理内容
// 追加推理内容到消息内容中
func (m *Message) AppendReasoningContent(delta string) {
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
		m.Parts = append(m.Parts, ReasoningContent{
			Thinking:  delta,
			StartedAt: time.Now().Unix(),
		})
	}
}

// AppendThoughtSignature 追加思考签名
func (m *Message) AppendThoughtSignature(signature string, toolCallID string) {
	for i, part := range m.Parts {
		if c, ok := part.(ReasoningContent); ok {
			m.Parts[i] = ReasoningContent{
				Thinking:         c.Thinking,
				ThoughtSignature: c.ThoughtSignature + signature,
				ToolID:           toolCallID,
				Signature:        c.Signature,
				StartedAt:        c.StartedAt,
				FinishedAt:       c.FinishedAt,
			}
			return
		}
	}
	m.Parts = append(m.Parts, ReasoningContent{ThoughtSignature: signature})
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

// SetReasoningResponsesData 设置推理响应数据
// 设置推理响应数据到消息内容中
func (m *Message) SetReasoningResponsesData(data *openai.ResponsesReasoningMetadata) {
	for i, part := range m.Parts {
		if c, ok := part.(ReasoningContent); ok {
			m.Parts[i] = ReasoningContent{
				Thinking:      c.Thinking,
				ResponsesData: data,
				StartedAt:     c.StartedAt,
				FinishedAt:    c.FinishedAt,
			}
			return
		}
	}
}

// FinishThinking 完成思考
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

// ThinkingDuration 计算思考持续时间
func (m *Message) ThinkingDuration() time.Duration {
	// 获取推理内容
	reasoning := m.ReasoningContent()
	if reasoning.StartedAt == 0 {
		return 0
	}

	// 获取完成时间
	endTime := reasoning.FinishedAt
	// 如果完成时间为0,则设置为当前时间
	if endTime == 0 {
		endTime = time.Now().Unix()
	}

	// 计算思考持续时间
	return time.Duration(endTime-reasoning.StartedAt) * time.Second
}

// FinishToolCall 完成工具调用
func (m *Message) FinishToolCall(toolCallID string) {
	for i, part := range m.Parts {
		if c, ok := part.(ToolCall); ok {
			if c.ID == toolCallID {
				m.Parts[i] = ToolCall{
					ID:       c.ID,
					Name:     c.Name,
					Input:    c.Input,
					Finished: true,
				}
				return
			}
		}
	}
}

// AppendToolCallInput 追加工具调用输入
func (m *Message) AppendToolCallInput(toolCallID string, inputDelta string) {
	for i, part := range m.Parts {
		if c, ok := part.(ToolCall); ok {
			if c.ID == toolCallID {
				m.Parts[i] = ToolCall{
					ID:       c.ID,
					Name:     c.Name,
					Input:    c.Input + inputDelta,
					Finished: c.Finished,
				}
				return
			}
		}
	}
}

// AddToolCall 添加工具调用
func (m *Message) AddToolCall(tc ToolCall) {
	for i, part := range m.Parts {
		if c, ok := part.(ToolCall); ok {
			// 如果工具调用ID相同,则替换工具调用
			if c.ID == tc.ID {
				m.Parts[i] = tc
				return
			}
		}
	}
	// 如果工具调用ID不同,则添加工具调用
	m.Parts = append(m.Parts, tc)
}

// SetToolCalls 设置工具调用
func (m *Message) SetToolCalls(tc []ToolCall) {
	// 移除任何存在的工具调用内容,因为可能有多条工具调用内容
	parts := make([]ContentPart, 0)
	for _, part := range m.Parts {
		// 如果消息内容为工具调用内容,则跳过,其实就是过滤掉原来存在的工具调用内容
		if _, ok := part.(ToolCall); ok {
			continue
		}

		// 保留其他类型内容
		parts = append(parts, part)
	}
	m.Parts = parts
	for _, toolCall := range tc {
		// 添加工具调用内容
		m.Parts = append(m.Parts, toolCall)
	}
}

// AddToolResult 添加工具结果
func (m *Message) AddToolResult(tr ToolResult) {
	// 添加工具结果内容
	m.Parts = append(m.Parts, tr)
}

// SetToolResults 设置工具结果
func (m *Message) SetToolResults(tr []ToolResult) {
	for _, toolResult := range tr {
		m.Parts = append(m.Parts, toolResult)
	}
}

// Clone 返回一个消息的深拷贝,防止并发修改消息内容时出现竞态条件
func (m *Message) Clone() Message {
	clone := *m
	clone.Parts = make([]ContentPart, len(m.Parts))
	copy(clone.Parts, m.Parts)
	return clone
}

// AddFinish 添加完成内容
func (m *Message) AddFinish(reason FinishReason, message, details string) {
	// 移除任何存在的完成内容,因为可能有多条完成内容
	for i, part := range m.Parts {
		if _, ok := part.(Finish); ok {
			// 删除完成内容
			m.Parts = slices.Delete(m.Parts, i, i+1)
			break
		}
	}
	// 添加完成内容
	m.Parts = append(m.Parts, Finish{Reason: reason, Time: time.Now().Unix(), Message: message, Details: details})
}

// AddImageURL 添加图片url内容
func (m *Message) AddImageURL(url, detail string) {
	m.Parts = append(m.Parts, ImageURLContent{URL: url, Detail: detail})
}

// AddBinary 添加二进制内容
func (m *Message) AddBinary(mimeType string, data []byte) {
	m.Parts = append(m.Parts, BinaryContent{MIMEType: mimeType, Data: data})
}

// PromptWithTextAttachments 添加文本附件
func PromptWithTextAttachments(prompt string, attachments []Attachment) string {
	var sb strings.Builder
	sb.WriteString(prompt)
	addedAttachments := false
	for _, content := range attachments {
		// 如果附件不是文本类型,则跳过
		if !content.IsText() {
			continue
		}

		// 如果还没有添加附件,则添加附件系统信息
		if !addedAttachments {
			// The files below have been attached by the user, consider them in your response
			// 附件系统信息,表示附件是由用户添加的,考虑在响应中使用它们
			sb.WriteString("\n<system_info>The files below have been attached by the user, consider them in your response</system_info>\n")
			addedAttachments = true
		}

		// 添加附件文件路径,如果文件路径不为空,则添加文件路径,否则添加文件名
		if content.FilePath != "" {
			// 添加附件文件路径,如果文件路径不为空,则添加文件路径,否则添加文件名
			fmt.Fprintf(&sb, "<file path='%s'>\n", content.FilePath)
		} else {
			// 添加附件文件名,如果文件路径为空,则添加文件名
			sb.WriteString("<file>\n")
		}
		// 添加附件内容
		sb.WriteString("\n")
		// 添加附件内容
		sb.Write(content.Content)
		sb.WriteString("\n</file>\n")
	}
	// 返回文本附件字符串
	return sb.String()
}

// ToAIMessage 将消息转换为AI消息
// 将消息转换为AI消息
func (m *Message) ToAIMessage() []fantasy.Message {
	var messages []fantasy.Message // AI消息列表
	switch m.Role {
	case User: // 用户消息： 其实用户消息无非就是：文本内容和附件内容
		var parts []fantasy.MessagePart

		// 获取用户的文本内容
		text := strings.TrimSpace(m.Content().Text)
		var textAttachments []Attachment
		// 获取用户的附件内容
		for _, content := range m.BinaryContent() {
			// 如果附件不是文本附件，则跳过
			if !strings.HasPrefix(content.MIMEType, "text/") {
				continue
			}
			// 添加文本附件
			textAttachments = append(textAttachments, Attachment{
				FilePath: content.Path,
				MimeType: content.MIMEType,
				Content:  content.Data,
			})
		}

		// 添加文本附件
		text = PromptWithTextAttachments(text, textAttachments)
		if text != "" {
			// 添加到parts中
			parts = append(parts, fantasy.TextPart{Text: text})
		}

		// 获取用户的附件内容
		for _, content := range m.BinaryContent() {
			// 如果附件是文本附件，则跳过
			if strings.HasPrefix(content.MIMEType, "text/") {
				continue
			}
			// 添加附件
			parts = append(parts, fantasy.FilePart{
				Filename:  content.Path,
				Data:      content.Data,
				MediaType: content.MIMEType,
			})
		}
		// 添加用户消息
		messages = append(messages, fantasy.Message{
			Role:    fantasy.MessageRoleUser,
			Content: parts,
		})
	case Assistant: // 助手消息：其实助手消息无非就是：文本内容和推理内容
		var parts []fantasy.MessagePart
		// 获取文本内容
		text := strings.TrimSpace(m.Content().Text)
		if text != "" {
			// 添加文本内容
			parts = append(parts, fantasy.TextPart{Text: text})
		}

		// 获取推理内容
		reasoning := m.ReasoningContent()
		// 如果推理内容不为空，则添加推理内容
		if reasoning.Thinking != "" {
			// 添加推理内容
			reasoningPart := fantasy.ReasoningPart{Text: reasoning.Thinking, ProviderOptions: fantasy.ProviderOptions{}}
			// 如果推理签名不为空，则添加推理签名
			if reasoning.Signature != "" {
				// 添加推理签名
				reasoningPart.ProviderOptions[anthropic.Name] = &anthropic.ReasoningOptionMetadata{
					Signature: reasoning.Signature,
				}
			}

			// 如果推理响应数据不为空，则添加推理响应数据
			if reasoning.ResponsesData != nil {
				// 添加推理响应数据
				reasoningPart.ProviderOptions[openai.Name] = reasoning.ResponsesData
			}

			// 如果思考签名不为空，则添加思考签名
			if reasoning.ThoughtSignature != "" {
				// 添加思考签名
				reasoningPart.ProviderOptions[google.Name] = &google.ReasoningMetadata{
					Signature: reasoning.ThoughtSignature,
					ToolID:    reasoning.ToolID,
				}
			}
			parts = append(parts, reasoningPart)
		}

		// 获取工具调用内容,则添加工具调用内容
		for _, call := range m.ToolCalls() {
			// 添加工具调用内容
			parts = append(parts, fantasy.ToolCallPart{
				ToolCallID:       call.ID,
				ToolName:         call.Name,
				Input:            call.Input,
				ProviderExecuted: call.ProviderExecuted,
			})
		}
		// 添加助手消息
		messages = append(messages, fantasy.Message{
			Role:    fantasy.MessageRoleAssistant,
			Content: parts,
		})
	case Tool: // 工具消息：其实工具消息无非就是：工具调用内容和工具结果内容
		var parts []fantasy.MessagePart
		// 获取工具结果内容
		for _, result := range m.ToolResults() {
			var content fantasy.ToolResultOutputContent // 工具结果内容
			// 如果工具结果内容为错误，则添加错误内容
			if result.IsError {
				// 添加错误内容
				content = fantasy.ToolResultOutputContentError{
					Error: errors.New(result.Content),
				}
			} else if result.Data != "" {
				// 添加工具结果数据，媒体类型
				content = fantasy.ToolResultOutputContentMedia{
					Data:      result.Data,
					MediaType: result.MIMEType,
				}
			} else {
				// 添加工具结果文本内容
				content = fantasy.ToolResultOutputContentText{
					Text: result.Content,
				}
			}
			// 添加工具结果内容
			parts = append(parts, fantasy.ToolResultPart{
				ToolCallID: result.ToolCallID,
				Output:     content,
			})
		}
		// 添加工具消息
		messages = append(messages, fantasy.Message{
			Role:    fantasy.MessageRoleTool,
			Content: parts,
		})
	}
	return messages
}
