# ADR-004: Library layer reads no environment variables

**Status:** Accepted
**Date:** 2026-04-11 (Phase 1 commit `d020a5e`, issue #98)

## Context

Prior to this decision, library packages (`internal/issuestore`,
`internal/formula`, `internal/config`) read `AF_ROLE`, `AF_ROOT`,
`BD_ACTOR`, and other env vars directly via `os.Getenv`. This caused:

- CI flakes: tests leaked env state across packages because Go's
  `t.Setenv` is per-test but library package init read env at
  construction time.
- Non-hermetic unit tests: a test in package A setting `AF_ROLE` could
  affect package B's behavior in the same test binary.
- Hidden dependencies: it was not possible to tell from a library
  function's signature which env vars it consumed.

## Decision

Library-layer code (everything under `internal/` that is not
`internal/cmd/` or `internal/session/`) **must not read environment
variables**. All context must be plumbed through constructors or
function parameters.

Specific mechanisms:
- `formula.ResolveVars` takes an injected `EnvLookup` function (not a
  direct `os.Getenv` call).
- `mcpstore.New` takes `actor` as a constructor parameter
  (`newIssueStore` reads `BD_ACTOR` in the cmd layer and injects it —
  see `helpers.go:17-24`).
- A regression test scans library sources for `os.Getenv` (commits
  `e4cb7a0`, `7875acc`).

## Consequences

**Accepted costs:**
- More parameters on library constructors. Small API cost; worth it.
- Cmd-layer callers must be explicit about which env vars they forward.

**Earned properties:**
- Hermetic unit tests — a library test cannot be polluted by env state
  from another test. This closed a class of CI flakes.
- Env-var consumption is an audit target: `grep os.Getenv internal/`
  should only match `internal/cmd/` and `internal/session/`.
- `session.Manager` as the sole writer of identity env vars becomes
  enforceable — library code cannot even observe leaked writes from
  elsewhere.

## Corpus links

- `invariants.md#INV-3` — the MUST-hold statement and enforcement
- `history.md#theme-2` — the 4-phase cleanup log
- Enforcement mechanism: library-source regression scan at commits
  `e4cb7a0`, `7875acc`
- `trust-boundaries.md` — this ADR is the structural counterpart to
  ADR-003 (no caller-supplied identity) and ADR-002 (default-safe
  actor scoping)
- Related ADRs: [ADR-003](ADR-003-no-identity-overrides.md),
  [ADR-009](ADR-009-package-var-seams.md)
