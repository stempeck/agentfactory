# ADR-001: MCP server over direct SQLite library for the issue store

**Status:** Accepted
**Date:** 2026-04-16 (decisive: Phase 4 commit `ba77510`; design doc `f174c5e`)

## Context

Agentfactory's original issue store was a shell-out to an external `bd`
CLI binary. That backend had latency (baseline benchmark
committed in `039110c`, Phase 0 of issue #80) and required a separate
installable binary in the dependency chain. A replacement was required.

Two replacement shapes were on the table:

1. Import a Go SQLite library directly into `internal/issuestore/` and
   talk to the file from inside the `af` process.
2. Run SQLite behind a separate process exposing a JSON-RPC tool surface
   (MCP — Model Context Protocol).

## Decision

Run SQLite behind a separate Python 3.12 MCP server (aiohttp +
SQLAlchemy + SQLite WAL), with a Go adapter (`internal/issuestore/mcpstore/`)
speaking JSON-RPC over loopback HTTP.

The migration was executed as 11 explicit phases (`history.md#theme-1`),
with the old `bdstore` adapter deleted in Phase 7 (`7acd617`,
2097 lines removed).

## Consequences

**Accepted costs:**
- Lazy-start lifecycle in the Go client
  (`internal/issuestore/mcpstore/lifecycle.go`).
- Endpoint-file rendezvous under `.runtime/mcp_server.json` with
  lock-guarded startup serialization (ADR-010).
- Python 3.12 hard dependency; preflight in `checkPython312` before any
  filesystem mutation (ADR-012).
- Fragile error sentinel mapping: JSON-RPC error message
  `"issue not found:"` is substring-matched to
  `issuestore.ErrNotFound` (`subsystems/py-issuestore.md`).

**Earned properties:**
- `issuestore.Store` interface became backend-agnostic, so swapping
  storage later (Postgres, remote service) does not require changing
  the cmd layer.
- Multi-language clients can now use the MCP surface; Python agents and
  tools can read the same store without a Go binary dependency.
- SQLite WAL + SQLAlchemy gave the concurrency semantics that the
  previous bd CLI lacked (benchmark `c6431e2`: 8 goroutines × 200 ops
  × 10 loops).

## Corpus links

- `history.md#theme-1` — full 11-phase migration log
- `seams.md#2` — wire contract
- `subsystems/py-issuestore.md` — server shape
- `subsystems/issuestore.md` — Store interface and adapters
- Related ADRs: [ADR-006](ADR-006-loopback-no-auth.md),
  [ADR-010](ADR-010-endpoint-file-rendezvous.md),
  [ADR-012](ADR-012-python-preflight.md)
