# ZeroClaw 与 Crush 接入备忘

## 已实现

Nexbox 现已注册四个 agent：Codex、Claude Code、ZeroClaw、Crush。

共同原则：上层只依赖 `Agent`、模型目录和能力声明；每个下层适配器使用最合适的原生协议。ACP 不是能力上限。

### ZeroClaw

- 保持 ACP stdio 接入，不解析 `zeroclaw agent` 的终端输出。
- Nexbox 调用 `zeroclaw agents list`，将输出的每个 alias 原样作为模型下拉项。
- 新会话把选中的 alias 传为 ACP `session/new.agentAlias`。
- ZeroClaw 的 alias 内包含什么 provider、真实模型、工具、风险策略或人格，均由 ZeroClaw 配置决定，Nexbox 不解析也不校验。
- 模型仅能在首条消息前选择；会话开始后不可修改。fork 和 interject 不可用。

因此，若要在 ZeroClaw 中增加可选模型，只需新增一个 ZeroClaw agent alias；不需要修改 Nexbox 配置。

### Crush

- 使用 Crush 原生 HTTP/SSE 服务，不使用 ACP，也不解析 TUI 或 `crush run` 输出。
- Nexbox 按工作目录创建并复用 Crush workspace，保存 Crush session ID 以支持续聊。
- Crush 的配置、鉴权和模型目录由 Crush 自己管理；Nexbox 不保存这些状态，也不会在用户项目中创建或修改 `.crush`。
- 通过 SSE 映射文本、思考、工具调用、工具结果、权限请求、完成和取消事件。
- 每轮 Crush 对话开始时，Nexbox 自动执行 `crush server` 并在本机随机端口监听；该轮完成、失败或取消后立即关闭 workspace/SSE 与子进程。多个并发回合共用同一子进程，直到最后一个回合结束。
- Nexbox 不转写或保存 OpenRouter 密钥。部署 Nexbox 的进程直接提供 Crush 所需的 `OPENROUTER_API_KEY` 即可；子进程原样继承该环境变量。
- Crush 支持在已有会话中改模型；Nexbox 将它声明为 `current_session`，模型下拉在会话中仍可选择。
- 当前原生接口未实现 Nexbox 语义的 fork 或 active-turn interject，因此两项均禁用。

## Crush 模型配置

Nexbox 读取 Crush 原生 server 的已配置 provider/model；不维护模型列表或 provider 配置，也不会带入 Catwalk 的全量目录。使用全局 `~/.config/crush/crushrc` 配置少量需要的模型：

```bash
option default-providers false
provider add openrouter --type openai-compat --base-url "https://openrouter.ai/api/v1" --api-key "$OPENROUTER_API_KEY"
model add openrouter/minimax/minimax-m3:free --name "MiniMax M3 Free"
```

其中每个 `model add` 就是 Nexbox 下拉中的一项。`OPENROUTER_API_KEY` 始终只保留在部署进程环境中；Nexbox 不转写或保存它。Crush 模型必须由用户显式选择，Nexbox 不自动补默认模型。

## 安装与验证记录

- Crush 使用官方方式安装：`go install github.com/charmbracelet/crush@v0.91.2`。
- 已通过直接提供 `OPENROUTER_API_KEY`，使用 `openrouter/minimax/minimax-m3:free` 完成真实 Crush 请求。
- 已通过适配器级真实集成测试：启动原生 Crush server、创建 workspace/session、选择模型、发送请求、接收 SSE 完成结果。

## 后续接入原则

新增 agent 时优先判断其原生机器接口能否覆盖 Nexbox 所需能力；若 ACP 足够则复用 ACP 客户端，否则写独立原生适配器。每个适配器必须明确声明模型目录来源和模型可切换范围：`current_session` 或 `new_session_only`。
