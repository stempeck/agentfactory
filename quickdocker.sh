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
# Overridable so quickdocker-pro.sh (the registry-image wrapper) can inject a
# pre-pulled image (e.g. ghcr.io/stempeck/agentfactory-base:latest) without copying
# Step 1. Default is unchanged: build/use the local agentfactory-base:latest.
BASE_IMAGE="${BASE_IMAGE:-agentfactory-base:latest}"
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

# ─── Issue #425 Phase 1B: host (laptop) listen-port derivation ───────────────
# Derive a deterministic, stateless host listen port from the UNIQUE container
# name $CONTAINER_NAME (af_<user>_<repo>), never the colliding REPO_NAME
# basename — hashing the basename re-opens the design's "Gap 9" (alice/myrepo
# and bob/myrepo would collide). Formula, range [20000, 30000):
#     HOSTPORT = 20000 + (cksum(name) mod 10000)
# Same name always re-derives the same base port (stable, bookmarkable URL) with
# no registry file — determinism comes from the hash, not persistence. If
# 127.0.0.1:HOSTPORT is already taken on the laptop, scan forward in range for
# the next free port. The /dev/tcp probe is wrapped (2>/dev/null + explicit
# return) so it is safe under `set -euo pipefail`: a free port fails the connect
# (nonzero) and must NOT abort the script — it is only consumed in a `while`
# condition, where errexit is suspended. Echoes the chosen port to stdout.
#
# Defined here but NOT called in Phase 1B (no --web caller exists yet). Phase 2's
# --web short-circuit invokes it as:
#     HOSTPORT="$(derive_hostport "$CONTAINER_NAME")"
# It takes the container name as $1 because the Phase-2 --web path runs before
# the CONTAINER_NAME assignment site and computes the name itself.
derive_hostport() {
    local name="$1"
    local HOSTPORT_BASE=20000
    local HOSTPORT_RANGE=10000
    local _name_hash HOSTPORT
    _name_hash="$(printf '%s' "$name" | cksum | cut -d' ' -f1)"
    HOSTPORT=$(( HOSTPORT_BASE + (_name_hash % HOSTPORT_RANGE) ))
    # free-port fallback: if 127.0.0.1:HOSTPORT is taken, advance within range.
    _port_in_use() { (exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null && { exec 3>&-; return 0; } || return 1; }
    while _port_in_use "$HOSTPORT"; do
        HOSTPORT=$(( HOSTPORT_BASE + ((HOSTPORT - HOSTPORT_BASE + 1) % HOSTPORT_RANGE) ))
    done
    printf '%s\n' "$HOSTPORT"
}

# ─── Issue #425 Phase 2: --web docker-exec loopback bridge ───────────────────
# Stand up a detached, idempotent, 127.0.0.1-only forking bridge to webui's
# in-container loopback and print the clickable URL, then return immediately.
# Because the relay reaches webui from INSIDE the container, webui sees a loopback
# client and its existing auth/origin checks pass with NO Go change (ADR-006).
# Nothing is ever published; the container is never recreated (ADR-017 / CR-1).
#
# SINGLE SOURCE OF TRUTH: derive_hostport()+_web_bridge() live ONLY here.
# quickdocker-pro.sh is a thin wrapper that pulls the registry base image and then
# `exec`s this script, so the bridge is inherited — never copied. Do not duplicate
# it back into the wrapper.
_web_bridge() {
    local CONTAINER_NAME="$1"
    # The SPECIFIC factory this container serves (${WORKSPACE_DIR}/<repo>) — used to target the
    # right webui rendezvous and to pin AF_ROOT on relaunch. Never glob-and-guess (a sibling
    # factory under /home/dev/af would otherwise be served by mistake).
    local _factory_root="$2"

    # (1) Resolve the target: the <repo>-derived name if running, else a single af_* container.
    if ! docker ps --format '{{.Names}}' 2>/dev/null | grep -qx "$CONTAINER_NAME"; then
        local _running
        _running="$(docker ps --format '{{.Names}}' 2>/dev/null | grep '^af_' || true)"
        if [[ -n "$_running" && "$(printf '%s\n' "$_running" | grep -c . || true)" == "1" ]]; then
            CONTAINER_NAME="$_running"
        else
            echo "ERROR: container '$CONTAINER_NAME' is not running — start it (docker start '$CONTAINER_NAME') or create it first, then retry." >&2
            exit 1
        fi
    fi

    # (2) Require webui installed inside — do NOT build on the fly.
    if ! docker exec "$CONTAINER_NAME" test -x /home/dev/.local/bin/webui 2>/dev/null; then
        echo "ERROR: webui not installed in this container — pull latest and run quickstart.sh inside it once, then retry." >&2
        exit 1
    fi

    # Host listener tool: socat -> python3 (forking, 127.0.0.1 only); never silent. nc is
    # deliberately NOT in the ladder (Issue #428): BSD/Apple nc (macOS) and OpenBSD nc
    # (default Linux) both reject `-e`, so an `nc … -e` listener binds nothing and the
    # browser gets connection-refused. python3 is a de-facto hard dependency here (the
    # in-container relay below runs `docker exec … python3 -c`), so socat→python3 covers
    # the realistic host set without the BSD/OpenBSD-nc failure surface.
    local _tool=""
    if   command -v socat   >/dev/null 2>&1; then _tool="socat"
    elif command -v python3 >/dev/null 2>&1; then _tool="python3"
    else
        echo "ERROR: need socat or python3 on this machine — install one and retry." >&2
        exit 1
    fi

    # (5) Deterministic laptop listen port (Phase 1B helper).
    local HOSTPORT
    HOSTPORT="$(derive_hostport "$CONTAINER_NAME")"
    local _url="http://127.0.0.1:${HOSTPORT}/"
    local _marker="${TMPDIR:-/tmp}/af-web-bridge-${CONTAINER_NAME}.pid"

    # (3) Idempotent + self-healing: reuse ONLY if a prior bridge's marker names a live
    # supervisor AND the port it actually bound still answers. The marker records "PID PORT"
    # (see the supervisor below), so the reuse path probes the REAL persisted port and reprints
    # the SAME URL — NOT the freshly-derived HOSTPORT. This matters because derive_hostport's
    # free-port scan advances past any in-use port, including the live bridge's own: a re-run
    # would derive HOSTPORT+1, so probing the derived port would always miss the running listener
    # and spawn a duplicate on a shifted URL. A marker whose listener never bound (e.g. a pre-fix
    # failed run, Issue #428) fails the port probe and self-heals: the stale marker is removed and
    # we fall through to re-setup, so the user never has to hunt a file in $TMPDIR.
    _port_in_use() { (exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null && { exec 3>&-; return 0; } || return 1; }
    if [[ -f "$_marker" ]]; then
        local _mpid="" _mport=""
        read -r _mpid _mport < "$_marker" 2>/dev/null || true
        if [[ -n "$_mpid" ]] && kill -0 "$_mpid" 2>/dev/null && [[ -n "$_mport" ]] && _port_in_use "$_mport"; then
            local _rurl="http://127.0.0.1:${_mport}/"
            echo "🔗 Open your factory:  ${_rurl}"
            command -v open >/dev/null 2>&1 && open "$_rurl" 2>/dev/null || true
            return 0
        fi
        rm -f "$_marker" 2>/dev/null || true   # stale marker (listener never bound / died) -> re-setup
    fi

    # (4) Learn webui's LIVE loopback address for THIS factory. The bridge targets ONE specific
    # factory — _factory_root = ${WORKSPACE_DIR}/<repo>, passed in — never "whatever webui is
    # running": multiple factories can coexist under /home/dev/af, and relaying to the wrong one is
    # a silent correctness bug (it would show a different repo's console). The published "address"
    # is NOT trusted blindly: a webui that died (e.g. `docker stop`/`docker start` with no Phase-4
    # relaunch) leaves a STALE rendezvous pointing at a dead port -> ERR_EMPTY_RESPONSE (Issue #428).
    # So discovery reads ONLY this factory's .runtime/webui_server.json and HEALTH-CHECKS it (urllib
    # GET /healthz); if it is not live, it relaunches webui detached WITH AF_ROOT PINNED to this
    # factory root (an unpinned launch roots webui at $HOME and never serves — the Phase-4 trap).
    # webui's idempotent rendezvous.Ensure heals the canonical file to the new live address.
    if [[ -z "$_factory_root" ]]; then
        echo "ERROR: --web could not determine the factory root for container '$CONTAINER_NAME'." >&2
        exit 1
    fi
    local _internal="" _relaunched="" _i _out
    for _i in $(seq 1 30); do
        _out="$(docker exec "$CONTAINER_NAME" python3 -c '
import json, os, sys, urllib.request
root = sys.argv[1]
def live(a):
    try:
        with urllib.request.urlopen("http://" + a + "/healthz", timeout=2) as r:
            return r.status == 200
    except Exception:
        return False
a = ""
try:
    a = json.load(open(os.path.join(root, ".runtime", "webui_server.json"))).get("address", "")
except Exception:
    a = ""
print("LIVE " + a if (a and live(a)) else "STALE")
' "$_factory_root" 2>/dev/null || true)"
        if [[ "$_out" == LIVE\ * ]]; then _internal="${_out#LIVE }"; break; fi
        if [[ -z "$_relaunched" ]]; then
            _relaunched=1
            docker exec -d -u dev "$CONTAINER_NAME" sh -lc "AF_ROOT='$_factory_root' nohup \"\$HOME/.local/bin/webui\" >/tmp/webui.log 2>&1 &" 2>/dev/null || true
        fi
        sleep 0.3
    done
    if [[ -z "$_internal" ]]; then
        echo "ERROR: webui did not come up with a LIVE loopback address for '${_factory_root}' within ~9s (see /tmp/webui.log inside the container)." >&2
        exit 1
    fi

    # Per-connection relay (host helper): pipe a connection's bytes through docker exec into an
    # INLINE in-container python3 relay that reaches webui's loopback. The relay is inlined via
    # `python3 -c` (never copied into the container).
    local _handler="${TMPDIR:-/tmp}/af-web-bridge-${CONTAINER_NAME}.handler"
    cat > "$_handler" <<EOF
#!/bin/bash
exec docker exec -i "${CONTAINER_NAME}" python3 -c 'import os,socket,sys,threading
h,p=sys.argv[1].rsplit(":",1)
s=socket.create_connection((h,int(p)))
def up():
    while True:
        d=os.read(0,65536)
        if not d: break
        s.sendall(d)
    try: s.shutdown(socket.SHUT_WR)
    except Exception: pass
threading.Thread(target=up,daemon=True).start()
while True:
    d=s.recv(65536)
    if not d: break
    os.write(1,d)' "${_internal}"
EOF
    chmod +x "$_handler"

    # (5,6) Detached supervisor: a forking 127.0.0.1-only listener on HOSTPORT + a watcher that
    # self-exits (removing the pidfile) when the container stops. nohup/setsid keep it alive past
    # this shell; the function returns immediately ("reveal the URL", not a session).
    local _logf="${TMPDIR:-/tmp}/af-web-bridge-${CONTAINER_NAME}.log"
    local _supervisor='set +e
cname="$1"; hostport="$2"; handler="$3"; tool="$4"; marker="$5"
case "$tool" in
    socat)
        socat "TCP-LISTEN:${hostport},bind=127.0.0.1,reuseaddr,fork" "EXEC:${handler}" &
        ;;
    python3)
        python3 - "$hostport" "$handler" <<"PYL" &
import socket, subprocess, sys, threading
port = int(sys.argv[1]); handler = sys.argv[2]
srv = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
srv.bind(("127.0.0.1", port)); srv.listen(64)
def handle(conn):
    p = None
    try:
        p = subprocess.Popen([handler], stdin=subprocess.PIPE, stdout=subprocess.PIPE)
        def c2p():
            try:
                while True:
                    d = conn.recv(65536)
                    if not d: break
                    p.stdin.write(d); p.stdin.flush()
            except Exception: pass
            try: p.stdin.close()
            except Exception: pass
        threading.Thread(target=c2p, daemon=True).start()
        while True:
            # read1: return as soon as ANY bytes are available (one underlying read).
            # Plain read(65536) on a BufferedReader BLOCKS until the full 65536 bytes
            # arrive or EOF — which never happens on a keep-alive HTTP response smaller
            # than the buffer, deadlocking the response direction (the page spins forever).
            d = p.stdout.read1(65536)
            if not d: break
            conn.sendall(d)
    except Exception: pass
    finally:
        try: conn.close()
        except Exception: pass
        try:
            if p: p.terminate()
        except Exception: pass
while True:
    try: c, _ = srv.accept()
    except Exception: break
    threading.Thread(target=handle, args=(c,), daemon=True).start()
PYL
        ;;
esac
lpid="$!"
# Marker written AFTER the listener launches (Issue #428): records the supervisor PID AND the
# port it bound ("PID PORT"). The PID lets the next run liveness-check; the persisted PORT lets
# it reprint the SAME URL and reuse this listener instead of deriving a shifted port (HOSTPORT+1)
# and spawning a duplicate. Paired with the port-probe in the idempotency check, a failed launch
# never leaves a reuse-passing marker.
printf "%s %s\n" "$$" "$hostport" > "$marker"
# Self-exit watcher: live exactly as long as the container.
while docker inspect -f "{{.State.Running}}" "$cname" 2>/dev/null | grep -q true; do
    sleep 2
done
kill "$lpid" 2>/dev/null
rm -f "$marker" "$handler"'
    if command -v setsid >/dev/null 2>&1; then
        setsid bash -c "$_supervisor" _ "$CONTAINER_NAME" "$HOSTPORT" "$_handler" "$_tool" "$_marker" >"$_logf" 2>&1 </dev/null &
    else
        nohup bash -c "$_supervisor" _ "$CONTAINER_NAME" "$HOSTPORT" "$_handler" "$_tool" "$_marker" >"$_logf" 2>&1 </dev/null &
    fi

    # (7) Reveal the URL and return immediately.
    echo "🔗 Open your factory:  ${_url}"
    command -v open >/dev/null 2>&1 && open "$_url" 2>/dev/null || true
}

# Parse arguments
if [[ $# -lt 1 ]] || [[ "$1" == "-h" ]] || [[ "$1" == "--help" ]]; then
    echo "Usage: $0 <github-repo-path> [--platform ios] [--build-host user@host] [--web] [--shell]"
    echo ""
    echo "  <github-repo-path>   Target project repo (e.g., user/myrepo)"
    echo ""
    echo "Options:"
    echo "  --platform ios         Enable iOS build-host support (dedicated SSH key setup,"
    echo "                         build-host.json config, connectivity verification)"
    echo "  --build-host user@host SSH build host in user@host format"
    echo "                         (or set AF_BUILD_HOST_USER and AF_BUILD_HOST_HOST)"
    echo "  --web                  Open the web console for an ALREADY-RUNNING container."
    echo "                         Prints/opens a 127.0.0.1 URL via a local loopback bridge."
    echo "                         Acts on the existing container only — never creates or"
    echo "                         modifies it, never publishes a port."
    echo "  --shell                Open a shell in an ALREADY-RUNNING container."
    echo "                         Acts on the existing container only — never creates or"
    echo "                         modifies it."
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

# ─── Issue #425 Phase 2: --web / --shell early short-circuit ──────────────────
# Detect --web/--shell among the args and act on the ALREADY-RUNNING container,
# then exit — BEFORE the flag loop / clone / create / recreate logic below.
# Falling through would reach the unknown-option reject, the recreate prompt, and
# `docker run`, rebuilding the user's factory (ADR-017: af infrastructure must
# never destroy customer data). CONTAINER_NAME is assigned further down, so
# compute it here from the already-normalized REPO_PATH.
for _arg in "$@"; do
    case "$_arg" in
        --shell)
            CONTAINER_NAME="af_$(echo "$REPO_PATH" | sed 's/[^a-zA-Z0-9_.-]/_/g')"
            exec docker exec -it -u dev "$CONTAINER_NAME" bash
            ;;
        --web)
            CONTAINER_NAME="af_$(echo "$REPO_PATH" | sed 's/[^a-zA-Z0-9_.-]/_/g')"
            # Target THIS repo's factory root inside the container (WORKSPACE_DIR/<repo basename>),
            # so the bridge serves agentfactory-pro's console — not a sibling factory under /home/dev/af.
            _web_bridge "$CONTAINER_NAME" "${WORKSPACE_DIR}/${REPO_PATH##*/}"
            exit 0
            ;;
    esac
done

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

# Install the webui relaunch guard on a LOGIN-shell init path (~/.bash_profile).
# The container CMD is \`bash --login\`, which reads ~/.bash_profile / ~/.profile but NOT
# ~/.bashrc, so a relaunch placed only in ~/.bashrc would silently never fire on
# \`docker stop && docker start\` (Issue #425 CR H1). webui owns its own rendezvous +
# start-lock, so firing this guard on every login is idempotent (no double-bind). AF_ROOT
# is pinned to the known factory root (host-expanded below) because a login shell starts
# with CWD=\$HOME, not the repo dir, so \$PWD cannot be trusted at restart (Gap 8).
docker exec -u dev "$CONTAINER_NAME" bash -c "cat >> ~/.bash_profile << 'PROFILE_EOF'

# Source .bashrc for PATH and the auto-cd default in login shells.
if [ -f \"\$HOME/.bashrc\" ]; then . \"\$HOME/.bashrc\"; fi

# >>> phase4 webui login-init relaunch guard (~/.bash_profile) >>>
# Relaunch the optional web console on every login (incl. PID-1 bash --login after
# docker start). Pinned AF_ROOT keeps the served root and rendezvous file under the
# factory root even though a login shell starts with CWD=\$HOME, not the repo dir.
if [ -x \"\$HOME/.local/bin/webui\" ]; then
    AF_ROOT=\"\${AF_ROOT:-${WORKSPACE_DIR}/${REPO_NAME}}\" nohup \"\$HOME/.local/bin/webui\" >/tmp/webui.log 2>&1 &
fi
# <<< phase4 webui login-init relaunch guard (~/.bash_profile) <<<
PROFILE_EOF"

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
