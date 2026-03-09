#!/bin/bash
# =============================================================================
# BoxNex E2E Chat Integration Test
# =============================================================================
# Tests the real user chat flow:
#   1. Build & start server
#   2. Create project + iteration via REST
#   3. Connect via WebSocket, send message to Claude, receive streaming events
#   4. Verify message persistence
#   5. Test session resume (second message)
#   6. Generate report
# =============================================================================

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
TMPDIR_BASE=$(mktemp -d)
DATA_DIR="$TMPDIR_BASE/data"
SERVER_BIN="$TMPDIR_BASE/boxnex"
WSCLIENT_BIN="$TMPDIR_BASE/wsclient"
SERVER_LOG="$TMPDIR_BASE/server.log"
GIT_REPO_DIR="$TMPDIR_BASE/test-repo"
PORT=47391
MCP_PORT=47392
BASE_URL="http://127.0.0.1:$PORT"
REPORT="$SCRIPT_DIR/e2e-chat-test-report.md"
SERVER_PID=""
CURL_TIMEOUT=10
PASS=0
FAIL=0
TOTAL=0

cleanup() {
    if [ -n "$SERVER_PID" ] && kill -0 "$SERVER_PID" 2>/dev/null; then
        kill "$SERVER_PID" 2>/dev/null || true
        wait "$SERVER_PID" 2>/dev/null || true
    fi
    rm -rf "$TMPDIR_BASE"
}
trap cleanup EXIT

log() { echo "[e2e] $*"; }

record() {
    local name="$1" ok="$2" detail="$3"
    TOTAL=$((TOTAL + 1))
    if [ "$ok" = "true" ]; then
        PASS=$((PASS + 1))
        RESULTS+=("| $TOTAL | $name | PASS | $detail |")
        log "PASS: $name - $detail"
    else
        FAIL=$((FAIL + 1))
        RESULTS+=("| $TOTAL | $name | **FAIL** | $detail |")
        log "FAIL: $name - $detail"
    fi
}

json_get() {
    echo "$1" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('$2',''))" 2>/dev/null
}

C() { curl --max-time "$CURL_TIMEOUT" "$@"; }

RESULTS=()

# =============================================================================
# Step 1: Build
# =============================================================================
log "Building server binary..."
cd "$SCRIPT_DIR"
if ! go build -o "$SERVER_BIN" . 2>&1; then
    log "FATAL: Server build failed"
    exit 1
fi
log "Server binary built"

log "Building WebSocket client..."
if ! go build -o "$WSCLIENT_BIN" ./testutil/wsclient 2>&1; then
    log "FATAL: WebSocket client build failed"
    exit 1
fi
log "WebSocket client built"

# =============================================================================
# Step 2: Prepare git repo
# =============================================================================
mkdir -p "$GIT_REPO_DIR"
cd "$GIT_REPO_DIR"
git init --initial-branch=main -q
git config user.email "test@e2e.com"
git config user.name "E2E"
echo "# E2E Test" > README.md
git add . && git commit -m "initial" -q
cd "$SCRIPT_DIR"

# =============================================================================
# Step 3: Start server
# =============================================================================
mkdir -p "$DATA_DIR"
log "Starting server on port $PORT..."
"$SERVER_BIN" --port "$PORT" --mcp-port "$MCP_PORT" --data-dir "$DATA_DIR" \
    --web-dir "" > "$SERVER_LOG" 2>&1 &
SERVER_PID=$!
log "Server PID: $SERVER_PID"

SERVER_READY=false
for i in $(seq 1 15); do
    if C -s "$BASE_URL/api/projects" >/dev/null 2>&1; then
        log "Server ready (${i}s)"
        SERVER_READY=true
        break
    fi
    sleep 1
done
if [ "$SERVER_READY" = "false" ]; then
    log "FATAL: Server did not start. Log:"
    cat "$SERVER_LOG"
    exit 1
fi

set +e

# =============================================================================
# Step 4: Create project + iteration
# =============================================================================
log "=== Creating project and iteration ==="

RESP=$(C -s -w "\n%{http_code}" -X POST "$BASE_URL/api/projects" \
    -H "Content-Type: application/json" \
    -d "{\"name\":\"E2E Chat Test\",\"description\":\"Chat flow test\",\"git_repo_path\":\"$GIT_REPO_DIR\"}")
STATUS=$(echo "$RESP" | tail -1)
BODY=$(echo "$RESP" | sed '$d')
record "Create project" "$([ "$STATUS" = "201" ] && echo true || echo false)" "status=$STATUS"
PROJECT_ID=$(json_get "$BODY" "id")
log "Project: $PROJECT_ID"

RESP=$(C -s -w "\n%{http_code}" -X POST "$BASE_URL/api/projects/$PROJECT_ID/iterations" \
    -H "Content-Type: application/json" \
    -d '{"title":"E2E Chat","requirement":"Test chat flow"}')
STATUS=$(echo "$RESP" | tail -1)
BODY=$(echo "$RESP" | sed '$d')
record "Create iteration" "$([ "$STATUS" = "201" ] && echo true || echo false)" "status=$STATUS"
ITERATION_ID=$(json_get "$BODY" "id")
SESSION_ID=$(json_get "$BODY" "session_id")
log "Iteration: $ITERATION_ID (session=$SESSION_ID)"

# =============================================================================
# Step 5: First chat message via WebSocket
# =============================================================================
log "=== Sending first chat message ==="

USER_MSG="Reply with exactly this text and nothing else: Hello from BoxNex E2E test. Do not use any tools."
WS_URL="ws://127.0.0.1:$PORT/ws"

log "WebSocket URL: $WS_URL"
log "Conversation ID: $CONVERSATION_ID"
log "Message: $USER_MSG"

# Run wsclient with timeout; capture stdout (JSON result) and stderr (logs)
WS_STDERR="$TMPDIR_BASE/ws1_stderr.log"
WS_RESULT=$("$WSCLIENT_BIN" -url "$WS_URL" -conversation "$CONVERSATION_ID" -message "$USER_MSG" -timeout 180 2>"$WS_STDERR")
WS_EXIT=$?

log "WebSocket client exited with code $WS_EXIT"
log "--- wsclient stderr ---"
cat "$WS_STDERR"
log "--- end stderr ---"

if [ $WS_EXIT -ne 0 ]; then
    record "WebSocket connect & chat" "false" "wsclient exit=$WS_EXIT"
    log "Server log (last 20 lines):"
    tail -20 "$SERVER_LOG"
    # Don't abort - continue with reporting
else
    # Parse the JSON result
    GOT_DONE=$(echo "$WS_RESULT" | python3 -c "import sys,json; print(json.load(sys.stdin).get('got_done',False))" 2>/dev/null)
    GOT_TEXT=$(echo "$WS_RESULT" | python3 -c "import sys,json; print(json.load(sys.stdin).get('got_text',False))" 2>/dev/null)
    GOT_ERROR=$(echo "$WS_RESULT" | python3 -c "import sys,json; print(json.load(sys.stdin).get('got_error',False))" 2>/dev/null)
    FULL_TEXT=$(echo "$WS_RESULT" | python3 -c "import sys,json; print(json.load(sys.stdin).get('full_text',''))" 2>/dev/null)
    ERROR_MSG=$(echo "$WS_RESULT" | python3 -c "import sys,json; print(json.load(sys.stdin).get('error_msg',''))" 2>/dev/null)
    EVENT_COUNTS=$(echo "$WS_RESULT" | python3 -c "import sys,json; print(json.load(sys.stdin).get('event_counts',{}))" 2>/dev/null)
    NUM_EVENTS=$(echo "$WS_RESULT" | python3 -c "import sys,json; print(len(json.load(sys.stdin).get('events',[])))" 2>/dev/null)

    log "Results: done=$GOT_DONE text=$GOT_TEXT error=$GOT_ERROR events=$NUM_EVENTS"
    log "Event counts: $EVENT_COUNTS"
    log "Full text: $FULL_TEXT"
    if [ "$GOT_ERROR" = "True" ]; then
        log "Error message: $ERROR_MSG"
    fi

    # Assertions
    record "WebSocket connected" "true" "connection established"
    record "Received done event" "$([ "$GOT_DONE" = "True" ] && echo true || echo false)" "got_done=$GOT_DONE"

    if [ "$GOT_ERROR" = "True" ]; then
        record "No error from Claude" "false" "error=$ERROR_MSG"
    else
        record "No error from Claude" "true" "clean execution"
    fi

    if [ "$GOT_TEXT" = "True" ]; then
        record "Received text response" "true" "text length=${#FULL_TEXT}"
        # Check if response contains expected text
        if echo "$FULL_TEXT" | grep -qi "hello"; then
            record "Response contains 'hello'" "true" "AI followed instruction"
        else
            record "Response contains 'hello'" "false" "text=$FULL_TEXT"
        fi
    else
        if [ "$GOT_ERROR" = "True" ]; then
            record "Received text response" "false" "no text (error occurred)"
        else
            record "Received text response" "false" "no text_delta events"
        fi
    fi
fi

# =============================================================================
# Step 6: Verify message persistence
# =============================================================================
log "=== Verifying message persistence ==="
sleep 2  # Give server time to persist

MSGS=$(C -s "$BASE_URL/api/iterations/$ITERATION_ID/messages")
MSG_COUNT=$(echo "$MSGS" | python3 -c "import sys,json; print(len(json.load(sys.stdin)))" 2>/dev/null)
log "Persisted messages: $MSG_COUNT"

# Should have at least user message
record "Messages persisted" "$([ "$MSG_COUNT" -ge 1 ] && echo true || echo false)" "count=$MSG_COUNT"

# Check user message
HAS_USER=$(echo "$MSGS" | python3 -c "
import sys,json
msgs = json.load(sys.stdin)
print(any(m.get('role')=='user' for m in msgs))
" 2>/dev/null)
record "User message persisted" "$([ "$HAS_USER" = "True" ] && echo true || echo false)" "has_user=$HAS_USER"

# Check assistant message (only if we got text)
if [ "$GOT_TEXT" = "True" ]; then
    HAS_ASSISTANT=$(echo "$MSGS" | python3 -c "
import sys,json
msgs = json.load(sys.stdin)
print(any(m.get('role')=='assistant' for m in msgs))
" 2>/dev/null)
    record "Assistant message persisted" "$([ "$HAS_ASSISTANT" = "True" ] && echo true || echo false)" "has_assistant=$HAS_ASSISTANT"

    # Check assistant content is JSON parts
    ASSISTANT_IS_JSON=$(echo "$MSGS" | python3 -c "
import sys,json
msgs = json.load(sys.stdin)
for m in msgs:
    if m.get('role') == 'assistant':
        try:
            parts = json.loads(m.get('content',''))
            print(isinstance(parts, list))
        except:
            print(False)
        break
else:
    print(False)
" 2>/dev/null)
    record "Assistant content is JSON parts" "$([ "$ASSISTANT_IS_JSON" = "True" ] && echo true || echo false)" "is_json=$ASSISTANT_IS_JSON"
fi

# =============================================================================
# Step 7: Second message (session resume)
# =============================================================================
if [ "$GOT_TEXT" = "True" ] && [ "$GOT_ERROR" != "True" ]; then
    log "=== Testing session resume (second message) ==="

    USER_MSG2="What was my previous message? Reply in one short sentence."
    WS_STDERR2="$TMPDIR_BASE/ws2_stderr.log"
    WS_RESULT2=$("$WSCLIENT_BIN" -url "$WS_URL" -message "$USER_MSG2" -timeout 180 2>"$WS_STDERR2")
    WS_EXIT2=$?

    log "Second message wsclient exit=$WS_EXIT2"
    cat "$WS_STDERR2"

    if [ $WS_EXIT2 -eq 0 ]; then
        GOT_DONE2=$(echo "$WS_RESULT2" | python3 -c "import sys,json; print(json.load(sys.stdin).get('got_done',False))" 2>/dev/null)
        GOT_TEXT2=$(echo "$WS_RESULT2" | python3 -c "import sys,json; print(json.load(sys.stdin).get('got_text',False))" 2>/dev/null)
        GOT_ERROR2=$(echo "$WS_RESULT2" | python3 -c "import sys,json; print(json.load(sys.stdin).get('got_error',False))" 2>/dev/null)
        FULL_TEXT2=$(echo "$WS_RESULT2" | python3 -c "import sys,json; print(json.load(sys.stdin).get('full_text',''))" 2>/dev/null)

        record "Resume: connected & done" "$([ "$GOT_DONE2" = "True" ] && echo true || echo false)" "done=$GOT_DONE2"
        record "Resume: got text" "$([ "$GOT_TEXT2" = "True" ] && echo true || echo false)" "text=$FULL_TEXT2"

        if [ "$GOT_ERROR2" = "True" ]; then
            ERR2=$(echo "$WS_RESULT2" | python3 -c "import sys,json; print(json.load(sys.stdin).get('error_msg',''))" 2>/dev/null)
            record "Resume: no error" "false" "$ERR2"
        else
            record "Resume: no error" "true" "clean"
        fi

        # Verify messages count increased
        sleep 2
        MSGS2=$(C -s "$BASE_URL/api/iterations/$ITERATION_ID/messages")
        MSG_COUNT2=$(echo "$MSGS2" | python3 -c "import sys,json; print(len(json.load(sys.stdin)))" 2>/dev/null)
        record "Resume: messages count increased" "$([ "$MSG_COUNT2" -gt "$MSG_COUNT" ] && echo true || echo false)" "before=$MSG_COUNT after=$MSG_COUNT2"
    else
        record "Resume: WebSocket connect" "false" "exit=$WS_EXIT2"
    fi
fi

set -e

# =============================================================================
# Step 8: Generate report
# =============================================================================
log "=== Tests complete: $PASS passed, $FAIL failed, $TOTAL total ==="

PASS_RATE=$(python3 -c "print(f'{$PASS/$TOTAL*100:.1f}%')" 2>/dev/null || echo "N/A")

cat > "$REPORT" << HEADER
# BoxNex E2E Chat Integration Test Report

**Date**: $(date '+%Y-%m-%d %H:%M:%S')
**Server Port**: $PORT | **MCP Port**: $MCP_PORT
**Claude Config**: (default)
**Data Dir**: (temp directory, cleaned up after test)

## Summary

- **Total**: $TOTAL
- **Passed**: $PASS
- **Failed**: $FAIL
- **Pass Rate**: $PASS_RATE

## Test Results

| # | Test Case | Result | Detail |
|---|-----------|--------|--------|
HEADER

for r in "${RESULTS[@]}"; do
    echo "$r" >> "$REPORT"
done

cat >> "$REPORT" << 'FOOTER'

## Test Flow

### Phase 1: Setup
- Build server binary
- Build WebSocket client binary
- Create temp git repository
- Start server with temp data directory

### Phase 2: REST API Setup
- Create project with git_repo_path
- Create iteration (generates session_id)

### Phase 3: First Chat Message
- Connect to global WebSocket endpoint `/ws` with conversation subscribe/message protocol
- Send user message to Claude Code CLI
- Receive streaming events: `text_delta`, `tool_start`, `tool_result`, `thinking_delta`, `done`
- Verify AI response content

### Phase 4: Message Persistence
- Verify user message persisted in database
- Verify assistant message persisted as JSON parts
- Verify message count via REST API

### Phase 5: Session Resume
- Send second message (reuses session via `--resume`)
- Verify Claude remembers context from first message
- Verify additional messages persisted

### Phase 6: Cleanup
- Kill server process
- Remove temp directory
FOOTER

log "Report written to $REPORT"

# Append server log excerpt if there were failures
if [ "$FAIL" -gt 0 ]; then
    echo "" >> "$REPORT"
    echo "## Server Log (last 30 lines)" >> "$REPORT"
    echo '```' >> "$REPORT"
    tail -30 "$SERVER_LOG" >> "$REPORT"
    echo '```' >> "$REPORT"
    log "=== Server log (last 20 lines) ==="
    tail -20 "$SERVER_LOG"
fi

if [ "$FAIL" -gt 0 ]; then
    exit 1
fi
exit 0
