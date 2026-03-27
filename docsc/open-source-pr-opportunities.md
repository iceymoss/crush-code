# Crush 开源贡献执行手册（代码级别）

> 目标：这份文档不是“建议列表”，而是“几周后回来也能直接开工”的待办手册。
> 每个候选 PR 都包含：目标代码、问题描述、落地步骤、测试清单、验收标准和建议提交信息。

---

## 已完成 PR 记录（避免重复）

### PR-C（已完成）
- 分支：`fix/event-pairs-to-props-panic`
- 提交：`fix(event): prevent panic on non-string telemetry keys`
- 代码：
  - `internal/event/event.go` 的 `pairsToProps(props ...any)`
  - `internal/event/event_test.go` 的 `TestPairsToProps`
- 结果：非 string key 不再 panic，并且保留后续合法属性。

### PR-B（已完成）
- 分支：`fix/ui-openeditor-tempfile-cleanup`
- 提交：`fix(ui): always clean up external editor temp file`
- 代码：
  - `internal/ui/model/ui.go` 的 `openEditor(value string) tea.Cmd`
- 结果：临时文件在成功、错误、空内容路径都能清理。

---

## 优先级 A：可直接开做（低风险、高通过率）

## A1. LSP 纯函数测试补齐（强烈推荐）
- **目标代码**
  - `internal/lsp/manager.go`
  - 函数：`resolveServerName`、`handlesFiletype`、`hasRootMarkers`、`handles`
- **问题描述**
  - LSP 启动策略关键逻辑集中在这几个函数，但当前覆盖薄弱，容易被未来重构误伤。
- **执行步骤**
  - 新建 `internal/lsp/manager_test.go`。
  - 用表驱动测试四个函数，不起真实 LSP 进程。
  - 文件系统相关测试统一用 `t.TempDir()` + 写 marker 文件。
- **建议测试用例**
  - `resolveServerName`
    - 输入为已存在 server name，原样返回。
    - 输入为 command 名称，能映射到 canonical name。
    - 无匹配时，原样返回。
  - `handlesFiletype`
    - `fileTypes` 为空时返回 true。
    - 扩展名与 language 名匹配时返回 true。
    - 不匹配时返回 false。
  - `hasRootMarkers`
    - 无 marker 返回 false。
    - 存在 marker 返回 true。
  - `handles`
    - 覆盖“文件类型匹配 + 根标记匹配”的组合条件。
- **验收标准**
  - `go test ./internal/lsp -run Test.*(Resolve|Handles|Root).*` 通过。
  - 不引入生产行为改动，只新增测试。
- **建议提交**
  - 分支：`test/lsp-manager-pure-functions`
  - Commit：`test(lsp): add coverage for manager decision helpers`

---

## A2. 移除 UI 未使用渲染上下文
- **目标代码**
  - `internal/ui/chat/tools.go`
  - 符号：`type DefaultToolRenderContext struct{}`、`(*DefaultToolRenderContext).RenderTool`
- **问题描述**
  - 疑似未被实际调用，增加阅读成本和误导。
- **执行步骤**
  - 删除上述类型及方法。
  - 保留 `NewToolMessageItem` 现有分发逻辑不变。
- **建议测试/验证**
  - `go test ./internal/ui/...`
  - 手动打开聊天界面，触发任意工具消息渲染（bash/view 等）确认显示正常。
- **验收标准**
  - 仅删除无引用代码，无行为变化。
- **建议提交**
  - 分支：`refactor/ui-remove-unused-tool-context`
  - Commit：`refactor(ui): remove unused default tool render context`

---

## A3. `GetMCPPrompt` 传递 `context.Context`
- **目标代码**
  - `internal/commands/commands.go`：`GetMCPPrompt(...)`
  - `internal/ui/model/ui.go`：`runMCPPrompt(...)`
- **问题描述**
  - `GetMCPPrompt` 内部使用 `context.Background()`，无法继承上游取消/超时语义。
- **执行步骤**
  - 把签名改为：`GetMCPPrompt(ctx context.Context, cfg *config.ConfigStore, ...)`。
  - 调用 `mcp.GetPromptMessages(ctx, ...)`。
  - 在 `runMCPPrompt` 的调用点透传 `ctx`（若上层无现成可取消 ctx，先显式传 `context.Background()`，但接口保持可扩展）。
- **建议测试/验证**
  - 至少补一个 `commands` 层单测，验证参数透传不破坏现有行为。
  - 手工走一次 MCP prompt 路径，确认 UI 行为不变。
- **验收标准**
  - 编译通过，MCP prompt 功能不回退。
- **建议提交**
  - 分支：`refactor/commands-mcp-prompt-context`
  - Commit：`refactor(commands): pass context through GetMCPPrompt`

---

## A4. `OnRetry` 最小可观测性补齐
- **目标代码**
  - `internal/agent/agent.go`
  - 区域：`OnRetry: func(err *fantasy.ProviderError, delay time.Duration) { ... }`
- **问题描述**
  - Provider 重试发生时缺乏可观察信号，排障困难。
- **执行步骤**
  - 在回调中增加结构化日志：错误类型、延迟、模型/provider 关键信息。
  - 禁止记录敏感字段（token、raw prompt）。
- **建议测试/验证**
  - 先做最小日志实现，不强制单测。
  - 手工模拟 provider 错误路径（可通过 mock provider 或无效 key 场景）观察日志。
- **验收标准**
  - 重试时可观测，正常路径无噪音。
- **建议提交**
  - 分支：`chore/agent-log-provider-retry`
  - Commit：`chore(agent): add retry logging for provider errors`

---

## 优先级 B：第二阶段（中等投入，简历加分）

## B1. `internal/db` 连接与迁移 smoke test
- **目标代码**
  - `internal/db/connect.go`：`Connect(ctx, dataDir)`
  - 新增：`internal/db/connect_test.go`
- **问题描述**
  - DB 是核心路径，但当前包级测试薄弱。
- **执行步骤**
  - 先加 3 个基础用例，不碰业务层：
    - `dataDir == ""` 返回错误。
    - `t.TempDir()` 下连接成功。
    - 同目录重复连接可用（迁移幂等）。
- **建议测试断言**
  - 返回 error 是否符合预期。
  - DB 文件 `crush.db` 是否创建。
  - `db.PingContext` 是否成功。
- **验收标准**
  - `go test ./internal/db` 通过，且测试稳定无 flaky。
- **建议提交**
  - 分支：`test/db-connect-smoke`
  - Commit：`test(db): add smoke tests for Connect and migrations`

---

## B2. `internal/db` session/message 最小 CRUD 测试
- **目标代码**
  - `internal/db/sql/*.sql` 对应的 sqlc 生成方法（`internal/db/*.sql.go`）
- **问题描述**
  - schema 和 query 变化容易产生无声回归。
- **执行步骤**
  - 选 2-3 个最关键方法（session 创建/查询，message 创建/查询）做最小闭环测试。
  - 测试里只关注输入输出，不做 UI/agent 端到端。
- **验收标准**
  - 关键 query 契约锁定，后续 schema 改动可快速报警。
- **建议提交**
  - 分支：`test/db-session-message-queries`
  - Commit：`test(db): cover core session and message queries`

---

## B3. `glob` 工具参数与截断行为测试
- **目标代码**
  - `internal/agent/tools/glob.go`
  - 函数：`NewGlobTool`、`globFiles`
  - 参数：`GlobParams`
- **问题描述**
  - 工具输入边界多，适合用表驱动锁行为。
- **建议测试用例**
  - 空 `pattern` 返回错误响应文本。
  - `path` 非法/不存在时行为符合预期。
  - 匹配数量超过 limit 时 `truncated` 元数据正确。
- **验收标准**
  - `go test ./internal/agent/tools -run Test.*Glob.*` 通过。
- **建议提交**
  - 分支：`test/tools-glob-boundary-cases`
  - Commit：`test(tools): add glob boundary and truncation coverage`

---

## B4. `ls` 工具 depth/limit/权限路径测试
- **目标代码**
  - `internal/agent/tools/ls.go`
  - 函数：`NewLsTool`
  - 结构：`LSParams`
  - 常量：`maxLSFiles`
- **问题描述**
  - 目录树深度、文件上限和权限控制是高风险组合路径。
- **建议测试用例**
  - 深度限制生效。
  - 超过上限时提示文案和截断行为正确。
  - 权限拒绝时返回预期错误。
- **验收标准**
  - 行为一致，避免未来权限模型改动导致泄露。
- **建议提交**
  - 分支：`test/tools-ls-depth-and-permissions`
  - Commit：`test(tools): cover ls depth limits and permission failures`

---

## B5. `fetch_helpers` 纯函数回归测试
- **目标代码**
  - `internal/agent/tools/fetch_helpers.go`
  - 函数：`cleanupMarkdown`、`ConvertHTMLToMarkdown`、`FormatJSON`
- **问题描述**
  - 纯函数无测试时，依赖升级后容易出现输出格式漂移。
- **建议测试用例**
  - HTML 转 Markdown 的基础场景。
  - 合法 JSON 的格式化稳定性。
  - 非法 JSON 的错误返回行为。
  - 多空行压缩逻辑。
- **验收标准**
  - 函数契约明确并固化。
- **建议提交**
  - 分支：`test/tools-fetch-helpers`
  - Commit：`test(tools): add coverage for fetch helper utilities`

---

## B6. `view` 工具负 offset 边界保护
- **目标代码**
  - `internal/agent/tools/view.go`
  - 函数：`NewViewTool`、`readTextFile`、`addLineNumbers`
  - 参数：`ViewParams.Offset`
- **问题描述**
  - 当前 `offset < 0` 时读取逻辑与行号展示语义可能不一致。
- **执行步骤**
  - 方案一：入口处拒绝负值并返回明确错误。
  - 方案二：`offset` 小于 0 时 clamp 到 0。
  - 建议优先方案一（行为更明确）。
- **建议测试用例**
  - 负 offset 输入。
  - offset=0 正常行为不变。
  - 大 offset 超文件长度行为。
- **验收标准**
  - 行号语义稳定，文档与实现一致。
- **建议提交**
  - 分支：`fix/tools-view-negative-offset`
  - Commit：`fix(tools): validate negative offset in view tool`

---

## 优先级 C：先提 issue 再动手（高不确定性）

## C1. `pubsub` 选项参数与实现一致性
- **目标代码**
  - `internal/pubsub/broker.go`
  - 结构：`Broker`
  - 函数：`NewBrokerWithOptions`、`Subscribe`、`Publish`
  - 常量：`bufferSize`
- **关注点**
  - `NewBrokerWithOptions(channelBufferSize, maxEvents)` 的参数是否被完整消费。
  - `maxEvents` 目前如何影响实际发布逻辑。
- **建议**
  - 先提 issue 对齐设计，再提交修复 PR。

## C2. onboarding TODO 清理与错误反馈
- **目标代码**
  - `internal/ui/model/onboarding.go`
  - 函数：`markProjectInitialized`、`skipInitializeProject`
- **关注点**
  - TODO 表述与实际行为是否一致。
  - 错误是否需要统一走 footer 提示。
- **建议**
  - 先和 maintainer 对齐产品预期，再改实现。

---

## 通用执行模板（每个 PR 都按这个流程）

1. 建分支：`git checkout -b <type/scope-short-desc>`。
2. 改一个问题，不跨模块扩散。
3. 先跑最小测试，再跑受影响包测试。
4. commit 使用语义前缀：`fix:`、`test:`、`refactor:`、`chore:`。
5. PR 描述固定四段：背景、改动点、测试计划、风险说明。

---

## 推荐命令清单（回归用）

```bash
# 只跑 event 包
go test ./internal/event

# 只跑 UI model 包
go test ./internal/ui/model

# 跑 LSP 包
go test ./internal/lsp

# 跑 tools 包（可配合 -run 聚焦）
go test ./internal/agent/tools

# 跑 db 包
go test ./internal/db
```

---

## 未来三周建议节奏（防中断版本）

- 第 1 周：A1（LSP 测试）+ A2（清理死代码）
- 第 2 周：B3（glob 测试）+ B6（view offset 修复）
- 第 3 周：B1（db smoke）+ B2（db CRUD）

每周目标：至少 2 个小 PR，保持 review 节奏和可见活跃度。

