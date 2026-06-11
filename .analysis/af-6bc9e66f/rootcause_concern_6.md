# Concern #6 Investigation: Trigger condition — real skills dir vs symlink

**Investigated by**: Sub-agent
**Date**: 2026-06-11

## Verdict: VALIDATED

## Summary
The destructive teardown path is gated entirely on the on-disk type of the worktree's `.claude/skills` entry. `cleanupMergedSkills` (worktree.go:507-522) early-returns on anything that is missing or a symlink (the guard at line 511), and only performs `os.RemoveAll` on factory-named entries when `.claude/skills` is a **real directory**. That real-directory state is produced by `EnsureWorktreeLinks` (worktree.go:59-97) precisely when the worktree branch has committed real skill files: `os.Lstat(source)` succeeds with `err == nil`, the entry `IsDir()`, and the merge branch at lines 77-86 runs `mergeSkillsDir` (copying additional factory skills *into* the already-existing real dir) and leaves it a real dir — it never creates a symlink. Conversely, when the worktree has no committed `.claude/skills`, `os.Lstat` fails, and line 92 creates a whole-directory symlink. Therefore: **branch-committed skills => real dir => destructive cleanup runs; no committed skills => symlink => cleanup no-ops.** The trigger condition asserted in the concern is confirmed exactly, both by code trace and by an end-to-end simulation reproducing the two scenarios.

## 5-Whys Analysis

### Why #1: Why does the destructive `os.RemoveAll` loop run for some worktrees but not others?
Because `cleanupMergedSkills` guards on the on-disk type of `worktreePath/.claude/skills`. At worktree.go:510-513 it does `fi, err := os.Lstat(wtSkillsDir); if err != nil || fi.Mode()&os.ModeSymlink != 0 { return }`. The deletion loop at lines 519-521 is reached only when `.claude/skills` exists and is **not** a symlink (i.e., a real directory). A symlink or a missing entry short-circuits to a no-op.

### Why #2: Why is `.claude/skills` a real directory for some worktrees and a symlink for others?
Because `EnsureWorktreeLinks` chooses between symlinking and merging based on `os.Lstat(source)` at worktree.go:69. Three outcomes for `relPath == ".claude/skills"`:
- `err != nil` (source absent): falls through to line 92 `os.Symlink(target, source)` => **symlink**.
- `err == nil` and symlink (71-76): keeps/recreates the symlink => **symlink**.
- `err == nil` and `fi.IsDir()` (77-86): calls `mergeSkillsDir(target, source)` then `continue` — it **never symlinks**, so the entry **stays a real directory**.

### Why #3: Why would `.claude/skills` already exist as a real directory at the time `EnsureWorktreeLinks` runs?
Because `EnsureWorktreeLinks(factoryRoot, absPath)` is called from `Create` at worktree.go:399 *after* `git worktree add` (line 377) has already checked out the branch. If the worktree's branch has **committed** real `.claude/skills/<skill>` files, git materializes them on disk as a real directory before `EnsureWorktreeLinks` runs. So `os.Lstat` succeeds with a real dir, selecting the merge branch (77-86). If the branch has nothing committed under `.claude/skills`, the path is absent and the symlink branch (92) runs.

### Why #4: Why does `unlinkBeforeRemove` not neutralize the destructive branch (i.e., why doesn't removing the symlink before cleanup protect the real-dir case)?
Because `unlinkBeforeRemove` (worktree.go:494-505) also gates on type: it only `os.Remove`s the entry when `fi.Mode()&os.ModeSymlink != 0` (line 501). For a **real directory** it does nothing (no matching branch — `os.Remove` is never called). So in the destructive scenario the real `.claude/skills` survives `unlinkBeforeRemove` and is still a real dir when `cleanupMergedSkills` runs immediately after (Remove: lines 529-530; ForceRemove: lines 560-561). For the symlink scenario, `unlinkBeforeRemove` deletes the symlink first, after which `cleanupMergedSkills`'s `os.Lstat` returns ENOENT and the guard early-returns — so the symlink case is doubly protected (symlink-guard AND prior unlink). Either way, the two functions agree on type and `unlinkBeforeRemove` never converts the dangerous real-dir case into a safe one.

### Why #5 (root): Why does the design delete factory-named entries from the worktree's real `.claude/skills` at teardown at all?
Because the merge feature (commit c8f8702, "merge factory skills into git-tracked worktree skill directories") copies factory skills *into* the worktree's real dir so the agent can use them, and `cleanupMergedSkills` is the symmetric undo intended to remove those *copies* before `git worktree remove` so they don't appear as untracked changes. The root flaw: cleanup identifies "merged copies" purely by **name match against the factory skills dir** (lines 515-520), with no record of which entries were actually copied by `mergeSkillsDir` vs which were branch-committed. `mergeSkillsDir` deliberately *skips* name-colliding entries to preserve branch content (lines 107-109), but cleanup deletes by name regardless of origin — so any branch-committed skill whose name collides with a factory skill is destroyed. The trigger (real dir) is exactly the state in which branch-committed skills exist, which is why the destructive path and the data-loss condition coincide.

## Evidence Gathered

| Finding | Source | Evidence |
|---------|--------|----------|
| cleanup early-returns on symlink/missing | internal/worktree/worktree.go:510-513 | `fi, err := os.Lstat(wtSkillsDir)` then `if err != nil || fi.Mode()&os.ModeSymlink != 0 { return }` |
| cleanup deletes factory-named entries only when real dir | internal/worktree/worktree.go:514-521 | reads factory skills dir, loops `os.RemoveAll(filepath.Join(wtSkillsDir, entry.Name()))` |
| Ensure: absent source => symlink | internal/worktree/worktree.go:69, 92 | `fi, err := os.Lstat(source)`; on `err != nil` falls through to `os.Symlink(target, source)` |
| Ensure: existing real dir => merge, stays real dir | internal/worktree/worktree.go:77-86 | `else if fi.IsDir() && relPath == ".claude/skills" { mergeSkillsDir(...); continue }` — no symlink created |
| Ensure runs after branch checkout in Create | internal/worktree/worktree.go:377, 399 | `git worktree add ... -b branch absPath` at 377; `EnsureWorktreeLinks(factoryRoot, absPath)` at 399 |
| unlinkBeforeRemove only removes symlinks, leaves real dirs | internal/worktree/worktree.go:501-503 | `if fi.Mode()&os.ModeSymlink != 0 { os.Remove(p) }` — no else branch |
| Teardown order: unlink then cleanup, in both Remove paths | internal/worktree/worktree.go:529-530, 560-561 | `unlinkBeforeRemove(absPath)` immediately followed by `cleanupMergedSkills(factoryRoot, absPath)` |
| mergeSkillsDir skips name collisions (preserves branch content) | internal/worktree/worktree.go:107-109 | `if _, err := os.Stat(destPath); err == nil { continue }` |
| Existing test locks in real-dir => destructive cleanup | internal/worktree/worktree_test.go:1815-1847 (TestCleanupMergedSkills_RemovesFactoryCopiedEntries) | factory-named skill-A/skill-B removed from worktree real dir; only git-only-skill survives |
| Existing test locks in symlink => no-op | internal/worktree/worktree_test.go:1849-1872 (TestCleanupMergedSkills_NoOpOnSymlink) | symlink `.claude/skills` "should not panic or remove the symlink"; remains a symlink |
| Existing test: real committed dir takes merge branch, stays real | internal/worktree/worktree_test.go:1748-1813 (TestEnsureWorktreeLinks_RealSkillsDir_MergesFactorySkills) | worktree pre-seeded real `.claude/skills/factory-skill-B`; after Ensure, factory-skill-A merged in, B keeps git-tracked content, dir is real (only `.runtime`/`AGENTS.md` asserted symlinks) |
| Bug introduced with merge feature | git log commit c8f8702 (Fixes #49, PR #50) | "merge factory skills into git-tracked worktree skill directories" — added merge + cleanup logic |

## Tests Performed

| Test | Command | Result |
|------|---------|--------|
| Read full source, trace all four functions | Read internal/worktree/worktree.go (1-773) | Confirmed lstat-based type gating in Ensure (69-94), cleanup (510-521), unlink (501-503); teardown order (529-530, 560-561) |
| Read existing unit tests | Read internal/worktree/worktree_test.go (1462-1911) | Tests already encode: real dir => entries removed; symlink => no-op; real committed dir => merge branch keeps it real |
| Locate callers of the four functions | `grep -rn "cleanupMergedSkills\|mergeSkillsDir\|EnsureWorktreeLinks\|unlinkBeforeRemove" internal/` | Only referenced within worktree.go (Create:399, Remove:529-530, ForceRemove:560-561) and its tests — single, well-bounded code path |
| End-to-end simulation, Scenario A (committed real dir, name collision `code-review`) | `go run /tmp/concern6/sim.go` (faithful replica of the four functions) | Ensure -> "REAL DIR -> mergeSkillsDir (stays a real dir)"; isSymlink=false isDir=true; unlink -> "real dir -> LEFT IN PLACE"; cleanup -> "RemoveAll on [code-review]"; **committed code-review SKILL.md -> GONE**; branch-only SKILL.md -> EXISTS (DESTRUCTIVE CONFIRMED) |
| End-to-end simulation, Scenario B (no committed skills, clean worktree) | `go run /tmp/concern6/sim.go` | Ensure -> "absent -> symlink created"; isSymlink=true; unlink -> "was symlink -> REMOVED symlink"; cleanup -> "EARLY RETURN -> NO-OP"; **factory code-review SKILL.md -> EXISTS** (NO-OP CONFIRMED) |
| Attempt to run package tests directly | `go test ./internal/worktree/ -run ...` | Blocked by sandbox ("fork/exec ... permission denied"); substituted with standalone simulation that replicates the exact lstat/Mode logic |

## Conclusion
VALIDATED. The concern's trigger condition is confirmed exactly. The on-disk type of `worktreePath/.claude/skills` is the sole switch:

- **Branch-committed real skill files => real directory => DESTRUCTIVE.** `git worktree add` (worktree.go:377) materializes committed skills as a real dir; `EnsureWorktreeLinks` takes the `fi.IsDir()` merge branch (77-86) and leaves it a real dir; `unlinkBeforeRemove` leaves real dirs untouched (501-503); `cleanupMergedSkills` passes its symlink guard (510-513) and runs `os.RemoveAll` on every entry whose name matches a factory skill (515-521). Simulation Scenario A shows the branch-committed `code-review/SKILL.md` is deleted while the non-colliding `branch-only` skill survives.

- **No committed skills => symlink => NO-OP.** `EnsureWorktreeLinks` creates a whole-dir symlink (92); `unlinkBeforeRemove` removes that symlink (501-503); `cleanupMergedSkills`'s `os.Lstat` then returns ENOENT (and the symlink-guard would catch it regardless) and early-returns (511). Simulation Scenario B shows nothing is deleted and the factory skill is untouched.

The `unlinkBeforeRemove` interaction does **not** mitigate the bug: it gates on the same type test and only removes symlinks, so it cannot convert the dangerous real-dir case into a safe one — it merely reinforces the safe symlink case. Existing tests `TestCleanupMergedSkills_RemovesFactoryCopiedEntries` (1815-1847) and `TestCleanupMergedSkills_NoOpOnSymlink` (1849-1872) already codify exactly this real-dir-vs-symlink dichotomy, and `TestEnsureWorktreeLinks_RealSkillsDir_MergesFactorySkills` (1748-1813) confirms a committed real dir stays real. The underlying defect is that cleanup identifies "merged copies" by name match against the factory skills dir with no provenance tracking, so any branch-committed skill whose name collides with a factory skill is destroyed whenever the trigger (real `.claude/skills` directory) is met.
