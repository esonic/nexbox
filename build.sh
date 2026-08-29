#!/bin/bash
set -euo pipefail

ROOT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
OUTPUT_PATH=${NEXBOX_OUTPUT:-$ROOT_DIR/dist/nexbox}

# Build frontend
cd "$ROOT_DIR/web"
npm run build

# Copy web dist into server for Go embed
rm -rf "$ROOT_DIR/server/webdist"
cp -r "$ROOT_DIR/web/dist" "$ROOT_DIR/server/webdist"

# Build Go server with embedded frontend
mkdir -p "$(dirname -- "$OUTPUT_PATH")"
cd "$ROOT_DIR/server"
VERSION=$(date +%Y%m%d%H%M)
go build -o "$OUTPUT_PATH" -ldflags "-s -w -X main.Version=${VERSION}"
echo "Built nexbox version ${VERSION}: $OUTPUT_PATH"
