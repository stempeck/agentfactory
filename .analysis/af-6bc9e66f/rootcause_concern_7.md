# Concern #7 Investigation: Ordering hazard — destructive cleanup before non-force git remove

**Investigated by**: Sub-agent
**Date**: 2026-06-11

## Verdict: VALIDATED

## Summary
`Remove` (worktree.go:526-553) calls `cleanupMergedSkills` at line 530 — which
deletes branch-committed skill files from the worktree's working tree — BEFORE
running `git worktree remove` at line 533. That git command carries NO `--force`
flag (line 533), and git refuses by default to remove a worktree containing
modified files. The just-performed deletions register as ` D` (deleted, unstaged)
entries, so the non-force remove FAILS with exit 128 (`fatal: '<wt>' contains
modified or untracked files, use --force to delete it`). The early `return` at
line 536 then aborts the whole teardown, leaving the worktree alive on disk in a
corrupted state: its HEAD still references the committed skill files while its
working tree no longer contains them. This exactly reproduces the OBSERVED state
in issue #59 — a live worktree showing ` D` skill entries. The same
cleanup-before-remove ordering exists in `ForceRemove` (lines 561-562), but its
`--force` flag (line 562) masks the symptom there, so the live-corrupted-worktree
outcome is specific to the non-force `Remove` path.

## 5-Whys Analysis

### Why #1: Why does the worktree survive teardown in a state where its working tree diverges from its own HEAD (committed skills missing)?
Because `Remove`'s `git worktree remove` (worktree.go:533) runs WITHOUT `--force`,
git refuses to delete a worktree with modified files, the command returns a
non-zero exit, and `Remove` returns the error at line 536 — aborting before the
worktree is removed. The deletions performed just prior (line 530) remain on disk.
Empirically reproduced: exit code 128, `fatal: 'wt' contains modified or untracked
files, use --force to delete it`, and `git worktree list` still shows the worktree.

### Why #2: Why is the worktree dirty (has ` D` entries) at the moment of `git worktree remove`?
Because `cleanupMergedSkills` (called at line 530, defined 507-522) runs
immediately BEFORE the git remove and physically deletes files from the worktree
working tree via `os.RemoveAll` (line 520). Those deletions of tracked,
branch-committed files appear to git as unstaged deletions (` D`), making the
worktree dirty. Verified: `git status --porcelain` was empty before the deletion
and showed ` D .claude/skills/myskill.md` after.

### Why #3: Why does deleting those skill files create *tracked* deletions rather than touching only untracked content?
Because in the collision scenario the branch has COMMITTED real skill files into
`.claude/skills` (replacing the normal factory-root symlink). `cleanupMergedSkills`
only proceeds when `.claude/skills` is a real directory — it returns early if the
path is still a symlink (line 511, `fi.Mode()&os.ModeSymlink != 0`). It then
iterates factory-root skill names (lines 515-519) and `os.RemoveAll`s any worktree
entry whose NAME collides with a factory-root skill (line 520). When the branch
committed a skill of the same name, those deleted files are tracked in the
worktree's HEAD — hence tracked ` D` deletions.

### Why #4: Why is destructive working-tree mutation performed before the worktree is removed at all?
Because the teardown sequence is ordered cleanup-first, remove-second in both
functions (`Remove`: unlink 529 -> cleanupMergedSkills 530 -> git remove 533;
`ForceRemove`: unlink 560 -> cleanupMergedSkills 561 -> git remove --force 562).
The cleanup was designed to strip factory-merged skills so they are not treated as
worktree-local content, but it mutates the live working tree that the very next
step expects to delete. For a worktree about to be discarded, mutating its working
tree provides no benefit and only introduces dirtiness.

### Why #5 (ROOT CAUSE): Why does the ordering not account for git's default refusal to remove a dirty worktree?
The root cause is an unguarded ordering assumption: `cleanupMergedSkills` was
placed ahead of `git worktree remove` without accounting for (a) git's default
non-force refusal on a dirty tree and (b) the case where deleted skill files are
branch-COMMITTED (tracked) rather than merely symlinked/untracked. The deletion is
both unnecessary for a worktree being destroyed AND actively harmful: it
guarantees the non-force `Remove` will fail whenever a branch committed
name-colliding skills, leaving a live, internally-inconsistent worktree. The fix
direction is to remove the worktree first (or use force / skip the working-tree
mutation entirely on the teardown path), but per investigation rules no source is
modified here.

## Evidence Gathered
| Finding | Source | Evidence |
|---------|--------|----------|
| `Remove` calls cleanupMergedSkills BEFORE git remove | worktree.go:530, 533 | line 530 `cleanupMergedSkills(factoryRoot, absPath)` precedes line 533 `cmd := exec.Command("git", "worktree", "remove", absPath)` |
| `Remove`'s git remove is NON-FORCE | worktree.go:533 | `exec.Command("git", "worktree", "remove", absPath)` — no `--force` arg; comment line 532 `// git worktree remove (does NOT force — fails on uncommitted changes)` |
| `ForceRemove` also cleans before remove but USES `--force` | worktree.go:561, 562 | line 561 cleanup, line 562 `exec.Command("git", "worktree", "remove", "--force", absPath)` |
| `Remove` aborts on git-remove failure, leaving worktree on disk | worktree.go:535-537 | `if out, err := cmd.CombinedOutput(); err != nil { return fmt.Errorf("git worktree remove: %w\n%s", err, out) }` |
| cleanupMergedSkills physically deletes worktree files | worktree.go:519-520 | `for _, entry := range entries { os.RemoveAll(filepath.Join(wtSkillsDir, entry.Name())) }` |
| Deletion targets only NAME-colliding factory-root skills | worktree.go:514-521 | reads `factorySkillsDir` entries and RemoveAll's same-named worktree entries — matches issue title "names collide with factory-root skills" |
| Cleanup only fires when skills dir is a real dir (branch committed it) | worktree.go:510-513 | `fi, err := os.Lstat(wtSkillsDir); if err != nil || fi.Mode()&os.ModeSymlink != 0 { return }` |
| Normal `.claude/skills` is a symlink to factory root | worktree.go:51-55, EnsureWorktreeLinks 57-62 | `worktreeSymlinks = []string{filepath.Join(".claude","skills"), ...}` |
| unlinkBeforeRemove handles symlinks only, does NOT delete committed skills | worktree.go:494-505 | removes entries only when `fi.Mode()&os.ModeSymlink != 0` (line 501) — committed real files are untouched, so cleanupMergedSkills is the sole source of the ` D` deletions |

## Tests Performed
| Test | Command | Result |
|------|---------|--------|
| Reproduce dirty-tree + non-force remove failure | Create repo, add worktree on branch1, commit a skill into worktree's `.claude/skills/myskill.md`, `rm` it (simulating cleanupMergedSkills), then `git worktree remove wt` | FAILED with `fatal: 'wt' contains modified or untracked files, use --force to delete it`, EXIT_CODE=128 |
| Confirm worktree survives the failed remove | `ls -d wt` and `git worktree list` after failure | `WORKTREE_STILL_EXISTS`; `git worktree list` still lists `/tmp/wttest/wt [branch1]` |
| Confirm working tree diverges from HEAD | `git -C wt show HEAD:.claude/skills/myskill.md` vs `cat wt/.claude/skills/myskill.md` | HEAD still holds `branchskill committed in worktree branch`; working-tree file is `<FILE ABSENT IN WORKING TREE>` — diverged/corrupted live worktree |
| Confirm dirtiness type is tracked deletion (` D`) | `git -C wt status --porcelain` before and after the delete | empty before; ` D .claude/skills/myskill.md` after |
| Confirm `--force` present/absent per function | `grep -n 'worktree", "remove' worktree.go` | line 533 (Remove) has no `--force`; line 562 (ForceRemove) has `--force` |

## Conclusion
VALIDATED. The ordering hazard is real and directly reproduces the issue's observed
state. `cleanupMergedSkills` (worktree.go:530 in `Remove`) destructively deletes
branch-committed, name-colliding skill files from the worktree's working tree
BEFORE the teardown attempts `git worktree remove` — which at line 533 carries no
`--force`. Git refuses by default to remove a dirty worktree, so the command fails
(empirically: exit 128, `fatal: ... contains modified or untracked files`), and
`Remove` returns at line 536 leaving the worktree alive on disk. Because the
deleted skills are still present in the worktree's HEAD (verified via
`git show HEAD:...`) but absent from the working tree (verified via `cat`), the
surviving worktree is internally diverged — exactly the ` D` skill-entry state
described in issue #59. The identical cleanup-before-remove ordering in
`ForceRemove` (lines 561-562) does not surface the live-corrupted outcome because
its `--force` flag (line 562) overrides git's dirty-tree refusal. Root cause: an
unguarded ordering that mutates a soon-to-be-removed worktree's working tree before
a non-force remove, ignoring both git's default dirty-tree refusal and the case of
branch-committed (tracked) name-colliding skills.
