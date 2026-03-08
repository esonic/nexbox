# BeeHub 运维手册

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
10. [Docker 容器隔离（可选）](#10-docker-容器隔离可选)
11. [本地开发](#11-本地开发)
12. [常见问题排查](#12-常见问题排查)

---

## 1. 项目概览

BeeHub 是一个**多用户 AI Agent SaaS 平台**，允许多个用户独立使用 Claude 等 AI Agent 完成编程任务。

| 组件 | 技术栈 |
|------|--------|
| 后端 | Go 1.22+，chi 路由，JWT 鉴权 |
| 前端 | React + TypeScript + Vite，嵌入到 Go 二进制中 |
| 数据库 | SQLite（开发/单机）或 MySQL 8.x（生产多实例） |
| AI Agent | Claude CLI，通过 Anthropic API Key 调用 |
| 容器隔离 | Docker（可选），每个项目独立沙盒容器 |

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
| Go | 1.22+ | 编译后端 |
| Node.js | 18+ | 编译前端 |
| npm | 8+ | 前端包管理 |

### 可选软件
| 软件 | 用途 |
|------|------|
| Docker Engine | 项目容器隔离（`docker_enabled: true` 时必须） |
| MySQL 8.x | 生产多实例共享数据库 |

### 端口
- 默认监听：`9040`（可通过 `--port` 参数修改）
- 服务默认绑定到 `0.0.0.0`（生产环境建议用 nginx 反向代理到 localhost）

---

## 3. 快速部署（全流程）

### 3.1 准备环境（一键脚本）

```bash
git clone <repo-url> beehub
cd beehub

# 完整安装（Docker + Go + 构建镜像 + 生成 config.json）
bash scripts/setup.sh

# 仅安装 Go，跳过 Docker（不启用容器隔离）
bash scripts/setup.sh --no-docker
```

脚本会自动完成：
- 安装 Docker Engine（处理 WSL2 iptables 兼容性）
- 安装 Go 1.22.5
- 构建 `nexbox-agent:latest` Docker 镜像
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
  "jwt_exp_days": 7,
  "anthropic_key": "sk-ant-...",
  "token_price_per_million": 15,
  "resend_api_key": "re_xxxx",
  "resend_from": "BeeHub <noreply@box-nex.top>"
}
```

> `config.json` 保存时自动设置文件权限 `0600`，防止泄露 secret。

### 3.3 编译

```bash
./build.sh
```

产物：`server/beehub`（单一二进制，前端已嵌入）

### 3.4 启动

```bash
# 直接启动（前台，适合测试）
server/beehub --data-dir server/data --port 9040

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
| `anthropic_key` | string | — | Anthropic API Key，Agent 执行必填 |
| `token_price_per_million` | float | `0` | 每百万 token 计费金额（用于余额折算显示） |
| `resend_api_key` | string | — | Resend API Key，生产发邮件必填；空则验证码打到日志 |
| `resend_from` | string | `BeeHub <noreply@box-nex.top>` | 发件人地址（需在 Resend 控制台验证域名） |
| `marketplace_dir` | string | — | 本地插件目录路径，支持三种格式（见下方 Marketplace 章节） |
| `docker_enabled` | bool | `false` | 是否启用 Docker 容器隔离 |
| `docker_image` | string | `"nexbox-agent:latest"` | 容器镜像名 |
| `cors_origins` | []string | — | 允许的 CORS 源，空则使用内置开发默认值 |
| `disk_quota_bytes` | int | `0`（不限） | 用户磁盘配额 |
| `max_concurrent_tasks` | int | `0`（不限） | 用户最大并发任务数 |
| `max_sessions_per_user` | int | `0`（不限） | 用户最大 session 数 |

---

## 4.1 Marketplace 配置

`marketplace_dir` 指向一个本地目录，后端会**递归扫描**其中的插件/技能/连接器。每次用户访问 Marketplace 页面时实时扫描，无需重启服务。

支持三种格式（可混合使用）：

| 格式 | 识别方式 | 说明 |
|------|---------|------|
| 自定义 JSON | `*.json` 文件 | 原有格式，兼容保留 |
| Anthropic Skills | 目录包含 `SKILL.md`（YAML frontmatter） | 来自 `anthropics/skills` 仓库 |
| Anthropic Plugins/Connectors | 目录包含 `.claude-plugin/plugin.json` | 来自 `anthropics/claude-plugins-official` 仓库 |

推荐目录结构：

```
data/marketplace/
  skills/                      ← git clone https://github.com/anthropics/skills
  claude-plugins-official/     ← git clone https://github.com/anthropics/claude-plugins-official
  other-repo/                  ← 第三方仓库，管理员自行 clone
  custom/
    my-mcp.json                ← 自定义 JSON 插件
```

配置示例：

```json
{
  "marketplace_dir": "/opt/beehub/server/data/marketplace"
}
```

初始化和更新：

```bash
# 首次设置
mkdir -p /opt/beehub/server/data/marketplace
cd /opt/beehub/server/data/marketplace
git clone https://github.com/anthropics/skills
git clone https://github.com/anthropics/claude-plugins-official

# 后续更新（无需重启服务，刷新页面即可生效）
cd /opt/beehub/server/data/marketplace/skills && git pull
cd /opt/beehub/server/data/marketplace/claude-plugins-official && git pull
```

Connector（外部服务连接器，如 GitHub、Slack 等）位于 `claude-plugins-official/external_plugins/` 下，会自动识别并归类到 Marketplace 的 Connectors 标签页。OAuth 类型的 Connector（如 Slack）当前标注为需要 OAuth，暂不支持安装。

---

## 5. 日常运维

### 5.1 systemd 服务配置

```ini
# /etc/systemd/system/beehub.service
[Unit]
Description=BeeHub AI Agent Platform
After=network.target

[Service]
Type=simple
User=beehub
WorkingDirectory=/opt/beehub
ExecStart=/opt/beehub/server/beehub --data-dir /opt/beehub/server/data --port 9040
Restart=on-failure
RestartSec=5
Environment=CLAUDE_CODE_OAUTH_TOKEN=<your-anthropic-token>

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now beehub
sudo systemctl status beehub
```

### 5.2 nginx 反向代理（推荐）

```nginx
server {
    listen 443 ssl;
    server_name beehub.yourdomain.com;

    ssl_certificate     /etc/letsencrypt/live/beehub.yourdomain.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/beehub.yourdomain.com/privkey.pem;

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

日志文件位于 `--data-dir/logs/beehub.log`，滚动存储（单文件最大 5MB，保留 5 个）：

```bash
tail -f server/data/logs/beehub.log
# 或如果 data-dir 是 server/
tail -f server/logs/beehub.log
```

**开发模式下查看验证码：**（未配置 `resend_api_key` 时验证码打到此日志）
```bash
grep "Verification code" server/logs/beehub.log | tail -5
```

### 5.4 升级部署

```bash
git pull
./build.sh
sudo systemctl restart beehub
```

编译产物 `server/beehub` 包含嵌入的前端，无需单独部署静态文件。

### 5.5 备份

**SQLite 模式（单机）：**
```bash
# 在线备份（SQLite 内置，安全）
sqlite3 server/data/beehub.db ".backup /backup/beehub-$(date +%Y%m%d).db"

# 或直接拷贝（服务停止时）
cp server/data/beehub.db /backup/
```

**MySQL 模式：**
```bash
mysqldump -u beehub -p beehub > /backup/beehub-$(date +%Y%m%d).sql
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
server/beehub（Go 单二进制）
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
          nexbox-agent 容器（每个 project 一个）
```

**鉴权流程：**
- REST API：`Authorization: Bearer <JWT>` header
- WebSocket：`?token=<JWT>` query 参数（浏览器 WS 不支持自定义 header）
- JWT payload 含 `uid`、`username`、`role`、`features`，纯本地验证，不查 DB

---

## 7. 数据库

### SQLite（默认，单机部署）

数据库文件位于 `--data-dir/beehub.db`。

首次启动自动建表，后续启动自动做加列迁移（additive migration），**不会删除已有数据**。

```bash
# 查看表结构
sqlite3 server/data/beehub.db ".schema"

# 查看所有用户
sqlite3 server/data/beehub.db "SELECT id, username, role, features FROM users;"
```

### MySQL（生产多实例）

```json
{
  "store_type": "mysql",
  "mysql_dsn": "beehub:password@tcp(127.0.0.1:3306)/beehub?parseTime=true&charset=utf8mb4"
}
```

建表由程序自动完成（首次启动）。MySQL 用户需要 `CREATE TABLE`、`ALTER TABLE` 权限。

```sql
-- 创建数据库和用户（MySQL 8.x）
CREATE DATABASE beehub CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE USER 'beehub'@'localhost' IDENTIFIED BY 'strong-password';
GRANT ALL PRIVILEGES ON beehub.* TO 'beehub'@'localhost';
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
sqlite3 server/data/beehub.db "UPDATE users SET role='admin' WHERE username='user@example.com';"

# MySQL
mysql -u beehub -p beehub -e "UPDATE users SET role='admin' WHERE username='user@example.com';"
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

BeeHub 使用 [Resend](https://resend.com) 发送注册验证码邮件。

### 配置步骤

1. 注册 Resend 账号，获取 API Key
2. 在 Resend 控制台绑定并验证你的发件域名
3. 在 `config.json` 中填入：

```json
{
  "resend_api_key": "re_xxxx",
  "resend_from": "BeeHub <noreply@box-nex.top>"
}
```

### 未配置 Resend 时（开发/测试）

验证码会打印到日志文件，无需真实邮箱：

```bash
grep "Verification code" server/logs/beehub.log | tail -3
```

### 限流规则

- 同一 IP 对同一邮箱：60 秒内只能发一次验证码
- 验证码有效期：10 分钟

---

## 10. Docker 容器隔离（可选）

启用后，每个 project 的 AI Agent 运行在独立的 Docker 容器中，项目间文件完全隔离。

### 前提条件

```bash
# 确认 Docker daemon 运行中
docker info

# 构建 agent 镜像
bash scripts/build-agent-image.sh
```

### 配置

```json
{
  "docker_enabled": true,
  "docker_image": "nexbox-agent:latest"
}
```

### 工作原理

- 创建 session 时，`ContainerManager.EnsureContainer()` 检查是否已有容器
- 容器以 `tail -f /dev/null` 保活，项目 git 目录 bind-mount 到 `/workspace`
- Agent 执行通过 `docker exec` 在容器内运行
- 删除 project 时自动调用 `docker stop` 停止容器

### WSL2 注意事项

WSL2 下 Docker 使用 iptables 可能有兼容性问题，`scripts/setup.sh` 会自动切换为 `iptables-legacy`。
如果手动安装 Docker 后无法启动，执行：

```bash
sudo update-alternatives --set iptables /usr/sbin/iptables-legacy
sudo update-alternatives --set ip6tables /usr/sbin/ip6tables-legacy
sudo service docker restart
```

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
server/beehub --data-dir server/data --port 9040
```

### 本地开发模式（local mode）

`config.json` 中**不填 `jwt_secret`** → 服务以 local 模式启动，无需登录即可使用全部功能。适合个人开发，不适合暴露给外部用户。

### 测试注册验证码

无 Resend 配置时，验证码出现在日志里：

```bash
tail -f server/logs/beehub.log | grep "Verification code"
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
grep '"GET /ws"' server/logs/beehub.log | tail -10
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

### 问题：`docker: command not found` 或容器无法启动

```bash
docker info           # 检查 daemon 是否运行
sudo service docker start  # WSL2 下手动启动

# 检查镜像是否存在
docker images | grep nexbox-agent

# 重新构建镜像
bash scripts/build-agent-image.sh
```

---

### 问题：MySQL 连接失败

```bash
# 测试连接
mysql -u beehub -p -h 127.0.0.1 beehub -e "SELECT 1"

# 检查 DSN 格式（注意 parseTime=true 是必须的）
# 正确格式："user:pass@tcp(host:3306)/dbname?parseTime=true&charset=utf8mb4"
```

---

### 问题：`config.json` 修改后不生效

**大部分配置修改需重启服务：**
```bash
sudo systemctl restart beehub
```

**JWT secret 修改后**：所有已登录用户的 token 立即失效，需重新登录。

---

## 附录：关键文件路径

| 文件/目录 | 说明 |
|----------|------|
| `server/beehub` | 编译后的服务二进制 |
| `server/data/config.json` | 运行时配置（权限 0600） |
| `server/data/beehub.db` | SQLite 数据库 |
| `server/data/logs/beehub.log` | 运行日志（滚动，5MB × 5个） |
| `server/data/marketplace/` | 本地插件 JSON 文件目录 |
| `scripts/setup.sh` | 一键环境初始化脚本 |
| `build.sh` | 编译脚本（前端 + 后端） |
| `Dockerfile.agent` | Agent 沙盒容器镜像定义 |
