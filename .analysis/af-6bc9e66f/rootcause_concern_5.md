# Concern #5 Investigation: Git-tracked skills coexist with symlink/merge shared-resource design

**Investigated by**: Sub-agent
**Date**: 2026-06-11

## Verdict: VALIDATED

## Summary
The repository tracks real skill files under `.claude/skills/` at HEAD (5 files across
4 skill directories), while the worktree subsystem simultaneously declares `.claude/skills`
as a shared symlink/merge resource in `worktreeSymlinks` (internal/worktree/worktree.go:51-55).
Because git checks out the tracked skill files into every new worktree, `.claude/skills`
materializes as a *real directory* rather than a symlink, which causes `EnsureWorktreeLinks`
to fall into the merge branch (worktree.go:77-85) and copy factory-root skills *into the same
directory* alongside the git-tracked ones. The two designs do not merely coexist passively —
they overlap on identical directory names (`documentation-update`, `formula-create`,
`github-issue`, `rapid-implement`), and on teardown `cleanupMergedSkills` (worktree.go:507-522)
blindly `os.RemoveAll`s every worktree skill dir whose name matches a factory-root skill,
with no way to distinguish a branch-committed skill from a merged-in copy. This coexistence
is exactly the structural precondition for the destructive collision described in issue #59.

## 5-Whys Analysis

### Why #1: Why is `.claude/skills` both git-tracked and treated as a shared symlink resource?
Two independent design decisions target the same path. (a) Real skill files are committed to
git — `git ls-files .claude/skills/` returns 5 tracked files
(`documentation-update/SKILL.md`, `formula-create/SKILL.md`, `formula-create/skillmd-mode.md`,
`github-issue/SKILL.md`, `rapid-implement/SKILL.md`). (b) The worktree system lists
`filepath.Join(".claude", "skills")` in `worktreeSymlinks` (worktree.go:52) as a factory-root
resource that should be symlinked/merged into each worktree. Neither design is aware of the other.

### Why #2: Why does the symlink design not simply symlink the directory and avoid the tracked files?
Because git has already checked out the tracked files before `EnsureWorktreeLinks` runs. In
`Create`, `git worktree add` (worktree.go:377) populates the worktree from the branch — including
the tracked `.claude/skills/*` files — *before* `EnsureWorktreeLinks(factoryRoot, absPath)` is
called (worktree.go:399). So when `EnsureWorktreeLinks` lstats the source, it finds a *real
directory*, not an absent path. Confirmed empirically: in this worktree
`ls -ld .claude/skills` reports `drwxr-xr-x` (a real directory), not a symlink.

### Why #3: Why does a pre-existing real directory cause a *merge* rather than being left alone?
`EnsureWorktreeLinks` has an explicit special case: when the source exists, is a directory, and
the relPath equals `.claude/skills`, it calls `mergeSkillsDir(target, source)` (worktree.go:77-85)
instead of skipping. `mergeSkillsDir` copies every factory-root skill *not already present by name*
into the worktree skills dir (worktree.go:99-134). Result: the worktree `.claude/skills` now
contains a *mixture* of git-tracked skill dirs and copied factory-root skill dirs, all siblings in
one real directory. Verified: this worktree's `.claude/skills` holds the 4 tracked dirs PLUS the
factory-only dirs `architecture-docs` and `improve-agent` that were merged in.

### Why #4: Why is the merged/tracked mixture dangerous at teardown?
Because teardown cannot tell the two apart. `Remove` (worktree.go:526) and `ForceRemove`
(worktree.go:558) both call `cleanupMergedSkills` (worktree.go:530, 561). That function reads the
*factory-root* skills dir and, for every entry name, runs
`os.RemoveAll(filepath.Join(wtSkillsDir, entry.Name()))` (worktree.go:519-520). It uses the
factory-root listing as its delete list and applies it to the worktree, deleting by *name* with no
check of whether the worktree copy is git-tracked, modified, or branch-committed.

### Why #5 (ROOT CAUSE): Why does name-based deletion destroy branch-committed work?
Because the colliding skill directories share *identical names AND identical file names* across the
two sources, the cleanup's name match is guaranteed to fire on git-tracked dirs. Evidence: the
intersection of tracked-skill dir names and factory-root dir names is
{`documentation-update`, `formula-create`, `github-issue`, `rapid-implement`} — and within each,
the tracked file set equals the factory file set (e.g. `formula-create` has `SKILL.md` +
`skillmd-mode.md` in both). So `cleanupMergedSkills` will `os.RemoveAll` these git-tracked
directories from the worktree before `git worktree remove` runs. The root cause is a **namespace
collision between two authorities over the same path**: git owns `.claude/skills/<name>` as
versioned content, while the worktree merge/cleanup machinery owns `.claude/skills/<name>` as a
disposable shared resource keyed solely on factory-root names. There is no provenance marker
distinguishing "tracked, must preserve" from "merged copy, safe to delete," so the deletion is
indiscriminate. This coexistence is the structural precondition for the destructive collision.

## Evidence Gathered
| Finding | Source | Evidence |
|---------|--------|----------|
| Repo tracks real skill files at HEAD | `git ls-files .claude/skills/` (run in wt-2a7c06 root) | 5 files: `documentation-update/SKILL.md`, `formula-create/SKILL.md`, `formula-create/skillmd-mode.md`, `github-issue/SKILL.md`, `rapid-implement/SKILL.md` |
| `.claude/skills` declared as shared symlink resource | internal/worktree/worktree.go:51-55 | `var worktreeSymlinks = []string{ filepath.Join(".claude", "skills"), ".runtime", "AGENTS.md" }` |
| Worktree `.claude/skills` is a REAL dir, not symlink | `ls -ld .claude/skills` in wt-2a7c06 | `drwxr-xr-x 8 dev dev ... .claude/skills` (no `l` mode bit) |
| Real-dir source triggers merge, not skip | internal/worktree/worktree.go:77-85 | `} else if fi.IsDir() && relPath == filepath.Join(".claude", "skills") { merged, err := mergeSkillsDir(target, source) ... continue }` |
| Merge copies factory skills into worktree dir | internal/worktree/worktree.go:99-134 | `mergeSkillsDir`: skips names already present, else `copyDir`/`copyFile` factory skill into `worktreeSkillsDir` |
| Tracked + merged mixture present in this worktree | `ls -la .claude/skills/` | Contains tracked dirs (documentation-update, formula-create, github-issue, rapid-implement) PLUS factory-only merged dirs (architecture-docs, improve-agent) |
| Teardown deletes worktree skill dirs by factory-root name | internal/worktree/worktree.go:507-522 | `for _, entry := range entries { os.RemoveAll(filepath.Join(wtSkillsDir, entry.Name())) }` — entries come from `factorySkillsDir` |
| cleanupMergedSkills runs on BOTH teardown paths | internal/worktree/worktree.go:530, 561 | called in `Remove` (526) and `ForceRemove` (558) before `git worktree remove` |
| Collision set is non-empty (4 dirs) | `comm -12` of tracked dir names vs factory-root names | `documentation-update`, `formula-create`, `github-issue`, `rapid-implement` |
| Colliding dirs have identical file sets | per-dir diff of `git ls-files` vs factory `ls` | e.g. `formula-create`: both have `SKILL.md` + `skillmd-mode.md`; others both have `SKILL.md` |

## Tests Performed
| Test | Command | Result |
|------|---------|--------|
| List git-tracked skills in worktree | `git ls-files .claude/skills/` (in wt-2a7c06 root) | 5 tracked files across 4 dirs — confirms repo tracks real skill files at HEAD |
| List factory-root skills | `ls -la /home/dev/af/agentfactory/.claude/skills/` | 6 dirs: architecture-docs, documentation-update, formula-create, github-issue, improve-agent, rapid-implement |
| Confirm worktreeSymlinks declaration | Read worktree.go:51-55 | `.claude/skills` present as first entry — confirms shared-resource design |
| Determine worktree `.claude/skills` type | `ls -ld .claude/skills` | Real directory (drwx...), NOT a symlink — git checkout wins, merge path taken |
| Compute collision intersection | `comm -12 <(tracked names) <(factory names)` | 4 colliding names — non-empty precondition for destruction |
| Compare file sets within collisions | `git ls-files <dir>` vs `ls <factory dir>` for each | Identical file sets — cleanup name-match cannot avoid tracked files |
| Confirm teardown deletion wiring | `grep -n cleanupMergedSkills internal/worktree/worktree.go` | Defined at 507; called at 530 (Remove) and 561 (ForceRemove) |

## Conclusion
VALIDATED. The two designs provably coexist on this branch: git tracks 5 real skill files
(`git ls-files .claude/skills/`) and `.claude/skills` is declared a shared symlink/merge resource
(worktree.go:51-55). They do not merely coexist — they actively conflict, because the git checkout
materializes `.claude/skills` as a real directory (verified `drwxr-xr-x`, not a symlink), forcing
`EnsureWorktreeLinks` into the merge branch (worktree.go:77-85) and producing a single directory
that mixes branch-committed skills with copied factory skills. At teardown, `cleanupMergedSkills`
(worktree.go:507-522) deletes worktree skill dirs purely by factory-root name with no provenance
check, and the collision set is non-empty (4 dirs with identical file contents). Therefore every
branch that commits a skill whose name matches a factory-root skill creates the exact structural
precondition for the destructive teardown collision in issue #59. This concern is the root
structural precondition, and it is confirmed.
