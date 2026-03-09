# Claude Agent SDK 对比分析报告

> 分析时间：2026-03-09
> 参考来源：`/home/ericdu/src/claude-agent-sdk-python`（官方 Python SDK 源码）
> 对比代码：`server/agent/claude_agent.go`、`server/agent/claude_cli.go`

---

## 总体评价

核心机制已正确实现：双向控制协议（`control_request` / `control_response`）、`can_use_tool` 处理、`AskUserQuestion` 路由、session resume/expire、进程组管理、token budget 等。但存在以下几类差距。

---

## 一、有功能性问题的点

### 1. 只能响应 control_request，缺少主动发出 control_request 的能力

`claude_cli.go` 的 `Query()` 方法只能响应 CLI 发来的 `control_request`（如 `can_use_tool`），从未主动向 CLI 发出 `control_request`。

官方 SDK 的 `_send_control_request()` 可以主动发：
```
SDK → CLI: {"type":"control_request","request_id":"req_1","request":{"subtype":"interrupt"}}
CLI → SDK: {"type":"control_response","response":{"subtype":"success","request_id":"req_1"}}
```

当前缺失的主动 control_request 能力：
- `interrupt` - 优雅中断（我们用 SIGTERM 替代）
- `initialize` - 初始化握手（见下条）
- `set_model` - 运行时动态切换模型
- `set_permission_mode` - 动态切换权限模式
- `mcp_status` / `mcp_reconnect` / `mcp_toggle` - MCP 服务器管理
- `stop_task` - 停止子任务
- `rewind_files` - 文件回滚

### 2. 缺少 initialize 握手（hooks 完全无法工作）

官方 SDK 在发送第一条用户消息**之前**，发出 `initialize` control_request：
```json
{
  "type": "control_request",
  "request_id": "req_1",
  "request": {
    "subtype": "initialize",
    "hooks": {"PreToolUse": [...], "PostToolUse": [...]},
    "agents": {"code-reviewer": {...}}
  }
}
```

CLI 返回响应后，hooks 才在 CLI 侧生效。我们对来自 CLI 的 `initialize` 子类型只是 auto-acknowledge，但从不主动发这个请求，导致：
- 即使以后加了 hook 配置，也无法传给 CLI
- 无法获取 CLI 能力信息

### 3. `--allowed-tools` 参数格式不一致

我们传的是：
```go
args = append(args, "--allowed-tools")
args = append(args, opts.AllowedTools...)  // 多个独立参数
```

官方 SDK 传的是：
```python
cmd.extend(["--allowedTools", ",".join(self._options.allowed_tools)])  // camelCase + 逗号分隔
```

需要验证 Claude CLI 是否同时接受这两种格式（kebab-case 多参数 vs camelCase 单参数）。

---

## 二、缺失但重要的功能

### 4. 没有 Partial Messages / 实时流式输出

官方 SDK 支持 `--include-partial-messages` 配合环境变量 `CLAUDE_CODE_ENABLE_FINE_GRAINED_TOOL_STREAMING=1`，产生 `stream_event` 类型消息，携带 Claude 逐字生成的文本。

当前我们只在 Claude 完成整个 assistant turn 后才触发 `EventTextDelta`，用户看到的是"一段话突然全部出现"而非打字机效果。

需要：
1. 传 `--include-partial-messages`
2. 处理 `stream_event` 消息（目前会走到 `default` 分支被忽略）

### 5. Hooks 系统完全缺失

官方 SDK 支持四类 hook：
- `PreToolUse` - 工具调用前拦截（可 allow/deny + 修改 input）
- `PostToolUse` - 工具调用后审计（可注入 systemMessage 给下一轮）
- `UserPromptSubmit` - 用户消息提交前注入上下文
- `SessionStart` - 会话开始时初始化

我们对 `hook_callback` 直接 auto-continue，无法实际拦截：
```go
case "hook_callback":
    c.sendControlSuccess(writeStdin, requestID, map[string]interface{}{
        "continue": true,
    })
```

PreToolUse hook 尤其有价值：在不修改 bypassPermissions 模式的前提下对特定工具做细粒度审计或拦截。

### 6. ThinkingBlock 缺少 `signature` 字段

官方 SDK 解析 thinking 块时同时捕获 `signature`：
```python
ThinkingBlock(thinking=block["thinking"], signature=block["signature"])
```

我们的 `AssistantContent` 只有 `Thinking string`，没有 `Signature`。

`signature` 在多轮 extended thinking 对话中是必要的——Claude 在后续轮次需要用它验证 thinking 块的完整性。

### 7. task_started / task_progress / task_notification 消息未处理

官方 SDK 专门处理这三种 system 子类型，由 Claude 的 `Agent` 工具产生（委托子 agent 时）。我们除 `compact_boundary` 外，其他 system subtype 统一忽略。对展示子任务进度有用。

---

## 三、行为不同但属于设计选择的点

### 8. 没有传 `--setting-sources ""`

官方 SDK 默认传空字符串，禁止 Claude CLI 加载磁盘上的 CLAUDE.md 和用户配置文件。

我们没传，Claude 会加载宿主环境的 CLAUDE.md。这可能是想要的（让用户 CLAUDE.md 生效）也可能不是（服务器环境下宿主配置干扰行为）。需要明确决策。

### 9. 没有传 `--system-prompt ""`

官方 SDK 默认清空 system prompt：
```python
if self._options.system_prompt is None:
    cmd.extend(["--system-prompt", ""])
```

我们不传，依赖 Claude 的默认 system prompt，通过 `BuildPrompt()` 在用户消息前缀注入上下文。可行，但 Claude 默认 system prompt 可能与我们注入的上下文产生冲突。

### 10. 没有设置 `CLAUDE_CODE_ENTRYPOINT` 环境变量

官方 SDK 设置：
```python
process_env["CLAUDE_CODE_ENTRYPOINT"] = "sdk-py"
process_env["CLAUDE_AGENT_SDK_VERSION"] = __version__
```

影响 Anthropic 侧使用遥测统计，不影响功能。建议加 `"sdk-go"`。

### 11. 没有 CLI 版本检查

官方 SDK 启动时运行 `claude -v` 验证版本 >= 2.0.0，低于该版本发出警告。

---

## 四、result 消息中缺少的字段

| 字段 | 官方 SDK | 我们 |
|------|----------|------|
| `duration_ms` | ✅ | ✅ |
| `duration_api_ms` | ✅ | ❌ |
| `is_error` | ✅ | ✅ |
| `num_turns` | ✅ | ✅ |
| `session_id` | ✅ | ✅ |
| `stop_reason` | ✅ | ❌ |
| `total_cost_usd` | ✅ | ✅ |
| `usage` | ✅ | ✅ |
| `result` | ✅ | ✅ |
| `structured_output` | ✅ | ❌ |

`stop_reason` 特别有用，可区分 max_turns 还是正常完成。

---

## 五、关于旁路架构的评估

MCP、plugin 通过 `--mcp-config` 和 `--plugin-dir` 传递，与官方 SDK 机制完全一致，旁路维护不影响兼容性，未来支持 Codex CLI 等其他 agent 也无需修改。

---

## 优先级建议

| 优先级 | 问题 | 理由 |
|--------|------|------|
| P0 | `--allowedTools` 格式验证 | 若 CLI 不认识格式会导致工具不可用 |
| P1 | Partial messages 流式输出 | 直接影响用户体验（打字机效果） |
| P1 | 主动 control_request 能力（从 interrupt 开始） | 停止任务依赖 SIGTERM，不够优雅 |
| P2 | ThinkingBlock signature 字段 | 多轮 extended thinking 的正确性 |
| P2 | 补全 result 字段（stop_reason、duration_api_ms） | 更准确的状态记录 |
| P3 | Hooks 系统 | 强大但需要较多改动 |
| P3 | task_notification 等 system 子类型处理 | 子任务进度展示 |
| P3 | CLI 版本检查 | 防御性措施 |
| P4 | `--setting-sources` / `CLAUDE_CODE_ENTRYPOINT` | 规范性改进 |
