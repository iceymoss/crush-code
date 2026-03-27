package permission

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/google/uuid"
)

// ErrorPermissionDenied 定义了权限拒绝错误
var ErrorPermissionDenied = errors.New("user denied permission")

// CreatePermissionRequest 定义了创建权限请求的结构,其实就是对各个工具的使用权限
type CreatePermissionRequest struct {
	SessionID   string `json:"session_id"`   // 会话id
	ToolCallID  string `json:"tool_call_id"` // 工具调用id
	ToolName    string `json:"tool_name"`    // 工具名称
	Description string `json:"description"`  // 描述
	Action      string `json:"action"`       // 活动
	Params      any    `json:"params"`       // 参数
	Path        string `json:"path"`         // 路径
}

// PermissionNotification 定义了权限通知的结构
type PermissionNotification struct {
	ToolCallID string `json:"tool_call_id"` // 工具调用id
	Granted    bool   `json:"granted"`      // 是否授予
	Denied     bool   `json:"denied"`       // 是否拒绝
}

// PermissionRequest 定义了权限请求的结构
type PermissionRequest struct {
	ID          string `json:"id"`           // 请求id
	SessionID   string `json:"session_id"`   // 会话id
	ToolCallID  string `json:"tool_call_id"` // 工具调用id
	ToolName    string `json:"tool_name"`    // 工具名称
	Description string `json:"description"`  // 描述
	Action      string `json:"action"`       // 活动
	Params      any    `json:"params"`       // 参数
	Path        string `json:"path"`         // 路径
}

// Service 定义了权限服务接口
type Service interface {
	pubsub.Subscriber[PermissionRequest]                                                    // 实现了pubsub.Subscriber[PermissionRequest]接口，让权限服务可以订阅权限请求事件
	GrantPersistent(permission PermissionRequest)                                           // 授予持久化权限
	Grant(permission PermissionRequest)                                                     // 授予权限
	Deny(permission PermissionRequest)                                                      // 拒绝权限
	Request(ctx context.Context, opts CreatePermissionRequest) (bool, error)                // 请求权限
	AutoApproveSession(sessionID string)                                                    // 自动批准会话
	SetSkipRequests(skip bool)                                                              // 设置是否跳过请求
	SkipRequests() bool                                                                     // 是否跳过请求
	SubscribeNotifications(ctx context.Context) <-chan pubsub.Event[PermissionNotification] // 订阅权限通知事件
}

// permissionService 定义了权限服务的实现
type permissionService struct {
	*pubsub.Broker[PermissionRequest] // 继承Broker让权限服务拥有可被订阅和发布事件的能力

	notificationBroker    *pubsub.Broker[PermissionNotification] // 用于对权限处理的各种事件做订阅/发布通知，例如：UI订阅他，用于显示权限请求的通知
	workingDir            string                                 // 工作目录
	sessionPermissions    []PermissionRequest                    // 会话权限列表, 记录各个会话的工具调用权限
	sessionPermissionsMu  sync.RWMutex                           // 会话权限列表互斥锁
	pendingRequests       *csync.Map[string, chan bool]          // 待处理的请求列表，待处理的请求列表，权限id => chan bool 用于同步授权状态
	autoApproveSessions   map[string]bool                        // 自动批准会话列表, 记录各个会话的自动批准状态
	autoApproveSessionsMu sync.RWMutex                           // 自动批准会话列表互斥锁
	skip                  bool                                   // 是否跳过请求
	allowedTools          []string                               // 允许的工具列表, 允许的工具列表, 记录各个工具的权限

	// used to make sure we only process one request at a time
	requestMu       sync.Mutex         // 请求互斥锁
	activeRequest   *PermissionRequest // 当前活动的请求
	activeRequestMu sync.Mutex         // 当前活动的请求互斥锁
}

// GrantPersistent 处理权限请求，授予持久化权限
func (s *permissionService) GrantPersistent(permission PermissionRequest) {
	// 发布一个“创建权限”事件
	s.notificationBroker.Publish(pubsub.CreatedEvent, PermissionNotification{
		ToolCallID: permission.ToolCallID, // 工具调用id
		Granted:    true,                  // 是否授予
	})

	// 获取待处理的请求列表，如果在这个map中，直接授权
	respCh, ok := s.pendingRequests.Get(permission.ID)
	if ok {
		// 如果在这个map中，直接授权
		respCh <- true
	}

	// 将权限添加到会话权限列表中
	s.sessionPermissionsMu.Lock()
	s.sessionPermissions = append(s.sessionPermissions, permission)
	s.sessionPermissionsMu.Unlock()

	s.activeRequestMu.Lock()
	// 如果处理的权限请求就是当前正活跃状态的请求，则清空当前活动的请求，方便下一个授权请求，标记为活跃请求
	if s.activeRequest != nil && s.activeRequest.ID == permission.ID {
		s.activeRequest = nil
	}
	s.activeRequestMu.Unlock()
}

// Grant 处理权限请求，授予权限
func (s *permissionService) Grant(permission PermissionRequest) {
	// 发布一个“创建权限”事件
	s.notificationBroker.Publish(pubsub.CreatedEvent, PermissionNotification{
		ToolCallID: permission.ToolCallID,
		Granted:    true,
	})

	// 获取待处理的请求列表，如果在这个map中，直接授权
	respCh, ok := s.pendingRequests.Get(permission.ID)
	if ok {
		respCh <- true // 授权
	}

	// 如果处理的权限请求就是当前正活跃状态的请求，则清空当前活动的请求，方便下一个授权请求，标记为活跃请求
	s.activeRequestMu.Lock()
	if s.activeRequest != nil && s.activeRequest.ID == permission.ID {
		s.activeRequest = nil
	}
	s.activeRequestMu.Unlock()
}

// Deny 处理权限请求，拒绝权限
func (s *permissionService) Deny(permission PermissionRequest) {
	// 发布一个“创建权限”事件
	s.notificationBroker.Publish(pubsub.CreatedEvent, PermissionNotification{
		ToolCallID: permission.ToolCallID,
		Granted:    false,
		Denied:     true,
	})

	// 获取待处理的请求列表，如果在这个map中，直接拒绝
	respCh, ok := s.pendingRequests.Get(permission.ID)
	if ok {
		respCh <- false // 拒绝
	}

	s.activeRequestMu.Lock()
	// 如果处理的权限请求就是当前正活跃状态的请求，则清空当前活动的请求，方便下一个授权请求，标记为活跃请求
	if s.activeRequest != nil && s.activeRequest.ID == permission.ID {
		s.activeRequest = nil
	}
	s.activeRequestMu.Unlock()
}

// Request 请求权限，返回true表示允许，false表示拒绝
func (s *permissionService) Request(ctx context.Context, opts CreatePermissionRequest) (bool, error) {
	if s.skip {
		// 如果配置了跳过请求，则直接允许
		return true, nil
	}

	// 检查工具/动作组合是否在允许列表中
	commandKey := opts.ToolName + ":" + opts.Action
	if slices.Contains(s.allowedTools, commandKey) || slices.Contains(s.allowedTools, opts.ToolName) { // 如果工具/动作组合在允许列表中，则直接授权
		// 直接允许，不需要再请求
		return true, nil
	}

	// 发布一个“创建权限”事件，通知UI权限请求
	s.notificationBroker.Publish(pubsub.CreatedEvent, PermissionNotification{
		ToolCallID: opts.ToolCallID,
	})
	s.requestMu.Lock()
	defer s.requestMu.Unlock()

	// 检查会话是否在自动批准列表中，如果自动批准，则直接授权
	s.autoApproveSessionsMu.RLock()
	autoApprove := s.autoApproveSessions[opts.SessionID]
	s.autoApproveSessionsMu.RUnlock()

	if autoApprove {
		s.notificationBroker.Publish(pubsub.CreatedEvent, PermissionNotification{
			ToolCallID: opts.ToolCallID,
			Granted:    true,
		})
		// 直接允许，不需要再请求
		return true, nil
	}

	// 检查路径是否存在，如果存在，则获取路径的目录
	fileInfo, err := os.Stat(opts.Path)
	dir := opts.Path
	if err == nil { // 如果路径存在，则获取路径的目录
		if fileInfo.IsDir() {
			dir = opts.Path // 如果路径是目录，则直接使用路径
		} else {
			// 如果路径不是目录，则获取路径的父目录
			dir = filepath.Dir(opts.Path)
		}
	}

	if dir == "." { // 如果路径是当前目录，则使用工作目录
		dir = s.workingDir
	}
	permission := PermissionRequest{
		ID:          uuid.New().String(), // 生成权限请求id
		Path:        dir,                 // 路径
		SessionID:   opts.SessionID,      // 会话id
		ToolCallID:  opts.ToolCallID,     // 工具调用id
		ToolName:    opts.ToolName,       // 工具名称
		Description: opts.Description,    // 描述
		Action:      opts.Action,         // 动作
		Params:      opts.Params,         // 参数
	}

	s.sessionPermissionsMu.RLock()
	// 检查会话权限列表中是否存在相同的权限请求,如果存在，就直接允许，不需要再请求
	for _, p := range s.sessionPermissions {
		if p.ToolName == permission.ToolName && p.Action == permission.Action && p.SessionID == permission.SessionID && p.Path == permission.Path {
			s.sessionPermissionsMu.RUnlock()
			s.notificationBroker.Publish(pubsub.CreatedEvent, PermissionNotification{
				ToolCallID: opts.ToolCallID,
				Granted:    true,
			})
			return true, nil
		}
	}
	s.sessionPermissionsMu.RUnlock()

	// 设置当前活动的请求为当前权限请求，这里使用了锁，防止多个请求同时设置当前活动的请求，导致数据不一致
	s.activeRequestMu.Lock()
	s.activeRequest = &permission
	s.activeRequestMu.Unlock()

	// 创建一个用于同步授权状态的通道，用于等待授权结果
	respCh := make(chan bool, 1)
	// 将通道添加到待处理的请求列表中，待处理的请求列表，权限id => chan bool 用于同步授权状态
	s.pendingRequests.Set(permission.ID, respCh)

	// 删除待处理的请求列表中的通道，用于释放资源
	defer s.pendingRequests.Del(permission.ID)

	// Publish the request
	// 发布权限请求事件，用于通知UI权限请求
	s.Publish(pubsub.CreatedEvent, permission)

	// 等待授权结果，如果上下文取消，则返回错误，如果授权成功，则返回授权状态
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case granted := <-respCh: // 如果授权成功，则返回授权状态
		return granted, nil // 返回授权状态
	}
}

// AutoApproveSession 将指定会话设置为自动批准，自动批准的会话将直接授权，不需要再请求
func (s *permissionService) AutoApproveSession(sessionID string) {
	s.autoApproveSessionsMu.Lock()
	s.autoApproveSessions[sessionID] = true
	s.autoApproveSessionsMu.Unlock()
}

// SubscribeNotifications 订阅权限通知事件，用于接收权限通知事件
func (s *permissionService) SubscribeNotifications(ctx context.Context) <-chan pubsub.Event[PermissionNotification] {
	return s.notificationBroker.Subscribe(ctx)
}

// SetSkipRequests 设置是否跳过请求，如果设置为true，则直接允许，不需要再请求
func (s *permissionService) SetSkipRequests(skip bool) {
	s.skip = skip
}

// SkipRequests 获取是否跳过请求，如果设置为true，则直接允许，不需要再请求
func (s *permissionService) SkipRequests() bool {
	return s.skip
}

// NewPermissionService 创建权限服务
func NewPermissionService(workingDir string, skip bool, allowedTools []string) Service {
	return &permissionService{
		Broker:              pubsub.NewBroker[PermissionRequest](),      // 创建权限请求事件的发布者/订阅者模式中的发布者
		notificationBroker:  pubsub.NewBroker[PermissionNotification](), // 创建权限通知事件的发布者/订阅者模式中的发布者
		workingDir:          workingDir,                                 // 创建工作目录
		sessionPermissions:  make([]PermissionRequest, 0),               // 创建会话权限列表
		autoApproveSessions: make(map[string]bool),                      // 创建自动批准会话列表
		skip:                skip,                                       // 创建是否跳过请求
		allowedTools:        allowedTools,                               // 创建允许的工具列表
		pendingRequests:     csync.NewMap[string, chan bool](),          // 创建待处理的请求列表
	}
}
