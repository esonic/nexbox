# BoxNex 运维手册

> 面向未来维护、部署本项目的工程师。

---

## 目录

1. [项目概览](#1-项目概览)
2. [依赖与环境要求](#2-依赖与环境要求)
3. [快速部署（全流程）](#3-快速部署全流程)
4. [配置参考](#4-配置参考)
5. [日常运维](#5-日常运维)
6. [架构说明](#6-架构说明)
7. [数据库](#7-数据库)
8. [用户与权限管理](#8-用户与权限管理)
9. [邮件发送（Resend）](#9-邮件发送resend)
10. [Podman 容器隔离（可选）](#10-podman-容器隔离可选)
11. [本地开发](#11-本地开发)
12. [常见问题排查](#12-常见问题排查)

---

## 1. 项目概览

BoxNex 是一个**多用户 AI Agent SaaS 平台**，允许多个用户独立使用 Claude 等 AI Agent 完成编程任务。

| 组件 | 技术栈 |
|------|--------|
| 后端 | Go 1.26+，chi 路由，JWT 鉴权 |
| 前端 | React + TypeScript + Vite，嵌入到 Go 二进制中 |
| 数据库 | SQLite（开发/单机）或 MySQL 8.x（生产多实例） |
| AI Agent | Claude CLI，通过 Anthropic API Key 调用 |
| 容器隔离 | Podman Rootless（可选），每个项目独立沙盒容器 |

**两种运行模式：**

| 模式 | 触发条件 | 说明 |
|------|---------|------|
| `local` | `config.json` 里无 `jwt_secret` | 无需登录，仅本机可访问，适合个人开发 |
| `needs_login` | 有 `jwt_secret` | 所有 API 需要 JWT，多用户，适合生产部署 |

---

## 2. 依赖与环境要求

### 操作系统
- Ubuntu 22.04 / 24.04（推荐）
- 其他 Linux 发行版可用，需自行安装依赖

### 必要软件
| 软件 | 版本 | 用途 |
|------|------|------|
| Go | 1.26+ | 编译后端 |
| Node.js | 18+ | 编译前端 |
| npm | 8+ | 前端包管理 |

### 可选软件
| 软件 | 用途 |
|------|------|
| Podman（rootless） | 项目容器隔离（`podman_enabled: true` 时必须） |
| MySQL 8.x | 生产多实例共享数据库 |

> **为什么用 Podman 而不是 Docker？**
> Podman Rootless 无需 root、无守护进程，用户命名空间隔离开箱即用，内存占用更低，更适合多用户 SaaS 生产部署。Podman 不是 Go 库，BoxNex 通过 `exec.Command("podman", ...)` 调用系统中安装的 podman 二进制，`go.mod` 中不会出现 podman 依赖。

### 端口
- 默认监听：`9040`（可通过 `--port` 参数修改）
- 服务默认绑定到 `0.0.0.0`（生产环境建议用 nginx 反向代理到 localhost）

---

## 3. 快速部署（全流程）

### 3.1 准备环境（一键脚本）

```bash
git clone <repo-url> boxnex
cd boxnex

# 完整安装（Podman + Go + 构建镜像 + 生成 config.json）
bash scripts/setup.sh

# 仅安装 Go，跳过 Podman（不启用容器隔离）
bash scripts/setup.sh --no-podman
```

脚本会自动完成：
- 安装 Podman（rootless 模式，无需 root）
- 安装 Go 1.26.1
- 构建 `boxnex-agent:latest` 容器镜像（使用 `Dockerfile.agent`）
- 生成 `server/data/config.json`（含随机 JWT secret，权限 0600）

### 3.2 编辑配置

```bash
vi server/data/config.json
```

**最小生产配置：**

```json
{
  "store_type": "sqlite",
  "jwt_secret": "<openssl rand -hex 32 生成>",
  "token_price_per_million": 15,
  "resend_api_key": "re_xxxx",
  "podman_enabled": true,
  "claude_auth_token": "<CLAUDE_CODE_OAUTH_TOKEN 或 ANTHROPIC_API_KEY>"
}
```

> `jwt_exp_days` 默认 7 天；`resend_from` 默认 `BoxNex <noreply@box-nex.top>`，如需自定义发件人再添加。
> `claude_auth_token` 也可不写入 config，改用环境变量 `CLAUDE_CODE_OAUTH_TOKEN` 启动服务（两者取其一）。

> `config.json` 保存时自动设置文件权限 `0600`，防止泄露 secret。

### 3.3 编译

```bash
./build.sh
```

产物：`server/boxnex`（单一二进制，前端已嵌入）

### 3.4 启动

```bash
# 直接启动（前台，适合测试）
server/boxnex --data-dir server/data --port 9040

# 生产用 systemd（见下方）
```

### 3.5 创建管理员账号

**首个注册的用户自动成为 admin**，无需手动改库。

打开浏览器访问 `http://<your-server>:9040`，走注册流程（邮箱 + 验证码 + 密码）即可。

---

## 4. 配置参考

配置文件位于 `--data-dir` 指定目录下的 `config.json`，默认 `server/data/config.json`。
修改后**重启服务**生效（JWT secret、store_type 等核心配置不支持热重载）。

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `jwt_secret` | string | （空→local 模式） | JWT 签名密钥，生产必填，建议 64 字符随机串 |
| `jwt_exp_days` | int | `7` | JWT 有效期（天） |
| `store_type` | string | `"sqlite"` | `"sqlite"` 或 `"mysql"` |
| `mysql_dsn` | string | — | MySQL DSN，`store_type=mysql` 时必填 |
| `token_price_per_million` | float | `0` | 每百万 token 计费金额（用于余额折算显示） |
| `resend_api_key` | string | — | Resend API Key，生产发邮件必填；空则验证码打到日志 |
| `resend_from` | string | `BoxNex <noreply@box-nex.top>` | 发件人地址（需在 Resend 控制台验证域名） |
| `marketplace_dir` | string | `{datadir}/marketplace` | 本地插件目录路径，支持多种格式（见下方 Marketplace 章节） |
| `podman_enabled` | bool | `false` | 是否启用 Podman 容器隔离 |
| `podman_image` | string | `"boxnex-agent:latest"` | 容器镜像名（需提前构建） |
| `podman_network` | string | `"boxnex-egress"` | Podman 网络名，启动时自动创建 |
| `claude_auth_token` | string | — | Claude OAuth Token 或 API Key（推荐通过 `CLAUDE_CODE_OAUTH_TOKEN` 环境变量注入，避免写入磁盘） |
| `proxy_listen_addr` | string | `<网关IP>:8118` | 出口代理监听地址，默认自动绑定到 Podman 网络网关 IP |
| `proxy_allowed_hosts` | []string | — | 除默认白名单外额外允许的出网域名（见下方说明） |
| `disk_quota_bytes` | int | `268435456`（256MB） | 容器 workspace 磁盘配额，超出则终止 agent |
| `cors_origins` | []string | — | 允许的 CORS 源，空则使用内置开发默认值 |

---

## 4.1 Marketplace 配置

`marketplace_dir` 指向一个本地目录，后端会**递归扫描**其中的插件/技能/连接器。每次用户访问 Marketplace 页面时实时扫描，无需重启服务。

默认值为 `{datadir}/marketplace`，即不配置也会自动使用数据目录下的 `marketplace/` 子目录。如需自定义可在 config.json 中指定绝对路径。

支持四种格式（可混合使用）：

| 格式 | 识别方式 | 说明 |
|------|---------|------|
| 自定义 JSON | `*.json` 文件 | 原有格式，兼容保留 |
| Anthropic Skills | 目录包含 `SKILL.md`（YAML frontmatter） | 来自 `anthropics/skills` 仓库 |
| Anthropic Plugins/Connectors | 目录包含 `.claude-plugin/plugin.json` | 来自 `anthropics/claude-plugins-official` 仓库 |
| 第三方插件注册表 | 目录包含 `.claude-plugin/marketplace.json` | 第三方仓库，`plugins` 数组定义多个插件 |

推荐目录结构：

```
data/marketplace/
  skills/                      ← git clone https://github.com/anthropics/skills
  claude-plugins-official/     ← git clone https://github.com/anthropics/claude-plugins-official
  claude-equity-research/      ← 第三方仓库（marketplace.json 格式）
  other-repo/                  ← 其他三方仓库，管理员自行 clone
  custom/
    my-mcp.json                ← 自定义 JSON 插件
```

如需覆盖默认路径，在 config.json 中配置：

```json
{
  "marketplace_dir": "/opt/boxnex/server/data/marketplace"
}
```

初始化和更新：

```bash
# 首次设置
mkdir -p /opt/boxnex/server/data/marketplace
cd /opt/boxnex/server/data/marketplace
git clone https://github.com/anthropics/skills
git clone https://github.com/anthropics/claude-plugins-official

# 后续更新（无需重启服务，刷新页面即可生效）
cd /opt/boxnex/server/data/marketplace/skills && git pull
cd /opt/boxnex/server/data/marketplace/claude-plugins-official && git pull
```

Connector（外部服务连接器，如 GitHub、Slack 等）位于 `claude-plugins-official/external_plugins/` 下，会自动识别并归类到 Marketplace 的 Connectors 标签页。OAuth 类型的 Connector（如 Slack）当前标注为需要 OAuth，暂不支持安装。

---

## 5. 日常运维

### 5.1 systemd 服务配置

```ini
# /etc/systemd/system/boxnex.service
[Unit]
Description=BoxNex AI Agent Platform
After=network.target

[Service]
Type=simple
User=boxnex
WorkingDirectory=/opt/boxnex
ExecStart=/opt/boxnex/server/boxnex --data-dir /opt/boxnex/server/data --port 9040
Restart=on-failure
RestartSec=5
# Claude OAuth Token（推荐通过环境变量注入，避免写入 config.json 磁盘）
Environment=CLAUDE_CODE_OAUTH_TOKEN=<your-oauth-token>

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now boxnex
sudo systemctl status boxnex
```

### 5.2 nginx 反向代理（推荐）

```nginx
server {
    listen 443 ssl;
    server_name boxnex.yourdomain.com;

    ssl_certificate     /etc/letsencrypt/live/boxnex.yourdomain.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/boxnex.yourdomain.com/privkey.pem;

    location / {
        proxy_pass http://127.0.0.1:9040;
        proxy_http_version 1.1;

        # WebSocket 支持（必须，聊天和终端走 WS）
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";

        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_read_timeout 3600s;
    }
}
```

> **重要**：WebSocket 连接（聊天、终端）需要 nginx 配置 `Upgrade` 头和足够长的 `proxy_read_timeout`，否则连接会被超时断开。

### 5.3 查看日志

日志文件位于 `--data-dir/logs/boxnex.log`，滚动存储（单文件最大 5MB，保留 5 个）：

```bash
tail -f server/data/logs/boxnex.log
# 或如果 data-dir 是 server/
tail -f server/logs/boxnex.log
```

**开发模式下查看验证码：**（未配置 `resend_api_key` 时验证码打到此日志）
```bash
grep "Verification code" server/logs/boxnex.log | tail -5
```

### 5.4 升级部署

```bash
git pull
./build.sh
sudo systemctl restart boxnex
```

编译产物 `server/boxnex` 包含嵌入的前端，无需单独部署静态文件。

### 5.5 备份

**SQLite 模式（单机）：**
```bash
# 在线备份（SQLite 内置，安全）
sqlite3 server/data/boxnex.db ".backup /backup/boxnex-$(date +%Y%m%d).db"

# 或直接拷贝（服务停止时）
cp server/data/boxnex.db /backup/
```

**MySQL 模式：**
```bash
mysqldump -u boxnex -p boxnex > /backup/boxnex-$(date +%Y%m%d).sql
```

---

## 6. 架构说明

```
浏览器
  │  HTTPS / WSS
  ▼
nginx（反向代理，SSL 终止）
  │
  ▼
server/boxnex（Go 单二进制）
  ├── 嵌入前端（React SPA，路由 /）
  ├── REST API（/api/...）
  │     ├── 公开路由：/api/info, /api/auth/login, /api/auth/register,
  │     │             /api/auth/send-verification
  │     ├── AuthMiddleware（JWT 验证）
  │     │     └── 所有业务 API：projects, sessions, files, git, balance...
  │     └── AdminMiddleware（role=admin）
  │           └── /api/admin/...（用户管理、充值、pprof）
  └── WebSocket
        ├── /ws（聊天，需 JWT token 参数）
        └── /ws/terminal/*（终端，需 JWT token 参数）
              │
              ▼
          Claude CLI（agent/claude_cli.go）
              │  可选 docker exec
              ▼
          boxnex-agent 容器（每个 project 一个）
```

**鉴权流程：**
- REST API：`Authorization: Bearer <JWT>` header
- WebSocket：`?token=<JWT>` query 参数（浏览器 WS 不支持自定义 header）
- JWT payload 含 `uid`、`username`、`role`、`features`，纯本地验证，不查 DB

---

## 7. 数据库

### SQLite（默认，单机部署）

数据库文件位于 `--data-dir/boxnex.db`。

首次启动自动建表，后续启动自动做加列迁移（additive migration），**不会删除已有数据**。

```bash
# 查看表结构
sqlite3 server/data/boxnex.db ".schema"

# 查看所有用户
sqlite3 server/data/boxnex.db "SELECT id, username, role, features FROM users;"
```

### MySQL（生产多实例）

```json
{
  "store_type": "mysql",
  "mysql_dsn": "boxnex:password@tcp(127.0.0.1:3306)/boxnex?parseTime=true&charset=utf8mb4"
}
```

建表由程序自动完成（首次启动）。MySQL 用户需要 `CREATE TABLE`、`ALTER TABLE` 权限。

```sql
-- 创建数据库和用户（MySQL 8.x）
CREATE DATABASE boxnex CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE USER 'boxnex'@'localhost' IDENTIFIED BY 'strong-password';
GRANT ALL PRIVILEGES ON boxnex.* TO 'boxnex'@'localhost';
FLUSH PRIVILEGES;
```

---

## 8. 用户与权限管理

### 用户角色

| role | 权限 |
|------|------|
| `user` | 只能操作自己的 project/session，查自己余额 |
| `admin` | 可查看所有用户、project；可充值；可访问 /api/admin/* |

首个注册用户自动成为 admin。后续如需提升某用户：

```bash
# SQLite
sqlite3 server/data/boxnex.db "UPDATE users SET role='admin' WHERE username='user@example.com';"

# MySQL
mysql -u boxnex -p boxnex -e "UPDATE users SET role='admin' WHERE username='user@example.com';"
```

### 功能位掩码（features）

控制用户能看到/使用哪些功能，通过管理员 API 设置：

```
PATCH /api/admin/users/{userId}/features
Body: {"features": 15}
```

| 位 | 值 | 功能 |
|----|-----|------|
| bit 0 | 1 | Task（任务面板） |
| bit 1 | 2 | Asset（资产管理） |
| bit 2 | 4 | Workflow（工作流） |
| bit 3 | 8 | Terminal（终端） |

示例：`features=15`（二进制 `1111`）→ 开放全部功能；`features=0` → 全部关闭（默认）。

### 余额充值

管理员手动给用户充值 Token 额度：

```bash
curl -X POST http://localhost:9040/api/admin/recharge \
  -H "Authorization: Bearer <admin-token>" \
  -H "Content-Type: application/json" \
  -d '{"user_id": "<target-user-id>", "tokens": 1000000}'
```

---

## 9. 邮件发送（Resend）

BoxNex 使用 [Resend](https://resend.com) 发送注册验证码邮件。

### 配置步骤

1. 注册 Resend 账号，获取 API Key
2. 在 Resend 控制台绑定并验证你的发件域名
3. 在 `config.json` 中填入：

```json
{
  "resend_api_key": "re_xxxx",
  "resend_from": "BoxNex <noreply@box-nex.top>"
}
```

### 未配置 Resend 时（开发/测试）

验证码会打印到日志文件，无需真实邮箱：

```bash
grep "Verification code" server/logs/boxnex.log | tail -3
```

### 限流规则

- 同一 IP 对同一邮箱：60 秒内只能发一次验证码
- 验证码有效期：10 分钟

---

## 10. Podman 容器隔离（可选）

启用后，每个 project 的 AI Agent 运行在独立的 Podman Rootless 容器中，项目间文件完全隔离，并内置出口代理防止 API Key 泄露。

### 安装 Podman（Debian/Ubuntu）

```bash
# Debian 12 / Ubuntu 22.04+
sudo apt-get install -y podman

# 验证安装（rootless 模式，不需要 sudo）
podman info | grep -E "rootless|version"
```

### 构建 Agent 镜像

```bash
# 镜像定义在项目根目录 Dockerfile.agent，推荐用脚本构建
bash scripts/build-agent-image.sh

# 或手动构建
podman build -t boxnex-agent:latest -f Dockerfile.agent .

# 验证
podman image ls boxnex-agent
```

### 配置

```json
{
  "podman_enabled": true,
  "podman_image": "boxnex-agent:latest",
  "podman_network": "boxnex-egress",
  "proxy_allowed_hosts": []
}
```

`CLAUDE_CODE_OAUTH_TOKEN` **建议通过环境变量注入**，避免写入 `config.json` 磁盘文件：

```ini
# /etc/systemd/system/boxnex.service
[Service]
Environment=CLAUDE_CODE_OAUTH_TOKEN=<your-oauth-token>
```

### 工作原理

```
BoxNex 服务（宿主机）
  │  podman exec -u 9999:9999 -e CLAUDE_CODE_OAUTH_TOKEN=<真实key> ...
  ▼
容器内 claude-runner 用户（UID 9999）
  │  直接持有真实 API Key，连接 api.anthropic.com
  │  agent 用户（UID 1000）无法通过 /proc 读取（无 CAP_SYS_PTRACE）
  ▼
Anthropic API
```

**安全模型说明：**

- 容器内有两个用户：`agent`（UID 1000，运行用户命令）和 `claude-runner`（UID 9999，专属运行 Claude CLI）
- 真实 API Key 仅在 `podman exec -u 9999:9999 -e KEY=...` 时注入，从不写入容器持久环境
- 容器以 `--cap-drop ALL` 启动，无 `CAP_SYS_PTRACE`，UID 1000 无法读取 UID 9999 进程的 `/proc/<pid>/environ`
- Claude CLI 二进制权限设为 `0500`，只有 UID 9999 可执行，agent 用户无法直接运行
- 启动时自动创建 `boxnex-egress` 网络（带 `--opt isolate=true` 阻止容器间直接通信）
- 出口代理优先绑定到 Podman 网络网关 IP（如 `10.89.0.1:8118`）；若网关 IP 暂未绑定（Podman rootless 懒建桥接口），则 fallback 到 `0.0.0.0:8118`，并通过宿主机出口 IP 告知容器代理地址
- workspace 磁盘超配额时自动终止 agent

### 出网域名白名单

容器通过出口代理访问外网，只有白名单内的域名可以建立 HTTPS CONNECT 隧道。`api.anthropic.com` 在白名单内，Claude CLI 直接使用由 `podman exec -e` 注入的真实 API Key 连接。

**内置默认白名单**（无需配置，自动放行）：

| 域名 | 用途 |
|------|------|
| `api.anthropic.com` | Claude API（Claude CLI 直连，Key 由 podman exec -e 注入） |
| `statsig.anthropic.com` | Claude CLI 功能开关 |
| `sentry.io` | Claude CLI 错误上报 |
| `registry.npmjs.org` / `npmjs.org` | npm 包下载（npx 类 MCP 必须） |
| `github.com` / `api.github.com` | Git 操作 + GitHub Copilot MCP |
| `gitlab.com` | Git 操作 + GitLab MCP |
| `mcp.slack.com` | Slack MCP |
| `mcp.stripe.com` | Stripe MCP |
| `mcp.linear.app` | Linear MCP |
| `mcp.asana.com` | Asana MCP |
| `api.githubcopilot.com` | GitHub Copilot MCP（REST） |
| `api.greptile.com` | Greptile MCP |
| `mcp.supabase.com` | Supabase MCP |

**追加额外域名**（如自建服务、其他 Connector）：

```json
{
  "proxy_allowed_hosts": [
    "your-service.example.com",
    "api.notion.com"
  ]
}
```

> **注意**：只需填主机名，不需要带协议或端口。允许的是 HTTPS CONNECT 隧道。对于不遵守 `HTTP_PROXY` 环境变量的工具（直接 raw socket），代理无法拦截——这是 Podman Rootless 无 root 权限的结构性限制。

### 常用运维命令

```bash
# 查看正在运行的 agent 容器
podman ps --filter "name=boxnex-proj-"

# 查看某个容器日志
podman logs boxnex-proj-<name>

# 手动停止某个项目的容器
podman stop boxnex-proj-<project-id>

# 查看出口网络
podman network ls
podman network inspect boxnex-egress

# 重建镜像后重启（容器会在下次请求时自动用新镜像启动）
bash scripts/build-agent-image.sh
sudo systemctl restart boxnex
```

### WSL2 开发注意事项

WSL2 下 Podman Rootless 可直接工作，无需处理 iptables 兼容性问题（这是相比 Docker 的优势之一）。

---

## 11. 本地开发

### 启动方式

```bash
# 终端 1：前端热重载
cd web && npm run dev

# 终端 2：后端（本地模式，无需登录）
cd server && go run . --data-dir ./data --port 9040
```

或用已有脚本一次性编译并运行：
```bash
./build.sh          # 编译前端 + 后端
server/boxnex --data-dir server/data --port 9040
```

### 本地开发模式（local mode）

`config.json` 中**不填 `jwt_secret`** → 服务以 local 模式启动，无需登录即可使用全部功能。适合个人开发，不适合暴露给外部用户。

### 测试注册验证码

无 Resend 配置时，验证码出现在日志里：

```bash
tail -f server/logs/boxnex.log | grep "Verification code"
```

### 运行单元测试

```bash
cd server && go test ./...
```

---

## 12. 常见问题排查

### 问题：服务启动后浏览器显示空白

**检查：** `server/webdist/` 目录是否存在，前端是否编译过。

```bash
./build.sh  # 重新编译前后端
```

---

### 问题：WebSocket 连接失败（401）

**原因：** 前端 WS 连接时 token 未正确传入 `?token=` 参数，或 token 已过期。

**检查日志：**
```bash
grep '"GET /ws"' server/logs/boxnex.log | tail -10
```

**解决：** 退出登录重新登录，获取新 token。

---

### 问题：登录返回 429

两种情况：

1. **IP 封锁**：同一 IP 1 分钟内超过 10 次登录尝试。等 1 分钟后重试。
2. **账号锁定**：同一邮箱连续失败 5 次，锁定 15 分钟。错误信息里有剩余秒数。

---

### 问题：注册时"verification code invalid"

- 验证码有效期 10 分钟，超时需重新发送
- 验证码已使用过（一次性）

---

### 问题：`podman: command not found` 或容器无法启动

```bash
# 检查 podman 是否安装
which podman && podman --version

# 安装（Debian/Ubuntu）
sudo apt-get install -y podman

# 检查镜像是否存在
podman image ls | grep boxnex-agent

# 重新构建镜像
bash scripts/build-agent-image.sh
```

---

### 问题：容器启动后 Claude API 返回 401

可能原因：

1. **`CLAUDE_CODE_OAUTH_TOKEN` 未配置或已过期**：检查 systemd 服务的 `Environment=` 行，或确认环境变量已正确注入。Token 过期需在 Anthropic Console 重新生成。

2. **旧容器残留**：服务重启后旧容器若未停止，下次调用时 BoxNex 会复用旧容器，但注入的是新读取的 Key，一般不会有问题。如仍有异常：

```bash
# 手动停止所有 boxnex 容器，下次请求时自动用当前 Key 重启
podman stop $(podman ps -q --filter "name=boxnex-proj-")
```

或直接重启服务（`RecoverRunningContainers` 会自动清理旧容器）：

```bash
sudo systemctl restart boxnex
```

---

### 问题：Connector/MCP 工具无法访问外网

原因：容器出口代理只允许白名单域名，第三方 API 域名未放行。

**解决**：在 `config.json` 的 `proxy_allowed_hosts` 中添加需要的域名：

```json
{
  "proxy_allowed_hosts": ["api.github.com", "registry.npmjs.org"]
}
```

修改后重启服务生效。如果工具仍然无法访问，可能是该工具不遵守 `HTTP_PROXY` 环境变量（直接 raw socket），属于 Podman Rootless 的结构性限制，无法通过白名单解决。

---

### 问题：MySQL 连接失败

```bash
# 测试连接
mysql -u boxnex -p -h 127.0.0.1 boxnex -e "SELECT 1"

# 检查 DSN 格式（注意 parseTime=true 是必须的）
# 正确格式："user:pass@tcp(host:3306)/dbname?parseTime=true&charset=utf8mb4"
```

---

### 问题：`config.json` 修改后不生效

**大部分配置修改需重启服务：**
```bash
sudo systemctl restart boxnex
```

**JWT secret 修改后**：所有已登录用户的 token 立即失效，需重新登录。

---

## 附录：关键文件路径

| 文件/目录 | 说明 |
|----------|------|
| `server/boxnex` | 编译后的服务二进制 |
| `server/data/config.json` | 运行时配置（权限 0600） |
| `server/data/boxnex.db` | SQLite 数据库 |
| `server/data/logs/boxnex.log` | 运行日志（滚动，5MB × 5个） |
| `server/data/marketplace/` | 本地插件 JSON 文件目录 |
| `scripts/setup.sh` | 一键环境初始化脚本 |
| `scripts/build-agent-image.sh` | 构建 Agent 容器镜像脚本 |
| `scripts/e2e-container-test.sh` | 容器隔离端到端测试套件 |
| `build.sh` | 编译脚本（前端 + 后端） |
| `Dockerfile.agent` | Agent 沙盒容器镜像定义 |
