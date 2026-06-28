# Designer Update Log — Cross-Review Round 1 (Issue #73)

**Designer:** design-v7
**Date:** 2026-06-28
**Reviewer source:** `.designs/73/cross-review/analyst-review-design.md` (rootcause-all), grounded in `.analysis/73/rootcause_analysis.md`.
**Disposition:** ALL CRITICAL (C1, C2) and HIGH (H1, H2) findings incorporated into `.designs/73/design-doc.md`. LOW (L1–L4) were already-agreements; no change required. Each analyst claim was re-verified firsthand against the codebase before incorporation.

## Firsthand verification of the analyst's claims (before editing)

| Claim | Verification command/result | Verdict |
|-------|------------------------------|---------|
| C1: 4 role templates already embedded | `ls internal/templates/roles/{rapid-soldesign-plan,rapid-implement,ultra-review,rapid-increment}.md.tmpl` → all 4 exist | CONFIRMED |
| C1: no `DefaultAgentsConfigJSON` exists yet | `grep -rn "func DefaultAgentsConfigJSON" internal/config/` → none | CONFIRMED (would be NEW) |
| C2: `startDispatch` gated by `blanket` | `up.go:306 if blanket {` wraps `up.go:330 if startupCfg.StartDispatch { startDispatch(...) }` | CONFIRMED — `af up manager` (positional) is gated out |
| H1: `startDispatch` idempotent | `dispatch.go:1322-1325` → `if running { "Dispatcher already running"; return nil }` | CONFIRMED — hoisting is a safe no-op if running |

## Changes applied (per finding)

### C1 (CRITICAL) — bare `af install --init` left the populated default invalid-by-construction
- **Change:** Redefined **K5** from "provision specialists via quickstart/`agent-gen-all.sh`" to **"`DefaultAgentsConfigJSON()` seeds the 4 specialist registry entries into the default `agents.json` within `runInstallInit` (the SAME write as K4)"**. Because the 4 role templates are already embedded, a seeded `agents.json` entry (with `formula`) is sufficient for `af prime`/sling — no `agent-gen` run, no rebuild, no quickstart-only dependency.
- **Sections edited:** Key Components (K5 row); Executive Summary (now "four moves", valid on every init path); Data Model (new "Default `agents.json` (K5)" subsection with the seeded JSON); Cross-Dimension Trade-offs (D2×D6 resolution); Cross-Perspective Conflicts (new C1 row + superseded the `agent-gen-all.sh` row); Decisions Made (C-6 mechanism row → seed agents.json, reversibility Easy); Risk Registry (new bare-init row); Implementation Plan (K5 moved into Phase 1, same `runInstallInit` write); Dependency Graph (K4→K5 same write, cross-entry-point sequencing eliminated); AC-2 traceability row.
- **Effect:** the default is valid-by-construction on EVERY init path, including the documented "hard way" (`USING_AGENTFACTORY.md:40-52`). K7's cross-file invariant now holds on bare init, not just quickstart.

### C2 (CRITICAL) — documented `af up manager` does not auto-start the dispatcher
- **Change:** Added new component **K9 — hoist the `if startupCfg.StartDispatch { startDispatch(...) }` block out of the `blanket`-only gate** (`up.go:306,330`) so positional `af up <name>` (the documented `af up manager`) also auto-starts the dispatcher. Idempotent no-op if already running (`dispatch.go:1322-1325`).
- **Sections edited:** Key Components (new K9 row); Executive Summary ("(4) hoisting dispatcher auto-start"); Cross-Perspective Conflicts (new C2 row); Decisions Made (new "Dispatcher auto-start on positional `af up`" row); Risk Registry (new positional-`af up` row); Implementation Plan (Phase 2 deliverable 1 = K9, with an `af up manager` acceptance criterion); AC-2 traceability row ("K9 hoists dispatcher auto-start").
- **Effect:** "tag an issue without visiting the manager" holds on the documented `af up manager` path, not only blanket `af up`.

### H1 (HIGH) — commit to the minimal K5 provisioning mechanism
- **Change:** K5 is now committed to a single mechanism — **default `agents.json` seeding** (the C1 fix) — explicitly dropping `agent-gen-all.sh` (heavyweight: `af down --all` + full rebuild) and the prior recursion-prone `af install --agents`. Recorded in Decisions Made (options list shows all four considered; chosen = seed) and the Risk Registry (the `af install --agents` recursion risk row is removed/superseded as it no longer applies).
- **Sections edited:** Decisions Made; Cross-Perspective Conflicts (H1/H2 row); Implementation Plan (Phase 2 no longer mentions `agent-gen-all.sh`).

### H2 (HIGH) — make K8 observability mandatory wherever K6 is enabled
- **Change:** **K8** elevated from "optional/additive" to **MANDATORY whenever K6 ships** — added as an explicit component row, noted in the K6 row ("K8 is MANDATORY wherever K6 is enabled"), in Decisions Made (dispatcher-tolerance row), and in Implementation Plan Phase 2 deliverable 3 ("MANDATORY with K6"). Rationale captured: K6 without K8 turns a clean friendly-skip into a silently-warning loop that looks active but dispatches nothing.
- **Sections edited:** Key Components (K6 + new K8 rows); Decisions Made; Implementation Plan (Phase 2).

### LOW (L1–L4) — agreements, no change
- L1 (omit `notify_on_complete`): already adopted (Gap-7). L2 (AC-5 formula-layer scoping): already the design's position; analyst confirms it matches Concern 9. L3 (K3 `owner/name` validator): already present; analyst notes dispatcher's `strings.Cut` (dispatch.go:537-539) as a useful 2nd line — already nodded to in the design. L4 (installed-base net-new scope, ADR-017): already named in Six-Sigma Caveats. No edits required.

## Post-edit integrity checks
- AC Traceability rows = 6 (= source ACs); Constraints Respected = 6 (≥ source Cs).
- No INACCURATE claim from the fidelity report reintroduced (`internal/cmd/dispatch_workflow_test.go` absent; the correct `internal/config/` path retained).
- design-doc.md grew 319 → 365 lines; all new components (K5 `DefaultAgentsConfigJSON`, K8 mandatory, K9) present.

## Convergence note
The analyst's review independently converged with the design on every primary point
(K1 single-source builder, K2 discovery, K7 drift test, two-label UX trap, AC-5
formula-layer scoping, Frame-correct + repo-self-derivation lift). The two CRITICAL
items were genuine entry-point/sequencing gaps the design missed; both are small,
localized, and reinforce — not redirect — the valid-by-construction thesis.
