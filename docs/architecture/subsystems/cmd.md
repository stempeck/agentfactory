# cmd subsystem

**Covers:** internal/cmd

## Shape

`internal/cmd/` is the single cobra-based CLI layer behind the `af` binary; `Execute()` at `internal/cmd/root.go:28` is the only entry point invoked from `cmd/af`. Every subcommand is glued onto the top-level `rootCmd` (`internal/cmd/root.go:16-25`) through package `init()` functions (e.g. `internal/cmd/sling.go:60`, `internal/cmd/done.go:42`, `internal/cmd/mail.go:27`). All subcommands follow the same shape: resolve cwd via `getWd()` (`internal/cmd/helpers.go:26`), discover the factory root via `config.FindFactoryRoot` (e.g. `internal/cmd/sling.go:81`, `internal/cmd/mail.go:385`), then delegate to domain packages (`config`, `issuestore`, `mail`, `session`, `worktree`, `formula`, `templates`, `checkpoint`, `lock`, `tmux`, `claude`). The issue-store seam is indirected through a package-level `newIssueStore` var (`internal/cmd/helpers.go:17-24`) so tests can substitute `memstore`; `launchAgentSession` is similarly a package-level var (`internal/cmd/sling.go:616`) to decouple dispatch tests from `tmux + claude` being on PATH (rationale in commit `c3cf1f1`).

## Public surface

| Command | RunE anchor | One-line contract |
|---------|-------------|-------------------|
| `af root` | `internal/cmd/root_cmd.go:13` | Prints the factory root (walks cwd upward for `.agentfactory/`) |
| `af prime [--hook]` | `internal/cmd/prime.go:38` | Emits role context, formula step, mail injection; `--hook` parses `session_id` JSON on stdin (`internal/cmd/prime.go:64-68`) |
| `af install --init` / `af install <role>` | `internal/cmd/install.go:44` | Bootstraps factory or provisions an agent; `--init` hard-fails if Python != 3.12 (C-16, `internal/cmd/install.go:60-66`) |
| `af up [agents…]` | `internal/cmd/up.go:27` | Starts tmux sessions; calls `worktree.ResolveOrCreate` + `SetWorktree` before `mgr.Start()` (R-ENF-1 guard, commit `b78e24f`) |
| `af down [agents…] [--all]` | `internal/cmd/down.go:30` | Stops tmux sessions; `cleanupAgentWorktree` removes owned worktrees (R-INT-3, `internal/cmd/down.go:95`); `--all` pkills orphaned claude processes (`internal/cmd/down.go:120`) |
| `af attach <agent>` | `internal/cmd/attach.go:24` | `switch-client` inside tmux, `attach-session` otherwise (`internal/cmd/attach.go:61-64`) |
| `af done [--phase-complete --gate <id>]` | `internal/cmd/done.go:48` | Closes current ready step, advances or mails WORK_DONE + auto-terminates dispatched sessions (`internal/cmd/done.go:229-232`) |
| `af sling --formula <name>` / `af sling --agent <name> "task"` | `internal/cmd/sling.go:71` | Instantiates formula (parent epic + step tasks + DAG) and optionally launches; specialist-dispatch path in `dispatchToSpecialist` (`internal/cmd/sling.go:114`) |
| `af mail send|inbox|read|delete|check|reply` | `internal/cmd/mail.go:96,147,191,239,265,332` | Inter-agent messaging via `mail.Router`/`mail.Mailbox` over `issuestore.Store`; `check --inject` emits `<system-reminder>` XML for hooks (`internal/cmd/mail.go:303-315`) |
| `af bead show|create|update|list|close|dep` | `internal/cmd/bead.go:96,143,231,258,330,357` | Thin wrapper over `issuestore.Store`; `list --all` sets `IncludeAllAgents + IncludeClosed` (D13, `internal/cmd/bead.go:291-296`) |
| `af formula agent-gen <name> [--delete]` | `internal/cmd/formula.go:61` | Generates/removes a formula-derived agent: writes `.md.tmpl`, workspace `CLAUDE.md`, `.claude/settings.json`, and an `agents.json` entry |
| `af dispatch [--dry-run]` / `af dispatch start|stop|status` | `internal/cmd/dispatch.go:100,312,358,372` | GitHub-issue dispatcher: shells to `gh`, matches labels to agents, spawns `af sling` subprocesses; `start` runs a shell loop in tmux session `af-dispatch` (`internal/cmd/dispatch.go:310`) |
| `af handoff [-c] [-n] [-s ...] [-m ...]` | `internal/cmd/handoff.go:44` | Writes checkpoint, mails self, `tmux respawn-pane` with `af prime` as initial command (`internal/cmd/handoff.go:121`); must run inside tmux (`internal/cmd/handoff.go:55-61`) |
| `af step current` | `internal/cmd/step.go:92` | Emits single-line JSON with `state` ∈ {no_formula, blocked, all_complete, ready, error} for fidelity-gate Stop hook (jq-parsed; shape pinned by `TestStepCurrent_SchemaSnapshot`) |
| `af fidelity [on|off]` | `internal/cmd/fidelity.go:35` | Toggles `<factoryRoot>/.fidelity-gate` file |
| `af quality [on|off]` | `internal/cmd/quality.go:29` | Toggles `<factoryRoot>/.quality-gate` file |
| (internal) `outputCommandPreflight` | `internal/cmd/preflight.go:187` | Scans current step description for referenced CLI commands, warns when any are missing from PATH; used inline by `outputFormulaContext` (`internal/cmd/prime.go:439`) |

## Seams (what it touches)

| Seam | Direction | Contract | Anchor |
|------|-----------|----------|--------|
| `internal/config` | OUT | `FindFactoryRoot`, `FindLocalRoot`, `AgentsConfigPath`, `LoadAgentConfig`, `LoadDispatchConfig`, `AgentDir`, `ConfigDir`, `DetectAgentFromCwd`, `ValidateAgentName`, `AddAgentEntry`, `RemoveAgentEntry`, `SaveAgentConfig` — 18 files touch it (`Grep config\.(Load|Find|AgentsConfigPath)` across `internal/cmd/`) | `internal/cmd/helpers.go:55-66`; `internal/cmd/sling.go:222-223` |
| `internal/issuestore` + `mcpstore` | OUT | `issuestore.Store` obtained only through `newIssueStore(wd, beadsDir, actor)` seam; production returns `mcpstore.New(factoryRoot, actor)` — the Python MCP server is lazy-started on first call | `internal/cmd/helpers.go:17-24`; commit `c93f9ef` ("Phase 6: wire mcpstore into production seams"); `install.go:126` is the ONE documented bypass, required to print the bootstrap banner |
| `internal/mail` | OUT | `mail.NewMailbox(sender, store)`, `mail.NewRouter(wd, store)`, `mail.NewMessage`, `mail.NewReplyMessage`, `mail.ParsePriority` | `internal/cmd/mail.go:113-141`; `newMailboxForSender` / `storeForMail` at `internal/cmd/mail.go:412-428` |
| `internal/session` | OUT | `session.Manager` (`NewManager`, `Start`, `Stop`, `SetWorktree`, `SetInitialPrompt`, `BuildStartupCommand`); errors `ErrAlreadyRunning`, `ErrNotRunning`, `ErrNotProvisioned` | `internal/cmd/up.go:83-95`; `internal/cmd/sling.go:124-126,633-646`; `internal/cmd/handoff.go:106-108` |
| `internal/worktree` | OUT | `ResolveOrCreate`, `SetupAgent`, `FindByOwner`, `Remove`, `RemoveAgent` — `af up`/`af sling` call the resolve pair BEFORE `session.Start` (R-ENF-1, commit `b78e24f`) | `internal/cmd/up.go:69-76`; `internal/cmd/sling.go:158-177,271-283`; `internal/cmd/down.go:95-118` |
| `internal/lock` | OUT | `lock.New(workDir).Acquire(sessionID)`, `.Release()` — acquired in `primeAgent` (best-effort), released in `sendWorkDoneAndCleanup` | `internal/cmd/prime.go:273-278`; `internal/cmd/done.go:196` |
| `internal/checkpoint` | OUT | `Capture`, `Read`, `Write`, `Remove`, `IsStale`, `Age`, `WithFormula`, `WithHookedBead`, `WithNotes` | `internal/cmd/prime.go:465-502`; `internal/cmd/done.go:191`; `internal/cmd/handoff.go:126-148` |
| `internal/formula` | OUT | `FindFormulaFile`, `ParseFile`, `ResolveVars`, `MergeInputsToVars`, `ExpandTemplateVars`, `TopologicalSort`, `ResolveContext`, types `Formula/Step/Leg/Template/Aspect/Var/Input` | `internal/cmd/sling.go:303-400`; `internal/cmd/formula.go:84-92` |
| `internal/templates` | OUT | `templates.New().RenderRole(role, RoleData)`, `HasRole` | `internal/cmd/prime.go:131-157`; `internal/cmd/install.go:239-258`; `internal/cmd/formula.go:124` |
| `internal/claude` | OUT | `claude.EnsureSettings(dir, roleType)`, `claude.RoleTypeFor(role, agents)` | `internal/cmd/install.go:265-266`; `internal/cmd/formula.go:243` |
| `internal/tmux` | OUT | `tmux.NewTmux()`, `IsAvailable`, `HasSession`, `KillSession`, `NewSession`, `SendKeys`, `AttachSession`, `SwitchClient`, `ClearHistory`, `RespawnPane`, `IsInsideTmux` | `internal/cmd/attach.go:49-64`; `internal/cmd/handoff.go:55,118-122`; `internal/cmd/sling.go:151-155` |
| `internal/fsutil` | (none) | cmd does not import fsutil directly; atomic writes (e.g. dispatch state) are done inline via temp-file + rename (`internal/cmd/dispatch.go:283-298`) | — |
| Subprocess: `python3` | OUT-exec | `python3 --version` version gate in `checkPython312` | `internal/cmd/install.go:281` |
| Subprocess: `git` | OUT-exec | Current branch detection and working-tree dirtiness checks | `internal/cmd/prime.go:507`; `internal/cmd/formula.go:311` |
| Subprocess: `gh` | OUT-exec | `gh auth status`, `gh issue list --json` for dispatcher | `internal/cmd/dispatch.go:209,214` |
| Subprocess: `pgrep`/`pkill` | OUT-exec | Orphan-claude reaper under `af down --all` | `internal/cmd/down.go:126,132` |
| Subprocess: self `af` | OUT-exec | `af mail send` from `af done` and `af handoff`; `af mail check --inject` from `af prime`; `af sling` from `af dispatch`; `af dispatch` from `af dispatch start` loop. Guarded by `isTestBinary()` fork-bomb check (`internal/cmd/prime.go:307`, commit `392717f`) | `internal/cmd/done.go:262`; `internal/cmd/handoff.go:165`; `internal/cmd/prime.go:334`; `internal/cmd/dispatch.go:257,402` |

## Formative commits

| SHA | Date | Subject | Why it matters architecturally |
|-----|------|---------|--------------------------------|
| `c4712c5` | 2026-03-23 | Phase 1 Foundation — Go module, config, CLI, Makefile | Creates the cobra scaffold cmd subsystem still uses |
| `fd13e73` | 2026-03-23 | Phase 2 Mail System — messaging, routing, CLI commands | Establishes sender-detection + mailbox-per-agent seam that mail.go still follows |
| `f0c9f16` | 2026-03-23 | Phase 3 Prime System — role detection, templates, identity lock | First `detectRole`/`detectSender` fan-out — precursor to the idiom centralized in commit `800ab5c` |
| `1b0e2d3` | 2026-03-23 | Phase 5 Install & Settings — factory init, agent provisioning | Shape of `af install --init` vs `af install <role>` split |
| `32af131` | 2026-03-23 | Phase 7 `af up` creates tmux sessions named after agents | Establishes `af-<agent>` session-naming convention (`session.SessionName`) |
| `7153843` | 2026-03-25 | prime fan-out from factory root + graceful ErrNotProvisioned | `runPrimeAll` pattern + 5s timeout on `af mail check --inject` subprocess |
| `392717f` | 2026-03-27 | guard `os.Executable()` self-invocation against go test fork bomb (#6) | `isTestBinary()` is the guard that every self-exec call site still checks |
| `bafdbfb` | 2026-03-27 | Phase 7A `af sling --formula` — parse, resolve vars, topo-sort, create beads | Template that `dispatchToSpecialist` (2026-03-31) is a siblingized extension of |
| `b326859` | 2026-03-27 | Phase 7B `af prime --formula` formula-aware context | Original `outputFormulaContext`; `--formula` flag later hidden in commit `9278bfd` |
| `3489686` | 2026-03-27 | Phase 7C `af done` — step completion + WORK_DONE mail | Shape of runDoneCore still present |
| `7fe12eb` | 2026-03-27 | Phase 7D `af formula agent-gen` — generate agent .md from formula | Installs the "formula-derived agents" pattern `formula.go` still implements |
| `9278bfd` | 2026-03-31 | Remove `--formula` flag from `af prime` — context injection unconditional (#28) | Rationale: agents lost formula context on every compaction; inner guards already no-op when no formula is hooked |
| `34c45d1`/`2dd7937` | 2026-03-31 | Extract `instantiateFormulaWorkflow`, then: specialist dispatch instantiates formula + runtime artifacts like formula path | Reason given in commit: prior `SetInitialPrompt` approach "breaks `af done` and crash recovery… introduces a shell injection surface" |
| `9610f53` | 2026-04-01 | Replace hardcoded `manager` with `{{orchestrator}}` (issue #31 phase 3) | Dispatch-side caller identity threaded through formulas via `CallerIdentity` → `resolvedVars["orchestrator"]` (`sling.go:389-391`) |
| `92bb304` | 2026-04-01 | Add `--reset` flag to `af sling --agent` (#38) | Stop-session + cleanup-worktree + purge runtime files before re-dispatch (`sling.go:126-147`) |
| `1435495`/`1f038a0` | 2026-04-03 | `.runtime/dispatched` marker + auto-terminate | Distinguishes dispatched vs persistent sessions so `af done` can kill tmux only for the former (`done.go:297-310`) |
| `b7a0e9b`/`b19c3ac` | 2026-04-03 | `af bead` command group + enforcement test blocking `bd` in formula TOMLs | `af bead` is the only abstraction over `issuestore` that formulas may call; tests block regression to raw `bd` |
| `e41342d` | 2026-04-09 | Migrate command entry points off `bd` text-scraping onto `issuestore.Store` | Explicit commit message: "BD_ACTOR default scoping was misfiring WORK_DONE one step into every multi-step formula" — this is the origin of the `IncludeAllAgents: true` idiom in done/prime/step/mail |
| `bab1271` | 2026-04-09 | Phase 4 — migrate `af bead` and `af install` off `internal/bd`, delete `internal/bd/` | `install.go` is the "one consumer allowed to bypass the seam" |
| `c3cf1f1` | 2026-04-09 | Convert `launchAgentSession` into a package-level var; add `installNoopLaunchSession` | Dispatch tests previously hung for 4m when `bd` was present but `claude` was not; the seam is the fix |
| `39ee3b5`/`871e9f9`/`7101f3a` | 2026-04-09/10 | Fidelity gate (#65): `af step current`, `hooks/fidelity-gate.sh`, dual-hook smoke test | `step.go`'s JSON contract exists solely to feed the bash Stop hook |
| `800ab5c` | 2026-04-11 | Centralize agent identity into `resolveAgentName` (issue #78) | The three-tier idiom; before this, five functions duplicated the logic and `af mail`/`af done` broke inside worktrees |
| `682a1d8`/`059dc67`/`b78e24f` | 2026-04-11 | Worktree isolation lifecycle — sling creates, down/done clean up, R-ENF-1 guard | Establishes AF_WORKTREE/AF_WORKTREE_ID env handoff and the ResolveOrCreate+SetWorktree precondition for `Manager.Start` |
| `8cff1bf` | 2026-04-12 | Issue #88 fix — membership gate in `resolveAgentName` | Path-derived name must exist in `agents.json`; otherwise fall through to AF_ROLE |
| `eff2387` | 2026-04-12 | Issue #89 fix — unloadable `agents.json` treated as membership miss | Closes silent-skip when `agents.json` is missing/malformed |
| `4123f8d`/`f959234` | 2026-04-14/16 | Issue #98: env-var library isolation (PR #104, Phase 3) | Actor scoping is a `Store.actor` field via `NewWithActor`; no os.Setenv from tests |
| `c93f9ef` | 2026-04-16 | Phase 6 — wire `mcpstore` into production seams | `newIssueStore` now takes `actor` as third arg; 13 call sites updated; `checkPython312` gate added |
| `7acd617` | 2026-04-17 | Phase 7 — delete `internal/issuestore/bdstore/` | Strips last `BD_ACTOR` fallbacks; `mcpstore` is sole production adapter |

## Load-bearing invariants (this subsystem's contribution)

- **`af install --init` aborts before any filesystem mutation if Python 3.12 is missing (C-16).** Enforced by `checkPython312` at `internal/cmd/install.go:60-66` BEFORE `os.MkdirAll(configDir, …)`. Rationale: a mid-run failure would leave partial state that a subsequent re-run cannot detect or roll back (code comment at `install.go:60-63`).
- **A single issue-store constructor seam.** Every cmd-layer caller uses `newIssueStore(wd, beadsDir, actor)` from `internal/cmd/helpers.go:17-24`. The only sanctioned bypass is `install.go:126` (`mcpstore.New(cwd, "")` for the bootstrap banner). Rationale in `install.go:118-124` and commit `bab1271`.
- **The three-tier agent identity resolution is the single source of truth.** `resolveAgentName` (`internal/cmd/helpers.go:55`) is the only authority for agent-from-cwd; `detectSender`, `detectAgentName`, `detectCreatingAgent`, `detectRole` all delegate (see Cross-referenced idioms below). Membership against `agents.json` is required to authorize a path-derived name; AF_ROLE fallback is consulted on BOTH path-detection error AND membership miss (`helpers.go:90-97`). Established by commit `800ab5c`; hardened by commits `8cff1bf` (#88) and `eff2387` (#89).
- **`IncludeAllAgents: true` is the explicit escape hatch from per-actor default scoping.** Sling-created step beads have no Assignee, so an actor-scoped `List` returns zero. Four production call sites MUST pass this flag; their absence misfires WORK_DONE one step into every multi-step formula (commit `e41342d`). See Cross-referenced idioms.
- **H-4 / D15 atomic-write ordering for dispatch caller identity.** `persistFormulaCaller` is called from `dispatchToSpecialist` (`sling.go:187`) BEFORE `instantiateFormulaWorkflow` creates beads. If the process crashes between caller-write and bead-create, the next dispatch proceeds without a stale bead; `af done` will never observe a formula bead without a corresponding caller file. Pinned by `done_test.go::TestDone_NoCallerFile_NoMail`. Documented in `sling.go:577-590`.
- **D1: no caller → no WORK_DONE mail (no fallback).** `sendWorkDoneAndCleanup` skips the mail send when `readFormulaCaller` returns empty; there is no default recipient. `done.go:166-170`.
- **R-ENF-1: `session.Manager.Start()` requires a preceding `worktree.ResolveOrCreate` + `SetWorktree` call.** Commit `b78e24f` adopted a runtime precondition rather than a type-level interlock to avoid inverting `session → worktree` package layering. The precondition lives in `session.Manager.Start` (not in cmd), but every cmd entry point (`up.go:69-88`, `sling.go:158-177,271-283`) conforms.
- **Fork-bomb guard for self-exec.** `isTestBinary()` (`prime.go:307`) short-circuits every self-spawned `af` subprocess under `go test`. Guard sites: `done.go:249`, `handoff.go:153`, `prime.go:317`. Established by commit `392717f` (issue #6).
- **`.runtime/dispatched` marker distinguishes dispatched from persistent sessions.** Written by `writeDispatchedMarker` (`sling.go:667`); read before cleanup by `isDispatchedSession` (`done.go:297`). Auto-terminate fires only if dispatched AND mail-send succeeded (`shouldAutoTerminate`, `done.go:306-311`; commit `5331934` extracted the mail-failure suppression branch).

## Cross-referenced idioms

### `IncludeAllAgents` opt-out idiom

Every site uses the filter to defeat per-actor default scoping inherited from the historical `bd` CLI. The axis each site leverages:

- `internal/cmd/done.go:100` — "open children of the formula instance" when no ready step was returned; axis = `Parent + Statuses=[Open]`. Checks whether to misfire WORK_DONE. Pinned by `TestDone_MultiStepFormula_ProgressesCorrectly`.
- `internal/cmd/done.go:136` — "open children of the formula instance" after closing current step; same axis as above. Decides "more steps remain vs all complete".
- `internal/cmd/done.go:369` (`countAllChildren`) — "all children, open + closed" for WORK_DONE step-count; axis = `Parent + IncludeClosed`. Without the flag the mail body would read "All 0 steps complete".
- `internal/cmd/step.go:139` — Empty-ready branch: decides `blocked` (some children open) vs `all_complete` (none). Code comment explicitly notes that `memstore`'s R-INT-9 empty-assignee carve-out MASKS the regression, so the test suite cannot catch a dropped flag — pin is the `TestStepCurrent_IncludeAllAgentsRequired` test at `step_test.go:398,478`.
- `internal/cmd/prime.go:422` — Computes `openCount` so the displayed "Step N of total" line does not underflow to `totalSteps+1`.
- `internal/cmd/bead.go:294` — `af bead list --all` user-facing toggle: axis = both `IncludeAllAgents` and `IncludeClosed` together (D13 splits bd's overloaded `--all` into the two axes).
- `internal/cmd/integration_test.go:286`, `done_integration_test.go:109` — tests exercising the same axes against real mcpstore.

The sibling idiom in `internal/mail/mailbox.go:47` is referenced from `done.go:95` and `step.go:135` as precedent. Absence of the flag here was the "default actor-scoping misfiring WORK_DONE" bug called out in commit `e41342d`.

### `resolveAgentName` three-tier

Three-tier resolution: `FindLocalRoot`+`DetectAgentFromCwd` (worktree) → `DetectAgentFromCwd` against factoryRoot → `AF_ROLE` env var. Contract lives in `internal/cmd/helpers.go:55-99`. Call sites:

- `internal/cmd/mail.go:390` via `detectSender` — identify `From:` for outbound mail and inbox owner for inbound.
- `internal/cmd/prime.go:185` via `detectRole` — identify the role to render templates for.
- `internal/cmd/bead.go:224` via `detectCreatingAgent` — auto-tag `af bead create` with `created-by:<agent>`; returns "" on error so tagging is best-effort (`bead.go:223-229`).
- `internal/cmd/bead.go:286` via same helper — auto-scope `af bead list` to the caller unless `--all`.
- `internal/cmd/sling.go:604` via `detectAgentName` — resolve the launching agent when `--agent` is omitted.
- `internal/cmd/sling.go:161,270` — `resolveAgentName(callerWd, root)` used to compute the worktree `creator` argument for `ResolveOrCreate`.
- `internal/cmd/up.go:68` — same "creator for worktree" role.
- `internal/cmd/handoff.go:70` via `detectRole` — identify the agent to mail self.

### `config.Load*` conventions

Every cmd file that needs agent config does: `config.LoadAgentConfig(config.AgentsConfigPath(root))`. No callers embed the path literal. Sites: `install.go:223`, `helpers.go:79`, `dispatch.go:115`, `down.go:42`, `attach.go:39`, `up.go:44`, `mail.go:396`, `prime.go:83,112,191`, `sling.go:223,623`, `formula.go:165,287`. Dispatch additionally loads `config.LoadDispatchConfig(root)` (`dispatch.go:111,327`). `LoadMessagingConfig` is not called from cmd — it is consumed inside `internal/mail/router.go` (via `mail.NewRouter(wd, store)` at `mail.go:134,368`).

## Formal constraint tags in code

- **C-16** — `internal/cmd/install.go:60` — Python 3.12 version gate must precede any filesystem mutation.
- **C-DATA-2 / R-DATA-4** — `internal/cmd/step_test.go:484` — description content must survive JSON round-trip (regression pin on `state.description`).
- **C14** — `internal/cmd/step_test.go:470,479` — regression pin on the `IncludeAllAgents: true` requirement at `step.go:139`.
- **D1** — `internal/cmd/done.go:164` — no fallback on missing caller file; skip WORK_DONE rather than default-route.
- **D13** — `internal/cmd/bead.go:291` — split bd's overloaded `--all` into `IncludeAllAgents` + `IncludeClosed` axes.
- **H-4 / D15** — `internal/cmd/sling.go:578`, `internal/cmd/done.go:164`, `internal/cmd/done_test.go:354,401` — atomic-write ordering invariant between caller-file and formula bead.
- **R-ENF-1** — `internal/cmd/up.go` / `internal/cmd/sling.go` (referenced in commit `b78e24f`) — runtime precondition ordering `ResolveOrCreate` → `SetWorktree` → `Start`; guard lives in session package but cmd call sites conform.
- **R-INT-3** — `internal/cmd/down.go:93` — worktree remove only when no co-tenant agents remain.
- **R-INT-9** — referenced at `internal/cmd/step.go:130` and `internal/cmd/step_test.go:408` — memstore empty-assignee carve-out that MASKS IncludeAllAgents regressions in tests.
- **R-INT-10** — referenced at `internal/cmd/hook_pair_smoke_test.go:22` — dual-hook (quality + fidelity) lock/env collision detection.
- **R-API-2** — `internal/cmd/step.go:148` — `result.Steps[0]` is "next ready step" convention shared with `prime.go:375`, `done.go:105`, `handoff.go:138,177`.
- **R-API-5** — referenced in commit `bab1271` — `store.Render` / `store.RenderList` as the escape hatch for human-readable output in `af bead show`/`list`.
- **Gotcha #D6 (WRONG)** — `internal/cmd/prime.go:577-582`, `internal/cmd/prime_formula_test.go:321,377` — spec incorrectly said `len(iss.BlockedBy) > 0` was sufficient; code defensively re-reads each blocker via `store.Get` and checks `Status.IsTerminal()`.
- **Gotcha #D13** — `internal/cmd/step.go:42-55` — deliberate deviation from spec: `GateID` omits `omitempty` so the ready-branch JSON always has exactly 7 keys for the jq consumer.
- **Gotcha #22** — referenced in commit `e41342d` — `ctx` flows from cobra through every migrated signature.
- **AC-1 / AC-2 / AC-4 / AC-5 / AC-6 / AC #13 / AC #17** — acceptance criteria pinned inline: `worktree_integration_test.go:497,510,515,558,625,639,651,653`; `integration_test.go:421`; `done_test.go:355,395`.

## Gaps

- **Why `runMailCheckInject` uses a 5-second timeout (`internal/cmd/prime.go:331-335`).** Commit `7153843` says "add 5s context timeout to runMailCheckInject to prevent hangs" but does not name the hang mode. unknown — needs review.
- **Why `af dispatch` uses a 24h TTL + `retry_after_seconds` on top (`internal/cmd/dispatch.go:300-308,158-171`).** Two overlapping retry mechanisms; commit `2d214d3` states "State tracking prevents re-dispatching the same issue within a 24-hour TTL window" without reconciling the two. unknown — needs review.
- **Why `af prime` acquires the identity lock best-effort (warn, do not fail) (`internal/cmd/prime.go:273-278`).** No commit-message rationale found for the soft-failure mode. unknown — needs review.
- **Why `--formula` flag on `af prime` is kept as a hidden deprecated flag rather than removed (`internal/cmd/prime.go:33-34`).** Commit `9278bfd` made context injection unconditional but the flag still parses. unknown — needs review.
- **Why `runBeadCreate` defaults empty `--priority` to `Normal` rather than using the zero value (`internal/cmd/bead.go:201-205`).** Code comment explains the mechanism but not the originating bug/issue number. unknown — needs review.
- **Why `af down --all` uses SIGKILL (`pkill -9`) rather than SIGTERM (`internal/cmd/down.go:132`).** Commit `edaecb2` says "all agents get --dangerously-skip-permissions, orphan cleanup catches all" but does not justify `-9`. unknown — needs review.
- **Why `af formula agent-gen` writes role templates to a source-tree path (`internal/templates/roles/<name>.md.tmpl`) that requires `make build` to take effect (`internal/cmd/formula.go:199-207`).** The "embed at build time, require rebuild" flow is deliberate but its rationale over a runtime-loaded templates directory is not anchored. unknown — needs review.
- **Why `dispatched` marker is written unconditionally with no no-overwrite semantics while `formula_caller` preserves existing values (`internal/cmd/sling.go:664-671` vs `:591-598`).** Code comments describe the asymmetry but not why it was chosen. unknown — needs review.
