# Seams

Cross-subsystem contracts: wire protocols, shared types, package-var
seams, and embed-tree boundaries. Every entry has a direction, a
contract, and anchors. See `subsystems/*.md` for the per-subsystem view;
this doc is the index.

---

## 1. `issuestore.Store` interface — the primary domain seam

**Contract:** The Store interface in `internal/issuestore/store.go` is the
stable shape for bead CRUD. Two adapters implement it:

- `internal/issuestore/mcpstore/` — production, via JSON-RPC to Python
  server.
- `internal/issuestore/memstore/` — in-memory, for tests.

**Shared types:**

- `Filter` struct with `IncludeAllAgents`, `IncludeClosed`, `Assignee`,
  `Statuses`, etc. (`store.go:145-155`). The `IncludeAllAgents` axis is
  **the** cross-cutting idiom — see `idioms.md#1`.
- `Status` enum (6 values; D11/C-1) with `IsTerminal()` method —
  `invariants.md#INV-5`.
- `Issue` DTO with Notes + CloseReason (commits `1a117c2`, `f959234`).

**Who calls in:**

| Caller | Why |
|--------|-----|
| `internal/cmd/*` via `newIssueStore` package var at `helpers.go:17-24` | All cmd-level bead ops |
| `internal/mail/` | Mail is a Store-backed view (no bespoke persistence) |
| `internal/issuestore/contract.go` | Adapter-conformance test suite (21 sub-tests) |

**Package-var injection (test seam):** `newIssueStore` at
`internal/cmd/helpers.go:17-24` is the one documented swap point. Tests
replace the var to use memstore or a fake; production returns mcpstore.
One documented bypass: `internal/cmd/install.go:126` calls `mcpstore.New`
directly for the installer banner.

**Error contract:** `issuestore.ErrNotFound` is the sentinel for
not-found. The mcpstore client at
`internal/issuestore/mcpstore/client.go:80-84` maps JSON-RPC errors to
this sentinel via substring match of `"issue not found:"` against the
server's `KeyError` repr — fragile, flagged in `subsystems/py-issuestore.md`.

---

## 2. Python MCP server wire contract (JSON-RPC over HTTP, loopback)

**Contract:** JSON-RPC 2.0 dispatched via an aiohttp handler in
`py/issuestore/server.py`. 9 tool names prefixed `issuestore_*`. Full
list with param/return shapes is in
`subsystems/py-issuestore.md#wire-contract`.

**Bind:** `127.0.0.1`, ephemeral port. **INV-4**.

**Rendezvous:** `.runtime/mcp_server.json` written by server, read by Go
client. Startup serialization via `.runtime/mcp_start.lock`
(PID-based via `internal/lock.NewWithPath`, commit `ef0411c`).

**Go side:**
- `internal/issuestore/mcpstore/client.go` — HTTP + JSON-RPC marshalling.
- `internal/issuestore/mcpstore/lifecycle.go` — lazy start, health probe,
  crash recovery, lock-guarded serialization.
- `internal/issuestore/mcpstore/mcpstore.go` — Store impl on top of client.

**Shutdown contract (AC-8 b):** SIGTERM must flush WAL and exit cleanly.
Anchored at `py/issuestore/server.py:279-285`; E2E test
`internal/issuestore/mcpstore/lifecycle_test.go:148-187`.

---

## 3. tmux subprocess seam

**Contract:** `internal/tmux/tmux.go` wraps the tmux CLI with thin
exec.Command calls. Readiness check via `tmux -V`; has-session via
`tmux has-session -t <name>`; attach via `tmux attach-session -t <name>`.

**Callers:**

| Caller | File:line | Purpose |
|--------|-----------|---------|
| `internal/session/session.go` | many | Create + manage agent sessions |
| `internal/cmd/attach.go:39` | via session | `af attach` |
| `internal/cmd/up.go` / `down.go` | via session | Lifecycle |
| `internal/worktree/worktree.go:302` | bare call | `has-session` aliveness probe before GC (**known inconsistency**: uses bare owner name, not `af-` prefix — see `subsystems/session.md` Gaps) |

**Naming convention:** `internal/session/names.go` owns the `af-<role>`
prefix.

---

## 4. Git worktree seam (via `exec.Command("git", ...)`)

**Contract:** `internal/worktree/worktree.go` shells out to git for
worktree add/remove/branch-delete.

**Anchors:**
- Worktree add: `worktree.go:114`
- Worktree remove: `worktree.go:233` (no `--force`; see `subsystems/fs-primitives.md` Gaps — potential GC livelock)
- Branch delete: `worktree.go:246`
- Parent branch detect: `worktree.go:100`

**Also shells git:** `internal/checkpoint/checkpoint.go:130, 147, 155`
for dirty-state / HEAD / branch snapshots.

---

## 5. Claude Code settings + hooks (embed + install)

**Source of truth:** `internal/claude/config/*.json` (go:embed).
**Installed to:** `.claude/settings.json` in each agent workspace.
**Rendering:** `internal/cmd/install.go` calls `internal/claude.EnsureSettings`.

**Role template source:** `internal/templates/roles/*.md.tmpl` (go:embed).
**Installed to:** `CLAUDE.md` in each agent workspace (role-specific).

**Hook source:** `hooks/*.sh + *.txt` mirrored to
`internal/cmd/install_hooks/*`. Drift-detected by
`internal/cmd/install_hooks_drift_test.go:30-56` (**INV-8**).

**Interactive vs autonomous asymmetry:** Autonomous sessions run both
gates; interactive runs only quality-gate. See
`subsystems/hooks.md#shape`. Rationale is unanchored — flagged in
`gaps.md`.

---

## 6. Self-invoked `af` seam (fork-and-run)

**Contract:** A few cmd paths invoke `af` recursively rather than calling
the implementation function directly.

**Anchors:**
- `internal/cmd/done.go:262` — `exec.Command(afPath, "mail", "send", ...)` for
  WORK_DONE delivery. Architectural consequence: mail delivery is a
  subprocess, not an in-process call (makes `sendWorkDoneMail` testable
  only at the integration level).
- `internal/cmd/prime.go:334` — `exec.CommandContext(ctx, afPath, "mail",
  "check", "--inject")` for startup mail injection.

**Justification:** Unknown — neither comment nor commit message explains
the subprocess boundary. In-process refactor is probably safe but hasn't
been done. See `gaps.md`.

---

## 7. Config-load seam (the pair pattern)

**Shape:** See `idioms.md#3`. Every consumer of messaging.json first
loads agents.json and passes it to `LoadMessagingConfig`. This is a seam
because the order is type-enforced.

**Anchor:** Canonical use at `internal/mail/router.go:26-38`.

---

## 8. Formula DAG ↔ execution seam

**Source:** `internal/formula/` owns parsing, validation, DAG topo sort,
variable resolution (pure library).
**Consumer:** `internal/cmd/sling.go`, `internal/cmd/formula.go`
(`af formula agent-gen`, `af sling --formula`).

**Contract:**
- `formula.Parse(path) (*Formula, error)` — TOML only (the `.formula.json`
  discovery path at `internal/formula/discover.go` is broken/aspirational;
  see `subsystems/formula.md` Gaps).
- `formula.Sort(f)` — Kahn topo sort with cycle detection.
- `formula.ResolveVars(f, ctx)` — variable resolution with CR-1 CLI
  override precedence.

---

## 9. Quality gate → agent inbox (out-of-band mail)

**Shape:** Gate evaluator (claude CLI invoked by the shell script) emits
a verdict; the hook script translates the verdict into a mail bead in
the agent's own inbox (subject `QUALITY_GATE` / `STEP_FIDELITY`).

**Consequence:** Gate feedback is *asynchronous* — the Claude turn
completes (since the hook exits 0), and the bead appears on the next
mail check.

**Anchors:** See `subsystems/hooks.md`.

---

## Cross-reference

Every seam here has a more detailed home:

- `issuestore.Store` → `subsystems/issuestore.md`
- Python MCP wire → `subsystems/py-issuestore.md`
- tmux → `subsystems/session.md`
- git worktree / exec → `subsystems/fs-primitives.md`
- claude settings + templates → `subsystems/embedded-assets.md`
- hooks + mail-as-enforcement → `subsystems/hooks.md`, `subsystems/mail.md`
- self-invoked `af` → `subsystems/cmd.md`
- config-load pair → `subsystems/config.md`, `subsystems/mail.md`
- formula → `subsystems/formula.md`
