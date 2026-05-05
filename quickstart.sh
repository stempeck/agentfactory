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
    make build
    mkdir -p "$HOME/.local/bin"
    cp "$SCRIPT_DIR/af" "$HOME/.local/bin/af"

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
        echo 'export ANTHROPIC_MODEL="claude-opus-4-6"'
        echo 'export ANTHROPIC_DEFAULT_OPUS_MODEL="claude-opus-4-6"'
        echo 'export ANTHROPIC_DEFAULT_SONNET_MODEL="claude-sonnet-4-6"'
        echo 'export CLAUDE_CODE_EFFORT_LEVEL="high"'
        echo 'export CLAUDE_CODE_DISABLE_AUTO_MEMORY=1'
        echo "$end_marker"
    } >> "$shell_config"
    log_info "Updated model config in $shell_config"

    log_success "Shell environment configured"
}

configure_factory() {
    log_step "Configuring agentfactory workspace"

    # Find the repo directory
    local repo_dir
    repo_dir=$(find "$WORKSPACE_DIR" -maxdepth 1 -mindepth 1 -type d | head -1)

    if [ -z "$repo_dir" ]; then
        log_error "No repository found under $WORKSPACE_DIR"
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

    # Initialize factory (creates .agentfactory/, hooks/, .beads/)
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

    # Set user identity if not configured (required for commits inside container)
    if ! git config --global user.email >/dev/null 2>&1; then
        git config --global user.email "dev@agentfactory.local"
        git config --global user.name "agentfactory"
        log_info "Set default git identity"
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
