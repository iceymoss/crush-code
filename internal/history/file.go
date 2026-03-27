package history

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/charmbracelet/crush/internal/db"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/google/uuid"
)

const (
	// InitialVersion 初始版本
	InitialVersion = 0 // 初始版本为0
)

// File 定义了文件的结构
type File struct {
	ID        string // 文件id
	SessionID string // 会话id
	Path      string // 文件路径
	Content   string // 文件内容
	Version   int64  // 文件版本
	CreatedAt int64  // 创建时间戳
	UpdatedAt int64  // 更新时间戳
}

// Service 定义了文件服务接口，管理文件版本和历史记录
type Service interface {
	pubsub.Subscriber[File]                                                    // 实现了pubsub.Subscriber[File]接口，让文件服务可以订阅文件事件
	Create(ctx context.Context, sessionID, path, content string) (File, error) // 创建文件

	// CreateVersion creates a new version of a file.
	// CreateVersion 创建文件版本，创建一个新版本
	CreateVersion(ctx context.Context, sessionID, path, content string) (File, error) // 创建文件版本

	Get(ctx context.Context, id string) (File, error)                              // 获取文件
	GetByPathAndSession(ctx context.Context, path, sessionID string) (File, error) // 通过path和会话id获取文件
	ListBySession(ctx context.Context, sessionID string) ([]File, error)           // 通过会话id获取文件列表
	ListLatestSessionFiles(ctx context.Context, sessionID string) ([]File, error)  // 通过会话id获取最新文件列表
	Delete(ctx context.Context, id string) error                                   // 删除文件
	DeleteSessionFiles(ctx context.Context, sessionID string) error                // 删除会话文件
}

// service 定义了文件服务的实现
type service struct {
	*pubsub.Broker[File]             // 实现了pubsub.Broker[File]接口，让文件服务可以发布文件事件
	db                   *sql.DB     // 数据库连接
	q                    *db.Queries // 数据库查询器，用于执行数据库操作
}

func NewService(q *db.Queries, db *sql.DB) Service {
	return &service{
		Broker: pubsub.NewBroker[File](),
		q:      q,
		db:     db,
	}
}

// Create 创建文件
func (s *service) Create(ctx context.Context, sessionID, path, content string) (File, error) {
	return s.createWithVersion(ctx, sessionID, path, content, InitialVersion)
}

// CreateVersion creates a new version of a file with auto-incremented version
// number. If no previous versions exist for the path, it creates the initial
// version. The provided content is stored as the new version.
func (s *service) CreateVersion(ctx context.Context, sessionID, path, content string) (File, error) {
	// Get the latest version for this path
	files, err := s.q.ListFilesByPath(ctx, path)
	if err != nil {
		return File{}, err
	}

	if len(files) == 0 {
		// No previous versions, create initial
		return s.Create(ctx, sessionID, path, content)
	}

	// Get the latest version
	latestFile := files[0] // Files are ordered by version DESC, created_at DESC
	nextVersion := latestFile.Version + 1

	return s.createWithVersion(ctx, sessionID, path, content, nextVersion)
}

// createWithVersion 创建文件版本，创建一个新版本
func (s *service) createWithVersion(ctx context.Context, sessionID, path, content string, version int64) (File, error) {
	// Maximum number of retries for transaction conflicts
	// 最大重试次数为3次
	const maxRetries = 3
	var file File
	var err error

	// Retry loop for transaction conflicts
	// 重试循环，最多重试3次
	for attempt := range maxRetries {
		// Start a transaction
		// 开启事务
		tx, txErr := s.db.BeginTx(ctx, nil)
		if txErr != nil {
			return File{}, fmt.Errorf("failed to begin transaction: %w", txErr)
		}

		// Create a new queries instance with the transaction
		qtx := s.q.WithTx(tx)

		// 尝试在事务中创建文件记录
		// Try to create the file within the transaction
		dbFile, txErr := qtx.CreateFile(ctx, db.CreateFileParams{
			ID:        uuid.New().String(),
			SessionID: sessionID,
			Path:      path,
			Content:   content,
			Version:   version,
		})
		if txErr != nil {
			// Rollback the transaction
			tx.Rollback()

			// Check if this is a uniqueness constraint violation
			// 检查是否是唯一性约束违反
			if strings.Contains(txErr.Error(), "UNIQUE constraint failed") {
				if attempt < maxRetries-1 {
					// If we have retries left, increment version and try again
					version++
					continue
				}
			}
			return File{}, txErr
		}

		// Commit the transaction
		// 提交事务
		if txErr = tx.Commit(); txErr != nil {
			return File{}, fmt.Errorf("failed to commit transaction: %w", txErr)
		}

		// 将db文件数据结构转为文件对象
		file = s.fromDBItem(dbFile)

		// 发布一个“文件创建”事件
		s.Publish(pubsub.CreatedEvent, file)
		return file, nil
	}

	return file, err
}

// Get 获取文件
func (s *service) Get(ctx context.Context, id string) (File, error) {
	dbFile, err := s.q.GetFile(ctx, id)
	if err != nil {
		return File{}, err
	}
	return s.fromDBItem(dbFile), nil
}

// GetByPathAndSession 通过path和会话id获取文件
func (s *service) GetByPathAndSession(ctx context.Context, path, sessionID string) (File, error) {
	dbFile, err := s.q.GetFileByPathAndSession(ctx, db.GetFileByPathAndSessionParams{
		Path:      path,
		SessionID: sessionID,
	})
	if err != nil {
		return File{}, err
	}
	return s.fromDBItem(dbFile), nil
}

// ListBySession 通过会话id获取文件列表
func (s *service) ListBySession(ctx context.Context, sessionID string) ([]File, error) {
	dbFiles, err := s.q.ListFilesBySession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	files := make([]File, len(dbFiles))
	for i, dbFile := range dbFiles {
		files[i] = s.fromDBItem(dbFile)
	}
	return files, nil
}

// ListLatestSessionFiles 通过会话id获取最新文件列表
func (s *service) ListLatestSessionFiles(ctx context.Context, sessionID string) ([]File, error) {
	dbFiles, err := s.q.ListLatestSessionFiles(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	files := make([]File, len(dbFiles))
	for i, dbFile := range dbFiles {
		files[i] = s.fromDBItem(dbFile)
	}
	return files, nil
}

// Delete 删除文件
func (s *service) Delete(ctx context.Context, id string) error {
	file, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	err = s.q.DeleteFile(ctx, id)
	if err != nil {
		return err
	}
	s.Publish(pubsub.DeletedEvent, file)
	return nil
}

// DeleteSessionFiles 删除会话文件
func (s *service) DeleteSessionFiles(ctx context.Context, sessionID string) error {
	// 通过会话id获取文件列表
	files, err := s.ListBySession(ctx, sessionID)
	if err != nil {
		return err
	}

	// 删除文件列表中的每个文件
	for _, file := range files {
		err = s.Delete(ctx, file.ID)
		if err != nil {
			return err
		}
	}
	return nil
}

// fromDBItem 将数据库文件数据结构转为文件对象
func (s *service) fromDBItem(item db.File) File {
	return File{
		ID:        item.ID,        // 文件id
		SessionID: item.SessionID, // 会话id
		Path:      item.Path,      // 文件路径
		Content:   item.Content,   // 文件内容
		Version:   item.Version,   // 文件版本
		CreatedAt: item.CreatedAt, // 创建时间戳
		UpdatedAt: item.UpdatedAt, // 更新时间戳
	}
}
