# Concern #9 Investigation: Systemic gaps blocking end-to-end autonomy to a pushed PR

**Investigated by**: Sub-agent
**Date**: 2026-06-28

## Verdict: VALIDATED

## Summary
Fixing `dispatch.json` ALONE does not satisfy issue #73's acceptance criterion. The
dispatchable formulas (`rapid-implement`, `rapid-soldesign-plan`) reach a pushed PR only
by invoking `git push -u origin ...` and `gh pr create` in their tail steps, both of which
hard-depend on infrastructure that NO `af` command provisions: (a) a configured `origin`
remote pointing at GitHub, and (b) authenticated `gh` / git push credentials. On the
documented fresh-container path these are provisioned at the CONTAINER layer by
`quickdocker.sh` (`gh auth login --with-token` + `gh auth setup-git`, lines 557-567), not
by `af install --init`. So a fresh repo created only via `af install --init` (no GitHub
remote, no `gh` auth) cannot reach a pushed PR autonomously — `detectDefaultBranch` degrades
to the "main" sentinel with a warning (`sling.go:506`), and the final push/PR steps fail.
Critically, there is NO `doctor` command in the codebase at all — "doctor --fix" exists ONLY
as a negative guard in `e2e_sling_test.go:67-69`, so acceptance's "no doctor --fix" clause is
about asserting that property in tests, not about an existing tool. The systemic gaps to NAME
are: (1) origin remote provisioning + (2) `gh`/push auth provisioning happen outside `af`, and
(3) the default `dispatch.json` shipped by `install.go:145` is empty (no repos/mappings).

## 5-Whys Analysis

### Why #1: Why might `af sling --agent <agent> "task"` fail to reach a pushed PR on a fresh setup?
Because the formula's final steps run `git push -u origin <branch>` and `gh pr create`
(`rapid-implement.formula.toml:803, :881`; `rapid-soldesign-plan.formula.toml:496, :527`),
which require an `origin` remote and authenticated push/`gh` credentials.

### Why #2: Why would those credentials/remote be missing on a fresh setup?
Because `af install --init` provisions NO git remote and NO `gh` auth. Searching install.go
for `git remote`/`origin`/`gh auth` returns nothing relevant (only `.git/info/exclude` hook
plumbing). The default branch detection chain (`detect_default_branch.go:52-73`) requires an
`origin` remote in all three methods (local `origin/HEAD`, `ls-remote origin`, `gh repo view`).

### Why #3: Why is the remote/auth not provisioned by `af`?
By design, these live at the container/setup layer. `quickdocker.sh` injects the PAT via
`gh auth login --with-token` (line 557) and wires git credentials via `gh auth setup-git`
(line 567), and derives the repo from `git remote get-url origin` of the host clone (line 41).
So the documented fresh-container path DOES have auth+remote — but a bare `af install --init`
in an arbitrary repo does not, and nothing in `af` repairs that.

### Why #4: Why doesn't `dispatch.json` alone close the gap?
The default `dispatch.json` written by `install.go:145` ships with empty `repos` and empty
`mappings`, so label-dispatch matches nothing (the existing rootcause_analysis.md confirms
this, concerns #1-#8). Even a fully populated `dispatch.json` only routes a labeled issue to
an agent+formula; it does not create an `origin` remote or `gh` auth. The push/PR steps still
depend on the container-layer provisioning. So `dispatch.json` is necessary but not sufficient.

### Why #5: Why does the "no doctor --fix" clause matter, and is any run dependent on doctor?
There is NO `doctor` command in this codebase (no `cmdDoctor`, no `"doctor"` registration; grep
of internal/cmd for `doctor` outside tests returns nothing). "doctor --fix" appears ONLY as a
negative assertion in `e2e_sling_test.go:67-69` ("sling output must not contain 'doctor --fix'").
Therefore NO agent run is dependent on `doctor --fix`; the acceptance clause is a property to
hold (and is already test-guarded), not a dependency to remove. This is an INVALIDATED sub-claim
within an otherwise VALIDATED concern.

## Evidence Gathered
| Finding | Source | Evidence |
|---------|--------|----------|
| Final step pushes branch & opens PR via `gh pr create` | `internal/cmd/install_formulas/rapid-implement.formula.toml:803, :881` | `git push -u origin $(git branch --show-current)` then `gh pr create --title ... --body ...` |
| rapid-soldesign-plan pushes & creates PR | `internal/cmd/install_formulas/rapid-soldesign-plan.formula.toml:496, :527` | `git push origin HEAD`; `PR_URL=$(gh pr create --title ... --body ...)`; aborts/escalates if PR_URL not a URL (`:536-540`) |
| Formula HARD-requires `gh` auth, fails fast if missing | `internal/cmd/install_formulas/rapid-soldesign-plan.formula.toml:146-149` | `gh auth status \|\| { echo "ERROR: gh CLI not authenticated. Run 'gh auth login' first."; exit 1; }` — explicit human-action error |
| Worktree branch is LOCAL with NO upstream | `internal/worktree/worktree.go:500` | `git worktree add --quiet -b <branch>` creates a fresh local branch; no `--set-upstream`, hence formulas use `push -u origin` |
| NO `doctor` command exists | grep `internal/cmd/*.go` (non-test) for `doctor`/`cmdDoctor`/`--fix` | Zero hits; only reference is the negative guard in `e2e_sling_test.go:67-69` |
| "doctor --fix" is a test guard, not a tool | `internal/cmd/e2e_sling_test.go:67-69` | `if strings.Contains(slingOut, "doctor --fix") { t.Fatalf(...) }` |
| No agent run depends on doctor --fix | (consequence of above) | Nothing to remove; clause is a property assertion |
| `af install --init` provisions NO git remote / gh auth | `internal/cmd/install.go` | grep for `git remote`/`origin`/`gh auth`/`remote add` returns only `.git/info/exclude` hook plumbing; no remote/auth setup |
| Default `dispatch.json` is empty (no repos/mappings) | `internal/cmd/install.go:145` | `{"repos":[],...,"mappings":[],...}` — label-dispatch matches nothing out of the box |
| Default-branch detection requires an `origin` remote | `internal/cmd/detect_default_branch.go:52-73` | All 3 methods key off `origin` (symbolic-ref `refs/remotes/origin/HEAD`, `ls-remote --symref origin`, `gh repo view`); returns "" otherwise |
| sling fails LOUD then baits "main" if no remote | `internal/cmd/sling.go:504-507` | `warning: could not detect repository default branch (origin/HEAD unset, no GitHub remote); re-run with --var default_branch=<name>` then sets sentinel `main` |
| Container path DOES provision auth + git creds | `quickdocker.sh:557, :567` | `gh auth login --with-token`; `gh auth setup-git` (configures git credential helper) |
| Container path derives repo from host remote | `quickdocker.sh:41` | `AF_REMOTE="$(git -C "$SCRIPT_DIR" remote get-url origin ...)"` — provisioning knows the repo, `install.go` does not |
| Dispatcher itself gates on gh auth | `internal/cmd/dispatch.go:150-153, :292-295` | `checkGHAuth()` runs `gh auth status`; dispatch errors "GitHub CLI not authenticated" if it fails |

## Tests Performed
| Test | Command | Result |
|------|---------|--------|
| Is `doctor` a real command? | `grep -rn '"doctor"\|cmdDoctor\|Doctor\|--fix' internal/cmd/ (non-test)` | No hits — `doctor` does not exist as a command |
| Where does "doctor --fix" string live? | `grep -rn 'doctor' internal/cmd/` | Only `e2e_sling_test.go:67-68` (a negative guard) |
| Do formulas push + open PR in tail steps? | Read `rapid-implement.formula.toml` :785-911, `rapid-soldesign-plan.formula.toml` :485-541 | Yes: `git push -u origin` / `git push origin HEAD` + `gh pr create` |
| Does any formula require `gh` auth / a remote? | Read `rapid-soldesign-plan.formula.toml:146-149` | Yes — explicit `gh auth status \|\| exit 1` |
| Does `af install --init` set up a remote / gh auth? | grep `install.go` for remote/origin/gh auth | No |
| Does default-branch detection need a remote? | Read `detect_default_branch.go:52-73` | Yes — all 3 methods require `origin` |
| Does the fresh-container path provision auth? | Read `quickdocker.sh:557, :567` | Yes — `gh auth login --with-token` + `gh auth setup-git` |
| Is default `dispatch.json` populated? | Read `install.go:145` | No — empty repos + mappings |

## Conclusion

**Verdict: VALIDATED.** Real systemic gaps exist beyond `dispatch.json`. Fixing
`dispatch.json` alone is necessary but NOT sufficient for issue #73's acceptance criterion.

Named systemic gaps (with scope tags):

1. **`origin` GitHub remote provisioning** — `[likely OUT-OF-SCOPE for the immediate
   dispatch.json fix, but MUST be named]`. The push/PR tail steps
   (`rapid-implement.formula.toml:803/:881`, `rapid-soldesign-plan.formula.toml:496/:527`)
   and default-branch detection (`detect_default_branch.go:52-73`) all require an `origin`
   remote that `af install --init` never creates. Provisioned only at the container layer
   (`quickdocker.sh:41`). A fresh repo without a GitHub remote cannot reach a pushed PR.

2. **`gh` / git-push authentication provisioning** — `[OUT-OF-SCOPE for dispatch.json, but
   MUST be named]`. `gh pr create` and `git push` need authenticated credentials; one formula
   even fails fast on `gh auth status` (`rapid-soldesign-plan.formula.toml:146-149`), and the
   dispatcher gates on `checkGHAuth` (`dispatch.go:150-153`). Provisioned only by
   `quickdocker.sh:557/:567` (`gh auth login --with-token` + `gh auth setup-git`), NOT by `af`.
   On a bare `af install --init` setup with no auth, autonomy to a PR is impossible.

3. **Empty default `dispatch.json`** — `[IN-SCOPE — this is the primary issue #73 fix]`.
   `install.go:145` ships empty `repos`/`mappings`, so label-dispatch matches nothing. (Covered
   in depth by concerns #1-#8; named here for completeness of the end-to-end chain.)

4. **No declared `origin` upstream on the worktree branch** — `[IN-SCOPE as a documentation/
   formula concern; LOW severity]`. `worktree.go:500` creates a local branch with no upstream;
   the formulas correctly compensate with `push -u origin`, so this is handled, but it MEANS the
   push step strictly depends on gaps #1 and #2 — confirming the chain.

Sub-claim INVALIDATED: there is NO ongoing `doctor --fix` dependency to remove — `doctor` is
not a command in this codebase; "doctor --fix" exists only as a negative test guard
(`e2e_sling_test.go:67-69`). The acceptance clause is already a test-enforced property.

**Needs review (uncertain):** Whether issue #73 intends gaps #1/#2 to be solved within this
change (e.g., by extending `quickstart.sh`/`quickdocker.sh` and/or `install --init` to bake a
remote/auth check), or whether the fresh-container path already satisfies them and only
`dispatch.json` population is in scope. The codebase shows the container path provisions
auth+remote, so on the INTENDED (quickdocker) fresh setup the only gap is the empty
`dispatch.json`; on a bare `af install --init`-only setup, gaps #1/#2 also bite. This scope
boundary should be confirmed with the issue author / Supervisor.
