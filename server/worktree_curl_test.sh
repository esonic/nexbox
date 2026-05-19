#!/usr/bin/env bash
# worktree_curl_test.sh — Black-box integration test for the worktree API.
#
# Usage:
#   ./worktree_curl_test.sh           # starts its own server, auto-cleans up
#   BASE_URL=http://127.0.0.1:9040 ./worktree_curl_test.sh  # run against existing server
#
# Requirements: bash, curl, git, jq

set -euo pipefail

# ── colours ─────────────────────────────────────────────────────────────────
GREEN='\033[0;32m'; RED='\033[0;31m'; YELLOW='\033[0;33m'; NC='\033[0m'
pass() { echo -e "${GREEN}PASS${NC}  $1"; }
fail() { echo -e "${RED}FAIL${NC}  $1"; FAILED=$((FAILED + 1)); }
info() { echo -e "${YELLOW}INFO${NC}  $1"; }

FAILED=0
TMPDIR_LOCAL=$(mktemp -d)
trap 'rm -rf "$TMPDIR_LOCAL"' EXIT

# ── server setup ────────────────────────────────────────────────────────────
SERVER_PID=""
if [[ -z "${BASE_URL:-}" ]]; then
  PORT=19041
  BASE_URL="http://127.0.0.1:$PORT"
  DATA_DIR="$TMPDIR_LOCAL/data"
  mkdir -p "$DATA_DIR"

  SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
  BINARY="$SCRIPT_DIR/nexbox"
  if [[ ! -x "$BINARY" ]]; then
    echo "nexbox binary not found at $BINARY — run: cd server && go build -o nexbox ." >&2
    exit 1
  fi

  info "Starting server on :$PORT with data-dir=$DATA_DIR"
  "$BINARY" --port "$PORT" --data-dir "$DATA_DIR" --listen 127.0.0.1 \
    2>"$TMPDIR_LOCAL/server.log" &
  SERVER_PID=$!
  trap 'kill $SERVER_PID 2>/dev/null; rm -rf "$TMPDIR_LOCAL"' EXIT

  # Wait for server to be ready
  for i in $(seq 1 30); do
    if curl -sf "$BASE_URL/api/info" >/dev/null 2>&1; then break; fi
    sleep 0.3
    if [[ $i -eq 30 ]]; then
      echo "Server did not start in time. Log:" >&2
      cat "$TMPDIR_LOCAL/server.log" >&2
      exit 1
    fi
  done
  info "Server ready at $BASE_URL"
fi

API="$BASE_URL/api"

# ── helpers ──────────────────────────────────────────────────────────────────

# json_field <field> <json>  — extract a string field from JSON using jq or python
json_field() {
  local field="$1" json="$2"
  if command -v jq &>/dev/null; then
    echo "$json" | jq -r ".$field"
  else
    echo "$json" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('$field',''))"
  fi
}

# assert_eq <got> <want> <label>
assert_eq() {
  if [[ "$1" == "$2" ]]; then pass "$3"; else fail "$3 (got='$1' want='$2')"; fi
}

# assert_not_empty <got> <label>
assert_not_empty() {
  if [[ -n "$1" && "$1" != "null" ]]; then pass "$2"; else fail "$2 (empty/null)"; fi
}

# http_status <method> <url> [body]
http_status() {
  local method="$1" url="$2" body="${3:-}"
  if [[ -n "$body" ]]; then
    curl -s -o /dev/null -w "%{http_code}" -X "$method" -H "Content-Type: application/json" -d "$body" "$url"
  else
    curl -s -o /dev/null -w "%{http_code}" -X "$method" "$url"
  fi
}

# http_body <method> <url> [body]
http_body() {
  local method="$1" url="$2" body="${3:-}"
  if [[ -n "$body" ]]; then
    curl -s -X "$method" -H "Content-Type: application/json" -d "$body" "$url"
  else
    curl -s -X "$method" "$url"
  fi
}

echo ""
echo "═══════════════════════════════════════════════════════════════"
echo " Worktree API Black-box Tests  ($BASE_URL)"
echo "═══════════════════════════════════════════════════════════════"
echo ""

# ════════════════════════════════════════════════════════════════════
# 1. Server sanity
# ════════════════════════════════════════════════════════════════════
echo "── 1. Server sanity ──────────────────────────────────────────"
STATUS=$(http_status GET "$API/info")
assert_eq "$STATUS" "200" "GET /api/info → 200"

# ════════════════════════════════════════════════════════════════════
# 2. Session creation & default worktree fields
# ════════════════════════════════════════════════════════════════════
echo ""
echo "── 2. Session creation & default worktree fields ─────────────"
SESS_BODY=$(http_body POST "$API/sessions" '{"name":"wt-test"}')
SESS_ID=$(json_field "id" "$SESS_BODY")
assert_not_empty "$SESS_ID" "POST /api/sessions → session ID"

# GET session — should have default worktree_mode=local
GET_BODY=$(http_body GET "$API/sessions/$SESS_ID")
WT_MODE=$(json_field "worktree_mode" "$GET_BODY")
WT_PATH=$(json_field "worktree_path" "$GET_BODY")
assert_eq "$WT_MODE" "local" "new session worktree_mode=local"
# worktree_path may be empty string or null
[[ -z "$WT_PATH" || "$WT_PATH" == "null" ]] && pass "new session worktree_path is empty" || fail "new session worktree_path should be empty (got '$WT_PATH')"

# ListSessions should include our session with worktree fields
LIST_BODY=$(http_body GET "$API/sessions")
if echo "$LIST_BODY" | grep -q "\"$SESS_ID\""; then pass "session appears in list"; else fail "session not in list"; fi

# ════════════════════════════════════════════════════════════════════
# 3. PUT /worktree-mode — set to worktree
# ════════════════════════════════════════════════════════════════════
echo ""
echo "── 3. PUT /worktree-mode ──────────────────────────────────────"

STATUS=$(http_status PUT "$API/sessions/$SESS_ID/worktree-mode" '{"mode":"worktree"}')
assert_eq "$STATUS" "200" "PUT worktree-mode=worktree → 200"

BODY=$(http_body PUT "$API/sessions/$SESS_ID/worktree-mode" '{"mode":"worktree"}')
UPDATED_MODE=$(json_field "worktree_mode" "$BODY")
assert_eq "$UPDATED_MODE" "worktree" "response worktree_mode=worktree"

# Verify GET reflects the change
GET_BODY=$(http_body GET "$API/sessions/$SESS_ID")
assert_eq "$(json_field 'worktree_mode' "$GET_BODY")" "worktree" "GET session reflects worktree mode"

# Reset back to local
STATUS=$(http_status PUT "$API/sessions/$SESS_ID/worktree-mode" '{"mode":"local"}')
assert_eq "$STATUS" "200" "PUT worktree-mode=local (reset) → 200"
GET_BODY=$(http_body GET "$API/sessions/$SESS_ID")
assert_eq "$(json_field 'worktree_mode' "$GET_BODY")" "local" "GET session reflects local mode after reset"

# ════════════════════════════════════════════════════════════════════
# 4. PUT /worktree-mode — error cases
# ════════════════════════════════════════════════════════════════════
echo ""
echo "── 4. PUT /worktree-mode error cases ─────────────────────────"

# Invalid mode value
STATUS=$(http_status PUT "$API/sessions/$SESS_ID/worktree-mode" '{"mode":"bad"}')
assert_eq "$STATUS" "400" "PUT worktree-mode=bad → 400"

# Invalid JSON body
STATUS=$(http_status PUT "$API/sessions/$SESS_ID/worktree-mode" 'not-json')
assert_eq "$STATUS" "400" "PUT worktree-mode invalid JSON → 400"

# Non-existent session
STATUS=$(http_status PUT "$API/sessions/does-not-exist/worktree-mode" '{"mode":"worktree"}')
assert_eq "$STATUS" "404" "PUT worktree-mode for non-existent session → 404"

# ════════════════════════════════════════════════════════════════════
# 5. Locked after first message (agent_session_id set)
# ════════════════════════════════════════════════════════════════════
echo ""
echo "── 5. Locked after first message ─────────────────────────────"

LOCKED_BODY=$(http_body POST "$API/sessions" '{"name":"wt-locked"}')
LOCKED_ID=$(json_field "id" "$LOCKED_BODY")

# Simulate first message by directly updating agent_session_id via DB
# We use the fact that the session PUT endpoint accepts partial updates
# However there's no public API to set agent_session_id — we verify that
# an unstarted session is NOT locked (200), and note that locking is
# tested via unit tests which can set agent_session_id directly.
STATUS=$(http_status PUT "$API/sessions/$LOCKED_ID/worktree-mode" '{"mode":"worktree"}')
assert_eq "$STATUS" "200" "PUT worktree-mode on new (unlocked) session → 200"
info "Lock-after-first-message is verified in unit tests (agent_session_id not settable via REST)"

# ════════════════════════════════════════════════════════════════════
# 6. GET /worktree-status
# ════════════════════════════════════════════════════════════════════
echo ""
echo "── 6. GET /worktree-status ─────────────────────────────────────"

# Status for local session → dirty=false
STATUS_BODY=$(http_body GET "$API/sessions/$SESS_ID/worktree-status")
STATUS_CODE=$(http_status GET "$API/sessions/$SESS_ID/worktree-status")
assert_eq "$STATUS_CODE" "200" "GET worktree-status (local) → 200"
assert_eq "$(json_field 'dirty' "$STATUS_BODY")" "false" "worktree-status (local) dirty=false"

# Status for worktree session with no path yet → dirty=false
STATUS=$(http_status PUT "$API/sessions/$SESS_ID/worktree-mode" '{"mode":"worktree"}')
STATUS_BODY=$(http_body GET "$API/sessions/$SESS_ID/worktree-status")
assert_eq "$(json_field 'dirty' "$STATUS_BODY")" "false" "worktree-status (mode=worktree, no path) dirty=false"

# Non-existent session
STATUS=$(http_status GET "$API/sessions/does-not-exist/worktree-status")
assert_eq "$STATUS" "404" "GET worktree-status for non-existent session → 404"

# ════════════════════════════════════════════════════════════════════
# 7. Git worktree integration (requires git)
# ════════════════════════════════════════════════════════════════════
echo ""
echo "── 7. Git worktree integration ─────────────────────────────────"

if ! command -v git &>/dev/null; then
  info "git not found — skipping git integration tests"
else
  # Create a git repo in a temp dir
  GIT_REPO="$TMPDIR_LOCAL/gitrepo"
  mkdir -p "$GIT_REPO"
  git -C "$GIT_REPO" init -q
  git -C "$GIT_REPO" config user.email "test@test.com"
  git -C "$GIT_REPO" config user.name "Test"
  echo "hello" > "$GIT_REPO/README.md"
  git -C "$GIT_REPO" add .
  git -C "$GIT_REPO" commit -q -m "init"

  # Create a project, then set git_repo_path via PUT
  # (POST /api/projects clears git_repo_path before DB insert in local SQLite mode)
  PROJ_BODY=$(http_body POST "$API/projects" '{"name":"wt-proj"}')
  PROJ_ID=$(json_field "id" "$PROJ_BODY")
  assert_not_empty "$PROJ_ID" "POST /api/projects → project ID"

  # Set git_repo_path via PUT (persisted to SQLite in local mode)
  PUT_BODY=$(http_body PUT "$API/projects/$PROJ_ID" \
    "{\"git_repo_path\":\"$GIT_REPO\"}")
  assert_eq "$(json_field 'git_repo_path' "$PUT_BODY")" "$GIT_REPO" "PUT project sets git_repo_path"

  # Create session bound to project
  WSESS_BODY=$(http_body POST "$API/sessions" \
    "{\"name\":\"wt-git\",\"project_id\":\"$PROJ_ID\"}")
  WSESS_ID=$(json_field "id" "$WSESS_BODY")
  assert_not_empty "$WSESS_ID" "POST /api/sessions (bound) → session ID"

  # Set worktree mode
  WT_SET_BODY=$(http_body PUT "$API/sessions/$WSESS_ID/worktree-mode" '{"mode":"worktree"}')
  assert_eq "$(json_field 'worktree_mode' "$WT_SET_BODY")" "worktree" "worktree mode set to worktree"

  # worktree-status for mode=worktree but no path yet
  WT_STATUS=$(http_body GET "$API/sessions/$WSESS_ID/worktree-status")
  assert_eq "$(json_field 'dirty' "$WT_STATUS")" "false" "worktree-status before creation: dirty=false"

  # ── Git status endpoint (falls back to project root when no worktree path) ──
  GIT_STATUS=$(http_body GET "$API/projects/$PROJ_ID/git-status?session_id=$WSESS_ID")
  # Just check it returns 200 (not an error)
  GS_CODE=$(http_status GET "$API/projects/$PROJ_ID/git-status?session_id=$WSESS_ID")
  assert_eq "$GS_CODE" "200" "GET git-status with session_id → 200"

  # ── Git log ──
  GL_CODE=$(http_status GET "$API/projects/$PROJ_ID/git-log?limit=5&session_id=$WSESS_ID")
  assert_eq "$GL_CODE" "200" "GET git-log with session_id → 200"

  # ── Git branches ──
  GB_CODE=$(http_status GET "$API/projects/$PROJ_ID/git-branches?session_id=$WSESS_ID")
  assert_eq "$GB_CODE" "200" "GET git-branches with session_id → 200"

  # ── Files tree (falls back to project root) ──
  FT_CODE=$(http_status GET "$API/projects/$PROJ_ID/files?session_id=$WSESS_ID")
  assert_eq "$FT_CODE" "200" "GET /files with session_id → 200"

  # ── File content ──
  FC_CODE=$(http_status GET "$API/projects/$PROJ_ID/file-content?path=README.md&session_id=$WSESS_ID")
  assert_eq "$FC_CODE" "200" "GET /file-content with session_id → 200"

  # ── diff-stats ──
  DS_CODE=$(http_status GET "$API/projects/$PROJ_ID/diff-stats?session_id=$WSESS_ID")
  assert_eq "$DS_CODE" "200" "GET /diff-stats with session_id → 200"

  info "Full worktree creation (ensureWorktree) tested via unit tests requiring first message send"
fi

# ════════════════════════════════════════════════════════════════════
# 8. Session delete + worktree-status interaction
# ════════════════════════════════════════════════════════════════════
echo ""
echo "── 8. Session delete ───────────────────────────────────────────"

DEL_BODY=$(http_body POST "$API/sessions" '{"name":"wt-del"}')
DEL_ID=$(json_field "id" "$DEL_BODY")
http_status PUT "$API/sessions/$DEL_ID/worktree-mode" '{"mode":"worktree"}' >/dev/null

# Delete (soft-delete)
DEL_CODE=$(http_status DELETE "$API/sessions/$DEL_ID")
assert_eq "$DEL_CODE" "204" "DELETE session → 204"

# Should appear in trash
TRASH=$(http_body GET "$API/sessions/trash")
if echo "$TRASH" | grep -q "\"$DEL_ID\""; then pass "deleted session appears in trash"; else fail "deleted session not in trash"; fi

# Should no longer appear in active sessions
ACTIVE=$(http_body GET "$API/sessions")
if echo "$ACTIVE" | grep -q "\"$DEL_ID\""; then fail "deleted session still in active list"; else pass "deleted session gone from active list"; fi

# ════════════════════════════════════════════════════════════════════
# Summary
# ════════════════════════════════════════════════════════════════════
echo ""
echo "═══════════════════════════════════════════════════════════════"
if [[ $FAILED -eq 0 ]]; then
  echo -e "${GREEN}All tests passed!${NC}"
else
  echo -e "${RED}$FAILED test(s) FAILED${NC}"
fi
echo "═══════════════════════════════════════════════════════════════"
echo ""

exit $FAILED
