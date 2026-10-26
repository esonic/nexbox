# NexBox

在浏览器中使用编码 Agent 的个人工作台。

NexBox 把对话、代码文件、Git、终端和工作流放进同一个界面。它运行在你的电脑或自己的服务器上，直接调用该机器上已配置的 Agent CLI。前端随 Go 服务一起打包，运行时只需一个可执行文件，无需单独部署前端或安装桌面壳。

## 功能

- **多 Agent 对话**：Claude Code、Codex、Pi、Kimi Code、ZeroClaw 和 Crush；流式展示文本、思考和工具调用。
- **会话管理**：续聊、分叉、会话置顶、活动排序，以及后端支持时的运行中插话。
- **代码工作区**：文件浏览、搜索、Monaco 编辑器、工具调用 diff 和多仓库 Git 面板。
- **终端与浏览器**：集成终端，以及按需启用的 Chrome 浏览器工具。
- **可复用能力**：提示词模板、MCP、Skills 和插件市场；支持项目级与全局安装。
- **任务自动化**：工作流、调度和跨会话协作。

## 运行要求

| 组件 | 要求 |
| --- | --- |
| 操作系统 | Linux 或 macOS；Windows 使用 WSL2 |
| Go | 1.25 或以上，仅构建时需要 |
| Node.js / npm | Node.js 22.12+、npm 9+，仅构建和前端开发时需要 |

远程部署方式见[部署文档](docs/ops_manual.md)。

## 快速开始

在源码目录中执行：

```bash
./build.sh
```

构建脚本按锁文件安装前端依赖、构建前端并嵌入 Go 可执行文件，产物为 `dist/nexbox`。Go 构建会自动下载缺失的模块。已有可执行文件时，可直接运行，无需运行构建脚本。

启动：

```bash
./dist/nexbox
```

打开 <http://127.0.0.1:9040> 即可使用，无需创建账号或登录。选择已配置的 Agent 开始对话，也可以创建项目并接入现有代码目录。首次使用前，先确认对应 CLI 在同一系统账号下可以独立运行。

### 可选：启用密码登录

需要远程访问或与受信的人共用时，可以添加本地账号：

```bash
read -r -s -p 'Password: ' NEXBOX_PASSWORD
printf '\n'
./dist/nexbox add-user local "$NEXBOX_PASSWORD"
unset NEXBOX_PASSWORD
```

添加第一个账号后立即启用密码登录，无需重启；已有项目和会话归入该账号，文件保持原位。删除最后一个账号会恢复免登录使用。已有账号的实例升级后仍需登录。

账号只属于这台 NexBox 实例，无需注册外部服务。具体命令见[本地账号](docs/ops_manual.md#本地账号)。

## 常用命令

```bash
./build.sh                         # 从源码构建 dist/nexbox，包含前端
./dist/nexbox --help               # 查看服务参数和账号命令
./dist/nexbox --port 9050          # 使用其他端口
./dist/nexbox list-users           # 查看本地账号
```

可执行文件可以放在任意可写目录。默认数据目录是可执行文件所在目录；也可以显式指定：

```bash
export NEXBOX_DATA_DIR="$HOME/nexbox-data"
./dist/nexbox list-users
./dist/nexbox
```

账号命令和服务应使用相同的数据目录。新的数据目录默认免登录。服务参数 `--data-dir` 的优先级高于环境变量。

运行时不需要 Node.js、npm、Go 或源码目录，但需要 Git 和所用 Agent 的 CLI。部署到其他机器时，复制匹配其操作系统和 CPU 架构的可执行文件，并在目标机器配置好 Agent 即可。

## 文档

- [部署、配置与维护](docs/ops_manual.md)：本地运行、服务器部署、数据目录和升级。
- [开发指南](docs/development.md)：开发服务、构建、验证和代码结构。
- [Agent 支持](docs/agents.md)：CLI、协议、模型选择和能力限制。
- [架构说明](docs/architecture.md)：进程、存储和工作区语义。
- [Marketplace 与 MCP](docs/marketplace.md)：来源、安装作用域、授权和可用性。
- [Claude Code 协议排查](docs/claude-code-protocol-troubleshooting.md)。
