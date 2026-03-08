# Marketplace 方案设计

## 一、Claude Code Plugin 体系调研

### 1.1 仓库结构

Claude 的 marketplace 由**多个 git 仓库**组成，每个仓库根目录有 `.claude-plugin/marketplace.json`，列出该仓库提供的所有插件。

我们目前有 3 个仓库：
- `claude-plugins-official` — Anthropic 官方插件目录（56 个插件）
- `skills` — Anthropic 示例 skills 集合（3 组，含 17 个 skill）
- `claude-equity-research` — 第三方社区插件（1 个 command）

### 1.2 插件的 6 种组件

一个 plugin 是一个目录，可包含以下任意组合：

```
plugin-name/
├── .claude-plugin/
│   └── plugin.json          # 插件元数据（name, description, author）
├── .mcp.json                # MCP Server 配置（可选）
├── commands/                # 斜杠命令 *.md（可选）
│   └── help.md              # /help 命令
├── agents/                  # Agent 定义 *.md（可选）
│   └── analyzer.md          # 可被 Claude 调用的 sub-agent
├── skills/                  # Skill 定义（可选）
│   └── my-skill/
│       └── SKILL.md         # 领域知识/能力定义
├── hooks/                   # 事件钩子（可选）
│   ├── hooks.json           # 钩子注册（PreToolUse, PostToolUse, Stop 等）
│   └── pretooluse.py        # 钩子脚本
└── README.md
```

| 组件 | 格式 | 触发方式 | 说明 |
|------|------|---------|------|
| **Command** | `commands/*.md` | 用户输入 `/command-name` | YAML frontmatter 定义 description, allowed-tools, argument-hint |
| **Skill** | `skills/*/SKILL.md` | Claude 自动匹配 | YAML frontmatter 定义 name, description；Claude 根据上下文自动使用 |
| **Agent** | `agents/*.md` | Claude 决定 spawn | YAML frontmatter 定义 name, description, model, tools, color |
| **Hook** | `hooks/hooks.json` + 脚本 | 事件驱动 | PreToolUse/PostToolUse/Stop/UserPromptSubmit |
| **MCP Server** | `.mcp.json` | 自动连接 | stdio 或 HTTP transport，提供工具给 Claude |
| **LSP Server** | `lspServers` 字段 (marketplace.json 内联) | 自动连接 | 代码智能（无独立目录） |

### 1.3 marketplace.json 的两种 source

```jsonc
// 本地目录引用（40 个）— 插件代码在同一仓库内
{ "name": "github", "source": "./external_plugins/github" }

// 远程 git URL（16 个）— 插件代码在外部仓库
{ "name": "slack", "source": { "source": "url", "url": "https://github.com/slackapi/slack-mcp-plugin.git" } }
```

Claude Code CLI 安装远程插件时会 `git clone` 到本地。

### 1.4 MCP 鉴权的 4 种模式

通过分析所有 `.mcp.json`，归纳出 4 种鉴权模式：

| 模式 | .mcp.json 特征 | 安装流程 | 示例 |
|------|---------------|---------|------|
| **无鉴权 (stdio)** | `"command": "npx"` | 直接安装运行 | firebase, context7, playwright, serena |
| **API Key** | `headers.Authorization: "Bearer ${VAR}"` | 安装时提示用户输入 token | github (`GITHUB_PERSONAL_ACCESS_TOKEN`), greptile (`GREPTILE_API_KEY`) |
| **MCP 协议 OAuth** | HTTP URL，无 auth 字段 | 连接时服务端返回 401，客户端发起浏览器 OAuth 流程 | gitlab, asana, linear, supabase, stripe |
| **自定义 OAuth** | 显式 `"oauth": { "clientId": "..." }` | 客户端按 clientId 发起 OAuth | slack |

关键发现：
- GitLab、Asana、Linear、Supabase、Stripe 的 `.mcp.json` 里**没有任何 auth 配置**
- 它们依赖 **MCP 协议级别的 OAuth Discovery**：客户端连接 → 服务端 401 → 客户端请求 `/.well-known/oauth-authorization-server` → 发起浏览器授权
- 这是 MCP Streamable HTTP Transport 规范的标准行为
- Claude Code CLI 内置了这个 OAuth 流程（截图中 GitLab 的 "在浏览器中授权"）

### 1.5 LSP 插件

11 个 LSP 插件（typescript-lsp, pyright-lsp, gopls-lsp 等）比较特殊：
- 它们**没有独立目录**，没有 `.claude-plugin/plugin.json`
- 配置内联在 marketplace.json 的 `lspServers` 字段中
- Claude Code CLI 直接读取 marketplace.json 中的 lspServers 配置来启动语言服务器

---

## 二、现状差距分析

### 2.1 我们的架构

```
用户 → Web UI → API Server → loadMarketplacePlugins() → 扫描本地目录
                                                         ↓
                                                    3 种检测格式:
                                                    1. .claude-plugin/plugin.json (独立插件)
                                                    2. .claude-plugin/marketplace.json (仓库索引)
                                                    3. SKILL.md (独立 skill)
```

安装后：
- MCP → `installed_mcps` 表 → 注入到 agent 的 MCP server 列表
- Skill → 复制到项目 `.claude/skills/` → agent 通过 `--plugin-dir` 读取
- Plugin → `installed_plugins` 表 → agent 通过 `--plugin-dir` 读取

### 2.2 差距一览

| 能力 | Claude Code CLI | 我们的实现 | 差距 |
|------|----------------|-----------|------|
| marketplace.json 解析 | 完整读取所有字段 | 只读 name/desc/version/author/tags | 缺少 source, category, homepage, lspServers, strict |
| 识别插件子目录内容 | 读取 source 指向的子目录 | loadMarketplaceJSON 不读子目录 | **GitHub/GitLab 等 connector 未被识别为 MCP 类型** |
| API Key 输入 | 安装时 interactive prompt | 无 | **需要环境变量输入弹窗** |
| MCP 协议 OAuth | 内置浏览器 OAuth 流程 | 无 | 需长期支持（Claude agent 侧处理） |
| 自定义 OAuth | 按 clientId 发起 | 无 | 暂不支持（同上） |
| remote source (git URL) | git clone 安装 | 不支持 | 暂不需要（可预先 clone 到本地） |
| Commands | `/command-name` 触发 | 不支持 | 我们的 agent 是 Claude Code CLI，它原生支持 |
| Agents | Claude 自动 spawn | 不支持 | 同上，CLI 原生支持 |
| Hooks | 事件驱动脚本 | 不支持 | 同上，CLI 原生支持 |
| Skills | SKILL.md 注入上下文 | 已支持 | OK |
| MCP Server | .mcp.json 连接 | 已支持 | OK |
| LSP Server | lspServers 字段 | 不支持 | 低优先级 |
| Plugin 整体安装 | 复制整个插件目录 | 分别安装 MCP/Skill/Plugin | 需要统一 |

### 2.3 核心问题

**问题 1：loadMarketplaceJSON 没有读取插件子目录**

marketplace.json 的 `source: "./external_plugins/github"` 指向子目录，但我们没去读那个子目录的 `.mcp.json` 和 `plugin.json`。导致：
- 56 个插件中大量 connector (GitHub/GitLab/Stripe...) 显示为普通 "plugin" 而非 "mcp"
- 没有提取 env_vars 和 auth_type
- 没有提取 mcp_config

**问题 2：Plugin 应该是一个整体**

Claude 的 plugin 是一个**包含多种组件的整体**（可能同时有 MCP + Skills + Commands + Hooks），但我们把它拆成了 3 个独立的安装流程（installMCP / installSkill / installPlugin）。应该统一为"安装一个 plugin"，自动处理其中所有组件。

**问题 3：API Key 输入**

安装需要 env vars 的 connector 时，没有弹窗让用户输入值。

---

## 三、方案设计

### 3.1 目标

1. marketplace.json 中所有插件正确分类显示
2. 识别每个插件包含的组件（mcp, skills, commands, agents, hooks）
3. API Key 类型的 connector 安装时弹窗输入 env vars
4. OAuth 类型正确标记，暂不支持安装
5. 插件作为整体安装（安装一个 plugin = 安装其所有组件）

### 3.2 数据模型调整

`MarketplacePlugin` 新增字段：

```go
type MarketplacePlugin struct {
    // 现有字段...
    ID          string   `json:"id"`
    Name        string   `json:"name"`
    Description string   `json:"description"`
    Type        string   `json:"type"`        // 改为主类型: "mcp", "skill", "plugin", "lsp"
    Version     string   `json:"version"`
    Author      string   `json:"author,omitempty"`
    Source      string   `json:"source,omitempty"`
    MCPConfig   string   `json:"mcp_config,omitempty"`
    AuthType    string   `json:"auth_type,omitempty"`    // "none", "apikey", "oauth", "oauth_discovery"
    EnvVars     []string `json:"env_vars,omitempty"`
    InstallPath string   `json:"install_path,omitempty"`
    Command     string   `json:"command,omitempty"`
    Args        []string `json:"args,omitempty"`
    IconURL     string   `json:"icon_url,omitempty"`

    // 新增字段
    Category    string   `json:"category,omitempty"`     // "development", "productivity", "security"...
    Homepage    string   `json:"homepage,omitempty"`
    Components  []string `json:"components,omitempty"`   // ["mcp","skills","commands","agents","hooks"]
    RemoteURL   string   `json:"remote_url,omitempty"`   // git URL for remote-source plugins
}
```

### 3.3 auth_type 重新定义

| auth_type | 含义 | UI 行为 |
|-----------|------|---------|
| `"none"` | 无需鉴权（stdio 命令，或无 auth 的本地服务） | 直接 Install |
| `"apikey"` | 需要用户提供 API Key / Token | Install 弹窗填 env vars |
| `"oauth_discovery"` | MCP 协议 OAuth（服务端 401 触发） | 标记"需浏览器授权"，暂允许安装（由 agent 运行时处理） |
| `"oauth"` | 显式 OAuth clientId | 标记"OAuth 暂不支持"，禁止安装 |

关键决策：`oauth_discovery` 类型（GitLab/Stripe/Asana 等）**允许安装**。

**已验证的技术链路**：
1. 用户安装 connector → 后端存入 `installed_mcps` 表
2. 发消息时 → `resolveInstalledMCPsAndMap()` 从 DB 读取 → 构建 `MCPServerEntry` 列表
3. `claude_agent.go:134` 遍历 `ExtraMCPServers`，写入 `mcpServers` JSON
4. 传给 Claude CLI `--mcp-config /tmp/beehub-mcp-xxx.json`
5. Claude CLI (v2.1.71) 内置 MCP HTTP transport 的 OAuth 处理：
   - 连接 HTTP URL → 服务端 401 → CLI 自动发现 OAuth 端点 → 打开浏览器授权
   - CLI 的 `claude mcp add` 有 `--callback-port`, `--client-id`, `--client-secret` 选项

**但有一个问题**：我们的 agent 是通过 `--mcp-config` JSON 文件传入 MCP server 配置的。
这个 JSON 只有 `type`, `url`, `headers` 等基本字段。对于 Slack 那种需要 `oauth.clientId` 的，
这些 OAuth 字段能否通过 JSON config 传递给 CLI，还需要验证。

**对于 oauth_discovery 型** (GitLab/Stripe/Asana/Linear/Supabase)：
- 这些的 `.mcp.json` 只有 `{ "type": "http", "url": "..." }`
- 完全可以通过我们的 `--mcp-config` JSON 传给 CLI
- CLI 连接时自动处理 401 → OAuth → 浏览器授权
- **但问题是**：在我们的 Web 场景中，CLI 运行在服务端，"打开浏览器"在服务端无意义
- 这意味着 **oauth_discovery 在我们的 Web 架构下也无法工作**，除非用户在同一台机器访问

**对于 Slack 的显式 OAuth**：
- `.mcp.json` 有 `"oauth": { "clientId": "..." }` 字段
- 我们的 `MCPServerEntry` 结构不包含 oauth 字段，无法传递给 CLI
- 暂不支持

**结论**：
- `none` (stdio/无 auth HTTP) → 可安装，可用
- `apikey` (API Key) → 可安装，需弹窗填 token，可用
- `oauth_discovery` (协议 OAuth) → 允许安装，但需要**用户和服务端在同一台机器**才能完成浏览器授权；远程部署场景下不可用。UI 标记 "Requires browser auth"
- `oauth` (显式 OAuth) → 暂不支持安装

### 3.4 loadMarketplaceJSON 改造

```
marketplace.json 中每个 plugin entry:
  ├── source 是本地路径 (./plugins/xxx)?
  │   ├── 是 → 读取子目录:
  │   │   ├── .claude-plugin/plugin.json → 提取元数据
  │   │   ├── .mcp.json → 提取 MCP 配置、auth_type、env_vars
  │   │   ├── skills/ → 标记 has_skills
  │   │   ├── commands/ → 标记 has_commands
  │   │   ├── agents/ → 标记 has_agents
  │   │   └── hooks/ → 标记 has_hooks
  │   └── Type 判定:
  │       ├── 有 .mcp.json → type="mcp"
  │       ├── 有 lspServers → type="lsp"
  │       ├── 有 skills/ 但无 mcp → type="skill"
  │       └── 其他 → type="plugin"
  │
  ├── source 是 URL 对象?
  │   └── 标记为 remote，RemoteURL=url，type 从目录结构推断
  │       （远程插件暂不支持安装，显示 "Clone required"）
  │
  └── entry 内联 lspServers?
      └── type="lsp"，标记为 LSP 暂不支持
```

### 3.5 安装流程改造

**当前**：用户在 Connectors/Skills/Plugins 三个 tab 中分别安装

**目标**：
- Tab 改为按 `category` 分组 或 统一列表 + 筛选
- 安装是**整体安装一个 plugin**
- 安装时根据插件内容自动处理：
  1. 复制/记录插件目录到 installed_plugins（`--plugin-dir` 注入给 agent）
  2. 如果有 `.mcp.json`，同时注册到 installed_mcps
  3. 如果有 `env_vars`，弹窗让用户输入
- 卸载时自动清理所有组件

### 3.6 API Key 输入弹窗

前端安装 API Key 类型的 connector 时：

```
┌─────────────────────────────────────┐
│  Install github                     │
│                                     │
│  This connector requires:           │
│                                     │
│  GITHUB_PERSONAL_ACCESS_TOKEN       │
│  ┌─────────────────────────────┐    │
│  │ ghp_xxxxxxxxxxxx            │    │
│  └─────────────────────────────┘    │
│                                     │
│           [Cancel]  [Install]       │
└─────────────────────────────────────┘
```

值通过 `env` 字段传给 `POST /api/marketplace/installed-mcps`，存入 `env_values`，运行时由 `resolveInstalledMCPsAndMap` 注入。

---

## 四、分步实施计划

### Phase 1：修复插件检测与分类（后端）

**目标**：所有 56 个官方插件 + 社区插件正确显示，正确分类

改动文件：`server/api/marketplace.go`, `server/store/models.go`

1. `MarketplacePlugin` 新增 `Category`, `Homepage`, `Components`, `RemoteURL` 字段
2. `marketplaceJSONFile.Plugins` 结构补全：`Category`, `Homepage`, `LspServers`, `Source` 支持 string 和 object
3. `loadMarketplaceJSON` 改造：
   - 对本地 source 路径，读取子目录的 `.mcp.json` / `skills/` / `commands/` / `agents/` / `hooks/`
   - 正确设置 Type、AuthType、EnvVars、MCPConfig、Components
   - 对 URL source，设置 RemoteURL，标记为 remote
   - 对内联 lspServers，设置 Type="lsp"
4. 去重逻辑：marketplace.json entry 与子目录 `.claude-plugin/plugin.json` 检测可能重复，需要 marketplace.json 优先

### Phase 2：前端分类与展示优化

**目标**：UI 正确展示所有插件类型，标签和状态清晰

改动文件：`web/src/pages/MarketplacePage.tsx`, `web/src/lib/api.ts`

1. `LocalPlugin` 接口新增 `category`, `homepage`, `components`, `remote_url`
2. Tab 调整或增加筛选：
   - Connectors (type=mcp)
   - Skills (type=skill)
   - Plugins (type=plugin，含 commands/agents/hooks)
   - 可选：LSP (type=lsp，标记暂不支持)
3. 插件卡片增强：
   - 显示 category 标签
   - 显示 components 图标（MCP/Skill/Cmd/Agent/Hook）
   - auth_type 标签：none 不显示，apikey 显示 "API Key"，oauth_discovery 显示 "OAuth (auto)"，oauth 显示 "OAuth"
   - remote 插件显示 "External" 标签 + homepage 链接，禁止安装
   - LSP 插件标记 "LSP - Not supported"

### Phase 3：API Key 输入弹窗

**目标**：apikey 类型 connector 安装时输入 env vars

改动文件：`web/src/pages/MarketplacePage.tsx`

1. 新建 `EnvVarsDialog` 组件
2. 点击 Install 时，如果 `plugin.env_vars?.length > 0`，弹窗
3. 弹窗中每个 env var 一个输入框
4. 确认后调用 installMCP，env 字段传入用户输入的值

### Phase 4：统一安装流程（后续）

**目标**：plugin 作为整体安装，自动处理所有组件

这需要更大的改造，后续再做：
- 安装一个 plugin 时，如果它同时包含 MCP + Skills + Commands...
  - MCP → 注册到 installed_mcps
  - 整个插件目录 → 通过 `--plugin-dir` 传给 agent
  - agent (Claude Code CLI) 原生识别 commands/, agents/, hooks/
- 目前先保持分别安装 MCP 和 Skill 的方式，但确保 Plugin 整体安装 (`--plugin-dir`) 也能工作

---

## 五、OAuth Discovery 详细分析

### 5.1 MCP 协议 OAuth 流程

GitLab/Stripe/Asana/Linear/Supabase 等 connector 的 `.mcp.json` 没有 auth 配置，
但它们的 HTTP 端点在连接时会返回 401，触发 MCP 协议的 OAuth Discovery 流程：

```
Claude CLI → GET https://gitlab.com/api/v4/mcp → 401 Unauthorized
Claude CLI → GET https://gitlab.com/.well-known/oauth-authorization-server → OAuth metadata
Claude CLI → 打开本地浏览器 → 用户在浏览器中授权 → callback localhost:port → 获得 access_token
Claude CLI → GET https://gitlab.com/api/v4/mcp (with Bearer token) → 200 OK
```

### 5.2 我们架构下的限制

Claude Code CLI (v2.1.71) 确认支持这个流程（`claude mcp add` 有 `--callback-port` 等选项）。

但在我们的架构中：
- Claude CLI 运行在**服务端**（beehub 进程 spawn 的子进程）
- "打开浏览器"只能打开服务端的浏览器，对远程用户无效
- 只有当用户**直接在服务器本机访问** Web UI 时，才有可能完成浏览器授权

**技术链路验证**：
```
用户安装 GitLab connector
→ 后端存入 installed_mcps: { key: "gitlab", type: "http", url: "https://gitlab.com/api/v4/mcp" }
→ 用户发消息时，claude_agent.go 将其写入 --mcp-config JSON
→ Claude CLI 尝试连接 gitlab MCP → 收到 401
→ CLI 尝试打开浏览器（在服务端）→ 可能失败（无 DISPLAY）或打开了服务端浏览器
→ 如果是本地部署（用户 = 服务端），浏览器能打开 → 用户授权 → 成功
→ 如果是远程部署，浏览器打不开 → 失败
```

### 5.3 各类型处理策略

| auth_type | 技术可行性 | UI 处理 |
|-----------|-----------|---------|
| `none` (stdio) | 完全可用 | 直接 Install |
| `none` (HTTP 无 auth) | 完全可用 | 直接 Install |
| `apikey` | 完全可用 | Install 弹窗填 env vars |
| `oauth_discovery` | 本地部署可用，远程部署不可用 | 允许 Install，标记 "Requires browser auth"，tooltip 说明 |
| `oauth` (显式) | 不可用（MCPServerEntry 缺少 oauth 字段） | 禁止 Install，标记 "OAuth not supported" |

### 5.4 远程部署的 OAuth 长期方案（Phase 5，暂不实施）

如果未来需要支持远程部署的 OAuth：
1. 服务端拦截 CLI 的 OAuth 请求
2. 生成授权 URL 推送给前端
3. 前端在用户浏览器中打开授权 URL
4. 用户授权后，callback 到我们的服务端
5. 服务端将 token 传回 CLI 进程

这需要对 Claude CLI 和我们的 agent wrapper 做较大改造，暂不考虑。
