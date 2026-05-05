# ADR-009: Package-var seams for test swapping of binary-dependent callouts

**Status:** Accepted
**Date:** 2026-04-16 (`newIssueStore` seam, commit `c93f9ef`);
`launchAgentSession` seam (commit `c3cf1f1`)

## Context

Two code paths in `internal/cmd/` depend on external binaries that CI
cannot reliably provide:

1. **Issue store construction** (`newIssueStore`) requires Python 3.12
   and the MCP server. Unit tests want to run against `memstore` or a
   fake without that dependency.
2. **Agent session launch** (`launchAgentSession`) blocks in
   `tmux.WaitForCommand` until `claude` writes a readiness sentinel.
   On CI where `claude` is absent, that never happens and the test
   suite hits its global timeout (~4 minutes).

Options considered:
- Dependency injection through cobra command closures (verbose,
  requires plumbing).
- Interface+mock (adds new abstractions for a single-call seam).
- Package-level `var` that tests reassign.

## Decision

Use **package-level `var` seams** for exactly these narrow cases.
Declared as `var <name> = func(...) ...` and reassigned in test files
to no-op or fake implementations.

Canonical example:
```go
// internal/cmd/helpers.go:17-24
var newIssueStore = func(workDir, beadsDir, actor string) (issuestore.Store, error) {
    return mcpstore.New(workDir, beadsDir, actor)
}
```

Tests swap with:
```go
t.Cleanup(func() { newIssueStore = originalImpl })
newIssueStore = func(...) (issuestore.Store, error) {
    return memstore.New(actor), nil
}
```

## Scope (what counts as a seam-worthy concern)

Package-var seams are used **only** for code paths that:
1. Shell out to or block on an external binary (Python, tmux, claude),
   and
2. Have a test-time substitute that is stable (memstore, a no-op
   launcher).

This ADR is **not** a license to make arbitrary globals "for testing."
Every seam has a docstring explaining why it's a var — see the
comments at `helpers.go:17-24` and `sling.go:607-615`.

## Consequences

**Accepted costs:**
- Package-level mutable state. Tests must restore the original on
  cleanup or they leak state into other tests in the same binary.
- Not thread-safe — but cobra commands run serially anyway.
- One documented bypass: `internal/cmd/install.go:126` calls
  `mcpstore.New` directly instead of going through `newIssueStore`.
  This is the installer banner; it's isolated.

**Earned properties:**
- Unit tests under `internal/cmd/` run without tmux, without claude,
  without Python 3.12, and without spinning up the MCP server.
- Integration tests still hit the real paths — the seam doesn't hide
  breakage, just keeps unit tests fast.
- The idiom is enumerated in `idioms.md#9`, making new seam additions
  a named design choice rather than silent globals.

## Corpus links

- `idioms.md#9` — full enumeration of seam sites and misuse detectors
- `seams.md#1` — issuestore seam shape
- `subsystems/cmd.md` — cmd-layer package-var conventions
- Call sites: `internal/cmd/helpers.go:17-24` (newIssueStore),
  `internal/cmd/sling.go:616-650` (launchAgentSession)
- Related ADRs: [ADR-001](ADR-001-mcp-over-sqlite.md) (why the Python
  dependency exists)
