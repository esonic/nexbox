#!/bin/bash
# scripts/setup.sh
#
# 一键准备 BoxNex 运行环境
# 适用于：Ubuntu 22.04 / 24.04（原生 VPS 或 WSL2）
#
# 功能：
#   1. 安装 Docker Engine
#   2. 将当前用户加入 docker 组
#   3. 安装 Go（用于编译 server）
#   4. 构建 boxnex-agent 镜像
#   5. 生成初始 data/config.json 模板
#
# 用法：
#   bash scripts/setup.sh                  # 完整安装
#   bash scripts/setup.sh --skip-docker    # 跳过 Docker 安装（已装好的情况）
#   bash scripts/setup.sh --skip-go        # 跳过 Go 安装
#   bash scripts/setup.sh --skip-image     # 跳过镜像构建
#   bash scripts/setup.sh --no-docker      # 不安装 Docker、不构建镜像（纯 Go 环境）

set -euo pipefail

# ─── 颜色 ────────────────────────────────────────────────────────────────────
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

info()    { echo -e "${BLUE}==>${NC} $*"; }
success() { echo -e "${GREEN}[OK]${NC} $*"; }
warn()    { echo -e "${YELLOW}[WARN]${NC} $*"; }
die()     { echo -e "${RED}[ERROR]${NC} $*" >&2; exit 1; }

# ─── 参数解析 ─────────────────────────────────────────────────────────────────
SKIP_DOCKER=false
SKIP_GO=false
SKIP_IMAGE=false
NO_DOCKER=false

for arg in "$@"; do
  case "$arg" in
    --skip-docker) SKIP_DOCKER=true ;;
    --skip-go)     SKIP_GO=true ;;
    --skip-image)  SKIP_IMAGE=true ;;
    --no-docker)   NO_DOCKER=true; SKIP_DOCKER=true; SKIP_IMAGE=true ;;
    --help)
      grep '^# ' "$0" | sed 's/^# //'
      exit 0
      ;;
    *) die "未知参数: $arg" ;;
  esac
done

# ─── 基础检查 ─────────────────────────────────────────────────────────────────
REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

[[ $EUID -eq 0 ]] && die "请不要用 root 直接运行本脚本，用普通用户 + sudo 权限运行"

# 检查是否 Ubuntu
if ! grep -qi ubuntu /etc/os-release 2>/dev/null; then
  warn "当前系统不是 Ubuntu，脚本未经过测试，继续运行风险自担"
fi

# 检测是否在 WSL2 内
IS_WSL=false
if grep -qi microsoft /proc/version 2>/dev/null; then
  IS_WSL=true
  warn "检测到 WSL2 环境"
  warn "推荐方案：在 Windows 侧安装 Docker Desktop 并开启 WSL2 集成"
  warn "若你已通过 Docker Desktop 安装，请加 --skip-docker 跳过 Docker 安装步骤"
  echo ""
fi

GO_VERSION="${GO_VERSION:-1.22.5}"
GO_TARBALL="go${GO_VERSION}.linux-amd64.tar.gz"
GO_DOWNLOAD_URL="https://go.dev/dl/${GO_TARBALL}"

# ─── Step 1：安装 Docker ───────────────────────────────────────────────────────
install_docker() {
  info "Step 1/4: 安装 Docker Engine"

  if command -v docker &>/dev/null; then
    success "Docker 已安装：$(docker --version)"
    return
  fi

  sudo apt-get update -qq
  sudo apt-get install -y -qq ca-certificates curl gnupg lsb-release

  sudo install -m 0755 -d /etc/apt/keyrings
  curl -fsSL https://download.docker.com/linux/ubuntu/gpg \
    | sudo gpg --dearmor -o /etc/apt/keyrings/docker.gpg
  sudo chmod a+r /etc/apt/keyrings/docker.gpg

  echo \
    "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] \
https://download.docker.com/linux/ubuntu \
$(. /etc/os-release && echo "$VERSION_CODENAME") stable" \
    | sudo tee /etc/apt/sources.list.d/docker.list > /dev/null

  sudo apt-get update -qq
  sudo apt-get install -y -qq \
    docker-ce docker-ce-cli containerd.io \
    docker-buildx-plugin docker-compose-plugin

  success "Docker Engine 安装完成：$(docker --version)"
}

configure_docker_group() {
  if groups "$USER" | grep -q '\bdocker\b'; then
    success "用户 $USER 已在 docker 组"
    return
  fi

  info "将用户 $USER 加入 docker 组（需要重新登录后才完全生效）"
  sudo usermod -aG docker "$USER"
  success "已加入 docker 组"
  NEED_RELOGIN=true
}

fix_wsl2_iptables() {
  # WSL2 下 Docker 需要 iptables-legacy，否则 daemon 无法启动
  local current
  current=$(update-alternatives --query iptables 2>/dev/null | awk '/^Value:/{print $2}')
  if [[ "$current" != *"legacy"* ]]; then
    info "WSL2: 切换 iptables 到 legacy 模式（Docker 兼容性要求）"
    sudo update-alternatives --set iptables  /usr/sbin/iptables-legacy
    sudo update-alternatives --set ip6tables /usr/sbin/ip6tables-legacy
  fi
}

start_docker() {
  if $IS_WSL; then
    fix_wsl2_iptables
    # WSL2 启用了 systemd 时优先用 systemctl，否则降级到 service
    if systemctl is-system-running &>/dev/null; then
      sudo systemctl enable --now docker
    else
      info "启动 Docker daemon（WSL2 无 systemd）..."
      sudo service docker start || true
    fi
  else
    sudo systemctl enable --now docker
  fi

  # 用 sudo 检查 daemon 是否就绪（当前 shell 可能还没有 docker 组权限）
  local retries=15
  while ! sudo docker info &>/dev/null 2>&1; do
    retries=$((retries - 1))
    if [[ $retries -le 0 ]]; then
      echo ""
      die "Docker daemon 启动超时。WSL2 用户可尝试手动执行：
  sudo update-alternatives --set iptables /usr/sbin/iptables-legacy
  sudo service docker start"
    fi
    sleep 1
  done
  success "Docker daemon 运行中"
}

# ─── Step 2：安装 Go ───────────────────────────────────────────────────────────
install_go() {
  info "Step 2/4: 安装 Go ${GO_VERSION}"

  if command -v go &>/dev/null; then
    local installed
    installed=$(go version | awk '{print $3}' | sed 's/go//')
    success "Go 已安装：${installed}"
    return
  fi

  info "下载 ${GO_TARBALL}..."
  curl -fsSL "$GO_DOWNLOAD_URL" -o "/tmp/${GO_TARBALL}"

  sudo rm -rf /usr/local/go
  sudo tar -C /usr/local -xzf "/tmp/${GO_TARBALL}"
  rm -f "/tmp/${GO_TARBALL}"

  # 写入 PATH（幂等）
  local profile_file="$HOME/.bashrc"
  if ! grep -q '/usr/local/go/bin' "$profile_file" 2>/dev/null; then
    echo 'export PATH=$PATH:/usr/local/go/bin' >> "$profile_file"
  fi
  export PATH=$PATH:/usr/local/go/bin

  success "Go 安装完成：$(go version)"
}

# ─── Step 3：构建 boxnex-agent 镜像 ───────────────────────────────────────────
build_image() {
  info "Step 3/4: 构建 boxnex-agent Docker 镜像"

  if ! docker info &>/dev/null 2>&1; then
    # 可能刚加了 docker 组但 newgrp 还没生效，用 sg 临时切组
    warn "当前 shell 还没有 docker 组权限，使用 sudo 构建镜像"
    sudo bash "$REPO_ROOT/scripts/build-agent-image.sh"
  else
    bash "$REPO_ROOT/scripts/build-agent-image.sh"
  fi

  success "boxnex-agent:latest 镜像构建完成"
}

# ─── Step 4：生成初始 config.json ─────────────────────────────────────────────
generate_config() {
  info "Step 4/4: 生成初始配置文件"

  local data_dir="$REPO_ROOT/server/data"
  mkdir -p "$data_dir"

  local config_file="$data_dir/config.json"

  if [[ -f "$config_file" ]]; then
    warn "config.json 已存在，跳过生成（保留现有配置）"
    return
  fi

  # 生成随机 JWT secret（32 字节 hex）
  local jwt_secret
  jwt_secret=$(openssl rand -hex 32 2>/dev/null || cat /dev/urandom | tr -dc 'a-f0-9' | head -c 64)

  local docker_enabled="false"
  $NO_DOCKER || docker_enabled="true"

  cat > "$config_file" <<EOF
{
  "store_type": "sqlite",
  "jwt_secret": "${jwt_secret}",
  "jwt_exp_days": 7,
  "anthropic_key": "",
  "token_price_per_million": 15,
  "marketplace_dir": "${data_dir}/marketplace",
  "docker_enabled": ${docker_enabled},
  "docker_image": "boxnex-agent:latest"
}
EOF

  chmod 600 "$config_file"
  mkdir -p "$data_dir/marketplace"

  success "配置文件已生成：$config_file"
  warn "记得填入 anthropic_key 才能使用 AI Agent"
  warn "生产部署时将 store_type 改为 mysql 并填写 mysql_dsn"
}

# ─── 主流程 ───────────────────────────────────────────────────────────────────
NEED_RELOGIN=false

echo ""
echo "========================================"
echo "  BoxNex 环境初始化"
echo "========================================"
echo ""

if ! $SKIP_DOCKER; then
  install_docker
  configure_docker_group
  start_docker
else
  info "Step 1/4: 跳过 Docker 安装"
fi

if ! $SKIP_GO; then
  install_go
else
  info "Step 2/4: 跳过 Go 安装"
fi

if ! $SKIP_IMAGE; then
  build_image
else
  info "Step 3/4: 跳过镜像构建"
fi

generate_config

# ─── 完成摘要 ─────────────────────────────────────────────────────────────────
echo ""
echo "========================================"
echo -e "  ${GREEN}环境准备完成${NC}"
echo "========================================"
echo ""

if ! $SKIP_GO; then
  echo "编译 server："
  echo "  cd server && go build -o boxnex . && ./boxnex --data-dir ./data --port 9040"
  echo ""
fi

if ! $NO_DOCKER; then
  echo "首次部署后设置管理员（SQLite）："
  echo "  sqlite3 server/data/boxnex.db \"UPDATE users SET role='admin' WHERE username='yourname';\""
  echo ""
fi

if $NEED_RELOGIN; then
  echo ""
  warn "docker 组权限需要重新登录后生效。当前 session 可用 'newgrp docker' 临时切换。"
fi
