# 容器安全加固任务列表

> 基于 `security_analysis.md` 和 `container_security_plan.md` 的综合安全方案
> 运行时决策：**Podman Rootless**（替代 Docker rootful）
> 生产环境：Debian

---

## 运行时迁移：Docker → Podman Rootless

在实施所有安全措施之前，先完成运行时切换。Podman Rootless 在架构上消除了 Docker rootful daemon 的根本性风险（root socket = 宿主机 root 入口）。

### M-01 安装与配置 Podman Rootless

- Debian 上安装 `podman`、`slirp4netns`/`pasta`、`uidmap`
- 为 BoxNex server 的运行用户（如 `boxnex`）配置 `/etc/subuid` 和 `/etc/subgid`
- 验证 `podman info` 中 rootless 模式已启用，user namespace 正常
- 配置 `containers.conf` 和 `storage.conf`，指定镜像存储路径

### M-02 迁移 Go 代码的容器管理接口

- 将 `docker run`/`docker exec`/`docker stop` 等调用切换为 Podman 等价实现
- 选择接入方式（二选一）：
  - **方案 A（推荐）**：Podman REST API socket（`$XDG_RUNTIME_DIR/podman/podman.sock`），兼容 Docker API，可复用现有 SDK 代码
  - **方案 B**：`github.com/containers/podman/v4/pkg/bindings`
- 更新容器名称、网络名称等相关常量
- 完整回归测试：容器启动、exec、停止、清理流程

### M-03 迁移 `Dockerfile.agent` 为 Podman 兼容镜像

- 将 `Dockerfile.agent` 改名为 `Containerfile`（Podman 惯例，向下兼容）
- 确认 `FROM` 基础镜像在 rootless 环境下运行正常
- 用 `podman build` 替代 `docker build` 验证构建

---

## P0 — 上线前必须完成

### 网络隔离

#### N-01 在 Go Server 内嵌 HTTP/HTTPS 白名单代理

- 引入 `github.com/elazarl/goproxy` 库
- Proxy 监听在容器专用网络的网关 IP + 固定端口（如 `:8118`）
- 实现域名白名单逻辑：只放行 `api.anthropic.com`，其余一律返回 403
- **阻断 CONNECT 方法**到非白名单域名，防止 HTTPS 隧道绕过
- Proxy 与 BoxNex server 同进程启动和关闭，无需独立部署
- 记录每次出站请求日志（projectID、目标域名、时间戳、响应状态）

#### N-02 创建专用容器网络

- 创建隔离的 Podman 网络（如 `boxnex-egress`），不使用默认 `podman` 网络
- 所有 agent 容器接入此网络
- 确认网关 IP 与 Proxy 监听地址一致
- 在网络层配置 iptables/nftables：只允许容器访问 Proxy 端口，拒绝其他出站连接

#### N-03 容器启动时注入代理环境变量

```
HTTP_PROXY=http://<gateway>:8118
HTTPS_PROXY=http://<gateway>:8118
NO_PROXY=localhost,127.0.0.1
```

- 确认 Claude CLI、curl、wget、npm、pip 等工具均遵守上述环境变量

#### N-04 验证代理隔离效果

- 容器内执行 `curl https://google.com`，确认被拒绝
- 容器内执行 `curl https://api.anthropic.com`，确认正常通过
- 容器内执行 `curl https://evil.com`（CONNECT 方式），确认被拒绝
- 验证容器无法直接访问宿主机其他端口

---

### API Token 保护

#### T-01 移除容器内的真实 ANTHROPIC_API_KEY

- 容器启动时不再注入真实 API Key
- 改为注入**临时 session token**（UUID，服务器生成）

#### T-02 在 goproxy 中实现 API Key 中继

- 拦截所有目标为 `api.anthropic.com` 的请求
- 验证请求中的 session token 合法且与 projectID 绑定
- 将 `Authorization` header 替换为真实 `ANTHROPIC_API_KEY`（存于 server 内存，不落盘）
- 转发请求到 Anthropic，返回响应

#### T-03 Session Token 生命周期管理

- session token 在容器启动时创建，与 projectID 绑定存入内存
- 容器停止/销毁时立即从内存中删除，token 失效
- Token 不可复用，每次容器启动生成新 token

---

### 磁盘配额

#### D-01 后台 Goroutine 定时监控 workspace 大小

- agent 任务启动时同步启动监控 goroutine，与 agent context 绑定
- 每 10 秒执行一次 `du -sb /workspace`（利用内核 VFS 缓存，性能可接受）
- 超过配额阈值时调用 `cancel()` 终止 agent context，向前端发送超限错误事件
- goroutine 在 agent 结束时自动退出，无泄漏

#### D-02 磁盘配额覆盖本地模式

- 移除 `checkDiskQuota` 中对 `JWTSecret` 的判断
- 本地模式（`disable_cloud=true`）同样执行配额检查，使用独立配置项指定本地配额上限

#### D-03 验证 `--storage-opt size=1g` 是否生效

- 在部署环境执行 `podman info | grep "Storage Driver"`
- 若未启用 overlay2 + project quota，该参数静默失效，需依赖 D-01 兜底
- 记录验证结果到运维文档

---

### 容器安全参数

#### C-01 添加 `--security-opt no-new-privileges`

- 零成本，阻止容器内通过 setuid 可执行文件提权
- 即使存在 capabilities 也受此约束

#### C-02 收缩 Linux Capabilities

```bash
--cap-drop ALL \
--cap-add CHOWN \
--cap-add DAC_OVERRIDE \
--cap-add FOWNER \
--cap-add SETUID \
--cap-add SETGID
```

- 先以 `--cap-drop ALL` 不加任何 cap 测试 Claude 基本功能
- 按实际失败的操作逐一追加所需 cap，保持最小集合

#### C-03 添加 tmpfs 挂载

```bash
--tmpfs /tmp:size=256m,noexec,nosuid
```

#### C-04 关闭 swap

```bash
--memory-swap <同 --memory 值>
```

防止内存耗尽时进程通过 swap 绕过内存限制。

#### C-05 确认 `docker exec` 超时 context

- 检查所有 `podman exec` 调用均传入有效的超时 context
- 防止单个命令永久阻塞 agent

---

## P1 — 上线后尽快补齐

### 非 root 用户

#### U-01 Containerfile 中创建专用非 root 用户

```dockerfile
RUN adduser --disabled-password --uid 1000 agent
USER agent
```

#### U-02 容器启动加 `--user 1000:1000`

- Podman rootless 下，容器内 uid 1000 映射到宿主机的 sub-uid 范围内的无特权 uid
- 即使容器逃逸，宿主机真实权限极低

#### U-03 宿主机 repoPath 目录权限配置

- 所有 `repoPath` 目录由 BoxNex service 账号（uid=1000）创建
- 目录权限设为 `750`，确保容器内 agent 用户可读写

#### U-04 `podman exec` 同步指定 `--user 1000:1000`

- 确保 exec 进程身份与容器主进程一致，不以 root 运行

---

### Claude Session 持久化

#### S-01 单独 bind mount `~/.claude`

- 将 `~/.claude` 从 tmpfs（`/root`）中分离，改为独立的宿主机 bind mount
- 路径示例：`<projectDataPath>/.claude:/home/agent/.claude`
- 保证 Claude session 跨容器重启保留

#### S-02 为 `~/.claude` 设置独立磁盘配额

- 在 D-01 的 goroutine 中同时监控 `.claude` 目录大小
- 建议上限：100MB

---

### Capabilities 验证

#### C-06 在 staging 环境完整验证最小 cap 集

- 测试项目：`git clone`、`npm install`、`pip install`、Bash 脚本、文件读写
- 记录实际需要的 cap 列表，固化到容器启动参数

---

## P2 — 中期优化

### 容器间隔离

#### I-01 禁用容器间通信

- 在 `boxnex-egress` 网络配置中设置 `options.isolate=true`（Podman 网络选项）
- 或通过 iptables/nftables 规则禁止同网段容器互访

#### I-02（可选）每个 project 独立网络

- 每个 project 容器使用独立的 Podman 网络，彻底隔离
- 权衡：网络数量随 project 线性增长，需要生命周期管理

---

### seccomp

#### SC-01 确认默认 seccomp profile 生效

- 确保容器启动未传 `--security-opt seccomp=unconfined`
- Podman 默认 seccomp profile 已屏蔽 `ptrace`、`kexec_load` 等高危 syscall

#### SC-02 自定义 seccomp profile，额外禁用

- `mount` / `umount`
- `clone` 带 `CLONE_NEWUSER` 标志
- `keyctl`
- `bpf`
- `ptrace`

将 profile 文件存入代码仓库，容器启动时加载。

---

### 空闲回收优化

#### R-01 缩短空闲容器回收阈值

- 从当前 1 小时缩短至 **15-20 分钟**
- 减少后台进程（如持续下载）的存活窗口

#### R-02 双重超时保障

- **Agent 层**：单次 Claude 任务最大执行时长（如 30 分钟），超时强制终止
- **容器层**：空闲检测兜底，应对 agent 进程崩溃导致 context cancel 未传播的场景

---

### 代理增强

#### N-05 按 project 实现 API 调用速率限制

- 在 goproxy 中按 projectID 统计 Anthropic API 调用频率
- 超过阈值时返回 429，防止单个 project 耗尽全局 API quota

#### N-06 代理日志接入审计系统

- 出站请求日志（projectID、URL、时间、状态码）写入结构化日志
- 为异常检测（如频繁访问非白名单域名的尝试）提供数据基础

---

## 附：Podman Rootless 的安全收益说明

| 风险 | Docker Rootful | Podman Rootless |
|------|---------------|----------------|
| Daemon socket 被利用 = 宿主机 root | 存在（`/var/run/docker.sock`）| 不存在（无 daemon）|
| 容器逃逸后宿主机权限 | root | 受限普通用户（sub-uid 映射）|
| 用户 namespace remapping | 需手动配置 `userns-remap` | 默认启用 |
| 单点故障 | daemon 挂掉影响所有容器管理 | 每容器独立进程，互不影响 |
| 常驻内存开销 | ~80MB（dockerd + containerd）| ~0MB（无 daemon）|
