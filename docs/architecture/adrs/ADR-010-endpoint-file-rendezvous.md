# ADR-010: MCP endpoint rendezvous via file under `.runtime/`

**Status:** Accepted
**Date:** 2026-04-16 (Phase 5 commit `ef0411c`)

## Context

The Python MCP server binds an **ephemeral port** (ADR-006). The Go
client needs to discover which port to connect to, without requiring
the user or operator to configure one.

Options:
1. Fixed port. Simple but collides with other software and fails
   silently if two instances run.
2. Env var set by a wrapper script. Fragile; adds a wrapper.
3. Endpoint file written by server, read by client. Requires
   coordination but no external config.

## Decision

**File-based rendezvous** under `.runtime/mcp_server.json`, coordinated
by a **startup lock** under `.runtime/mcp_start.lock`:

1. Go client acquires the PID-based lock (via `internal/lock.NewWithPath`).
2. If endpoint file exists and server responds to a health probe → use it.
3. Otherwise, fork the Python server; wait for it to write the endpoint
   file; read port + host.
4. Release the lock.

The lock-guarded pattern prevents two `af` invocations from
simultaneously starting two MCP servers.

## Consequences

**Accepted costs:**
- File-system side effects in `.runtime/` that must be cleaned up on
  server shutdown (SIGTERM handler at `py/issuestore/server.py:279-285`
  flushes WAL and removes the file).
- Stale endpoint files after a crash: lifecycle code probes the
  endpoint before trusting it; a stale file produces a fail-to-connect
  which triggers a re-spawn.
- Lock itself is PID-based — a truly stuck lock requires manual
  cleanup. Rare in practice.

**Earned properties:**
- Zero configuration: works out of the box.
- Multiple `af` invocations share one MCP server per factory root.
- Crash recovery is clean: integration test
  `internal/issuestore/mcpstore/lifecycle_test.go:148-187` covers both
  SIGTERM-clean shutdown and crash-then-restart.

## Related invariants

- **INV-4** — loopback bind (ADR-006 companion).
- The `.runtime/` directory is also used for other agent state:
  `hooked_formula`, `formula_caller`, `dispatched`, `worktree_id`,
  etc. All conventional; no single owner enforces layout.

## Corpus links

- `seams.md#2` — wire + rendezvous contract
- `subsystems/py-issuestore.md` — server side
- `subsystems/issuestore.md` — mcpstore lifecycle
- `history.md#theme-1` (Phase 5 row)
- Lock implementation: `internal/lock/lock.go` (`NewWithPath` added in
  commit `ef0411c` for exactly this use case)
- Related ADRs: [ADR-001](ADR-001-mcp-over-sqlite.md),
  [ADR-006](ADR-006-loopback-no-auth.md)
