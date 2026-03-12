# BoxNex 容器安全设计

> 本文档描述 BoxNex 当前的容器隔离架构、安全层级、实现细节及已知局限。
> 早期方案（Podman + UID 隔离 + egress proxy）已废弃，当前使用 Docker rootless + Sysbox。

---

## 架构概览

```
┌──────────────────────────────────────────────────────────────────┐
│  BoxNex Server (Go)                                              │
│                                                                  │
│  ContainerManager ──► docker run                                 │
│       │                    --runtime sysbox-runc                 │
│       │                    --memory / --cpus / --pids-limit      │
│       │                    --network boxnex-net                  │
│       │                    --label boxnex.project=<projectID>    │
│       │                    -v <repoPath>:/workspace              │
│       │                    -v boxnex-home-<userID>:/root         │
│       │                    -v /opt/boxnex/inject.so:ro           │
│       │                                                          │
│       └──► docker exec                                           │
│                    -e LD_PRELOAD=/usr/lib/inject.so              │
│                    claude ...                                    │
└──────────────────────────────────────────────────────────────────┘
```

每个项目对应一个独立 Docker 容器，容器生命周期由 ContainerManager 管理。

---

## 安全层级

### L1 — Sysbox 容器隔离

- `--runtime sysbox-runc`：使用 Sysbox 而非默认 runc
  - 支持 DinD（Docker-in-Docker），无需 `--privileged`
  - 独立 user namespace、mount namespace、pid namespace
  - 容器内无法感知宿主机进程树
- `--rm`：容器停止时文件系统自动销毁（home volume 保留）
- `--network boxnex-net`：容器加入专用 Docker 网络，与宿主 bridge 隔离

### L2 — 资源配额

每个容器在 `docker run` 时通过 Docker 参数强制施加资源上限：

| 参数 | 默认值 | 每用户覆盖 |
|------|--------|-----------|
| `--memory` | 2g | `users.quotas.memory` |
| `--memory-swap` | 等于 memory（关闭 swap） | — |
| `--cpus` | 1.0 | `users.quotas.cpu` |
| `--pids-limit` | 200 | `users.quotas.pids` |
| workspace 磁盘 | 1GB（软限制） | `config.disk_quota_bytes` |

管理员通过 `PATCH /api/admin/users/{userID}/quota` 设置用户配额（写入前校验 JSON 合法性）。
用户可通过 `GET /api/quota` 查询自己的配额。

磁盘配额：每次任务启动前通过 `checkDiskQuota()` 做一次预检查；`WatchWorkspace()` 已实现
（定期轮询 `du`），待后续接入任务执行中的持续监控（见 [待办](#待办)）。

### L3 — OAuth Token 注入（LD_PRELOAD inject.so）

**问题**：直接用 `docker exec -e CLAUDE_CODE_OAUTH_TOKEN=<token>` 传入，token 以明文
出现在 `/proc/<pid>/environ`，低权限进程、日志系统或意外泄露途径均可读取。

**方案**：

1. token 以 XOR 混淆形式硬编码在 `inject.so` 中（源码：`inject/inject.c`）
2. `inject.so` 以 bind mount 方式挂载到容器 `/usr/lib/inject.so:ro`（不内嵌在镜像）
3. `docker exec` 时设置 `LD_PRELOAD=/usr/lib/inject.so`
4. Claude CLI 启动时，`inject.so` 的 `__attribute__((constructor))` 函数执行：
   - 解码 token（XOR 解混淆）
   - `setenv("CLAUDE_CODE_OAUTH_TOKEN", token, 1)`
   - `memset(token, 0, ...)` 立即清零 heap 拷贝

**安全属性**：

| 攻击面 | 防护效果 |
|--------|---------|
| `/proc/<pid>/cmdline` | ✅ token 不在进程参数中 |
| `/proc/<pid>/environ` | ✅ token 不以明文出现（constructor 结束后 env 已设置但 SO 不再持有明文） |
| `strings inject.so` | ✅ XOR 混淆，无法直接提取 |
| 容器内低权限进程读 `/proc` | ✅ Sysbox namespace 隔离 |
| 宿主机读 inject.so | ⚠️ 需要 root；可逆向 |
| 容器内进程内存转储 | ⚠️ token 在 `setenv` 后存在于进程 env block 中 |

**这是深度防御，不是密码学保证。** 核心目标是防止 token 出现在日志和 proc 文件系统中。

**Token 轮换**（一条命令）：

```bash
sudo inject/rotate.sh <new_token>
```

`rotate.sh` 自动完成：XOR 编码 → 编译临时 .c → 安装到 `/opt/boxnex/inject.so` → 删除临时文件。
`inject.c` 本身始终保持 placeholder 状态，token 不入库，不落磁盘明文。
运行中的容器在下次 `docker exec` 时自动使用新 inject.so（bind mount，无需重启容器或重建镜像）。

**inject.so 不存在时的行为**：

- `EnsureContainer`：跳过 `-v /opt/boxnex/inject.so` 挂载，打印 warning
- `ExecClaudeInContainer`：同样检查 host 路径，不设置 `LD_PRELOAD`
- Claude 无法获取 token → 调用失败（预期行为，避免静默降级）

### L4 — 用户 Home Volume 隔离

每个用户拥有独立的 Docker named volume：`boxnex-home-<userID>`，挂载到 `/root`。

- Claude CLI 配置（`~/.claude/`）、工具链缓存持久化
- 跨容器重建后状态保留（不需要重新初始化 claude）
- 不同用户 home volume 名称含 userID，严格隔离

### L5 — 容器生命周期

| 事件 | 行为 | 代码位置 |
|------|------|---------|
| WebSocket subscribe | 预热启动容器（异步） | `chat.go: warmupContainer()` |
| 用户发消息 | 重置闲置计时器 | `chat.go → container.go: TouchActivity()` |
| 闲置超 20 分钟 | 停止容器 | `main.go → StopIdleContainers()` (5min 轮询) |
| 服务器重启 | 重新注册现有容器（不停止） | `main.go → RecoverRunningContainers()` |

容器检索使用 `--label boxnex.project=<projectID>`，`docker ps --filter label=...`。

---

## 实现文件速查

| 文件 | 职责 |
|------|------|
| `server/agent/container.go` | ContainerManager 核心：EnsureContainer, StopIdleContainers, ExecClaudeInContainer, WatchWorkspace |
| `server/api/chat.go` | warmupContainer, userQuota, TouchActivity 集成 |
| `server/api/auth_cloud.go` | PATCH /api/admin/users/{userID}/quota, GET /api/quota |
| `server/api/router.go` | 路由注册 |
| `server/store/models.go` | `User.Quotas` 字段 |
| `server/store/db.go` | `addColumnIfNotExists` 幂等迁移（quotas 列） |
| `server/config/config.go` | ContainerEnabled, ContainerImage, ContainerNetwork, DiskQuotaBytes |
| `inject/inject.c` | LD_PRELOAD token 注入 C 源码 |
| `inject/encode.sh` | 生成 XOR 编码字节数组 |
| `inject/Makefile` | `make` 编译 inject.so，`make install` 部署到 /opt/boxnex/ |
| `scripts/build-agent-image.sh` | 构建 boxnex-agent:latest Docker 镜像 |
| `scripts/e2e-container-test.sh` | 端到端测试（T01–T14） |

---

## 配置示例

`~/.nexbox/config.json`：
```json
{
  "container_enabled": true,
  "container_image": "boxnex-agent:latest",
  "container_network": "boxnex-net",
  "disk_quota_bytes": 1073741824
}
```

用户配额（管理员设置）：
```json
{ "cpu": "2.0", "memory": "4g", "pids": 500 }
```

---

## 待办

| 项目 | 优先级 | 说明 |
|------|--------|------|
| WatchWorkspace 接入 | P2 | 任务执行中的持续磁盘监控，已实现未接入 |
| 网络出站过滤 | P2 | boxnex-net 容器目前不限制出站；可用 iptables 白名单或 HTTP 代理实现 |
| XFS prjquota 磁盘硬限制 | P3 | bind mount workspace 的块级磁盘配额，需要 XFS 文件系统 |
| inject.so 加密存储 | P3 | 当前 XOR 混淆；可考虑 kernel keyring / Vault / 系统 secret manager |
| DinD 出站过滤 | P3 | Sysbox 容器内运行 Docker 时的出站限制 |
| 镜像签名验证 | P3 | 确保 boxnex-agent:latest 未被篡改（Docker Content Trust） |
