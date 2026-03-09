# BoxNex 待办任务列表（第四轮）

> 整理时间：2026-03-09。基于 todo3.md 检查后的剩余未实现项，并新增本轮任务。

---

## 已完成项汇总（含本轮新增已完成）

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
| T10 | 国际化（中英文切换） |
| T11 | 余额显示 |
| T12 | 模型选择（Sonnet/Haiku） |
| T13.1 | 移除 / 和 @ 特殊交互 |
| T14 | Claude CLI 交互式响应（AskUserQuestion 全链路） |
| T2 | MCP OAuth Connector 安装流程 |
| T5 | MCP 工具可见性精细控制 |
| T16 | 工作区磁盘配额改为 256MB |
| T17 | 首次注册自动创建 Project + Session |

---

## 🔴 高优先级

### T3.2 — 容器网络白名单

**现状**：容器无网络限制，可访问任意地址（含宿主机其他端口）。

**目标**：容器只允许访问 Claude API 及特定域名，其他地址一律禁止。

**实现思路**（二选一）：

1. **Docker 自定义网络 + iptables**
   - 创建 `--network=boxnex-isolated` 网络
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

**目标**：token 剩余 quota 为 0 的用户，30 天后自动清理掉其所有 project。

**实现思路**：
- 定期 goroutine（每天检查一次）
- 查询 balance=0 的用户列表
- 检查这些用户的 project 最后活跃时间，超 30 天未使用则自动删除
- 删除前可选：发邮件/站内通知提醒用户充值
- 复用已有的 project 删除逻辑（含 os.RemoveAll + container stop）

**影响文件**：`server/main.go`、`server/api/projects.go`、`server/store/`

---

## 🟡 中优先级

### T13.2 — / 命令透传 Claude CLI

**现状**：T13.1 已完成（移除自定义 /@ 交互）。`@` 文件补全由 T18.3 覆盖。

**目标**：
- `/` 命令：研究 Claude CLI 的 `/compact`、`/clear`、`/model` 等命令协议，实现透传和交互式响应渲染
- 输入 `/` 后弹出可用命令提示列表（如 `/compact`、`/clear` 等），选中后直接发送给 Claude CLI

**影响文件**：`web/src/components/chat/ChatInput.tsx`、`server/agent/claude_agent.go`

---

### T18 — 拖拽上传 + 聊天框 @ 文件选择器

本任务分为三个子项，可独立实施。

---

#### T18.1 — 拖拽上传到 Files 面板

**现状**：Files 面板（`web/src/components/files/FileBrowser.tsx`）已有点击上传按钮（lines 436-463），但不支持拖拽。

**目标**：将文件拖入 FileBrowser 区域即可上传，视觉上有 drop zone 高亮反馈。

**实现思路**：

```tsx
// FileBrowser.tsx - 在容器 div 上绑定拖拽事件
const [isDragging, setIsDragging] = useState(false);

const handleDragOver = (e: React.DragEvent) => {
  e.preventDefault();
  setIsDragging(true);
};
const handleDragLeave = () => setIsDragging(false);
const handleDrop = async (e: React.DragEvent) => {
  e.preventDefault();
  setIsDragging(false);
  const files = Array.from(e.dataTransfer.files);
  for (const file of files) {
    await filesApi.upload(projectId, file, currentPath, sessionId);
  }
  refreshTree();
};
```

- 容器 div 加 `className={cn(..., isDragging && "ring-2 ring-primary/50 bg-primary/5")}`
- 重用已有的 `filesApi.upload()`（`web/src/lib/api.ts:401-417`）和 quota 检查逻辑

**影响文件**：`web/src/components/files/FileBrowser.tsx`

---

#### T18.2 — 拖拽到聊天输入框自动上传并 @ 引用

**现状**：ChatInput（`web/src/components/chat/ChatInput.tsx`）无拖拽支持。已有 `pickedAssets` badge 展示机制（lines 764-782）和 CustomEvent `reference-asset` 监听机制（`useChatSession.ts:608-616`）。

**目标**：将文件拖入聊天输入框区域时：
1. 自动上传文件到当前 project
2. 上传成功后自动以 `@filename` 形式附加到输入文本

**实现思路**：

```tsx
// ChatInput.tsx - 在输入框容器上绑定拖拽
const handleChatDrop = async (e: React.DragEvent) => {
  e.preventDefault();
  const files = Array.from(e.dataTransfer.files);
  for (const file of files) {
    const result = await filesApi.upload(projectId, file, "/", sessionId);
    // 在输入框末尾追加 @filename
    setInputText(prev => (prev ? prev + " " : "") + `@${file.name}`);
  }
};
```

- 需要 ChatInput 能访问 `projectId`（已有 prop 或通过 store）
- 上传进行中显示 loading 状态（避免重复提交）
- 支持多文件批量拖拽

**影响文件**：`web/src/components/chat/ChatInput.tsx`、`web/src/hooks/useChatSession.ts`（透传 projectId）

---

#### T18.3 — 聊天框 @ 文件选择器

**现状**：
- `@asset` / `@task` 通过侧边栏按钮 + CustomEvent 机制实现（非输入框内联触发）
- `MentionTextarea.tsx`（`web/src/plugins/task/components/MentionTextarea.tsx`）实现了成员 @ 下拉框，可作为**架构参考**
- ChatInput 无任何文件 @ 联想功能

**目标**：用户在 ChatInput 中输入 `@` 时，弹出文件选择器（按文件名过滤，默认展示前 10 个），选中后：
1. 在输入框中插入 `@filename`
2. 文件名作为上下文提示传给 Claude（可直接拼入消息文本，也可作为 file_ref part）

**实现思路**（参考 MentionTextarea.tsx 的核心模式）：

**A. 检测 @ 触发**（类似 MentionTextarea.tsx:94-113）：
```tsx
// ChatInput.tsx handleInputChange 内
const atIdx = value.lastIndexOf("@", cursorPos);
if (atIdx !== -1 && !/\s/.test(value.slice(atIdx + 1, cursorPos))) {
  const query = value.slice(atIdx + 1, cursorPos);
  setFilePickerQuery(query);
  setFilePickerAnchor(atIdx);
  setFilePickerOpen(true);
} else {
  setFilePickerOpen(false);
}
```

**B. 文件列表搜索**：
- 调用已有的 `filesApi.list(projectId, "/")`（或新增 search endpoint）
- 前端按文件名过滤，取前 10 个
- 用户继续输入则实时筛选

**C. 下拉 UI**（参考 MentionTextarea.tsx:137-164 的 createPortal 方案）：
```tsx
// 通过 createPortal 渲染到 body，避免 overflow 截断
{filePickerOpen && createPortal(
  <div style={dropdownPosition} className="...">
    {filteredFiles.map(file => (
      <div key={file.path} onClick={() => insertFileRef(file)}>
        <FileIcon /> {file.name}
      </div>
    ))}
  </div>,
  document.body
)}
```

**D. 插入文件引用**（类似 MentionTextarea.tsx:76-92）：
```tsx
const insertFileRef = (file: FileEntry) => {
  const before = inputText.slice(0, filePickerAnchor);
  const after = inputText.slice(cursorPos);
  setInputText(before + `@${file.name} ` + after);
  setFilePickerOpen(false);
  // 聚焦并移动光标到插入点末尾
};
```

**E. 键盘导航**：ArrowUp/Down 切换、Enter/Tab 选中、Escape 关闭（同 MentionTextarea.tsx:115-135）

**文件较多时**：默认展示最近修改的 10 个文件，输入关键词后实时过滤（前端 filter，无需额外 API）。

**影响文件**：
- `web/src/components/chat/ChatInput.tsx`（主要改动）
- `web/src/lib/api.ts`（filesApi.list 确认可用）
- 可能新建 `web/src/components/chat/FileAtMention.tsx`（独立组件）

---

### T19 — 计费：停止响应时的 Token 扣费确认与接入

**背景调研结论**：

经过代码分析，当前系统的 token 计费状态如下：

**已实现（数据记录层）**：
- `server/store/sqlite_cloud.go`（lines 176-217）已实现 `ReserveTokens()` 和 `SettleTokens()`
- 停止响应时，Claude agent 仍会返回实际消耗的 token 数（`claude_agent.go:419-441`）
- `EventDone` handler 无论是否停止，**均会**记录 input/output token 到 MessageMetadata（`chat.go:473-489`）
- 最终 token 数据**均会**持久化到消息表（`chat.go:634-649`）

**未实现（实际扣费层）**：
- `ReserveTokens()` 和 `SettleTokens()` **从未被调用**，余额不会被实际扣减
- 当前系统只记录数据，不执行扣费

**结论**：停止响应时 token **不会漏记**（数据层完整），但**也不会被扣费**（扣费层未接入）。

**目标**：将数据层与扣费层打通：

```
chat 开始 → ReserveTokens(estimated) → 执行 Agent → EventDone → SettleTokens(actual)
```

**关键设计点**：
1. **预留额度**：发送前预估 token 数（可按 context 长度粗估），调用 `ReserveTokens()`；余额不足直接返回错误，禁止发送
2. **结算**：`EventDone` 回调中（无论正常完成还是被停止），用实际 token 数调用 `SettleTokens()`
3. **停止场景**：已验证 Claude 在被 cancel 后仍返回已消耗的实际 token 数，因此 `SettleTokens()` 收到的是真实数字，不会漏算
4. **异常场景**：若 agent 崩溃未触发 EventDone，预留的 token 需要有定期回收机制（超时自动释放）

**影响文件**：
- `server/api/chat.go`（发送前调用 ReserveTokens，EventDone 中调用 SettleTokens）
- `server/store/store.go`（确认接口签名）
- `server/store/sqlite_cloud.go`（已实现，确认逻辑正确）

---

## 🟢 低优先级

### T8.2 — gVisor 容器运行时

**现状**：容器使用默认 runc 运行时。

**目标**：长期可用 gVisor (runsc) 作为容器运行时，进一步隔离系统调用。

**实现思路**：docker run 加 `--runtime=runsc`，需宿主机安装 gVisor。

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
| 🟡 中 | T13.2 | / 命令透传 Claude CLI | 中 |
| 🟡 中 | T18.1 | Files 面板拖拽上传 | 低 |
| 🟡 中 | T18.2 | 聊天框拖拽上传并 @ 引用 | 低 |
| 🟡 中 | T18.3 | 聊天框 @ 文件选择器（联想补全） | 中 |
| 🟡 中 | T19 | 计费：停止响应时 Token 扣费接入 | 中 |
| 🟢 低 | T8.2 | gVisor 容器运行时 | 低（配置） |
