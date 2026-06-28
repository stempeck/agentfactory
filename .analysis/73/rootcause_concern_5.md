# Concern #5 Investigation: Mapped formulas' dispatch input-compatibility

**Investigated by**: Sub-agent
**Date**: 2026-06-28

## Verdict: INVALIDATED

## Summary
All four formulas mapped in the proposed issue-#73 dispatch.json
(`rapid-soldesign-plan`, `rapid-implement`, `ultra-review`, `rapid-increment`)
accept exactly ONE required input that has no default, and that single input is
satisfiable from the labeled issue/PR URL that the dispatcher supplies as the
positional `task` argument. The dispatcher slings `af sling --agent <name>
--reset <itemURL>` (`internal/cmd/dispatch.go:422-426`), and the sling
input-bridging logic fills the one unsatisfied required input with that URL
(`internal/cmd/sling.go:472-484` for `[inputs]`-bearing formulas; the
auto-assignment-bead path at `internal/cmd/sling.go:432-444` for rapid-implement,
which has no `[inputs]` table and consumes the URL through its bead). No mapped
formula has more than one required-without-default input, so none of these
mappings is broken by the "multiple required inputs" failure mode. The concern's
hypothesis — that a mapped formula (e.g. rapid-implement needing `outline_path` /
a plan artifact) requires more than the issue/PR and would fail to dispatch
autonomously — is NOT borne out by the code. The concern is INVALIDATED.

## 5-Whys Analysis

### Why #1: Why might a label-dispatch fail to run a mapped formula autonomously?
Because a label-dispatch supplies ONLY the labeled issue/PR URL. If the mapped
formula declares MORE than one required input with no default, sling cannot fill
them all and errors out before instantiation. This is enforced explicitly:
`instantiateFormulaWorkflow` returns an error when
`findUnsatisfiedRequiredInputs` returns >1 (`internal/cmd/sling.go:477-483`):
`"formula %q has %d required inputs not provided ... provide all but one via
--var flags, the remaining one receives the positional text argument"`.

### Why #2: Why would a formula have multiple required inputs that the URL can't fill?
Because a formula could require both the problem item AND a separately-produced
artifact (the concern's example: rapid-implement needing `outline_path` or a plan
artifact from rapid-soldesign-plan). So the question reduces to: does ANY mapped
formula declare a second required-without-default input?

### Why #3: Why does each mapped formula end up with only ONE unsatisfiable input?
Because `findUnsatisfiedRequiredInputs` (`internal/cmd/sling.go:872-895`) skips an
input when it has a non-empty `Default` (line 875: `if !inp.Required ||
inp.Default != ""`). rapid-soldesign-plan declares four `required=true` inputs,
but three of them (`analyst_name`, `designer_name`, `impl_name`) carry defaults,
so only `issue_uri` remains unsatisfied — exactly one. ultra-review and
rapid-increment declare a single required `pr_uri` (their other input,
`min_confidence`, is `required=false` with a default). rapid-implement has NO
`[inputs]` table at all; its `issue` comes from a `[vars.issue]` with
`source=cli`, satisfied by the auto-created assignment bead, not the inputs-bridge.

### Why #4: Why does rapid-implement (no `[inputs]`) still receive the URL correctly?
Because the inputs-bridge block is guarded by `len(f.Inputs) > 0`
(`internal/cmd/sling.go:473`), which is false for rapid-implement. Instead, the
earlier auto-bead path fires: when `TaskDescription != "" && cliVars["issue"] ==
""` (`internal/cmd/sling.go:432`), sling creates an assignment bead carrying the
URL in its description and sets `cliVars["issue"] = iss.ID`
(`internal/cmd/sling.go:442`). rapid-implement's `load-context` step then reads
the bead and classifies the input mode (issue vs PR link) from its content
(rapid-implement.formula.toml:88-110). So the single `issue` var is satisfied and
the URL is delivered. Both bridging mechanisms converge on "single input gets the
URL."

### Why #5: Why doesn't the feature-workflow (rapid-plan → rapid-engineer) need an
explicit plan-artifact `--var` to bridge the two phases?
Because rapid-implement does NOT take a `plan`/`outline_path` input at all — it is
self-classifying. The proposed dispatch.json wires the two-phase pipeline through
GitHub labels + the workflow engine, not through a CLI var. The `feature-workflow`
workflow (`label: feature-workflow`, `phases: [rapid-plan, rapid-engineer]`)
hands the SAME issue URL to BOTH phases: after `rapid-plan` (rapid-soldesign-plan)
completes, the workflow advances the label and re-slings `rapid-engineer`
(rapid-implement) with the same `item.URL`
(`internal/cmd/dispatch.go:1159-1177` `slingPhase`; URL re-used at line 1161).
The handoff of the produced plan happens through the GitHub PR that
rapid-soldesign-plan opens (its `commit-and-pr` step opens a design PR and its
`dispatch-impl` step pushes `implementation_plan_outline.md` onto that PR branch),
which the next consumer reads from GitHub — NOT through a formula `--var`. So no
multi-required-input bridging is needed, and the concern's worry that
rapid-implement needs an extra `outline_path` var is unfounded.

## Evidence Gathered

| Finding | Source | Evidence |
|---------|--------|----------|
| Dispatcher slings the URL as the single positional task arg | `internal/cmd/dispatch.go:422-426` | `args := []string{"sling", "--agent", agent, "--reset"}` ... `args = append(args, itemURL)` |
| dispatchToSpecialist appends `task=<url>` and routes to instantiation | `internal/cmd/sling.go:220-226` | `CLIVars: append(slingVars, fmt.Sprintf("task=%s", task))` |
| Input-bridging rule: text fills the SINGLE unsatisfied required input | `internal/cmd/sling.go:472-484` | `if len(unsatisfied)==1 { cliVars[unsatisfied[0]] = params.TaskDescription }` |
| Multiple unsatisfied required inputs => hard error (not autonomous) | `internal/cmd/sling.go:477-483` | `else if len(unsatisfied) > 1 { return ... "%d required inputs not provided" }` |
| Inputs with a default are NOT counted as unsatisfied | `internal/cmd/sling.go:875` | `if !inp.Required \|\| inp.Default != "" { continue }` |
| Auto-bead path satisfies `issue` for formulas without `[inputs]` | `internal/cmd/sling.go:432-444` | `if params.TaskDescription != "" && cliVars["issue"] == "" { ... cliVars["issue"] = iss.ID }` |
| rapid-soldesign-plan: one required-no-default input `issue_uri`; other 3 required inputs carry defaults | `rapid-soldesign-plan.formula.toml:83-105` | `issue_uri` required, no default; `analyst_name`/`designer_name`/`impl_name` required WITH `default=` |
| rapid-implement: no `[inputs]`; single `[vars.issue]` source=cli | `rapid-implement.formula.toml:913-917` | `[vars.issue] ... required = true ... source = "cli"` (grep `^\[inputs` count = 0) |
| rapid-implement is self-classifying (issue OR pr link), needs no plan var | `rapid-implement.formula.toml:88-110` | load-context classifies MODE=pr/issue from the bead input; no `outline_path` input exists |
| ultra-review: one required-no-default input `pr_uri`; `min_confidence` optional+default | `ultra-review.formula.toml:112-122` | `pr_uri` required; `min_confidence` `required=false` `default="70"` |
| rapid-increment: single required-no-default input `pr_uri` | `rapid-increment.formula.toml:995-999` | `pr_uri` required, no default; no other cli vars |
| All four formulas infer `TypeWorkflow` (have `[[steps]]`) so inputs-bridge path applies | `internal/formula/parser.go:37-51` | `if len(f.Steps) > 0 { f.Type = TypeWorkflow }` |
| Each mapped agent name resolves 1:1 to the same-named formula in agents.json | `.agentfactory/agents.json` | `rapid-soldesign-plan→rapid-soldesign-plan`, `rapid-implement→rapid-implement`, `ultra-review→ultra-review`, `rapid-increment→rapid-increment` |
| Workflow engine re-uses the SAME item URL across phases (no var handoff) | `internal/cmd/dispatch.go:1159-1177` | `slingPhase` calls `dispatchItem(w.root, agent, w.item.URL, ...)` for every phase |
| Plan→impl artifact handoff is via the GitHub PR, not a CLI var | `rapid-soldesign-plan.formula.toml:585-654, 656-734` | `dispatch-impl` slings impl agent with the PR link; `finalize` verifies `implementation_plan_outline.md` on the PR branch |

## Tests Performed

| Test | Command | Result |
|------|---------|--------|
| Enumerate required-no-default inputs + cli vars per formula | python TOML scan over the 4 `.formula.toml` files | rapid-soldesign-plan: only `issue_uri`; rapid-implement: only `issue` (var); ultra-review: only `pr_uri`; rapid-increment: only `pr_uri` — each exactly ONE |
| Confirm dispatcher argv passes URL positionally | `grep -n 'args := \[\]string{"sling"' internal/cmd/dispatch.go` | dispatch.go:422 `{"sling","--agent",agent,"--reset"}` + `append(args, itemURL)` |
| Confirm agent→formula 1:1 mapping | `jq '.agents[$a].formula' .agentfactory/agents.json` for all 4 | each maps to its same-named formula |
| Confirm formula type inference (workflow) | read `internal/formula/parser.go:37-51` | `[[steps]]` ⇒ TypeWorkflow for all 4 |
| Confirm default-bearing required inputs are skipped by bridging | read `findUnsatisfiedRequiredInputs` (`sling.go:872-895`) | line 875 short-circuits on non-empty Default |

## Conclusion

**Verdict: INVALIDATED.** The concern hypothesizes that one or more mapped
formulas (specifically rapid-implement, suspected of needing an `outline_path` /
plan artifact) require MORE than the labeled issue/PR and would therefore fail to
run autonomously under label-dispatch. The codebase does not support this: every
mapped formula has exactly one required-without-default input, all four are
satisfiable from the single issue/PR URL the dispatcher supplies, and the
plan→impl handoff is mediated by GitHub PRs (label-driven workflow advance),
never by a second required CLI var.

### Per-agent input-compatibility table

| Mapped agent (→ formula) | Source | Required inputs WITHOUT default | Optional / defaulted inputs | URL-only dispatch satisfies all required? | Label-dispatchable as-is? |
|--------------------------|--------|---------------------------------|------------------------------|--------------------------------------------|----------------------------|
| rapid-soldesign-plan → rapid-soldesign-plan | issue | `issue_uri` (1) | `analyst_name`, `designer_name`, `impl_name` (required but defaulted) | YES — `issue_uri` ← URL via inputs-bridge | YES |
| rapid-implement → rapid-implement | issue | `issue` (1, via `[vars]` source=cli; no `[inputs]`) | — | YES — `issue` ← auto-assignment bead carrying the URL | YES |
| ultra-review → ultra-review | pr | `pr_uri` (1) | `min_confidence` (`required=false`, default 70) | YES — `pr_uri` ← URL via inputs-bridge | YES |
| rapid-increment → rapid-increment | pr | `pr_uri` (1) | — | YES — `pr_uri` ← URL via inputs-bridge | YES |

**No mapped formula needs more than the issue/PR to dispatch.** The
"multiple-required-inputs" failure mode (`sling.go:477-483`) is NOT triggered by
any of the four proposed mappings.
