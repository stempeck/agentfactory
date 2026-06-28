# audit.md — Pre-Synthesis Audit (re-grounded against source.md)

Re-read source.md top to bottom before producing these tables. The constraint
set (C-1..C-6) and AC set (AC-1..AC-6) below are re-copied verbatim from source.md,
NOT from verification.md.

For the constraint columns, this audit tracks the THREE constraints with the
broadest cross-dimension reach as C1/C2/C3 columns, since 6 constraints don't fit a
compact table — but every constraint is covered in the prose and Table C:
- **C1 column = C-1** "baked-in default … with agentfactory" (source.md:45)
- **C2 column = C-2** "bootstrapped when … first created during initial setup" (source.md:50)
- **C3 column = C-3** "we know the repository-name and … include that appropriately" (source.md:55-57)
- (C-4 codebase-truth, C-5 no-doctor/no-human, C-6 agents-must-exist are audited in prose + Table C rows.)

---

## Table A — Constraint Audit

| Dimension | Recommendation | C1 (C-1 baked-in) | C2 (C-2 first-create) | C3 (C-3 actual repo) | Status |
|-----------|----------------|-------------------|-----------------------|----------------------|--------|
| API (D1) | A1.1 `DefaultDispatchConfigJSON(repo)` + A2.1/A2.2 discovery + A3.1 warn-don't-abort | PASS — Go func, baked into tool | PASS — called from runInstallInit's first-create write (install.go:152) | PASS — repo is a parameter populated by discovery | PASS |
| Data (D2) | D2.2-A (4 mappings + workflow, intent-corrected) + C-6 rider; D2.3-A write-once | PASS — default value of an existing baked file | PASS — write-if-absent guard = "first created" | PASS — `repos:["<discovered owner/name>"]` | PASS (C-6 deferred to D6) |
| UX (D3) | U1.1 zero-touch + U1.2 banner + U2.1 actionable errors | PASS — no hand-authoring needed | PASS — present right after init | PASS — banner echoes the discovered repo | PASS |
| Scale (D4) | S1.1 single discovery call + S2.1 inherit cadence + S3.1 one repo | PASS — no extra infra | PASS — discovery once at first-create | PASS — the one discovered repo | PASS |
| Security (D5) | SEC1.1 allowlist repo + SEC2.1 provision (+SEC2.2 tolerate) + SEC3.1 write-if-absent | PASS | PASS — write-if-absent preserves C-2 + ADR-017 | PASS — validates owner/name before write | PASS |
| Integration (D6) | I1.1 build in config + I2.1 discover in init + I3.1 provision (+I3.2 tolerate) + I4.1 route to specialists | PASS | PASS — runInstallInit starterConfigs map | PASS — discovery feeds repos | PASS (resolves C-6) |

C-4 (codebase-truth): PASS for all dimensions — every cited path/line/function is
verified against the code this session (install.go, dispatch.go, config.go,
paths.go, config_set.go, quickstart.sh, agent-gen-all.sh) or in codebase-snapshot.md.
C-5 (no doctor / no human): PASS — no option depends on `doctor --fix`; the only
human touchpoints are genuine failure paths (degraded default), never the happy
path; SEC2.3 (rely on doctor) is REJECTED. C-6 (referenced agents must exist): the
crux; RESOLVED by D6/I3.1 (provision via `af install --agents` in quickstart) +
I3.2 (dispatcher skip-and-warn). All Table A rows PASS.

---

## Table B — AC Coverage Audit (clause-by-clause)

| AC-id | Verbatim text (re-copied from source.md) | Owner dimension(s) | Option chosen | Clause-by-clause: does it satisfy EVERY clause? | Evidence |
|-------|------------------------------------------|--------------------|---------------|--------------------------------------------------|----------|
| AC-1 | "We need to update dispatch.json to include a baked-in default for dispatch with agentfactory" | Data, Integration | D2.2-A + I1.1 | **Clause 1** "update dispatch.json" → YES (runInstallInit starter write, install.go:145→DefaultDispatchConfigJSON). **Clause 2** "baked-in default" → YES (Go function, single-source). **Clause 3** "for dispatch with agentfactory" → YES (mappings/workflows wire the dispatch system). | `DefaultDispatchConfigJSON` mirrors `DefaultFactoryConfigJSON` (config.go:111, verified); replaces literal at install.go:145 |
| AC-2 | "they could opt to just start tagging their github issues with tags instead to kick off work without ever needing to visit the manager." | UX, Integration | U1.1 + I3.1 | **Clause 1** "start tagging github issues with tags" → YES (trigger_label "agentic" + mapping labels rapid-plan/rapid-engineer/pr-review/pr-iterate). **Clause 2** "to kick off work" → YES (dispatcher slings the mapped specialist, dispatch.go cycle step 6). **Clause 3** "without ever needing to visit the manager" → YES (dispatcher auto-starts on `af up`, startup.json start_dispatch:true, install.go:147; no manager interaction). REQUIRES C-6 rider (specialists provisioned) — resolved I3.1. | dispatch cycle (codebase-snapshot §4); start_dispatch:true (install.go:147, verified) |
| AC-3 | "The dispatch.json should be boostrapped when the dispatch.json is first created during initial setup of the repository so we know the repository-name and can include that appropriately in the dispatch.json" | Integration, Data | I2.1 + D2.3-A | **Clause 1** "bootstrapped when … first created" → YES (write-if-absent guard, install.go:152 = "first created"). **Clause 2** "during initial setup of the repository" → YES (runInstallInit, called by quickstart configure_factory:442). **Clause 3** "so we know the repository-name" → YES (discovery via gh/git remote in CWD). **Clause 4** "include that appropriately in the dispatch.json" → YES (validated owner/name written to repos). | install.go:152 (verified); quickstart cd's into repo before init (:428,442, verified) |
| AC-4 | "when we get to the step where we ask the manager,`run af sling --agent <some-agent> \"task description\"`, we should have the work executed autonomously using the step-by-step formula that represents the IDENTITY of that agent and respects the formulas rigid step-by-step process up to the point where human interaction is necessary for next steps" | Integration, UX | I4.1 + U3.1 | **Clause 1** "af sling --agent <some-agent> 'task'" → YES (existing specialist dispatch, sling.go resolveSpecialistAgent, codebase-snapshot §4). **Clause 2** "executed autonomously using the step-by-step formula" → YES (formula instantiated, run via af prime/af done). **Clause 3** "represents the IDENTITY of that agent" → YES (formula-bearing specialists, codebase-snapshot §3). **Clause 4** "respects the formulas rigid step-by-step process" → YES (formula step machinery, unchanged). **Clause 5** "up to the point where human interaction is necessary" → YES (gates; existing behavior). | `af sling --agent` specialist mode (codebase-snapshot §4, sling.go verified) — EXISTING; this change only routes to such agents |
| AC-5 | "all the code branches created should have been pushed as PR's against the main branch without doctor fixes or human interaction (unless absolutely necessary, or in the case of doctor - a `doctor --fix` is acceptable only as a bandaid/fix for legacy broken behaviors, not as an ongoing operational dependency)." | Integration, Security | I4.1 + SEC2.1 | **Clause 1** "branches pushed as PRs against main" → YES (property of the rapid-*/ultra-review formulas the default routes to; routing verified, formula-internal push step is [UNVERIFIED] at step level but out of this change's scope). **Clause 2** "without doctor fixes … as an ongoing dependency" → YES (SEC2.3 rejected; default is valid-by-construction via I3.1, no doctor needed). **Clause 3** "without human interaction (unless absolutely necessary)" → YES (zero human step on happy path; degraded path is the only-when-necessary exception). | Default valid-by-construction (I3.1); SEC2.3 REJECTED on C-5; routing to existing formulas (I4.1) |
| AC-6 | "The agents should follow the known working formula process that IS their IDENTITY to perform their work so that we have consistent successful outcomes out of each agent. Your mission when addressing any problem scenario is to seek to understand how to achieve this desired outcome with systemic improvements while addressing the scenario." | Integration, UX, Scale | I4.1 + U3.1 + S2.1 | **Clause 1** "follow the known working formula process that IS their IDENTITY" → YES (routes to existing formula-bearing specialists, no formula edits). **Clause 2** "consistent successful outcomes out of each agent" → YES (valid-by-construction default + provisioned specialists + inherited stable cadence). **Clause 3** "systemic improvements while addressing the scenario" → YES (single-source default builder + repo discovery + provisioning fix = systemic, not a one-off patch). | I4.1 (formulas unchanged); D2/I1.1 single-source; I3.1 systemic provisioning |

ALL 6 ACs: every clause = YES. No clause unsatisfied. The single conditional is the
C-6 rider on AC-2 and AC-5 (referenced specialists must be provisioned), which is
explicitly RESOLVED by D6/I3.1 + I3.2.

---

## Table C — Additional Context Coverage

| Context item (verbatim from source.md) | Reflected in dimension(s) / dismissed with rationale |
|-----------------------------------------|------------------------------------------------------|
| C-4: "review the codebase as the only real source-of-truth … *.md documents might have outdated information" (source.md:62) | Reflected in EVERY dimension: all claims anchored to verified file:line (install.go, dispatch.go, config.go, quickstart.sh) per the Codebase Fidelity Rule; markdown used only as search aid. |
| C-5: "without doctor fixes or human interaction … doctor --fix … only as a bandaid … not as an ongoing operational dependency" (source.md:67) | Reflected in D5/SEC2.3 (REJECTED — relying on doctor violates C-5), D3/U1.1 (zero-touch happy path), D6/I3.1 (valid-by-construction so no doctor needed), Table A C-5 prose. |
| C-6 [inferred]: mappings reference rapid-soldesign-plan/rapid-implement/ultra-review/rapid-increment; labels rapid-plan/rapid-engineer/pr-review/pr-iterate; workflow phases [rapid-plan, rapid-engineer] (source.md:71-74) | Reflected in D2 (the 4 mappings + workflow content), D6/I3 (the C-6 resolution — provision via `af install --agents` + dispatcher tolerance), D5/SEC2. THE central finding: fresh agents.json lacks these 4 specialists (quickstart.sh provisions only manager+supervisor, :448-470, verified). |
| "Before You Proceed" directive: "Read USING_AGENTFACTORY.md first … state the Vision, Mission … how agents start, how they receive work, how formulas drive execution" (source.md:79-83) | Directed at the human/agent investigator's process, not a design deliverable. Reflected operationally: the design respects formula-driven execution (AC-4/AC-6) and the agents-receive-work-via-dispatch flow. Not a config artifact; dismissed as process guidance, honored in framing. |
| Setup flow steps 1-7 (quickdocker → claude → quickstart.sh → af up manager → attach … OR af sling --agent) (source.md:85-98) | Reflected in D6 (the integration map anchors discovery to quickstart's cd-then-init, :428/442) and D3 (the happy path = quickstart → af up → tag issue). The "recommended" step 6 (`af sling --agent`) is exactly AC-4's path. |
| Proposed default dispatch.json with "the source's typos/unclosed brackets; the *intent* matters, not the literal JSON" (source.md:100-148) | Reflected in D2/D2.1 (the schema-vs-source consistency findings: emit from the `DispatchConfig` struct, NOT the malformed literal; the unclosed bracket at source.md:114 is fixed by struct marshaling). Intent honored, literal corrected. |
| Scope: "medium — all 6 dimensions … narrow in surface … touches install/setup code, dispatch config schema, repo-name discovery, and correctness of agent/label references" (source.md:150-154) | Reflected as the scope frame of all 6 dimension files; heavyweight scale machinery (S1.2/S2.2) REJECTED on scope grounds. |

NO context item is unaddressed. Every Additional Context block from source.md maps
to at least one dimension or is dismissed with explicit rationale.

---

## Audit result: PASS

No Table A row fails. No AC clause is unsatisfied (the C-6 conditional on AC-2/AC-5
is resolved by D6/I3.1+I3.2). No Additional Context item is unaddressed. The single
HIGH-severity finding (C-6: fresh agents.json lacks the 4 referenced specialists)
is surfaced in D2/D5/D6 and resolved by I3.1 (provision in quickstart) + I3.2
(dispatcher skip-and-warn). No return-to-dimension required.
