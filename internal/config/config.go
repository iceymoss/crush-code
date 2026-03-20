package config

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/env"
	"github.com/charmbracelet/crush/internal/oauth"
	"github.com/charmbracelet/crush/internal/oauth/copilot"
	"github.com/invopop/jsonschema"
)

// 默认的配置文件名称
const (
	appName              = "crush"
	defaultDataDirectory = ".crush"
	defaultInitializeAs  = "AGENTS.md"
)

// 默认的上下文路径
var defaultContextPaths = []string{
	".github/copilot-instructions.md",
	".cursorrules",
	".cursor/rules/",
	"CLAUDE.md",
	"CLAUDE.local.md",
	"GEMINI.md",
	"gemini.md",
	"crush.md",
	"crush.local.md",
	"Crush.md",
	"Crush.local.md",
	"CRUSH.md",
	"CRUSH.local.md",
	"AGENTS.md",
	"agents.md",
	"Agents.md",
}

// SelectedModelType 选择的模型类型。
type SelectedModelType string

// String 返回 [SelectedModelType] 的字符串表示.
func (s SelectedModelType) String() string {
	return string(s)
}

// SelectedModelType 选择模型的类型，large或者small
const (
	SelectedModelTypeLarge SelectedModelType = "large"
	SelectedModelTypeSmall SelectedModelType = "small"
)

// agent的类型，coder或者task
const (
	AgentCoder string = "coder"
	AgentTask  string = "task"
)

// SelectedModel 模型配置
// 用于定义在应用中被选中的大语言模型及其相关的生成参数
type SelectedModel struct {
	// 提供者 API 使用的模型具体 ID（必填项）。
	// 例如："gpt-4o", "claude-3-5-sonnet-20241022", "deepseek-chat"
	Model string `json:"model" jsonschema:"required,description=提供者API使用的型号ID，example=gpt-4o"`

	// 模型提供者标识符（必填项）。
	// 用于路由到对应的 API 客户端实现，并与配置文件中的密钥匹配。
	// 例如："openai", "anthropic", "deepseek"
	Provider string `json:"provider" jsonschema:"required,description=与提供者配置中密钥匹配的模型提供者ID，example=openai"`

	// 推理力度（仅限 OpenAI 的 o1/o3 等推理模型使用）。
	// 控制模型在给出最终答案前“思考”的时间长短。
	ReasoningEffort string `json:"reasoning_effort,omitempty" jsonschema:"description=支持 OpenAI 模型的推理水平,enum=low,enum=medium,enum=high"`

	// 启用扩展思考模式（主要用于 Anthropic 的 Claude 3.7 Sonnet 等模型）。
	// 开启后，模型会在返回正文前进行一段隐式的推理演算，提升复杂逻辑处理能力。
	Think bool `json:"think,omitempty" jsonschema:"description=为支持推理的 anthropic 模型启用思维模式"`

	// 覆盖默认的通用模型配置参数：

	// 最大 Token 数。
	// 限制模型单次回复生成的最大长度，防止过度消耗额度或产生无限循环（例如：4096）。
	MaxTokens int64 `json:"max_tokens,omitempty" jsonschema:"description=Maximum number of tokens for model responses,maximum=200000,example=4096"`

	// 采样温度 (0.0 ~ 1.0+)。
	// 控制输出的随机性和创造力。值越高（如 0.8）回答越多变；值越低（如 0.2）回答越严谨、确定。代码补全通常设为极低值。
	Temperature *float64 `json:"temperature,omitempty" jsonschema:"description=Sampling temperature,minimum=0,maximum=1,example=0.7"`

	// 核采样参数 (Top-p, 0.0 ~ 1.0)。
	// 另一种控制随机性的方式，限制模型只在概率累计达到 p 的候选词中选择。
	// 官方通常建议：Temperature 和 TopP 修改其中一个即可，不要同时修改。
	TopP *float64 `json:"top_p,omitempty" jsonschema:"description=Top-p (nucleus) sampling parameter,minimum=0,maximum=1,example=0.9"`

	// Top-k 采样参数。
	// 限制模型在生成每个词时，只考虑概率排名前 K 的候选词（部分开源模型或平台特有参数）。
	TopK *int64 `json:"top_k,omitempty" jsonschema:"description=Top-k sampling parameter"`

	// 频率惩罚系数。
	// 根据词汇在已生成文本中出现的频率进行惩罚。正值可以减少模型一直像复读机一样重复特定词汇的概率。
	FrequencyPenalty *float64 `json:"frequency_penalty,omitempty" jsonschema:"description=Frequency penalty to reduce repetition"`

	// 存在惩罚系数。
	// 只要词汇在已生成文本中出现过，无论频率多低都进行惩罚。正值可以鼓励模型主动转移到新的话题。
	PresencePenalty *float64 `json:"presence_penalty,omitempty" jsonschema:"description=Presence penalty to increase topic diversity"`

	// 提供者专有配置字典。
	// 极其重要的扩展字段！用于动态传入某些模型独有的、未被抽象到上述标准字段中的特殊参数（例如特定的安全过滤级别、工具调用强制策略等）。
	ProviderOptions map[string]any `json:"provider_options,omitempty" jsonschema:"description=Additional provider-specific options for the model"`
}

// ProviderConfig 大模型提供商配置
// 用于定义和管理一个独立的大模型服务提供方（如 OpenAI官方、DeepSeek、第三方中转API、或是本地的 Ollama）
type ProviderConfig struct {
	// 提供商的全局唯一标识符。
	// 在系统内部路由请求时使用。例如："openai", "deepseek", "local-ollama"
	ID string `json:"id,omitempty" jsonschema:"description=Unique identifier for the provider,example=openai"`

	// 提供商的展示名称。
	// 仅用于在 TUI 终端界面（如选择模型的下拉列表）中向用户展示。例如："OpenAI", "本地 Ollama 服务"
	Name string `json:"name,omitempty" jsonschema:"description=Human-readable name for the provider,example=OpenAI"`

	// API 请求的基础地址（极其重要！）。
	// 如果你使用中转代理、本地模型或兼容 OpenAI 格式的其他模型（如 DeepSeek），就需要改这里。
	// 例如："https://api.deepseek.com/v1" 或 "http://localhost:11434/v1"
	BaseURL string `json:"base_url,omitempty" jsonschema:"description=Base URL for the provider's API,format=uri,example=https://api.openai.com/v1"`

	// 提供商的协议类型（决定了底层的 HTTP 请求体会如何被序列化）。
	// 默认为 openai。常见选项包括 "openai", "openai-compat" (兼容模式), "anthropic", "gemini" 等。
	// 即使你用的是 DeepSeek，只要它兼容 OpenAI 格式，这里也填 "openai-compat" 或 "openai"。
	Type catwalk.Type `json:"type,omitempty" jsonschema:"description=Provider type that determines the API format,enum=openai,enum=openai-compat,enum=anthropic,enum=gemini,enum=azure,enum=vertexai,default=openai"`

	// 用于身份验证的 API Key。
	// 实际发起请求时携带的鉴权密钥。
	APIKey string `json:"api_key,omitempty" jsonschema:"description=API key for authentication with the provider,example=$OPENAI_API_KEY"`

	// API Key 的原始模板映射（不会被序列化到 JSON 配置文件中，注意 `json:"-"`）。
	// 比如用户在配置里写的是 `$OPENAI_API_KEY`，这里存的就是这个占位符，
	// 当鉴权失败需要重新读取环境变量时，系统会回退使用这个模板重新解析。
	APIKeyTemplate string `json:"-"`

	// OAuth2 令牌。
	// 专门为那些不支持简单 API Key，而要求标准 OAuth2 鉴权的企业级提供商准备（例如 Google Cloud Vertex AI 或某些企业级 Azure 部署）。
	OAuthToken *oauth.Token `json:"oauth,omitempty" jsonschema:"description=OAuth2 token for authentication with the provider"`

	// 禁用开关。
	// 设为 true 时，该提供商及其下的所有模型将不会在终端的可用列表中显示。
	Disable bool `json:"disable,omitempty" jsonschema:"description=Whether this provider is disabled,default=false"`

	// 自定义系统提示词前缀。
	// 可以在发给该提供商的所有 System Prompt 最前面强行插入一段话。
	// 非常适合用来给特定的模型做“底层洗脑”或规避某些厂商特有的格式要求。
	SystemPromptPrefix string `json:"system_prompt_prefix,omitempty" jsonschema:"description=Custom prefix to add to system prompts for this provider"`

	// 附加 HTTP 请求头。
	// 极其有用的扩展字段！比如你使用 OpenRouter 这种聚合 API 时，官方要求必须在 Header 里带上 `HTTP-Referer` 和 `X-Title`，就可以配在这里。
	ExtraHeaders map[string]string `json:"extra_headers,omitempty" jsonschema:"description=Additional HTTP headers to send with requests"`

	// 附加的 JSON Body 字段。
	// 仅在 `openai-compat` 兼容模式下生效。比如某些本地跑的特殊模型需要你在 JSON 体里额外传一个 `{"stream_options": {"include_usage": true}}`。
	ExtraBody map[string]any `json:"extra_body,omitempty" jsonschema:"description=Additional fields to include in request bodies, only works with openai-compatible providers"`

	// 提供商特有配置选项。
	// 用于存储特定厂商才有的参数，比如 Azure OpenAI 特有的部署名 (Deployment Name) 或者 API Version。
	ProviderOptions map[string]any `json:"provider_options,omitempty" jsonschema:"description=Additional provider-specific options for this provider"`

	// 运行时附加参数（不会持久化到配置 JSON 中，注意 `json:"-"`）。
	// 用于在程序运行期间，各个组件之间传递一些临时的内部状态或参数。
	ExtraParams map[string]string `json:"-"`

	// 该提供商支持的模型列表。
	// 记录了这个 Provider 下面挂载了哪些具体的模型（比如挂载了 deepseek-chat 和 deepseek-coder）。
	// 通常可以通过厂商的 `/v1/models` 接口动态拉取并缓存在这里。
	Models []catwalk.Model `json:"models,omitempty" jsonschema:"description=List of models available from this provider"`
}

// ToProvider 将[ProviderConfig]转换为[catwalk.Provider].
func (pc *ProviderConfig) ToProvider() catwalk.Provider {
	// Convert config provider to provider.Provider format
	provider := catwalk.Provider{
		Name:   pc.Name,
		ID:     catwalk.InferenceProvider(pc.ID),
		Models: make([]catwalk.Model, len(pc.Models)),
	}

	// Convert models
	for i, model := range pc.Models {
		provider.Models[i] = catwalk.Model{
			ID:                     model.ID,
			Name:                   model.Name,
			CostPer1MIn:            model.CostPer1MIn,
			CostPer1MOut:           model.CostPer1MOut,
			CostPer1MInCached:      model.CostPer1MInCached,
			CostPer1MOutCached:     model.CostPer1MOutCached,
			ContextWindow:          model.ContextWindow,
			DefaultMaxTokens:       model.DefaultMaxTokens,
			CanReason:              model.CanReason,
			ReasoningLevels:        model.ReasoningLevels,
			DefaultReasoningEffort: model.DefaultReasoningEffort,
			SupportsImages:         model.SupportsImages,
		}
	}

	return provider
}

// SetupGitHubCopilot 设置 GitHub Copilot 的请求头。
func (pc *ProviderConfig) SetupGitHubCopilot() {
	maps.Copy(pc.ExtraHeaders, copilot.Headers())
}

// MCPType 定义了 MCP 服务器与客户端（也就是 crush）之间的通信通道类型。
type MCPType string

const (
	// MCPStdio MCPStdio：标准输入输出模式（最常用）。
	// crush 会在后台直接启动一个本地子进程（比如运行一段 Node.js 或 Python 脚本），
	// 然后通过系统的 stdin/stdout 与这个进程进行 JSON-RPC 通信。
	MCPStdio MCPType = "stdio"

	// MCPSSE MCPSSE：Server-Sent Events 模式。
	// crush 会通过网络连接到一个远程或本地的 SSE 服务器来保持长连接收发消息。
	MCPSSE MCPType = "sse"

	// MCPHttp MCPHttp：标准 HTTP 模式。
	// crush 通过普通的 HTTP POST 请求与 MCP 服务器通信。
	MCPHttp MCPType = "http"
)

// MCPConfig 定义了单个 MCP 服务器的完整连接配置。
// 用户通常会在 `~/.config/crush/crush.json` 里配置这个数组，给 AI 赋予各种超能力。
//
//   - 例如：
//
//     "mcp_servers": {
//     "my_local_redis": {
//     "type": "stdio",
//     "command": "npx",
//     "args": ["-y", "@modelcontextprotocol/server-redis", "redis://localhost:6379"],
//     "timeout": 30
//     }
type MCPConfig struct {
	// 【针对 Stdio 模式】
	// 要执行的可执行文件命令。比如你想用官方的 SQLite MCP 插件，这里可能填 "npx" 或 "python"。
	Command string `json:"command,omitempty" jsonschema:"description=Command to execute for stdio MCP servers,example=npx"`

	// 传递给子进程的环境变量。
	// 非常重要！比如某个 MCP 插件需要访问数据库，你可以在这里传入 DB_PASSWORD。
	Env map[string]string `json:"env,omitempty" jsonschema:"description=Environment variables to set for the MCP server"`

	// 传递给命令的参数列表。
	// 接着上面的例子，这里可能是 ["-y", "@modelcontextprotocol/server-sqlite", "--db", "./my.db"]。
	Args []string `json:"args,omitempty" jsonschema:"description=Arguments to pass to the MCP server command"`

	// 【通用选项】
	// 也就是上面定义的 stdio, sse, 或 http，决定了怎么连这个插件。
	Type MCPType `json:"type" jsonschema:"required,description=Type of MCP connection,enum=stdio,enum=sse,enum=http,default=stdio"`

	// 【针对 SSE / HTTP 模式】
	// 如果插件是一个跑在 Docker 里或者远端的网络服务，这里填它的 API 地址。
	URL string `json:"url,omitempty" jsonschema:"description=URL for HTTP or SSE MCP servers,format=uri,example=http://localhost:3000/mcp"`

	// 是否全局禁用这个 MCP 服务器。如果设为 true，crush 启动时就不会加载它。
	Disabled bool `json:"disabled,omitempty" jsonschema:"description=Whether this MCP server is disabled,default=false"`

	// 颗粒度更细的权限控制：禁用该服务器提供的特定工具。
	// 一个 MCP 服务器通常会提供多个 Tool。比如一个 GitHub MCP 可能提供 "read_repo" 和 "create_issue"。
	// 如果你怕 AI 乱建 issue，可以在这里把 "create_issue" 填进去禁用掉。
	DisabledTools []string `json:"disabled_tools,omitempty" jsonschema:"description=List of tools from this MCP server to disable,example=get-library-doc"`

	// 连接或执行工具的超时时间（秒）。
	// 防止某个外部 MCP 插件卡死导致整个 crush 界面失去响应。
	Timeout int `json:"timeout,omitempty" jsonschema:"description=Timeout in seconds for MCP server connections,default=15,example=30,example=60,example=120"`

	// 【针对 SSE / HTTP 模式】
	// 请求网络 MCP 服务时携带的自定义 HTTP 头（通常用于存放 Bearer Token 鉴权）。
	// TODO ：未来可能会支持直接从环境变量里读，避免把密钥明文写在 JSON 里。
	Headers map[string]string `json:"headers,omitempty" jsonschema:"description=HTTP headers for HTTP/SSE MCP servers"`
}

// LSPConfig 定义了语言服务器 (Language Server) 的启动和连接配置。
// AI Agent 通过加载这个配置，获得代码的语法树解析、跳转定义、查找引用等 IDE 级别的超能力。
type LSPConfig struct {
	// 禁用开关。
	// 如果某个语言的 LSP 太吃内存（比如某些庞大的 Java/C++ 项目），可以在这里设为 true 把它关掉。
	Disabled bool `json:"disabled,omitempty" jsonschema:"description=Whether this LSP server is disabled,default=false"`

	// 启动 LSP 服务器的核心命令。
	// 比如：写 Go 填 "gopls"，写 Rust 填 "rust-analyzer"，写前端填 "typescript-language-server"。
	Command string `json:"command,omitempty" jsonschema:"description=Command to execute for the LSP server,example=gopls"`

	// 传递给 LSP 进程的命令行参数。
	// 比如有的 LSP 需要传 ["--stdio"] 来强制它使用标准输入输出与 crush 通信，而不是开一个 Web 端口。
	Args []string `json:"args,omitempty" jsonschema:"description=Arguments to pass to the LSP server command"`

	// LSP 进程的专属环境变量。
	// 比如为了让 gopls 能正确拉取私有仓库，你可能需要在这里注入 "GOPRIVATE": "github.com/myorg/*"。
	Env map[string]string `json:"env,omitempty" jsonschema:"description=Environment variables to set to the LSP server command"`

	// 监听的文件后缀类型。
	// 当 crush 发现你正在跟它聊或者修改这些后缀的文件时，才会唤醒对应的 LSP。
	// 比如：["go", "mod", "work"] 或者 ["rs"]。
	FileTypes []string `json:"filetypes,omitempty" jsonschema:"description=File types this LSP server handles,example=go,example=mod,example=rs,example=c,example=js,example=ts"`

	// 项目根目录的标志性文件（极其关键！）。
	// LSP 必须知道你的项目是从哪一层文件夹开始的，才能正确解析相对路径和依赖。
	// 比如找 Go 项目根目录看 "go.mod"，找 Rust 看 "Cargo.toml"，找 Node 项目看 "package.json"。
	// crush 会不断向父目录递归寻找这些文件来确定 Root Path。
	RootMarkers []string `json:"root_markers,omitempty" jsonschema:"description=Files or directories that indicate the project root,example=go.mod,example=package.json,example=Cargo.toml"`

	// LSP 初始化参数 (InitializationOptions)。
	// 这是在建立连接的第一条 `initialize` 请求中发给 LSP 的。
	// 包含一些高度定制化的底层参数，比如告诉 gopls 开启某些还在实验阶段的代码分析特性。
	InitOptions map[string]any `json:"init_options,omitempty" jsonschema:"description=Initialization options passed to the LSP server during initialize request"`

	// 工作区专属设置 (Workspace Configuration / Settings)。
	// 对应 VS Code 里的 `settings.json` 中传给扩展的配置。
	// 比如在这里配置代码格式化工具的单行最大长度，或者是否在保存时自动插入 import。
	Options map[string]any `json:"options,omitempty" jsonschema:"description=LSP server-specific settings passed during initialization"`

	// 初始化超时时间（秒）。
	// 像大型项目启动 LSP 可能会狂吃 CPU 建索引，如果超过这个时间还没就绪，crush 就会放弃它，防止把终端卡死。
	Timeout int `json:"timeout,omitempty" jsonschema:"description=Timeout in seconds for LSP server initialization,default=30,example=60,example=120"`
}

// TUIOptions 定义了终端用户界面 (TUI) 的个性化显示和交互配置。
// 用户可以在 ~/.config/crush/crush.json 中修改这些选项来定制自己的界面。
type TUIOptions struct {
	// 紧凑模式。
	// 如果设为 true，crush 会减少气泡、列表和边框周围的空白边距（Padding/Margin），
	// 非常适合屏幕较小或者喜欢满屏都是高密度代码块的硬核开发者。
	CompactMode bool `json:"compact_mode,omitempty" jsonschema:"description=Enable compact mode for the TUI interface,default=false"`

	// 代码差异（Diff）的显示模式。
	// 当 crush 帮你重构了一段代码时，它会展示修改前后的对比。
	// 可选值："unified"（类似 GitHub 默认的上下单栏对比）或 "split"（左右双栏对比）。
	DiffMode string `json:"diff_mode,omitempty" jsonschema:"description=Diff mode for the TUI interface,enum=unified,enum=split"`

	// （开发者留下的 TODO：未来可以在这里加自定义主题颜色等配置）
	// Here we can add themes later or any TUI related options

	// 路径补全和文件列表 UI 的配置。
	// 注意这里用了 `omitzero`（通常在处理结构体零值时比 omitempty 更严谨，防止空结构体被序列化）。
	Completions Completions `json:"completions,omitzero" jsonschema:"description=Completions UI options"`

	// 透明背景支持。
	// 注意这里用的是指针 `*bool`！这非常关键：
	// 如果不用指针，当值为 false 时，你无法区分是“用户明确想要关闭透明（false）”，还是“用户根本没配这个字段（默认零值 false）”。
	// 开启后，crush 会放弃绘制自己的背景色，完美融入你终端自带的毛玻璃或自定义背景图中。
	Transparent *bool `json:"transparent,omitempty" jsonschema:"description=Enable transparent background for the TUI interface,default=false"`
}

// Completions 定义了输入框在做路径自动补全（或者底层使用 ls 工具读取本地文件时）的性能边界。
// 为什么需要这个？因为如果在一个包含 `node_modules` 或 `.git` 的巨型项目根目录下触发补全，
// 不加限制地递归读取目录会直接把终端卡死甚至内存溢出。
type Completions struct {
	// 最大递归深度。
	// 例如设为 1，就只会提示当前目录下的文件，不会钻到子目录里去。
	MaxDepth *int `json:"max_depth,omitempty" jsonschema:"description=Maximum depth for the ls tool,default=0,example=10"`

	// 最大返回条目数。
	// 限制补全列表里最多显示多少个文件，防止渲染上千个选项导致 TUI 引擎掉帧（默认限制为 1000）。
	MaxItems *int `json:"max_items,omitempty" jsonschema:"description=Maximum number of items to return for the ls tool,default=1000,example=100"`
}

// Limits 是一个非常优雅的辅助方法，用于安全地提取指针的值。
// 在业务逻辑（比如实际去读文件的那个方法）里，直接调 c.Limits() 就能拿到安全的 int 值。
func (c Completions) Limits() (depth, items int) {
	// ptrValOr 是一个泛型工具函数（在这个包的其他地方定义的）。
	// 逻辑是：如果 c.MaxDepth 是 nil，就返回默认值 0；如果不是 nil，就解引用返回 *c.MaxDepth。
	// 这样就完美避免了可怕的 "invalid memory address or nil pointer dereference" 运行崩溃。
	return ptrValOr(c.MaxDepth, 0), ptrValOr(c.MaxItems, 0)
}

// Permissions 定义了 AI 助手调用外部工具时的安全和权限策略。
// 默认情况下，如果 AI 想在你电脑上跑一个 `rm -rf` 或者发一个网络请求，
// 终端界面会弹出一个提示框，问你“是否允许”。这段配置就是用来定制这个行为的。
type Permissions struct {
	// 自动放行名单 (白名单)。
	// 列表里的工具在被 AI 调用时，不再需要你的手动确认。
	// 比如：你觉得 `view` (只读查看文件) 很安全，就可以加进来；
	// 但千万别把 `bash` (执行任意终端命令) 随便加进来，除非你极其信任它。
	AllowedTools []string `json:"allowed_tools,omitempty" jsonschema:"description=List of tools that don't require permission prompts,example=bash,example=view"`

	// YOLO 模式开关 (You Only Live Once - 你只活一次 / 莽夫模式)。
	// 注意这个 `json:"-"`！它不会被写进配置文件。
	// 还记得我们最初看 main.go 时支持的那个 `crush --yolo` 命令行参数吗？
	// 如果运行时带了这个参数，这个字段就会变成 true。此时 AI 拥有最高神权，
	// 调用任何工具、执行任何删库跑路的命令都不再弹窗问你，直接执行！（极其危险，谨慎使用）
	SkipRequests bool `json:"-"`
}

// TrailerStyle 定义了 Git 提交信息的尾部标签风格。
type TrailerStyle string

const (
	// TrailerStyleNone TrailerStyleNone：深藏功与名，不加任何 AI 参与的标签。
	TrailerStyleNone TrailerStyle = "none"

	// TrailerStyleCoAuthoredBy TrailerStyleCoAuthoredBy：联合署名。
	// 会在 Git Commit 最后加上一行 `Co-authored-by: Crush <...>`。
	// GitHub 识别到这行字后，会在 Commit 记录里同时显示你和 AI 的头像！
	TrailerStyleCoAuthoredBy TrailerStyle = "co-authored-by"

	// TrailerStyleAssistedBy TrailerStyleAssistedBy：辅助署名。
	// 相对低调一点，表示 AI 只是打了下手。
	TrailerStyleAssistedBy TrailerStyle = "assisted-by"
)

// Attribution 定义了当 AI 自动为你生成 Git Commit、PR (Pull Request) 或 Issue 时，
// 它该如何给自己“邀功”（署名策略）。
type Attribution struct {
	// 具体的 Git Trailer 风格（默认是 assisted-by 辅助模式）。
	TrailerStyle TrailerStyle `json:"trailer_style,omitempty" jsonschema:"description=Style of attribution trailer to add to commits,enum=none,enum=co-authored-by,enum=assisted-by,default=assisted-by"`

	// [已废弃] 旧版本的联合署名开关。
	// 为了向前兼容老版本的配置文件而保留，但建议使用上面的 TrailerStyle。
	CoAuthoredBy *bool `json:"co_authored_by,omitempty" jsonschema:"description=Deprecated: use trailer_style instead"`

	// 生成标记开关。
	// 如果为 true，AI 在帮你写 GitHub PR 描述或者 Issue 内容时，
	// 会在底部悄悄加上一句类似于 "🤖 Generated with Crush" 的小尾巴。
	GeneratedWith bool `json:"generated_with,omitempty" jsonschema:"description=Add Generated with Crush line to commit messages and issues and PRs,default=true"`
}

// JSONSchemaExtend 是给 jsonschema 库用的一个高级钩子方法（Hook）。
// crush 的配置文件 (crush.json) 支持在 VS Code 等编辑器里提供自动补全和校验。
// 这个方法会在程序生成 JSON Schema 文档时动态介入。
func (Attribution) JSONSchemaExtend(schema *jsonschema.Schema) {
	if schema.Properties != nil {
		// 它去查找刚刚上面那个 "co_authored_by" 字段
		if prop, ok := schema.Properties.Get("co_authored_by"); ok {
			// 然后在 Schema 元数据里把它硬性标记为 "已废弃" (Deprecated = true)。
			// 这样，当你在 VS Code 里编辑 ~/.config/crush/crush.json 时，
			// 如果你写了 `co_authored_by: true`，编辑器不仅会给你画一条删除线（Strikethrough），
			// 还会提示你：“老兄，这个字段废弃了，请改用 trailer_style 吧！”
			prop.Deprecated = true
		}
	}
}

// Options 定义了 crush 应用运行时的全局偏好设置。
// 它掌管了文件系统交互、调试输出、上下文管理以及 UI 行为。
type Options struct {
	// 上下文指令文件路径。
	// 类似于 Cursor 编辑器里的 `.cursorrules`。
	// AI 每次回答你的问题前，都会偷偷先去读这些文件里的规则（比如："本项目只准用 Go 1.22 的新特性" 或 "不要用 GORM，写原生 SQL"）。
	// 让 AI 彻底融入你的团队代码规范。
	ContextPaths []string `json:"context_paths,omitempty" jsonschema:"description=Paths to files containing context information for the AI,example=.cursorrules,example=CRUSH.md"`

	// Agent 技能包目录。
	// crush 允许你编写可复用的 "Skills"（技能）。比如你写了一个专门教 AI 如何编写特定的 Makefile 的 prompt，
	// 放在这些文件夹里，AI 就能在对话中按需加载这些技能。
	SkillsPaths []string `json:"skills_paths,omitempty" jsonschema:"description=Paths to directories containing Agent Skills (folders with SKILL.md files),example=~/.config/crush/skills,example=./skills"`

	// TUI (终端界面) 的外观配置。
	TUI *TUIOptions `json:"tui,omitempty" jsonschema:"description=Terminal user interface options"`

	// 全局调试日志开关。
	// 开启后，会在 ~/.config/crush/crush.log 里疯狂输出底层网络请求、Pub/Sub 事件流，方便你排查 bug。
	Debug bool `json:"debug,omitempty" jsonschema:"description=Enable debug logging,default=false"`

	// LSP 专属调试日志开关。
	// 因为 LSP (语言服务器，如 gopls) 互相发 JSON-RPC 消息极其频繁，和普通业务日志混在一起会爆炸，
	// 所以单独提出来一个开关，专门排查“AI 为什么读不到这个 Go 函数定义”的问题。
	DebugLSP bool `json:"debug_lsp,omitempty" jsonschema:"description=Enable debug logging for LSP servers,default=false"`

	// 禁用自动对话总结（极其重要的 LLM 资源管理配置！）。
	// 【背景】：大模型的上下文窗口（Context Window）是有限的，且 tokens 越长越贵。
	// crush 默认会在你聊了几十个回合后，偷偷在后台调一个便宜的模型把前面的废话总结成一小段核心要点，从而释放 Token 空间。
	// 如果你财大气粗（或者用本地免费的 Ollama），或者绝对不能丢失任何对话细节，可以设为 true 关掉这个机制。
	DisableAutoSummarize bool `json:"disable_auto_summarize,omitempty" jsonschema:"description=Disable automatic conversation summarization,default=false"`

	// 本地数据存储目录。
	// 默认是 `.crush`。这是相对你当前所在的“项目根目录”的。
	// 里面会存放 SQLite 数据库（存你的聊天记录）、缓存等。这也是为什么你在不同的项目目录里敲 `crush`，看到的历史对话是分开的。
	DataDirectory string `json:"data_directory,omitempty" jsonschema:"description=Directory for storing application data (relative to working directory),default=.crush,example=.crush"`

	// 禁用内置工具。
	// crush 自带了一些原生的强大工具（比如执行 `bash`，搜索代码库 `sourcegraph`）。
	// 如果你在公司服务器上跑，出于安全考虑，可以直接在这里把高危工具阉割掉。
	DisabledTools []string `json:"disabled_tools,omitempty" jsonschema:"description=List of built-in tools to disable and hide from the agent,example=bash,example=sourcegraph"`

	// 禁用提供商模型列表自动更新。
	// 默认情况下，crush 会定时去调 API (比如 OpenAI 的 /v1/models) 拉取最新的模型列表。
	DisableProviderAutoUpdate bool `json:"disable_provider_auto_update,omitempty" jsonschema:"description=Disable providers auto-update,default=false"`

	// 禁用默认提供商兜底。
	// 如果开启，crush 将变成一个绝对纯粹的“白板”。它会无视代码里硬编码的 OpenAI/Anthropic 默认配置，
	// 完全只认你在 `crush.json` 里一行行敲上去的 Provider。非常适合企业级内网/Airgap（物理隔离）环境。
	DisableDefaultProviders bool `json:"disable_default_providers,omitempty" jsonschema:"description=Ignore all default/embedded providers. When enabled, providers must be fully specified in the config file with base_url, models, and api_key - no merging with defaults occurs,default=false"`

	// Git 自动提交的署名配置。
	Attribution *Attribution `json:"attribution,omitempty" jsonschema:"description=Attribution settings for generated content"`

	// 禁用遥测/数据统计。
	// CLI 工具常见的隐私开关。如果你不希望给官方发崩溃日志或者匿名使用频次统计，设为 true。
	DisableMetrics bool `json:"disable_metrics,omitempty" jsonschema:"description=Disable sending metrics,default=false"`

	// 项目初始化脚手架上下文文件名。
	// 当你第一次在一个空项目里运行类似 `crush init` 的命令时，它会自动帮你生成一个 Markdown 规则文件。
	// 这里决定了那个文件叫什么名字（默认生成 `AGENTS.md`）。
	InitializeAs string `json:"initialize_as,omitempty" jsonschema:"description=Name of the context file to create/update during project initialization,default=AGENTS.md,example=AGENTS.md,example=CRUSH.md,example=CLAUDE.md,example=docs/LLMs.md"`

	// 零配置自动 LSP 嗅探。
	// 注意这里又是 `*bool`！默认开启（true）。
	// 开启后，哪怕你什么都没配，crush 只要一看到你目录里有个 `go.mod`，就会极其智能地在后台帮你把 `gopls` 拉起来。
	AutoLSP *bool `json:"auto_lsp,omitempty" jsonschema:"description=Automatically setup LSPs based on root markers,default=true"`

	// 系统级长耗时进度条支持。
	// 对于支持 ANSI 高级进度条转义码的现代终端（如 Windows Terminal），允许其在屏幕最下方显示原生的 Loading 特效。
	Progress *bool `json:"progress,omitempty" jsonschema:"description=Show indeterminate progress updates during long operations,default=true"`

	// 禁用桌面系统通知。
	// 如果 AI 帮你在后台跑了一个耗时 5 分钟的超大型代码重构，搞定之后它通常会调用 MacOS/Windows 的系统通知弹个窗提醒你“搞定了”。
	// 如果你嫌吵，可以在这里关掉。
	DisableNotifications bool `json:"disable_notifications,omitempty" jsonschema:"description=Disable desktop notifications,default=false"`
}

// MCPs 定义了一个键值对集合。
// 在你的 ~/.config/crush/crush.json 里，它长这样：
//
//	"mcp_servers": {
//	   "github": { "command": "npx", ... },
//	   "sqlite": { "command": "npx", ... }
//	}
type MCPs map[string]MCPConfig

// MCP 是一个扁平化的结构体。
// 它的作用是把上面 map 里的 Key（比如 "github"）提取出来，塞进 Name 字段里，
// 从而把 map 的键值对转换成一个单体对象，方便放进切片（Slice）里。
type MCP struct {
	Name string    `json:"name"`
	MCP  MCPConfig `json:"mcp"`
}

// Sorted 是一个极其常见的 Go 语言 UI 渲染模式 (Map to Sorted Slice)。
// Go 语言里的 map 遍历是绝对无序的（官方刻意为之）。如果你直接遍历 map 渲染到 TUI 界面上，
// 每次启动 crush，插件列表的顺序都会变来变去，用户体验极差。
// 解法：把它转成切片，并按字母排序！
func (m MCPs) Sorted() []MCP {
	// 预分配切片容量，避免 append 时频繁触发底层数组扩容，提升性能
	sorted := make([]MCP, 0, len(m))

	// 1. 将 map 展平并塞入切片
	for k, v := range m {
		sorted = append(sorted, MCP{
			Name: k,
			MCP:  v,
		})
	}

	// 2. 使用 Go 1.21 引入的新标准库 slices 进行原地排序
	// 按插件的名称 (Name) 进行字母顺序比较 (A-Z)
	slices.SortFunc(sorted, func(a, b MCP) int {
		return strings.Compare(a.Name, b.Name)
	})

	return sorted
}

// LSPs 定义了一个键值对集合
type LSPs map[string]LSPConfig

type LSP struct {
	Name string    `json:"name"`
	LSP  LSPConfig `json:"lsp"`
}

func (l LSPs) Sorted() []LSP {
	sorted := make([]LSP, 0, len(l))
	for k, v := range l {
		sorted = append(sorted, LSP{
			Name: k,
			LSP:  v,
		})
	}
	slices.SortFunc(sorted, func(a, b LSP) int {
		return strings.Compare(a.Name, b.Name)
	})
	return sorted
}

// ResolvedEnv 将 LSPConfig 中定义的 map[string]string 格式的环境变量，
// 转换为 Go 标准库 `os/exec.Cmd` 要求的 []string{"KEY=VALUE"} 格式。
func (l LSPConfig) ResolvedEnv() []string {
	// resolveEnvs 是包内的一个隐藏助手函数。
	// 它不仅会做格式转换，通常还会做“变量展开”。
	// 比如配置里写了 "GOPATH": "$HOME/go"，它会把它真实解析成 "GOPATH=/Users/xxx/go"
	return resolveEnvs(l.Env)
}

// ResolvedEnv 同上，为 MCP 插件子进程准备环境变量。
func (m MCPConfig) ResolvedEnv() []string {
	return resolveEnvs(m.Env)
}

// ResolvedHeaders 处理 HTTP/SSE 模式下的网络请求头。
// 这是一个非常关键的安全设计！
func (m MCPConfig) ResolvedHeaders() map[string]string {
	// 实例化一个 Shell 变量解析器
	resolver := NewShellVariableResolver(env.New())

	for e, v := range m.Headers {
		var err error
		// 动态解析 Header 的值！
		// 【场景】：你绝对不应该把 GitHub Token 明文写在 crush.json 里。
		// 你在配置里应该写："Authorization": "Bearer $GITHUB_TOKEN"。
		// 这里的 ResolveValue 方法会读取你操作系统的真实环境变量，
		// 把 `$GITHUB_TOKEN` 替换成真正的 `ghp_xxxxxx`。
		m.Headers[e], err = resolver.ResolveValue(v)
		if err != nil {
			// 如果解析失败（比如环境变量不存在），打一条错误日志，但程序继续跑（容错机制）
			slog.Error("Error resolving header variable", "error", err, "variable", e, "value", v)
			continue
		}
	}
	return m.Headers
}

// Agent 定义了一个特定领域智能体的专属配置。
// 通过组装不同的模型、工具和上下文，你可以创造出极具针对性的专家级 AI。
type Agent struct {
	// 智能体的唯一标识符（程序内部调用时使用）。
	// 例如："rust-expert", "code-reviewer"
	ID string `json:"id,omitempty"`

	// 智能体的展示名称（在终端 UI 里展示给用户看的名字）。
	// 例如："Rust 资深架构师", "无情的回滚机器"
	Name string `json:"name,omitempty"`

	// 智能体的详细描述。
	// 告诉用户（也可能在底层告诉调度系统）这个 Agent 擅长干什么。
	Description string `json:"description,omitempty"`

	// 禁用开关。
	// 如果某个自定义 Agent 暂时不需要，或者它的系统提示词 (System Prompt) 还在调试，可以先把它关掉。
	Disabled bool `json:"disabled,omitempty"`

	// 该 Agent 使用的模型“体量”类型。
	// 【架构考量】：这是一个极好的抽象！它没有硬编码具体的模型名字（比如 gpt-4o），
	// 而是分成了 "large" (大模型，聪明但慢/贵，适合写复杂架构)
	// 和 "small" (小模型，快且便宜，适合做简单的拼写检查或文本总结)。
	// 这样，当你在全局配置里把 large 模型从 OpenAI 换成 DeepSeek 时，所有挂载 large 类型的 Agent 都会无缝切换！
	Model SelectedModelType `json:"model" jsonschema:"required,description=The model type to use for this agent,enum=large,enum=small,default=large"`

	// 该 Agent 被允许使用的内置工具列表（核心：最小权限原则）。
	// 如果是 nil（用户没配置这个字段），说明这是个全能神，可以使用所有工具。
	// 如果显式配置了 ["bash", "read_file"]，那它就绝对不能去调用网络请求或者操作 Git。
	AllowedTools []string `json:"allowed_tools,omitempty"`

	// 该 Agent 被允许使用的外部 MCP (Model Context Protocol) 插件及具体工具。
	// 这是一个非常精细的二级权限 Map： map[MCP服务器名]允许的工具切片。
	// - 如果 map 为空：允许使用所有配置好的 MCP 服务器。
	// - 如果 map 里有这个 MCP，但它的 Value (切片) 是 nil：允许使用这个 MCP 提供的 *所有* 工具。
	// - 如果 map 是 {"github": ["read_repo", "list_issues"]}：该 Agent 只能使用 github 插件，且只能读代码和看 issue，绝不能创建 issue！
	AllowedMCP map[string][]string `json:"allowed_mcp,omitempty"`

	// 该 Agent 专属的上下文路径（覆盖全局配置）。
	// 【场景】：全局配置里可能有一个 `CRUSH.md` 写着通用的代码规范。
	// 但对于这个特定的 Agent（比如专门负责前端的），你可以在这里配上 `FRONTEND_RULES.md`，
	// 让它在回答前去读前端的专属规范。
	ContextPaths []string `json:"context_paths,omitempty"`
}

// Tools 定义了 crush 自带的内置工具（区别于外部的 MCP 插件）的配置集合。
// 目前主要包含了文件系统探索工具 `ls` 和内容搜索工具 `grep`。
type Tools struct {
	// ls 工具配置。
	// 注意这里非常前沿地使用了 `omitzero`！
	// 💡 Go 开发者小贴士：这是 Go 1.24 标准库 encoding/json 刚刚引入的新特性！
	// 它比 `omitempty` 更严谨，如果 ToolLs 是个空结构体，它就不会被序列化到 JSON 中。
	Ls ToolLs `json:"ls,omitzero"`

	// grep 工具配置。
	Grep ToolGrep `json:"grep,omitzero"`
}

// ToolLs 定义了 AI 调用 `ls`（查看目录结构）命令时的边界限制。
type ToolLs struct {
	// 最大递归深度。
	// 如果大模型想看当前目录结构，防守策略是：只能看当前层（比如 default=0），
	// 绝对不允许它毫无节制地往下钻（防 node_modules 刺客）。
	MaxDepth *int `json:"max_depth,omitempty" jsonschema:"description=Maximum depth for the ls tool,default=0,example=10"`

	// 最大返回条目数。
	// 极其关键的 Token 保护机制！如果一个目录下有 10000 个缓存文件，
	// 把这 10000 个文件名全发给 DeepSeek，上下文直接爆炸。这里做了硬性截断。
	MaxItems *int `json:"max_items,omitempty" jsonschema:"description=Maximum number of items to return for the ls tool,default=1000,example=100"`
}

// Limits 是安全的解包方法。
// 每次执行真实的本地 `ls` 逻辑前，都会调这个方法拿到安全的 int 值。
func (t ToolLs) Limits() (depth, items int) {
	// 如果用户没配置，默认深度为 0（只看当前层），默认条目数虽然这里写 0，
	// 但通常实际执行代码里如果发现是 0，会用代码里的常量（比如 1000）做最终兜底。
	return ptrValOr(t.MaxDepth, 0), ptrValOr(t.MaxItems, 0)
}

// ToolGrep 定义了 AI 调用 `grep`（文本搜索）命令时的边界限制。
type ToolGrep struct {
	// 搜索超时时间。
	// 使用了 Go 标准库的 *time.Duration。
	// 💡 防御性编程：如果你在一个几百 GB 的日志目录里让 AI 跑 grep，
	// 磁盘 I/O 会直接拉满，程序可能永远阻塞。加上超时时间，随时掐断！
	Timeout *time.Duration `json:"timeout,omitempty" jsonschema:"description=Timeout for the grep tool call,default=5s,example=10s"`
}

// GetTimeout 获取超时时间。
func (t ToolGrep) GetTimeout() time.Duration {
	// 优雅的兜底：如果用户在 ~/.config/crush/crush.json 里没配置 grep 的超时，
	// 就强制使用 5 秒！超过 5 秒搜不出来，直接告诉 AI 搜索超时了，让 AI 换个搜法。
	return ptrValOr(t.Timeout, 5*time.Second)
}

// Config 是 crush 运行时的总配置根节点。
// 整个应用的状态、可用模型、各种插件的开关，全都在这里。
type Config struct {
	// JSON Schema 声明。
	// 让 VS Code 知道用哪个规范来校验你的 crush.json 文件。
	Schema string `json:"$schema,omitempty"`

	// 核心路由：任务类型 -> 具体模型的映射。
	// 目前系统只支持 "large" (大模型，如 deepseek-reasoner/gpt-4o) 和 "small" (小模型，如 deepseek-chat/gpt-4o-mini)。
	// 这样设计极其优雅：业务代码只需要说“给我来个大模型写代码”或“给我来个小模型做摘要”，
	// 至于大模型具体是哪家的，由这个 Map 动态路由决定。
	Models map[SelectedModelType]SelectedModel `json:"models,omitempty" jsonschema:"description=Model configurations for different model types,example={\"large\":{\"model\":\"gpt-4o\",\"provider\":\"openai\"}}"`

	// 最近使用的模型历史（保存在 .crush 数据目录里）。
	// 注意 `jsonschema:"-"`，这玩意不会暴露给用户去手动配置，是程序自己维护的缓存。
	RecentModels map[SelectedModelType][]SelectedModel `json:"recent_models,omitempty" jsonschema:"-"`

	// 提供商池 (Providers Pool)。
	// 包含了所有你配置过的 API Key 和厂商信息（比如 OpenAI, Anthropic, 自定义兼容源等）。
	// 【核心亮点】：注意这里用的是 `*csync.Map` 而不是普通的 `map`！
	// 因为 crush 在后台可能会有并发的 Goroutine 同时去读取或刷新 Token，使用并发安全的 Map 防止了经典的 map concurrent read/write panic。
	Providers *csync.Map[string, ProviderConfig] `json:"providers,omitempty" jsonschema:"description=AI provider configurations"`

	// 外部工具接口配置
	MCP MCPs `json:"mcp,omitempty" jsonschema:"description=Model Context Protocol server configurations"`

	// 代码分析能力配置
	LSP LSPs `json:"lsp,omitempty" jsonschema:"description=Language Server Protocol configurations"`

	// 全局偏好设置
	Options *Options `json:"options,omitempty" jsonschema:"description=General application options"`

	// 工具调用权限
	Permissions *Permissions `json:"permissions,omitempty" jsonschema:"description=Permission settings for tool usage"`

	// 内置工具边界限制
	Tools Tools `json:"tools,omitzero" jsonschema:"description=Tool configurations"`

	// 多智能体定义。
	// 注意 `json:"-"`，目前版本的 Agent 配置可能还没有完全对用户开放 JSON 静态配置，
	// 或者是通过动态读取工作目录下的 markdown 文件来生成的。
	Agents map[string]Agent `json:"-"`
}

// ----------------------------------------------------------------
// 以下是 Config 的 Helper Methods (防爆盾方法)
// 因为配置层级太深 (Config -> Provider -> Model)，直接链式调用很容易空指针 Panic。
// 所以作者封装了这一系列极其安全的获取方法。
// ----------------------------------------------------------------

// EnabledProviders 过滤并返回所有未被禁用的提供商。
func (c *Config) EnabledProviders() []ProviderConfig {
	var enabled []ProviderConfig
	// 遍历并发安全的 Map
	for p := range c.Providers.Seq() {
		if !p.Disable {
			enabled = append(enabled, p)
		}
	}
	return enabled
}

// IsConfigured 检查系统是否具备最起码的运行条件（至少配了一个能用的提供商）。
func (c *Config) IsConfigured() bool {
	return len(c.EnabledProviders()) > 0
}

// GetModel 从指定的提供商中，安全地查找到具体的模型配置。
func (c *Config) GetModel(provider, model string) *catwalk.Model {
	if providerConfig, ok := c.Providers.Get(provider); ok {
		for _, m := range providerConfig.Models {
			if m.ID == model {
				return &m
			}
		}
	}
	return nil // 找不到就体面地返回 nil，外层做好 nil 判断即可
}

// GetProviderForModel 根据任务类型 ("large" 或 "small")，找出它背后挂载的提供商配置。
func (c *Config) GetProviderForModel(modelType SelectedModelType) *ProviderConfig {
	model, ok := c.Models[modelType]
	if !ok {
		return nil
	}
	if providerConfig, ok := c.Providers.Get(model.Provider); ok {
		return &providerConfig
	}
	return nil
}

// GetModelByType 根据任务类型 ("large" 或 "small")，直接拿到最终的底层模型实体。
func (c *Config) GetModelByType(modelType SelectedModelType) *catwalk.Model {
	model, ok := c.Models[modelType]
	if !ok {
		return nil
	}
	return c.GetModel(model.Provider, model.Model)
}

// LargeModel 获取当前配置的“干重活”的大模型。
func (c *Config) LargeModel() *catwalk.Model {
	model, ok := c.Models[SelectedModelTypeLarge]
	if !ok {
		return nil
	}
	return c.GetModel(model.Provider, model.Model)
}

// SmallModel 获取当前配置的“打杂/总结”的小模型。
func (c *Config) SmallModel() *catwalk.Model {
	model, ok := c.Models[SelectedModelTypeSmall]
	if !ok {
		return nil
	}
	return c.GetModel(model.Provider, model.Model)
}

// 限制最近使用的模型历史记录最多存 5 条。
const maxRecentModelsPerType = 5

// allToolNames 内置工具清单,返回了 crush 当前版本原生支持的所有超能力工具名。
// 当 Agent 思考时，它就会从这个列表里挑选合适的工具去执行。
func allToolNames() []string {
	return []string{
		"agent",              // 呼叫其他 Agent
		"bash",               // 执行任意终端命令 (高危！)
		"job_output",         // 查看后台任务输出
		"job_kill",           // 杀掉后台任务
		"download",           // 下载网络文件
		"edit",               // 编辑文件 (AI 写代码的核心)
		"multiedit",          // 批量跨文件编辑
		"lsp_diagnostics",    // 向 LSP 请求语法报错信息
		"lsp_references",     // 向 LSP 请求查找函数被谁调用了
		"lsp_restart",        // 重启卡死的 LSP 服务器
		"fetch",              // 抓取普通网页内容
		"agentic_fetch",      // 智能抓取并分析网页
		"glob",               // 模式匹配查找文件
		"grep",               // 全局文本搜索
		"ls",                 // 查看目录结构
		"sourcegraph",        // 借助 Sourcegraph 查阅企业级代码库
		"todos",              // 管理任务清单
		"view",               // 读取文件内容
		"write",              // 新建并写入文件
		"list_mcp_resources", // 查看外部 MCP 插件有什么资源
		"read_mcp_resource",  // 读取外部 MCP 插件的资源
	}
}

// resolveAllowedTools 负责处理“黑名单 (Blacklist)”逻辑。
// 它接收“系统所有的工具 (allTools)”和“用户在配置里禁用的工具 (disabledTools)”，
// 最终返回一个安全的可用工具列表。
func resolveAllowedTools(allTools []string, disabledTools []string) []string {
	// 优雅的短路拦截：如果用户压根没配禁用列表，直接把整个武器库交出去
	if disabledTools == nil {
		return allTools
	}
	// 调用底层过滤函数，采用 "排除模式" (include=false)
	// 意思是：遍历 allTools，如果某个工具在 disabledTools 里，就把它剔除掉。
	// 这对应了集合论中的“差集 (Difference)”。
	return filterSlice(allTools, disabledTools, false)
}

// resolveReadOnlyTools 负责处理“白名单 (Whitelist) / 安全模式”逻辑。
// 这是一个极其重要的保命功能！如果 AI 处于某种限制状态（比如用户要求只读不写），
// 它会把传入的工具列表强制压缩到只有这几个“绝对安全”的读操作工具。
func resolveReadOnlyTools(tools []string) []string {
	// 核心安全名单：模式匹配、文本搜索、目录列举、代码库搜索、查看文件。
	// 你会发现这里绝对没有 `bash`, `edit`, `write` 这种能改变系统状态的危险工具。
	readOnlyTools := []string{"glob", "grep", "ls", "sourcegraph", "view"}

	// 调用底层过滤函数，采用 "包含模式" (include=true)
	// 意思是：遍历当前 tools，如果它【同时存在于】readOnlyTools 里，才予以保留。
	// 这对应了集合论中的“交集 (Intersection)”。
	return filterSlice(tools, readOnlyTools, true)
}

// filterSlice 是一个非常巧妙的切片集合运算通用函数。
// data: 原始切片
// mask: 用来做判断的参照切片 (可以当成黑名单，也可以当成白名单)
// include: 布尔开关。true 代表求交集(保留 mask 里的)，false 代表求差集(剔除 mask 里的)
func filterSlice(data []string, mask []string, include bool) []string {
	var filtered []string
	for _, s := range data {
		// 巧妙的布尔相等判断！
		// slices.Contains(mask, s) 会检查 s 是否在 mask 里，返回 true/false。
		//
		// 情况 1: include = true (白名单模式)
		// 如果 s 在 mask 里 (Contains 返回 true)，true == true，结果为真，保留！
		// 如果 s 不在 mask 里 (Contains 返回 false)，true == false，结果为假，丢弃！
		//
		// 情况 2: include = false (黑名单模式)
		// 如果 s 在 mask 里 (Contains 返回 true)，false == true，结果为假，丢弃！
		// 如果 s 不在 mask 里 (Contains 返回 false)，false == false，结果为真，保留！
		if include == slices.Contains(mask, s) {
			filtered = append(filtered, s)
		}
	}
	return filtered
}

// SetupAgents 初始化并装载 crush 系统自带的默认智能体 (Agents)。
func (c *Config) SetupAgents() {
	// 获取基础权限池：将所有内置工具扣除掉用户在配置中明确禁用的工具。
	allowedTools := resolveAllowedTools(allToolNames(), c.Options.DisabledTools)

	agents := map[string]Agent{
		// 🤖 员工 A：Coder (主力开发)
		AgentCoder: {
			ID:           AgentCoder,
			Name:         "Coder",
			Description:  "An agent that helps with executing coding tasks.", // 负责执行写代码的任务
			Model:        SelectedModelTypeLarge,                             // 必须用最聪明的大模型 (干重活)
			ContextPaths: c.Options.ContextPaths,                             // 默认使用项目根目录下的所有文件作为上下文
			AllowedTools: allowedTools,                                       // 所有能使用的工具
		},

		// 🕵️‍♂️ 员工 B：Task (调研/分析员)
		AgentTask: {
			ID:           AgentTask,
			Name:         "Task",
			Description:  "An agent that helps with searching for context and finding implementation details.", // 负责搜索上下文和查找实现细节
			Model:        SelectedModelTypeLarge,
			ContextPaths: c.Options.ContextPaths,
			AllowedTools: resolveReadOnlyTools(allowedTools), // 哪怕 allowedTools 里有 bash，经过这个函数一洗，Task 员工也只能做 grep, ls, view 这种纯只读操作。
			AllowedMCP:   map[string][]string{},              // 彻底剥夺 Task 员工调用外部 MCP 插件的权限，防止它被恶意仓库里的 Prompt Injection 诱导执行高危操作。
		},
	}
	// 装载进全局配置
	c.Agents = agents
}

// TestConnection 测试当前 Provider 的 API Key 和 BaseURL 是否真的能调通。
func (c *ProviderConfig) TestConnection(resolver VariableResolver) error {
	var (
		providerID = catwalk.InferenceProvider(c.ID)
		testURL    = ""
		headers    = make(map[string]string)
		// 解析可能写成 $MY_API_KEY 的环境变量
		apiKey, _ = resolver.ResolveValue(c.APIKey)
	)

	// ---------------------------------------------------------
	// 阶段 1：静态规则校验 (针对那些根本不提供 /models 校验接口的奇葩厂商)
	// ---------------------------------------------------------
	switch providerID {
	case catwalk.InferenceProviderMiniMax, catwalk.InferenceProviderMiniMaxChina:
		// MiniMax 没有好用的端点来验证 API Key，只能退而求其次，校验一下字符串前缀是不是 "sk-"。
		if !strings.HasPrefix(apiKey, "sk-") {
			return fmt.Errorf("invalid API key format for provider %s", c.ID)
		}
		return nil
	}

	// ---------------------------------------------------------
	// 阶段 2：构建各大阵营的 HTTP 请求参数
	// ---------------------------------------------------------
	switch c.Type {
	case catwalk.TypeOpenAI, catwalk.TypeOpenAICompat, catwalk.TypeOpenRouter:
		baseURL, _ := resolver.ResolveValue(c.BaseURL)
		// 💡 Go 1.22 神器：cmp.Or！如果 baseURL 是空的，就完美 fallback 到官方的 API 地址。
		baseURL = cmp.Or(baseURL, "https://api.openai.com/v1")

		switch providerID {
		case catwalk.InferenceProviderOpenRouter:
			testURL = baseURL + "/credits" // OpenRouter 查余额的接口最适合做心跳探测
		default:
			testURL = baseURL + "/models" // 大部分兼容 OpenAI 的用 /models 接口测试
		}
		// OpenAI 阵营标准的鉴权 Header
		headers["Authorization"] = "Bearer " + apiKey

	case catwalk.TypeAnthropic:
		baseURL, _ := resolver.ResolveValue(c.BaseURL)
		baseURL = cmp.Or(baseURL, "https://api.anthropic.com/v1")

		switch providerID {
		case catwalk.InferenceKimiCoding:
			testURL = baseURL + "/v1/models"
		default:
			testURL = baseURL + "/models"
		}

		headers["x-api-key"] = apiKey
		headers["anthropic-version"] = "2023-06-01"

	case catwalk.TypeGoogle:
		baseURL, _ := resolver.ResolveValue(c.BaseURL)
		baseURL = cmp.Or(baseURL, "https://generativelanguage.googleapis.com")
		// 谷歌 Gemini 阵营喜欢把 Key 直接拼在 URL 的 Query 参数里
		testURL = baseURL + "/v1beta/models?key=" + url.QueryEscape(apiKey)
	case catwalk.TypeBedrock:
		// NOTE: Bedrock has a `/foundation-models` endpoint that we could in
		// theory use, but apparently the authorization is region-specific,
		// so it's not so trivial.
		if strings.HasPrefix(apiKey, "ABSK") { // Bedrock API keys
			return nil
		}
		return errors.New("not a valid bedrock api key")
	case catwalk.TypeVercel:
		// NOTE: Vercel does not validate API keys on the `/models` endpoint.
		if strings.HasPrefix(apiKey, "vck_") { // Vercel API keys
			return nil
		}
		return errors.New("not a valid vercel api key")
	}

	// ---------------------------------------------------------
	// 阶段 3：执行真实的 HTTP 探测
	// ---------------------------------------------------------

	// 极其标准的防挂死操作：创建一个 5 秒必杀的上下文。
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client := &http.Client{}
	req, err := http.NewRequestWithContext(ctx, "GET", testURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create request for provider %s: %w", c.ID, err)
	}

	// 把上面 switch 里凑好的 Header 全塞进去
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	// 把用户在 crush.json 里自定义的 ExtraHeaders 也塞进去
	for k, v := range c.ExtraHeaders {
		req.Header.Set(k, v)
	}

	// 发射请求！
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to create request for provider %s: %w", c.ID, err)
	}
	defer resp.Body.Close()

	// ---------------------------------------------------------
	// 阶段 4：评估结果
	// ---------------------------------------------------------
	switch providerID {
	case catwalk.InferenceProviderZAI:
		// 某些特定的供应商即使报错 400 也能证明网络是通的，只要不是 401 鉴权失败就行。
		if resp.StatusCode == http.StatusUnauthorized {
			return fmt.Errorf("failed to connect to provider %s: %s", c.ID, resp.Status)
		}
	default:
		// 大多数正常人：不是 200 OK 就是失败。
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("failed to connect to provider %s: %s", c.ID, resp.Status)
		}
	}
	return nil // 恭喜，API Key 验证通过！
}

// resolveEnvs 是专门为底层的操作系统进程调用（如 os/exec）准备的“数据清洗器”。
// 它负责把用户在 JSON 里配置的 map 格式环境变量，动态解析并转换为系统进程能认的格式。
func resolveEnvs(envs map[string]string) []string {
	// 1. 初始化一个 Shell 变量解析器。
	// 它可以读取当前操作系统的真实环境变量池（env.New()）。
	resolver := NewShellVariableResolver(env.New())

	// 2. 遍历用户传进来的环境变量 Map
	for e, v := range envs {
		var err error
		// 【核心魔法】：动态展开变量！
		// 如果用户在配置里写了 "PATH": "$PATH:/my/custom/bin" 或 "TOKEN": "${API_KEY}"，
		// 这里会调用解析器，把占位符替换成宿主机的真实环境变量值。
		envs[e], err = resolver.ResolveValue(v)
		if err != nil {
			// 【防御性编程】：如果解析失败（比如引用了一个不存在的变量），
			// 不要让整个程序 Panic，而是打一条 Error 日志，然后跳过当前变量继续执行。
			slog.Error("Error resolving environment variable", "error", err, "variable", e, "value", v)
			continue
		}
	}

	// 3. 准备返回值。
	// 利用 make 预分配切片容量（len(envs)），避免 append 时底层数组频繁扩容，非常好的性能优化习惯。
	res := make([]string, 0, len(envs))

	// 4. 将 Map 扁平化成 []string。
	// 为什么要做这一步？因为 Go 语言标准库启动子进程时（os/exec.Cmd.Env），
	// 要求的环境变量格式必须是切片：[]string{"KEY1=VALUE1", "KEY2=VALUE2"}。
	for k, v := range envs {
		res = append(res, fmt.Sprintf("%s=%s", k, v))
	}
	return res
}

// ptrValOr 是一个极其优雅的“安全解包”泛型工具函数。
// [T any] 表示它支持 Go 1.18+ 的泛型，可以接收任何类型的指针（*int, *bool, *float64 等）。
// 它的作用是：给你一个指针和一个兜底的默认值，如果指针为空，就给你默认值；如果不为空，就把值剥出来给你。
func ptrValOr[T any](t *T, el T) T {
	// 如果指针是 nil（意味着用户在配置文件中压根没填这个字段）
	if t == nil {
		// 返回我们代码里硬编码的默认兜底值 (el)
		return el
	}
	// 如果指针不为空，说明用户填了，直接安全解引用 (*t) 获取真实值
	return *t
}
