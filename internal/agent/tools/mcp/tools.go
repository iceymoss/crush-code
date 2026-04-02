package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"log/slog"
	"slices"
	"strings"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Tool = mcp.Tool

// ToolResult represents the result of running an MCP tool.
type ToolResult struct {
	Type      string
	Content   string
	Data      []byte
	MediaType string
}

var allTools = csync.NewMap[string, []*Tool]()

// Tools returns all available MCP tools.
func Tools() iter.Seq2[string, []*Tool] {
	return allTools.Seq2()
}

// RunTool runs an MCP tool with the given input parameters.
func RunTool(ctx context.Context, cfg *config.ConfigStore, name, toolName string, input string) (ToolResult, error) {
	var args map[string]any
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return ToolResult{}, fmt.Errorf("error parsing parameters: %s", err)
	}

	c, err := getOrRenewClient(ctx, cfg, name)
	if err != nil {
		return ToolResult{}, err
	}
	result, err := c.CallTool(ctx, &mcp.CallToolParams{
		Name:      toolName,
		Arguments: args,
	})
	if err != nil {
		return ToolResult{}, err
	}

	if len(result.Content) == 0 {
		return ToolResult{Type: "text", Content: ""}, nil
	}

	var textParts []string
	var imageData []byte
	var imageMimeType string
	var audioData []byte
	var audioMimeType string

	for _, v := range result.Content {
		switch content := v.(type) {
		case *mcp.TextContent:
			textParts = append(textParts, content.Text)
		case *mcp.ImageContent:
			if imageData == nil {
				imageData = content.Data
				imageMimeType = content.MIMEType
			}
		case *mcp.AudioContent:
			if audioData == nil {
				audioData = content.Data
				audioMimeType = content.MIMEType
			}
		default:
			textParts = append(textParts, fmt.Sprintf("%v", v))
		}
	}

	textContent := strings.Join(textParts, "\n")

	// MCP SDK returns Data as already base64-encoded, so we use it directly.
	if imageData != nil {
		return ToolResult{
			Type:      "image",
			Content:   textContent,
			Data:      imageData,
			MediaType: imageMimeType,
		}, nil
	}

	if audioData != nil {
		return ToolResult{
			Type:      "media",
			Content:   textContent,
			Data:      audioData,
			MediaType: audioMimeType,
		}, nil
	}

	return ToolResult{
		Type:    "text",
		Content: textContent,
	}, nil
}

// RefreshTools gets the updated list of tools from the MCP and updates the
// global state.
func RefreshTools(ctx context.Context, cfg *config.ConfigStore, name string) {
	session, ok := sessions.Get(name)
	if !ok {
		slog.Warn("Refresh tools: no session", "name", name)
		return
	}

	tools, err := getTools(ctx, session)
	if err != nil {
		updateState(name, StateError, err, nil, Counts{})
		return
	}

	toolCount := updateTools(cfg, name, tools)

	prev, _ := states.Get(name)
	prev.Counts.Tools = toolCount
	updateState(name, StateConnected, nil, session, prev.Counts)
}

// getTools 获取MCP客户端的工具列表
//
//	ctx 上下文
//	session MCP客户端会话
//	返回MCP客户端工具列表和错误
func getTools(ctx context.Context, session *ClientSession) ([]*Tool, error) {
	// 总是调用 ListTools 获取实际可用的工具列表
	// InitializeResult 的 Capabilities.Tools 字段可能是空对象 {},
	// 这在 MCP 规范中是有效的，但我们仍然需要调用 ListTools 来发现工具
	result, err := session.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		return nil, err
	}
	return result.Tools, nil
}

// updateTools 更新MCP客户端工具列表
//
//	cfg 配置存储
//	name 客户端名称
//	tools MCP客户端工具列表
//	返回MCP客户端工具列表数量
func updateTools(cfg *config.ConfigStore, name string, tools []*Tool) int {
	// 剔除掉配置中被禁用的工具，保留有效工具
	tools = filterDisabledTools(cfg, name, tools)
	if len(tools) == 0 {
		// 如果过滤后没有任何可用工具（可能是上游没提供，或者全部被禁用了），
		// 则从全局/缓存中清理掉该客户端的工具记录，避免残留旧数据或空列表
		allTools.Del(name)
		return 0
	}
	// 更新MCP客户端工具列表
	allTools.Set(name, tools)
	return len(tools)
}

// filterDisabledTools 剔除掉配置中被禁用的工具，保留有效工具
//
//	cfg 配置存储
//	mcpName MCP客户端名称
//	tools MCP客户端工具列表
//	返回MCP客户端工具列表
func filterDisabledTools(cfg *config.ConfigStore, mcpName string, tools []*Tool) []*Tool {
	mcpCfg, ok := cfg.Config().MCP[mcpName]
	// 如果MCP客户端配置不存在，或者禁用的工具列表为空，则返回原始工具列表
	if !ok || len(mcpCfg.DisabledTools) == 0 {
		return tools
	}

	// 创建一个新的工具列表
	filtered := make([]*Tool, 0, len(tools))
	for _, tool := range tools {
		// 如果工具名称不在禁用的工具列表中，则添加到新的工具列表中
		if !slices.Contains(mcpCfg.DisabledTools, tool.Name) {
			filtered = append(filtered, tool)
		}
	}
	return filtered
}
