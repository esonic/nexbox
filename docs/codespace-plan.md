# Codespace 功能计划

目标：让用户拥有类似 GitHub Codespace 的完整开发环境——可以自己装软件、跑 Docker、不用时自动停机、登录即恢复。

---

## 技术选型

**容器运行时：Docker rootless + Sysbox**

- Docker rootless 保持无 root daemon 的安全模型
- Sysbox 提供完整 DinD、systemd、mount 等能力，安全隔离优于 `--privileged`
- 宿主机要求：cgroup v2（Debian 12+ 默认开启），kernel >= 5.11（overlay 性能）

**磁盘配额：Docker volume + XFS project quota**

- 宿主机一次性配置 XFS prjquota，内核层硬性 ENOSPC
- 不再依赖轮询，替代现有 `WatchWorkspace`

---

## 任务清单

### P0 — 核心基础 ✅ 已完成

**T1. DockerRuntime** ✅
- `server/agent/container.go` 全部改为 `docker` CLI，支持 `--runtime=sysbox-runc`
- Podman 代码彻底移除（`proxy.go` 删除，`container_test.go` 清理）
- `config.go`：新增 `container_enabled` / `container_image` / `container_network`

**T2. 移除 egress proxy** ✅
- `server/agent/proxy.go` 删除，容器直接出网
- tc 限速的 `bandwidth_kbps` 字段已在 quotas JSON 中预留（随 T7 一起管理）

**T3. LD_PRELOAD token 注入** ✅
- `experiments/ld-preload-inject/inject.c`：XOR 混淆 hardcode token
- `experiments/ld-preload-inject/encode.sh`：生成混淆字节数组的工具脚本
- `inject.so` 宿主机维护，容器启动时挂载 `-v /opt/boxnex/inject.so:/usr/lib/inject.so:ro`
- `ExecClaudeInContainer` 自动注入 `LD_PRELOAD=/usr/lib/inject.so`

**T4. 新版基础镜像** 📝 待执行
- 基于 Debian slim，含 git、build-essential、node、python3、Claude CLI
- inject.so 不内置于镜像，运行时从宿主机挂载

**T5. 磁盘配额** 📝 待执行
- 宿主机：XFS prjquota 硬性 ENOSPC（内核层，无需轮询）
- 代码：`docker volume create` per-user volume 替代 `WatchWorkspace` 轮询

---

### P1 — 用户环境持久化 ✅ 已完成

**T6. Per-user 持久 home volume** ✅
- `EnsureContainer` 接受 `userID`，自动创建并挂载 `boxnex-home-<userID>:/root`
- 用户 dotfiles、shell history、自装软件在容器重建后不丢失

**T7. 资源配额可配置化** ✅
- DB `users.quotas` JSON 字段（含向后兼容 `ALTER TABLE` 迁移）：
  ```json
  { "cpu": "1.0", "memory": "2g", "pids": 200 }
  ```
- `EnsureContainer` 读取配额，缺省字段退回默认值
- Admin API：`PATCH /api/admin/users/{userID}/quota`
- 用户 API：`GET /api/quota`

---

### P2 — 生命周期管理 ✅ 已完成

**T8. 订阅 session 时预热容器** ✅
- WebSocket `type: "subscribe"` → 后台 goroutine 调用 `warmupContainer`
- 用户开始聊天时容器已就绪，消除冷启动延迟

**T9. 闲置检测（chat 消息）** ✅
- `handleUserMessage` 在 project 解析后调用 `ContainerMgr.TouchActivity(projectID)`
- 后台 reaper 每 5 分钟检查，20 分钟无 chat 消息自动停止容器

**T10. 重启后容器状态恢复** ✅
- `RecoverRunningContainers` 改为扫描 `docker ps --filter label=boxnex.project`
- 找到的容器重新注册到 `lastActivity`（不再停止它们），由 idle reaper 自然管理

---

### P3 — 体验完善

**T11. 端口转发**
- `GET /api/sessions/{sessionId}/ports`：扫描容器内监听端口（`docker exec ss -tlnp`）
- `/proxy/{sessionId}/{port}/*`：HTTP 反向代理到容器对应端口
- 前端：检测到新端口时提示"在浏览器打开"

**T12. Sysbox + DinD 验证**
- 确认用户能在容器内 `docker build`、`docker compose up`
- 确认宿主机资源限制（cgroup v2）对嵌套容器仍然生效

---

## 宿主机前置要求（部署时一次性操作）

```bash
# 1. 安装 Docker rootless
curl -fsSL https://get.docker.com/rootless | sh

# 2. 安装 Sysbox
# 参考 https://github.com/nestybox/sysbox 官方安装包

# 3. 开启 cgroup delegation
mkdir -p /etc/systemd/system/user@.service.d
cat > /etc/systemd/system/user@.service.d/delegate.conf << EOF
[Service]
Delegate=yes
EOF
systemctl daemon-reload

# 4. XFS 磁盘 quota（如需硬性磁盘限制）
mkfs.xfs -f /dev/sdX
mount -o prjquota /dev/sdX /var/lib/docker-user   # rootless docker data root

# 5. 准备 inject.so
# 编译：cc -shared -fPIC -O2 -o /opt/boxnex/inject.so experiments/ld-preload-inject/inject.c
# 容器启动时挂载：-v /opt/boxnex/inject.so:/usr/lib/inject.so:ro
```

---

## 代码变更汇总

| 文件 | 状态 | 变化 |
|------|------|------|
| `server/agent/container.go` | ✅ | Docker 替换 Podman；`EnsureContainer` 加 userID/quota 参数；home volume 挂载；`RecoverRunningContainers` 改为重注册 |
| `server/agent/proxy.go` | ✅ **删除** | — |
| `server/agent/claude_agent.go` | ✅ | 传递 UserID/ContainerQuota 给 EnsureContainer；去掉 fallback |
| `server/agent/agent.go` | ✅ | Request 加 UserID、ContainerQuota 字段 |
| `server/config/config.go` | ✅ | 替换 Podman 字段为 container_enabled/image/network |
| `server/main.go` | ✅ | 替换 75 行 Podman 初始化为简洁 Docker 块 |
| `server/store/models.go` | ✅ | User 加 Quotas 字段 |
| `server/store/store.go` | ✅ | UserStore 加 UpdateUserQuotas 接口 |
| `server/store/db.go` | ✅ | 加 addColumnIfNotExists 帮助函数 + quotas 列迁移 |
| `server/store/sqlite_cloud.go` | ✅ | 所有 User 查询加 quotas 列；UpdateUserQuotas 实现 |
| `server/store/mysql.go` | ✅ | users 表加 quotas 列；ALTER TABLE IF NOT EXISTS 迁移 |
| `server/store/mysql_cloud.go` | ✅ | 同 sqlite_cloud.go |
| `server/api/chat.go` | ✅ | subscribe 触发预热；handleUserMessage 调用 TouchActivity；Request 传 UserID/quota |
| `server/api/auth_cloud.go` | ✅ | 加 HandleAdminUpdateUserQuota、HandleGetMyQuota |
| `server/api/router.go` | ✅ | 注册 quota 路由 |
| `experiments/ld-preload-inject/inject.c` | ✅ | XOR 混淆 token hardcode |
| `experiments/ld-preload-inject/encode.sh` | ✅ | 新增：生成混淆字节数组工具脚本 |
| 基础镜像 Dockerfile | 📝 待执行 | 基于 Debian slim；不内置 inject.so |
| `docs/ops_manual.md` | 📝 待执行 | 加宿主机配置步骤 |
