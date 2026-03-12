#!/bin/bash
# inject/rotate.sh — rotate the CLAUDE_CODE_OAUTH_TOKEN baked into inject.so
#
# Usage:
#   sudo inject/rotate.sh <new_token>
#   BOXNEX_TOKEN=<new_token> sudo -E inject/rotate.sh
#
# What it does:
#   1. XOR-encodes the token (key = 0x5A, same as inject.c)
#   2. Compiles a temp copy of inject.c with the real enc_token[] (inject.c itself is never modified)
#   3. Installs the result to /opt/boxnex/inject.so
#   4. Deletes the temp file — the token never lands on disk as plaintext
#
# Running containers pick up the new inject.so automatically on their next exec call
# because inject.so is bind-mounted at runtime, not baked into the image.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
INJECT_C="${SCRIPT_DIR}/inject.c"
INSTALL_PATH="/opt/boxnex/inject.so"
XOR_KEY=90  # 0x5A

# ── Resolve token ─────────────────────────────────────────────────────────────
TOKEN="${1:-${BOXNEX_TOKEN:-}}"
if [ -z "$TOKEN" ]; then
  echo "Usage: $0 <token>" >&2
  echo "   or: BOXNEX_TOKEN=<token> $0" >&2
  exit 1
fi

# ── XOR-encode token into a C array literal ───────────────────────────────────
encode_token() {
  local token="$1"
  local len="${#token}"
  local out="    "
  local i=0
  while [ $i -lt $len ]; do
    local char
    char=$(printf '%s' "$token" | cut -c$((i+1)))
    local ord
    ord=$(printf '%d' "'$char")
    local xor=$(( ord ^ XOR_KEY ))
    out="${out}$(printf '0x%02x' $xor)"
    i=$((i+1))
    if [ $i -lt $len ]; then
      out="${out}, "
      if [ $(( i % 8 )) -eq 0 ]; then
        out="${out}
    "
      fi
    fi
  done
  printf '%s' "$out"
}

ENCODED=$(encode_token "$TOKEN")

# ── Build in a temp file — inject.c stays clean ───────────────────────────────
TMPDIR_BUILD=$(mktemp -d)
TMPC="${TMPDIR_BUILD}/inject_build.c"
TMPSO="${TMPDIR_BUILD}/inject.so"

cleanup() { rm -rf "$TMPDIR_BUILD"; }
trap cleanup EXIT

# Substitute the placeholder enc_token[] with the real encoded bytes.
# Uses awk so no token bytes touch sed's substitution context.
awk -v encoded="$ENCODED" '
  /\/\* PLACEHOLDER:/ { in_placeholder = 1 }
  in_placeholder && /\};/ {
    print encoded
    print "};"
    in_placeholder = 0
    next
  }
  in_placeholder { next }
  { print }
' "$INJECT_C" > "$TMPC"

# ── Compile ───────────────────────────────────────────────────────────────────
CC="${CC:-gcc}"
if ! command -v "$CC" &>/dev/null; then
  echo "error: $CC not found — install build-essential" >&2
  exit 1
fi

"$CC" -shared -fPIC -O2 -Wall -o "$TMPSO" "$TMPC"
echo "compiled: inject.so ($(wc -c < "$TMPSO") bytes)"

# ── Install ───────────────────────────────────────────────────────────────────
mkdir -p "$(dirname "$INSTALL_PATH")"
install -m 0755 "$TMPSO" "$INSTALL_PATH"
echo "installed: $INSTALL_PATH"
echo "done — running containers will use the new token on their next exec call"
