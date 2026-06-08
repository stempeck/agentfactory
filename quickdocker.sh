#!/bin/bash
set -euo pipefail

#
# Agentfactory Container Creator
# ===============================
# Creates a Docker container from the agentfactory-base image, clones
# agentfactory (to build af) and the target project repo, runs quickstart.sh,
# and leaves the container ready for `af up`.
#
# Usage:
#   ./quickdocker.sh <github-repo-path>
#   ./quickdocker.sh user/myrepo
#   ./quickdocker.sh --help
#
# Prerequisites:
#   - Docker installed and running
#   - GitHub PAT with repo and read:org scopes
#
# Base image:
#   If agentfactory-base:latest is not present locally, this script builds it
#   from the Dockerfile in this repo. The Dockerfile build context requires:
#     - Dockerfile          (in repo root)
#     - py/requirements.txt (COPY'd into image for Python MCP dependencies)
#   Both files ship with the agentfactory repo. If either is missing, the
#   docker build will fail with a clear error.
#
# Environment variables (optional — will prompt if not set):
#   GH_TOKEN or GITHUB_TOKEN    GitHub Personal Access Token
#

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BASE_IMAGE="agentfactory-base:latest"
CONTAINER_MEMORY="16g"
PROJECTS_DIR="/home/dev/projects"
WORKSPACE_DIR="/home/dev/af"
QUICKSTART_SRC="$SCRIPT_DIR/quickstart.sh"
AF_REMOTE="$(git -C "$SCRIPT_DIR" remote get-url origin 2>/dev/null || echo "")"
AF_REMOTE="${AF_REMOTE#https://github.com/}"
AF_REMOTE="${AF_REMOTE#git@github.com:}"
AF_REMOTE="${AF_REMOTE%.git}"
if [[ -z "$AF_REMOTE" ]]; then
    echo "Error: cannot determine agentfactory repo from git remote in $SCRIPT_DIR" >&2
    exit 1
fi
AF_REPO="$AF_REMOTE"
AF_DIR="${AF_REPO##*/}"

TOTAL_STEPS=8

step() {
    local num="$1"
    local msg="$2"
    echo ""
    echo "[${num}/${TOTAL_STEPS}] ${msg}"
}

step_done() {
    echo "  -> Done"
}

# Parse arguments
if [[ $# -lt 1 ]] || [[ "$1" == "-h" ]] || [[ "$1" == "--help" ]]; then
    echo "Usage: $0 <github-repo-path> [--platform ios] [--build-host user@host]"
    echo ""
    echo "  <github-repo-path>   Target project repo (e.g., user/myrepo)"
    echo ""
    echo "Options:"
    echo "  --platform ios         Enable iOS build-host support (dedicated SSH key setup,"
    echo "                         build-host.json config, connectivity verification)"
    echo "  --build-host user@host SSH build host in user@host format"
    echo "                         (or set AF_BUILD_HOST_USER and AF_BUILD_HOST_HOST)"
    echo ""
    echo "Example:"
    echo "  $0 stempeck/myproject"
    echo "  $0 stempeck/myproject --platform ios --build-host admin@macmini.local"
    echo ""
    echo "Environment variables (optional — prompts if not set):"
    echo "  GH_TOKEN or GITHUB_TOKEN      GitHub Personal Access Token"
    echo "  AF_BUILD_HOST_USER             iOS build host SSH username"
    echo "  AF_BUILD_HOST_HOST             iOS build host hostname/IP"
    if [[ $# -lt 1 ]]; then
        exit 1
    fi
    exit 0
fi

# Normalize input: accept full URLs, SSH URLs, or shorthand (owner/repo)
REPO_PATH="$1"
REPO_PATH="${REPO_PATH#https://}"
REPO_PATH="${REPO_PATH#http://}"
REPO_PATH="${REPO_PATH#git@github.com:}"
REPO_PATH="${REPO_PATH#git@github.com/}"
REPO_PATH="${REPO_PATH#github.com/}"
REPO_PATH="${REPO_PATH%.git}"

PLATFORM=""
BUILD_HOST_ARG=""
[[ $# -gt 0 ]] && shift
while [[ $# -gt 0 ]]; do
    case "$1" in
        --platform) PLATFORM="$2"; shift 2 ;;
        --build-host) BUILD_HOST_ARG="$2"; shift 2 ;;
        *) echo "Unknown option: $1" >&2; exit 1 ;;
    esac
done

REPO_NAME="${REPO_PATH##*/}"
CONTAINER_NAME="af_$(echo "$REPO_PATH" | sed 's/[^a-zA-Z0-9_.-]/_/g')"

echo ""
echo "=========================================="
echo "  Agentfactory Container Creator"
echo "=========================================="
echo ""
echo "  Target repo: $REPO_PATH"
echo "  Container:   $CONTAINER_NAME"
echo "  Workspace:   $WORKSPACE_DIR/$REPO_NAME"
echo ""

# ─── Step 1: Check / build base image ─────────────────────────────────────

step 1 "Checking base image..."

if docker image inspect "$BASE_IMAGE" &>/dev/null; then
    echo "  Using existing base image: $BASE_IMAGE"
else
    if [[ ! -f "$SCRIPT_DIR/Dockerfile" ]]; then
        echo "" >&2
        echo "Error: Base image '$BASE_IMAGE' not found and no Dockerfile in $SCRIPT_DIR." >&2
        echo "  Either build the image manually or add a Dockerfile to this repo." >&2
        exit 1
    fi
    if [[ ! -f "$SCRIPT_DIR/py/requirements.txt" ]]; then
        echo "" >&2
        echo "Error: Dockerfile requires py/requirements.txt but it is missing from $SCRIPT_DIR." >&2
        exit 1
    fi
    echo "  Base image not found locally. Building from $SCRIPT_DIR/Dockerfile..."
    docker build -t "$BASE_IMAGE" "$SCRIPT_DIR"
    echo "  Built base image: $BASE_IMAGE"
fi
step_done

# ─── Step 2: Collect credentials ────────────────────────────────────────────

step 2 "Collecting credentials..."

# GitHub PAT
GH_TOKEN="${GH_TOKEN:-${GITHUB_TOKEN:-}}"
if [ -z "$GH_TOKEN" ]; then
    echo ""
    echo "  GitHub authentication required."
    echo "  Enter a GitHub Personal Access Token (PAT)"
    echo "  with repo and read:org scopes."
    echo ""
    read -rsp "  GitHub PAT: " GH_TOKEN
    echo ""
fi

if [ -z "$GH_TOKEN" ]; then
    echo "Error: GitHub PAT is required" >&2
    exit 1
fi

if [[ "$PLATFORM" == "ios" ]]; then
    BUILD_USER="${AF_BUILD_HOST_USER:-}"
    BUILD_HOST="${AF_BUILD_HOST_HOST:-}"
    if [[ -n "$BUILD_HOST_ARG" ]]; then
        if [[ "$BUILD_HOST_ARG" != *@* ]]; then
            echo "ERROR: --build-host must be in user@host format" >&2
            exit 1
        fi
        BUILD_USER="${BUILD_HOST_ARG%%@*}"
        BUILD_HOST="${BUILD_HOST_ARG#*@}"
    fi
    if [[ -z "$BUILD_HOST" ]]; then
        read -rp "  SSH build host (user@host): " BUILD_HOST_INPUT
        if [[ "$BUILD_HOST_INPUT" != *@* ]]; then
            echo "ERROR: Build host must be in user@host format" >&2
            exit 1
        fi
        BUILD_USER="${BUILD_HOST_INPUT%%@*}"
        BUILD_HOST="${BUILD_HOST_INPUT#*@}"
    fi
fi

if [[ "$PLATFORM" == "ios" ]]; then
    AF_KEY="${HOME}/.ssh/af_container_ed25519"
    if [[ ! -f "$AF_KEY" ]]; then
        echo "  Generating container SSH keypair..."
        ssh-keygen -t ed25519 -f "$AF_KEY" -N "" -C "agentfactory-container"
    fi
    # Key authorization: the build host IS the local Mac (the container reaches it
    # via host.docker.internal, which only resolves inside containers). Authorize by
    # appending the pubkey to the LOCAL authorized_keys — no SSH. Only on macOS, where
    # sshd ("Remote Login") serves the container's connection.
    if [[ "$OSTYPE" == "darwin"* ]]; then
        mkdir -p "${HOME}/.ssh"
        chmod 700 "${HOME}/.ssh"
        touch "${HOME}/.ssh/authorized_keys"
        chmod 600 "${HOME}/.ssh/authorized_keys"
        if ! grep -qF "agentfactory-container" "${HOME}/.ssh/authorized_keys"; then
            echo "  Authorizing container key in ~/.ssh/authorized_keys..."
            cat "${AF_KEY}.pub" >> "${HOME}/.ssh/authorized_keys"
        fi
    fi
fi

step_done

# ─── Step 3: Create container ───────────────────────────────────────────────

step 3 "Creating container: $CONTAINER_NAME..."

# Check for existing container
if docker ps -a --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
    echo "  Container '$CONTAINER_NAME' already exists."
    read -rp "  Remove and recreate? (y/N): " CONFIRM
    if [[ "$CONFIRM" == "y" || "$CONFIRM" == "Y" ]]; then
        docker rm -f "$CONTAINER_NAME"
        echo "  Removed existing container."
    else
        echo "  Aborted. Use a different repo path or remove the container manually."
        unset GH_TOKEN
        exit 1
    fi
fi

# Check quickstart.sh exists
if [[ ! -f "$QUICKSTART_SRC" ]]; then
    echo "Error: quickstart.sh not found at $QUICKSTART_SRC" >&2
    unset GH_TOKEN
    exit 1
fi

IOS_DOCKER_ARGS=""
if [[ "$PLATFORM" == "ios" ]]; then
    HOST_MOUNT="${HOME}/.af-containers/${CONTAINER_NAME}"
    mkdir -p "$HOST_MOUNT"
    IOS_DOCKER_ARGS="-v ${HOST_MOUNT}:${WORKSPACE_DIR}/${REPO_NAME}"
fi

docker run -dit \
    --memory="$CONTAINER_MEMORY" \
    --memory-swap="24g" \
    --tmpfs /tmp:size=2g \
    --shm-size=256m \
    $IOS_DOCKER_ARGS \
    --name "$CONTAINER_NAME" \
    "$BASE_IMAGE" bash --login

if [[ "$PLATFORM" == "ios" ]]; then
    docker exec -u dev "$CONTAINER_NAME" mkdir -p /home/dev/.ssh
    docker cp "${HOME}/.ssh/af_container_ed25519" "${CONTAINER_NAME}:/home/dev/.ssh/id_ed25519"
    docker cp "${HOME}/.ssh/af_container_ed25519.pub" "${CONTAINER_NAME}:/home/dev/.ssh/id_ed25519.pub"
    docker exec "$CONTAINER_NAME" chown dev:dev /home/dev/.ssh/id_ed25519 /home/dev/.ssh/id_ed25519.pub
    docker exec "$CONTAINER_NAME" chmod 600 /home/dev/.ssh/id_ed25519
fi

step_done

# ─── Step 4: Inject GitHub PAT ──────────────────────────────────────────────

step 4 "Authenticating GitHub inside container..."

echo "$GH_TOKEN" | docker exec -i -u dev "$CONTAINER_NAME" gh auth login --with-token
unset GH_TOKEN

# Verify auth
if ! docker exec -u dev "$CONTAINER_NAME" gh auth status &>/dev/null; then
    echo "Error: GitHub authentication failed inside container" >&2
    exit 1
fi

# Configure git credential helper
docker exec -u dev "$CONTAINER_NAME" gh auth setup-git

step_done

# ─── Step 5: Clone repositories ─────────────────────────────────────────────

step 5 "Cloning repositories..."

# Clone agentfactory source (for building af)
echo "  Cloning agentfactory..."
docker exec -u dev "$CONTAINER_NAME" mkdir -p "$PROJECTS_DIR"
docker exec -u dev -w "$PROJECTS_DIR" "$CONTAINER_NAME" gh repo clone "$AF_REPO"

# Clone target project repo
echo "  Cloning $REPO_PATH..."
docker exec -u dev -w "$WORKSPACE_DIR" "$CONTAINER_NAME" gh repo clone "$REPO_PATH"

step_done

# ─── Step 6: Copy quickstart.sh and dependencies into container ──────────────

step 6 "Copying quickstart.sh and dependencies into container..."

# Copy quickstart.sh into the agentfactory source tree (overwrite with host version)
docker cp "$QUICKSTART_SRC" "${CONTAINER_NAME}:${PROJECTS_DIR}/${AF_DIR}/quickstart.sh"
docker exec "$CONTAINER_NAME" chown dev:dev "${PROJECTS_DIR}/${AF_DIR}/quickstart.sh"
docker exec "$CONTAINER_NAME" chmod +x "${PROJECTS_DIR}/${AF_DIR}/quickstart.sh"

step_done

# ─── Step 7: Configure shell defaults ─────────────────────────────────────
# Must run BEFORE quickstart.sh because quickstart.sh ends with `exec bash`
# which sources .bashrc — if we wrote this after, it would never execute.

step 7 "Configuring shell defaults..."

# Set default working directory for direct bash sessions (not tmux)
docker exec -u dev "$CONTAINER_NAME" bash -c "cat >> ~/.bashrc << 'BASHRC_EOF'

# Auto-cd to project directory for direct bash sessions only
# Skip when inside tmux to allow agent sessions to keep their working directory
if [[ -z \"\$TMUX\" ]]; then
    cd ${WORKSPACE_DIR}/${REPO_NAME} 2>/dev/null || true
fi
BASHRC_EOF"

step_done

# ─── Step 8: Run quickstart.sh ──────────────────────────────────────────────

step 8 "Running quickstart.sh (this may take a few minutes)..."

docker exec -it -u dev -w "${PROJECTS_DIR}/${AF_DIR}" "$CONTAINER_NAME" \
    ./quickstart.sh

step_done

if [[ "$PLATFORM" == "ios" ]]; then
    echo ""
    echo "  Configuring iOS build host..."
    docker exec -u dev -w "$WORKSPACE_DIR/$REPO_NAME" "$CONTAINER_NAME" af config build-host --mode ssh --host "$BUILD_HOST" --user "$BUILD_USER" --mount-path "${HOME}/.af-containers/${CONTAINER_NAME}" --skip-ssh-check || {
        echo "ERROR: af config build-host failed inside container" >&2
        exit 1
    }
    echo "  Verifying SSH connectivity..."
    docker exec -u dev "$CONTAINER_NAME" \
        ssh -o ConnectTimeout=5 -o BatchMode=yes -o StrictHostKeyChecking=accept-new \
            "$BUILD_USER@$BUILD_HOST" echo ok || {
        echo "ERROR: SSH connectivity verification failed from inside container." >&2
        echo "Check: network access, host key acceptance, key authorization on build host." >&2
        exit 1
    }
    echo "  -> iOS build host configured and verified"
fi

# ─── Success ──────────────────────────────────────────────────────────────

echo ""
echo "Container ready!"
echo ""
echo "=========================================="
echo "  Setup complete!"
echo "=========================================="
echo ""
echo "  Container: $CONTAINER_NAME"
echo "  Workspace: $WORKSPACE_DIR/$REPO_NAME"
if [[ "$PLATFORM" == "ios" ]]; then
    echo "  iOS build host: $BUILD_USER@$BUILD_HOST"
    echo "  SSH key-based auth: configured"
    echo "  Shared volume: ${HOME}/.af-containers/${CONTAINER_NAME}"
fi
echo ""
echo "  Connect:"
echo "    docker exec -it -u dev $CONTAINER_NAME bash"
echo ""
echo "  Start agents:"
echo "    af up"
echo ""
echo "  If container stops, restart with:"
echo "    docker start $CONTAINER_NAME"
echo "=========================================="
echo ""

# Connect to the container
docker exec -it -u dev "$CONTAINER_NAME" bash
