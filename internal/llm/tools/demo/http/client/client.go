package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	log.Println("========================================")
	log.Println("    MCP HTTP 客户端启动")
	log.Println("========================================")

	url := "http://127.0.0.1:8080"
	ctx := context.Background()

	// Create the URL for the server.
	log.Printf("Connecting to MCP server at %s", url)

	// Create an MCP client.
	client := mcp.NewClient(&mcp.Implementation{
		Name:    "http-tools-server",
		Version: "v1.0.0",
	}, nil)

	// Connect to the server.
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: url}, nil)
	if err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}
	defer session.Close()

	log.Printf("Connected to server (session ID: %s)", session.ID())

	// 测试工具列表
	// First, list available tools.
	log.Println("Listing available tools...")
	toolsResult, err := session.ListTools(ctx, nil)
	if err != nil {
		log.Fatalf("Failed to list tools: %v", err)
	}

	for _, tool := range toolsResult.Tools {
		log.Printf("工具 - %s: %s\n", tool.Name, tool.Description)
	}

	// 2. 测试问候工具
	fmt.Println("\n=== 测试 1: 问候工具 ===")
	testGreet(ctx, session, []string{"小明", "张三", "李四"})

	// 3. 测试创建文件工具
	fmt.Println("\n=== 测试 2: 创建文件工具 ===")
	testCreateFile(ctx, session, []string{
		"test_http_1.txt",
		"test_http_2.log",
		"demo.md",
	})

	// 4. 测试列出文件工具
	fmt.Println("\n=== 测试 3: 列出文件工具 ===")
	testListFiles(ctx, session, ".")

	log.Println("\n========================================")
	log.Println("    客户端演示完成")
	log.Println("========================================")
}

// 测试问候工具
func testGreet(ctx context.Context, session *mcp.ClientSession, names []string) {
	for _, name := range names {
		fmt.Printf("\n→ 调用 greet(name=\"%s\")\n", name)

		result, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name:      "greet",
			Arguments: map[string]any{"name": name},
		})

		if err != nil {
			log.Printf("❌ 调用失败: %v\n", err)
			continue
		}

		printResult(result)
	}
}

// 测试创建文件工具
func testCreateFile(ctx context.Context, session *mcp.ClientSession, files []string) {
	for _, file := range files {
		fmt.Printf("\n→ 调用 create_file(file_path=\"%s\")\n", file)

		result, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name:      "create_file",
			Arguments: map[string]any{"file_path": file},
		})

		if err != nil {
			log.Printf("❌ 调用失败: %v\n", err)
			continue
		}

		printResult(result)
	}
}

// 测试列出文件工具
func testListFiles(ctx context.Context, session *mcp.ClientSession, dir string) {
	fmt.Printf("\n→ 调用 list_files(directory=\"%s\")\n", dir)

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "list_files",
		Arguments: map[string]any{"directory": dir},
	})

	if err != nil {
		log.Printf("❌ 调用失败: %v\n", err)
		return
	}

	printResult(result)
}

// 打印结果的辅助函数
func printResult(result *mcp.CallToolResult) {
	if result.IsError {
		fmt.Println("❌ 工具返回错误")
		return
	}

	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			// 尝试解析为 JSON 并美化输出
			var output map[string]interface{}
			if err := json.Unmarshal([]byte(text.Text), &output); err == nil {
				prettyJSON, _ := json.MarshalIndent(output, "  ", "  ")
				fmt.Printf("← 响应:\n  %s\n", string(prettyJSON))
			} else {
				fmt.Printf("← 响应: %s\n", text.Text)
			}
		}
	}
}
