#!/usr/bin/env bash
set -euo pipefail

#
# agent-gen-all.sh — Regenerate all formula-derived agent templates and rebuild af
#
# Run from a project directory that has .agentfactory/store/formulas/*.formula.toml files.
# The agentfactory source repo is auto-detected from this script's location.
#
# Usage:
#   ~/af/agentfactory/agent-gen-all.sh              # regenerate all formulas
#   ~/af/agentfactory/agent-gen-all.sh --no-build   # regenerate + sync, skip rebuild
#   AF_SRC=/path/to/af agent-gen-all.sh             # override af source location
#
# NOTE: Run from the main repo checkout, not a worktree. Worktrees do not share
# .agentfactory/store/formulas/ and this script syncs from $AF_SRC which defaults
# to the script's directory (the main repo root).

AF_SRC="${AF_SRC:-$(cd "$(dirname "$0")" && pwd)}"
FORMULA_DIR=".agentfactory/store/formulas"
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
# K16 (#541): deliver the AC-6 guidance in-band — the 2>/dev/null on the next line
# masks the refusal text when this script runs inside an agent session.
echo "note: if 'af down --all' below is refused, that is expected — factory-wide teardown is an operator action; skip it and continue."
af down --all 2>/dev/null || true

# --- Sync formulas from source -----------------------------------------------

echo "syncing formulas from source..."
updated=0

# K10 (issue #538): plugins.json records the formulas staged from installed plugin repos.
# A staged plugin formula/template has no internal/cmd/install_formulas/ counterpart, so the
# source-repo orphan passes below would delete it on the next redeploy. Derive the manifest
# path from FORMULA_DIR (== <root>/.agentfactory/plugins.json, matching
# config.PluginsConfigPath) so the same code is correct at runtime AND drivable by the
# hermetic sync test, whose cwd is not the factory root.
plugins_manifest="$(dirname "$(dirname "$FORMULA_DIR")")/plugins.json"

# plugin_owner_of_stem <bare-stem> — if plugins.json records a formula whose bare stem
# matches, set PLUGIN_OWNER to the owning plugin name and return 0; else return 1. Mirrors
# config.PluginsConfig.OwnsAgent (internal/config/plugins.go), including its sorted-first
# owner when several plugins record a stem: callers strip .formula.toml
# off the query stem (XR-5) and the jq compare strips it off each stored key too, so a match
# holds whether K7 recorded keys as "<stem>" or "<stem>.formula.toml".
#
# An absent manifest returns 1, so the orphan passes stay byte-identical to today with no
# plugins.json (AC-6). A PRESENT manifest that LoadPluginsConfig would reject must never read
# as "zero plugins": that would reap exactly the artifacts the manifest protects. Such a
# manifest preserves every orphan candidate instead, with one WARNING per run (ADR-017: when
# in doubt, don't delete). The jq shape check mirrors the Go decode
# (TestPluginManifestShapeParity pins the two together), and plugins_schema_version mirrors
# config.CurrentPluginsVersion so a newer schema, whose ownership fields this script cannot
# know, is refused too. Each exit status is
# captured with `|| rc=$?` and no pipeline is used, so neither `set -e` nor a SIGPIPE under
# pipefail can turn a jq failure into a silent non-match. Without jq, a grep for either
# key form preserves conservatively.
plugins_manifest_state=""
plugins_schema_version=2

plugins_manifest_unreadable() {
    if [ "$plugins_manifest_state" != unreadable ]; then
        plugins_manifest_state=unreadable
        echo "WARNING: $plugins_manifest is present but unreadable or invalid (exit $1; want the shape af accepts: a JSON object with an object-valued \"plugins\" and schema version 1..$plugins_schema_version) — preserving every orphan formula and template this run; fix or restore the file"
    fi
    PLUGIN_OWNER="(unknown: plugins.json unreadable)"
}

plugin_owner_of_stem() {
    local stem="$1" owners="" rc=0
    [ -e "$plugins_manifest" ] || return 1
    if [ -z "$plugins_manifest_state" ]; then
        if command -v jq >/dev/null 2>&1; then
            jq -e --argjson max "$plugins_schema_version" '
                def str_or_null: . == null or type == "string";
                type == "object"
                and (.version | . == null or (type == "number" and . == floor and . >= 1 and . <= $max))
                and (.plugins | type == "object")
                and all(.plugins[]; . == null or (type == "object"
                    and all(.source, .commit, .installed_at; str_or_null)
                    and (.formulas | . == null or type == "object")
                    and (.integration | . == null or type == "object")
                    and all((.formulas // {})[]; . == null or (type == "object" and (.sha256 | str_or_null)))))
            ' "$plugins_manifest" >/dev/null || rc=$?
            if [ "$rc" -eq 0 ]; then
                plugins_manifest_state=valid
            else
                plugins_manifest_unreadable "$rc"
            fi
        else
            plugins_manifest_state=no-jq
        fi
    fi
    case "$plugins_manifest_state" in
        unreadable)
            PLUGIN_OWNER="(unknown: plugins.json unreadable)"
            return 0
            ;;
        no-jq)
            grep -qF -e "\"$stem\"" -e "\"$stem.formula.toml\"" "$plugins_manifest" || rc=$?
            case "$rc" in
                0) PLUGIN_OWNER="(unknown; jq unavailable)"; return 0 ;;
                1) return 1 ;;
                *) plugins_manifest_unreadable "$rc"; return 0 ;;
            esac
            ;;
    esac
    owners="$(jq -r --arg s "$stem" \
        '.plugins | to_entries | sort_by(.key)[] | select([(.value.formulas // {}) | keys[] | rtrimstr(".formula.toml")] | index($s)) | .key' \
        "$plugins_manifest")" || rc=$?
    if [ "$rc" -ne 0 ]; then
        plugins_manifest_unreadable "$rc"
        return 0
    fi
    [ -n "$owners" ] || return 1
    PLUGIN_OWNER="${owners%%$'\n'*}"
    return 0
}

is_source_repo=false
# Only treat as source repo when PROJECT is literally the same directory as AF_SRC.
# The go.mod heuristic was too broad — it matched any agentfactory fork/checkout,
# causing customer-created formulas and templates to be deleted as "orphans."
proj_real="$(cd "$PROJECT" && pwd -P)"
afsrc_real="$(cd "$AF_SRC" && pwd -P)"
if [ "$proj_real" = "$afsrc_real" ]; then
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
            # K10: preserve a plugin-owned formula recorded in plugins.json before treating
            # it as a deletable orphan (XR-5: this pass keys on $name WITH .formula.toml, so
            # strip it to the bare stem the manifest keys on).
            if plugin_owner_of_stem "${name%.formula.toml}"; then
                echo "preserving plugin formula: $name (installed by plugin $PLUGIN_OWNER)"
                continue
            fi
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
            # K10: preserve a plugin-owned role template recorded in plugins.json before
            # deleting it (XR-5: tmpl_name is already the bare stem the manifest keys on).
            if plugin_owner_of_stem "$tmpl_name"; then
                echo "preserving plugin template: $tmpl_name (installed by plugin $PLUGIN_OWNER)"
                continue
            fi
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
# For each formula in .agentfactory/store/formulas/, regenerate the agent in place
# (config entry, template, workspace) via `af formula agent-gen`. This loop's
# domain is always a formula file that still exists, so there is never an orphan
# to clean up here — a prior delete-then-regenerate here only ever destroyed and
# immediately recreated a still-valid agent, wiping operator-owned agents.json
# fields (continuous_improvement, model, sparse_paths, base_url, auth_token) on
# every redeploy (issue #527). `af formula agent-gen` already preserves those
# fields on regeneration; deliberate agent removal remains available via a
# standalone `af formula agent-gen <name> --delete`, unaffected by this loop. We
# never touch internal/templates/roles/ directly — manager and supervisor are
# builtin roles without formulas and must be left alone.

count=0
failed=()

for f in "$FORMULA_DIR"/*.formula.toml; do
    [ -f "$f" ] || continue
    name="$(basename "$f" .formula.toml)"

    echo ""
    echo "[$name]"

    # Regenerate in place — preserves operator-owned fields via the existing
    # agents.json merge.
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
