# ADR-012: Python 3.12 enforced before any filesystem mutation

**Status:** Accepted
**Date:** 2026-04-16 (Phase 6 commit `c93f9ef`)

## Context

The MCP issue-store server (ADR-001) requires Python 3.12. The `af`
CLI runs many commands; most of them will eventually touch the MCP
server (directly or indirectly). If Python 3.12 is missing, those
commands fail partway through after already performing side effects
(creating directories, writing config files).

A broken Python environment must not leave the user's factory tree in
a half-initialized state.

## Decision

`checkPython312` runs as a **preflight** at the start of any command
that mutates the filesystem or depends on the MCP server. If Python
3.12 is missing or the wrong version, the command fails immediately
with a clear error — before any side effect.

This is constraint **C-16**.

## Consequences

**Accepted costs:**
- A small latency cost per command invocation (one subprocess spawn
  to check `python3.12 --version`).
- Developers/users on systems without `python3.12` installed cannot
  use agentfactory. This is accepted — the MCP server requires it and
  there is no alternative backend currently.

**Earned properties:**
- Commands are **atomic-ish**: either they fail cleanly at the
  preflight, or they run to completion. No half-initialized state
  on a broken Python environment.
- Error messages are actionable: the user knows immediately to
  install/fix Python rather than debugging a downstream MCP-client
  error that blames the wrong thing.
- Discoverability: new filesystem-mutating commands added later can
  opt in to the same preflight pattern (convention).

## Scope

Preflight runs on:
- `af install --init` / `af install <role>` (before any config write)
- `af sling` (before bead creation)
- `af done` (transitively, via the Store construction)
- Any command that calls `newIssueStore`

Preflight is **not** run on pure read commands like `af root` or
`af --help`, because there's no state to corrupt.

## Corpus links

- Constraint **C-16** (preflight requirement)
- `subsystems/cmd.md` — preflight call sites
- `history.md#theme-1` (Phase 6 row, commit `c93f9ef`)
- Related ADRs: [ADR-001](ADR-001-mcp-over-sqlite.md) (why Python 3.12
  is required in the first place)
