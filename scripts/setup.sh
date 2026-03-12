#!/bin/bash
# scripts/setup.sh
#
# 一键准备 BoxNex 运行环境
# 适用于：Debian 12 / Ubuntu 22.04+（原生 VPS 或 WSL2）
#
# 功能：
#   1. 检测 Docker Engine（提示安装方式）
#   2. 检测 Sysbox runtime（提示安装方式）
#   3. 安装 Go（用于编译 server）
#   4. 构建 boxnex-agent 镜像
#   5. 生成初始 data/config.json 模板
#
# 用法：
#   bash scripts/setup.sh                  # 完整流程
#   bash scripts/setup.sh --skip-go        # 跳过 Go 安装
#   bash scripts/setup.sh --skip-image     # 跳过镜像构建
#   bash scripts/setup.sh --no-docker      # 跳过 Docker/镜像步骤（不启用容器隔离）

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
SKIP_GO=false
SKIP_IMAGE=false
NO_DOCKER=false

for arg in "$@"; do
  case "$arg" in
    --skip-go)     SKIP_GO=true ;;
    --skip-image)  SKIP_IMAGE=true ;;
    --no-docker)   NO_DOCKER=true; SKIP_IMAGE=true ;;
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

GO_VERSION="${GO_VERSION:-1.26.1}"
GO_TARBALL="go${GO_VERSION}.linux-amd64.tar.gz"
GO_DOWNLOAD_URL="https://go.dev/dl/${GO_TARBALL}"

# ─── Step 1：检测 Docker ───────────────────────────────────────────────────────
check_docker() {
  info "Step 1/5: 检测 Docker Engine"

  if ! command -v docker &>/dev/null; then
    warn "Docker 未安装。安装方式："
    warn "  curl -fsSL https://get.docker.com | sh"
    warn "  sudo usermod -aG docker \$USER && newgrp docker"
    warn "安装完成后重新运行本脚本。"
    return 1
  fi

  if ! docker info &>/dev/null; then
    warn "Docker daemon 未运行或当前用户无权限。检查："
    warn "  sudo systemctl start docker"
    warn "  sudo usermod -aG docker \$USER && newgrp docker"
    return 1
  fi

  success "Docker $(docker version --format '{{.Client.Version}}' 2>/dev/null || echo '?') 已就绪"
  return 0
}

# ─── Step 2：检测 Sysbox ──────────────────────────────────────────────────────
check_sysbox() {
  info "Step 2/5: 检测 Sysbox runtime"

  if docker info 2>/dev/null | grep -q sysbox; then
    success "sysbox-runc 已注册为 Docker runtime"
    return 0
  fi

  warn "Sysbox 未安装或未注册。安装方式："
  warn "  # 下载对应发行版的 .deb（https://github.com/nestybox/sysbox/releases）"
  warn "  wget https://downloads.nestybox.com/sysbox/releases/v0.6.4/sysbox-ce_0.6.4-0.linux_amd64.deb"
  warn "  sudo apt-get install -y ./sysbox-ce_0.6.4-0.linux_amd64.deb"
  warn ""
  warn "安装完成后 Sysbox 会自动注册到 Docker，无需重启 daemon。"
  warn "继续：容器将以默认 runc 启动（无 DinD，安全隔离降级）"
  return 0  # 非致命，继续流程
}

# ─── Step 3：安装 Go ───────────────────────────────────────────────────────────
install_go() {
  info "Step 3/5: 安装 Go ${GO_VERSION}"

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

# ─── Step 4：构建 boxnex-agent 镜像 ───────────────────────────────────────────
build_image() {
  info "Step 4/5: 构建 boxnex-agent 容器镜像"

  bash "$REPO_ROOT/scripts/build-agent-image.sh"

  success "boxnex-agent:latest 镜像构建完成"
}

# ─── Step 5：生成初始 config.json ─────────────────────────────────────────────
generate_config() {
  info "Step 5/5: 生成初始配置文件"

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

  local container_enabled="false"
  $NO_DOCKER || container_enabled="true"

  cat > "$config_file" <<EOF
{
  "store_type": "sqlite",
  "jwt_secret": "${jwt_secret}",
  "jwt_exp_days": 7,
  "token_price_per_million": 15,
  "marketplace_dir": "${data_dir}/marketplace",
  "container_enabled": ${container_enabled},
  "container_image": "boxnex-agent:latest",
  "container_network": "boxnex-net"
}
EOF

  chmod 600 "$config_file"
  mkdir -p "$data_dir/marketplace"

  success "配置文件已生成：$config_file"
  warn "生产部署时将 store_type 改为 mysql 并填写 mysql_dsn"
}

# ─── 主流程 ───────────────────────────────────────────────────────────────────
echo ""
echo "========================================"
echo "  BoxNex 环境初始化"
echo "========================================"
echo ""

DOCKER_OK=false
if ! $NO_DOCKER; then
  check_docker && DOCKER_OK=true || true
  check_sysbox
else
  info "Step 1/5: 跳过 Docker 检测（--no-docker）"
  info "Step 2/5: 跳过 Sysbox 检测（--no-docker）"
fi

if ! $SKIP_GO; then
  install_go
else
  info "Step 3/5: 跳过 Go 安装"
fi

if ! $SKIP_IMAGE && $DOCKER_OK; then
  build_image
elif $SKIP_IMAGE; then
  info "Step 4/5: 跳过镜像构建"
else
  warn "Step 4/5: Docker 不可用，跳过镜像构建"
fi

generate_config

# ─── 完成摘要 ─────────────────────────────────────────────────────────────────
echo ""
echo "========================================"
echo -e "  ${GREEN}环境准备完成${NC}"
echo "========================================"
echo ""

echo "编译并启动："
echo "  ./build.sh"
echo "  target/boxnex --data-dir server/data --port 9040"
echo ""

echo "首次部署后设置管理员（SQLite）："
echo "  sqlite3 server/data/boxnex.db \"UPDATE users SET role='admin' WHERE username='yourname';\""
echo ""

if ! $NO_DOCKER; then
  echo "部署 Claude OAuth Token（inject.so 方式，token 不入库）："
  echo "  sudo apt-get install -y build-essential   # 如未安装 gcc"
  echo "  sudo inject/rotate.sh <your_claude_oauth_token>"
  echo ""
fi
