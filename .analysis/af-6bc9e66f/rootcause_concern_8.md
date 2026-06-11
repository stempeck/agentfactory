# Concern #8 Investigation: Session-bound skill registration makes deletion surface as lost skill

**Investigated by**: Sub-agent
**Date**: 2026-06-11

## Verdict: VALIDATED

## Summary
The concern decomposes into two claims of differing verifiability. The first — that a
worktree working-tree deletion of `.claude/skills/<name>/SKILL.md` actually occurs and
strikes git-committed skill files when names collide with factory-root skills — is fully
VALIDATED from in-repo code: `cleanupMergedSkills` (`internal/worktree/worktree.go:507-522`)
`os.RemoveAll`s the entire worktree skill directory (SKILL.md included) for every entry whose
name matches a factory-root skill, with no record of whether that entry was merge-copied or
branch-committed. `github-issue` is a confirmed collision: it exists in factory-root
`.claude/skills/` AND is git-tracked at `.claude/skills/github-issue/SKILL.md` on the worktree
branch, and the factory and worktree copies are byte-identical. The second claim — that Claude
Code skill registration is *session-bound* (harness scans `.claude/skills/*/SKILL.md` at
SessionStart, a deleted SKILL.md is unloadable for the whole session, `/reload-skills` or a fresh
session re-registers it) — describes the external Claude Code harness and is NOT verifiable from
this repo's code (no agentfactory code scans skills to build an available-skills list; the only
in-repo `SessionStart` hooks run `af prime`/`af mail check`). That part is plausible
*environmental context* consistent with the system's design but is documented-harness-behavior,
not a code-verifiable fact. The overall concern — that a working-tree deletion produces a
session-long loss of the skill — holds: the deletion is real and code-confirmed, and the
session-bound surfacing is the standard, documented behavior of the harness this project runs on.

## 5-Whys Analysis

### Why #1: Why did `/github-issue` become unavailable mid-session even though it is committed to the branch?
Because the SKILL.md that backs it was deleted from the live worktree's working tree. The skill is
loaded by *path* (`.claude/skills/github-issue/SKILL.md`), and that path no longer pointed to a
file. Code-verified: the file is git-tracked on the branch
(`git ls-files .claude/skills/github-issue/` → `.claude/skills/github-issue/SKILL.md`) yet the
teardown routine removes it (see Why #2).

### Why #2: Why was the committed SKILL.md deleted from the working tree?
Because `cleanupMergedSkills` (`internal/worktree/worktree.go:507-522`) iterates every entry in
factory-root `.claude/skills/` and `os.RemoveAll(filepath.Join(wtSkillsDir, entry.Name()))`
(`worktree.go:519-520`) on the worktree's same-named directory. `github-issue` is a factory-root
skill, so the entire `.claude/skills/github-issue/` directory — including its tracked SKILL.md — is
removed. The removal is the whole directory tree, not just merge-introduced files.

### Why #3: Why does cleanup delete a branch-committed file it never created?
Because cleanup identifies "merged" skills purely by *name match against the factory root*, with no
record of provenance. There is no marker distinguishing a skill that was merge-copied in at creation
from one the branch committed itself. Any worktree skill whose name appears in the factory root is
treated as factory-introduced and destroyed.

### Why #4: Why is name-match-without-provenance wrong here?
Because of a creation/teardown asymmetry. Creation (`mergeSkillsDir`, `worktree.go:99-134`) is
name-aware and *preserving*: it copies a factory skill into the worktree only if no entry of that
name already exists (`worktree.go:107-109` — `os.Stat(destPath); err == nil { continue }`), so a
branch-committed skill is left untouched and is never actually "merged." Teardown
(`cleanupMergedSkills`) is name-match and *destroying*, deleting that same untouched committed skill.
The two halves disagree on what "a factory skill present in the worktree" means.

### Why #5 (root cause): Why does a working-tree deletion produce a *session-long* loss rather than a transient blip?
Two compounding root causes:

  (a) **Code root cause (in-repo, verified):** `cleanupMergedSkills` lacks provenance tracking and
  uses a destructive `os.RemoveAll` keyed only on factory-root name collision, so it physically
  removes branch-committed SKILL.md files. In `Remove` (`worktree.go:526+`) the mutation happens
  *before* `git worktree remove`, which refuses a dirty tree — so when a collision-bearing worktree
  is still live, the file is already gone but the worktree persists, leaving a live working tree
  diverged from its own HEAD with the SKILL.md showing as ` D`.

  (b) **Environmental root cause (Claude Code harness, NOT in-repo-verifiable):** Claude Code is
  documented/claimed to register skills by scanning `.claude/skills/*/SKILL.md` once at SessionStart
  to build the available-skills list. Registration is therefore session-bound: once the path is gone
  at scan time (or the running session's view is invalidated), the skill stays unavailable for the
  remainder of that session regardless of the fact that the deletion is only an uncommitted
  working-tree change. Recovery requires restoring the file AND `/reload-skills` or a fresh session.
  This is the mechanism that turns a filesystem-level deletion into a session-duration capability
  loss. It cannot be confirmed from this repository's code — agentfactory contains no skill-scanning
  logic — so it is recorded as environmental context, not a code-verified fact.

**Root cause statement:** A provenance-blind, name-collision-keyed `os.RemoveAll` in
`cleanupMergedSkills` deletes branch-committed SKILL.md files (in-repo, verified); the session-bound
nature of Claude Code's SessionStart skill scan then converts that one-time filesystem deletion into
a loss that persists for the entire session (environmental, harness-documented).

## Evidence Gathered
| Finding | Source | Evidence |
|---------|--------|----------|
| Cleanup removes whole skill dir (incl. SKILL.md) by factory name-match | `internal/worktree/worktree.go:519-520` | `for _, entry := range entries { os.RemoveAll(filepath.Join(wtSkillsDir, entry.Name())) }` over `os.ReadDir(factorySkillsDir)` |
| Cleanup is invoked during teardown, before `git worktree remove` | `internal/worktree/worktree.go:530`, `:561` | `cleanupMergedSkills(factoryRoot, absPath)` called in both `Remove` and `ForceRemove` |
| Creation is name-preserving (does NOT overwrite an existing entry) | `internal/worktree/worktree.go:107-109` | `if _, err := os.Stat(destPath); err == nil { continue }` |
| `github-issue` exists in factory-root skills | `ls /home/dev/af/agentfactory/.claude/skills/` | `architecture-docs documentation-update formula-create github-issue improve-agent rapid-implement` |
| `github-issue/SKILL.md` is git-tracked on the worktree branch (the collision) | `git ls-files .claude/skills/github-issue/` (worktree) | `.claude/skills/github-issue/SKILL.md` |
| Worktree's tracked skills all collide by name with factory skills | `git ls-files .claude/skills/` (worktree) | tracked: documentation-update, formula-create, github-issue, rapid-implement — all present in factory root |
| Factory-root and worktree-tracked `github-issue/SKILL.md` are identical | `diff -q` | `IDENTICAL` (so the merge step's "skip if exists" left the committed file untouched, but cleanup still deletes it) |
| SessionStart hooks in this repo run `af prime`/mail, NOT a skill scan | `USING_AGENTFACTORY.md:211`; `internal/claude/config/settings-*.json:3`; `docs/architecture/subsystems/embedded-assets.md:21-24` | `SessionStart` → `af prime --hook` (+ `af mail check --inject` for autonomous); no skill-scanning code |
| No in-repo code/doc implements or describes the harness skill scan | `grep -rni "scans the .claude/skills\|available-skills list\|reload-skills"` across repo | No agentfactory match; only the issue/concern notes reference it — confirms it is external harness behavior |
| Issue #59 explicitly labels the session-bound claim as harness behavior + repro | `gh issue view 59` | "Claude Code skill registration is session-bound. The harness scans `.claude/skills/*/SKILL.md` at SessionStart ... Restoring the file plus `/reload-skills` (or a fresh session) re-registers it. This is how the bug first surfaced: `/github-issue` was unavailable ..." |

## Tests Performed
| Test | Command | Result |
|------|---------|--------|
| Read cleanup function | `Read worktree.go:507-522` | Confirmed `os.RemoveAll` of whole worktree skill dir per factory name-match |
| Read creation/merge function | `Read worktree.go:99-134` | Confirmed `mergeSkillsDir` skips existing entries (preserving) — proves asymmetry |
| Factory-root skills present | `ls /home/dev/af/agentfactory/.claude/skills/` | 6 skills incl. `github-issue` |
| Worktree skill files exist on disk now | `ls .../wt-2a7c06/.claude/skills/github-issue/` | `SKILL.md` present (12559 bytes) |
| Git tracking (factory) | `git ls-files .claude/skills/github-issue/` | `.claude/skills/github-issue/SKILL.md` tracked |
| Git tracking (worktree branch) | `git ls-files .claude/skills/` (worktree) | documentation-update, formula-create (+skillmd-mode.md), github-issue, rapid-implement — all collide |
| Identity of the two SKILL.md copies | `diff -q factory worktree github-issue/SKILL.md` | `IDENTICAL` |
| Search for in-repo skill-scan/registration logic | `grep -rni "SessionStart\|reload-skills\|available-skills\|scan.*skill"` | Only `af prime`/mail hooks found; no skill scanner — confirms harness behavior is external |
| Confirm issue framing | `gh issue view 59` | Concern #8 text matches the "Additional Context" bullet verbatim |

## Conclusion
**VALIDATED.** The deletion at the heart of this concern is a real, code-confirmed defect:
`cleanupMergedSkills` (`internal/worktree/worktree.go:507-522`) `os.RemoveAll`s any worktree skill
directory whose name collides with a factory-root skill, and `github-issue` is a confirmed collision
(factory-root skill + git-tracked-on-branch SKILL.md, byte-identical copies). Because the creation
path (`mergeSkillsDir`, `:107-109`) is preserving while teardown is destroying, a branch-committed
SKILL.md that was never actually merged in still gets deleted — and in `Remove` this happens before
the dirty-tree-refusing `git worktree remove`, leaving a live worktree whose working tree shows the
committed SKILL.md as deleted.

The *session-bound* half of the concern (harness scans `.claude/skills/*/SKILL.md` at SessionStart;
deletion is unrecoverable mid-session without `/reload-skills` or a fresh session) is the correct
explanation for why the deletion surfaced as `/github-issue` being unavailable, but it describes the
external Claude Code harness and is **not verifiable from this repository** — there is no
agentfactory code that scans skills or builds an available-skills list; the only in-repo SessionStart
hooks run `af prime`/`af mail` (`USING_AGENTFACTORY.md:211`,
`internal/claude/config/settings-*.json`). It is therefore recorded as plausible, internally
consistent **environmental context** (also asserted in issue #59 and matching this very session's
available-skills behavior) rather than a code-verified fact. The concern as a whole — that a
working-tree deletion produces a session-long loss of the skill — is VALIDATED: the deletion is real
and code-confirmed, and the session-scoped surfacing follows directly from the documented harness
registration model.
