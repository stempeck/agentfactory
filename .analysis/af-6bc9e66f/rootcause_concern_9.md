# Concern #9 Investigation: Test gap — no coverage for branch-committed skill colliding with factory skill

**Investigated by**: Sub-agent
**Date**: 2026-06-11

## Verdict: VALIDATED

## Summary
The worktree package has exactly four skill-related tests, and none of them
exercises the failing scenario from issue #59. `TestCleanupMergedSkills_RemovesFactoryCopiedEntries`
(worktree_test.go:1815) sets up factory skills named `skill-A`/`skill-B` and a
single worktree-only survivor named `git-only-skill` — a name deliberately chosen
so it does NOT match any factory entry. Because `cleanupMergedSkills`
(worktree.go:507-522) deletes worktree skills purely by name-match against the
factory skills directory, the survivor is only safe precisely because its name
avoids the collision. There is no test in which a branch-committed worktree skill
shares a name with a factory skill, which is the exact destructive case. I
reproduced the bug with a throwaway in-package test: a branch-committed skill
named `shared-skill` (different content from the factory `shared-skill`) is
deleted by `cleanupMergedSkills`, with zero existing tests guarding against it.

## 5-Whys Analysis

### Why #1: Why did the test suite miss the branch-committed/factory-name-collision deletion bug?
Because the only test asserting survival of a non-factory skill,
`TestCleanupMergedSkills_RemovesFactoryCopiedEntries` (worktree_test.go:1815-1847),
uses a survivor whose directory name (`git-only-skill`, lines 1830-1831) cannot
collide with either factory skill (`skill-A`, `skill-B`, lines 1820-1823). The
survival assertion at line 1844 therefore never traverses the destructive code
path — it only proves that a *non-colliding* name survives.

### Why #2: Why was the survivor named to avoid the collision instead of testing the collision?
Because the test's mental model was "merged (factory-copied) entries get removed,
git-only entries survive" — encoded in the comment at line 1825
(`// Worktree has skills A, B (merged), and C (git-tracked only)`). The author
modeled the survivor as "git-tracked only" (a skill that exists ONLY in the
branch, with no factory namesake). The author never modeled "git-tracked AND
sharing a factory name," so the survivor's name was chosen to be factory-disjoint.

### Why #3: Why was the "git-tracked AND sharing a factory name" case never modeled?
Because the cleanup contract was conceived as the inverse of the merge step, and
the merge step is name-keyed and idempotent: `mergeSkillsDir` (worktree.go:99-133)
SKIPS any factory entry whose name already exists in the worktree
(`if _, err := os.Stat(destPath); err == nil { continue }`, lines 107-109). So at
merge time a colliding branch skill is correctly preserved (proven by
`TestEnsureWorktreeLinks_RealSkillsDir_MergesFactorySkills`, worktree_test.go:1748,
which keeps git-tracked `factory-skill-B` content, lines 1786-1792). The cleanup
step, however, does NOT mirror that skip logic — it removes by name
unconditionally (worktree.go:519-521). The asymmetry between merge (content/name-aware,
non-destructive) and cleanup (name-only, destructive) was never captured as a test
case, so the gap was invisible.

### Why #4: Why did the cleanup function's name-only deletion not get its own collision test when merge had a collision test?
Because `cleanupMergedSkills` has no way to distinguish a factory-copied entry
from a branch-committed entry — it only knows the factory's list of names
(worktree.go:515 `os.ReadDir(factorySkillsDir)`) and blindly `os.RemoveAll`s each
matching name in the worktree (line 520). Since the function itself cannot tell
the two cases apart, a test would have had to manufacture a colliding survivor on
purpose. The existing tests instead validated only the cases the implementation
trivially handles (distinct names removed/kept, symlink no-op at lines 1849-1872),
which all pass and create a false sense of coverage.

### Why #5 (ROOT CAUSE): Why was a destructive-by-name operation shipped without a test asserting that a same-named-but-distinct branch artifact survives?
Root cause: the test suite was written to confirm the *intended happy path*
("remove what we merged in, keep what we didn't") rather than to probe the
*destructive boundary* of a name-keyed delete. A name-based `os.RemoveAll` is
inherently lossy whenever a worktree name can come from two sources (factory merge
vs. branch commit), but no negative/adversarial test was authored to assert that a
branch-committed artifact sharing a factory name is preserved. The collision case
is the one scenario that distinguishes "delete what we copied" from "delete by
name," and it is the single untested case — so the regression had no guard and
ships green.

## Evidence Gathered
| Finding | Source | Evidence |
|---------|--------|----------|
| Cleanup deletes worktree skills by factory name-match, unconditionally | internal/worktree/worktree.go:519-521 | `for _, entry := range entries { os.RemoveAll(filepath.Join(wtSkillsDir, entry.Name())) }` — no content/origin check |
| Existing survival test's factory skills are `skill-A`, `skill-B` | internal/worktree/worktree_test.go:1820-1823 | `MkdirAll(... "skills", "skill-A")` and `"skill-B"` |
| Existing survival test's survivor is `git-only-skill` (non-colliding name) | internal/worktree/worktree_test.go:1830-1831 | `MkdirAll(... "skills", "git-only-skill")` — name shares no prefix/identity with factory skills |
| Survivor name was modeled as "git-tracked only," not "git-tracked + colliding" | internal/worktree/worktree_test.go:1825 | comment `// Worktree has skills A, B (merged), and C (git-tracked only)` |
| Survival assertion only checks the non-colliding name | internal/worktree/worktree_test.go:1844-1846 | `os.Stat(... "git-only-skill")` — never asserts a factory-named branch skill survives |
| Merge step is collision-safe (skips existing names), so collision-survival is expected by users | internal/worktree/worktree.go:107-109 | `if _, err := os.Stat(destPath); err == nil { continue }` |
| Merge collision-safety IS tested (asymmetry made cleanup gap invisible) | internal/worktree/worktree_test.go:1786-1792 | keeps `git-tracked B content` for `factory-skill-B` after merge |
| Only four skill tests exist; none covers the cleanup collision case | internal/worktree/worktree_test.go:1748,1815,1849,1874 | `TestEnsureWorktreeLinks_RealSkillsDir_MergesFactorySkills`, `TestCleanupMergedSkills_RemovesFactoryCopiedEntries`, `TestCleanupMergedSkills_NoOpOnSymlink`, `TestMergeSkillsDir_SkipsSymlinksInFactory` |
| No "collision"/"collide" reference anywhere in worktree cleanup tests | grep internal/worktree/worktree_test.go | only matches are at lines 1833/1863 (the `cleanupMergedSkills` calls), none for collision assertions |
| `cleanupMergedSkills` is invoked on real teardown (Remove) | internal/worktree/worktree.go:530, :561 | `cleanupMergedSkills(factoryRoot, absPath)` inside `Remove` (and second teardown path) |

## Tests Performed
| Test | Command | Result |
|------|---------|--------|
| Run all four existing skill tests | `go test ./internal/worktree/ -run 'TestCleanupMergedSkills\|TestEnsureWorktreeLinks_RealSkillsDir\|TestMergeSkillsDir' -v` | All 4 PASS — confirms the suite is green with no collision coverage |
| Reproduce bug: branch-committed `shared-skill` (content `BRANCH-COMMITTED-WORK`) vs factory `shared-skill` (content `FACTORY`), then call `cleanupMergedSkills` | throwaway in-package `TestExperiment_CollidingGitSkillDestroyed` (created, run, then removed; no source modified) | PASS with log `CONFIRMED BUG: branch-committed colliding skill was DELETED by cleanup` — proves the untested failing scenario |
| Enumerate all Skill/Cleanup/Merge test functions in the package | `grep -rn 'func Test.*Skill\|func Test.*Cleanup\|func Test.*Merge' internal/worktree/*.go` | Exactly 4 functions; none asserts collision survival |
| Search whole repo for collision/cleanup references | `grep -rn 'cleanupMergedSkills\|collision\|collide' internal/ --include=*.go` | No cleanup collision test anywhere; "collision" matches are unrelated (formula vars, tmux session names) |

## Conclusion
VALIDATED. The single test that asserts non-factory skill survival
(`TestCleanupMergedSkills_RemovesFactoryCopiedEntries`, worktree_test.go:1815)
uses a survivor named `git-only-skill` (lines 1830-1831) whose name is disjoint
from the factory skills `skill-A`/`skill-B` (lines 1820-1823). Because
`cleanupMergedSkills` deletes strictly by factory name-match
(worktree.go:519-521), the survivor passes only because its name avoids the
collision — the destructive path is never traversed for it. No test in the
package (the four enumerated at worktree_test.go:1748/1815/1849/1874) sets up a
branch-committed worktree skill sharing a name with a factory skill, which is the
exact failing scenario in issue #59. I empirically reproduced the deletion with a
throwaway in-package test, and the existing suite stays fully green, proving the
coverage gap lets the regression ship undetected. The root cause (Why #5) is that
the suite validated the intended happy path of a name-keyed delete rather than
probing its destructive boundary — the one case that distinguishes "delete what we
copied" from "delete by name" is the one untested case.
