# ADR-006: Python MCP server binds loopback only, no authentication

**Status:** Accepted
**Date:** 2026-04-16 (Phase 4 commit `ba77510`)

## Context

The Python MCP issue-store server (ADR-001) accepts JSON-RPC tool calls
over HTTP from the Go `af` process. The question is how to authenticate
those calls and who is allowed to connect.

## Decision

Bind **127.0.0.1 only** (loopback), on an **ephemeral port**, with
**no authentication**. Trust model: same machine = same user.

Anchors:
- `py/issuestore/server.py:267-268` — literal loopback bind.
- `internal/issuestore/mcpstore/mcpstore.go:7` — R-SEC-1 rationale
  comment.
- Pinned by **INV-4**.

## Consequences

**Accepted costs:**
- Any local process running as the same user can connect and issue
  arbitrary bead mutations. This is an accepted risk: agentfactory is a
  single-developer tool; there is no multi-tenant scenario.
- The design cannot be deployed as-is to a shared host without
  revisiting this ADR.

**Earned properties:**
- Zero-latency auth: no handshake, token exchange, or certificate
  distribution.
- Simple endpoint rendezvous (ADR-010): the Go client reads
  `.runtime/mcp_server.json` and connects; no credentials to pass.

## Defense-in-depth gap

The Go client does **not** verify the endpoint file's host is literally
`127.0.0.1` before connecting. A future misconfiguration that wrote a
non-loopback host into `.runtime/mcp_server.json` would connect without
warning. This is a known gap (`gaps.md`) — not a live vulnerability
(the server only writes loopback) but a defense-in-depth hole.

## Corpus links

- `invariants.md#INV-4` — loopback bind as enforced invariant
- `trust-boundaries.md#cross-process-trust-boundaries` — same-user trust
- `subsystems/py-issuestore.md` — full server shape
- `seams.md#2` — wire contract
- `gaps.md` — missing defense-in-depth host validation
- Related ADRs: [ADR-001](ADR-001-mcp-over-sqlite.md),
  [ADR-010](ADR-010-endpoint-file-rendezvous.md)
