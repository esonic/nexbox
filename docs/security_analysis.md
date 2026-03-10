# NexBox 容器安全性分析报告

> 分析日期：2026-03-10
> 分析范围：`server/agent/container.go`、`server/agent/claude_cli.go`、`server/api/files.go`、`server/api/router.go`、`Dockerfile.agent`

---

## 一、现状总览

NexBox 的多用户隔离核心依赖 Docker 容器：每个 Project 对应一个持久容器，Agent（Claude CLI）通过 `docker exec` 在其中执行。服务端运行在宿主机，通过 Docker CLI 管理容器生命周期。

### 1.1 当前实际启动参数（`container.go:104-116`）

```bash
docker run -d \
  --name boxnex-proj-<hash> \
  --rm \
  --memory 4g \
  --cpus 1.0 \
  --pids-limit 200 \
  --storage-opt size=1g \
  -v <repoPath>:/workspace \
  -w /workspace \
  boxnex-agent:latest \
  tail -f /dev/null
```

### 1.2 已实现的安全措施

| 措施 | 状态 | 位置 |
|------|------|------|
| 内存限制 4GB | ✅ 已实现 | `container.go:108` |
| CPU 限制 1.0 核 | ✅ 已实现 | `container.go:109` |
| PID 限制 200 | ✅ 已实现 | `container.go:110` |
| 存储限制 1GB (`--storage-opt`) | ✅ 已实现（有效性存疑，见下文） | `container.go:111` |
| 每 project 独立挂载目录 | ✅ 已实现 | `container.go:112` |
| 不挂载宿主机 config / 凭证目录 | ✅ 已实现 | — |
| API 层 JWT 鉴权 + project 归属校验 | ✅ 已实现 | `router.go`, `files.go` |
| 文件路径安全校验（symlink 防穿越） | ✅ 已实现 | `files.go:resolveSecurePath` |
| 上传文件大小限制（10MB） | ✅ 已实现 | `files.go:UploadProjectFile` |
| 下载文件大小限制（50MB） | ✅ 已实现 | `files.go:DownloadProjectFile` |
| 磁盘配额（256MB，云模式） | ✅ 已实现（有缺口，见下文） | `files.go:checkDiskQuota` |
| 空闲容器回收（1h） | ✅ 已实现 | `main.go:392-403` |
| IP 级 API 请求限速 | ✅ 已实现 | `router.go:newIPRateLimiter` |

---

## 二、关键安全缺口

### 🔴 2.1 网络隔离：完全缺失（最高优先级）

**现状：** 容器启动时没有指定 `--network`，使用 Docker 默认桥接网络，容器可以：
- 访问任意外网 IP 和域名
- 访问宿主机内网（通过 Docker 默认网关 172.17.0.1 等）
- 发起反向 Shell 连接
- 进行端口扫描、DDoS 等攻击

**规划文档（`cloud_tech.md:6.4`）中已有方案但未实现：**
```bash
--network boxnex-egress   # 专用出站受限网络
# iptables 规则只放行 api.anthropic.com + DNS
```

**影响：** 这是当前最严重的安全问题。用户可以通过 Claude 的 Bash 工具发起任意出站连接，危害其他系统、泄露数据、充当跳板机。

---

### 🔴 2.2 磁盘配额对 Agent 写操作无效

**现状：** 磁盘配额（`checkDiskQuota`）只在以下两个卡口检查：
1. 用户通过 API **上传文件**时（`files.go:UploadProjectFile`）
2. 文件上传入口

**未覆盖的场景：** Claude 在容器内通过 Bash 工具执行 `curl`、`wget`、`git clone`、`npm install` 等命令写入 `/workspace`，完全绕过磁盘配额检查。

**`--storage-opt size=1g` 的局限性：**
- 该参数限制的是容器自身 overlay 文件系统的大小
- `/workspace` 是宿主机 bind mount（`-v repoPath:/workspace`），**不受 `--storage-opt` 约束**
- 实际效果：Claude 可以向 `/workspace` 写入远超 1GB 的数据

**攻击场景：**
```
用户：请帮我下载这个数据集（指向一个 100GB 文件的链接）
Claude：好的，执行 wget https://...（写入 /workspace）
结果：宿主机磁盘被撑满，影响所有用户
```

---

### 🔴 2.3 容器以 root 身份运行

**现状：** 容器内没有指定运行用户（无 `--user` 参数），Claude 进程以 root 身份运行。

**风险：**
- 一旦发生容器逃逸漏洞，攻击者直接获得宿主机 root 权限
- 容器内写入的文件（包括 `/workspace` bind mount 上的文件）归属于 root，宿主机上可能产生只有 root 能清理的遗留文件

**缓解方向：** 在 Dockerfile 创建非 root 用户，或使用 `--user uid:gid` 启动；配合 User Namespace Remapping。

---

### 🟠 2.4 Linux Capabilities 未收缩

**现状：** 容器使用 Docker 默认 Capabilities 集合，包含大量非必要权限：
- `CAP_NET_RAW`：可以构造原始网络包（配合 ping 进行扫描）
- `CAP_SYS_CHROOT`：目录沙箱逃逸辅助
- `CAP_AUDIT_WRITE`：写内核审计日志
- `CAP_SETUID` / `CAP_SETGID`：提权前提条件

**应添加：**
```bash
--cap-drop ALL \
--cap-add CHOWN \          # 文件归属操作（如 git）
--cap-add SETUID \         # 某些脚本需要
--cap-add SETGID \
--cap-add DAC_OVERRIDE \   # 文件读写
```

---

### 🟠 2.5 无 seccomp / AppArmor 配置

**现状：** 未配置任何 seccomp 过滤规则，容器可以调用所有 Linux syscall，包括：
- `ptrace`（注入其他进程）
- `mount`（挂载文件系统）
- `clone` with `CLONE_NEWUSER`（尝试创建用户命名空间提权）
- `keyctl`（访问内核 keyring）

Docker 有一套默认 seccomp profile，但没有使用专为 AI agent 场景定制的更严格规则。

---

### 🟠 2.6 工具权限过于宽泛

**现状（`claude_cli.go:578-582`）：**
```go
// Non-interactive tools: auto-allow (same as bypassPermissions)
c.sendControlSuccess(writeStdin, requestID, map[string]interface{}{
    "behavior":     "allow",
    "updatedInput": req.Input,
})
```

所有非交互类工具（包括 Bash、文件读写、网络访问工具）均自动允许，无需用户确认。这意味着：
- 用户可以通过 Claude 执行任意 shell 命令
- Claude 自主决策下载、删除文件，用户没有二次确认机会

当前启动参数：`--tools default`（使用默认工具集），未限制具体工具。

---

### 🟡 2.7 容器间网络隔离不完整

**现状：** 所有 project 容器都在同一个 Docker 默认桥接网络，容器之间可以互相通信（通过容器 IP 或容器名）。

**风险：** 恶意用户的容器可以向其他用户的容器发起攻击（端口扫描、协议利用等）。

---

### 🟡 2.8 `--storage-opt` 并非在所有环境下生效

**现状：** `--storage-opt size=1g` 仅在 overlay2 + project quota 已启用的环境下生效，默认 Docker 安装通常不支持此选项，会静默忽略。

**验证方式：** `docker info | grep "Storage Driver"`；若未看到 `xfs` 或未启用 project quota，该参数不生效。

---

### 🟡 2.9 磁盘配额只在云模式生效

```go
// files.go:47-49
if s.Config.Get().JWTSecret == "" {
    return 0, 0, nil  // 本地模式不检查配额
}
```

本地模式（`disable_cloud=true`）完全不检查磁盘配额。如果这套系统以本地部署方式提供给多用户使用，配额保护失效。

---

### 🟡 2.10 空闲容器回收时间较长

**现状：** 容器空闲 1 小时后才被回收（`main.go:392`），检测间隔 10 分钟。

若用户在容器内启动了后台进程（如持续下载），即使 Claude 任务结束，容器内的进程仍会持续运行长达 1 小时（无法通过 Agent 活动检测到）。

---

## 三、对比设计文档与实际代码的差距

| 功能 | 设计文档（cloud_tech.md） | 实际代码（container.go） | 差距 |
|------|--------------------------|--------------------------|------|
| 内存限制 | 1g | 4g | 配置值不同（4g 更宽松） |
| PID 限制 | 100 | 200 | 配置值不同（200 更宽松） |
| 网络隔离 | `--network boxnex-egress` | 无 | **完全未实现** |
| claude 会话挂载 | 挂载 `~/.claude` | 无 | 未实现（影响 session 持久化） |
| 用户命名空间 | 未提及 | 无 | 未实现 |

---

## 四、威胁场景汇总

| 威胁场景 | 当前防御 | 能否阻止 |
|----------|----------|---------|
| Claude 下载超大文件撑满磁盘 | --storage-opt（对 bind mount 无效） | ❌ 无法阻止 |
| Claude 发起反向 Shell 连接 | 无网络限制 | ❌ 无法阻止 |
| Claude 扫描内网服务 | 无网络限制 | ❌ 无法阻止 |
| Claude fork 炸弹 | --pids-limit 200 | ✅ 能阻止 |
| Claude 内存耗尽 | --memory 4g | ⚠️ 有限（单用户 4g 消耗仍大） |
| Claude 挖矿 | --cpus 1.0 + 空闲回收 | ⚠️ 部分缓解（低效挖矿仍可进行） |
| 用户 A 读取用户 B 的文件 | 独立 bind mount + project 归属校验 | ✅ 能阻止 |
| 路径穿越访问宿主机文件 | EvalSymlinks 校验 | ✅ 能阻止（API 层） |
| Claude 读取宿主机 config/token | config 目录不挂载 | ✅ 能阻止 |
| 容器逃逸后提权 | 无（容器以 root 运行） | ❌ 无防御 |
| 用户绕过余额检查（并发） | 原子预占设计（若已实现） | ⚠️ 取决于实现 |
| 容器间互相攻击 | 无网络隔离 | ❌ 无法阻止 |

---

## 五、优先级排序与建议

### P0（上线前必须解决）

#### 5.1 实现网络出站限制

最小方案：启动容器时加 `--network none`（完全断网），如果不需要 Claude 的 WebSearch/Fetch 工具。

进阶方案：按设计文档创建 `boxnex-egress` Docker 网络，配合 iptables 白名单只放行 Anthropic API。

#### 5.2 对 Agent 写磁盘行为建立有效限制

选项 A（推荐）：定期后台检测 `/workspace` 大小，超额时强制停止容器。
选项 B：使用 cgroups v2 blkio throttle 限制写速率（实现复杂）。
选项 C：将 `checkDiskQuota` 扩展为 Agent 任务启动前也检查，且任务运行过程中定期轮询。

#### 5.3 Agent 任务执行超时

确认每次 `docker exec` 都传入了有效的超时 context（设计文档 6.7 提到但需验证实现），防止任务永久运行。

---

### P1（上线后尽快补齐）

#### 5.4 容器以非 root 用户运行

在 `Dockerfile.agent` 中创建专用用户：
```dockerfile
RUN adduser -D -u 1000 agent
USER agent
```
同时在 `docker run` 传入 `--user 1000:1000`，宿主机 bind mount 目录也需要相应权限设置。

#### 5.5 收缩 Linux Capabilities

```bash
--cap-drop ALL \
--cap-add CHOWN \
--cap-add DAC_OVERRIDE \
--cap-add FOWNER \
--cap-add SETUID \
--cap-add SETGID
```
在测试环境验证 Claude 基本功能（git、bash、文件操作）是否正常。

#### 5.6 磁盘配额扩展到本地模式

去掉 `checkDiskQuota` 中对 `JWTSecret` 的判断，本地多用户部署也应有配额保护。

---

### P2（中期优化）

#### 5.7 容器间网络隔离

每个 project 容器使用独立 Docker 网络，或在 `boxnex-egress` 网络上禁用容器间通信（`--icc=false`）。

#### 5.8 seccomp 策略

应用 Docker 默认 seccomp profile 或更严格的自定义 profile，至少禁用 `ptrace`、`mount`、`keyctl` 等高危 syscall。

#### 5.9 缩短空闲回收时间

将 1 小时空闲阈值调整为 15-30 分钟，减少资源滥用窗口。

#### 5.10 验证 `--storage-opt` 是否生效

在部署环境上确认 overlay2 + project quota，否则该参数无效需寻找替代方案。

---

## 六、现有安全措施的正面评价

以下已有设计值得保留和维护：
- **路径安全校验**（`filepath.EvalSymlinks` + 前缀校验）实现完整，防止 symlink 攻击和路径穿越
- **项目归属校验**在所有文件操作 API 中均有实现，用户间隔离良好
- **空闲容器回收**机制避免长期无用容器占用资源
- **宿主机 config 目录不挂载**，容器内无法读取 JWT secret、GitLab token 等凭证
- **PID 限制**有效防止 fork 炸弹
- **容器名称哈希化**，避免 projectID 中的特殊字符导致命令注入

---

## 七、结论

当前最紧迫的安全问题是**网络隔离缺失**和**Agent 写磁盘无有效限额**这两项。两者都可以通过相对小的改动解决，建议在正式对外提供服务前完成。容器以 root 运行、Capabilities 未收缩等问题会在发生容器逃逸时显著放大危害，应在 P1 阶段补齐。
