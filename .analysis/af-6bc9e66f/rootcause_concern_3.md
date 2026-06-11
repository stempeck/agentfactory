# Concern #3 Investigation: No provenance record of merge-introduced skills

**Investigated by**: Sub-agent
**Date**: 2026-06-11

## Verdict: VALIDATED

## Summary
The worktree skills lifecycle keeps no persisted record of which skill directories
`mergeSkillsDir` actually copied into a worktree. `mergeSkillsDir` returns only an
`int` count (`merged`) and discards the names of what it copied (worktree.go:99-134).
The `Meta` struct (worktree.go:27-35) and `meta.json` contain no provenance field, and
no manifest/marker file is written anywhere in the codebase. At teardown,
`cleanupMergedSkills` (worktree.go:507-522) reconstructs the deletion set by re-reading
the *live* factory skills directory (worktree.go:514-520) and deleting every worktree
entry whose name matches a factory entry. Because a name in the factory root is the only
signal used, any branch-committed (git-tracked) skill in the worktree that happens to
share a name with a factory skill is deleted, even though the lifecycle never introduced
it. This is the textbook "infer instead of record" defect: the deletion set is
*derived from current state* rather than *driven by a record of the action that needs
undoing*.

## 5-Whys Analysis

### Why #1: Why does teardown delete branch-committed skills that collide with factory-root skill names?
Because `cleanupMergedSkills` builds its deletion set by reading `factorySkillsDir` at
teardown time and removing every worktree entry whose name matches a factory entry
(worktree.go:514-520). It uses name-equality with the live factory dir as the sole
criterion — it cannot tell a merge-introduced copy apart from a git-tracked file of the
same name.

### Why #2: Why does it use the live factory dir as the criterion instead of "what was merged"?
Because there is no record of what was merged to use instead. `cleanupMergedSkills`
has no input other than `factoryRoot` and `worktreePath` (worktree.go:507); it has no
list of merge-introduced names available, so the live factory directory is the only data
source it can consult.

### Why #3: Why is there no record of what was merged available at teardown?
Because the merge step never persisted one. `mergeSkillsDir` does track which entries it
copies internally (it increments `merged` at worktree.go:131 for each copied entry), but
it returns only the integer count (`return merged, nil`, worktree.go:133) and throws away
`entry.Name()` for each copied skill. The caller (`EnsureWorktreeLinks`,
worktree.go:78-84) likewise only uses the count to print an info line and persists
nothing.

### Why #4: Why does no persistent provenance field exist to hold those names?
Because the data model has no slot for it. The `Meta` struct (worktree.go:27-35) — the
only per-worktree state written to disk via `WriteMeta`/`meta.json`
(worktree.go:296-327) — has fields `ID, Owner, Branch, Path, Agents, CreatedAt,
ParentBranch` and no merged-skills field. No separate manifest or marker file is written
either; a repo-wide search found no manifest/marker mechanism for merged skills
(only unrelated `merged` variables in `formula/vars.go` and `sling.go`).

### Why #5 (root cause): Why was the lifecycle designed to infer the deletion set from current state rather than record the introducing action?
Because the cleanup was implemented as a "re-derive what to remove by comparing against
the factory" heuristic instead of a "record-then-reverse" operation. This is the exact
anti-pattern ADR-017 was written to forbid: it says af may manage its own artifacts but
"Customer content ... must not be deleted. Track provenance to distinguish"
(docs/architecture/adrs/ADR-017-no-customer-repo-mutations.md:31-35). The same ADR notes
this principle "has been violated three times ... each time by code that constructed a
path inside the factory root and deleted whatever it found there" (ADR-017:13-15).
**Root cause:** the merge→cleanup lifecycle has no provenance/manifest contract — merge
does not persist the set of dirs it created, so cleanup is forced to guess via name
collision against the live factory dir, conflating lifecycle-introduced copies with
branch-committed skills.

## Evidence Gathered
| Finding | Source | Evidence |
|---------|--------|----------|
| `mergeSkillsDir` returns only a count, discards names | worktree.go:99,131,133 | `func mergeSkillsDir(...) (int, error)` ... `merged++` ... `return merged, nil` — `entry.Name()` is used to copy (line 106) but never recorded |
| Caller persists nothing from the merge | worktree.go:78-84 | `merged, err := mergeSkillsDir(...)`; only uses `merged > 0` to print an info line |
| `Meta` struct has no provenance/merged field | worktree.go:27-35 | fields: `ID, Owner, Branch, Path, Agents, CreatedAt, ParentBranch` — no merged-skills slot |
| meta.json is the only per-worktree persisted state, written from Meta | worktree.go:296-327 | `WriteMeta`/`ReadMeta` (de)serialize `Meta` to `{ID}.meta.json` — nothing else |
| `cleanupMergedSkills` re-reads live factory dir at teardown | worktree.go:514-520 | `factorySkillsDir := filepath.Join(factoryRoot, skillsRel)`; `entries, _ := os.ReadDir(factorySkillsDir)`; `for ... os.RemoveAll(filepath.Join(wtSkillsDir, entry.Name()))` |
| Deletion criterion is pure name-match against factory, no provenance check | worktree.go:519-520 | loop over factory entries, unconditional `os.RemoveAll` of same-named worktree entry |
| No manifest/marker mechanism anywhere in repo | grep across `*.go/*.json/*.md` | only unrelated `merged` vars in `formula/vars.go:152-169`, `sling.go:460-482`; no skills manifest/marker |
| Existing test encodes the buggy contract: deletes any factory-named entry | worktree_test.go:1815-1847 | skill-A/B "merged" and git-only-skill set up identically as real dirs; test asserts A/B deleted purely because they match factory names — a git-tracked skill named `skill-A` would be deleted too |
| ADR mandates provenance tracking to avoid exactly this | ADR-017:31-35 | "Customer content ... must not be deleted. Track provenance to distinguish" |
| ADR notes this class of bug recurred 3x | ADR-017:13-15 | "violated three times ... code that constructed a path inside the factory root and deleted whatever it found there" |

## Tests Performed
| Test | Command | Result |
|------|---------|--------|
| Locate all merge/cleanup/manifest refs in non-test source | `grep -rn "manifest\|provenance\|merged\|mergeSkillsDir\|cleanupMergedSkills" --include=*.go internal/` | Confirmed: only the count `merged`; no provenance persisted; no manifest |
| Search worktree pkg for marker/merged-tracking fields | `grep -rn "marker\|\.merged\|MergedBy\|MergedSkills\|SkillsMerged\|introduced" --include=*.go internal/worktree/` | No matches except the function names themselves; no tracking field exists |
| Repo-wide search for manifest/provenance artifacts | `grep -rn "manifest\|provenance\|\.merged\|merged-skills" --include=*.go/*.json/*.md .` | No skills manifest; surfaced ADR-017 mandating provenance tracking |
| Inspect `Meta` struct and meta.json (de)serialization | Read worktree.go:27-35, 296-327 | Confirmed no merged-skills/provenance field on persisted state |
| Inspect existing cleanup test contract | Read worktree_test.go:1815-1847 | Confirmed test deletes entries solely by factory-name match, with no provenance discriminator |

## Conclusion
**VALIDATED.** The lifecycle records no provenance of what it merged. `mergeSkillsDir`
returns only an `int` count and discards the copied skill names (worktree.go:131-133);
the `Meta`/`meta.json` data model has no field for merged skills (worktree.go:27-35,
296-327); and a repo-wide search finds no manifest or marker mechanism. Consequently
`cleanupMergedSkills` reconstructs its deletion set from the *live* factory directory at
teardown (worktree.go:514-520) and deletes every worktree entry whose name matches a
factory skill — the exact behavior the existing test pins (worktree_test.go:1815-1847).
This is a missing-provenance defect: the deletion is inferred from current state rather
than driven by a record of the action being undone, directly contradicting ADR-017's
"Track provenance to distinguish" requirement (ADR-017:31-35). A branch-committed skill
whose name collides with a factory skill is therefore deleted even though the lifecycle
never introduced it.
