# 容器安全加固方案

> 基于 security_analysis.md 的分析结果，本文档给出具体实施方案。

---

## 一、Docker 推荐启动参数

综合"用户在 workspace 内正常使用"和"不危害宿主/其他用户"两个目标：

```bash
docker run -d \
  --name boxnex-proj-<hash> \
  --rm \

  # ── 资源限制 ──────────────────────────────────────────
  --memory 2g \
  --memory-swap 2g \          # 关闭 swap，防止内存压力蔓延到宿主机
  --cpus 1.0 \
  --pids-limit 200 \

  # ── 磁盘：tmpfs 覆盖容器内临时区域 ───────────────────
  # /workspace 是 bind mount，--storage-opt 对它无效（见正文说明）
  # 用 tmpfs 限制容器内临时文件区域：
  --tmpfs /tmp:size=256m,noexec \
  --tmpfs /root:size=64m \    # Claude CLI session 状态（~/.claude）

  # ── 只读系统层 ────────────────────────────────────────
  # 容器内除 bind mount 和 tmpfs 以外全部只读，
  # 防止 Claude 修改容器自身的系统文件：
  --read-only \

  # ── 网络：只放行 Anthropic API（见第二节）───────────
  --network boxnex-egress \

  # ── 安全加固 ──────────────────────────────────────────
  --cap-drop ALL \
  --cap-add CHOWN \
  --cap-add DAC_OVERRIDE \
  --cap-add FOWNER \
  --cap-add SETUID \
  --cap-add SETGID \
  --security-opt no-new-privileges \

  # ── 挂载 ──────────────────────────────────────────────
  -v <repoPath>:/workspace \
  -w /workspace \
  boxnex-agent:latest \
  tail -f /dev/null
```

### 参数说明

| 参数 | 作用 |
|------|------|
| `--memory 2g --memory-swap 2g` | 限制内存并关闭 swap，防止内存耗尽影响宿主机 |
| `--cpus 1.0` | 限制 CPU，防止挖矿等高强度计算 |
| `--pids-limit 200` | 防止 fork 炸弹 |
| `--tmpfs /tmp:noexec` | 临时目录大小受限，且不可执行二进制（防止写入后执行） |
| `--read-only` | 容器 rootfs 只读，Claude 无法修改系统文件 |
| `--cap-drop ALL` + `--cap-add ...` | 最小权限原则，只保留必要的 Linux Capabilities |
| `--security-opt no-new-privileges` | 禁止进程通过 setuid/setgid 提升权限 |
| `--network boxnex-egress` | 出站流量受限网络（见第二节） |

### 关于 `--storage-opt size=1g`

**此参数对 bind mount 无效**，`-v <repoPath>:/workspace` 挂载的是宿主机目录，不受容器存储驱动配额控制。Claude 下载的文件写入 `/workspace` 不会被这个参数拦截。workspace 的磁盘配额需依靠应用层检查（见第三节）。

---

## 二、网络隔离方案

### 目标

- ✅ 允许：Claude CLI 访问 `api.anthropic.com`（必须，用于调用 API）
- ✅ 允许：DNS 解析（53/udp）
- ❌ 拒绝：访问内网（防止横向渗透）
- ❌ 拒绝：访问任意外网（防止反向 Shell、数据外泄、DDoS 等）

### 方案 A：HTTP 代理 + `--network none`（推荐，无需 iptables 权限）

在宿主机上运行一个只放行 `*.anthropic.com` 的 HTTP 代理，容器完全断网，只能通过代理访问外部。

**1. 使用 tinyproxy 搭建白名单代理**

```bash
# 安装 tinyproxy
apt install tinyproxy  # 或 brew install tinyproxy

# 配置 /etc/tinyproxy/tinyproxy.conf
Port 8888
Listen 172.17.0.1        # Docker 默认网关地址，容器可访问
Allow 127.0.0.1
Allow 172.16.0.0/12      # 允许所有 Docker 子网访问代理

# 白名单：只放行 Anthropic
Filter "/etc/tinyproxy/filter.txt"
FilterURLs On
FilterExtended On
# filter.txt 内容：
#   \.anthropic\.com$

systemctl restart tinyproxy
```

**2. 容器启动参数调整**

```bash
# 将 --network boxnex-egress 替换为：
--network none \
-e HTTPS_PROXY=http://172.17.0.1:8888 \
-e HTTP_PROXY=http://172.17.0.1:8888 \
-e NO_PROXY=localhost,127.0.0.1 \
```

**优点：** 不需要 iptables 权限，在容器化部署（如 k8s）环境中也能用，代理日志可审计所有出站请求。

---

### 方案 B：Docker 专用网络 + iptables 白名单

**1. 创建专用网络（一次性，服务启动时执行）**

```bash
docker network create \
  --driver bridge \
  boxnex-egress
```

**2. iptables 规则（宿主机，需要 root）**

```bash
# 获取 boxnex-egress 对应的网桥名称
BRIDGE=$(docker network inspect boxnex-egress \
  --format '{{.Options}}' | grep -o 'com.docker.network.bridge.name:[^ }]*' \
  | cut -d: -f2)
# 若未自定义，可用：ip link show | grep br-

# 用 ipset 维护 Anthropic IP 白名单（定时更新）
ipset create anthropic-ips hash:net 2>/dev/null || ipset flush anthropic-ips
for ip in $(dig +short api.anthropic.com); do
  ipset add anthropic-ips $ip
done

# 放行 DNS
iptables -I FORWARD -i $BRIDGE -p udp --dport 53 -j ACCEPT
iptables -I FORWARD -i $BRIDGE -p tcp --dport 53 -j ACCEPT

# 放行已建立的连接（返回包）
iptables -I FORWARD -i $BRIDGE -m state --state ESTABLISHED,RELATED -j ACCEPT

# 放行 Anthropic IP
iptables -I FORWARD -i $BRIDGE -m set --match-set anthropic-ips dst -j ACCEPT

# 拒绝其他所有出站
iptables -A FORWARD -i $BRIDGE -j DROP
```

**注意：** Anthropic IP 可能变化，需要 cron job 定时刷新 ipset。

---

### 方案选择建议

| 场景 | 推荐方案 |
|------|---------|
| 普通 Linux 服务器，有 root 权限 | 方案 B（更彻底，规则在内核层生效） |
| 容器内运行 NexBox（无 iptables 权限） | 方案 A（代理方案，无需特权） |
| 不需要 WebSearch/WebFetch 工具 | `--network none`（最简单，无需代理） |

---

## 三、磁盘配额：运行中检查

### 现状

`chat.go:185` 在每次任务**启动前**检查一次磁盘配额。如果 Claude 在任务执行过程中下载大文件，要等到下次发消息时才会被发现。

### 补充方案：StreamCallback 中的周期性检查

在 Agent 的 StreamCallback 里，每处理 N 条输出消息就做一次 `dirSize` 检查，超限则 cancel context：

```go
// chat.go，在 StreamCallback 内
var diskCheckCounter int
callback := func(msg agent.StreamMessage, parsed *agent.AssistantMessage) {
    diskCheckCounter++
    if diskCheckCounter % 50 == 0 {  // 每 50 条消息检查一次
        if _, _, qErr := s.checkDiskQuota(projectWorkDir); qErr != nil {
            s.Hub.Emit(sessionID, ChatEvent{
                Type:    "error",
                Message: "disk_quota_exceeded: " + qErr.Error(),
            })
            cancel()  // 终止 Agent 任务
            return
        }
    }
    // ... 原有处理逻辑
}
```

**`dirSize` 的性能说明：** 遍历目录树有 I/O 开销，每 50 条消息触发一次在实际场景中频率约为每隔数秒，可以接受。如果目录很大可适当调大间隔（如 100 次）。

---

## 四、实施优先级

### P0（上线前必须完成）

- [ ] 实现网络隔离（方案 A 或 B 二选一）
- [ ] 更新 `container.go` 的 `docker run` 参数（`--cap-drop ALL`、`--read-only`、`--tmpfs` 等）

### P1（上线后尽快补齐）

- [ ] StreamCallback 中加磁盘配额周期性检查
- [ ] 验证 `--read-only` 下 Claude CLI 是否正常工作（重点检查 session 文件写入路径）
- [ ] 如选方案 B，设置 cron job 定时刷新 ipset（Anthropic IP 会变化）

### P2（中期优化）

- [ ] 评估是否使用非 root 用户运行容器（需要 workspace 目录权限配合）
- [ ] 配置 seccomp profile，禁用 `ptrace`、`mount` 等高危 syscall
- [ ] 将空闲容器回收时间从 1 小时缩短至 15-30 分钟
