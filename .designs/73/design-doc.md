# Design: Baked-in default `dispatch.json` for zero-touch label-triggered autonomy

## Executive Summary

Today `af install --init` creates `.agentfactory/dispatch.json` with an EMPTY
default (`repos:[]`, `mappings:[]`, no `workflows` — `internal/cmd/install.go:145`,
verified), and `runInstallInit` performs no repo-name discovery. So a freshly
bootstrapped factory cannot drive autonomous work from GitHub labels until a human
hand-edits the file — the gap issue #73 targets. This design bakes a useful,
schema-valid default into the tool (a `DefaultDispatchConfigJSON(repo)` function in
`internal/config`, mirroring the established `DefaultFactoryConfigJSON()` at
`config.go:111`), populated with the four label→agent mappings and the
`feature-workflow` the issue proposes, and with `repos` filled from the actual
`owner/name` discovered non-interactively at install time (`git remote` / `gh`).

The pivotal finding from three independent analyses (Dimensions, Architecture
Elevation, Six-Sigma Gap) and firsthand re-verification: shipping that default is
**necessary but not sufficient**. The default's mappings reference four specialist
agents (`rapid-soldesign-plan`, `rapid-implement`, `ultra-review`,
`rapid-increment`) that a FRESH `agents.json` does not contain (install.go:143 ships
only `manager`+`supervisor`, verified), and `ValidateDispatchConfig`
(`internal/config/dispatch.go:93+`) **hard-fails the entire dispatch cycle** on the
first unknown mapped agent — invoked unconditionally at `internal/cmd/dispatch.go:146`
(verified firsthand). So the default alone would break dispatch on every fresh
factory. The design therefore couples three moves into one systemic change: (1) the
baked-in default builder, (2) non-interactive repo discovery feeding `repos`, and
(3) provisioning the referenced specialists during bootstrap so the default is
**valid-by-construction**, backed by a defense-in-depth dispatcher tolerance and a
golden test that mechanically gates drift.

The Architecture Elevation verdict is **Frame correct** (the dispatch fields must
exist; deleting `dispatch.json` only relocates them) with **one Frame-lift OFFERED**
— repo self-derivation — which this design adopts as the repo-discovery component.

## Constraints Respected

All proposals respect the constraints captured verbatim in `source.md`:
- C-1 (baked-in default): the default ships in the `af` binary as `DefaultDispatchConfigJSON(repo)` (a Go function), not authored by a script — mirrors `DefaultFactoryConfigJSON()` (config.go:111).
- C-2 (bootstrapped at first creation): written by the existing idempotent write-if-absent path in `runInstallInit` (install.go:152) — "first created == this write"; a customer-edited file is never clobbered.
- C-3 (actual repository-name): `repos` is populated from the real `owner/name` discovered at install via `git remote get-url origin` / `gh repo view`, validated, then written.
- C-4 (codebase is source of truth): every claim in this design is anchored to a verified `file:line` (re-read this session); docs used only as search aids.
- C-5 (no doctor fixes / no human as ongoing dependency): the default is valid-by-construction (provisioned specialists), so no `doctor --fix` is needed; the only human touchpoints are genuine failure paths (e.g. unparseable remote), never the happy path.
- C-6 (referenced agents must exist): resolved by provisioning the four specialists during bootstrap (primary) plus a dispatch-loop skip-and-warn tolerance (defense-in-depth); the write path (`af config dispatch set`) stays strict.

## AC Traceability (REQUIRED)

| AC ref | Verbatim quote from source.md | Clause breakdown | Addressed by | Verified by |
|--------|-------------------------------|------------------|--------------|-------------|
| AC-1 | "We need to update dispatch.json to include a baked-in default for dispatch with agentfactory" | (i) update dispatch.json (ii) baked-in default (iii) for dispatch | K1 `DefaultDispatchConfigJSON` + K4 wiring at install.go:145 | `TestDefaultDispatchConfigJSON_*` golden test that the shipped default parses + equals expected mappings/workflow (model: `internal/config/dispatch_workflow_test.go`) |
| AC-2 | "they could opt to just start tagging their github issues with tags instead to kick off work without ever needing to visit the manager." | (i) tag issues with labels (ii) kick off work (iii) without visiting the manager | K1 mappings + `trigger_label`; EX dispatcher auto-start (`start_dispatch:true`, install.go:147) + K5 provisioned specialists | Integration test: fresh-init temp factory + provisioned specialists → `ValidateDispatchConfig` passes; `af dispatch --dry-run` matches a `agentic`+`rapid-plan` issue to `rapid-soldesign-plan` |
| AC-3 | "The dispatch.json should be boostrapped when the dispatch.json is first created during initial setup of the repository so we know the repository-name and can include that appropriately in the dispatch.json" | (i) first created (ii) during initial setup (iii) know repo-name (iv) include appropriately | K2 discovery + K3 validation + K4 write-if-absent (install.go:152) | Unit test: temp repo with known `git remote origin` → `af install --init` writes `repos:["<org>/<repo>"]`; re-run does not clobber |
| AC-4 | "when we get to the step where we ask the manager,`run af sling --agent <some-agent> \"task description\"`, we should have the work executed autonomously using the step-by-step formula that represents the IDENTITY of that agent and respects the formulas rigid step-by-step process up to the point where human interaction is necessary for next steps" | (i) `af sling --agent` (ii) autonomous via formula (iii) formula = identity (iv) rigid steps (v) up to human gate | EX `af sling --agent` specialist dispatch (sling.go, unchanged) + K5 (routed agents are formula-bearing) | Existing sling/formula behavior (unchanged); K5 ensures the 4 agents resolve as specialists with formulas |
| AC-5 | "all the code branches created should have been pushed as PR's against the main branch without doctor fixes or human interaction (unless absolutely necessary, or in the case of doctor - a `doctor --fix` is acceptable only as a bandaid/fix for legacy broken behaviors, not as an ongoing operational dependency)." | (i) branches → PRs against main (ii) no doctor as ongoing dep (iii) no human (unless necessary) | (i) property of the routed FORMULAS (K5 routes; scoped to formula layer — see Six-Sigma Caveats) (ii) K1+K5 valid-by-construction → no doctor (iii) EX zero-touch happy path | Clause (i) owned by the formula layer (out of this change's scope, traceably noted); (ii)/(iii) by the valid-by-construction default + no-doctor-on-happy-path |
| AC-6 | "The agents should follow the known working formula process that IS their IDENTITY to perform their work so that we have consistent successful outcomes out of each agent. Your mission when addressing any problem scenario is to seek to understand how to achieve this desired outcome with systemic improvements while addressing the scenario." | (i) formula process IS identity (ii) consistent outcomes (iii) systemic improvements | K5 routes to existing formulas unchanged; K1 single-source + K5 provisioning + K7 drift/golden test = repeatable validity; K1–K7 is systemic | Design review: no formula edits; default cannot drift (single-source + golden test); fix is structural not one-off |

## Architecture Elevation Verdict

**Verdict: Frame correct (with grounded constraint) — one Frame-lift OFFERED.**

From `elevation_assessment.md`: the concern ("a fresh dispatch.json is empty/
placeholder so label-triggering doesn't work out of box") is NOT a symptom of a
removable abstraction. Candidate 0 (delete `dispatch.json`, fold fields into
`agents.json` / derive from the formula registry) FAILS the subtraction gate — it
relocates the same `repos`/`mappings`/`workflows` decisions and breaks the
deliberate L-1 cross-file validation seam (dispatch.go:84-92). So the file must
exist and the right move is to populate the default well (this design's core),
validating the in-frame dimension analysis.

**The OFFERED lift — adopted by this design:** self-derive the home repo from
`git remote get-url origin` at `af install --init`. Today NO layer in `internal/`
reads a git remote (verified, 0 grep hits); the C-3 placeholder exists only because
`runInstallInit` discards a fact already in the environment (quickstart `cd`s into
the repo at ~:428 before `af install --init` at ~:442). Deriving it eliminates the
entire "operator hand-edits `repos`" category for the single-home-repo case. It is
OFFERED (not required) because empty-`repos` remains a valid fallback and the
multi-repo edge requires keeping `Repos []string` editable. **This design adopts the
lift as component K2** while keeping the field editable and degrading (write empty
`repos`, warn loudly) on an unparseable/missing remote — honoring ADR-014
(non-interactive) and ADR-017 (read-only, write-if-absent).

## Problem Statement

(verbatim from `source.md`):

> We need to update dispatch.json to include a baked-in default for dispatch with
> agentfactory, so that when someone new to the project uses
> `./quickdocker.sh repo-link` and gets their container bootstrapped and lands in
> `/af/repo` with their newly bootstrapped repository ready to use agentfactory,
> they could opt to just start tagging their github issues with tags instead to
> kick off work without ever needing to visit the manager.
>
> The dispatch.json should be boostrapped when the dispatch.json is first created
> during initial setup of the repository so we know the repository-name and can
> include that appropriately in the dispatch.json [...].

## Proposed Design

### Overview

Replace the lone inline `dispatch.json` literal in `runInstallInit` with a
single-source `DefaultDispatchConfigJSON(repo string)` builder in `internal/config`;
discover and validate the home repo non-interactively at install time and inject it
into `repos`; and make the default's four referenced specialists present-and-valid
on a fresh factory by provisioning them during bootstrap, with a dispatch-loop
skip-and-warn tolerance and a golden cross-file test as backstops.

### Key Components

| Id | Component | Location (verified) | New/Modified |
|----|-----------|---------------------|--------------|
| K1 | `DefaultDispatchConfigJSON(repo string) string` — builds the default from the `DispatchConfig` struct (compile-time field safety), emitting the 4 mappings + `feature-workflow` + `trigger_label:"agentic"` + struct-default cadence | `internal/config` (beside `DefaultFactoryConfigJSON`, config.go:111) | NEW |
| K2 | Repo-discovery helper: `gh repo view --json nameWithOwner` (primary) with `git remote get-url origin` normalization (no-auth fallback); warn-don't-abort | `internal/cmd/install.go` (`runInstallInit`) | NEW |
| K3 | Strict `owner/name` validator (allowlist regex) applied at the write boundary — guards `gh --repo` flag-injection and terminal-escape in the install banner | `internal/cmd` or `internal/config` | NEW |
| K4 | Wire `runInstallInit` starter-config map (install.go:139-148) to call K2→K3→K1; reuse write-if-absent (install.go:150-157) | `internal/cmd/install.go:145` | MODIFIED |
| K5 | Provision the referenced specialists during bootstrap so `agents.json` contains them before the dispatcher runs — invoke `agent-gen-all.sh` directly (or targeted `af formula agent-gen` for the 4), NOT `af install --agents` | `quickstart.sh` `configure_factory` (~:414-471) | MODIFIED |
| K6 | Dispatch-loop unknown-agent tolerance: skip-and-warn instead of hard-fail, scoped to the dispatch-loop caller ONLY (`af config dispatch set` stays strict) | `internal/cmd/dispatch.go` (caller of `ValidateDispatchConfig`, :146) | MODIFIED (defense-in-depth) |
| K7 | Golden + cross-file tests: the shipped default parses (`validateDispatchConfig`) AND cross-validates (`ValidateDispatchConfig`) against the default+provisioned `agents.json` | `internal/config/*_test.go`, `internal/cmd/*_test.go` | NEW |

### Component Dependency Graph

(from `dependencies.md`; DAG verified acyclic, topo order K3 → K1 → K2 → K4 → K5 → K6 → K7)

```
K4 (install wiring) → K1 (default builder) → EX1 validateDispatchConfig (output must pass struct validation)
K4 → K2 (repo discovery) → K3 (repo validator)
K4 → EX4 write-if-absent guard (reused, unchanged)
K5 (provisioning) → EX3 agent-gen-all.sh / af formula agent-gen
K6 (dispatch tolerance) → EX2 ValidateDispatchConfig (relaxes ONLY the dispatch-loop caller)
K7 (tests) → K1, K4, K5, K6, EX1, EX2

Runtime sequencing: K2→K3→K1→K4 at install; K5 must complete before the dispatcher
runs EX2; EX2 consumes K4's written default + K5's provisioned agents.json.
```
No cycles. **Constraint:** any new validation in `internal/config` MUST NOT import
`internal/formula` (it imports `internal/config` → cycle; dispatch.go:130-136).

### Interface (from api.md)

- `func DefaultDispatchConfigJSON(repo string) string` in `internal/config` —
  returns the marshaled default; `repo` is the validated `owner/name` (empty string
  → `repos:[]` fallback). Built by marshaling a `DispatchConfig` value, never a hand
  string, so the field set is compiler-checked.
- No new CLI flags. `runInstallInit` keeps its signature; discovery is internal.
  The operator-facing surface is unchanged except an additive install banner line
  echoing the discovered repo (escape-safe via K3).

### Data Model (from data.md — MUST match storage constraints)

No new storage format, no DB, no schema migration, no SQL. The `DispatchConfig` JSON
schema already exists (dispatch.go:17-45). This change only sets the DEFAULT VALUE
written into the existing file. The intent-corrected default (the source's malformed
literal fixed by struct marshaling):

```json
{
  "repos": ["<discovered owner/name>"],
  "trigger_label": "agentic",
  "interval_seconds": 300,
  "retry_after_seconds": 1800,
  "remove_trigger_after_dispatch": true,
  "mappings": [
    { "labels": ["rapid-plan"],     "source": "issue", "agent": "rapid-soldesign-plan" },
    { "labels": ["rapid-engineer"], "source": "issue", "agent": "rapid-implement" },
    { "labels": ["pr-review"],      "source": "pr",    "agent": "ultra-review" },
    { "labels": ["pr-iterate"],     "source": "pr",    "agent": "rapid-increment" }
  ],
  "workflows": [
    { "label": "feature-workflow", "phases": ["rapid-plan", "rapid-engineer"] }
  ]
}
```

`notify_on_complete` is **omitted** (it defaults to `"manager"` at runtime via
`validateDispatchConfig`; an explicit value would add a brittle cross-file existence
check — Six-Sigma Gap-7). This default satisfies `validateWorkflows`: each phase is a
single-label mapping (`phaseResolvesAlone`, dispatch.go:256), both phases share
source `"issue"`, and `feature-workflow` collides with neither `trigger_label` nor a
mapping label.

## Cross-Dimension Trade-offs

(from `conflicts.md`; every conflict has a resolution — none unresolved)

| Conflict | Resolution | Rationale |
|----------|-----------|-----------|
| D2×D6 (X) — default references 4 unprovisioned specialists → cross-file validation hard-fails (THE crux) | K5 provision specialists in bootstrap (primary) + K6 dispatcher skip-and-warn (defense-in-depth); Data ships the faithful default unchanged | Provisioning makes the default valid-by-construction (C-5/C-6); tolerance backstops a partial provision |
| D5×D6 (X) — how to resolve C-6 vs write-path strictness | Provision + scope the K6 tolerance to the dispatch-LOOP caller ONLY; `af config dispatch set` keeps strict `ValidateDispatchConfig` | The default is valid-by-construction; the dispatch loop degrades gracefully; human edits still catch typos |
| D1×D5 / D3×D5 (T) — untrusted repo string from a crafted remote | K3 validate `owner/name` at the WRITE boundary before storing/echoing | A bad value never reaches disk, `gh --repo`, or the banner; dispatcher's `strings.Cut` becomes a 2nd line of defense |
| D1×D2 (T) — function output must satisfy `validateDispatchConfig` | Build K1 from the `DispatchConfig` struct (compile-time field safety) + K7 golden test pins content | Struct guarantees well-formedness; golden test guarantees the specific mappings |
| D1×D6 (T) — discovery ordering | Discover→validate→build→write in `runInstallInit` before the starterConfigs map is built (one write) | Wrong ordering would write a placeholder repos |
| D3×D6 (T) — zero-touch path depends on C-6 + auto-start | K5 delivers the C-6 precondition; EX `start_dispatch:true` (install.go:147) auto-starts; U2.1 actionable errors as fallback | The "just tag an issue" promise holds only if specialists exist and dispatch auto-starts |

## Cross-Perspective Conflicts

(Step 3.1b — mining across the independent analyses)

| Finding Source | Finding | Conflicts With | Nature | Resolution |
|----------------|---------|----------------|--------|------------|
| Elevation | Repo self-derivation is a Frame-lift OFFERED (not required) | Dimensions/Integration treat repo discovery (K2) as a core recommended component | tension (status vs adoption) | Adopt K2 as a design component AND document its "offered" elevation status; keep `Repos` editable (multi-repo edge) — both views reconciled |
| Six-Sigma Gap-1 + Integration I3.1 | Provision specialists via `af install --agents` in quickstart | Integration's own quickstart call site | **DIRECT (synthesis-discovered)** — `af install --agents` re-invokes `quickstart.sh` (USING_AGENTFACTORY.md:634), so calling it FROM quickstart RECURSES | **Correction:** K5 invokes `agent-gen-all.sh` directly (or targeted `af formula agent-gen` for the 4), NOT `af install --agents`. Same provisioning effect, no recursion |
| Six-Sigma Gap-5 | AC-5 (PRs without doctor/human) is a property of the 4 FORMULAS, not dispatch.json | AC-5's apparent ownership by this change | scoping | Scope AC-5 clause (i) to the formula layer (necessary-but-not-sufficient); this change routes to those formulas and removes the doctor dependency, but does not (and cannot) verify formula-internal PR-push |
| Six-Sigma Gap-2 | `trigger_label` is a hard query pre-filter (dispatch.go:301) — tagging ONLY a mapping label dispatches nothing | AC-2's natural reading ("just tag rapid-plan") | escape path | Document the two-label requirement (`agentic` + mapping label) in the default and setup docs; widening the query is out of scope (expands blast radius) |
| Decision History (ADR-017) | Write-if-absent never upgrades existing empty-default installs | C-2 "first created" for the installed base | scope boundary | Scope to NET-NEW installs (ADR-017-honest); existing factories opt in via `af config dispatch set` — named, not silently accepted |

No cross-perspective conflict is left unresolved. Pairs checked:
elevation-vs-dimensions (reconciled), gaps-vs-dimensions (recursion correction +
AC-5 scoping), elevation-vs-gaps (both agree dispatch.json must exist),
decision-history-vs-dimensions (ADR-014/017 shape K2/K4/K5, no contradiction).

## Decisions Made

| Decision | Options Considered | Chosen | Rationale | Reversibility |
|----------|--------------------|--------|-----------|---------------|
| Default builder location | Inline literal (status quo) / `DefaultDispatchConfigJSON()` in `internal/config` | `DefaultDispatchConfigJSON(repo)` | Mirrors `DefaultFactoryConfigJSON` single-source (config.go:111); removes the last inline-literal drift surface (issue #371 Gap-6) | Easy |
| Default content | 4 mappings + `feature-workflow` (D2.2-A) / mappings-only (D2.2-C) / manager-supervisor-only (D2.2-B, rejected: fails AC-2) | 4 mappings + `feature-workflow`, intent-corrected from the struct | Maximally faithful to source intent; D2.2-C is the conservative fallback if the workflow surface is deemed risky | Easy |
| Repo name | hand-authored placeholder (status quo) / `--init --repo` flag (I2.2) / self-derive in Go (I2.1) | Self-derive via `gh`/`git remote` in `runInstallInit`, validated | Adopts the elevation Frame-lift; ADR-014 prefers the Go path self-sufficient over a script-fed flag | Easy |
| `notify_on_complete` | explicit `"manager"` (source) / omit | Omit (defaults to manager) | Strictly smaller validation surface; identical runtime behavior (Gap-7) | Easy |
| C-6 provisioning mechanism | `af install --agents` in quickstart (I3.1 as written) / `agent-gen-all.sh` direct / targeted `af formula agent-gen` | `agent-gen-all.sh` direct (or targeted agent-gen) | `af install --agents` re-invokes quickstart → RECURSION (USING:634); direct invocation provisions specialists without recursion | Moderate |
| Dispatcher tolerance | none (hard-fail) / skip-and-warn everywhere / skip-and-warn dispatch-loop only | Skip-and-warn scoped to the dispatch loop; write path strict | Graceful degradation on partial provision without weakening the human write-path typo check | Moderate |
| Installed base | blind overwrite (rejected, ADR-017) / scope to net-new | Net-new installs; existing factories opt in | ADR-017 forbids clobbering customer-edited config | n/a (policy) |

## Risk Registry

| Risk | Severity | Likelihood | Mitigation | Owner | Source |
|------|----------|-----------|------------|-------|--------|
| Default references 4 specialists absent from fresh agents.json → `ValidateDispatchConfig` hard-fails dispatch cycle | HIGH | Certain (without K5) | K5 provision specialists in bootstrap + K6 dispatch-loop skip-and-warn | Integration | dimensions / elevation / gap |
| `af install --agents` from quickstart recurses | HIGH | Certain (if K5 used `--agents`) | K5 invokes `agent-gen-all.sh` directly / targeted `agent-gen`, never `af install --agents` | Integration | synthesis cross-perspective |
| Repo discovery fails (no/non-GitHub/non-`origin` remote) → empty `repos`, dispatcher silently skips (looks like benign "not configured", dispatch.go:1330) | HIGH | Possible | K2 warn-don't-abort writes loadable default; surface a distinct fail-loud message (ADR-014); `af up` pre-flight warns (K8 observability, below) | Security/UX | gap-3 / gap-8 |
| Crafted remote URL injects bad `repos` (gh flag-injection / terminal escape in banner) | MED | Low | K3 strict `owner/name` allowlist at the write boundary | Security | security |
| K1 output fails `validateDispatchConfig` (drift) | MED | Low | Build from struct + K7 golden test | API/Data | conflicts |
| K6 over-broad relaxation weakens `af config dispatch set` strictness | MED | Low | Scope tolerance to dispatch-loop caller only; config_set.go unchanged | Integration/Security | conflicts |
| Future formula rename breaks the default's agent references | MED | Low | ADR-008 drift test ties formula names to embedded files; K7 cross-file test pins the default | Integration | dependencies |
| User tags only a mapping label (not `agentic`) → nothing dispatched, no signal | MED | Likely | Document two-label requirement in default + docs (query-widening out of scope) | UX | gap-2 |
| Fresh-install dispatch validation failure is low-visibility (loops in tmux pane) | MED | Possible | K8 (below): `af up`/`af dispatch status` surface a "config invalid: <reason>" state; pre-flight warns, never aborts `af up` | UX/Integration | gap-8 |

(K8 = observability hardening: a pre-flight `ValidateDispatchConfig` at `af up`/
`af dispatch status` that warns loudly and distinguishes "empty by design" from
"discovery failed" from "references unprovisioned agents". Feasible; additive to the
existing `af dispatch status --json` contract.)

## Six-Sigma Caveats

| Gap | Category | Impact | Feasibility | Constraint |
|-----|----------|--------|-------------|-----------|
| PR-push (AC-5) is a formula-layer property | scope gap | MED | Partially feasible | Full AC-5 verification is E2E/live-gh, out of config-bootstrap scope; this change routes correctly and removes the doctor dependency, but formula-internal push is verified separately |
| "Tagged a mapping label but not `agentic`" miss | escape path | HIGH (UX) | Partially feasible | Diagnosing the miss is architecturally ceilinged — the trigger-label query never fetches the item; only documentation or query-widening helps, the latter out of scope |
| Installed base not auto-migrated | version drift | MED | Partially feasible | ADR-017 forbids clobbering customer config; existing factories opt in via `af config dispatch set` |
| Non-`origin`/non-GitHub remote | dependency fragility | LOW | Infeasible to auto-resolve | Legitimately cannot derive `org/repo`; MUST fail loud (ADR-014), operator supplies the repo |

**Practical ceiling:** the achievable six-sigma target for THIS change is "the
shipped default is valid-by-construction, the repo is correctly discovered or fails
loud, and the validity is mechanically test-gated against drift" — Gaps 1, 3, 4, 7, 8
closed; Gaps 2, 5, 6 named-and-scoped. The end-to-end autonomous OUTCOME (AC-5) is a
multi-component property; this design owns the config-and-provisioning component and
says so. Residual accepted risk: (a) exotic remotes fail discovery and require an
explicit repo; (b) AC-5 is a downstream-formula guarantee verified separately;
(c) existing factories are excluded by design.

## Implementation Plan

### Phase 1: Single-source default builder + repo discovery (Effort: M)
**Deliverables:**
1. K1 `DefaultDispatchConfigJSON(repo string) string` in `internal/config` (build from `DispatchConfig` struct; omit `notify_on_complete`; `remove_trigger_after_dispatch:true`).
2. K2 repo-discovery helper in `runInstallInit` (`gh repo view --json nameWithOwner` → fallback `git remote get-url origin`), warn-don't-abort.
3. K3 strict `owner/name` validator applied before write/echo.
4. K4 replace install.go:145 literal with `config.DefaultDispatchConfigJSON(discoveredRepo)`; reuse write-if-absent.

**Source ACs satisfied**: AC-1, AC-3 (and the substrate of AC-2).
**Dependencies**: none new (uses existing struct, write path).
**Risks addressed**: drift (single-source), injection (K3), discovery failure (warn-don't-abort).
**Six-Sigma gaps closed**: Gap-7 (omit notify), partial Gap-3 (discovery exists).

**Phase acceptance criteria:**
- [ ] `DefaultDispatchConfigJSON("org/repo")` output passes `validateDispatchConfig` (unit test).
- [ ] In a temp repo with a known `git remote origin`, `af install --init` writes `dispatch.json` with `repos:["org/repo"]` and the 4 mappings + `feature-workflow`; a re-run does not clobber it.
- [ ] A crafted/garbage remote URL is rejected by K3 and yields `repos:[]` + a loud warning, not a malformed write.

### Phase 2: C-6 resolution — provision specialists + dispatcher tolerance (Effort: M)
**Deliverables:**
1. K5 bootstrap provisioning in `quickstart.sh` `configure_factory` via `agent-gen-all.sh` (or targeted `af formula agent-gen` for the 4 referenced agents) — explicitly NOT `af install --agents` (recursion).
2. K6 dispatch-loop skip-and-warn on unknown-agent mappings, scoped to the dispatcher caller of `ValidateDispatchConfig` (`internal/cmd/dispatch.go:146`); `af config dispatch set` unchanged (strict).
3. K8 observability: pre-flight validation surfaced at `af up` / `af dispatch status` (warn, never abort).

**Source ACs satisfied**: AC-2, AC-4, AC-6 (and the no-doctor clause of AC-5).
**Dependencies**: Phase 1's default (the mappings that name the specialists).
**Risks addressed**: hard-fail-on-fresh-factory (HIGH), recursion (HIGH), low-visibility failure (MED).
**Six-Sigma gaps closed**: Gap-1, Gap-8; Gap-2 documented.

**Phase acceptance criteria:**
- [ ] After a fresh bootstrap (init + K5 provisioning), `agents.json` contains the 4 specialists and `ValidateDispatchConfig(default, agents)` returns nil.
- [ ] With one mapped agent absent, the dispatch LOOP skips that mapping with a warning and still dispatches the others; `af config dispatch set` with the same config still exits non-zero (strict).
- [ ] `af up`/`af dispatch status` reports a distinct, actionable state for "references unprovisioned agents" vs "empty by design" vs "discovery failed".

### Phase 3: Drift/golden test gate + docs (Effort: S)
**Deliverables:**
1. K7 golden test: the shipped `DefaultDispatchConfigJSON()` output parses (`validateDispatchConfig`) AND cross-validates (`ValidateDispatchConfig`) against the default+provisioned `agents.json` (model: `internal/config/dispatch_workflow_test.go`).
2. Docs: the two-label requirement (`agentic` + mapping/workflow label) for AC-2; the net-new-install scope for the installed base.

**Source ACs satisfied**: AC-1 (drift-gated), AC-6 (systemic + repeatable).
**Dependencies**: Phases 1 and 2 (the default + provisioned agents).
**Risks addressed**: future drift (formula rename / label edit), AC-2 mis-use, installed-base confusion.
**Six-Sigma gaps closed**: Gap-4; Gap-2 and Gap-6 named-and-scoped.

**Phase acceptance criteria:**
- [ ] A test fails CI if any future edit makes the shipped default fail struct OR cross-file validation against the default+provisioned agents.json.
- [ ] Setup docs state the two-label requirement and that the default ships for net-new installs (existing factories opt in via `af config dispatch set`).

## Appendix: Analysis Artifacts
- [source.md](source.md)
- [verification.md](verification.md)
- [codebase-snapshot.md](codebase-snapshot.md)
- Dimension analyses: api.md, data.md, ux.md, scale.md, security.md, integration.md
- [audit.md](audit.md)
- [conflicts.md](conflicts.md)
- [dependencies.md](dependencies.md)
- [elevation_assessment.md](elevation_assessment.md)
- [six_sigma_gaps.md](six_sigma_gaps.md)
- [verification-report.md](verification-report.md)
- [synthesis-checklist.md](synthesis-checklist.md)
