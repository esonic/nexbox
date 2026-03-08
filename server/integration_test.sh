#!/usr/bin/env bash
# integration_test.sh — Comprehensive black-box integration test for all BeeHub backend APIs.
#
# Usage:
#   ./integration_test.sh                                    # starts server, runs all tests
#   BASE_URL=http://127.0.0.1:9040 ./integration_test.sh    # run against existing server
#
# Requirements: bash, curl, python3 (for JWT generation + JSON parsing), git

set -euo pipefail

# ── colours ─────────────────────────────────────────────────────────────────
GREEN='\033[0;32m'; RED='\033[0;31m'; YELLOW='\033[0;33m'; CYAN='\033[0;36m'; NC='\033[0m'
pass() { echo -e "  ${GREEN}PASS${NC}  $1"; PASSED=$((PASSED + 1)); }
fail() { echo -e "  ${RED}FAIL${NC}  $1"; FAILED=$((FAILED + 1)); }
info() { echo -e "  ${YELLOW}INFO${NC}  $1"; }
section() { echo -e "\n${CYAN}── $1 ──${NC}"; }

PASSED=0
FAILED=0
TMPDIR_LOCAL=$(mktemp -d)
trap 'rm -rf "$TMPDIR_LOCAL"' EXIT

# ── server setup ────────────────────────────────────────────────────────────
SERVER_PID=""
if [[ -z "${BASE_URL:-}" ]]; then
  PORT=19042
  BASE_URL="http://127.0.0.1:$PORT"
  DATA_DIR="$TMPDIR_LOCAL/data"
  mkdir -p "$DATA_DIR"

  SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
  BINARY="$SCRIPT_DIR/beehub"
  if [[ ! -x "$BINARY" ]]; then
    echo "beehub binary not found at $BINARY — run build.sh first" >&2
    exit 1
  fi

  info "Starting server on :$PORT with data-dir=$DATA_DIR"
  "$BINARY" --port "$PORT" --data-dir "$DATA_DIR" --listen 127.0.0.1 \
    2>"$TMPDIR_LOCAL/server.log" &
  SERVER_PID=$!
  trap 'kill $SERVER_PID 2>/dev/null; rm -rf "$TMPDIR_LOCAL"' EXIT

  for i in $(seq 1 30); do
    if curl -sf "$BASE_URL/api/info" >/dev/null 2>&1; then break; fi
    sleep 0.3
    if [[ $i -eq 30 ]]; then
      echo "Server did not start in time. Log:" >&2
      cat "$TMPDIR_LOCAL/server.log" >&2
      exit 1
    fi
  done
  info "Server ready at $BASE_URL (PID=$SERVER_PID)"
fi

API="$BASE_URL/api"

# ── Generate JWT for test user ──────────────────────────────────────────────
# The server uses a well-known dev secret when JWTSecret config is empty.
JWT_SECRET="nexbox-dev-secret-change-in-production"
TEST_USER_ID="test-user-$(date +%s)"
TEST_USERNAME="testuser@integration.test"

generate_jwt() {
  python3 -c "
import json, hmac, hashlib, base64, time
header = base64.urlsafe_b64encode(json.dumps({'alg':'HS256','typ':'JWT'}).encode()).rstrip(b'=').decode()
payload = base64.urlsafe_b64encode(json.dumps({
    'user_id': '$TEST_USER_ID',
    'username': '$TEST_USERNAME',
    'role': 'admin',
    'features': 65535,
    'sub': '$TEST_USER_ID',
    'iat': int(time.time()),
    'exp': int(time.time()) + 86400,
}).encode()).rstrip(b'=').decode()
msg = f'{header}.{payload}'
sig = base64.urlsafe_b64encode(hmac.new(b'$JWT_SECRET', msg.encode(), hashlib.sha256).digest()).rstrip(b'=').decode()
print(f'{msg}.{sig}')
"
}

TOKEN=$(generate_jwt)
AUTH="Authorization: Bearer $TOKEN"
info "Generated JWT for user=$TEST_USER_ID"

# ── helpers ──────────────────────────────────────────────────────────────────
json_field() {
  local field="$1" json="$2"
  if command -v jq &>/dev/null; then
    echo "$json" | jq -r ".$field"
  else
    echo "$json" | python3 -c "
import sys,json
d=json.load(sys.stdin)
v=d.get('$field','')
if isinstance(v, bool):
    print(str(v).lower())
else:
    print(v)
" 2>/dev/null
  fi
}

# http_status <method> <url> [body]
http_status() {
  local method="$1" url="$2" body="${3:-}"
  if [[ -n "$body" ]]; then
    curl -s -o /dev/null -w "%{http_code}" -X "$method" -H "Content-Type: application/json" -H "$AUTH" -d "$body" "$url"
  else
    curl -s -o /dev/null -w "%{http_code}" -X "$method" -H "$AUTH" "$url"
  fi
}

# http_body <method> <url> [body]
http_body() {
  local method="$1" url="$2" body="${3:-}"
  if [[ -n "$body" ]]; then
    curl -s -X "$method" -H "Content-Type: application/json" -H "$AUTH" -d "$body" "$url"
  else
    curl -s -X "$method" -H "$AUTH" "$url"
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

# assert_contains <haystack> <needle> <label>
assert_contains() {
  if echo "$1" | grep -q "$2"; then pass "$3"; else fail "$3 (not found: '$2')"; fi
}

echo ""
echo "═══════════════════════════════════════════════════════════════════════"
echo " BeeHub Integration Tests  ($BASE_URL)"
echo "═══════════════════════════════════════════════════════════════════════"

# ════════════════════════════════════════════════════════════════════════════
# 1. Server info (public, no auth)
# ════════════════════════════════════════════════════════════════════════════
section "1. Server info (public)"

INFO_STATUS=$(curl -s -o /dev/null -w "%{http_code}" "$API/info")
assert_eq "$INFO_STATUS" "200" "GET /api/info → 200"

INFO_BODY=$(curl -s "$API/info")
assert_not_empty "$(json_field 'agent' "$INFO_BODY")" "/api/info has agent field"

# ════════════════════════════════════════════════════════════════════════════
# 2. Auth — unauthenticated requests rejected
# ════════════════════════════════════════════════════════════════════════════
section "2. Auth enforcement"

NOAUTH_STATUS=$(curl -s -o /dev/null -w "%{http_code}" "$API/sessions")
assert_eq "$NOAUTH_STATUS" "401" "GET /api/sessions without auth → 401"

# ════════════════════════════════════════════════════════════════════════════
# 3. Projects CRUD
# ════════════════════════════════════════════════════════════════════════════
section "3. Projects CRUD"

# Create project
PROJ_BODY=$(http_body POST "$API/projects" '{"name":"integ-test-proj","description":"test"}')
PROJ_ID=$(json_field "id" "$PROJ_BODY")
assert_not_empty "$PROJ_ID" "POST /api/projects → project ID"
assert_eq "$(json_field 'name' "$PROJ_BODY")" "integ-test-proj" "project name matches"

# List projects
LIST_BODY=$(http_body GET "$API/projects")
assert_contains "$LIST_BODY" "$PROJ_ID" "project appears in list"

# Get project
GET_PROJ=$(http_body GET "$API/projects/$PROJ_ID")
assert_eq "$(json_field 'id' "$GET_PROJ")" "$PROJ_ID" "GET /api/projects/:id returns correct project"

# Update project — set up a git repo for later tests
GIT_REPO="$TMPDIR_LOCAL/gitrepo"
mkdir -p "$GIT_REPO"
git -C "$GIT_REPO" init -q
git -C "$GIT_REPO" config user.email "test@test.com"
git -C "$GIT_REPO" config user.name "Test"
echo "# Integration Test" > "$GIT_REPO/README.md"
mkdir -p "$GIT_REPO/src"
echo 'package main; func main() {}' > "$GIT_REPO/src/main.go"
git -C "$GIT_REPO" add .
git -C "$GIT_REPO" commit -q -m "initial commit"

PUT_PROJ=$(http_body PUT "$API/projects/$PROJ_ID" "{\"git_repo_path\":\"$GIT_REPO\",\"description\":\"updated\"}")
assert_eq "$(json_field 'git_repo_path' "$PUT_PROJ")" "$GIT_REPO" "PUT project sets git_repo_path"
assert_eq "$(json_field 'description' "$PUT_PROJ")" "updated" "PUT project updates description"

# ════════════════════════════════════════════════════════════════════════════
# 4. Sessions CRUD
# ════════════════════════════════════════════════════════════════════════════
section "4. Sessions CRUD"

# Create session bound to project
SESS_BODY=$(http_body POST "$API/sessions" "{\"name\":\"test-session\",\"project_id\":\"$PROJ_ID\"}")
SESS_ID=$(json_field "id" "$SESS_BODY")
assert_not_empty "$SESS_ID" "POST /api/sessions → session ID"
assert_eq "$(json_field 'name' "$SESS_BODY")" "test-session" "session name matches"
assert_eq "$(json_field 'project_id' "$SESS_BODY")" "$PROJ_ID" "session bound to project"

# List sessions
LIST_SESS=$(http_body GET "$API/sessions")
assert_contains "$LIST_SESS" "$SESS_ID" "session appears in list"

# Get session
GET_SESS=$(http_body GET "$API/sessions/$SESS_ID")
assert_eq "$(json_field 'id' "$GET_SESS")" "$SESS_ID" "GET /api/sessions/:id correct"

# Update session
UPD_SESS=$(http_body PUT "$API/sessions/$SESS_ID" '{"name":"renamed-session"}')
assert_eq "$(json_field 'name' "$UPD_SESS")" "renamed-session" "PUT session renames"

# ════════════════════════════════════════════════════════════════════════════
# 5. Session model selection (T12)
# ════════════════════════════════════════════════════════════════════════════
section "5. Model selection (T12)"

MODEL_SESS=$(http_body POST "$API/sessions" "{\"name\":\"model-test\",\"project_id\":\"$PROJ_ID\",\"model\":\"claude-haiku-4-5\"}")
MODEL_SESS_ID=$(json_field "id" "$MODEL_SESS")
assert_eq "$(json_field 'model' "$MODEL_SESS")" "claude-haiku-4-5" "session created with model=claude-haiku-4-5"

GET_MODEL=$(http_body GET "$API/sessions/$MODEL_SESS_ID")
assert_eq "$(json_field 'model' "$GET_MODEL")" "claude-haiku-4-5" "GET session returns model"

# Update model
UPD_MODEL=$(http_body PUT "$API/sessions/$MODEL_SESS_ID" '{"model":"claude-sonnet-4-6"}')
assert_eq "$(json_field 'model' "$UPD_MODEL")" "claude-sonnet-4-6" "PUT updates model"

# ════════════════════════════════════════════════════════════════════════════
# 6. Session limits (T9.2) — max 20 per project
# ════════════════════════════════════════════════════════════════════════════
section "6. Session limit (T9.2)"

# We already have 2 sessions. Create 18 more to hit the limit.
for i in $(seq 3 20); do
  S=$(http_body POST "$API/sessions" "{\"name\":\"limit-$i\",\"project_id\":\"$PROJ_ID\"}")
  SID=$(json_field "id" "$S")
  if [[ -z "$SID" || "$SID" == "null" ]]; then
    fail "Failed to create session $i"
    break
  fi
done

# 21st session should fail with 409
LIMIT_STATUS=$(http_status POST "$API/sessions" "{\"name\":\"overflow\",\"project_id\":\"$PROJ_ID\"}")
assert_eq "$LIMIT_STATUS" "409" "21st session → 409 (limit reached)"

# ════════════════════════════════════════════════════════════════════════════
# 7. Session soft-delete & trash (T9.1)
# ════════════════════════════════════════════════════════════════════════════
section "7. Session trash & restore"

DEL_STATUS=$(http_status DELETE "$API/sessions/$MODEL_SESS_ID")
assert_eq "$DEL_STATUS" "204" "DELETE session → 204"

TRASH=$(http_body GET "$API/sessions/trash")
assert_contains "$TRASH" "$MODEL_SESS_ID" "deleted session in trash"

ACTIVE=$(http_body GET "$API/sessions")
if echo "$ACTIVE" | grep -q "$MODEL_SESS_ID"; then
  fail "deleted session still in active list"
else
  pass "deleted session not in active list"
fi

# Restore
RESTORE_STATUS=$(http_status POST "$API/sessions/$MODEL_SESS_ID/restore")
assert_eq "$RESTORE_STATUS" "200" "POST /restore → 200"

AFTER_RESTORE=$(http_body GET "$API/sessions/$MODEL_SESS_ID")
assert_not_empty "$(json_field 'id' "$AFTER_RESTORE")" "session accessible after restore"

# Delete again for limit headroom
http_status DELETE "$API/sessions/$MODEL_SESS_ID" >/dev/null

# ════════════════════════════════════════════════════════════════════════════
# 8. Worktree mode (T4)
# ════════════════════════════════════════════════════════════════════════════
section "8. Worktree mode"

WT_SET=$(http_body PUT "$API/sessions/$SESS_ID/worktree-mode" '{"mode":"worktree"}')
assert_eq "$(json_field 'worktree_mode' "$WT_SET")" "worktree" "set worktree mode"

WT_STATUS=$(http_body GET "$API/sessions/$SESS_ID/worktree-status")
assert_eq "$(json_field 'dirty' "$WT_STATUS")" "false" "worktree-status dirty=false"

# Reset
http_status PUT "$API/sessions/$SESS_ID/worktree-mode" '{"mode":"local"}' >/dev/null

# Error cases
ERR_STATUS=$(http_status PUT "$API/sessions/$SESS_ID/worktree-mode" '{"mode":"invalid"}')
assert_eq "$ERR_STATUS" "400" "invalid worktree mode → 400"

ERR_STATUS=$(http_status PUT "$API/sessions/does-not-exist/worktree-mode" '{"mode":"worktree"}')
assert_eq "$ERR_STATUS" "404" "worktree mode on missing session → 404"

# ════════════════════════════════════════════════════════════════════════════
# 9. File browser — list, read, search (existing) + upload, download (T7)
# ════════════════════════════════════════════════════════════════════════════
section "9. File browser & upload/download (T7)"

# List files
FILES_STATUS=$(http_status GET "$API/projects/$PROJ_ID/files")
assert_eq "$FILES_STATUS" "200" "GET /files → 200"

FILES_BODY=$(http_body GET "$API/projects/$PROJ_ID/files")
assert_contains "$FILES_BODY" "README.md" "file tree contains README.md"
assert_contains "$FILES_BODY" "src" "file tree contains src directory"

# Read file content
FC_BODY=$(http_body GET "$API/projects/$PROJ_ID/file-content?path=README.md")
assert_eq "$(json_field 'binary' "$FC_BODY")" "false" "README.md is not binary"
assert_contains "$(json_field 'content' "$FC_BODY")" "Integration Test" "README.md content matches"

# Search by name
SEARCH_BODY=$(http_body GET "$API/projects/$PROJ_ID/file-search?q=main&type=name")
assert_contains "$SEARCH_BODY" "main.go" "search by name finds main.go"

# Search by content
CSEARCH_BODY=$(http_body GET "$API/projects/$PROJ_ID/file-search?q=package+main&type=content")
assert_contains "$CSEARCH_BODY" "main.go" "search by content finds main.go"

# Upload file
UPLOAD_STATUS=$(curl -s -o /dev/null -w "%{http_code}" -X POST \
  -H "$AUTH" \
  -F "file=@$GIT_REPO/README.md" \
  -F "path=uploads" \
  "$API/projects/$PROJ_ID/files/upload")
assert_eq "$UPLOAD_STATUS" "200" "POST /files/upload → 200"

# Verify uploaded file exists
UPLOAD_CHECK=$(http_body GET "$API/projects/$PROJ_ID/file-content?path=uploads/README.md")
assert_eq "$(json_field 'binary' "$UPLOAD_CHECK")" "false" "uploaded file readable"

# Download file
DL_STATUS=$(curl -s -o /dev/null -w "%{http_code}" -H "$AUTH" \
  "$API/projects/$PROJ_ID/files/download?path=README.md")
assert_eq "$DL_STATUS" "200" "GET /files/download → 200"

# Path traversal protection
TRAVERSAL_STATUS=$(http_status GET "$API/projects/$PROJ_ID/file-content?path=../../etc/passwd")
assert_eq "$TRAVERSAL_STATUS" "400" "path traversal blocked"

# ════════════════════════════════════════════════════════════════════════════
# 10. Git operations
# ════════════════════════════════════════════════════════════════════════════
section "10. Git operations"

GS_STATUS=$(http_status GET "$API/projects/$PROJ_ID/git-status")
assert_eq "$GS_STATUS" "200" "GET /git-status → 200"

GL_STATUS=$(http_status GET "$API/projects/$PROJ_ID/git-log?limit=5")
assert_eq "$GL_STATUS" "200" "GET /git-log → 200"

GL_BODY=$(http_body GET "$API/projects/$PROJ_ID/git-log?limit=5")
assert_contains "$GL_BODY" "initial commit" "git log contains initial commit"

GB_STATUS=$(http_status GET "$API/projects/$PROJ_ID/git-branches")
assert_eq "$GB_STATUS" "200" "GET /git-branches → 200"

DS_STATUS=$(http_status GET "$API/projects/$PROJ_ID/diff-stats")
assert_eq "$DS_STATUS" "200" "GET /diff-stats → 200"

# Stage, commit, and verify
echo "new file" > "$GIT_REPO/new.txt"
STAGE_STATUS=$(http_status POST "$API/projects/$PROJ_ID/git-stage" '{"file_paths":["new.txt"]}')
assert_eq "$STAGE_STATUS" "204" "POST /git-stage → 204"

COMMIT_STATUS=$(http_status POST "$API/projects/$PROJ_ID/git-commit" '{"message":"add new.txt","file_paths":["new.txt"]}')
assert_eq "$COMMIT_STATUS" "200" "POST /git-commit → 200"

GL_AFTER=$(http_body GET "$API/projects/$PROJ_ID/git-log?limit=5")
assert_contains "$GL_AFTER" "add new.txt" "git log contains new commit"

# ════════════════════════════════════════════════════════════════════════════
# 11. Marketplace — local plugins (T1)
# ════════════════════════════════════════════════════════════════════════════
section "11. Marketplace — local plugins (T1)"

# Create marketplace dir inside DATA_DIR (server falls back to {DataDir}/marketplace)
MKTPL_DIR="$DATA_DIR/marketplace"
mkdir -p "$MKTPL_DIR"
cat > "$MKTPL_DIR/test-mcp.json" <<'PLUGIN_EOF'
{
  "id": "test-mcp-server",
  "name": "Test MCP Server",
  "description": "A test MCP server for integration testing",
  "type": "mcp",
  "version": "1.0.0",
  "command": "echo",
  "args": ["hello"],
  "env_vars": ["API_KEY"]
}
PLUGIN_EOF

# Create a test skill with SKILL.md format
mkdir -p "$MKTPL_DIR/skills/test-skill"
cat > "$MKTPL_DIR/skills/test-skill/SKILL.md" <<'SKILL_EOF'
---
name: test-skill
description: A test skill for integration
version: 1.0.0
---
# Test Skill
This is a test skill.
SKILL_EOF

# List plugins
PLUGINS_BODY=$(http_body GET "$API/marketplace/plugins")
PLUGINS_STATUS=$(http_status GET "$API/marketplace/plugins")
assert_eq "$PLUGINS_STATUS" "200" "GET /marketplace/plugins → 200"
# Check if our test plugin is in the list
if echo "$PLUGINS_BODY" | python3 -c "import sys,json; items=json.load(sys.stdin); exit(0 if any(p.get('id')=='test-mcp-server' for p in items) else 1)" 2>/dev/null; then
  pass "test-mcp-server appears in plugin list (JSON format)"
else
  fail "test-mcp-server not found in plugin list"
fi

# Check if SKILL.md format works
if echo "$PLUGINS_BODY" | python3 -c "import sys,json; items=json.load(sys.stdin); exit(0 if any(p.get('id')=='skill-test-skill' for p in items) else 1)" 2>/dev/null; then
  pass "skill-test-skill appears in plugin list (SKILL.md format)"
else
  fail "skill-test-skill not found in plugin list"
fi

# Get single plugin
PLUGIN_BODY=$(http_body GET "$API/marketplace/plugins/test-mcp-server")
assert_eq "$(json_field 'id' "$PLUGIN_BODY")" "test-mcp-server" "GET /marketplace/plugins/:id correct"
assert_eq "$(json_field 'type' "$PLUGIN_BODY")" "mcp" "plugin type is mcp"

# Install plugin
INSTALL_STATUS=$(http_status POST "$API/marketplace/installed" '{"plugin_id":"test-mcp-server","config":"{}"}')
assert_eq "$INSTALL_STATUS" "201" "POST /marketplace/installed → 201"

# List installed
INSTALLED_BODY=$(http_body GET "$API/marketplace/installed")
assert_contains "$INSTALLED_BODY" "test-mcp-server" "test-mcp-server in installed list"

# Uninstall
UNINSTALL_STATUS=$(http_status DELETE "$API/marketplace/installed/test-mcp-server")
if [[ "$UNINSTALL_STATUS" == "200" || "$UNINSTALL_STATUS" == "204" ]]; then
  pass "DELETE /marketplace/installed/:id → $UNINSTALL_STATUS"
else
  fail "DELETE /marketplace/installed/:id → $UNINSTALL_STATUS (expected 200 or 204)"
fi

# Installed MCPs
MCPS_STATUS=$(http_status GET "$API/marketplace/installed-mcps")
assert_eq "$MCPS_STATUS" "200" "GET /marketplace/installed-mcps → 200"

# Installed skills
SKILLS_STATUS=$(http_status GET "$API/marketplace/installed-skills")
assert_eq "$SKILLS_STATUS" "200" "GET /marketplace/installed-skills → 200"

# ════════════════════════════════════════════════════════════════════════════
# 12. Balance endpoint (T11)
# ════════════════════════════════════════════════════════════════════════════
section "12. Balance (T11)"

BAL_STATUS=$(http_status GET "$API/balance")
# Balance may return 200 with zeros or 500 if cloud not configured
if [[ "$BAL_STATUS" == "200" ]]; then
  pass "GET /balance → 200"
else
  info "GET /balance → $BAL_STATUS (cloud mode may not be active)"
fi

# ════════════════════════════════════════════════════════════════════════════
# 13. Message endpoints
# ════════════════════════════════════════════════════════════════════════════
section "13. Messages"

MSGS_STATUS=$(http_status GET "$API/sessions/$SESS_ID/messages")
assert_eq "$MSGS_STATUS" "200" "GET /sessions/:id/messages → 200"

MSGS_BODY=$(http_body GET "$API/sessions/$SESS_ID/messages")
# Should be an empty array or object
assert_not_empty "$MSGS_BODY" "messages endpoint returns data"

# ════════════════════════════════════════════════════════════════════════════
# 14. Message queue
# ════════════════════════════════════════════════════════════════════════════
section "14. Message queue"

Q_LIST=$(http_status GET "$API/sessions/$SESS_ID/queue")
assert_eq "$Q_LIST" "200" "GET /sessions/:id/queue → 200"

Q_CREATE=$(http_body POST "$API/sessions/$SESS_ID/queue" '{"text":"queued message test"}')
Q_ID=$(json_field "id" "$Q_CREATE")
if [[ -n "$Q_ID" && "$Q_ID" != "null" ]]; then
  pass "POST /sessions/:id/queue → created"

  Q_UPDATE_STATUS=$(http_status PUT "$API/sessions/$SESS_ID/queue/$Q_ID" '{"text":"updated queued message"}')
  assert_eq "$Q_UPDATE_STATUS" "204" "PUT /queue/:id → 204"

  Q_DELETE_STATUS=$(http_status DELETE "$API/sessions/$SESS_ID/queue/$Q_ID")
  assert_eq "$Q_DELETE_STATUS" "204" "DELETE /queue/:id → 204"
else
  fail "POST /sessions/:id/queue failed"
fi

# ════════════════════════════════════════════════════════════════════════════
# 15. Image upload
# ════════════════════════════════════════════════════════════════════════════
section "15. Image upload"

# Create a tiny PNG (1x1 pixel)
printf '\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x02\x00\x00\x00\x90wS\xde\x00\x00\x00\x0cIDATx\x9cc\xf8\x0f\x00\x00\x01\x01\x00\x05\x18\xd8N\x00\x00\x00\x00IEND\xaeB`\x82' > "$TMPDIR_LOCAL/test.png"

IMG_STATUS=$(curl -s -o /dev/null -w "%{http_code}" -X POST \
  -H "$AUTH" \
  -F "image=@$TMPDIR_LOCAL/test.png;type=image/png" \
  "$API/sessions/$SESS_ID/upload-image")
assert_eq "$IMG_STATUS" "200" "POST /upload-image → 200"

# ════════════════════════════════════════════════════════════════════════════
# 16. Settings & utility endpoints
# ════════════════════════════════════════════════════════════════════════════
section "16. Settings & utility"

SETTINGS_STATUS=$(http_status GET "$API/settings")
assert_eq "$SETTINGS_STATUS" "200" "GET /settings → 200"

AGENTS_STATUS=$(http_status GET "$API/available-agents")
assert_eq "$AGENTS_STATUS" "200" "GET /available-agents → 200"

AGENTS_BODY=$(http_body GET "$API/available-agents")
assert_contains "$AGENTS_BODY" "claude" "available agents include claude"

# ════════════════════════════════════════════════════════════════════════════
# 17. Changes (audit trail)
# ════════════════════════════════════════════════════════════════════════════
section "17. Changes"

CHANGES_STATUS=$(http_status GET "$API/changes")
assert_eq "$CHANGES_STATUS" "200" "GET /changes → 200"

# ════════════════════════════════════════════════════════════════════════════
# 18. Tasks CRUD
# ════════════════════════════════════════════════════════════════════════════
section "18. Tasks CRUD"

TASK_BODY=$(http_body POST "$API/tasks" "{\"title\":\"Test Task\",\"project_id\":\"$PROJ_ID\",\"description\":\"integration test task\"}")
TASK_ID=$(json_field "id" "$TASK_BODY")
assert_not_empty "$TASK_ID" "POST /tasks → task ID"

TASK_GET=$(http_body GET "$API/tasks/$TASK_ID")
assert_eq "$(json_field 'title' "$TASK_GET")" "Test Task" "GET /tasks/:id correct"

TASK_LIST=$(http_body GET "$API/tasks?project_id=$PROJ_ID")
assert_contains "$TASK_LIST" "$TASK_ID" "task in list"

TASK_UPD=$(http_body PUT "$API/tasks/$TASK_ID" '{"title":"Updated Task","status":"in_progress"}')
assert_eq "$(json_field 'title' "$TASK_UPD")" "Updated Task" "PUT /tasks/:id updates title"

# Task comments
TC_BODY=$(http_body POST "$API/tasks/$TASK_ID/comments" '{"body":"test comment"}')
TC_ID=$(json_field "id" "$TC_BODY")
assert_not_empty "$TC_ID" "POST /tasks/:id/comments → comment ID"

TC_LIST_STATUS=$(http_status GET "$API/tasks/$TASK_ID/comments")
assert_eq "$TC_LIST_STATUS" "200" "GET /tasks/:id/comments → 200"

# Labels
LBL_BODY=$(http_body POST "$API/projects/$PROJ_ID/labels" '{"name":"bug","color":"#ff0000"}')
LBL_ID=$(json_field "id" "$LBL_BODY")
if [[ -n "$LBL_ID" && "$LBL_ID" != "null" ]]; then
  pass "POST /labels → label created"
  ADD_LBL_STATUS=$(http_status POST "$API/tasks/$TASK_ID/labels" "{\"name\":\"bug\"}")
  assert_eq "$ADD_LBL_STATUS" "204" "POST /tasks/:id/labels → 204"
else
  info "Label creation returned: $(echo "$LBL_BODY" | head -c 100)"
fi

# Delete task
DEL_TASK_STATUS=$(http_status DELETE "$API/tasks/$TASK_ID")
assert_eq "$DEL_TASK_STATUS" "204" "DELETE /tasks/:id → 204"

# ════════════════════════════════════════════════════════════════════════════
# 19. Assets CRUD
# ════════════════════════════════════════════════════════════════════════════
section "19. Assets CRUD"

ASSET_BODY=$(http_body POST "$API/projects/$PROJ_ID/assets" '{"title":"Test Asset","content":"asset content","type":"doc"}')
ASSET_ID=$(json_field "id" "$ASSET_BODY")
assert_not_empty "$ASSET_ID" "POST /assets → asset ID"

ASSET_GET=$(http_body GET "$API/assets/$ASSET_ID")
assert_eq "$(json_field 'title' "$ASSET_GET")" "Test Asset" "GET /assets/:id correct"

ASSET_UPD=$(http_body PUT "$API/assets/$ASSET_ID" '{"title":"Updated Asset","content":"updated content"}')
assert_eq "$(json_field 'title' "$ASSET_UPD")" "Updated Asset" "PUT /assets/:id updates"

# Versions
VER_STATUS=$(http_status GET "$API/assets/$ASSET_ID/versions")
assert_eq "$VER_STATUS" "200" "GET /assets/:id/versions → 200"

# Delete
DEL_ASSET_STATUS=$(http_status DELETE "$API/assets/$ASSET_ID")
assert_eq "$DEL_ASSET_STATUS" "204" "DELETE /assets/:id → 204"

# ════════════════════════════════════════════════════════════════════════════
# 20. Reactions
# ════════════════════════════════════════════════════════════════════════════
section "20. Reactions"

# Create a task for reactions
RXN_TASK=$(http_body POST "$API/tasks" "{\"title\":\"Reaction Test\",\"project_id\":\"$PROJ_ID\"}")
RXN_TASK_ID=$(json_field "id" "$RXN_TASK")

if [[ -n "$RXN_TASK_ID" && "$RXN_TASK_ID" != "null" ]]; then
  ADD_RXN_STATUS=$(http_status POST "$API/reactions/task/$RXN_TASK_ID" '{"emoji":"👍"}')
  assert_eq "$ADD_RXN_STATUS" "204" "POST /reactions → 204"

  GET_RXN_STATUS=$(http_status GET "$API/reactions/task/$RXN_TASK_ID")
  assert_eq "$GET_RXN_STATUS" "200" "GET /reactions → 200"

  # DELETE /reactions uses query param ?emoji=, not JSON body
  DEL_RXN_STATUS=$(curl -s -o /dev/null -w "%{http_code}" -X DELETE -H "$AUTH" \
    "$API/reactions/task/$RXN_TASK_ID?emoji=%F0%9F%91%8D")
  assert_eq "$DEL_RXN_STATUS" "204" "DELETE /reactions → 204"

  http_status DELETE "$API/tasks/$RXN_TASK_ID" >/dev/null
fi

# ════════════════════════════════════════════════════════════════════════════
# 21. Project deletion cleanup (T3.4 + T9.4)
# ════════════════════════════════════════════════════════════════════════════
section "21. Project deletion (T3.4 + T9.4)"

# Create a project with a managed workspace to test cleanup
CLEANUP_DIR="$DATA_DIR/workspaces/cleanup-test"
mkdir -p "$CLEANUP_DIR"
echo "test" > "$CLEANUP_DIR/file.txt"

CP_BODY=$(http_body POST "$API/projects" '{"name":"cleanup-test"}')
CP_ID=$(json_field "id" "$CP_BODY")
http_body PUT "$API/projects/$CP_ID" "{\"git_repo_path\":\"$CLEANUP_DIR\"}" >/dev/null

# Delete
DEL_PROJ_STATUS=$(http_status DELETE "$API/projects/$CP_ID")
assert_eq "$DEL_PROJ_STATUS" "204" "DELETE /projects/:id → 204"

# Check workspace was cleaned up
if [[ -d "$CLEANUP_DIR" ]]; then
  fail "workspace dir should be removed after project deletion"
else
  pass "workspace dir cleaned up after project deletion"
fi

# ════════════════════════════════════════════════════════════════════════════
# 22. Error handling — various 404s
# ════════════════════════════════════════════════════════════════════════════
section "22. Error handling"

assert_eq "$(http_status GET "$API/sessions/nonexistent")" "404" "GET nonexistent session → 404"
assert_eq "$(http_status GET "$API/projects/nonexistent")" "404" "GET nonexistent project → 404"
assert_eq "$(http_status GET "$API/tasks/nonexistent")" "404" "GET nonexistent task → 404"
assert_eq "$(http_status GET "$API/projects/$PROJ_ID/file-content?path=nonexistent.txt")" "404" "GET nonexistent file → 404"

# ════════════════════════════════════════════════════════════════════════════
# Cleanup — delete the main test project (and all its sessions)
# ════════════════════════════════════════════════════════════════════════════
section "Cleanup"

DEL_MAIN=$(http_status DELETE "$API/projects/$PROJ_ID")
info "Deleted main test project: status=$DEL_MAIN"

# ════════════════════════════════════════════════════════════════════════════
# Summary
# ════════════════════════════════════════════════════════════════════════════
echo ""
echo "═══════════════════════════════════════════════════════════════════════"
TOTAL=$((PASSED + FAILED))
echo -e " Results: ${GREEN}$PASSED passed${NC}, ${RED}$FAILED failed${NC}, $TOTAL total"
if [[ $FAILED -eq 0 ]]; then
  echo -e " ${GREEN}All tests passed!${NC}"
else
  echo -e " ${RED}$FAILED test(s) FAILED${NC}"
  if [[ -n "$SERVER_PID" && -f "$TMPDIR_LOCAL/server.log" ]]; then
    echo ""
    echo "Server log (last 30 lines):"
    tail -30 "$TMPDIR_LOCAL/server.log"
  fi
fi
echo "═══════════════════════════════════════════════════════════════════════"
echo ""

exit $FAILED
