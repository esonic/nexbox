#!/bin/bash
# scripts/setup.sh
#
# 一键准备 BoxNex 运行环境
# 适用于：Debian 12 / Ubuntu 22.04+（原生 VPS 或 WSL2）
#
# 功能：
#   1. 安装 Podman（rootless 模式，无需 root）
#   2. 安装 Go（用于编译 server）
#   3. 构建 boxnex-agent 镜像
#   4. 生成初始 data/config.json 模板
#
# 用法：
#   bash scripts/setup.sh                  # 完整安装
#   bash scripts/setup.sh --skip-podman    # 跳过 Podman 安装（已装好的情况）
#   bash scripts/setup.sh --skip-go        # 跳过 Go 安装
#   bash scripts/setup.sh --skip-image     # 跳过镜像构建
#   bash scripts/setup.sh --no-podman      # 不安装 Podman、不构建镜像（纯 Go 环境）

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
SKIP_PODMAN=false
SKIP_GO=false
SKIP_IMAGE=false
NO_PODMAN=false

for arg in "$@"; do
  case "$arg" in
    --skip-podman) SKIP_PODMAN=true ;;
    --skip-go)     SKIP_GO=true ;;
    --skip-image)  SKIP_IMAGE=true ;;
    --no-podman)   NO_PODMAN=true; SKIP_PODMAN=true; SKIP_IMAGE=true ;;
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

# 检测是否在 WSL2 内
IS_WSL=false
if grep -qi microsoft /proc/version 2>/dev/null; then
  IS_WSL=true
  warn "检测到 WSL2 环境"
  info "Podman Rootless 在 WSL2 下可直接使用，无需处理 iptables 兼容性问题"
  echo ""
fi

GO_VERSION="${GO_VERSION:-1.26.1}"
GO_TARBALL="go${GO_VERSION}.linux-amd64.tar.gz"
GO_DOWNLOAD_URL="https://go.dev/dl/${GO_TARBALL}"

# ─── Step 1：安装 Podman ───────────────────────────────────────────────────────
install_podman() {
  info "Step 1/4: 安装 Podman（rootless 模式）"

  if command -v podman &>/dev/null; then
    success "Podman 已安装：$(podman --version)"
    return
  fi

  sudo apt-get update -qq
  sudo apt-get install -y -qq podman

  # 确保 newuidmap/newgidmap 可用（rootless 用户命名空间必须）
  sudo apt-get install -y -qq uidmap 2>/dev/null || true

  success "Podman 安装完成：$(podman --version)"
}

verify_podman_rootless() {
  # 验证 rootless 子 UID/GID 已分配（/etc/subuid、/etc/subgid）
  if ! grep -q "^${USER}:" /etc/subuid 2>/dev/null; then
    info "为用户 $USER 分配 subuid/subgid（rootless 必须）"
    sudo usermod --add-subuids 100000-165535 --add-subgids 100000-165535 "$USER" 2>/dev/null \
      || warn "subuid/subgid 分配失败，rootless 容器可能无法启动；请手动执行：
  sudo usermod --add-subuids 100000-165535 --add-subgids 100000-165535 $USER"
  fi

  # 快速冒烟测试
  if podman run --rm alpine echo ok &>/dev/null; then
    success "Podman rootless 验证通过"
  else
    warn "Podman rootless 冒烟测试失败，请检查 subuid/subgid 配置"
  fi
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
  info "Step 3/4: 构建 boxnex-agent 容器镜像"

  bash "$REPO_ROOT/scripts/build-agent-image.sh"

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

  local podman_enabled="false"
  $NO_PODMAN || podman_enabled="true"

  cat > "$config_file" <<EOF
{
  "store_type": "sqlite",
  "jwt_secret": "${jwt_secret}",
  "jwt_exp_days": 7,
  "token_price_per_million": 15,
  "marketplace_dir": "${data_dir}/marketplace",
  "podman_enabled": ${podman_enabled},
  "podman_image": "boxnex-agent:latest",
  "podman_network": "boxnex-egress"
}
EOF

  chmod 600 "$config_file"
  mkdir -p "$data_dir/marketplace"

  success "配置文件已生成：$config_file"
  warn "CLAUDE_CODE_OAUTH_TOKEN 建议通过环境变量注入（systemd Environment= 或 export），不要写入 config.json"
  warn "生产部署时将 store_type 改为 mysql 并填写 mysql_dsn"
}

# ─── 主流程 ───────────────────────────────────────────────────────────────────
echo ""
echo "========================================"
echo "  BoxNex 环境初始化"
echo "========================================"
echo ""

if ! $SKIP_PODMAN; then
  install_podman
  verify_podman_rootless
else
  info "Step 1/4: 跳过 Podman 安装"
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

echo "首次部署后设置管理员（SQLite）："
echo "  sqlite3 server/data/boxnex.db \"UPDATE users SET role='admin' WHERE username='yourname';\""
echo ""

if ! $NO_PODMAN; then
  echo "注入 API Key（推荐方式）："
  echo "  export CLAUDE_CODE_OAUTH_TOKEN=<your-oauth-token>"
  echo "  # 或写入 systemd service: Environment=CLAUDE_CODE_OAUTH_TOKEN=<your-oauth-token>"
  echo ""
fi
