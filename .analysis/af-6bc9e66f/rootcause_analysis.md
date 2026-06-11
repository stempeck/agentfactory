# Root Cause Analysis: Worktree teardown deletes branch-committed skill files when names collide with factory-root skills

**Date**: 2026-06-11
**Status**: Synthesized — all concerns VALIDATED (10/10)
**Problem File**: Bead af-6bc9e66f → GitHub issue https://github.com/stempeck/agentfactory/issues/59
**Primary code under analysis**: `internal/worktree/worktree.go`, `internal/worktree/worktree_test.go`, CLI teardown entry points (`internal/cmd/{down,reset,sling,done}.go`)

## Problem Summary

On worktree teardown, `cleanupMergedSkills()` `os.RemoveAll`s every entry in the worktree's
`.claude/skills/` whose directory name matches a skill present in the factory-root
`.claude/skills/` — with no record of whether that skill was copied in by the merge step
(`mergeSkillsDir`) or committed to the worktree's own branch. Branches that track real skill
files under `.claude/skills/` therefore have those committed files destroyed during teardown,
surfacing as uncommitted ` D` (deleted) entries in the live worktree's `git status` and making
the affected skills unloadable for the Claude Code session.

## Concerns from Problem File

All ten concerns were independently investigated by parallel sub-agents in Phase 2.
**Every concern returned VALIDATED (10/10); none INVALIDATED.** Each verdict is backed by
code reading and, in most cases, an empirical reproduction (see linked evidence files).

| # | Concern | Verdict | Evidence Link |
|---|---------|---------|---------------|
| 1 | `cleanupMergedSkills` (worktree.go:507-522) deletes worktree skill directories purely by factory-root name match (`os.RemoveAll` at line 520), with NO provenance check — so any branch-committed skill whose name collides with a factory skill is destroyed. | VALIDATED | rootcause_concern_1.md |
| 2 | Structural asymmetry: creation via `mergeSkillsDir` (line 107-109) is name-aware and *preserving* (copies a factory skill only if no dir of that name already exists); teardown via `cleanupMergedSkills` is name-match and *destroying*. This asymmetry is the proposed structural root cause. | VALIDATED | rootcause_concern_2.md |
| 3 | `cleanupMergedSkills` keeps no record/manifest of which skills the lifecycle actually introduced via merge; it infers "merged" solely from a name appearing in the factory root, which is equally true of every branch-committed skill of the same name. | VALIDATED | rootcause_concern_3.md |
| 4 | All five teardown entry points reach `cleanupMergedSkills` via `Remove`/`ForceRemove`: `af down` (down.go), `af down --reset` (reset.go), `af sling --reset` (sling.go), formula-completion (done.go), and `GC()` (worktree.go:609). | VALIDATED | rootcause_concern_4.md |
| 5 | The repo tracks real files under `.claude/skills/` at HEAD (`git ls-files .claude/skills/` returns multiple paths), while the worktree system *also* treats `.claude/skills` as a symlinked/merged shared resource (`worktreeSymlinks`, line 51-55). These two designs coexist on any branch that commits skill files — the structural precondition for the bug. | VALIDATED | rootcause_concern_5.md |
| 6 | Trigger condition: the destructive path only runs when the worktree's `.claude/skills` is a *real directory* (not a symlink). `cleanupMergedSkills` no-ops on a symlink (line 511) and `EnsureWorktreeLinks` symlinks the whole dir only when none exists; a branch that commits skill files forces the real-directory/merge path. | VALIDATED | rootcause_concern_6.md |
| 7 | Ordering hazard: in both `Remove` (526-553) and `ForceRemove` (558-582), `cleanupMergedSkills` mutates the worktree *before* `git worktree remove` runs. `Remove`'s non-force git step refuses a dirty tree, so if it fails the worktree survives with its committed skills already deleted — a live worktree whose working tree diverges from its own HEAD. | VALIDATED | rootcause_concern_7.md |
| 8 | Surfacing symptom / environmental claim: Claude Code skill registration is session-bound (harness scans `.claude/skills/*/SKILL.md` at SessionStart). A deleted `SKILL.md` makes that skill unloadable for the whole session even though the deletion is only an uncommitted working-tree change. This is how the bug first surfaced (`/github-issue` unavailable). | VALIDATED | rootcause_concern_8.md |
| 9 | Test gap: the existing teardown test `TestCleanupMergedSkills_RemovesFactoryCopiedEntries` (worktree_test.go:1815) passes *precisely because* its surviving skill (`git-only-skill`) is named to avoid any factory collision; there is NO test asserting that a branch-committed skill whose name collides with a factory skill survives teardown. | VALIDATED | rootcause_concern_9.md |
| 10 | Concrete repro fidelity: with the factory-root skill set (architecture-docs, documentation-update, formula-create, github-issue, improve-agent, rapid-implement) and a branch tracking documentation-update, formula-create, github-issue, rapid-implement, exactly the four colliding names get deleted while the two factory-only skills are no-ops. Verify the factory skill set and the overlap. | VALIDATED | rootcause_concern_10.md |

## Synthesized Root Cause(s)

Based on investigation of 10 concerns:
- **10 concerns VALIDATED** as contributing factors
- **0 concerns INVALIDATED**

The validated concerns are not ten independent bugs; they are facets of a single defect plus
its enabling preconditions, amplifiers, surfacing mechanism, and the test gap that let it ship.

### Primary Root Cause

**`cleanupMergedSkills` decides what to delete by *inferring* provenance from current state
(name collision with the live factory `.claude/skills/`) instead of *recording* what the
lifecycle actually introduced — so it destroys branch-committed skills whose directory name
collides with a factory skill.**
(from concerns #1, #2, #3 → `rootcause_concern_1.md`, `_2.md`, `_3.md`)

This single root cause has three nested facets, deepest last:

1. **Proximate defect (concern #1):** `cleanupMergedSkills` (`internal/worktree/worktree.go:507-522`)
   enumerates the *factory-root* skill names (line 515) and runs an unconditional
   `os.RemoveAll(filepath.Join(wtSkillsDir, entry.Name()))` (line 520) for each — no content
   comparison, no `os.SameFile`/hash, no git-tracked check, no provenance guard.

2. **Structural form (concern #2):** creation and teardown are **asymmetric on the same key**.
   `mergeSkillsDir` is name-aware and *preserving* — it copies a factory skill into the worktree
   only if no entry of that name already exists (`if _, err := os.Stat(destPath); err == nil { continue }`,
   lines 107-109), so branch-committed skills survive creation. `cleanupMergedSkills` is name-match
   and *destroying*. A name that creation deliberately **preserves** is exactly a name that teardown
   **destroys**.

3. **Deepest design cause (concern #3):** the merge step persists **no provenance**. `mergeSkillsDir`
   returns only an `int` count (line 133), discards `entry.Name()`, and the caller logs the count and
   stores nothing; `Meta`/`meta.json` (lines 27-35, 296-327) has no merged-skills field; no manifest or
   marker exists anywhere in the repo. Teardown therefore *must* reconstruct its delete-set from the
   live factory directory — a proxy that is wrong precisely for collision cases. This is a verbatim
   recurrence of the anti-pattern **ADR-017** was written to forbid: *"Customer content … must not be
   deleted. Track provenance to distinguish"* (`docs/architecture/adrs/ADR-017-no-customer-repo-mutations.md:31-35`),
   an anti-pattern that ADR notes has *"been violated three times … each time by code that constructed
   a path inside the factory root and deleted whatever it found there"* (ADR-017:13-15). Issue #59 is the
   **fourth** recurrence.

### Contributing Factors

**A. Structural precondition — when the bug can fire (concerns #5, #6).**
The repo git-tracks real files under `.claude/skills/` at HEAD, while the worktree subsystem *also*
declares `.claude/skills` a shared symlink/merge resource (`worktreeSymlinks`, lines 51-55). When a
branch commits skill files, `git worktree add` (line 377) materializes `.claude/skills` as a **real
directory** *before* `EnsureWorktreeLinks` runs (line 399), forcing the merge branch (lines 77-86) and
leaving a real dir. Teardown's symlink guard (line 511) is then bypassed and the destructive loop runs.
The clean (no committed skills) case yields a **symlink**, which `unlinkBeforeRemove` deletes and the
guard no-ops on. So: **branch-committed real dir ⇒ destructive; symlink ⇒ safe.**

**B. Blast radius — every teardown path is affected (concern #4).**
Both teardown primitives `Remove` (line 530) and `ForceRemove` (line 561) hard-code the
`cleanupMergedSkills` call with no skip flag, so `af down`, `af down --reset`, `af done` formula
completion, and `GC()` all reach it. Refinements found during investigation: `af sling --reset`
reaches teardown only in its `--agent` form (`sling.go:139`), **not** its `--formula` form
(`sling.go:284-298`); and a **sixth, uncited trigger** exists — `ResolveOrCreate` calls `GC()` during
ordinary agent startup (`worktree.go:686`), so the destructive path can fire even with no explicit
teardown command.

**C. Ordering hazard — explains the observed live-corrupted worktree (concern #7).**
In `Remove`, `cleanupMergedSkills` mutates the working tree (line 530) *before* a **non-force**
`git worktree remove` (line 533). Git refuses to remove a dirty worktree, so the just-deleted committed
skills (now ` D` deletions) make the remove fail with exit 128, and `Remove` returns at line 536 — leaving
a **live worktree whose working tree diverges from its own HEAD**. This is exactly the observed ` D`
skill-entry state in issue #59. (`ForceRemove`'s `--force` masks the symptom but still performs the
destructive deletion.)

**D. Surfacing mechanism — why a working-tree deletion became a lost skill (concern #8).**
The deletion removes the whole skill directory including `SKILL.md`. Claude Code registers skills by
scanning `.claude/skills/*/SKILL.md` at SessionStart, so a deleted `SKILL.md` makes the skill unloadable
for the entire session (how `/github-issue` first went missing). The deletion is **code-verified in-repo**;
the session-bound registration is **documented external-harness behavior**, recorded as environmental
context rather than a repo-verifiable fact.

**E. Test gap — why it shipped green (concern #9).**
The only survival test, `TestCleanupMergedSkills_RemovesFactoryCopiedEntries` (`worktree_test.go:1815`),
uses a survivor named `git-only-skill` whose name is deliberately **disjoint** from the factory skills, so
the destructive name-match path is never traversed for it. No test asserts that a branch-committed skill
whose name **collides** with a factory skill survives teardown — the exact failing scenario. The suite
validated the happy path of a name-keyed delete rather than probing its destructive boundary.

**F. Repro fidelity — the concrete case is exact (concern #10).**
Current on-disk state matches the issue name-for-name: factory skills =
{architecture-docs, documentation-update, formula-create, github-issue, improve-agent, rapid-implement};
branch-tracked = {documentation-update, formula-create (+skillmd-mode.md), github-issue, rapid-implement};
the 4-way intersection is deleted, the 2 factory-only skills are no-ops.

**Provenance note:** the destructive merge+cleanup machinery was introduced by commit **c8f8702**
("merge factory skills into git-tracked worktree skill directories", PR #50, Fixes #49) — see concern #6.

## Fishbone Diagram

```
                                                                  [PROBLEM]
                          Worktree teardown deletes branch-committed .claude/skills files
                                    when names collide with factory-root skills
                                                                      ▲
                                                                      │
   PRIMARY CAUSE                          PRECONDITION                │              AMPLIFIERS
   (no provenance / name-match)           (when it fires)             │              (reach & corruption)
        │                                       │                     │                     │
        │ #1 cleanupMergedSkills:               │ #5 git-tracked      │   #4 ALL teardown paths funnel through
        │    unconditional os.RemoveAll         │    skills coexist    │      Remove(:530)/ForceRemove(:561);
        │    keyed on factory name (w.go:519-20)│    with symlink/     │      +6th trigger GC@startup (:686)
        ├───►│                                  │    merge design ────►│◄──── │
        │ #2 create PRESERVES / teardown        │    (w.go:51-55) ─────│      #7 cleanup BEFORE non-force
        │    DESTROYS same key (w.go:107-9 vs   │ #6 trigger: real dir │         git remove ⇒ live worktree
        │    519-21) ──────────────────────────►│    (committed) =     │         diverged from HEAD (:530→:533)
        │ #3 NO provenance persisted; teardown  │    destructive;      │
        │    infers from live factory dir;      │    symlink = no-op   │
        │    = ADR-017 4th violation (:514-20) ─►│    (w.go:511,77-86) ►│
        │                                                              │
        │                                                              │
   SURFACING MECHANISM                     TEST GAP                    │      REPRO FIDELITY
        │                                       │                      │             │
        │ #8 SKILL.md deleted →                 │ #9 only survival test │   #10 factory ∩ branch-tracked
        │    Claude Code session-bound          │    uses NON-colliding │       = 4 dirs deleted, 2 no-ops;
        │    skill scan ⇒ skill lost            │    name; collision    │       exact name match, zero drift
        │    for whole session ─────────────────►│   case untested ─────►│       (w.go:519-21) ──────────────►
        │    (env. context, harness)            │   (test:1815-1847)    │
        │                                                              │
        └──────────────────────── ALL 10 CONCERNS VALIDATED ──────────┘

   Legend: w.go = internal/worktree/worktree.go ; test = internal/worktree/worktree_test.go
```

## Solution

### Approach selection (single solution)

Three approaches were evaluated; one is selected.

| Approach | Verdict |
|----------|---------|
| **A — Content/hash comparison**: delete a worktree skill only if byte-identical to the factory copy. | **Rejected.** The repro's committed copies (e.g. `github-issue/SKILL.md`) are *byte-identical* to the factory copy (concern #8, #10), so content comparison would still delete branch-committed work. Fails the exact case. |
| **B — Persisted manifest**: record the names `mergeSkillsDir` copied in `meta.json`, delete only those at teardown. | **Rejected as primary.** Correct in spirit (matches ADR-017's "formula field in agents.json" example) but: (1) every worktree created before the fix has no manifest → migration gap; (2) requires threading merged names out of `EnsureWorktreeLinks` → `Create` → `Meta`. More surface area, weaker coverage of existing worktrees. |
| **C — Git provenance (SELECTED)**: at teardown, treat a skill directory that is *git-tracked in the worktree* as branch content (preserve) and a skill directory that is *untracked* and name-matches a factory skill as a merge copy (remove). | **Selected.** Git is the authoritative record of "what the branch committed." Robust to the byte-identical case, needs no schema change or migration (works for all existing worktrees), and is localized to `cleanupMergedSkills` so **all** teardown paths (concern #4) inherit the fix. |

**Why C also resolves the ordering hazard (concern #7) with no separate change:** the only files `cleanupMergedSkills` deletes become *untracked* merge copies. Removing untracked files makes the tree *cleaner*, and branch-committed skills (now preserved) are clean/committed — so the non-force `git worktree remove` in `Remove` no longer encounters a dirty tree and succeeds. No committed file is ever deleted, so the live-worktree-diverged-from-HEAD state cannot arise. The cleanup-before-remove ordering is therefore acceptable once deletion is provenance-gated; no reordering is required.

### Files to Modify

| File | Change |
|------|--------|
| `internal/worktree/worktree.go` | Rewrite `cleanupMergedSkills` (507-522) to skip git-tracked skill directories; add helper `trackedSkillDirs(worktreePath)`. On inability to determine git tracking, delete nothing (ADR-017 rule 3: "when in doubt, don't delete"). |
| `internal/worktree/worktree_test.go` | (a) Update `TestCleanupMergedSkills_RemovesFactoryCopiedEntries` (1815) so the worktree dir is a real git repo with the "merged" entries left **untracked** (matching production reality), so they are still removed. (b) Add regression test `TestCleanupMergedSkills_PreservesBranchCommittedCollidingSkill` asserting a git-tracked skill whose name collides with a factory skill **survives** teardown while an untracked merge copy is removed. |

### Implementation Steps

**1. Make `cleanupMergedSkills` provenance-aware (`internal/worktree/worktree.go`).**

Replace the body of `cleanupMergedSkills` (lines 507-522) and add `trackedSkillDirs`:

```go
// cleanupMergedSkills removes factory skills that were merge-copied into the
// worktree at creation, WITHOUT touching skills the worktree's branch committed
// itself. Provenance comes from git: a skill directory tracked in the worktree's
// index is branch content and is preserved; an untracked directory whose name
// matches a factory skill is a merge copy and is removed. If git tracking cannot
// be determined, nothing is removed (ADR-017: when in doubt, don't delete).
func cleanupMergedSkills(factoryRoot, worktreePath string) {
	skillsRel := filepath.Join(".claude", "skills")
	wtSkillsDir := filepath.Join(worktreePath, skillsRel)
	fi, err := os.Lstat(wtSkillsDir)
	if err != nil || fi.Mode()&os.ModeSymlink != 0 {
		return
	}
	factorySkillsDir := filepath.Join(factoryRoot, skillsRel)
	entries, err := os.ReadDir(factorySkillsDir)
	if err != nil {
		return
	}
	tracked, ok := trackedSkillDirs(worktreePath)
	if !ok {
		return // provenance unknown — preserve everything (ADR-017)
	}
	for _, entry := range entries {
		if tracked[entry.Name()] {
			continue // branch-committed skill — preserve
		}
		os.RemoveAll(filepath.Join(wtSkillsDir, entry.Name()))
	}
}

// trackedSkillDirs returns the set of top-level directory names under
// .claude/skills tracked in the worktree's git index. ok is false if git
// tracking could not be determined (e.g. git unavailable), in which case the
// caller must not delete anything.
func trackedSkillDirs(worktreePath string) (map[string]bool, bool) {
	cmd := exec.Command("git", "ls-files", "-z", "--", ".claude/skills")
	cmd.Dir = worktreePath
	out, err := cmd.Output()
	if err != nil {
		return nil, false
	}
	const prefix = ".claude/skills/"
	dirs := make(map[string]bool)
	for _, p := range strings.Split(string(out), "\x00") {
		if !strings.HasPrefix(p, prefix) {
			continue
		}
		rest := p[len(prefix):]
		name := rest
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			name = rest[:i]
		}
		if name != "" {
			dirs[name] = true
		}
	}
	return dirs, true
}
```

Notes:
- `exec`, `strings`, `os`, `filepath` are already imported in `worktree.go` — no new imports.
- `git ls-files` emits repo-root-relative paths with `/` separators, so the literal `.claude/skills/` prefix and `/` split are correct on the supported platforms (linux/darwin). `-z` avoids quoting/whitespace issues.
- The symlink guard (line 511) and factory-dir read (515) are unchanged, so the existing symlink no-op behavior (concern #6, `TestCleanupMergedSkills_NoOpOnSymlink`) is preserved.

**2. Add the regression test for the collision case (`internal/worktree/worktree_test.go`).**

Use the package's existing git-repo test helpers (the worktree tests already create real git repos/worktrees). Illustrative shape:

```go
func TestCleanupMergedSkills_PreservesBranchCommittedCollidingSkill(t *testing.T) {
	factoryRoot := t.TempDir()
	for _, s := range []string{"github-issue", "architecture-docs"} {
		dir := filepath.Join(factoryRoot, ".claude", "skills", s)
		mustMkdirAll(t, dir)
		mustWriteFile(t, filepath.Join(dir, "SKILL.md"), "factory "+s)
	}

	// Worktree is a real git repo whose branch COMMITS .claude/skills/github-issue
	// (name collides with a factory skill).
	wt := t.TempDir()
	gitInit(t, wt) // git init + user.name/email config
	committed := filepath.Join(wt, ".claude", "skills", "github-issue", "SKILL.md")
	mustMkdirAll(t, filepath.Dir(committed))
	mustWriteFile(t, committed, "branch-committed github-issue")
	gitAddCommit(t, wt, "add committed skill")

	// An UNTRACKED merge copy of a factory skill also present.
	mergedDir := filepath.Join(wt, ".claude", "skills", "architecture-docs")
	mustMkdirAll(t, mergedDir)
	mustWriteFile(t, filepath.Join(mergedDir, "SKILL.md"), "factory architecture-docs")

	cleanupMergedSkills(factoryRoot, wt)

	if _, err := os.Stat(committed); err != nil {
		t.Fatalf("branch-committed colliding skill was destroyed: %v", err)
	}
	if _, err := os.Stat(mergedDir); !os.IsNotExist(err) {
		t.Fatalf("untracked merge copy should have been removed; err=%v", err)
	}
}
```

**3. Update the existing teardown test to reflect git provenance (`internal/worktree/worktree_test.go`).**

`TestCleanupMergedSkills_RemovesFactoryCopiedEntries` (1815) currently uses a plain temp dir; with git-gated deletion, `trackedSkillDirs` would return `ok=false` and nothing would be removed. Initialize the worktree as a real git repo and leave the "merged" entries (`skill-A`, `skill-B`) **untracked** (do not `git add` them), keeping `git-only-skill` as-is. Then the assertions stand: untracked factory-named copies are removed, the non-colliding `git-only-skill` survives.

### Enforcement Level

| Step | Level | Notes |
|------|-------|-------|
| 1 — provenance-gated deletion in `cleanupMergedSkills` | **Interlock** | The code structurally cannot `os.RemoveAll` a git-tracked skill directory — branch-committed data loss is made *impossible by construction*. The only residual is git itself being unavailable, which **fails safe to preserve** (deletes nothing), so there is no path that deletes committed content. |
| 2 — regression test for collision survival | **Runtime guard (CI)** | Fails the build if a future change reintroduces name-match deletion of branch-committed skills; locks in the interlock. |
| 3 — update existing test to use a git repo | **Advisory/supporting** | Keeps the suite faithful to production (cleanup always runs inside a git worktree); prevents a false failure from the new git dependency. |

The primary fix (Step 1) is an **Interlock**, so no weaker-enforcement escalation is required. For completeness, the interlock property is: *deletion is gated on `!tracked[name]`, and `tracked` is derived from `git ls-files` in the worktree*, so a branch-committed file (always in the index) can never be selected for removal.

### Verification Steps

1. **Build:** `make build` → compiles cleanly (no new imports needed).
2. **New + updated tests pass:**
   `go test ./internal/worktree/ -run 'TestCleanupMergedSkills|TestEnsureWorktreeLinks_RealSkillsDir|TestMergeSkillsDir' -v`
   → all pass, including the new `TestCleanupMergedSkills_PreservesBranchCommittedCollidingSkill`.
3. **Full suite:** `make test` → green.
4. **End-to-end (acceptance criteria):** create a worktree on a branch that commits a skill whose name collides with a factory skill, then tear it down via `af down <agent>`:
   - the committed `.claude/skills/<name>/SKILL.md` remains present and `git status` in any *live* worktree shows **no** ` D` skill deletions;
   - the worktree is removed cleanly (non-force `git worktree remove` succeeds);
   - any untracked merge copy the lifecycle introduced is gone (no stray skill copies left behind).
5. **Skill availability:** a Claude Code session in such a worktree retains `/<name>` skills across lifecycle commands (the SKILL.md is never deleted).

### Code Convention Issues

- The function name `cleanupMergedSkills` becomes *accurate* after the fix (it now cleans up only genuinely-merged, untracked copies). No rename required.
- `trackedSkillDirs` shells out to `git`, consistent with the rest of `worktree.go` (which already runs `git worktree add/remove/branch`).
- **Architectural follow-up (out of scope for this fix, worth a separate note):** ADR-017 has now been violated four times by the same "construct a path in the factory root and delete what's there" shape (designs 170, 173, #50→#59). A lint/test asserting that no `os.RemoveAll` in `internal/cmd`/`internal/worktree` targets a factory-root-derived path without a provenance guard would prevent a fifth recurrence. Recommend filing as a separate hardening bead rather than expanding this fix.
