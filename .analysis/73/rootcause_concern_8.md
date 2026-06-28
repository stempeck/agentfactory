# Concern #8 Investigation: trigger_label / AND-matching / remove_trigger_after_dispatch semantics

**Investigated by**: Sub-agent
**Date**: 2026-06-28

## Verdict: VALIDATED

## Summary
The proposed default's semantics are mechanically CORRECT and SAFE: the dispatcher
queries GitHub for ONLY items carrying `trigger_label:"agentic"`, then AND-matches each
mapping's `labels` against the item's labels, so a user must apply BOTH `agentic` AND a
mapping label (e.g. `rapid-plan`) for anything to dispatch. `remove_trigger_after_dispatch:true`
removes ONLY the `agentic` trigger label (not the mapping label) after a successful
non-workflow dispatch, which is the safe choice — it converts the dispatch into a
one-shot (no re-dispatch), eliminating the duplicate-dispatch window entirely while the
`.runtime/dispatch-state.json` 24h TTL + `retry_after_seconds` retry path remain as the
fallback when the trigger label persists. All field names/values in the proposed JSON
match the `DispatchConfig`/`DispatchMapping`/`Workflow` struct json tags exactly (verified
by round-tripping the JSON through the real struct). The concern is VALIDATED in the sense
that the design intent is real and worth confirming, and the implementation honors it —
with TWO caveats that belong in the issue: (1) the proposed JSON has two literal syntax
errors (an unterminated string + missing closing bracket on the first mapping's `"rapid-plan`),
and (2) the requirement "a new user could just tag issues to start work" has a genuine UX
trap — tagging ONLY a mapping label (e.g. `rapid-plan`) WITHOUT `agentic` does NOTHING,
because the trigger label is what makes the item appear in the query at all.

## 5-Whys Analysis

### Why #1: Does `trigger_label` gate dispatch such that an item needs BOTH `agentic` AND a mapping label?
YES. The dispatcher's GitHub query filters by the trigger label BEFORE any mapping is
considered. `queryGitHubIssues`/`queryGitHubPRs` pass `--label <triggerLabel>` to
`gh issue list`/`gh pr list` (`internal/cmd/dispatch.go:178,194,299-305,317-324`). The
returned items are then matched by `matchItemToAgent`, which requires ALL of a mapping's
labels to be present (AND semantics) (`internal/cmd/dispatch.go:335-355`). So an item that
carries `rapid-plan` but NOT `agentic` is never even fetched; an item with `agentic` but no
mapping label is fetched but matches no mapping and is skipped. Effective requirement:
`agentic` AND `rapid-plan`.

### Why #2: Why AND, and is a single-label mapping affected by AND?
`matchItemToAgent` loops a mapping's `m.Labels` and sets `allMatch=false` on the first
absent label (`dispatch.go:342-349`); it returns the agent only if `allMatch && len(m.Labels) > 0`
(`dispatch.go:350-352`). For the proposed mappings (each has exactly ONE label, e.g.
`["rapid-plan"]`), the AND degenerates to "is `rapid-plan` present?". Combined with the
trigger-label pre-filter, the user-facing rule is exactly "`agentic` + one mapping label".
First-match-wins across mappings (`dispatch.go:350` returns on the first satisfied mapping).

### Why #3: What exactly does `remove_trigger_after_dispatch:true` remove, and when?
ONLY the trigger label (`agentic`), and ONLY on the NON-workflow path AFTER a successful
`dispatchItem`. `runDispatch` writes the state record, then `if dispatchCfg.RemoveTriggerAfterDispatch
{ removeTriggerLabel(repo, item.Number, dispatchCfg.TriggerLabel, ...) }`
(`dispatch.go:274-278`). `removeTriggerLabel` runs `gh {issue|pr} edit --remove-label <triggerLabel>`
— the mapping label is untouched (`dispatch.go:370-380`). A failure to remove is a warning,
not an error (`dispatch.go:276`), so dispatch is not rolled back. For WORKFLOWS the trigger
label is NOT removed per-phase; it is removed only at terminal completion alongside the last
phase label (`terminal()`, `dispatch.go:1077-1106`), and phase labels are swapped add+remove
in one atomic `gh edit` by `editItemLabels` during `advance()` (`dispatch.go:1036-1075,389-402`).

### Why #4: Is removing the trigger after dispatch SAFE as a default (re-dispatch / retry interaction)?
YES, and it is the safer of the two options. With removal ON, a dispatched item drops out of
the next query (its `agentic` label is gone), so it cannot be double-dispatched and cannot be
re-tried via the query — it is one-shot. With removal OFF (the field absent, as in today's
install default), the item KEEPS `agentic` and re-dispatch is governed by the
`.runtime/dispatch-state.json` record: a re-dispatch only happens if the agent is idle AND
`time.Since(entry.DispatchedAt) >= retry_after_seconds` (default 1800s)
(`dispatch.go:232-245`), and the record is pruned after 24h (`pruneDispatchState`,
`dispatch.go:1225-1235`). So removal=true trades away the auto-retry-on-failure path for
guaranteed no-duplicate; the explicit `retry_after_seconds:1800` in the proposed default is
effectively dead for non-workflow items once the trigger is gone, but it still governs the
workflow re-sling path (`resling`, `dispatch.go:1120-1126`). Net: safe; the only behavioral
nuance is that a dispatched-but-failed agent run will NOT be auto-retried by re-querying —
the operator/manager (notified via `notify_on_complete`) re-tags. This is a defensible
default for a "fire once, notify a human" UX.

### Why #5: Are the proposed JSON field names/values exactly what the loader expects?
YES for field names. Round-tripping the proposed JSON (with the two literal syntax errors
corrected) through the real `DispatchConfig` struct parses cleanly: `trigger_label`,
`notify_on_complete`, `interval_seconds`, `retry_after_seconds`,
`remove_trigger_after_dispatch`, `mappings[].labels`, `mappings[].source`, `mappings[].agent`,
`workflows[].label`, `workflows[].phases` ALL match the json tags at
`internal/config/dispatch.go:19-45`. The loader uses plain `json.Unmarshal` with NO
`DisallowUnknownFields` (`dispatch.go:58`), so a stray/misspelled field would be silently
ignored rather than erroring — worth knowing, but the proposed fields are all correct. The
four referenced agents (`rapid-soldesign-plan`, `rapid-implement`, `ultra-review`,
`rapid-increment`) all have formulas in `.agentfactory/store/formulas/`, so the cross-file
`ValidateDispatchConfig` agent-existence check passes given matching `agents.json` entries.

## Evidence Gathered
| Finding | Source | Evidence |
|---------|--------|----------|
| Query fetches ONLY trigger-labeled items | `internal/cmd/dispatch.go:178,194` then `:299-305,317-324` | `gh issue/pr list --label <triggerLabel> --state open` |
| Mapping labels AND-matched | `internal/cmd/dispatch.go:337-355` | loop `m.Labels`, `allMatch=false` on first absent; return agent iff `allMatch && len(m.Labels)>0` |
| User needs BOTH agentic + mapping label | composition of query filter + AND match | item w/o `agentic` never fetched; item w/ `agentic` but no mapping label → skipped (`:223-226`) |
| remove_trigger removes ONLY trigger label, post-dispatch, non-workflow | `internal/cmd/dispatch.go:274-278`, `:370-380` | `removeTriggerLabel(... dispatchCfg.TriggerLabel ...)`; `gh edit --remove-label <triggerLabel>` |
| remove failure is warn-not-error | `internal/cmd/dispatch.go:276` | `warning: failed to remove trigger label ...` |
| Non-workflow retry gated by state record + retry_after_seconds (24h prune) | `internal/cmd/dispatch.go:232-245`, `:1225-1235` | retry only if idle AND `time.Since(DispatchedAt) >= retryAfter` |
| Workflow removes trigger only at terminal, swaps phase labels atomically | `internal/cmd/dispatch.go:1059,1097`, `:389-402` | `editItemLabels(... add[next] remove[phase] ...)`; terminal removes `[phase, triggerLabel]` |
| struct json tags match proposed JSON exactly | `internal/config/dispatch.go:19-45` | all 11 proposed keys map to tags; round-trip parse OK |
| Loader tolerates unknown fields (no DisallowUnknownFields) | `internal/config/dispatch.go:58` | plain `json.Unmarshal(data, &cfg)` |
| trigger_label is REQUIRED; mappings REQUIRED; defaults filled | `internal/config/dispatch.go:142-184` | interval→300, retry→1800, notify→"manager" defaults |
| CURRENT install default differs from proposed | `internal/cmd/install.go:145` | `{"repos":[],"trigger_label":"agentic","notify_on_complete":"manager","mappings":[],"interval_seconds":300,"retry_after_seconds":1800}` — empty repos/mappings, NO remove_trigger, NO workflows |
| Empty install default is intentionally "not configured" | `internal/cmd/dispatch.go:1327-1332` | empty mappings → `ErrMissingField` → "skipping dispatch (dispatch.json not configured)" |
| Proposed agents have formulas | `.agentfactory/store/formulas/` | rapid-soldesign-plan, rapid-implement, ultra-review, rapid-increment all present |
| Proposed JSON has literal syntax errors | issue #73 body | `"labels": ["rapid-plan` — unterminated string + missing `]` |

## Tests Performed
| Test | Command | Result |
|------|---------|--------|
| Parse proposed JSON (errors fixed) vs real struct | `go run` of struct round-trip inside worktree | Parsed OK; trigger=agentic remove_trigger=true interval=300 retry=1800; 4 mappings, 1 workflow; all fields match |
| AND-matching semantics | `go test ./internal/cmd -run TestMatchItemToAgent` | PASS (AllLabelsMatch, PartialMatchFails, SingleLabelBackwardCompat, FirstMatchWins, NoMatch, MultiLabel_AND) |
| Workflow cursor / phase swap | `go test ./internal/cmd -run TestWorkflowCursor` | PASS (bootstrap, single, first, ambiguous cases) |
| Config validation | `go test ./internal/config -run 'Dispatch\|Workflow'` | ok (PASS) |
| Full cmd suite | `go test ./internal/cmd` | 2 FAILs, both environmental ("insufficient disk to create worktree: 1GB available") in sling worktree tests — UNRELATED to dispatch matching/trigger logic |

## Conclusion
**Verdict: VALIDATED.** The default semantics are correct and safe as implemented:

1. **trigger_label gating + AND-match is correct.** The dispatcher fetches ONLY `agentic`-labeled
   items (query-level `--label agentic`) and then AND-matches each mapping's `labels`. With
   single-label mappings this means the user applies `agentic` + one mapping label
   (`dispatch.go:178/299/337-355`). This is the intended "both labels required" behavior and is
   test-covered.

2. **remove_trigger_after_dispatch:true is safe** and is the more conservative default: it
   removes ONLY the `agentic` trigger (not the mapping label), makes the dispatch one-shot
   (no double-dispatch, no re-query retry), and degrades gracefully (remove failure is a
   warning). The `retry_after_seconds:1800` value is largely inert for non-workflow items once
   the trigger is removed, but it is harmless and still governs the workflow re-sling ceiling
   path (`dispatch.go:1120-1126`).

3. **No schema field-name or type mismatches.** Every key/value in the proposed JSON maps
   1:1 to the `DispatchConfig`/`DispatchMapping`/`Workflow` json tags
   (`internal/config/dispatch.go:19-45`) and round-trips cleanly. NOTE: the loader has no
   `DisallowUnknownFields`, so a future typo would be silently dropped — not a defect in this
   proposal, but a robustness gap worth a one-line callout.

4. **UX caveats for the issue (recommend documenting, not blocking):**
   - The proposed JSON literal in the issue is malformed: `"labels": ["rapid-plan` is missing
     the closing quote and bracket. Must be fixed to `"labels": ["rapid-plan"]` before it
     parses.
   - The requirement narrative ("a new user could just tag issues to start work") has a real
     trap: tagging ONLY a mapping label (e.g. `rapid-plan`) WITHOUT `agentic` does NOTHING,
     because the trigger label is the query gate. New users will likely expect a single
     `rapid-plan` tag to suffice. This should be called out in docs/onboarding (the two-label
     requirement is correct and intentional, but non-obvious).
   - Today's shipped install default (`install.go:145`) is the EMPTY/"not configured" form
     (empty repos+mappings, no `remove_trigger_after_dispatch`, no workflows). Adopting the
     proposed richer default requires changing `install.go` AND making the literal
     repo-name-substituted at install time, as the issue itself notes. The proposed default
     is schema-valid and will load and validate cleanly once `repos`/`mappings` are populated
     and the JSON syntax is fixed.
