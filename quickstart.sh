#!/usr/bin/env bash
set -euo pipefail

#
# Agentfactory Quickstart Script
# ===============================
# In-container bootstrap script that checks prerequisites, installs missing
# dependencies (af, Claude Code), configures the factory workspace, and
# provisions default agents — all non-interactively and idempotently.
#
# Python 3.12 is a hard prerequisite (the in-tree MCP issue-store server at
# py/issuestore/ requires it). It is installed by the container image; this
# script only verifies it.
#
# Usage:
#   ./quickstart.sh           # Full setup (always auto mode, no prompts)
#   ./quickstart.sh --check   # Check prerequisites only
#   ./quickstart.sh --help    # Show this help
#
# This script is designed to run inside a container created by quickdocker.sh.
# It assumes the base image (from Dockerfile) provides Go, Node, git, gh, tmux, jq,
# build-essential, sqlite3, openssh-client, and Python 3.12.
#

# Cleanup trap for temporary files
CLEANUP_DIRS=()
cleanup() {
    for dir in "${CLEANUP_DIRS[@]}"; do
        rm -rf "$dir" 2>/dev/null || true
    done
}
trap cleanup EXIT

#------------------------------------------------------------------------------
# Configuration
#------------------------------------------------------------------------------

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORKSPACE_DIR="$HOME/af"
GO_MIN_VERSION="1.24"
GIT_MIN_VERSION="2.20"
TMUX_MIN_VERSION="3.0"
CHECK_ONLY=false

#------------------------------------------------------------------------------
# Logging Helpers
#------------------------------------------------------------------------------

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

log_info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

log_success() {
    echo -e "${GREEN}[OK]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

log_step() {
    echo -e "\n${GREEN}==>${NC} $1"
}

#------------------------------------------------------------------------------
# Version Helpers
#------------------------------------------------------------------------------

version_gte() {
    # Returns 0 if $1 >= $2
    printf '%s\n%s\n' "$2" "$1" | sort -V -C
}

command_exists() {
    command -v "$1" >/dev/null 2>&1
}

#------------------------------------------------------------------------------
# Phase 1: Check Prerequisites
#------------------------------------------------------------------------------

check_go() {
    log_step "Checking Go installation"

    if ! command_exists go; then
        log_error "Go is not installed"
        return 1
    fi

    GO_VERSION=$(go version | sed -n 's/.*go\([0-9][0-9]*\.[0-9][0-9]*\).*/\1/p')
    if version_gte "$GO_VERSION" "$GO_MIN_VERSION"; then
        log_success "Go $GO_VERSION installed (>= $GO_MIN_VERSION required)"
        return 0
    else
        log_error "Go $GO_VERSION is too old (need >= $GO_MIN_VERSION)"
        return 1
    fi
}

check_git() {
    log_step "Checking Git installation"

    if ! command_exists git; then
        log_error "Git is not installed"
        return 1
    fi

    GIT_VERSION=$(git --version | sed -n 's/.*[^0-9]\([0-9][0-9]*\.[0-9][0-9]*\).*/\1/p')
    if version_gte "$GIT_VERSION" "$GIT_MIN_VERSION"; then
        log_success "Git $GIT_VERSION installed (>= $GIT_MIN_VERSION required)"
        return 0
    else
        log_error "Git $GIT_VERSION is too old (need >= $GIT_MIN_VERSION)"
        return 1
    fi
}

check_gh() {
    log_step "Checking GitHub CLI installation"

    if ! command_exists gh; then
        log_error "GitHub CLI (gh) is not installed"
        return 1
    fi

    log_success "gh installed: $(gh --version | head -1)"
    return 0
}

check_tmux() {
    log_step "Checking tmux installation"

    if ! command_exists tmux; then
        log_error "tmux is not installed"
        return 1
    fi

    TMUX_VERSION=$(tmux -V | sed -n 's/.*[^0-9]\([0-9][0-9]*\.[0-9][0-9]*\).*/\1/p')
    if version_gte "$TMUX_VERSION" "$TMUX_MIN_VERSION"; then
        log_success "tmux $TMUX_VERSION installed (>= $TMUX_MIN_VERSION required)"
        return 0
    else
        log_warn "tmux $TMUX_VERSION is old (recommend >= $TMUX_MIN_VERSION)"
        return 0
    fi
}

check_jq() {
    log_step "Checking jq installation"

    if ! command_exists jq; then
        log_error "jq is not installed"
        return 1
    fi

    log_success "jq installed: $(jq --version 2>&1 | head -1)"
    return 0
}

check_node() {
    log_step "Checking Node.js installation"

    if ! command_exists node; then
        log_error "Node.js is not installed"
        return 1
    fi

    if ! command_exists npm; then
        log_error "npm is not installed"
        return 1
    fi

    log_success "Node.js installed: $(node --version)"
    return 0
}

check_python() {
    log_step "Checking Python 3.12 installation"

    if ! command_exists python3.12; then
        log_error "python3.12 is not installed (required by the in-tree MCP issue-store server)"
        return 1
    fi

    PY_VERSION=$(python3.12 --version 2>/dev/null)
    log_success "Python installed: $PY_VERSION"

    # Verify MCP server dependencies are importable
    if ! python3 -c "import aiohttp, sqlalchemy" 2>/dev/null; then
        log_warn "Python MCP dependencies missing; installing from $SCRIPT_DIR/py/requirements.txt"
        pip3 install --break-system-packages --require-hashes -r "$SCRIPT_DIR/py/requirements.txt" || {
            log_error "Failed to install Python MCP dependencies"
            return 1
        }
    fi

    return 0
}

check_af() {
    log_step "Checking af installation"

    if ! command_exists af; then
        log_warn "af is not installed"
        return 1
    fi

    AF_VERSION=$(af version 2>/dev/null | head -1)
    log_success "af installed: $AF_VERSION"
    return 0
}

check_claude() {
    log_step "Checking Claude Code installation"

    if ! command_exists claude; then
        log_warn "Claude Code is not installed"
        return 1
    fi

    CLAUDE_VERSION=$(claude --version 2>/dev/null | head -1)
    log_success "Claude Code installed: $CLAUDE_VERSION"
    return 0
}

run_all_checks() {
    log_step "Running prerequisite checks"
    echo ""

    ERRORS=0
    WARNINGS=0

    check_go || ERRORS=$((ERRORS + 1))
    check_git || ERRORS=$((ERRORS + 1))
    check_gh || ERRORS=$((ERRORS + 1))
    check_tmux || ERRORS=$((ERRORS + 1))
    check_jq || ERRORS=$((ERRORS + 1))
    check_node || ERRORS=$((ERRORS + 1))
    check_python || ERRORS=$((ERRORS + 1))
    check_af || WARNINGS=$((WARNINGS + 1))
    check_claude || WARNINGS=$((WARNINGS + 1))

    echo ""
    echo "----------------------------------------"
    if [ $ERRORS -gt 0 ]; then
        log_error "Prerequisites check failed with $ERRORS error(s)"
        echo "  Fix the errors above before continuing."
        return 1
    elif [ $WARNINGS -gt 0 ]; then
        log_warn "Prerequisites passed with $WARNINGS warning(s) (will install missing)"
        return 0
    else
        log_success "All prerequisites satisfied!"
        return 0
    fi
}

#------------------------------------------------------------------------------
# Phase 2: Install Missing Dependencies
#------------------------------------------------------------------------------

install_af() {
    log_step "Installing af (agentfactory CLI)"

    if ! command_exists go; then
        log_error "Go is required to install af"
        return 1
    fi

    # Must run from the agentfactory source tree — no remote install fallback.
    if [ ! -f "$SCRIPT_DIR/go.mod" ] || ! grep -q "agentfactory" "$SCRIPT_DIR/go.mod"; then
        log_error "quickstart.sh must be run from the agentfactory source tree"
        log_error "Clone the repo first, then run ./quickstart.sh from within it"
        return 1
    fi

    log_info "Building af from local source: $SCRIPT_DIR"
    cd "$SCRIPT_DIR"
    make sync-formulas
    make sync-skills
    make build
    mkdir -p "$HOME/.local/bin"
    cp "$SCRIPT_DIR/af" "$HOME/.local/bin/af"

    # Build + install the optional web console (separate web/ module: web/go.mod) so the phase-5
    # launch guard's [ -x "$HOME/.local/bin/webui" ] test is true and the console actually starts.
    # Done here (not the Dockerfile) because this site already has the cloned repo + Go toolchain on
    # hand. Best-effort: a failed web build must NEVER abort the factory bootstrap (mirrors the
    # install_claude warn-don't-abort posture). `make build-webui` builds ./webui into the repo root
    # but does NOT install it, so build fresh (avoids installing a stale copy left by a prior branch)
    # THEN install. CWD is still "$SCRIPT_DIR" here (set above), where build-webui's -o ../webui lands;
    # `install -m 0755` is a deliberate deviation from the adjacent `cp af` — it sets the exec bit the
    # guard gates on in a single step.
    if make build-webui 2>/dev/null && [ -f webui ]; then
        install -m 0755 webui "$HOME/.local/bin/webui"
        log_info "Installed webui to ~/.local/bin/webui"
    else
        log_warn "build-webui failed or produced no binary; web UI will be skipped"
    fi

    export PATH="$HOME/.local/bin:$HOME/go/bin:$PATH"

    if command_exists af; then
        log_success "af installed: $(af version 2>/dev/null | head -1)"
    else
        log_error "af installation failed"
        return 1
    fi
}

install_claude() {
    log_step "Installing Claude Code CLI"

    # Check if already installed and working
    if command_exists claude; then
        if claude --version >/dev/null 2>&1; then
            log_success "Claude Code already installed: $(claude --version 2>/dev/null | head -1)"
            return 0
        fi
        log_warn "Claude Code found but not working, reinstalling..."
    fi

    # Try stable channel installer first
    log_info "Installing Claude Code via stable channel..."
    if curl -fsSL https://claude.ai/install.sh | bash -s -- stable; then
        export PATH="$HOME/.local/bin:$PATH"
        if command_exists claude; then
            log_success "Claude Code installed: $(claude --version 2>/dev/null | head -1)"
            return 0
        fi
    fi

    # Fallback: npm global install
    log_info "Stable channel failed, trying npm install..."
    if command_exists npm; then
        npm install -g @anthropic-ai/claude-code 2>&1 || {
            # Try with sudo if npm global fails
            if command_exists sudo; then
                sudo npm install -g @anthropic-ai/claude-code 2>&1 || {
                    log_error "Failed to install Claude Code via npm"
                    return 1
                }
            else
                log_error "Failed to install Claude Code via npm"
                return 1
            fi
        }
        if command_exists claude; then
            log_success "Claude Code installed: $(claude --version 2>/dev/null | head -1)"
            return 0
        fi
    fi

    log_error "Failed to install Claude Code via both methods"
    return 1
}

#------------------------------------------------------------------------------
# Phase 3: Configure
#------------------------------------------------------------------------------

configure_shell() {
    log_step "Configuring shell environment"

    local shell_config="$HOME/.bashrc"

    # Ensure .bashrc exists
    touch "$shell_config"

    # PATH block: write-once (skip if already present)
    if ! grep -q "agentfactory quickstart" "$shell_config" 2>/dev/null; then
        {
            echo ""
            echo "# Added by agentfactory quickstart"
            echo 'export PATH="$HOME/.local/bin:$HOME/go/bin:/usr/local/go/bin:$PATH"'
        } >> "$shell_config"
        log_info "Added PATH to $shell_config"
    else
        log_info "PATH already configured in $shell_config"
    fi

    # Model config block: replaceable (stripped and rewritten every run)
    local begin_marker="# BEGIN agentfactory model config"
    local end_marker="# END agentfactory model config"

    if grep -q "$begin_marker" "$shell_config" 2>/dev/null; then
        sed -i "/$begin_marker/,/$end_marker/d" "$shell_config"
    fi

    {
        echo "$begin_marker"
        echo 'export ANTHROPIC_MODEL="${ANTHROPIC_MODEL:-claude-opus-4-8}"'
        echo 'export ANTHROPIC_DEFAULT_OPUS_MODEL="${ANTHROPIC_DEFAULT_OPUS_MODEL:-claude-opus-4-8}"'
        echo 'export ANTHROPIC_DEFAULT_SONNET_MODEL="${ANTHROPIC_DEFAULT_SONNET_MODEL:-claude-sonnet-4-6}"'
        echo 'export CLAUDE_CODE_EFFORT_LEVEL="${CLAUDE_CODE_EFFORT_LEVEL:-xhigh}"'
        echo 'export CLAUDE_CODE_DISABLE_AUTO_MEMORY="${CLAUDE_CODE_DISABLE_AUTO_MEMORY:-1}"'
        echo "$end_marker"
    } >> "$shell_config"
    log_info "Updated model config in $shell_config"

    log_success "Shell environment configured"
}

configure_factory() {
    log_step "Configuring agentfactory workspace"

    # Find the repo directory — first git repo under the workspace (the cloned target).
    local repo_dir=""
    for d in "$WORKSPACE_DIR"/*/; do
        [ -d "$d/.git" ] && { repo_dir="${d%/}"; break; }
    done

    if [ -z "$repo_dir" ]; then
        log_error "No git repository found under $WORKSPACE_DIR"
        return 1
    fi

    cd "$repo_dir"

    # Ensure PATH includes our tools
    export PATH="$HOME/.local/bin:$HOME/go/bin:/usr/local/go/bin:$PATH"

    # Ensure Python MCP server can find py/ source package.
    # SCRIPT_DIR points to the agentfactory source tree (where quickstart.sh lives).
    # The af binary's PYTHONPATH resolution uses AF_SOURCE_ROOT as a fallback
    # when factoryRoot (the target project) doesn't contain py/.
    export AF_SOURCE_ROOT="$SCRIPT_DIR"

    # Initialize factory (creates .agentfactory/, .agentfactory/hooks/, .agentfactory/store/)
    # Always run: configs are write-if-absent, hooks always update
    log_info "Running af install --init..."
    af install --init || {
        log_error "af install --init failed"
        return 1
    }
    log_success "Factory initialized"

    # Provision manager agent
    if [ ! -d ".agentfactory/agents/manager" ]; then
        log_info "Provisioning manager agent..."
        af install manager || {
            log_error "af install manager failed"
            return 1
        }
        log_success "Manager agent provisioned"
    else
        log_info "Manager agent already provisioned"
    fi

    # Provision supervisor agent
    if [ ! -d ".agentfactory/agents/supervisor" ]; then
        log_info "Provisioning supervisor agent..."
        af install supervisor || {
            log_error "af install supervisor failed"
            return 1
        }
        log_success "Supervisor agent provisioned"
    else
        log_info "Supervisor agent already provisioned"
    fi
}

configure_git_defaults() {
    log_step "Configuring git defaults"

    if ! git config --global init.defaultBranch >/dev/null 2>&1; then
        git config --global init.defaultBranch main
        log_info "Set git default branch to 'main'"
    fi

    # Set user identity if not configured (required for commits inside container).
    # Use the agentfactory default identity (issue #371 AC-2/AC-3) so commits in a
    # fresh container are authored by agentfactory-cli — not a placeholder that the
    # presence-gate would later honour, silently failing AC-2 in the docker target.
    # On a clean install factory.json does not exist yet: it is written later by
    # `af install --init` in configure_factory (below), so the `[ -f ]` guard fails,
    # the jq read is skipped, and the literal fallback (the same issue-#371 C-3
    # identity as the Go constants) is what runs. The jq read is best-effort and
    # only applies on a re-run where factory.json already exists.
    if ! git config --global user.email >/dev/null 2>&1; then
        local gi_name gi_email factory_json=".agentfactory/factory.json"
        if command -v jq >/dev/null 2>&1 && [ -f "$factory_json" ]; then
            gi_name=$(jq -r '.git_identity.name // empty' "$factory_json" 2>/dev/null)
            gi_email=$(jq -r '.git_identity.email // empty' "$factory_json" 2>/dev/null)
        fi
        : "${gi_name:=agentfactory-cli}"
        : "${gi_email:=293373236+agentfactory-cli@users.noreply.github.com}"
        git config --global user.email "$gi_email"
        git config --global user.name "$gi_name"
        log_info "Set default git identity ($gi_name)"
    fi

    log_success "Git configured"
}

#------------------------------------------------------------------------------
# Argument Parsing
#------------------------------------------------------------------------------

show_help() {
    cat << 'EOF'
Agentfactory Quickstart
=======================

Usage:
  ./quickstart.sh           Full setup (always auto mode, no prompts)
  ./quickstart.sh --check   Check prerequisites only
  ./quickstart.sh --help    Show this help

This script runs inside a container created by quickdocker.sh.
It installs bd, af, and Claude Code, then configures the factory workspace.

After running this script:
  af up                     Start agent sessions
  af down                   Stop agent sessions

EOF
}

parse_args() {
    while [[ $# -gt 0 ]]; do
        case $1 in
            --check)
                CHECK_ONLY=true
                shift
                ;;
            --help|-h)
                show_help
                exit 0
                ;;
            *)
                log_warn "Unknown option: $1 (ignoring)"
                shift
                ;;
        esac
    done
}

#------------------------------------------------------------------------------
# Main
#------------------------------------------------------------------------------

configure_login_init() {
    log_step "Installing webui login-shell restart guard"

    # Resolve the factory root (the cloned target repo under WORKSPACE_DIR) so the guard can PIN
    # AF_ROOT. At `docker start`, PID-1 `bash --login` starts with CWD=$HOME, NOT the factory root,
    # so the guard must NOT rely on $PWD (Phase-4 / Gap 8). This runs IN the container on every
    # quickstart, so it reaches existing/customer containers on upgrade — unlike the create-time
    # quickdocker.sh Step 7 guard, which never reaches an already-running container.
    local factory_root=""
    for d in "$WORKSPACE_DIR"/*/; do
        [ -d "$d/.git" ] && { factory_root="${d%/}"; break; }
    done
    if [ -z "$factory_root" ]; then
        log_warn "No factory root under $WORKSPACE_DIR; skipping webui login-shell guard"
        return 0
    fi

    local profile="$HOME/.bash_profile"
    local begin="# BEGIN agentfactory webui login guard"
    local end="# END agentfactory webui login guard"
    touch "$profile"
    # Idempotent (replaceable) + dedup: strip our block and any legacy create-time block.
    sed -i "/$begin/,/$end/d" "$profile" 2>/dev/null || true
    sed -i "/# >>> phase4 webui login-init relaunch guard/,/# <<< phase4 webui login-init relaunch guard/d" "$profile" 2>/dev/null || true
    {
        echo "$begin"
        echo "# bash --login (container PID 1 on docker start) reads ~/.bash_profile, NOT ~/.bashrc,"
        echo "# so the optional web console is relaunched here on every restart (AC-2). AF_ROOT is"
        echo "# PINNED because a login shell starts at \$HOME, not the repo (Phase-4 / Gap 8)."
        echo "# Idempotent: webui's rendezvous.Ensure no-ops if a healthy server is already up."
        echo "[ -f \"\$HOME/.bashrc\" ] && . \"\$HOME/.bashrc\""
        echo "if [ -x \"\$HOME/.local/bin/webui\" ]; then"
        echo "    AF_ROOT=\"\${AF_ROOT:-$factory_root}\" nohup \"\$HOME/.local/bin/webui\" >/tmp/webui.log 2>&1 &"
        echo "fi"
        echo "$end"
    } >> "$profile"
    log_success "Installed webui login-shell restart guard (AF_ROOT pinned to $factory_root)"
}

main() {
    parse_args "$@"

    echo ""
    echo "=========================================="
    echo "  Agentfactory Quickstart"
    echo "=========================================="
    echo ""

    # Ensure PATH includes common tool locations
    export PATH="$HOME/.local/bin:$HOME/go/bin:/usr/local/go/bin:$PATH"

    # Check-only mode
    if [ "$CHECK_ONLY" = true ]; then
        run_all_checks
        exit $?
    fi

    # Phase 1: Check prerequisites (base image tools)
    log_step "Phase 1: Checking prerequisites"

    check_go || {
        log_error "Go is required and should be in the base image"
        exit 1
    }
    check_git || {
        log_error "Git is required and should be in the base image"
        exit 1
    }
    check_gh || {
        log_error "GitHub CLI is required and should be in the base image"
        exit 1
    }
    check_tmux || {
        log_error "tmux is required and should be in the base image"
        exit 1
    }
    check_jq || {
        log_error "jq is required and should be in the base image"
        exit 1
    }
    check_node || {
        log_error "Node.js is required and should be in the base image"
        exit 1
    }
    check_python || {
        log_error "Python 3.12 is required and should be in the base image"
        exit 1
    }

    # Phase 2: Install missing application dependencies
    log_step "Phase 2: Installing application dependencies"

    # Always rebuild af from source to pick up latest changes
    install_af

    if ! check_claude; then
        install_claude || log_warn "Claude Code install failed (can be installed later)"
    fi

    # Phase 3: Configure
    log_step "Phase 3: Configuring workspace"

    configure_git_defaults
    configure_shell
    configure_factory
    configure_login_init

    # Phase 5 (C0): best-effort, IFF-available web console launch — driven from the container
    # bootstrap, NOT from `af up`/up.go, so af-core keeps ZERO UI knowledge (cross-review H-3).
    # Mirrors the watchdog/dispatcher warn-don't-abort posture: when the binary is absent we skip
    # silently and the factory bootstrap proceeds normally; when present we launch it detached and
    # it owns its own rendezvous + start-lock (.runtime/webui_server.json) so repeated launches are
    # idempotent. The socket stays loopback (never -p-published; CR-1) — reach it via SSH
    # local-forward (see README "Web Console").
    # >>> phase5 webui launch guard >>>
    if [ -x "$HOME/.local/bin/webui" ]; then
        # Export AF_ROOT so the detached webui's served root is deterministic rather than
        # CWD-dependent (Gap 8). At bootstrap $PWD is the factory root (configure_factory cd'd
        # here first); the persistent login-init relaunch guard (quickdocker*.sh Step 7) pins
        # AF_ROOT to the known repo path instead, since a login shell starts at $HOME.
        AF_ROOT="${AF_ROOT:-$PWD}" nohup "$HOME/.local/bin/webui" >/tmp/webui.log 2>&1 &
        # Honest status: the detached webui binds its loopback listener and publishes its
        # rendezvous file (.runtime/webui_server.json) ASYNCHRONOUSLY, so a success log at
        # spawn time would lie if the bind fails (port conflict) or startup panics. The
        # rendezvous file is written only AFTER the listener binds — poll for it (bounded)
        # before claiming success, and downgrade to a warning on timeout.
        webui_rendezvous="${AF_ROOT:-$PWD}/.runtime/webui_server.json"
        webui_ready=""
        webui_attempts=0
        while [ "$webui_attempts" -lt 25 ]; do
            if [ -f "$webui_rendezvous" ]; then
                webui_ready=1
                break
            fi
            webui_attempts=$((webui_attempts + 1))
            sleep 0.2
        done
        if [ -n "$webui_ready" ]; then
            log_success "Web UI started (optional)"
        else
            log_warn "Web UI launch attempted but did not confirm binding within 5s (see /tmp/webui.log); continuing"
        fi
    else
        log_info "webui binary not present; skipping optional web UI"
    fi
    # <<< phase5 webui launch guard <<<

    # Done!
    echo ""
    echo "=========================================="
    echo "  Setup Complete!"
    echo "=========================================="
    echo ""
    echo "Installed tools:"
    command_exists af && echo "  af:     $(af version 2>/dev/null | head -1)"
    command_exists claude && echo "  claude: $(claude --version 2>/dev/null | head -1)"
    echo ""
    echo "Next steps:"
    echo "  af up       # Start agent sessions"
    echo "  af down     # Stop agent sessions"
    echo ""
}

main "$@"

# If running in Docker, restart shell to pick up PATH changes
if [[ -f /.dockerenv ]]; then
    echo "Restarting shell to apply PATH changes..."
    exec bash
fi
