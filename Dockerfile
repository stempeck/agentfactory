FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive
ENV TZ=UTC

# System packages (no Chromium/browser deps — unlike gastown)
RUN apt-get update && apt-get install -y \
    git curl wget sudo tmux jq build-essential \
    openssh-client sqlite3 \
    && rm -rf /var/lib/apt/lists/*

# GitHub CLI
RUN curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
    | dd of=/usr/share/keyrings/githubcli-archive-keyring.gpg && \
    echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \
    > /etc/apt/sources.list.d/github-cli.list && \
    apt-get update && apt-get install -y gh && rm -rf /var/lib/apt/lists/*

# Go 1.24.2 (multi-arch: uses dpkg --print-architecture for correct binary)
# To update Go: visit https://go.dev/dl/, copy SHA256 for the new version's
# linux-amd64 and linux-arm64 tarballs. Update GO_VERSION and both SHA256 values.
ENV GO_VERSION=1.24.2
ENV GO_SHA256_AMD64="68097bd680839cbc9d464a0edce4f7c333975e27a90246890e9f1078c7e702ad"
ENV GO_SHA256_ARM64="756274ea4b68fa5535eb9fe2559889287d725a8da63c6aae4d5f23778c229f4b"
RUN ARCH=$(dpkg --print-architecture) && \
    wget -q "https://go.dev/dl/go${GO_VERSION}.linux-${ARCH}.tar.gz" && \
    EXPECTED=$([ "$ARCH" = "amd64" ] && echo "$GO_SHA256_AMD64" || echo "$GO_SHA256_ARM64") && \
    echo "${EXPECTED}  go${GO_VERSION}.linux-${ARCH}.tar.gz" | sha256sum --check --strict && \
    tar -C /usr/local -xzf "go${GO_VERSION}.linux-${ARCH}.tar.gz" && \
    rm "go${GO_VERSION}.linux-${ARCH}.tar.gz"
ENV PATH="/usr/local/go/bin:${PATH}"

# Python 3.12 — required by the in-tree MCP issue-store server (py/issuestore/)
# that mcpstore lazy-spawns. install.go's checkPython312 enforces 3.12.x at
# runtime. Ubuntu 24.04 (noble) ships python3 == 3.12 by default, so the
# distro packages are sufficient (no deadsnakes PPA needed). PEP 668 marks
# the system Python as externally-managed, so --break-system-packages is
# required for a pip install at the system site.
RUN apt-get update && apt-get install -y \
    python3 python3-pip python3-venv \
    && rm -rf /var/lib/apt/lists/*
COPY py/requirements.txt /tmp/requirements.txt
RUN pip3 install --break-system-packages --require-hashes -r /tmp/requirements.txt && rm /tmp/requirements.txt

# Node.js LTS via GPG-signed apt repo (dearmor required — key is ASCII-armored unlike GitHub CLI's binary key)
# To update Node.js major version: change NODE_MAJOR below.
RUN NODE_MAJOR=22 && \
    curl -fsSL https://deb.nodesource.com/gpgkey/nodesource-repo.gpg.key \
    | gpg --dearmor -o /usr/share/keyrings/nodesource.gpg && \
    echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/nodesource.gpg] https://deb.nodesource.com/node_${NODE_MAJOR}.x nodistro main" \
    > /etc/apt/sources.list.d/nodesource.list && \
    apt-get update && apt-get install -y nodejs && rm -rf /var/lib/apt/lists/*

# Non-root user with passwordless sudo
RUN useradd -m -s /bin/bash dev && \
    echo 'dev ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/dev && \
    chmod 440 /etc/sudoers.d/dev

# Workspace directory
RUN mkdir -p /home/dev/af \
    && chown -R dev:dev /home/dev
