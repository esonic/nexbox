# Marketplace 系统设计

> 基于 `scripts/analyze_marketplace.py` 对 60 个插件的分析，结合官方文档。

---

## 一、插件类型总览

| 类型 | 数量 | 组件 | 多 Agent |
|------|------|------|---------|
| **skill** | 10 | SKILL.md + 资源文件 | ✅ 文件形式，通用 |
| **plugin** | 27 | commands / agents / hooks / lsp | ⚠️ Claude 专属 |
| **mcp** | 12 | .mcp.json，连接外部服务 | ✅ MCP 协议通用 |
| **lsp** | 11 | .lsp.json，语言服务器 | ⚠️ Claude 专属 |

### MCP 鉴权分布（最关键）

| auth_type | 插件 |
|-----------|------|
| `none` stdio | context7, firebase, playwright, serena, laravel-boost |
| `apikey` | github (`GITHUB_PERSONAL_ACCESS_TOKEN`), greptile (`GREPTILE_API_KEY`) |
| `oauth_discovery` | gitlab, linear, stripe, supabase |
| `sse_discovery` | asana |
| `oauth` 显式 | slack（含 clientId） |

---

## 二、各类型安装策略

### Skill — 文件复制（现有，不改）

安装 = 复制 skill 目录到 `{project}/.claude/skills/{name}/`，并在项目根建 `.agents → .claude` 软链（供 Codex 用）。全局安装时同步复制到所有已有项目。

### Plugin（commands/agents/hooks/lsp）— `--plugin-dir` 每次传入

安装 = 记录 marketplace 中的目录路径到 DB（不复制文件）。
启动 Claude 时：`--plugin-dir {path}` 逐个传入所有已安装 plugin。
这是**设计上故意的**：每次传入，我们完全控制哪些插件生效。

`--plugin-dir` 会自动识别目录中的 `commands/`、`agents/`、`hooks/`、`.lsp.json`，无需额外处理。

**LSP**：LSP plugin 目录本身没有 `.lsp.json`（只有 README），需要在安装时从 marketplace.json 的 `lspServers` 字段生成 `.lsp.json` 写入一个临时目录，通过 `--plugin-dir` 传入。Binary 由用户/管理员自行安装到 PATH，不是我们的工作。

### MCP Connector — 代理注入鉴权

见第三节。

---

## 三、MCP 鉴权：Go 代理服务器

**核心思路**：在 Go 服务器内嵌一个 MCP 反向代理，对 Claude 暴露 `http://localhost:{port}/mcp-proxy/{name}`，代理层负责注入 Authorization header，Claude 无感知。

```
Claude → localhost/mcp-proxy/gitlab → Go 代理（注入 Bearer token） → gitlab.com/api/v4/mcp
```

### 各 auth_type 处理

| auth_type | Claude 看到的 | 代理做什么 |
|-----------|--------------|-----------|
| `none` stdio | 直接 stdio 命令 | 不走代理，直接传 |
| `none` HTTP | 直接 URL | 不走代理，直接传 |
| `apikey` | `localhost/mcp-proxy/{name}` | 注入 `Authorization: Bearer {stored_key}` |
| `oauth_discovery` | `localhost/mcp-proxy/{name}` | 注入 Bearer token，懒刷新 |
| `sse_discovery` | `localhost/mcp-proxy/{name}` | 透传 SSE + 注入 auth |
| `oauth` (Slack) | `localhost/mcp-proxy/{name}` | 同 oauth_discovery，clientId 来自 .mcp.json |

### OAuth 流程（Authorization Code + PKCE）

1. 用户点击 "Connect" → 前端调 `POST /api/connectors/{name}/oauth/start`
2. 后端做 OAuth Discovery（`GET {url}/.well-known/oauth-authorization-server`），生成 PKCE，返回 authorization_url
3. 前端打开新 tab → 用户在服务授权 → 重定向到 `GET /api/connectors/oauth/callback`
4. 后端换取 token，加密存 `mcp_oauth_tokens` 表，WS 推送成功
5. 代理收到请求时懒刷新：`expires_at - now < 5min` 则用 refresh_token 换新 token
6. 无 refresh_token 且 401 时：WS 推 `connector:reauth_required`，前端提示重新授权

---

## 四、数据模型

```
installed_mcps（已有）
  + auth_type 字段（新增）
  ← EnvValues 存 API Key（已有）

mcp_oauth_tokens（新建）
  mcp_name, project_id, access_token, refresh_token,
  expires_at, token_endpoint, client_id

installed_plugin_entries（新建）
  name, project_id, install_path, installed_at

installed_skills（已有，不改）
```

---

## 五、需要做的事

### P1 — marketplace 解析修复

- `loadMarketplaceJSON` 中，source 指向子目录时，检测 `.mcp.json`（→ type=mcp，解析 auth_type/env_vars）、`commands/`、`agents/`、`hooks/`、`skills/`（→ components）
- `lspServers` 字段 → type=lsp
- remote git source → 标记不可安装
- category、homepage 传到前端

### P1 — Plugin 安装

- 新建 `installed_plugin_entries` 表
- 安装时存路径到 DB
- 启动 Claude 时从 DB 取所有路径，逐个加 `--plugin-dir`
- LSP plugin：从 marketplace.json 的 `lspServers` 生成 `.lsp.json` 写临时目录，通过 `--plugin-dir` 传入

### P1 — MCP API Key 弹窗完善

- 安装时弹窗收集 env vars，存入 `installed_mcps.env_values`
- 代理层从 env_values 注入 header

### P2 — Go MCP 代理服务器

- 路由：`/mcp-proxy/{name}/{path...}`，反向代理到真实 URL
- apikey：注入 header
- `installed_mcps` 加 `auth_type` 字段

### P2 — OAuth 完整流程

- `mcp_oauth_tokens` 表
- `POST /api/connectors/{name}/oauth/start`（Discovery + PKCE）
- `GET /api/connectors/oauth/callback`（换 token + 存 DB + WS 推送）
- 代理懒刷新
- Slack：跳过 Discovery，直接用 `.mcp.json` 中的 `oauth.clientId`

### P3 — SSE 代理（Asana）

- 代理透传 SSE 连接，注入 Authorization header

### 暂不做 — Remote Git Plugins

marketplace.json 中 source 为外部 GitHub URL 的 16 个第三方插件（slack, figma, Notion, atlassian 等），当前标记为不可安装。等核心功能稳定后再支持（`git clone` 到本地后走普通 plugin 流程）。
