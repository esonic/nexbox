# BoxNex 统一 Store 架构重构

> 状态：已完成
>
> 日期：2026-02-12

---

## 1. 重构目标

将原先分散在 `api.Store`、`connect.Store`、`issues.Store` 三个独立实现中的数据操作，统一到单一的 `store.Store` 接口，为云模式（RemoteStore）和本地模式（SQLiteStore）提供一致的抽象层。

核心变更：
- 定义统一的 `store.Store` 接口
- 实现 `SQLiteStore`（本地 SQLite）和 `RemoteStore`（远程 HTTP 客户端）
- 整个删除 `connect` 包及其 WebSocket 广播机制
- 将本地配置迁移到 `config.json`
- 为共享数据添加 `namespace_id`、`version` 等云模式扩展字段
- 支持 `--store remote` 启动云模式

---

## 2. 实施阶段

### Phase 1：定义统一 Store 接口 + SQLiteStore 实现

**新增文件：**

| 文件 | 说明 |
|---|---|
| `server/store/store.go` | 统一 Store 接口定义（嵌入多个子接口） |
| `server/store/models.go` | 所有实体和 DTO 结构体（合并自 api/connect/issues） |
| `server/store/sqlite.go` | SQLiteStore 核心结构体、构造函数 |
| `server/store/sqlite_core.go` | Project/Session/Message/Task/Asset/Mindmap/ReviewComment 的 SQL 实现 |
| `server/store/sqlite_issues.go` | Issue/Label/Milestone/Reaction 的 SQL 实现 |
| `server/store/helpers.go` | GetTaskInfo/GetAssetInfo/GetDesignDocSummary 等辅助函数 |

**Store 接口结构：**

```go
type Store interface {
    ProjectStore        // Project CRUD + GetProjectsByIDs
    TaskStore           // Task CRUD + task-asset associations
    SessionStore        // Session CRUD + refs + reorder
    AssetStore          // Asset CRUD + versions + find by title
    MessageStore        // ChatMessage CRUD
    ReviewCommentStore  // Review comment CRUD + resolve all
    MindmapStore        // Mindmap CRUD
    IssueStore          // Issue CRUD + issue comments
    IssueLabelStore     // Labels + assignees
    MilestoneStore      // Milestone CRUD
    ReactionStore       // Reaction CRUD + summary
    ChangeStore         // ListChanges (cloud mode polling)
}
```

### Phase 2：本地配置迁移到 config.json

**新增文件：**

| 文件 | 说明 |
|---|---|
| `server/config/config.go` | Config 结构体 + Manager（线程安全读写 JSON 文件） |

**配置文件格式 (`~/.boxnex/config.json`)：**

```json
{
  "default_agent": "cursor",
  "terminal_alias": "",
  "server_url": "",
  "token": "",
  "projects": []
}
```

- `default_agent`、`terminal_alias`：原 SQLite settings 表中的设置
- `server_url`、`token`：云模式连接参数（手工配置）
- `projects`：本地路径与远程项目的映射关系

**迁移机制：** `main.go` 中的 `migrateSettingsToConfig()` 函数在首次启动时将旧 settings 表数据拷贝到 config.json。

### Phase 3：合并路由，删除 connect 包

**删除的文件/目录：**

| 删除 | 说明 |
|---|---|
| `server/connect/` (10 files) | 整个 connect 包：hub、manager、service、store、models、router、tasks/assets/mindmaps handlers |
| `server/api/store.go` | 旧的 api.Store 实现（已由 SQLiteStore 替代） |
| `server/api/connect_client.go` | 出站 WebSocket 客户端（connect 机制的一部分） |
| `server/plugins/issues/store.go` | 旧的 issues Store 实现 |
| `server/plugins/issues/models.go` | 旧的 issues 模型定义 |

**迁移到 api 包的 handler 文件：**

| 新文件 | 来源 | 说明 |
|---|---|---|
| `server/api/tasks.go` | connect/tasks.go | Task HTTP handlers，改用 s.Store |
| `server/api/assets.go` | connect/assets.go | Asset HTTP handlers，改用 s.Store |
| `server/api/mindmaps.go` | connect/mindmaps.go | Mindmap HTTP handlers，改用 s.Store |

**关键变更：**
- `api.Server.Store` 类型从 `*api.Store` 改为 `store.Store`（接口）
- `api/models.go` 改为类型别名（`type Project = store.Project`）
- `api/router.go` 统一挂载所有路由（含原 connect 的 tasks/assets/mindmaps）
- issues handler 重写，直接使用 `store.Store` 接口
- okone handler 更新为接收 `store.Store`
- plugins/router.go 的 `MountAll` 接收 `store.Store`
- mcp/server.go 的 `NewServer` 简化为单一 `store.Store` 参数
- 所有 `Hub.Notify*` 调用移除（CRUD 变更通知改为客户端轮询）

### Phase 4：数据模型扩展

**新增 SQLite 迁移（ALTER TABLE）：**

| 表 | 新增字段 | 默认值 | 说明 |
|---|---|---|---|
| projects | namespace_id | "local" | 所属 namespace |
| projects | gitlab_repo_url | "" | GitLab 仓库 URL |
| projects | created_by | "local" | 创建者 |
| projects | version | 1 | 乐观锁版本号 |
| tasks | namespace_id | "local" | 所属 namespace |
| tasks | project_id | "" | 关联项目 |
| tasks | created_by | "local" | 创建者 |
| tasks | version | 1 | 乐观锁版本号 |
| sessions | namespace_id | "local" | 所属 namespace |
| sessions | created_by | "local" | 创建者 |

**新增表：**

| 表 | 说明 |
|---|---|
| changes | 变更流水表，用于云模式客户端轮询增量变更 |

**版本号行为：**
- `UpdateProject` 和 `UpdateTask` 每次更新自动 `version++`
- Asset 复用 `current_version` 作为乐观锁版本号

### Phase 5：RemoteStore + 云模式实现

**新增文件：**

| 文件 | 说明 |
|---|---|
| `server/store/remote.go` | RemoteStore — HTTP 客户端，实现完整 Store 接口 |

**RemoteStore 设计：**
- 构造时注入 `server_url` 和 `token`
- 所有请求自动携带 `Authorization: Bearer <token>` 头
- 每个 Store 方法映射到远程 REST API 调用（`/api/v1/...`）
- 404 响应映射为 `nil` 返回（与 SQLiteStore 行为一致）
- `Ping()` 方法用于启动时验证连接

**main.go 改造：**

```
./boxnex --port 9040                    # 本地模式（默认 --store sqlite）
./boxnex --port 9040 --store remote     # 云模式
```

- `--store sqlite`：初始化 SQLite DB + SQLiteStore（默认行为）
- `--store remote`：从 config.json 读取 server_url/token，创建 RemoteStore，Ping 验证
- 云模式启动变更轮询 goroutine（每 5 秒调用 `ListChanges`，通过 ChatHub 广播给前端）
- `/api/info` 返回 `mode: "local"` 或 `mode: "cloud"`

---

## 3. 架构总览

```
┌─────────────────────────────────────────────────┐
│ 上层代码（完全不感知存储后端）                      │
│  ├─ api/ HTTP handlers                          │
│  ├─ mcp/ MCP server                             │
│  ├─ plugins/ (issues, okone)                    │
│  ├─ agent/ AI backends                          │
│  └─ ChatHub WebSocket                           │
├─────────────────────────────────────────────────┤
│              store.Store 接口                     │
├──────────────────────┬──────────────────────────┤
│ SQLiteStore (local)  │  RemoteStore (HTTP→远端)  │
└──────────────────────┴──────────────────────────┘
```

---

## 4. 文件变更清单

### 新增
- `server/store/store.go`
- `server/store/models.go`
- `server/store/sqlite.go`
- `server/store/sqlite_core.go`
- `server/store/sqlite_issues.go`
- `server/store/helpers.go`
- `server/store/remote.go`
- `server/config/config.go`
- `server/api/tasks.go`
- `server/api/assets.go`
- `server/api/mindmaps.go`

### 重大修改
- `server/main.go` — 移除 connect，添加 --store 参数，存储初始化重构
- `server/api/router.go` — Server 结构体改为 store.Store，统一路由
- `server/api/models.go` — 改为 store 包类型别名
- `server/api/helpers.go` — 使用 config.Manager，返回 mode 信息
- `server/api/chat.go` — 使用 store.GetTaskInfo/GetAssetInfo
- `server/api/gitlab.go` — 使用 store.GetDesignDocSummary
- `server/api/reviews.go` — 适配统一 Store 接口
- `server/store/db.go` — 添加 Phase 4 迁移语句
- `server/mcp/server.go` — 简化为单一 store.Store 参数
- `server/plugins/router.go` — MountAll 接收 store.Store
- `server/plugins/issues/router.go` — 适配 store.Store
- `server/plugins/issues/handlers.go` — 完全重写
- `server/plugins/okone/handler.go` — 适配 store.Store

### 删除
- `server/connect/` — 整个目录（10 个文件）
- `server/api/store.go`
- `server/api/connect_client.go`
- `server/plugins/issues/store.go`
- `server/plugins/issues/models.go`
