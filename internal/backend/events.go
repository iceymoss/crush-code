package backend

import (
	tea "charm.land/bubbletea/v2"

	"github.com/charmbracelet/crush/internal/app"
	"github.com/charmbracelet/crush/internal/config"
)

// SubscribeEvents returns the event channel for a workspace's app.
func (b *Backend) SubscribeEvents(workspaceID string) (<-chan tea.Msg, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}

	return ws.Events(), nil
}

// GetLSPStates returns the state of all LSP clients.
func (b *Backend) GetLSPStates(workspaceID string) (map[string]app.LSPClientInfo, error) {
	_, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}

	return app.GetLSPStates(), nil
}

// GetLSPDiagnostics returns diagnostics for a specific LSP client in
// the workspace.
func (b *Backend) GetLSPDiagnostics(workspaceID, lspName string) (any, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}

	for name, client := range ws.LSPManager.Clients().Seq2() {
		if name == lspName {
			return client.GetDiagnostics(), nil
		}
	}

	return nil, ErrLSPClientNotFound
}

// GetWorkspaceConfig returns the workspace-level configuration.
func (b *Backend) GetWorkspaceConfig(workspaceID string) (*config.Config, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}

	return ws.Cfg, nil
}

// GetWorkspaceProviders returns the configured providers for a
// workspace.
func (b *Backend) GetWorkspaceProviders(workspaceID string) (any, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}

	providers, _ := config.Providers(ws.Cfg)
	return providers, nil
}
