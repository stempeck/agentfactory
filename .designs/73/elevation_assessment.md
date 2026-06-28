# Architecture Elevation Assessment

**Date**: 2026-06-28
**Target**: .designs/73/source.md
**Mode**: advisory
**Verdict**: Frame correct (with grounded constraint) — one Frame-lift OFFERED (C-3 repo-name self-derivation)

---

## Phase 0 — Concerning Abstractions

The "concern" of issue #73 is: *a fresh repo's `dispatch.json` is created empty/placeholder, so the autonomous label-trigger path does not work out-of-box and requires a human to hand-edit (repo name, mappings) or run `doctor --fix`.* Phase 0 interrogates whether that concern exists because of an abstraction that could be deleted, rather than configured.

| Abstraction | file:line | Would removal eliminate the concern? (YES/NO + why) | Simpler system lacking this pattern |
|-------------|-----------|------------------------------------------------------|--------------------------------------|
| `dispatch.json` as a standalone on-disk config file separate from `agents.json` | `internal/config/dispatch.go:17-27` (`DispatchConfig`); written at `install.go:145` | **NO.** The dispatcher still must know WHICH repos to poll, WHICH labels map to WHICH agents. That data must live somewhere. Deleting the file just relocates the same fields; the concern (it's empty on a fresh repo) moves with it. | git's `.git/config` — repo identity is self-derived from `[remote "origin"] url`, not a separately-authored file. (Verified concept; `git config --get remote.origin.url`.) This is the relevant lift for the `repos` FIELD only (Candidate 1), not for the whole file. |
| `Repos []string` as a hand-authored field requiring the org/repo literal | `internal/config/dispatch.go:19`; consumed at `dispatch.go:172` (`for _, repo := range dispatchCfg.Repos`) | **PARTIALLY (this is the strongest candidate).** The repo the factory lives in is already determinable from `git remote get-url origin` at the CWD `af install --init` runs in (quickstart.sh `cd "$repo_dir"` at quickstart.sh:428 happens BEFORE `af install --init` at quickstart.sh:442). The *placeholder/manual-edit* sub-concern (C-3) exists ONLY because `runInstallInit` discards an available fact. **Verified: no git-remote read exists anywhere in `internal/` today** (grep for `git remote`/`remote.origin`/`RepoName` returned 0 hits). So the substrate of the C-3 concern is a *missing derivation*, and adding it eliminates a category of manual edit. | git: `git status` never asks you to type the repo name — it reads `.git`. The dispatcher could self-derive the single home repo the same way. |
| The split between `validateDispatchConfig` (struct-level) and `ValidateDispatchConfig` (cross-file agents.json) | `internal/config/dispatch.go:140-185` and `:93-138` | **NO.** This split is a deliberate seam (L-1, comment at dispatch.go:84-92) so the CLI write path and the dispatcher share one cross-file validator. It does not CREATE the empty-default concern; it is the mechanism that would REJECT a bad default. Removing it would make the concern *worse* (silent dispatch of unknown agents). | n/a — removal regresses. |
| `Mappings`/`Workflows` referencing agent NAMES by string rather than the agents being implied by the formula registry | `dispatch.go:30-45`; cross-checked at `dispatch.go:101` (`agents.Agents[m.Agent]`) | **NO.** Name-based binding is intentional indirection: one label → one agent, decoupled from formula internals (Workflow doc-comment dispatch.go:37-41, "mappings[] owns the agent binding, single source of truth, no formula edits"). The concern is that the *referenced agents are absent from a fresh `agents.json`*, which is a provisioning-ordering problem (Candidate 2), not an abstraction that can be deleted. | n/a within domain. |

**Phase 0 conclusion:** The concern is NOT a pure artifact of a removable abstraction. The dispatch fields must exist somewhere. EXACTLY ONE sub-concern — the C-3 repo-name placeholder — is a removable manual-decision category, because the fact is already present in the environment (git remote) and is currently discarded. That becomes Candidate 1 (Frame-lift candidate).

---

## Ownership Map

Where the relevant policy ("what does a fresh factory dispatch, against which repo, validated how") is currently enforced. Built from source, not summaries.

| Layer | Location (file:line) | Enforcement | Policy carried |
|-------|----------------------|-------------|----------------|
| Install-time default (the concern's origin) | `internal/cmd/install.go:145` — inline literal `{"repos":[],...,"mappings":[],...}` | mechanical (write-if-absent, install.go:150-156) | "A fresh dispatch.json has EMPTY repos, EMPTY mappings, no workflows." This is the literal substrate of the concern. |
| Repo identity | **NOWHERE** in `internal/` (verified: 0 grep hits for git-remote read) | absent | "Which repo does this factory poll?" — today hand-authored into `Repos`; never derived. `runInstallInit` takes no repo arg (install.go, verified). |
| Repo consumption | `internal/cmd/dispatch.go:172` (`for _, repo := range dispatchCfg.Repos`) | runtime | Each `Repos[]` entry is queried via `gh` (dispatch.go:178, :194). Empty `Repos` ⇒ loop body never runs ⇒ dispatcher polls nothing. |
| Cross-file agent existence | `internal/config/dispatch.go:93-138` `ValidateDispatchConfig`; invoked at `internal/cmd/dispatch.go:146` and `internal/cmd/config_set.go:89` | runtime (detects & **blocks** — `return err`) | "Every `mappings[].agent` MUST exist in agents.json." On a fresh install (agents.json = manager+supervisor only, install.go:143), the proposed 4-specialist default makes dispatch.go:146 **hard-fail the whole cycle**. |
| Struct-level shape | `internal/config/dispatch.go:140-185` `validateDispatchConfig`; default-fill interval/retry/notify/source | runtime (detects & blocks at load) | "repos≥1, trigger_label set, mappings≥1, workflow phases resolvable" (dispatch.go:142-171). The CURRENT empty default would FAIL these — but install writes the literal without loading it, so it is never checked at install time. |
| Default-as-Go-function pattern | `internal/config/config.go:108-124` `DefaultFactoryConfigJSON()` | mechanical (single-source; `TestDefaultFactoryConfigJSON_*` pins it) | "factory.json default is built from in-code constants, no on-disk literal to drift." dispatch.json is the ONLY starter config still an inline literal (install.go:145 vs :142) — duplicated-ownership candidate. |
| Provisioning order | `quickstart.sh:442` (`af install --init`) → `:451`,`:463` (`af install manager`/`supervisor`) only | instruction (shell script) | A fresh factory provisions ONLY manager+supervisor; the 4 dispatch specialists are NOT provisioned by the bootstrap path. |

**Duplicated-ownership flags:**
1. The default config literal at install.go:145 duplicates the schema/intent that `internal/config/dispatch.go` owns — every other starter config (`factory.json`) was migrated to a `Default…JSON()` function (config.go:111); dispatch.json was left behind. **N>1 carriers of the same invariant** → flag (this is the AC-1 design target, not an elevation).
2. Repo identity is owned by NO layer (the gap that produces the C-3 placeholder concern).

---

## Elimination Candidates

Candidate 0 is the Phase-0 frame-removal hypothesis (delete the abstraction so the concern cannot exist). Candidate 1 is the strongest *field-level* lift. Candidate 2 documents the provisioning sub-concern.

### Candidate 0 — Delete `dispatch.json` as a separate config; fold dispatch fields into `agents.json` / derive entirely from the formula registry
- **Altitude**: abstraction removal
- **Target boundary**: `internal/config/dispatch.go` (`DispatchConfig`), `install.go:145`
- **Invariant carried**: "which repos/labels/agents the dispatcher acts on"
- **Deletion ledger**: would remove `DispatchConfig` (dispatch.go:17-27), the install.go:145 literal, `LoadDispatchConfig`/`SaveDispatchConfig` (dispatch.go:48-82).
- **Addition ledger**: would ADD equivalent fields to `agents.json` schema + new loader/validator + migration of `dispatch.go:134,146,1273,1327` and `config_set.go` callers; ADD re-derivation logic to infer labels→agents from formulas.
- **Category elimination**: NO. The label→agent→repo decisions still must be made at runtime; they just move files. The empty-on-fresh-repo concern moves with them.
- **Subtraction gate**: Dim 1 (artifacts) deletions ≈ additions, **not** deletions>additions (net relocation). Dim 2 (categories) NO. **FAILS subtraction gate.**

### Candidate 1 — Self-derive the home repo from `git remote get-url origin` at `af install --init`, eliminating the hand-authored `Repos` literal for the common single-repo case  ★ STRONGEST
- **Altitude**: schema invariant + category elimination (turn a hand-authored field into a derived one)
- **Target boundary**: `internal/cmd/install.go` `runInstallInit` (writes dispatch.json at install.go:145) + a new `internal/config` derivation (mirroring `DefaultGitIdentity()`, config.go:89)
- **Invariant carried**: "the repo this factory polls == the repo it was installed into." Currently NO layer owns this (Ownership Map row 2).
- **Deletion ledger**: removes the user-decision category "type your org/repo into `Repos`" and removes the failure mode where `repos:[]` ⇒ dispatcher polls nothing (dispatch.go:172 loop is empty). Removes the C-3 placeholder `"org/repository"` as a manual-edit surface.
- **Addition ledger**: ADDS one git-remote read (e.g. `git remote get-url origin`, parsed to `org/repo`) called from `runInstallInit`; ADDS a `DefaultDispatchConfigJSON(repo string)`-style function in `internal/config` (mirrors `DefaultFactoryConfigJSON`, config.go:111); ADDS a unit test (temp repo w/ known origin → `repos:["org/repo"]`). Per ADR-014: on missing/unparseable remote it MUST fail-loud-with-flag or write empty `repos`, **never prompt**.
- **Category elimination**: **YES.** Eliminates the entire runtime category "operator manually edits `repos`" for the single-home-repo case — the dispatcher self-grounds in the repo it lives in, exactly like `git status` self-grounds in `.git`.
- **Subtraction gate**: Dim 2 (categories) **TRUE** → **PASSES** (a category of manual decision is eliminated even though artifact count rises slightly).

### Candidate 2 — Provision the 4 default-dispatch specialists into the fresh `agents.json` (or make the dispatcher SKIP, not hard-fail, unknown-agent mappings)
- **Altitude**: single-interface invariant (align the default's referenced agents with what install provisions)
- **Target boundary**: `internal/cmd/install.go:143` (default agents.json) and/or `internal/config/dispatch.go:100-104` (the hard-fail loop) and/or `internal/cmd/dispatch.go:146`
- **Invariant carried**: "a shipped default dispatch.json must not reference agents that a fresh agents.json lacks" (else dispatch.go:146 returns err and the whole cycle dies).
- **Deletion ledger**: if the dispatcher SKIPPED unknown-agent mappings instead of erroring, it removes a category of total-cycle-failure. But that WEAKENS a deliberate validator (ValidateDispatchConfig, L-1 single-source) → see Self-Challenge Q4.
- **Addition ledger**: ADDS 4 agent entries to the install.go:143 literal (or a default-agents function), OR ADDS skip-and-warn logic at dispatch.go:146.
- **Category elimination**: NO new abstraction removed; this is a *correctness/consistency fix* the design MUST make (C-6), but it is not an elevation — it adds config, it doesn't subtract a concern.
- **Subtraction gate**: Dim 1 additions>deletions; Dim 2 NO (skip-variant eliminates a failure category but at the cost of weakening validation). **FAILS subtraction gate as an elevation** (belongs in the design as a required correctness item, owned by Dimensions/Data, not as a frame-lift).

---

## Self-Challenge

Surviving candidate after the subtraction gate: **Candidate 1** (Candidate 0 failed the gate; Candidate 2 is a correctness item, not an elevation). I attempt to reject Candidate 1 on all five questions. Candidate 0 is also audited to formally settle the "Frame artificial" question.

### Candidate 1 — repo self-derivation grounding audit

| # | Question | Rejection succeeds? | Grounding provided | Grounding passes? |
|---|----------|---------------------|--------------------|-------------------|
| Q1 | Does the target boundary exist in this codebase's topology? | NO | `runInstallInit` exists (install.go, writes dispatch.json at :145); `internal/config` already houses derived-default funcs (`DefaultGitIdentity` config.go:89, `DefaultFactoryConfigJSON` config.go:111). quickstart.sh `cd "$repo_dir"` (:428) runs BEFORE `af install --init` (:442), so a git remote is present in CWD. | YES — boundary is real and the env fact is available. Rejection FAILS. |
| Q2 | Does the language/framework/runtime support the proposed invariant? | NO | Go can shell `git remote get-url origin` (the codebase already shells `gh` throughout dispatch.go, e.g. queryGitHubIssues). Parsing org/repo from a remote URL is trivial string work. | YES — fully supported. Rejection FAILS. |
| Q3 | Does an ADR or user-signed decision decline this migration? | NO (it CONSTRAINS, does not decline) | **ADR-014** (no interactive prompting in `cmd/`/`internal/`): the derivation MUST be non-interactive — read git remote, else fail-loud-with-flag, never prompt. **ADR-017** (no deleting customer data; reading git remote is read-only; writing inside `.agentfactory/` is permitted; preserve write-if-absent so a customer-edited dispatch.json is never clobbered — install.go:150-156 already does). **Neither ADR declines the lift**; both shape its non-interactive, idempotent form. | YES — ADRs permit it under a stated shape. Rejection FAILS. |
| Q4 | Does the lift create a worse defect class? | PARTIAL | Risk: a multi-repo factory, or a factory whose origin ≠ the repo to poll, would get a wrong/narrow `repos`. Mitigation: write the derived single repo as the *default*, keep `Repos []string` editable (don't delete the field), and on no/unparseable remote write empty `repos` (today's behavior) — strictly a superset of current capability. So no NEW defect class for existing users; only an added correct-by-default for the common case. | Rejection PARTIALLY succeeds (multi-repo edge) but is fully mitigated by keeping the field editable → counts as a **constraint**, not a kill. |
| Q5 | Does the lift step OUTSIDE the concern's named domain (dispatch-config bootstrap)? | NO | Reading the home repo's git remote at install time to populate the dispatch `repos` field is squarely inside "bootstrap dispatch.json with the repository-name" — it IS the literal text of C-3 / AC-3. | YES — in-domain. Rejection FAILS. |

**Result:** 4 of 5 rejections FAIL; Q4 succeeds only as a mitigated edge-case constraint. Candidate 1 survives as a **Frame-lift OFFERED** (≥1 rejection counts → "offered", not "required"). It is offered, not mandated, because today's empty-`repos` behavior is a valid fallback and the multi-repo edge requires the field to stay editable.

### Candidate 0 — frame-removal grounding audit (to settle "Frame artificial")

| # | Question | Rejection succeeds? | Grounding provided | Grounding passes? |
|---|----------|---------------------|--------------------|-------------------|
| Q1 | Target boundary exists? | YES (rejection succeeds) | The dispatch fields would have to be re-homed into `agents.json` (config.go) or re-derived from formulas — but that boundary does NOT today carry repo/label/trigger semantics; building it is net-new, not a deletion. | Rejection SUCCEEDS. |
| Q4 | Worse defect class? | YES | Folding dispatch into agents.json couples two independently-validated configs (dispatch.go vs agents.json loaders) and breaks the deliberate L-1 cross-file seam (dispatch.go:84-92). | Rejection SUCCEEDS. |

**Result:** Candidate 0 fails the subtraction gate AND its rejections succeed. The dispatch.json abstraction is NOT artificial — the concern is real and the file must exist. **Frame is NOT artificial.**

---

## Verdict

**Frame correct (with grounded constraint), and one Frame-lift OFFERED.**

Rationale, grounded:

1. **The frame is correct, not artificial.** The concern ("a fresh dispatch.json is empty/placeholder so autonomous label-triggering doesn't work out of box") is NOT the symptom of a removable abstraction. The dispatch fields (`Repos`, `Mappings`, `Workflows`, `TriggerLabel`) must live somewhere; deleting `dispatch.json` (Candidate 0) only relocates them (subtraction gate FAILS; Q1/Q4 rejections succeed). The right shape is therefore to *populate* the default well, which is exactly AC-1/AC-2/AC-3. This is a design-WITHIN-the-frame problem, validating Phase 2's dimension analysis rather than redirecting it.

2. **One genuine elevation is OFFERED — Candidate 1 (repo self-derivation).** Today NO layer owns repo identity (Ownership Map row 2: 0 grep hits for any git-remote read in `internal/`). The C-3/AC-3 placeholder (`"org/repository" <-update this...`) exists ONLY because `runInstallInit` discards a fact that is already in the environment (quickstart.sh `cd "$repo_dir"` at :428 precedes `af install --init` at :442, so `git remote get-url origin` is available). Deriving it ELIMINATES the entire runtime category "operator hand-edits `repos`" for the single-home-repo case (subtraction gate Dimension 2 = TRUE). It survives 4/5 self-challenge rejections; ADR-014 and ADR-017 (snapshot Decision History) permit it under a non-interactive, read-only, write-if-absent shape, which the existing `runInstallInit` already satisfies. It is OFFERED rather than REQUIRED because empty-`repos` remains a valid fallback and the multi-repo edge (Q4) requires keeping `Repos []string` editable — so the lift must *add a smart default*, never *remove the field*.

3. **Required correctness item flagged for the synthesis (not an elevation): Candidate 2.** The proposed default references 4 specialist agents (`rapid-soldesign-plan`, `rapid-implement`, `ultra-review`, `rapid-increment`), but a fresh `agents.json` (install.go:143) has only `manager`+`supervisor`. The dispatcher cross-validates at `dispatch.go:146` via `ValidateDispatchConfig`, which **`return err`s and hard-fails the entire cycle** when a mapped agent is missing (dispatch.go:100-104). So the proposed default, shipped as-is against a fresh factory, would BREAK dispatch entirely until those agents are provisioned. The design MUST either (a) provision the 4 specialists into the default `agents.json`/bootstrap, or (b) have the dispatcher skip-and-warn on unknown-agent mappings (which weakens the L-1 validator — see Q4). This is owned by the Data/Integration dimensions and is the single highest-severity correctness constraint for AC-2/AC-4/C-6; it is NOT a frame elevation (it adds config; it subtracts no concern).

4. **Single-source constraint (not an elevation, but a duplicated-ownership flag):** dispatch.json is the ONLY starter config still an inline literal at install.go:145, whereas `factory.json` was migrated to `DefaultFactoryConfigJSON()` (config.go:111, issue #371 Gap-6). The default should be expressed as a `DefaultDispatchConfigJSON(repo string)` function in `internal/config` (mirroring the established pattern) so the on-disk default cannot drift from the schema, and ADR-008's drift discipline applies to any formula names it references.

The single strongest elimination candidate is **Candidate 1: self-derive the home repo from `git remote get-url origin` at `af install --init`**, eliminating the C-3 manual-edit category while honoring ADR-014 (non-interactive) and ADR-017 (read-only/write-if-absent).
