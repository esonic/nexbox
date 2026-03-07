#!/bin/bash
set -e
cd "$(dirname "$0")"

export CLAUDE_CONFIG_DIR="${DACS:-$HOME}/.claude"
export CLAUDE_CODE_TMPDIR="${DACS:-$HOME}/.claude-tmp"
LISTEN=${LISTEN:-127.0.0.1}
PORT=${PORT:-9040}
VITE_PORT=${VITE_PORT:-5173}

MODE="$1"
shift 2>/dev/null || true

# Parse -listen from extra args so all services share the same value
EXTRA_ARGS=()
while [ $# -gt 0 ]; do
  case "$1" in
    -listen|--listen) LISTEN="$2"; shift 2 ;;
    *) EXTRA_ARGS+=("$1"); shift ;;
  esac
done
set -- "${EXTRA_ARGS[@]}"

if [ "$MODE" = "dev" ]; then
  (cd server && go build -o beehub . && exec ./beehub --listen "$LISTEN" --port "$PORT" "$@") &
  (cd web && exec npx vite --host "$LISTEN" --port "$VITE_PORT") &
  wait
else
  cd server && exec ./beehub --listen "$LISTEN" --port "$PORT" "$@"
fi
