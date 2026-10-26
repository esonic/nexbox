#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
OUTPUT_PATH=${NEXBOX_OUTPUT:-"$ROOT_DIR/dist/nexbox"}
[[ $OUTPUT_PATH == /* ]] || OUTPUT_PATH="$ROOT_DIR/$OUTPUT_PATH"
VERSION=$(git -C "$ROOT_DIR" describe --tags --always --dirty 2>/dev/null || printf 'dev')

# Build frontend from the lockfile.
cd "$ROOT_DIR/web"
npm ci
npm run build

# Embed the frontend in the Go executable.
rm -rf "$ROOT_DIR/server/webdist"
cp -r "$ROOT_DIR/web/dist" "$ROOT_DIR/server/webdist"
touch "$ROOT_DIR/server/webdist/.gitkeep"

mkdir -p "$(dirname -- "$OUTPUT_PATH")"
cd "$ROOT_DIR/server"
go build -trimpath -o "$OUTPUT_PATH" -ldflags "-s -w -X main.Version=${VERSION}"
echo "Built nexbox version ${VERSION}: $OUTPUT_PATH"
