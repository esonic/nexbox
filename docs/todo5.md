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
| T20.1 | Partial Messages 实时流式输出 |
| T20.2 | 主动 control_request 能力（优雅中断） |
| T20.3 | Initialize 握手 + Hooks 框架 |
| T20.4 | ThinkingBlock 补全 signature 字段 |
| T20.5 | result 消息补全字段 + stop_reason |
| T20.6 | task_notification 子任务消息处理 |

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
- 检查这些用户的 project 最后活跃时间，超 30 天未使用则自动删除 project 数据
- 复用已有的 project 删除逻辑（含 os.RemoveAll + container stop）
- 本地模式 jwt_secret 为空及 esonice@gmail.com 特权用户的数据不删

**影响文件**：`server/main.go`、`server/api/projects.go`、`server/store/`

---

## 🟡 中优先级

### T13.2 — / 命令透传 Claude CLI

**现状**：T13.1 已完成（移除自定义 /@ 交互）。`@` 文件补全由 T18.3 已覆盖。

**目标**：
- `/` 命令：研究 Claude CLI 的 `/compact`、`/clear`、`/model` 等命令协议，实现透传和交互式响应渲染
- 命令列表在 T20.3 任务实现时应该已经在启动时拉取到了 command 列表
- 输入 `/` 后弹出可用命令提示列表（如 `/compact`、`/clear` 等），选中后直接发送给 Claude CLI

**影响文件**：`web/src/components/chat/ChatInput.tsx`、`server/agent/claude_agent.go`

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

### T21 - 接入其他模型供应商的能力

复用 claude code，但使用其他模型供应商，使用以下环境变量

环境变量	描述
ANTHROPIC_DEFAULT_OPUS_MODEL	用于 opus 的模型，或在 Plan Mode 活跃时用于 opusplan 的模型。
ANTHROPIC_DEFAULT_SONNET_MODEL	用于 sonnet 的模型，或在 Plan Mode 不活跃时用于 opusplan 的模型。
ANTHROPIC_DEFAULT_HAIKU_MODEL	用于 haiku 的模型，或后台功能
CLAUDE_CODE_SUBAGENT_MODEL	用于 subagents 的模型
注意：ANTHROPIC_SMALL_FAST_MODEL 已弃用，改为使用 ANTHROPIC_DEFAULT_HAIKU_MODEL。

CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1
ANTHROPIC_BASE_URL=https://api.deepseek.com/anthropic
ANTHROPIC_AUTH_TOKEN=

---

模擬 subapi 主頁及登錄界面；接入 stripe

---

## 优先级总览

| 优先级 | 编号 | 任务 | 复杂度 |
|--------|------|------|--------|
| 🔴 高 | T3.2 | 容器网络白名单 | 高 |
| 🔴 高 | T15 | Quota=0 用户自动清理 Project | 中 |
| 🟡 中 | T13.2 | / 命令透传 Claude CLI | 中 |
| 🟢 低 | T8.2 | gVisor 容器运行时 | 低（配置） |
| 🟢 低 | T21 | 接入其他模型供应商 | 中 |
