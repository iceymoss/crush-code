package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	log.Println("=== MCP 客户端启动 ===")

	ctx := context.Background()

	// 创建客户端
	client := mcp.NewClient(&mcp.Implementation{
		Name:    "test-client",
		Version: "v1.0.0",
	}, nil)

	// 连接到服务器
	log.Println("正在启动并连接到服务器...")
	transport := &mcp.CommandTransport{
		Command: exec.Command("go", "run", "server.go"),
	}

	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		log.Fatalf("❌ 连接失败: %v", err)
	}
	defer session.Close()

	log.Println("✅ 成功连接到服务器！")
	time.Sleep(1 * time.Second)

	// 列出工具
	log.Println("\n📋 查询可用工具...")
	toolsResult, err := session.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		log.Fatalf("❌ 列出工具失败: %v", err)
	}

	fmt.Println("\n=== 可用工具列表 ===")
	for i, tool := range toolsResult.Tools {
		fmt.Printf("%d. %s - %s\n", i+1, tool.Name, tool.Description)
	}

	// 测试问候工具
	fmt.Println("\n=== 测试问候工具 ===")
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "greet",
		Arguments: map[string]any{"name": "小明"},
	})
	if err != nil {
		log.Printf("❌ 调用失败: %v\n", err)
	} else {
		printResult(result)
	}

	// 测试创建文件工具
	fmt.Println("\n=== 测试创建文件工具 ===")

	testFiles := []string{
		"test1.txt",
		"test2.log",
	}

	for _, filePath := range testFiles {
		fmt.Printf("\n→ 创建文件: %s\n", filePath)

		result, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name: "create_file",
			Arguments: map[string]any{
				"file_path": filePath,
			},
		})

		if err != nil {
			log.Printf("❌ 调用失败: %v\n", err)
			continue
		}

		printResult(result)
	}

	log.Println("\n=== 客户端演示完成 ===")
}

// 辅助函数：打印结果
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
				prettyJSON, _ := json.MarshalIndent(output, "", "  ")
				fmt.Printf("← 响应:\n%s\n", string(prettyJSON))
			} else {
				fmt.Printf("← 响应: %s\n", text.Text)
			}
		}
	}
}
