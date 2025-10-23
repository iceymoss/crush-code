package app

import (
	"context"
	"maps"
	"time"

	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/lsp"
	"github.com/charmbracelet/crush/internal/pubsub"
)

// LSPEventType 表示LSP事件的类型
type LSPEventType string

const (
	// LSPEventStateChanged LSP事件类型:状态改变
	LSPEventStateChanged LSPEventType = "state_changed"

	// LSPEventDiagnosticsChanged LSP事件类型:诊断改变
	LSPEventDiagnosticsChanged LSPEventType = "diagnostics_changed"
)

// LSPEvent 代表LSP系统中的一个事件
type LSPEvent struct {
	Type            LSPEventType    // 事件类型
	Name            string          // 事件名称
	State           lsp.ServerState // LSP服务器状态
	Error           error           // 错误
	DiagnosticCount int             // 诊断计数
}

// LSPClientInfo 保存有关 LSP 客户端状态的信息
type LSPClientInfo struct {
	Name            string          // LSP客户端名称
	State           lsp.ServerState // LSP服务器状态
	Error           error           // 错误
	Client          *lsp.Client     // LSP客户端
	DiagnosticCount int             // 诊断计数
	ConnectedAt     time.Time       // 连接时间
}

var (
	// LSP状态存储
	lspStates = csync.NewMap[string, LSPClientInfo]()

	// LSP事件Broker
	lspBroker = pubsub.NewBroker[LSPEvent]()
)

// SubscribeLSPEvents 返回 LSP 事件的通道
func SubscribeLSPEvents(ctx context.Context) <-chan pubsub.Event[LSPEvent] {
	return lspBroker.Subscribe(ctx)
}

// GetLSPStates 返回所有LSP客户端的当前状态
func GetLSPStates() map[string]LSPClientInfo {
	return maps.Collect(lspStates.Seq2())
}

// GetLSPState 返回特定LSP客户端的状态
func GetLSPState(name string) (LSPClientInfo, bool) {
	return lspStates.Get(name)
}

// updateLSPState 更新LSP客户端的状态并发布事件
func updateLSPState(name string, state lsp.ServerState, err error, client *lsp.Client, diagnosticCount int) {
	info := LSPClientInfo{
		Name:            name,
		State:           state,
		Error:           err,
		Client:          client,
		DiagnosticCount: diagnosticCount,
	}
	if state == lsp.StateReady {
		// 记录连接时间
		info.ConnectedAt = time.Now()
	}

	// 更新状态
	lspStates.Set(name, info)

	// 发布状态改变事件
	lspBroker.Publish(pubsub.UpdatedEvent, LSPEvent{
		Type:            LSPEventStateChanged,
		Name:            name,
		State:           state,
		Error:           err,
		DiagnosticCount: diagnosticCount,
	})
}

// updateLSPDiagnostics 更新 LSP 客户端的诊断计数并发布事件
func updateLSPDiagnostics(name string, diagnosticCount int) {
	if info, exists := lspStates.Get(name); exists {
		info.DiagnosticCount = diagnosticCount
		lspStates.Set(name, info)

		// 发布诊断更改事件
		lspBroker.Publish(pubsub.UpdatedEvent, LSPEvent{
			Type:            LSPEventDiagnosticsChanged,
			Name:            name,
			State:           info.State,
			Error:           info.Error,
			DiagnosticCount: diagnosticCount,
		})
	}
}
