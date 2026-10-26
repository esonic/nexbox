# 部署、配置与维护

NexBox 是运行在你自己的电脑或服务器上的工具。一个 Go 进程提供 API、WebSocket 和内嵌前端，SQLite 保存本地数据；Agent CLI 与终端在同一系统账号下执行。

## 本地运行

从源码构建：

```bash
./build.sh
```

启动：

```bash
./dist/nexbox
```

默认监听 `127.0.0.1:9040`，打开即可使用，无需创建账号或登录。前端已嵌入可执行文件，不需要另起 Node.js 服务。默认数据目录是可执行文件所在目录；构建产物位于 `dist/`，因此上述命令默认将数据写入 `dist/`。

## 使用已有可执行文件

把匹配目标平台的 `nexbox` 文件放到任意可写目录，直接运行即可，不需要安装器、启动脚本或源码。如果文件没有执行权限，先执行 `chmod +x nexbox`。

例如，将文件放在 `/opt/nexbox/nexbox` 后：

```bash
/opt/nexbox/nexbox --help
/opt/nexbox/nexbox
```

也可以把程序与数据分开存放：

```bash
export NEXBOX_DATA_DIR="$HOME/nexbox-data"
/opt/nexbox/nexbox list-users
/opt/nexbox/nexbox
```

默认免登录。需要密码登录时再添加账号；账号命令与服务启动使用同一数据目录即可。

## 服务器部署

目标机器需要 Git，以及所用 Agent 的 CLI、登录或 provider 配置。Chrome/Chromium 仅在使用托管浏览器时需要。只运行已构建的 NexBox 文件不需要 Go、Node.js 或 npm；构建产物必须匹配目标操作系统和 CPU 架构。

用运行服务的同一系统账号验证 Agent CLI。服务管理器通常不会读取交互式 shell 配置，因此需要显式设置 CLI 所需的 `PATH` 和环境变量。

公开域名访问或与受信的人共用时，可先按下文[本地账号](#本地账号)添加账号并启用密码登录。访问模式只由是否存在登录账号决定，不会根据监听地址或域名自动切换。

直接监听远程地址：

```bash
NEXBOX_DATA_DIR=/var/lib/nexbox /opt/nexbox/nexbox --listen 0.0.0.0 --port 9040
```

也可以保持默认回环监听，通过现有的 SSH 隧道或 HTTPS 反向代理访问。反向代理需要转发 WebSocket，并允许长时间流式响应；不要为对话连接启用响应缓冲或过短的空闲超时。

NexBox 服务本身提供 HTTP。公开网络的 TLS 在反向代理层配置。运行环境用于个人或受信用户，所有 Agent 和终端共享运行服务的系统账号权限。

需要开机启动或进程守护时，可自行使用惯用的服务管理器。NexBox 不要求特定的启动脚本。

## 运行参数

| 参数 | 默认值 | 含义 |
| --- | --- | --- |
| `--listen` | `127.0.0.1` | HTTP 监听地址 |
| `--port` | `9040` | HTTP 端口 |
| `--data-dir` | 见下文 | 服务数据目录 |
| `--version` | — | 显示构建版本后退出 |
| `--help` | — | 显示服务和账号命令 |

服务数据目录的优先级为：

1. 显式 `--data-dir` 参数。
2. `NEXBOX_DATA_DIR` 环境变量。
3. 可执行文件所在目录。

账号子命令在启动服务前执行，使用环境变量或可执行文件所在目录；`--data-dir` 仅用于服务启动。

## 本地账号

账号是可选功能。没有登录账号时，所有访问直接进入同一个本地工作区，界面不显示账号和退出登录入口。

```bash
./dist/nexbox list-users
./dist/nexbox add-user local "$NEXBOX_PASSWORD"
./dist/nexbox reset-password local "$NEXBOX_PASSWORD"
./dist/nexbox del-user local
```

创建和重设密码前，在终端以隐藏输入方式设置 `NEXBOX_PASSWORD`，完成后 `unset NEXBOX_PASSWORD`。账号名由 1–32 个英文字母、数字、下划线或连字符组成。

添加第一个账号会立即启用密码登录，已有项目和会话归入该账号，文件路径不变。后续添加的账号各自创建项目；删除最后一个账号后，全部项目恢复为免登录工作区。账号增删无需重启；切换模式时，已打开的对话和终端连接会断开，页面重新检查登录状态。

账号通过本机命令管理，没有外部注册、邮件服务或管理员角色配置。已有账号的实例升级后仍需登录。启用密码登录时，服务重启会让已有登录失效，需要重新登录。

## 应用配置

首次启动会创建 `<data-dir>/config.json`。常用选项可在界面设置中修改。下列为配置格式示例：

```json
{
  "default_agent": "claude",
  "chrome_headless": true,
  "mcp_tools": {
    "task_management": false
  },
  "suggest_next_actions": true,
  "prompt_templates": []
}
```

| 配置 | 含义 |
| --- | --- |
| `default_agent` | 新会话和轻量辅助任务使用的默认 Agent |
| `chrome_headless` | 新建托管 Chrome 时使用无头模式 |
| `mcp_tools.task_management` | 是否向 Agent 暴露内置任务管理工具，默认关闭 |
| `suggest_next_actions` | 是否请求回复末尾给出可点击的后续建议，默认开启 |
| `prompt_templates` | 全局提示词模板，单项包含 `name` 和 `content` |
模型服务、登录信息和 Agent 原生配置由对应 CLI 管理，不写入这个配置文件。手动编辑 `config.json` 后重启服务；在界面修改的设置按各功能直接生效。当前存储后端为 SQLite。

如需为 Claude CLI 指定独立临时目录，可设置 `NEXBOX_CLAUDE_TMPDIR`。该值会原样传给 Claude CLI 的 `CLAUDE_CODE_TMPDIR`。

### 可选的站点验证与 Android TWA

只有需要域名所有权验证或 Android Trusted Web Activity 的公开部署才需要
`public_web`。普通本地和服务器部署无需添加这些字段。配置后，NexBox 会提供
对应的根目录验证文件及 `/.well-known/assetlinks.json`：

```json
{
  "public_web": {
    "verification_files": {
      "provider-verification.txt": "replace-with-your-verification-value"
    },
    "android_asset_links": {
      "package_name": "com.example.app",
      "sha256_cert_fingerprints": ["AA:BB:CC:..."]
    }
  }
}
```

这些部署值放在实例现有的 `config.json` 中，不需要额外配置文件。没有配置时，
相关 URL 不会发布验证内容。

## 数据与工作目录

| 路径 | 内容 |
| --- | --- |
| `nexbox.db` | 账号、项目、会话、消息、工作流、市场安装记录等 |
| `config.json` | 应用设置 |
| `logs/` | 运行日志，按大小轮转 |
| `users/` | 由 NexBox 自动创建的项目工作目录 |
| `sessions/` | 会话附件等文件 |
| `worktrees/` | 会话或工作流创建的 Git worktree |
| `marketplace/` | 市场来源及其本地副本 |
| `global-plugins/` | 按需创建的全局插件安装目录 |
| `chrome/` | 托管 Chrome 的用户数据 |
| `crush/` | 使用 Crush 时的适配器运行数据 |

以上路径均相对数据目录。用户自行指定的项目路径可以位于数据目录之外；Agent 原生的会话和登录数据也可能存放在其自己的主目录中。只保存 `nexbox.db` 不包含这些外部文件。

项目可自动创建目录，也可接入现有目录；接入现有路径不会把文件复制进数据目录。

## 升级与停止

1. 停止 NexBox，保留原有数据目录。
2. 更新源码后执行 `./build.sh`，或直接替换为匹配目标平台的新可执行文件。
3. 使用原数据目录启动。SQLite 迁移会在启动时执行。

替换文件前停止原有进程，更新完成后重新启动。命令行运行时使用 `Ctrl+C`，服务管理器使用正常的停止命令；服务处理 SIGINT/SIGTERM 并关闭浏览器和 MCP 资源。数据迁移不承诺支持旧版本回退。

## 常见问题

**找不到构建产物**：默认产物是 `dist/nexbox`。设置过 `NEXBOX_OUTPUT` 时，运行该变量指定位置的文件。

**账号创建后无法登录，或者看不到已有数据**：确认账号命令与服务使用相同的程序和数据目录；尤其注意服务的 `--data-dir` 是否覆盖了 `NEXBOX_DATA_DIR`。

**Agent 找不到或无法使用模型**：确认 CLI 在运行服务的账号和 `PATH` 下可用，并能独立完成一次请求。部分 CLI 仅在启动时检测是否注册；安装后重启 NexBox。

**浏览器工具无法启动**：确认已安装 Chrome/Chromium；服务器一般保留 `chrome_headless: true`。连接外部 Chrome 时，填写 NexBox 服务进程可访问的 DevTools 地址。

**远程访问时对话或终端中断**：检查反向代理的 WebSocket 转发、响应缓冲和连接超时。先直接访问 NexBox 监听端口，以区分服务与代理问题。
