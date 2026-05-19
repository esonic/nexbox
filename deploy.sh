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
GOOS=linux GOARCH=amd64 go build -o ../online/nexbox -ldflags "-s -w -X main.Version=${VERSION}"
echo "Built nexbox version ${VERSION}"
scp ../online/nexbox ubuntu@170.106.117.226:/home/ubuntu/nex/nexbox
