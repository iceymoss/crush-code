// Package filetracker provides functionality to track file reads in sessions.
package filetracker

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/charmbracelet/crush/internal/db"
)

// Service 定义了文件跟踪服务的接口，用于跟踪文件的读取操作
type Service interface {
	// RecordRead 记录文件的读取操作
	RecordRead(ctx context.Context, sessionID, path string)

	// LastReadTime 返回文件的最后读取时间
	LastReadTime(ctx context.Context, sessionID, path string) time.Time

	// ListReadFiles 返回所有被读取的文件路径
	ListReadFiles(ctx context.Context, sessionID string) ([]string, error)
}

// service 定义了文件跟踪服务的实现，实现了Service接口
type service struct {
	q *db.Queries // 数据库查询器，用于执行数据库操作
}

// NewService creates a new file tracker service.
func NewService(q *db.Queries) Service {
	return &service{q: q}
}

// RecordRead 记录文件的读取操作
func (s *service) RecordRead(ctx context.Context, sessionID, path string) {
	// 记录文件的读取操作，如果记录失败，则记录错误日志
	if err := s.q.RecordFileRead(ctx, db.RecordFileReadParams{
		SessionID: sessionID,     // 会话id
		Path:      relpath(path), // 文件路径
	}); err != nil {
		slog.Error("Error recording file read", "error", err, "file", path) // 记录错误日志
	}
}

// LastReadTime 返回文件的最后读取时间
func (s *service) LastReadTime(ctx context.Context, sessionID, path string) time.Time {
	// 获取文件的最后读取时间，如果获取失败，则返回零时间
	readFile, err := s.q.GetFileRead(ctx, db.GetFileReadParams{
		SessionID: sessionID,
		Path:      relpath(path),
	})
	if err != nil {
		return time.Time{} // 如果获取失败，则返回零时间
	}

	// 返回文件的最后读取时间
	return time.Unix(readFile.ReadAt, 0)
}

// relpath 获取文件的相对路径
func relpath(path string) string {
	// 获取文件的相对路径，如果获取失败，则返回原始路径
	path = filepath.Clean(path)
	basepath, err := os.Getwd()
	if err != nil {
		slog.Warn("Error getting basepath", "error", err)
		return path
	}
	relpath, err := filepath.Rel(basepath, path)
	if err != nil {
		slog.Warn("Error getting relpath", "error", err)
		return path
	}
	return relpath
}

// ListReadFiles 获取一个会话中所有被读取的文件路径
func (s *service) ListReadFiles(ctx context.Context, sessionID string) ([]string, error) {
	// 获取一个会话中所有被读取的文件路径，如果获取失败，则返回错误
	readFiles, err := s.q.ListSessionReadFiles(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("listing read files: %w", err) // 返回错误
	}

	// 获取工作目录，如果获取失败，则返回错误
	basepath, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("getting working directory: %w", err)
	}

	// 创建一个用于存储文件路径的切片
	paths := make([]string, 0, len(readFiles))
	for _, rf := range readFiles {
		paths = append(paths, filepath.Join(basepath, rf.Path))
	}
	return paths, nil
}
