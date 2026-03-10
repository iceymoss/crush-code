package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"

	"github.com/charmbracelet/crush/internal/app"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/db"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/proto"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/version"
	"github.com/google/uuid"
)

type controllerV1 struct {
	*Server
}

func (c *controllerV1) handleGetHealth(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func (c *controllerV1) handleGetVersion(w http.ResponseWriter, _ *http.Request) {
	jsonEncode(w, proto.VersionInfo{
		Version:   version.Version,
		Commit:    version.Commit,
		GoVersion: runtime.Version(),
		Platform:  fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
	})
}

func (c *controllerV1) handlePostControl(w http.ResponseWriter, r *http.Request) {
	var req proto.ServerControl
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		c.logError(r, "Failed to decode request", "error", err)
		jsonError(w, http.StatusBadRequest, "failed to decode request")
		return
	}

	switch req.Command {
	case "shutdown":
		go func() {
			slog.Info("Shutting down server...")
			if err := c.Shutdown(context.Background()); err != nil {
				slog.Error("Failed to shutdown server", "error", err)
			}
		}()
	default:
		c.logError(r, "Unknown command", "command", req.Command)
		jsonError(w, http.StatusBadRequest, "unknown command")
		return
	}
}

func (c *controllerV1) handleGetConfig(w http.ResponseWriter, _ *http.Request) {
	jsonEncode(w, c.cfg)
}

func (c *controllerV1) handleGetWorkspaces(w http.ResponseWriter, _ *http.Request) {
	workspaces := []proto.Workspace{}
	for _, ws := range c.workspaces.Seq2() {
		workspaces = append(workspaces, proto.Workspace{
			ID:      ws.id,
			Path:    ws.path,
			YOLO:    ws.cfg.Permissions != nil && ws.cfg.Permissions.SkipRequests,
			DataDir: ws.cfg.Options.DataDirectory,
			Debug:   ws.cfg.Options.Debug,
			Config:  ws.cfg,
		})
	}
	jsonEncode(w, workspaces)
}

func (c *controllerV1) handleGetWorkspaceLSPDiagnostics(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ws, ok := c.workspaces.Get(id)
	if !ok {
		c.logError(r, "Workspace not found", "id", id)
		jsonError(w, http.StatusNotFound, "workspace not found")
		return
	}

	lspName := r.PathValue("lsp")
	var found bool
	for name, client := range ws.LSPManager.Clients().Seq2() {
		if name == lspName {
			diagnostics := client.GetDiagnostics()
			jsonEncode(w, diagnostics)
			found = true
			break
		}
	}

	if !found {
		c.logError(r, "LSP client not found", "id", id, "lsp", lspName)
		jsonError(w, http.StatusNotFound, "LSP client not found")
	}
}

func (c *controllerV1) handleGetWorkspaceLSPs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	_, ok := c.workspaces.Get(id)
	if !ok {
		c.logError(r, "Workspace not found", "id", id)
		jsonError(w, http.StatusNotFound, "workspace not found")
		return
	}

	lspClients := app.GetLSPStates()
	jsonEncode(w, lspClients)
}

func (c *controllerV1) handleGetWorkspaceAgentSessionPromptQueued(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ws, ok := c.workspaces.Get(id)
	if !ok {
		c.logError(r, "Workspace not found", "id", id)
		jsonError(w, http.StatusNotFound, "workspace not found")
		return
	}

	sid := r.PathValue("sid")
	queued := ws.App.AgentCoordinator.QueuedPrompts(sid)
	jsonEncode(w, queued)
}

func (c *controllerV1) handlePostWorkspaceAgentSessionPromptClear(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ws, ok := c.workspaces.Get(id)
	if !ok {
		c.logError(r, "Workspace not found", "id", id)
		jsonError(w, http.StatusNotFound, "workspace not found")
		return
	}

	sid := r.PathValue("sid")
	ws.App.AgentCoordinator.ClearQueue(sid)
	w.WriteHeader(http.StatusOK)
}

func (c *controllerV1) handleGetWorkspaceAgentSessionSummarize(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ws, ok := c.workspaces.Get(id)
	if !ok {
		c.logError(r, "Workspace not found", "id", id)
		jsonError(w, http.StatusNotFound, "workspace not found")
		return
	}

	sid := r.PathValue("sid")
	if err := ws.App.AgentCoordinator.Summarize(r.Context(), sid); err != nil {
		c.logError(r, "Failed to summarize session", "error", err, "id", id, "sid", sid)
		jsonError(w, http.StatusInternalServerError, "failed to summarize session")
		return
	}
}

func (c *controllerV1) handlePostWorkspaceAgentSessionCancel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ws, ok := c.workspaces.Get(id)
	if !ok {
		c.logError(r, "Workspace not found", "id", id)
		jsonError(w, http.StatusNotFound, "workspace not found")
		return
	}

	sid := r.PathValue("sid")
	if ws.App.AgentCoordinator != nil {
		ws.App.AgentCoordinator.Cancel(sid)
	}
	w.WriteHeader(http.StatusOK)
}

func (c *controllerV1) handleGetWorkspaceAgentSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ws, ok := c.workspaces.Get(id)
	if !ok {
		c.logError(r, "Workspace not found", "id", id)
		jsonError(w, http.StatusNotFound, "workspace not found")
		return
	}

	sid := r.PathValue("sid")
	se, err := ws.App.Sessions.Get(r.Context(), sid)
	if err != nil {
		c.logError(r, "Failed to get session", "error", err, "id", id, "sid", sid)
		jsonError(w, http.StatusInternalServerError, "failed to get session")
		return
	}

	var isSessionBusy bool
	if ws.App.AgentCoordinator != nil {
		isSessionBusy = ws.App.AgentCoordinator.IsSessionBusy(sid)
	}

	jsonEncode(w, proto.AgentSession{
		Session: proto.Session{
			ID:    se.ID,
			Title: se.Title,
		},
		IsBusy: isSessionBusy,
	})
}

func (c *controllerV1) handlePostWorkspaceAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ws, ok := c.workspaces.Get(id)
	if !ok {
		c.logError(r, "Workspace not found", "id", id)
		jsonError(w, http.StatusNotFound, "workspace not found")
		return
	}

	w.Header().Set("Accept", "application/json")

	var msg proto.AgentMessage
	if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
		c.logError(r, "Failed to decode request", "error", err)
		jsonError(w, http.StatusBadRequest, "failed to decode request")
		return
	}

	if ws.App.AgentCoordinator == nil {
		c.logError(r, "Agent coordinator not initialized", "id", id)
		jsonError(w, http.StatusBadRequest, "agent coordinator not initialized")
		return
	}

	if _, err := ws.App.AgentCoordinator.Run(c.ctx, msg.SessionID, msg.Prompt); err != nil {
		c.logError(r, "Failed to enqueue message", "error", err, "id", id, "sid", msg.SessionID)
		jsonError(w, http.StatusInternalServerError, "failed to enqueue message")
		return
	}
}

func (c *controllerV1) handleGetWorkspaceAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ws, ok := c.workspaces.Get(id)
	if !ok {
		c.logError(r, "Workspace not found", "id", id)
		jsonError(w, http.StatusNotFound, "workspace not found")
		return
	}

	var agentInfo proto.AgentInfo
	if ws.App.AgentCoordinator != nil {
		m := ws.App.AgentCoordinator.Model()
		agentInfo = proto.AgentInfo{
			Model:  m.CatwalkCfg,
			IsBusy: ws.App.AgentCoordinator.IsBusy(),
		}
	}
	jsonEncode(w, agentInfo)
}

func (c *controllerV1) handlePostWorkspaceAgentUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ws, ok := c.workspaces.Get(id)
	if !ok {
		c.logError(r, "Workspace not found", "id", id)
		jsonError(w, http.StatusNotFound, "workspace not found")
		return
	}

	if err := ws.App.UpdateAgentModel(r.Context()); err != nil {
		c.logError(r, "Failed to update agent model", "error", err)
		jsonError(w, http.StatusInternalServerError, "failed to update agent model")
		return
	}
}

func (c *controllerV1) handlePostWorkspaceAgentInit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ws, ok := c.workspaces.Get(id)
	if !ok {
		c.logError(r, "Workspace not found", "id", id)
		jsonError(w, http.StatusNotFound, "workspace not found")
		return
	}

	if err := ws.App.InitCoderAgent(r.Context()); err != nil {
		c.logError(r, "Failed to initialize coder agent", "error", err)
		jsonError(w, http.StatusInternalServerError, "failed to initialize coder agent")
		return
	}
}

func (c *controllerV1) handleGetWorkspaceSessionHistory(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ws, ok := c.workspaces.Get(id)
	if !ok {
		c.logError(r, "Workspace not found", "id", id)
		jsonError(w, http.StatusNotFound, "workspace not found")
		return
	}

	sid := r.PathValue("sid")
	historyItems, err := ws.App.History.ListBySession(r.Context(), sid)
	if err != nil {
		c.logError(r, "Failed to list history", "error", err, "id", id, "sid", sid)
		jsonError(w, http.StatusInternalServerError, "failed to list history")
		return
	}

	jsonEncode(w, historyItems)
}

func (c *controllerV1) handleGetWorkspaceSessionMessages(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ws, ok := c.workspaces.Get(id)
	if !ok {
		c.logError(r, "Workspace not found", "id", id)
		jsonError(w, http.StatusNotFound, "workspace not found")
		return
	}

	sid := r.PathValue("sid")
	messages, err := ws.App.Messages.List(r.Context(), sid)
	if err != nil {
		c.logError(r, "Failed to list messages", "error", err, "id", id, "sid", sid)
		jsonError(w, http.StatusInternalServerError, "failed to list messages")
		return
	}

	jsonEncode(w, messages)
}

func (c *controllerV1) handleGetWorkspaceSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ws, ok := c.workspaces.Get(id)
	if !ok {
		c.logError(r, "Workspace not found", "id", id)
		jsonError(w, http.StatusNotFound, "workspace not found")
		return
	}

	sid := r.PathValue("sid")
	sess, err := ws.App.Sessions.Get(r.Context(), sid)
	if err != nil {
		c.logError(r, "Failed to get session", "error", err, "id", id, "sid", sid)
		jsonError(w, http.StatusInternalServerError, "failed to get session")
		return
	}

	jsonEncode(w, sess)
}

func (c *controllerV1) handlePostWorkspaceSessions(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ws, ok := c.workspaces.Get(id)
	if !ok {
		c.logError(r, "Workspace not found", "id", id)
		jsonError(w, http.StatusNotFound, "workspace not found")
		return
	}

	var args session.Session
	if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
		c.logError(r, "Failed to decode request", "error", err)
		jsonError(w, http.StatusBadRequest, "failed to decode request")
		return
	}

	sess, err := ws.App.Sessions.Create(r.Context(), args.Title)
	if err != nil {
		c.logError(r, "Failed to create session", "error", err, "id", id)
		jsonError(w, http.StatusInternalServerError, "failed to create session")
		return
	}

	jsonEncode(w, sess)
}

func (c *controllerV1) handleGetWorkspaceSessions(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ws, ok := c.workspaces.Get(id)
	if !ok {
		c.logError(r, "Workspace not found", "id", id)
		jsonError(w, http.StatusNotFound, "workspace not found")
		return
	}

	sessions, err := ws.App.Sessions.List(r.Context())
	if err != nil {
		c.logError(r, "Failed to list sessions", "error", err)
		jsonError(w, http.StatusInternalServerError, "failed to list sessions")
		return
	}

	jsonEncode(w, sessions)
}

func (c *controllerV1) handlePostWorkspacePermissionsGrant(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ws, ok := c.workspaces.Get(id)
	if !ok {
		c.logError(r, "Workspace not found", "id", id)
		jsonError(w, http.StatusNotFound, "workspace not found")
		return
	}

	var req proto.PermissionGrant
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		c.logError(r, "Failed to decode request", "error", err)
		jsonError(w, http.StatusBadRequest, "failed to decode request")
		return
	}

	perm := permission.PermissionRequest{
		ID:          req.Permission.ID,
		SessionID:   req.Permission.SessionID,
		ToolCallID:  req.Permission.ToolCallID,
		ToolName:    req.Permission.ToolName,
		Description: req.Permission.Description,
		Action:      req.Permission.Action,
		Params:      req.Permission.Params,
		Path:        req.Permission.Path,
	}

	switch req.Action {
	case proto.PermissionAllow:
		ws.App.Permissions.Grant(perm)
	case proto.PermissionAllowForSession:
		ws.App.Permissions.GrantPersistent(perm)
	case proto.PermissionDeny:
		ws.App.Permissions.Deny(perm)
	default:
		c.logError(r, "Invalid permission action", "action", req.Action)
		jsonError(w, http.StatusBadRequest, "invalid permission action")
		return
	}
}

func (c *controllerV1) handlePostWorkspacePermissionsSkip(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ws, ok := c.workspaces.Get(id)
	if !ok {
		c.logError(r, "Workspace not found", "id", id)
		jsonError(w, http.StatusNotFound, "workspace not found")
		return
	}

	var req proto.PermissionSkipRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		c.logError(r, "Failed to decode request", "error", err)
		jsonError(w, http.StatusBadRequest, "failed to decode request")
		return
	}

	ws.App.Permissions.SetSkipRequests(req.Skip)
}

func (c *controllerV1) handleGetWorkspacePermissionsSkip(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ws, ok := c.workspaces.Get(id)
	if !ok {
		c.logError(r, "Workspace not found", "id", id)
		jsonError(w, http.StatusNotFound, "workspace not found")
		return
	}

	skip := ws.App.Permissions.SkipRequests()
	jsonEncode(w, proto.PermissionSkipRequest{Skip: skip})
}

func (c *controllerV1) handleGetWorkspaceProviders(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ws, ok := c.workspaces.Get(id)
	if !ok {
		c.logError(r, "Workspace not found", "id", id)
		jsonError(w, http.StatusNotFound, "workspace not found")
		return
	}

	providers, _ := config.Providers(ws.cfg)
	jsonEncode(w, providers)
}

func (c *controllerV1) handleGetWorkspaceEvents(w http.ResponseWriter, r *http.Request) {
	flusher := http.NewResponseController(w)
	id := r.PathValue("id")
	ws, ok := c.workspaces.Get(id)
	if !ok {
		c.logError(r, "Workspace not found", "id", id)
		jsonError(w, http.StatusNotFound, "workspace not found")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	events := ws.App.Events()

	for {
		select {
		case <-r.Context().Done():
			c.logDebug(r, "Stopping event stream")
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			c.logDebug(r, "Sending event", "event", fmt.Sprintf("%T %+v", ev, ev))
			data, err := json.Marshal(ev)
			if err != nil {
				c.logError(r, "Failed to marshal event", "error", err)
				continue
			}

			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}

func (c *controllerV1) handleGetWorkspaceConfig(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ws, ok := c.workspaces.Get(id)
	if !ok {
		c.logError(r, "Workspace not found", "id", id)
		jsonError(w, http.StatusNotFound, "workspace not found")
		return
	}

	jsonEncode(w, ws.cfg)
}

func (c *controllerV1) handleDeleteWorkspaces(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ws, ok := c.workspaces.Get(id)
	if ok {
		ws.App.Shutdown()
	}
	c.workspaces.Del(id)
}

func (c *controllerV1) handleGetWorkspace(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ws, ok := c.workspaces.Get(id)
	if !ok {
		c.logError(r, "Workspace not found", "id", id)
		jsonError(w, http.StatusNotFound, "workspace not found")
		return
	}

	jsonEncode(w, proto.Workspace{
		ID:      ws.id,
		Path:    ws.path,
		YOLO:    ws.cfg.Permissions != nil && ws.cfg.Permissions.SkipRequests,
		DataDir: ws.cfg.Options.DataDirectory,
		Debug:   ws.cfg.Options.Debug,
		Config:  ws.cfg,
	})
}

func (c *controllerV1) handlePostWorkspaces(w http.ResponseWriter, r *http.Request) {
	var args proto.Workspace
	if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
		c.logError(r, "Failed to decode request", "error", err)
		jsonError(w, http.StatusBadRequest, "failed to decode request")
		return
	}

	if args.Path == "" {
		c.logError(r, "Path is required")
		jsonError(w, http.StatusBadRequest, "path is required")
		return
	}

	id := uuid.New().String()
	cfg, err := config.Init(args.Path, args.DataDir, args.Debug)
	if err != nil {
		c.logError(r, "Failed to initialize config", "error", err)
		jsonError(w, http.StatusBadRequest, fmt.Sprintf("failed to initialize config: %v", err))
		return
	}

	if cfg.Permissions == nil {
		cfg.Permissions = &config.Permissions{}
	}
	cfg.Permissions.SkipRequests = args.YOLO

	if err := createDotCrushDir(cfg.Options.DataDirectory); err != nil {
		c.logError(r, "Failed to create data directory", "error", err)
		jsonError(w, http.StatusInternalServerError, "failed to create data directory")
		return
	}

	conn, err := db.Connect(c.ctx, cfg.Options.DataDirectory)
	if err != nil {
		c.logError(r, "Failed to connect to database", "error", err)
		jsonError(w, http.StatusInternalServerError, "failed to connect to database")
		return
	}

	appWorkspace, err := app.New(c.ctx, conn, cfg)
	if err != nil {
		slog.Error("Failed to create app workspace", "error", err)
		jsonError(w, http.StatusInternalServerError, "failed to create app workspace")
		return
	}

	ws := &Workspace{
		App:  appWorkspace,
		id:   id,
		path: args.Path,
		cfg:  cfg,
		env:  args.Env,
	}

	c.workspaces.Set(id, ws)
	jsonEncode(w, proto.Workspace{
		ID:      id,
		Path:    args.Path,
		DataDir: cfg.Options.DataDirectory,
		Debug:   cfg.Options.Debug,
		YOLO:    cfg.Permissions.SkipRequests,
		Config:  cfg,
		Env:     args.Env,
	})
}

func createDotCrushDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("failed to create data directory: %q %w", dir, err)
	}

	gitIgnorePath := filepath.Join(dir, ".gitignore")
	if _, err := os.Stat(gitIgnorePath); os.IsNotExist(err) {
		if err := os.WriteFile(gitIgnorePath, []byte("*\n"), 0o644); err != nil {
			return fmt.Errorf("failed to create .gitignore file: %q %w", gitIgnorePath, err)
		}
	}

	return nil
}

func jsonEncode(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func jsonError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(proto.Error{Message: message})
}
