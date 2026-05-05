# fs-primitives subsystem (worktree + lock + checkpoint + fsutil)

**Covers:** internal/worktree, internal/lock, internal/checkpoint, internal/fsutil

Covers the four filesystem / concurrency primitive packages under `internal/`:
`worktree`, `lock`, `checkpoint`, `fsutil`. These are leaf dependencies — they
are consumed by `cmd/` and `session/`, they do not consume each other except
that `worktree` writes its meta file through `fsutil.WriteFileAtomic`
(`internal/worktree/worktree.go:66`).

## Shape

- `worktree` manages git worktrees as per-agent workspaces with a JSON meta
  file, branch `af/{agent}-{id-suffix}`, and owner-based inheritance / GC
  (`internal/worktree/worktree.go:41`, `:93`, `:280`).
- `lock` is a PID-based advisory file lock with dead-PID reclaim via
  `syscall.Kill(pid, 0)` (`internal/lock/lock.go:53`, `:117`).
- `checkpoint` captures git state (dirty files, HEAD, branch) plus formula
  context as a best-effort breadcrumb — explicitly documented as informational,
  not a recovery driver (`internal/checkpoint/checkpoint.go:1-6`).
- `fsutil` provides exactly one function: `WriteFileAtomic`, a temp-file +
  rename atomic write helper (`internal/fsutil/atomic.go:18`).
- Only `worktree` currently calls into `fsutil`; `lock` and `checkpoint` do
  raw `os.WriteFile` (`internal/lock/lock.go:85`,
  `internal/checkpoint/checkpoint.go:100`).
- Within the cmd layer, the H-4/D15 atomic-write ordering is about *write
  ordering* (file-before-bead), not about byte-level atomicity — it does not
  route through `fsutil` (`internal/cmd/sling.go:578-590`,
  `internal/cmd/sling.go:598`).

## worktree (internal/worktree/)

### Surface

- `GenerateID` — `"wt-" + 6 hex chars` from `crypto/rand`
  (`internal/worktree/worktree.go:32`).
- `BranchName(agent, wtID)` → `"af/{agent}-{suffix}"`
  (`internal/worktree/worktree.go:41`).
- `Create(factoryRoot, agentName)` — `git rev-parse --abbrev-ref HEAD` to
  capture parent branch, `git worktree add --quiet -b <branch> <absPath>`,
  write `.factory-root` redirect, create `.agentfactory/agents/`, write meta
  (`internal/worktree/worktree.go:93-151`).
- `SetupAgent(factoryRoot, worktreePath, agentName, isOwner)` — renders
  `CLAUDE.md` via `templates.New`, generates `settings.json` via
  `claude.EnsureSettings`, writes `.runtime/worktree_id` and conditionally
  `.runtime/worktree_owner` (`internal/worktree/worktree.go:155-225`).
- `Remove(factoryRoot, meta)` — `git worktree remove` (no `--force`; fails on
  uncommitted changes), delete meta file, `git branch -D <branch>`
  (`internal/worktree/worktree.go:229-253`).
- `RemoveAgent(factoryRoot, worktreeID, agentName)` — drop one agent from
  `meta.Agents`, rewrite meta, return `(meta, empty)` tuple
  (`internal/worktree/worktree.go:257-276`).
- `GC(factoryRoot)` — scan `.meta.json`, drop each whose owner tmux session
  is gone (`internal/worktree/worktree.go:280-316`).
- `FindByOwner(factoryRoot, agentName)` — linear scan of meta files, returns
  first owner match or `(nil, nil)` (`internal/worktree/worktree.go:367-392`).
- `ResolveOrCreate(factoryRoot, newAgent, creator, envWT, envWTID)` — the
  cotenancy/adoption entry point (`internal/worktree/worktree.go:339-363`).
- `WriteMeta` / `UpdateMeta` / `ReadMeta` — JSON marshal + atomic write via
  `fsutil.WriteFileAtomic` (`internal/worktree/worktree.go:57-88`).

### Cotenancy / C-7 adoption

`ResolveOrCreate` documents a four-branch decision order at
`internal/worktree/worktree.go:320-338`:

1. If `creatorEnvWT != ""`, inherit the passed-in worktree; no disk I/O
   (`worktree.go:340`). Comment at `:322-323`: "the env var was set by a
   trusted source (session.Manager at a prior start)."
2. Else if `creatorAgent != ""`, `FindByOwner(creatorAgent)`; if found,
   inherit (`worktree.go:343-351`).
3. Else `FindByOwner(newAgentName)` — "self-adoption. C-7 backstop for
   restart-after-crash when GC may not have cleaned up the prior meta"
   (`worktree.go:326-328`, executed at `:352-356`).
4. Else `GC(factoryRoot)` then `Create(newAgentName)` (`worktree.go:357-362`).

C-7 is the "one-owner-per-worktree" invariant established for the
worktree-inheritance-fix design (see `.designs/worktree-inheritance-fix/design-doc.md:21`
and `:142-144`). The branch-3 self-adoption is the specific mechanism that
preserves C-7 when a crashed agent restarts before GC observes its dead tmux
session. Commit `775f434` (2026-04-13) is the phase-1 landing of
`ResolveOrCreate`.

### Seams

| Seam | Where | Contract |
|------|-------|----------|
| `git` (exec) | `worktree.go:100` (parent branch), `:114` (worktree add), `:233` (worktree remove), `:246` (branch -D) | Shells out with `exec.Command`; aborts worktree creation on any git error, aborts `Remove` on uncommitted changes (no `--force`). |
| `tmux has-session` | `worktree.go:302` | `GC` liveness probe: if owner's tmux session is *not* running (`checkCmd.Run() != nil`), the worktree is considered stale and removed. |
| `config.LoadAgentConfig` | `worktree.go:157` | `SetupAgent` reads `agents.json` to select template role and description; a missing agent entry is a hard error (`worktree.go:162-164`). |
| `templates.New` / `templates.RenderRole` | `worktree.go:173-193` | Renders `CLAUDE.md` from embedded templates; falls back `autonomous`→`supervisor`, `interactive`→`manager` when no role-specific template exists (`worktree.go:175-182`). |
| `claude.EnsureSettings` | `worktree.go:201` | Generates `settings.json` in the agent directory. |
| `fsutil.WriteFileAtomic` | `worktree.go:66` | Atomic meta write (see fsutil section). |

## lock (internal/lock/)

### Contract

- File-based advisory lock holding a JSON `LockInfo{PID, AcquiredAt,
  SessionID, Hostname}` (`internal/lock/lock.go:16-21`).
- `Acquire` reads any existing lock, checks `IsStale` via
  `processExists(pid)` (`syscall.Kill(pid, 0)` — treats `nil` and `EPERM` as
  "alive"), and either reclaims or returns `ErrLocked`
  (`internal/lock/lock.go:53-90`, `:117-121`).
- The comment at `internal/lock/lock.go:52` is explicit: "Note: not atomic
  (TOCTOU between read and write). Advisory lock only." — this is NOT a
  mutual-exclusion primitive under concurrent acquires, just a liveness
  marker.
- `Release` is an unconditional `os.Remove`; it does not check PID ownership
  (`internal/lock/lock.go:93-98`). Consequence documented in
  `.designs/auto-terminate-agent-sessions-16/implementation-plan/IMPLREADME_PHASE2.md:321`:
  "The lock PID belongs to the Claude process (set during `af prime`), not
  the `af done` subprocess. `Release()` ignores PID — it just removes the
  file."

### Surface

- `New(workerDir)` — lock at `<workerDir>/.runtime/agent.lock`
  (`internal/lock/lock.go:37-39`). The conventional agent-identity lock.
- `NewWithPath(path)` — lock at an arbitrary absolute path; `workerDir` left
  zero-valued (`internal/lock/lock.go:41-47`). Added in commit `ef0411c`
  (2026-04-16) specifically so the mcpstore lifecycle can use
  `.runtime/mcp_start.lock` without forking the package. The lock-package
  diff in that commit was +17 lines.
- `Acquire(sessionID)` (`internal/lock/lock.go:53-90`).
- `Release()` (`internal/lock/lock.go:93-98`).
- `Read()` — returns parsed `LockInfo` or error (`internal/lock/lock.go:101-113`).

### Consumers

Production callers (grep `lock.New|lock.NewWithPath`):

- `internal/cmd/prime.go:274` — `l := lock.New(workDir)` inside
  `acquireIdentityLock`; `Acquire` failures print a warning and continue
  (`prime.go:273-278`). This is how the lock is initially taken at agent
  startup.
- `internal/cmd/done.go:196` — `_ = lock.New(cwd).Release()` during cleanup.
  Scoped inside the auto-terminate path per
  `.designs/auto-terminate-agent-sessions-16/designer-update-log-round3.md:9`.
- `internal/session/session.go:202` — `_ = lock.New(m.workDir()).Release()`
  on explicit stop.
- `internal/issuestore/mcpstore/lifecycle.go:49-60` — `NewWithPath` over
  `.runtime/mcp_start.lock`; `ErrLocked` branch polls for the winner's
  endpoint file instead of failing (`lifecycle.go:54-55`). This is the only
  place `NewWithPath` is used in production.

## checkpoint (internal/checkpoint/)

### Contract

- Captures three git facts plus a timestamp and optional formula/bead
  context:
  - dirty working tree via `git status --porcelain`
    (`internal/checkpoint/checkpoint.go:130-144`), parsing `XY filename`
    format into `ModifiedFiles`.
  - `LastCommit` via `git rev-parse HEAD` (`checkpoint.go:147-152`).
  - `Branch` via `git rev-parse --abbrev-ref HEAD` (`checkpoint.go:155-160`).
- Git root resolution: try `config.FindLocalRoot(agentDir)`; on error fall
  back to `agentDir` and let git traverse upward (`checkpoint.go:122-127`).
  This makes `Capture` worktree-aware.
- `Write` serializes with `json.MarshalIndent`, `os.WriteFile` at mode 0600
  (`checkpoint.go:94-104`) — NOT via `fsutil`, NOT atomic.
- All git `exec.Command` calls swallow errors silently (`if err == nil`
  guards at `checkpoint.go:133`, `:150`, `:158`) — a checkpoint is written
  even when git is unusable; fields simply stay empty.

### Trust

The package comment is the load-bearing statement of consumer contract:

> "Checkpoints are informational — they do not drive automated recovery
> decisions. Recovery is handled by .runtime/hooked_formula and
> bdReadySteps() in the done/prime commands."
> (`internal/checkpoint/checkpoint.go:1-6`)

Commit `643982e` (2026-03-30) rewrote the package documentation "from 'crash
recovery' to 'session context breadcrumb'" — the downgrade from a trusted
recovery source to an informational one is intentional and commit-anchored.

Actual consumers (all cmd layer):

- `internal/cmd/prime.go:466` — `outputCheckpointContext` reads the
  checkpoint on session start, prints a human-readable summary to the Claude
  prompt, warns on branch drift (`prime.go:487-492`), discards checkpoints
  older than 24h (`prime.go:470-473`).
- `internal/cmd/prime.go:523,535,548` — `writeFormulaCheckpoint` during
  PreCompact; every `checkpoint.Write` is a trailing comment
  "best-effort".
- `internal/cmd/done.go:191` — `_ = checkpoint.Remove(cwd)` during cleanup.
- `internal/cmd/handoff.go:127,148,192` — `Capture` + `Write` during explicit
  handoff; reused `Capture` just to list modified files at
  `handoff.go:192-198`.

The fact that consumers pattern-match on `_ = ...` / `if err == nil` (not a
single caller propagates a checkpoint error) confirms the "informational"
trust level end-to-end.

## fsutil (internal/fsutil/)

### H-4/D15 atomic-write invariant

The package comment frames it (`internal/fsutil/atomic.go:1-3`): "filesystem
helpers that wrap os primitives with stronger guarantees than the stdlib
offers out of the box."

The invariant for `WriteFileAtomic` is stated verbatim at
`internal/fsutil/atomic.go:11-17`:

> "WriteFileAtomic writes data to path by creating a uniquely-named temp
> file in the same directory and renaming it into place. Readers and
> concurrent writers see either the old contents or the new contents, never
> a partial or interleaved write. Last-writer-wins semantics still apply —
> this helper addresses byte-level corruption, not read-modify-write logical
> races."

Mechanism (`atomic.go:19-42`): `os.CreateTemp(dir, base+".*.tmp")` in the
*same directory* (so rename is a single-filesystem atomic metadata op), then
`Write` → `Close` → `Chmod` → `Rename`. Any error path unlinks the temp.

Commit anchor: `757895a` (2026-04-13), subject "Fix failing test
TestConcurrentRemoveAgent_NoCorruption worktree_integration_test.go:441:
ReadMeta after concurrent RemoveAgent". The commit added `internal/fsutil/`
(43 lines of `atomic.go`, 102 lines of test) and updated exactly one line in
`worktree.go` (the meta write). The package exists because concurrent
`RemoveAgent` calls were corrupting the meta file.

### IMPORTANT: "H-4/D15 atomic-write invariant" means two different things

There are two distinct invariants that both get called "H-4/D15 atomic
write" in the design docs and code comments, and they do **not** refer to
the same mechanism:

1. **Byte-level atomicity** (this package) — `fsutil.WriteFileAtomic`
   guarantees no partial-write window. Applied only to `worktree` meta
   files (`worktree.go:66`). Commit `757895a`.
2. **Write-ordering invariant** (cmd layer) — `persistFormulaCaller`
   (`internal/cmd/sling.go:591-599`) MUST write `.runtime/formula_caller`
   *before* `instantiateFormulaWorkflow` creates the formula bead, so that
   `af done` can never observe a (bead-exists, caller-file-missing) state.
   The comment at `sling.go:578-590` is explicit, and the pinning test is
   `TestDone_NoCallerFile_NoMail` (`internal/cmd/done_test.go:354`,
   referenced at `internal/cmd/done.go:164-166`).

Note: `persistFormulaCaller` itself uses plain `os.WriteFile`
(`sling.go:598`), not `fsutil.WriteFileAtomic`. The ordering invariant does
not depend on byte-level atomicity — a partial caller file is still
"a caller file exists" from `done`'s perspective. The two invariants share
a name in the design history but are mechanically independent.

### Consumers

- `internal/worktree/worktree.go:66` — `WriteMeta` (only production caller).
- `internal/fsutil/atomic_test.go` — tests (concurrency regression for the
  worktree bug).

`grep fsutil.WriteFileAtomic` across the tree returns exactly the one
production call site above. `done.go`, `sling.go`, `checkpoint.go`, and
`lock.go` all still use raw `os.WriteFile`.

## Seams (combined)

| Seam | Direction | Contract | Anchor |
|------|-----------|----------|--------|
| `git` (subprocess) | worktree → git | `git worktree add/remove`, `git branch -D`, `git rev-parse` — no `--force` on remove | `worktree.go:100,114,233,246` |
| `git` (subprocess) | checkpoint → git | `git status --porcelain`, `git rev-parse HEAD`, `git rev-parse --abbrev-ref HEAD`; errors silently swallowed | `checkpoint.go:130,147,155` |
| `tmux has-session` | worktree → tmux | Owner liveness probe; exit code 0 = alive = keep worktree | `worktree.go:302` |
| `syscall.Kill(pid, 0)` | lock → OS | PID liveness; `nil` or `EPERM` = alive | `lock.go:117-121` |
| `config.LoadAgentConfig` | worktree → config | Hard dependency for template selection | `worktree.go:157` |
| `config.FindLocalRoot` | checkpoint → config | Worktree-aware git root; soft (fallback to agentDir) | `checkpoint.go:125` |
| `templates.New` / `RenderRole` | worktree → templates | CLAUDE.md rendering with type fallback | `worktree.go:173-193` |
| `claude.EnsureSettings` | worktree → claude | settings.json generation | `worktree.go:201` |
| `fsutil.WriteFileAtomic` | worktree → fsutil | Byte-atomic meta write | `worktree.go:66` |
| `lock` | mcpstore → lock | `NewWithPath` for the mcp start-race guard | `mcpstore/lifecycle.go:49-50` |
| `lock` | cmd/prime, cmd/done, session → lock | `New(dir)` at `.runtime/agent.lock` for agent identity | `prime.go:274`, `done.go:196`, `session.go:202` |
| `checkpoint` | cmd/prime, cmd/done, cmd/handoff → checkpoint | Best-effort read/write/remove; failures never propagate | `prime.go:466,523,535,548`, `done.go:191`, `handoff.go:127,148,192` |

## Formative commits (combined)

| SHA | Date | Package | Subject / why |
|-----|------|---------|---------------|
| `f0c9f16` | 2026-03-23 | lock | "Phase 3 Prime System — role detection, templates, identity lock, prime command" — introduces `lock` alongside the prime command that consumes it. |
| `3110871` | 2026-03-27 | checkpoint | "add internal/checkpoint package — session checkpointing for crash recovery (Phase 6C)". Original framing was *recovery*. |
| `643982e` | 2026-03-30 | checkpoint | "Rewrites checkpoint package documentation from 'crash recovery' to 'session context breadcrumb'". Downgrades the trust contract from driver to informational. |
| `837c920` | 2026-04-11 | worktree | Leaf-dependency phase: "Without the worktree package, there is no way to create, manage, or clean up worktrees." Introduces `FindFactoryRoot`/`FindLocalRoot` support alongside worktree. |
| `059dc67` | 2026-04-11 | worktree | Lifecycle integration: "Worktrees are automatically cleaned up when their owning agent completes (`af done`) or is stopped (`af down`)." |
| `8e95314` | 2026-04-11 | worktree | Full lifecycle integration tests land. |
| `757895a` | 2026-04-13 | fsutil (new) + worktree | "Fix failing test TestConcurrentRemoveAgent_NoCorruption" — creates `internal/fsutil/` solely to fix concurrent meta-file corruption. Single-line change in `worktree.go` swaps `os.WriteFile` for `fsutil.WriteFileAtomic`. |
| `775f434` | 2026-04-13 | worktree | `ResolveOrCreate` lands; `af up <agent>` now creates a worktree owned by that agent; C-7 self-adoption backstop in place. |
| `d020a5e` | 2026-04-15 | checkpoint | "remove os.Getenv from library-layer code (Phase 1) (#98)". Aligns checkpoint with the #98 invariant (no library-layer env reads). |
| `ef0411c` | 2026-04-16 | lock | `NewWithPath` added so mcpstore's `.runtime/mcp_start.lock` can reuse the package without a rename. "start-race guarded by internal/lock (extended with NewWithPath)". |

## Load-bearing invariants

- **H-4/D15 byte-level atomic write** — `fsutil.WriteFileAtomic` guarantees
  no partial-write window for worktree meta files
  (`internal/fsutil/atomic.go:11-17`). Currently enforced at one call site:
  `internal/worktree/worktree.go:66`.
- **H-4/D15 write-ordering (distinct invariant, same name)** —
  `persistFormulaCaller` writes `.runtime/formula_caller` before the
  formula bead is created, so `af done` cannot observe bead-without-caller
  (`internal/cmd/sling.go:578-590`). Pinned by
  `internal/cmd/done_test.go:354` (`TestDone_NoCallerFile_NoMail`). Uses
  plain `os.WriteFile` (`sling.go:598`), not fsutil.
- **PID-based lock with dead-PID reclaim** — a stale lock file (dead PID,
  probed via `syscall.Kill(pid, 0)`) is automatically removed on next
  `Acquire` (`internal/lock/lock.go:24-26`, `:57-64`, `:117-121`). The
  comment at `lock.go:52` explicitly flags this as *advisory* and *not
  atomic* under concurrent acquires.
- **C-7 one-worktree-per-owner** — `FindByOwner` returns at most one meta
  because `Create` never re-assigns `meta.Owner` and `ResolveOrCreate`'s
  branch 3 (self-adoption) preserves the invariant across restart-after-
  crash (`internal/worktree/worktree.go:326-328`, `:339-363`; design
  anchor `.designs/worktree-inheritance-fix/design-doc.md:21`).
- **Checkpoint is informational, not authoritative** — documented in the
  package comment (`internal/checkpoint/checkpoint.go:1-6`); pattern-confirmed
  by every consumer using `_ = checkpoint.Write(...)` or `if err == nil`.

## Cross-referenced idioms

- **Atomic-write-before-signal (cmd layer)** — `persistFormulaCaller` at
  `sling.go:156` runs before `instantiateFormulaWorkflow` at `sling.go:173`
  (the "signal" is bead creation, which is what `af done` observes). This
  pairs with the byte-atomic invariant *in spirit* but uses neither `fsutil`
  nor a rename. Anchored in `internal/cmd/sling.go:578-590` and pinned by
  `TestDone_NoCallerFile_NoMail` (`done_test.go:354`).
- **Best-effort checkpoint writes** — every consumer discards the error:
  `_ = checkpoint.Write(workDir, cp)` (`prime.go:535,548`),
  `_ = checkpoint.Remove(cwd)` (`done.go:191`). The `_` is the type-level
  expression of the "informational, not authoritative" contract.
- **Worktree-aware git dir resolution** — `checkpoint.Capture` at
  `checkpoint.go:122-127` uses `config.FindLocalRoot` then falls back to
  `agentDir`. Mirrors the same pattern in other cmd-layer consumers that
  need to run git inside a worktree.
- **Redirect-file handshake** — worktree creation writes `.factory-root`
  inside the worktree (`worktree.go:125-128`); this is the reciprocal of
  `config.FindFactoryRoot`'s redirect-resolution logic referenced in commit
  `837c920`.

## Formal constraint tags

| Tag | Location | Meaning |
|-----|----------|---------|
| C-7 | `internal/worktree/worktree.go:326-328` (self-adoption comment); design anchor `.designs/worktree-inheritance-fix/design-doc.md:21` | One-worktree-per-owner invariant. |
| H-4 | `internal/cmd/sling.go:578`, `internal/cmd/done.go:164`, `internal/cmd/done_test.go:354,401` | (Primary) Write-ordering so caller-file precedes formula bead. Also used as the name for the byte-level atomic write (see D15). |
| D15 | `internal/cmd/sling.go:578`, `internal/cmd/done.go:164`, `.designs/extract-issuestore-interface-53/design-doc.md:585` | Same ordering invariant, phrased as a design decision. |
| R-INT-3 | `.designs/worktree-isolation/design-doc.md:200`; code locus `internal/worktree/worktree.go:257-276` (`RemoveAgent`) | Race between owner cleanup and co-tenant done. `RemoveAgent` does read-modify-write without file locking; accepted as low-likelihood. The fsutil atomic write covers *byte-level* corruption on each write but NOT the logical RMW race — this is exactly the gap the `fsutil.WriteFileAtomic` comment calls out at `atomic.go:14-17`. |
| R-API-1 | `.designs/worktree-inheritance-fix/design-doc.md:188` | Restart-after-crash would create a duplicate owner meta; mitigated by C-7 self-adoption branch. |
| R-DATA-1 | `.designs/worktree-inheritance-fix/design-doc.md:190` | Duplicate-owner metas would break `FindByOwner`; mitigated by unchanged `Create`'s one-owner invariant. |

## Gaps

- **R-INT-3 is not actually fixed.** `fsutil.WriteFileAtomic` guarantees the
  meta file is never *corrupt*, but two concurrent `RemoveAgent` calls will
  still lose one agent's update (read-modify-write without locking at
  `worktree.go:257-276`). The design doc at
  `.designs/worktree-isolation/implementation-plan/IMPLREADME_PHASE3.md:335`
  accepts this explicitly ("A future hardening pass could add file locking
  to `WriteMeta`"). Not a documented invariant failure, but worth flagging.
- **Checkpoint write is not atomic.** `checkpoint.go:100` uses `os.WriteFile`
  directly. A crash mid-write (rare but possible on SIGKILL) would leave a
  truncated JSON blob. Consumer `checkpoint.Read` would fail
  `json.Unmarshal` and `prime.go` would proceed without the context. Low
  impact because of the informational-only trust contract, but inconsistent
  with `worktree.WriteMeta` which *does* go through fsutil.
- **`lock.Acquire` TOCTOU** is documented in-code (`lock.go:52`). No fix in
  tree; relies on "no more than one Claude process per agent" operational
  assumption. The lock is genuinely advisory.
- **`Remove` does not `--force`** (`worktree.go:233`). An agent session
  that died with uncommitted changes in its worktree will block
  `worktree.Remove` — which is then called from `GC` at `worktree.go:309`
  where the error is swallowed (`if err := Remove(...); err != nil {
  continue }`), potentially leaving worktrees permanently undeletable by
  GC. Not flagged in any design doc I found — unknown — needs review.
- **Two distinct invariants share the name "H-4/D15".** The design history
  in `.designs/extract-issuestore-interface-53/` is the source of the
  confusion. A future architecture pass may want to rename one of them.
