package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/charmbracelet/crush/internal/db"
	"github.com/charmbracelet/crush/internal/event"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/google/uuid"
	"github.com/zeebo/xxh3"
)

// TodoStatus 定义了任务的状态
type TodoStatus string

const (
	// TodoStatusPending 定义了任务的待处理状态
	TodoStatusPending TodoStatus = "pending"
	// TodoStatusInProgress 定义了任务的进行中状态
	TodoStatusInProgress TodoStatus = "in_progress"
	// TodoStatusCompleted 定义了任务的完成状态
	TodoStatusCompleted TodoStatus = "completed"
)

// HashID 返回会话ID的XXH3哈希值，作为十六进制字符串
func HashID(id string) string {
	h := xxh3.New()
	h.WriteString(id)
	return fmt.Sprintf("%x", h.Sum(nil))
}

// Todo 定义了任务的结构
type Todo struct {
	Content    string     `json:"content"`     // 任务内容
	Status     TodoStatus `json:"status"`      // 任务状态
	ActiveForm string     `json:"active_form"` // 任务的活跃表单
}

// HasIncompleteTodos 是否存在未完成的任务
func HasIncompleteTodos(todos []Todo) bool {
	// 遍历任务列表，如果存在未完成的任务，则返回true
	for _, todo := range todos {
		// 如果任务状态不为完成，则返回true
		if todo.Status != TodoStatusCompleted {
			return true
		}
	}
	return false
}

// Session 定义了会话的结构
type Session struct {
	ID               string  // 会话id
	ParentSessionID  string  // 父会话id
	Title            string  // 会话标题
	MessageCount     int64   // 消息数量
	PromptTokens     int64   // 提示token数量
	CompletionTokens int64   // 完成token数量
	SummaryMessageID string  // 摘要消息id
	Cost             float64 // 会话费用
	Todos            []Todo  // 待办事项列表
	CreatedAt        int64   // 创建时间戳
	UpdatedAt        int64   // 更新时间戳
}

// Service 定义了会话服务的接口
type Service interface {
	pubsub.Subscriber[Session]                                                                                                  // 对于Session类型的订阅者，实现了pubsub.Subscriber[Session]接口
	Create(ctx context.Context, title string) (Session, error)                                                                  // 创建会话
	CreateTitleSession(ctx context.Context, parentSessionID string) (Session, error)                                            // 创建标题会话
	CreateTaskSession(ctx context.Context, toolCallID, parentSessionID, title string) (Session, error)                          // 创建任务会话
	Get(ctx context.Context, id string) (Session, error)                                                                        // 获取会话
	GetLast(ctx context.Context) (Session, error)                                                                               // 获取最后一个会话
	List(ctx context.Context) ([]Session, error)                                                                                // 获取会话列表
	Save(ctx context.Context, session Session) (Session, error)                                                                 // 保存会话
	UpdateTitleAndUsage(ctx context.Context, sessionID, title string, promptTokens, completionTokens int64, cost float64) error // 更新标题和使用情况
	Rename(ctx context.Context, id string, title string) error                                                                  // 重命名会话
	Delete(ctx context.Context, id string) error                                                                                // 删除会话

	// agent工具会话管理
	CreateAgentToolSessionID(messageID, toolCallID string) string                            // 创建agent工具会话id
	ParseAgentToolSessionID(sessionID string) (messageID string, toolCallID string, ok bool) // 解析agent工具会话id
	IsAgentToolSession(sessionID string) bool                                                // 是否是agent工具会话
}

// service 定义了会话服务的实现
type service struct {
	*pubsub.Broker[Session]
	db *sql.DB     // 数据库连接
	q  *db.Queries // 数据库查询器，用于执行数据库操作
}

// Create 创建会话
func (s *service) Create(ctx context.Context, title string) (Session, error) {
	dbSession, err := s.q.CreateSession(ctx, db.CreateSessionParams{
		ID:    uuid.New().String(),
		Title: title,
	})
	if err != nil {
		return Session{}, err
	}

	// 将数据库会话数据结构转为会话对象
	session := s.fromDBItem(dbSession)

	// 发布一个“创建会话”事件
	s.Publish(pubsub.CreatedEvent, session)

	// 用于开发者对用户使用的埋点
	event.SessionCreated()
	return session, nil
}

// CreateTaskSession 创建任务会话
func (s *service) CreateTaskSession(ctx context.Context, toolCallID, parentSessionID, title string) (Session, error) {
	dbSession, err := s.q.CreateSession(ctx, db.CreateSessionParams{
		ID:              toolCallID,                                           // 任务会话id,使用工具调用id
		ParentSessionID: sql.NullString{String: parentSessionID, Valid: true}, // 父会话id
		Title:           title,                                                // 任务会话标题
	})
	if err != nil {
		return Session{}, err
	}
	session := s.fromDBItem(dbSession)
	s.Publish(pubsub.CreatedEvent, session)
	return session, nil
}

// CreateTitleSession 创建标题会话
func (s *service) CreateTitleSession(ctx context.Context, parentSessionID string) (Session, error) {
	dbSession, err := s.q.CreateSession(ctx, db.CreateSessionParams{
		ID:              "title-" + parentSessionID,                           // 标题会话id,使用父会话id
		ParentSessionID: sql.NullString{String: parentSessionID, Valid: true}, // 父会话id
		Title:           "Generate a title",                                   // 标题会话标题
	})
	if err != nil {
		return Session{}, err
	}
	session := s.fromDBItem(dbSession)
	s.Publish(pubsub.CreatedEvent, session)
	return session, nil
}

// Delete 删除会话
func (s *service) Delete(ctx context.Context, id string) error {
	// 开始事务
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	qtx := s.q.WithTx(tx)

	// 获取会话
	dbSession, err := qtx.GetSessionByID(ctx, id)
	if err != nil {
		return err
	}

	// 删除会话消息
	if err = qtx.DeleteSessionMessages(ctx, dbSession.ID); err != nil {
		return fmt.Errorf("deleting session messages: %w", err)
	}

	// 删除会话文件
	if err = qtx.DeleteSessionFiles(ctx, dbSession.ID); err != nil {
		return fmt.Errorf("deleting session files: %w", err)
	}

	// 删除会话
	if err = qtx.DeleteSession(ctx, dbSession.ID); err != nil {
		return fmt.Errorf("deleting session: %w", err)
	}

	// 提交事务
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("committing transaction: %w", err)
	}

	// 将数据库会话数据结构转为会话对象
	session := s.fromDBItem(dbSession)

	// 发布一个“删除会话”事件
	s.Publish(pubsub.DeletedEvent, session)
	event.SessionDeleted()
	return nil
}

// Get 获取会话
func (s *service) Get(ctx context.Context, id string) (Session, error) {
	dbSession, err := s.q.GetSessionByID(ctx, id)
	if err != nil {
		return Session{}, err
	}
	return s.fromDBItem(dbSession), nil
}

// GetLast 获取最后一个会话
func (s *service) GetLast(ctx context.Context) (Session, error) {
	dbSession, err := s.q.GetLastSession(ctx)
	if err != nil {
		return Session{}, err
	}
	return s.fromDBItem(dbSession), nil
}

// Save 保存会话
func (s *service) Save(ctx context.Context, session Session) (Session, error) {
	// 将任务列表序列化为JSON字符串
	todosJSON, err := marshalTodos(session.Todos)
	if err != nil {
		return Session{}, err
	}

	// 更新会话
	dbSession, err := s.q.UpdateSession(ctx, db.UpdateSessionParams{
		ID:               session.ID,
		Title:            session.Title,
		PromptTokens:     session.PromptTokens,
		CompletionTokens: session.CompletionTokens,
		SummaryMessageID: sql.NullString{
			String: session.SummaryMessageID,
			Valid:  session.SummaryMessageID != "",
		},
		Cost: session.Cost,
		Todos: sql.NullString{
			String: todosJSON,
			Valid:  todosJSON != "",
		},
	})
	if err != nil {
		return Session{}, err
	}
	session = s.fromDBItem(dbSession)
	s.Publish(pubsub.UpdatedEvent, session)
	return session, nil
}

// UpdateTitleAndUsage 更新标题和使用情况，只更新标题和使用情况，不更新其他字段
func (s *service) UpdateTitleAndUsage(ctx context.Context, sessionID, title string, promptTokens, completionTokens int64, cost float64) error {
	return s.q.UpdateSessionTitleAndUsage(ctx, db.UpdateSessionTitleAndUsageParams{
		ID:               sessionID,        // 会话id
		Title:            title,            // 会话标题
		PromptTokens:     promptTokens,     // 提示token数量
		CompletionTokens: completionTokens, // 完成token数量
		Cost:             cost,             // 会话费用
	})
}

// Rename 重命名会话，只更新标题，不更新其他字段
func (s *service) Rename(ctx context.Context, id string, title string) error {
	return s.q.RenameSession(ctx, db.RenameSessionParams{
		ID:    id,
		Title: title,
	})
}

// List 获取会话列表
func (s *service) List(ctx context.Context) ([]Session, error) {
	dbSessions, err := s.q.ListSessions(ctx)
	if err != nil {
		return nil, err
	}
	sessions := make([]Session, len(dbSessions))
	for i, dbSession := range dbSessions {
		sessions[i] = s.fromDBItem(dbSession)
	}
	return sessions, nil
}

// fromDBItem 将数据库会话数据结构转为会话对象
func (s service) fromDBItem(item db.Session) Session {
	todos, err := unmarshalTodos(item.Todos.String)
	if err != nil {
		slog.Error("Failed to unmarshal todos", "session_id", item.ID, "error", err)
	}
	return Session{
		ID:               item.ID,
		ParentSessionID:  item.ParentSessionID.String,
		Title:            item.Title,
		MessageCount:     item.MessageCount,
		PromptTokens:     item.PromptTokens,
		CompletionTokens: item.CompletionTokens,
		SummaryMessageID: item.SummaryMessageID.String,
		Cost:             item.Cost,
		Todos:            todos,
		CreatedAt:        item.CreatedAt,
		UpdatedAt:        item.UpdatedAt,
	}
}

// marshalTodos 将任务列表序列化为JSON字符串
func marshalTodos(todos []Todo) (string, error) {
	if len(todos) == 0 {
		return "", nil
	}
	data, err := json.Marshal(todos)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// unmarshalTodos 将JSON字符串反序列化为任务列表
func unmarshalTodos(data string) ([]Todo, error) {
	if data == "" {
		return []Todo{}, nil
	}
	var todos []Todo
	if err := json.Unmarshal([]byte(data), &todos); err != nil {
		return []Todo{}, err
	}
	return todos, nil
}

// NewService 创建会话服务
func NewService(q *db.Queries, conn *sql.DB) Service {
	broker := pubsub.NewBroker[Session]()
	return &service{
		Broker: broker,
		db:     conn,
		q:      q,
	}
}

// CreateAgentToolSessionID 创建agent工具会话id，使用格式 "messageID$$toolCallID"
func (s *service) CreateAgentToolSessionID(messageID, toolCallID string) string {
	return fmt.Sprintf("%s$$%s", messageID, toolCallID)
}

// ParseAgentToolSessionID 解析agent工具会话id，将其拆分为消息id和工具调用id
func (s *service) ParseAgentToolSessionID(sessionID string) (messageID string, toolCallID string, ok bool) {
	parts := strings.Split(sessionID, "$$")
	if len(parts) != 2 {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// IsAgentToolSession 检查会话id是否符合agent工具会话格式
func (s *service) IsAgentToolSession(sessionID string) bool {
	_, _, ok := s.ParseAgentToolSessionID(sessionID)
	return ok
}
