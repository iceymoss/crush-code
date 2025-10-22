package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/lsp"
)

// initLSPClients initializes LSP clients.
func (app *App) initLSPClients(ctx context.Context) {
	for name, clientConfig := range app.config.LSP {
		if clientConfig.Disabled {
			slog.Info("Skipping disabled LSP client", "name", name)
			continue
		}
		go app.createAndStartLSPClient(ctx, name, clientConfig)
	}
	slog.Info("LSP clients initialization started in background")
}

// createAndStartLSPClient 创建新的 LSP 客户端，初始化它，并启动其工作区观察程序
func (app *App) createAndStartLSPClient(ctx context.Context, name string, config config.LSPConfig) {
	slog.Info("Creating LSP client", "name", name, "command", config.Command, "fileTypes", config.FileTypes, "args", config.Args)

	// 检查工作目录中是否存在任何根标记（config 现在有默认值）
	if !lsp.HasRootMarkers(app.config.WorkingDir(), config.RootMarkers) {
		slog.Info("Skipping LSP client - no root markers found", "name", name, "rootMarkers", config.RootMarkers)
		updateLSPState(name, lsp.StateDisabled, nil, nil, 0)
		return
	}

	// 将状态更新为启动
	updateLSPState(name, lsp.StateStarting, nil, nil, 0)

	// 创建 LSP 客户端。
	lspClient, err := lsp.New(ctx, name, config, app.config.Resolver())
	if err != nil {
		slog.Error("Failed to create LSP client for", name, err)
		updateLSPState(name, lsp.StateError, err, nil, 0)
		return
	}

	// 设置诊断回调
	lspClient.SetDiagnosticsCallback(updateLSPDiagnostics)

	// Increase initialization timeout as some servers take more time to start.
	initCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Initialize LSP client.
	_, err = lspClient.Initialize(initCtx, app.config.WorkingDir())
	if err != nil {
		slog.Error("Initialize failed", "name", name, "error", err)
		updateLSPState(name, lsp.StateError, err, lspClient, 0)
		lspClient.Close(ctx)
		return
	}

	// 等待服务器准备就绪
	if err := lspClient.WaitForServerReady(initCtx); err != nil {
		slog.Error("Server failed to become ready", "name", name, "error", err)
		// 服务器从未达到就绪状态，但无论如何让我们继续，因为
		// 某些功能可能仍然有效。
		lspClient.SetServerState(lsp.StateError)
		updateLSPState(name, lsp.StateError, err, lspClient, 0)
	} else {
		// 服务器成功达到就绪状态
		slog.Info("LSP server is ready", "name", name)
		lspClient.SetServerState(lsp.StateReady)
		updateLSPState(name, lsp.StateReady, nil, lspClient, 0)
	}

	slog.Info("LSP client initialized", "name", name)

	// 在启动 goroutine 之前使用互斥锁保护添加到map
	app.LSPClients.Set(name, lspClient)
}
