# Claude Code 升级导致的协议问题 — 排查方法论

> 用途：Claude Code CLI 升级后，出现「消息显示不对 / 空回复 / 对话卡在处理中 / workflow 或 Agent 没执行完就结束」这类问题时，按本文排查。
> 沉淀自 2026-07 一次 CC 2.1.198→2.1.199 升级引发的连锁问题。

## 背景：为什么升级容易出问题

beehub **每一轮对话都新起一个 `claude --resume` 子进程**（`--input-format/--output-format stream-json` + control 协议），读到 `result` 就 break、关 stdin、等进程退出。
而官方 **Agent SDK 是一条常驻进程、一条 stdout 从头读到尾、收到 `result` 也不停读**。

这个「每轮起停」vs「常驻连续读」的错配，是绝大多数协议问题的根源——CLI 版本一变，`result` 的时机、消息的切分、后台任务的投递方式稍有调整，beehub 的「按轮 break」逻辑就容易踩空。

核心代码：`server/agent/claude_cli.go` 的 `Query()` 读流循环 + 收尾段。

## 排查手段（按决定性排序）

### 1. 写「活 CLI 探针」——最决定性
日志和 transcript 常常有缓冲/缺字段/歧义。直接用真实 `claude` 驱动 `ClaudeClient.Query`，把原始 stream 字段打出来，一发入魂。放一个 gated 测试：

```go
// PROBE=1 go test ./agent/ -run TestProbe -v -timeout 300s   （TMPDIR=/tmp 避开脏环境）
func TestProbe(t *testing.T) {
    if os.Getenv("PROBE") != "1" { t.Skip() }
    client := NewClaudeClient(); client.Verbose = false
    // prompt 里让模型做出你要复现的行为（起 workflow / 后台 Agent / resume 等）
    client.Query(ctx, prompt, QueryOptions{PermissionMode:"bypassPermissions", IncludePartialMessages:true},
        func(msg StreamMessage, parsed *AssistantMessage) {
            // 打关键字段：msg.Type/Subtype、TaskID/TaskType/Status/Patch、
            // result 的 NumTurns/TotalCostUSD、事件到达的相对时刻
        })
}
```
用它确认：消息类型/子类型序列、`task_type` 的确切值、`result` 是「撑住这一轮到最后才出」还是「提前甩出、任务甩后台」、完成通知走 system 还是 user 消息。**用完删掉探针文件**。

### 2. 对照官方 SDK 作为协议基准
仓库：`/home/ericdu.guest/src/claude-agent-sdk-python`
- `src/claude_agent_sdk/_internal/message_parser.py`——每种消息的**权威结构**（字段、子类型）。
- `src/claude_agent_sdk/_internal/query.py` 的 `_read_messages`——SDK **收到 result 不 break、跨轮连续读**（beehub 与它的关键差异点）。

### 3. Diff 不同版本的 transcript
`~/.claude/projects/<项目slug>/<session>.jsonl`（`version` 字段可分辨 CC 版本）。对比升级前后的结构变化——例如 assistant 内容按 block（thinking/text/tool_use）拆成多行、新增 system 子类型。

### 4. 看服务端日志（注意坑）
`server/logs/beehub.log`，grep 消息类型序列。
⚠️ 是 rolling log，**有缓冲延迟**，近几分钟的行可能还没落盘，实时排查会误判；且 `task_type`/`status` 等字段**不打进日志**（要靠探针）。

### 5. 直接查 DB 看症状
`server/beehub.db`，`chat_messages` 表：`parts='[]'`=空回复；同 `created_at`+同内容不同 id=重复行。
⚠️ `chat_messages.session_id` 是 beehub 的 **conversationID**，不是 CLI 的 agent session id（两者不同，别查错）。

### 6. grep CLI bundle 找常量/开关
`~/.local/share/claude/versions/<ver>`（ELF，用 `grep -a`）。找环境变量、默认值、报错文案，例如 `CLAUDE_CODE_PRINT_BG_WAIT_CEILING_MS`（后台任务等待上限，默认 600000ms）。

## 已知协议事实（排查时的先验）

- **assistant 内容按 block 拆分**：thinking / text / tool_use 各自一行 `assistant`，但**共享同一个 `msg.id`**。（2.1.198 起如此，非新问题）
- **premature `result`**：`--resume` 且有挂起的后台任务通知时，CLI 会在**真正这轮开始前**先吐一个 no-op result（`num_turns==0` 且 `cost==0`）。beehub 若在此 break 会得到空回复。
- **脱管后台任务**：`task_type` 为 `local_workflow`（Workflow 工具）或 `local_agent`（后台 Agent 子任务）。它们**工具调用立即返回、在后台继续跑、寿命超过前台这轮的 result**。完成信号有多种投递：system `task_notification`/`task_updated{status:completed}`、作为 **user 消息 `<task-notification>`** 回灌（重新唤起 agent 自动续写），或**（2.1.206+）只体现在 `background_tasks_changed` 快照里**。
  - 行为**因类型而异**：实测 CLI 对 `local_workflow` 会「撑住这一轮」（result 在完成之后），对 `local_agent` 则「脱管」（result 提前 ~7s 出、子任务还在跑）。**按显式类型集合跟踪，别靠观察到的撑/脱行为**。
- **`background_tasks_changed`（2.1.206 新增）**：一份**权威快照** `{"tasks":[{task_id,task_type,description}...]}`，列出当前所有在跑的后台任务，每次集合变化就发一次，全部完成时发 `tasks:[]`。**这是判断「后台是否还有任务」的最可靠信号**——直接用它替换本地跟踪集合，比「task_started 加 / task_notification 减」的增量计数健壮（增量在 finder→verifier 这类不断变动的任务集里会失同步，导致提前结束被 grace-kill）。
- **收尾阻塞**：break 后 `cmd.Wait()`/`wg.Wait()` 会等进程退出；若有后台任务没退，最长等到 600s 上限，表现为对话一直「处理中」。
- 每次升级可能冒出**无害的新 system 子类型**（如 `status`、`thinking_tokens`）和 `rate_limit_event`——不产生 parts、不该触发 break，忽略即可。

## 症状 → 病因速查

| 症状 | 大概率病因 | 查证手段 |
|---|---|---|
| 空回复（DB `parts='[]'`），但 `claude -r` 里有完整回答 | 在真实回答前的 no-op/premature result 处 break 了 | 探针看 result 的 num_turns/cost + 到达时机 |
| 对话一直「处理中」不结束 | 后台任务没被跟踪→吞 result 死循环；或 break 后 `cmd.Wait` 卡在 600s | 日志有无 `keeping stream open`/`force-killing`；探针看 result 时机 |
| workflow / Agent「没执行完就结束」 | 该 `task_type` 没被 `runningBgTasks` 跟踪 | 探针打 `task_started` 的 `task_type` |
| 消息莫名分裂/重复成两条 | 同一 turn 被双写（goroutine 二次起 / 队列重投） | DB 查同 `created_at`+同内容不同 id |

## 现有防护（`claude_cli.go` `Query`）

- **跳过 premature result**：`num_turns==0 && cost==0 && 尚无内容` 的 result 不当结束，继续读。
- **脱管任务跟踪**：优先用 `background_tasks_changed` 快照**权威替换** `runningBgTasks`（2.1.206+）；旧版兜底走增量——`task_started` 且 `task_type ∈ {local_workflow, local_agent}` 加入，system `task_notification`/`task_updated{terminal}` 或 user `<task-notification>` 清除。集合非空时 result 只表示前台空闲，**吞掉继续读**（有 64 次上限兜底）。
- **收尾 grace-kill**：break 后进程超过 `postTurnExitGrace`(30s) 不退就强杀进程组、干净返回，避免卡到 600s。
- 卡死的最终逃生：用户点 Stop（ctx cancel → 杀进程组），刻意不设内部超时。

> 新增脱管任务类型时，记得把它的 `task_type` 加进上面的跟踪集合，并用探针确认它是「撑住」还是「脱管」。回归测试见 `server/agent/claude_cli_premature_result_test.go`。
