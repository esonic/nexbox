# BeeHub 待办任务列表（第二轮）

> 整理时间：2026-03-08。基于 todo.md 的剩余未实现项整理。

---

## 已完成项（供参考，不再列入）

| 编号 | 任务 | 状态 |
|------|------|------|
| T3.1 | 容器资源限制（memory/cpu/pids/disk） | ✅ 已实现 |
| T4 | Project 创建 UX 简化（前后端） | ✅ 已实现 |
| T6 | Files/Terminal/Git 按钮移至顶部工具栏 | ✅ 已实现 |
| T7 | 文件上传/下载（后端 + 前端） | ✅ 已实现 |
| T9.1 | 软删除清理改为 1 天（后端 + 前端文案） | ✅ 已实现 |
| T9.2 | Session 数量上限（20/project） | ✅ 已实现 |
| T9.3 | 消息数量上限（100/session，每小时清理） | ✅ 已实现 |
| T11 | 余额显示（侧边栏 tokens + ¥） | ✅ 已实现 |
| T12 | 模型选择（Sonnet/Haiku，前后端联动） | ✅ 已实现 |
| T13.1 | 移除 / 和 @ 特殊交互 | ✅ 已实现 |
| T1.1 | 移除云端 packages 系统（后端路由 + 前端 API + 页面） | ✅ 已实现 |

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

### T3.3 — 容器 TTL / 空闲回收

**现状**：容器创建后一直运行，无超时回收机制。

**目标**：定期检查容器最后活跃时间，超 1h 无 agent 执行则 `docker stop`。

**实现思路**：
- `container.go` 或 `main.go` 新增 goroutine，每 10 分钟检查一次
- 记录用户最后容器相关操作时间，包括消息、文件上传等（内存 map 或 DB 字段）
- 超时则 `docker stop`（已有 `--rm`，stop 后自动删除）
- 下次需要时自动重建容器

**影响文件**：`server/agent/container.go`、`server/main.go`

---

### T3.4 — Project 删除时清理宿主机目录

**现状**：Project 删除后，宿主机上的 bind-mount 目录未清理。

**目标**：`DELETE /api/projects/{id}` 时同步 `os.RemoveAll` 宿主机工作目录。

- 另外，token 剩余 quota 为 0 的用户，30 天后自动清理掉他的 project，相当于删除 project

**影响文件**：`server/api/projects.go`

---

### T9.4 — 上传文件清理

**现状**：T7 文件上传已实现，但 project 删除时不会清理上传到工作目录的文件。

**目标**：project 删除时清理关联的上传文件/工作目录。与 T3.4 合并实现。

---

### T9.5 — SQLite 定期维护

**现状**：无 SQLite 维护逻辑，长期运行 WAL 文件和碎片会增长。

**目标**：定期执行 `PRAGMA wal_checkpoint(TRUNCATE)` + `VACUUM`。

**实现思路**：
- 在 `main.go` 的定期清理 goroutine 中加入，每天执行一次
- 仅 SQLite 模式下生效

**影响文件**：`server/main.go`、`server/store/sqlite_core.go`（或 `db.go`）

---

## 🟡 中优先级

### T1.2 — 适配 Anthropic 官方仓库格式

**现状**：T1.1 已完成（移除云端系统，前端改为调用本地插件 API）。但本地插件仅支持 `*.json` 文件格式，不支持 Anthropic 官方仓库的目录结构。

**目标**：
- 官方 skill 仓库（`anthropics/skills`）：每个 skill 是一个子目录，包含 README/manifest
- 官方 plugin 仓库（`anthropics/claude-plugins-official`）：类似结构
- 管理员 `git clone` 到 `marketplace_dir/` 后，后端自动扫描识别

**实现思路**：
- 后端新增目录 scanner（递归读 README/manifest/package.json）
- 现有 `*.json` 格式继续兼容
- 建议目录结构：
  ```
  data/marketplace/
    skills/         ← git clone anthropics/skills
    plugins/        ← git clone anthropics/claude-plugins-official
    custom/*.json   ← 自定义 JSON 格式
  ```

**影响文件**：`server/api/marketplace.go`（`loadMarketplacePlugins` 函数）

---

### T1.4 — Connector 官方仓库调研

**现状**：MCP Connector（OAuth 类型的 MCP server）的官方仓库定义位置未确定。

**目标**：支持 claude 官方网站上的 connector，例如连接到 gmail、notion 等。一是看从哪里拿到这份 connector 列表
二是看我们怎么本地维护，并支持用户使用这些 mcp（重点应该是如何授权）

---

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

**备注**：MVP 先支持 apikey 和 none 类型，OAuth 后期实现。

---

### T5 — MCP 工具可见性精细控制

**现状**：
- Task/Asset/Workflow/Terminal 面板受 `userFeatures` 位掩码控制
- 但后端传给 Claude 的工具列表未联动 features 位掩码
- NexBox MCP server 未配置时全部不可见

**目标**：
- **T5.1**：根据 features 位掩码过滤传给 Claude 的工具列表（如 `features & 1 == 0` 则不注册 task 相关工具）
- **T5.2**：确认 `ask_question` 工具始终注册，不受任何 feature flag 过滤

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

### T14 — Claude CLI 交互式响应支持

**现状**：
- Claude CLI 通过 `AskUserQuestion` 工具可向用户展示交互式选择界面（注意是claude原生工具，我们自己的ask_question未来不需要了）
- `ToolCallCard.tsx` 已有 `AskUserQuestion` 的渲染实现
- 但 `claude -p` JSON streaming 模式下 `AskUserQuestion` 的具体格式未研究清楚

**目标**：
1. 研究 Claude CLI `--output-format stream-json` 模式下 `AskUserQuestion` 的 JSON 格式
2. 确认 `ToolCallCard.tsx` 的渲染逻辑是否完整覆盖所有交互类型
3. 确保 WebSocket 协议能正确中继 tool_use → 前端渲染 → 用户选择 → tool_result 回传

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
| 🔴 高 | T3.3 | 容器空闲回收 | 中 |
| 🔴 高 | T3.4 + T9.4 | Project 删除时清理目录/文件 | 低 |
| 🔴 高 | T9.5 | SQLite 定期维护 | 低 |
| 🟡 中 | T1.2 | 适配 Anthropic 官方仓库格式 | 中 |
| 🟡 中 | T1.4 | Connector 官方仓库调研 | 低（调研） |
| 🟡 中 | T13.2 | / @ 命令透传 Claude CLI | 中 |
| 🟢 低 | T2 | MCP OAuth Connector | 高 |
| 🟢 低 | T5 | MCP 工具可见性精细控制 | 中 |
| 🟢 低 | T8.2 | gVisor 容器运行时 | 低（配置） |
| 🟢 低 | T10 | 国际化 i18n | 高（工作量） |
| 🟢 低 | T14 | Claude CLI 交互式响应 | 中（需调研） |
