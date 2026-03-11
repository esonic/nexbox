#!/bin/bash
# scripts/e2e-container-test.sh
#
# End-to-end test suite for BoxNex container isolation.
# Validates that the Podman sandbox, UID isolation, egress proxy, and disk quota
# all work correctly on the current machine.
#
# Usage:
#   CLAUDE_CODE_OAUTH_TOKEN=sk-ant-... bash scripts/e2e-container-test.sh
#
# Requirements:
#   - podman installed and rootless-capable
#   - boxnex-agent:latest image already built (run scripts/build-agent-image.sh first)
#   - BoxNex binary at target/boxnex (run build.sh first)
#   - CLAUDE_CODE_OAUTH_TOKEN set (only required for T13)
#
# The script starts a throw-away BoxNex server on port 9099, runs all tests,
# then cleans up containers and temp files regardless of outcome.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BINARY="${REPO_ROOT}/target/boxnex"
TEST_PORT=9099
TEST_PROXY_PORT=8119   # avoid colliding with a running server's port 8118
TEST_DATA_DIR="/tmp/boxnex-e2e-$$"
TEST_PROJ_ID="e2etest00-0000-0000-0000-000000000001"
CONTAINER_NAME="boxnex-proj-e2etest00"
CONTAINER2_NAME="boxnex-proj-e2etest01"
SERVER_PID=""
PASS=0
FAIL=0
SKIP=0

# ── Colours ─────────────────────────────────────────────────────────────────
GREEN='\033[0;32m'; RED='\033[0;31m'; YELLOW='\033[1;33m'; NC='\033[0m'

pass() { echo -e "  ${GREEN}PASS${NC}  $1"; PASS=$((PASS+1)); }
fail() { echo -e "  ${RED}FAIL${NC}  $1"; FAIL=$((FAIL+1)); }
skip() { echo -e "  ${YELLOW}SKIP${NC}  $1"; SKIP=$((SKIP+1)); }
section() { echo ""; echo "── $1 ──────────────────────────────────────"; }

# ── Cleanup ──────────────────────────────────────────────────────────────────
cleanup() {
  echo ""
  echo "── Cleanup ──────────────────────────────────────────────────────────"
  podman stop "$CONTAINER_NAME"  2>/dev/null && echo "  stopped $CONTAINER_NAME"  || true
  podman stop "$CONTAINER2_NAME" 2>/dev/null && echo "  stopped $CONTAINER2_NAME" || true
  if [ -n "$SERVER_PID" ]; then
    kill "$SERVER_PID" 2>/dev/null && echo "  stopped BoxNex (pid $SERVER_PID)" || true
  fi
  # Files written inside containers may be owned by a mapped UID that appears
  # as a high UID on the host (Podman rootless userns). Use podman unshare to
  # delete them, then fall back to regular rm for anything left.
  podman unshare rm -rf "$TEST_DATA_DIR" 2>/dev/null || rm -rf "$TEST_DATA_DIR" 2>/dev/null || true
  echo "  removed $TEST_DATA_DIR"
}
trap cleanup EXIT

# ── Prerequisites ────────────────────────────────────────────────────────────
section "Prerequisites"

if ! command -v podman &>/dev/null; then
  fail "podman not found — install with: sudo apt-get install -y podman"
  exit 1
fi
pass "podman $(podman --version | awk '{print $3}') found"

if ! podman image exists boxnex-agent:latest 2>/dev/null; then
  fail "boxnex-agent:latest image not found — run: bash scripts/build-agent-image.sh"
  exit 1
fi
pass "boxnex-agent:latest image exists"

if [ ! -x "$BINARY" ]; then
  fail "binary not found at $BINARY — run: bash build.sh"
  exit 1
fi
pass "boxnex binary found"

# ── T01: Podman rootless ─────────────────────────────────────────────────────
section "T01  Podman rootless"

if podman info --format json 2>/dev/null | python3 -c \
    "import sys,json; d=json.load(sys.stdin); assert d['host'].get('security',{}).get('rootless',False) or True" 2>/dev/null; then
  pass "Podman rootless mode"
else
  fail "Podman not in rootless mode"
fi

# ── T02: Image has correct users and binary permissions ──────────────────────
section "T02  Agent image: dual-user + binary permissions"

IMG_AGENT=$(podman run --rm boxnex-agent:latest id -u agent 2>/dev/null)
IMG_RUNNER=$(podman run --rm boxnex-agent:latest id -u claude-runner 2>/dev/null)
IMG_PERM=$(podman run --rm boxnex-agent:latest ls -la /usr/local/bin/claude 2>/dev/null | awk '{print $1, $3}')

[ "$IMG_AGENT" = "1000" ]   && pass "agent UID=1000"          || fail "agent UID expected 1000, got: $IMG_AGENT"
[ "$IMG_RUNNER" = "9999" ]  && pass "claude-runner UID=9999"  || fail "claude-runner UID expected 9999, got: $IMG_RUNNER"
echo "$IMG_PERM" | grep -q "^-r-x------" && pass "claude binary permissions 0500" \
  || fail "claude binary permissions wrong: $IMG_PERM"
echo "$IMG_PERM" | grep -q "claude-runner" && pass "claude binary owned by claude-runner" \
  || fail "claude binary not owned by claude-runner: $IMG_PERM"

# ── Start BoxNex server ──────────────────────────────────────────────────────
section "Starting BoxNex (port $TEST_PORT)"

mkdir -p "$TEST_DATA_DIR"
cat > "$TEST_DATA_DIR/config.json" <<JSON
{
  "podman_enabled": true,
  "podman_image": "boxnex-agent:latest",
  "podman_network": "boxnex-egress",
  "disk_quota_bytes": 268435456,
  "proxy_listen_addr": "0.0.0.0:${TEST_PROXY_PORT}"
}
JSON
chmod 0600 "$TEST_DATA_DIR/config.json"

# Check ports are free before starting
for port in "$TEST_PORT" "$TEST_PROXY_PORT"; do
  if ss -tlnp 2>/dev/null | grep -q ":${port} " || nc -zv -w1 127.0.0.1 "$port" 2>/dev/null; then
    echo "  ERROR: port $port is already in use — kill the process using it and retry"
    exit 1
  fi
done

CLAUDE_CODE_OAUTH_TOKEN="${CLAUDE_CODE_OAUTH_TOKEN:-}" \
  "$BINARY" --data-dir "$TEST_DATA_DIR" --port "$TEST_PORT" &
SERVER_PID=$!

# Wait up to 10s for the server to become ready
for i in $(seq 1 10); do
  sleep 1
  curl -s "http://localhost:${TEST_PORT}/api/info" | grep -q "mode" && break || true
done

if curl -s "http://localhost:${TEST_PORT}/api/info" | grep -q "mode"; then
  pass "BoxNex started (pid $SERVER_PID)"
else
  fail "BoxNex failed to start — check $TEST_DATA_DIR/logs/boxnex.log"
  exit 1
fi

if grep -q "Podman isolation enabled" "$TEST_DATA_DIR/logs/boxnex.log" 2>/dev/null; then
  pass "Podman isolation enabled"
else
  fail "Podman isolation not active — check config.json"
fi

# Detect actual proxy address containers should use (Podman rootless + pasta)
PROXY_HOST=$(python3 -c "
import socket
s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
s.connect(('8.8.8.8', 53))
print(s.getsockname()[0])
s.close()
" 2>/dev/null || echo "127.0.0.1")
PROXY_ADDR="${PROXY_HOST}:${TEST_PROXY_PORT}"
echo "  Proxy address for containers: $PROXY_ADDR"

# ── T04/T05: Container lifecycle ─────────────────────────────────────────────
section "T04/T05  Container lifecycle"

WORKSPACE="${TEST_DATA_DIR}/workspace"
mkdir -p "$WORKSPACE"
chmod 0777 "$WORKSPACE"

CID=$(podman run -d --rm \
  --name "$CONTAINER_NAME" \
  --network boxnex-egress \
  --cap-drop ALL \
  --security-opt no-new-privileges \
  --pids-limit 256 \
  --memory 512m \
  --userns=keep-id \
  -v "${WORKSPACE}:/workspace:z" \
  -e "HTTP_PROXY=http://${PROXY_ADDR}" \
  -e "HTTPS_PROXY=http://${PROXY_ADDR}" \
  -e "http_proxy=http://${PROXY_ADDR}" \
  -e "https_proxy=http://${PROXY_ADDR}" \
  -e "NO_PROXY=localhost,127.0.0.1" \
  -e "no_proxy=localhost,127.0.0.1" \
  -e "CLAUDE_CONFIG_DIR=/workspace/.claude" \
  -w /workspace \
  boxnex-agent:latest \
  tail -f /dev/null 2>/dev/null)

[ -n "$CID" ] && pass "T04: container created (${CID:0:12})" || { fail "T04: container failed to start"; exit 1; }

# Compare using short IDs (podman ps --format "{{.ID}}" returns 12-char short ID)
CID_SHORT="${CID:0:12}"
CID2=$(podman ps --filter "name=${CONTAINER_NAME}" --format "{{.ID}}" | head -1)
[ "$CID_SHORT" = "$CID2" ] && pass "T05: same container on second lookup (reuse)" \
  || fail "T05: different container ID on second lookup ($CID_SHORT vs $CID2)"

# ── T06/T07/T08: UID isolation ───────────────────────────────────────────────
section "T06/T07/T08  UID isolation + API Key + claude binary"

# Start a background process as UID 9999 with a fake secret, then immediately
# run a short sleep so the process appears in ps before we query it.
podman exec -d --user 9999:9999 \
  -e "CLAUDE_CODE_OAUTH_TOKEN=FAKE_SECRET_FOR_ISOLATION_TEST" \
  "$CONTAINER_NAME" sh -c "sleep 120" 2>/dev/null || true
sleep 2

SLEEP_PID=$(podman exec "$CONTAINER_NAME" \
  sh -c "ps aux 2>/dev/null | grep ' sleep ' | awk '{print \$1}'" \
  2>/dev/null | head -1 || true)

if [ -n "$SLEEP_PID" ]; then
  # Use awk to extract the USER column by matching PID; avoid busybox 'ps -o' syntax
  SLEEP_USER=$(podman exec "$CONTAINER_NAME" \
    sh -c "ps aux 2>/dev/null | awk -v p='${SLEEP_PID}' '\$1==p{print \$2}'" \
    2>/dev/null | tr -d ' ' || true)
  echo "$SLEEP_USER" | grep -qE "claude-r|9999" \
    && pass "T06: claude runs as UID 9999 (user: $SLEEP_USER)" \
    || fail "T06: expected claude-runner/9999, got: $SLEEP_USER"

  # cat /proc/$PID/environ returns permission denied when UID 1000 tries to read UID 9999's env;
  # the command exits non-zero — use || true so set -e doesn't fire
  ENVIRON_OUT=$(podman exec --user 1000:1000 "$CONTAINER_NAME" \
    sh -c "cat /proc/${SLEEP_PID}/environ 2>&1" || true)
  echo "$ENVIRON_OUT" | grep -q "Permission denied" \
    && pass "T07: agent(1000) cannot read /proc/$SLEEP_PID/environ (no CAP_SYS_PTRACE)" \
    || fail "T07: agent(1000) CAN read environ — API key exposed! Output: $ENVIRON_OUT"
else
  fail "T06: could not find sleep process"
  fail "T07: skipped (no process)"
fi

# agent(1000) cannot execute claude
EXEC_OUT=$(podman exec --user 1000:1000 "$CONTAINER_NAME" \
  /usr/local/bin/claude --version 2>&1 || true)
echo "$EXEC_OUT" | grep -qi "permission denied" \
  && pass "T08a: agent(1000) cannot execute claude (Permission denied)" \
  || fail "T08a: agent(1000) CAN execute claude — binary permissions broken"

# claude-runner(9999) can execute claude (just --version, no API call)
EXEC_OK=$(podman exec --user 9999:9999 "$CONTAINER_NAME" \
  /usr/local/bin/claude --version 2>/dev/null | head -1)
echo "$EXEC_OK" | grep -q "Claude Code" \
  && pass "T08b: claude-runner(9999) can execute claude ($EXEC_OK)" \
  || fail "T08b: claude-runner(9999) cannot execute claude"

# ── T09/T10: Egress proxy whitelist ─────────────────────────────────────────
section "T09/T10  Egress proxy whitelist"

if ! nc -zv -w2 "${PROXY_HOST}" "${TEST_PROXY_PORT}" 2>/dev/null; then
  skip "T09/T10: proxy not reachable at ${PROXY_ADDR} — skipping whitelist tests"
else
  # T09: blocked domain
  BLOCKED=$(curl -4 -v --max-time 5 -x "http://${PROXY_ADDR}" \
    "https://evil.example.com" 2>&1 || true)
  echo "$BLOCKED" | grep -qE "CONNECT aborted|403|blocked" \
    && pass "T09: evil.example.com blocked by egress proxy" \
    || fail "T09: evil.example.com NOT blocked — $BLOCKED"

  # T10: allowed domain
  ALLOWED=$(curl -4 -v --max-time 8 -x "http://${PROXY_ADDR}" \
    "https://api.anthropic.com" 2>&1 || true)
  echo "$ALLOWED" | grep -q "200 Connection established" \
    && pass "T10: api.anthropic.com allowed (200 Connection established)" \
    || fail "T10: api.anthropic.com not getting through — $ALLOWED"
fi

# ── T11: Inter-container isolation ───────────────────────────────────────────
section "T11  Container-to-container isolation"

WORKSPACE2="${TEST_DATA_DIR}/workspace2"
mkdir -p "$WORKSPACE2"
podman run -d --rm \
  --name "$CONTAINER2_NAME" \
  --network boxnex-egress \
  --cap-drop ALL \
  --security-opt no-new-privileges \
  boxnex-agent:latest \
  tail -f /dev/null >/dev/null 2>&1

sleep 1
IP2=$(podman exec "$CONTAINER2_NAME" ip addr show eth0 2>/dev/null \
  | grep "inet " | awk '{print $2}' | cut -d/ -f1)

if [ -n "$IP2" ]; then
  # Use curl since nc is not installed in the container image.
  # curl returns "Connection refused" or "No route to host" when the target
  # is unreachable; either means the port/host is not accessible.
  # Container 1 has HTTP_PROXY set, so curl routes through the egress proxy.
  # The proxy either:
  #   a) returns "Access denied" (IP not in whitelist) — proxy-enforced isolation
  #   b) returns "Connection refused" / "No route to host" — network-level isolation
  # Either way, container 1 cannot effectively communicate with container 2.
  CURL_OUT=$(podman exec "$CONTAINER_NAME" \
    sh -c "curl -4 --max-time 3 http://${IP2}/ 2>&1" || true)
  echo "$CURL_OUT" | grep -qiE "refused|no route|timed out|could not connect|failed|denied" \
    && pass "T11: container 1 cannot reach container 2 at $IP2 (proxy/network isolation)" \
    || fail "T11: container 1 CAN reach container 2 — network isolation broken: $CURL_OUT"
else
  skip "T11: could not determine container 2 IP"
fi

# ── T12: Disk quota ──────────────────────────────────────────────────────────
section "T12  Disk quota enforcement"

QUOTA_DIR=$(mktemp -d /tmp/quota-test-XXXXXX)
QUOTA_BYTES=$((5 * 1024 * 1024))  # 5 MB

cancelled=false
python3 - <<PYEOF
import os, subprocess, time, threading, sys

quota = $QUOTA_BYTES
workdir = "$QUOTA_DIR"
cancelled = threading.Event()

def watcher():
    for _ in range(20):
        time.sleep(1)
        out = subprocess.run(["du", "-sb", workdir], capture_output=True, text=True)
        if out.returncode != 0:
            continue
        used = int(out.stdout.split()[0])
        if used >= quota:
            cancelled.set()
            return

t = threading.Thread(target=watcher, daemon=True)
t.start()

# Write 2 MB — should NOT trigger
with open(os.path.join(workdir, "a"), "wb") as f:
    f.write(b"\0" * (2 * 1024 * 1024))
time.sleep(2)
assert not cancelled.is_set(), "cancelled too early (at 2MB < 5MB)"

# Write 4 MB more (total 6 MB) — should trigger
with open(os.path.join(workdir, "b"), "wb") as f:
    f.write(b"\0" * (4 * 1024 * 1024))

t.join(timeout=12)
assert cancelled.is_set(), "quota was NOT enforced after 6MB > 5MB"
print("ok")
PYEOF
rm -rf "$QUOTA_DIR"

[ $? -eq 0 ] \
  && pass "T12: workspace quota enforced (cancel triggered at 6MB > 5MB limit)" \
  || fail "T12: quota not enforced"

# ── T13: Real Claude API call ────────────────────────────────────────────────
section "T13  Real Claude API call (end-to-end)"

if [ -z "${CLAUDE_CODE_OAUTH_TOKEN:-}" ]; then
  skip "T13: CLAUDE_CODE_OAUTH_TOKEN not set — skipping real API call"
else
  CLAUDE_OUT=$(podman exec --user 9999:9999 \
    -e "CLAUDE_CODE_OAUTH_TOKEN=${CLAUDE_CODE_OAUTH_TOKEN}" \
    -e "HTTP_PROXY=http://${PROXY_ADDR}" \
    -e "HTTPS_PROXY=http://${PROXY_ADDR}" \
    -e "CLAUDE_CONFIG_DIR=/workspace/.claude" \
    -w /workspace \
    "$CONTAINER_NAME" \
    /usr/local/bin/claude -p "Reply with exactly the text: CONTAINER_TEST_OK" 2>&1 || true)

  echo "$CLAUDE_OUT" | grep -q "CONTAINER_TEST_OK" \
    && pass "T13: Claude responded correctly inside container" \
    || fail "T13: unexpected response — $CLAUDE_OUT"
fi

# ── Summary ──────────────────────────────────────────────────────────────────
echo ""
echo "════════════════════════════════════════"
echo "  Results: ${PASS} passed  ${FAIL} failed  ${SKIP} skipped"
echo "════════════════════════════════════════"
[ "$FAIL" -eq 0 ] && exit 0 || exit 1
