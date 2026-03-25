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
	session := s.fromDBItem(dbSession)
	s.Publish(pubsub.CreatedEvent, session)
	event.SessionCreated()
	return session, nil
}

func (s *service) CreateTaskSession(ctx context.Context, toolCallID, parentSessionID, title string) (Session, error) {
	dbSession, err := s.q.CreateSession(ctx, db.CreateSessionParams{
		ID:              toolCallID,
		ParentSessionID: sql.NullString{String: parentSessionID, Valid: true},
		Title:           title,
	})
	if err != nil {
		return Session{}, err
	}
	session := s.fromDBItem(dbSession)
	s.Publish(pubsub.CreatedEvent, session)
	return session, nil
}

func (s *service) CreateTitleSession(ctx context.Context, parentSessionID string) (Session, error) {
	dbSession, err := s.q.CreateSession(ctx, db.CreateSessionParams{
		ID:              "title-" + parentSessionID,
		ParentSessionID: sql.NullString{String: parentSessionID, Valid: true},
		Title:           "Generate a title",
	})
	if err != nil {
		return Session{}, err
	}
	session := s.fromDBItem(dbSession)
	s.Publish(pubsub.CreatedEvent, session)
	return session, nil
}

func (s *service) Delete(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	qtx := s.q.WithTx(tx)

	dbSession, err := qtx.GetSessionByID(ctx, id)
	if err != nil {
		return err
	}
	if err = qtx.DeleteSessionMessages(ctx, dbSession.ID); err != nil {
		return fmt.Errorf("deleting session messages: %w", err)
	}
	if err = qtx.DeleteSessionFiles(ctx, dbSession.ID); err != nil {
		return fmt.Errorf("deleting session files: %w", err)
	}
	if err = qtx.DeleteSession(ctx, dbSession.ID); err != nil {
		return fmt.Errorf("deleting session: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("committing transaction: %w", err)
	}

	session := s.fromDBItem(dbSession)
	s.Publish(pubsub.DeletedEvent, session)
	event.SessionDeleted()
	return nil
}

func (s *service) Get(ctx context.Context, id string) (Session, error) {
	dbSession, err := s.q.GetSessionByID(ctx, id)
	if err != nil {
		return Session{}, err
	}
	return s.fromDBItem(dbSession), nil
}

func (s *service) GetLast(ctx context.Context) (Session, error) {
	dbSession, err := s.q.GetLastSession(ctx)
	if err != nil {
		return Session{}, err
	}
	return s.fromDBItem(dbSession), nil
}

func (s *service) Save(ctx context.Context, session Session) (Session, error) {
	todosJSON, err := marshalTodos(session.Todos)
	if err != nil {
		return Session{}, err
	}

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

// UpdateTitleAndUsage updates only the title and usage fields atomically.
// This is safer than fetching, modifying, and saving the entire session.
func (s *service) UpdateTitleAndUsage(ctx context.Context, sessionID, title string, promptTokens, completionTokens int64, cost float64) error {
	return s.q.UpdateSessionTitleAndUsage(ctx, db.UpdateSessionTitleAndUsageParams{
		ID:               sessionID,
		Title:            title,
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		Cost:             cost,
	})
}

// Rename updates only the title of a session without touching updated_at or
// usage fields.
func (s *service) Rename(ctx context.Context, id string, title string) error {
	return s.q.RenameSession(ctx, db.RenameSessionParams{
		ID:    id,
		Title: title,
	})
}

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

func NewService(q *db.Queries, conn *sql.DB) Service {
	broker := pubsub.NewBroker[Session]()
	return &service{
		Broker: broker,
		db:     conn,
		q:      q,
	}
}

// CreateAgentToolSessionID creates a session ID for agent tool sessions using the format "messageID$$toolCallID"
func (s *service) CreateAgentToolSessionID(messageID, toolCallID string) string {
	return fmt.Sprintf("%s$$%s", messageID, toolCallID)
}

// ParseAgentToolSessionID parses an agent tool session ID into its components
func (s *service) ParseAgentToolSessionID(sessionID string) (messageID string, toolCallID string, ok bool) {
	parts := strings.Split(sessionID, "$$")
	if len(parts) != 2 {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// IsAgentToolSession checks if a session ID follows the agent tool session format
func (s *service) IsAgentToolSession(sessionID string) bool {
	_, _, ok := s.ParseAgentToolSessionID(sessionID)
	return ok
}
