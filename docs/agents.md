# Agent 支持

NexBox 调用运行机器上的 Agent CLI，不内置模型服务，也不代替 CLI 的登录与 provider 配置。先在运行 NexBox 的同一系统账号下配置好所用 CLI。

## 接入方式

| Agent | CLI | 协议 | 会话内切换模型 | 分叉 | 运行中插话 |
| --- | --- | --- | --- | --- | --- |
| Claude Code | `claude` | SDK control / stream-json | 支持 | 支持 | 支持 |
| Codex | `codex` | app-server JSON-RPC | 支持 | 支持 | 支持 |
| Pi | `pi` | RPC + NexBox extension | 支持 | 支持 | 支持 |
| Kimi Code | `kimi` | ACP stdio | 支持 | 仅最新完成回合 | 不支持，消息入队 |
| ZeroClaw | `zeroclaw` | ACP stdio | 仅新会话 | 不支持 | 不支持，消息入队 |
| Crush | `crush` | 原生 HTTP / SSE | 支持 | 不支持 | 不支持，消息入队 |

界面以适配器实际声明的能力为准。不同 CLI 的版本、模型和原生能力可能不同；出现兼容问题时，记录 CLI 版本与 NexBox 构建版本。

Kimi、ZeroClaw 和 Crush 在启动时检测可执行文件，未安装时不注册。Claude Code、Codex 和 Pi 仍需安装相应 CLI 才能发起对话。新增 CLI 或修改服务进程的环境后重启 NexBox。

## 模型与配置

- **Claude Code / Codex**：使用原生模型目录；登录信息由 CLI 管理。
- **Pi**：读取 Pi 的配置与选定模型目录；未明确配置目录时，让 Pi 使用其默认模型。支持通过 NexBox extension 注入 HTTP MCP，当前不支持该扩展下的 stdio MCP。
- **Kimi Code**：模型别名来自 `kimi provider list --json`，默认模型与会话持久化由 Kimi 管理。ACP 没有历史轮次选择器，因此仅允许从最新完成回合精确分叉。
- **ZeroClaw**：下拉项对应 `zeroclaw agents list` 的 Agent alias。alias 中的模型、provider 和执行策略由 ZeroClaw 决定；别名原样传给原生协议。可配置多个 alias。
- **Crush**：读取原生配置中的 provider/model，不使用完整的在线模型目录；已有会话可以切换模型。NexBox 按需管理其原生服务进程。

可选的 CLI 路径环境变量：

| 环境变量 | 用途 |
| --- | --- |
| `NEXBOX_PI_CLI` | Pi 可执行文件 |
| `NEXBOX_KIMI_CLI` | Kimi 可执行文件 |
| `NEXBOX_ZEROCLAW_CLI` | ZeroClaw 可执行文件 |
| `NEXBOX_ZEROCLAW_AGENT` | 显式选择 ZeroClaw alias |
| `NEXBOX_CRUSH_CLI` | Crush 可执行文件 |

CLI 原生环境变量按其自身规则生效。请通过对应 CLI 管理登录和模型配置，不把个人凭据写进源码或提交的配置示例。

## 会话与分叉

NexBox 的会话 ID 与底层 Agent 的原生会话 ID 不同。适配器保存底层续聊标识，并在支持时记录已完成回复的 checkpoint。

分叉复制选定回复之前的对话历史，但继承父会话**当前**的工作目录，包括未提交和未跟踪的文件。它不会回滚文件，也不会再创建一个分支或 worktree。父子会话后续对话各自独立，对文件的修改则互相可见。

## 浏览器与后台任务

当前 Claude Code、Codex 和 Kimi 的适配器声明了 Chrome 工具支持；可使用 NexBox 托管浏览器，也可按会话连接外部 Chrome。

子 Agent 的可见事件取决于原生协议。Kimi ACP 不展开子 Agent 内部过程；每轮进程退出后，其后台任务不会跨轮保留。不要把不同后端的能力当作完全一致。
