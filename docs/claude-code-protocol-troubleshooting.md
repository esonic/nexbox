# Claude Code Protocol Troubleshooting

This guide is for adapter maintainers. After a Claude Code CLI upgrade, investigate protocol events and turn boundaries if responses are empty, conversations remain in progress, background tasks end early, or tool diffs disappear.

## Adapter Design

NexBox starts a Claude CLI subprocess for each turn and passes the native session ID when resuming. `server/agent/claude_cli.go` reads events, responds to interactive requests, and manages the subprocess through stream-json and the bidirectional control protocol. `claude_agent.go` maps these to interface events.

A foreground `result` does not necessarily mean all work has finished. The adapter recognizes premature empty results, tracks background tasks, and closes input and waits for process exit only after confirming that the turn has ended.

## Information to Collect

Record the NexBox build version, Claude CLI version, whether the session is resumed, whether background agents are involved, and whether the issue reproduces with a simple single-turn request.

```bash
./dist/nexbox --version
claude --version
```

Service logs are stored in `<data-dir>/logs/nexbox.log`. Focus on event types, subtypes, native session IDs, tool IDs, parent tool IDs, and arrival order. Remove conversation content, personal paths, and authorization information before sharing logs.

Distinguish the two session identifiers:

- The NexBox session ID corresponds to SQLite `chat_messages.session_id` and links the interface, API, and project.
- The CLI's native session ID is used for continuation and differs from the NexBox session ID.

## Key Boundaries

### Premature Results

When resuming and processing pending background notifications, the CLI may emit a result before any actual turn content. The adapter uses `num_turns`, cost, and content already received to decide whether to ignore it, rather than ending at the first `result`.

### Background Tasks

Supported CLI versions provide snapshots of running tasks through `background_tasks_changed`. The adapter replaces its local task set with each snapshot to avoid missing or duplicating incremental notifications. Older protocols still update the set through `task_started`, terminal-state notifications, and `<task-notification>` in user messages.

A foreground result does not immediately end reading while the task set is nonempty. When adding task types, define their lifecycle explicitly. A returned tool card alone does not establish that background work has finished.

### Tool Results and Diffs

SDK stream-json uses `tool_use_result`, while saved transcripts may use `toolUseResult`; accept both spellings. An Edit result's `structuredPatch` can produce a diff. A Write result explicitly marked as create can be shown as a new file. Do not fabricate an all-added diff for an overwrite when neither previous content nor a patch is available.

### Subprocess Cleanup

After a normal turn ends, the adapter gives the process a bounded exit grace period. If it exceeds `postTurnExitGrace`, the remaining process group is terminated. A user stop interrupts execution through context cancellation. Distinguish ongoing work that requires continued reading from completed work whose process has not exited.

## Symptoms and Checks

| Symptom | Check first |
| --- | --- |
| Native CLI records contain a response, but the interface is empty | Premature results handled before actual content; missing assistant content events |
| Conversation stays in progress | Background task set not cleared; read loop exited but waiting for the subprocess |
| Main turn ends before a subagent finishes | Task types, parent tool IDs, and completion notification mapping |
| Tool call finishes without a diff | Result field spellings, structuredPatch, and create versus overwrite behavior |
| Messages are duplicated or split | Duplicate dispatch of a turn; appending both streamed deltas and final snapshots |

## Regression Tests and Protocol Probes

```bash
cd server
go test ./agent -run 'TestQuery|TestClaude'
```

Relevant tests are in `claude_cli_premature_result_test.go`, `claude_subagent_test.go`, and `claude_diff_test.go`. To verify a new CLI's behavior, capture raw events from a minimal request in a temporary working directory, then turn the relevant event sequence into repeatable test fixtures. Protocol regression tests must not depend on personal projects, production models, or fixed directories on a developer's machine.
