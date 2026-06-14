#!/bin/bash
# validate.sh — post-output validation for the architecture-docs skill.
#
# Mechanically checks that the produced corpus meets the skill's stated
# success criteria. Called from Phase 2.5 (after subsystem dispatch) and
# Phase 9 (final verification). Exit 0 = pass; exit non-zero = fail and
# downstream phases must not proceed.
#
# Usage: validate.sh [repo-root]
# Defaults to $(git rev-parse --show-toplevel).

set -u

ROOT="${1:-$(git rev-parse --show-toplevel)}"
DOCS="$ROOT/docs/architecture"
INVENTORY="$ROOT/todos/architecture-docs/inventory.md"

errors=0

echo "=== architecture-docs validation ==="
echo "Repo root: $ROOT"
echo ""

# ──────────────────────────────────────────────────────────────────────
# Check 1: Subsystem coverage — every subsystem path named in
# inventory.md is "covered" by some subsystems/<name>.md file. Coverage
# is declared by a machine-readable header in each subsystem file:
#
#   **Covers:** internal/worktree, internal/lock, internal/checkpoint
#
# One file may cover multiple paths (grouping is a valid design choice;
# e.g. fs-primitives.md covers four packages). The check unions every
# Covers: declaration and compares against the inventory.
# ──────────────────────────────────────────────────────────────────────
echo "[1/5] Subsystem coverage"
if [ -f "$INVENTORY" ]; then
    raw_candidates=$(grep -oE '(internal|py)/[a-z_-]+|^[[:space:]]*hooks/?$|`hooks`|hooks \(' "$INVENTORY" 2>/dev/null \
        | grep -oE '(internal|py)/[a-z_-]+|hooks' \
        | sort -u)
    # Filter to paths that actually exist as directories — prevents
    # non-subsystem references like `py/requirements` (a .txt file) from
    # being treated as missing subsystems.
    inv_paths=""
    while IFS= read -r p; do
        [ -z "$p" ] && continue
        if [ "$p" = "hooks" ] && [ -d "$ROOT/hooks" ]; then
            inv_paths="$inv_paths"$'\n'"$p"
        elif [ -d "$ROOT/$p" ]; then
            inv_paths="$inv_paths"$'\n'"$p"
        fi
    done <<< "$raw_candidates"
    inv_paths=$(echo "$inv_paths" | grep -v '^$' | sort -u)
    inv_count=$(echo "$inv_paths" | grep -c . || true)
    inv_count=${inv_count:-0}

    covered_paths=""
    missing_covers=""
    for f in "$DOCS/subsystems/"*.md; do
        [ -f "$f" ] || continue
        covers_line=$(grep -m1 '^\*\*Covers:\*\*' "$f" 2>/dev/null || true)
        if [ -z "$covers_line" ]; then
            missing_covers="$missing_covers $(basename "$f")"
            continue
        fi
        paths=$(echo "$covers_line" \
            | sed 's/^\*\*Covers:\*\*//' \
            | grep -oE '(internal|py)/[a-z_-]+|hooks')
        covered_paths="$covered_paths $paths"
    done
    covered_paths=$(echo "$covered_paths" | tr ' ' '\n' | grep -v '^$' | sort -u)
    covered_count=$(echo "$covered_paths" | grep -c . || true)
    covered_count=${covered_count:-0}

    echo "  Inventory subsystem paths: $inv_count"
    echo "  Covered by subsystem files: $covered_count"

    if [ -n "$missing_covers" ]; then
        echo "  FAIL: subsystem file(s) missing **Covers:** header:$missing_covers"
        errors=$((errors+1))
    fi

    uncovered=$(comm -23 <(echo "$inv_paths") <(echo "$covered_paths"))
    if [ -n "$uncovered" ]; then
        echo "  FAIL: inventory paths not covered by any subsystem file:"
        echo "$uncovered" | while read -r p; do [ -n "$p" ] && echo "    - $p"; done
        errors=$((errors+1))
    fi

    spurious=$(comm -13 <(echo "$inv_paths") <(echo "$covered_paths"))
    if [ -n "$spurious" ]; then
        echo "  WARN: subsystem files declare paths not in inventory:"
        echo "$spurious" | while read -r p; do [ -n "$p" ] && echo "    - $p"; done
    fi

    if [ -z "$missing_covers" ] && [ -z "$uncovered" ]; then
        echo "  PASS"
    fi
else
    echo "  SKIP (no inventory.md — Phase 1 not yet run)"
fi
echo ""

# ──────────────────────────────────────────────────────────────────────
# Check 2: Citation density — claim-bearing paragraphs must anchor.
#
# The corpus has two kinds of prose:
#   - CLAIMS: assertions about what the code does, what commits
#     changed, what an invariant is. These must anchor to file:line,
#     a commit SHA, or "unknown — needs review".
#   - NAVIGATION / INTERPRETATION: index text, how-to instructions,
#     ADR consequences (interpretation of tradeoffs), corpus links
#     sections. These intentionally do not anchor.
#
# The scan applies only to claim-bearing sections. It:
#   - skips README.md entirely (index/navigation)
#   - within ADRs, skips Consequences / Corpus links / Scope sections
#     (anchors live in Context and Decision)
#   - within subsystem files and overview files, skips Shape /
#     Rationale intro sections and "How to ..." sections
#
# A paragraph is a block of non-blank lines not interrupted by a
# heading, table row, or list bullet. Paragraphs > 400 chars without
# any anchor in a claim-bearing section fail.
# ──────────────────────────────────────────────────────────────────────
echo "[2/5] Citation density (claim-bearing prose without anchors)"
unanchored=0
for f in "$DOCS"/*.md "$DOCS/subsystems/"*.md "$DOCS/adrs/"*.md "$DOCS/flows/"*.md; do
    [ -f "$f" ] || continue
    base=$(basename "$f")
    case "$base" in
        README.md) continue ;;
    esac

    is_adr=0
    case "$f" in
        */adrs/*) is_adr=1 ;;
    esac

    violations=$(awk -v is_adr="$is_adr" '
        function is_exempt_section(h) {
            if (h == "") return 0
            if (is_adr && (h ~ /^## (Consequences|Corpus links|Scope|Status)/)) return 1
            if (h ~ /^## How to /) return 1
            if (h ~ /^## (Read this first|Ownership|Table of contents)/) return 1
            return 0
        }
        function is_exempt_para(p) {
            # Invariants and ADRs often open with a **Statement:** or
            # **Rationale:** paragraph that is definitional — the per-fact
            # anchors live in sibling paragraphs within the same section.
            if (p ~ /^ \*\*(Statement|Rationale|Accepted costs|Earned properties):\*\*/) return 1
            return 0
        }
        BEGIN { in_code=0; para=""; para_start=0; cur_h="" }
        /^```/ { in_code = !in_code; if (in_code == 0) { para=""; para_start=0 } next }
        in_code { next }
        /^## / { cur_h=$0; para=""; para_start=0; next }
        /^#/ { para=""; para_start=0; next }
        /^\|/ || /^[-*] / || /^[0-9]+\. / { para=""; para_start=0; next }
        /^$/ {
            if (para != "" && length(para) > 400 && !is_exempt_section(cur_h) && !is_exempt_para(para)) {
                if (para !~ /\.(go|py|md|sh|json|toml):[0-9]/ &&
                    para !~ /unknown — needs review/ &&
                    para !~ /`[0-9a-f]{7,}`/ &&
                    para !~ /commit `?[0-9a-f]{7,}/) {
                    print FILENAME ":" para_start " (section: " cur_h ")"
                }
            }
            para = ""; para_start = 0; next
        }
        {
            if (para == "") para_start = NR
            para = para " " $0
        }
    ' "$f")
    if [ -n "$violations" ]; then
        echo "$violations" | while read -r v; do echo "  UNANCHORED: $v"; done
        count=$(echo "$violations" | grep -c .)
        unanchored=$((unanchored + count))
    fi
done
if [ "$unanchored" -eq 0 ]; then
    echo "  PASS (no unanchored claim paragraphs > 400 chars)"
else
    echo "  FAIL: $unanchored unanchored claim paragraphs"
    errors=$((errors+1))
fi
echo ""

# ──────────────────────────────────────────────────────────────────────
# Check 3: gaps.md non-empty (≥ 10 lines of real content)
# ──────────────────────────────────────────────────────────────────────
echo "[3/5] gaps.md non-empty"
if [ -f "$DOCS/gaps.md" ]; then
    gap_lines=$(wc -l < "$DOCS/gaps.md" | tr -d ' ')
    if [ "$gap_lines" -gt 10 ]; then
        echo "  PASS ($gap_lines lines)"
    else
        echo "  FAIL: gaps.md under 10 lines ($gap_lines) — hiding gaps is a failure mode"
        errors=$((errors+1))
    fi
else
    echo "  FAIL: gaps.md missing"
    errors=$((errors+1))
fi
echo ""

# ──────────────────────────────────────────────────────────────────────
# Check 4: idioms.md contains enumerated call sites (≥ 1 idiom with
# ≥ 2 rows in a call-sites table).
# ──────────────────────────────────────────────────────────────────────
echo "[4/5] idioms.md call-site enumeration"
if [ -f "$DOCS/idioms.md" ]; then
    table_rows=$(grep -cE '^\| `?(internal|py|hooks|cmd)/' "$DOCS/idioms.md" 2>/dev/null || true)
    table_rows=${table_rows:-0}
    if [ "$table_rows" -ge 2 ]; then
        echo "  PASS ($table_rows call-site rows)"
    else
        echo "  FAIL: only $table_rows call-site rows — an idiom needs ≥2 sites"
        errors=$((errors+1))
    fi
else
    echo "  FAIL: idioms.md missing"
    errors=$((errors+1))
fi
echo ""

# ──────────────────────────────────────────────────────────────────────
# Check 5: Required top-level files present
# ──────────────────────────────────────────────────────────────────────
echo "[5/5] Required files present"
missing_files=""
for required in README.md invariants.md idioms.md trust-boundaries.md seams.md history.md gaps.md; do
    if [ ! -f "$DOCS/$required" ]; then
        missing_files="$missing_files $required"
    fi
done
if [ -z "$missing_files" ]; then
    echo "  PASS (all required files present)"
else
    echo "  FAIL: missing:$missing_files"
    errors=$((errors+1))
fi
echo ""

# ──────────────────────────────────────────────────────────────────────
# Summary
# ──────────────────────────────────────────────────────────────────────
if [ "$errors" -eq 0 ]; then
    echo "=== VALIDATION PASSED ==="
    exit 0
else
    echo "=== VALIDATION FAILED ($errors error(s)) ==="
    echo ""
    echo "Fix the failures above before proceeding to the next phase."
    echo "If a failure reflects a genuine gap that cannot be fixed without"
    echo "human input, add an explicit entry to gaps.md naming the gap."
    exit 1
fi
