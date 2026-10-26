# Claude Code 协议排查

本文面向适配器维护者。Claude Code CLI 升级后，如果出现空回复、对话长时间处理中、后台任务提前结束或工具 diff 丢失，先检查协议事件与回合边界。

## 适配方式

NexBox 每轮启动一个 Claude CLI 子进程，续聊时传入原生 session ID。`server/agent/claude_cli.go` 通过 stream-json 与双向 control 协议读取事件、响应交互请求并管理子进程；`claude_agent.go` 再把它们映射为界面事件。

一个前台 `result` 不一定代表所有工作已经结束。适配器会识别提前到达的空结果，跟踪后台任务，并在确认回合结束后关闭输入、等待进程退出。

## 收集信息

先记录 NexBox 构建版本、Claude CLI 版本、是否续聊、是否存在后台 Agent，以及问题是否能在普通单轮请求中复现。

```bash
./dist/nexbox --version
claude --version
```

服务日志位于 `<data-dir>/logs/nexbox.log`。分析时关注事件类型、子类型、原生 session ID、tool ID、parent tool ID 与到达顺序。分享日志前移除对话正文、个人路径和授权信息。

区分两个会话标识：

- NexBox 的会话 ID 对应 SQLite `chat_messages.session_id`，用于界面、API 和项目关联。
- CLI 的原生 session ID 用于续聊，不等于 NexBox 会话 ID。

## 关键边界

### 提前到达的 result

续聊并处理遗留后台通知时，CLI 可能先输出没有实际回合内容的结果。适配器结合 `num_turns`、费用和已经收到的内容判断是否忽略，而不是见到第一个 `result` 就结束。

### 后台任务

支持的 CLI 会通过 `background_tasks_changed` 提供运行中任务快照。适配器用快照替换本地任务集合，避免增量通知遗漏或重复。旧协议仍通过 `task_started`、终态通知和 user 消息中的 `<task-notification>` 更新集合。

任务集合非空时，前台结果不会立即结束读取。新增任务类型时需要明确其生命周期，不能仅根据工具卡片已返回就判断后台任务结束。

### 工具结果与 diff

SDK stream-json 使用 `tool_use_result`，保存的 transcript 可能使用 `toolUseResult`；两种拼写都需要接受。Edit 的 `structuredPatch` 可生成 diff；Write 明确标记为 create 时可呈现新增文件。覆盖写入没有旧内容或补丁时，不应凭空构造全绿 diff。

### 子进程收尾

正常回合结束后，适配器给进程有限的退出宽限期；超过 `postTurnExitGrace` 时终止残留进程组。用户停止通过 context 取消中断运行。要区分“仍有工作，继续读取”和“工作结束，进程未退出”两种情况。

## 症状与检查方向

| 症状 | 优先检查 |
| --- | --- |
| CLI 原生记录有回答，界面却为空 | 是否在真实内容到达前处理了提前 result；是否遗漏 assistant 内容事件 |
| 一直显示处理中 | 后台任务集合是否正确清空；读取循环是否已退出但在等待子进程 |
| 子 Agent 尚未完成，主回合就结束 | 任务类型、parent tool ID 和完成通知是否正确映射 |
| 工具调用完成但没有 diff | 结果字段拼写、structuredPatch，以及新建／覆盖写入的区别 |
| 消息重复或分裂 | 同一回合是否重复派发；流式增量与最终快照是否被重复追加 |

## 回归与协议探针

```bash
cd server
go test ./agent -run 'TestQuery|TestClaude'
```

对应测试集中在 `claude_cli_premature_result_test.go`、`claude_subagent_test.go` 和 `claude_diff_test.go`。需要验证新 CLI 行为时，在临时工作目录中使用最小请求记录原始事件，再把相关事件序列转成可重复的测试夹具。不要让协议回归测试依赖个人项目、生产模型或开发机中的固定目录。
