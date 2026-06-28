# Design Extraction — Issue #73 (Phase 1 artifact)

Structured extraction of `.designs/73/design-doc.md` and companion docs, assembled
during design-plan-impl Phase 1 (Discover Design Artifacts). This is the foundation
the implementation plan outline is built from. The **design-doc.md is authoritative**;
where a companion doc disagrees, the design-doc (latest synthesis) wins — drifts are
flagged below.

Design dir: `.designs/73/` · Input: PR #74 (`af/rapid-soldesign-plan-7605ca`) · MODE=pr

---

## 1. Phases (from design-doc.md §Implementation Plan, lines 305-352)

| Phase | Title | Effort | Deliverables (components) | Source ACs | Dependencies | Phase acceptance criteria (verbatim refs) |
|-------|-------|--------|---------------------------|------------|--------------|--------------------------------------------|
| 1 | Single-source default builder + repo discovery | M | K1 `DefaultDispatchConfigJSON`, K2 repo discovery, K3 `owner/name` validator, K4 install wiring, K5 `DefaultAgentsConfigJSON` seed | AC-1, AC-3, AC-2 substrate | none new (existing struct, write path, embedded templates) | (a) `DefaultDispatchConfigJSON("org/repo")` passes `validateDispatchConfig`; (b) bare `af install --init` in temp repo w/ known remote → `dispatch.json` has `repos:["org/repo"]`+4 mappings+`feature-workflow`, `agents.json` has 4 specialists, `ValidateDispatchConfig` returns nil, re-run clobbers neither; (c) crafted remote URL rejected by K3 → `repos:[]` + loud warning |
| 2 | Dispatcher auto-start + graceful degradation + observability | M | K9 hoist `StartDispatch` out of blanket gate, K6 dispatch-loop skip-and-warn, K8 (MANDATORY) pre-flight observability | AC-2, AC-4, AC-6 | Phase 1's seeded default | (a) `af up manager` (positional) launches dispatch tmux session when `start_dispatch:true` + default valid; 2nd `af up manager` benign no-op; (b) with one mapped agent absent, dispatch LOOP skips that mapping w/ warning + dispatches others; `af config dispatch set` still exits non-zero (strict); `af dispatch status` reports distinct "references unprovisioned agents" state |
| 3 | Drift/golden test gate + docs | S | K7 golden + cross-file test, docs (two-label requirement + net-new scope) | AC-1 (drift-gated), AC-6 | Phases 1 & 2 | (a) test fails CI if a future edit makes the shipped default fail struct OR cross-file validation against default+provisioned agents.json; (b) setup docs state two-label requirement + net-new-install scope |

**Dependency-ordered numbering note:** the design's phase order already follows
dependency order (Phase 1 default+seed → Phase 2 runtime auto-start/tolerance →
Phase 3 drift-gate/docs). The topo order of components is **K3 → K1 → K2 → K4 → K5 →
K6 → K7** (design-doc:130; K8/K9 added by cross-review). No renumbering needed.

---

## 2. Components (design-doc §Key Components, lines 114-148)

| Id | Component | Location (verified anchor) | New/Mod | Notes |
|----|-----------|----------------------------|---------|-------|
| K1 | `DefaultDispatchConfigJSON(repo string) string` — build from `DispatchConfig` struct; 4 mappings + `feature-workflow` + `trigger_label:"agentic"`; omit `notify_on_complete`; `remove_trigger_after_dispatch:true` | `internal/config` beside `DefaultFactoryConfigJSON` (config.go:111) | NEW | MUST NOT import `internal/formula` (cycle; dispatch.go:130-136) |
| K2 | Repo-discovery helper: `gh repo view --json nameWithOwner` (primary) + `git remote get-url origin` normalization (no-auth fallback); warn-don't-abort; **timeout-bounded** (scale S1.1) | `internal/cmd/install.go` `runInstallInit` (~:97+) | NEW | model: `DefaultGitIdentity()` config.go:89; precedent `quickdocker.sh:41` |
| K3 | Strict `owner/name` validator (allowlist regex `^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$`, one slash, no whitespace/shell-meta/`..`) at the WRITE boundary | `internal/cmd` or `internal/config` | NEW | guards `gh --repo` flag-injection (dispatch.go:300) + banner terminal-escape; `strings.Cut` (dispatch.go:537-539) is 2nd line of defense |
| K4 | Wire `runInstallInit` starter-config map to call K2→K3→K1; reuse write-if-absent | `internal/cmd/install.go:145` (literal replaced); guard install.go:150-157 | MOD | discover→validate→build→write BEFORE starterConfigs map built (one write) |
| K5 | `DefaultAgentsConfigJSON()` seeds 4 specialists `{"type":"autonomous","formula":"<name>"}` into default `agents.json` in SAME `runInstallInit` write | `internal/config` (new fn) + `internal/cmd/install.go:143` (literal replaced) | NEW+MOD | templates already embedded (`internal/templates/roles/{rapid-soldesign-plan,rapid-implement,ultra-review,rapid-increment}.md.tmpl`, verified) → no `agent-gen`/rebuild/quickstart step |
| K6 | Dispatch-loop unknown-agent tolerance: skip-and-warn (NOT hard-fail), scoped to dispatch-loop caller ONLY; `af config dispatch set` stays strict | `internal/cmd/dispatch.go` caller of `ValidateDispatchConfig` (:146) | MOD (defense-in-depth) | K8 MANDATORY wherever K6 enabled |
| K7 | Golden + cross-file tests: shipped default parses (`validateDispatchConfig`) AND cross-validates (`ValidateDispatchConfig`) against default-seeded `agents.json` — asserts on bare-init path | `internal/config/*_test.go`, `internal/cmd/*_test.go` | NEW | model: `internal/config/dispatch_workflow_test.go` |
| K8 | Pre-flight `ValidateDispatchConfig` surfaced at `af up` / `af dispatch status`; distinguishes "empty by design" vs "discovery failed" vs "references unprovisioned agents"; warn, never abort `af up` | `internal/cmd/up.go` / `af dispatch status` | NEW (mandatory) | additive to `af dispatch status --json` contract |
| K9 | Hoist `if startupCfg.StartDispatch { startDispatch(...) }` out of the `blanket`-only gate so positional `af up <name>` also auto-starts dispatcher | `internal/cmd/up.go:306,330` (gate up.go:92) | MOD | `startDispatch` idempotent — already-running benign no-op (dispatch.go:1322-1325, verified) |

**Existing (consumed, anchors):** EX1 `validateDispatchConfig` (dispatch.go:141-185, struct);
EX2 `ValidateDispatchConfig` (dispatch.go:93-138, cross-file; called dispatch.go:146 AND
config_set.go:89); EX3 `af install --agents`/`agent-gen-all.sh:134-153` (NOT used by revised K5);
EX4 write-if-absent guard (install.go:150-157).

---

## 3. Source ACs (source.md) & Constraints

**ACs (verbatim, source.md:24-40):**
- AC-1: baked-in default for dispatch in dispatch.json → K1+K4
- AC-2: tag issues to kick off work without visiting the manager → K1 mappings + K5 (specialists present) + K9 (auto-start on documented path)
- AC-3: bootstrap at first-create, know & include repo-name → K2 discovery + K3 validation + K4 write-if-absent
- AC-4: `af sling --agent` runs autonomously via identity formula up to human gate → EX sling (unchanged) + K5 (routed agents are formula-bearing)
- AC-5: branches pushed as PRs without doctor/human → **formula-layer property (scoped OUT, Gap-5)**; this change routes correctly + removes doctor dependency
- AC-6: formula process IS identity; systemic improvements → K5 routes to existing formulas unchanged; K1 single-source + K5 + K7 = repeatable validity

**Constraints (source.md:42-74):** C-1 baked-in Go func (not script); C-2 bootstrap at
first-create (write-if-absent); C-3 actual `owner/name` discovered at install; C-4
codebase is source of truth (docs are search aids); C-5 no doctor/human as ongoing
dependency (valid-by-construction); C-6 referenced agents MUST exist (K5 primary +
K6 defense).

---

## 4. Dimension docs discovered & read (all of `.designs/73/*.md`)

Read directly: design-doc.md, source.md, dependencies.md, conflicts.md.
Digested via parallel sub-agents (with file:line anchors preserved):
api.md, data.md, integration.md · security.md, scale.md, ux.md, codebase-snapshot.md ·
six_sigma_gaps.md, elevation_assessment.md, audit.md, verification.md,
verification-report.md, cross-review/analyst-review-design.md.
Remaining (context): problem-summary-73.md, synthesis-checklist.md,
design-refinement-progress.md, cross-review/{designer-update-log.md, pr-body.md}.

**Appendix links (design-doc:354-365):** source, verification, codebase-snapshot,
dimension analyses (api/data/ux/scale/security/integration), audit, conflicts,
dependencies, elevation_assessment, six_sigma_gaps, verification-report,
synthesis-checklist.

---

## 5. Six-Sigma Gaps (six_sigma_gaps.md) — status

| Gap | Impact | Status | Owner phase |
|-----|--------|--------|-------------|
| Gap-1: default references 4 absent specialists → hard-fail | CRITICAL | **closed** by K5 seed | P1 |
| Gap-2: `trigger_label` hard pre-filter — tagging only mapping label dispatches nothing (dispatch.go:301,320) | HIGH (UX) | named-and-scoped → docs (two-label requirement) | P3 |
| Gap-3: no repo discovery in Go init; empty `repos` silently skips | HIGH | closed by K2 (+K8 distinguishes) | P1/P2 |
| Gap-4: no golden/cross-file test → silent drift | HIGH | closed by K7 | P3 |
| Gap-5: AC-5 PR-push is a FORMULA-layer property, not dispatch.json | MED | scoped OUT (verified separately) | — |
| Gap-6: write-if-absent never upgrades existing installs | MED | named-and-scoped → net-new only (ADR-017); opt-in via `af config dispatch set` | P3 docs |
| Gap-7: explicit `notify_on_complete` adds brittle cross-file check | LOW | closed by omitting it (defaults to manager, const dispatch.go:15) | P1 |
| Gap-8: fresh-install dispatch failure low-visibility in tmux | MED | closed by K8 (mandatory) | P2 |

---

## 6. Cross-review CRITICAL/HIGH (analyst-review-design.md) — all incorporated

- **C1 (CRITICAL):** valid-by-construction held only on quickstart path; bare `af install
  --init` registered only manager+supervisor → hard-fail. **Fix:** K5 seeds 4 specialists
  in `runInstallInit` (templates embedded). Adopted verbatim.
- **C2 (CRITICAL):** dispatcher auto-start fires only on blanket `af up` (gate up.go:92,306,330);
  documented `af up manager` (positional) never starts polling. **Fix:** K9 hoists
  `StartDispatch` out of the blanket gate (idempotent). Adopted verbatim.
- **H1 (HIGH):** commit to minimal K5 mechanism (agents.json seeding, NOT `agent-gen-all.sh`).
  Incorporated.
- **H2 (HIGH):** K6 skip-and-warn masks "configured-but-broken" as "running" → K8 MANDATORY
  with K6. Incorporated.

---

## 7. Architecture Elevation (elevation_assessment.md)

Verdict: **Frame correct** — dispatch.json must exist (Candidate 0 "delete dispatch.json"
fails the subtraction gate; relocates the same fields + breaks the L-1 cross-file
validation seam dispatch.go:84-92). **One Frame-lift OFFERED & adopted:** repo
self-derivation (K2) from `git remote get-url origin` at install (today 0 grep hits for
git-remote read in `internal/`). OFFERED (not required) because empty-`repos` stays a
valid fallback and multi-repo keeps `Repos []string` editable.

---

## 8. Cross-doc DRIFT flags (design-doc is authoritative)

1. **K5 mechanism drift:** `dependencies.md:17` and `integration.md` (I3.1) and
   `security.md` (SEC2.1) describe K5 as the OLD `af install --agents`-in-`quickstart.sh`
   provisioning. The **design-doc.md (synthesis, post cross-review C1) supersedes this**:
   K5 is now `DefaultAgentsConfigJSON()` seeding into the default `agents.json` inside
   `runInstallInit` — NO quickstart step, NO `agent-gen-all.sh`, NO rebuild. The outline
   MUST follow the design-doc, and note the stale companion docs.
2. **K8/K9 are cross-review additions:** not present in dependencies.md's original DAG;
   the design-doc Risk Registry + Decisions + Implementation Plan carry them. Use the
   design-doc.
3. **`notify_on_complete`:** source's proposed JSON includes `"manager"`; design omits it
   (Gap-7). Follow the design (omit).
4. **`install.go` line drift:** the empty dispatch.json starter literal is cited as both
   `install.go:145` and `install.go:176` across docs (the map vs the literal value);
   codebase investigation (Phase 2) must confirm the actual current line numbers.

---

## 9. Key MUST / MUST-NOT (for acceptance criteria authoring)

- MUST build K1/K5 from structs (compile-time field safety); MUST NOT hand-author JSON.
- MUST NOT import `internal/formula` from `internal/config` (import cycle).
- MUST validate repo (K3 regex) at the WRITE boundary before disk/echo/`gh --repo`.
- MUST bound K2 discovery with a context timeout (scale S1.1); warn-don't-abort.
- MUST preserve write-if-absent (ADR-017): never clobber customer-edited config.
- MUST NOT prompt interactively (ADR-014); fail loud naming the remedy flag.
- K6 tolerance MUST be scoped to the dispatch-loop caller ONLY; write path strict.
- K8 MANDATORY wherever K6 ships (else K6 hides the failure).
- Default routes ONLY to formula-bearing specialists (verified present in
  `install_formulas/` + `store/formulas/`).
