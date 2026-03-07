# Lark Chat 中转设计

## 功能概述

用户在 Lark 给机器人发 `nex-chat` 命令，Java 服务端将消息原样写入 `nex_changes` 表（纯透传，不做业务检查）。用户本地的 Go 程序在现有的 changes 轮询循环中额外调用一个专用接口发现该消息，完成 project 匹配、session 注入、AI 处理，最后通过 Java 提供的回复接口将 AI 响应以 Lark 消息发回给用户。

## 消息格式

```
nex-chat [project_id前缀] 消息内容
```

- 前缀为 project UUID 的前 **8 位**（十六进制），用于多 project 时指定目标。
- 用户只有一个 project 时可省略前缀，直接写消息内容。
- **解析规则**（Go 侧执行）：取消息的第一个词，如果它恰好是 8 位十六进制字符串且能匹配到某个本地 project 的 ID 前缀，则视为 project 前缀，剩余部分为消息内容；否则整段文本都是消息内容。

示例：
```
nex-chat 部署状态怎么样了              → 无前缀，自动匹配唯一 project
nex-chat a3f2b1c9 帮我review最新的PR  → 前缀 a3f2b1c9，匹配后剩余为消息
```

## Java 侧改动（纯透传）

### 1. LarkWebHook 新增 nex-chat 命令

在 `processContent` 中增加 `nex-chat` 分支。收到该命令时：

1. 从 `operator`（如 `eric.du@okg.com`）提取 `userName`（如 `eric.du`）。
2. 将消息原样写入 `nex_changes` 表，不做任何业务检查：
   - `namespace_id` = 空字符串
   - `entity_type` = `lark_chat`
   - `entity_id` = userName
   - `action` = `send`
   - `summary` = 完整消息文本（VARCHAR 500，可截断）
   - `detail` = 完整消息文本（TEXT，不截断）
   - `user_name` = userName
3. 收到后立即回复 "Message received, processing..." 确认收到。

### 2. 新增 lark_chat 查询接口

路径：`GET /rca/api/v1/nexbox/lark-chat?since=0&limit=10`

查询：
```sql
SELECT ... FROM nex_changes
WHERE entity_type = 'lark_chat' AND entity_id = ? AND id > ?
ORDER BY id ASC LIMIT ?
```

`entity_id` 从 token 的 `user_name` 取。不改动现有的 `listSince`，完全独立。

### 3. 新增索引

```sql
CREATE INDEX idx_lark_chat ON nex_changes (entity_type, entity_id, id);
```

### 4. 新增 Lark 回复接口

路径：`POST /rca/api/v1/nexbox/lark-reply`

请求体：
```json
{
  "target_user": "eric.du",
  "content": "markdown内容",
  "timestamp": 1740200000
}
```

校验：
- 经过 NexboxAuthFilter 验证 Bearer token。
- token 中的 `user_name` 必须等于 `target_user`（只能给自己发消息）。
- `timestamp` 与服务器当前时间差不超过 60 秒（防回放）。

通过后，调用 Messenger 将 content 以 Lark 卡片消息发送给 `target_user@okg.com`。

## Go 侧改动

### 1. pollChanges 中额外轮询 lark_chat

在现有轮询循环中，每次 poll changes 时顺带调用 `GET /lark-chat` 接口。共用自适应频率（3s~30s），不额外起 goroutine。

遇到 lark_chat 记录时：
- 检查 `created_at` 是否超过 5 分钟，超过则跳过（应对程序重启后的积压消息）。
- 不广播到前端浏览器，异步调用 lark_chat 处理函数。

### 2. 处理 lark_chat 消息

1. **解析消息**：优先从 `detail` 读取完整内容（`summary` 作为后备），提取可能的 project 前缀和实际消息内容。
2. **匹配 project**：
   - 本地无 project → 通过 lark-reply 回复"未配置 project"。
   - 仅一个 project 且无前缀 → 自动使用该 project。
   - 多个 project 且无前缀/前缀不足 8 位 → 通过 lark-reply 回复 project 列表（含名称和 ID 前 8 位），方便用户重试。
   - 前缀为 8 位但匹配不到 → 同上，回复 project 列表。
3. **查找/创建 session**：在匹配到的 project 下查找最近使用的 session（按 `updated_at` 排序取第一个），没有则新建一个。
4. **注入消息**：复用现有的 `handleUserMessage` 将消息注入 session，效果等同于用户在界面上发送了该消息。
5. **等待完成**：轮询 `SessionManager.IsActive` 直到 AI 处理完毕（设超时上限）。
6. **提取回复**：从数据库读取该 session 最新的 assistant 消息。文本部分原样保留；tool call 部分仅保留步骤数和工具名称摘要（如 "*(4 steps: Listed assets, Listed tasks, ...)*"），thinking 和 tool_result 部分忽略。超过 14000 字符时截断并附加提示。
7. **发送回复**：调用 Java 的 `lark-reply` 接口，携带 cloud token 和当前时间戳，将 AI 回复发给用户。

## 安全机制

- **身份绑定**：lark-reply 接口要求 token 中用户与目标用户一致，无法给他人发消息。
- **时间戳校验**：请求的 timestamp 必须在 60 秒内，防止回放攻击。
- **消息过期**：Go 侧忽略超过 5 分钟的 lark_chat 消息，避免程序重启后处理历史积压。
