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
#   ./quickstart.sh --check   # Read-only drift report against the pins (changes nothing)
#   ./quickstart.sh --litellm # Full setup, then stand up the LiteLLM gateway
#                             # (the one opt-in step that may prompt: OpenAI key)
#   ./quickstart.sh --help    # Show this help
#
# This script is designed to run inside a container created by quickdocker.sh.
# It assumes the base image (from Dockerfile) provides Go, Node, git, gh, tmux, jq,
# build-essential, sqlite3, openssh-client, and Python 3.12.
#

# Cleanup trap for temporary files
CLEANUP_DIRS=()
cleanup() {
    # The ${…+…} guard: macOS's bash 3.2 treats an empty array as unbound under `set -u`.
    for dir in ${CLEANUP_DIRS[@]+"${CLEANUP_DIRS[@]}"}; do
        rm -rf "$dir" 2>/dev/null || true
    done
    # _gateway_stop's reconcile marker (K12/D22, issue #693 Phase 4): bash EXIT traps do not
    # stack, so the marker's removal is folded into this single existing trap rather than a
    # second, competing `trap … EXIT` inside _reconcile_gateway (decisions.md D9). SIGTERM gets
    # its own exiting handler (below) so a killed bootstrap runs cleanup and then exits 143
    # instead of resuming mid-reconcile with the marker cleared (decisions.md D10).
    rm -f .runtime/gateway/reconciling 2>/dev/null || true
}
trap cleanup EXIT
trap 'cleanup; exit 143' TERM

#------------------------------------------------------------------------------
# Configuration
#------------------------------------------------------------------------------

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORKSPACE_DIR="$HOME/af"
GO_MIN_VERSION="1.24"
GIT_MIN_VERSION="2.20"
TMUX_MIN_VERSION="3.0"
CHECK_ONLY=false
# Convergence state (K1/K16): the ledger of required failures (newline-delimited names), the row
# store the END block writes as convergence.json (one TAB-separated row per component), and the
# change flags that decide the closing `af down && af up` line.
CONVERGE_FAILURES=""
CONVERGE_ROWS=""
APT_CHANGED=0
PYTHON_CHANGED=0
WEBUI_REBUILT=0
NODE_APT_ERROR=""
RUN_AT=""
WITH_LITELLM=false
LITELLM_AUTH_MODE=""
LITELLM_VERSION="1.93.2"
LITELLM_PORT=4000
CODEX_LOGIN_TIMEOUT=900

# Telemetry backend (OpenObserve). Provisioned by DEFAULT — operator decision O-1 is opt-out
# (--no-telemetry), the design's recommendation (design-doc.md:315; ux.md F1). Pinned version +
# per-arch sha256 make an upgrade a deliberate quickstart re-run, never a silent drift (the CVE
# story, design-doc.md:380). Recompute both digests when bumping OPENOBSERVE_VERSION:
#   curl -fsSL <url>/openobserve-<ver>-linux-<arch>.tar.gz | sha256sum
WITH_TELEMETRY=true
OPENOBSERVE_VERSION="v1.1.2"
OPENOBSERVE_SHA256_AMD64="225883447a9e3c75cb52d5a64ece8618cf0d8f3d2eb0b424eebb2e51b0126faa"
OPENOBSERVE_SHA256_ARM64="4d00e03155594dba473202fdca4f2b8c3b549138f9bab4ff0f6894feda4b7354"
# Hoisted (D-67): --check and the converge-if-present pass probe the backend outside setup_telemetry.
OPENOBSERVE_BIN="$HOME/.local/bin/openobserve"

# K6 pin block (issue #743). Every value is a literal, never a ${X:-…} default: the image exports
# GO_VERSION/GO_SHA256_* for its own go1.24.2. Each digest's evidence is a row of
# .designs/743/implementation-plan/pin-data.md; a bump is a reviewed edit here and in
# internal/cmd/quickstart_pins_test.go, never a runtime input.
NODE_MAJOR="22"
NODE_APT_VERSION="22.23.4-1nodesource1"
GH_VERSION="2.103.2"
GH_SHA256_ARM64="4c3e99a50bd8b181a3fe9d69b25dd978bac768148d88f01507c6b4d1ef61a443"
GH_SHA256_AMD64="62ccf747115ddc61237efffdde66a3c8f70ba249433dc64f70801089b59c59c8"
GO_TOOLCHAIN_VERSION="go1.26.10"
GO_SHA256_ARM64="3f4227ce7b206a513e794f1393adb473a07b46e94d18bd15d1c684bbb7319797"
GO_SHA256_AMD64="ccfe7ae822d2c2f41f4fe7c956199967a4dac22d0ee0d2b2bfdb440aab3b20c5"
PLAYWRIGHT_VERSION="1.64.1"
PLAYWRIGHT_SHA512="1c2ad5847af9410bb0628b301141936fb806a79db6c9f95bbff493f27178b05586b8b8d00add7b43f12e6e918648d2989f5b6c4c23d9c6a1e11c27d5511bbb50"
PLAYWRIGHT_CORE_SHA512="042e2cf32e9211503a435ad2c594eda3c848d4a508df15e51c6660bc6a8fe08a8f748fdae855fd1f6f480cdcfbbcaa3d976d5be8b7d8a2c49c3cbe8cdf1c720d"
PLAYWRIGHT_CHROMIUM_TREE_SHA256_ARM64="ba6d2c17e743830e728e04eddbbd329ffaa69560d62621546086a221ca23ea21"
PLAYWRIGHT_CHROMIUM_TREE_SHA256_AMD64="221c5699c30db0c6eebe41ea883632001e2fb113a6dcdedabfce1dd5aa35d3dd"
PLAYWRIGHT_HEADLESS_SHELL_TREE_SHA256_ARM64="875c4b701f9d9d2f667385654ffd9d4dfa67c46d9542121eba80a9a556b1cc39"
PLAYWRIGHT_HEADLESS_SHELL_TREE_SHA256_AMD64="a292efc9ef3eb31933f20151cc39e619ba5b8d8caccd4e62c0dbfc6c1e41e4f3"
CODEX_VERSION="0.164.0"
CODEX_SHA512="4d68fb506c8f45edcf8ff03775d340c6cd6d7b76bf5bfc4077e2742c7aeb1539ac49f6a118c38b17fbe7f3c0f7402220013dc1d6b9b5cbf677358134af1e9c65"
CODEX_LINUX_ARM64_SHA512="2cbe624d08f20cbe18457e38116aa121f110e6e1a79a0c64401252c63652789f0bd4e83cde9765cd4256fcf449efaa1a570395821e875f0b8f12204d10753896"
CODEX_LINUX_AMD64_SHA512="5cb028696b06393da487397e3f81580cf4e4cc0afd5b45455c569592a945764334b286b5807ff84b4c120f9ec49371c7c248ab0041b3dbf3307a1b1e87e97932"
CLAUDE_CODE_VERSION="2.1.308"
CLAUDE_CODE_SHA256_ARM64="fdd22e6cbb734d580f5664c48df4294eeec850218ff07f186f9af30426396ee1"
CLAUDE_CODE_SHA256_AMD64="356733f185fa988e6bf3aa9a555269fdd167982a2e2a80d3c8a78037bb92ad58"
CLAUDE_CODE_NPM_SHA512="6f61e7b3383237bdf045314ff8860f0c9367f400d79c6ea3af6ea26cbfb0d371d22aa7dbe86a84f099dfaf89b3dfb66fe004ff34ff81e7fddcd45f3d59375648"
CLAUDE_CODE_LINUX_ARM64_SHA512="c6d4f609dee7fb264df4f8966d61da65ff70db961f955fbb54e5f92013c1fc8584aca64586cb2ddbf032d6d4a0abf333a75be9e7ac70151a8f6ba50023d05923"
CLAUDE_CODE_LINUX_AMD64_SHA512="7df52bc9ebe91fb3ce4ad3c9ff1a54239475a3361b1fda3f0db87fc4f4a6c9525e8442e744c6664677a37509aad137a3396fd26cd9c9161086320c08b0ea854d"
# 46 characters from inside the emSign ECC Root CA - G3 certificate body: 2.1.224 embeds that root
# as an unlabeled PEM, so the DER label alone misses it (H-2).
EMSIGN_G3_BODY_PROBE="FBVIcUqEqy6HyC5ltqtdyzdc6LBtCGI79G1Y4PPwT01xyS"
TELEMETRY_PORT=5080
# Seconds to wait for OpenObserve to answer /healthz on first launch. Its FIRST
# start initializes a metadata store on an empty data dir and regularly runs past
# the 30s that suffices for LiteLLM — too short a window is why a clean install was
# seen to WARN-and-skip with the backend still coming up. Regression-guarded by
# internal/cmd/telemetry_readiness_integration_test.go.
TELEMETRY_READY_TIMEOUT=90

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
# Convergence Helpers (issue #743)
#------------------------------------------------------------------------------
# Every vendor channel converges through _converge: probe -> compare with the pin -> install ->
# re-probe -> required/optional policy. A re-run therefore upgrades (or downgrades) a running
# container to the exact pins instead of skipping whatever is already present.

_arch() {
    case "$(uname -m)" in
        aarch64|arm64) echo arm64 ;;
        x86_64|amd64) echo amd64 ;;
        *) return 1 ;;
    esac
}

# _record_row NAME PIN INSTALLED RESULT: one TAB-separated row per component in CONVERGE_ROWS.
# A component can converge twice in one run (the converge-if-present pass, then its setup phase),
# so a later row replaces an earlier one by name — except that `kept` never masks the earlier
# outcome it is the consequence of. A `converged` row also takes its name off the ledger, so the END
# verdict agrees with the table; names ledgered without a row of their own keep failing the run.
_record_row() {
    local name="$1" pin="$2" installed="$3" result="$4" integrity at row line rest="" tab
    tab="$(printf '\t')"
    case "$name" in
        claude|gh|go|openobserve) integrity="sha256" ;;
        codex) integrity="sha512" ;;
        playwright) integrity="sha512+tree-sha256" ;;
        nodejs) integrity="apt-signed (nodesource)" ;;
        ubuntu-security) integrity="apt-signed (security pocket)" ;;
        python-lock|litellm-lock) integrity="pip --require-hashes" ;;
        *) integrity="n/a" ;;
    esac
    installed="$(printf '%s' "$installed" | tr '\t\n' '  ')"
    at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    row="$name$tab$pin$tab$installed$tab$integrity$tab$result$tab$at"
    while IFS= read -r line; do
        [ -n "$line" ] || continue
        case "$line" in
            "$name$tab"*)
                if [ "$result" = kept ] && [ "$(printf '%s' "$line" | cut -f5)" != kept ]; then
                    row="$line"
                fi
                ;;
            *) rest="$rest$line"$'\n' ;;
        esac
    done <<EOF
$CONVERGE_ROWS
EOF
    CONVERGE_ROWS="$rest$row"$'\n'
    if [ "$result" = converged ]; then
        rest=""
        while IFS= read -r line; do
            if [ -n "$line" ] && [ "$line" != "$name" ]; then
                rest="$rest$line"$'\n'
            fi
        done <<EOF
$CONVERGE_FAILURES
EOF
        CONVERGE_FAILURES="$rest"
    fi
}

# _quarantine_stale NAME: after a failed optional install, rename the stale Chromium builds so the
# render probe fails honestly instead of passing on an old browser. Revision 1243 is the reference
# integration's linked build and stays. A rename, never a delete (ADR-017).
_quarantine_stale() {
    [ "$1" = playwright ] || return 0
    local cache="$HOME/.cache/ms-playwright" d base ts
    ts="$(date -u +%Y%m%dT%H%M%SZ)"
    for d in "$cache"/chromium-* "$cache"/chromium_headless_shell-*; do
        [ -d "$d" ] || continue
        base="${d##*/}"
        case "$base" in
            *.stale-*|*-1243) continue ;;
        esac
        if mv "$d" "$d.stale-$ts"; then
            log_warn "quarantined $base as $base.stale-$ts"
        fi
    done
    return 0
}

# _converge NAME PIN PROBE_FN INSTALL_FN CLASS (design-doc.md Interface): PROBE_FN prints one line in
# PIN's syntax (nothing when absent); INSTALL_FN prints its own reason when it fails. Returns 1 for a
# drifted row under --check and for a failed required install, so every call site is `|| true`:
# the row and the CONVERGE_FAILURES ledger carry the outcome to the END report.
_converge() {
    local name="$1" pin="$2" probe="$3" install="$4" class="$5" have old msg
    have="$("$probe" 2>/dev/null)" || true
    if [ "$have" = "$pin" ]; then
        log_success "$name: $pin (kept)"
        _record_row "$name" "$pin" "$have" kept
        return 0
    fi
    old="${have:-absent}"
    if [ "$CHECK_ONLY" = true ]; then
        log_warn "$name: $old != pinned $pin (would converge)"
        _record_row "$name" "$pin" "$old" drift
        return 1
    fi
    if "$install"; then
        # A new binary earlier on PATH (gh, go) is invisible to the re-probe until bash forgets the
        # location it hashed for the old one.
        hash -r
        have="$("$probe" 2>/dev/null)" || true
        if [ "$have" = "$pin" ]; then
            log_success "$name: $pin (converged from $old)"
            _record_row "$name" "$pin" "$have" converged
            return 0
        fi
        msg="$name: ${have:-absent} != pinned $pin after install — the installer put a different build on PATH ($(command -v "$name" || echo none))"
    else
        hash -r
        have="$("$probe" 2>/dev/null)" || true
        msg="$name: ${have:-absent} != pinned $pin — install failed (reason above)"
    fi
    if [ "$class" = optional ]; then
        _quarantine_stale "$name"
        log_warn "$msg — visual checks will escalate to the human gate"
        _record_row "$name" "$pin" "${have:-absent}" skipped-optional
        return 0
    fi
    log_error "$msg"
    CONVERGE_FAILURES="$CONVERGE_FAILURES$name"$'\n'
    _record_row "$name" "$pin" "${have:-absent}" FAILED
    return 1
}

# _pip_install_locked LOCK [--system | --report FILE]: the one hash-locked install line (K2).
# --system installs the same lock into the root-owned system site, which the image's Dockerfile pip
# line populated; --report has pip write what the full-lock install actually changed to FILE.
# Debian ships that site's pip and wheel without a RECORD, so pip cannot replace them: those two go
# first with --ignore-installed, then the full lock upgrades every other package in place. A blanket
# --ignore-installed would leave each superseded version's dist-info behind, still reporting it.
_pip_install_locked() {
    local lock="$1" rc=0 subset report=()
    if [ "${2-}" = --report ]; then
        report=(--report "$3")
    fi
    if { [ "${2-}" = --system ] || [ "$(id -u)" = 0 ]; } && grep -Eq '^(pip|wheel)==' "$lock"; then
        subset="$(mktemp)" || { log_error "python lock $lock: mktemp failed"; return 1; }
        awk '/^[A-Za-z0-9]/ { keep = ($0 ~ /^(pip|wheel)==/) } keep && !/^[[:space:]]*#/' "$lock" >"$subset"
        if [ "${2-}" = --system ] && [ "$(id -u)" != 0 ]; then
            sudo -n pip3 install --ignore-installed --no-deps --break-system-packages --require-hashes --only-binary :all: --disable-pip-version-check -r "$subset" || rc=$?
        else
            pip3 install --ignore-installed --no-deps --break-system-packages --require-hashes --only-binary :all: --disable-pip-version-check -r "$subset" || rc=$?
        fi
        rm -f "$subset"
    fi
    if [ "$rc" -eq 0 ]; then
        if [ "${2-}" = --system ] && [ "$(id -u)" != 0 ]; then
            sudo -n pip3 install --break-system-packages --require-hashes --only-binary :all: --disable-pip-version-check -r "$lock" || rc=$?
        else
            pip3 install --break-system-packages --require-hashes --only-binary :all: --disable-pip-version-check ${report[@]+"${report[@]}"} -r "$lock" || rc=$?
        fi
    fi
    if [ "$rc" -ne 0 ]; then
        log_error "python lock $lock failed — --only-binary :all: refuses source distributions; a missing wheel for $(uname -m) is a lock defect, not a network error"
        return 1
    fi
    return 0
}

# _pip_would_change LOCK (K3): 0 = the lock would change the user site (the count is printed),
# 1 = no change, 2 = pip could not tell. The report goes to a file because pip skips its PEP 668
# refusal for a dry run only when --report is given, and pip's exit status is kept apart from the
# report's parse so an error never reads as "no change".
_pip_would_change() {
    local lock="$1" report rc=0 n
    report="$(mktemp)" || return 2
    pip3 install --dry-run --quiet --report "$report" --break-system-packages --require-hashes --only-binary :all: --disable-pip-version-check -r "$lock" >/dev/null 2>&1 || rc=$?
    if [ "$rc" -ne 0 ]; then
        rm -f "$report"
        return 2
    fi
    n="$(jq '.install | length' "$report" 2>/dev/null)" || n=""
    rm -f "$report"
    case "$n" in
        ''|*[!0-9]*) return 2 ;;
        0) return 1 ;;
    esac
    printf '%s\n' "$n"
    return 0
}

# _npm_install_pinned NAME VERSION SHA512 [PLATFORM_NAME PLATFORM_SHA512] (K4): npm pack -> sha512
# -> install the verified local tarball with --ignore-scripts -> overlay the platform package (the
# bytes that actually execute) from its own verified tarball. The global prefix stays where the
# image put it (root-owned /usr), so the sudo route is decided once, before anything is packed.
# Returns 3 when neither route exists, so callers print their own refusal.
_npm_install_pinned() {
    local name="$1" version="$2" sha="$3" pname="${4-}" psha="${5-}"
    local npm_root as_root="" work tgz pspec ptgz dest postinstall
    npm_root="$(npm root -g 2>/dev/null || true)"
    if [ -n "$npm_root" ] && [ -w "$npm_root" ]; then
        as_root=""
    elif command_exists sudo && sudo -n true 2>/dev/null; then
        as_root="sudo -n"
    else
        return 3
    fi

    # Never /tmp: it is noexec in agentfactory containers, and an unpacked package may run there.
    mkdir -p "$HOME/.cache/agentfactory" || return 1
    work="$(mktemp -d "$HOME/.cache/agentfactory/npm.XXXXXX")" || return 1
    CLEANUP_DIRS+=("$work")

    tgz="$(npm pack "$name@$version" --pack-destination "$work" --silent 2>/dev/null | tail -1)" || tgz=""
    if [ -z "$tgz" ] || [ ! -f "$work/$tgz" ]; then
        log_error "$name@$version: npm pack failed"
        return 1
    fi
    if ! printf '%s  %s\n' "$sha" "$work/$tgz" | sha512sum --check --strict >/dev/null 2>&1; then
        log_error "$name@$version: tarball sha512 does not match the pinned value — NOT installing (supply-chain signal; see Appendix A of issue #743)"
        return 1
    fi
    mkdir -p "$work/meta" || return 1
    tar -xzf "$work/$tgz" -C "$work/meta" package/package.json || return 1

    if [ -n "$pname" ]; then
        # The version comes from the verified tarball's own manifest; an `npm:` alias (codex's
        # platform packages) names the real package to pack.
        pspec="$(jq -r --arg p "$pname" '.optionalDependencies[$p] // .dependencies[$p] // empty' "$work/meta/package/package.json" 2>/dev/null)" || pspec=""
        case "$pspec" in
            '') log_error "$name@$version: its package.json names no $pname dependency"; return 1 ;;
            npm:*) pspec="${pspec#npm:}" ;;
            *) pspec="$pname@$pspec" ;;
        esac
        ptgz="$(npm pack "$pspec" --pack-destination "$work" --silent 2>/dev/null | tail -1)" || ptgz=""
        if [ -z "$ptgz" ] || [ ! -f "$work/$ptgz" ]; then
            log_error "$pspec: npm pack failed"
            return 1
        fi
        if ! printf '%s  %s\n' "$psha" "$work/$ptgz" | sha512sum --check --strict >/dev/null 2>&1; then
            log_error "$pspec: tarball sha512 does not match the pinned value — NOT installing (supply-chain signal; see Appendix A of issue #743)"
            return 1
        fi
    fi

    if [ -n "$as_root" ]; then
        sudo -n npm install -g --ignore-scripts "$work/$tgz" || return 1
    else
        npm install -g --ignore-scripts "$work/$tgz" || return 1
    fi
    npm_root="$(npm root -g 2>/dev/null)" || return 1
    if [ -n "$pname" ]; then
        dest="$npm_root/$name/node_modules/$pname"
        $as_root rm -rf "$dest" || return 1
        $as_root mkdir -p "$dest" || return 1
        $as_root tar -xzf "$work/$ptgz" -C "$dest" --strip-components=1 || return 1
    fi
    # Install scripts were skipped above so that nothing ran before the overlay; a package whose
    # postinstall wires up the platform binary (claude-code) runs it now, over verified bytes.
    postinstall="$(jq -r '.scripts.postinstall // empty' "$work/meta/package/package.json" 2>/dev/null)" || postinstall=""
    if [ -n "$postinstall" ]; then
        (cd "$npm_root/$name" && $as_root npm run --silent postinstall) || return 1
    fi
    return 0
}

#------------------------------------------------------------------------------
# Root layer (K5): the Ubuntu security pocket and the Node.js pin
#------------------------------------------------------------------------------

# _apt_get: the only two literal apt-get lines. sudo's env_reset drops an exported
# DEBIAN_FRONTEND, so the variable is passed through `env` (H-5); --check simulates as the user.
_apt_get() {
    if [ "$CHECK_ONLY" = true ] || [ "$(id -u)" = 0 ]; then
        env DEBIAN_FRONTEND=noninteractive apt-get -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold "$@"
    else
        sudo -n env DEBIAN_FRONTEND=noninteractive apt-get -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold "$@"
    fi
}

# Read, never sourced: /etc/os-release assigns names (VERSION, ID, …) this script would inherit.
_os_codename() {
    local c
    c="$(sed -n 's/^VERSION_CODENAME=//p' /etc/os-release 2>/dev/null | tr -d "\"'")"
    if [ -z "$c" ]; then
        c="$(sed -n 's/^UBUNTU_CODENAME=//p' /etc/os-release 2>/dev/null | tr -d "\"'")"
    fi
    printf '%s\n' "$c"
}

# _pending_security CODENAME: every installed package with ANY version above the installed one
# that a CODENAME-security source carries. The candidate's own pocket is not enough: a newer
# -updates build shadows the security fix, so `apt-get upgrade -s` never lists it (H-6). nodejs is
# pinned on its own line. apt-cache lists versions newest first, so "above the *** line" is "newer".
_pending_security() {
    local codename="$1" pkgs
    pkgs="$(dpkg-query -W -f='${db:Status-Abbrev} ${Package}\n' 2>/dev/null | awk '$1 == "ii" {print $2}')" || pkgs=""
    [ -n "$pkgs" ] || return 0
    # shellcheck disable=SC2086 # one argument per package name
    apt-cache policy $pkgs 2>/dev/null | awk -v pocket=" $codename-security/" '
        /^[^ ]/ { pkg = $1; sub(/:$/, "", pkg); table = 0; above = 0; next }
        /^  Version table:/ { table = 1; above = 1; next }
        table && /^ \*\*\* / { above = 0; next }
        table && above && index($0, pocket) { print pkg }
    ' | grep -v -x nodejs | sort -u || true
}

# _ensure_nodesource_list NODE_MAJOR: points the image's signed NodeSource source at NODE_MAJOR.
# An absent list or keyring is reported, never bootstrapped: a customer image without them gets the
# C-7 message when the Node pin cannot resolve.
_ensure_nodesource_list() {
    local major="$1" list="/etc/apt/sources.list.d/nodesource.list" key="/usr/share/keyrings/nodesource.gpg" line
    if [ ! -f "$list" ] || [ ! -f "$key" ]; then
        log_warn "no NodeSource apt source ($list or $key is absent); nodejs=$NODE_APT_VERSION can resolve only from another configured source"
        return 0
    fi
    if grep -q "node_${major}.x" "$list"; then
        return 0
    fi
    line="deb [arch=$(dpkg --print-architecture) signed-by=$key] https://deb.nodesource.com/node_${major}.x nodistro main"
    if [ "$(id -u)" = 0 ]; then
        printf '%s\n' "$line" >"$list" || return 0
    else
        printf '%s\n' "$line" | sudo -n tee "$list" >/dev/null || return 0
    fi
    log_info "NodeSource apt source now tracks node_${major}.x"
}

# _retire_gh_apt_source SOURCES_DIR: gh converges from its release tarball (D8), so the image's
# cli.github.com source is a dead index that Error-Mode=any turns into a required failure whenever
# that host is down. Found by the URIs its source lines name, renamed to .disabled (apt ignores it),
# never deleted. A file that also lists another repository stays enabled: renaming it would drop that
# repository too.
_retire_gh_apt_source() {
    local dir="$1" f uris
    [ "$CHECK_ONLY" = true ] && return 0
    for f in "$dir"/*.list "$dir"/*.sources; do
        [ -f "$f" ] || continue
        uris="$(grep -Ei '^[[:space:]]*(deb(-src)?[[:space:]]|URIs:)' "$f" 2>/dev/null \
            | grep -oE '[A-Za-z][A-Za-z0-9+.-]*://[^[:space:]]+')" || uris=""
        printf '%s\n' "$uris" | grep -qE '://cli\.github\.com(/|$)' || continue
        if printf '%s\n' "$uris" | grep -vE '://cli\.github\.com(/|$)' | grep -q .; then
            log_warn "$f lists cli.github.com beside other repositories; left enabled, so its github-cli entry still fails the update when that host is down"
            continue
        fi
        if [ "$(id -u)" = 0 ]; then
            mv "$f" "$f.disabled" || { log_warn "could not disable the apt source $f"; continue; }
        elif command_exists sudo && sudo -n true 2>/dev/null; then
            sudo -n mv "$f" "$f.disabled" || { log_warn "could not disable the apt source $f"; continue; }
        else
            return 0
        fi
        log_info "disabled the github-cli apt source $f"
    done
    return 0
}

# _apt_lists_date LISTS_DIR CODENAME: the date apt stamped on the security pocket's newest index (the
# mirror's Last-Modified, UTC), or "never refreshed". --check must not refresh the lists (K16), so its
# verdict states how old they are: lists fetched N days ago carry a date at least N days back.
_apt_lists_date() {
    local newest
    newest="$(ls -t "$1"/*_dists_"$2"-security_*Release 2>/dev/null | head -1)" || newest=""
    if [ -n "$newest" ]; then
        date -u -r "$newest" +%Y-%m-%dT%H:%M:%SZ
    else
        echo "never refreshed"
    fi
}

# _pin_managed_autoupdater SETTINGS_DIR: K9's third DISABLE_AUTOUPDATER site, the managed scope that
# outranks every user and project setting, so no session can auto-update Claude Code off its pin.
# A drop-in of our own, so an operator's managed-settings.json is never rewritten. A failure only
# warns: the agent settings templates and the shell export still hold the pin.
_pin_managed_autoupdater() {
    local dir="$1" target stage tmp as_root=""
    [ "$CHECK_ONLY" = true ] && return 0
    [ "$(uname -s)" = Linux ] || return 0
    if [ "$(id -u)" != 0 ]; then
        { command_exists sudo && sudo -n true 2>/dev/null; } || return 0
        as_root="sudo -n"
    fi
    target="$dir/agentfactory.json"
    stage="$HOME/.cache/agentfactory"
    if ! { mkdir -p "$stage" && tmp="$(mktemp "$stage/managed-settings.XXXXXX")"; }; then
        log_warn "could not stage the managed DISABLE_AUTOUPDATER setting under $stage"
        return 0
    fi
    printf '%s\n' '{"env":{"DISABLE_AUTOUPDATER":"1"}}' >"$tmp"
    if cmp -s "$tmp" "$target"; then
        rm -f "$tmp"
        return 0
    fi
    if $as_root mkdir -p "$dir" && $as_root install -o root -g root -m 0644 "$tmp" "$target"; then
        log_info "Claude Code auto-updates disabled machine-wide ($target)"
    else
        log_warn "could not write $target; the agent settings and the shell export still set DISABLE_AUTOUPDATER"
    fi
    rm -f "$tmp"
    return 0
}

_security_source_present() {
    grep -qs -- "$1-security" /etc/apt/sources.list /etc/apt/sources.list.d/*.list /etc/apt/sources.list.d/*.sources
}

# _apt_converge: root layer pass 1. Rows ubuntu-security and nodejs; always returns 0 (the rows and
# the ledger carry the outcome). Without root the layer is ONE ledgered failure, `root-layer`, whose
# C-6 message the END report prints after the table.
_apt_converge() {
    if [ "$(uname -s)" != Linux ]; then
        log_info "root layer skipped: not Linux"
        return 0
    fi
    log_info "Converging the root layer (Ubuntu security pocket, Node.js pin)"
    local codename sel n=0 have out before after pin_label lists
    codename="$(_os_codename)"
    pin_label="${codename}-security"
    if ! command_exists apt-get; then
        log_error "security updates cannot be applied: no apt-get on PATH. The bootstrap converges Ubuntu packages only against that pocket."
        CONVERGE_FAILURES="${CONVERGE_FAILURES}ubuntu-security"$'\n'"nodejs"$'\n'
        _record_row ubuntu-security "$pin_label" "no apt-get" FAILED
        _record_row nodejs "$NODE_APT_VERSION" "no apt-get" FAILED
        return 0
    fi

    if [ "$CHECK_ONLY" = true ]; then
        lists="$(_apt_lists_date /var/lib/apt/lists "$codename")"
        case "$lists" in
            "never refreshed") lists=" (apt lists never refreshed)" ;;
            *) lists=" (apt lists from $lists)" ;;
        esac
        sel="$(_pending_security "$codename")"
        if [ -n "$sel" ]; then
            n="$(printf '%s\n' "$sel" | wc -l | tr -d ' ')"
            # shellcheck disable=SC2086 # one argument per package name
            if ! out="$(_apt_get -s install -y --only-upgrade $sel 2>&1)"; then
                log_warn "ubuntu-security: apt-get -s could not resolve the selection: $(printf '%s\n' "$out" | tail -1)"
            fi
            log_warn "ubuntu-security: $n pending != pinned $pin_label (would converge)$lists"
            _record_row ubuntu-security "$pin_label" "$n pending$lists" drift
        elif _security_source_present "$codename"; then
            log_success "ubuntu-security: $pin_label (kept)$lists"
            _record_row ubuntu-security "$pin_label" "$pin_label$lists" kept
        else
            log_warn "ubuntu-security: no apt source for the $pin_label pocket (would fail)$lists"
            _record_row ubuntu-security "$pin_label" "no source$lists" FAILED
        fi
        have="$(dpkg-query -W -f='${Version}' nodejs 2>/dev/null)" || have=""
        if [ "$have" = "$NODE_APT_VERSION" ]; then
            log_success "nodejs: $NODE_APT_VERSION (kept)"
            _record_row nodejs "$NODE_APT_VERSION" "$have" kept
        else
            if ! out="$(_apt_get -s install -y --allow-downgrades nodejs="$NODE_APT_VERSION" 2>&1)"; then
                log_warn "nodejs: apt-get -s could not resolve nodejs=$NODE_APT_VERSION: $(printf '%s\n' "$out" | tail -1)"
            fi
            log_warn "nodejs: ${have:-absent} != pinned $NODE_APT_VERSION (would converge)"
            _record_row nodejs "$NODE_APT_VERSION" "${have:-absent}" drift
        fi
        return 0
    fi

    if [ "$(id -u)" = 0 ]; then
        :
    elif command_exists sudo && sudo -n true 2>/dev/null; then
        :
    else
        log_error "root layer: no root (id -u = $(id -u); sudo -n true failed) — the Ubuntu security pocket and the Node.js pin cannot converge; reported at the end"
        CONVERGE_FAILURES="${CONVERGE_FAILURES}root-layer"$'\n'
        _record_row ubuntu-security "$pin_label" "no root" FAILED
        _record_row nodejs "$NODE_APT_VERSION" "no root" FAILED
        return 0
    fi

    before="$(dpkg-query -W -f='${Package} ${Version}\n' 2>/dev/null | sha256sum)" || before=""
    _ensure_nodesource_list "$NODE_MAJOR"
    _retire_gh_apt_source /etc/apt/sources.list.d
    if ! out="$(_apt_get update -o APT::Update::Error-Mode=any 2>&1)"; then
        printf '%s\n' "$out" | tail -20
        NODE_APT_ERROR="$(printf '%s\n' "$out" | tail -1)"
        log_error "apt-get update failed: $NODE_APT_ERROR"
        CONVERGE_FAILURES="${CONVERGE_FAILURES}ubuntu-security"$'\n'"nodejs"$'\n'
        _record_row ubuntu-security "$pin_label" "update failed" FAILED
        have="$(dpkg-query -W -f='${Version}' nodejs 2>/dev/null)" || have=""
        _record_row nodejs "$NODE_APT_VERSION" "${have:-absent}" FAILED
        return 0
    fi

    sel="$(_pending_security "$codename")"
    if [ -n "$sel" ]; then
        n="$(printf '%s\n' "$sel" | wc -l | tr -d ' ')"
        log_info "Installing $n pending $pin_label update(s)"
        # shellcheck disable=SC2086 # one argument per package name
        if out="$(_apt_get install -y --only-upgrade $sel 2>&1)"; then
            log_success "ubuntu-security: $pin_label (converged from $n pending)"
            _record_row ubuntu-security "$pin_label" "$pin_label" converged
        else
            printf '%s\n' "$out" | tail -20
            log_error "ubuntu-security: the $pin_label upgrade failed: $(printf '%s\n' "$out" | tail -1)"
            CONVERGE_FAILURES="${CONVERGE_FAILURES}ubuntu-security"$'\n'
            _record_row ubuntu-security "$pin_label" "$n pending" FAILED
        fi
    elif _security_source_present "$codename"; then
        log_success "ubuntu-security: $pin_label (kept)"
        _record_row ubuntu-security "$pin_label" "$pin_label" kept
    else
        log_error "security updates cannot be applied: no apt source for the $pin_label pocket (checked /etc/apt/sources.list.d/*.list, *.sources — present: $(ls /etc/apt/sources.list.d 2>/dev/null | tr '\n' ' ')). The bootstrap converges Ubuntu packages only against that pocket."
        CONVERGE_FAILURES="${CONVERGE_FAILURES}ubuntu-security"$'\n'
        _record_row ubuntu-security "$pin_label" "no source" FAILED
    fi

    have="$(dpkg-query -W -f='${Version}' nodejs 2>/dev/null)" || have=""
    if out="$(_apt_get install -y --allow-downgrades nodejs="$NODE_APT_VERSION" 2>&1)"; then
        if [ "$have" = "$NODE_APT_VERSION" ]; then
            log_success "nodejs: $NODE_APT_VERSION (kept)"
            _record_row nodejs "$NODE_APT_VERSION" "$have" kept
        else
            log_success "nodejs: $NODE_APT_VERSION (converged from ${have:-absent})"
            _record_row nodejs "$NODE_APT_VERSION" "$NODE_APT_VERSION" converged
        fi
    else
        printf '%s\n' "$out" | tail -20
        NODE_APT_ERROR="$(printf '%s\n' "$out" | tail -1)"
        log_error "nodejs: ${have:-absent} != pinned $NODE_APT_VERSION — install failed (reason above)"
        CONVERGE_FAILURES="${CONVERGE_FAILURES}nodejs"$'\n'
        _record_row nodejs "$NODE_APT_VERSION" "${have:-absent}" FAILED
    fi

    after="$(dpkg-query -W -f='${Package} ${Version}\n' 2>/dev/null | sha256sum)" || after=""
    if [ "$before" != "$after" ]; then
        APT_CHANGED=1
    fi
    return 0
}

# _apt_security_upgrade: root layer pass 2, after install_playwright, whose --with-deps can pull in
# packages that have pending security versions of their own (Gap 18). No update, no Node line, no
# new row: the pass-1 row is replaced only when this pass fails.
_apt_security_upgrade() {
    [ "$CHECK_ONLY" = true ] && return 0
    [ "$(uname -s)" = Linux ] || return 0
    command_exists apt-get || return 0
    if [ "$(id -u)" != 0 ] && ! { command_exists sudo && sudo -n true 2>/dev/null; }; then
        return 0
    fi
    local codename sel out before after
    codename="$(_os_codename)"
    sel="$(_pending_security "$codename")"
    [ -n "$sel" ] || return 0
    before="$(dpkg-query -W -f='${Package} ${Version}\n' 2>/dev/null | sha256sum)" || before=""
    # shellcheck disable=SC2086 # one argument per package name
    if ! out="$(_apt_get install -y --only-upgrade $sel 2>&1)"; then
        printf '%s\n' "$out" | tail -20
        log_error "ubuntu-security: the post-Playwright ${codename}-security upgrade failed: $(printf '%s\n' "$out" | tail -1)"
        CONVERGE_FAILURES="${CONVERGE_FAILURES}ubuntu-security"$'\n'
        _record_row ubuntu-security "${codename}-security" "$(printf '%s\n' "$sel" | wc -l | tr -d ' ') pending" FAILED
    fi
    after="$(dpkg-query -W -f='${Package} ${Version}\n' 2>/dev/null | sha256sum)" || after=""
    if [ "$before" != "$after" ]; then
        APT_CHANGED=1
    fi
    return 0
}

#------------------------------------------------------------------------------
# Hoisted gateway and telemetry helpers (reachable from --check and the converge-if-present pass)
#------------------------------------------------------------------------------

# _gateway_stop (K12, issue #693 Phase 4): the ONLY kill site for the litellm tmux session.
# Hoisted from setup_litellm (D-23) so the converge-if-present pass can stop the gateway before
# the Python locks change the packages it runs on.
_gateway_stop() {
    mkdir -p .runtime/gateway
    printf '{"pid":%s,"since":%s}' "$$" "$(date +%s)" > .runtime/gateway/reconciling
    tmux kill-session -t =litellm 2>/dev/null || true
    rm -f .runtime/gateway/launch.json
    local waited=0
    while _port_in_use "$LITELLM_PORT" && [ "$waited" -lt 10 ]; do
        sleep 1
        waited=$((waited + 1))
    done
}

_litellm_session_running() {
    tmux list-sessions -F '#{session_name}' 2>/dev/null | grep -qx litellm
}

_probe_openobserve() {
    "$OPENOBSERVE_BIN" --version 2>/dev/null | awk 'NR==1{print $2}'
}

# _telemetry_stop: OpenObserve drains on SIGTERM but not on the SIGHUP that closing its tmux
# session delivers (H-7), so it gets SIGTERM and a bounded drain first. The process belongs to the
# tmux server, not to this shell, so only a kill -0 poll can wait for it.
_telemetry_stop() {
    local pane target owner walk stat waited=0
    pane="$(tmux list-panes -t telemetry -F '#{pane_pid}' 2>/dev/null | head -1)" || pane=""
    [ -n "$pane" ] || return 0
    target="$pane"
    # The pane runs a shell around openobserve; the port owner is the backend itself when it is a
    # descendant of that pane (the same parent walk setup_litellm uses).
    owner="$(_port_owner_pid "$TELEMETRY_PORT")"
    if [ -n "$owner" ]; then
        walk="$owner"
        while [ -n "$walk" ] && [ "$walk" != "0" ] && [ "$walk" != "1" ] && [ "$walk" != "$pane" ]; do
            stat="$(cat "/proc/$walk/stat" 2>/dev/null)" || stat=""
            walk="$(printf '%s' "${stat##*) }" | awk '{print $2}')"
        done
        if [ "$walk" = "$pane" ]; then
            target="$owner"
        fi
    fi
    kill -TERM "$target" 2>/dev/null || true
    while kill -0 "$target" 2>/dev/null && [ "$waited" -lt 30 ]; do
        sleep 1
        waited=$((waited + 1))
    done
    if kill -0 "$target" 2>/dev/null; then
        log_warn "OpenObserve (pid $target) did not exit within 30s of SIGTERM; closing its tmux session"
    fi
    if [ "$(tmux list-panes -t telemetry -F '#{pane_pid}' 2>/dev/null | head -1)" = "$pane" ]; then
        tmux kill-session -t telemetry 2>/dev/null || true
    fi
    return 0
}

# A failed download leaves any older binary in place; say so, because it is still serving.
_openobserve_install_failed() {
    log_error "$1"
    if [ -x "$OPENOBSERVE_BIN" ]; then
        log_error "openobserve $(_probe_openobserve) != pinned $OPENOBSERVE_VERSION and the verified download failed: $1. The old binary is still running (127.0.0.1:$TELEMETRY_PORT, with the advisories issue #743 row 12 lists); re-run after fixing the download. (--no-telemetry does not skip this: an installed backend converges on every run.)"
    fi
}

# _install_openobserve: download -> sha256 -> unpack -> atomic swap -> stop the old process ->
# record the version that will open the data dir. The swap comes before the stop, so a watchdog
# relaunch in between starts the NEW binary (D-58). cwd is the factory root in both callers.
_install_openobserve() {
    local arch expected tarball url tmpdir
    arch=""
    if [ "$(uname -s)" = Linux ]; then
        arch="$(_arch)" || arch=""
    fi
    case "$arch" in
        amd64) expected="$OPENOBSERVE_SHA256_AMD64" ;;
        arm64) expected="$OPENOBSERVE_SHA256_ARM64" ;;
        *) log_error "Unsupported arch '$(uname -s)/$(uname -m)' for OpenObserve (Linux arm64/amd64 builds only)"
           return 1 ;;
    esac
    tarball="openobserve-${OPENOBSERVE_VERSION}-linux-${arch}.tar.gz"
    url="https://downloads.openobserve.ai/releases/openobserve/${OPENOBSERVE_VERSION}/${tarball}"
    tmpdir="$(mktemp -d)" || return 1
    CLEANUP_DIRS+=("$tmpdir")
    log_info "Downloading OpenObserve ${OPENOBSERVE_VERSION} (${arch})"
    if ! curl -fsSL "$url" -o "$tmpdir/$tarball"; then
        _openobserve_install_failed "OpenObserve download failed ($url)"
        return 1
    fi
    # A mismatch is a supply-chain signal (security.md T8): refuse to unpack or run the binary.
    if ! printf '%s  %s\n' "$expected" "$tmpdir/$tarball" | sha256sum --check --strict; then
        _openobserve_install_failed "OpenObserve checksum verification FAILED (expected $expected) — not installing"
        return 1
    fi
    if ! { tar -C "$tmpdir" -xzf "$tmpdir/$tarball" && mkdir -p "$(dirname "$OPENOBSERVE_BIN")" \
            && install -m 0755 "$tmpdir/openobserve" "$OPENOBSERVE_BIN.new" \
            && mv -f "$OPENOBSERVE_BIN.new" "$OPENOBSERVE_BIN"; }; then
        _openobserve_install_failed "OpenObserve unpack/install failed"
        return 1
    fi
    _telemetry_stop
    mkdir -p .agentfactory/telemetry || return 1
    printf '%s\n' "$OPENOBSERVE_VERSION" > .agentfactory/telemetry/openobserve.version || return 1
    log_success "Installed OpenObserve ${OPENOBSERVE_VERSION} to $OPENOBSERVE_BIN"
    return 0
}

# The data dir is migrated forward by each release that opens it; a binary older than the one
# recorded in openobserve.version is never launched on it.
_openobserve_not_older_than_marker() {
    local marker=".agentfactory/telemetry/openobserve.version" want have
    [ -s "$marker" ] || return 0
    want="$(head -1 "$marker")"
    have="$(_probe_openobserve)" || have=""
    if version_gte "$have" "$want"; then
        return 0
    fi
    log_error "openobserve ${have:-unknown} is older than $want, the build that last opened the telemetry data dir — refusing to launch it"
    return 1
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

    # GOTOOLCHAIN=local: inside the source tree, go.mod's toolchain line would otherwise make the
    # image's go1.24.2 fetch and verify the newer toolchain itself (H-3).
    GO_VERSION=$(GOTOOLCHAIN=local go version | sed -n 's/.*go\([0-9][0-9]*\.[0-9][0-9]*\).*/\1/p')
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

    # The MCP dependencies are not checked here: --check runs this function and must change
    # nothing, and the hash-locked install is converge_present_components' job.
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

check_playwright() {
    log_step "Checking Playwright browser tooling"

    if command_exists playwright && compgen -G "$HOME/.cache/ms-playwright/chromium-*" >/dev/null 2>&1; then
        log_success "playwright with chromium installed"
        return 0
    fi

    log_warn "playwright/chromium not found (optional — agents' visual checks escalate to the human gate without it)"
    return 1
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
    check_playwright || WARNINGS=$((WARNINGS + 1))

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

    # The Go toolchain (K8): the image's go1.24.2 cmd/go is inside GO-2026-6602 and GO-2026-4984,
    # so it must never fetch and verify the go.mod toolchain switch itself (H-3). The pinned
    # toolchain comes from a sha256-verified go.dev tarball in an af-owned prefix instead, and the
    # probe reads that prefix binary under GOTOOLCHAIN=local, because `go version` otherwise
    # reports the toolchain it would switch to.
    _probe_go() {
        GOTOOLCHAIN=local "$HOME/.local/share/agentfactory/go/bin/go" version 2>/dev/null | awk '{print $3}'
    }
    _install_go() {
        local arch expected tarball parent stage prefix="$HOME/.local/share/agentfactory/go"
        arch="$(_arch)" || { log_error "go: unsupported architecture $(uname -m)"; return 1; }
        case "$arch" in
            arm64) expected="$GO_SHA256_ARM64" ;;
            amd64) expected="$GO_SHA256_AMD64" ;;
        esac
        tarball="go${GO_TOOLCHAIN_VERSION#go}.linux-${arch}.tar.gz"
        parent="$(dirname "$prefix")"
        mkdir -p "$parent" "$HOME/.local/bin" || return 1
        # Unpacked beside the prefix and swapped in whole: never over an older tree, never in /tmp.
        stage="$(mktemp -d "$parent/go-stage.XXXXXX")" || return 1
        CLEANUP_DIRS+=("$stage")
        log_info "Downloading Go toolchain $GO_TOOLCHAIN_VERSION ($arch)"
        curl -fsSL "https://go.dev/dl/$tarball" -o "$stage/$tarball" || return 1
        printf '%s  %s\n' "$expected" "$stage/$tarball" | sha256sum --check --strict >/dev/null || { log_error "go toolchain $GO_TOOLCHAIN_VERSION: $tarball does not match its pinned sha256 — NOT installing"; return 1; }
        tar -C "$stage" -xzf "$stage/$tarball" || return 1
        rm -rf "$prefix.old"
        if [ -d "$prefix" ]; then
            mv "$prefix" "$prefix.old" || return 1
        fi
        mv "$stage/go" "$prefix" || return 1
        rm -rf "$prefix.old"
        ln -sfn "$prefix/bin/go" "$HOME/.local/bin/go" || return 1
    }
    # _ledger_go INSTALLED: the run exits 1 over go, so its row must read FAILED like every other
    # ledgered row (K16). A failed `_converge go` has already recorded that row, with the install's
    # own reason, and ledgered go; the END report lists each component once.
    _ledger_go() {
        case "$(printf '%s' "$CONVERGE_ROWS" | awk -F'\t' '$1 == "go" {print $5}')" in
            FAILED) ;;
            *) _record_row go "$GO_TOOLCHAIN_VERSION" "$1" FAILED ;;
        esac
        case "$CONVERGE_FAILURES" in
            go$'\n'*|*$'\n'go$'\n'*) ;;
            *) CONVERGE_FAILURES="${CONVERGE_FAILURES}go"$'\n' ;;
        esac
    }

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

    if [ "$(uname -s)" = Linux ]; then
        _converge go "$GO_TOOLCHAIN_VERSION" _probe_go _install_go required || true
    else
        log_info "go: not Linux; building with the Go on PATH"
    fi
    if [ "$CHECK_ONLY" = true ]; then
        return 0
    fi
    if [ "$(uname -s)" = Linux ]; then
        hash -r
        local go_bin go_reported goenv stale removed=""
        go_bin="$(readlink -f "$(command -v go)" 2>/dev/null)" || go_bin=""
        case "$go_bin" in
            "$HOME/.local/share/agentfactory/go/"*)
                if ! GOTOOLCHAIN=local go env -w GOTOOLCHAIN="${GO_TOOLCHAIN_VERSION}+auto" 2>/dev/null; then
                    log_warn "go env -w GOTOOLCHAIN=${GO_TOOLCHAIN_VERSION}+auto failed (an exported GOTOOLCHAIN still overrides it)"
                fi
                go_reported="$(go version 2>/dev/null | awk '{print $3}')" || go_reported=""
                if [ "$go_reported" != "$GO_TOOLCHAIN_VERSION" ]; then
                    log_error "go version reports ${go_reported:-nothing}, not $GO_TOOLCHAIN_VERSION — an exported GOTOOLCHAIN overrides go env -w"
                    log_info "go: building af with GOTOOLCHAIN=local (the pinned $GO_TOOLCHAIN_VERSION prefix toolchain)"
                    _ledger_go "go version reports ${go_reported:-nothing}"
                    export GOTOOLCHAIN=local
                fi
                ;;
            *)
                # A +auto line an earlier converging run persisted would let this base go fetch the
                # pinned toolchain itself (H-3); only that af-written value is removed.
                goenv="$(GOTOOLCHAIN=local go env GOENV 2>/dev/null)" || goenv=""
                stale="$(grep -E '^GOTOOLCHAIN=go[0-9.]+[+]auto$' "$goenv" 2>/dev/null | head -1)" || stale=""
                if [ -n "$stale" ] && GOTOOLCHAIN=local go env -u GOTOOLCHAIN 2>/dev/null; then
                    removed="; removed the af-written $stale from $goenv"
                fi
                log_error "go toolchain $GO_TOOLCHAIN_VERSION: the go on PATH resolves to ${go_bin:-nothing}, not $HOME/.local/share/agentfactory/go; building with GOTOOLCHAIN=local so it never switches toolchains itself$removed"
                _ledger_go "PATH go is ${go_bin:-absent}"
                export GOTOOLCHAIN=local
                ;;
        esac
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
    # hand. A first install stays best-effort (a factory without the console is fully functional),
    # but an INSTALLED console that cannot be rebuilt would keep serving its old build against the
    # new af, so that failure is ledgered (D-46). `make build-webui` builds ./webui into the repo
    # root but does NOT install it, so build fresh (avoids installing a stale copy left by a prior
    # branch) THEN install. CWD is still "$SCRIPT_DIR" here (set above), where build-webui's -o
    # ../webui lands; `install -m 0755` is a deliberate deviation from the adjacent `cp af` — it sets
    # the exec bit the guard gates on in a single step.
    if [ -x "$HOME/.local/bin/webui" ]; then
        if make build-webui && [ -f webui ]; then
            install -m 0755 webui "$HOME/.local/bin/webui"
            WEBUI_REBUILT=1
            log_info "Rebuilt webui at ~/.local/bin/webui"
        else
            log_error "build-webui failed; the installed web console keeps its old build"
            CONVERGE_FAILURES="${CONVERGE_FAILURES}webui"$'\n'
        fi
    elif make build-webui 2>/dev/null && [ -f webui ]; then
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

# install_gh (D-1, D-5): gh converges from its release tarball into ~/.local/bin (cli.github.com's
# apt index keeps one version per architecture, so an apt pin would be a scheduled outage). Once the
# pinned gh is in place, the image's apt build at /usr/bin/gh is retired (_retire_apt_gh).
install_gh() {
    log_step "Converging the GitHub CLI (gh)"

    _probe_gh() {
        gh --version 2>/dev/null | awk 'NR==1{print $3}'
    }
    _install_gh() {
        local arch expected tarball stage key value
        if [ "$(uname -s)" != Linux ]; then
            log_error "gh: the pinned release tarball is a Linux build; not installing it on $(uname -s)"
            return 1
        fi
        arch="$(_arch)" || { log_error "gh: unsupported architecture $(uname -m)"; return 1; }
        case "$arch" in
            arm64) expected="$GH_SHA256_ARM64" ;;
            amd64) expected="$GH_SHA256_AMD64" ;;
        esac
        tarball="gh_${GH_VERSION}_linux_${arch}.tar.gz"
        mkdir -p "$HOME/.local/bin" "$HOME/.cache/agentfactory" || return 1
        stage="$(mktemp -d "$HOME/.cache/agentfactory/gh.XXXXXX")" || return 1
        CLEANUP_DIRS+=("$stage")
        log_info "Downloading gh $GH_VERSION ($arch)"
        if ! curl -fsSL "https://github.com/cli/cli/releases/download/v${GH_VERSION}/${tarball}" -o "$stage/$tarball"; then
            log_error "gh $GH_VERSION: download failed"
            return 1
        fi
        if ! printf '%s  %s\n' "$expected" "$stage/$tarball" | sha256sum --check --strict >/dev/null; then
            log_error "gh $GH_VERSION: $tarball does not match its pinned sha256 — NOT installing"
            return 1
        fi
        tar -C "$stage" -xzf "$stage/$tarball" || return 1
        install -m 0755 "$stage/gh_${GH_VERSION}_linux_${arch}/bin/gh" "$HOME/.local/bin/gh.new" || return 1
        mv -f "$HOME/.local/bin/gh.new" "$HOME/.local/bin/gh" || return 1
        # quickdocker.sh's `gh auth setup-git` pointed git's credential helper at the apt binary by
        # absolute path; re-point exactly those values, keeping each key's empty reset entry.
        git config --global --get-regexp '^credential\..*\.helper$' 2>/dev/null | while read -r key value; do
            case "$value" in
                *"/usr/bin/gh auth git-credential"*)
                    git config --global --replace-all "$key" "!$HOME/.local/bin/gh auth git-credential" '/usr/bin/gh auth git-credential' || true
                    ;;
            esac
        done || true
        return 0
    }

    _converge gh "$GH_VERSION" _probe_gh _install_gh required || true
    _retire_apt_gh /usr/local/bin
    return 0
}

# _retire_apt_gh LINK_DIR: the image's apt gh (2.90.0) carries advisories and stays executable on
# every PATH without ~/.local/bin (B2), so once the pinned gh answers it is removed. git's credential
# helpers are re-pointed first, and LINK_DIR/gh points at the pinned build so callers that use the
# image PATH (non-login `docker exec`, af's own gh calls) keep a gh. An operator file there is never
# replaced. A failed removal fails the gh row: a vulnerable gh stays reachable.
_retire_apt_gh() {
    local dir="$1" have state key value as_root=""
    [ "$CHECK_ONLY" = true ] && return 0
    have="$("$HOME/.local/bin/gh" --version 2>/dev/null | awk 'NR==1{print $3}')" || have=""
    [ "$have" = "$GH_VERSION" ] || return 0
    state="$(dpkg-query -W -f='${Status}' gh 2>/dev/null)" || state=""
    [ "$state" = "install ok installed" ] || return 0
    if [ "$(id -u)" != 0 ]; then
        if command_exists sudo && sudo -n true 2>/dev/null; then
            as_root="sudo -n"
        else
            log_warn "gh: the apt gh stays installed at /usr/bin/gh — no root (id -u = $(id -u); sudo -n true failed)"
            return 0
        fi
    else
        log_warn "gh: retiring the apt gh as root; another user's git helper or script that runs /usr/bin/gh must switch to $dir/gh, which points into $HOME"
    fi
    git config --global --get-regexp '^credential\..*\.helper$' 2>/dev/null | while read -r key value; do
        case "$value" in
            *"/usr/bin/gh auth git-credential"*)
                git config --global --replace-all "$key" "!$HOME/.local/bin/gh auth git-credential" '/usr/bin/gh auth git-credential' || true
                ;;
        esac
    done || true
    if { [ ! -e "$dir/gh" ] && [ ! -L "$dir/gh" ]; } || [ "$(readlink "$dir/gh" 2>/dev/null)" = "$HOME/.local/bin/gh" ]; then
        $as_root ln -sfn "$HOME/.local/bin/gh" "$dir/gh" || log_warn "gh: could not link $dir/gh to the pinned gh"
    fi
    if ! _apt_get remove -y gh >/dev/null 2>&1; then
        log_error "gh: apt-get remove gh failed; the apt gh stays reachable at /usr/bin/gh"
        CONVERGE_FAILURES="${CONVERGE_FAILURES}gh"$'\n'
        _record_row gh "$GH_VERSION" "apt gh $(dpkg-query -W -f='${Version}' gh 2>/dev/null) still installed" FAILED
        return 0
    fi
    hash -r
    return 0
}

install_claude() {
    log_step "Converging Claude Code CLI"

    _probe_claude() {
        if command_exists claude; then
            claude --version 2>/dev/null | head -1 | awk '{print $1}'
        fi
    }
    # 2.1.224 embeds the emSign ECC Root CA - G3 as an unlabeled PEM, so both the DER label and a
    # run of the certificate body are counted (H-2).
    _claude_emsign_clean() {
        local label body
        [ -f "$1" ] || return 1
        label="$(LC_ALL=C grep -a -c 'emSign ECC Root CA - G3' "$1" 2>/dev/null)" || true
        body="$(LC_ALL=C grep -a -c "$EMSIGN_G3_BODY_PROBE" "$1" 2>/dev/null)" || true
        if [ "${label:-0}" = 0 ] && [ "${body:-0}" = 0 ]; then
            return 0
        fi
        log_error "claude $CLAUDE_CODE_VERSION embeds 'emSign ECC Root CA - G3' ($((${label:-0} + ${body:-0})) matches) — refusing to accept this build; bump CLAUDE_CODE_VERSION"
        return 1
    }
    # The official installer re-points ~/.local/bin/claude before its build is verified, so a
    # rejected build would leave the link dangling and every agent session unlaunchable. Put back
    # the link the run started with ($prev, one hop, as written), or drop a dangling one. A relative
    # $prev resolves against the link's own directory, not the cwd quickstart runs in.
    _claude_relink_prev() {
        local resolved="$prev"
        case "$prev" in
            ""|/*) ;;
            *) resolved="$HOME/.local/bin/$prev" ;;
        esac
        if [ -n "$prev" ] && [ -x "$resolved" ]; then
            ln -sfn "$prev" "$HOME/.local/bin/claude"
        elif [ -L "$HOME/.local/bin/claude" ] && [ ! -e "$HOME/.local/bin/claude" ]; then
            rm -f "$HOME/.local/bin/claude"
        fi
    }
    # Three channels, each verified before anything it fetched runs. ~/.local/bin/claude is a
    # symlink into versions/ (the official installer's layout), so it is only ever re-pointed with
    # `ln -sfn`: copying onto it would overwrite the OLD version's bytes.
    _install_claude() {
        local arch platform expected suffix versions bin target prev rc=0
        if [ "$(uname -s)" != Linux ]; then
            log_error "claude: the pinned release binary is a Linux build; not installing it on $(uname -s)"
            return 1
        fi
        arch="$(_arch)" || arch=""
        case "$arch" in
            arm64) platform="linux-arm64"; suffix="ARM64"; expected="$CLAUDE_CODE_SHA256_ARM64" ;;
            amd64) platform="linux-x64"; suffix="AMD64"; expected="$CLAUDE_CODE_SHA256_AMD64" ;;
            *) log_error "claude: unsupported architecture $(uname -m)"; return 1 ;;
        esac
        versions="$HOME/.local/share/claude/versions"
        bin="$versions/$CLAUDE_CODE_VERSION"
        mkdir -p "$versions" "$HOME/.local/bin" || return 1

        log_info "Downloading Claude Code $CLAUDE_CODE_VERSION ($platform)"
        rm -f "$bin.partial"
        if curl -fsSL -o "$bin.partial" "https://downloads.claude.ai/claude-code-releases/$CLAUDE_CODE_VERSION/$platform/claude" \
            && printf '%s  %s\n' "$expected" "$bin.partial" | sha256sum --check --strict >/dev/null; then
            if ! _claude_emsign_clean "$bin.partial"; then
                rm -f "$bin.partial"
                return 1
            fi
            mv -f "$bin.partial" "$bin" || return 1
            chmod 0755 "$bin" || return 1
            ln -sfn "$bin" "$HOME/.local/bin/claude" || return 1
            return 0
        fi
        rm -f "$bin.partial"
        log_warn "claude $CLAUDE_CODE_VERSION: the release download failed or did not match CLAUDE_CODE_SHA256_$suffix; trying the official installer"

        prev="$(readlink "$HOME/.local/bin/claude" 2>/dev/null)" || prev=""
        if curl -fsSL https://claude.ai/install.sh | bash -s -- "$CLAUDE_CODE_VERSION"; then
            target="$(readlink -f "$HOME/.local/bin/claude" 2>/dev/null)" || target=""
            if [ -n "$target" ] && printf '%s  %s\n' "$expected" "$target" | sha256sum --check --strict >/dev/null; then
                if ! _claude_emsign_clean "$target"; then
                    rm -f "$target"
                    _claude_relink_prev
                    return 1
                fi
                return 0
            fi
            if [ "$target" = "$bin" ]; then
                rm -f "$bin"
                log_error "claude $CLAUDE_CODE_VERSION: installed binary sha256 does not match CLAUDE_CODE_SHA256_$suffix — removed"
            else
                log_error "claude $CLAUDE_CODE_VERSION: the official installer did not lay down versions/$CLAUDE_CODE_VERSION (~/.local/bin/claude -> ${target:-nothing})"
            fi
        fi
        _claude_relink_prev

        log_info "Trying the npm package for Claude Code $CLAUDE_CODE_VERSION"
        _npm_install_pinned @anthropic-ai/claude-code "$CLAUDE_CODE_VERSION" "$CLAUDE_CODE_NPM_SHA512" "@anthropic-ai/claude-code-$platform" "$(if [ "$arch" = arm64 ]; then echo "$CLAUDE_CODE_LINUX_ARM64_SHA512"; else echo "$CLAUDE_CODE_LINUX_AMD64_SHA512"; fi)" || rc=$?
        if [ "$rc" -eq 3 ]; then
            log_error "cannot install Claude Code from npm: the npm global prefix is root-owned and passwordless sudo is unavailable in this container"
        fi
        [ "$rc" -eq 0 ] || return 1
        # npm's bin link sits behind ~/.local/bin on PATH; point ~/.local/bin/claude at it, or a
        # stale build there would keep answering the probe.
        target="$(npm prefix -g 2>/dev/null)/bin/claude"
        if [ ! -e "$target" ]; then
            log_error "claude: the npm package installed no binary at $target"
            return 1
        fi
        if ! _claude_emsign_clean "$(readlink -f "$target" 2>/dev/null)"; then
            return 1
        fi
        ln -sfn "$target" "$HOME/.local/bin/claude" || return 1
        return 0
    }

    _converge claude "$CLAUDE_CODE_VERSION" _probe_claude _install_claude required || true
    return 0
}

install_playwright() {
    log_step "Converging Playwright browser tooling (headless visual verification)"

    # Optional tier: agents use `playwright screenshot` for visual triage of
    # transplanted UIs. Every failure path degrades to log_warn + return 0 —
    # a factory without a browser stays fully functional; agents' visual
    # checks escalate to the human demo gate instead.

    # The version, suffixed +no-chromium when any browser directory the pinned CLI would use is
    # missing, so a CLI without its browser never reads as converged.
    _probe_playwright() {
        local v loc
        v="$(playwright --version 2>/dev/null | awk '{print $2}')" || true
        [ -n "$v" ] || return 0
        while IFS= read -r loc; do
            if [ -z "$loc" ] || [ ! -d "$loc" ]; then
                v="$v+no-chromium"
                break
            fi
        done <<EOF
$(playwright install --dry-run chromium 2>/dev/null | sed -n 's/^ *Install location: *//p')
EOF
        printf '%s\n' "$v"
    }
    # .designs/743/implementation-plan/pin-data.md § Tree digest algorithm: one `F <sha256> <path>`
    # or `L <target> <path>` line per entry under the build's single top-level directory, sorted
    # under LC_ALL=C, hashed as a whole.
    _playwright_tree_digest() {
        (cd "$1" && find . -mindepth 1 \( -type f -o -type l \) -printf '%P\n' | LC_ALL=C sort | while IFS= read -r p; do
            if [ -L "$p" ]; then
                printf 'L %s %s\n' "$(readlink "$p")" "$p"
            else
                printf 'F %s %s\n' "$(sha256sum < "$p" | cut -d' ' -f1)" "$p"
            fi
        done | sha256sum | cut -d' ' -f1)
    }
    _install_playwright() {
        if ! command_exists npm; then
            log_warn "npm not found; cannot install Playwright"
            return 1
        fi

        # Every command below writes to the install log; the terminal sees only
        # clean status lines. The log is surfaced (tail) only on real failure —
        # this runs once per factory across the whole fleet, so first-run noise
        # multiplies by the number of factories.
        local pw_log="/tmp/af-playwright-install.log"
        : >"$pw_log"

        local rc=0 arch want_chromium want_shell loc want root got checked=0
        log_info "Installing playwright $PLAYWRIGHT_VERSION via npm (log: $pw_log)..."
        _npm_install_pinned playwright "$PLAYWRIGHT_VERSION" "$PLAYWRIGHT_SHA512" playwright-core "$PLAYWRIGHT_CORE_SHA512" >>"$pw_log" 2>&1 || rc=$?
        case "$rc" in
            0) ;;
            3) log_warn "npm global prefix not writable and no passwordless sudo; cannot install playwright"; return 1 ;;
            *) tail -20 "$pw_log"; log_warn "npm install of the pinned playwright failed"; return 1 ;;
        esac
        hash -r
        if ! command_exists playwright; then
            log_warn "playwright not on PATH after install"
            return 1
        fi

        # Ubuntu's apt chromium is a snap transition stub (snaps don't run in standard
        # containers), so playwright's bundled chromium is the reliable path. --with-deps
        # apt-installs the browser's shared libraries and needs passwordless sudo (the
        # container 'dev' user has it).
        # The apt run inside --with-deps prints a benign "debconf: delaying package
        # configuration" warning (the slim base image lacks apt-utils) — it goes to
        # the log with everything else.
        log_info "Downloading chromium (first run only, ~250MB; log: $pw_log)..."
        if command_exists sudo && sudo -n true 2>/dev/null; then
            playwright install --with-deps chromium >>"$pw_log" 2>&1 || {
                tail -20 "$pw_log"
                log_warn "chromium download or system deps failed"
                return 1
            }
        else
            playwright install chromium >>"$pw_log" 2>&1 || {
                tail -20 "$pw_log"
                log_warn "chromium download failed"
                return 1
            }
        fi

        # The browser builds are fetched by the CLI, outside npm's integrity check, so each tree is
        # held to its pinned digest (L-8).
        arch="$(_arch)" || arch=""
        case "$arch" in
            arm64) want_chromium="$PLAYWRIGHT_CHROMIUM_TREE_SHA256_ARM64"; want_shell="$PLAYWRIGHT_HEADLESS_SHELL_TREE_SHA256_ARM64" ;;
            amd64) want_chromium="$PLAYWRIGHT_CHROMIUM_TREE_SHA256_AMD64"; want_shell="$PLAYWRIGHT_HEADLESS_SHELL_TREE_SHA256_AMD64" ;;
            *) log_warn "playwright: no pinned chromium digest for $(uname -m)"; return 1 ;;
        esac
        for loc in $(playwright install --dry-run chromium 2>/dev/null | sed -n 's/^ *Install location: *//p'); do
            case "${loc##*/}" in
                chromium-*) want="$want_chromium" ;;
                chromium_headless_shell-*) want="$want_shell" ;;
                *) continue ;;
            esac
            root="$(find "$loc" -mindepth 1 -maxdepth 1 -type d 2>/dev/null)"
            if [ -z "$root" ] || [ "$(printf '%s\n' "$root" | wc -l | tr -d ' ')" != 1 ]; then
                log_warn "playwright: ${loc##*/} does not hold exactly one build directory"
                return 1
            fi
            got="$(_playwright_tree_digest "$root")"
            if [ "$got" != "$want" ]; then
                log_warn "playwright: ${loc##*/} tree digest $got does not match the pinned $want"
                return 1
            fi
            checked=$((checked + 1))
        done
        if [ "$checked" -ne 2 ]; then
            log_warn "playwright: found $checked of the 2 pinned browser trees (chromium, headless shell)"
            return 1
        fi
        return 0
    }

    if [ "$(uname -s)" = Linux ]; then
        _converge playwright "$PLAYWRIGHT_VERSION" _probe_playwright _install_playwright optional || true
    else
        log_info "playwright: not Linux; the pinned browser builds and their tree digests are Linux-only"
    fi
    if [ "$CHECK_ONLY" = true ] || ! command_exists playwright; then
        return 0
    fi

    # Calibrate by rendering, not by version string: an installed-but-unrenderable
    # browser must read as a warning, never a success.
    local probe="/tmp/af-playwright-probe.png"
    if playwright screenshot "data:text/html,<h1>af</h1>" "$probe" >/dev/null 2>&1 \
        && [ -s "$probe" ]; then
        log_success "Playwright chromium renders headlessly"
    else
        log_warn "Playwright installed but rendering probe failed — visual checks will escalate to the human gate"
    fi
    rm -f "$probe" 2>/dev/null || true
    return 0
}

_ensure_codex_cli() {
    # Consent is asked only for a FIRST install, and never under --check: a present codex converges
    # to the pin like every other component.
    if ! command_exists codex && [ "$CHECK_ONLY" != true ]; then
        local consent="${AF_CODEX_INSTALL_CONSENT:-}"
        case "$consent" in
            yes)
                ;;
            "")
                if [ -t 0 ]; then
                    local _consent_ans=""
                    read -r -p "This will INSTALL the codex cli, are you sure? y/N " _consent_ans
                    if [ "$_consent_ans" != "y" ] && [ "$_consent_ans" != "Y" ]; then
                        log_error "codex CLI install declined — subscription mode cannot run without the Codex CLI; nothing was installed" >&2
                        exit 1
                    fi
                else
                    log_error "codex CLI install declined (no terminal to ask, and AF_CODEX_INSTALL_CONSENT is not \"yes\") — subscription mode cannot run without the Codex CLI; nothing was installed" >&2
                    exit 1
                fi
                ;;
            *)
                log_error "invalid AF_CODEX_INSTALL_CONSENT value \"$consent\": must be unset or exactly \"yes\"" >&2
                exit 1
                ;;
        esac
    fi

    _probe_codex() {
        codex --version 2>/dev/null | awk '{print $2}'
    }
    # Same probe -> sudo -n shape as install_playwright's npm-global-root writability check
    # (intake DO-NOT-CHANGE: reuse its probe -> sudo -n shape); unlike playwright's optional
    # tooling, the codex CLI is load-bearing for subscription mode, so every declined/infeasible
    # path here fails the run (decline: `exit 1`; install: `return 1` to a caller that exits), never install_playwright's degrade-safe `return 0`.
    _install_codex() {
        local codex_log="/tmp/af-codex-install.log" platform sha rc=0
        : >"$codex_log"
        case "$(_arch)" in
            arm64) platform="@openai/codex-linux-arm64"; sha="$CODEX_LINUX_ARM64_SHA512" ;;
            amd64) platform="@openai/codex-linux-x64"; sha="$CODEX_LINUX_AMD64_SHA512" ;;
            *) log_error "codex: unsupported architecture $(uname -m)"; return 1 ;;
        esac
        log_info "Installing codex CLI $CODEX_VERSION (log: $codex_log)"
        _npm_install_pinned @openai/codex "$CODEX_VERSION" "$CODEX_SHA512" "$platform" "$sha" >>"$codex_log" 2>&1 || rc=$?
        case "$rc" in
            0) return 0 ;;
            3)
                log_error "cannot install the Codex CLI: the npm global prefix (/usr) is root-owned and passwordless sudo is unavailable in this container"
                return 1
                ;;
            *)
                tail -20 "$codex_log"
                log_error "codex CLI install failed (see $codex_log, last 20 lines above)"
                return 1
                ;;
        esac
    }

    _converge codex "$CODEX_VERSION" _probe_codex _install_codex required
}

_codex_session_valid() {
    local status_output
    if ! status_output="$(codex login status 2>&1)"; then
        return 1
    fi
    # The CLI's status text is logged, never parsed (D7/Gap 10): a wording change upstream must not
    # invalidate a genuinely authenticated session. Validity keys on the exit code (above) plus
    # auth_mode + refresh-token read from auth.json directly, via jq.
    local auth_file="${CODEX_HOME:-$HOME/.codex}/auth.json"
    if [ ! -f "$auth_file" ]; then
        return 1
    fi
    # An absent/null/empty auth_mode with tokens is ChatGPT, as import and the Go twin
    # codexSessionValid read it. Only null is dropped (not `// empty`), so a non-string
    # auth_mode such as false stays refused on both sides.
    local auth_mode
    auth_mode="$(jq -r '.auth_mode | select(. != null)' "$auth_file" 2>/dev/null)" || return 1
    if [ -n "$auth_mode" ] && ! printf '%s' "$auth_mode" | grep -qiE "^(chatgpt|chatgptauthtokens)$"; then
        return 1
    fi
    local refresh_token
    refresh_token="$(jq -r '.tokens.refresh_token // empty' "$auth_file" 2>/dev/null)" || refresh_token=""
    if [ -z "$refresh_token" ]; then
        return 1
    fi
    return 0
}

# _run_with_timeout <secs> <command...>: coreutils timeout(1)'s contract (TERM the command at the
# deadline, return 124) in plain bash, because vanilla macOS ships no `timeout`. The explicit <&0
# keeps the command's stdin, which bash otherwise points at /dev/null for a background job. The
# watchdog's output goes to /dev/null so its sleep never holds the caller's pipes open, and so do
# the waits' stderr, where bash 3.2 announces every job a signal ended ("Terminated: 15").
_run_with_timeout() {
    local secs="$1" cmd_pid watchdog_pid rc=0 watchdog_rc=0
    shift
    "$@" <&0 &
    cmd_pid=$!
    (
        sleep_pid=""
        trap 'kill "$sleep_pid" 2>/dev/null; exit 0' TERM
        sleep "$secs" &
        sleep_pid=$!
        wait "$sleep_pid"
        # Past the deadline: the caller's TERM, racing the kill below, must not turn 124 into 0.
        trap '' TERM
        kill -TERM "$cmd_pid" 2>/dev/null && exit 124
        exit 0
    ) >/dev/null 2>&1 &
    watchdog_pid=$!
    wait "$cmd_pid" 2>/dev/null || rc=$?
    kill -TERM "$watchdog_pid" 2>/dev/null || true
    wait "$watchdog_pid" 2>/dev/null || watchdog_rc=$?
    if [ "$watchdog_rc" -eq 124 ]; then
        return 124
    fi
    return "$rc"
}

_ensure_codex_session() {
    if _codex_session_valid; then
        log_success "Codex session verified (no login needed)"
        return 0
    fi
    log_info "Authenticate to Codex: follow the URL and one-time code printed below (the code expires in 15 minutes)"
    log_info "Waiting for the Codex login to complete…"
    local _login_rc=0
    _run_with_timeout "$CODEX_LOGIN_TIMEOUT" codex login --device-auth || _login_rc=$?
    if [ "$_login_rc" -eq 124 ]; then
        log_error "Codex login was not completed within 15 minutes; the gateway is not authenticated and agents are down until a bootstrap completes"
        exit 1
    fi
    if [ "$_login_rc" -ne 0 ]; then
        log_error "codex login --device-auth failed (exit $_login_rc); the gateway is not authenticated and agents are down until a bootstrap completes"
        exit 1
    fi
    if ! _codex_session_valid; then
        log_error "codex login --device-auth exited cleanly but did not establish a valid Codex session; the gateway is not authenticated and agents are down until a bootstrap completes"
        exit 1
    fi
    log_success "Codex session verified"
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
        echo 'export ANTHROPIC_MODEL="${ANTHROPIC_MODEL:-claude-opus-5}"'
        echo 'export ANTHROPIC_DEFAULT_OPUS_MODEL="${ANTHROPIC_DEFAULT_OPUS_MODEL:-claude-opus-5}"'
        echo 'export ANTHROPIC_DEFAULT_SONNET_MODEL="${ANTHROPIC_DEFAULT_SONNET_MODEL:-claude-sonnet-5}"'
        echo 'export CLAUDE_CODE_EFFORT_LEVEL="${CLAUDE_CODE_EFFORT_LEVEL:-xhigh}"'
        echo 'export CLAUDE_CODE_DISABLE_AUTO_MEMORY="${CLAUDE_CODE_DISABLE_AUTO_MEMORY:-1}"'
        echo 'export DISABLE_AUTOUPDATER="${DISABLE_AUTOUPDATER:-1}"'
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
  ./quickstart.sh           Full setup (always auto mode, no prompts). Every run converges
                            the container to the pinned versions: Claude Code, gh, Go,
                            Playwright, the Codex CLI and OpenObserve when present, both
                            Python locks, nodejs and the Ubuntu security pocket. A required
                            component that cannot converge makes the run exit 1 at the end,
                            after every other component has been attempted
  ./quickstart.sh --check   Read-only drift report: prints each component that differs from
                            its pin, refreshes .runtime/bootstrap/convergence.json and exits
                            1 on any drift; installs and changes nothing
  ./quickstart.sh --litellm Full setup, then stand up the LiteLLM gateway for
                            OpenAI-model profiles (see USING_LITELLM.md)
  ./quickstart.sh --litellm-auth=<api-key|codex-subscription>
                            Choose the LiteLLM gateway's upstream auth mode
                            (default: api-key). codex-subscription runs agents
                            through a Codex/ChatGPT subscription instead of an
                            OpenAI API key; requires --litellm.
  ./quickstart.sh --no-telemetry
                            Full setup, but SKIP the OpenObserve telemetry backend
                            (provisioned by default; installing it is not enabling it —
                            run 'af telemetry on' to start exporting)
  ./quickstart.sh --help    Show this help

Environment:
  AF_CODEX_INSTALL_CONSENT=yes
                            Skip the interactive install-consent prompt when the codex
                            CLI must be installed for --litellm-auth=codex-subscription.

This script runs inside a container created by quickdocker.sh.
It builds af, converges the pinned tools, then configures the factory workspace.

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
            --litellm)
                WITH_LITELLM=true
                shift
                ;;
            --litellm-auth=*)
                # PR #688 Phase 3: --litellm-auth REQUIRES --litellm (checked once parse_args
                # finishes, order-independent) — it does not imply it. See install.go's mirrored
                # requires-refusal and decisions.md D10.
                LITELLM_AUTH_MODE="${1#--litellm-auth=}"
                case "$LITELLM_AUTH_MODE" in
                    api-key|codex-subscription) ;;
                    *)
                        log_error "invalid --litellm-auth value '$LITELLM_AUTH_MODE': must be api-key or codex-subscription"
                        exit 1
                        ;;
                esac
                shift
                ;;
            --litellm-auth)
                # Space form `--litellm-auth <mode>` (F25/PR #688): consume the value in $2 rather
                # than falling through to the *) warn-and-ignore arm, which would silently leave the
                # mode empty. Same enum validation as the =-form arm above.
                LITELLM_AUTH_MODE="${2:-}"
                case "$LITELLM_AUTH_MODE" in
                    api-key|codex-subscription) ;;
                    *)
                        log_error "invalid --litellm-auth value '$LITELLM_AUTH_MODE': must be api-key or codex-subscription"
                        exit 1
                        ;;
                esac
                shift 2
                ;;
            --no-telemetry)
                # O-1: opt-out. A REAL case arm — without it the flag would fall through to
                # the *) arm below (warn + continue) and be silently swallowed while the
                # backend still installed by default.
                WITH_TELEMETRY=false
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

# _litellm_auth_mode_read / _litellm_auth_mode_write (K2, issue #693 Phase 1): bash twins of
# gatewayAuthMode's record accessor. Thin, non-migrating (decisions.md D6): read echoes the
# record's content if present, empty if absent; write is a literal, unguarded printf.
# setup_litellm is the only writer in the tree; wiring the write call into its ladder is K13, a
# later phase — Phase 1 only lands the pair, unwired.
_litellm_auth_mode_read() {
    if [ -f ".agentfactory/litellm-auth-mode" ]; then
        cat ".agentfactory/litellm-auth-mode"
    fi
}

_litellm_auth_mode_write() {
    printf '%s\n' "$1" > .agentfactory/litellm-auth-mode
}

# _ensure_mode_yaml <mode> (issue af-d0bff338, PR #694 Phase 3, K9): gate + legacy-migration
# helper for the per-mode litellm config file. Returns 0 when the caller should (re)write the
# mode's seed now — the target is absent, or (api-key only) an existing litellm.yaml carries
# ONLY chatgpt/ lanes, a hand-switched pre-693 artifact (decisions.md D2: mirrors
# gatewayAuthMode's tier-2 lane scan, gateway_auth.go:220-240, exactly — chatgpt-only triggers,
# both-present or neither-present does not). Returns 1 when an existing, non-legacy file must be
# left untouched (content and mtime unchanged). The legacy save-aside (never delete, ADR-017) +
# log_warn naming the copy fire here as a side effect; the actual heredoc write stays in
# setup_litellm() (decisions.md D5: heredocs stay byte-unchanged and physically inside
# setup_litellm(); D1: UTC, colon-free timestamp).
_ensure_mode_yaml() {
    local mode="$1"
    local target
    if [ "$mode" = "api-key" ]; then
        target=".agentfactory/litellm.yaml"
    else
        target=".agentfactory/litellm.codex-subscription.yaml"
    fi

    if [ ! -f "$target" ]; then
        return 0
    fi

    if [ "$mode" = "api-key" ]; then
        local saw_openai=false saw_chatgpt=false lane
        while IFS= read -r lane; do
            case "$lane" in
                openai/*) saw_openai=true ;;
                chatgpt/*) saw_chatgpt=true ;;
            esac
        done < <(grep -E '^[[:space:]]+model:[[:space:]]*[^[:space:]#]+' "$target" | sed -E 's/^[[:space:]]+model:[[:space:]]*([^[:space:]#]+).*/\1/')

        if $saw_chatgpt && ! $saw_openai; then
            local saved="${target}.$(date -u +%Y%m%dT%H%M%SZ).saved"
            cp "$target" "$saved"
            log_warn "Legacy $target carries only chatgpt/ lanes under api-key mode; saved aside to $saved and reseeding"
            return 0
        fi
    fi

    return 1
}

# _write_codex_compat_module <factory_root>: writes the af-owned LiteLLM proxy hook that the
# subscription yaml names in litellm_settings.callbacks. LiteLLM resolves that module relative to
# the CONFIG FILE's directory (proxy/types_utils/utils.py get_instance_fn), so the file lives next
# to the yaml. Rewritten on every bootstrap — it is af's, never the operator's — and a changed body
# stops the running gateway so _reconcile_gateway relaunches it with the new hook loaded: the
# launch identity hashes only the yaml, and a gateway serving a stale hook is the stale-proxy class
# issue #693 removed. Why it exists: the ChatGPT/Codex backend rejects system-role input items
# ("System messages are not allowed"); LiteLLM folds a STRING system prompt into Responses
# `instructions` but forwards the block-array system prompt Claude Code sends on every turn as a
# system-role item (BerriAI/litellm#21420, closed not-planned). Flattening the array to a string
# before translation is the whole fix. Retire it when `af config models check <profile> --first`
# passes with the callbacks line removed.
_write_codex_compat_module() {
    local dir="$1/.agentfactory" target tmp
    target="$dir/af_codex_compat.py"
    tmp="$(mktemp "$dir/.af_codex_compat.XXXXXX")"
    cat > "$tmp" << 'PYEOF'
"""af-owned LiteLLM proxy hook for the ChatGPT-subscription (Codex) route.

Written by quickstart.sh on every bootstrap; edits here are overwritten. The Codex backend
rejects system-role input items ("System messages are not allowed"). LiteLLM folds a string
system prompt into Responses `instructions` but forwards a block-array system prompt — what
Claude Code sends on every turn — as a system-role item. This hook flattens the array into a
string before LiteLLM translates the request (upstream: BerriAI/litellm#21420).
"""
from typing import Any, Optional, Union

from litellm.integrations.custom_logger import CustomLogger


def flatten_system(system: Any) -> Any:
    """Return a block-array system prompt as one string; anything else unchanged."""
    if not isinstance(system, list):
        return system
    texts = [
        block["text"]
        for block in system
        if isinstance(block, dict)
        and block.get("type") == "text"
        and isinstance(block.get("text"), str)
        and block["text"]
    ]
    return "\n\n".join(texts) if texts else None


class AfCodexCompat(CustomLogger):
    async def async_pre_call_hook(
        self, user_api_key_dict, cache, data: dict, call_type
    ) -> Optional[Union[Exception, str, dict]]:
        if call_type == "anthropic_messages" and isinstance(data.get("system"), list):
            flattened = flatten_system(data["system"])
            if flattened is None:
                data.pop("system", None)
            else:
                data["system"] = flattened
        return data


proxy_handler_instance = AfCodexCompat()
PYEOF
    chmod 0644 "$tmp"
    if [ -f "$target" ] && [ "$(sha256sum "$tmp" | cut -d' ' -f1)" = "$(sha256sum "$target" | cut -d' ' -f1)" ]; then
        rm -f "$tmp"
        return 0
    fi
    mv "$tmp" "$target"
    log_info "Wrote $target (af-owned LiteLLM hook, rewritten by every bootstrap)"
    if tmux has-session -t =litellm 2>/dev/null; then
        log_info "The gateway hook changed; stopping the gateway so it relaunches with the new hook"
        _gateway_stop
    fi
}

# _ensure_codex_compat_wiring <yaml>: makes an existing subscription yaml name the hook above in
# litellm_settings.callbacks. The seed carries the line; a yaml seeded before the hook existed does
# not, and the operator owns that file (never overwritten), so the one line is inserted in place —
# after a bare `litellm_settings:` header, or as a new block when there is none — with the
# previous copy saved aside (ADR-017: never delete). Idempotent: an already-wired file is not
# touched. A yaml that declares its own callbacks is operator-managed beyond what a textual insert
# can do safely, so that case ends loud with the exact line to add. Missing file: nothing to do.
_ensure_codex_compat_wiring() {
    local target="$1" saved
    [ -f "$target" ] || return 0
    if grep -qE '^[[:space:]]+callbacks:[[:space:]]*af_codex_compat\.proxy_handler_instance([[:space:]]|$)' "$target"; then
        return 0
    fi
    if grep -qE '^[[:space:]]*callbacks:' "$target"; then
        log_error "$target declares litellm_settings.callbacks without af_codex_compat.proxy_handler_instance; the Codex backend rejects Claude Code's system prompt without that hook — add af_codex_compat.proxy_handler_instance to that callbacks entry"
        return 1
    fi
    # Appending a second top-level litellm_settings would silently replace the operator's block (YAML
    # keeps the last key), so any header form sed cannot extend is refused instead.
    local header_re='^litellm_settings:[[:space:]]*(#.*)?$' other_header
    if ! grep -qE "$header_re" "$target"; then
        other_header="$(grep -m1 -E "^[\"']?litellm_settings[\"']?[[:space:]]*:" "$target")" || other_header=""
        if [ -n "$other_header" ]; then
            log_error "$target declares litellm_settings as '$other_header', which af cannot safely edit; the Codex backend rejects Claude Code's system prompt without the hook — add 'callbacks: af_codex_compat.proxy_handler_instance' under litellm_settings"
            return 1
        fi
    fi
    saved="${target}.$(date -u +%Y%m%dT%H%M%SZ).saved"
    cp "$target" "$saved"
    if grep -qE "$header_re" "$target"; then
        # Attached -i suffix and an escaped literal newline: the only forms GNU and BSD sed share.
        sed -i.bak -E 's/^(litellm_settings:[[:space:]]*(#.*)?)$/\1\
  callbacks: af_codex_compat.proxy_handler_instance/' "$target" || return 1
        rm -f "$target.bak"
    else
        printf '\nlitellm_settings:\n  callbacks: af_codex_compat.proxy_handler_instance\n' >> "$target"
    fi
    log_warn "Wired af_codex_compat into $target (previous copy saved to $saved): the Codex backend rejects Claude Code's system prompt without it"
    return 0
}

# _port_in_use / _port_owner_pid (K11, issue #693 Phase 1): hoisted to top level so both
# setup_litellm and setup_telemetry can reach them (decisions.md D7: hoist is location-only, the
# body below is byte-identical to the one previously nested inside setup_telemetry). No `ss`/`lsof`
# in the image, so both probe /proc directly.
_port_in_use() { (exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null && { exec 3>&-; return 0; } || return 1; }

# _port_owner_pid: resolves the pid owning a listening port from /proc/net/tcp, matched against
# /proc/<pid>/fd of the current process tree. Returns the owning pid or empty — no third
# error-return path (decisions.md D5); a permission-denied fd on one candidate pid is skipped, not
# fatal to the whole scan.
_port_owner_pid() {
    local port="$1" hexport inode pid fd_target
    hexport="$(printf '%04X' "$port")"
    inode="$(awk -v p=":$hexport" '$2 ~ p"$" {split($10,a,":"); print a[1]; exit}' /proc/net/tcp 2>/dev/null)"
    [ -z "$inode" ] && { echo ""; return; }
    for pid in /proc/[0-9]*; do
        pid="${pid#/proc/}"
        for fd in "/proc/$pid/fd/"*; do
            [ -e "$fd" ] || continue
            fd_target="$(readlink "$fd" 2>/dev/null)" || continue
            if [ "$fd_target" = "socket:[$inode]" ]; then
                echo "$pid"
                return
            fi
        done
    done
    echo ""
}

# setup_litellm (--litellm): automates USING_LITELLM.md's manual steps. Runs LAST,
# after the normal bootstrap has fully completed — the factory never depends on it.
# Because the flag is an explicit opt-in, failures here are loud (exit 1), not the
# warn-and-continue posture used for optional components. This is also the one
# quickstart path allowed to prompt (for the OpenAI key, env-first like the
# quickdocker PAT flow); unattended runs must provide OPENAI_API_KEY or the
# secret file instead.
setup_litellm() {
    log_step "Setting up LiteLLM gateway (--litellm)"

    # configure_factory cd'd to the factory root earlier in main; everything below
    # is factory-root-relative, matching the launch-line deref convention.
    local factory_root="$PWD"
    local secrets_dir=".agentfactory/secrets"
    local openai_key_file="$secrets_dir/openai.key"
    local master_key_file="$secrets_dir/litellm.key"

    if [ ! -f ".agentfactory/models.json" ]; then
        log_error ".agentfactory/models.json not found — factory workspace is not configured"
        exit 1
    fi

    # _reconcile_gateway (K12, issue #693 Phase 4) stops through the top-level _gateway_stop, the
    # only kill site for the litellm tmux session.
    # _reconcile_gateway <relaunch_script>: desired identity (from the script's own --identity
    # mode) vs. recorded identity (launch.json minus launched_at/pane_pid) decide whether the
    # running session is kept, restarted, or a fresh one launched. A foreign listener on the
    # port with no session refuses rather than launching into a collision.
    _reconcile_gateway() {
        local relaunch_script="$1"
        local desired recorded desired_canon
        desired="$("$relaunch_script" --identity 2>/dev/null)"
        if tmux has-session -t =litellm 2>/dev/null; then
            if [ -f .runtime/gateway/launch.json ]; then
                recorded="$(jq -S -c 'del(.launched_at, .pane_pid)' .runtime/gateway/launch.json 2>/dev/null)" || recorded=""
            else
                recorded=""
            fi
            desired_canon="$(printf '%s' "$desired" | jq -S -c 'del(.launched_at, .pane_pid)' 2>/dev/null)"
            if [ -z "$(printf '%s' "$desired" | jq -r '.credential_ref // empty' 2>/dev/null)" ]; then
                log_info "Gateway credential reference is unknown (no API key mtime or import record); restarting"
            elif [ -n "$recorded" ] && [ "$desired_canon" = "$recorded" ]; then
                log_success "Gateway identity matches the running gateway; no restart"
                return 0
            else
                log_info "Gateway identity changed since the last launch; restarting"
            fi
            log_warn "restarting the gateway causes a brief blip for any live agent session"
            _gateway_stop
        else
            if _port_in_use "$LITELLM_PORT"; then
                log_error "port $LITELLM_PORT is occupied by a process outside the 'litellm' tmux session; af does not stop processes it did not start"
                exit 1
            fi
        fi
        "$relaunch_script"
        if tmux has-session -t =litellm 2>/dev/null; then
            log_info "Launched LiteLLM in tmux session 'litellm'"
        else
            log_warn "gateway-relaunch.sh did not start the LiteLLM session (missing subscription handle or api key?); see USING_LITELLM.md"
        fi
    }

    # K3 -> stop -> K2 (D-9): pip's dry run decides whether the lock changes the installed build; a
    # live gateway is stopped first because a mixed-version live process is worse than a stopped one.
    # The locked install itself always runs, and pip decides what it changes.
    local lite_rc=0 lite_report=""
    _pip_would_change "$SCRIPT_DIR/py/litellm-requirements.txt" >/dev/null || lite_rc=$?
    if [ "$lite_rc" -ne 1 ] && _litellm_session_running; then
        _gateway_stop
    fi
    log_info "Installing the LiteLLM lock (py/litellm-requirements.txt)"
    if [ "$lite_rc" -eq 2 ]; then
        lite_report="$(mktemp)" || lite_report=""
    fi
    if ! _pip_install_locked "$SCRIPT_DIR/py/litellm-requirements.txt" ${lite_report:+--report "$lite_report"}; then
        log_error "litellm install failed"
        exit 1
    fi
    _lock_result litellm-lock py/litellm-requirements.txt "$lite_rc" 0 "$lite_report"
    rm -f "$lite_report"
    log_success "litellm present: $(litellm --version 2>/dev/null | grep -m1 . || echo 'version unknown')"
    if ! python3 -c 'import litellm.proxy' 2>/dev/null; then
        log_error "litellm.proxy failed to import after install — the build appears corrupt"
        exit 1
    fi

    mkdir -p "$secrets_dir"
    chmod 700 "$secrets_dir"

    # Mode ladder (BODY-1/decisions.md D17): parity with install.go's resolveLitellmAuthMode
    # (install.go:944-962) — an explicit --litellm-auth flag (parse_args already enum-validated it,
    # left in LITELLM_AUTH_MODE) wins, else the AF_LITELLM_AUTH env override, else the recorded mode
    # (_litellm_auth_mode_read), else handle-presence migration. install.go (D2) always forwards a
    # fully-resolved mode, so the lower tiers do handle-detection work only on a direct
    # ./quickstart.sh invocation. The both-handles refusal (E9) guards-and-surfaces ONLY on the
    # migration leg: once a flag, env, or record resolves the mode, coexisting handles are
    # unambiguous and must not refuse.
    local chatgpt_auth_file=".agentfactory/secrets/chatgpt/auth.json"
    if [ -z "$LITELLM_AUTH_MODE" ] && [ -n "${AF_LITELLM_AUTH:-}" ]; then
        case "$AF_LITELLM_AUTH" in
            api-key|codex-subscription) LITELLM_AUTH_MODE="$AF_LITELLM_AUTH" ;;
            *) log_error "invalid AF_LITELLM_AUTH value '$AF_LITELLM_AUTH': must be api-key or codex-subscription"; exit 1 ;;
        esac
    fi
    if [ -z "$LITELLM_AUTH_MODE" ]; then
        LITELLM_AUTH_MODE="$(_litellm_auth_mode_read)"
    fi
    if [ -z "$LITELLM_AUTH_MODE" ]; then
        local has_key=false has_sub=false
        [ -s "$openai_key_file" ] && has_key=true
        [ -s "$chatgpt_auth_file" ] && has_sub=true
        if $has_key && $has_sub; then
            log_error "both an OpenAI API key ($openai_key_file) and a ChatGPT-subscription handle ($chatgpt_auth_file) exist; pass --litellm-auth=<api-key|codex-subscription> to choose one"
            exit 1
        elif $has_sub; then
            LITELLM_AUTH_MODE="codex-subscription"
        else
            LITELLM_AUTH_MODE="api-key"
        fi
    fi
    log_info "LiteLLM upstream auth mode: $LITELLM_AUTH_MODE"

    if [ "$LITELLM_AUTH_MODE" = "api-key" ]; then
        # OpenAI key: existing secret file > environment > interactive prompt.
        # printf '%s' is load-bearing: the launch-line deref "$(cat …)" tolerates but
        # must not rely on trailing-newline trimming.
        if [ -s "$openai_key_file" ]; then
            log_info "Using existing OpenAI key at $openai_key_file"
        elif [ -n "${OPENAI_API_KEY:-}" ]; then
            printf '%s' "$OPENAI_API_KEY" > "$openai_key_file"
            log_info "Persisted OPENAI_API_KEY from environment to $openai_key_file"
        elif [ -t 0 ]; then
            local _openai_key=""
            read -rsp "OpenAI API key (stored at $openai_key_file): " _openai_key
            echo ""
            if [ -z "$_openai_key" ]; then
                log_error "No OpenAI API key provided"
                exit 1
            fi
            printf '%s' "$_openai_key" > "$openai_key_file"
            unset _openai_key
        else
            log_error "--litellm needs an OpenAI API key: set OPENAI_API_KEY or create $openai_key_file"
            exit 1
        fi
        chmod 600 "$openai_key_file"
    else
        # Subscription mode: the ChatGPT-subscription handle comes from `af gateway auth import`
        # (E2), never an OpenAI key — the frame-lift invariant keeps this branch and the api-key
        # branch above fully disjoint; neither ever falls back to the other.
        _ensure_codex_cli || exit 1
        _ensure_codex_session
        if ! af gateway auth import; then
            exit 1
        fi
    fi

    # Stamp the mode record only AFTER the mode's prerequisites have succeeded (T15/decisions.md D13):
    # api-key has its key, subscription has a CLI + a valid session + an imported handle. Writing it
    # up in the ladder (the PR's original K13 position) left a lying `codex-subscription` record when
    # consent/login was declined and the branch exited 1, bricking later flagless redeploys
    # (AC-11/AC-12). The relaunch script regenerated below reads this record at runtime, so stamping
    # it here does not starve it.
    _litellm_auth_mode_write "$LITELLM_AUTH_MODE"

    # Master key: generated once, reused on every rerun (regenerating would orphan
    # any copy already handed out).
    if [ ! -s "$master_key_file" ]; then
        printf 'sk-litellm-%s' "$(openssl rand -hex 16 2>/dev/null || od -An -tx1 -N16 /dev/urandom | tr -d ' \n')" > "$master_key_file"
        log_info "Generated LiteLLM master key at $master_key_file"
    fi
    chmod 600 "$master_key_file"

    # Seed configs only when absent — operator edits are never overwritten. Each mode seeds its
    # own dedicated target file via _ensure_mode_yaml (K9, issue af-d0bff338 PR #694 Phase 3);
    # the two targets are independent (decisions.md D4/D5) — a subscription run never touches
    # litellm.yaml and vice versa. The prior phase's save-aside orphan fork is retired; a legacy
    # chatgpt-only litellm.yaml under api-key mode is migrated by _ensure_mode_yaml itself
    # (decisions.md D2/D3), not by this call site.
    if [ "$LITELLM_AUTH_MODE" = "api-key" ]; then
      if _ensure_mode_yaml "api-key"; then
        cat > ".agentfactory/litellm.yaml" << 'EOF'
model_list:
  - model_name: gpt-4o                # substitute the OpenAI model id you want agents on
    litellm_params:
      model: openai/gpt-4o
      api_key: os.environ/OPENAI_API_KEY
  - model_name: gpt-4o-mini           # small model for Claude Code's background calls
    litellm_params:
      model: openai/gpt-4o-mini
      api_key: os.environ/OPENAI_API_KEY

  # Aliases for the model ids the host asks for BY NAME. A session picks its main model from the
  # profile, but sub-agents and background calls request a CLASS, and any class key left unset falls
  # back to the host's own built-in claude-* id — which a gateway with a closed model_list refuses,
  # killing the spawn that asked for it. No ANTHROPIC_* key can redirect those, so only an alias
  # answers them; `af config models check` demands one per claude-* id this factory's direct
  # profiles name. Point the LARGE classes (opus/sonnet/fable) at the MAIN model: the host sizes
  # their context window large by the claude- id it asked for, so aliasing them to the small backend
  # trades an unserved-model failure for an over-window one. The haiku class routes to the small
  # backend below — the host sizes a claude-haiku-4-5 request small, so that is safe and cheaper.
  # Ids are enumerated one per line — LiteLLM wildcard routing is not
  # relied on. Add a line here whenever you add a direct claude-* profile.
  - model_name: claude-opus-5
    litellm_params:
      model: openai/gpt-4o
      api_key: os.environ/OPENAI_API_KEY
  - model_name: claude-sonnet-5
    litellm_params:
      model: openai/gpt-4o
      api_key: os.environ/OPENAI_API_KEY
  - model_name: claude-opus-4-8
    litellm_params:
      model: openai/gpt-4o
      api_key: os.environ/OPENAI_API_KEY
  - model_name: claude-fable-5
    litellm_params:
      model: openai/gpt-4o
      api_key: os.environ/OPENAI_API_KEY
  - model_name: claude-haiku-4-5       # haiku-class requests go to the small backend
    litellm_params:
      model: openai/gpt-4o-mini
      api_key: os.environ/OPENAI_API_KEY

general_settings:
  master_key: os.environ/LITELLM_MASTER_KEY
EOF
        log_info "Seeded .agentfactory/litellm.yaml"
      fi
    else
      # Subscription seed (F14/PR #688, K9/PR #694 Phase 3): the subscription litellm.yaml body has
      # ONE source — this heredoc captured into a variable — seeded into its own dedicated target,
      # .agentfactory/litellm.codex-subscription.yaml, only when absent (_ensure_mode_yaml); an
      # api-key-mode litellm.yaml, if any, is never touched by this branch. No api_key entry (the
      # upstream credential lives in gateway-relaunch.sh's CHATGPT_TOKEN_DIR export, never in this
      # file) and no fallbacks block; drop_params is LiteLLM's own litellm_settings key, not a
      # per-entry param. The SUBEOF delimiter stays distinct from heredoc #1's 'EOF' so the two seeds
      # never corrupt each other's test extraction.
      local subscription_yaml_body
      subscription_yaml_body="$(cat << 'SUBEOF'
model_list:
  - model_name: gpt-5.6-sol            # substitute the Codex/ChatGPT model id you want agents on
    litellm_params:
      model: chatgpt/responses/gpt-5.6-sol
  - model_name: gpt-5.6-terra          # mid lane: the codex profile's sonnet class
    litellm_params:
      model: chatgpt/responses/gpt-5.6-terra
  - model_name: gpt-5.6-luna           # small model for Claude Code's background calls
    litellm_params:
      model: chatgpt/responses/gpt-5.6-luna

  # Aliases for the model ids the host asks for BY NAME — same rationale as the api-key seed
  # above, routed through the chatgpt/responses backend instead of openai/.
  - model_name: claude-opus-5
    litellm_params:
      model: chatgpt/responses/gpt-5.6-sol
  - model_name: claude-sonnet-5
    litellm_params:
      model: chatgpt/responses/gpt-5.6-sol
  - model_name: claude-opus-4-8
    litellm_params:
      model: chatgpt/responses/gpt-5.6-sol
  - model_name: claude-fable-5
    litellm_params:
      model: chatgpt/responses/gpt-5.6-sol
  - model_name: claude-fable-5-1
    litellm_params:
      model: chatgpt/responses/gpt-5.6-sol
  - model_name: claude-opus-5-5
    litellm_params:
      model: chatgpt/responses/gpt-5.6-sol
  - model_name: claude-haiku-4-5       # haiku-class requests go to the small backend
    litellm_params:
      model: chatgpt/responses/gpt-5.6-luna

litellm_settings:
  # af_codex_compat.py sits next to this file and is rewritten by every bootstrap: it flattens
  # Claude Code's block-array system prompt to a string before translation, because the Codex
  # backend rejects system-role items ("System messages are not allowed", BerriAI/litellm#21420).
  # Drop this line only when `af config models check <profile> --first` passes without it.
  callbacks: af_codex_compat.proxy_handler_instance
  drop_params: true

router_settings:
  num_retries: 2
  timeout: 3000               # agentic turns can run long; don't use 30s

general_settings:
  master_key: os.environ/LITELLM_MASTER_KEY
SUBEOF
)"
      if _ensure_mode_yaml "codex-subscription"; then
        printf '%s\n' "$subscription_yaml_body" > ".agentfactory/litellm.codex-subscription.yaml"
        log_info "Seeded .agentfactory/litellm.codex-subscription.yaml"
      fi
      # The hook the yaml names, and the one-line wiring for a yaml seeded before the hook existed.
      _ensure_codex_compat_wiring ".agentfactory/litellm.codex-subscription.yaml" || exit 1
      _write_codex_compat_module "$factory_root"
    fi

    # Context window: deliberately NOT seeded. CLAUDE_CODE_MAX_CONTEXT_TOKENS
    # declares the model's real window; CLAUDE_CODE_AUTO_COMPACT_WINDOW declares
    # the token count the host compacts at; above 200k it needs the first key
    # or the host silently caps it. quickstart cannot know what your gateway
    # actually serves, and a guessed value would be the unnegotiated constant
    # issue #602 exists to remove. Operators set both per profile; the
    # runbook for choosing values is USING_LITELLM.md.
    if [ "$LITELLM_AUTH_MODE" = "api-key" ]; then
      if ! jq -e '.models.codex' .agentfactory/models.json >/dev/null 2>&1; then
        jq --arg url "http://localhost:$LITELLM_PORT" '.models.codex = {
            "ANTHROPIC_BASE_URL": $url,
            "ANTHROPIC_AUTH_TOKEN": "file:.agentfactory/secrets/litellm.key",
            "ANTHROPIC_MODEL": "gpt-4o",
            "ANTHROPIC_DEFAULT_HAIKU_MODEL": "gpt-4o-mini",
            "ANTHROPIC_API_KEY": ""
        }' .agentfactory/models.json > .agentfactory/models.json.tmp \
            && mv .agentfactory/models.json.tmp .agentfactory/models.json
        log_info "Seeded codex profile in .agentfactory/models.json"
      fi
    else
      # NEW jq block, sibling to .models.codex above — NEVER a mutation of that block (the
      # frame-lift invariant: upstream-auth mode is never a key inside a profile map).
      if ! jq -e '.models["codex-subscription"]' .agentfactory/models.json >/dev/null 2>&1; then
        jq --arg url "http://localhost:$LITELLM_PORT" '.models["codex-subscription"] = {
            "ANTHROPIC_BASE_URL": $url,
            "ANTHROPIC_AUTH_TOKEN": "file:.agentfactory/secrets/litellm.key",
            "ANTHROPIC_MODEL": "gpt-5.6-sol",
            "ANTHROPIC_DEFAULT_HAIKU_MODEL": "gpt-5.6-luna",
            "ANTHROPIC_API_KEY": "",
            "AF_DISABLE_PARALLEL_SUBAGENTS": "1"
        }' .agentfactory/models.json > .agentfactory/models.json.tmp \
            && mv .agentfactory/models.json.tmp .agentfactory/models.json
        log_info "Seeded codex-subscription profile in .agentfactory/models.json"
      fi
    fi

    # NEW .agentfactory/gateway-relaunch.sh — a STANDALONE script (not merely inlined),
    # modeled on the telemetry relaunch.sh writer (:1136-1166 era). Called from BOTH the tmux
    # first-bring-up below and the login-shell relaunch guard, so the launch line has exactly
    # one home — no inline `litellm --config …` line remains anywhere in this function. Mode
    # 0700 (not telemetry's 0755): this script's body exports secret-derived launch env
    # (OPENAI_API_KEY, or the ChatGPT-subscription handle), unlike telemetry's inert body.
    #
    # Two heredocs: the first (unquoted) bakes in the write-time-known factory_root/port/mode as
    # literal values (safe — no $(...) substitutions here); the second (QUOTED, 'EOF') is copied
    # byte-for-byte so the $(cat …) secret derefs stay literal in BOTH quickstart.sh's own source
    # and the written file, deferred to the pane shell that actually evaluates them.
    local relaunch_script="$factory_root/.agentfactory/gateway-relaunch.sh"
    cat > "$relaunch_script" << EOF
#!/bin/bash
# Written by quickstart.sh's setup_litellm() (PR #688 Phase 3, issue #686). Relaunches the
# LiteLLM gateway when its tmux session is absent. Best-effort and idempotent.
umask 077
FACTORY_ROOT="$factory_root"
LITELLM_PORT="$LITELLM_PORT"
EOF
    cat >> "$relaunch_script" << 'EOF'
# Auth mode comes from the record .agentfactory/litellm-auth-mode at RUN time (K10/BODY-4), never a
# write-time baked value: three homes for one fact drift. Missing/invalid ⇒ warn + exit 0 (never
# launch under an unknown mode). setup_litellm stamps the record before writing this script.
LITELLM_AUTH_MODE="$(cat "$FACTORY_ROOT/.agentfactory/litellm-auth-mode" 2>/dev/null)" || LITELLM_AUTH_MODE=""
# credential_ref marks the credential's version, never its bytes, their hash, or its path, so a
# rotation changes the identity and the next reconcile restarts the gateway. Subscription uses the
# state record's imported_at, which only `af gateway auth import` writes: LiteLLM rewrites the
# handle itself on every token refresh, and that must not restart it. Unreadable ⇒ empty ⇒ unknown.
case "$LITELLM_AUTH_MODE" in
    api-key)
        _gw_config_file="$FACTORY_ROOT/.agentfactory/litellm.yaml"
        _gw_credential_ref="$(date -r "$FACTORY_ROOT/.agentfactory/secrets/openai.key" +%s 2>/dev/null)"
        ;;
    codex-subscription)
        _gw_config_file="$FACTORY_ROOT/.agentfactory/litellm.codex-subscription.yaml"
        _gw_credential_ref="$(jq -r '.imported_at // empty' "$FACTORY_ROOT/.runtime/gateway_auth/codex-subscription.json" 2>/dev/null)"
        ;;
    "")
        echo "warning: no .agentfactory/litellm-auth-mode record; refusing to relaunch the gateway (factory unaffected)" >&2
        exit 0
        ;;
    *)
        echo "error: .agentfactory/litellm-auth-mode names an unknown mode '$LITELLM_AUTH_MODE'; refusing to relaunch" >&2
        exit 0
        ;;
esac
# One identity computation feeds --identity, launch.json, AND (by mode) the launch line, so the
# config file is named and hashed identically at every site (BODY-5); a single source makes the
# name/hash-drift class of bug structurally impossible. sha256sum is a coreutil the image
# guarantees; openssl is not (BODY-6). grep -m1 . skips litellm --version's leading blank line (T3).
_gw_litellm_version="$(litellm --version 2>/dev/null | grep -m1 .)"
_gw_config_sha256="$(sha256sum "$_gw_config_file" 2>/dev/null)"
_gw_config_sha256="${_gw_config_sha256%% *}"
if [ "$1" = "--identity" ]; then
    jq -n \
        --arg mode "$LITELLM_AUTH_MODE" \
        --arg config_sha256 "$_gw_config_sha256" \
        --arg litellm_version "$_gw_litellm_version" \
        --arg credential_ref "$_gw_credential_ref" \
        --argjson port "$LITELLM_PORT" \
        --arg factory_root "$FACTORY_ROOT" \
        '{"v":1,"mode":$mode,"config_sha256":$config_sha256,"litellm_version":$litellm_version,"credential_ref":$credential_ref,"port":$port,"factory_root":$factory_root}'
    exit 0
fi
if ! command -v litellm >/dev/null 2>&1 || [ ! -s "$FACTORY_ROOT/.agentfactory/secrets/litellm.key" ]; then
    exit 0
fi
if tmux has-session -t =litellm 2>/dev/null; then
    exit 0
fi
launched=0
if [ "$LITELLM_AUTH_MODE" = "api-key" ]; then
    if [ ! -s "$FACTORY_ROOT/.agentfactory/secrets/openai.key" ]; then
        echo "warning: OpenAI API key handle missing — refusing to relaunch (factory unaffected)" >&2
        exit 0
    fi
    if tmux new-session -d -s litellm -c "$FACTORY_ROOT" \
        'OPENAI_API_KEY="$(cat .agentfactory/secrets/openai.key)" LITELLM_MASTER_KEY="$(cat .agentfactory/secrets/litellm.key)" litellm --port '"$LITELLM_PORT"' --config .agentfactory/litellm.yaml'; then
        launched=1
    fi
elif [ "$LITELLM_AUTH_MODE" = "codex-subscription" ]; then
    if [ ! -s "$FACTORY_ROOT/.agentfactory/secrets/chatgpt/auth.json" ]; then
        echo "warning: ChatGPT-subscription handle missing — refusing to relaunch into the device-code path (factory unaffected)" >&2
        exit 0
    fi
    if tmux new-session -d -s litellm -c "$FACTORY_ROOT" \
        'CHATGPT_TOKEN_DIR="'"$FACTORY_ROOT"'/.agentfactory/secrets/chatgpt" CHATGPT_DEFAULT_INSTRUCTIONS="You are a helpful coding assistant." LITELLM_MASTER_KEY="$(cat .agentfactory/secrets/litellm.key)" litellm --port '"$LITELLM_PORT"' --config .agentfactory/litellm.codex-subscription.yaml'; then
        launched=1
    fi
fi
if [ "$launched" = "1" ]; then
    mkdir -p "$FACTORY_ROOT/.runtime/gateway"
    pane_pid="$(tmux list-panes -t =litellm -F '#{pane_pid}' 2>/dev/null | head -1)"
    (
        umask 077
        jq -n \
            --arg mode "$LITELLM_AUTH_MODE" \
            --arg config_sha256 "$_gw_config_sha256" \
            --arg litellm_version "$_gw_litellm_version" \
            --arg credential_ref "$_gw_credential_ref" \
            --argjson port "$LITELLM_PORT" \
            --arg factory_root "$FACTORY_ROOT" \
            --argjson launched_at "$(date +%s)" \
            --arg pane_pid "$pane_pid" \
            '{"v":1,"mode":$mode,"config_sha256":$config_sha256,"litellm_version":$litellm_version,"credential_ref":$credential_ref,"port":$port,"factory_root":$factory_root,"launched_at":$launched_at,"pane_pid":$pane_pid}' \
            > "$FACTORY_ROOT/.runtime/gateway/launch.json"
    )
fi
tmux set-environment -g -u OPENAI_API_KEY
tmux set-environment -g -u CHATGPT_TOKEN_DIR
tmux set-environment -g -u CHATGPT_AUTH_FILE
tmux set-environment -g -u CHATGPT_API_BASE
tmux set-environment -g -u CODEX_HOME
EOF
    chmod 0700 "$relaunch_script"

    # Login-shell relaunch guard, same idiom as the webui guard: the tmux session dies with the
    # container, so bring the gateway back on the next login shell. Written here (K15), before
    # verification, so a run that dies in readiness/smoke never leaves a stale-mode guard. The
    # guard body only ever calls the relaunch script — it already no-ops via its own internal
    # has-session check when a session is present.
    local bash_profile_path="$HOME/.bash_profile"
    touch "$bash_profile_path"
    sed -i '/BEGIN agentfactory litellm login guard/,/END agentfactory litellm login guard/d' "$bash_profile_path" 2>/dev/null || true
    cat >> "$bash_profile_path" << EOF
# BEGIN agentfactory litellm login guard
"$relaunch_script"
# END agentfactory litellm login guard
EOF

    _reconcile_gateway "$relaunch_script"

    # Readiness: poll the authenticated model list (free) before the paid smoke.
    local master_key ready="" attempts=0
    master_key="$(cat "$master_key_file")"
    while [ "$attempts" -lt 30 ]; do
        if curl -fsS -H "Authorization: Bearer $master_key" "http://localhost:$LITELLM_PORT/v1/models" >/dev/null 2>&1; then
            ready=1
            break
        fi
        attempts=$((attempts + 1))
        sleep 1
    done
    if [ -z "$ready" ]; then
        log_error "LiteLLM not ready on port $LITELLM_PORT after 30s (logs: tmux attach -t litellm)"
        exit 1
    fi

    # Ownership proof (T7/K12, decisions.md D9): readiness answering on the port proves only that
    # *something* serves — not that it is THIS run's gateway. Now that the port is bound (so the
    # owner pid is resolvable), confirm the listener is the 'litellm' pane's process or a descendant
    # of it, walking /proc/<pid>/stat's PPID chain. A foreign listener that happened to hold the port
    # fails loudly here rather than being certified as the gateway.
    local _pane_pid _owner_pid _walk _ppid
    _pane_pid="$(tmux list-panes -t =litellm -F '#{pane_pid}' 2>/dev/null | head -1)"
    _owner_pid="$(_port_owner_pid "$LITELLM_PORT")"
    if [ -z "$_owner_pid" ] || [ -z "$_pane_pid" ]; then
        log_error "could not confirm which process owns port $LITELLM_PORT for the 'litellm' session; refusing to certify a gateway af cannot prove it started"
        exit 1
    fi
    _walk="$_owner_pid"
    while [ -n "$_walk" ] && [ "$_walk" != "0" ] && [ "$_walk" != "1" ] && [ "$_walk" != "$_pane_pid" ]; do
        # /proc/<pid>/stat is `pid (comm) state ppid …`; comm can contain spaces or `)`, so strip
        # through the last `") "` before reading the PPID (field 2 of the remainder).
        _ppid="$(cat "/proc/$_walk/stat" 2>/dev/null)" || _ppid=""
        _walk="$(printf '%s' "${_ppid##*) }" | awk '{print $2}')"
    done
    if [ "$_walk" != "$_pane_pid" ]; then
        log_error "port $LITELLM_PORT is served by pid $_owner_pid, which is not the 'litellm' pane ($_pane_pid) or a descendant; a foreign process owns the gateway port — af does not adopt processes it did not start"
        exit 1
    fi

    # Smoke test: af config models check --live round-trips a real request through the
    # profile matching this run's resolved auth mode, proving translation AND upstream
    # credential validity in one call (replaces the old hand-rolled curl smoke).
    local profile="codex"
    if [ "$LITELLM_AUTH_MODE" = "codex-subscription" ]; then
        profile="codex-subscription"
    fi

    # _first_probe_bootstrap <profile> <relaunch_script> (issue #693 K14): one classified
    # /v1/messages request, far cheaper than --live's per-class sweep, run ahead of it so a
    # stale ChatGPT-subscription session gets one chance at a forced re-login before the fuller
    # --live smoke runs. Nested here, same idiom as _gateway_stop/_reconcile_gateway above, so
    # it can be extracted via extractShellFunction and stub-harness-tested in isolation instead
    # of needing a stub for setup_litellm()'s entire ~500-line body. `_relogin_attempted` scopes
    # the ladder to at most once per invocation (D6) — "no further login on a second failure."
    _first_probe_bootstrap() {
        local profile="$1" relaunch_script="$2"
        local _relogin_attempted=0 _first_out
        if ! _first_out="$(af config models check "$profile" --first 2>&1)"; then
            if [ "$LITELLM_AUTH_MODE" = "codex-subscription" ] && [ "$_relogin_attempted" -eq 0 ] \
                && printf '%s' "$_first_out" | grep -qE -- '--first → (AUTH|TIMEOUT)'; then
                _relogin_attempted=1
                log_info "af config models check $profile --first reported an auth verdict; polling for a device-code re-login window"
                local device_code_requested="" _poll_attempts=0
                while [ "$_poll_attempts" -lt 10 ]; do
                    if af gateway auth status --json 2>/dev/null | jq -e '.device_code_requested == true' >/dev/null 2>&1; then
                        device_code_requested=1
                        break
                    fi
                    _poll_attempts=$((_poll_attempts + 1))
                    sleep 2
                done
                if [ -z "$device_code_requested" ]; then
                    log_error "af gateway auth status --json never reported device_code_requested after 20s (10 attempts x 2s) — gateway is not authenticated; bootstrap failed"
                    exit 1
                fi
                _ensure_codex_session
                if ! af gateway auth import; then
                    exit 1
                fi
                _reconcile_gateway "$relaunch_script"
                if ! _first_out="$(af config models check "$profile" --first 2>&1)"; then
                    log_error "af config models check $profile --first still failed after the forced re-login: $_first_out"
                    log_error "the line above carries the gateway's own error text; the gateway pane has the rest: tmux capture-pane -t litellm -p -S -200"
                    exit 1
                fi
            else
                log_error "af config models check $profile --first failed: $_first_out"
                log_error "the line above carries the gateway's own error text; the gateway pane has the rest: tmux capture-pane -t litellm -p -S -200"
                exit 1
            fi
        fi
    }
    _first_probe_bootstrap "$profile" "$relaunch_script"

    if ! af config models check "$profile" --live; then
        log_error "af config models check $profile --live failed"
        exit 1
    fi

    # Other-gateway-profile non-live check with an agent-mapping warning (D5): the resolved
    # profile's sibling gets a cheap structural check only — never --first/--live, which would
    # spend quota against a profile this run is not using. The warning fires only when the
    # sibling's own check hard-fails AND an agent is mapped to it — never on a healthy but
    # unused profile.
    local other_profile="codex-subscription"
    if [ "$profile" = "codex-subscription" ]; then
        other_profile="codex"
    fi
    if ! af config models check "$other_profile" >/dev/null 2>&1; then
        local mapped_agents
        mapped_agents="$(jq -r --arg p "$other_profile" '.agents // {} | to_entries[] | select(.value == $p) | .key' .agentfactory/models.json 2>/dev/null | paste -sd, -)"
        if [ -n "$mapped_agents" ]; then
            log_warn "profile \"$other_profile\" failed its non-live check and is mapped to agent(s): $mapped_agents"
        fi
    fi

    log_success "LiteLLM gateway ready at http://localhost:$LITELLM_PORT (tmux session: litellm)"
}

# _await_telemetry_ready <health-url> [max_seconds]: poll a loopback health URL once
# per second until it answers 2xx, returning 0 the moment it is ready and 1 on timeout.
# Bounded and non-fatal by contract — the caller decides what a timeout means. The
# default window is TELEMETRY_READY_TIMEOUT so the production value has one home; the
# integration test drives this same function to prove 30s reproduces the cold-start
# failure and the production window survives it.
# The third argument is the name of a liveness predicate, and it is OPTIONAL on purpose: the
# integration test extracts this function by name and runs it standalone in a bare shell with no
# tmux session, so a hard-wired liveness call here would turn a passing test red against a
# correct fix. Absent it, behaviour is exactly as before.
#
# Exit codes: 0 ready, 1 timed out, 2 the process died. The distinction is the whole point —
# "crashed on startup" and "still warming up" were indistinguishable, so a backend that exited in
# two seconds was reported as "not ready after 90s" and read as a slow cold start.
_await_telemetry_ready() {
    local url="$1" max_attempts="${2:-$TELEMETRY_READY_TIMEOUT}" liveness="${3:-}" attempts=0
    while [ "$attempts" -lt "$max_attempts" ]; do
        if curl -fsS "$url" >/dev/null 2>&1; then
            return 0
        fi
        # Checked AFTER the health probe so a process that became ready and exited in the same
        # second is still reported ready, and BEFORE the sleep so a crash costs one second rather
        # than the whole window.
        if [ -n "$liveness" ] && ! "$liveness"; then
            return 2
        fi
        attempts=$((attempts + 1))
        sleep 1
    done
    return 1
}

# _migrate_telemetry_endpoint <config_path> <port>: repair a telemetry.json seeded before the
# organisation segment moved into `endpoint`.
#
# Why it needs repairing: the af plane worked because it carried the org prefix on its own path,
# but the native plane is handed `endpoint` alone and appends /v1/{signal} to it, so every usage
# event went to a URL this backend answers 404 to. Moving the segment leaves the af plane's
# absolute URL byte-identical and gives the native plane a base it can derive a served path from.
#
# The guard matches the two shipped values EXACTLY as they were written, so an operator who has
# changed either one is left alone — this repairs the shipped default and nothing else.
#
# It is a function rather than an inline branch so a test can EXECUTE the real rewrite. The
# previous shape had the test run its own pasted copy of the sed, which is the same asymmetry the
# review objected to: the copy passes while the shipped command is free to rot.
#
# Exit codes are the three distinct outcomes: 0 repaired, 1 nothing to repair, 2 repair failed.
_migrate_telemetry_endpoint() {
    local cfg="$1" port="$2"
    [ -f "$cfg" ] || return 1
    grep -q '"endpoint": "http://127.0.0.1:'"$port"'"' "$cfg" || return 1
    grep -q '"otlp_http_path_traces": "/api/default/v1/traces"' "$cfg" || return 1

    # `if sed …; then` rather than `sed … && rm`: with the latter, a failed rewrite skips the
    # cleanup and still reports success, so an operator is told their config was repaired when it
    # was not. The .bak is removed only on a rewrite that actually worked, and a failure leaves the
    # original in place.
    if sed -i.bak \
        -e 's|"endpoint": "http://127.0.0.1:'"$port"'"|"endpoint": "http://127.0.0.1:'"$port"'/api/default"|' \
        -e 's|"otlp_http_path_traces": "/api/default/v1/traces"|"otlp_http_path_traces": "/v1/traces"|' \
        "$cfg"; then
        rm -f "$cfg.bak"
        return 0
    fi
    return 2
}

# _telemetry_password_is_compliant <password>: the pinned backend's own rule, quoted from the
# panic it raises when the rule is broken — "Password must be 8-128 characters and contain at
# least one lowercase letter, one uppercase letter, one digit, and one special character."
#
# Kept as a predicate rather than inlined so both the generator and the repair path ask the same
# question, and so a test can execute it instead of pattern-matching this file.
_telemetry_password_is_compliant() {
    local pw="$1"
    [ "${#pw}" -ge 8 ] && [ "${#pw}" -le 128 ] || return 1
    printf '%s' "$pw" | grep -q '[a-z]' || return 1
    printf '%s' "$pw" | grep -q '[A-Z]' || return 1
    printf '%s' "$pw" | grep -q '[0-9]' || return 1
    printf '%s' "$pw" | grep -q '[^a-zA-Z0-9]' || return 1
    return 0
}

# _generate_telemetry_password: emits one policy-compliant credential on stdout.
#
# The previous generator was `openssl rand -hex 16`, and a hex alphabet cannot contain an
# uppercase letter or a special character, so it failed the policy on every clean install
# deterministically — the backend panicked at job init and exited 1 in about two seconds.
#
# Four characters are drawn one per required class and the remainder from the full alphabet, so
# compliance is guaranteed by construction rather than by retry-until-valid, which would make
# install time depend on luck. tr -dc discards bytes outside the class, which is why each draw
# reads generously from the source.
_generate_telemetry_password() {
    local alphabet='A-Za-z0-9!@#%^_+=:,.?' pw
    # `dd` a bounded block and let `tr` consume ALL of it, rather than `tr < /dev/urandom | head -c`.
    # The latter reads correctly and is fatal here: head exits once it has its bytes, tr is killed by
    # SIGPIPE (141), pipefail promotes that to the pipeline's status, and errexit aborts the whole
    # script — truncating the credential file it was redirecting into. Nothing in this form can
    # receive SIGPIPE, because no reader closes early.
    _rand_chars() {
        local class="$1" want="$2" out=""
        while [ "${#out}" -lt "$want" ]; do
            out+="$(LC_ALL=C dd if=/dev/urandom bs=256 count=1 2>/dev/null | LC_ALL=C tr -dc "$class" || true)"
        done
        printf '%s' "${out:0:want}"
    }
    pw="$(_rand_chars 'a-z' 1)$(_rand_chars 'A-Z' 1)$(_rand_chars '0-9' 1)$(_rand_chars '!@#%^_+=:,.?' 1)$(_rand_chars "$alphabet" 20)"
    # A short read from /dev/urandom would silently produce a weak credential, so the result is
    # checked rather than assumed. There is no fallback: a factory is better off failing loudly
    # here than installing a backend that cannot start.
    if ! _telemetry_password_is_compliant "$pw"; then
        echo "ERROR: could not generate a policy-compliant telemetry credential" >&2
        return 1
    fi
    printf '%s' "$pw"
}

# setup_telemetry (default; --no-telemetry skips it): provisions the OpenObserve observability
# backend the SAME way setup_litellm provisions the gateway — pinned, checksummed, loopback-bound,
# secrets-protected, restart-documented. The binary converge is REQUIRED: a failed install is
# ledgered and the END report exits 1. Launch, readiness, smoke and dashboard hiccups still warn
# and return 0, because an installed backend that is slow to come up leaves the factory working.
# --no-telemetry skips this function and leaves an ABSENT backend uninstalled, but an installed
# backend converges in converge_present_components regardless (D-47). Installing the backend is NOT
# enabling collection: the .telemetry-gate is a separate operator lever (af telemetry on) and this
# function never touches it. No interactive prompt (ADR-014); idempotent (guard clauses +
# write-if-absent), so a re-run is the upgrade path.
setup_telemetry() {
    log_step "Setting up OpenObserve telemetry backend (default; --no-telemetry to skip)"

    # configure_factory cd'd to the factory root earlier in main; everything below is
    # factory-root-relative, matching setup_litellm's launch-line deref convention.
    local factory_root="$PWD"
    local secrets_dir=".agentfactory/secrets"
    local auth_file="$secrets_dir/telemetry.auth"
    local root_pass_file="$secrets_dir/telemetry.root"
    local root_email="root@agentfactory.local"
    local oo_bin="$OPENOBSERVE_BIN"
    local oo_data="$factory_root/.agentfactory/telemetry/openobserve"

    # Port-occupancy probe: _port_in_use is now a top-level function (K11, issue #693 Phase 1,
    # hoisted above setup_litellm so setup_litellm can reach it too); this function's body no
    # longer defines it locally.

    # Relaunch script + login-guard append, written BEFORE every early return below
    # (fable-implement Step 2, issue #584, E-6/R5a interlock): an install whose
    # backend fails on first start (unsupported arch, download/checksum/unpack
    # failure, occupied port, tmux-launch failure, exited during startup, not ready
    # in time) must still receive the one recovery mechanism the design relies on.
    # Both writes are unconditional and idempotent, so a later successful install on
    # a re-run simply overwrites them with the same content.
    #
    # relaunch.sh is a STANDALONE script (not merely inlined in ~/.bash_profile)
    # because it now has three callers, only one of which is a login shell: the
    # ~/.bash_profile guard below, af up's ensureTelemetryBackend (cold start), and
    # the watchdog's periodic tick (internal/cmd/telemetry_backend.go). It carries
    # its own port-occupancy refusal — the guard body alone has no such check, and
    # once this script is invoked far more often than once per login, a foreign
    # process squatting on the port would otherwise turn an occasional futile retry
    # into a persistent fail-loop (concern_blast.md §2.4).
    mkdir -p "$factory_root/.agentfactory/telemetry"
    cat > "$factory_root/.agentfactory/telemetry/relaunch.sh" << EOF
#!/bin/bash
# Written by quickstart.sh's setup_telemetry() (fable-implement Step 2, issue #584).
# Relaunches the OpenObserve telemetry backend when its tmux session is absent.
# Best-effort and idempotent: every exit path is a silent or warned no-op, never a
# failure the caller must handle.
if ! command -v openobserve >/dev/null 2>&1 || [ ! -s "$factory_root/.agentfactory/secrets/telemetry.auth" ]; then
    exit 0
fi
# The data dir is migrated forward by each release that opens it: never start an older binary on it.
oo_marker="$factory_root/.agentfactory/telemetry/openobserve.version"
if [ -s "\$oo_marker" ]; then
    oo_want="\$(head -1 "\$oo_marker")"
    oo_have="\$(openobserve --version 2>/dev/null | awk 'NR==1{print \$2}')"
    if ! printf '%s\n%s\n' "\$oo_want" "\$oo_have" | sort -V -C; then
        echo "warning: openobserve \${oo_have:-unknown} is older than \$oo_want, the build that last opened the telemetry data dir — refusing to relaunch (factory unaffected)" >&2
        exit 0
    fi
fi
if tmux has-session -t telemetry 2>/dev/null; then
    exit 0
fi
if (exec 3<>"/dev/tcp/127.0.0.1/$TELEMETRY_PORT") 2>/dev/null; then
    exec 3>&-
    echo "warning: 127.0.0.1:$TELEMETRY_PORT is occupied by a foreign process — refusing to relaunch (factory unaffected)" >&2
    exit 0
fi
# The flag order below is deliberate: do not normalise it to match the other tmux
# launches in this file. Those are written with the detach flag first; this one is
# written with the session name first. The two orders behave identically to tmux, but
# the install-ordering test in internal/cmd/telemetry_views_test.go anchors the real
# backend launch by taking the FIRST textual match of the detach-first spelling within
# setup_telemetry()'s body — and this line is inside that body, above the real launch.
# Spelling it the same way puts an earlier match ahead of the launch, and the test then
# fails claiming the view files are copied too late — a message about view seeding that
# points nowhere near the edit that caused it.
tmux new-session -s telemetry -d -c "$factory_root" \\
    'ZO_ROOT_USER_EMAIL="$root_email" ZO_ROOT_USER_PASSWORD="\$(cat .agentfactory/secrets/telemetry.root)" ZO_HTTP_ADDR="127.0.0.1" ZO_GRPC_ADDR="127.0.0.1" ZO_HTTP_PORT="$TELEMETRY_PORT" ZO_DATA_DIR="$oo_data" openobserve'
EOF
    chmod 0755 "$factory_root/.agentfactory/telemetry/relaunch.sh"

    local profile="$HOME/.bash_profile"
    local tbegin="# BEGIN agentfactory telemetry login guard"
    local tend="# END agentfactory telemetry login guard"
    touch "$profile"
    sed -i "/$tbegin/,/$tend/d" "$profile" 2>/dev/null || true
    cat >> "$profile" << EOF
$tbegin
[ -x "$factory_root/.agentfactory/telemetry/relaunch.sh" ] && "$factory_root/.agentfactory/telemetry/relaunch.sh"
$tend
EOF

    # The install is _install_openobserve (hoisted, D-22), shared with --check and the
    # converge-if-present pass. A failed FRESH install is already reported and ledgered by
    # _converge, and a binary older than the data dir's marker must never start on it.
    _converge openobserve "$OPENOBSERVE_VERSION" _probe_openobserve _install_openobserve required || true
    { [ -x "$oo_bin" ] && _openobserve_not_older_than_marker; } || return 0
    log_success "OpenObserve present: $("$oo_bin" --version 2>/dev/null | head -1 || echo "pinned $OPENOBSERVE_VERSION")"

    mkdir -p "$secrets_dir"
    chmod 700 "$secrets_dir"

    # Root credential: generated once, reused on every rerun (regenerating would orphan the Basic
    # header already baked into telemetry.auth). Same generate-once idiom as the litellm master key.
    #
    # A stored credential that FAILS the backend's password policy is the one exception, and it
    # has to be, because the policy is enforced at job init: OpenObserve panics and exits 1
    # rather than starting. So a factory holding a non-compliant credential has a backend that
    # has never once come up — which is also what makes replacing it safe, since no root user was
    # ever provisioned from it. Leaving it in place would mean this fix repaired nothing on any
    # factory already installed, and every factory installed before this commit holds one.
    if [ ! -s "$root_pass_file" ]; then
        _generate_telemetry_password > "$root_pass_file"
        log_info "Generated OpenObserve root credential at $root_pass_file"
    elif ! _telemetry_password_is_compliant "$(cat "$root_pass_file")"; then
        _generate_telemetry_password > "$root_pass_file"
        # The derived header is rebuilt from the new credential below. Removing it here rather
        # than rewriting it in place keeps the two files from ever disagreeing: the login guard
        # at the end of this function treats a present telemetry.auth as its precondition.
        rm -f "$auth_file"
        log_warn "Replaced an OpenObserve root credential that could not satisfy the backend's password policy (the backend would exit at startup); re-derived the ingest header"
    fi
    chmod 600 "$root_pass_file"

    # OTLP ingest auth is HTTP Basic. telemetry.auth holds the FULL "Basic base64(email:pass)"
    # header value so telemetry.json can reference it via file: indirection (security.md S1) — the
    # raw credential never lands in telemetry.json. Written only when absent, like the master key.
    if [ ! -s "$auth_file" ]; then
        local root_pass
        root_pass="$(cat "$root_pass_file")"
        printf 'Basic %s' "$(printf '%s:%s' "$root_email" "$root_pass" | base64 | tr -d '\n')" > "$auth_file"
        log_info "Wrote OTLP Basic-auth header to $auth_file"
    fi
    chmod 600 "$auth_file"

    # Seed telemetry.json only when absent — operator edits (e.g. an external OTLP endpoint) survive
    # a re-run. Shape is api.md:116-125: loopback endpoint + pinned port + OpenObserve's OTLP traces
    # path + the file: header ref. There is no enable field here — enablement is the gate file.
    if [ ! -f ".agentfactory/telemetry.json" ]; then
        cat > ".agentfactory/telemetry.json" << EOF
{
  "endpoint": "http://127.0.0.1:$TELEMETRY_PORT/api/default",
  "otlp_http_path_traces": "/v1/traces",
  "headers": { "Authorization": "file:.agentfactory/secrets/telemetry.auth" },
  "protocol": "http/json",
  "export_timeout_ms": 500,
  "resource_attributes_extra": {}
}
EOF
        log_info "Seeded .agentfactory/telemetry.json (installed is not enabled)"
    else
        local migrate_rc=0
        _migrate_telemetry_endpoint ".agentfactory/telemetry.json" "$TELEMETRY_PORT" || migrate_rc=$?
        case "$migrate_rc" in
            0) log_warn "Repaired .agentfactory/telemetry.json: the usage-event address was missing its organisation segment, so per-step token data could never arrive" ;;
            2) log_warn "Could not repair .agentfactory/telemetry.json (left unchanged); set \"endpoint\" to http://127.0.0.1:$TELEMETRY_PORT/api/default and \"otlp_http_path_traces\" to /v1/traces by hand, or per-step token data will not arrive" ;;
        esac
    fi

    # FIRST half of operator decision O-4 (view delivery), recorded rather than self-adjudicated —
    # the full record is internal/cmd/install_telemetry_views/README.md. Delivery is hybrid because
    # neither half stands alone: OpenObserve has no file-based dashboard provisioning (v0.91.3, and
    # v1.1.2, the pinned release, where the dashboards HTTP API is the only create path), so files alone
    # are never consumed; but this function returns early by design on its failure paths (a failed
    # install, occupied port, launch, exit during startup, readiness) and a push-only seed would
    # leave nothing behind on any of them. So the copy runs HERE, before any of those exits,
    # and the push runs later only if the backend actually came up.
    local views_src="$SCRIPT_DIR/internal/cmd/install_telemetry_views"
    local views_dst="$factory_root/.agentfactory/telemetry/views"
    if [ -d "$views_src" ]; then
        mkdir -p "$views_dst"
        local vf vname
        for vf in "$views_src"/*.json; do
            vname="$(basename "$vf")"
            # The view definitions are af-owned and refreshed every run. pricing.json holds the
            # operator's own rates, so an existing copy is never overwritten.
            if [ "$vname" = "pricing.json" ] && [ -f "$views_dst/$vname" ]; then
                continue
            fi
            cp "$vf" "$views_dst/$vname" 2>/dev/null || true
        done
        log_info "Seeded telemetry views to .agentfactory/telemetry/views (edit pricing.json, then re-run to republish)"
    else
        log_warn "Telemetry views not found at install_telemetry_views; skipping view seeding (factory unaffected)"
    fi

    # Detached tmux session, loopback-bound EXPLICITLY (ZO_HTTP_ADDR=127.0.0.1, never the wildcard). The
    # $(cat …) form stays single-quoted so the credential resolves INSIDE the pane shell — never on
    # this script's command line. Idempotent: reuse our session if present; refuse to race a foreign
    # owner of the loopback port (Gap 13).
    if tmux has-session -t telemetry 2>/dev/null; then
        log_info "OpenObserve already running in tmux session 'telemetry'; reusing"
    elif _port_in_use "$TELEMETRY_PORT"; then
        log_warn "127.0.0.1:$TELEMETRY_PORT is already in use by another process — skipping OpenObserve launch (factory unaffected)"
        return 0
    else
        mkdir -p "$oo_data"
        tmux new-session -d -s telemetry -c "$factory_root" \
            'ZO_ROOT_USER_EMAIL="'"$root_email"'" ZO_ROOT_USER_PASSWORD="$(cat .agentfactory/secrets/telemetry.root)" ZO_HTTP_ADDR="127.0.0.1" ZO_GRPC_ADDR="127.0.0.1" ZO_HTTP_PORT="'"$TELEMETRY_PORT"'" ZO_DATA_DIR="'"$oo_data"'" openobserve' \
            || { log_warn "Failed to launch OpenObserve tmux session; skipping telemetry backend (factory unaffected)"; return 0; }
        log_info "Launched OpenObserve in tmux session 'telemetry' (127.0.0.1:$TELEMETRY_PORT)"
    fi

    # Readiness: poll the loopback health endpoint before the smoke. The window lives
    # in _await_telemetry_ready / TELEMETRY_READY_TIMEOUT — see that helper for why
    # OpenObserve's cold start needs more than LiteLLM's 30s. Bounded and non-fatal:
    # a timeout WARNs and returns cleanly (telemetry is optional; factory unaffected).
    #
    # The liveness probe is what separates the two failures that used to look identical. The tmux
    # session exits with its command, so its absence means the backend process is gone — a crash
    # is then reported in about a second, with the cause named, instead of consuming the whole
    # window and being read as a slow cold start. The window itself stays at
    # TELEMETRY_READY_TIMEOUT: a genuine cold start still needs it, and a ceiling that is never
    # reached costs nothing.
    _telemetry_session_alive() { tmux has-session -t telemetry 2>/dev/null; }
    # `|| ready_rc=$?` and not a bare call: this script runs with errexit enabled at the top, so a
    # non-zero return from a bare command aborts immediately and the case below would never be
    # reached — turning an optional component's warning into a failed bootstrap. Assigning through
    # `||` puts the call in a condition context, where errexit is suppressed by design.
    local ready_rc=0
    _await_telemetry_ready "http://127.0.0.1:$TELEMETRY_PORT/healthz" "$TELEMETRY_READY_TIMEOUT" _telemetry_session_alive || ready_rc=$?
    case "$ready_rc" in
        0) : ;;
        2)
            log_warn "OpenObserve exited during startup — the backend is not running (logs: tmux attach -t telemetry, or check the credential in $root_pass_file); factory unaffected"
            return 0
            ;;
        *)
            log_warn "OpenObserve not ready on 127.0.0.1:$TELEMETRY_PORT after ${TELEMETRY_READY_TIMEOUT}s (logs: tmux attach -t telemetry); factory unaffected"
            return 0
            ;;
    esac

    # Install-time smoke on the OTLP traces path: an empty resourceSpans POST proves the ingest
    # endpoint + Basic auth resolve without shipping a real span (the live join is P6b's runbook).
    local auth smoke smoke_logs
    auth="$(cat "$auth_file")"
    smoke="$(curl -sS -o /dev/null -w '%{http_code}' \
        -X POST "http://127.0.0.1:$TELEMETRY_PORT/api/default/v1/traces" \
        -H "Authorization: $auth" -H "content-type: application/json" \
        -d '{"resourceSpans":[]}' 2>/dev/null || true)"
    case "$smoke" in
        2*) log_success "OpenObserve step-timing ingest reachable (HTTP $smoke)" ;;
        *)  log_warn "OpenObserve step-timing smoke returned HTTP ${smoke:-none} (verify live in P6b); factory unaffected" ;;
    esac

    # The check above only ever exercised the path the af binary posts to, which is the one that
    # already carried the organisation segment. The agent sessions post their per-request token
    # usage to a DIFFERENT address, derived by appending /v1/logs to `endpoint`, and that is where
    # the exact per-step token counts live. Probing only the working path is how a 404 on the
    # expensive half survived a green install, so both are checked now.
    smoke_logs="$(curl -sS -o /dev/null -w '%{http_code}' \
        -X POST "http://127.0.0.1:$TELEMETRY_PORT/api/default/v1/logs" \
        -H "Authorization: $auth" -H "content-type: application/json" \
        -d '{"resourceLogs":[]}' 2>/dev/null || true)"
    case "$smoke_logs" in
        2*) log_success "OpenObserve token-usage ingest reachable (HTTP $smoke_logs)" ;;
        404) log_warn "OpenObserve answered 404 for token-usage ingest — per-step token counts will not arrive; check that \"endpoint\" in .agentfactory/telemetry.json ends in /api/default" ;;
        *)  log_warn "OpenObserve token-usage smoke returned HTTP ${smoke_logs:-none} (verify live in P6b); factory unaffected" ;;
    esac

    # SECOND half of operator decision O-4: publish the seeded views as dashboards. The files are
    # already on disk from the copy above, so everything here is best-effort — a backend that
    # rejects a push costs the operator nothing but a hand-import.
    #
    # There is no upsert on this API, so a dashboard whose title is already present is left alone
    # rather than duplicated on every re-run.
    if [ -d "$views_src" ]; then
        local vf dtitle code listing pushed=0
        listing="$(curl -sS -H "Authorization: $auth" \
            "http://127.0.0.1:$TELEMETRY_PORT/api/default/dashboards" 2>/dev/null || true)"
        for vf in "$views_src"/*.json; do
            dtitle="$(jq -r '.dashboard.title // empty' "$vf" 2>/dev/null || true)"
            if [ -z "$dtitle" ]; then
                continue
            fi
            # Compare against the titles the backend reports rather than grepping the whole body,
            # so an error payload that happens to echo a title cannot read as "already published".
            if printf '%s' "$listing" | jq -e --arg t "$dtitle" \
                '[.. | objects | .title? // empty] | index($t)' >/dev/null 2>&1; then
                continue
            fi
            code="$(jq -c '.dashboard' "$vf" 2>/dev/null | curl -sS -o /dev/null -w '%{http_code}' \
                -X POST "http://127.0.0.1:$TELEMETRY_PORT/api/default/dashboards?folder=default" \
                -H "Authorization: $auth" -H "content-type: application/json" \
                --data-binary @- 2>/dev/null || true)"
            case "$code" in
                2*) pushed=$((pushed + 1)) ;;
                *)  log_warn "Dashboard '$dtitle' returned HTTP ${code:-none} — import it by hand from .agentfactory/telemetry/views; factory unaffected" ;;
            esac
        done
        if [ "$pushed" -gt 0 ]; then
            log_success "Published $pushed telemetry dashboard(s) to OpenObserve"
        fi
    fi

    # Login-shell relaunch guard, THIRD of three (webui, litellm, telemetry): written
    # earlier in this function now, not here — fable-implement Step 2 (issue #584,
    # E-6/R5a interlock) moved the relaunch.sh write and this guard's ~/.bash_profile
    # append ahead of the install and every early return above, so an install that fails on
    # first start still receives the recovery mechanism. See the writes near the
    # top of this function, immediately after the local variable declarations.

    # Two-lever summary (plain statement, never a prompt — ADR-014): the backend is INSTALLED, not
    # ENABLED. Runtime export stays off until the operator flips the gate.
    log_success "OpenObserve installed at http://127.0.0.1:$TELEMETRY_PORT (tmux session: telemetry)"
    log_info "Telemetry installed is not enabled — run 'af telemetry on' to start exporting; 'af telemetry off' (or --no-telemetry next run) to keep it off"
}

#------------------------------------------------------------------------------
# Convergence passes: converge-if-present (K17), --check, and the END report (K16)
#------------------------------------------------------------------------------

# _lock_result NAME LOCK K3_RC INSTALL_RC [REPORT]: the row for a hash-locked install that has run.
# When the dry run could not tell (K3_RC 2), REPORT — the real install's pip report — decides it.
_lock_result() {
    local name="$1" label="$2" k3="$3" rc="$4" n
    if [ "$rc" -ne 0 ]; then
        CONVERGE_FAILURES="$CONVERGE_FAILURES$name"$'\n'
        _record_row "$name" "$label" "install failed" FAILED
    elif [ "$k3" -eq 1 ]; then
        log_success "$name: $label (kept)"
        _record_row "$name" "$label" "$label" kept
    elif [ "$k3" -eq 2 ]; then
        n="$(jq '.install | length' "${5-}" 2>/dev/null)" || n=""
        case "$n" in
            0)
                log_success "$name: $label (kept)"
                _record_row "$name" "$label" "$label" kept
                ;;
            ''|*[!0-9]*)
                PYTHON_CHANGED=1
                log_warn "$name: $label installed, but pip's install report is unreadable — counted as changed"
                _record_row "$name" "$label" "unknown (no install report)" converged
                ;;
            *)
                PYTHON_CHANGED=1
                log_success "$name: $label (converged)"
                _record_row "$name" "$label" "$label" converged
                ;;
        esac
    else
        PYTHON_CHANGED=1
        log_success "$name: $label (converged)"
        _record_row "$name" "$label" "$label" converged
    fi
}

# _lock_drift NAME LOCK K3_RC COUNT: the --check row for a lock, read from pip's dry run alone.
_lock_drift() {
    local name="$1" label="$2" k3="$3" n="$4"
    case "$k3" in
        1)
            log_success "$name: $label (kept)"
            _record_row "$name" "$label" "$label" kept
            ;;
        0)
            log_warn "$name: $n would change != pinned $label (would converge)"
            _record_row "$name" "$label" "$n would change" drift
            ;;
        *)
            log_warn "$name: unknown (pip dry-run failed) != pinned $label (would converge)"
            _record_row "$name" "$label" "unknown (pip dry-run failed)" drift
            ;;
    esac
}

# converge_present_components (K17): components that exist converge on every run whatever the
# mode flags say — the flags decide only whether an ABSENT component is installed. cwd is the
# factory root (configure_factory). Every outcome goes to a row and the ledger; this returns 0.
converge_present_components() {
    log_step "Converging installed components (Python locks, Codex CLI, OpenObserve)"
    local py_rc=0 lite_rc=1 stopped=0 tele_up=0 rc report

    _pip_would_change "$SCRIPT_DIR/py/requirements.txt" >/dev/null || py_rc=$?
    if command_exists litellm; then
        lite_rc=0
        _pip_would_change "$SCRIPT_DIR/py/litellm-requirements.txt" >/dev/null || lite_rc=$?
    fi
    # The gateway runs on these packages and on the system libraries the root layer may have just
    # replaced: it is restarted on what changed, never on every run (D20).
    if { [ "$py_rc" -ne 1 ] || [ "$lite_rc" -ne 1 ] || [ "$APT_CHANGED" = 1 ]; } && _litellm_session_running; then
        _gateway_stop
        stopped=1
    fi

    # A dry run that could not tell (2) takes its row from what the user-site install changed; the
    # system-site install never decides it.
    rc=0
    report=""
    if [ "$py_rc" -eq 2 ]; then
        report="$(mktemp)" || report=""
    fi
    _pip_install_locked "$SCRIPT_DIR/py/requirements.txt" ${report:+--report "$report"} || rc=1
    if [ "$(id -u)" = 0 ] || { command_exists sudo && sudo -n true 2>/dev/null; }; then
        _pip_install_locked "$SCRIPT_DIR/py/requirements.txt" --system || rc=1
    else
        log_warn "python-lock: no root, so the system-site copy of py/requirements.txt cannot converge (the root layer reports why)"
    fi
    _lock_result python-lock py/requirements.txt "$py_rc" "$rc" "$report"
    rm -f "$report"

    if command_exists litellm; then
        rc=0
        report=""
        if [ "$lite_rc" -eq 2 ]; then
            report="$(mktemp)" || report=""
        fi
        _pip_install_locked "$SCRIPT_DIR/py/litellm-requirements.txt" ${report:+--report "$report"} || rc=1
        _lock_result litellm-lock py/litellm-requirements.txt "$lite_rc" "$rc" "$report"
        rm -f "$report"
    fi

    # Under --litellm, setup_litellm reconciles the gateway itself later in this run.
    if [ "$stopped" = 1 ] && [ "$WITH_LITELLM" != true ] && [ -x .agentfactory/gateway-relaunch.sh ]; then
        log_info "Relaunching the LiteLLM gateway on the converged packages"
        .agentfactory/gateway-relaunch.sh || log_warn "gateway-relaunch.sh did not restart the gateway; see USING_LITELLM.md"
    fi

    if command_exists codex; then
        _ensure_codex_cli || true
    fi

    if [ -x "$OPENOBSERVE_BIN" ]; then
        if tmux list-panes -t telemetry >/dev/null 2>&1; then
            tele_up=1
        fi
        _converge openobserve "$OPENOBSERVE_VERSION" _probe_openobserve _install_openobserve required || true
        if [ "$tele_up" = 1 ] && [ "$APT_CHANGED" = 1 ] && tmux list-panes -t telemetry >/dev/null 2>&1; then
            _telemetry_stop
        fi
        # --no-telemetry never leaves a backend stopped that was running before this pass (D-47);
        # with telemetry on, setup_telemetry relaunches it.
        if [ "$tele_up" = 1 ] && [ "$WITH_TELEMETRY" != true ] && [ -x .agentfactory/telemetry/relaunch.sh ] \
            && ! tmux list-panes -t telemetry >/dev/null 2>&1; then
            .agentfactory/telemetry/relaunch.sh || log_warn "telemetry relaunch.sh did not restart OpenObserve"
        fi
    fi
    return 0
}

# _restart_rebuilt_webui: a running console keeps its old build until it is restarted; the phase-5
# launch guard below then starts the rebuilt binary.
_restart_rebuilt_webui() {
    [ "$WEBUI_REBUILT" = 1 ] || return 0
    # Under AF_SRC the web console itself is running this bootstrap (Generate All); stopping it
    # would kill the job mid-run.
    [ -z "${AF_SRC:-}" ] || return 0
    local rendezvous="${AF_ROOT:-$PWD}/.runtime/webui_server.json" pid exe waited=0
    [ -f "$rendezvous" ] || return 0
    pid="$(jq -r '.pid // empty' "$rendezvous" 2>/dev/null)" || pid=""
    [ -n "$pid" ] || return 0
    exe="$(readlink "/proc/$pid/exe" 2>/dev/null)" || exe=""
    case "$exe" in
        "$HOME/.local/bin/webui"|"$HOME/.local/bin/webui (deleted)") ;;
        *) return 0 ;;
    esac
    log_info "Restarting the web console (pid $pid) on the rebuilt binary"
    kill -TERM "$pid" 2>/dev/null || return 0
    while kill -0 "$pid" 2>/dev/null && [ "$waited" -lt 10 ]; do
        sleep 1
        waited=$((waited + 1))
    done
    rm -f "$rendezvous"
    return 0
}

# _factory_repo_dir: the factory root configure_factory selects (the first git repo under the
# workspace), for a --check run that never reaches configure_factory.
_factory_repo_dir() {
    local d
    for d in "$WORKSPACE_DIR"/*/; do
        if [ -d "$d/.git" ]; then
            printf '%s\n' "${d%/}"
            return 0
        fi
    done
    return 1
}

# _converge_check (--check): the same wrappers and probes a real run uses, each returning right after
# its _converge line, so the report is about the real probes and nothing mutating is reachable.
_converge_check() {
    local checks_rc=0 rc n
    run_all_checks || checks_rc=$?
    if [ "$checks_rc" -ne 0 ]; then
        CONVERGE_FAILURES="${CONVERGE_FAILURES}prerequisites"$'\n'
    fi
    _apt_converge
    # errexit is off inside this group on purpose: one wrapper's refusal (no go, not the source
    # tree) must not stop the rows the others report.
    {
        install_af
        install_gh
        install_claude
        install_playwright
        if command_exists codex; then
            _ensure_codex_cli
        fi
    } || true
    if [ -x "$OPENOBSERVE_BIN" ]; then
        _converge openobserve "$OPENOBSERVE_VERSION" _probe_openobserve _install_openobserve required || true
    fi
    rc=0
    n="$(_pip_would_change "$SCRIPT_DIR/py/requirements.txt")" || rc=$?
    _lock_drift python-lock py/requirements.txt "$rc" "$n"
    if command_exists litellm; then
        rc=0
        n="$(_pip_would_change "$SCRIPT_DIR/py/litellm-requirements.txt")" || rc=$?
        _lock_drift litellm-lock py/litellm-requirements.txt "$rc" "$n"
    fi
    _converge_report
}

# _converge_report (END, D-26): the summary table, the convergence record, the root-layer message,
# then the run's exit status. It is the only place a failed component turns into a non-zero exit.
_converge_report() {
    local tab name pin installed integrity result at drift=0 failed=0 root record
    tab="$(printf '\t')"
    echo ""
    log_step "Convergence summary"
    printf '  %-16s %-30s %-28s %-30s %s\n' component installed pinned integrity action
    while IFS="$tab" read -r name pin installed integrity result at; do
        [ -n "$name" ] || continue
        printf '  %-16s %-30s %-28s %-30s %s\n' "$name" "$installed" "$pin" "$integrity" "$result"
        case "$result" in
            drift) drift=1 ;;
            FAILED) failed=1 ;;
        esac
    done <<EOF
$CONVERGE_ROWS
EOF

    root="${AF_ROOT:-}"
    if [ -z "$root" ]; then
        root="$(_factory_repo_dir)" || root=""
    fi
    if [ -z "$root" ]; then
        log_warn "no factory root (AF_ROOT is unset and no git repository is under $WORKSPACE_DIR); convergence.json not written"
    else
        record="$root/.runtime/bootstrap/convergence.json"
        if mkdir -p "$root/.runtime/bootstrap" \
            && jq -n --arg run_at "${RUN_AT:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}" \
                --argjson litellm "$(if [ "$WITH_LITELLM" = true ]; then echo true; else echo false; fi)" \
                --argjson check_only "$(if [ "$CHECK_ONLY" = true ]; then echo true; else echo false; fi)" \
                --argjson apt_changed "$(if [ "$APT_CHANGED" = 1 ]; then echo true; else echo false; fi)" \
                --argjson python_changed "$(if [ "$PYTHON_CHANGED" = 1 ]; then echo true; else echo false; fi)" \
                --arg rows "$CONVERGE_ROWS" \
                '{run_at: $run_at, mode: {litellm: $litellm, check_only: $check_only},
                  components: [$rows | split("\n")[] | select(length > 0) | split("\t")
                    | {name: .[0], pin: .[1], installed: .[2], integrity: .[3], result: .[4], at: .[5]}],
                  apt_changed: $apt_changed, python_changed: $python_changed}' >"$record.tmp" \
            && mv -f "$record.tmp" "$record"; then
            log_info "Convergence record: $record"
        else
            log_warn "could not write $record"
        fi
    fi

    if [ "$CHECK_ONLY" = true ]; then
        if [ "$drift" = 1 ] || [ "$failed" = 1 ] || [ -n "$CONVERGE_FAILURES" ]; then
            log_warn "drift found: the next ./quickstart.sh or af install --agents converges the rows above"
            exit 1
        fi
        log_success "every component matches its pin"
        return 0
    fi

    case "$CONVERGE_FAILURES" in
        *root-layer*)
            log_error "security updates cannot be applied: no root in this container (id -u = $(id -u); sudo -n true failed). Ubuntu $(_os_codename)-security updates and the Node.js pin did not converge; everything else did (see the table above). agentfactory containers give dev passwordless sudo (Dockerfile:55-57); this container lacks it."
            ;;
        *)
            if [ -n "$NODE_APT_ERROR" ]; then
                log_error "cannot pin nodejs=$NODE_APT_VERSION: apt said \"$NODE_APT_ERROR\". A customer image may lack /etc/apt/sources.list.d/nodesource.list (Dockerfile:47-52 show the signed form); a pin that no longer resolves needs NODE_APT_VERSION bumped — a stale pin also fails B2."
            fi
            ;;
    esac
    if [ "$APT_CHANGED" = 1 ] || [ "$PYTHON_CHANGED" = 1 ]; then
        log_warn "python packages or system libraries changed: run 'af down && af up' so sessions pick them up"
    fi
    if [ -n "$CONVERGE_FAILURES" ]; then
        log_error "convergence failed for: $(printf '%s' "$CONVERGE_FAILURES" | tr '\n' ' ')(see the table above)"
        exit 1
    fi
    return 0
}

main() {
    parse_args "$@"

    if [ -n "$LITELLM_AUTH_MODE" ] && [ "$WITH_LITELLM" != true ]; then
        log_error "--litellm-auth requires --litellm"
        exit 1
    fi

    echo ""
    echo "=========================================="
    echo "  Agentfactory Quickstart"
    echo "=========================================="
    echo ""

    # Ensure PATH includes common tool locations
    export PATH="$HOME/.local/bin:$HOME/go/bin:/usr/local/go/bin:$PATH"

    RUN_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

    # Check-only mode: a read-only drift report over the same probes a real run uses (D-68).
    if [ "$CHECK_ONLY" = true ]; then
        _converge_check
        exit 0
    fi

    # Phase 1: Check prerequisites (base image tools)
    log_step "Phase 1: Checking prerequisites"

    # The root layer first: the Ubuntu security pocket and the Node.js pin (K5).
    _apt_converge
    _pin_managed_autoupdater /etc/claude-code/managed-settings.d

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
    install_gh
    install_claude
    install_playwright
    _apt_security_upgrade

    # Phase 3: Configure
    log_step "Phase 3: Configuring workspace"

    configure_git_defaults
    configure_shell
    configure_factory
    converge_present_components
    _restart_rebuilt_webui
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

    # Opt-in LiteLLM gateway — LAST, after the normal bootstrap is fully done, so
    # it behaves exactly like running USING_LITELLM.md's steps by hand post-setup.
    if [ "$WITH_LITELLM" = true ]; then
        setup_litellm
    fi

    # Telemetry backend, default-on (O-1 opt-out via --no-telemetry). Its binary converge is a
    # required row, so it runs before the report that judges the run.
    if [ "$WITH_TELEMETRY" = true ]; then
        setup_telemetry
    fi

    # The END (D-26): the convergence summary, the record and the exit status, after every setup
    # step so none can print below them. A failed required row exits 1 here.
    _converge_report

    # Done! Printed only once the report passed (K16; K15/D25, issue #693 Phase 4): a run that dies
    # inside setup_litellm, or ends with a failed required row, never announces completion.
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
