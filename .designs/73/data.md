# D2 — Data Model (data.md)

Owner of (verification.md): AC-1 (with Integration), AC-3 (with Integration),
C-6 (with Integration, Security). This dimension governs the SHAPE and CONTENT of
the baked-in default `dispatch.json`: which fields, which mappings, which
workflows, and whether any storage/schema/migration change is needed.

CONSTRAINT-SENSITIVE NOTE (storage): There is NO new storage format, NO database,
NO schema migration, and NO SQL in any option below. The `DispatchConfig` JSON
schema already exists (`internal/config/dispatch.go:18-45`, verified) and the
file is already written by the install flow (`internal/cmd/install.go:145`,
verified). This dimension only changes the DEFAULT VALUE written into an
existing file with an existing schema. C-4 (codebase-only truth) is honored by
anchoring every field below to the verified struct.

---

## D2.0 Established facts (verified, anchored)

- `DispatchConfig` fields and JSON tags: `repos`, `trigger_label`,
  `notify_on_complete`, `mappings`, `interval_seconds`, `retry_after_seconds`,
  `remove_trigger_after_dispatch`, `workflows` (dispatch.go:18-27, verified).
  **Every field name in the proposed JSON matches the struct exactly**
  (codebase-snapshot §6).
- `DispatchMapping`: `label` (deprecated singular, auto-migrates to `labels`),
  `labels`, `source` ("issue"|"pr", default "issue"), `agent` (dispatch.go:30-35,
  verified).
- `Workflow`: `label`, `phases` (dispatch.go:42-45, verified).
- Struct validation (`validateDispatchConfig`, dispatch.go:141-185, verified)
  requires: `len(repos) > 0`, non-empty `trigger_label`, `len(mappings) > 0`,
  each mapping has ≥1 label and a non-empty `agent`. Workflow phases must each
  resolve to a single-label mapping on the phase label ALONE
  (`phaseResolvesAlone`, dispatch.go:256-267; `validateWorkflows`, dispatch.go:192-249,
  verified), and a workflow's phases must share one source (dispatch.go:239-245).
- Cross-file validation (`ValidateDispatchConfig`, dispatch.go:93-138, verified):
  every `mapping.agent` must exist in agents.json; every workflow-phase agent must
  be formula-bearing.

## D2.1 Critical schema-vs-source consistency findings (block naive copy of the proposed JSON)

These are DATA-model defects in the source's *proposed* JSON (source.md:100-148)
that any chosen option MUST fix — the source itself says "the *intent* matters,
not the literal JSON" (source.md:100):

1. The proposed mappings use `"labels"` (plural, dispatch.go:32 `Labels`), but the
   workflow `phases` are `["rapid-plan", "rapid-engineer"]` while the mapping
   labels are `rapid-plan` and `rapid-engineer`. For `validateWorkflows` to pass,
   each phase must equal a single-label mapping's lone label
   (`phaseResolvesAlone`, dispatch.go:256-267). The proposed mappings ARE
   single-label (`["rapid-plan"]`, `["rapid-engineer"]`) — so this resolves, but
   ONLY if the workflow phases reference the *mapping labels* `rapid-plan` /
   `rapid-engineer` (they do). The `pr-review` / `pr-iterate` mappings are NOT in
   any workflow, which is legal.
2. `validateWorkflows` HIGH-2 (dispatch.go:239-245) requires all phases of a
   workflow to share a source. The `feature-workflow` phases `rapid-plan`
   (source "issue") and `rapid-engineer` (source "issue") share source "issue" ✓.
3. The proposed `workflows[].label` is `"feature-workflow"`. `validateWorkflows`
   rejects a workflow label that collides with `trigger_label` ("agentic")
   (dispatch.go:202-204) — no collision ✓ — and rejects a workflow label that is
   ALSO a mapping label (HIGH-B, dispatch.go:219-221) — `feature-workflow` is not a
   mapping label ✓.

These are not "options"; they are constraints any default must satisfy. The
options below differ in WHAT agents/workflows the default ships, not whether it is
schema-valid.

## D2.2 — Default content: which mappings and workflows ship

### Option D2.2-A — Ship the proposed 4 mappings + feature-workflow VERBATIM (intent-corrected) — RECOMMENDED, with the C-6 provisioning rider

Bake the default exactly as the source proposes (source.md:100-148), corrected to
valid JSON: `trigger_label:"agentic"`, the 4 mappings (`rapid-plan`→
`rapid-soldesign-plan`, `rapid-engineer`→`rapid-implement`, `pr-review`→
`ultra-review`/source pr, `pr-iterate`→`rapid-increment`/source pr), and the
`feature-workflow` workflow `["rapid-plan","rapid-engineer"]`. `repos` is injected
by D1/A2 discovery. All other scalars match the proposed values and the struct
defaults (interval 300, retry 1800, notify "manager", remove_trigger true).

- Trade-offs: Maximally faithful to the source intent (AC-1). All 4 referenced
  formulas EXIST in `install_formulas/` and `store/formulas/` (codebase-snapshot
  §3, re-verified: `rapid-soldesign-plan`, `rapid-implement`, `ultra-review`,
  `rapid-increment` all present). The risk is purely C-6 cross-file: a FRESH
  `agents.json` has only `manager`+`supervisor` (codebase-snapshot §6,
  install.go:143, verified), so these 4 agents are ABSENT until provisioned →
  `ValidateDispatchConfig` would fail (dispatch.go:100-104).
- THE C-6 RIDER (mandatory companion, owned jointly with Integration): the default
  is only safe IFF the 4 agents are present in agents.json by the time
  `ValidateDispatchConfig` runs. Note that `LoadDispatchConfig` does NOT run the
  cross-file check (dispatch.go:48-65, verified — struct-only); only the dispatcher
  path and `af config dispatch set` do (codebase-snapshot §6). So the file LOADS
  fine; the cross-file failure surfaces at dispatch-start. The rider: bootstrap
  must provision the referenced specialists (via `af install --agents`, which runs
  `af formula agent-gen` for EVERY shipped formula — agent-gen-all.sh:134-153,
  verified) before the autonomous path is exercised, OR the dispatcher must skip
  unknown-agent mappings (a dispatcher change owned by Integration). This rider is
  the crux of C-6 and is escalated to Integration/D6.
- Reversibility: Easy (the default is one Go function body).
- Constraints: satisfies C-1, AC-1, and (with the rider) C-6. Recommended.

### Option D2.2-B — Ship a MINIMAL default that references only provisioned agents (manager/supervisor), with placeholder mappings the operator edits — REJECTED

Bake a default whose mappings reference only `manager`/`supervisor` (the two
agents guaranteed present in a fresh agents.json), or ship empty mappings with a
comment.

- REJECTED: fails [AC-1], fails [AC-2]. AC-1 requires a "baked-in default for
  dispatch with agentfactory" that is useful out of the box; AC-2 requires that
  applying the trigger/mapping labels actually dispatches WORK (specialist
  formulas), "without ever needing to visit the manager." `manager`/`supervisor`
  are not specialists with formulas (codebase-snapshot §6: fresh agents.json has
  `manager` type interactive, `supervisor` type autonomous — neither is a
  formula-bearing specialist). Mapping the trigger labels to them dispatches no
  meaningful formula work, so the autonomous outcome AC-2/AC-4 demand never occurs.
  Also, an empty/placeholder mappings default fails `validateDispatchConfig`
  (`len(mappings) > 0`, dispatch.go:148-150) or loads but does nothing.

### Option D2.2-C — Ship the 4 mappings WITHOUT the `feature-workflow` workflow (mappings-only default) — RECOMMENDED (fallback / conservative)

Identical to D2.2-A but omit the `workflows` block. Mappings alone already satisfy
AC-1/AC-2 (label→agent dispatch); the `workflows` multi-phase pipeline (issue #378,
dispatch.go:38) is an additional convenience the source proposes but no AC strictly
requires.

- Trade-offs: Smaller blast radius — drops the `validateWorkflows` surface
  (dispatch.go:192-249) and the HIGH-2 same-source / formula-bearing constraints
  entirely. Loses the one-label `feature-workflow` convenience the source proposes.
  No AC names "feature-workflow" specifically; AC-1 ("a baked-in default") and AC-2
  (label-tagging dispatches work) are fully met by mappings alone.
- Reversibility: Easy.
- Constraints: satisfies C-1, AC-1, AC-2 (with the same C-6 rider as D2.2-A).
  Recommended as the conservative fallback if the workflow adds validation risk on
  a fresh factory. NOTE: this does NOT reverse a deliberate change — issue #378's
  `workflows` feature (dispatch.go:38, Decision History) remains in the schema and
  usable; we simply don't ship one BY DEFAULT. So no ADR/recent-change reversal.

## D2.3 — `repos` data lifecycle

### Option D2.3-A — `repos: ["<discovered owner/name>"]` written once at first-create, never re-written — RECOMMENDED

The discovered repo (D1/A2) is written into `repos` exactly once, by the existing
idempotent write-if-absent path (`os.Stat … os.IsNotExist`, install.go:152,
verified). A re-run of `af install --init` does NOT clobber an operator-edited
`repos` (the file already exists → skipped).

- Trade-offs: Honors C-2 ("first created") and ADR-017 (never clobber customer
  data — codebase-snapshot Decision History). If the operator later moves the repo
  or adds repos, their edit survives. Cost: if discovery yielded the wrong repo at
  first-create, only a manual edit (or delete+reinit) fixes it — acceptable, and
  symmetric with every other starter config.
- Reversibility: Easy.
- Constraints: satisfies C-2, C-3, complies with ADR-017. Recommended.

### Option D2.3-B — Re-derive and overwrite `repos` on every `af install --init` — REJECTED

- REJECTED: violates [C-2] and contradicts [ADR-017]. C-2 ties the bootstrap to
  "first created"; the existing write-if-absent guard (install.go:152, verified)
  is precisely the "first created == this write" semantics codebase-snapshot §6
  records. Overwriting on every run would clobber a customer-edited `repos`,
  which ADR-017 ("af infrastructure commands must not delete customer data")
  forbids for an inside-af-dir file that the customer may have curated.

## Reversibility (this dimension): Easy

The change is the body of one default-config builder plus the repo value. No
schema change, no migration, no on-disk format change. A revert restores the
empty-default literal.

## Dependencies produced

- PROVIDES to **API (D1)**: the exact field/value set the `DefaultDispatchConfigJSON`
  function must emit so its output passes `validateDispatchConfig` (the 4 mappings,
  the workflow or not, the scalar defaults).
- PROVIDES to **Integration (D6)**: the C-6 RIDER — the list of agents the default
  references (`rapid-soldesign-plan`, `rapid-implement`, `ultra-review`,
  `rapid-increment`) that MUST be provisioned (or skipped) before
  `ValidateDispatchConfig` runs.
- REQUIRES from **API (D1)**: the discovered `owner/name` repo string to place in
  `repos`.
- REQUIRES from **Integration (D6)**: confirmation of WHEN cross-file validation
  runs relative to specialist provisioning in the bootstrap sequence.

## Risks identified

| Risk | Severity | Mitigation |
|------|----------|------------|
| Default references 4 specialists absent from a fresh agents.json → `ValidateDispatchConfig` fails at dispatch-start (C-6) | HIGH | The C-6 rider: bootstrap provisions specialists via `af install --agents` (agent-gen-all.sh:134-153) before dispatch, OR Integration makes the dispatcher skip unknown-agent mappings |
| Naively copying the source's malformed JSON (unclosed bracket, source.md:114) | HIGH | D2.1: emit from the `DispatchConfig` struct, not from the literal; the struct guarantees well-formed JSON |
| Shipping `feature-workflow` adds `validateWorkflows` failure surface on edge factories | Low | D2.2-C fallback (mappings-only) removes the workflow surface entirely |
| A formula referenced by the default is renamed/removed in a future release, breaking the default | Medium | ADR-008 drift test ties shipped formula names to embedded files; a golden test asserting the default validates against the shipped agents catches a rename |
