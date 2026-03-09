# BoxNex 待办任务列表（第四轮）

> 整理时间：2026-03-09。基于 todo3.md 检查后的剩余未实现项，并新增本轮任务。

---

## 已完成项汇总（含本轮新增已完成）

| 编号 | 任务 |
|------|------|
| T1.1 | 移除云端 packages 系统 |
| T1.2 | 适配 Anthropic 官方仓库格式（SKILL.md / .claude-plugin / JSON） |
| T1.4 | Connector 格式支持（.claude-plugin/plugin.json + .mcp.json） |
| T3.1 | 容器资源限制（memory/cpu/pids/disk） |
| T3.3 | 容器空闲回收（TouchActivity + 10min check / 1h timeout） |
| T3.4 | Project 删除时清理宿主机目录（os.RemoveAll） |
| T4 | Project 创建 UX 简化 |
| T6 | Files/Terminal/Git 按钮移至顶部工具栏 |
| T7 | 文件上传/下载（后端 + 前端） |
| T9.1 | 软删除清理改为 1 天 |
| T9.2 | Session 数量上限（20/project） |
| T9.3 | 消息数量上限（100/session） |
| T9.4 | 上传文件清理（随 T3.4 project 删除一并清理） |
| T9.5 | SQLite 定期维护（WAL checkpoint + VACUUM，每日） |
| T10 | 国际化（中英文切换） |
| T11 | 余额显示 |
| T12 | 模型选择（Sonnet/Haiku） |
| T13.1 | 移除 / 和 @ 特殊交互 |
| T14 | Claude CLI 交互式响应（AskUserQuestion 全链路） |
| T2 | MCP OAuth Connector 安装流程 |
| T5 | MCP 工具可见性精细控制 |
| T16 | 工作区磁盘配额改为 256MB |
| T17 | 首次注册自动创建 Project + Session |
| T18.1 | Files 面板拖拽上传 |
| T18.2 | 聊天框拖拽上传并 @ 引用 |
| T18.3 | 聊天框 @ 文件选择器（联想补全） |
| T19 | 计费：停止响应时 Token 扣费接入 |

---

## 🔴 高优先级

### T3.2 — 容器网络白名单

**现状**：容器无网络限制，可访问任意地址（含宿主机其他端口）。

**目标**：容器只允许访问 Claude API 及特定域名，其他地址一律禁止。

**实现思路**（二选一）：

1. **Docker 自定义网络 + iptables**
   - 创建 `--network=boxnex-isolated` 网络
   - 宿主机 iptables OUTPUT chain 限制容器子网的目标 IP
   - 白名单域名通过 config 配置，定期 DNS 解析更新 iptables 规则

2. **HTTP 正向代理方案**（推荐，更易维护）
   - 部署 squid/tinyproxy 作为容器唯一出口
   - 容器内设 `HTTP_PROXY` / `HTTPS_PROXY` 环境变量
   - 代理层做域名白名单（`api.anthropic.com` 等）

**影响文件**：`server/agent/container.go`（docker run 参数）、config

---

### T15 — Quota=0 用户自动清理 Project

**现状**：Project 删除时已实现目录清理，但无自动过期机制。

**目标**：token 剩余 quota 为 0 的用户，30 天后自动清理掉其所有 project。

**实现思路**：
- 定期 goroutine（每天检查一次）
- 查询 balance=0 的用户列表
- 检查这些用户的 project 最后活跃时间，超 30 天未使用则自动删除
- 删除前可选：发邮件/站内通知提醒用户充值
- 复用已有的 project 删除逻辑（含 os.RemoveAll + container stop）

**影响文件**：`server/main.go`、`server/api/projects.go`、`server/store/`

---

## 🟡 中优先级

### T13.2 — / 命令透传 Claude CLI

**现状**：T13.1 已完成（移除自定义 /@ 交互）。`@` 文件补全由 T18.3 覆盖。

**目标**：
- `/` 命令：研究 Claude CLI 的 `/compact`、`/clear`、`/model` 等命令协议，实现透传和交互式响应渲染
- 输入 `/` 后弹出可用命令提示列表（如 `/compact`、`/clear` 等），选中后直接发送给 Claude CLI

**影响文件**：`web/src/components/chat/ChatInput.tsx`、`server/agent/claude_agent.go`

---


## 🟡 中优先级（续）

### T20.1 — Partial Messages 实时流式输出

**现状**：`EventTextDelta` 只在 Claude 完成整个 assistant turn 后才触发，用户看到文字"一段话突然全部出现"。

**目标**：实现打字机效果——Claude 生成中即逐字推送到前端。

**实现思路**：
- 传 `--include-partial-messages` CLI 参数
- 设置环境变量 `CLAUDE_CODE_ENABLE_FINE_GRAINED_TOOL_STREAMING=1`
- 在 `claude_agent.go` 中新增对 `stream_event` 消息类型的处理（当前走 `default` 分支被忽略）
- `stream_event` 携带 partial text delta，映射到 `EventTextDelta` 事件

**影响文件**：`server/agent/claude_cli.go`（参数构建）、`server/agent/claude_agent.go`（消息处理）

---

### T20.2 — 主动 control_request 能力（优雅中断）

**现状**：停止任务依赖 SIGTERM，进程级别强杀，Claude 无法完成清理工作。

**目标**：发送 `interrupt` control_request，让 CLI 优雅地停止当前 turn 并返回结果消息。

**实现思路**：
- 在 `claude_cli.go` 中新增 `sendControlRequest(subtype, payload)` 方法，向 stdin 写入 `control_request` JSON
- 等待 CLI 返回对应 `control_response`（带 request_id 匹配）
- 当前 `Cancel()` 调用改为先发 `interrupt`，超时后再 SIGTERM
- 这也是后续支持 `set_model`、`mcp_status` 等高级能力的基础

**影响文件**：`server/agent/claude_cli.go`

---

### T20.3 — Initialize 握手 + Hooks 框架

**现状**：没有 initialize 握手，`hook_callback` 直接 auto-continue，hooks 完全无法工作。

**目标**：
1. 发送第一条用户消息前，先向 CLI 发 `initialize` control_request（注册 hooks、agents）
2. 建立 hook 回调框架，使 `PreToolUse` 等 hook 可被实际执行

**实现思路**：
- 在 `Query()` 开始时，发出 `{"subtype":"initialize","hooks":...}` control_request
- 等待 CLI 的 initialize 响应
- 在 `hook_callback` 处理中调用已注册的回调（Go 函数），而不是直接 auto-continue
- `QueryOptions` 增加 `Hooks map[string]HookFunc` 字段

**影响文件**：`server/agent/claude_cli.go`、`server/agent/agent.go`（QueryOptions）

**依赖**：T20.2（需要先有主动 control_request 能力）

---

### T20.4 — ThinkingBlock 补全 signature 字段

**现状**：`AssistantContent` 只有 `Thinking string`，缺少 `Signature string`。

**目标**：多轮 extended thinking 对话中 signature 字段被正确保留并在后续轮次回传给 CLI，确保 thinking 验证机制正常工作。

**影响文件**：`server/agent/claude_cli.go`（`AssistantContent` 结构体）、`server/agent/claude_agent.go`（thinking 块解析）

---

### T20.5 — result 消息补全字段 + stop_reason 处理

**现状**：`stop_reason`、`duration_api_ms` 未捕获，无法区分"达到 max_turns"还是"正常完成"。

**目标**：
- 捕获 `stop_reason`（`end_turn` / `max_turns` / `tool_use` 等）并透传到前端
- 捕获 `duration_api_ms`（纯 API 耗时，不含工具执行时间）
- 前端对 `stop_reason=max_turns` 显示特殊提示

**影响文件**：`server/agent/claude_cli.go`（`StreamMessage` 结构体）、`server/agent/claude_agent.go`（result 处理）、前端 ChatMessage

---

## 🟢 低优先级

### T20.6 — task_notification 等子任务消息处理

**现状**：`task_started`、`task_progress`、`task_notification` system 子类型被忽略。

**目标**：当 Claude 使用 `Agent` 工具委托子任务时，在前端展示子任务进度（类似 Claude.ai 的 subagent 展示）。

**影响文件**：`server/agent/claude_agent.go`、前端 StreamEvent 展示

---

### T20.7 — 规范性改进（CLI 版本检查 + 环境变量）

**现状**：启动时不检查 Claude CLI 版本；没有设置 `CLAUDE_CODE_ENTRYPOINT` 环境变量。

**目标**：
- 启动时检查 `claude -v`，低于 2.0.0 发出警告
- 设置 `CLAUDE_CODE_ENTRYPOINT=sdk-go` 用于遥测追踪

**影响文件**：`server/agent/claude_cli.go`

---

### T8.2 — gVisor 容器运行时

**现状**：容器使用默认 runc 运行时。

**目标**：长期可用 gVisor (runsc) 作为容器运行时，进一步隔离系统调用。

**实现思路**：docker run 加 `--runtime=runsc`，需宿主机安装 gVisor。

---

## 📝 备忘（产品方向）

- context 占用量查看（显示当前 session 的 context window 使用百分比）
- 手机端适配（响应式 UI）
- 理念：
  - 云端 Claude cowork、code 能力
  - 安全：隔离，可授予 Claude 所有权限
  - 便宜：可多人共享 Pro Max 订阅
  - 云办公：云空间，手机可随时访问

---

## 优先级总览

| 优先级 | 编号 | 任务 | 复杂度 |
|--------|------|------|--------|
| 🔴 高 | T3.2 | 容器网络白名单 | 高 |
| 🔴 高 | T15 | Quota=0 用户自动清理 Project | 中 |
| 🟡 中 | T13.2 | / 命令透传 Claude CLI | 中 |
| 🟡 中 | T20.1 | Partial Messages 实时流式输出 | 中 |
| 🟡 中 | T20.2 | 主动 control_request 能力（优雅中断） | 中 |
| 🟡 中 | T20.3 | Initialize 握手 + Hooks 框架 | 高 |
| 🟡 中 | T20.4 | ThinkingBlock 补全 signature 字段 | 低 |
| 🟡 中 | T20.5 | result 消息补全字段 + stop_reason | 低 |
| 🟢 低 | T20.6 | task_notification 子任务消息处理 | 中 |
| 🟢 低 | T20.7 | CLI 版本检查 + CLAUDE_CODE_ENTRYPOINT | 低 |
| 🟢 低 | T8.2 | gVisor 容器运行时 | 低（配置） |
