# Concern #6 Investigation: Proposed feature-workflow validity

**Investigated by**: Sub-agent
**Date**: 2026-06-28

## Verdict: VALIDATED

## Summary
The proposed `{label:"feature-workflow", phases:["rapid-plan","rapid-engineer"]}` workflow
PASSES every workflow-validation rule in `internal/config/dispatch.go` against the proposed
mappings. Both phases back single-label mappings (`rapid-plan`→`rapid-soldesign-plan`,
`rapid-engineer`→`rapid-implement`); both mappings are `source:"issue"`, so they share a
source; the workflow label `"feature-workflow"` is distinct from `trigger_label:"agentic"`
and from every mapping label (`rapid-plan`, `rapid-engineer`, `pr-review`, `pr-iterate`);
neither phase equals `trigger_label`; and both phase agents are formula-bearing in
agents.json (`rapid-soldesign-plan`→formula `rapid-soldesign-plan`, `rapid-implement`→formula
`rapid-implement`), so the cross-file formula-bearing check also passes. The codebase even
ships a `TestUsingDoc_WorkflowsExampleValidates` test whose documented example uses the SAME
`feature-workflow` label with a structurally identical two-phase issue-source pipeline, and
all 12 workflow tests pass in this worktree. "Validates as written" is established by both
hand-tracing every rule with file:line evidence and by running the existing test suite.

## 5-Whys Analysis

### Why #1: Why might the proposed feature-workflow fail validation?
Because `validateWorkflows` (dispatch.go:192-249) enforces seven distinct rules, and a
workflow that trips any one is rejected at load time. So I must trace each rule against the
exact proposed config.

### Why #2: Why does the workflow label "feature-workflow" pass the label rules?
- Non-empty (dispatch.go:195-197): "feature-workflow" is non-empty. PASS.
- Not duplicated (dispatch.go:198-201, `seen` map): only one workflow in the proposed config. PASS.
- Label != `trigger_label` (dispatch.go:202-204): "feature-workflow" != "agentic". PASS.
- Label not in its own phases (LOW-2, dispatch.go:210-214): phases are `["rapid-plan","rapid-engineer"]`; neither is "feature-workflow". PASS.
- Label not also a mapping label (HIGH-B, dispatch.go:219-221, via `phaseInAnyMapping`): the
  four mapping labels are `rapid-plan`, `rapid-engineer`, `pr-review`, `pr-iterate`;
  "feature-workflow" is none of them. PASS.

### Why #3: Why does each phase pass the per-phase rules?
Loop at dispatch.go:223-246 over `["rapid-plan","rapid-engineer"]`:
- Phase != `trigger_label` (LOW-2, dispatch.go:225-227): neither "rapid-plan" nor
  "rapid-engineer" equals "agentic". PASS.
- Phase resolves on the label ALONE (CRITICAL-2, dispatch.go:232-238, via
  `phaseResolvesAlone` dispatch.go:256-267): `phaseResolvesAlone` returns the mapping whose
  `Labels` is exactly `[phase]`. Mapping 1 is `{labels:["rapid-plan"], agent:"rapid-soldesign-plan"}`
  (single-label, exact match) and mapping 2 is `{labels:["rapid-engineer"], agent:"rapid-implement"}`
  (single-label, exact match). Both resolve. PASS. (No multi-label ANDed mapping is involved,
  so the "on the phase label alone" rejection branch is not reached.)

### Why #4: Why do the phases pass the same-source rule (HIGH-2)?
dispatch.go:239-245 records `workflowSource` from the first phase's resolved mapping and
rejects any later phase whose mapping source differs. The proposed mappings set
`source:"issue"` explicitly on both `rapid-plan` and `rapid-engineer`. Even if `source` were
omitted, `validateDispatchConfig` defaults an empty source to `"issue"` (dispatch.go:165-167)
BEFORE `validateWorkflows` runs (ordering guaranteed by dispatch.go:172 calling
`validateWorkflows` after the mapping loop, as documented at dispatch.go:187-191). Both phases
resolve to `source:"issue"` → identical. PASS. (The `pr`-source mappings `pr-review` and
`pr-iterate` are NOT referenced by this workflow's phases, so they are irrelevant to its
same-source check.)

### Why #5: Why does the cross-file formula-bearing check pass?
`ValidateDispatchConfig` (dispatch.go:93-138) re-resolves each phase via `phaseResolvesAlone`
(dispatch.go:115-129) and rejects a phase whose mapped agent has an empty `Formula`
(dispatch.go:125-127). In `.agentfactory/agents.json`, `rapid-soldesign-plan` has
`formula:"rapid-soldesign-plan"` and `rapid-implement` has `formula:"rapid-implement"` — both
non-empty. Both formula files exist (`internal/cmd/install_formulas/rapid-soldesign-plan.formula.toml`
and `rapid-implement.formula.toml`, also mirrored under `.agentfactory/store/formulas/`).
So the formula-bearing check passes. Every rule passes → the workflow VALIDATES as written.

## Evidence Gathered

| Finding | Source | Evidence |
|---------|--------|----------|
| Workflow struct is `{Label, Phases}` (bare v1 shape) | internal/config/dispatch.go:42-45 | `Label string`; `Phases []string` |
| Rule: workflow label non-empty | internal/config/dispatch.go:195-197 | `if wf.Label == "" { return ... "workflow must have a label" }` |
| Rule: no duplicate workflow label | internal/config/dispatch.go:198-201 | `seen` map; "has duplicate label" |
| Rule: workflow label != trigger_label | internal/config/dispatch.go:202-204 | `if wf.Label == cfg.TriggerLabel` → "collides with trigger_label" |
| Rule: workflow must have ≥1 phase | internal/config/dispatch.go:205-207 | "must have at least one phase" |
| Rule (LOW-2): label not in its own phases | internal/config/dispatch.go:210-214 | `if phase == wf.Label` → "also appears in its phases" |
| Rule (HIGH-B): workflow label != any mapping label | internal/config/dispatch.go:219-221 | `phaseInAnyMapping(cfg.Mappings, wf.Label)` → "must not also be a mapping label" |
| Rule (LOW-2): phase != trigger_label | internal/config/dispatch.go:225-227 | `if phase == cfg.TriggerLabel` → "collides with trigger_label" |
| Rule (CRITICAL-2): phase backs a SINGLE-label mapping | internal/config/dispatch.go:232-238 + 256-267 | `phaseResolvesAlone` requires `len(Labels)==1 && Labels[0]==phase` |
| Rule (HIGH-2): all phases same source (v1) | internal/config/dispatch.go:239-245 | records `workflowSource` from phase 0, rejects mismatch |
| Source defaults to "issue" before workflow check | internal/config/dispatch.go:165-167, 172, 187-191 | empty `Source` set to `"issue"`; `validateWorkflows` runs after mapping loop |
| Cross-file rule: phase agent must have a formula | internal/config/dispatch.go:110-129 | `if entry.Formula == "" { return ... "no formula (cannot signal completion)" }` |
| `rapid-soldesign-plan` agent has formula binding | .agentfactory/agents.json | `formula="rapid-soldesign-plan"`, type=autonomous |
| `rapid-implement` agent has formula binding | .agentfactory/agents.json | `formula="rapid-implement"`, type=autonomous |
| Both phase formulas exist | internal/cmd/install_formulas/{rapid-soldesign-plan,rapid-implement}.formula.toml | files present (also in .agentfactory/store/formulas/) |
| Docs ship the same `feature-workflow` label, structurally identical pipeline | USING_AGENTFACTORY.md:216-238 | `{"label":"feature-workflow","phases":["design","build"]}`; both phases single-label issue mappings |
| Documented example is test-enforced to load+validate | internal/config/using_workflow_doc_test.go:13-41 | `TestUsingDoc_WorkflowsExampleValidates` loads the doc's JSON via `LoadDispatchConfig` |

## Tests Performed

| Test | Command | Result |
|------|---------|--------|
| Existing workflow validation suite passes in this worktree | `go test ./internal/config/ -run 'Workflow\|MixedSource\|PhaseNotResolvable\|DuplicateWorkflow\|LabelPhaseTrigger' -v` (TMPDIR/GOCACHE redirected inside worktree) | PASS — 12 tests incl. `TestLoadDispatchConfig_ValidWorkflow_Loads`, `TestValidateDispatchConfig_WorkflowPhaseAgentWithFormula_OK`, `TestUsingDoc_WorkflowsExampleValidates` |
| Confirm formula bindings for proposed phase agents | `python3` read of `.agentfactory/agents.json` | `rapid-soldesign-plan`→`formula="rapid-soldesign-plan"`; `rapid-implement`→`formula="rapid-implement"` (both non-empty) |
| Confirm doc example uses same workflow shape | `grep -A30 '"workflows"' USING_AGENTFACTORY.md` | `feature-workflow` with two single-label issue-source phases |
| Attempted standalone external-module driver | (abandoned) | Go blocks importing `internal/config` from an external module; the in-package tests above cover the exact shapes — chosen instead of modifying source (read-only constraint) |

## Conclusion

**Verdict: VALIDATED.** The proposed `{label:"feature-workflow", phases:["rapid-plan","rapid-engineer"]}`
workflow passes `dispatch.go` workflow validation as written against the proposed mappings.

Per-rule pass/fail for the proposed workflow:

| Rule | dispatch.go | Result |
|------|-------------|--------|
| Workflow label non-empty | :195-197 | PASS ("feature-workflow") |
| No duplicate workflow label | :198-201 | PASS (single workflow) |
| Workflow label != trigger_label ("agentic") | :202-204 | PASS |
| ≥1 phase | :205-207 | PASS (2 phases) |
| Label not in its own phases | :210-214 | PASS |
| Workflow label != any mapping label | :219-221 | PASS (not rapid-plan/rapid-engineer/pr-review/pr-iterate) |
| Each phase != trigger_label | :225-227 | PASS (both) |
| Each phase backs a single-label mapping (resolves alone) | :232-238, 256-267 | PASS (rapid-plan→rapid-soldesign-plan; rapid-engineer→rapid-implement) |
| All phases same source (v1) | :239-245 | PASS (both source="issue") |
| Each phase agent has a formula (cross-file) | :110-129 | PASS (both formula-bearing in agents.json) |

No rule is violated. Caveat: the cross-file formula-bearing check (`ValidateDispatchConfig`)
is only run by the CLI/external-write path, not by `LoadDispatchConfig` itself; for the
proposed config it passes regardless, because both phase agents have non-empty formula
bindings in agents.json. The structural validity of this workflow is independently corroborated
by the codebase's own `feature-workflow` documentation example and the test that enforces it.
