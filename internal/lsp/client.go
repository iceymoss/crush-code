package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/fsext"
	"github.com/charmbracelet/crush/internal/home"
	powernap "github.com/charmbracelet/x/powernap/pkg/lsp"
	"github.com/charmbracelet/x/powernap/pkg/lsp/protocol"
	"github.com/charmbracelet/x/powernap/pkg/transport"
)

// DiagnosticCounts 持有诊断数量按严重性分类
type DiagnosticCounts struct {
	Error       int // 错误数量
	Warning     int // 警告数量
	Information int // 信息数量
	Hint        int // 提示数量
}

// Client 表示一个LSP客户端
type Client struct {
	client *powernap.Client // 底层LSP客户端
	name   string           // 客户端名称
	debug  bool             // 调试模式

	// Working directory this LSP is scoped to. 这个LSP客户端的作用域工作目录
	cwd string // 工作目录

	// File types this LSP server handles (e.g., .go, .rs, .py)
	fileTypes []string // 文件类型

	// Configuration for this LSP client
	config config.LSPConfig // 配置

	// Original context and resolver for recreating the client 原始上下文和解析器用于重新创建客户端
	ctx      context.Context         // 原始上下文
	resolver config.VariableResolver // 解析器

	// Diagnostic change callback 诊断变化回调
	onDiagnosticsChanged func(name string, count int) // 诊断变化回调

	// Diagnostic cache
	diagnostics *csync.VersionedMap[protocol.DocumentURI, []protocol.Diagnostic] // 诊断缓存

	// Cached diagnostic counts to avoid map copy on every UI render. 缓存诊断数量避免每次UI渲染时复制映射
	diagCountsCache   DiagnosticCounts // 诊断缓存
	diagCountsVersion uint64           // 诊断版本
	diagCountsMu      sync.Mutex       // 诊断互斥锁

	// Files are currently opened by the LSP 当前由LSP打开的文件
	openFiles *csync.Map[string, *OpenFileInfo] // 打开文件缓存

	// Server state 服务器状态
	serverState atomic.Value // 服务器状态
}

// New creates a new LSP client using the powernap implementation.
// 创建一个新的LSP客户端使用powernap实现
func New(
	ctx context.Context, // 原始上下文
	name string,
	cfg config.LSPConfig, // 配置
	resolver config.VariableResolver, // 解析器
	cwd string, // 工作目录
	debug bool, // 调试模式
) (*Client, error) {
	client := &Client{ // 创建一个新的LSP客户端
		name:        name,                                                                 // 客户端名称
		fileTypes:   cfg.FileTypes,                                                        // 文件类型
		diagnostics: csync.NewVersionedMap[protocol.DocumentURI, []protocol.Diagnostic](), // 诊断缓存
		openFiles:   csync.NewMap[string, *OpenFileInfo](),                                // 打开文件缓存
		config:      cfg,                                                                  // 配置
		ctx:         ctx,                                                                  // 原始上下文
		debug:       debug,                                                                // 调试模式
		resolver:    resolver,                                                             // 解析器
		cwd:         cwd,                                                                  // 工作目录
	}
	client.serverState.Store(StateStopped) // 设置服务器状态为Stopped

	if err := client.createPowernapClient(); err != nil {
		return nil, err
	}

	return client, nil // 返回新的LSP客户端
}

// Initialize initializes the LSP client and returns the server capabilities.
func (c *Client) Initialize(ctx context.Context, workspaceDir string) (*protocol.InitializeResult, error) {
	if err := c.client.Initialize(ctx, false); err != nil {
		return nil, fmt.Errorf("failed to initialize the lsp client: %w", err)
	}

	// Convert powernap capabilities to protocol capabilities
	caps := c.client.GetCapabilities()
	protocolCaps := protocol.ServerCapabilities{
		TextDocumentSync: caps.TextDocumentSync,
		CompletionProvider: func() *protocol.CompletionOptions {
			if caps.CompletionProvider != nil {
				return &protocol.CompletionOptions{
					TriggerCharacters:   caps.CompletionProvider.TriggerCharacters,
					AllCommitCharacters: caps.CompletionProvider.AllCommitCharacters,
					ResolveProvider:     caps.CompletionProvider.ResolveProvider,
				}
			}
			return nil
		}(),
	}

	result := &protocol.InitializeResult{
		Capabilities: protocolCaps,
	}

	c.registerHandlers()

	return result, nil
}

// closeTimeout is the maximum time to wait for a graceful LSP shutdown.
const closeTimeout = 5 * time.Second

// Kill kills the client without doing anything else.
func (c *Client) Kill() { c.client.Kill() }

// Close closes all open files in the client, then shuts down gracefully.
// If shutdown takes longer than closeTimeout, it falls back to Kill().
func (c *Client) Close(ctx context.Context) error {
	c.CloseAllFiles(ctx)

	// Use a timeout to prevent hanging on unresponsive LSP servers.
	// jsonrpc2's send lock doesn't respect context cancellation, so we
	// need to fall back to Kill() which closes the underlying connection.
	closeCtx, cancel := context.WithTimeout(ctx, closeTimeout)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		if err := c.client.Shutdown(closeCtx); err != nil {
			slog.Warn("Failed to shutdown LSP client", "error", err)
		}
		done <- c.client.Exit()
	}()

	select {
	case err := <-done:
		return err
	case <-closeCtx.Done():
		c.client.Kill()
		return closeCtx.Err()
	}
}

// createPowernapClient creates a new powernap client with the current configuration.
func (c *Client) createPowernapClient() error {
	rootURI := string(protocol.URIFromPath(c.cwd))

	command, err := c.resolver.ResolveValue(c.config.Command)
	if err != nil {
		return fmt.Errorf("invalid lsp command: %w", err)
	}

	clientConfig := powernap.ClientConfig{
		Command:     home.Long(command),
		Args:        c.config.Args,
		RootURI:     rootURI,
		Environment: maps.Clone(c.config.Env),
		Settings:    c.config.Options,
		InitOptions: c.config.InitOptions,
		WorkspaceFolders: []protocol.WorkspaceFolder{
			{
				URI:  rootURI,
				Name: filepath.Base(c.cwd),
			},
		},
	}

	powernapClient, err := powernap.NewClient(clientConfig)
	if err != nil {
		return fmt.Errorf("failed to create lsp client: %w", err)
	}

	c.client = powernapClient
	return nil
}

// registerHandlers registers the standard LSP notification and request handlers.
func (c *Client) registerHandlers() {
	c.RegisterServerRequestHandler("workspace/applyEdit", HandleApplyEdit(c.client.GetOffsetEncoding()))
	c.RegisterServerRequestHandler("workspace/configuration", HandleWorkspaceConfiguration)
	c.RegisterServerRequestHandler("client/registerCapability", HandleRegisterCapability)
	c.RegisterNotificationHandler("window/showMessage", func(ctx context.Context, method string, params json.RawMessage) {
		if c.debug {
			HandleServerMessage(ctx, method, params)
		}
	})
	c.RegisterNotificationHandler("textDocument/publishDiagnostics", func(_ context.Context, _ string, params json.RawMessage) {
		HandleDiagnostics(c, params)
	})
}

// Restart closes the current LSP client and creates a new one with the same configuration.
func (c *Client) Restart() error {
	var openFiles []string
	for uri := range c.openFiles.Seq2() {
		openFiles = append(openFiles, string(uri))
	}

	closeCtx, cancel := context.WithTimeout(c.ctx, 10*time.Second)
	defer cancel()

	if err := c.Close(closeCtx); err != nil {
		slog.Warn("Error closing client during restart", "name", c.name, "error", err)
	}

	c.SetServerState(StateStopped)

	c.diagCountsCache = DiagnosticCounts{}
	c.diagCountsVersion = 0

	if err := c.createPowernapClient(); err != nil {
		return err
	}

	initCtx, cancel := context.WithTimeout(c.ctx, 30*time.Second)
	defer cancel()

	c.SetServerState(StateStarting)

	if err := c.client.Initialize(initCtx, false); err != nil {
		c.SetServerState(StateError)
		return fmt.Errorf("failed to initialize lsp client: %w", err)
	}

	c.registerHandlers()

	if err := c.WaitForServerReady(initCtx); err != nil {
		slog.Error("Server failed to become ready after restart", "name", c.name, "error", err)
		c.SetServerState(StateError)
		return err
	}

	for _, uri := range openFiles {
		if err := c.OpenFile(initCtx, uri); err != nil {
			slog.Warn("Failed to reopen file after restart", "file", uri, "error", err)
		}
	}
	return nil
}

// ServerState represents the state of an LSP server
type ServerState int

const (
	StateUnstarted ServerState = iota
	StateStarting
	StateReady
	StateError
	StateStopped
	StateDisabled
)

// GetServerState returns the current state of the LSP server
func (c *Client) GetServerState() ServerState {
	if val := c.serverState.Load(); val != nil {
		return val.(ServerState)
	}
	return StateStarting
}

// SetServerState sets the current state of the LSP server
func (c *Client) SetServerState(state ServerState) {
	c.serverState.Store(state)
}

// GetName returns the name of the LSP client
func (c *Client) GetName() string {
	return c.name
}

// SetDiagnosticsCallback sets the callback function for diagnostic changes
func (c *Client) SetDiagnosticsCallback(callback func(name string, count int)) {
	c.onDiagnosticsChanged = callback
}

// WaitForServerReady waits for the server to be ready
func (c *Client) WaitForServerReady(ctx context.Context) error {
	// Set initial state
	c.SetServerState(StateStarting)

	// Try to ping the server with a simple request
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	if c.debug {
		slog.Debug("Waiting for LSP server to be ready...")
	}

	c.openKeyConfigFiles(ctx)

	for {
		select {
		case <-ctx.Done():
			c.SetServerState(StateError)
			return fmt.Errorf("timeout waiting for LSP server to be ready")
		case <-ticker.C:
			// Check if client is running
			if !c.client.IsRunning() {
				if c.debug {
					slog.Debug("LSP server not ready yet", "server", c.name)
				}
				continue
			}

			// Server is ready
			c.SetServerState(StateReady)
			if c.debug {
				slog.Debug("LSP server is ready")
			}
			return nil
		}
	}
}

// OpenFileInfo contains information about an open file
type OpenFileInfo struct {
	Version int32
	URI     protocol.DocumentURI
}

// HandlesFile checks if this LSP client handles the given file based on its
// extension and whether it's within the working directory.
func (c *Client) HandlesFile(path string) bool {
	if c == nil {
		return false
	}
	if !fsext.HasPrefix(path, c.cwd) {
		slog.Debug("File outside workspace", "name", c.name, "file", path, "workDir", c.cwd)
		return false
	}
	return handlesFiletype(c.name, c.fileTypes, path)
}

// OpenFile opens a file in the LSP server.
func (c *Client) OpenFile(ctx context.Context, filepath string) error {
	if !c.HandlesFile(filepath) {
		return nil
	}

	uri := string(protocol.URIFromPath(filepath))

	if _, exists := c.openFiles.Get(uri); exists {
		return nil // Already open
	}

	// Skip files that do not exist or cannot be read
	content, err := os.ReadFile(filepath)
	if err != nil {
		return fmt.Errorf("error reading file: %w", err)
	}

	// Notify the server about the opened document
	if err = c.client.NotifyDidOpenTextDocument(ctx, uri, string(powernap.DetectLanguage(filepath)), 1, string(content)); err != nil {
		return err
	}

	c.openFiles.Set(uri, &OpenFileInfo{
		Version: 1,
		URI:     protocol.DocumentURI(uri),
	})

	return nil
}

// NotifyChange notifies the server about a file change.
func (c *Client) NotifyChange(ctx context.Context, filepath string) error {
	if c == nil {
		return nil
	}
	uri := string(protocol.URIFromPath(filepath))

	content, err := os.ReadFile(filepath)
	if err != nil {
		return fmt.Errorf("error reading file: %w", err)
	}

	fileInfo, isOpen := c.openFiles.Get(uri)
	if !isOpen {
		return fmt.Errorf("cannot notify change for unopened file: %s", filepath)
	}

	// Increment version
	fileInfo.Version++

	// Create change event
	changes := []protocol.TextDocumentContentChangeEvent{
		{
			Value: protocol.TextDocumentContentChangeWholeDocument{
				Text: string(content),
			},
		},
	}

	return c.client.NotifyDidChangeTextDocument(ctx, uri, int(fileInfo.Version), changes)
}

// IsFileOpen checks if a file is currently open.
func (c *Client) IsFileOpen(filepath string) bool {
	uri := string(protocol.URIFromPath(filepath))
	_, exists := c.openFiles.Get(uri)
	return exists
}

// CloseAllFiles closes all currently open files.
func (c *Client) CloseAllFiles(ctx context.Context) {
	for uri := range c.openFiles.Seq2() {
		if c.debug {
			slog.Debug("Closing file", "file", uri)
		}
		if err := c.client.NotifyDidCloseTextDocument(ctx, uri); err != nil {
			slog.Warn("Error closing file", "uri", uri, "error", err)
			continue
		}
		c.openFiles.Del(uri)
	}
}

// GetFileDiagnostics returns diagnostics for a specific file.
func (c *Client) GetFileDiagnostics(uri protocol.DocumentURI) []protocol.Diagnostic {
	diags, _ := c.diagnostics.Get(uri)
	return diags
}

// GetDiagnostics returns all diagnostics for all files.
func (c *Client) GetDiagnostics() map[protocol.DocumentURI][]protocol.Diagnostic {
	if c == nil {
		return nil
	}
	return c.diagnostics.Copy()
}

// GetDiagnosticCounts returns cached diagnostic counts by severity.
// Uses the VersionedMap version to avoid recomputing on every call.
func (c *Client) GetDiagnosticCounts() DiagnosticCounts {
	if c == nil {
		return DiagnosticCounts{}
	}
	currentVersion := c.diagnostics.Version()

	c.diagCountsMu.Lock()
	defer c.diagCountsMu.Unlock()

	if currentVersion == c.diagCountsVersion {
		return c.diagCountsCache
	}

	// Recompute counts.
	counts := DiagnosticCounts{}
	for _, diags := range c.diagnostics.Seq2() {
		for _, diag := range diags {
			switch diag.Severity {
			case protocol.SeverityError:
				counts.Error++
			case protocol.SeverityWarning:
				counts.Warning++
			case protocol.SeverityInformation:
				counts.Information++
			case protocol.SeverityHint:
				counts.Hint++
			}
		}
	}

	c.diagCountsCache = counts
	c.diagCountsVersion = currentVersion
	return counts
}

// OpenFileOnDemand opens a file only if it's not already open.
func (c *Client) OpenFileOnDemand(ctx context.Context, filepath string) error {
	if c == nil {
		return nil
	}
	// Check if the file is already open
	if c.IsFileOpen(filepath) {
		return nil
	}

	// Open the file
	return c.OpenFile(ctx, filepath)
}

// RegisterNotificationHandler registers a notification handler.
func (c *Client) RegisterNotificationHandler(method string, handler transport.NotificationHandler) {
	c.client.RegisterNotificationHandler(method, handler)
}

// RegisterServerRequestHandler handles server requests.
func (c *Client) RegisterServerRequestHandler(method string, handler transport.Handler) {
	c.client.RegisterHandler(method, handler)
}

// openKeyConfigFiles opens important configuration files that help initialize the server.
func (c *Client) openKeyConfigFiles(ctx context.Context) {
	// Try to open each file, ignoring errors if they don't exist
	for _, file := range c.config.RootMarkers {
		file = filepath.Join(c.cwd, file)
		if _, err := os.Stat(file); err == nil {
			// File exists, try to open it
			if err := c.OpenFile(ctx, file); err != nil {
				slog.Error("Failed to open key config file", "file", file, "error", err)
			} else {
				slog.Debug("Opened key config file for initialization", "file", file)
			}
		}
	}
}

// WaitForDiagnostics waits until diagnostics change or the timeout is reached.
func (c *Client) WaitForDiagnostics(ctx context.Context, d time.Duration) {
	if c == nil {
		return
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.After(d)
	pv := c.diagnostics.Version()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timeout:
			return
		case <-ticker.C:
			if pv != c.diagnostics.Version() {
				return
			}
		}
	}
}

// FindReferences finds all references to the symbol at the given position.
func (c *Client) FindReferences(ctx context.Context, filepath string, line, character int, includeDeclaration bool) ([]protocol.Location, error) {
	if err := c.OpenFileOnDemand(ctx, filepath); err != nil {
		return nil, err
	}

	// Add timeout to prevent hanging on slow LSP servers.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// NOTE: line and character should be 0-based.
	// See: https://microsoft.github.io/language-server-protocol/specifications/lsp/3.17/specification/#position
	return c.client.FindReferences(ctx, filepath, line-1, character-1, includeDeclaration)
}
