# Crush 开发者使用指南

> **Crush** - 终端AI编程助手，让AI直接在终端中帮你写代码

## 📋 目录

- [快速开始](#快速开始)
- [配置管理](#配置管理)
- [模型与会话](#模型与会话)
- [界面操作](#界面操作)
- [开发工作流](#开发工作流)
- [最佳实践](#最佳实践)

---

## 快速开始

### 安装

```bash
# Go 安装
go install github.com/charmbracelet/crush@latest

# 或使用包管理器
brew install charmbracelet/tap/crush
```

### 首次运行

```bash
crush
# 选择模型提供商 → 输入API Key → 开始使用
```

---

## 配置管理

### 配置文件层级

```
优先级：项目配置 > 全局配置 > 环境变量

~/.local/share/crush/
├── crush.json          # 全局配置
└── providers.json      # 模型数据库（自动更新）

your-project/
├── .crush.json         # 项目配置 ⭐
└── CRUSH.md           # 项目规则 ⭐
```

### 环境变量配置

```bash
# API Keys
export ANTHROPIC_API_KEY="sk-ant-..."
export OPENAI_API_KEY="sk-..."
export GEMINI_API_KEY="..."

# 功能开关
export CRUSH_DISABLE_METRICS=1
export DO_NOT_TRACK=1
```

### 项目配置 (.crush.json)

```json
{
  "$schema": "https://charm.land/crush.json",
  "lsp": {
    "go": {
      "command": "gopls",
      "enabled": true
    }
  },
  "permissions": {
    "allowed_tools": ["view", "ls", "grep", "edit"]
  },
  "options": {
    "debug": false,
    "context_paths": ["README.md", "docs/"]
  }
}
```

### 项目规则 (CRUSH.md)

```markdown
# 项目规范

## 构建命令
```bash
go build -o bin/server cmd/server/main.go
```

## 测试命令
```bash
go test ./...
```

## 代码风格
- 使用 gofumpt 格式化
- 错误处理必须显式
- 导出函数必须有 godoc 注释
```

---

## 模型与会话

### 模型切换

| 快捷键 | 功能 |
|--------|------|
| `Ctrl+P` | 打开命令面板 |
| 选择 "Switch Model" | 切换模型 |
| `Tab` | 切换 Large/Small 模型类型 |

**模型类型**：
- **Large Models**: Claude Sonnet 4, GPT-4（复杂任务）
- **Small Models**: Claude Haiku, GPT-4o-mini（简单任务）

### 会话管理

| 快捷键 | 功能 |
|--------|------|
| `Ctrl+N` | 新建会话 |
| `Ctrl+S` | 切换会话 |
| 选择 "Summarize Session" | 总结会话 |

**使用策略**：
- 不同功能使用不同会话
- 会话自动保存，随时可恢复
- 复杂任务用 Large 模型，简单任务用 Small 模型

---

## 界面操作

### 核心快捷键

| 快捷键 | 功能 |
|--------|------|
| `Tab` | 切换焦点到聊天 |
| `Ctrl+P` | 命令面板 |
| `Ctrl+J` | 插入新行 |
| `Ctrl+C` | 退出程序 |
| `Ctrl+G` | 显示帮助 |
| `Ctrl+F` | 附加文件 |
| `↑↓` | 滚动历史 |

### 界面功能介绍

#### 顶部状态栏
- **当前模型显示**：DeepSeek-V3.1, Claude Sonnet 4 等
- **运行模式**：Thinking Mode（思考中）/ Ready（就绪）
- **运行时间**：显示当前会话持续时间
- **状态指示**：Ready?（等待输入）

#### 主聊天区域
- **对话历史**：显示完整的对话记录
- **工具调用显示**：实时显示 Crush 执行的操作
- **代码块渲染**：支持语法高亮的代码显示
- **文件附件**：支持附加文件作为上下文

#### 右侧信息面板
- **修改文件列表**：显示当前会话中修改的文件
- **LSP 状态**：显示语言服务器连接状态
- **MCP 状态**：显示模型上下文协议服务器状态
- **推理进度**：显示 AI 思考进度和成本

#### 底部快捷键栏
- **动态显示**：根据当前状态显示相关快捷键
- **操作提示**：帮助用户了解可执行的操作

### 命令面板功能

```
New Session          - 新建会话
Switch Session       - 切换会话  
Switch Model         - 切换模型
Summarize Session    - 总结会话
Toggle Sidebar       - 切换侧边栏
Toggle Yolo Mode     - 自动确认模式
Toggle Help          - 切换帮助
Initialize Project   - 初始化项目
Quit                 - 退出
```

---

## 开发工作流

### 典型开发流程

```bash
# 1. 启动项目
crush -c /path/to/project

# 2. 理解代码库
> "请分析这个项目的架构和主要功能"

# 3. 添加功能
> "为 Todo 添加优先级字段，包括数据库迁移"

# 4. 测试验证
> "运行测试并修复发现的问题"

# 5. 代码审查
> "审查新添加的代码，给出改进建议"
```

### 工具调用示例

Crush 会自动使用以下工具：

```
🔧 Tool: view - 查看文件内容
🔧 Tool: edit - 编辑文件  
🔧 Tool: bash - 执行命令
🔧 Tool: grep - 搜索代码
🔧 Tool: write - 创建文件
🔧 Tool: ls - 列出文件
🔧 Tool: glob - 文件匹配
🔧 Tool: multiedit - 批量编辑
🔧 Tool: references - 查找引用
🔧 Tool: diagnostics - 代码诊断
```

### Crush 作为开发工具的能力

**Crush 可以独立完成**：
- ✅ 代码生成和编辑
- ✅ 文件创建和删除
- ✅ 代码搜索和分析
- ✅ 运行测试和构建
- ✅ 执行 Git 操作
- ✅ 数据库操作（通过工具）
- ✅ 配置文件管理

**Crush 的优势**：
- 🚀 **直接操作文件系统**：不只是建议，实际修改文件
- 🧠 **理解项目上下文**：通过 LSP 获取类型信息和引用关系
- 🔧 **自动执行命令**：构建、测试、部署等
- 📝 **生成完整代码**：从接口定义到实现，从测试到文档

### 与 IDE 的协作模式

#### 模式 1：Crush 为主，IDE 为辅
**适用场景**：新项目、快速原型、简单功能开发

```
工作流程：
1. Crush 中完成 80% 的开发工作
   - 生成项目结构
   - 实现核心功能
   - 编写测试用例
   - 生成文档

2. IDE 中完成 20% 的精细工作
   - 调试复杂问题
   - 性能优化
   - 最终代码审查
```

#### 模式 2：IDE 为主，Crush 为辅
**适用场景**：复杂项目、团队协作、生产代码

```
工作流程：
1. IDE 中完成核心开发
   - 架构设计
   - 业务逻辑实现
   - 调试和测试

2. Crush 中完成辅助工作
   - 代码审查
   - 重构建议
   - 文档生成
   - 自动化脚本
```

#### 模式 3：并行协作
**适用场景**：大型项目、多模块开发

```
工作流程：
- Crush 负责：新功能开发、代码生成
- IDE 负责：调试、重构、性能优化
- 两者实时同步，互为补充
```

### 是否需要结合 IDE？

**答案：取决于使用场景**

#### 可以完全使用 Crush 的场景：
- ✅ **新项目开发**：从零开始创建项目
- ✅ **脚本编写**：自动化脚本、工具脚本
- ✅ **快速原型**：验证想法、概念验证
- ✅ **简单功能**：CRUD 操作、简单 API
- ✅ **文档生成**：README、API 文档、注释

#### 建议结合 IDE 的场景：
- ⚠️ **复杂调试**：需要断点、变量查看
- ⚠️ **性能优化**：需要性能分析工具
- ⚠️ **大型重构**：需要代码导航和引用分析
- ⚠️ **团队协作**：需要代码审查、合并冲突解决

### 对接工作方式

#### 1. 文件系统对接
```
Crush 直接操作项目文件
↓
IDE 自动检测文件变化
↓
实时同步显示修改
```

#### 2. Git 工作流对接
```
Crush 中：
> "提交当前更改，消息：添加用户认证功能"

IDE 中：
- 查看提交历史
- 处理合并冲突
- 代码审查
```

#### 3. 构建系统对接
```
Crush 中：
> "构建项目并运行测试"

IDE 中：
- 查看构建输出
- 分析测试结果
- 调试失败用例
```

### 主要使用方式

#### 方式 1：纯 Crush 开发
```bash
# 启动 Crush
crush -c /path/to/project

# 完整开发流程
> "创建一个 REST API 项目，包含用户认证"
> "添加数据库迁移"
> "编写单元测试"
> "生成 API 文档"
```

#### 方式 2：Crush + IDE 混合
```bash
# Crush 中生成代码
> "生成用户管理的 CRUD 接口"

# IDE 中精细调整
- 使用 IDE 调试接口
- 优化性能和错误处理
- 添加业务逻辑验证

# 回到 Crush 继续
> "为接口添加集成测试"
```

#### 方式 3：团队协作模式
```bash
# 开发者 A：Crush 中快速原型
> "实现订单处理功能"

# 开发者 B：IDE 中代码审查
- 审查生成的代码
- 提出改进建议
- 合并到主分支

# 开发者 C：Crush 中完善功能
> "根据代码审查建议优化订单处理"
```

---

## 最佳实践

### 提示词技巧

**✅ 好的提示词**：
```
请为 User 模型添加 JWT 认证：
1. 添加 login/logout 接口
2. 创建中间件验证 token
3. 更新现有接口保护
4. 添加相关测试
```

**❌ 避免的提示词**：
```
帮我加个登录功能
```

### 权限配置

```json
{
  "permissions": {
    "allowed_tools": [
      "view",      // 查看文件
      "ls",        // 列出文件  
      "grep",      // 搜索代码
      "glob"       // 文件匹配
      // "edit",   // 编辑文件（需确认）
      // "bash"    // 执行命令（需确认）
    ]
  }
}
```

### 成本优化

**策略**：
- 复杂任务：Large 模型（架构设计、重构）
- 简单任务：Small 模型（查看文件、格式化）
- 预期节省：50-70% 成本

### 团队协作

**共享配置**：
- ✅ 提交 `.crush.json` 到 Git
- ✅ 提交 `CRUSH.md` 到 Git  
- ❌ 不要提交 API Keys

**团队规范**：
```markdown
# CRUSH.md 模板

## 构建命令
```bash
make build
```

## 测试命令  
```bash
make test
```

## 代码规范
- 使用 ESLint + Prettier
- 提交前必须运行测试
- 函数长度不超过 50 行
```

---

## 常用命令

### 命令行操作

```bash
# 基本使用
crush                           # 交互模式
crush run "解释这段代码"         # 一次性执行
crush -d                        # 调试模式
crush -c /path/to/project       # 指定目录

# 日志查看
crush logs                      # 查看日志
crush logs -f                   # 实时跟踪
crush logs --tail 500          # 查看最近500行

# 配置管理
crush dirs                      # 查看目录
crush update-providers          # 更新模型列表
```

### 环境变量

```bash
# API Keys
export ANTHROPIC_API_KEY="sk-ant-..."
export OPENAI_API_KEY="sk-..."

# 功能开关
export CRUSH_DISABLE_METRICS=1
export CRUSH_DISABLE_PROVIDER_AUTO_UPDATE=1
export DO_NOT_TRACK=1

# 调试
export CRUSH_PROFILE=1          # 启用性能分析
```

---

## 故障排除

### 常见问题

**Q: 模型切换失败？**
```bash
# 检查 API Key
echo $ANTHROPIC_API_KEY

# 查看日志
crush logs | grep -i model
```

**Q: LSP 不工作？**
```bash
# 安装 gopls
go install golang.org/x/tools/gopls@latest

# 检查配置
cat .crush.json | grep -A5 lsp
```

**Q: 工具调用被拒绝？**
```bash
# 检查权限配置
cat .crush.json | grep permissions

# 或使用 YOLO 模式（谨慎）
crush --yolo
```

### 调试技巧

```bash
# 启用详细日志
crush -d

# 查看实时日志
crush logs -f

# 性能分析
CRUSH_PROFILE=1 crush
# 访问 http://localhost:6060/debug/pprof/
```

---

## 官网文档对比

### GitHub 官方文档内容
根据 [Crush GitHub 仓库](https://github.com/charmbracelet/crush)，官方文档包含：

#### ✅ 已覆盖的内容
- 安装方法（包管理器、源码构建）
- 基础配置（API Key、环境变量）
- 模型提供商配置（OpenAI、Anthropic、本地模型）
- LSP 集成配置
- MCP 协议支持
- 权限管理
- 日志查看

#### 📋 官方文档特色功能
- **Provider Auto-Updates**：自动更新模型列表
- **Metrics 收集**：使用统计（可禁用）
- **多平台支持**：macOS、Linux、Windows、FreeBSD 等
- **Catwalk 集成**：开源模型数据库

#### 🔧 本指南的补充
- **详细的工作流程**：实际开发中的使用方式
- **IDE 协作策略**：与开发工具的集成方法
- **成本优化技巧**：模型选择和使用策略
- **团队协作规范**：企业级使用最佳实践
- **故障排除指南**：常见问题解决方案

---

## 全局法律与规范

> **说明**：这里的"法律"是指开发团队使用 Crush 的**强制性规范**，不是真正的法律条文。

### 文件结构说明

**全局法律与规范** 包含多个配置文件：

```
项目根目录/
├── CRUSH.md                    # 项目开发规范 ⭐
├── .crush.json                 # Crush 配置
├── .crushignore               # 忽略文件
└── docs/
    ├── DEVELOPMENT_RULES.md    # 详细开发规范
    ├── AI_USAGE_GUIDELINES.md  # AI 使用指南
    └── TEAM_COLLABORATION.md   # 团队协作规范
```

### 核心规范文件

#### 1. CRUSH.md（项目规范）- 主要文件

```markdown
# 项目开发规范

## 构建和测试命令
```bash
# 开发环境
npm run dev

# 构建
npm run build

# 测试
npm test
```

## 代码质量标准
- 所有代码必须通过 ESLint 检查
- 函数复杂度不超过 10
- 文件长度不超过 500 行
- 必须有 80% 以上的测试覆盖率

## AI 辅助开发规范
- 生成的代码必须符合项目 ESLint 规则
- 复杂业务逻辑必须添加详细注释
- 自动生成的代码需要人工审查
- 禁止直接提交 AI 生成的代码到主分支

## 提交规范
- 使用语义化提交信息：feat:, fix:, docs:, style:
- 每次提交前必须运行 `npm test`
- 大功能必须拆分为多个小提交
- 提交前自动运行代码格式化

## 安全规范
- 禁止在代码中硬编码 API Key
- 所有用户输入必须验证和清理
- 使用参数化查询防止 SQL 注入
- API 接口必须添加认证和限流
```

#### 2. .crush.json（工具配置）

```json
{
  "$schema": "https://charm.land/crush.json",
  "lsp": {
    "typescript": {
      "command": "typescript-language-server",
      "enabled": true
    }
  },
  "permissions": {
    "allowed_tools": ["view", "ls", "grep", "edit", "bash"]
  },
  "options": {
    "context_paths": [
      "CRUSH.md",
      "package.json",
      "README.md"
    ]
  }
}
```

#### 3. .crushignore（忽略文件）

```gitignore
# 构建产物
dist/
build/
*.min.js

# 依赖
node_modules/
.pnpm-store/

# 环境配置
.env
.env.local
.env.production

# 日志
*.log
logs/

# 临时文件
.tmp/
temp/
```

### 团队规范文件

#### DEVELOPMENT_RULES.md（详细开发规范）

```markdown
# 详细开发规范

## 代码审查检查清单
- [ ] 代码符合项目风格指南
- [ ] 所有测试通过
- [ ] 没有 console.log 残留
- [ ] 错误处理完整
- [ ] 性能影响评估
- [ ] 安全性检查

## 分支管理规范
- feature/* - 新功能开发
- bugfix/* - Bug 修复
- hotfix/* - 紧急修复
- release/* - 版本发布

## 代码风格规范
- 使用 2 空格缩进
- 使用单引号
- 行尾不加分号
- 函数名使用 camelCase
- 常量使用 UPPER_SNAKE_CASE
```

#### AI_USAGE_GUIDELINES.md（AI 使用指南）

```markdown
# AI 辅助开发指南

## 权限分级
- **Level 1**（新手）：view, ls, grep
- **Level 2**（熟练）：+ edit, write
- **Level 3**（高级）：+ bash, diagnostics
- **Level 4**（专家）：所有工具

## 成本控制
- 简单查询：使用 Small 模型
- 代码生成：使用 Large 模型
- 月度限额：$50/人
- 超出限额需要审批

## 质量保证
- AI 生成的代码必须通过人工审查
- 复杂功能需要编写测试用例
- 关键业务逻辑需要详细注释
```

### 多文档处理能力

#### ✅ 可以处理多个设计文档

**方式 1：附加多个文件**
```bash
# 在 Crush 中使用 Ctrl+F 附加文件
> Ctrl+F → 选择 design.md
> Ctrl+F → 选择 api-spec.md  
> Ctrl+F → 选择 database-schema.md

# 然后输入需求
> "根据这些设计文档实现用户管理系统"
```

**方式 2：使用 glob 模式**
```markdown
# 在 CRUSH.md 中配置
## 设计文档
所有设计文档位于 docs/design/ 目录：
- api-specification.md
- database-design.md
- ui-mockups.md
- user-stories.md
```

**方式 3：项目初始化时包含**
```bash
# Crush 会自动读取配置的上下文文件
crush -c /path/to/project
> "实现用户认证功能，参考设计文档"
```

#### ✅ 可以处理图片

**支持的图片类型**：
- PNG, JPG, JPEG, GIF
- SVG（矢量图）
- WebP

**使用场景**：
```bash
# 1. UI 设计稿实现
> Ctrl+F → 选择 ui-design.png
> "根据这个设计稿实现前端页面"

# 2. 架构图理解
> Ctrl+F → 选择 system-architecture.png  
> "分析这个系统架构图，实现对应的模块"

# 3. 错误截图调试
> Ctrl+F → 选择 error-screenshot.png
> "分析这个错误，提供解决方案"

# 4. 流程图实现
> Ctrl+F → 选择 workflow-diagram.png
> "根据这个流程图实现业务逻辑"
```

**图片处理限制**：
- 需要模型支持图片理解（如 GPT-4 Vision、Claude 3.5 Sonnet）
- 图片大小限制：通常 20MB 以内
- 复杂图片可能需要多次交互

### 实际使用示例

#### 示例 1：多文档开发

```bash
# 1. 准备文档
project/
├── docs/
│   ├── requirements.md      # 需求文档
│   ├── api-spec.md         # API 规范
│   ├── database-schema.sql # 数据库设计
│   └── ui-mockups.png      # UI 设计稿
├── CRUSH.md                # 项目规范
└── .crush.json             # Crush 配置

# 2. 启动 Crush
crush -c /path/to/project

# 3. 附加所有相关文档
> Ctrl+F → requirements.md
> Ctrl+F → api-spec.md
> Ctrl+F → database-schema.sql
> Ctrl+F → ui-mockups.png

# 4. 开始开发
> "根据这些文档实现完整的用户管理系统，包括前端、后端和数据库"
```

#### 示例 2：图片驱动开发

```bash
# 1. 准备设计稿
> Ctrl+F → 选择 login-page-design.png

# 2. 实现页面
> "根据这个登录页面设计稿，使用 React + Tailwind CSS 实现前端页面"

# 3. 处理响应式
> "让这个页面支持移动端响应式布局"

# 4. 添加交互
> "添加表单验证和登录逻辑"
```

### 最佳实践

#### 文档组织建议

```markdown
# 项目文档结构
docs/
├── design/                 # 设计文档
│   ├── requirements.md
│   ├── api-specification.md
│   ├── database-design.md
│   └── ui-mockups/        # 设计稿文件夹
├── development/           # 开发文档
│   ├── setup.md
│   ├── coding-standards.md
│   └── testing-guidelines.md
└── deployment/           # 部署文档
    ├── docker.md
    └── ci-cd.md
```

#### 图片处理建议

```markdown
# 图片处理规范
- 设计稿命名：feature-name-design.png
- 架构图命名：system-component-architecture.png
- 错误截图命名：error-timestamp.png
- 图片大小：建议 5MB 以内
- 图片格式：优先使用 PNG（无损）
```

---

## 总结

### 核心优势

- 🚀 **直接操作代码**：不只是建议，实际修改文件
- 🧠 **理解项目上下文**：通过 LSP 获取代码信息  
- 💬 **会话管理**：保持对话历史，随时恢复
- 🔧 **可控自动化**：权限系统确保安全
- ⚙️ **高度可定制**：配置、工具、模型都可自定义

### 适用场景

- ✅ 代码生成和重构
- ✅ Bug 调试和修复  
- ✅ 项目架构设计
- ✅ 代码审查和优化
- ✅ 文档生成和维护

### 学习路径

```
1. 快速体验 → crush
2. 配置项目 → .crush.json + CRUSH.md  
3. 掌握快捷键 → Ctrl+P, Ctrl+S, Ctrl+N
4. 优化工作流 → Large/Small 模型切换
5. 团队协作 → 共享配置和规范
```

---

**开始你的 Crush 之旅！** 🚀

---

*文档版本: 1.0 | 适用于: Crush v0.11.2+ | 最后更新: 2025-10*
