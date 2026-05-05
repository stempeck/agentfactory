# config subsystem

**Covers:** internal/config

## Shape

The `internal/config/` package owns on-disk JSON configuration parsing, path construction under `.agentfactory/`, and factory-root discovery (`internal/config/config.go:1-225`, `internal/config/paths.go:1-75`, `internal/config/root.go:1-40`, `internal/config/dispatch.go:1-75`). Four config types exist: `FactoryConfig` (root marker, version-gated) at `config.go:52-57`, `AgentConfig` (agents.json) at `config.go:26-36`, `MessagingConfig` (groups cross-validated against agents) at `config.go:47-50`, and `DispatchConfig` (GitHub issue dispatcher) at `dispatch.go:10-23`. All on-disk paths hang off a single `root` string passed as the first argument to every path helper (`paths.go:13-19`) — the package is deliberately root-parameterized rather than env-parameterized, and no `os.Getenv`/`os.LookupEnv` call exists in any file in the package (grep-verified; see invariants below). Root discovery walks upward from a start directory and accepts a `.factory-root` redirect file to support git worktrees (`root.go:22-29`).

## Public surface

| Symbol | Role | Anchor |
|--------|------|--------|
| `FactoryConfig` | factory.json schema (type/version/name) | `config.go:52-57` |
| `AgentConfig` / `AgentEntry` | agents.json schema (name → type/description/directive/formula) | `config.go:26-36` |
| `MessagingConfig` | messaging.json schema (group name → agent list) | `config.go:47-50` |
| `DispatchConfig` / `DispatchMapping` | dispatch.json schema (GitHub issue dispatcher) | `dispatch.go:10-23` |
| `CurrentFactoryVersion` | highest supported factory.json schema version (= 1) | `config.go:23` |
| `ErrNotFound`, `ErrInvalidVersion`, `ErrInvalidType`, `ErrMissingField`, `ErrAgentExists`, `ErrAgentNotFound`, `ErrManualAgent` | sentinel errors used by `errors.Is` from callers | `config.go:13-20` |
| `LoadFactoryConfig(path)` | loads+validates factory.json | `config.go:104-123` |
| `LoadAgentConfig(path)` | loads+validates agents.json | `config.go:60-79` |
| `LoadMessagingConfig(path, agents)` | loads+validates messaging.json, cross-references `agents` | `config.go:82-101` |
| `LoadDispatchConfig(root)` | loads+validates dispatch.json (takes root, not path) | `dispatch.go:26-43` |
| `SaveAgentConfig(path, cfg)` | atomic write (temp file + rename) | `config.go:185-196` |
| `AddAgentEntry(cfg, name, entry)` | adds/updates agent with formula-field protection | `config.go:214-224` |
| `RemoveAgentEntry(cfg, name)` | removes agent; refuses if `Formula == ""` | `config.go:201-211` |
| `ValidateAgentName(name)` | filesystem/JSON safety + reserved-name check | `config.go:168-182` |
| `FindFactoryRoot(startDir)` | walk-up discovery, honors `.factory-root` redirect | `root.go:11-40` |
| `FindLocalRoot(startDir)` | walk-up to nearest `.agentfactory/` (worktree or factory) | `paths.go:59-74` |
| `DetectAgentFromCwd(cwd, root)` | parse agent name from `.agentfactory/agents/<name>` path | `paths.go:24-53` |
| `ConfigDir`, `AgentsDir`, `AgentDir`, `FactoryConfigPath`, `AgentsConfigPath`, `MessagingConfigPath`, `DispatchConfigPath` | pure path constructors over `root` | `paths.go:13-19` |

## Seams (who loads what, in what order)

| Config | Loader | Callers | Load order relative to siblings |
|--------|--------|---------|----------------------------------|
| `factory.json` | `LoadFactoryConfig(path)` (`config.go:104`) | Not called from production code today — only `config_test.go`. Root presence is checked via `os.Stat(FactoryConfigPath(dir))` in `FindFactoryRoot` (`root.go:15`,`root.go:31`), not via `LoadFactoryConfig`. | n/a — file presence is the root marker; schema is not parsed at command dispatch. |
| `agents.json` | `LoadAgentConfig(path)` (`config.go:60`) | `internal/cmd/down.go:42`, `internal/cmd/install.go:223`, `internal/cmd/prime.go:83,112,191`, `internal/cmd/up.go:44`, `internal/cmd/attach.go:39`, `internal/cmd/sling.go:223,623`, `internal/cmd/mail.go:396`, `internal/cmd/formula.go:165,287`, `internal/cmd/dispatch.go:115`, `internal/cmd/helpers.go:79`, `internal/mail/router.go:32`, `internal/worktree/worktree.go:157` | Loaded BEFORE `LoadMessagingConfig` (messaging validator dereferences the `*AgentConfig` arg — `config.go:96,146`). |
| `messaging.json` | `LoadMessagingConfig(path, agentsCfg)` (`config.go:82`) | `internal/mail/router.go:38` | Requires a loaded `*AgentConfig`; every group member is rejected unless it appears in `agents.Agents` (`config.go:144-149`). |
| `dispatch.json` | `LoadDispatchConfig(root)` (`dispatch.go:26`) | `internal/cmd/dispatch.go:111,327` | Loaded independently of agents.json at the config layer. `runDispatch` performs a separate cross-check of `NotifyOnComplete` against agents.json AFTER loading both (`internal/cmd/dispatch.go:126-127`) — this check is NOT inside `validateDispatchConfig`. |
| factory root | `FindFactoryRoot(cwd)` (`root.go:11`) | 20+ call sites across `internal/cmd/*`, `internal/mail/router.go:26`, `internal/worktree/`, `internal/checkpoint/`, `internal/formula/discover.go:22` | Called FIRST in every command that needs any other config; its result is the `root` argument passed to every path helper and every `Load*`. |
| local root | `FindLocalRoot(cwd)` (`paths.go:59`) | `internal/cmd/root_cmd.go:18`, `internal/cmd/prime.go:143`, `internal/cmd/helpers.go:56`, `internal/checkpoint/checkpoint.go:125`, `internal/worktree/worktree_test.go:255` | Distinct from `FindFactoryRoot`: returns the nearest `.agentfactory/` (worktree root) rather than resolving redirects to the factory root. Used where the worktree-local perspective matters (e.g. `af root`). |

## Path conventions

All paths are rooted at `<root>/.agentfactory/` (`paths.go:10` `const dotDir = ".agentfactory"`). The path helpers (`paths.go:13-19`) are pure functions of `root` — no environment lookup — producing:

- `<root>/.agentfactory/` — `ConfigDir(root)` (`paths.go:13`)
- `<root>/.agentfactory/factory.json` — `FactoryConfigPath(root)` (`paths.go:16`); presence is the root marker.
- `<root>/.agentfactory/agents.json` — `AgentsConfigPath(root)` (`paths.go:17`)
- `<root>/.agentfactory/messaging.json` — `MessagingConfigPath(root)` (`paths.go:18`)
- `<root>/.agentfactory/dispatch.json` — `DispatchConfigPath(root)` (`paths.go:19`)
- `<root>/.agentfactory/agents/` — `AgentsDir(root)` (`paths.go:14`)
- `<root>/.agentfactory/agents/<name>/` — `AgentDir(root, name)` (`paths.go:15`)
- `<root>/.agentfactory/.factory-root` — optional redirect file read by `FindFactoryRoot` (`root.go:23-29`) that points from a worktree root to the real factory root.

Root resolution order in `FindFactoryRoot(startDir)` (`root.go:11-40`):
1. If `<startDir>/config/factory.json` exists AND `<startDir>/.agentfactory/factory.json` does not, fail with a migration hint (`root.go:14-18`). Old-layout diagnostic, cited commit subject "run 'af install --init' to migrate to new layout".
2. Walk upward from `startDir`. At each directory: (a) if `.agentfactory/.factory-root` exists and the path it names contains `.agentfactory/factory.json`, return that path (`root.go:22-29`); (b) else if `.agentfactory/factory.json` exists here, return this directory (`root.go:30-33`).
3. Stop when `filepath.Dir(dir) == dir`; return `"not in an agentfactory workspace"` error (`root.go:34-37`).

`FindLocalRoot(startDir)` (`paths.go:59-74`) uses the same walk but returns the FIRST directory containing either `factory.json` OR `.factory-root` without resolving the redirect — so a worktree root is returned as itself, not as the factory root.

`AF_ROOT` is NOT read by the Go library. It appears only in:
- Shell hooks (`hooks/quality-gate.sh:12`, `hooks/fidelity-gate.sh:17`) which use `${AF_ROOT:-$(af root 2>/dev/null)}`.
- `internal/session/session.go:115,159` where it is EXPORTED into the tmux session environment for use by those hooks.
- Test assertions that verify the export happens (`internal/session/session_test.go:34`, etc.) and that hook scripts reference `${AF_ROOT}` (`internal/claude/settings_test.go:79-84`, `internal/cmd/hook_envvar_test.go:42-52`).

No `os.Getenv("AF_ROOT")` exists anywhere in the Go codebase (grep across all `.go` files).

## Formative commits

| SHA | Date | Subject | Why it matters |
|-----|------|---------|----------------|
| `c4712c5` | 2026-03-23 | "feat: Phase 1 Foundation — Go module, config, CLI, Makefile" | Birth of the package. Originally `FindFactoryRoot` looked for `config/factory.json` (see `ef0ecd9` body referencing the old location). `LoadAgentConfig`, `LoadMessagingConfig`, `LoadFactoryConfig` all land together. |
| `d0f5a81` | 2026-03-28 | "feat: add schema extension, validation, and atomic writes for agent provisioning" | Added `AgentEntry.Formula` (omitempty, backward-compatible), `ErrAgentExists`, `ValidateAgentName`, `SaveAgentConfig` (atomic via `.tmp` + `Rename`), `AddAgentEntry` with formula-field protection. Commit body: "All changes are purely additive — no existing code modified." Issue #14 Phase 1. |
| `a6625c2` | 2026-03-30 | "Added agent-gen --delete capability to remove agents" | Added `ErrAgentNotFound`, `ErrManualAgent`, `RemoveAgentEntry` — the counterpart to `AddAgentEntry`. Same formula-field protection (refuses to delete entries with empty `Formula`). Issue #24. |
| `2d214d3` | 2026-04-04 | Dispatch Phase 1 of 3 — "af dispatch runs a single cycle and exits" | Added `dispatch.go` + `DispatchConfig` + `LoadDispatchConfig` + `validateDispatchConfig`. Also added the `dispatch` reserved agent name in `reservedNames` (`config.go:43-45`) because `session.SessionName("dispatch")` would collide with the dispatcher's tmux session. Rationale lives in code comment (`config.go:41-42`). |
| `9b6e0bd` | 2026-04-05 | Dispatch Phase for notify_on_complete | Added `DispatchConfig.NotifyOnComplete` with default `"manager"` in `validateDispatchConfig` (`dispatch.go:70-72`). NOTE: the intra-config validator only defaults; cross-validation against agents.json happens in `internal/cmd/dispatch.go:126-127`, NOT in this package. |
| `ef0ecd9` | 2026-04-06 | Agents-directory relocation | Added `paths.go` (52 lines — all the `ConfigDir`/`AgentDir`/`AgentsConfigPath`/... helpers) and `DetectAgentFromCwd`. Changed `FindFactoryRoot` root marker from `config/factory.json` to `.agentfactory/factory.json`. This is the commit that formalized the `.agentfactory/` layout. |
| `c21f270` | 2026-04-07 | "This phase replaces ~45 hardcoded path constructions across 12 production source files with calls to those Phase 1 helpers." | Turned the new path helpers into the single source of truth across the codebase. |
| `837c920` | 2026-04-11 | Worktree isolation Phase 1 | Added `.factory-root` redirect handling in `FindFactoryRoot` (`root.go:22-29`) and added `FindLocalRoot` (`paths.go:59-74`). Commit body states these are "leaf dependencies" — without them "agents in worktrees cannot find their factory config" and "af root returns the factory root instead of the worktree root, breaking cross-agent runtime reads". |
| `800ab5c` | 2026-04-11 | `resolveAgentName()` centralization | Did NOT modify `internal/config/*.go` production files (only added tests at `paths_test.go:145-179`). Established the three-tier `FindLocalRoot` → `FindFactoryRoot` → `AF_ROLE` resolution order in `internal/cmd/helpers.go:55-99`. Relevant because it locks in that `DetectAgentFromCwd` is NOT authoritative on its own — membership in `agents.json` is the authority (`helpers.go:78-88`). Issue #78. |

## Load-bearing invariants (this subsystem's contribution)

- **Library layer does not read process env.** No `os.Getenv`/`os.LookupEnv` in `internal/config/` (grep-verified). This aligns with the broader invariant introduced by commit `d020a5e` ("fix: remove os.Getenv from library-layer code (Phase 1) (#98)", 2026-04-15) which cleaned the same pattern out of `checkpoint/`, `formula/`, and `tmux/`. `internal/config/` did not need the cleanup because it was already compliant — but any future addition to this package must remain so. See `internal/cmd/env_hermetic_test.go` (referenced by commit `7875acc`) for the regression scan.
- **Factory version gate is single-ended upper bound.** `CurrentFactoryVersion = 1` (`config.go:23`). `validateFactoryConfig` rejects `version < 1` AND `version > CurrentFactoryVersion` (`config.go:158-163`). Tests `TestLoadFactoryConfig_VersionZero` (`config_test.go:229`) and `TestLoadFactoryConfig_FutureVersion` (`config_test.go:246`) enforce both sides.
- **Factory type is fixed string.** `validateFactoryConfig` requires `c.Type == "factory"` (`config.go:155-157`); no other type is accepted. Test at `config_test.go:212-227`.
- **Agent type is a closed enum.** `validateAgentConfig` requires `agent.Type ∈ {"interactive","autonomous"}` (`config.go:130-132`); `"daemon"` or any other value is rejected with `ErrInvalidType`. Test at `config_test.go:42-57`.
- **Messaging groups cross-validate at load time.** `validateMessagingConfig` iterates every group member and requires `agents.Agents[member]` to exist (`config.go:144-149`). This is the reason `LoadMessagingConfig` takes `*AgentConfig` rather than a path — you CANNOT load messaging.json without already having loaded agents.json. Test at `config_test.go:130-152`.
- **Atomic agents.json writes.** `SaveAgentConfig` writes `.agents.json.tmp` then renames (`config.go:190-195`). Test `TestSaveAgentConfig_AtomicNoTempFileRemains` (`config_test.go:538-556`) verifies no tmp file is left behind on success.
- **Formula-field protection (both directions).** `AddAgentEntry` rejects updates to an existing entry whose `Formula == ""` with `ErrAgentExists` (`config.go:215-218`). `RemoveAgentEntry` rejects removal of an entry whose `Formula == ""` with `ErrManualAgent` (`config.go:206-208`). This is the mechanism preventing `af formula agent-gen` from stomping on hand-authored agents like `manager`/`supervisor`. Tests at `config_test.go:468-490` and `config_test.go:663-681`.
- **Reserved agent names.** `reservedNames["dispatch"] = true` (`config.go:43-45`); rationale in comment at `config.go:40-45`: `session.SessionName("dispatch")` → `"af-dispatch"` which collides with the dispatcher's tmux session. Test at `config_test.go:634-642`.
- **Agent-name grammar.** `^[a-zA-Z][a-zA-Z0-9_-]*$`, max 64 chars (`config.go:38`,`config.go:173`,`config.go:178`). Rejects path traversal, shell injection, spaces, leading dot, leading digit (tests at `config_test.go:325-345`, `config_test.go:610-632`).
- **DispatchConfig defaults are applied in the validator, not in the struct.** `IntervalSecs` defaults to 300, `RetryAfterSecs` to 1800, `NotifyOnComplete` to `"manager"` (`dispatch.go:64-72`). Because `validateDispatchConfig` is called inside `LoadDispatchConfig` (`dispatch.go:39`), all downstream consumers see populated values. Tests at `dispatch_test.go:184-233`.
- **DispatchConfig cross-validation is NOT in this package.** `NotifyOnComplete` is only defaulted here; validation against `agents.json` happens in `internal/cmd/dispatch.go:126-127`. Commit `9b6e0bd` kept it there deliberately because `LoadDispatchConfig` takes `root`, not a loaded `*AgentConfig`.
- **`DetectAgentFromCwd` returns path parts WITHOUT membership validation.** `paths.go:52` returns `parts[2]` unconditionally — a typo directory under `.agentfactory/agents/typo/` yields `("typo", nil)`. Membership validation is the caller's responsibility; see `internal/cmd/helpers.go:78-88` (issue #88, issue #89).
- **`FindFactoryRoot` refuses to follow a stale redirect.** If `.factory-root` points to a directory that no longer contains `factory.json`, the walk continues rather than returning the bogus path (`root.go:26-28`). Test at `root_test.go:143-164`.
- **Old-layout diagnostic is load-bearing for migration UX.** `FindFactoryRoot` explicitly checks for `<startDir>/config/factory.json` and emits a targeted error directing the user to `af install --init` (`root.go:14-18`). Test at `root_test.go:76-103`. Removing this check would make old-layout workspaces silently report "not in an agentfactory workspace".

## Cross-referenced idioms

- **Config-load pair pattern (mail subsystem).** `internal/mail/router.go:26-38`: `FindFactoryRoot(workDir)` → `LoadAgentConfig(AgentsConfigPath(root))` → `LoadMessagingConfig(MessagingConfigPath(root), agentsCfg)`. The agents-then-messaging order is forced by the validator signature (`config.go:82`).
- **Dispatch cross-validation pattern (dispatch command).** `internal/cmd/dispatch.go:111-127`: `LoadDispatchConfig(root)` → `LoadAgentConfig(AgentsConfigPath(root))` → `agentsCfg.Agents[dispatchCfg.NotifyOnComplete]` lookup. The cross-ref is in the command layer because `LoadDispatchConfig` intentionally does not take an `*AgentConfig`.
- **Root-first-then-paths pattern.** Every command handler that needs any config begins with `wd, _ := os.Getwd()` → `config.FindFactoryRoot(wd)` → pass the returned `root` to path helpers. Example: `internal/cmd/up.go:33`, `internal/cmd/sling.go:81`, `internal/cmd/mail.go:385`.
- **Three-tier agent identity pattern.** `internal/cmd/helpers.go:55-99` (`resolveAgentName`): `FindLocalRoot` → `DetectAgentFromCwd(cwd, localRoot)` → on failure, `DetectAgentFromCwd(cwd, factoryRoot)` → on failure OR membership miss, `os.Getenv("AF_ROLE")`. This idiom depends on `FindLocalRoot` and `DetectAgentFromCwd` from this package; the env read is intentionally at the command boundary, not in this package.
- **Seam override pattern (for tests).** The config package itself exposes no seams — it is pure IO with pure functions. Tests use `t.TempDir()` + `os.WriteFile` to stage real JSON on disk (`config_test.go` throughout). Compare to `internal/cmd/helpers.go:17-24` (`newIssueStore`) which IS a test seam — this package has no equivalent because there is nothing to stub.

## Formal constraint tags

No file in `internal/config/` references constraint tags like `C-n`, `D-n`, `H-n`, `R-*`, or `Gate-n` directly. The closest in-tree references:
- `internal/claude/settings_test.go:79` comments reference "C12" in the context of the `${AF_ROOT}` hook convention — the subject of that constraint is hook-layer path resolution, not this package.
- `.designs/worktree-isolation/constraint-verification.md:12` references `config.AgentsConfigPath(root)` in verifying a constraint about worktree isolation, but the constraint is owned by the worktree subsystem, not this package.

unknown — needs review: whether `/docs/architecture/invariants.md` (produced by the parallel archaeologist pass) will assign this subsystem formal tags. No such tags exist in code today.

## Gaps

- **`LoadFactoryConfig` has no production caller.** Every call to it in the repo lives in `config_test.go`. Production root discovery uses `os.Stat(FactoryConfigPath(dir))` (`root.go:15,31`) without parsing the JSON. This means `validateFactoryConfig`'s version-gate and type-gate are effectively dead code at runtime — enforced only at test time. unknown — needs review: whether any caller intentionally bypasses it, or whether this is dead code that was always meant to be called from `FindFactoryRoot`.
- **`DispatchConfig.NotifyOnComplete` cross-validation lives in the command layer (`internal/cmd/dispatch.go:126-127`), duplicating the pattern that `LoadMessagingConfig` embeds directly.** unknown — needs review: whether the asymmetry is deliberate (dispatch.json can validate alone; messaging.json cannot) or historical accident. Design doc at `.designs/dispatch-lifecycle-48/design-doc.md:222-224` shows the cross-check was specified to live in `runDispatch` but does not explain why it isn't pushed down into `validateDispatchConfig` with an optional `*AgentConfig` argument like messaging.
- **`reservedNames` contains only `"dispatch"`.** `session.SessionName(name)` prepends `"af-"`, so any name that collides with another named tmux session created by agentfactory should in principle be reserved. unknown — needs review: whether other tmux session names (`af-quality`, `af-fidelity`, etc. — if they exist) should also appear in `reservedNames`.
- **No schema-migration path for `FactoryConfig.Version`.** `CurrentFactoryVersion = 1` and the validator rejects anything else. There is no v0 → v1 migration code and no structure for adding v2. unknown — needs review: whether this is "YAGNI until v2 is needed" or a latent technical debt item.
- **`DetectAgentFromCwd` returning an unvalidated agent name is a known sharp edge** per the comment at `internal/cmd/helpers.go:68-77` referencing issues #88 and #89. It is not fixed inside this package — the membership check lives in `resolveAgentName`. unknown — needs review: whether the membership check should move into `DetectAgentFromCwd` itself (requiring it to take `*AgentConfig`).
- **Atomic-write temp file name is a constant `.agents.json.tmp`** (`config.go:191`), not a unique per-invocation name. Two concurrent `SaveAgentConfig` calls on the same path would race on the temp file. unknown — needs review: whether this is guarded by higher-level locking (`internal/lock/`) in practice, or whether it is a latent concurrency hazard.
