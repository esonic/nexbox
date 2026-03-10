# boxnex-agent — per-project isolation container (Podman rootless)
#
# Build:  podman build -f Containerfile -t boxnex-agent:latest .
#
# This image is NOT the server image. It is the sandbox container that
# runs one instance per project when PodmanEnabled=true in config.json.
# The project's git repository is bind-mounted to /workspace at runtime.
#
# Security posture:
#   - Runs as non-root user "agent" (uid/gid 1000)
#   - In Podman rootless mode the container uid 1000 maps to a
#     sub-uid range on the host — a host privilege boundary even on escape
#   - /tmp provided via tmpfs (noexec, nosuid) at runtime (--tmpfs flag)
#   - Claude session data stored under /workspace/.claude via CLAUDE_CONFIG_DIR

FROM alpine:latest

# Runtime dependencies required by the Claude CLI (glibc-compat) and
# common development tools used by Claude during task execution.
RUN apk add --no-cache \
    libgcc \
    libstdc++ \
    ripgrep \
    curl \
    git \
    bash \
    python3 \
    nodejs \
    npm

# Install the official Claude CLI
RUN curl -fsSL https://claude.ai/install.sh | bash

# Verify the installation
RUN claude --version

# Create a non-root user with a known uid/gid so Podman rootless uid mapping
# is predictable and workspace bind-mount permissions can be set correctly.
RUN addgroup -g 1000 agent && \
    adduser -D -u 1000 -G agent -h /home/agent -s /bin/bash agent

# Ensure the agent user owns its home directory.
RUN chown -R agent:agent /home/agent

# Switch to non-root user for all subsequent layers and at runtime.
USER agent

# Default working directory — overridden at runtime via -w /workspace.
WORKDIR /workspace
