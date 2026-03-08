# BeeHub 待办任务列表（第三轮）

> 整理时间：2026-03-08。基于 todo2.md 检查后的剩余未实现项。

---

## 已完成项汇总

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
| T11 | 余额显示 |
| T12 | 模型选择（Sonnet/Haiku） |
| T13.1 | 移除 / 和 @ 特殊交互 |
| T14 | Claude CLI 交互式响应（AskUserQuestion 全链路） |

---

## 🔴 高优先级

### T3.2 — 容器网络白名单

**现状**：容器无网络限制，可访问任意地址（含宿主机其他端口）。

**目标**：容器只允许访问 Claude API 及特定域名，其他地址一律禁止。

**实现思路**（二选一）：

1. **Docker 自定义网络 + iptables**
   - 创建 `--network=beehub-isolated` 网络
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

**目标**：token 剩余 quota 为 0 的用户，30 天后自动清理掉其所有 project（等同于删除 project）。

**实现思路**：
- 定期 goroutine（每天检查一次）
- 查询 balance=0 的用户列表
- 检查这些用户的 project 最后活跃时间，超 30 天未使用则自动删除
- 删除前可选：发邮件/站内通知提醒用户充值
- 复用已有的 project 删除逻辑（含 os.RemoveAll + container stop）

**影响文件**：`server/main.go`、`server/api/projects.go`、`server/store/`（需要查询 balance=0 用户的方法）

---

## 🟡 中优先级

### T13.2 — / 和 @ 命令透传 Claude CLI

**现状**：T13.1 已完成（移除自定义 /@ 交互）。用户输入 `/` 或 `@` 作为普通文本发送。

**目标**：
- `/` 命令：研究 Claude CLI 的 `/compact`、`/clear`、`/model` 等命令协议，实现透传和交互式响应渲染
- `@` 文件补全：输入 `@` 弹出容器内文件树搜索，选择后插入路径，透传给 Claude CLI

**影响文件**：`web/src/components/chat/ChatInput.tsx`、`server/agent/claude_agent.go`

---

## 🟢 低优先级

### T2 — MCP OAuth Connector 安装流程

**现状**：完全未实现。`InstallMCP` 直接存配置，无 OAuth 跳转。

**目标**：
- 研究 MCP OAuth spec（参考 modelcontextprotocol.io）
- `MarketplacePlugin` 新增 `auth_type: "oauth" | "apikey" | "none"` 字段
- 安装 OAuth 类型 MCP 时后端返回 `auth_url`，前端弹 OAuth 窗口，回调后存 token

**备注**：MVP 先支持 apikey 和 none 类型，OAuth 后期实现。与 T1.4（connector 格式支持）配合使用。

---

### T5 — MCP 工具可见性精细控制

**现状**：
- Task/Asset/Workflow/Terminal 面板受 `userFeatures` 位掩码控制
- 但后端传给 Claude 的工具列表未联动 features 位掩码
- NexBox MCP server 未配置时全部不可见

**目标**：
- **T5.1**：根据 features 位掩码过滤传给 Claude 的工具列表（如 `features & 1 == 0` 则不注册 task 相关工具）

**影响文件**：`server/agent/claude_agent.go`

---

### T8.2 — gVisor 容器运行时

**现状**：容器使用默认 runc 运行时。

**目标**：长期可用 gVisor (runsc) 作为容器运行时，进一步隔离系统调用。

**实现思路**：docker run 加 `--runtime=runsc`，需宿主机安装 gVisor。

---

### T10 — 国际化（中英文切换）

**现状**：完全未实现，无 i18n 基础设施。

**目标**：
1. 引入 `react-i18next`
2. 创建 `web/src/i18n/zh.json` + `en.json`
3. 语言偏好存 `localStorage`（`beehub_lang`）
4. 登录界面右上角 + Settings 页提供切换
5. 可分批迁移（先登录界面和主导航）

**备注**：工作量大，建议分阶段实施。

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
| 🟡 中 | T13.2 | / @ 命令透传 Claude CLI | 中 |
| 🟢 低 | T2 | MCP OAuth Connector | 高 |
| 🟢 低 | T5 | MCP 工具可见性精细控制 | 中 |
| 🟢 低 | T8.2 | gVisor 容器运行时 | 低（配置） |
| 🟢 低 | T10 | 国际化 i18n | 高（工作量） |
