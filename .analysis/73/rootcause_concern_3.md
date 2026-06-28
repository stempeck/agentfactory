# Concern #3 Investigation: Default label→agent mappings — correctness & validation

**Investigated by**: Sub-agent
**Date**: 2026-06-28

## Verdict: VALIDATED

## Summary
The four proposed mappings are **structurally well-formed** and pass the struct-level
validator `validateDispatchConfig` (`internal/config/dispatch.go:141-185`): each has a
non-empty `labels`, an `agent`, and a `source` of `"issue"` or `"pr"` — the only two
allowed values. The source-vs-formula semantics are also correct: `rapid-soldesign-plan`
is genuinely issue-driven, `rapid-implement` accepts an issue, and both `ultra-review` and
`rapid-increment` are genuinely PR-driven (they take a `pr_uri`). **However, the mappings
do NOT pass the cross-file validator `ValidateDispatchConfig` (dispatch.go:93-138) on a
fresh install, and would fail at dispatch time**, because `mappings[].agent` must name a
registered agent in `agents.json` — and a fresh install ships `agents.json` with only
`manager` and `supervisor` (`internal/cmd/install.go:144`). The values
`rapid-soldesign-plan` / `rapid-implement` / `ultra-review` / `rapid-increment` are
**formula names**, not registered agent names. Unless issue #73's implementation ALSO seeds
agents.json with agents bearing those exact names (the `af formula agent-gen` convention
defaults agent name = formula name, so this is achievable), every proposed mapping
references an unknown agent. The mappings are therefore correct in shape and semantics but
incomplete as a standalone change: they require a paired agents.json seeding to be valid.

## 5-Whys Analysis

### Why #1: Why do the proposed mappings pass struct-level validation?
Because `validateDispatchConfig` (`dispatch.go:151-171`) only enforces: not both `label` and
`labels` (`:152`), at least one label (`:159`), `source` ∈ {"" , "issue", "pr"} with ""
defaulting to "issue" (`:162-167`), and a non-empty `agent` (`:168`). All four proposed
mappings satisfy every clause: each supplies `labels` (singular array), an explicit
`source` that is exactly `"issue"` or `"pr"`, and a non-empty `agent`. There is no
duplicate-label rule and no per-mapping label-uniqueness rule at the struct level (the only
duplicate checks are for `workflows[].label`, `dispatch.go:198`), so four distinct
single-label mappings collide with nothing.

### Why #2: Why are `source:"issue"` and `source:"pr"` both accepted?
Because the source whitelist at `dispatch.go:162` permits exactly `"issue"` and `"pr"`
(empty defaults to `"issue"` at `:165-166`). The dispatcher consumes `source` in
`groupMappingsBySource` (`internal/cmd/dispatch.go:357-366`): `source=="pr"` routes the
mapping to the PR query (`queryGitHubPRs`), everything else to the issue query
(`queryGitHubIssues`). So `source` is the issue-vs-PR selector, and both proposed source
values are valid and meaningful.

### Why #3: Why are the source assignments semantically correct per formula?
Cross-checked each formula's input contract in `internal/cmd/install_formulas/`:
- `rapid-soldesign-plan` — "rapid design refinement **from a GitHub issue** URI"
  (`rapid-soldesign-plan.formula.toml:3`); its sling line takes `--var issue_uri=<github-issue-url>`
  (`internal/templates/roles/rapid-soldesign-plan.md.tmpl:19`). → **issue-source correct.**
- `rapid-implement` — "Requirements come from the assigned bead — which may contain ... a
  link to a GitHub issue, or a link to a GitHub pull request" (`rapid-implement.formula.toml:6`);
  variable `issue` "The issue/PR ID you're assigned to work on" (`:23`). It accepts an issue,
  so `source:"issue"` is valid. → **issue-source correct (flexible; issue is a legitimate input).**
- `ultra-review` — "Multi-agent **pull request** review"; variable `pr_uri` "Pull request to
  review" (`ultra-review.formula.toml:33`). → **pr-source correct.**
- `rapid-increment` — "addresses the UNRESOLVED review comments on a **pull request**";
  variable `pr_uri` (`rapid-increment.formula.toml:27`). → **pr-source correct.**

### Why #4: Why do the mappings nonetheless fail the FULL validator / dispatch?
Because the second validator, `ValidateDispatchConfig` (`dispatch.go:93-138`), cross-checks
every `mapping.agent` against `agents.json`: `if _, ok := agents.Agents[m.Agent]; !ok {
return ... "dispatch mapping references unknown agent %q" }` (`dispatch.go:100-104`). At
dispatch time the same gate is enforced harder: `matchItemToAgent` returns the raw
`m.Agent` string (`internal/cmd/dispatch.go:351`), the dispatcher slings it via
`af sling --agent <name>` (`dispatch.go:422`), and `resolveSpecialistAgent`
(`sling.go:252-269`) fails with `agent %q not found in agents.json` (`:261`) if the name is
not a registered agent — and with `not a specialist (no formula field)` (`:265`) if it has
no formula. A fresh install's `agents.json` contains only `manager` and `supervisor`
(`internal/cmd/install.go:144`), neither of which matches any proposed `agent`. So all four
mappings reference unknown agents on a fresh install.

### Why #5: Why is the `agent` field a name-not-formula mismatch, and is it fixable?
The `agent` field is an agents.json key (a registered agent), NOT a formula name
(`DispatchMapping.Agent`, `dispatch.go:34`; consumed as a sling `--agent` value). The four
proposed values are formula names. They become valid agent names only if agents bearing
those exact names are registered — which is exactly what `af formula agent-gen <formula>`
does: it defaults the agent name to the formula name (`formula.go:62`, `:145`, `:235`) and
writes an AgentEntry with `Formula: f.Name` into agents.json. So issue #73 is correct in
INTENT (the names follow the agent-gen convention), but the dispatch.json mappings alone are
insufficient: the change must ALSO seed agents.json with the four agents (or document that
the operator runs `af formula agent-gen` for each), or the dispatcher will reject every item.

## Evidence Gathered
| Finding | Source | Evidence |
|---------|--------|----------|
| `source` whitelist is exactly {issue, pr}; empty→issue | `internal/config/dispatch.go:162-167` | `if m.Source != "" && m.Source != "issue" && m.Source != "pr" { return ... }` |
| Mapping struct rules: not both label+labels; ≥1 label; non-empty agent | `internal/config/dispatch.go:152,159,168` | three returns under the mappings loop |
| Singular `label` is migrated to `labels` and cleared | `internal/config/dispatch.go:155-158` | `cfg.Mappings[i].Labels = []string{m.Label}` |
| NO struct-level duplicate-label / label-uniqueness rule for mappings | `internal/config/dispatch.go:151-171` | only workflow labels get a `seen` dup check (`:198`) |
| Cross-file validator rejects mapping agent not in agents.json | `internal/config/dispatch.go:100-104` | `"dispatch mapping references unknown agent %q"` |
| `source` drives issue-vs-PR query routing | `internal/cmd/dispatch.go:357-366` | `if m.Source == "pr" { prs = append(...) } else { issues = ... }` |
| Dispatch slings the raw agent name; must be a registered specialist | `internal/cmd/dispatch.go:351,422`; `internal/cmd/sling.go:259-266` | `return m.Agent`; `"sling","--agent",agent`; `"agent %q not found in agents.json"` / `"not a specialist"` |
| Fresh-install agents.json = only manager + supervisor | `internal/cmd/install.go:144` | `"agents.json": {"agents":{"manager":...,"supervisor":...}}` |
| Fresh-install dispatch.json = empty mappings | `internal/cmd/install.go:145` | `"dispatch.json": {... "mappings":[], ...}` |
| rapid-soldesign-plan is issue-sourced | `internal/cmd/install_formulas/rapid-soldesign-plan.formula.toml:3`; `.../roles/rapid-soldesign-plan.md.tmpl:19` | "from a GitHub issue URI"; `--var issue_uri=<github-issue-url>` |
| rapid-implement accepts an issue input | `internal/cmd/install_formulas/rapid-implement.formula.toml:6,23` | "a link to a GitHub issue, or ... pull request"; var `issue` |
| ultra-review is PR-sourced | `internal/cmd/install_formulas/ultra-review.formula.toml:33` | var `pr_uri` "Pull request to review" |
| rapid-increment is PR-sourced | `internal/cmd/install_formulas/rapid-increment.formula.toml:27` | var `pr_uri`; "review comments on a pull request" |
| agent-gen defaults agent name = formula name | `internal/cmd/formula.go:62,145,235` | `"Override agent name (default: formula name)"`; `agentName := formulaName`; `Formula: f.Name` |

## Tests Performed
| Test | Command | Result |
|------|---------|--------|
| Dispatch config validation unit tests | `GOTMPDIR=$PWD/.gotmp go test ./internal/config/ -run Dispatch` | `ok` (PASS) — validators behave as documented |
| Confirm source whitelist values | read `dispatch.go:162` | only `"issue"`/`"pr"`/`""` accepted; all 4 proposed sources valid |
| Confirm agent field is cross-checked vs agents.json | read `dispatch.go:100-104` + `sling.go:259-266` | unknown agent → error at validate AND at dispatch time |
| Confirm fresh-install agents.json contents | read `install.go:144` | only `manager`, `supervisor` — none of the 4 proposed agents present |

## Conclusion

**Verdict: VALIDATED** — the concern (no default mappings exist; assess proposed mappings'
correctness and validation) is real and the investigation produces an actionable per-mapping
assessment.

Per-mapping assessment:

| # | Proposed mapping | Struct validation (`validateDispatchConfig`) | Source semantics | Cross-file validation (`ValidateDispatchConfig` + dispatch) |
|---|---|---|---|---|
| 1 | `{labels:[rapid-plan], source:issue, agent:rapid-soldesign-plan}` | PASS | CORRECT — issue-driven planner | **FAILS on fresh install** — `rapid-soldesign-plan` not in agents.json |
| 2 | `{labels:[rapid-engineer], source:issue, agent:rapid-implement}` | PASS | CORRECT — rapid-implement accepts an issue | **FAILS on fresh install** — `rapid-implement` not in agents.json |
| 3 | `{labels:[pr-review], source:pr, agent:ultra-review}` | PASS | CORRECT — PR reviewer | **FAILS on fresh install** — `ultra-review` not in agents.json |
| 4 | `{labels:[pr-iterate], source:pr, agent:rapid-increment}` | PASS | CORRECT — addresses PR comments | **FAILS on fresh install** — `rapid-increment` not in agents.json |

Key findings:
1. **All four mappings are structurally valid and semantically correct** on source (issue vs
   PR) and agent-intent. No mapping has a wrong source, no nonexistent-as-a-formula name, and
   no duplicate-label collision.
2. **The blocker is the agent/formula-name distinction.** `mappings[].agent` is a registered
   agents.json key (slung as `af sling --agent <name>`), not a formula name. The proposed
   values are formula names. On a fresh install agents.json holds only `manager` and
   `supervisor`, so `ValidateDispatchConfig` rejects all four with
   `"dispatch mapping references unknown agent"`, and even if struct-saved, the dispatcher
   would error `agent ... not found in agents.json` at dispatch time.
3. **The fix is paired, not standalone.** Issue #73 must ALSO seed agents.json with four
   formula-bearing agents named exactly `rapid-soldesign-plan`, `rapid-implement`,
   `ultra-review`, `rapid-increment` (the `af formula agent-gen` convention already makes
   agent name = formula name, so the names chosen are right), OR the proposed mappings must
   reference whatever agent names the install flow actually registers. Shipping the mappings
   without the agents would make a fresh install's dispatch.json fail validation/dispatch.
