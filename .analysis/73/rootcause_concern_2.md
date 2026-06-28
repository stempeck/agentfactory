# Concern #2 Investigation: Repo name not auto-discovered/injected at setup

**Investigated by**: Sub-agent
**Date**: 2026-06-28

## Verdict: VALIDATED

## Summary
At factory-init time `af install --init` writes `dispatch.json` from a hardcoded static literal string with an empty `repos:[]` array (`internal/cmd/install.go:145`). The actual `org/repository` is never discovered or injected, even though every realistic install path runs with the repo trivially available: `quickstart.sh` does `cd "$repo_dir"` into the cloned target repo (which has `origin` configured) *before* invoking `af install --init` (`quickstart.sh:428` then `:442`), and `quickdocker.sh` receives the repo explicitly as its `<github-repo-path>` argument (`quickdocker.sh:356`). The literal cannot know the repo because it is a compile-time constant inside a `map[string]string`, and no code path in `runInstallInit` (`install.go:97-297`) ever runs `git remote get-url`, `gh repo view`, or reads its own argument for a repo. The factory already ships a clean, read-only, ADR-009-seam'd git/gh detection idiom (`detectDefaultBranch`/`runGitDetect` in `internal/cmd/detect_default_branch.go`) that is the exact precedent for the fix. Recommended mechanism: detect `org/repo` from the git remote of `getWd()` at init time using a new `detectRepoSlug` built on the existing `runGitDetect` seam; recommended injection point: `runInstallInit`, building the `dispatch.json` content programmatically instead of from the static literal.

## 5-Whys Analysis

### Why #1: Why does a fresh install write an empty `repos:[]` in dispatch.json?
Because `dispatch.json` is produced from a hardcoded compile-time literal in the `starterConfigs` map, with `repos` set to an empty array:
`internal/cmd/install.go:145`:
```go
"dispatch.json":  `{"repos":[],"trigger_label":"agentic","notify_on_complete":"manager","mappings":[],"interval_seconds":300,"retry_after_seconds":1800}`,
```
The map values are written verbatim (write-if-absent) at `install.go:150-157`. A constant string has no access to runtime state, so it structurally *cannot* contain the repo.

### Why #2: Why is the literal static instead of computed?
Because `runInstallInit` treats all starter configs uniformly as static blobs (`install.go:139-157`). Only `factory.json` is built from in-code defaults (`config.DefaultFactoryConfigJSON()`, `install.go:142`); every other config — including `dispatch.json` — is an inline string. There is no per-config "compute then write" branch, so dispatch.json was never given a chance to interrogate the environment.

### Why #3: Why does no code path during setup learn the org/repo?
`runInstallInit` (`install.go:97-297`) never shells out to git or gh for repo identity. The ONLY git interaction is `ensureGitExclude` (`install.go:270-271` -> `install.go:842-878`), which just appends paths to `.git/info/exclude` and never reads the remote. `runInstall` (`install.go:76-95`) takes no repo argument. So even though the info is reachable, the init flow simply never asks for it.

### Why #4: Why is this a missed opportunity rather than an impossibility — is the repo actually available at init time?
Yes, the repo is available at init time on every real path:
- **quickstart path**: `configure_factory` finds the cloned target repo and `cd`s into it (`quickstart.sh:417-428`) *before* `af install --init` (`quickstart.sh:441-445`). The target was cloned via `gh repo clone "$REPO_PATH"` (`quickdocker.sh:582`), so its `origin` remote is set. `runInstallInit` resolves its working dir via `getWd()` (`install.go:98` -> `helpers.go:176-182`, a thin `os.Getwd()`), which is therefore that repo dir — a `git remote get-url origin` / `gh repo view` there would return the slug.
- **quickdocker path**: the slug is handed in *explicitly* as `<github-repo-path>` and normalized to `owner/repo` form (`quickdocker.sh:355-362`, stored in `REPO_PATH`). quickdocker even already parses its OWN remote into `owner/repo` for the AF source clone (`quickdocker.sh:41-50`), proving the parsing pattern is trivial. But `REPO_PATH` is only threaded to `gh repo clone` and the container name — never forwarded to `af install --init`, so the slug is dropped before init runs.

### Why #5: Why hasn't a detection mechanism been wired in, given one already exists?
Because the existing git/gh detection idiom was built for a *different* concern (default branch) and never generalized. `internal/cmd/detect_default_branch.go` already provides:
- `runGitDetect(workDir, name, args...)` — a bounded, read-only, ADR-009-seam'd (`var`) exec wrapper returning trimmed stdout or `""` on error (`detect_default_branch.go:34-44`).
- `detectDefaultBranch(workDir)` — a layered chain calling `git symbolic-ref`, `git ls-remote`, and `gh repo view --json ... -q ...` (`detect_default_branch.go:52-73`).
The only missing piece is a sibling `detectRepoSlug` using the same seam (e.g. `gh repo view --json nameWithOwner -q .nameWithOwner`, or `git remote get-url origin` + parse). No such helper exists today (grep for `nameWithOwner` returns zero hits anywhere in the repo), so the wiring was simply never done. The root cause is a static literal that predates — and was never reconciled with — the detection idiom added for default-branch resolution.

## Evidence Gathered

| Finding | Source | Evidence |
|---------|--------|----------|
| dispatch.json is a static literal with empty `repos` | `internal/cmd/install.go:145` | ``"dispatch.json": `{"repos":[],"trigger_label":"agentic",...}` `` inside the `starterConfigs` map |
| Literal cannot know the repo (compile-time const in a map) | `internal/cmd/install.go:139-157` | starter configs are static strings written verbatim; only `factory.json` is computed (`config.DefaultFactoryConfigJSON()`, line 142) |
| init flow never queries git/gh for repo identity | `internal/cmd/install.go:97-297` | no `git remote`, `gh repo view`, or arg-read for repo; only git touch is `ensureGitExclude` (write-only, lines 270-271, 842-878) |
| init runs with cwd = cloned repo (has origin) | `quickstart.sh:417-428,441-445` + `install.go:98` | `cd "$repo_dir"` then `af install --init`; `getWd()` (`helpers.go:176-182`) returns that dir |
| target repo cloned with origin set | `quickdocker.sh:582` | `gh repo clone "$REPO_PATH"` populates `origin` in the cloned repo |
| quickdocker KNOWS the slug explicitly but drops it | `quickdocker.sh:355-362,398,582` | `REPO_PATH` normalized to `owner/repo`; passed only to clone/container-name, never to `af install --init` |
| quickdocker already parses owner/repo from a remote | `quickdocker.sh:41-50` | strips `https://github.com/`, `git@github.com:`, `.git` from `git remote get-url origin` for the AF source repo — the exact parse pattern needed |
| Existing read-only git/gh detection seam | `internal/cmd/detect_default_branch.go:34-73` | `runGitDetect` (ADR-009 `var` seam) + `detectDefaultBranch` chain using `git symbolic-ref`, `git ls-remote`, `gh repo view --json` |
| No org/repo-slug helper exists anywhere | `grep nameWithOwner` (repo-wide) | zero matches in `.go`/`.sh`; the parse helper must be added |
| `repos` is consumed as `owner/repo` (`org/repository`) form | `internal/cmd/dispatch.go:172,300,537-539` | dispatch iterates `dispatchCfg.Repos`, passes each as `gh --repo <repo>`; `ghLinkedPRs` does `strings.Cut(repo, "/")` and errors if not `owner/name` |
| empty `repos:[]` is actually INVALID for the dispatcher | `internal/config/dispatch.go:142-144` | `validateDispatchConfig` returns an error: "dispatch config must have at least one repo" — so the shipped default cannot even pass its own validator |

## Tests Performed

| Test | Command | Result |
|------|---------|--------|
| Confirm dispatch.json literal and empty repos | `grep -n 'dispatch.json' internal/cmd/install.go` | Single hit at line 145 with `"repos":[]` |
| Confirm no nameWithOwner / repo-slug helper exists | `grep -rni "nameWithOwner" . --include="*.go" --include="*.sh"` | Zero matches repo-wide |
| Confirm init flow git interactions | `grep -niE "git remote\|get-url\|gh repo view" internal/cmd/install.go` | No matches (no repo detection in install.go) |
| Confirm cwd at init = cloned repo with origin | Read `quickstart.sh:414-445`, `quickdocker.sh:573-584`, `helpers.go:176-182` | `cd "$repo_dir"` -> `af install --init`; repo cloned via `gh repo clone`; `getWd`=`os.Getwd()` |
| Confirm quickdocker has the slug but drops it | `grep -niE "REPO_PATH\|install --init" quickdocker.sh` | `REPO_PATH` used for clone+container name only; no `af install --init` call in quickdocker (it delegates to quickstart, passing nothing repo-specific) |
| Confirm `repos` consumed as owner/repo | Read `internal/cmd/dispatch.go:172,537-539` | `strings.Cut(repo, "/")`; errors if not `owner/name` form |
| Confirm empty repos fails dispatch validation | Read `internal/config/dispatch.go:142-144` | `validateDispatchConfig` errors when `len(cfg.Repos)==0` |
| Confirm existing detection idiom to mirror | Read `internal/cmd/detect_default_branch.go:34-73` | `runGitDetect` seam + `detectDefaultBranch` layered chain |

## Conclusion

**VALIDATED.** The org/repository is never discovered or injected at setup. The mechanism gap is concrete and proven: `dispatch.json` is born from a static compile-time literal (`internal/cmd/install.go:145`) that ships `repos:[]`, and nothing in `runInstallInit` (`install.go:97-297`) ever interrogates the environment for the repo slug — despite the slug being trivially available (the init cwd is the cloned target repo with `origin` set, per `quickstart.sh:428`/`:442` and `getWd()`/`os.Getwd()`). The empty default is not merely unhelpful: `validateDispatchConfig` (`internal/config/dispatch.go:142-144`) actively rejects a zero-length `repos`, so the shipped literal cannot pass the dispatcher's own validator — the dispatcher is dead until an operator manually edits the file.

**Recommended mechanism — detect the git remote at init time (do NOT rely on quickdocker/quickstart to pass it in).** Mirror the existing, already-blessed read-only detection idiom in `internal/cmd/detect_default_branch.go`: add a sibling `var detectRepoSlug = func(workDir string) string` built on the existing `runGitDetect` ADR-009 seam, using a layered chain:
1. `gh repo view --json nameWithOwner -q .nameWithOwner` (canonical `org/repo`, network/GitHub), then
2. `git remote get-url origin` parsed by stripping `https://github.com/` / `git@github.com:` / `.git` (the exact, already-proven parse from `quickdocker.sh:41-50`), as the offline fallback.
Validate the result against an `owner/repo` shape before accepting it (so a non-GitHub or missing remote yields `""` and the install gracefully falls back to today's empty `repos:[]` rather than baking garbage).

**Recommended injection point — `runInstallInit` in `internal/cmd/install.go`.** Stop emitting `dispatch.json` from the static `starterConfigs` literal; instead build its content programmatically right before the write loop (`install.go:139-157`), substituting the detected slug into `repos`. This keeps the change in ONE place, reuses the existing detection seam (so it is unit-testable with canned command output exactly like `detectDefaultBranch`'s tests), preserves the write-if-absent idempotency, and degrades safely to the current behavior when no GitHub remote is present. Threading the slug down through `quickdocker.sh -> quickstart.sh -> af install --init` as a new flag is feasible but inferior: it touches three files, leaves the bare `af install --init` (and `agent-gen-all`) paths uncovered, and duplicates parsing logic the binary can do itself from a cwd it already resolves.

## VERDICT: VALIDATED
The actual `org/repository` is never discovered/injected at setup because `dispatch.json` comes from a static literal at `internal/cmd/install.go:145` with no env interrogation; recommended fix is to add a `detectRepoSlug` helper on the existing `runGitDetect` seam (`gh repo view --json nameWithOwner` with a `git remote get-url origin` parse fallback) and inject the detected slug into `repos` by building `dispatch.json` programmatically inside `runInstallInit` (`internal/cmd/install.go`).
