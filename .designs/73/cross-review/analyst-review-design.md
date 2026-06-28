# Analyst Cross-Review of `design-doc.md` — Round 1 (Issue #73)

**Reviewer:** rootcause-all (Analyst)
**Date:** 2026-06-28
**Basis:** Grounded in `.analysis/73/rootcause_analysis.md` (9 concerns; 8 VALIDATED, 1 INVALIDATED) and the per-concern investigations `rootcause_concern_{1..9}.md`. All citations re-checked against the codebase.

## Summary Verdict

The design is **strong and substantially correct** — it independently converges with my root-cause findings on every primary point. Two issues rise to **CRITICAL** because they are entry-point/sequencing gaps that defeat the design's own "valid-by-construction" and "without visiting the manager" guarantees on documented paths; both are directly evidenced by my VALIDATED concerns #1/#4 and #7. Fixing them is small and keeps the design's shape intact.

## Convergence (no action needed — recorded for confidence)

| Design element | My analysis | Agreement |
|---|---|---|
| K1 `DefaultDispatchConfigJSON(repo)` single-source builder (config.go:111 idiom) | Solution §"DefaultDispatchConfigJSON" | ✅ identical |
| K2 repo discovery via `gh repo view --json nameWithOwner` + `git remote` fallback, warn-don't-abort | Concern 2 recommended mechanism (`detectRepoSlug` on `runGitDetect` seam) | ✅ identical |
| Empty-`repos` fallback preserves friendly-skip | Concern 1/7 (`dispatch.go:142-150`, `:1327-1339`) | ✅ |
| K7 golden + cross-file drift test | Solution §"drift-interlock test (Poka-yoke)" | ✅ identical intent |
| Two-label requirement (`agentic` + mapping label) is a UX trap to document | Concern 8 | ✅ |
| AC-5 "no doctor": `doctor` is not a real command; PR-push is a formula-layer property | Concern 9 (`e2e_sling_test.go:67-69`) | ✅ identical |
| Architecture-elevation: Frame correct + repo self-derivation lift | (consistent with my synthesis) | ✅ |

## CRITICAL

### C1 — "Valid-by-construction" holds only on the quickstart path; bare `af install --init` is left internally inconsistent
**Evidence:** Concern 1 + Concern 4. The default `dispatch.json` (with the four mappings) is written by **`runInstallInit`** (K4, `install.go:145`), but the four specialists are provisioned by **K5 in `quickstart.sh configure_factory` (~:414-471)** — a *different* entry point. `af install --init` registers only `manager`+`supervisor` (`install.go:143`), and `ValidateDispatchConfig` hard-fails the dispatch cycle on the first unknown mapped agent (`internal/config/dispatch.go:101-102`, invoked at `internal/cmd/dispatch.go:146`). Therefore any path that runs `af install --init` **without** quickstart — including the documented "hard way" in `USING_AGENTFACTORY.md:40-52` (`af install --init` → `af install manager` → `af install supervisor`) — produces a populated default whose mappings reference unregistered agents. The cross-file invariant the design's own K7 test asserts ("default cross-validates against default+provisioned agents.json") is only true on the quickstart path; on bare init it is **false by construction**.

**Why K6 doesn't save it:** with all four agents unknown (bare-init), the K6 skip-and-warn tolerance makes the dispatcher *run but match nothing* — functionally identical to the empty default, so AC-2 still fails for these users.

**Recommended fix (small, keeps the design's shape):** make the default valid-by-construction *within `runInstallInit` itself* — seed the four specialist entries (`{"type":"autonomous","formula":"<name>"}`) into the default `agents.json` in the same write, rather than relying on a separate `quickstart.sh` step. I verified the four role templates are **already embedded** (`internal/templates/roles/{rapid-soldesign-plan,rapid-implement,ultra-review,rapid-increment}.md.tmpl` exist), so a seeded `agents.json` entry is sufficient for `af prime`/sling to resolve each specialist — no `agent-gen` run is required at install. This collapses K5's cross-entry-point dependency into the single `--init` write and makes K7's cross-file invariant hold on **every** init path. (Mirror the `DefaultAgentsConfigJSON()` single-source idiom alongside `DefaultDispatchConfigJSON()`.)

### C2 — The design relies on dispatcher auto-start but does not address that the documented `af up manager` flow does NOT start it
**Evidence:** Concern 7. Auto-start of the dispatcher fires **only on the blanket `af up`** (no positional args): the `startDispatch` call sits inside `if blanket {` (`internal/cmd/up.go:92, 306, 330`). The issue's documented setup flow is `af up manager` (positional — `USING_AGENTFACTORY.md:67`, and the issue body steps 4–5), which is **gated out** and will not launch the polling loop even with a valid populated config and provisioned agents. The design cites "EX dispatcher auto-start (`start_dispatch:true`, install.go:147)" as satisfying AC-2 (rows AC-2, D3×D6) but nowhere reconciles the blanket-vs-positional gating. So the "just tag an issue without visiting the manager" promise breaks for any user who follows the documented `af up manager` step.

**Recommended fix:** include an explicit component to either (a) hoist the `if startupCfg.StartDispatch { startDispatch(...) }` block out of the `blanket`-only gate so it runs on any `af up` (the launch is idempotent — already-running is a benign no-op, `dispatch.go:1322-1325`), or (b) at minimum, change the documented flow/docs to use bare `af up` and state that positional `af up <name>` does not start dispatch. Option (a) is the robust interlock and is the one my analysis recommends. This belongs in Phase 2 alongside K5/K6/K8.

## HIGH

### H1 — Pick the minimal K5 provisioning mechanism; `agent-gen-all.sh` is heavyweight and disruptive
**Evidence:** Concern 4 + my template-embedding verification. K5 currently says "`agent-gen-all.sh` direct (or targeted `af formula agent-gen` for the 4)" — it does not commit to one. `agent-gen-all.sh` runs `af down --all` and regenerates **every** template plus a full rebuild (`USING_AGENTFACTORY.md:636`), which is costly and could disrupt the bootstrap sequence mid-`configure_factory`. Since the four templates are already embedded, the **minimal** mechanism is direct `agents.json` seeding (my C1 fix) — no rebuild, no `af down --all`, no recursion. If the design prefers an `agent-gen` call, scope it to the **four targeted** agents, never the all-variant. Commit to one mechanism (single-solution discipline).

### H2 — K6 skip-and-warn can mask "configured-but-broken" as "running," degrading observability vs. the clean friendly-skip
**Evidence:** Concern 1/7. Today an unconfigured default produces a clean, single "not configured" friendly-skip (`dispatch.go:1327-1339`). With a populated-but-unprovisioned default (the bare-init case), K6 makes the loop *start and then skip every mapping with warnings in a tmux pane* — which looks active but dispatches nothing, an arguably worse signal. K6 is sound as defense-in-depth, but it must be paired with K8 so `af up`/`af dispatch status` reports a **distinct** "references unprovisioned agents" state (the design lists this under K8 — good; the review note is to make K8 **non-optional** wherever K6 is enabled, not a "feasible/additive" nicety, since K6 without K8 hides the failure my C1 describes).

## LOW

- **L1 — `notify_on_complete` omission:** Agree. Defaulting to `"manager"` via `validateDispatchConfig` with a smaller validation surface (Gap-7) is sound; runtime behavior is unchanged. No action.
- **L2 — AC-5 formula-layer scoping:** Agree, and it matches Concern 9 exactly (PR-push lives in the formulas; `origin`+`gh` auth are container-layer via `quickdocker.sh:557/:567`, not `af install --init`). Good, honest scoping.
- **L3 — K3 `owner/name` validator:** Good security addition my analysis did not cover (injection via crafted remote). Concur; note the dispatcher's existing `strings.Cut(repo, "/")` (`dispatch.go:537-539`) is a useful second line of defense (the design already nods to this).
- **L4 — Installed-base scope (net-new only, ADR-017):** Agree it must be named; the write-if-absent idempotency (`install.go:150-157`) is the right boundary. No action.

## Disposition

- **CRITICAL (must address before implementation):** C1 (seed specialists in `runInstallInit` so the default is valid-by-construction on every init path), C2 (handle dispatcher auto-start on the documented `af up manager` path).
- **HIGH (should address):** H1 (commit to the minimal provisioning mechanism), H2 (make K8 mandatory wherever K6 is enabled).
- **LOW:** L1–L4 are agreements/nits.

Both CRITICAL items are small, localized changes that *strengthen* the design's own valid-by-construction thesis rather than redirect it. With C1 and C2 incorporated, the design fully satisfies AC-2 and AC-3 on every documented setup path, not just the quickstart happy path.
