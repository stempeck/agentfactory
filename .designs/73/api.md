# D1 — API & Interface Design (api.md)

Owner of (verification.md): C-1 (with Integration, Data), C-3 (with Integration, Security).
This dimension governs: the Go function surface for the baked-in default, the
repo-discovery interface, and any CLI surface/error messages introduced.

The design adds NO new user-facing CLI subcommand or flag. `af install --init`
already exists (`internal/cmd/install.go:88-90`, verified) and is the sole entry
point; the work is internal-API + behavior. This is itself an API decision (see
Option A1.3) and is constraint-driven by C-2 (bootstrap at first-create, not a new
command).

---

## A1. Default-config interface (how the baked-in default is expressed in Go)

### Option A1.1 — `DefaultDispatchConfigJSON(repo string) string` in `internal/config` — RECOMMENDED

A package-level function in `internal/config` (mirroring `DefaultFactoryConfigJSON()`
at `internal/config/config.go:111`, verified) that builds a `DispatchConfig` value
from in-code constants, sets `Repos` to `[]string{repo}` (or `[]string{}` if repo
is ""), `json.Marshal`s it, and returns the string. `runInstallInit` calls it for
the `dispatch.json` starter entry (replacing the inline literal at
`internal/cmd/install.go:145`, verified).

Signature:
```go
func DefaultDispatchConfigJSON(repo string) string
```

- Trade-offs: Exactly mirrors the established single-source pattern
  (`DefaultFactoryConfigJSON`, config.go:111). The default is built from the
  `DispatchConfig` struct (dispatch.go:18-27, verified), so it cannot drift from
  the schema — field renames break compilation. Takes the discovered repo as a
  parameter, keeping discovery (an install-time, git-touching concern) OUT of the
  config package (which has no git dependency today).
- Reversibility: Easy (delete the func, restore the literal).
- Constraints: satisfies C-1 (baked into the tool as Go code), supports C-3 (repo
  is a parameter populated by the caller), aligns with ADR-008 (a Go func is
  inherently single-source — no embed-vs-source drift; codebase-snapshot Decision
  History). Recommended.

### Option A1.2 — Inline literal string in `runInstallInit` (status-quo shape extended) — REJECTED

Keep the current inline-literal approach (`internal/cmd/install.go:145`, verified)
but expand it to the non-empty default with the repo string interpolated via
`fmt.Sprintf`.

- REJECTED: reverses [Decision History: issue #371 Gap-6 single-source pattern].
  codebase-snapshot.md §6 records that `dispatch.json` is "the ONLY starter config
  still an inline literal; `factory.json` uses `DefaultFactoryConfigJSON()`
  (install.go:142) — established single-source pattern (issue #371 Gap-6)." A
  hand-typed literal with a non-trivial mappings/workflows body is precisely the
  drift risk that pattern exists to remove (a `fmt.Sprintf` body cannot be schema-
  checked by the compiler). Extending the literal entrenches the one exception the
  prior change set out to eliminate.

### Option A1.3 — New `af dispatch init` / `af config dispatch default` subcommand — REJECTED

Add a dedicated CLI command the operator runs after install to populate the default.

- REJECTED: fails [AC-2], violates [C-2]. C-2 (verification.md) requires the
  default to be written "when dispatch.json is FIRST created during init — not
  lazily, not later." A separate command is a later, manual step; it reintroduces
  the human interaction AC-2 ("without ever needing to visit the manager") and C-5
  ("without human interaction as an ongoing dependency") forbid. Also adds CLI
  surface for no behavioral gain over A1.1.

---

## A2. Repo-discovery interface (how the actual org/repository reaches the default)

C-3 (verification.md) requires `repos` populated with the ACTUAL org/repository
"discovered during install, not a literal placeholder." codebase-snapshot.md §6
confirms `runInstallInit` "takes NO repo argument and does NO git-remote lookup."
So this is net-new code. ADR-014 (Decision History) forbids interactive prompting
in `cmd/`/`internal/` Go paths: discovery MUST be non-interactive (git remote) or
fail-loud-with-flag — never a prompt.

The dispatcher consumes `repos[]` as `owner/name` strings: it passes each entry
directly to `gh --repo <repo>` (`internal/cmd/dispatch.go:300`, verified) and
splits it via `strings.Cut(repo, "/")` (dispatch.go:537, verified). So discovery
MUST yield the `owner/name` form, matching the proposed JSON `"org/repository"`.

### Option A2.1 — `gh repo view --json nameWithOwner` at init, best-effort — RECOMMENDED

In `runInstallInit`, before building the dispatch default, shell out to
`gh repo view --json nameWithOwner -q .nameWithOwner` in the install CWD. `gh` is a
verified hard prerequisite (quickstart.sh `check_gh`, lines 128-138, verified, and
the dispatcher already depends on `gh` for every query). `gh repo view` resolves
the `owner/repo` from the local git remote AND the GitHub API, returning canonical
`nameWithOwner` directly in the dispatcher's required form.

- Trade-offs: Returns the canonical `owner/name` with no SSH/HTTPS URL parsing.
  Requires `gh auth` at install time; the dispatcher already requires `gh auth`
  (codebase-snapshot §4: "check gh auth"), so this adds no NEW operational
  dependency for the autonomous path. On failure (no remote / not authed) fall
  back to A2.3 (empty repos) rather than aborting install.
- Reversibility: Easy (discovery is one call site in runInstallInit).
- Constraints: satisfies C-3, complies with ADR-014 (non-interactive; no prompt),
  ADR-017 (read-only git/gh access — no writes outside af dirs). Recommended.

### Option A2.2 — Parse `git remote get-url origin` and normalize to `owner/name` — RECOMMENDED (fallback / no-gh-auth)

Run `git remote get-url origin`, then normalize the result
(`git@github.com:org/repo.git`, `https://github.com/org/repo.git`,
`https://github.com/org/repo`) to `owner/name` by stripping scheme/host/`.git`.
quickstart.sh `cd`s into the repo before `af install --init` (quickstart.sh:428,
442, verified), so a git remote IS present in CWD at that moment
(codebase-snapshot §6 confirms this).

- Trade-offs: Works WITHOUT `gh auth` (pure git), so it covers the window before
  `claude`/`gh` auth in the setup flow (source.md setup steps 1-3 precede manager
  start). Cost: URL-normalization is hand-rolled string handling — a real but
  bounded surface (3 URL shapes). Best used as the FALLBACK when A2.1's `gh` call
  fails, or as the PRIMARY if we prefer zero gh-auth coupling at init.
- Reversibility: Easy.
- Constraints: satisfies C-3, complies with ADR-014 (non-interactive), ADR-017
  (read-only). Recommended as the layered fallback to A2.1.

### Option A2.3 — Leave repos empty; require operator to fill it later — REJECTED

Ship the non-empty mappings/workflows default but keep `repos: []`, expecting the
operator to add the repo by hand or via `af config dispatch set`.

- REJECTED: fails [AC-3], violates [C-3]. C-3 explicitly forbids leaving `repos`
  as a placeholder; AC-3 requires the actual repo-name in `repos` at first create.
  Additionally, `validateDispatchConfig` REQUIRES `len(cfg.Repos) > 0`
  (`internal/config/dispatch.go:142-144`, verified): a default with empty repos but
  non-empty mappings would FAIL to load via `LoadDispatchConfig` (dispatch.go:48-65,
  verified), breaking the dispatcher on a fresh repo. (Retained ONLY as the
  graceful-degradation target when discovery genuinely fails — see A2.1 fallback —
  in which case mappings must ALSO be empty to keep the file loadable; that
  degraded shape is the status quo, acceptable only as a non-default failure path.)

---

## A3. Error-message / observability surface at discovery failure

### Option A3.1 — Warn-don't-abort: discovery failure prints a warning, writes degraded-but-loadable default — RECOMMENDED

If both A2.1 and A2.2 fail (no remote, no auth), `runInstallInit` prints a
structured warning to stderr naming the manual remedy
(`af config dispatch set`, verified at config_set.go:24-32) and writes the
status-quo loadable default (`repos:[]`, `mappings:[]`) so install still succeeds.

- Trade-offs: install never hard-fails on a missing remote (matches install's
  current robustness posture and quickstart's "warn-don't-abort" idiom, e.g.
  webui launch guard quickstart.sh:690-697, verified). The autonomous path is
  simply unavailable until the operator names the repo — an honest degradation.
- Reversibility: Easy.
- Constraints: complies with ADR-014 (a structured stderr message naming the flag
  is exactly the ADR-014 "fail loud with a structured error naming the exact flag"
  shape — here non-fatal because install has more to do). Recommended.

### Option A3.2 — Hard-fail install when repo cannot be discovered — REJECTED

Abort `af install --init` with a non-zero exit if no git remote is found.

- REJECTED: violates [C-5] (would block the no-human-interaction bootstrap when a
  repo has no remote yet, e.g. a brand-new local repo), and regresses the existing
  robustness of `af install --init`, which today writes a loadable default
  unconditionally (install.go:145, verified). A1/A2 already cover the happy path;
  hardening install into a hard-fail on a missing remote trades a recoverable
  degradation for a setup-blocking error.

---

## Reversibility (this dimension): Easy

All changes are additive and localized to `runInstallInit` plus one new
`internal/config` function. No schema change, no migration, no CLI surface change.

## Dependencies produced

- PROVIDES to **Data (D2)**: the `DefaultDispatchConfigJSON(repo)` function shape —
  Data owns WHAT mappings/workflows the default contains; API owns the function
  signature and the repo-injection point.
- PROVIDES to **Integration (D6)**: the single call site in `runInstallInit`
  (install.go starterConfigs map, :139-148, verified) and the discovery call
  ordering (discover repo → build default → write).
- REQUIRES from **Security (D5)**: validation of the discovered repo string
  (owner/name shape; reject shell-meta) before it is written into the config.
- REQUIRES from **Data (D2)**: the exact field set the default must populate so the
  marshaled output passes `validateDispatchConfig` (dispatch.go:141-185).

## Risks identified

| Risk | Severity | Mitigation |
|------|----------|------------|
| `gh repo view` requires `gh auth`, which may not be present at the install moment (auth happens at setup step 2, source.md) | Medium | Layer A2.2 (`git remote`, no auth needed) as the primary or fallback; A3.1 degrades gracefully |
| Discovered repo string contains an unexpected URL form, yielding a malformed `owner/name` | Medium | Security (D5) validates owner/name shape; on validation failure fall to A3.1 degraded default |
| Adding a `gh`/`git` shell-out to `runInstallInit` introduces a new failure mode in a previously git-agnostic function | Low | Best-effort + warn-don't-abort (A3.1); install proceeds regardless |
