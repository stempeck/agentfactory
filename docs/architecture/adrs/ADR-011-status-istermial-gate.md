# ADR-011: 6-value `Status` enum with `IsTerminal()` as the single gate

**Status:** Accepted
**Date:** Originally part of the `bd` CLI era; preserved and pinned in
Phase 1 of the MCP migration (commit `1a117c2`, 2026-04-16) as D11/C-1.

## Context

Beads have a lifecycle: open, assigned, in-progress, done, closed,
blocked (6 values). Callers frequently want to ask "is this bead
finished?" — i.e., in a state where no further work is expected.

The naive implementation uses string comparison: `if status == "closed"
|| status == "done"`. This is brittle in two ways:
- Every caller must enumerate the terminal values. Miss one → silent
  wrong behavior.
- Adding a new terminal value (hypothetical "cancelled") requires
  updating every call site.

## Decision

- A typed `Status` enum with exactly 6 values (pinned by D11/C-1).
- A method `Status.IsTerminal() bool` that is the **single gate** for
  "is this finished?" logic.
- Callers MUST use `IsTerminal()` — **never** string comparison.

Constraint tags `D11` and `C-1` anchor this in the codebase's own
comment convention.

## Consequences

**Accepted costs:**
- New terminal states require adding a case to `IsTerminal()` — one
  place, not N call sites. This is the opposite of a cost; it's the
  win.
- Linter or convention is the enforcement mechanism; no compile error
  if someone writes `status == StatusClosed`. But every such site is
  discoverable with `grep`.

**Earned properties:**
- Ready-queue logic (`store.Ready`) is a single conceptual gate.
- Tests against the Store contract (`issuestore/contract.go`) exercise
  every terminal and non-terminal combination through one predicate.
- The 6-value shape is stable across adapters (memstore, mcpstore),
  so cross-adapter tests apply the same predicate.

## Red-flag detector (from README.md)

> "Compare `status == \"closed\"` for the fast path" — INV-5, idiom #7.
> Use `Status.IsTerminal()`.

This is listed among the named anti-patterns reviewers should flag
during design review.

## Corpus links

- `invariants.md#INV-5` — the MUST-hold statement
- `idioms.md#7` — call sites of `IsTerminal()` and the deviation
  detector
- `seams.md#1` — `Status` is part of the shared type surface of
  `issuestore.Store`
- Origin: D11/C-1 constraint tags; preserved in commit `1a117c2`
- Related ADRs: [ADR-002](ADR-002-includeallagents-idiom.md) (companion
  "use the canonical predicate, not a string-match bypass")
