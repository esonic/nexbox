# NexBox V2 概念重设计

## 一、设计目标

将 NexBox 从 "Project → Iteration → Asset" 的嵌套结构改为更扁平、更灵活的概念模型：
- **去掉 Iteration**，Asset 直接属于 Project
- **新增 Task**（顶层概念，类似 Jira 工单），可关联 Asset（支持跨项目）
- **Session 独立**为顶层概念，通过引用机制关联 Project / Task / Asset
- 旧数据库直接删除，不做数据迁移
- Brainstorm 前端入口隐藏，前后端代码保留

---

## 二、概念模型

### 2.1 顶层概念（三个平级入口）

| 概念 | 说明 |
|------|------|
| **Session** | AI 对话的载体。默认不绑定 Project（不能写代码），用户需选定 Project 后才能执行代码/Git 操作。可通过引用机制关联 Project / Task / Asset 到上下文 |
| **Task** | 任务工单，类似 Jira。有标题、描述、状态。可通过 AI 对话创建，也可手工创建。可关联 Asset（支持跨项目，N:M） |
| **Project** | 项目，关联 Git 仓库，直接拥有 Asset |

### 2.2 子概念

| 概念 | 所属 | 说明 |
|------|------|------|
| **Asset** | Project | 文档产物（需求文档、设计文档、代码变更、测试用例等），有版本历史 |
| **AssetVersion** | Asset | Asset 的历史版本 |
| **TaskAsset** | 关联表 | Task ↔ Asset 多对多关联 |
| **SessionRef** | Session | Session 引用的上下文实体（类型 + ID） |
| **ChatMessage** | Session | 聊天消息 |
| **ReviewComment** | Asset | Asset 上的评审评论（保留） |

### 2.3 去掉的概念

- **Iteration**：彻底去掉，Asset 直接 `project_id` 关联 Project
- **Conversation**：重命名为 Session，提升为顶层概念
- **Mindmap/Brainstorm**：前端入口隐藏，代码保留（表保留，API 保留但不在前端暴露）

---

## 三、实体关系

```
Session ──1:N──> ChatMessage
  │
  └──1:N──> SessionRef ──> (project / task / asset)
                                  │           │
Project ──1:N──> Asset <──N:M──> Task
  │                │
  │                └──1:N──> AssetVersion
  │                └──1:N──> ReviewComment
  │
  └── git_repo_path (Git 操作基础)
```

---

## 四、数据库表设计

### 4.1 保留的表（字段调整）

```sql
-- 项目表：保持不变
CREATE TABLE projects (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    description TEXT DEFAULT '',
    git_repo_path TEXT DEFAULT '',
    gitlab_token TEXT DEFAULT '',
    extra       TEXT DEFAULT '{}',  -- JSON
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- 产物表：iteration_id → project_id
CREATE TABLE assets (
    id              TEXT PRIMARY KEY,
    project_id      TEXT NOT NULL REFERENCES projects(id),
    type            TEXT NOT NULL,  -- requirement_doc, design_doc, code_change, unit_test, test_case
    title           TEXT NOT NULL,
    current_version INTEGER DEFAULT 1,
    status          TEXT DEFAULT 'draft',  -- draft, reviewing, approved
    created_at      DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at      DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- 产物版本表：不变
CREATE TABLE asset_versions (
    id         TEXT PRIMARY KEY,
    asset_id   TEXT NOT NULL REFERENCES assets(id),
    version    INTEGER NOT NULL,
    content    TEXT DEFAULT '',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- 评审评论表：不变
CREATE TABLE review_comments (
    id            TEXT PRIMARY KEY,
    asset_id      TEXT NOT NULL REFERENCES assets(id),
    asset_version INTEGER DEFAULT 1,
    line_number   INTEGER,
    content       TEXT NOT NULL,
    author        TEXT DEFAULT 'user',
    status        TEXT DEFAULT 'open',  -- open, resolved
    created_at    DATETIME DEFAULT CURRENT_TIMESTAMP
);
```

### 4.2 新增的表

```sql
-- 任务表（顶层概念）
CREATE TABLE tasks (
    id          TEXT PRIMARY KEY,
    title       TEXT NOT NULL,
    description TEXT DEFAULT '',
    status      TEXT DEFAULT 'open',  -- open, in_progress, done, closed
    priority    TEXT DEFAULT 'medium',  -- low, medium, high, critical
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- 任务-产物关联表（多对多）
CREATE TABLE task_assets (
    id         TEXT PRIMARY KEY,
    task_id    TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    asset_id   TEXT NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(task_id, asset_id)
);

-- 会话表（顶层概念，替代 conversations）
CREATE TABLE sessions (
    id          TEXT PRIMARY KEY,
    name        TEXT DEFAULT '',
    project_id  TEXT DEFAULT '' REFERENCES projects(id),  -- 绑定的项目（空=未绑定）
    agent_session_id TEXT DEFAULT '',  -- agent 层面的 session ID（用于恢复）
    stream_id   TEXT DEFAULT '',
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- 会话上下文引用表
CREATE TABLE session_refs (
    id          TEXT PRIMARY KEY,
    session_id  TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    ref_type    TEXT NOT NULL,  -- project, task, asset
    ref_id      TEXT NOT NULL,
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(session_id, ref_type, ref_id)
);

-- 聊天消息表：conversation_id → session_id
CREATE TABLE chat_messages (
    id         TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    role       TEXT NOT NULL,  -- user, assistant, system
    parts      TEXT DEFAULT '[]',  -- JSON array of MessagePart
    metadata   TEXT DEFAULT '{}',  -- JSON
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
```

### 4.3 保留但不迁移的表

```sql
-- mindmaps, mindmap_nodes 表保留，代码保留，前端入口隐藏
```

### 4.4 删除的表

```sql
-- iterations: 彻底删除
-- conversations: 被 sessions 替代
```

---

## 五、Session 行为设计

### 5.1 Session 与 Project 的关系

- Session **默认不绑定 Project**（`project_id` 为空）
- 未绑定 Project 时：
  - 可以自由对话
  - 可以创建 Task
  - 可以浏览 Task / Project / Asset（只读引用）
  - **不能**执行代码操作（写文件、运行命令等）
  - **不能**执行 Git 操作
  - 如果用户试图写代码，提示："请先选择一个 Project 才能执行代码操作"
- 绑定 Project 后：
  - 解锁代码操作和 Git 操作
  - Header 出现 "Git" 按钮（如果 Project 有 git_repo_path）

### 5.2 上下文引用机制

Session 可引用三种类型的上下文：

| 引用类型 | 引入时自动携带 | 说明 |
|---------|--------------|------|
| **Project** | 项目名 + 描述 + Git 路径摘要 | 绑定项目上下文 |
| **Task** | Task 标题 + 描述 + 关联 Asset 列表（仅标题摘要） | 引入任务上下文 |
| **Asset** | Asset 标题 + 类型 + 最新版本内容摘要 + 所属 Project 名 | 引入具体产物 |

**引入方式：**
1. **关联按钮**：Project / Task / Asset 卡片上都有 "添加到 Session" 按钮
2. **`@` 符号**：在聊天输入框中输入 `@` 触发搜索弹窗，可搜索并选择 Project / Task / Asset
3. **Context Bar**：Session 顶部的上下文栏，有 `+` 按钮可添加引用

**摘要策略（避免上下文过长）：**
- 引入 Task 时，关联的 Asset 仅显示标题列表，不包含内容
- 引入 Asset 时，内容超过 50 字截断，让 AI 调用读取资产的 mcp 工具
- 引入 Project 时，只带基本信息，不自动展开所有 Asset。
- 注意：project 本质上只是引入git操作范围和工作目录，它的关联级别不太等同于 asset 和 task，需要注意交互设计，最好与asset和task是不一定的，相当于只是指定workspace的目录

---

## 六、Task 行为设计

### 6.1 创建方式

1. **手工创建**：Tasks 页面点 "新建 Task" 按钮，填写标题、描述
2. **AI 创建**：在 Session 中对话，AI 识别到需要创建任务时调用工具创建，或者有内置的 /create_task 命令包含内置提示词来引导创建

### 6.2 状态流转

```
open → in_progress → done → closed
                   ↘ closed（可直接关闭）
```

### 6.3 关联 Asset（初期手工关联）

在 Task 详情页内选择 Asset，交互流程：

1. 点击 "关联 Asset" 按钮
2. **先选 Project**（Project 列表，简洁卡片）
3. **再选 Asset**（该 Project 下的 Asset 列表）
   - 按时间倒序排列
   - 默认显示 Top 20
   - 底部有 "加载更多" 按钮翻页
   - 按 Asset 类型分组显示（需求文档、设计文档、代码变更等）
4. 确认关联

**跨项目关联**：同样的流程，只是选了不同的 Project。一个 Task 可以关联多个不同 Project 下的 Asset。

---

## 七、布局设计

### 7.1 设计原则

- **尽量复用现有 UI 模式**，减少重写，降低 bug 风险
- Session Tab 栏复用现有的 Conversation 顶部 Tab 样式
- 侧边栏在现有基础上改造（Projects 保留，去掉 Iteration/Brainstorm，加入 Tasks）
- 右侧面板只保留 Git（原 Review），Assets 浏览挪到侧边栏文件树

### 7.2 整体布局

```
┌──────────────┬─────────────────────────────────────┬───────────────┐
│   Sidebar    │           Main Area                  │  Right Panel  │
│   (240px)    │                                     │  (optional)   │
│              │ ┌─────────────────────────────────┐ │               │
│ ┌──────────┐ │ │ Session Tabs    [S1][S2][S3][+] │ │   Git Panel   │
│ │ Projects │ │ ├─────────────────────────────────┤ │   (原Review)  │
│ │  ▸ ProjA │ │ │ Context Bar                     │ │               │
│ │    📋 需求│ │ │ [🔒ProjA] [@Task1] [@doc.md] +│ │  Changes      │
│ │    📐 设计│ │ ├─────────────────────────────────┤ │  History      │
│ │    💻 代码│ │ │                                 │ │               │
│ │    ...   │ │ │     Chat Messages               │ │               │
│ │  ▸ ProjB │ │ │                                 │ │               │
│ ├──────────┤ │ │                                 │ │               │
│ │ Tasks    │ │ │                                 │ │               │
│ │  · T1 ●  │ │ │                                 │ │               │
│ │  · T2 ○  │ │ ├─────────────────────────────────┤ │               │
│ │  · T3 ●  │ │ │ [@] Input Box            [Send] │ │               │
│ └──────────┘ │ └─────────────────────────────────┘ │               │
└──────────────┴─────────────────────────────────────┴───────────────┘
```

### 7.3 Sidebar 设计

Sidebar 分两个区域：**Projects** 和 **Tasks**。

#### 区域 1：Projects（文件树样式）

展示所有项目及其 Asset，类似 IDE 文件树：

```
▾ Project Alpha                    [➕]
  ▾ 📋 需求文档 (3)
      登录模块需求
      注册模块需求
      权限管理需求
  ▾ 📐 设计文档 (2)
      JWT 方案设计
      数据库设计
  ▸ 💻 代码变更 (12)               ← 折叠，点击展开
      auth middleware
      user model
      ... (显示前5条)
      [Load more...]               ← 加载更多
  ▸ 🧪 测试用例 (8)
▸ Project Beta                     [➕]
▸ Project Gamma                    [➕]
```

**交互规则：**
- 每个 Project 可展开/折叠，展开后显示 Asset（按类型分组）
- 每个类型分组可展开/折叠
- **分组内默认显示前 5 条 Asset**（按时间倒序），超出部分显示 `[Load more...]` 按钮
- 点击 Asset 名称：如果在 Session 页面，弹出 Asset 查看/编辑面板；否则弹出预览
- 每个 Project 行有 `[➕]` 按钮：将 Project 添加到当前 Session 上下文
- 每个 Asset 右键或 hover 显示操作菜单：编辑、添加到 Session 上下文、查看版本等
- Project 行右键或 hover：新建 Asset、Settings 等
- 顶部有 `[+ New Project]` 按钮

#### 区域 2：Tasks（简洁列表）

只显示 Task 标题和状态，不展开 Asset（Asset 关联在 Task 详情页管理）：

```
[+ New Task]
● 实现用户登录                      [➕]
● 重构数据库层                      [➕]
○ 编写 API 文档
○ 性能优化
[Load more...]
```

**交互规则：**
- 列表项：状态圆点 + Task 标题
  - 🔵 open / 🟡 in_progress / 🟢 done / ⚫ closed
- 点击 Task → 导航到 Task 详情页
- `[➕]` 按钮：将 Task 添加到当前 Session 上下文
- 默认显示最近 20 条，底部 `[Load more...]` 加载更多
- 顶部有 `[+ New Task]` 按钮

#### Sidebar 折叠态
- 只显示图标列（FolderGit2 / ListTodo 图标）
- 点击展开

### 7.4 Main Area（Session 工作区）

#### Session Tab 栏
复用现有 Conversation 的 Tab 切换样式，位于主区域顶部：

```
[Session 1] [Session 2] [Session 3] [+]
```

- 每个 Tab 显示 Session 名称（默认用第一条消息摘要或 "New Session"）
- 点击切换 Session
- `[+]` 新建 Session
- Tab 上右键可重命名、删除
- 当前活跃 Session 高亮

#### Context Bar（上下文栏）
位于 Session Tab 栏下方，显示当前 Session 的上下文引用：

```
[🔒 Project A] [@Task: 登录功能] [@需求文档.md] [+]
```

- **Project 引用**样式与 Task/Asset 引用不同：
  - 使用 🔒 锁图标或特殊底色（如偏深色/带边框），体现它是 "工作目录/环境" 的定位
  - 一个 Session 最多绑定一个 Project
  - 绑定 Project 后才能执行代码/Git 操作
- **Task / Asset 引用**：普通标签样式，可有多个
- 每个引用标签可点击 × 移除
- `[+]` 按钮 → 打开选择 Popover（可选 Project / Task / Asset）
- 未绑定 Project 时显示淡色提示文字："Select a project to enable code operations"

#### Chat Area
- 消息流展示（和现有一致）
- 支持 AI 工具调用卡片展示（ToolCallCard 等保持不变）

#### Input Area
- `@` 符号触发上下文引用搜索（弹出菜单，支持搜索 Project / Task / Asset）
- 聊天输入框
- 发送按钮
- `/` 可以触发命令，与现有一致，未来可扩展

### 7.5 Right Panel（右侧面板 — 仅 Git）

右侧面板**只保留 Git 功能**（原 Review 改名为 Git）：

- **显示条件**：当前 Session 绑定的 Project 有 `git_repo_path` 时可用
- **打开方式**：Header 上的 "Git" 按钮（替代原 "Review" 按钮）
- **内容**：与原 DiffView 完全一致
  - Changes 子 Tab：文件变更列表 + 提交 + 丢弃 + AI commit message
  - History 子 Tab：提交历史 + per-commit diff
- 可拖拽调整宽度
- 默认关闭

### 7.6 非 Session 页面

**注意**：去掉 Project 详情页（Asset 在侧边栏文件树已经能看到和操作）。

#### Task 详情页 (`/tasks/:id`)
```
┌──────────────────────────────────────────┐
│ ← Tasks                                 │
│                                          │
│ Task: 实现用户登录功能                      │
│ Status: [in_progress ▼]  Priority: [high]│
│                                          │
│ Description:                             │
│ 需要实现基于 JWT 的登录功能...              │
│                                          │
│ ─── 关联 Assets ──────────────────────── │
│ [+ 关联 Asset]                            │
│                                          │
│ Project A:                               │
│   📄 需求文档 - 登录模块         [× 解除]  │
│   📝 设计文档 - JWT 方案          [× 解除] │
│                                          │
│ Project B:                               │
│   💻 代码变更 - auth middleware   [× 解除] │
└──────────────────────────────────────────┘
```

- 关联 Asset 按所属 Project 分组显示
- 每条可解除关联
- 点击 Asset 名称可预览/编辑

#### Home 页 (`/`)
- 简洁的欢迎页 + 快捷新建 Session 按钮
- 或直接创建一个新 Session 并跳转（待定，保持简单即可）

---

## 八、前端路由

| 路由 | 页面 | 说明 |
|------|------|------|
| `/` | Home / SessionWorkspace | 默认进入工作区（自动创建或恢复最近 Session） |
| `/tasks/:id` | TaskDetail | 任务详情（关联 Asset、状态管理） |

**说明**：
- Session 切换通过主区域顶部 Tab 栏完成，不需要独立路由
- Project 没有独立详情页，Asset 在侧边栏文件树浏览和操作
- Task 有独立详情页（管理关联 Asset）
- Brainstorm 路由保留但隐藏入口：`/projects/:projectId/brainstorm/:mindmapId`

---

## 九、API 设计

### 9.1 Sessions

```
GET    /api/sessions                 # 列表（支持分页）
POST   /api/sessions                 # 新建
GET    /api/sessions/:id             # 详情
PUT    /api/sessions/:id             # 更新（名称、绑定 project 等）
DELETE /api/sessions/:id             # 删除

GET    /api/sessions/:id/messages    # 消息列表
POST   /api/sessions/:id/refs        # 添加上下文引用
GET    /api/sessions/:id/refs        # 获取上下文引用列表
DELETE /api/sessions/:id/refs/:refId # 移除上下文引用
```

### 9.2 Tasks

```
GET    /api/tasks                    # 列表（支持分页、状态过滤）
POST   /api/tasks                    # 新建
GET    /api/tasks/:id                # 详情
PUT    /api/tasks/:id                # 更新
DELETE /api/tasks/:id                # 删除

GET    /api/tasks/:id/assets         # 获取关联 Asset 列表
POST   /api/tasks/:id/assets         # 关联 Asset（body: { asset_id })
DELETE /api/tasks/:id/assets/:assetId # 解除关联
```

### 9.3 Projects

```
GET    /api/projects                 # 列表
POST   /api/projects                 # 新建
GET    /api/projects/:id             # 详情
PUT    /api/projects/:id             # 更新
DELETE /api/projects/:id             # 删除

GET    /api/projects/:id/assets      # Asset 列表（支持分页、类型过滤）
POST   /api/projects/:id/assets      # 新建 Asset
```

### 9.4 Assets

```
GET    /api/assets/:id               # 详情
PUT    /api/assets/:id               # 更新
DELETE /api/assets/:id               # 删除

GET    /api/assets/:id/versions      # 版本列表
GET    /api/assets/:id/diff          # 版本差异

GET    /api/assets/:id/comments      # 评论列表
POST   /api/assets/:id/comments      # 新增评论
PUT    /api/comments/:commentId      # 更新评论
DELETE /api/comments/:commentId      # 删除评论
```

### 9.5 Git（改为 Project 级别）

```
GET    /api/projects/:id/git-status           # Git 状态
POST   /api/projects/:id/git-commit           # 提交
POST   /api/projects/:id/git-discard          # 丢弃变更
GET    /api/projects/:id/git-log              # 提交历史
GET    /api/projects/:id/git-commit-files     # 某次提交的文件
GET    /api/projects/:id/git-commit-diff      # 某次提交的差异
POST   /api/projects/:id/generate-commit-message  # AI 生成提交消息
GET    /api/projects/:id/diff-stats           # 差异统计
GET    /api/projects/:id/file-diff            # 单文件差异
POST   /api/projects/:id/create-mr            # 创建 MR
POST   /api/projects/:id/create-change-order  # 创建变更单
```

### 9.6 WebSocket

```
GET    /ws                           # 全局 WebSocket（不变）
```

### 9.7 Asset 列表分页参数

```
GET /api/projects/:id/assets?type=requirement_doc&limit=20&offset=0
```

- `type`：可选，按类型过滤
- `limit`：默认 20
- `offset`：默认 0

---

## 十、Brainstorm 处理

### 前端
- **隐藏入口**：
  - Sidebar 中去掉 Brainstorm 分区（去掉 "New Brainstorm" 按钮和 mindmap 列表）
  - 路由 `/projects/:projectId/brainstorm/:mindmapId` 保留但不从导航入口进入
- **保留代码**：BrainstormPage、BrainstormPanel、MindmapCanvas、StyleToolbar 等组件代码不删除

### 后端
- Mindmap 相关 API 保留（路由注册保留）
- mindmaps、mindmap_nodes 表保留

---

## 十一、关键交互流程

### 11.1 新建 Session 并开始工作

1. 用户点击 Session Tab 栏的 `[+]` 新建 Session
2. 创建空 Session，Tab 栏出现新 Tab 并自动激活
3. Context Bar 显示淡色提示 "Select a project to enable code operations"
4. 用户可直接对话（例如讨论需求、创建 Task）
5. 当需要写代码时，通过 Context Bar 的 `[+]` 选择 Project，或侧边栏 Project 的 `[➕]` 按钮
6. Session 绑定 Project 后，Header 出现 "Git" 按钮（如果 Project 有 git_repo_path）

### 11.2 通过 AI 创建 Task

1. 用户在 Session 中说："帮我创建一个任务：实现用户登录功能"
2. AI 识别意图，调用 create_task 工具
3. Task 创建成功，Sidebar Tasks 区域实时更新

### 11.3 关联 Asset 到 Task

1. 进入 Task 详情页（点击侧边栏 Task 条目）
2. 点击 "关联 Asset"
3. 弹出选择面板：
   - 第一步：选择 Project（列表展示）
   - 第二步：展示该 Project 下 Asset，按类型分组，每组时间倒序，默认显示 Top 20
   - 底部 "加载更多" 按钮
4. 选择 Asset → 确认关联

### 11.4 @ 引用

1. 在聊天输入框输入 `@`
2. 弹出搜索菜单，分三类：Projects / Tasks / Assets
3. 输入关键字实时搜索
4. 选择后自动添加到 Session 的 SessionRef
5. 消息中显示 `@Project A` 这样的引用标签

### 11.5 侧边栏浏览和操作 Asset

1. 在侧边栏 Projects 区域展开某个 Project
2. 按类型分组看到 Asset（每组默认 5 条，点 Load more 加载更多）
3. 点击 Asset → 在主区域弹出查看/编辑面板（复用现有 AssetEditor）
4. 右键/hover Asset → 菜单：添加到 Session、查看版本、编辑、删除等

---

## 十二、实现计划

### Phase 1：基础结构（后端）
1. 新建数据库 schema（删除旧 DB 文件，重建）
2. 实现 Session / Task / TaskAsset / SessionRef 的 model 和 store
3. 修改 Asset 的 store（`project_id` 替代 `iteration_id`）
4. 实现新 API 路由
5. 修改 WebSocket hub 适配 Session（session_id 替代 iteration_id/conversation_id）

### Phase 2：前端核心
1. 改造 Sidebar（去掉 Iteration/Brainstorm，改为 Projects 文件树 + Tasks 列表）
2. 改造主区域（Session Tab 栏复用 Conversation Tab 样式 + Context Bar + Chat）
3. "Review" 按钮改名 "Git"，右侧面板只保留 Git
4. 实现 Task 详情页
5. 去掉 Project 详情页，去掉 IterationWorkspace 入口
6. 隐藏 Brainstorm 入口（代码保留）

### Phase 3：交互完善
1. Context Bar 引用管理（Project 特殊样式 + Task/Asset 引用）
2. `@` 符号搜索弹窗
3. Asset 关联选择器（跨项目、分页）
4. AI 创建 Task 的工具注册
5. Session 未绑定 Project 时的限制提示

---

## 十三、与现有代码的对应关系

| 现有 | V2 | 变化 |
|------|-----|------|
| `server/models/iteration.go` | 删除 | Iteration 概念移除 |
| `server/store/iterations.go` | 删除 | |
| `server/api/iterations.go` | 删除 | |
| `server/models/conversation.go` | → `session.go` | 重命名+字段调整 |
| `server/store/conversations.go` | → `sessions.go` | |
| `server/api/conversations.go` | → `sessions.go` | |
| `server/models/asset.go` | 修改 | `iteration_id` → `project_id` |
| `web/src/pages/IterationWorkspace.tsx` | → `SessionWorkspace.tsx` | 重构为 Session 工作区 |
| `web/src/components/layout/AppSidebar.tsx` | 改造 | Projects 文件树 + Tasks 列表（去掉 Iteration/Brainstorm） |
| `web/src/pages/ProjectDetail.tsx` | 删除 | Asset 在侧边栏文件树浏览，不需要独立详情页 |
| `web/src/pages/ProjectList.tsx` | 删除 | 项目在侧边栏展示 |
| ChatPanel 中 Session Tab 栏 | 改造 | 复用现有 Conversation Tab 样式，改为 Session |
| `IterationWorkspace` 中 "Review" 按钮 | → "Git" | 重命名 |
| Sidebar Brainstorm 分区 | 隐藏 | 代码保留 |
| 新增 `server/models/task.go` | 新建 | Task + TaskAsset |
| 新增 `server/models/session_ref.go` | 新建 | SessionRef |
| 新增 `web/src/pages/TaskDetail.tsx` | 新建 | Task 详情页（关联 Asset 管理） |
| 新增 Context Bar 组件 | 新建 | Session 上下文引用标签栏 |
