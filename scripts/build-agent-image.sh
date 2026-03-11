#!/bin/bash
# scripts/build-agent-image.sh
#
# Builds the boxnex-agent container image used for per-project container isolation.
# Run this once before enabling podman_enabled: true in config.json.
#
# Usage:
#   ./scripts/build-agent-image.sh                          # native arch
#   PLATFORM=linux/amd64 ./scripts/build-agent-image.sh    # cross-build for amd64
#   ./scripts/build-agent-image.sh --push                  # build and push
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
      echo "  PLATFORM=linux/amd64 $0   # cross-build (requires qemu-user-static)"
      echo "  REGISTRY=... $0 --push    # push to a registry"
      exit 0
      ;;
  esac
done

# PLATFORM defaults to native arch; set PLATFORM=linux/amd64 for production builds
# on an arm64 dev machine (requires qemu-user-static installed on the host).
PLATFORM_ARGS=()
if [ -n "${PLATFORM:-}" ]; then
  PLATFORM_ARGS=(--platform "${PLATFORM}")
  echo "    Platform:   ${PLATFORM}"
fi

echo "==> Building ${FULL_IMAGE}"
echo "    Dockerfile: Dockerfile.agent"
echo "    Context:    ${REPO_ROOT}"
echo ""

podman build \
  "${PLATFORM_ARGS[@]}" \
  -f "${REPO_ROOT}/Dockerfile.agent" \
  -t "${FULL_IMAGE}" \
  "${REPO_ROOT}"

echo ""
echo "==> Build complete: ${FULL_IMAGE}"

# Show image size
podman image ls "${IMAGE_NAME}" --format "    Size: {{.Size}}" | head -1

if [ "$PUSH" = true ]; then
  echo ""
  echo "==> Pushing ${FULL_IMAGE}"
  podman push "${FULL_IMAGE}"
  echo "==> Push complete"
fi

echo ""
echo "Next steps:"
echo "  1. Set podman_enabled: true in config.json"
echo "  2. Set podman_image: \"${FULL_IMAGE}\" in config.json (or leave blank for default)"
echo "  3. Set CLAUDE_CODE_OAUTH_TOKEN environment variable (do not write to config.json)"
echo "  4. Restart the boxnex server"
