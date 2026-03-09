# BoxNex 待办任务列表

> 整理时间：2026-03-08。每项包含：现状、还需做什么、大致实现思路。

---

## T1 — Marketplace：MCP / Skill / Plugin / Connector 整合

### 现状

后端存在两套并行的 marketplace 系统，**决定只保留本地系统**，移除云端 packages 系统（review/star/version 不需要）。

| 系统 | 后端路由 | 状态 | 说明 |
|------|---------|------|------|
| **本地插件系统** | `/api/marketplace/plugins`<br>`/api/marketplace/installed` | **保留，需前端对接** | 管理员 clone 仓库或放 JSON，用户浏览安装 |
| ~~云端 packages 系统~~ | `/api/marketplace/packages` 等 | **删除** | review/star/version 不需要 |

**权限模型**：用户不能自己上传/发布，只能浏览和安装。所有 skill/plugin/MCP 均由管理员在后台配置（clone 仓库或放置 JSON 文件）。

本地插件 JSON 格式：
```json
{
  "id": "my-mcp-server", "name": "My MCP Server",
  "type": "mcp", "version": "1.0.0",
  "command": "npx", "args": ["-y", "@my/mcp-server"],
  "env_vars": ["API_KEY"]
}
```

Anthropic 官方 skill/plugin 仓库格式待研究，需要适配目录结构。

### 还需做什么

**T1.1 移除云端 packages 系统**：删除后端 `/api/marketplace/packages` 相关路由和 handler，删除前端 `marketplaceApi.listPackages` 等调用。前端 MarketplacePage 改为调用本地 `/api/marketplace/plugins` 接口。

**T1.2 适配 Anthropic 官方仓库格式**
- 官方 skill 仓库：https://github.com/anthropics/skills（每个 skill 是子目录）
- 官方 plugin 仓库：https://github.com/anthropics/claude-plugins-official
- 第三方仓库示例：https://github.com/quant-sentiment-ai/claude-equity-research（管理员可选择性 clone）
- 管理员 `git clone` 到 `marketplace_dir/skills/<name>/` 和 `marketplace_dir/plugins/<name>/`
- 后端新增目录 scanner（递归读 README/manifest），现有 JSON 格式继续兼容

**T1.3 目录结构建议**：
```
data/marketplace/
  skills/         ← git clone anthropics/skills
  plugins/        ← git clone anthropics/claude-plugins-official
  custom/*.json   ← 自定义 JSON 格式
```

**T1.4** Connector 官方仓库定义位置待调研（可能在 modelcontextprotocol.io）

---

## T2 — MCP OAuth Connector 安装流程

### 现状

完全未实现。`InstallMCP` 直接存配置，无 OAuth 跳转。

### 还需做什么

- 研究 MCP OAuth spec（参考 modelcontextprotocol.io）
- `MarketplacePlugin` 新增 `auth_type: "oauth" | "apikey" | "none"` 字段
- 安装 OAuth 类型 MCP 时后端返回 `auth_url`，前端弹 OAuth 窗口，回调后存 token
- **列为后期功能**，MVP 先支持 apikey 和 none 类型

---

## T3 — 容器隔离：全局配置隔离 + 资源限制

### 现状

每个 **project** 一个 Docker 容器（不是每用户一个）。容器内有独立 Claude CLI 环境。

- 容器内的全局配置（`~/.claude/`）只影响该容器，**不影响其他用户**
- 容器有 `--rm`：停止后容器层自动删除
- `/workspace` 是 bind-mount 到宿主机，**文件永久保留**
- 容器内非 `/workspace` 路径的文件（`/tmp` 等）随容器销毁而消失
- **当前无任何资源限制**（内存/CPU/进程数/网络）

### 还需做什么

**T3.1 容器资源限制**（高优先级，生产必要）

在 `agent/container.go` 的 `docker run` 加：
```go
"--memory", "4g",
"--cpus", "1.0",
"--pids-limit", "200",
"--storage-opt", "size=1g",   // 容器层磁盘限 1G（需 overlay2）
```

**T3.2 网络白名单**：容器只允许访问 Claude API 及特定域名，其他地址（含 localhost 其他端口）一律禁止。实现方式：
- 创建自定义 Docker network（`--network=boxnex-isolated`）
- 容器内 iptables 规则或宿主机 iptables OUTPUT chain 限制目标 IP
- 白名单域名列表通过 config 配置，定期 DNS 解析更新 iptables 规则
- 或使用 HTTP 代理（squid/tinyproxy）作为容器唯一出口，代理层做域名白名单

**T3.3 容器 TTL / 空闲回收**：定期检查容器最后活跃时间，超 1h 无 agent 执行则 `docker stop`。

**T3.4 超大文件**：Project 删除时同步 `os.RemoveAll` 宿主机目录。

---

## T4 — Project 创建 UX 简化

### 现状

- `ProjectsPage.tsx` 有本地目录路径输入（用户需填 `git_repo_path`）
- 侧边栏底部有 "Projects" 按钮，跳转 Projects 页面
- 侧边栏有 "Unlink to local" 按钮（清空 `git_repo_path`）

### 还需做什么

**T4.1 后端**：`CreateProject` 不再要求 `git_repo_path`。Project 的工作目录就是容器内的 `/workspace`（bind-mount 到宿主机），无需额外在 datadir 建目录。后端只需记录 project 元数据（name, desc），容器启动时自动 bind-mount 对应宿主机目录到 `/workspace`。

**T4.2 前端**：
- 创建 project 弹窗只需 Name + Description（去掉路径输入）
- 移除左下角 "Projects" 按钮
- Sessions 标题右侧（trash 左边）加"新建 Project"按钮，点击弹窗
- "Unlink to local" 改为"删除 Project"，调用 DELETE `/api/projects/{id}`，需输入 project 名确认

---

## T5 — MCP 工具显示控制（ask_question 保留）

### 现状

- `ask_question` **已有完整实现**：`ToolCallCard.tsx` 特殊渲染，聊天中显示为交互卡片
- Task/Asset/Workflow/Terminal 面板受 `userFeatures` 位掩码（bit 0-3）控制
- 后端传给 Claude 的工具列表由 MCP server 配置决定，尚未联动 features 位掩码
- **当前工具不显示的机制**：BoxNex MCP server 未配置（`MCPServerURL` 为空），所以 `claude_agent.go` 不注册任何 MCP 工具。`ask_question` 等工具是 MCP 工具集的一部分，MCP server 整体未启用时全部不可见

### 还需做什么

**T5.1 后端**：根据 features 位掩码过滤传给 Claude 的工具列表（如 features & 1 == 0 则不注册 task 相关工具）。

**T5.2** 确认 `ask_question` 工具始终注册，不受任何 feature flag 过滤。

---

## T6 — Files / Terminal / Git 按钮位置调整

### 现状

- Task / Asset / Workflow 按钮：右侧边栏顶部（features bit 0/1/2 控制）
- Files 按钮：右侧面板中部（无 features 控制）
- Git 按钮：右侧面板中部（无 features 控制）
- Terminal：底部，features bit 3 控制，`Ctrl+\`` 快捷键

### 还需做什么

**T6.1** Files 移到右侧边栏顶部，与 Task/Asset/Workflow 同行，始终显示。

**T6.2** Terminal 和 Git 也移到同一行，默认隐藏，受 features 控制（Terminal=bit3，Git 新增 bit4 或共用）。`Ctrl+\`` 快捷键同样受 features 控制，无权限时无效。

---

## T7 — 文件上传 / 下载

### 现状

**完全未实现**。`api/files.go` 无 upload/download handler，前端无上传 UI。

### 还需做什么

**T7.1 后端**：
```
POST /api/projects/{projectId}/files/upload   multipart/form-data: file + path
GET  /api/projects/{projectId}/files/download?path=src/main.go
```

**T7.2 前端**：Files 面板文件树增加右键下载、拖拽/点击上传 UI。

安全要点：复用已有 `isSubPath` 路径遍历检查。

---

## T8 — 容器安全隔离（补充）

### 还需做什么

**T8.1** 网络隔离已合并到 T3.2。

**T8.2** 长期可用 gVisor (runsc) 作为容器运行时，进一步隔离系统调用。

---

## T9 — 数据清理 / 防无限增长

### 现状

已有：
- 每小时清理 30 天前软删除的 session + worktree（**改为 1 天**）
- 每小时清理旧 message part detail（保留最近 50 session）（**不再需要，见下**）

缺失：上传文件、项目磁盘、DB 无限增长的治理。

### 还需做什么

**T9.1 软删除清理周期**：改为清理 **1 天前**（而非 30 天）软删除的 session 和 worktree。前端 Trash 列表的提示文字也改为"1 天后自动删除"。

**T9.2 Session 数量上限**：每个 project 最多 **20 个** active session。超过时：
- 禁止创建新 session，前端提示"已达上限，请删除旧 session"
- Trash 中的恢复按钮也不可用（active 数 ≥ 20 时），同样提示已达上限
- 有了 session 数量限制，message part detail 的"保留最近 50 session"清理逻辑**可以移除**

**T9.3 消息数量上限**：每个 session 最多保留最近 **100 条**消息（含用户和 AI 消息）。每小时定期清理超出部分。

**T9.4 上传文件清理**（T7 实现后）：project 删除时清理上传目录。

**T9.5 SQLite 维护**：定期 `PRAGMA wal_checkpoint` + `VACUUM`。

---

## T10 — 国际化（中英文切换）

### 现状

**完全未实现**。无 i18n 基础设施。

### 还需做什么

1. 引入 `react-i18next`
2. 创建 `web/src/i18n/zh.json` + `en.json`
3. 语言偏好存 **localStorage**（`boxnex_lang`），不是 config.json
4. 登录界面右上角 + Settings 页提供切换
5. 工作量大，可分批迁移（先登录界面和主导航）

---

## T11 — 用户 Token 余额显示

### 现状

`GET /api/balance` 已实现，返回 `balance_tokens`、`balance_cny`。前端无任何展示。

### 还需做什么

左侧边栏 Marketplace 上方、分隔线上面加余额显示：
```
💰 1,234,567 tokens  ≈ ¥18.52
```
- 登录后调用 `/api/balance` 存入 Zustand store
- agent 执行结束后刷新（监听 WS settle 事件）

---

## T12 — 模型选择

### 现状

- `Session.Agent` 字段记录 agent 类型，前端 agent 选择 codex 已隐藏
- 聊天界面**无模型选择器**
- Claude CLI 支持 `--model` 参数

### 还需做什么

**T12.1 前端**：聊天输入框旁 agent 下拉增加模型选项：
- agent=claude → 可选 claude-sonnet-4-6（默认）、claude-haiku-4-5；**Opus 暂不开放**（成本高）
- agent=codex（未来）→ 对应模型

**T12.2 后端**：`CreateSessionRequest` 加 `Model string`；`claude_agent.go` 构建命令时注入 `--model <model>`。

**T12.3 联动**：agent 切换时重置模型选项。

---

## T13 — / 和 @ 命令处理

### 现状

`ChatInput.tsx` 有 slash command 菜单（`/design`、`/revise`、`/code`、`/review`、`/e2e_test`）和 `@` mention（task/asset/workflow），都是 BoxNex 自定义功能。

### 还需做什么

**阶段一（当前）**：直接移除 `/` 和 `@` 的特殊交互。用户输入 `/` 或 `@` 不触发任何菜单/补全，作为普通文本发送。

**阶段二（后续）**：
- 研究 Claude CLI 的 `/` 命令协议（`/compact`、`/clear`、`/model` 等），实现透传和交互式响应渲染
- `@` 文件补全：输入 `@` 弹出容器内文件树搜索，选择后插入路径，透传给 Claude CLI

---

## 优先级建议

| 优先级 | 任务 | 理由 |
|--------|------|------|
| 🔴 高 | T4 Project 创建简化 | 核心使用流程 |
| 🔴 高 | T11 余额显示 | 计费功能完整性 |
| 🔴 高 | T12 模型选择 | 用户需要选 sonnet/haiku |
| 🔴 高 | T3.1-3.2 容器资源+网络限制 | 生产安全必要 |
| 🔴 高 | T9.1-9.2 清理周期+session上限 | 数据治理基础 |
| 🔴 高 | T13 阶段一：移除 /@ 特殊交互 | 简单改动，立即生效 |
| 🟡 中 | T1 Marketplace 整合（仅本地） | 管理员配置 skill/plugin 基础 |
| 🟡 中 | T6 按钮位置调整 | UI 一致性 |
| 🟡 中 | T7 文件上传下载 | 基础能力 |
| 🟢 低 | T10 i18n | 工作量大 |
| 🟢 低 | T2 OAuth Connector | 后期功能 |
| 🟢 低 | T5 工具可见性精细控制 | 当前基本可用 |
| 🟢 低 | T13 阶段二：/ @ 透传 Claude | 后续实现 |
| 🟢 低 | T14 Claude CLI 交互式响应支持 | 需研究协议 |

---

## T14 — Claude CLI 交互式响应支持

### 现状

Claude CLI 通过 `ask_question` 工具可以向用户展示交互式选择界面（多选项 + 自由输入 + "Chat about this"）。当前 BoxNex 的 `ToolCallCard.tsx` 已有 `ask_question` 的渲染实现。

但通过命令行（`claude -p` 或 JSON streaming）调用时，这种交互式工具调用在 JSON 返回中的具体格式尚未研究清楚：
- tool_use block 的 input schema 是什么样的（选项列表、描述、允许自由输入等字段）
- 用户选择后如何通过 tool_result 返回给 CLI
- 是否有超时/默认行为

### 还需做什么

1. 研究 Claude CLI `--output-format stream-json` 模式下 `ask_question` 的 JSON 格式
2. 确认当前 `ToolCallCard.tsx` 的渲染逻辑是否已完整覆盖所有交互类型
3. 确保 WebSocket 协议能正确中继交互式 tool_use → 前端渲染 → 用户选择 → tool_result 回传

---

## 备忘（产品方向）

- context 占用量查看
- 手机端适配
理念：
- 云端 claude cowork、code 能力
- 安全：隔离，可授予 claude 所有权限
- 便宜：可多人共享 pro max 订阅
- 云办公：云空间，手机可随时访问
