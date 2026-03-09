# AI Agent Web 平台技术规格文档
## 概述
基于现有 boxnex 代码库改造，构建一个多租户 AI Agent Web 平台。整体架构为：单个 Go 进程服务多用户，每个 Project 对应一个 Docker 容器运行 Agent，数据存储在本地 SQLite + 宿主机文件系统。
---
## 一、模块裁剪（移除/禁用）
以下模块为内部专用，在改造版本中不对外暴露。原则是**不删代码，在路由注册和初始化处屏蔽**，保留后续可按需恢复的能力。
### 1.1 完全移除
| 模块 | 位置 | 原因 |
|---|---|---|
| 自动更新 (updater) | `server/updater/` | 会后台发外网请求至 `sre-admin.okg.com`，完全移除 |
| OKOne 插件 | `server/plugins/okone/` | 内部 CI/CD 专用 |
| Cursor Agent | `server/agent/cursor_agent.go` | 内部 agent |
### 1.2 路由层屏蔽（不注册）
- `/api/auth/login-url`、`/api/auth/callback`（patrol OAuth 登录）
- `/api/okone`（OKOne 插件）
- Lark Chat 相关路由（`api/lark_chat.go`）
- Ralph 并行任务池路由（`api/ralph.go`）
- `/api/projects/{id}/members`（成员管理）
- `/api/projects/{id}/create-mr`（GitLab MR）
- Code Review MCP 路由
### 1.3 初始化层屏蔽
- `main.go` 不初始化 `RemoteStore`、cloud 相关逻辑、`OnCloudActivated` 回调
- `main.go` 不启动 `updater`
- `allAgents` map 不注册 CursorAgent
### 1.4 前端隐藏
- 侧边栏隐藏 Task、Asset、workflow 入口
- 删除 `ConnectPage.tsx`（cloud 登录页）
- 隐藏"云模式/本地模式"切换 UI
- 注意隐藏的 task、asset，并不是删除，未来可能给高级付费用户启用协作能力
---
## 二、用户认证
### 2.1 数据模型
新增 `users` 表：
| 字段 | 类型 | 说明 |
|---|---|---|
| id | TEXT PK | UUID |
| username | TEXT UNIQUE | 登录名 |
| password_hash | TEXT | bcrypt hash |
| email | TEXT | 可选 |
| created_at | INTEGER | Unix timestamp |
### 2.2 接口
替换原有 patrol OAuth 接口，新增：
- `POST /api/auth/register`：注册，username + password，写 users 表
- `POST /api/auth/login`：登录，验证 bcrypt，返回 JWT
- `POST /api/auth/logout`：客户端丢弃 token 即可（无状态）
- `GET /api/auth/me`：返回当前用户信息
### 2.3 JWT 中间件
- 所有 `/api/` 路由（login/register 除外）加 `AuthMiddleware`
- 中间件从 `Authorization: Bearer <token>` 提取并验证 JWT
- 验证通过后将 `userID` 注入 request context，下游 handler 通过 context 获取
- JWT secret 在服务器 config 中配置，token 有效期可配（默认 7 天）
### 2.4 前端
- 新增 `LoginPage.tsx`、`RegisterPage.tsx`
- `App.tsx` 加路由守卫，未登录重定向 `/login`
- 所有 API 请求 header 附带 `Authorization: Bearer <token>`
- token 存 localStorage，过期后跳登录页
---
## 三、多用户数据隔离
### 3.1 Project 归属
- `projects` 表新增 `owner_id`（关联 users.id）
- `CreateProject`：自动绑定 `owner_id = 当前用户`，在 `DataDir/<userID>/` 下自动创建工作目录
- `ListProjects`：只返回当前用户的 project
- 所有 project 操作（update、delete、文件读写、git 操作）：校验 `owner_id == 当前用户`，不匹配返回 403
### 3.2 Session 归属
- `sessions` 表 `created_by` 字段改为关联 user id（现已有该字段）
- `ListSessions`、`GetSession` 等过滤只返回当前用户的 session
### 3.3 文件路径安全
- 所有文件操作接口使用 `filepath.EvalSymlinks` + `strings.HasPrefix` 校验路径在 project 根目录内
- 拒绝跟随 symlink 指向 project 外部
- 操作路径不由前端原始传入直接使用，统一经过服务层校验
---
## 四、文件管理
### 4.1 新增接口
在现有只读文件接口（ListProjectFiles、ReadProjectFile）基础上新增：
- `POST /api/projects/{id}/upload`：multipart 文件上传，写入 project 目录
- `GET /api/projects/{id}/download?path=xxx`：流式文件下载
- `DELETE /api/projects/{id}/file?path=xxx`：删除文件
- `PUT /api/projects/{id}/rename`：重命名文件（`from`、`to` 字段）
所有接口均需路径安全校验（见 3.3），上传前需检查磁盘配额（见 4.2）。
### 4.2 磁盘配额
**方案**：`du` + 缓存，纯 Go 实现，不依赖 OS quota 特性。
- `user_quotas` 表（或并入 `user_balances`）存 `disk_quota_bytes`，默认值在服务器 config 中配置（如 10 GB）
- 共用 helper `checkDiskQuota(userID) error`：`du -s /data/<userID>/`，与配额对比
- 结果缓存 60 秒（内存 map），后台定时刷新，请求时用缓存值，避免每次 I/O
- **两处卡口**：
  1. 上传文件前检查
  2. 发送消息（触发 Agent）前检查
- 超额时返回错误信息，前端展示提示
### 4.3 FileEntry 补全
现有 `FileEntry` 补充字段：
- `ModTime`：文件修改时间
- `MimeType`：文件 MIME 类型（按扩展名推断）
### 4.4 前端
- 文件树新增操作：上传按钮、拖拽区域、下载、删除、重命名
- 预览支持：Markdown 渲染、图片展示、PDF 内嵌查看
- 文件列表显示大小、类型、修改时间
---
## 五、预付费计费系统
### 5.1 数据模型
新增两张表：
**`user_balances`**：
| 字段 | 类型 | 说明 |
|---|---|---|
| user_id | TEXT PK | 关联 users.id |
| balance_tokens | INTEGER | 当前可用 token 数 |
| reserved_tokens | INTEGER | 已预占但未结算的 token 数 |
| total_recharged | INTEGER | 历史总充值 token 数 |
| updated_at | INTEGER | Unix timestamp |
**`token_transactions`**：
| 字段 | 类型 | 说明 |
|---|---|---|
| id | TEXT PK | UUID |
| user_id | TEXT | 关联 users.id |
| session_id | TEXT | 关联 sessions.id，可为空（充值时） |
| delta | INTEGER | 变化量（负数=消耗，正数=充值） |
| reason | TEXT | "consume" \| "recharge" \| "reserve" \| "settle" |
| created_at | INTEGER | Unix timestamp |
### 5.2 Token → 货币换算
- 换算汇率在服务器 config 中配置，例如：`token_price_per_million: 30.0`（元/百万 token）
- 换算公式：`货币金额 = tokens / 1_000_000 * price_per_million`
- 汇率可随时调整（不影响已消耗记录），定价要确保有利润空间
### 5.3 预占 → 消耗 → 结算流程
**任务启动前（原子预占）**：
```sql
UPDATE user_balances
SET balance_tokens = balance_tokens - :estimated,
    reserved_tokens = reserved_tokens + :estimated
WHERE user_id = :uid AND balance_tokens >= :estimated
```
返回影响行数为 0 则表示余额不足，拒绝启动任务，返回 402。
预占的 estimated 值：可按历史平均消耗或固定值（如 10,000 tokens）。
**任务执行中**：
claude CLI 每条 result 消息带有 `usage.input_tokens` + `usage.output_tokens`，累加到内存中。
**任务结束后（结算）**：
```sql
UPDATE user_balances
SET balance_tokens = balance_tokens - (actual - estimated),
    reserved_tokens = reserved_tokens - estimated
WHERE user_id = :uid
```
同时写 `token_transactions` 记录实际消耗。若实际 < 预占，退还差额。
### 5.4 接口
- `GET /api/balance`：返回当前余额（tokens + 对应货币金额）、消耗历史
- `POST /api/admin/recharge`：管理员手动充值（user_id + tokens），后续接入支付网关时替换
### 5.5 并发保护
- 每个用户同时运行的 Agent 任务数上限（config 可配，默认 3）
- 超出限制时发消息返回 429，前端提示"已有任务在运行"
- 结合预占机制，防止并发启动多任务绕过余额检查
### 5.6 前端
- 顶部导航栏显示剩余余额（tokens + 折算货币）
- 余额不足时聊天框禁用，展示提示和充值入口
---
## 六、Agent 执行隔离（Docker）
### 6.1 整体架构
每个 Project 对应一个持久 Docker 容器，作为 Agent 的执行环境。Go Server 本身在宿主机运行（或特权容器），通过 Docker SDK / CLI 管理 Agent 容器。
```
用户发消息
  → Go Server 查找该 project 是否有存活容器
  → 无 → 启动容器（docker run）
  → 有 → 直接使用
  → docker exec <containerID> claude -p --resume ...
  → stdin/stdout pipe 接回 Go Server 处理流
```
### 6.2 容器启动参数
```bash
docker run -d \
  --name boxnex-proj-<projectID> \
  --network boxnex-egress \        # 出口受限网络（见 6.4）
  --pids-limit 100 \               # 防 fork 炸弹
  --memory 1g \
  --cpus 1.0 \
  -v /data/<userID>/<projectID>:/workspace:rw \   # project 文件，持久化
  -v /data/<userID>/claude-sessions:/root/.claude:rw \  # agent session 历史，持久化
  boxnex-agent-image \
  sleep infinity
```
### 6.3 文件持久化
- Project 文件挂载自宿主机目录，容器重启/删除不影响文件
- `~/.claude/`（claude CLI session 状态）同样挂载自宿主机，容器回收后 session 仍可 resume
- 宿主机目录结构：
```
/data/
  <userID>/
    <projectID>/          ← 挂载为容器 /workspace
    claude-sessions/      ← 挂载为容器 /root/.claude
```

- DB 拆分，除主要表外，大部分 session 相关的表需要拆分到独立的 sqlite db 文件，放在每个 session 的目录下，以提升性能、方便清理，实现时需考虑拆分粒度。目标是能用目录和文件组织的，就尽量少用 db
- session 需要限制总数量，当多于 20 个未 trash 的 session 时，告诉用户不能再新建了
### 6.4 网络隔离
创建专用 Docker 网络 `boxnex-egress`，通过 iptables OUTPUT 规则限制该网络出站流量：
- 允许：`api.anthropic.com`（claude CLI 需要访问）
- 允许：DNS（53/udp）
- 拒绝：其他所有出站连接
这样用户在容器内绑定的端口对外不可达，也无法访问内网或向外发起任意连接。
如果不需要支持 Agent 的 WebSearch/WebFetch 工具，可改用 `--network none` 完全断网，更简单。
### 6.5 容器生命周期管理（ContainerManager）
新增 `api/container_manager.go` 模块：
```
ContainerManager
  ├── containers: map[projectID]ContainerInfo  (containerID, lastActiveAt)
  ├── EnsureRunning(projectID, projectPath, userID) → containerID
  ├── Exec(projectID, args...) → (io.Reader, io.Writer, error)
  ├── Stop(projectID)
  └── GC()  ← 后台 ticker，停止空闲超过 N 分钟的容器
```
`claude_cli.go` 中将 `exec.Command("claude", ...)` 替换为通过 ContainerManager 的 `Exec` 方法执行 `docker exec`，stdin/stdout pipe 逻辑基本不变。
### 6.6 容器镜像
维护一个 `boxnex-agent-image` 基础镜像：
- 基础：ubuntu 或 debian slim
- 安装：Node.js、claude CLI（npm install -g @anthropic-ai/claude-code）
- 跟随 claude CLI 版本更新镜像
### 6.7 执行超时
每次 `docker exec` 调用时传入带超时的 context（例如 30 分钟），超时后 kill 对应 exec 进程。Go Server 层已有 context cancel → kill process group 逻辑，适配 docker exec 场景即可。
---
## 七 Marketplace（补全 Plugin 支持）
### 7.1 现有能力（不需改动）
MCP 和 Skill 的安装范围已实现：
- `project_id = ""`：全局安装，对所有 project 生效
- `project_id = "<id>"`：安装到指定 project
- 查询时自动合并，project 级优先于全局
### 7.2 新增 Plugin 类型
Claude Code CLI 通过 `--plugin-dir <dir>` 注入插件。
**数据模型**，新增 `installed_plugins` 表（参考 `installed_skills`）：
| 字段 | 类型 | 说明 |
|---|---|---|
| name | TEXT | 包名（PK 组件） |
| project_id | TEXT | "" = 全局，else project 级 |
| version | TEXT | 安装版本 |
| install_path | TEXT | 解压后的插件目录绝对路径 |
| installed_at | INTEGER | Unix timestamp |
**安装逻辑**：参考现有 skill 安装流程（下载 tarball → 解压到插件目录 → 写 DB）。
**注入 Agent**：
- `agent/claude_cli.go`：`QueryOptions` 新增 `PluginDirs []string`，`buildArgs` 追加 `--plugin-dir <path>`（每个 plugin dir 一个参数）
- `agent/claude_agent.go`：构建 `QueryOptions` 时，从 `ListInstalledPlugins(projectID)` 读取插件路径注入
**前端**：Marketplace UI 新增 Plugin tab，与 MCP/Skill tab 平级。
### 7.3 Marketplace 数据来源与管理员维护机制
#### 7.3.1 设计原则
Marketplace 不对接外部包索引，数据完全来源于管理员在宿主机上手动维护的本地目录与清单文件。用户只能浏览和安装管理员预定义好的内容，不能自行添加来源。
#### 7.3.2 目录结构
管理员在服务器上维护如下目录（路径在服务器 config 中配置，默认 `/marketplace`）：
```
/marketplace/
  mcp/
  skill/
  plugin/
```
自动始终使用新版本，不再需要版本。
#### 7.3.3 服务端加载机制
Go Server 启动时扫描 `/marketplace/` 目录，将所有合法条目加载进内存缓存（`MarketplaceRegistry`）。
- 提供 `GET /api/marketplace/list?type=mcp|skill|plugin` 接口，返回内存缓存中的条目列表
- **定时轮询**：加载新的内容进来
#### 7.3.4 安装流程
用户在 Marketplace 页面选择条目后点击安装：
1. 前端调用 `POST /api/projects/{id}/marketplace/install`，body 传入 `{ type, name, scope }` （scope = `"project"` 或 `"global"`）
2. 服务端从 `MarketplaceRegistry` 查找该条目，获取其在宿主机上的 `install_path`
3. 按类型分别写入对应的已安装表（`installed_mcps` / `installed_skills` / `installed_plugins`），记录 `install_path` 直接指向 marketplace 目录（无需复制文件）
4. 返回成功，前端刷新已安装列表
#### 7.3.5 约束
- 用户不能提交自定义来源，只能从 `MarketplaceRegistry` 中的条目选择安装
- 管理员通过 SSH 登录服务器，手动在 `/marketplace/` 下增删改目录文件来维护内容
- Marketplace 接口为只读，不提供增删 marketplace 条目的用户侧 API
---
## 八、安全性补充说明
### 8.1 防御层次
安全防御从外到内分四层：
1. **API 层**：JWT 鉴权 + project 归属校验 + 路径安全校验（防路径穿越 + symlink）
2. **资源层**：磁盘配额检查（上传 + Agent 启动前）、并发任务数限制、余额检查
3. **容器层**：Docker 资源限制（`--pids-limit`、`--memory`、`--cpus`）
4. **网络层**：Docker 出站网络白名单（仅放行 Anthropic API 等域名，通过配置文件指定）
### 8.2 关键威胁及对应措施
| 威胁 | 对应措施 |
|---|---|
| 用户启动持久 HTTP 服务 | 容器端口不发布 + 出站网络限制（无法对外提供服务） |
| 挖矿 | `--cpus` 限制 + 执行超时强制终止 |
| Fork 炸弹 | `--pids-limit 100` |
| 磁盘耗尽 | 磁盘配额检查 |
| 读取其他用户文件 | Docker 只挂载自己的目录 + project 归属校验 |
| 读取服务器配置/密钥 | `DataDir`/`config.json` 不挂载进容器 |
| 反向 Shell / 外网扫描 | 出站网络白名单 |
| 预算绕过 | 原子预占 + 并发任务数限制 |
| 路径穿越 | `filepath.EvalSymlinks` 校验 + 拒绝 symlink |

