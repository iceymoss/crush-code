// Package lsp provides a manager for Language Server Protocol (LSP) clients.
// LSP是Language Server Protocol的缩写，是一种用于代码编辑器的协议
package lsp

import (
	"cmp"
	"context"
	"errors"
	"io"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/fsext"
	powernapconfig "github.com/charmbracelet/x/powernap/pkg/config"
	powernap "github.com/charmbracelet/x/powernap/pkg/lsp"
	"github.com/sourcegraph/jsonrpc2"
)

// unavailable 定义了一个用于存储不可用LSP服务器的映射
var unavailable = csync.NewMap[string, struct{}]()

// Manager 定义了LSP管理器，用于管理LSP的各种操作, 本质是和项目配置的LSP服务器进行交互
// 让agent和大模型有代码语义分析能力，而不是靠大模型的文本分析能力，提高代码分析的准确性和效率
type Manager struct {
	clients  *csync.Map[string, *Client]       // 存储LSP客户端的映射
	cfg      *config.ConfigStore               // 配置存储，用于存储配置信息
	manager  *powernapconfig.Manager           // 功率Nap配置管理器，用于管理功率Nap的各种操作
	callback func(name string, client *Client) // 回调函数，用于回调LSP客户端的各种操作
}

// NewManager 创建一个新的LSP管理器
func NewManager(cfg *config.ConfigStore) *Manager {
	manager := powernapconfig.NewManager() // 创建一个新的功率Nap配置管理器
	manager.LoadDefaults()                 // 加载默认的功率Nap配置

	// 将用户配置的LSP服务器合并到管理器中
	// 遍历用户配置的LSP服务器，如果服务器被禁用，则跳过
	for name, clientConfig := range cfg.Config().LSP {
		if clientConfig.Disabled {
			slog.Debug("LSP disabled by user config", "name", name)
			manager.RemoveServer(name)
			continue
		}

		// 这里是一个hack，用户可能在自己的配置中使用命令名称而不是实际名称。
		// 找到并使用正确的名称。
		actualName := resolveServerName(manager, name)

		// 将服务器添加到管理器中
		manager.AddServer(actualName, &powernapconfig.ServerConfig{
			Command:     clientConfig.Command,     // 服务器命令
			Args:        clientConfig.Args,        // 服务器参数
			Environment: clientConfig.Env,         // 服务器环境变量
			FileTypes:   clientConfig.FileTypes,   // 服务器文件类型
			RootMarkers: clientConfig.RootMarkers, // 服务器根标记
			InitOptions: clientConfig.InitOptions, // 服务器初始化选项
			Settings:    clientConfig.Options,     // 服务器设置
		})
	}

	return &Manager{
		clients:  csync.NewMap[string, *Client](),
		cfg:      cfg,
		manager:  manager,
		callback: func(string, *Client) {}, // default no-op callback
	}
}

// Clients 返回LSP客户端的映射，例如gopls、rust-analyzer、typescript-language-server等
func (s *Manager) Clients() *csync.Map[string, *Client] {
	return s.clients
}

// SetCallback 设置一个回调函数，当一个新的LSP客户端成功启动时，会调用这个回调函数
// 这个回调函数允许协调器添加LSP工具
// client is successfully started. This allows the coordinator to add LSP tools.
func (s *Manager) SetCallback(cb func(name string, client *Client)) {
	s.callback = cb
}

// TrackConfigured 会回调用户配置的LSP服务器，但不会创建任何客户端
func (s *Manager) TrackConfigured() {
	var wg sync.WaitGroup

	// 遍历管理器中的服务器，如果服务器没有被用户配置，则跳过
	for name := range s.manager.GetServers() {
		// 如果服务器没有被用户配置，则跳过
		if !s.isUserConfigured(name) {
			continue
		}

		// 在新的goroutine中回调用户配置的LSP服务器
		wg.Go(func() {
			s.callback(name, nil)
		})
	}

	// 等待所有回调完成
	wg.Wait()
}

// Start 启动一个LSP服务器，可以处理给定的文件路径
// 如果一个合适的LSP服务器已经在运行，则不启动新的服务器，这是一个空操作
func (s *Manager) Start(ctx context.Context, path string) {
	if !fsext.HasPrefix(path, s.cfg.WorkingDir()) {
		return
	}

	var wg sync.WaitGroup

	// 遍历管理器中的服务器,启动每个服务器
	for name, server := range s.manager.GetServers() {
		wg.Go(func() {
			s.startServer(ctx, name, path, server)
		})
	}
	wg.Wait()
}

// skipAutoStartCommands 包含一些命令，这些命令太通用或模糊，无法在没有显式用户配置的情况下自动启动
var skipAutoStartCommands = map[string]bool{
	"buck2":   true,
	"buf":     true,
	"cue":     true,
	"dart":    true,
	"deno":    true,
	"dotnet":  true,
	"dprint":  true,
	"gleam":   true,
	"java":    true,
	"julia":   true,
	"koka":    true,
	"node":    true,
	"npx":     true,
	"perl":    true,
	"plz":     true,
	"python":  true,
	"python3": true,
	"R":       true,
	"racket":  true,
	"rome":    true,
	"rubocop": true,
	"ruff":    true,
	"scarb":   true,
	"solc":    true,
	"stylua":  true,
	"swipl":   true,
	"tflint":  true,
}

// startServer 启动一个LSP服务器，可以处理给定的文件路径
func (s *Manager) startServer(ctx context.Context, name, filepath string, server *powernapconfig.ServerConfig) {
	// 构建配置
	cfg := s.buildConfig(name, server)
	if cfg.Disabled {
		// 如果服务器被禁用，则不启动
		return
	}

	// 如果服务器不可用，则不启动
	if _, exists := unavailable.Get(name); exists {
		// 不启动
		return
	}

	// 如果服务器已经在运行，则不启动
	if client, ok := s.clients.Get(name); ok {
		switch client.GetServerState() { // 获取服务器状态
		case StateReady, StateStarting, StateDisabled:
			s.callback(name, client) // 回调LSP客户端的各种操作
			// 已经完成，返回
			return
		}
	}

	// 检查服务器是否被用户配置
	userConfigured := s.isUserConfigured(name)

	// 如果服务器没有被用户配置，则检查服务器是否安装
	if !userConfigured {
		// 检查服务器是否安装
		if _, err := exec.LookPath(server.Command); err != nil {
			// 如果服务器没有安装，则不启动
			slog.Debug("LSP server not installed, skipping", "name", name, "command", server.Command)
			unavailable.Set(name, struct{}{}) // 设置服务器不可用
			return
		}
		if skipAutoStartCommands[server.Command] {
			slog.Debug("LSP command too generic for auto-start, skipping", "name", name, "command", server.Command)
			return
		}
	}

	// 检查服务器是否处理给定的文件路径
	if !handles(server, filepath, s.cfg.WorkingDir()) {
		// 不处理给定的文件路径，不启动
		return
	}

	// 再次检查服务器是否已经在运行
	if client, ok := s.clients.Get(name); ok {
		switch client.GetServerState() { // 获取服务器状态
		case StateReady, StateStarting, StateDisabled:
			s.callback(name, client) // 回调LSP客户端的各种操作
			return
		}
	}

	// 创建一个新的LSP客户端
	client, err := New(
		ctx,                             // 上下文
		name,                            // 服务器名称
		cfg,                             // 服务器配置
		s.cfg.Resolver(),                // 变量解析器
		s.cfg.WorkingDir(),              // 工作目录
		s.cfg.Config().Options.DebugLSP, // 是否调试
	)
	if err != nil {
		slog.Error("Failed to create LSP client", "name", name, "error", err) // 创建LSP客户端失败，记录错误日志
		return
	}
	// 存储非nil的客户端。如果另一个goroutine竞争我们，则优先使用已经存储的客户端，避免重复创建客户端。
	// 优先使用已经存储的客户端，避免重复创建客户端。
	if existing, ok := s.clients.Get(name); ok {
		switch existing.GetServerState() { // 获取服务器状态
		case StateReady, StateStarting, StateDisabled:
			_ = client.Close(ctx)
			s.callback(name, existing) // 回调LSP客户端的各种操作
			return
		}
	}
	s.clients.Set(name, client) // 存储LSP客户端
	defer func() {
		s.callback(name, client) // 回调LSP客户端的各种操作
	}()

	switch client.GetServerState() { // 获取服务器状态
	case StateReady, StateStarting, StateDisabled:
		// 已经完成，返回
		return
	}

	// 设置服务器状态为Starting
	client.serverState.Store(StateStarting)

	// 创建一个初始化上下文，用于初始化LSP客户端
	initCtx, cancel := context.WithTimeout(ctx, time.Duration(cmp.Or(cfg.Timeout, 30))*time.Second)
	defer cancel()

	// 初始化LSP客户端
	if _, err := client.Initialize(initCtx, s.cfg.WorkingDir()); err != nil {
		slog.Error("LSP client initialization failed", "name", name, "error", err)
		_ = client.Close(ctx)
		s.clients.Del(name)
		return
	}

	// 等待LSP客户端准备好
	if err := client.WaitForServerReady(initCtx); err != nil {
		slog.Warn("LSP server not fully ready, continuing anyway", "name", name, "error", err)
		client.SetServerState(StateError) // 设置服务器状态为Error
	} else {
		client.SetServerState(StateReady) // 设置服务器状态为Ready
	}

	slog.Debug("LSP client started", "name", name) // 记录LSP客户端启动日志
}

// isUserConfigured 检查服务器是否被用户配置
func (s *Manager) isUserConfigured(name string) bool {
	// 获取服务器配置
	cfg, ok := s.cfg.Config().LSP[name]
	// 如果服务器配置存在，并且服务器被禁用，则返回false
	return ok && !cfg.Disabled
}

// buildConfig 构建配置
func (s *Manager) buildConfig(name string, server *powernapconfig.ServerConfig) config.LSPConfig {
	cfg := config.LSPConfig{
		Command:     server.Command,     // 服务器命令
		Args:        server.Args,        // 服务器参数
		Env:         server.Environment, // 服务器环境变量
		FileTypes:   server.FileTypes,   // 服务器文件类型
		RootMarkers: server.RootMarkers, // 服务器根标记
		InitOptions: server.InitOptions, // 服务器初始化选项
		Options:     server.Settings,    // 服务器设置
	}
	// 如果用户配置存在，则使用用户配置的超时时间
	if userCfg, ok := s.cfg.Config().LSP[name]; ok {
		cfg.Timeout = userCfg.Timeout // 用户配置的超时时间
	}
	return cfg
}

// resolveServerName 解析服务器名称
// 如果服务器名称在管理器中存在，则返回服务器名称
// 否则，遍历管理器中的服务器，如果服务器命令名称与给定名称匹配，则返回服务器名称
// 否则，返回给定名称
func resolveServerName(manager *powernapconfig.Manager, name string) string {
	// 如果服务器名称在管理器中存在，则返回服务器名称
	if _, ok := manager.GetServer(name); ok {
		return name
	}
	for sname, server := range manager.GetServers() {
		if server.Command == name {
			return sname
		}
	}
	return name
}

// handlesFiletype 检查服务器是否处理给定的文件类型
func handlesFiletype(sname string, fileTypes []string, filePath string) bool {
	// 如果文件类型为空，则返回true
	if len(fileTypes) == 0 {
		// 处理给定的文件类型
		return true
	}

	// 检测文件类型
	kind := powernap.DetectLanguage(filePath)
	// 获取文件名称
	name := strings.ToLower(filepath.Base(filePath))
	// 遍历文件类型
	for _, filetype := range fileTypes {
		// 将文件类型转换为小写
		suffix := strings.ToLower(filetype)
		// 如果文件类型不以.开头，则添加.
		if !strings.HasPrefix(suffix, ".") {
			suffix = "." + suffix
		}
		if strings.HasSuffix(name, suffix) || filetype == string(kind) {
			slog.Debug("Handles file", "name", sname, "file", name, "filetype", filetype, "kind", kind)
			return true
		}
	}

	slog.Debug("Doesn't handle file", "name", sname, "file", name)
	return false
}

// hasRootMarkers 检查服务器是否处理给定的根标记
func hasRootMarkers(dir string, markers []string) bool {
	// 如果根标记为空，则返回true
	if len(markers) == 0 {
		return true
	}
	// 遍历根标记
	for _, pattern := range markers {
		// 使用filepath.Glob进行非递归检查
		// 在根目录中。这避免了遍历整个树（这在大型多仓库项目中是灾难性的，例如node_modules等）。
		matches, err := filepath.Glob(filepath.Join(dir, pattern))
		if err == nil && len(matches) > 0 {
			return true
		}
	}
	return false
}

// handles 检查服务器是否处理给定的文件路径
func handles(server *powernapconfig.ServerConfig, filePath, workDir string) bool {
	// 检查服务器是否处理给定的文件类型
	return handlesFiletype(server.Command, server.FileTypes, filePath) &&
		// 检查服务器是否处理给定的根标记
		hasRootMarkers(workDir, server.RootMarkers)
}

// KillAll 强制杀死所有LSP客户端
//
// 这通常比[Manager.StopAll]更快，因为它不等待服务器优雅地退出，但如果在服务器中间写入某些内容，可能会导致数据丢失。
// 通常在关闭Crush时，这并不重要。
func (s *Manager) KillAll(context.Context) {
	var wg sync.WaitGroup
	for name, client := range s.clients.Seq2() {
		wg.Go(func() {
			defer func() { s.callback(name, client) }()
			client.client.Kill()
			client.SetServerState(StateStopped)
			s.clients.Del(name)
			slog.Debug("Killed LSP client", "name", name)
		})
	}
	wg.Wait()
}

// StopAll 停止所有正在运行的LSP客户端并清除客户端映射
func (s *Manager) StopAll(ctx context.Context) {
	var wg sync.WaitGroup
	for name, client := range s.clients.Seq2() {
		wg.Go(func() {
			defer func() { s.callback(name, client) }()
			if err := client.Close(ctx); err != nil &&
				!errors.Is(err, io.EOF) &&
				!errors.Is(err, context.Canceled) &&
				!errors.Is(err, jsonrpc2.ErrClosed) &&
				err.Error() != "signal: killed" {
				slog.Warn("Failed to stop LSP client", "name", name, "error", err)
			}
			client.SetServerState(StateStopped)
			s.clients.Del(name)
			slog.Debug("Stopped LSP client", "name", name)
		})
	}
	wg.Wait()
}
