# BoxNex 云模式概要设计

## 1. 背景与目标

BoxNex 目前作为纯本地应用运行，所有数据存储在 SQLite 中。需要支持两种运行模式：

- **本地模式（Local）**：当前行为，数据存于本地 SQLite，完全自包含
- **云模式（Cloud）**：连接远程存储服务，**仅数据存取**走远程，其余所有能力（Agent 执行、WebSocket/Chat、MCP 等）仍在本地运行

远程存储服务是一个独立的 REST API 服务，可用 Go 或 Java 实现，部署到 k8s，以 MySQL 作为数据库后端。该服务的唯一职责是**数据的持久化读写**，相当于一个带鉴权和 namespace 隔离的共享存储层。

### 核心目标

1. 本地模式和云模式共享同一套前端、同一套 API handler、同一套 Agent/Chat 逻辑，差异仅在存储后端
2. 存储层（Store）与 API 层彻底解耦，通过接口抽象，可自由切换 SQLite / MySQL / 远程 HTTP 后端
3. 云模式实现团队协作：namespace 内共享 project、task、asset（session 为私有数据，预留未来共享能力）
4. 项目以 GitLab 仓库 URL 作为 namespace 内唯一标识
5. Token 鉴权，绑定 namespace + 用户身份
6. 变更通知机制 + 版本冲突管理，支持多人协作
7. 远程存储 API 设计独立，可由 Go 或 Java 独立实现

---

## 2. 整体架构

### 2.1 架构图

```
┌───────────────── 本地模式 (Local) ──────────────────┐
│                                                      │
│  浏览器 ──► 本地 Go Server（完整能力）                 │
│              ├─ 前端 SPA                              │
│              ├─ API handlers                          │
│              ├─ Agent (Claude/Codex/Cursor)           │
│              ├─ WebSocket / Chat                      │
│              ├─ MCP Server                            │
│              └─ Store Interface ──► SQLiteStore ──► SQLite │
│                                                      │
└──────────────────────────────────────────────────────┘

┌───────────────── 云模式 (Cloud) ──────────────────────┐
│                                                        │
│  浏览器 ──► 本地 Go Server（完整能力，存储除外）         │
│              ├─ 前端 SPA                                │
│              ├─ API handlers（与本地模式完全一致）        │
│              ├─ Agent (Claude/Codex/Cursor)             │
│              ├─ WebSocket / Chat                        │
│              ├─ MCP Server                              │
│              └─ Store Interface ──► RemoteStore ──HTTP──┐│
│                                                        ││
│           ┌────────────────────────────────────────────┘│
│           ▼                                             │
│   远程存储服务 (k8s)                                     │
│   ├─ REST API + Token 鉴权                              │
│   ├─ Namespace 隔离                                     │
│   ├─ Changes 变更流水                                    │
│   └─ Store ──► MySQL (或 SQLite)                        │
│                                                        │
└────────────────────────────────────────────────────────┘
```

### 2.2 关键设计原则

1. **本地 Server 能力不变**：云模式下本地 Server 仍然运行全部能力（Agent、Chat、MCP、Git 操作等），**唯一区别是 Store 实现从 SQLiteStore 换成了 RemoteStore**
2. **Store 接口统一**：API handler 代码完全不感知底层存储是 SQLite、MySQL 还是远程 HTTP
3. **远程服务仅做存储**：远程存储服务不运行 Agent、不处理 WebSocket、不做业务逻辑，它只是一个带鉴权的数据库 CRUD API
4. **存储后端可切换**：即使在本地模式下，也可以通过配置将存储后端从 SQLite 切换为 MySQL（例如本地开发环境连接本地 MySQL）

### 2.3 模式选择

通过命令行参数选择运行模式：

```bash
# 本地模式（默认，当前行为）
./boxnex --port 9040 --data-dir ~/.boxnex

# 云模式，连接远程存储服务（server_url 和 token 从 config.json 读取）
./boxnex --port 9040 --store remote
```

命令行参数：

| 参数 | 默认值 | 说明 |
|---|---|---|
| `--store` | `sqlite` | 存储后端类型：`sqlite` / `remote` |
| `--data-dir` | `.` | SQLite 数据目录（仅 sqlite 模式） |

当 `--store remote` 时即为"云模式"，本地不使用 SQLite，不读取本地数据库。云模式所需的 `server_url` 和 `token` 必须在 `~/.boxnex/config.json` 中手动配置（参见 5.4 节）。

---

## 3. Store 层抽象

### 3.1 Store 接口定义

从当前 `store.DB` 的方法中抽取统一的 `Store` 接口。所有 API handler、MCP server 等上层代码仅依赖此接口：

```
Store 接口
├── Project 相关
│   ├── CreateProject(req) → Project
│   ├── ListProjects() → []Project          // 本地模式返回全部；云模式不使用
│   ├── GetProjectsByIDs(ids) → []Project   // 云模式按 config.json 中的 ID 列表查询
│   ├── GetProject(id) → Project
│   ├── UpdateProject(id, req) → Project
│   └── DeleteProject(id)
├── Task 相关
│   ├── CreateTask(req) → Task
│   ├── ListTasks() → []Task
│   ├── GetTask(id) → Task
│   ├── UpdateTask(id, req) → Task
│   ├── DeleteTask(id)
│   ├── LinkTaskAsset(taskId, assetId)
│   ├── ListTaskAssets(taskId) → []Asset
│   └── UnlinkTaskAsset(taskId, assetId)
├── Session 相关
│   ├── CreateSession(req) → Session
│   ├── ListSessions() → []Session
│   ├── GetSession(id) → Session
│   ├── UpdateSession(id, req) → Session
│   ├── UpdateSessionAgentID(id, agentSessionId)
│   ├── UpdateSessionStreamID(id, streamId)
│   └── DeleteSession(id)
├── SessionRef 相关
│   ├── CreateSessionRef(sessionId, req) → SessionRef
│   ├── ListSessionRefs(sessionId) → []SessionRef
│   └── DeleteSessionRef(sessionId, refId)
├── Asset 相关
│   ├── CreateAsset(projectId, req) → Asset
│   ├── ListAssets(projectId) → []Asset
│   ├── ListAssetsByType(projectId, assetType) → []Asset
│   ├── GetAsset(id) → Asset
│   ├── UpdateAsset(id, req) → Asset  // 需要版本校验
│   ├── DeleteAsset(id)
│   ├── GetAssetVersions(id) → []AssetVersion
│   ├── GetAssetVersion(assetId, version) → AssetVersion
│   ├── FindAssetByTitle(projectId, title) → Asset
│   └── GetLatestAssetContent(id) → string
├── Message 相关
│   ├── CreateMessage(msg) → ChatMessage
│   ├── ListMessages(sessionId) → []ChatMessage
│   ├── DeleteMessagesBySession(sessionId)
│   └── CreateUserMessage(sessionId, content) → ChatMessage
├── ReviewComment 相关
│   ├── CreateReviewComment(assetId, req) → ReviewComment
│   ├── ListReviewComments(assetId) → []ReviewComment
│   ├── GetReviewComment(id) → ReviewComment
│   ├── UpdateReviewComment(id, req) → ReviewComment
│   ├── DeleteReviewComment(id)
│   └── ResolveAllReviewComments(assetId) → (count, error)
├── Mindmap 相关
│   ├── CreateMindmap(projectId, req) → Mindmap
│   ├── ListMindmaps(projectId) → []Mindmap
│   ├── GetMindmap(id) → Mindmap
│   ├── UpdateMindmap(id, req) → Mindmap
│   └── DeleteMindmap(id)
├── Issue 相关
│   ├── ListIssues(projectId, state, limit, offset) → []Issue
│   ├── GetIssue(id) → Issue
│   ├── GetIssueByNumber(projectId, number) → Issue
│   ├── CreateIssue(projectId, req) → Issue
│   ├── UpdateIssue(id, req) → Issue
│   ├── DeleteIssue(id)
│   ├── ListIssueComments(issueId) → []IssueComment
│   ├── CreateIssueComment(issueId, req) → IssueComment
│   ├── UpdateIssueComment(id, req) → IssueComment
│   └── DeleteIssueComment(id)
├── Label 相关
│   ├── ListLabels(projectId) → []Label
│   ├── GetIssueLabels(issueId) → []Label
│   ├── CreateLabel(projectId, req) → Label
│   ├── UpdateLabel(id, req) → Label
│   ├── DeleteLabel(id)
│   ├── AddLabelToIssue(issueId, labelId)
│   ├── RemoveLabelFromIssue(issueId, labelId)
│   ├── GetIssueAssignees(issueId) → []string
│   ├── AddAssignee(issueId, assignee)
│   └── RemoveAssignee(issueId, assignee)
├── Milestone 相关
│   ├── ListMilestones(projectId, state) → []Milestone
│   ├── GetMilestone(id) → Milestone
│   ├── CreateMilestone(projectId, req) → Milestone
│   ├── UpdateMilestone(id, req) → Milestone
│   └── DeleteMilestone(id)
├── Reaction 相关
│   ├── ListReactions(targetType, targetId) → []Reaction
│   ├── AddReaction(targetType, targetId, req) → Reaction
│   ├── GetReaction(id) → Reaction
│   ├── RemoveReaction(targetType, targetId, author, emoji)
│   └── GetReactionSummary(targetType, targetId, currentUser) → map
└── Changes 相关
    └── ListChanges(since, limit) → []Change
```

### 3.2 两种实现（当前阶段）

| 实现 | 存储后端 | 使用场景 |
|---|---|---|
| **SQLiteStore** | 本地 SQLite 文件 | 本地模式默认，从当前 `store.DB` 重构而来 |
| **RemoteStore** | 远程 HTTP API | 云模式，本地 Server 通过 HTTP 调用远程存储服务 |

> **MySQLStore** 在终极架构（见第 16 章）中需要实现，届时 Go 程序直连 MySQL，不再需要独立的 Java 服务。

### 3.3 RemoteStore 设计

RemoteStore 是一个 HTTP 客户端，实现了 Store 接口，将每个 Store 方法映射为对远程存储服务的 HTTP 请求：

```
RemoteStore.CreateProject(req)
  → POST {server_url}/api/v1/projects
    Headers: Authorization: Bearer <token>
    Body: { name, description, gitlab_repo_url }
  ← 200: { existing project }  // gitlab_repo_url 已存在时返回已有项目
  ← 201: { new project }

RemoteStore.GetProjectsByIDs(ids)
  → GET {server_url}/api/v1/projects?ids=id1,id2,id3
    Headers: Authorization: Bearer <token>
  ← 200: [ project objects ]

RemoteStore.UpdateAsset(id, req)
  → PUT {server_url}/api/v1/assets/{id}
    Headers: Authorization: Bearer <token>
    Body: { title, content, status, expected_version: 4 }
  ← 200: { updated asset }
  ← 409: { error: "version conflict", current_version: 5 }
```

RemoteStore 在构造时注入 `server_url` 和 `token`，所有请求自动携带 `Authorization: Bearer <token>` 头。

### 3.4 存储后端与上层代码的解耦关系

```
┌─────────────────────────────────────────────┐
│ 上层代码（不感知存储后端）                      │
│  ├─ api/ handlers                           │
│  ├─ mcp/ MCP server                         │
│  ├─ agent/ Agent backends                   │
│  └─ WebSocket / Chat                        │
├─────────────────────────────────────────────┤
│ Store 接口                                   │
├──────────┬──────────┬───────────────────────┤
│ SQLiteStore │ MySQLStore │ RemoteStore (HTTP) │
└──────────┴──────────┴───────────────────────┘
```

---

## 4. 远程存储服务设计（独立服务）

> 本章描述的远程存储服务是一个**独立的 REST API 服务**，可以用 Go 实现（复用 MySQLStore），也可以用 Java 等其他语言独立实现。设计上与 BoxNex 主程序解耦，仅通过 HTTP API 交互。

### 4.1 服务职责

远程存储服务**仅负责**：
1. 数据的 CRUD 持久化（MySQL 或 SQLite）
2. Token 鉴权和 namespace 隔离
3. 变更流水记录（changes 表）
4. 版本冲突检测
5. GitLab 仓库 URL 去重判断

**不负责**：
- Agent 执行
- WebSocket / Chat 管理
- MCP 服务
- Git 本地操作
- 前端服务

### 4.2 部署方式

远程存储服务作为独立项目部署（推荐使用 Java/Spring Boot 实现，详见第 15 章），部署到 k8s 集群：

- 独立的 REST API 服务 + Token 鉴权中间件 + MySQL 数据库连接
- 不运行 Agent、MCP、ChatHub 等 BoxNex 业务逻辑
- Go 侧通过 `RemoteStore` HTTP 客户端与之交互

### 4.3 REST API 设计

API 前缀：`/api/v1`（独立版本号，与 BoxNex 主 API 区分）

#### 4.3.1 鉴权

所有请求必须携带 `Authorization: Bearer <token>`。

鉴权中间件流程：
1. 从 Header 提取 token
2. 计算 token 的 SHA-256 哈希
3. 在 `tokens` 表中查找匹配记录
4. 校验 `is_active`、`expires_at`
5. 提取 `namespace_id`、`user_name`
6. 注入到请求上下文中

未通过鉴权返回 `401 Unauthorized`。

#### 4.3.2 API 端点一览

**Project**

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | /api/v1/projects | 创建项目（按 gitlab_repo_url 在 namespace 内去重，已存在则返回已有项目） |
| GET | /api/v1/projects?ids=a,b,c | 按 ID 批量获取项目（客户端传入本地已映射的 ID 列表） |
| GET | /api/v1/projects/{id} | 获取项目详情（校验 namespace 归属） |
| PUT | /api/v1/projects/{id} | 更新项目（含 expected_version 冲突检测） |
| DELETE | /api/v1/projects/{id} | 删除项目 |

**Task**

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | /api/v1/tasks | 创建任务（自动注入 namespace） |
| GET | /api/v1/tasks | 列出当前 namespace 下的任务 |
| GET | /api/v1/tasks/{id} | 获取任务详情 |
| PUT | /api/v1/tasks/{id} | 更新任务（含 expected_version 冲突检测） |
| DELETE | /api/v1/tasks/{id} | 删除任务 |
| POST | /api/v1/tasks/{id}/assets | 关联任务与资产 |
| GET | /api/v1/tasks/{id}/assets | 列出任务关联的资产 |
| DELETE | /api/v1/tasks/{id}/assets/{assetId} | 取消关联 |

**Session**（私有数据，仅创建者可操作，不触发 changes 记录）

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | /api/v1/sessions | 创建会话（自动注入 namespace + created_by） |
| GET | /api/v1/sessions | 列出当前用户自己创建的会话（按 namespace_id + created_by 过滤） |
| GET | /api/v1/sessions/{id} | 获取会话详情（校验 created_by） |
| PUT | /api/v1/sessions/{id} | 更新会话（校验 created_by） |
| DELETE | /api/v1/sessions/{id} | 删除会话（校验 created_by） |
| POST | /api/v1/sessions/{id}/refs | 创建会话引用 |
| GET | /api/v1/sessions/{id}/refs | 列出会话引用 |
| DELETE | /api/v1/sessions/{id}/refs/{refId} | 删除会话引用 |

**Asset**

> Asset 本身没有 `namespace_id` 字段，通过所属 Project 间接归属 namespace。远端 API 处理 `GET/PUT/DELETE /api/v1/assets/{id}` 时，需 JOIN projects 表校验 namespace 归属。

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | /api/v1/projects/{projectId}/assets | 创建资产（通过 projectId 间接校验 namespace） |
| GET | /api/v1/projects/{projectId}/assets | 列出项目下资产 |
| GET | /api/v1/assets/{id} | 获取资产详情（JOIN projects 校验 namespace） |
| PUT | /api/v1/assets/{id} | 更新资产（**必须带 expected_version，用于冲突检测**） |
| DELETE | /api/v1/assets/{id} | 删除资产 |
| GET | /api/v1/assets/{id}/versions | 列出资产版本历史 |
| GET | /api/v1/assets/{id}/versions/{version} | 获取指定版本内容 |

**Message**（私有数据，随 session 隔离，不触发 changes 记录）

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | /api/v1/sessions/{sessionId}/messages | 创建消息（校验 session 归属） |
| GET | /api/v1/sessions/{sessionId}/messages | 列出会话消息（校验 session 归属） |
| DELETE | /api/v1/sessions/{sessionId}/messages | 删除会话所有消息（校验 session 归属） |

**ReviewComment**

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | /api/v1/assets/{assetId}/comments | 创建评审评论 |
| GET | /api/v1/assets/{assetId}/comments | 列出评审评论 |
| PUT | /api/v1/comments/{id} | 更新评论 |
| DELETE | /api/v1/comments/{id} | 删除评论 |

**Changes（变更通知）**

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | /api/v1/changes?since={changeId}&limit={n} | 拉取指定 ID 之后的变更记录 |

**Info**

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | /api/v1/info | 获取当前用户的 namespace、user_name 等信息 |

#### 4.3.3 响应格式

**成功响应**：直接返回实体 JSON，不做 data/meta 包装（保持简单，与 Go 本地 API 一致）：

```json
// 单个实体
{ "id": "xxx", "name": "...", "version": 1, ... }

// 列表
[{ "id": "xxx", ... }, { "id": "yyy", ... }]
```

**错误响应**：

```json
{ "error": "version conflict: expected 4, current is 5" }
```

**常用 HTTP 状态码**：

| HTTP 状态码 | 说明 |
|---|---|
| 401 | Token 无效或过期 |
| 403 | 无权访问该 namespace 下的资源 |
| 404 | 资源不存在 |
| 409 | 版本冲突，需要先拉取最新版本 |
| 410 | Changes 游标失效（since 指向的记录已被清理） |
| 422 | 请求参数校验失败 |

---

## 5. 数据模型设计

### 5.0 数据共享性分类

并非所有实体都适合在团队间共享和追踪。需要明确区分**共享数据**和**私有数据**：

| 实体 | 共享性 | 说明 | changes 追踪 | 版本冲突检测 |
|---|---|---|---|---|
| Project | **共享（客户端过滤）** | 项目数据存于远端 namespace 下，客户端仅展示本地已添加的项目 | 是 | 是 (version) |
| Task | **共享** | 任务是团队协作的工作单元 | 是 | 是 (version) |
| Asset | **共享** | 资产（需求文档、设计文档等）是核心协作产物 | 是 | 是 (current_version) |
| AssetVersion | **共享** | 资产的版本快照，随 asset 共享 | 否（通过 asset 变更间接体现） | 否 |
| ReviewComment | **共享** | 对共享资产的评审评论 | 是 | 否（追加型数据，冲突风险低） |
| TaskAsset | **共享** | 任务与资产的关联关系 | 是（通过 task 变更间接体现） | 否 |
| Mindmap | **共享** | 思维导图，属于项目维度的协作产物 | 是 | 否 |
| Issue | **共享** | 问题跟踪，属于项目维度 | 是 | 否 |
| IssueComment | **共享** | Issue 评论，追加型 | 是 | 否 |
| Label | **共享** | 项目标签 | 是 | 否 |
| Milestone | **共享** | 项目里程碑 | 是 | 否 |
| Reaction | **共享** | 对 Issue/Comment 的表情反应 | 是 | 否 |
| Session | **私有** | 用户个人与 AI 的对话会话，他人无需感知 | 否 | 否 |
| ChatMessage | **私有** | 会话中的每条消息，属于私人对话记录 | 否 | 否 |
| SessionRef | **私有** | 会话的上下文引用，属于会话的一部分 | 否 | 否 |

**关键设计决策：**

- **Session / ChatMessage / SessionRef 是私有数据**：用户自己创建的 AI 对话会话及其消息，其他用户没有必要感知，也不会产生协作冲突。因此这些表不需要 `version` 字段，不写入 `changes` 流水表。
- 私有数据仍然存储在远程（云模式下），同样归属于 namespace，以便将来在需要时可以开放共享能力（如原始需求中提到的"未来需要时甚至可以共享 session 信息"）。但当前阶段仅创建者自己可见。
- **共享数据**才需要 changes 追踪和版本冲突管理。

### 5.1 新增表

#### `namespaces` — 命名空间/团队

| 字段 | 类型 | 说明 |
|---|---|---|
| id | VARCHAR(36) PK | UUID |
| name | VARCHAR(255) UNIQUE | 命名空间名称（如 "sre-team"） |
| description | TEXT | 描述 |
| created_at | DATETIME | 创建时间 |
| updated_at | DATETIME | 更新时间 |

#### `tokens` — 鉴权令牌

| 字段 | 类型 | 说明 |
|---|---|---|
| id | VARCHAR(36) PK | UUID |
| token_hash | VARCHAR(255) UNIQUE | Token 的 SHA-256 哈希值 |
| namespace_id | VARCHAR(36) FK → namespaces.id | 绑定的 namespace |
| user_name | VARCHAR(255) | 绑定的用户名 |
| description | TEXT | Token 描述（如 "Eric 的开发机"） |
| is_active | BOOLEAN | 是否启用 |
| created_at | DATETIME | 创建时间 |
| expires_at | DATETIME NULL | 过期时间（NULL 表示永不过期） |

#### `changes` — 变更流水表

> 仅记录**共享数据**的变更。Session、ChatMessage、SessionRef 等私有数据不写入此表。

| 字段 | 类型 | 说明 |
|---|---|---|
| id | BIGINT AUTO_INCREMENT PK | 自增 ID，客户端用此做增量拉取游标 |
| namespace_id | VARCHAR(36) | 所属 namespace |
| entity_type | VARCHAR(50) | 实体类型（仅共享实体）：project / task / asset / comment |
| entity_id | VARCHAR(36) | 实体 ID |
| action | VARCHAR(20) | 操作类型：create / update / delete |
| summary | TEXT | 变更摘要（可选，如 "title changed", "version 4 → 5"） |
| user_name | VARCHAR(255) | 操作人 |
| entity_version | INT NULL | 变更后的实体版本号（仅对有 version 字段的实体有值） |
| created_at | DATETIME | 变更时间 |

索引：`INDEX idx_changes_ns_id (namespace_id, id)` —— 用于 `WHERE namespace_id = ? AND id > ? ORDER BY id LIMIT ?` 的高效查询。

### 5.2 现有表扩展

> 以下扩展字段在远程存储服务使用的数据库（MySQL 或 SQLite）中存在。本地纯 SQLite 模式下，这些字段不是必需的（本地单用户无 namespace 概念），但为统一 Store 接口，建议 SQLite 也加上这些字段并填充默认值。

#### `projects` — 项目（扩展字段）

| 新增字段 | 类型 | 说明 |
|---|---|---|
| namespace_id | VARCHAR(36) | 所属 namespace（本地模式填默认值 "local"） |
| gitlab_repo_url | VARCHAR(512) | GitLab 远端仓库 URL（标准化后），namespace 内唯一 |
| created_by | VARCHAR(255) | 第一个创建者的用户名 |
| version | INT DEFAULT 1 | 乐观锁版本号，每次更新 +1 |

唯一约束：`UNIQUE(namespace_id, gitlab_repo_url)` —— 同一 namespace 内同一个 GitLab 仓库只能有一个项目。

> 注意：`git_repo_path`（本地仓库路径）不存储在远端 projects 表中，因为每个用户、每台机器、甚至同一仓库的不同克隆目录路径都不同。本地路径保存在各自机器的 `config.json` 中（见 5.4 节）。本地模式下仍使用现有 `projects.git_repo_path` 字段（单用户无此问题）。

#### `tasks` — 任务（扩展字段）

| 新增字段 | 类型 | 说明 |
|---|---|---|
| namespace_id | VARCHAR(36) | 所属 namespace |
| project_id | VARCHAR(36) | 可选关联项目 |
| created_by | VARCHAR(255) | 创建者 |
| version | INT DEFAULT 1 | 乐观锁版本号 |

#### `sessions` — 会话（扩展字段）

| 新增字段 | 类型 | 说明 |
|---|---|---|
| namespace_id | VARCHAR(36) | 所属 namespace |
| created_by | VARCHAR(255) | 创建者 |

> Session 为私有数据（参见 5.0 节），不需要 `version` 字段。

#### `assets` — 资产

现有 `current_version` 字段已用于内容版本管理，可直接复用为冲突检测版本号。

无需额外添加 `version` 字段，`current_version` 即乐观锁版本号。

### 5.3 本地 SQLite 兼容策略

为了让 Store 接口统一，本地 SQLite 也添加上述扩展字段，但使用默认值：

- `namespace_id` 默认值 `"local"`
- `created_by` 默认值 `"local"`
- `version` 默认值 `1`（仅共享实体：projects、tasks）
- `gitlab_repo_url` 默认值 `""`（本地模式不强制要求）

这样 SQLiteStore 和 MySQLStore 可以使用完全相同的 SQL 查询逻辑，仅连接方式和 SQL 方言微有差异。

### 5.4 本地配置文件（云模式专用）

云模式下，虽然业务数据全部存储在远程，但本地仍需要保存**本地路径与远程项目的对应关系**。这个信息天然属于本地（不同机器路径不同、同一仓库可能克隆多份），不适合存到远端。

**使用 JSON 配置文件**：`~/.boxnex/config.json`，用户可以直接用文本编辑器查看和修改（例如本地目录 mv 了，改一下路径即可）。

#### 配置文件格式

```json
{
  "server_url": "https://boxnex-api.example.com",
  "token": "nex_a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4",
  "projects": [
    {
      "remote_id": "proj_abc123",
      "git_repo_path": "/Users/eric/code/my-service",
      "gitlab_repo_url": "https://gitlab.example.com/group/my-service"
    },
    {
      "remote_id": "proj_abc123",
      "git_repo_path": "/Users/eric/code/my-service-v2",
      "gitlab_repo_url": "https://gitlab.example.com/group/my-service"
    },
    {
      "remote_id": "proj_def456",
      "git_repo_path": "/Users/eric/code/another-repo",
      "gitlab_repo_url": "https://gitlab.example.com/group/another-repo"
    }
  ]
}
```

> `server_url` 和 `token` 必须由用户在 config.json 中手动配置。启动云模式（`--store remote`）时，程序从此文件读取这两个字段，若缺失则报错退出。

**设计要点：**

- 纯文本 JSON，用户可手动编辑（mv 了目录改一下 `git_repo_path` 就行）
- 同一个 `remote_id` 可以出现多次（同一仓库 clone 到不同目录）
- 同一用户在不同机器上各自有自己的 `config.json`，互不影响
- `gitlab_repo_url` 冗余存储，方便用户阅读和排查

**工作流：**

1. 用户添加项目时：本地检测 GitLab URL → 调远端创建或获取项目 → 拿到 `remote_id` → 追加到 `config.json` 的 `projects` 数组
2. 列出项目时：读 `config.json` 拿到所有 `remote_id`，去重后批量从远端获取项目详情
3. 操作项目下的 asset 时：通过 `config.json` 查到 `remote_id`，调远端 API
4. 移除本地项目映射：从 `config.json` 删除对应条目，远端数据不受影响

---

## 6. 变更通知机制

### 6.1 设计思路

采用**客户端轮询**（Pull）模式，而非 WebSocket 推送：

- 简单可靠，无状态，兼容各种网络环境
- 远程存储服务不需要维护长连接
- 客户端周期性拉取 `changes` 表的增量记录

### 6.2 工作流程

```
┌──── 客户端 A（写入方） ────┐      ┌──── 客户端 B（接收方） ────┐
│                            │      │                            │
│ 1. 更新 Asset v4→v5       │      │ 3. 轮询: GET /changes      │
│    PUT /assets/{id}        │      │    ?since=100&limit=50     │
│    { expected_version: 4 } │      │                            │
│           │                │      │ 4. 收到变更记录:            │
│           ▼                │      │    [{                      │
│ 2. 服务端写入 assets 表    │      │      id: 101,              │
│    同时写入 changes 表:    │      │      entity_type: "asset", │
│    {                       │      │      entity_id: "xxx",     │
│      entity_type: "asset", │      │      action: "update",     │
│      entity_id: "xxx",     │      │      entity_version: 5,   │
│      action: "update",     │      │      user_name: "alice"   │
│      entity_version: 5,    │      │    }]                     │
│      user_name: "alice"    │      │           │                │
│    }                       │      │           ▼                │
│                            │      │ 5. 检测到 asset 变更,      │
│                            │      │    重新拉取最新数据         │
│                            │      │    GET /assets/{id}        │
│                            │      │                            │
└────────────────────────────┘      └────────────────────────────┘
```

### 6.3 客户端轮询策略

- 客户端维护 `lastChangeId`（上次拉取到的最大 change.id）
- 初始值为 0，首次拉取获取全部历史（或通过 limit 控制）
- 轮询间隔：正常 5~10 秒；检测到有变更时可临时加快到 2 秒
- 请求：`GET /api/v1/changes?since={lastChangeId}&limit=100`
- 响应包含变更列表，客户端按 `entity_type` + `entity_id` 判断需要刷新哪些数据

### 6.4 客户端处理逻辑

收到变更列表后，客户端按以下逻辑处理：

| entity_type | action | 客户端行为 |
|---|---|---|
| project | create/update | 刷新项目列表或项目详情 |
| project | delete | 从本地列表中移除 |
| task | create/update | 刷新任务列表或任务详情 |
| task | delete | 从本地列表中移除 |
| asset | create/update | 刷新资产列表或资产详情；若用户正在编辑该资产，提示有新版本 |
| asset | delete | 从本地列表中移除 |
| comment | create/update | 如果用户正在查看对应资产，刷新评论列表 |
| comment | delete | 从评论列表中移除 |

**关于可见性：**
- 私有数据的变更不会出现在 changes 表中（参见 5.0 节数据共享性分类）。
- Asset 的变更在 changes 表中一视同仁地记录，不区分客户端是否加入了对应 project。客户端收到 asset 变更通知后，若发现本地并未加入该 asset 所属的 project，直接忽略即可。

### 6.5 Changes 表写入时机

远程存储服务仅在**共享数据**的写操作时写入 changes 记录：

**写入 changes 的操作：**
- Project 的 Create / Update / Delete
- Task 的 Create / Update / Delete
- Task 的 LinkTaskAsset / UnlinkTaskAsset（entity_type 记为 `"task"`，summary 中注明关联变更，如 "linked asset xxx"）
- Asset 的 Create / Update / Delete
- ReviewComment 的 Create / Update / Delete

私有数据（Session、ChatMessage、SessionRef）的操作不写入 changes 表。

写入 changes 记录与实体数据更新在**同一个数据库事务**中完成，保证一致性。

### 6.6 Changes 表清理

changes 表会持续增长，需定期清理（具体策略见第 14 节）。客户端需配合处理清理后的游标失效：若 `lastChangeId` 指向已被清理的记录，服务端在响应中标记 `X-Changes-Reset: true`，客户端收到后做全量数据刷新并重置游标。

---

## 7. 版本冲突管理

### 7.1 乐观锁机制

采用**乐观锁**（Optimistic Locking）策略，仅对共享数据（参见 5.0 节）通过版本号防止并发写入冲突：

- 需要版本校验的实体及其版本字段参见 5.0 节分类表
- 客户端更新时必须携带 `expected_version`（即客户端当前持有的版本号）
- 服务端更新前校验：若当前版本 ≠ expected_version，拒绝更新，返回 409

### 7.2 冲突检测实现

直接在 UPDATE 的 WHERE 条件中带上版本号，通过 `affected_rows` 判断是否成功：

```sql
UPDATE assets
SET content = ?, current_version = current_version + 1, updated_at = NOW()
WHERE id = ? AND current_version = ?
```

- `affected_rows = 1` → 更新成功，无冲突
- `affected_rows = 0` → 版本不匹配（他人已更新），返回 409

同理适用于 projects、tasks 等带 `version` 字段的实体：

```sql
UPDATE tasks
SET title = ?, status = ?, version = version + 1, updated_at = NOW()
WHERE id = ? AND version = ?
```

一条 SQL 完成检测和更新，无需先 SELECT 再 UPDATE。

### 7.3 客户端冲突处理

当客户端收到 409 VERSION_CONFLICT 时：

1. 提示用户："该资产已被其他用户更新为版本 5，请先查看最新版本"
2. 客户端拉取最新版本数据
3. 用户自行对比差异，决定如何处理（覆盖 / 合并 / 放弃）
4. 用户确认后，以最新版本号重新提交

### 7.4 版本校验适用范围

哪些实体需要版本校验、使用哪个版本字段，参见 5.0 节数据共享性分类表。

> Delete 操作不做版本校验（最后一个删除者胜出）。Create 操作无需版本校验。

---

## 8. Namespace 与鉴权模型

### 8.1 Token 生命周期

1. 管理员在数据库中创建 `namespaces` 记录
2. 管理员生成 token（随机字符串），将其哈希写入 `tokens` 表，绑定 namespace + user_name
3. 将明文 token 分发给对应团队成员
4. 团队成员将 token 配置到本地 `~/.boxnex/config.json` 的 `token` 字段
5. 所有请求自动携带 token，服务端解析得到身份

### 8.2 Token 格式

Token 为不透明随机字符串，建议格式：`nex_` + 32 位随机 hex，例如：

```
nex_a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4
```

服务端存储的是 SHA-256(token) 的哈希值，明文 token 仅在生成时出现一次。

### 8.3 Namespace 隔离与可见性规则

**共享数据 — 项目维度（客户端侧过滤）：**

| 实体 | 远端隔离方式 | 客户端可见性 |
|---|---|---|
| Project | namespace_id 过滤 | 客户端仅展示本地 `config.json` 中映射了的项目 |
| Asset | 属于 Project | 仅本地添加了所属 Project 的客户端可见 |
| ReviewComment | 属于 Asset | 同上 |

**共享数据 — namespace 维度（服务端过滤）：**

| 实体 | 远端隔离方式 | 可见性 |
|---|---|---|
| Task | namespace_id 过滤 | namespace 内所有成员可见 |

**私有数据**（Session、ChatMessage、SessionRef）仅创建者可见，通过 `namespace_id + created_by` 隔离（详见 5.0 节）。

**隔离原则：**
- 跨 namespace 完全不可见、不可访问（服务端强制）
- **Project 的可见性由客户端本地映射控制**：远端不做用户级别的项目权限管理，而是由客户端根据本地 `config.json` 决定展示哪些项目。用户必须主动选择一个本地 Git 仓库目录来"添加"项目，添加时本地记录映射关系
- 这通过 GitLab 仓库权限实现了**天然的访问控制**：没有 GitLab 仓库访问权限的人无法 clone 仓库，自然无法在本地选择该仓库目录，也就无法添加项目
- 远端 API（如 `GET /api/v1/projects/{id}`）仍需校验 namespace 归属，但不做用户级别的项目成员校验

---

## 9. 项目创建流程（云模式）

### 9.1 流程

```
用户在前端输入本地 Git 仓库路径
         │
         ▼
本地 Go Server 验证路径是否存在、是否是 Git 仓库
         │
         ▼
本地 Go Server 执行 `git remote -v` 提取远端 URL
         │
         ├── 未找到 GitLab 远端 → 拒绝创建，提示"云模式下必须关联 GitLab 仓库"
         │
         ▼
标准化 GitLab URL（见 9.2）
         │
         ▼
本地调用 RemoteStore.CreateProject()
  → POST /api/v1/projects
    { name, description, gitlab_repo_url }
         │
         ▼
远程存储服务处理:
  检查 namespace 内是否已存在相同 gitlab_repo_url 的项目
         │
         ├── 已存在 → 直接返回已有项目（HTTP 200）
         │
         └── 不存在 → 创建新项目记录，写入 changes，返回 201
         │
         ▼
本地 Server 收到远端返回的项目信息
         │
         ▼
写入本地 config.json 映射:
  { remote_id, git_repo_path（本地路径）, gitlab_repo_url }
         │
         ▼
前端显示项目（若为已有项目，提示"已加入现有项目"）
```

### 9.2 GitLab URL 标准化

为确保唯一性判断的准确性，GitLab URL 需要标准化处理：

- 移除 `.git` 后缀
- 统一为 HTTPS 格式
- 小写化处理

示例：
- `git@gitlab.example.com:group/project.git` → `https://gitlab.example.com/group/project`
- `https://gitlab.example.com/GROUP/Project.git` → `https://gitlab.example.com/group/project`

标准化逻辑在**本地 Go Server** 侧完成（在调用 RemoteStore 之前），也在**远程存储服务**侧做二次校验。

---

## 10. 本地 Server 启动流程对比

### 10.1 本地模式（默认）

```
解析命令行参数
    │
    ▼
初始化 SQLiteStore (data-dir/boxnex.db)
    │
    ▼
启动 MCP Server
    │
    ▼
初始化 Agent backends (Claude/Codex/Cursor)
    │
    ▼
创建 ChatHub
    │
    ▼
创建 API Server { Store: sqliteStore, Agents, Hub, ... }
    │
    ▼
注册路由 (api/ 下所有 handler)
    │
    ▼
启动 HTTP Server
```

### 10.2 云模式

```
解析命令行参数 (--store remote)
    │
    ▼
加载本地 config.json (~/.boxnex/config.json)
    │  - 读取 server_url 和 token（缺失则报错退出）
    │  - 读取已添加的项目映射列表
    │  - 若文件不存在则报错退出，提示用户先配置
    │
    ▼
初始化 RemoteStore (server_url, token)
    │
    ├── 调用 GET /api/v1/info 验证 token 有效性
    │   拿到 namespace、user_name
    │   失败则退出，提示 token 无效
    │
    ▼
启动 MCP Server（MCP 内部也使用 RemoteStore）
    │
    ▼
初始化 Agent backends（与本地模式完全一致）
    │
    ▼
创建 ChatHub
    │
    ▼
创建 API Server { Store: remoteStore, Agents, Hub, Mode: "cloud", ... }
    │
    ▼
注册路由（同本地模式，但 API handler 中部分行为因 Mode 而异）
    │
    ├── 创建项目时：本地做 Git 检测 + GitLab URL 提取
    ├── GET /api/info 返回 mode: "cloud", namespace, user 等信息
    │
    ▼
启动变更轮询 goroutine
    │  - 每 5~10 秒调用 RemoteStore.ListChanges(since)
    │  - 检测到变更时通过 ChatHub 广播给前端 WebSocket
    │
    ▼
启动 HTTP Server
```

---

## 11. 前端适配

### 11.1 模式感知

前端通过 `GET /api/info` 获取当前运行模式信息：

```json
{
  "mode": "cloud",
  "namespace": "sre-team",
  "user": "eric",
  "server_url": "https://boxnex-api.example.com",
  "version": "1.0.0"
}
```

前端根据 `mode` 字段调整 UI 行为。

### 11.2 云模式 UI 差异

| 功能 | 本地模式 | 云模式 |
|---|---|---|
| 项目创建 | 可任意本地路径 | 必须是含 GitLab 远端的 Git 仓库 |
| 项目列表 | 本地全部项目 | 仅显示本地已添加的项目（config.json 中映射的） |
| 任务列表 | 本地全部任务 | namespace 内共享任务 |
| 会话列表 | 本地全部会话 | 仅显示自己创建的会话（私有） |
| 资产更新 | 直接保存 | 保存时可能收到版本冲突提示 |
| 侧边栏 | 无 namespace 信息 | 显示 namespace 和当前用户 |

### 11.3 变更通知 UI

前端需要处理从 WebSocket 广播收到的变更通知（仅共享数据）：

- 资产被他人更新：页面顶部提示"该资产已有新版本，点击刷新"
- 新任务创建：任务列表自动刷新
- 任务状态变更：任务列表/详情自动刷新
- 项目信息变更：项目列表自动刷新
- 新评论：若正在查看对应资产，刷新评论列表
- 版本冲突：弹窗提示，引导用户查看最新版本并处理冲突

> 私有数据的变更不会产生通知（参见 5.0 节）。

### 11.4 版本冲突 UI 流程

```
用户编辑 Asset（本地版本 v4）
    │
    ▼
用户点击保存
    │
    ▼
前端发起: PUT /api/assets/{id} { content: "...", expected_version: 4 }
    │
    ├── 200 OK → 保存成功，更新本地版本号为 v5
    │
    └── 409 VERSION_CONFLICT → 弹出冲突对话框
        │
        ├── 显示: "该资产已被 alice 更新到版本 5"
        ├── 选项 1: 查看最新版本（拉取 v5，放弃本地修改）
        ├── 选项 2: 强制覆盖（以 v5 为 expected_version 重新提交）
        └── 选项 3: 对比差异（显示 v4 → v5 的 diff + 本地修改）
```

---

## 12. 代码结构（当前实际）

```
server/
├── main.go                     # 入口，根据 --store 参数选择 SQLiteStore / RemoteStore
├── store/
│   ├── store.go                # Store 接口定义 + ErrVersionConflict
│   ├── models.go               # 所有实体和请求/响应结构体
│   ├── db.go                   # SQLite 初始化和 schema 迁移
│   ├── sqlite.go               # SQLiteStore 结构体 + NewSQLiteStore
│   ├── sqlite_core.go          # SQLiteStore 核心实体方法（Project/Task/Session/Asset/Message/...）
│   ├── sqlite_issues.go        # SQLiteStore Issues/Labels/Milestones/Reactions 方法
│   └── remote.go               # RemoteStore HTTP 客户端（覆盖全部 Store 接口）
├── config/
│   └── config.go               # 本地 config.json 管理（Manager + ProjectMapping）
├── api/
│   ├── router.go               # Chi 路由注册 + Server 结构体
│   ├── models.go               # 类型别名（api.Project = store.Project 等）
│   ├── hub.go                  # ChatHub（WebSocket 聊天广播）
│   ├── helpers.go              # GetServerInfo / Settings / BrowseDir
│   ├── projects.go             # Project CRUD handler（含云模式 config.json 映射）
│   ├── tasks.go                # Task CRUD handler
│   ├── sessions.go             # Session CRUD handler
│   ├── assets.go               # Asset CRUD handler
│   ├── chat.go                 # WebSocket 聊天 handler
│   ├── git.go                  # Git 操作 handler
│   ├── terminal.go             # Terminal PTY handler
│   └── ...
├── agent/                      # Agent 后端（Claude/Codex/Cursor）
├── mcp/                        # MCP Server
├── ws/                         # WebSocket 基础 Hub
├── plugins/
│   ├── issues/                 # Issues 插件（handler + router，使用 store.Store）
│   └── okone/                  # OKOne 插件
└── frontend.go                 # 嵌入式前端 SPA
```

---

## 13. 分阶段实施计划

### Phase 1：Store 接口抽象 ✅ 已完成

- ✅ 定义统一 `store.Store` 接口（嵌入 ProjectStore、TaskStore、SessionStore 等子接口）
- ✅ 实现 `SQLiteStore`（合并原 api.Store、connect.Store、issues.Store）
- ✅ `api.Server.Store` 改为 `store.Store` 接口类型
- ✅ MCP Server 改为依赖 `store.Store` 接口
- ✅ 删除旧 `connect` 包（10 个文件）、`api/store.go`、`api/connect_client.go`
- ✅ Issues 和 OKOne 插件适配统一 Store
- ✅ 本地配置迁移到 `config.json`（`server/config/config.go`）
- ✅ 现有功能不受影响，`go build ./...` 通过

> 详见 `docs/store-architecture-refactoring.md`

### Phase 2：数据模型扩展 ✅ 已完成

- ✅ projects 表添加 namespace_id、gitlab_repo_url、created_by、version
- ✅ tasks 表添加 namespace_id、project_id、created_by、version
- ✅ sessions 表添加 namespace_id、created_by
- ✅ 创建 changes 表（变更流水，用于云模式增量轮询）
- ✅ 所有默认值填充（namespace_id="local"、created_by="local"、version=1）
- ✅ UpdateProject/UpdateTask 自动 version++
- ✅ 模型结构体（`store/models.go`）同步更新

### Phase 3：RemoteStore + 云模式基础 ✅ 已完成

- ✅ 实现 RemoteStore HTTP 客户端（`store/remote.go`，覆盖全部 Store 接口方法）
- ✅ 支持 `--store sqlite/remote` 命令行参数
- ✅ 云模式从 config.json 读取 server_url + token
- ✅ 启动时 Ping 验证远程连接
- ✅ 变更轮询 goroutine（每 5 秒 ListChanges → ChatHub 广播）
- ✅ `/api/info` 返回 mode: "local" / "cloud"

### Phase 4：Go 侧云模式完善 ✅ 已完成

- ✅ **乐观锁版本冲突**：UpdateProject/Task/Asset 请求体新增 `expected_version` 字段；SQLiteStore 在 WHERE 条件中校验版本，不匹配返回 `ErrVersionConflict`；RemoteStore 将 HTTP 409 映射为 `ErrVersionConflict`；API handler 返回 409 给前端
- ✅ **云模式项目列表过滤**：ListProjects 在 remote 模式下读取 config.json 映射列表，调用 GetProjectsByIDs；GetProject 覆盖本地 git_repo_path
- ✅ **`/api/info` 返回完整云模式信息**：RemoteStore.Ping 缓存 namespace + user_name，GetServerInfo 返回 server_url / namespace / user_name
- ✅ **云模式项目创建/删除流程**：CreateProject 在 remote 模式下不发送 git_repo_path 给远端，成功后自动写入 config.json 映射；DeleteProject 自动清理 config.json 映射
- ✅ **变更轮询优化**：自适应间隔（有数据 3s，无数据指数退避至 30s）；HTTP 410 自动重置游标
- ✅ **前端路径修复**：Tasks/Assets/Mindmaps 从 `/api/connect/*` 修正为 `/api/*`；移除 connectApi，ConnectPage 改为显示服务器模式信息

### Phase 5：Java 远程存储服务 ⏳ 待实现（独立项目）

> 由 Java 团队实现，Go 侧提供 RemoteStore 中定义的 REST API 契约（`store/remote.go`）。

- [ ] 实现 `/api/v1/*` REST API（参见 4.3.2 节端点一览）
- [ ] Token 鉴权中间件（SHA-256 哈希校验 → namespace_id + user_name 注入）
- [ ] MySQL 数据库 + 迁移（含 namespaces、tokens、changes 表）
- [ ] Namespace 隔离（所有查询过滤 namespace_id）
- [ ] 共享数据写操作同事务写入 changes 流水
- [ ] 乐观锁版本冲突检测（WHERE version = expected_version，affected_rows=0 → 409）
- [ ] 私有数据隔离（Session/Message 按 created_by 过滤）
- [ ] GitLab URL 去重（UNIQUE(namespace_id, gitlab_repo_url)）
- [ ] 统一响应格式（data + meta 包装，error code 体系）
- [ ] 数据清理定时任务（见第 14 章）

### Phase 6：前端适配 ⏳ 待实现

- [ ] 项目创建对话框增加 GitLab 校验（云模式下必填）
- [ ] 共享数据的版本冲突处理 UI（资产、任务、项目等）
- [ ] 变更通知提示（仅共享数据：项目、任务、资产、评论）
- [ ] namespace / 用户信息展示
- [ ] 共享实体列表显示创建者
- [ ] 会话列表仅显示自己的（私有）

### 不再需要的内容（当前阶段）

以下内容因架构决策调整而在当前阶段不实现：

- **MySQLStore / `--mysql-dsn`**：当前阶段由 Java 服务端管理 MySQL。终极架构阶段会在 Go 侧实现（见第 16 章）
- **`dataservice/` Go 模块 / `--mode serve`**：远程存储服务由 Java 独立实现
- **settings SQLite 表**：已删除，所有配置使用 `~/.boxnex/config.json`
- **connect 包 / WebSocket 资源通知**：已删除，共享数据变更通过 changes 表轮询

---

## 14 数据清理策略

远程存储服务需要定期清理 MySQL 中的过期数据，防止无限增长。通过定时任务（如 cron 或 k8s CronJob）执行：

| 数据 | 清理规则 | 说明 |
|---|---|---|
| changes 表 | 保留最近 **3 天**，删除更早的记录 | 客户端轮询间隔通常在秒级，3 天足够覆盖离线场景；超过 3 天未上线的客户端做全量刷新 |
| sessions 表 | 每个用户保留最近 **20 个**，删除更早的 | 按 `created_by` 分组，按 `created_at` 倒序保留 20 个，其余删除（CASCADE 自动清理关联的 session_refs） |
| chat_messages 表 | 每个 session 保留最近 **100 条**，删除更早的 | 按 `session_id` 分组，按 `created_at` 倒序保留 100 条，其余删除 |

**清理频率**：建议每天执行一次（低峰时段）。

**changes 表清理与客户端的配合**：客户端的 `lastChangeId` 如果指向的记录已被清理（即 `since` 参数对应的 ID 在表中不存在），服务端返回一个特殊标记（如响应头 `X-Changes-Reset: true`），客户端收到后放弃增量模式，做一次全量数据刷新，并将 `lastChangeId` 重置为当前最大 ID。

**其他清理**：
- 过期 token（`expires_at < NOW()` 且 `is_active = false`）：可定期清理，但不紧急

---

## 15 Java 远程存储服务实现指南

本章是 Java 服务端开发的完整参考。Go 客户端（`store/remote.go` 中的 `RemoteStore`）已实现全部 HTTP 调用，Java 只需实现对应的 REST 端点即可。

### 15.1 总体架构

```
BoxNex Go Server                       Java Data Service
┌────────────────┐    HTTP/JSON        ┌─────────────────┐
│  RemoteStore   │ ─── /api/v1/* ───▶  │  REST Controller │
│  (HTTP Client) │ ◀── JSON Response── │  + MySQL Storage │
└────────────────┘                     └─────────────────┘
```

- Java 服务是一个**纯 CRUD 数据服务**，无业务逻辑（AI 聊天、Git 操作等仍在 Go 侧）
- 所有端点以 `/api/v1/` 为前缀
- 请求认证：`Authorization: Bearer <token>`
- 响应格式：直接返回实体 JSON（不需要 data/meta 包装），错误返回 `{"error": "message"}`

### 15.2 认证与鉴权

**Token 校验流程**：

```
Request → Extract Bearer token → SHA-256 hash → 查 tokens 表
  → 找到: 注入 namespace_id + user_name 到请求上下文
  → 未找到/过期: 返回 401 {"error": "unauthorized"}
```

**数据库表**：

```sql
CREATE TABLE namespaces (
  id          VARCHAR(36) PRIMARY KEY,
  name        VARCHAR(100) NOT NULL UNIQUE,
  created_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE tokens (
  id            VARCHAR(36) PRIMARY KEY,
  namespace_id  VARCHAR(36) NOT NULL REFERENCES namespaces(id),
  user_name     VARCHAR(100) NOT NULL,
  token_hash    VARCHAR(64) NOT NULL UNIQUE,  -- SHA-256 hex
  is_active     BOOLEAN DEFAULT TRUE,
  expires_at    TIMESTAMP NULL,
  created_at    TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_tokens_hash ON tokens(token_hash);
```

### 15.3 数据隔离规则

| 规则 | 说明 |
|------|------|
| **Namespace 隔离** | 所有表的查询和写入都必须过滤 `namespace_id`（从 token 解析而来） |
| **私有数据** | `sessions`、`chat_messages` 按 `created_by` 过滤，只能看到自己的 |
| **共享数据** | `projects`、`tasks`、`assets`、`issues` 等同一 namespace 下的用户互相可见 |
| **GitLab URL 去重** | `projects` 表 `UNIQUE(namespace_id, gitlab_repo_url)`，防止同一仓库重复创建 |

### 15.4 完整 REST API 端点一览

> 以下端点全部来自 Go 的 `RemoteStore`（`store/remote.go`），每个端点对应一个方法调用。

#### 15.4.1 服务信息

| Method | Path | 说明 | 请求体 | 响应 |
|--------|------|------|--------|------|
| GET | `/api/v1/info` | 连通性检查 + 元数据 | - | `{"namespace": "...", "user_name": "..."}` |

#### 15.4.2 Projects（共享数据，需乐观锁）

| Method | Path | 说明 | 请求体 | 响应 |
|--------|------|------|--------|------|
| POST | `/api/v1/projects` | 创建项目 | `CreateProjectRequest` | `Project` |
| GET | `/api/v1/projects` | 列出项目（可选 `?ids=a,b,c` 过滤） | - | `[Project]` |
| GET | `/api/v1/projects/{id}` | 获取单个项目 | - | `Project` / 404 |
| PUT | `/api/v1/projects/{id}` | 更新项目 | `UpdateProjectRequest`（可含 `expected_version`） | `Project` / 409 |
| DELETE | `/api/v1/projects/{id}` | 删除项目 | - | 204 |

**Project 模型字段**：

```json
{
  "id": "uuid",
  "namespace_id": "from-token",
  "name": "string",
  "description": "string",
  "git_repo_path": "",
  "gitlab_repo_url": "string",
  "gitlab_token": "string",
  "extra": {},
  "created_by": "from-token",
  "version": 1,
  "created_at": "2024-01-01T00:00:00Z",
  "updated_at": "2024-01-01T00:00:00Z"
}
```

#### 15.4.3 Tasks（共享数据，需乐观锁）

| Method | Path | 说明 |
|--------|------|------|
| POST | `/api/v1/tasks` | 创建任务 |
| GET | `/api/v1/tasks?limit=N&offset=N` | 列出任务（分页） |
| GET | `/api/v1/tasks/{id}` | 获取任务 |
| PUT | `/api/v1/tasks/{id}` | 更新任务（可含 `expected_version`） |
| DELETE | `/api/v1/tasks/{id}` | 删除任务（含子任务） |
| POST | `/api/v1/tasks/{id}/ref-count` | ref_count + 1 |
| POST | `/api/v1/tasks/{taskId}/assets` | 关联 Asset（body: `{"asset_id": "..."}`) |
| GET | `/api/v1/tasks/{taskId}/assets` | 列出任务关联的 Assets |
| DELETE | `/api/v1/tasks/{taskId}/assets/{assetId}` | 取消关联 |

#### 15.4.4 Sessions（私有数据，无版本号）

| Method | Path | 说明 |
|--------|------|------|
| POST | `/api/v1/sessions` | 创建（注入 `created_by`，按 `created_by` 隔离） |
| GET | `/api/v1/sessions` | 列出**当前用户的** sessions |
| GET | `/api/v1/sessions/{id}` | 获取 |
| PUT | `/api/v1/sessions/{id}` | 更新（`name`/`project_id`/`agent`/`agent_session_id`/`stream_id`） |
| DELETE | `/api/v1/sessions/{id}` | 删除 |
| PUT | `/api/v1/sessions/reorder` | 重新排序（body: `{"session_ids": [...]}`) |
| POST | `/api/v1/sessions/{id}/refs` | 创建引用 |
| GET | `/api/v1/sessions/{id}/refs` | 列出引用 |
| DELETE | `/api/v1/refs/{id}` | 删除引用 |

#### 15.4.5 Assets（共享数据，用 current_version 做乐观锁）

| Method | Path | 说明 |
|--------|------|------|
| POST | `/api/v1/projects/{projectId}/assets` | 创建 |
| GET | `/api/v1/projects/{projectId}/assets?limit=N&offset=N&type=X` | 列出（可按 type 过滤） |
| GET | `/api/v1/assets/{id}` | 获取（含 content） |
| PUT | `/api/v1/assets/{id}` | 更新（`expected_version` = `current_version`） |
| DELETE | `/api/v1/assets/{id}` | 删除 |
| GET | `/api/v1/assets/{id}/versions` | 列出版本历史 |
| GET | `/api/v1/assets/{id}/versions?version=N` | 获取指定版本 |
| GET | `/api/v1/assets/{id}/content` | 获取最新内容 |
| GET | `/api/v1/projects/{projectId}/assets?title=X` | 按标题查找 |

**版本管理**：Asset 更新 content 时自增 `current_version` 并写入 `asset_versions` 表。

#### 15.4.6 Chat Messages（私有数据）

| Method | Path | 说明 |
|--------|------|------|
| POST | `/api/v1/sessions/{id}/messages` | 创建消息（body: `{"role": "...", "parts": [...], "metadata": {...}}`) |
| GET | `/api/v1/sessions/{id}/messages` | 列出消息 |
| DELETE | `/api/v1/sessions/{id}/messages` | 清空会话消息 |

**注意**：`parts` 和 `metadata` 为 JSON 类型，MySQL 中建议用 `JSON` 或 `TEXT` 列存储序列化后的 JSON 字符串。

#### 15.4.7 Review Comments（共享数据）

| Method | Path | 说明 |
|--------|------|------|
| POST | `/api/v1/assets/{id}/comments` | 创建评论 |
| GET | `/api/v1/assets/{id}/comments` | 列出评论 |
| GET | `/api/v1/comments/{id}` | 获取单条 |
| PUT | `/api/v1/comments/{id}` | 更新 |
| DELETE | `/api/v1/comments/{id}` | 删除 |
| POST | `/api/v1/assets/{id}/comments/resolve-all` | 关闭全部 open 评论（返回 `{"resolved_count": N}`) |

#### 15.4.8 Mindmaps（共享数据）

| Method | Path | 说明 |
|--------|------|------|
| POST | `/api/v1/projects/{projectId}/mindmaps` | 创建 |
| GET | `/api/v1/projects/{projectId}/mindmaps` | 列出 |
| GET | `/api/v1/mindmaps/{id}` | 获取 |
| PUT | `/api/v1/mindmaps/{id}` | 更新 |
| DELETE | `/api/v1/mindmaps/{id}` | 删除 |

#### 15.4.9 Issues（共享数据）

| Method | Path | 说明 |
|--------|------|------|
| POST | `/api/v1/issues?project_id=X` | 创建 Issue |
| GET | `/api/v1/issues?project_id=X&state=open&limit=N&offset=N` | 列出 |
| GET | `/api/v1/issues/{id}` | 获取 |
| GET | `/api/v1/issues/by-number/{number}?project_id=X` | 按编号获取 |
| PUT | `/api/v1/issues/{id}` | 更新 |
| DELETE | `/api/v1/issues/{id}` | 删除 |
| GET | `/api/v1/issues/{id}/comments` | 列出 Issue 评论 |
| POST | `/api/v1/issues/{id}/comments` | 创建评论 |
| PUT | `/api/v1/issue-comments/{id}` | 更新评论 |
| DELETE | `/api/v1/issue-comments/{id}` | 删除评论 |

#### 15.4.10 Labels（共享数据）

| Method | Path | 说明 |
|--------|------|------|
| GET | `/api/v1/labels?project_id=X` | 列出项目标签 |
| POST | `/api/v1/labels?project_id=X` | 创建标签 |
| PUT | `/api/v1/labels/{id}` | 更新标签 |
| DELETE | `/api/v1/labels/{id}` | 删除标签 |
| GET | `/api/v1/issues/{id}/labels` | 获取 Issue 的标签 |
| POST | `/api/v1/issues/{id}/labels` | 添加标签到 Issue |
| DELETE | `/api/v1/issues/{id}/labels/{labelId}` | 移除标签 |
| GET | `/api/v1/issues/{id}/assignees` | 获取 assignees |
| POST | `/api/v1/issues/{id}/assignees` | 添加 assignee |
| DELETE | `/api/v1/issues/{id}/assignees/{assignee}` | 移除 assignee |

#### 15.4.11 Milestones（共享数据）

| Method | Path | 说明 |
|--------|------|------|
| GET | `/api/v1/milestones?project_id=X&state=open` | 列出 |
| GET | `/api/v1/milestones/{id}` | 获取 |
| POST | `/api/v1/milestones?project_id=X` | 创建 |
| PUT | `/api/v1/milestones/{id}` | 更新 |
| DELETE | `/api/v1/milestones/{id}` | 删除 |

#### 15.4.12 Reactions（共享数据）

| Method | Path | 说明 |
|--------|------|------|
| GET | `/api/v1/reactions?target_type=X&target_id=Y` | 列出 |
| POST | `/api/v1/reactions?target_type=X&target_id=Y` | 添加 |
| GET | `/api/v1/reactions/{id}` | 获取 |
| DELETE | `/api/v1/reactions?target_type=X&target_id=Y&author=A&emoji=E` | 删除 |
| GET | `/api/v1/reactions/summary?target_type=X&target_id=Y&current_user=U` | 摘要 |

#### 15.4.13 Changes（变更流水）

| Method | Path | 说明 |
|--------|------|------|
| GET | `/api/v1/changes?since={lastId}&limit=100` | 拉取新变更 |

### 15.5 乐观锁实现

当 Update 请求的 body 中包含 `expected_version` 字段时：

```sql
UPDATE projects
SET name=?, description=?, ..., version = version + 1, updated_at = NOW()
WHERE id = ? AND namespace_id = ? AND version = ?  -- ? = expected_version
```

- `affected_rows = 1`：成功，返回更新后的实体（含新 version）
- `affected_rows = 0`：版本冲突，返回 **HTTP 409** `{"error": "version conflict"}`

适用实体：`projects`（version）、`tasks`（version）、`assets`（current_version）。

当 `expected_version` 未提供时，无条件更新（向后兼容本地模式）。

### 15.6 Changes 表写入规则

**共享数据**的写操作（Create/Update/Delete）需要在**同一事务**中写入 changes 表：

```sql
INSERT INTO changes (namespace_id, entity_type, entity_id, action, summary, user_name, entity_version, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, NOW())
```

| 字段 | 说明 |
|------|------|
| entity_type | `project` / `task` / `asset` / `issue` / `milestone` / `label` / `mindmap` / `review_comment` / `issue_comment` / `reaction` |
| action | `created` / `updated` / `deleted` |
| user_name | 从 token 解析 |
| entity_version | 更新后的 version 值（仅 project/task/asset） |

**私有数据**（session、chat_message）**不写** changes。

### 15.7 Changes 游标失效处理

当客户端传入的 `since` ID 对应的记录已被清理时：

```
GET /api/v1/changes?since=12345&limit=100
```

- 检查 `MIN(id)` from changes 表（或检查 `since` 对应的记录是否存在）
- 如果 `since < MIN(id)`，返回 **HTTP 410 Gone**（或正常 200 + 响应头 `X-Changes-Reset: true`）
- Go 客户端收到后会重置游标为 0，从头拉取

推荐用 HTTP 410 返回，Go 的 `pollChanges` 已实现对 410 的处理。

### 15.8 MySQL 表结构概要

> 以下与 Go 的 SQLite schema（`store/db.go`）一一对应，加上 namespace 隔离。

```sql
-- 核心索引策略：所有按 namespace_id 过滤的表加 namespace_id 前缀索引

CREATE TABLE projects (
  id              VARCHAR(36) PRIMARY KEY,
  namespace_id    VARCHAR(36) NOT NULL,
  name            VARCHAR(255) NOT NULL,
  description     TEXT,
  git_repo_path   VARCHAR(500) DEFAULT '',
  gitlab_repo_url VARCHAR(500) DEFAULT '',
  gitlab_token    VARCHAR(500) DEFAULT '',
  extra           JSON,
  created_by      VARCHAR(100) NOT NULL DEFAULT '',
  version         INT NOT NULL DEFAULT 1,
  created_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  UNIQUE KEY uk_ns_gitlab (namespace_id, gitlab_repo_url)
);

CREATE TABLE tasks (
  id           VARCHAR(36) PRIMARY KEY,
  namespace_id VARCHAR(36) NOT NULL,
  project_id   VARCHAR(36) DEFAULT '',
  parent_id    VARCHAR(36) DEFAULT '',
  title        VARCHAR(500) NOT NULL,
  description  TEXT,
  status       VARCHAR(20) DEFAULT 'todo',
  priority     VARCHAR(20) DEFAULT 'medium',
  ref_count    INT DEFAULT 0,
  created_by   VARCHAR(100) NOT NULL DEFAULT '',
  version      INT NOT NULL DEFAULT 1,
  created_at   TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at   TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  INDEX idx_ns (namespace_id)
);

CREATE TABLE task_assets (
  id         VARCHAR(36) PRIMARY KEY,
  task_id    VARCHAR(36) NOT NULL,
  asset_id   VARCHAR(36) NOT NULL,
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  UNIQUE KEY uk_task_asset (task_id, asset_id)
);

CREATE TABLE sessions (
  id               VARCHAR(36) PRIMARY KEY,
  namespace_id     VARCHAR(36) NOT NULL,
  name             VARCHAR(255) DEFAULT '',
  project_id       VARCHAR(36) DEFAULT '',
  agent            VARCHAR(50) DEFAULT '',
  agent_session_id VARCHAR(100) DEFAULT '',
  stream_id        VARCHAR(100) DEFAULT '',
  sort_order       INT DEFAULT 0,
  created_by       VARCHAR(100) NOT NULL DEFAULT '',
  created_at       TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at       TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  INDEX idx_ns_user (namespace_id, created_by)
);

CREATE TABLE session_refs (
  id         VARCHAR(36) PRIMARY KEY,
  session_id VARCHAR(36) NOT NULL,
  ref_type   VARCHAR(50) NOT NULL,
  ref_id     VARCHAR(36) NOT NULL,
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  INDEX idx_session (session_id)
);

CREATE TABLE assets (
  id              VARCHAR(36) PRIMARY KEY,
  project_id      VARCHAR(36) NOT NULL,
  type            VARCHAR(50) NOT NULL,
  title           VARCHAR(500) NOT NULL,
  current_version INT DEFAULT 1,
  status          VARCHAR(20) DEFAULT 'draft',
  created_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  INDEX idx_project (project_id),
  INDEX idx_project_type (project_id, type)
);

CREATE TABLE asset_versions (
  id         VARCHAR(36) PRIMARY KEY,
  asset_id   VARCHAR(36) NOT NULL,
  version    INT NOT NULL,
  content    LONGTEXT,
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  INDEX idx_asset_version (asset_id, version)
);

CREATE TABLE chat_messages (
  id         VARCHAR(36) PRIMARY KEY,
  session_id VARCHAR(36) NOT NULL,
  role       VARCHAR(20) NOT NULL,
  parts      JSON,
  metadata   JSON,
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  INDEX idx_session (session_id)
);

CREATE TABLE review_comments (
  id            VARCHAR(36) PRIMARY KEY,
  asset_id      VARCHAR(36) NOT NULL,
  asset_version INT NOT NULL,
  line_number   INT NULL,
  content       TEXT NOT NULL,
  author        VARCHAR(100) NOT NULL,
  status        VARCHAR(20) DEFAULT 'open',
  created_at    TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  INDEX idx_asset (asset_id)
);

CREATE TABLE mindmaps (
  id         VARCHAR(36) PRIMARY KEY,
  project_id VARCHAR(36) NOT NULL,
  title      VARCHAR(500) NOT NULL,
  data       LONGTEXT,
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  INDEX idx_project (project_id)
);

CREATE TABLE issues (
  id           VARCHAR(36) PRIMARY KEY,
  project_id   VARCHAR(36) NOT NULL,
  number       INT NOT NULL,
  title        VARCHAR(500) NOT NULL,
  body         LONGTEXT,
  author       VARCHAR(100) NOT NULL,
  state        VARCHAR(20) DEFAULT 'open',
  milestone_id VARCHAR(36) DEFAULT '',
  created_at   TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at   TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  closed_at    TIMESTAMP NULL,
  UNIQUE KEY uk_project_number (project_id, number),
  INDEX idx_project_state (project_id, state)
);

CREATE TABLE issue_comments (
  id         VARCHAR(36) PRIMARY KEY,
  issue_id   VARCHAR(36) NOT NULL,
  author     VARCHAR(100) NOT NULL,
  body       LONGTEXT,
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  INDEX idx_issue (issue_id)
);

CREATE TABLE labels (
  id          VARCHAR(36) PRIMARY KEY,
  project_id  VARCHAR(36) NOT NULL,
  name        VARCHAR(100) NOT NULL,
  color       VARCHAR(20) NOT NULL DEFAULT '#808080',
  description TEXT,
  created_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  UNIQUE KEY uk_project_name (project_id, name)
);

CREATE TABLE issue_labels (
  issue_id VARCHAR(36) NOT NULL,
  label_id VARCHAR(36) NOT NULL,
  PRIMARY KEY (issue_id, label_id)
);

CREATE TABLE issue_assignees (
  issue_id VARCHAR(36) NOT NULL,
  assignee VARCHAR(100) NOT NULL,
  PRIMARY KEY (issue_id, assignee)
);

CREATE TABLE milestones (
  id          VARCHAR(36) PRIMARY KEY,
  project_id  VARCHAR(36) NOT NULL,
  title       VARCHAR(500) NOT NULL,
  description TEXT,
  state       VARCHAR(20) DEFAULT 'open',
  due_date    DATE NULL,
  created_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  closed_at   TIMESTAMP NULL,
  INDEX idx_project (project_id)
);

CREATE TABLE reactions (
  id          VARCHAR(36) PRIMARY KEY,
  target_type VARCHAR(50) NOT NULL,
  target_id   VARCHAR(36) NOT NULL,
  author      VARCHAR(100) NOT NULL,
  emoji       VARCHAR(20) NOT NULL,
  created_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  UNIQUE KEY uk_target_author_emoji (target_type, target_id, author, emoji),
  INDEX idx_target (target_type, target_id)
);

CREATE TABLE changes (
  id             BIGINT AUTO_INCREMENT PRIMARY KEY,
  namespace_id   VARCHAR(36) NOT NULL,
  entity_type    VARCHAR(50) NOT NULL,
  entity_id      VARCHAR(36) NOT NULL,
  action         VARCHAR(20) NOT NULL,
  summary        VARCHAR(500) DEFAULT '',
  user_name      VARCHAR(100) NOT NULL,
  entity_version INT NULL,
  created_at     TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  INDEX idx_ns_id (namespace_id, id)
);
```

### 15.9 数据清理定时任务

参见第 14 章。Java 服务需实现以下清理逻辑（建议 `@Scheduled` 或 cron）：

| 目标 | SQL | 频率 |
|------|-----|------|
| changes | `DELETE FROM changes WHERE created_at < DATE_SUB(NOW(), INTERVAL 3 DAY)` | 每天 |
| sessions | 每个 `created_by` 保留最近 20 个，删除其余 | 每天 |
| chat_messages | 每个 `session_id` 保留最近 100 条，删除其余 | 每天 |
| 过期 tokens | `DELETE FROM tokens WHERE is_active = false AND expires_at < NOW()` | 每周 |

### 15.10 实现检查清单

按以下顺序实现，每完成一个阶段可用 Go 的 `--store remote` 进行端到端测试：

- [ ] **阶段 1：基础框架**
  - Spring Boot 项目骨架 + MySQL 连接
  - Token 认证 Filter（解析 `Authorization: Bearer` → 注入 namespace_id + user_name）
  - `GET /api/v1/info` 返回 namespace + user_name
  - 统一错误处理（`{"error": "message"}`）

- [ ] **阶段 2：核心 CRUD**
  - Projects CRUD（含 `?ids=` 过滤、`UNIQUE(namespace_id, gitlab_repo_url)` 约束）
  - Tasks CRUD（含 ref-count、task-asset 关联）
  - Sessions CRUD（`created_by` 隔离、reorder、refs）
  - Assets CRUD（含 asset_versions 管理、按 type/title 查询、content 接口）
  - Chat Messages CRUD

- [ ] **阶段 3：扩展实体**
  - Review Comments（含 resolve-all）
  - Mindmaps
  - Issues（含按 number 查询、comment_count 计算）
  - Issue Comments
  - Labels + Issue-Label 关联
  - Issue Assignees
  - Milestones（含 open_issues / closed_issues 统计）
  - Reactions（含 summary 接口）

- [ ] **阶段 4：变更追踪**
  - Changes 表写入（共享实体的 Create/Update/Delete 同事务写入）
  - `GET /api/v1/changes?since=&limit=` 端点
  - 游标失效检测（since < MIN(id) → 410 Gone）

- [ ] **阶段 5：乐观锁**
  - Projects/Tasks 的 Update：`WHERE version = expected_version`，不匹配 → 409
  - Assets 的 Update：`WHERE current_version = expected_version`，不匹配 → 409

- [ ] **阶段 6：数据清理**
  - 定时任务清理 changes（3天）、sessions（20个/用户）、messages（100条/session）

---

## 16 终极架构远景：全云端单体 Go 服务

> 本章描述一个长远目标——当 AI Agent（Claude Code、Codex 等）全部迁移到云端运行时，BoxNex 可以演进为一个**单一 Go 进程**，同时承担 AI 执行、数据存储、Web 服务的全部职责，不再需要独立的 Java 存储服务。

### 16.1 架构对比

```
当前阶段（本地 Go + 远程 Java）               终极形态（单体云端 Go）
┌──────────┐                                  ┌──────────┐  ┌──────────┐
│ 浏览器    │ ← localhost →  ┌────────┐       │ 浏览器 A  │  │ 浏览器 B  │ ...
└──────────┘                │ Go     │       └─────┬────┘  └─────┬────┘
                            │ Server │             │              │
  本地 Claude Code ←─────── │ SQLite │             └──── WAN ─────┘
  本地 Git 操作              └────────┘                   │
       │                         │                 ┌─────┴──────┐
       │  --store remote         │                 │  Go Server  │
       │                         ▼                 │  (云端)      │
       │                   ┌────────────┐          │ ┌─────────┐ │
       │                   │ Java 服务   │          │ │ MySQL   │ │
       │                   │ MySQL      │          │ │ Agent   │ │
       │                   └────────────┘          │ │ Git     │ │
       │                                           │ │ Web     │ │
       │                                           │ └─────────┘ │
       │                                           └─────────────┘
```

终极形态下，一个 Go 进程包揽一切：
- **数据存储**：直连 MySQL（MySQLStore）
- **AI Agent**：在服务器上调用 Claude Code / Codex API
- **Git 操作**：在服务器的 workspace 目录中操作
- **Web 服务**：前端 SPA + WebSocket 聊天 + REST API
- **多人协作**：多个浏览器同时连接，共享项目数据

### 16.2 从当前架构演进所需的改动

| 模块 | 改动量 | 说明 |
|------|--------|------|
| **MySQLStore** | 小（1-2天） | 实现 Store 接口的 MySQL 版本，与 SQLiteStore 逻辑基本一致，仅 SQL 方言差异 |
| **认证中间件** | 小（1天） | Chi middleware 解析 Bearer token → 注入 namespace_id + user_name 到 context |
| **AI Agent 调用** | 几乎无 | `agent.Agent` 接口已是抽象的，调 Claude API 不依赖运行位置 |
| **WebSocket 聊天** | 无 | ChatHub 天然支持多客户端，session_id 路由天然隔离 |
| **Workspace 管理** | 中（1-2天） | 项目创建时 `git clone` 到服务器目录；`GitRepoPath` 由服务器分配而非用户填写 |
| **Terminal 安全** | 中（1-2天） | 服务器端 PTY 需容器化隔离或限制在 workspace 目录内 |
| **前端** | 极小 | 已是纯 HTTP/WS 通信，无本地依赖 |

**总工作量约 1 周**，核心原因是当前架构已做对了几件关键的事：

1. **统一 Store 接口** — 换存储引擎只需新增实现，不改 handler
2. **Agent 抽象** — `agent.Agent` 接口与部署位置无关
3. **HTTP + WebSocket** — 前后端完全解耦，多浏览器连接天然工作
4. **session_id 路由** — 聊天流已按 session 隔离

### 16.3 关键设计变化

**config.json 的角色变化**：
- 当前：每台机器本地存储 server_url、token、项目路径映射
- 终极：不再需要本地 config.json，所有配置在服务器端（用户通过浏览器登录即可）

**项目与 Git 的关系**：
- 当前：用户手动指定本地 Git 目录
- 终极：项目创建时服务器自动 `git clone`，workspace 统一在 `/data/workspaces/{project_id}/`

**RemoteStore 的命运**：
- 终极形态下 RemoteStore 不再需要（Go 直连 MySQL，不经过 HTTP）
- 但 RemoteStore 可保留作为"分布式部署"的选项（多个 Go 实例共享一个数据服务）

### 16.4 实现路径

```
当前状态                    过渡阶段                     终极形态
──────────                 ──────────                   ──────────
本地 Go + SQLite     →     Go + RemoteStore        →    云端 Go + MySQL
                           + Java 存储服务                单进程全包揽
                           (当前实现目标)                 (远期目标)
```

过渡阶段（Java 存储服务）的价值：
- 快速实现多人数据共享，不需要等 Go 全部云端化
- Java 服务可复用已有团队的运维能力和基础设施
- RemoteStore 的 HTTP 契约为终极阶段的 MySQLStore 提供了完整的功能验证
