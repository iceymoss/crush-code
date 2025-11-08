package main

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"path/filepath"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ============================================
// 工具 1: 问候工具
// ============================================

// GreetInput 定义工具的输入参数
type GreetInput struct {
	// 要问候的人的名字
	Name string `json:"name" jsonschema:"required"`
}

// GreetOutput 定义工具的输出结果
type GreetOutput struct {
	// 返回的问候语
	Greeting string `json:"greeting"`
}

// SayHi 是一个简单的问候工具处理函数
func SayHi(ctx context.Context, req *mcp.CallToolRequest, input GreetInput) (*mcp.CallToolResult, GreetOutput, error) {
	// 构造问候语
	greeting := "你好，" + input.Name + "！欢迎使用 MCP Go SDK！"

	log.Printf("收到请求: 问候 %s", input.Name)

	// 返回结果
	return nil, GreetOutput{Greeting: greeting}, nil
}

// ============================================
// 工具 2: 创建文件工具
// ============================================

type CreateFileInput struct {
	// 要创建的文件路径
	FilePath string `json:"file_path" jsonschema:"required"`
}

type CreateFileOutput struct {
	// 操作是否成功
	Success bool `json:"success"`
	// 创建的文件路径
	FilePath string `json:"file_path"`
	// 操作消息
	Message string `json:"message"`
}

// CreateFile 使用 touch 命令创建文件
func CreateFile(ctx context.Context, req *mcp.CallToolRequest, input CreateFileInput) (
	*mcp.CallToolResult,
	CreateFileOutput,
	error,
) {
	log.Printf("收到请求: 创建文件 %s", input.FilePath)

	// 清理和验证文件路径
	cleanPath := filepath.Clean(input.FilePath)

	// 执行 touch 命令
	cmd := exec.CommandContext(ctx, "touch", cleanPath)

	// 执行命令
	output, err := cmd.CombinedOutput()
	if err != nil {
		errMsg := fmt.Sprintf("创建文件失败: %v, 输出: %s", err, string(output))
		log.Printf("错误: %s", errMsg)
		return nil, CreateFileOutput{
			Success:  false,
			FilePath: cleanPath,
			Message:  errMsg,
		}, nil
	}

	successMsg := fmt.Sprintf("成功创建文件: %s", cleanPath)
	log.Printf("成功: %s", successMsg)

	return nil, CreateFileOutput{
		Success:  true,
		FilePath: cleanPath,
		Message:  successMsg,
	}, nil
}

func main() {
	log.Println("启动 MCP 服务器...")

	// 1. 创建 MCP 服务器实例
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "file-tools-server",
		Version: "v1.0.0",
	}, nil)

	// 2. 添加问候工具
	mcp.AddTool(server, &mcp.Tool{
		Name:        "greet",
		Description: "向指定的人发送问候",
	}, SayHi)

	// 3. 添加创建文件工具
	mcp.AddTool(server, &mcp.Tool{
		Name:        "create_file",
		Description: "使用 touch 命令创建一个文件",
	}, CreateFile)

	log.Println("已注册工具:")
	log.Println("  - greet: 问候工具")
	log.Println("  - create_file: 创建文件工具")
	log.Println("服务器正在运行，等待客户端连接...")
	log.Println("(使用 Ctrl+C 停止服务器)")

	// 4. 在标准输入/输出上运行服务器
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatalf("服务器运行失败: %v", err)
	}
}
