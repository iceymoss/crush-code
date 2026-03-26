package message

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/charmbracelet/crush/internal/db"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/google/uuid"
)

// CreateMessageParams 创建消息参数
type CreateMessageParams struct {
	Role             MessageRole   // 消息角色,表示消息的类型,例如,用户消息、助手消息、系统消息、工具消息等
	Parts            []ContentPart // 消息内容,表示消息的文本、图片、音频、视频等内容，具体由实现者决定
	Model            string        // 模型,表示消息的模型,例如,gpt-4o、gpt-4o-mini、claude-3-5-sonnet等
	Provider         string        // 提供者,表示消息的提供者,例如,openai、anthropic、google等
	IsSummaryMessage bool          // 是否是摘要消息,表示消息是否是摘要消息,例如,是否是系统消息、是否是用户消息等
}

// Service 定义了消息功能
type Service interface {
	pubsub.Subscriber[Message]                                                                 // 有用订阅能力
	Create(ctx context.Context, sessionID string, params CreateMessageParams) (Message, error) // 创建消息
	Update(ctx context.Context, message Message) error                                         // 更新消息
	Get(ctx context.Context, id string) (Message, error)                                       // 获取消息
	List(ctx context.Context, sessionID string) ([]Message, error)                             // 获取会话消息列表
	ListUserMessages(ctx context.Context, sessionID string) ([]Message, error)                 // 获取用户消息列表
	ListAllUserMessages(ctx context.Context) ([]Message, error)                                // 获取所有用户消息列表
	Delete(ctx context.Context, id string) error                                               // 删除消息
	DeleteSessionMessages(ctx context.Context, sessionID string) error                         // 删除会话消息
}

// service 定义了消息服务的实现
type service struct {
	*pubsub.Broker[Message]            // 实现了pubsub.Subscriber[Message]接口，也实现了pubsub.Publisher[Message]接口，让消息服务可以订阅和发布消息事件
	q                       db.Querier // 数据库查询器，用于执行数据库操作
}

// NewService 创建消息服务
func NewService(q db.Querier) Service {
	return &service{
		Broker: pubsub.NewBroker[Message](),
		q:      q,
	}
}

// Delete 删除消息
func (s *service) Delete(ctx context.Context, id string) error {
	message, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	err = s.q.DeleteMessage(ctx, message.ID)
	if err != nil {
		return err
	}
	// 克隆消息，避免并发修改Parts切片
	// 发布一个“删除消息”事件
	s.Publish(pubsub.DeletedEvent, message.Clone())
	return nil
}

// Create 创建消息
func (s *service) Create(ctx context.Context, sessionID string, params CreateMessageParams) (Message, error) {
	// 如果消息角色不是Assistant，则添加一个完成内容
	// Assistant: 助手角色，用于回复用户消息
	if params.Role != Assistant {
		// 添加一个完成内容，原因：stop
		params.Parts = append(params.Parts, Finish{
			Reason: "stop",
		})
	}

	// 将消息内容序列化为JSON字节数组
	partsJSON, err := marshalParts(params.Parts)
	if err != nil {
		return Message{}, err
	}
	// 是否是摘要消息
	isSummary := int64(0)
	if params.IsSummaryMessage {
		isSummary = 1
	}

	// 写入数据库
	dbMessage, err := s.q.CreateMessage(ctx, db.CreateMessageParams{
		ID:               uuid.New().String(),
		SessionID:        sessionID,
		Role:             string(params.Role),
		Parts:            string(partsJSON),
		Model:            sql.NullString{String: string(params.Model), Valid: true},
		Provider:         sql.NullString{String: params.Provider, Valid: params.Provider != ""},
		IsSummaryMessage: isSummary,
	})
	if err != nil {
		return Message{}, err
	}
	// 将数据库消息数据结构转为消息对象
	message, err := s.fromDBItem(dbMessage)
	if err != nil {
		return Message{}, err
	}

	// 发布一个“创建消息”事件
	// 克隆消息，避免并发修改Parts切片
	s.Publish(pubsub.CreatedEvent, message.Clone())
	return message, nil
}

// DeleteSessionMessages 删除会话消息
func (s *service) DeleteSessionMessages(ctx context.Context, sessionID string) error {
	// 获取会话消息列表
	messages, err := s.List(ctx, sessionID)
	if err != nil {
		return err
	}
	// 遍历会话消息列表，删除消息
	for _, message := range messages {
		if message.SessionID == sessionID {
			// 删除消息
			err = s.Delete(ctx, message.ID)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// Update 更新消息
func (s *service) Update(ctx context.Context, message Message) error {
	// 将消息内容序列化为JSON字节数组
	parts, err := marshalParts(message.Parts)
	if err != nil {
		return err
	}

	// 完成时间
	finishedAt := sql.NullInt64{}
	// 使用类型断言，提取是否为“完成内容”类型，并返回完成内容
	if f := message.FinishPart(); f != nil {
		// 完成时间
		finishedAt.Int64 = f.Time
		finishedAt.Valid = true
	}
	// 更新消息
	err = s.q.UpdateMessage(ctx, db.UpdateMessageParams{
		ID:         message.ID,
		Parts:      string(parts),
		FinishedAt: finishedAt,
	})
	if err != nil {
		return err
	}
	message.UpdatedAt = time.Now().Unix()
	// 发布一个“更新消息”事件
	// 克隆消息，避免并发修改Parts切片
	// concurrent modifications to the Parts slice.
	s.Publish(pubsub.UpdatedEvent, message.Clone())
	return nil
}

// Get 获取消息
func (s *service) Get(ctx context.Context, id string) (Message, error) {
	dbMessage, err := s.q.GetMessage(ctx, id)
	if err != nil {
		return Message{}, err
	}
	return s.fromDBItem(dbMessage)
}

// List 获取会话消息列表
func (s *service) List(ctx context.Context, sessionID string) ([]Message, error) {
	dbMessages, err := s.q.ListMessagesBySession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	messages := make([]Message, len(dbMessages))
	for i, dbMessage := range dbMessages {
		messages[i], err = s.fromDBItem(dbMessage)
		if err != nil {
			return nil, err
		}
	}
	return messages, nil
}

// ListUserMessages 获取当前会话下用户消息列表
func (s *service) ListUserMessages(ctx context.Context, sessionID string) ([]Message, error) {
	// 获取当前会话下用户消息列表
	dbMessages, err := s.q.ListUserMessagesBySession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	messages := make([]Message, len(dbMessages))
	for i, dbMessage := range dbMessages {
		messages[i], err = s.fromDBItem(dbMessage)
		if err != nil {
			return nil, err
		}
	}
	return messages, nil
}

// ListAllUserMessages 获取所有用户消息列表
func (s *service) ListAllUserMessages(ctx context.Context) ([]Message, error) {
	// 获取所有用户消息列表
	dbMessages, err := s.q.ListAllUserMessages(ctx)
	if err != nil {
		return nil, err
	}
	messages := make([]Message, len(dbMessages))
	for i, dbMessage := range dbMessages {
		messages[i], err = s.fromDBItem(dbMessage)
		if err != nil {
			return nil, err
		}
	}
	return messages, nil
}

// fromDBItem 将db消息数据结构转为消息对象
func (s *service) fromDBItem(item db.Message) (Message, error) {
	parts, err := unmarshalParts([]byte(item.Parts))
	if err != nil {
		return Message{}, err
	}
	return Message{
		ID:               item.ID,
		SessionID:        item.SessionID,
		Role:             MessageRole(item.Role),
		Parts:            parts,
		Model:            item.Model.String,
		Provider:         item.Provider.String,
		CreatedAt:        item.CreatedAt,
		UpdatedAt:        item.UpdatedAt,
		IsSummaryMessage: item.IsSummaryMessage != 0,
	}, nil
}

// partType 消息内容类型
type partType string

const (
	// ReasoningType 推理内容类型
	reasoningType partType = "reasoning"
	// TextType 文本内容类型
	textType partType = "text"
	// ImageURLType 图片url内容类型
	imageURLType partType = "image_url"
	// BinaryType 二进制内容类型
	binaryType partType = "binary"
	// ToolCallType 工具调用内容类型
	toolCallType partType = "tool_call"
	// ToolResultType 工具结果内容类型
	toolResultType partType = "tool_result"
	// FinishType 完成内容类型
	finishType partType = "finish"
)

// partWrapper 消息内容包装器
// 用于包装消息内容，便于序列化和反序列化
type partWrapper struct {
	Type partType    `json:"type"` // 消息内容类型
	Data ContentPart `json:"data"` // 消息内容
}

// marshalParts 序列化消息内容
// 将消息内容序列化为JSON字节数组
func marshalParts(parts []ContentPart) ([]byte, error) {
	wrappedParts := make([]partWrapper, len(parts))

	for i, part := range parts {
		var typ partType

		switch part.(type) {
		case ReasoningContent: // 推理内容
			typ = reasoningType
		case TextContent: // 文本内容
			typ = textType
		case ImageURLContent: // 图片url内容
			typ = imageURLType
		case BinaryContent:
			typ = binaryType
		case ToolCall: // 工具调用内容
			typ = toolCallType
		case ToolResult: // 工具结果内容
			typ = toolResultType
		case Finish: // 完成内容
			typ = finishType
		default: // 未知内容
			return nil, fmt.Errorf("unknown part type: %T", part)
		}

		// 将消息内容包装成partWrapper结构体
		wrappedParts[i] = partWrapper{
			Type: typ,
			Data: part,
		}
	}
	return json.Marshal(wrappedParts)
}

// unmarshalParts 反序列化消息内容
func unmarshalParts(data []byte) ([]ContentPart, error) {
	temp := []json.RawMessage{}

	if err := json.Unmarshal(data, &temp); err != nil {
		return nil, err
	}

	parts := make([]ContentPart, 0)

	for _, rawPart := range temp {
		// 定义一个结构体，用于存储消息内容
		var wrapper struct {
			Type partType        `json:"type"`
			Data json.RawMessage `json:"data"`
		}

		// 反序列化消息内容
		if err := json.Unmarshal(rawPart, &wrapper); err != nil {
			return nil, err
		}

		switch wrapper.Type {
		case reasoningType: // 推理内容
			part := ReasoningContent{}
			if err := json.Unmarshal(wrapper.Data, &part); err != nil {
				return nil, err
			}
			parts = append(parts, part)
		case textType: // 文本内容
			part := TextContent{}
			if err := json.Unmarshal(wrapper.Data, &part); err != nil {
				return nil, err
			}
			parts = append(parts, part)
		case imageURLType: // 图片url内容
			part := ImageURLContent{}
			if err := json.Unmarshal(wrapper.Data, &part); err != nil {
				return nil, err
			}
			parts = append(parts, part)
		case binaryType: // 二进制内容
			part := BinaryContent{}
			if err := json.Unmarshal(wrapper.Data, &part); err != nil {
				return nil, err
			}
			parts = append(parts, part)
		case toolCallType: // 工具调用内容
			part := ToolCall{}
			if err := json.Unmarshal(wrapper.Data, &part); err != nil {
				return nil, err
			}
			parts = append(parts, part)
		case toolResultType: // 工具结果内容
			part := ToolResult{}
			if err := json.Unmarshal(wrapper.Data, &part); err != nil {
				return nil, err
			}
			parts = append(parts, part)
		case finishType: // 完成内容
			part := Finish{}
			if err := json.Unmarshal(wrapper.Data, &part); err != nil {
				return nil, err
			}
			parts = append(parts, part)
		default: // 未知内容
			return nil, fmt.Errorf("unknown part type: %s", wrapper.Type)
		}
	}

	// 返回消息内容
	return parts, nil
}
