# System-wide invariants

Each invariant here MUST hold for the system to behave correctly. An entry
consists of:

- **Statement** — MUST-form one sentence.
- **Rationale** — why it exists, anchored to commit SHA or file:line.
- **Enforcement mechanism** — combination of axes / types / runtime checks
  / tests. "Convention only" is itself a finding — flagged explicitly.
- **Consequences if violated** — what breaks, with a real incident when one
  exists in git history.
- **Relevant idioms** — cross-links to `idioms.md`.

---

## INV-1 — Default-actor overlay opt-out is explicit

**Statement:** A call to `store.List` / `store.Ready` / `store.RenderList`
MUST pass `Filter{IncludeAllAgents: true}` whenever it intends to see
issues not assigned to the store's configured actor.

**Rationale:** The mcpstore adapter auto-scopes `List` to the store's
actor when `IncludeAllAgents: false` and the store has an actor
(`internal/issuestore/mcpstore/mcpstore.go:199-214`). The default exists
so per-agent views stay per-agent by default (RBAC); the opt-out exists
so operational queries (mail, step discovery, done cascade) work.

**Enforcement:**
- Contract tests across all adapters:
  `internal/issuestore/contract.go:324-352, 840-847`.
- White-box spy: `internal/cmd/step_test.go:398-479`
  (`TestStepCurrent_IncludeAllAgentsRequired`) — the **C14** regression pin.
- E2E: `internal/cmd/done_integration_test.go:17-109` fires WORK_DONE only
  on final step; fails without the opt-out.
- Convention at every production call site (no compile-time gate prevents
  forgetting it).

**Consequences if violated:** Real incidents in git history —
- WORK_DONE misfires one step into every multi-step formula (commit
  `e41342d`).
- A recent design (commit `63307bb`, 2026-04-18) tried to "fix" an
  RBAC issue by bypassing the overlay at the mcpstore seam instead of
  using this idiom. It was deleted rather than archived so it can't be
  cited as prior art.

**Relevant idioms:** #1 (`Filter{IncludeAllAgents: true}`).

---

## INV-2 — Identity is derived from trusted context, not caller input

**Statement:** Agent identity (who is making this call) MUST be resolved
via `resolveAgentName(cwd, factoryRoot)` — cwd → agents.json membership
→ AF_ROLE env fallback — and MUST NOT accept a caller-supplied
`--as` / `--actor` / `--from` flag or any user-facing identity override.

**Rationale:** The user's exact phrasing (preserved in feedback memory and
visible in the rejected design's deletion commit behavior):
*"why would we add overrides? when we add overrides... agents find them
and use them. definitely ZERO interest in adding an override if we can
achieve it another way."* AF_ROLE is writable only by
`internal/session/session.go:116, 159` — both trusted write paths.

**Enforcement:**
- Canonical implementation: `internal/cmd/helpers.go:55-102`.
- Tests: `internal/cmd/bead_test.go:121-158`,
  `internal/cmd/sling_test.go:230-282` pin the three-tier behavior including
  the "wrong path, AF_ROLE wins" and "wrong path, no AF_ROLE, error" cases.
- Convention: absence of any CLI flag that accepts identity
  (verifiable: `grep -rn "String(\"as\"\|String(\"actor\"" internal/cmd/`
  returns nothing).

**Consequences if violated:** The trust model collapses. Any agent can
impersonate any other agent in the bead store, the mail system, and the
worktree-ownership graph.

**Relevant idioms:** #2 (`resolveAgentName`).

---

## INV-3 — Library layer reads no environment variables

**Statement:** Code under `internal/issuestore/`, `internal/config/`,
`internal/formula/`, `internal/mail/`, `internal/worktree/`, `internal/lock/`,
`internal/checkpoint/`, `internal/fsutil/`, `internal/tmux/` MUST NOT call
`os.Getenv` / `os.LookupEnv` / `os.Setenv` / `os.Unsetenv`. Only the
command layer (`internal/cmd/`), session manager (`internal/session/`),
and entry point may touch environment state.

**Rationale:** GitHub issue #98. Before commit `d020a5e` (2026-04-11),
library-layer code read env vars directly, which meant tests leaked state
via `os.Setenv` across packages, producing intermittent failures.
Commit `f959234` (Phase 3) threaded actor through `NewWithActor`; commit
`d020a5e` removed library env-reads; commit `e4cb7a0` (#113) added the
regression scan; commit `7875acc` (#118) broadened it to all named env
values.

**Enforcement:**
- `internal/cmd/env_hermetic_test.go` (regression scan). Broadened to catch
  `os.Setenv` / `os.Unsetenv` in tests as well.
- Formula library accepts env lookup via an injected `EnvLookup`
  parameter, not a direct call (`internal/formula/vars.go`, commit
  `d020a5e`).

**Consequences if violated:** Flaky tests that only fail in CI (parallel
test runs), and hidden coupling that makes library behavior depend on
caller environment.

**Relevant idioms:** #2 (`resolveAgentName` is the one place env reads
are *permitted* because it's in `internal/cmd/`).

---

## INV-4 — MCP issue-store server binds loopback only (R-SEC-1)

**Statement:** The Python MCP server (`py/issuestore/server.py`) MUST
bind to `127.0.0.1` only. The bind address MUST NOT be configurable by
command-line or environment.

**Rationale:** R-SEC-1 — the server exposes full write access to the
issue store without authentication. Loopback-only restricts access to
processes on the same machine.

**Enforcement:**
- `py/issuestore/server.py:267-268` — hardcoded `127.0.0.1`.
- Comment anchor at `internal/issuestore/mcpstore/mcpstore.go:7` cites
  R-SEC-1.

**Convention-only gap:** The Go-side client does not verify the endpoint
file's address is literally `127.0.0.1` before connecting — so a
rogue/future server that wrote a different host into `.runtime/mcp_server.json`
would be reached. Defense-in-depth verification is unimplemented.
(See `gaps.md`.)

**Consequences if violated:** Unauthenticated remote write access to the
entire issue store on any reachable network interface.

**Relevant idioms:** #8 (endpoint-file rendezvous).

---

## INV-5 — Status.IsTerminal() is the single "done?" gate (D11/C-1)

**Statement:** The "is this issue finished?" decision MUST go through
`Status.IsTerminal()` (returns true for `closed` and `done`). Callers
MUST NOT compare the status field to a single sentinel value like
`"closed"`.

**Rationale:** The pre-fix code at `mail/translate.go` (prior to commit
`045c1e1`) compared against `"closed"` only, silently treating `done`-status
issues as unread. R-DATA-3 high-severity risk.

**Enforcement:**
- Type / method: `internal/issuestore/store.go` defines `Status.IsTerminal()`.
- `internal/issuestore/store_test.go:10` (`TestStatusIsTerminal`)
  pins the enum matrix.
- `internal/mail/translate_test.go:13-47` pins the six-status mail-read
  matrix.
- Convention at every caller — no lint stops a string comparison.

**Consequences if violated:** Mail silently unread when sender used `done`
instead of `closed` (real pre-`045c1e1` behavior). Issue-finished
predicates inconsistent across subsystems.

**Relevant idioms:** #7 (`Status.IsTerminal()`).

---

## INV-6 — H-4/D15 atomic-write ordering (done-to-mail)

**Statement:** When a command will trigger downstream effects based on a
file being present (e.g. WORK_DONE mail based on `formula_caller`), the
file MUST be written before the triggering event, and the file write
MUST itself be atomic.

**Rationale:** H-4/D15. A crash between non-atomic write and signal emits
a WORK_DONE with a caller file that contains a partial record, corrupting
downstream routing.

**Enforcement:**
- `internal/cmd/sling.go:578-598` — `persistFormulaCaller` call ordering.
- `internal/cmd/done.go:164` — cited rationale ("D1: no fallback. Per
  H-4/D15, a missing caller file means there is no caller").
- `internal/cmd/done_test.go:354` (`TestDone_NoCallerFile_NoMail`) —
  crash-between scenario.
- `internal/fsutil/atomic.go:11-17` — `WriteFileAtomic` for the atomic
  half.

**Known drift** (flagged in `gaps.md`): `persistFormulaCaller` at
`internal/cmd/sling.go:598` uses plain `os.WriteFile`, not
`fsutil.WriteFileAtomic`. The test passes because the crash window the
test simulates is between-writes (write-ordering half), not mid-write
(byte-level half). The "H-4/D15 atomic-write invariant" phrase in code
comments overloads both meanings; only the write-ordering half is
mechanically enforced there.

**Consequences if violated:** WORK_DONE mail pointing to a non-existent
or partially-written caller file; dispatch router silently drops the
event; parent agent waits forever.

---

## INV-7 — PID-based lock with dead-PID reclaim

**Statement:** The `internal/lock` package's file lock MUST serialize
access by writing the holder's PID and allowing subsequent callers to
reclaim the lock when the recorded PID is no longer live
(`processExists(pid)` returns false).

**Rationale:** A tmux-dead agent must not leave a permanently-held lock.

**Enforcement:**
- `internal/lock/lock.go:17-25, 74, 115` — PID struct + processExists.
- `internal/lock/lock_test.go:26-107` — dead-PID reclaim + self-PID
  scenarios.

**Consequences if violated:** Crashed agent leaves a stale lockfile; a
fresh agent attempting to start the MCP server, or a fresh sling
instantiation, blocks indefinitely.

**Relevant idioms:** #8 (endpoint-file rendezvous).

---

## INV-8 — Installable shell hooks source-of-truth is `hooks/`

**Statement:** The contents of `hooks/*.sh` and `hooks/*.txt` MUST be
byte-identical to `internal/cmd/install_hooks/*` — the directory that is
`//go:embed`ed into the binary.

**Rationale:** `go:embed` requires the file be inside the Go package tree;
the authorial source lives at the repo root so it can be tested and edited
without touching the embed path. Drift between the two is silent and
catastrophic (installed hook differs from source hook).

**Enforcement:**
- `internal/cmd/install_hooks_drift_test.go:30-56`
  (`TestInstallHooks_NoDrift`) — byte-equality assertion.

**Consequences if violated:** Install-time render writes a stale hook;
agents in the field run different hook logic than the repo source
documents. Real incident class: anyone editing `hooks/` and forgetting to
copy to `install_hooks/` breaks every new install until the drift test
catches it at build.

**Relevant idioms:** #5 (embedded-assets drift test).

---

## INV-9 — Python 3.12 enforced before filesystem mutation

**Statement:** `af install` MUST verify `python3 --version` reports
`Python 3.12.x` before creating or modifying any file on disk.

**Rationale:** The MCP server requires `aiohttp` and `sqlalchemy` pinned
to versions validated against Python 3.12. A partial install with a wrong
Python version produces a mangled factory that refuses to start.

**Enforcement:**
- `internal/cmd/install.go:60, 281-290` — `checkPython312` runs before
  any filesystem mutation (C-16).

**Consequences if violated:** Install succeeds silently; first `af up`
crashes on import error or subtle SQLAlchemy behavior difference.

---

## INV-10 — Rendered shell hooks prefer trusted-context env vars

**Statement:** Shell hooks (quality-gate, fidelity-gate) MUST resolve
`ROLE` and `FACTORY_ROOT` via `${AF_ROLE:-...}` / `${AF_ROOT:-...}`
with fallback, NOT via a bare `basename $(pwd)` or `af root` call.

**Rationale:** Constraint C12, commit `d053e5e`. When a hook fires inside
a worktree subdirectory or under `af dispatch`, `$(pwd)` is not the role
directory — the env-var preference keeps identity/root resolution stable
across those contexts.

**Enforcement:**
- `internal/cmd/hook_envvar_test.go:21-57`
  (`TestHookScripts_UseEnvVarFallback`) — four substring rules.
- Drift test ensures rendered and source copies agree (INV-8).

**Consequences if violated:** Hooks attribute quality failures to the
wrong agent, or fail to find the factory root when running under worktree
isolation.

**Relevant idioms:** #6 (`${AF_ROLE:-<fallback>}`).

---

## INV-11 — Factory-config version gate is declared but test-only

**Statement (degraded):** `FactoryConfig.Version` SHOULD be validated at
every load (`TestLoadFactoryConfig_VersionZero` /
`TestLoadFactoryConfig_FutureVersion` declare the gate). In practice,
`LoadFactoryConfig` has **no production callers** —
`internal/config/root.go:15, 31` uses `os.Stat(FactoryConfigPath(dir))` to
detect the factory root without ever parsing the JSON.

**Rationale:** Unknown — the loader and its tests exist (commit trace not
conclusively anchored by the subsystem archaeologist). The intent was
almost certainly that factory.json be version-gated at load, but that
load path is dead.

**Enforcement mechanism:** **Convention only** — and the convention is
silently unheld because no production caller invokes the version gate.

**Consequences if violated:** A future-version factory.json is not
rejected by any production code path; behavior on a mismatch is
undefined.

**Gap:** Flagged in `gaps.md` — either wire the loader into root
discovery or delete the dead version gate.

---

## INV-12 — No path literals for `.agentfactory/` or `agents.json`

**Statement:** All callers that need a path inside the factory root MUST
use the helper functions in `internal/config/paths.go`
(`AgentsConfigPath`, `FactoryConfigPath`, etc.), never a string literal
like `filepath.Join(root, "agents.json")`.

**Rationale:** Commit `ef0ecd9` relocated the config tree under
`.agentfactory/`; every path-literal caller had to be rewritten
(`c21f270` path-helper rollout). Path literals are the vector for silent
drift on a future relocation.

**Enforcement:** Convention — no lint rule. Verified across the cmd layer
(13 `AgentsConfigPath` call sites, zero deviations — see `subsystems/cmd.md`).

**Consequences if violated:** A future relocation quietly breaks any
caller that was never updated.
