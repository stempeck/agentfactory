#!/usr/bin/env bash
# Installs the committed af/package-lock.json into an af-owned prefix: the playwright CLI and the
# Playwright MCP server, each on its own locked core, both downloading chromium revision 1243. A global
# playwright on PATH is never adopted: npm ci over the lock, not a registry spec, decides what installs.
set -u

prefix="$HOME/.local/share/agentfactory/playwright/1.63.0"
pinned_version="1.63.0"
chromium_dir="$HOME/.cache/ms-playwright/chromium-1243"
shipped_dir="$(cd "$(dirname "$0")" && pwd)"
pw="$prefix/node_modules/.bin/playwright"

log_info() { echo "[INFO] $1"; }
log_success() { echo "[OK] $1"; }
log_error() { echo "[ERROR] $1"; }
command_exists() { command -v "$1" >/dev/null 2>&1; }
installed_version() { [ -x "$pw" ] && "$pw" --version 2>/dev/null | awk '{print $2}'; }
passwordless_sudo() { command_exists sudo && sudo -n true 2>/dev/null; }
prefix_holds_shipped_lock() {
    cmp -s "$shipped_dir/package.json" "$prefix/package.json" &&
        cmp -s "$shipped_dir/package-lock.json" "$prefix/package-lock.json"
}

if [ "$(installed_version)" = "$pinned_version" ] && prefix_holds_shipped_lock && [ -d "$chromium_dir" ]; then
    log_success "playwright $pinned_version with its chromium build is already installed in $prefix"
    exit 0
fi

if ! command_exists npm; then
    log_error "npm not found; Node.js is required to install playwright"
    exit 1
fi

# npm ci refuses (EUSAGE) a prefix that holds no package.json and package-lock.json.
# --ignore-scripts is safe: no package in the lock declares an install script.
mkdir -p "$prefix" || { log_error "cannot create $prefix"; exit 1; }
cp "$shipped_dir/package.json" "$shipped_dir/package-lock.json" "$prefix/" || { log_error "cannot copy the lock into $prefix"; exit 1; }
log_info "Installing the locked playwright CLI and MCP server into $prefix"
npm ci --ignore-scripts --prefix "$prefix" || { log_error "npm ci of the playwright lock failed"; exit 1; }

if [ "$(installed_version)" != "$pinned_version" ]; then
    log_error "$pw reports '$(installed_version)' after the install, not $pinned_version"
    exit 1
fi

# --with-deps apt-installs chromium's shared libraries and needs passwordless sudo.
log_info "Downloading chromium for playwright $pinned_version"
if passwordless_sudo; then
    "$pw" install --with-deps chromium || { log_error "chromium download or system deps failed"; exit 1; }
else
    "$pw" install chromium || { log_error "chromium download failed"; exit 1; }
fi

# Calibrate by rendering, not by version string: an installed but unrenderable browser is a failure.
probe_dir="$(mktemp -d "${TMPDIR:-$HOME}/af-playwright-install.XXXXXX")" || { log_error "cannot create a scratch dir"; exit 1; }
trap 'rm -rf "$probe_dir"' EXIT
if "$pw" screenshot "data:text/html,<h1>af</h1>" "$probe_dir/probe.png" >/dev/null 2>&1 && [ -s "$probe_dir/probe.png" ]; then
    log_success "playwright $pinned_version renders headlessly"
else
    log_error "playwright installed, but chromium did not render the probe page"
    exit 1
fi
