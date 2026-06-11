# Concern #4 Investigation: All teardown entry points reach cleanupMergedSkills

**Investigated by**: Sub-agent
**Date**: 2026-06-11

## Verdict: VALIDATED

## Summary
Every worktree-teardown entry point in the codebase funnels through exactly one of
two destructive functions, `Remove` (worktree.go:526) or `ForceRemove`
(worktree.go:558), and BOTH unconditionally call `cleanupMergedSkills`
(worktree.go:530 and :561). I traced the five claimed paths and confirmed that
four of the five reach `Remove`/`ForceRemove` directly. The fifth claimed path —
`af sling --reset` — is split: the specialist-dispatch form (`--agent ... --reset`)
DOES reach teardown via `resetAgentState → ForceRemove`, but the formula form
(`--formula ... --reset`) does NOT call teardown (it only closes beads and wipes
`.runtime`). I also found a SIXTH reachable trigger not in the original list:
`ResolveOrCreate` (worktree.go:686) calls `GC()` during normal agent startup, so
teardown — and therefore `cleanupMergedSkills` — can fire while merely starting a
new agent. The concern that all teardown paths share the single destructive
function is correct: there is no teardown route that bypasses
`cleanupMergedSkills`, and no flag/parameter to suppress it.

## 5-Whys Analysis

### Why #1: Why do all five teardown entry points reach `cleanupMergedSkills`?
Because there are only two teardown primitives — `Remove` and `ForceRemove` — and
each one hard-codes a call to `cleanupMergedSkills` as the first action after
`unlinkBeforeRemove`:
- `Remove` calls it at worktree.go:530.
- `ForceRemove` calls it at worktree.go:561.
Every CLI path that tears a worktree down must call one of these two functions, so
each path inherits the `cleanupMergedSkills` call transitively. There is no
third teardown function and no conditional that skips the call.

### Why #2: Why does each CLI path call `Remove` or `ForceRemove`?
Each command was wired directly to the primitive it needed:
- `af down [agent]` → `cleanupAgentWorktree` (down.go:138) → `worktree.Remove`
  (down.go:153) when `RemoveAgent` reports the agents list is empty (down.go:147-152).
- `af down --reset` → `resetAgent` (down.go:79, :92) → `resetAgentState`
  (down.go:193) → `worktree.ForceRemove` (reset.go:32).
- `af sling --agent <name> --reset` → `resetAgentState` (sling.go:139) →
  `worktree.ForceRemove` (reset.go:32).
- formula-completion `af done` → `worktree.Remove` (done.go:263) when the owner's
  `RemoveAgent` reports empty (done.go:258-266).
- `GC()` (worktree.go:638) → `worktree.ForceRemove` for each stale worktree;
  `GC` is invoked from `ResolveOrCreate` (worktree.go:686) during agent startup.
The single-agent vs. force distinction (uncommitted-change tolerance) is the only
reason two primitives exist; both still need to clean the worktree dir, so both
were given the same skill-cleanup step.

### Why #3: Why is the skill cleanup baked into the teardown primitive rather than the call sites?
Because skill cleanup is conceptually "part of removing a worktree." The factory
copies (rather than symlinks) certain skills into each worktree's `.claude/skills`
during setup, and the author wanted teardown to undo that copy so that
`git worktree remove` would not choke on factory-injected files. Placing the
cleanup inside `Remove`/`ForceRemove` guarantees it runs for every teardown without
each call site having to remember to do it — a DRY/centralization decision.

### Why #4: Why is the centralized cleanup destructive to branch-committed files?
`cleanupMergedSkills` (worktree.go:507-522) cannot distinguish a factory-copied
skill from a skill the agent committed on its branch. It enumerates the FACTORY
root's `.claude/skills` entries (worktree.go:515) and then `os.RemoveAll`s any
entry by the SAME NAME inside the worktree (worktree.go:519-520). It keys purely on
name collision against the factory root — it does not check git tracking, file
hash, or provenance. So a branch-committed skill whose directory name happens to
match a factory skill is deleted as collateral damage. The function's only guard is
that it skips when the worktree `.claude/skills` is itself a symlink
(worktree.go:511-512).

### Why #5 (root cause): Why does the centralized destructive function lack provenance discrimination?
Because the design treated "skill present in worktree with a factory-root
counterpart" as definitionally a factory-injected copy that is safe to delete. The
mental model was "we copied it in, so we copy it out," and that model never
accounted for the case where the agent's own branch legitimately adds/modifies a
skill of the same name. The root cause is a missing provenance/ownership check in
`cleanupMergedSkills`: it operates on name collision alone, with no concept of "did
this worktree branch commit this file?" Combined with the Why #1-#3 centralization
decision, this single un-guarded function is reached by EVERY teardown path, so the
defect has factory-wide blast radius rather than being isolated to one command.

## Evidence Gathered

| Finding | Source | Evidence |
|---------|--------|----------|
| `cleanupMergedSkills` deletes any worktree skill whose name matches a factory-root skill | internal/worktree/worktree.go:514-520 | `entries, _ := os.ReadDir(factorySkillsDir)` then `os.RemoveAll(filepath.Join(wtSkillsDir, entry.Name()))` |
| `Remove` calls cleanupMergedSkills unconditionally | internal/worktree/worktree.go:530 | `cleanupMergedSkills(factoryRoot, absPath)` |
| `ForceRemove` calls cleanupMergedSkills unconditionally | internal/worktree/worktree.go:561 | `cleanupMergedSkills(factoryRoot, absPath)` |
| PATH 1 — `af down [agent]` reaches Remove | internal/cmd/down.go:147,152-153 | `RemoveAgent(...)` → `if empty { worktree.Remove(factoryRoot, updated) }` |
| PATH 2 — `af down --reset` reaches ForceRemove | internal/cmd/down.go:79,92,192-193 → reset.go:32 | `resetAgent → resetAgentState → worktree.ForceRemove(factoryRoot, updated)` |
| PATH 3a — `af sling --agent --reset` reaches ForceRemove | internal/cmd/sling.go:135,139 → reset.go:32 | `if slingReset { ... resetAgentState(...) }` → `ForceRemove` |
| PATH 3b — `af sling --formula --reset` does NOT reach teardown | internal/cmd/sling.go:284-298 | only `closeAgentBeads`, `os.RemoveAll(.runtime)`, `checkpoint.Remove`; no Remove/ForceRemove call |
| PATH 4 — `af done` formula completion reaches Remove | internal/cmd/done.go:252,258-263 | `if shouldTerminate { ... if isWorktreeOwner ... if empty { worktree.Remove(factoryRoot, meta) } }` |
| PATH 5 — `GC()` reaches ForceRemove | internal/worktree/worktree.go:638 | `if err := ForceRemove(factoryRoot, meta); err != nil` inside GC loop |
| SIXTH trigger — `GC` runs during agent startup | internal/worktree/worktree.go:686 | `ResolveOrCreate` calls `_, _ = GC(factoryRoot)` before `Create`, so teardown of stale worktrees fires on normal startup |
| No teardown path bypasses cleanupMergedSkills | grep over internal/cmd + internal/worktree | only call sites of cleanupMergedSkills are worktree.go:530 and :561; every Remove/ForceRemove caller enumerated above |
| No flag/param to suppress skill cleanup | internal/worktree/worktree.go:526,558 | `Remove(factoryRoot, meta)` / `ForceRemove(factoryRoot, meta)` signatures carry no skip-cleanup option |

## Tests Performed

| Test | Command | Result |
|------|---------|--------|
| Enumerate all Remove/ForceRemove/cleanupMergedSkills references | `grep -rn "Remove\|ForceRemove\|cleanupMergedSkills" internal/cmd internal/worktree` | Confirmed only worktree.go:530,561 call cleanupMergedSkills; all CLI callers identified |
| Confirm both primitives call cleanupMergedSkills | Read worktree.go:526-582 | Verified `Remove` @530 and `ForceRemove` @561 both call it, first thing after unlinkBeforeRemove |
| Trace down.go path | Read down.go:60-162 | `cleanupAgentWorktree` → `Remove` (non-reset); `resetAgent` → `resetAgentState` (reset) |
| Trace reset.go path | Read reset.go:1-58 | `resetAgentState` → `ForceRemove` @32 when agents list empty |
| Trace sling.go reset paths | Read sling.go:130-149, 282-298 | `--agent --reset` reaches ForceRemove; `--formula --reset` does NOT call teardown |
| Trace done.go path | Read done.go:230-288 | `Remove` @263 reached when shouldTerminate && isWorktreeOwner && empty |
| Trace GC callers | `grep -rn "GC("` + Read worktree.go:609-645,668-689 | GC → ForceRemove @638; GC invoked by ResolveOrCreate @686 (startup) |

## Conclusion
VALIDATED. The destructive function `cleanupMergedSkills` is reached by every
worktree-teardown route because both teardown primitives (`Remove`, `ForceRemove`)
hard-code the call (worktree.go:530, :561) and there is no third teardown function
and no flag to skip it. Of the five claimed entry points, four reach teardown
directly: `af down [agent]` (down.go:153, via `Remove`), `af down --reset`
(reset.go:32, via `ForceRemove`), `af done` formula completion (done.go:263, via
`Remove`), and `GC()` (worktree.go:638, via `ForceRemove`). The fifth, `af sling
--reset`, reaches teardown ONLY in its specialist-dispatch form (`--agent ...
--reset` → sling.go:139 → ForceRemove); its formula form (`--formula ... --reset`,
sling.go:284-298) does NOT — that nuance is noted for accuracy but does not weaken
the concern, since the agent-dispatch form does reach teardown. Additionally, I
found a SIXTH path the original list omitted: `GC` is also invoked from
`ResolveOrCreate` (worktree.go:686) during ordinary agent startup, so
`cleanupMergedSkills` can run even when no explicit teardown command was issued —
widening the blast radius. The root cause (Why #5) is that `cleanupMergedSkills`
keys deletion on name collision against the factory root
(worktree.go:514-520) with no provenance/ownership check, and the centralization of
this un-guarded logic inside the shared primitives gives the defect factory-wide
reach.
