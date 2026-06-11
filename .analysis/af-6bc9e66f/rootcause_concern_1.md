# Concern #1 Investigation: cleanupMergedSkills deletes by factory name-match with no provenance check

**Investigated by**: Sub-agent
**Date**: 2026-06-11

## Verdict: VALIDATED

## Summary
`cleanupMergedSkills` (`internal/worktree/worktree.go:507-522`) iterates the
**factory-root** skill entries and calls `os.RemoveAll` on the worktree skill
directory of the same name (lines 519-520), with no check of whether that
worktree entry was merge-copied in at worktree creation or committed to the
worktree's own branch. The merge step (`mergeSkillsDir`, lines 99-134) returns
only an `int` count and never persists a manifest, marker, or any record of
which entries it copied. Consequently, the cleanup decision is driven purely by
**name collision with the factory** — there is no content comparison, no
`os.SameFile`/hash/`bytes.Equal`, and no provenance distinction. Any
branch-committed skill whose directory name matches a factory skill is destroyed
during teardown. This is confirmed both by direct code reading and by the
project's own passing test, which deletes a worktree `skill-A` solely because a
factory `skill-A` exists.

## 5-Whys Analysis

### Why #1: Why does a branch-committed worktree skill get deleted on teardown?
Because `cleanupMergedSkills` removes worktree skill dirs by **name match against
the factory**, not by provenance. The loop reads the factory skills dir and, for
each factory entry, unconditionally removes the same-named worktree entry:
```go
factorySkillsDir := filepath.Join(factoryRoot, skillsRel)   // worktree.go:514
entries, err := os.ReadDir(factorySkillsDir)                // worktree.go:515
for _, entry := range entries {                             // worktree.go:519
    os.RemoveAll(filepath.Join(wtSkillsDir, entry.Name()))  // worktree.go:520
}
```
The `os.RemoveAll` target is computed solely from `entry.Name()` (a factory
name) joined to the worktree skills dir. There is no condition between the
`for` and the `RemoveAll`.

### Why #2: Why is there no condition guarding the removal?
Because the function has no information to condition on. It never compares
contents (no `bytes.Equal`/`os.SameFile`/hash; the only `os.ReadFile` calls in
the file are in `copyFile` at line 137 and `ReadMeta` at line 318, neither in
the cleanup path), and it never consults any record of what was merged. The
worktree entry's mtime, content, and git-tracked status are all ignored; only
its **name** matters.

### Why #3: Why is no merge record available to consult?
Because the merge operation does not persist one. `mergeSkillsDir`
(worktree.go:99-134) copies factory skills that don't already exist in the
worktree (skip at lines 107-109) and increments a local `merged` counter
(line 131), returning it as an `int` (line 133). The caller `EnsureWorktreeLinks`
only logs that count to stderr ("info: merged N factory skill(s)", line 83). No
manifest file, `.merged` marker, sidecar list, or meta field is written. A
codebase-wide grep for `manifest|provenance|.merged|merged-skills|MergedSkills`
in `internal/worktree/` returns no provenance storage of any kind.

### Why #4: Why was name-match deemed acceptable as the deletion criterion?
Because the design assumed the **only** reason a worktree skill dir would share a
name with a factory skill is that `mergeSkillsDir` copied it in — i.e. it
conflated "name collides with factory" with "was merge-copied". This assumption
ignores the legitimate case where the worktree's branch commits its own skill
files under `.claude/skills/` (e.g. a PR that adds/edits a skill whose name
matches an existing factory skill). The fidelity of cleanup depends entirely on
that assumption holding, and it does not.

### Why #5 (root cause): Why does the assumption break?
Because merge-on-create and branch-commit are two independent sources of files in
the worktree's `.claude/skills/`, and the system records nothing to tell them
apart. The skills dir starts as a real (non-symlink) directory when the factory
has skills (EnsureWorktreeLinks lines 77-85 merges into it instead of
symlinking), so committed skill files and merged copies coexist in the same real
directory under identical naming. With no provenance ledger, the **only**
distinguishing signal cleanup could use is name, and name cannot distinguish
"merged" from "committed". **Root cause: the merge step persists no provenance,
so cleanup falls back to a name-match heuristic that destroys legitimately
branch-committed skills on collision.**

## Evidence Gathered
| Finding | Source | Evidence |
|---------|--------|----------|
| Cleanup iterates FACTORY entries, not worktree-specific markers | worktree.go:514-519 | `factorySkillsDir := filepath.Join(factoryRoot, skillsRel)` then `entries := os.ReadDir(factorySkillsDir)` then `for _, entry := range entries` |
| Removal target derived solely from factory name | worktree.go:520 | `os.RemoveAll(filepath.Join(wtSkillsDir, entry.Name()))` — `entry` is a factory entry |
| No condition between iteration and removal | worktree.go:519-521 | loop body is a single unconditional `os.RemoveAll` |
| No content/identity comparison anywhere in cleanup | worktree.go:507-522 | no `bytes.Equal`, `os.SameFile`, hash, or `ReadFile` in the function |
| Merge persists no provenance, only a count | worktree.go:104, 131, 133 | `merged := 0` ... `merged++` ... `return merged, nil` |
| Merge count only logged, never stored | worktree.go:82-84 | `if merged > 0 { fmt.Fprintf(stderrWriter, "info: merged %d ...") }` |
| No manifest/provenance store exists in package | grep `manifest\|provenance\|\.merged\|MergedSkills` in internal/worktree | only matches are `cleanupMergedSkills` name itself and its two call sites |
| Skills dir is a real dir (not symlink) when factory has skills, so committed + merged files coexist | worktree.go:77-85 | `else if fi.IsDir() && relPath == ".claude/skills" { mergeSkillsDir(...) }` — merges into real dir instead of symlinking |
| Cleanup invoked on every teardown path | worktree.go:530, 561 | `Remove` and `ForceRemove` both call `cleanupMergedSkills(factoryRoot, absPath)` before `git worktree remove` |
| Existing test encodes the flawed name-only rule | worktree_test.go:1825-1846 | worktree `skill-A` (written directly, no real merge) is asserted removed because factory has `skill-A`; `git-only-skill` survives only because factory lacks that name |

## Tests Performed
| Test | Command | Result |
|------|---------|--------|
| Confirm callers + check for any provenance/manifest storage | `grep -rn "cleanupMergedSkills\|mergeSkillsDir\|merged" internal/` and `grep -rn "manifest\|provenance\|\.merged\|MergedSkills\|record" internal/worktree/` | Only call sites + a stderr-logged `int` count found; NO provenance store exists |
| Check for content comparison in cleanup | `grep -rn "os.SameFile\|bytes.Equal\|ReadFile\|hash\|checksum" internal/worktree/worktree.go` | Only `ReadFile` in `copyFile` (l137) and `ReadMeta` (l318); none in cleanup path |
| Run the project's own cleanup tests to observe current behavior | `go test ./internal/worktree/ -run TestCleanupMergedSkills -v` (TMPDIR redirected to exec-allowed dir) | PASS — `TestCleanupMergedSkills_RemovesFactoryCopiedEntries` deletes worktree `skill-A`/`skill-B` purely by factory name-match, empirically confirming name-only deletion with no provenance |

## Conclusion
**VALIDATED.** The loop at `worktree.go:519-520` removes worktree skill
directories by factory-root **name match** with zero provenance distinction. The
merge operation (`mergeSkillsDir`, lines 99-134) stores no record of which
entries were copied (only an `int` count, logged at line 83), so cleanup has no
way to tell a merge-copied skill from a branch-committed one and falls back to
deleting any worktree entry whose name appears in the factory skills dir. There
is no content comparison, no `os.SameFile`, no hash, and no manifest anywhere in
the path. The project's own passing test
`TestCleanupMergedSkills_RemovesFactoryCopiedEntries`
(worktree_test.go:1815-1847) directly demonstrates the bug: a worktree
`skill-A`/`skill-B` is deleted solely because the factory contains directories of
those names, regardless of how those worktree files originated. Because
`cleanupMergedSkills` runs on every teardown via both `Remove` (line 530) and
`ForceRemove` (line 561), any branch that commits real skill files under
`.claude/skills/` with names colliding with factory skills will lose them on
teardown. The concern is confirmed.
