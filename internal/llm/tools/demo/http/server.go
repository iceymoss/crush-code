package main

import (
	"context"
	"fmt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"log"
	"net/http"
	"os/exec"
	"path/filepath"
)

// ============================================
// 工具 1: 问候工具
// ============================================

type GreetInput struct {
	// 要问候的人的名字
	Name string `json:"name" jsonschema:"required"`
}

type GreetOutput struct {
	// 返回的问候语
	Greeting string `json:"greeting"`
}

func SayHi(ctx context.Context, req *mcp.CallToolRequest, input GreetInput) (
	*mcp.CallToolResult,
	GreetOutput,
	error,
) {
	greeting := "你好，" + input.Name + "！欢迎使用 MCP HTTP 服务器！"
	log.Printf("收到请求: 问候 %s", input.Name)
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

func CreateFile(ctx context.Context, req *mcp.CallToolRequest, input CreateFileInput) (
	*mcp.CallToolResult,
	CreateFileOutput,
	error,
) {
	log.Printf("收到请求: 创建文件 %s", input.FilePath)

	cleanPath := filepath.Clean(input.FilePath)

	// 执行 touch 命令
	cmd := exec.CommandContext(ctx, "touch", cleanPath)
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

// ============================================
// 工具 3: 列出文件工具
// ============================================

type ListFilesInput struct {
	// 要列出的目录路径
	Directory string `json:"directory" jsonschema:"required"`
}

type ListFilesOutput struct {
	// 操作是否成功
	Success bool `json:"success"`
	// 文件列表
	Files []string `json:"files"`
	// 操作消息
	Message string `json:"message"`
}

func ListFiles(ctx context.Context, req *mcp.CallToolRequest, input ListFilesInput) (
	*mcp.CallToolResult,
	ListFilesOutput,
	error,
) {
	log.Printf("收到请求: 列出目录 %s", input.Directory)

	cleanPath := filepath.Clean(input.Directory)

	// 执行 ls 命令
	cmd := exec.CommandContext(ctx, "ls", "-1", cleanPath)
	output, err := cmd.CombinedOutput()

	if err != nil {
		errMsg := fmt.Sprintf("列出文件失败: %v, 输出: %s", err, string(output))
		log.Printf("错误: %s", errMsg)
		return nil, ListFilesOutput{
			Success: false,
			Files:   []string{},
			Message: errMsg,
		}, nil
	}

	// 解析输出
	files := []string{}
	if len(output) > 0 {
		files = append(files, string(output))
	}

	successMsg := fmt.Sprintf("成功列出目录: %s", cleanPath)
	log.Printf("成功: %s", successMsg)

	return nil, ListFilesOutput{
		Success: true,
		Files:   files,
		Message: successMsg,
	}, nil
}

// ============================================
// 主函数
// ============================================

func main() {
	log.Println("========================================")
	log.Println("    MCP HTTP SSE 服务器启动")
	log.Println("========================================")

	// 创建 MCP 服务器
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "http-tools-server",
		Version: "v1.0.0",
	}, nil)

	// 注册工具
	mcp.AddTool(server, &mcp.Tool{
		Name:        "greet",
		Description: "向指定的人发送问候",
	}, SayHi)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "create_file",
		Description: "使用 touch 命令创建一个文件",
	}, CreateFile)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_files",
		Description: "列出指定目录的文件",
	}, ListFiles)

	log.Println("\n已注册工具:")
	log.Println("  1. greet       - 问候工具")
	log.Println("  2. create_file - 创建文件工具")
	log.Println("  3. list_files  - 列出文件工具")

	// Create the streamable HTTP handler.
	handler := mcp.NewStreamableHTTPHandler(func(req *http.Request) *mcp.Server {
		return server
	}, nil)

	//handlerWithLogging := loggingHandler(handler)

	log.Printf("MCP server listening on %s", "127.0.0.1:8080")

	// Start the HTTP server with logging handler.
	if err := http.ListenAndServe("127.0.0.1:8080", handler); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

// responseWriter wraps http.ResponseWriter to capture the status code.
type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

func loggingHandler(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Println("Request:", r.Method, r.URL)
	})
}
