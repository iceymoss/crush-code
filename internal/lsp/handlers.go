package lsp

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/charmbracelet/crush/internal/lsp/util"
	powernap "github.com/charmbracelet/x/powernap/pkg/lsp"
	"github.com/charmbracelet/x/powernap/pkg/lsp/protocol"
)

// HandleWorkspaceConfiguration 处理工作区配置请求
func HandleWorkspaceConfiguration(_ context.Context, _ string, params json.RawMessage) (any, error) {
	return []map[string]any{{}}, nil
}

// HandleRegisterCapability 处理能力注册请求
func HandleRegisterCapability(_ context.Context, _ string, params json.RawMessage) (any, error) {
	var registerParams protocol.RegistrationParams // 注册参数
	// 解析注册参数
	if err := json.Unmarshal(params, &registerParams); err != nil {
		slog.Error("Error unmarshaling registration params", "error", err)
		return nil, err
	}

	// 遍历注册参数
	for _, reg := range registerParams.Registrations {
		// 根据方法类型处理
		switch reg.Method {
		case "workspace/didChangeWatchedFiles": // 处理文件变化通知
			// 解析注册选项
			optionsJSON, err := json.Marshal(reg.RegisterOptions)
			if err != nil {
				slog.Error("Error marshaling registration options", "error", err)
				continue
			}
			var options protocol.DidChangeWatchedFilesRegistrationOptions // 注册选项
			// 解析注册选项
			if err := json.Unmarshal(optionsJSON, &options); err != nil {
				slog.Error("Error unmarshaling registration options", "error", err)
				continue
			}
			// 存储文件监听器注册
			notifyFileWatchRegistration(reg.ID, options.Watchers)
		}
	}
	return nil, nil
}

// HandleApplyEdit 处理工作区编辑请求
func HandleApplyEdit(encoding powernap.OffsetEncoding) func(_ context.Context, _ string, params json.RawMessage) (any, error) {
	return func(_ context.Context, _ string, params json.RawMessage) (any, error) {
		var edit protocol.ApplyWorkspaceEditParams // 编辑参数
		// 解析编辑参数
		if err := json.Unmarshal(params, &edit); err != nil {
			return nil, err
		}

		// 应用工作区编辑
		err := util.ApplyWorkspaceEdit(edit.Edit, encoding)
		// 如果应用工作区编辑失败，则返回错误
		if err != nil {
			slog.Error("Error applying workspace edit", "error", err)
			// 返回应用工作区编辑失败的结果
			return protocol.ApplyWorkspaceEditResult{Applied: false, FailureReason: err.Error()}, nil
		}

		return protocol.ApplyWorkspaceEditResult{Applied: true}, nil
	}
}

// FileWatchRegistrationHandler 当文件监听器注册接收时调用的函数
type FileWatchRegistrationHandler func(id string, watchers []protocol.FileSystemWatcher)

// fileWatchHandler 持有当前的文件监听器注册处理程序
var fileWatchHandler FileWatchRegistrationHandler

// RegisterFileWatchHandler 设置文件监听器注册处理程序
func RegisterFileWatchHandler(handler FileWatchRegistrationHandler) {
	fileWatchHandler = handler
}

// notifyFileWatchRegistration 通知处理程序关于新的文件监听器注册
func notifyFileWatchRegistration(id string, watchers []protocol.FileSystemWatcher) {
	// 如果文件监听器处理程序不为空，则调用文件监听器处理程序
	if fileWatchHandler != nil {
		// 调用文件监听器处理程序
		fileWatchHandler(id, watchers)
	}
}

// HandleServerMessage 处理服务器消息
func HandleServerMessage(_ context.Context, method string, params json.RawMessage) {
	var msg protocol.ShowMessageParams // 消息参数
	// 解析消息参数
	if err := json.Unmarshal(params, &msg); err != nil {
		slog.Debug("Error unmarshal server message", "error", err)
		return
	}

	switch msg.Type {
	case protocol.Error: // 错误消息
		slog.Error("LSP Server", "message", msg.Message)
	case protocol.Warning: // 警告消息
		slog.Warn("LSP Server", "message", msg.Message)
	case protocol.Info: // 信息消息
		slog.Info("LSP Server", "message", msg.Message)
	case protocol.Log: // 日志消息
		slog.Debug("LSP Server", "message", msg.Message)
	}
}

// HandleDiagnostics 处理诊断通知来自LSP服务器
func HandleDiagnostics(client *Client, params json.RawMessage) {
	var diagParams protocol.PublishDiagnosticsParams // 诊断参数
	// 解析诊断参数
	if err := json.Unmarshal(params, &diagParams); err != nil {
		slog.Error("Error unmarshaling diagnostics params", "error", err)
		return
	}

	// 存储诊断
	client.diagnostics.Set(diagParams.URI, diagParams.Diagnostics)

	// 计算总诊断数量
	totalCount := 0
	for _, diagnostics := range client.diagnostics.Seq2() {
		totalCount += len(diagnostics)
	}

	// 如果诊断回调不为空，则调用诊断回调
	if client.onDiagnosticsChanged != nil {
		client.onDiagnosticsChanged(client.name, totalCount)
	}
}
