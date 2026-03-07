#!/bin/bash
set -e

# Build frontend
cd web
npm run build
cd ..

# Copy web dist into server for Go embed
rm -rf server/webdist
cp -r web/dist server/webdist

# Build Go server with embedded frontend
cd server
VERSION=$(date +%Y%m%d%H%M)
go build -o beehub -ldflags "-s -w -X main.Version=${VERSION}"
echo "Built beehub version ${VERSION}"
