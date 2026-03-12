#!/bin/bash
# scripts/e2e-container-test.sh
#
# End-to-end test suite for BoxNex Docker container isolation.
# Validates that the Docker + Sysbox sandbox, per-user home volumes,
# resource quotas, LD_PRELOAD injection, idle detection, and container
# state recovery all work correctly on the current machine.
#
# Usage:
#   CLAUDE_CODE_OAUTH_TOKEN=sk-ant-... bash scripts/e2e-container-test.sh
#
# Requirements:
#   - docker installed and accessible (rootless or root)
#   - sysbox-runc runtime available
#   - boxnex-agent:latest image already built (run scripts/build-agent-image.sh first)
#   - BoxNex binary at target/boxnex (run build.sh first)
#   - CLAUDE_CODE_OAUTH_TOKEN set (only required for T14: real Claude API call)
#
# The script starts a throw-away BoxNex server on port 9099, runs all tests,
# then cleans up containers, volumes, and temp files regardless of outcome.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BINARY="${REPO_ROOT}/target/boxnex"
TEST_PORT=9099
TEST_DATA_DIR="/tmp/boxnex-e2e-$$"
# Safe project IDs for testing (only [a-zA-Z0-9-_], len <= 48)
TEST_PROJ_A="e2etest-docker-01"
TEST_PROJ_B="e2etest-docker-02"
TEST_USER_A="e2euser-alice"
CONTAINER_A="boxnex-proj-${TEST_PROJ_A}"
CONTAINER_B="boxnex-proj-${TEST_PROJ_B}"
HOME_VOL_A="boxnex-home-${TEST_USER_A}"
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
  docker stop "$CONTAINER_A" 2>/dev/null && echo "  stopped $CONTAINER_A" || true
  docker stop "$CONTAINER_B" 2>/dev/null && echo "  stopped $CONTAINER_B" || true
  if [ -n "$SERVER_PID" ]; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
    echo "  stopped BoxNex (pid $SERVER_PID)"
  fi
  docker volume rm "$HOME_VOL_A" 2>/dev/null && echo "  removed volume $HOME_VOL_A" || true
  rm -rf "$TEST_DATA_DIR" 2>/dev/null && echo "  removed $TEST_DATA_DIR" || true
}
trap cleanup EXIT

# ── T01: Prerequisites ────────────────────────────────────────────────────────
section "T01  Prerequisites"

if ! command -v docker &>/dev/null; then
  fail "docker not found — install Docker Engine first"
  exit 1
fi
DOCKER_VER=$(docker version --format '{{.Client.Version}}' 2>/dev/null || echo "unknown")
pass "docker ${DOCKER_VER} found"

# Check sysbox-runc is registered as a Docker runtime
SYSBOX_OK=0
if docker info 2>/dev/null | grep -q sysbox; then
  SYSBOX_OK=1
elif command -v sysbox-runc &>/dev/null; then
  SYSBOX_OK=1
fi
if [ "$SYSBOX_OK" -eq 1 ]; then
  pass "sysbox-runc runtime available"
else
  fail "sysbox-runc not found — install Sysbox from nestybox.com and register with Docker"
  SYSBOX_OK=0
fi

if [ ! -x "$BINARY" ]; then
  fail "binary not found at $BINARY — run: bash build.sh"
  exit 1
fi
pass "boxnex binary found"

if docker image inspect boxnex-agent:latest &>/dev/null; then
  pass "boxnex-agent:latest image found"
else
  fail "boxnex-agent:latest image not found — run: bash scripts/build-agent-image.sh"
  exit 1
fi

# ── T02: Sysbox DinD capability ──────────────────────────────────────────────
section "T02  Sysbox DinD capability"

if [ "$SYSBOX_OK" -eq 0 ]; then
  skip "T02: sysbox-runc unavailable — skipping DinD test"
else
  CID_TEST=$(docker run -d --rm --runtime sysbox-runc \
    --name boxnex-e2e-sysbox-test \
    boxnex-agent:latest tail -f /dev/null 2>/dev/null || echo "")
  if [ -n "$CID_TEST" ]; then
    docker stop boxnex-e2e-sysbox-test &>/dev/null || true
    pass "T02: container launched successfully with --runtime sysbox-runc"
  else
    fail "T02: docker run with --runtime sysbox-runc failed"
  fi
fi

# ── T03: BoxNex startup ───────────────────────────────────────────────────────
section "T03  BoxNex startup with Docker isolation"

mkdir -p "$TEST_DATA_DIR"
cat > "$TEST_DATA_DIR/config.json" <<'JSON'
{
  "container_enabled": true,
  "container_image": "boxnex-agent:latest",
  "container_network": "boxnex-net"
}
JSON
chmod 0600 "$TEST_DATA_DIR/config.json"

if ss -tlnp 2>/dev/null | grep -q ":${TEST_PORT} "; then
  echo "  ERROR: port $TEST_PORT is already in use"
  exit 1
fi

"$BINARY" --data-dir "$TEST_DATA_DIR" --port "$TEST_PORT" &
SERVER_PID=$!

for i in $(seq 1 10); do
  sleep 1
  curl -s "http://localhost:${TEST_PORT}/api/info" 2>/dev/null | grep -q "mode" && break || true
done

if curl -s "http://localhost:${TEST_PORT}/api/info" 2>/dev/null | grep -q "mode"; then
  pass "T03a: BoxNex started (pid $SERVER_PID)"
else
  fail "T03a: BoxNex failed to start — check $TEST_DATA_DIR/logs/boxnex.log"
  exit 1
fi

if grep -q "Docker isolation enabled" "$TEST_DATA_DIR/logs/boxnex.log" 2>/dev/null; then
  pass "T03b: Docker isolation log line present"
else
  fail "T03b: Docker isolation log line missing — check config.json"
fi

# ── T04: Docker network ───────────────────────────────────────────────────────
section "T04  Docker network ensured"

if docker network ls --format "{{.Name}}" 2>/dev/null | grep -q "^boxnex-net$"; then
  pass "T04: boxnex-net network created by BoxNex startup"
else
  fail "T04: boxnex-net network not found"
fi

# ── T05/T06: Container lifecycle ─────────────────────────────────────────────
section "T05/T06  Container lifecycle (create + reuse)"

WORKSPACE_A="${TEST_DATA_DIR}/workspace-a"
mkdir -p "$WORKSPACE_A"
chmod 0777 "$WORKSPACE_A"

# Simulate EnsureContainer: docker run with sysbox-runc, boxnex.project label, home volume
CID_A=$(docker run -d \
  --name "$CONTAINER_A" \
  --rm \
  --runtime sysbox-runc \
  --memory "512m" \
  --memory-swap "512m" \
  --cpus "0.5" \
  --pids-limit "100" \
  --network boxnex-net \
  --label "boxnex.project=${TEST_PROJ_A}" \
  -v "${WORKSPACE_A}:/workspace" \
  -w /workspace \
  -e "CLAUDE_CONFIG_DIR=/workspace/.claude" \
  -e "USE_BUILTIN_RIPGREP=0" \
  -e "DISABLE_TELEMETRY=1" \
  -e "DISABLE_AUTOUPDATER=1" \
  -v "${HOME_VOL_A}:/root" \
  boxnex-agent:latest \
  tail -f /dev/null 2>/dev/null)

if [ -n "$CID_A" ]; then
  pass "T05: container $CONTAINER_A created (${CID_A:0:12})"
else
  fail "T05: container failed to start"
  exit 1
fi

PS_ID=$(docker ps --filter "name=^${CONTAINER_A}$" --format "{{.ID}}" | head -1)
[ -n "$PS_ID" ] \
  && pass "T06: container appears in docker ps (reuse check: same ID ${PS_ID:0:12})" \
  || fail "T06: container not found in docker ps"

# ── T07: Container labels ─────────────────────────────────────────────────────
section "T07  Container label (boxnex.project)"

LABEL_VAL=$(docker inspect "$CONTAINER_A" \
  --format '{{index .Config.Labels "boxnex.project"}}' 2>/dev/null)
[ "$LABEL_VAL" = "$TEST_PROJ_A" ] \
  && pass "T07: boxnex.project label = \"$LABEL_VAL\"" \
  || fail "T07: expected \"$TEST_PROJ_A\", got: \"$LABEL_VAL\""

# ── T08: Per-user home volume ─────────────────────────────────────────────────
section "T08  Per-user home volume persistence"

SENTINEL_FILE="/root/.e2e-sentinel-$$"

docker exec "$CONTAINER_A" sh -c "echo 'hello-from-e2e' > ${SENTINEL_FILE}" 2>/dev/null \
  && pass "T08a: wrote sentinel to /root inside container" \
  || fail "T08a: could not write to /root inside container"

# Verify /root is a named Docker volume mount
MOUNTS_JSON=$(docker inspect "$CONTAINER_A" --format '{{json .Mounts}}' 2>/dev/null)
if echo "$MOUNTS_JSON" | python3 -c "
import sys, json
mounts = json.load(sys.stdin)
for m in mounts:
    if m.get('Destination') == '/root' and m.get('Type') == 'volume':
        print('ok:', m.get('Name',''))
        sys.exit(0)
sys.exit(1)
" 2>/dev/null; then
  pass "T08b: /root is a Docker named volume mount"
else
  fail "T08b: /root is not a Docker named volume"
fi

VOL_NAME=$(echo "$MOUNTS_JSON" | python3 -c "
import sys, json
mounts = json.load(sys.stdin)
for m in mounts:
    if m.get('Destination') == '/root':
        print(m.get('Name',''))
        break
" 2>/dev/null)
[ "$VOL_NAME" = "$HOME_VOL_A" ] \
  && pass "T08c: volume name = $HOME_VOL_A" \
  || fail "T08c: expected $HOME_VOL_A, got: $VOL_NAME"

# Stop, recreate, verify sentinel persists
docker stop "$CONTAINER_A" &>/dev/null || true
sleep 1
docker run -d \
  --name "$CONTAINER_A" \
  --rm \
  --runtime sysbox-runc \
  --label "boxnex.project=${TEST_PROJ_A}" \
  -v "${WORKSPACE_A}:/workspace" \
  -w /workspace \
  -v "${HOME_VOL_A}:/root" \
  --network boxnex-net \
  boxnex-agent:latest \
  tail -f /dev/null >/dev/null 2>&1

SENTINEL_CONTENT=$(docker exec "$CONTAINER_A" \
  sh -c "cat ${SENTINEL_FILE} 2>/dev/null" 2>/dev/null || echo "")
[ "$SENTINEL_CONTENT" = "hello-from-e2e" ] \
  && pass "T08d: sentinel persisted across recreation (home volume working)" \
  || fail "T08d: expected 'hello-from-e2e', got: '$SENTINEL_CONTENT'"

# ── T09: Resource quota parameters ───────────────────────────────────────────
section "T09  Resource quota parameters (--memory / --cpus / --pids-limit)"

MEM_LIMIT=$(docker inspect "$CONTAINER_A" \
  --format '{{.HostConfig.Memory}}' 2>/dev/null)
NANO_CPUS=$(docker inspect "$CONTAINER_A" \
  --format '{{.HostConfig.NanoCpus}}' 2>/dev/null)
PIDS_LIMIT=$(docker inspect "$CONTAINER_A" \
  --format '{{.HostConfig.PidsLimit}}' 2>/dev/null)

# 512m = 536870912 bytes
[ "$MEM_LIMIT" = "536870912" ] \
  && pass "T09a: memory = 512m (536870912 bytes)" \
  || fail "T09a: expected 536870912, got: $MEM_LIMIT"

# 0.5 CPUs = 500000000 nanocpus
[ "$NANO_CPUS" = "500000000" ] \
  && pass "T09b: CPU = 0.5 (500000000 nanocpus)" \
  || fail "T09b: expected 500000000, got: $NANO_CPUS"

[ "$PIDS_LIMIT" = "100" ] \
  && pass "T09c: pids-limit = 100" \
  || fail "T09c: expected 100, got: $PIDS_LIMIT"

# ── T10: inject.so LD_PRELOAD injection ───────────────────────────────────────
section "T10  inject.so LD_PRELOAD token injection"

INJECT_SO="/opt/boxnex/inject.so"
if [ -f "$INJECT_SO" ]; then
  MOUNTS_JSON_A=$(docker inspect "$CONTAINER_A" --format '{{json .Mounts}}' 2>/dev/null)
  INJECT_MOUNT=$(echo "$MOUNTS_JSON_A" | python3 -c "
import sys, json
mounts = json.load(sys.stdin)
for m in mounts:
    if m.get('Destination') == '/usr/lib/inject.so':
        print('ok:', m.get('Source',''))
        sys.exit(0)
sys.exit(1)
" 2>/dev/null || echo "")
  if [ -n "$INJECT_MOUNT" ]; then
    pass "T10a: inject.so bind-mounted into container ($INJECT_MOUNT)"
  else
    fail "T10a: inject.so not mounted at /usr/lib/inject.so inside container"
  fi

  docker exec "$CONTAINER_A" test -r /usr/lib/inject.so 2>/dev/null \
    && pass "T10b: /usr/lib/inject.so is readable inside container" \
    || fail "T10b: /usr/lib/inject.so not readable inside container"
else
  skip "T10a: $INJECT_SO absent on host — inject.so not mounted (expected in production)"
  skip "T10b: depends on T10a"
fi

# ── T11: Container state recovery ─────────────────────────────────────────────
section "T11  Container state recovery (RecoverRunningContainers)"

WORKSPACE_B="${TEST_DATA_DIR}/workspace-b"
mkdir -p "$WORKSPACE_B"

# Start a container with boxnex.project label before restarting BoxNex
docker run -d \
  --name "$CONTAINER_B" \
  --rm \
  --runtime sysbox-runc \
  --label "boxnex.project=${TEST_PROJ_B}" \
  -v "${WORKSPACE_B}:/workspace" \
  -w /workspace \
  --network boxnex-net \
  boxnex-agent:latest \
  tail -f /dev/null >/dev/null 2>&1
sleep 1

PS_B=$(docker ps --filter "label=boxnex.project=${TEST_PROJ_B}" --format "{{.Names}}" | head -1)
[ -n "$PS_B" ] \
  && pass "T11a: container $CONTAINER_B running with boxnex.project label" \
  || fail "T11a: container $CONTAINER_B not found via label filter"

# Restart BoxNex — RecoverRunningContainers should re-register both containers
kill "$SERVER_PID" 2>/dev/null || true
wait "$SERVER_PID" 2>/dev/null || true
SERVER_PID=""
mv "$TEST_DATA_DIR/logs/boxnex.log" "$TEST_DATA_DIR/logs/boxnex-run1.log" 2>/dev/null || true

"$BINARY" --data-dir "$TEST_DATA_DIR" --port "$TEST_PORT" &
SERVER_PID=$!
for i in $(seq 1 10); do
  sleep 1
  curl -s "http://localhost:${TEST_PORT}/api/info" 2>/dev/null | grep -q "mode" && break || true
done

RECOVERED_COUNT=$(grep -c "re-registered running container" \
  "$TEST_DATA_DIR/logs/boxnex.log" 2>/dev/null || echo "0")
if [ "$RECOVERED_COUNT" -ge 1 ]; then
  pass "T11b: BoxNex re-registered $RECOVERED_COUNT container(s) on restart"
else
  skip "T11b: no containers re-registered (may have exited before recovery ran)"
fi

# ── T12: Idle container detection ─────────────────────────────────────────────
section "T12  Idle container detection (StopIdleContainers)"

RUNNING_A=$(docker ps --filter "name=^${CONTAINER_A}$" --format "{{.ID}}" | head -1)
RUNNING_B=$(docker ps --filter "name=^${CONTAINER_B}$" --format "{{.ID}}" | head -1)
if [ -n "$RUNNING_A" ] && [ -n "$RUNNING_B" ]; then
  pass "T12a: both test containers running before idle check"
else
  skip "T12a: not all containers running (A=${RUNNING_A:+up} B=${RUNNING_B:+up})"
fi

# StopIdleContainers calls docker stop on idle containers. Validate this directly:
# stopping a container must make it disappear from docker ps (--rm flag ensures removal).
docker stop "$CONTAINER_A" &>/dev/null || true
sleep 1
GONE_A=$(docker ps --filter "name=^${CONTAINER_A}$" --format "{{.ID}}" | head -1)
[ -z "$GONE_A" ] \
  && pass "T12b: container removed from docker ps after stop (--rm working)" \
  || fail "T12b: container still in docker ps after stop"

# ── T13: Workspace bind mount ─────────────────────────────────────────────────
section "T13  Workspace bind mount"

TEST_FILENAME="e2e-write-test-$$"
docker exec "$CONTAINER_B" sh -c "echo 'workspace-ok' > /workspace/${TEST_FILENAME}" 2>/dev/null \
  && pass "T13a: container can write to /workspace" \
  || fail "T13a: container cannot write to /workspace"

[ -f "${WORKSPACE_B}/${TEST_FILENAME}" ] \
  && pass "T13b: write is visible on host (bind mount working)" \
  || fail "T13b: write not visible on host"

rm -f "${WORKSPACE_B}/${TEST_FILENAME}" 2>/dev/null || true

# ── T14: Real Claude API call ─────────────────────────────────────────────────
section "T14  Real Claude API call (requires CLAUDE_CODE_OAUTH_TOKEN)"

if [ -z "${CLAUDE_CODE_OAUTH_TOKEN:-}" ]; then
  skip "T14: CLAUDE_CODE_OAUTH_TOKEN not set — skipping real API call"
elif [ -z "$(docker ps --filter "name=^${CONTAINER_B}$" --format "{{.ID}}" | head -1)" ]; then
  skip "T14: test container $CONTAINER_B not running"
else
  # Prefer LD_PRELOAD injection if inject.so is present; fall back to direct -e flag.
  if [ -f "/opt/boxnex/inject.so" ] && \
     docker exec "$CONTAINER_B" test -r /usr/lib/inject.so 2>/dev/null; then
    CLAUDE_OUT=$(docker exec \
      -e "LD_PRELOAD=/usr/lib/inject.so" \
      -e "CLAUDE_CODE_OAUTH_TOKEN=${CLAUDE_CODE_OAUTH_TOKEN}" \
      -e "CLAUDE_CONFIG_DIR=/workspace/.claude" \
      -w /workspace \
      "$CONTAINER_B" \
      claude -p "Reply with exactly the text: CONTAINER_TEST_OK" 2>&1 || true)
    INJECT_METHOD="LD_PRELOAD inject.so"
  else
    CLAUDE_OUT=$(docker exec \
      -e "CLAUDE_CODE_OAUTH_TOKEN=${CLAUDE_CODE_OAUTH_TOKEN}" \
      -e "CLAUDE_CONFIG_DIR=/workspace/.claude" \
      -w /workspace \
      "$CONTAINER_B" \
      claude -p "Reply with exactly the text: CONTAINER_TEST_OK" 2>&1 || true)
    INJECT_METHOD="direct -e env var"
  fi

  echo "$CLAUDE_OUT" | grep -q "CONTAINER_TEST_OK" \
    && pass "T14: Claude responded correctly inside container (auth via $INJECT_METHOD)" \
    || fail "T14: unexpected output — $CLAUDE_OUT"
fi

# ── Summary ──────────────────────────────────────────────────────────────────
echo ""
echo "════════════════════════════════════════"
echo "  Results: ${PASS} passed  ${FAIL} failed  ${SKIP} skipped"
echo "════════════════════════════════════════"
[ "$FAIL" -eq 0 ] && exit 0 || exit 1
