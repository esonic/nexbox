# BeeHub Cloud SaaS 改造实施文档

## 目标与背景

本次改造将 nexbox/BeeHub 从一个**单用户本地工具**变成一个**多租户云 SaaS 平台**，部署在 VPS 上，支持多用户独立使用 AI Agent（Claude、Codex 等）完成编程任务。

### 核心需求

| 需求 | 说明 |
|------|------|
| **多用户注册/登录** | JWT 鉴权，开放注册，无需 patrol OAuth |
| **数据隔离** | 每个用户只能看到/操作自己的 project、session、文件 |
| **Token 计费** | 平台持有 Anthropic Key；用户购买 Token 额度；管理员手动充值（MVP） |
| **功能权限控制** | task/asset/workflow/terminal 功能通过位掩码按用户开关 |
| **MySQL 支持** | 多实例 VPS 部署需要共享数据库；SQLite 保留用于本地开发 |
| **Docker 容器隔离** | 每个 project 运行在独立容器中，互相隔离（可选开关） |
| **Marketplace** | 管理员维护本地插件目录；用户浏览/安装插件 |

---

## 已完成的工作

### Phase 0 — main.go 清理

- 移除了 patrol OAuth、lark chat worker、OKOne 插件、updater 等单用户时代的功能
- 移除了 RemoteStore / CompositeStore 初始化（云版本统一用 MySQL 或 SQLite）
- 保留了 MCP server、DetailCache、worktree 清理等核心功能

### Phase 1 — Store 接口扩展 + MySQL 实现

**新增接口：**
- `UserStore`：CreateUser / GetUser / GetUserByUsername / ListUsers / UpdateUserFeatures / UpdateUserPassword
- `BalanceStore`：GetBalance / RechargeBalance / ReserveTokens / SettleTokens / ListTransactions
- `MarketplaceStore`：ListInstalledPlugins / InstallPlugin / UninstallPlugin / GetInstalledPlugin

**新增文件：**
- `store/mysql.go`：MySQLStore 结构体、连接池、全量建表（含索引）
- `store/mysql_core.go`：projects、sessions、tasks、labels、reactions、messages 等核心表的 MySQL 实现
- `store/mysql_workflow.go`：workflow/execution 相关的 MySQL 实现
- `store/mysql_marketplace.go`：marketplace 相关 MySQL 实现
- `store/mysql_cloud.go`：用户、余额、Token 交易的 MySQL 实现
- `store/sqlite_cloud.go`：用户、余额、Token 交易的 SQLite 实现（原有 SQLite 扩展）
- `store/sqlite_marketplace.go`：已安装插件的 SQLite 实现

**修改文件：**
- `store/models.go`：新增 User、UserBalance、TokenTransaction、InstalledPlugin 类型；Project 加 OwnerID、Session 加 UserID
- `store/db.go`：新增 users、user_balances、token_transactions、installed_plugins 表的 DDL；新增 additive column migration
- `store/sqlite_core.go`：同步 owner_id / user_id 字段到 project/session 的增删查

**双 Store 切换逻辑（main.go）：**
```
config.StoreType == "mysql" && config.MySQLDSN != ""  →  MySQLStore
否则                                                   →  SQLiteStore
```

### Phase 2 — JWT 鉴权 + 路由重构

**新增文件：**
- `api/auth_cloud.go`：注册、登录、`/api/auth/me`、管理员 List/UpdateFeatures 接口
- `api/middleware.go`：AuthMiddleware（JWT 验证 → 注入 context）、AdminMiddleware（检查 role）、GetUserID / GetUserRole / GetUserFeatures 辅助函数

**鉴权原理：**
- JWT payload 含 `user_id`、`username`、`role`、`features`，HS256 签名
- AuthMiddleware 做纯本地 crypto 验证，**不查数据库**，注入 request context
- 后续所有权限判断从 context 读取，零额外 DB 查询
- **Trade-off**：改 role/features 后需重新登录才能生效（旧 JWT 在有效期内仍有旧权限）

**路由结构：**
```
/api/auth/register   public
/api/auth/login      public
/api/...             需要 AuthMiddleware
  /api/admin/...     需要 AdminMiddleware（role == "admin"）
```

**注意**：目前没有"首个注册用户自动成为 admin"的逻辑。部署后首个管理员需手动改库：
```sql
UPDATE users SET role='admin' WHERE username='yourname';
```

### Phase 3 — 多用户数据隔离

- `api/projects.go`：CreateProject 设置 OwnerID；ListProjects 按 OwnerID 过滤；Get/Update/Delete 检查 403
- `api/sessions.go`：CreateSession 设置 UserID；List 按 UserID 过滤；Get/Update/Delete/Restore 检查 403；CreateSession 检查 session 数量上限
- `api/files.go`：`resolveFilesRoot` 加 project + session 双重 ownership 检查，返回 errForbidden（403）
- `api/git.go`：`resolveWorkDir` / `resolveRequestWorkDir` 加 ownership 检查，所有 git handler 正确返回 403
- `api/diff.go`：GetProjectDiff / GetProjectDiffStats / GetProjectFileDiff 加 ownership 检查
- `api/terminal.go`：HandleTerminalWS / HandleTerminalSessionWS 加 ownership 检查

### Phase 4 — 计费 API

**新增文件：**
- `api/billing.go`：
  - `GET /api/balance`：返回余额 + 最近 20 条流水 + CNY 折算（TokenPricePerMillion 配置）
  - `POST /api/admin/recharge`：管理员给指定用户充 Token

**计费流程：**
```
Agent 执行前  →  ReserveTokens(estimated)
               原子 UPDATE WHERE balance_tokens >= estimated（不足则拒绝执行）

Agent 执行后  →  SettleTokens(estimated, actual)
               actual < estimated：退回差额到 balance
               actual > estimated：从 balance 补扣差额
```

### Phase 5 — Docker 容器隔离

**新增文件：**
- `agent/container.go`：ContainerManager，使用 docker CLI（非 SDK，无新依赖）
  - `EnsureContainer(projectID, repoPath)`：检查是否已有容器，无则 `docker run -d --rm -v repoPath:/workspace`
  - `StopContainer(projectID)`：`docker stop`
  - `ExecInContainer(containerID, cmd, env, workDir)`：返回 `docker exec -i [-e K=V]* [-w workDir] ...` 的 *exec.Cmd

**修改文件：**
- `config/config.go`：新增 `DockerEnabled bool`、`DockerImage string`
- `agent/claude_cli.go`：加 `ContainerMgr`、`ContainerID` 字段，执行时若已设置则走 `docker exec`
- `agent/claude_agent.go`：加 `ContainerMgr`，SendMessage 时 EnsureContainer 并注入到 ClaudeClient
- `api/router.go`：Server 加 `ContainerMgr` 字段
- `api/projects.go`：DeleteProject 时调用 `ContainerMgr.StopContainer`（忽略错误）
- `main.go`：`DockerEnabled=true` 时创建 ContainerManager 并注入

**镜像准备（见 Dockerfile.agent）：**
```bash
./scripts/build-agent-image.sh
```

**配置示例：**
```json
{
  "docker_enabled": true,
  "docker_image": "nexbox-agent:latest"
}
```

### Phase 6 — Marketplace

- `api/marketplace.go`：`loadMarketplacePlugins(dir)` 扫描 `MarketplaceDir/*.json`；5 个 REST 接口（list/get/installed/install/uninstall）
- `store/sqlite_marketplace.go` + `store/mysql_marketplace.go`：installed_plugins 的增删查

**插件格式**（放在 `MarketplaceDir/<id>.json`）：
```json
{
  "id": "my-mcp-server",
  "name": "My MCP Server",
  "type": "mcp",
  "version": "1.0.0",
  "command": "npx",
  "args": ["-y", "@my/mcp-server"],
  "env_vars": ["API_KEY"]
}
```

---

## 已测试的内容

集成测试通过真实服务器（SQLite 模式，端口 19040）用 curl 验证，共 42 项：

| 分类 | 测试项 | 结果 |
|------|--------|------|
| 注册/登录 | 正常注册、重复用户名 409、密码过短 400、登录正确/错误 | ✅ 全通过 |
| JWT 鉴权 | 有效 token、无效 token 401、无 token 401 | ✅ 全通过 |
| 余额 | 查询余额（初始为 0） | ✅ |
| 管理员接口 | 列用户、普通用户访问 403、更新 features、充值 | ✅ 全通过 |
| Projects | CRUD、多用户隔离（跨用户访问 403）、admin 可见全部 | ✅ 全通过 |
| Sessions | CRUD、多用户隔离（跨用户访问 403） | ✅ 全通过 |
| Marketplace | 空目录返回空列表 | ✅ |
| 路径安全 | `../../etc/passwd` 被拦截 400 | ✅ |
| 文件访问隔离 | 跨用户访问文件/git/diff 正确返回 403 | ✅（修复后） |

**发现并修复的 Bug：**
- **Bug 1**：`resolveFilesRoot`、`resolveWorkDir`、`resolveDiffWorkDir`、terminal handler 均缺少 ownership 检查，任何登录用户可访问他人的文件树、git 状态、diff、终端
- **Bug 2**（设计缺陷）：`HandleMe` 使用裸类型断言可能 panic（低风险，已记录）

---

## 未完成 / 未测试的内容

### 未测试（需要准备环境后验证）

| 功能 | 前提条件 | 说明 |
|------|---------|------|
| **Docker 容器生命周期** | 启动 Docker daemon，构建 nexbox-agent 镜像 | 首次启动、重启复用、project 删除停容器 |
| **DockerEnabled 模式下 agent 执行** | 同上 | claude_cli 是否走 `docker exec` |
| **文件读/搜索** | 需要有 git_repo_path 的真实 project | ReadProjectFile、SearchProjectFiles |
| **Git 操作** | 需要真实 git 仓库 | status/commit/log/diff/branches/discard |
| **WebSocket 聊天** | 需要 Anthropic API Key | agent 实际执行、消息流 |
| **WebSocket 终端** | PTY 环境 | 终端权限隔离 |
| **Token 计费全链路** | 需要 agent 执行 | reserve → execute → settle，数字正确性 |
| **Session worktree** | 需要 git 仓库 | 创建 worktree、切换目录、清理 |
| **Session 消息历史** | 需要先跑过 agent | ListSessionMessages 的 ownership 检查 |
| **Marketplace 安装/卸载** | 需要在 MarketplaceDir 放 JSON 文件 | install/uninstall/list installed |
| **MySQL 模式** | 需要 MySQL 实例 | 完整功能在 MySQL 下运行 |
| **并发安全** | 需要压测 | 多用户同时操作同一资源 |

### 已知 Bug（未修复）

| 编号 | 位置 | 严重程度 | 说明 |
|------|------|---------|------|
| H5 | `agent/container.go` EnsureContainer | High | `docker ps --filter name=X` 是子串匹配，会错误复用容器。修复：用 `--filter name=^/nexbox-proj-EXACT$` |
| C3 | `store/sqlite_cloud.go` SettleTokens | Critical | actual > estimated 时不做余额保护，可导致 balance_tokens 为负 |
| C4 | `store/sqlite_marketplace.go` | Critical | installed_plugins 用 `project_id` 列存 `user_id`，列名错误，是历史遗留，未来改 schema 会数据泄露 |
| C5 | `api/router.go` | Critical | 注册接口无频率限制，可被滥用批量注册 |
| H6 | `store/sqlite_cloud.go` CreateUser | High | user + balance 两次 INSERT 无事务，中途崩溃会产生无余额的僵尸用户 |
| M1 | `main.go` | Medium | MySQL 模式下 worktree 清理查的是 SQLite，不会清理 MySQL 里的过期 session |
| M2 | `api/billing.go` | Medium | recharge 成功但 GetBalance 失败时返回 204，管理员无确认信息 |

### 未实现的功能

| 功能 | 说明 |
|------|------|
| **注册频率限制** | 注册接口无 IP 限流或邀请码，需加中间件 |
| **首个用户自动成为 admin** | 目前需手动改库，`HandleRegister` 可以判断 users 表为空时设 role=admin |
| **Token 计费保护** | SettleTokens 需加 `WHERE balance_tokens >= diff` 原子保护 |
| **CreateUser 事务化** | user + balance 的两次 INSERT 应包在事务里 |
| **Docker filter 精确匹配** | EnsureContainer 的 `docker ps --filter name=` 改为正则锚定 |
| **WebSocket 鉴权** | `/ws` 和 `/ws/terminal/*` 目前在 AuthMiddleware 组外，token 应通过 query param 传入并验证 |
| **/debug/pprof 保护** | `r.Mount("/debug", http.DefaultServeMux)` 应移至 AdminMiddleware 组内或在生产环境禁用 |
| **pprof 端点禁用（生产）** | 见上一条 |
| **CORS 可配置** | localhost 开发域名硬编码在源码里，应从 config 读取 |
| **config.json 文件权限** | 保存时应 chmod 0600，避免泄露 JWTSecret 和 MySQLDSN |

---

## 部署指南（快速参考）

### 本地开发（SQLite）

```bash
cd server
go build -o beehub .
./beehub --data-dir ./data --port 9040
```

### 生产部署（MySQL）

1. 准备 MySQL 8.x，创建数据库和用户
2. 编辑 `data/config.json`：
```json
{
  "store_type": "mysql",
  "mysql_dsn": "user:pass@tcp(host:3306)/nexbox?parseTime=true",
  "jwt_secret": "<随机长字符串>",
  "jwt_exp_days": 7,
  "anthropic_key": "sk-ant-...",
  "token_price_per_million": 15,
  "marketplace_dir": "/opt/nexbox/marketplace"
}
```
3. 启动后手动将首个用户设为 admin：
```sql
UPDATE users SET role='admin' WHERE username='yourname';
```

### 启用 Docker 容器隔离

```bash
# 1. 确保 Docker daemon 在运行
# 2. 构建 agent 镜像
./scripts/build-agent-image.sh

# 3. 在 config.json 中添加
# "docker_enabled": true,
# "docker_image": "nexbox-agent:latest"
```

---

## 文件结构变更摘要

```
server/
  api/
    auth_cloud.go      新增  JWT 注册/登录/me/admin 用户管理
    middleware.go      新增  AuthMiddleware / AdminMiddleware
    billing.go         新增  余额查询 / 管理员充值
    marketplace.go     修改  本地目录扫描 + 安装/卸载 API
    projects.go        修改  OwnerID 过滤 + 403 检查
    sessions.go        修改  UserID 过滤 + 403 检查 + 数量限制
    files.go           修改  ownership 检查 + errForbidden
    git.go             修改  ownership 检查
    diff.go            修改  ownership 检查
    terminal.go        修改  ownership 检查
    router.go          修改  路由重构，加 ContainerMgr 字段
  agent/
    container.go       新增  ContainerManager（docker CLI 封装）
    container_test.go  新增  ContainerManager 单元测试
    claude_cli.go      修改  支持 docker exec 模式
    claude_agent.go    修改  注入 ContainerMgr
  store/
    store.go           修改  UserStore / BalanceStore / MarketplaceStore 接口
    models.go          修改  User / UserBalance / TokenTransaction / InstalledPlugin 类型
    db.go              修改  新增 4 张表的 DDL + additive migration
    sqlite_core.go     修改  owner_id / user_id 字段同步
    sqlite_cloud.go    新增  用户 + 余额 + Token 的 SQLite 实现
    sqlite_marketplace.go 新增/修改  已安装插件 SQLite 实现
    mysql.go           新增  MySQLStore + 全量建表
    mysql_core.go      新增  核心表 MySQL 实现
    mysql_workflow.go  新增  workflow MySQL 实现
    mysql_marketplace.go 新增  marketplace MySQL 实现
    mysql_cloud.go     新增  用户 + 余额 + Token MySQL 实现
    remote_cloud.go    新增  RemoteStore 的 stub 实现（保持接口完整）
  config/
    config.go          修改  新增 StoreType/MySQLDSN/JWTSecret/DockerEnabled 等字段
  main.go              修改  双 Store 初始化 + ContainerManager 注入

Dockerfile.agent       新增  per-project 沙盒容器镜像
scripts/
  build-agent-image.sh 新增  镜像构建脚本
docs/
  cloud_implementation.md  本文档
```
