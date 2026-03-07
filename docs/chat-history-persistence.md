# Chat History Persistence

## Approach

NexBox 自己在 WebSocket 消息流转过程中保存对话记录，不依赖 Claude CLI 内部存储。

## Changes

### 1. Data model: new `chat_messages` table

In `server/store/db.go`, add migration:

```sql
CREATE TABLE IF NOT EXISTS chat_messages (
    id TEXT PRIMARY KEY,
    iteration_id TEXT NOT NULL REFERENCES iterations(id) ON DELETE CASCADE,
    role TEXT NOT NULL,         -- 'user' | 'assistant' | 'tool_use' | 'error'
    content TEXT NOT NULL,
    tool_name TEXT DEFAULT '',  -- for tool_use events
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
)
```

In `server/models/models.go`, add `ChatMessage` struct.

### 2. Store layer: CRUD for messages

New file `server/store/messages.go`:

- `CreateMessage(iterationID, role, content, toolName)` - insert one message
- `ListMessages(iterationID)` - return all messages ordered by `created_at ASC`

### 3. API: GET endpoint

In `server/api/router.go`, add route:

- `GET /api/iterations/{iterationId}/messages` - returns chat history

### 4. Chat handler: persist during stream

In `server/api/chat.go`:

- When receiving `user_message`, save a row with `role=user` **before** calling Claude (ensures user message is always persisted even if Claude fails)
- In the stream callback, accumulate assistant text; on completion, save a row with `role=assistant` and the full accumulated text
- On `content_block_start` with tool_use, save a row with `role=tool_use`
- On Claude error, save a row with `role=error` containing the error message

### 5. Frontend: load history on mount

In `web/src/components/chat/ChatPanel.tsx`:

- On mount, call `GET /api/iterations/:id/messages`
- Render existing messages before the WebSocket connects
- This way refresh/re-enter shows full history

### 6. Tests

- Store tests for `CreateMessage` / `ListMessages`
- API test for the new GET endpoint
- Verify messages are ordered chronologically
- Verify iteration isolation (messages from one iteration don't leak to another)
- Verify cascade delete (deleting project cascades to iterations then to messages)
