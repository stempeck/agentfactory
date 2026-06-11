# Concern #10 Investigation: Concrete repro fidelity (factory vs branch-committed skill overlap)

**Investigated by**: Sub-agent
**Date**: 2026-06-11

## Verdict: VALIDATED

## Summary
The issue's concrete repro is empirically faithful, name-for-name, against the
current on-disk state. The factory-root skill set is exactly
{architecture-docs, documentation-update, formula-create, github-issue,
improve-agent, rapid-implement}. The worktree branch (HEAD) git-tracks SKILL.md
content under exactly {documentation-update, formula-create (incl.
skillmd-mode.md), github-issue, rapid-implement}. The intersection of factory
skill names with git-tracked worktree skill directory names is precisely the
four colliding directories {documentation-update, formula-create, github-issue,
rapid-implement}. Per `cleanupMergedSkills` (worktree.go:507-522), teardown
iterates ALL factory skill names and `os.RemoveAll`s the matching worktree
directory — so all four colliding (branch-committed) directories are deleted,
and the two factory-only names {architecture-docs, improve-agent} are no-ops
because no worktree-tracked content lives under them. The repro mechanism —
collisions get deleted regardless of whether they are branch-committed — holds
exactly as described.

## 5-Whys Analysis
### Why #1: Why do the four branch-committed skill files get deleted on teardown?
Because `cleanupMergedSkills` (worktree.go:519-521) loops over every entry in
the FACTORY skills directory and calls
`os.RemoveAll(filepath.Join(wtSkillsDir, entry.Name()))` for each. The factory
contains `documentation-update`, `formula-create`, `github-issue`, and
`rapid-implement`, and the worktree has same-named directories on disk
(confirmed: `ls .claude/skills/` in the worktree lists all six names), so each
of these four worktree directories is removed.

### Why #2: Why does it remove branch-committed content rather than only merged/synced content?
Because the deletion key is purely the NAME match between factory entries and
worktree directories — there is no check of git status, no diff against the
factory copy, and no provenance check distinguishing "synced from factory" from
"authored on this branch". `os.RemoveAll` (worktree.go:520) erases the entire
named directory tree, including `formula-create/SKILL.md` and
`formula-create/skillmd-mode.md`, which are git-tracked on the branch
(confirmed via `git ls-files .claude/skills/formula-create/`).

### Why #3: Why is name-only matching used instead of a content-aware reconciliation?
Because the function's intent is "clean up skills that were merged back to the
factory" (the name `cleanupMergedSkills`), and it assumes any worktree skill
directory whose name also exists in the factory is a redundant copy safe to
delete. That assumption is invalid whenever a branch legitimately commits skill
content under a name that ALSO exists in the factory — exactly the collision
case. The function never distinguishes the two cases.

### Why #4: Why doesn't the function notice the worktree copy differs from / is independent of the factory copy?
Because it never reads or compares the factory copy at all for the deletion
decision. `os.ReadDir(factorySkillsDir)` (worktree.go:515) is used only to
enumerate NAMES; the factory file CONTENTS are never opened, hashed, or
diffed against the worktree contents. Deletion is unconditional on name match.

### Why #5 (root cause): Why was a name-only deletion considered acceptable?
Root cause: the teardown logic conflates "shares a name with a factory skill"
with "is a disposable synced copy of a factory skill." The collision space —
where a branch authors/edits a skill that happens to share a name with a
factory-root skill — was not accounted for. Because the operation is
unconditional `os.RemoveAll` keyed on factory-name enumeration, any
branch-committed skill whose directory name collides with a factory skill name
is destroyed on worktree removal, losing committed work. The two non-colliding
factory skills are harmless no-ops only by accident of name non-overlap.

## Evidence Gathered
| Finding | Source | Evidence |
|---------|--------|----------|
| Factory-root skills = 6 names | `ls -1 /home/dev/af/agentfactory/.claude/skills/` | architecture-docs, documentation-update, formula-create, github-issue, improve-agent, rapid-implement |
| Worktree branch git-tracks 5 SKILL files across 4 dirs | `git ls-files .claude/skills/` (worktree root) | documentation-update/SKILL.md, formula-create/SKILL.md, formula-create/skillmd-mode.md, github-issue/SKILL.md, rapid-implement/SKILL.md |
| Worktree-tracked dir NAMES (deduped) = 4 | `git ls-files ... | sed ... | sort -u` | documentation-update, formula-create, github-issue, rapid-implement |
| Collisions (factory ∩ worktree-tracked) = 4 → DELETED | `comm -12 factory wt` | documentation-update, formula-create, github-issue, rapid-implement |
| Factory-only names = 2 → NO-OP | `comm -23 factory wt` | architecture-docs, improve-agent |
| No worktree-only tracked skill dirs | `comm -13 factory wt` | (empty) |
| Worktree `.claude/skills` is a real directory, NOT a symlink → guard at worktree.go:511 does NOT early-return | `ls -ld .claude/skills` | `drwxr-xr-x ... .claude/skills` |
| On-disk worktree contains all 4 colliding dirs (real RemoveAll targets) | `ls -1 .claude/skills/` (worktree) | architecture-docs, documentation-update, formula-create, github-issue, improve-agent, rapid-implement |
| Factory skills dir is a directory → `os.ReadDir` at worktree.go:515 succeeds | `ls -ld /home/dev/af/agentfactory/.claude/skills` | `drwxr-xr-x ...` |
| Deletion is unconditional name-keyed RemoveAll | worktree.go:519-521 | `for _, entry := range entries { os.RemoveAll(filepath.Join(wtSkillsDir, entry.Name())) }` |
| Symlink guard only protects the parent skills dir, not contents | worktree.go:510-513 | `fi, err := os.Lstat(wtSkillsDir); if err != nil || fi.Mode()&os.ModeSymlink != 0 { return }` |

## Tests Performed
| Test | Command | Result |
|------|---------|--------|
| Enumerate factory skills | `ls -1 /home/dev/af/agentfactory/.claude/skills/` | 6 names: architecture-docs, documentation-update, formula-create, github-issue, improve-agent, rapid-implement |
| Enumerate git-tracked worktree skills | `git ls-files .claude/skills/` (worktree root) | 5 tracked files under 4 dirs (matches issue text) |
| Compute collisions | `comm -12 <(factory) <(wt-tracked-dirs)` | 4 collisions → deleted: documentation-update, formula-create, github-issue, rapid-implement |
| Compute factory-only no-ops | `comm -23 <(factory) <(wt-tracked-dirs)` | 2 no-ops: architecture-docs, improve-agent |
| Compute worktree-only survivors | `comm -13 <(factory) <(wt-tracked-dirs)` | none (empty) |
| Confirm worktree skills dir is not a symlink (guard not triggered) | `ls -ld .claude/skills` (worktree) | regular directory → guard at worktree.go:511 passes, function proceeds |
| Confirm on-disk collision dirs exist as RemoveAll targets | `ls -1 .claude/skills/` (worktree) | all 4 colliding dirs present on disk |
| Confirm factory dir readable by os.ReadDir | `ls -ld /home/dev/af/agentfactory/.claude/skills` | directory, readable → ReadDir at worktree.go:515 succeeds |

## Conclusion
VALIDATED. The repro is faithful both in MECHANISM and in SPECIFIC NAMES against
the current repo state. Empirically: factory skills = {architecture-docs,
documentation-update, formula-create, github-issue, improve-agent,
rapid-implement}; worktree branch-tracks {documentation-update, formula-create
(+skillmd-mode.md), github-issue, rapid-implement}; the four-way intersection
{documentation-update, formula-create, github-issue, rapid-implement} is deleted
by `cleanupMergedSkills` (worktree.go:519-521, unconditional name-keyed
`os.RemoveAll`), while the two factory-only names {architecture-docs,
improve-agent} are no-ops because no worktree directory content lives under them.
The symlink guard (worktree.go:510-513) does not save the contents — the
worktree `.claude/skills` is a real directory and the per-entry RemoveAll
targets named subdirectories directly. The repro mechanism — branch-committed
skills get deleted whenever their directory name collides with a factory-root
skill name, irrespective of git-tracked/committed status — holds exactly. The
exact names in the issue text match the on-disk reality with zero drift.
