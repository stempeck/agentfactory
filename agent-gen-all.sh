#!/usr/bin/env bash
set -euo pipefail

#
# agent-gen-all.sh — Regenerate all formula-derived agent templates and rebuild af
#
# Run from a project directory that has .beads/formulas/*.formula.toml files.
# The agentfactory source repo is auto-detected from this script's location.
#
# Usage:
#   ~/af/agentfactory/agent-gen-all.sh              # regenerate all formulas
#   ~/af/agentfactory/agent-gen-all.sh --no-build   # regenerate + sync, skip rebuild
#   AF_SRC=/path/to/af agent-gen-all.sh             # override af source location
#
# NOTE: Run from the main repo checkout, not a worktree. Worktrees do not share
# .beads/formulas/ and this script syncs from $AF_SRC which defaults to the
# script's directory (the main repo root).

AF_SRC="${AF_SRC:-$(cd "$(dirname "$0")" && pwd)}"
FORMULA_DIR=".beads/formulas"
DO_BUILD=true

for arg in "$@"; do
    case "$arg" in
        --no-build) DO_BUILD=false ;;
        --help|-h)
            sed -n '3,/^$/{ s/^# \?//; p }' "$0"
            exit 0
            ;;
        *)
            echo "unknown option: $arg" >&2
            exit 1
            ;;
    esac
done

# --- Validate environment ---------------------------------------------------

if [ ! -d "$FORMULA_DIR" ]; then
    echo "error: $FORMULA_DIR not found — run from a project root with formulas installed" >&2
    exit 1
fi

if [ ! -f "$AF_SRC/go.mod" ] || ! grep -q agentfactory "$AF_SRC/go.mod" 2>/dev/null; then
    echo "error: AF_SRC=$AF_SRC is not the agentfactory source repo" >&2
    exit 1
fi

PROJECT="$(pwd)"
echo "project:   $PROJECT"
echo "af source: $AF_SRC"
echo ""

# --- Stop running agents (--delete refuses while tmux sessions are live) -----

echo "stopping agents..."
af down --all 2>/dev/null || true

# --- Sync formulas from source -----------------------------------------------

echo "syncing formulas from source..."
updated=0
is_source_repo=false
if [ "$PROJECT" = "$AF_SRC" ] || { [ -f "$PROJECT/go.mod" ] && grep -q agentfactory "$PROJECT/go.mod" 2>/dev/null; }; then
    is_source_repo=true
fi
for f in "$AF_SRC"/internal/cmd/install_formulas/*.formula.toml; do
    [ -f "$f" ] || continue
    name=$(basename "$f")
    dest="$FORMULA_DIR/$name"
    if [ ! -f "$dest" ] || [ "$f" -nt "$dest" ]; then
        cp "$f" "$dest"
        echo "  updated: $name"
        updated=$((updated + 1))
    fi
done
if [ "$is_source_repo" = true ]; then
    for f in "$FORMULA_DIR"/*.formula.toml; do
        [ -f "$f" ] || continue
        name=$(basename "$f")
        if [ ! -f "$AF_SRC/internal/cmd/install_formulas/$name" ]; then
            echo "WARNING: removing local formula not in source tree: $name"
            echo "  (To preserve, promote it: cp $FORMULA_DIR/$name $AF_SRC/internal/cmd/install_formulas/)"
            rm "$f"
            echo "  removed orphan: $name"
            updated=$((updated + 1))
        fi
    done
else
    customer_count=0
    for f in "$FORMULA_DIR"/*.formula.toml; do
        [ -f "$f" ] || continue
        name=$(basename "$f")
        if [ ! -f "$AF_SRC/internal/cmd/install_formulas/$name" ]; then
            customer_count=$((customer_count + 1))
        fi
    done
    if [ "$customer_count" -gt 0 ]; then
        echo "  preserving $customer_count customer formula(s)"
    fi
fi
if [ "$is_source_repo" = true ]; then
    # Remove orphan role templates (no corresponding formula)
    for tmpl_file in "$AF_SRC/internal/templates/roles/"*.md.tmpl; do
        [ -f "$tmpl_file" ] || continue
        tmpl_name="$(basename "$tmpl_file" .md.tmpl)"
        # Skip built-in templates
        case "$tmpl_name" in manager|supervisor) continue ;; esac
        if [ ! -f "$AF_SRC/internal/cmd/install_formulas/${tmpl_name}.formula.toml" ]; then
            echo "WARNING: removing orphan template: $tmpl_file (no matching formula)"
            rm "$tmpl_file"
        fi
    done
fi
if [ "$updated" -eq 0 ]; then
    echo "  formulas already current"
fi
echo ""

# --- Regenerate each formula -------------------------------------------------
# For each formula in .beads/formulas/, delete the existing agent (which removes
# its template, config entry, and workspace via `af formula agent-gen --delete`)
# and regenerate. We never touch internal/templates/roles/ directly — manager
# and supervisor are builtin roles without formulas and must be left alone.

count=0
failed=()

for f in "$FORMULA_DIR"/*.formula.toml; do
    [ -f "$f" ] || continue
    name="$(basename "$f" .formula.toml)"

    echo ""
    echo "[$name]"

    # Delete existing agent artifacts (fails gracefully if agent doesn't exist)
    if ! af formula agent-gen "$name" --delete --af-src "$AF_SRC" 2>&1; then
        echo "  (no existing agent to delete)"
    fi

    # Generate fresh
    if af formula agent-gen "$name" --af-src "$AF_SRC"; then
        count=$((count + 1))
    else
        echo "  FAILED: $name" >&2
        failed+=("$name")
    fi
done

# --- Rebuild af --------------------------------------------------------------

if [ "$DO_BUILD" = true ]; then
    echo ""
    echo "rebuilding af..."
    make -C "$AF_SRC" install
    echo ""
    echo "af installed: $(af version 2>/dev/null | head -1 || echo '(unknown version)')"
fi

# --- Summary -----------------------------------------------------------------

echo ""
if [ ${#failed[@]} -gt 0 ]; then
    echo "done — $count agents regenerated, ${#failed[@]} failed: ${failed[*]}"
    exit 1
else
    echo "done — $count agents regenerated"
fi
