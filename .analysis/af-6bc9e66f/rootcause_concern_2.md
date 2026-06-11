# Concern #2 Investigation: Create/teardown asymmetry (preserving vs destroying) is the structural root cause

**Investigated by**: Sub-agent
**Date**: 2026-06-11

## Verdict: VALIDATED

## Summary
The bug is caused by a structural asymmetry between the worktree skill-creation path
and the worktree skill-teardown path. Creation (`mergeSkillsDir`, worktree.go:99-134)
is **name-aware and preserving**: it copies a factory skill into the worktree *only if
no entry of that name already exists* (the `os.Stat` skip at lines 107-109), so any
branch-committed worktree skill survives creation untouched — even if its name collides
with a factory skill. Teardown (`cleanupMergedSkills`, worktree.go:507-522) is
**name-match and destroying**: it iterates factory-root skill names and `os.RemoveAll`s
the worktree entry of that name **unconditionally**, with no record of what was actually
copied in and no provenance check. The two halves use the same matching key (skill name)
but opposite intent, so a name that creation *preserved* is exactly a name that teardown
*destroys*. This asymmetry — not a logic typo or a missing nil-check — is the structural
root cause: the system never records the merge as a fact, so teardown is forced to *guess*
which entries it owns by re-deriving from the factory directory, and the heuristic
("present in factory ⇒ I put it there") is wrong precisely for collision cases.

## 5-Whys Analysis

### Why #1: Why does teardown delete a branch-committed skill?
Because `cleanupMergedSkills` removes every worktree skill whose **name** matches a factory
skill, without checking whether that worktree entry was actually copied in by the factory or
was committed on the branch. worktree.go:519-521:
```go
for _, entry := range entries {            // entries = factory-root skill names
    os.RemoveAll(filepath.Join(wtSkillsDir, entry.Name()))
}
```
There is no provenance test — only a name match against `factorySkillsDir` (514-518).

### Why #2: Why does teardown match by factory-root name instead of "what I copied in"?
Because the merge step left **no record** of what it copied. `mergeSkillsDir` returns only an
`int` count (`return merged, nil`, worktree.go:131-133) and `EnsureWorktreeLinks` discards it
beyond logging (worktree.go:82-84). With no persisted manifest of merged entries, teardown has
nothing authoritative to consult, so it reconstructs the set by re-reading the factory skills
directory — a *proxy* for "what was merged," not the truth.

### Why #3: Why is the factory directory an incorrect proxy for "what was merged"?
Because creation is **preserving**, not overwriting. `mergeSkillsDir` skips any name that already
exists in the worktree (worktree.go:107-109):
```go
destPath := filepath.Join(worktreeSkillsDir, entry.Name())
if _, err := os.Stat(destPath); err == nil {
    continue          // pre-existing (e.g. branch-committed) entry is left intact
}
```
So when a factory name collides with a branch-committed worktree entry, **nothing is copied** for
that name — yet the name is still present in the factory directory. Teardown's proxy set
("names in factory") therefore includes a name that creation never actually merged, and deletes
the branch-committed entry that was preserved. The skip-set at create and the delete-set at
teardown are computed from the *same* key but should be *complements*, not equal.

### Why #4: Why was the system designed so create preserves but teardown destroys by the same key?
Because the two functions were written to satisfy two independent local goals without a shared,
authoritative notion of ownership. Create's goal: "make factory skills available in the worktree
without clobbering the branch's own skills" → preserve-on-collision (correct in isolation).
Teardown's goal: "leave the branch with only its own skills, removing the factory copies" →
remove-by-factory-name (correct *only* when no name collisions exist). Each is locally correct;
together they are inconsistent because neither side records the actual merge as a durable fact.
The asymmetry is the seam where the two local correctness assumptions contradict each other.

### Why #5 (root): Why is there no shared authoritative ownership record?
Because the design treats "merged skills" as a derivable property (recompute from the factory dir
on demand) rather than as **state to be tracked**. The merge action is fire-and-forget: it mutates
the worktree filesystem and returns a count, persisting nothing. Teardown is consequently forced to
infer ownership, and inference cannot distinguish a factory-copied entry from a branch-committed
entry of the same name. **This is the structural root cause**: the create path is name-aware and
preserving while the teardown path is name-match and destroying, and because no provenance/manifest
ties them together, the two paths disagree exactly on collision names — destroying branch-committed
work.

## Evidence Gathered
| Finding | Source | Evidence |
|---------|--------|----------|
| Create preserves any pre-existing (incl. branch-committed) name | worktree.go:107-109 | `if _, err := os.Stat(destPath); err == nil { continue }` |
| Create copies only when name absent; returns only a count, persists no manifest | worktree.go:104-133 | `merged := 0` ... `merged++` ... `return merged, nil` |
| Merge result is discarded (only logged) | worktree.go:78-84 | `merged, err := mergeSkillsDir(...)` then only `Fprintf` info/warn |
| Teardown derives delete-set from factory dir names | worktree.go:514-518 | `factorySkillsDir := filepath.Join(...)` ; `entries, err := os.ReadDir(factorySkillsDir)` |
| Teardown removes by name match, unconditionally, no provenance | worktree.go:519-521 | `for _, entry := range entries { os.RemoveAll(filepath.Join(wtSkillsDir, entry.Name())) }` |
| Same matching key (skill name) used for both preserve and destroy | worktree.go:106 vs 520 | both join `entry.Name()` onto the worktree skills dir |
| Existing test locks in PRESERVING create on name collision | worktree_test.go:1761-1792 | worktree skill `factory-skill-B` keeps `"git-tracked B content"` after merge |
| Existing test locks in DESTROYING teardown on name match | worktree_test.go:1815-1846 | factory-named `skill-A`/`skill-B` removed; only `git-only-skill` (no factory match) survives |
| Combined, the two tests encode the exact bug | worktree_test.go:1748-1847 | a name that create preserves (collision) is a name teardown removes |
| Teardown reached by both Remove and ForceRemove | worktree.go:530, 561 | `cleanupMergedSkills(factoryRoot, absPath)` in both removal paths |

## Tests Performed
| Test | Command | Result |
|------|---------|--------|
| Existing merge/cleanup suite passes (locks in current asymmetric behavior) | `TMPDIR=$PWD/.tmptest go test ./internal/worktree/ -run 'TestCleanupMergedSkills\|TestEnsureWorktreeLinks_RealSkillsDir\|TestMergeSkillsDir' -v` | PASS (4 tests: merge-preserves, cleanup-removes-factory-named, cleanup-noop-on-symlink, merge-skips-symlinks) |
| One-off round-trip proof (added a temp `_test.go`, ran, then removed it — no source modified) | `go test -run TestConcern2_RoundTripDataLoss -v` | PASS: a branch-committed skill `shared` is PRESERVED after `EnsureWorktreeLinks`/`mergeSkillsDir`, then DELETED after `cleanupMergedSkills` — round-trip data loss reproduced |
| Verified no residual test artifacts / no source changes | `ls internal/worktree/concern2_proof_test.go; git status --short internal/worktree/` | proof file absent; clean working tree (investigation only) |

## Conclusion
**VALIDATED.** The create/teardown asymmetry is confirmed as the structural root cause.

1. Creation is name-aware and **preserving** — `mergeSkillsDir` skips any name that already
   exists in the worktree (worktree.go:107-109), so branch-committed skills survive creation
   even on name collision. This is locked in by `TestEnsureWorktreeLinks_RealSkillsDir_MergesFactorySkills`
   (worktree_test.go:1785-1792), which asserts the git-tracked `factory-skill-B` retains its
   own content.

2. Teardown is name-match and **destroying** — `cleanupMergedSkills` removes every worktree
   entry whose name appears in the factory skills directory, unconditionally and without
   provenance (worktree.go:519-521). This is locked in by
   `TestCleanupMergedSkills_RemovesFactoryCopiedEntries` (worktree_test.go:1835-1846), where
   `git-only-skill` survives *only because it lacks a factory name match* — the converse proving
   any factory-named branch skill would be removed.

3. The round-trip proof test directly demonstrates the data loss: identical skill name `shared`
   is preserved through creation and then destroyed at teardown. The two paths agree on their
   matching key (skill name) but oppose on intent, and because the merge persists no manifest
   (worktree.go:131-133, result discarded at 82-84), teardown is forced to infer ownership from
   the factory directory — an inference that is wrong precisely for collision cases.

The 5-whys terminates at a design-level cause (no authoritative ownership/provenance record tying
create to teardown), which is structural rather than a local coding defect. The proposed structural
root cause is therefore VALIDATED.
