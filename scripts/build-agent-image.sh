#!/bin/bash
# scripts/build-agent-image.sh
#
# Builds the boxnex-agent Docker image used for per-project container isolation.
# Run this once before enabling DockerEnabled=true in config.json.
#
# Usage:
#   ./scripts/build-agent-image.sh              # build with default tag
#   ./scripts/build-agent-image.sh --push       # build and push to registry
#   REGISTRY=registry.example.com ./scripts/build-agent-image.sh --push

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
IMAGE_NAME="${REGISTRY:-}boxnex-agent"
TAG="${TAG:-latest}"
FULL_IMAGE="${IMAGE_NAME}:${TAG}"
PUSH=false

for arg in "$@"; do
  case "$arg" in
    --push) PUSH=true ;;
    --help)
      echo "Usage: $0 [--push]"
      echo "  REGISTRY=... $0 --push   # push to a registry"
      exit 0
      ;;
  esac
done

echo "==> Building ${FULL_IMAGE}"
echo "    Dockerfile: Dockerfile.agent"
echo "    Context:    ${REPO_ROOT}"
echo ""

docker build \
  --platform linux/amd64 \
  -f "${REPO_ROOT}/Dockerfile.agent" \
  -t "${FULL_IMAGE}" \
  "${REPO_ROOT}"

echo ""
echo "==> Build complete: ${FULL_IMAGE}"

# Show image size
docker images "${IMAGE_NAME}" --format "    Size: {{.Size}}" | head -1

if [ "$PUSH" = true ]; then
  echo ""
  echo "==> Pushing ${FULL_IMAGE}"
  docker push "${FULL_IMAGE}"
  echo "==> Push complete"
fi

echo ""
echo "Next steps:"
echo "  1. Set docker_enabled: true in config.json"
echo "  2. Set docker_image: \"${FULL_IMAGE}\" in config.json (or leave blank for default)"
echo "  3. Restart the boxnex server"
