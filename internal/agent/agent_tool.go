package agent

import (
	"context"
	_ "embed"
	"errors"

	"charm.land/fantasy"

	"github.com/charmbracelet/crush/internal/agent/prompt"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/config"
)

//go:embed templates/agent_tool.md
var agentToolDescription []byte

type AgentParams struct {
	Prompt string `json:"prompt" description:"The task for the agent to perform"`
}

const (
	// AgentToolName agent 工具名称
	AgentToolName = "agent"
)

// agentTool 构建并返回一个专门用于执行 Agent 任务的代理工具实例
// 这个代理工具内部会调用 buildAgent 构建一个完整的 sessionAgent 实例，然后将其包装为 ParallelAgentTool
func (c *coordinator) agentTool(ctx context.Context) (fantasy.AgentTool, error) {
	// 从配置中获取 AgentTask 配置
	agentCfg, ok := c.cfg.Config().Agents[config.AgentTask]
	if !ok {
		return nil, errors.New("task agent not configured")
	}

	// 构建任务提示词
	prompt, err := taskPrompt(prompt.WithWorkingDir(c.cfg.WorkingDir()))
	if err != nil {
		return nil, err
	}

	// 构建一个用于执行agent任务的agent实例
	agent, err := c.buildAgent(ctx, prompt, agentCfg, true)
	if err != nil {
		return nil, err
	}
	return fantasy.NewParallelAgentTool(
		AgentToolName,                // 工具 agent 名称
		string(agentToolDescription), // 工具 agent描述
		func(ctx context.Context, params AgentParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) { // 工具执行函数
			// 检查提示词
			if params.Prompt == "" {
				return fantasy.NewTextErrorResponse("prompt is required"), nil
			}

			// 获取会话ID
			sessionID := tools.GetSessionFromContext(ctx)
			if sessionID == "" {
				return fantasy.ToolResponse{}, errors.New("session id missing from context")
			}

			// 获取 agent 消息ID
			agentMessageID := tools.GetMessageFromContext(ctx)
			if agentMessageID == "" {
				return fantasy.ToolResponse{}, errors.New("agent message id missing from context")
			}

			// 执行工具agent 任务
			return c.runSubAgent(ctx, subAgentParams{
				Agent:          agent,               // 子 agent 实例
				SessionID:      sessionID,           // 会话ID
				AgentMessageID: agentMessageID,      // agent 消息ID
				ToolCallID:     call.ID,             // 工具调用ID
				Prompt:         params.Prompt,       // 提示词
				SessionTitle:   "New Agent Session", // 会话标题
			})
		}), nil
}
