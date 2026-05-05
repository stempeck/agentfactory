# session + tmux subsystem

**Covers:** internal/session, internal/tmux

## Shape

`internal/session/` owns agent-session lifecycle: creating a tmux session, launching Claude inside it, exporting the trusted-identity environment, and tearing it down (`internal/session/session.go:23-32`, `:83-152`, `:181-205`). `internal/tmux/` is a thin subprocess wrapper — every method is a one-shot `exec.Command("tmux", …)` with classified error handling (`internal/tmux/tmux.go:37-80`). They are documented as one subsystem because `session.Manager` holds `*tmux.Tmux` as its only external dependency (`internal/session/session.go:28`, `:40`) and every lifecycle operation (new-session, set-environment, send-keys, kill-session, has-session) is a passthrough. Neither package is imported by the other's dependencies — tmux has no session awareness, session has no shell knowledge beyond `shellQuote`. The tmux package was "ported from internal/tmux/tmux.go with agentfactory simplifications" during the monorepo-to-standalone split (`internal/tmux/tmux.go:2`); both packages were first added to the tree by commit `32af131` (2026-03-23), the "Phase 7 `af up`" change that introduced per-agent tmux sessions so agents could mail each other.

## session.Manager surface

- `NewManager(factoryRoot, agentName, entry)` — constructs a Manager holding factory root, agent name, config entry, and a fresh tmux wrapper (`internal/session/session.go:35-42`).
- `SetInitialPrompt(prompt)` — stashes a task string that becomes claude's first user message via CLI argument; setting it also suppresses the startup nudge (`internal/session/session.go:46-48`, `:167-169`, `:221-223`). Added in `f5f909b` for `af sling --agent` specialist dispatch.
- `SetWorktree(path, id)` — configures worktree-based workDir and `AF_WORKTREE{,_ID}` exports; rejects empty path with a plain error (`internal/session/session.go:54-61`).
- `SessionID()` → `SessionName(agentName)` = `"af-" + agent` (`internal/session/session.go:64-66`, `internal/session/names.go:7-12`).
- `WorkDir()` / `workDir()` — returns worktree agent dir if `SetWorktree` was called, otherwise factory agent dir (`internal/session/session.go:70-80`).
- `Start()` — precondition-guarded lifecycle: worktree-set check → zombie detection → workspace stat → `tmux new-session` → `set-environment` of four (or six) vars → shell-ready poll → send startup command → wait-for-claude → bypass-permissions dismissal → optional nudge (`internal/session/session.go:83-152`).
- `Stop()` — has-session check, Ctrl-C, sleep 100ms, kill-session, best-effort lock release (`internal/session/session.go:181-205`).
- `IsRunning()` — single `HasSession` call (`internal/session/session.go:208-210`).
- `BuildStartupCommand()` / `BuildNudge()` — test-only exports of unexported builders (`internal/session/session.go:213-215`, `:232-234`).

## Environment-variable contract

All four env vars are written twice by `Start()`: once via `tmux set-environment` (`internal/session/session.go:115-118`) so they're present in the session's default shell environment, then again as inline `export …` prefixes in the claude launch command (`internal/session/session.go:159-160`) so they survive even if the shell doesn't inherit session env for any reason. Both writers use `shellQuote` for the inline form (`internal/session/session.go:160`, `:176-178`, hardening added in `99fb45a` item #4). The six-var form applies when a worktree is set (`internal/session/session.go:119-122`, `:161-164`).

| Var | Producer | Consumers | Why |
|-----|----------|-----------|-----|
| `AF_ROOT` | `session.go:115`, `:159` | `hooks/quality-gate.sh:9`, `hooks/fidelity-gate.sh:14` via `${AF_ROOT:-$(af root)}` (enforced by `internal/cmd/hook_envvar_test.go:43-52`); agent `settings.json` Stop hooks (`internal/claude/settings_test.go:79-84`, `:131-136`) | Factory root path must survive worktree cwd changes — hooks and CLI fallbacks need the original repo root, not the worktree path. |
| `AF_ROLE` | `session.go:116`, `:159` | `hooks/quality-gate.sh:9`, `hooks/fidelity-gate.sh:14` via `${AF_ROLE:-$(basename "$(pwd)")}`; `internal/cmd/helpers.go:91-93` (`resolveAgentName` fallback); `internal/cmd/done.go:202` (worktree cleanup); all `detect*Agent` / `detectRole` / `detectSender` / `detectAgentName` / `detectCreatingAgent` paths via the shared `resolveAgentName` (tests: `helpers_test.go`, `prime_test.go`, `mail_test.go`, `sling_test.go`, `bead_test.go`). | Trust anchor. `AF_ROLE` is the fallback identity when cwd-based detection misses (e.g., worktree paths that don't match the factory layout). The `helpers.go:37-49` comment states the invariant: AF_ROLE is set by `session.Manager` "from a trusted source." |
| `BD_ACTOR` | `session.go:117`, `:159` | `internal/cmd/bead.go:109,155,244,270,343,371`, `internal/cmd/done.go:74`, `internal/cmd/handoff.go:135,178`, `internal/cmd/mail.go:418`, `internal/cmd/prime.go:365,527`, `internal/cmd/sling.go:336`, `internal/cmd/step.go:111`. Read at the cmd layer only, passed into `newIssueStore(ctx, root, actor)` as explicit constructor arg per `.designs/80/security.md:63-64`. | Actor identity for issuestore writes (creator, assignee, closer). Survives the bdstore → mcpstore swap (commit `7acd617`); `mcp_server_problem.md:71` explicitly called out `BD_ACTOR` as part of the stable agent contract. |
| `BEADS_DIR` | `session.go:118`, `:159` | Passed into mcpstore via the `newIssueStore` seam; see `.designs/worktree-isolation/constraint-verification.md:5-6` (every session must point `BEADS_DIR` at the ORIGINAL factory-root `.beads/`, never a worktree). | One issuestore per factory, shared across worktrees. Worktree agents must still write to the same DB. |
| `AF_WORKTREE` | `session.go:120`, `:162` (only when worktree set) | `internal/worktree/worktree.go:321-323` documents this as "set by a trusted source (session.Manager at a prior start)" and the value is trusted for inheritance decisions. | Lets newly-slung agents inherit their creator's worktree without re-running `FindByOwner`. |
| `AF_WORKTREE_ID` | `session.go:121`, `:163` | Same as `AF_WORKTREE` — paired. | Stable ID for the worktree (distinct from the filesystem path). |

Load-bearing rule (documented inline at `internal/cmd/helpers.go:41-49`, `:91-93`): library layers do NOT read env directly. Commit `d020a5e` (Phase 1 of #98) removed the last library-layer `os.Getenv` calls — including `tmux.IsInsideTmux` which now takes `tmuxEnv string` as an explicit param (`internal/tmux/tmux.go:33-35`, called from `internal/cmd/handoff.go:55`). Env reads happen only at the cobra-command boundary under `internal/cmd/`.

## tmux wrapper surface

All methods are receivers on `*Tmux` unless noted. Every call funnels through `run(args ...)` which does `exec.Command("tmux", args...)` and classifies stderr into `ErrNoServer` / `ErrSessionExists` / `ErrSessionNotFound` (`internal/tmux/tmux.go:46-80`).

- `IsInsideTmux(tmuxEnv string) bool` — pure, package-level (`internal/tmux/tmux.go:33-35`).
- `NewTmux() *Tmux` (`internal/tmux/tmux.go:41-43`).
- `IsAvailable() bool` — `tmux -V` probe (`internal/tmux/tmux.go:83-86`).
- `NewSession(name, workDir)` — `tmux new-session -d -s NAME [-c DIR]` (`internal/tmux/tmux.go:89-96`).
- `HasSession(name)` — `has-session -t =NAME`; the `=` prefix forces exact match, preventing prefix collisions (`internal/tmux/tmux.go:100-109`).
- `KillSession(name)` (`internal/tmux/tmux.go:112-115`).
- `ListSessions()` — returns nil on no-server (`internal/tmux/tmux.go:118-132`).
- `AttachSession(name)` — wires stdin/stdout/stderr directly; replaces the terminal (`internal/tmux/tmux.go:136-142`).
- `SwitchClient(name)` — use when already inside tmux (`internal/tmux/tmux.go:146-149`).
- `SendKeys` / `SendKeysDebounced` / `SendKeysRaw` / `SendKeysDelayed` — the debounced variants do literal-mode paste, wait, then send Enter separately (`internal/tmux/tmux.go:153-179`).
- `NudgeSession(session, message)` — paste + 500ms debounce + Enter with 3x retry, the reliable path for Claude input (`internal/tmux/tmux.go:183-202`).
- `SendNotificationBanner(session, from, subject)` — printable mail banner injected into the pane (`internal/tmux/tmux.go:205-215`).
- `GetPaneCommand(session)` — `list-panes -F #{pane_current_command}` (`internal/tmux/tmux.go:218-224`).
- `IsAgentRunning(session, expected...)` — if expected given, exact match; else any non-shell command counts (`internal/tmux/tmux.go:229-250`).
- `IsClaudeRunning(session)` — matches `node`, `claude`, or regex `^\d+\.\d+\.\d+` version-style pane commands (`internal/tmux/tmux.go:254-264`).
- `CapturePane(session, lines)` — `capture-pane -p -t SESSION -S -N` (`internal/tmux/tmux.go:267-269`).
- `ClearHistory(pane)` (`internal/tmux/tmux.go:272-275`).
- `RespawnPane(pane, command)` (`internal/tmux/tmux.go:278-281`).
- `SetEnvironment(session, key, value)` — `tmux set-environment -t SESSION KEY VALUE` (`internal/tmux/tmux.go:284-287`).
- `WaitForShellReady(session, timeout)` — polls `GetPaneCommand` until it matches one of `supportedShells` (`internal/tmux/tmux.go:290-306`).
- `WaitForCommand(session, exclude, timeout)` — inverse: polls until pane is NOT one of the excluded commands (`internal/tmux/tmux.go:309-330`).
- `AcceptBypassPermissionsWarning(session)` — captures 30 lines, scans for "Bypass Permissions mode", if present sends Down then Enter (`internal/tmux/tmux.go:333-356`).
- Package-level: `ClaudeStartTimeout()` = 60s, `SupportedShells()` = `{bash, zsh, sh, fish}` (`internal/tmux/tmux.go:30`, `:358-365`).

Constants (inlined, not in `internal/constants`): `debounceMs = 500`, `pollInterval = 100ms`, `claudeStartTimeout = 60s` (`internal/tmux/tmux.go:24-28`).

## Naming convention

- Prefix constant: `const Prefix = "af-"` (`internal/session/names.go:7`).
- `SessionName(agent) = Prefix + strings.TrimRight(strings.TrimSpace(agent), "/")` — trims surrounding whitespace and trailing slashes only (`internal/session/names.go:10-12`). Test cases pin: `"manager"` → `"af-manager"`, `""` → `"af-"`, `" agent/ "` → `"af-agent"` (`internal/session/names_test.go:10-17`).
- Collision behavior: exact-match lookup via tmux's `=` prefix in `HasSession` (`internal/tmux/tmux.go:100-109`) — two sessions named `af-foo` and `af-foobar` cannot be confused. `Start()` uses this for zombie detection; `af down`/`af attach` paths and `worktree.GC` also depend on it.

One exception: `internal/worktree/worktree.go:302` calls `tmux has-session -t meta.Owner` with the **bare agent name**, not `af-{name}` — pinned in the integration test comment `internal/worktree/worktree_integration_test.go:263`. This is an inconsistency; see Gaps.

## Zombie detection

Location: `internal/session/session.go:90-100`, called at the top of `Start()` before any creation work.

Logic:
1. `HasSession(sessionID)` — error ignored, treated as "not running".
2. If running, `IsClaudeRunning(sessionID)` — matches `node`, `claude`, or `^\d+\.\d+\.\d+` (`internal/tmux/tmux.go:254-264`).
3. If Claude is running → return `ErrAlreadyRunning`.
4. If tmux alive but Claude dead → `KillSession`, fall through to recreate. Failure to kill returns a wrapped error.

Detection only runs on `Start()` — not continuously. Flagged this as a limitation ("Zombie detection only on Start"). `worktree.GC` (`internal/worktree/worktree.go:302-312`) provides the inverse: it removes worktree meta files for agents whose tmux session is gone.

## Seams

| Seam | Direction | Contract | Anchor |
|------|-----------|----------|--------|
| `tmux` binary | OUT | Every op is `exec.Command("tmux", …)` | `internal/tmux/tmux.go:47` |
| `claude` binary | OUT | Launched inside tmux via `send-keys` with `--dangerously-skip-permissions` and optional positional prompt arg | `internal/session/session.go:166-169`, `:134` |
| `internal/config` | OUT (session→config) | `config.AgentEntry` in constructor, `config.AgentDir(root, name)` for workDir | `internal/session/session.go:11`, `:27`, `:72-74` |
| `internal/lock` | OUT (session→lock) | `lock.New(workDir).Release()` on Stop (best-effort) | `internal/session/session.go:12`, `:202` |
| `internal/tmux` | OUT (session→tmux) | Embedded `*tmux.Tmux` as only runtime dep | `internal/session/session.go:13`, `:28`, `:40` |
| `internal/worktree` | interaction (no import) | worktree calls `tmux has-session` with bare name for GC; session exports `AF_WORKTREE{,_ID}` read by `worktree.ResolveOrCreate` | `internal/worktree/worktree.go:302`, `:321-323` |
| `internal/cmd/up`, `internal/cmd/sling` | IN | Construct `Manager`, must call `SetWorktree` before `Start` | `internal/cmd/up.go:85`, `internal/cmd/sling.go:635` |
| Factory-root env contract | OUT | `AF_ROOT` / `AF_ROLE` / `BD_ACTOR` / `BEADS_DIR` written, read by hooks and `internal/cmd/*` | `internal/session/session.go:115-118`, `:159` |

## Formative commits

| SHA | Date | What |
|-----|------|------|
| `32af131` | 2026-03-23 | Created `internal/session/` and `internal/tmux/` (the "`af up` like `gt up`" phase) — established per-agent tmux sessions named for cross-agent mail. Only commit that adds these packages from scratch. |
| `312eb11` | 2026-03-25 | Added startup-directive nudge from `agents.json` — source of `buildNudge()` and `m.agentEntry.Directive` usage (`session.go:220-228`). |
| `edaecb2` | 2026-03-25 | All agents get `--dangerously-skip-permissions`, orphan cleanup catches all — locks in the claude launch flag at `session.go:166`. |
| `f5f909b` | 2026-03-29 | `af sling --agent` specialist dispatch — added `SetInitialPrompt`, the prompt-as-CLI-arg path, nudge suppression when prompt is set. |
| `99fb45a` | 2026-03-29 | Hardening: applied `shellQuote` to all four export values in `buildStartupCommand` (item #4 in the commit body). |
| `682a1d8` | 2026-04-11 | Worktree wiring — added `AF_WORKTREE` / `AF_WORKTREE_ID` env vars in the tmux session. |
| `b78e24f` | 2026-04-13 | R-ENF-1 mitigation: added `ErrWorktreeNotSet` runtime precondition at `Start()` instead of a type-level interlock (rejected because `session → worktree` would have inverted package layering). Commit message is the rationale. |
| `d020a5e` | 2026-04-15 | Removed `os.Getenv` from library layer — changed `tmux.IsInsideTmux()` to take `tmuxEnv string`. Library-layer env-read prohibition anchor. |
| `7acd617` | 2026-04-17 | Deleted `internal/issuestore/bdstore/`. session.go env exports were NOT touched — `BD_ACTOR` remained in the contract by design (see `mcp_server_problem.md:71`). |

## Load-bearing invariants

- **AF_ROLE is set ONLY by session.Manager from a trusted source.** Producer is `session.go:116` and `session.go:159`. The fallback in `resolveAgentName` (`internal/cmd/helpers.go:91-93`) treats it as the trust anchor when cwd-based detection misses. No user-facing override exists — MEMORY note `feedback_no_agent_overrides` explicitly says don't propose CLI/env overrides of context-derived identity. Tests at `helpers_test.go:17-143`, `mail_test.go:85-155`, `prime_test.go:99-111`, `sling_test.go:230-264`, `bead_test.go:121-138` all pin the AF_ROLE trust-fallback semantics (#88).
- **Start MUST be preceded by SetWorktree with non-empty path.** Runtime precondition at `session.go:86-88`; regression test at `internal/cmd/worktree_integration_test.go:659-722`. Rationale in commit `b78e24f`: type-level interlock was rejected to avoid `session → worktree` import inversion.
- **All env writes happen twice.** Once via `tmux set-environment` (session default env), once inline-exported on the claude command line (`session.go:115-122` and `:159-164`). Deliberate belt-and-suspenders — neither alone is sufficient.
- **Library code never reads env.** Enforced by commit `d020a5e` (#98). `tmux.IsInsideTmux` takes `tmuxEnv string`. `BD_ACTOR` / `AF_ROLE` / `AF_ROOT` are read only under `internal/cmd/*`.
- **Session naming: `af-` prefix, exact-match lookup.** Prefix at `names.go:7`; `=` prefix in `HasSession` at `tmux.go:101` prevents `af-foo`/`af-foobar` collisions.
- **Zombie detection runs at Start, not continuously.** `session.go:90-100`. Not a defect.
- **Factory-root `BEADS_DIR` regardless of worktree.** `beadsDir = filepath.Join(m.factoryRoot, ".beads")` — `factoryRoot`, not `worktreePath` (`session.go:114`, `:158`). One issuestore per factory.

## Cross-referenced idioms

- **Session-set env vars as "trusted context"** — `AF_ROLE`, `AF_ROOT`, `BEADS_DIR`, `AF_WORKTREE{,_ID}` are populated by `session.Manager` and consumed by the cmd layer for identity / root / worktree resolution. The `internal/cmd/helpers.go:42` comment states it outright: "AF_ROLE is set by session.Manager from a trusted source."
- **Shell-quoted exports** — `shellQuote()` wraps every export value (`session.go:176-178`) using the POSIX `'\''` escape idiom. Added defensively in `99fb45a` to handle paths with spaces.
- **Polling with pollInterval** — both `WaitForShellReady` and `WaitForCommand` use the same 100ms tick (`tmux.go:26`).
- **Best-effort on Stop** — `SendKeysRaw` Ctrl-C, lock release, even env exports on Start are all `_ =` ignored. Start errors only on the load-bearing operations (NewSession, WaitForShellReady, SendKeysDelayed).

## Formal constraint tags

- **C-env-trust** — `AF_ROLE` producer is session.go:116,159; consumer trust boundary at `internal/cmd/helpers.go:91-93` (commit `8cff1bf` resolves #88).
- **C-no-env-in-lib** — No `os.Getenv` under `internal/session/`, `internal/tmux/`, or any library-layer package. Anchor: commit `d020a5e` (#98).
- **C-worktree-precondition** — `session.go:86-88` + regression test `worktree_integration_test.go:659-722`. Commit `b78e24f` (R-ENF-1).
- **C-exact-session-match** — `tmux.go:101` uses `=` prefix for `has-session`.
- **C-factory-root-beads** — `session.go:114`, `:158` use `m.factoryRoot`, not `m.worktreePath`. Pinned by `.designs/worktree-isolation/constraint-verification.md:5-6`.
- **C-dangerous-skip-permissions** — `session.go:166` hardcoded; commit `edaecb2`.
- **H-session-prefix** — `names.go:7`, verified `names_test.go:30-34`.
- **R-env-quoting** — every export value runs through `shellQuote` (`session.go:159-164`); commit `99fb45a` item #4.

## Gaps

- **`BD_ACTOR` after bdstore removal** — Verified. `BD_ACTOR` is NOT vestigial. After commit `7acd617` deleted bdstore, `BD_ACTOR` is still read at 12 call sites under `internal/cmd/` (`bead.go:109,155,244,270,343,371`, `done.go:74`, `handoff.go:135,178`, `mail.go:418`, `prime.go:365,527`, `sling.go:336`, `step.go:111`) and passed into `newIssueStore(…, actor)` as the mcpstore client-side actor default. `.designs/80/security.md:63-64` and `mcp_server_problem.md:71` explicitly preserve it as part of the stable agent contract. No action.
- **`worktree.GC` uses bare agent name, not `af-{name}`** — `internal/worktree/worktree.go:302` calls `tmux has-session -t meta.Owner` where `meta.Owner` is the agent name. `session.Manager` creates sessions as `af-{agent}` (`names.go:7`). `worktree_integration_test.go:263` acknowledges this in a comment but pins the bare-name behavior in the test. Either `meta.Owner` stores the prefixed name in some code path I didn't find, or `GC` is looking for the wrong session name and never finding it, causing it to always-remove worktrees it shouldn't — unknown — needs review.
- **Zombie detection is one-shot** — Only runs on `Start()` (`session.go:90-100`). Flagged this. No continuous health monitor exists. Known limitation, not a defect.
- **Env exports are fire-and-forget** — `SetEnvironment` errors are `_ =` ignored (`session.go:115-122`). If tmux ever returned an error here, the session would silently launch without the expected env. Mitigated by the inline-export belt (`session.go:159`), but the belt fires before the inline exports actually happen — if both paths drop a var, nothing surfaces. Unknown — needs review whether this is worth tightening.
- **`IsAvailable()` is never called** — defined at `tmux.go:83-86` but no production or test caller grep-matches `IsAvailable`. Dead code or intended for future preflight — unknown — needs review.
- **`ClearHistory` and `RespawnPane` appear unused by session** — defined at `tmux.go:272-281`. Possibly used by mail/banner paths elsewhere; not required by session lifecycle — unknown whether they're called from other packages.
- **Hardcoded 5-second sleep in `AcceptBypassPermissionsWarning`** — `tmux.go:334`. Not parameterized; if Claude startup gets faster or slower, this wastes time or misses the dialog. Not a constraint, but a fragility.
