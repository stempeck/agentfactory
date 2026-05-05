# ADR-005: Runtime precondition over type-level interlock for session ↔ worktree

**Status:** Accepted
**Date:** 2026-04-13 (commit `b78e24f`)

## Context

The session subsystem (`internal/session/`) needs to know which worktree
an agent is running in, so it can set `AF_ROOT`, `BEADS_DIR`, etc. The
worktree subsystem (`internal/worktree/`) resolves or creates worktrees
and returns a path + ID.

A design option considered was a **type-level interlock**: have
`session.Manager.Start()` require a `*worktree.Meta` parameter so that
the type system guarantees the session cannot start without one.

That design would require `session` to import `worktree` — which would
**invert the existing package layering** (worktree is lower-level and
the natural import direction is `session → worktree`, or neither imports
the other and `cmd` wires them together).

## Decision

Use a **runtime precondition** (`mgr.SetWorktree(path, id)` must be
called before `mgr.Start()`), not a type-level interlock. Document the
tradeoff explicitly as R-ENF-1.

From the commit message of `b78e24f`:
> R-ENF-1 mitigation — runtime precondition instead of type-level
> interlock. Documented tradeoff: type-level would have inverted
> `session → worktree` package layering.

## Consequences

**Accepted costs:**
- The compiler does not catch a missing `SetWorktree` call.
  `mgr.Start()` must panic or error clearly at runtime.
- Future developers can misuse the API (call `Start` without
  `SetWorktree`) and the failure will only surface in integration.

**Earned properties:**
- Package layering stays clean: `cmd` imports both `session` and
  `worktree`; neither imports the other. This prevents import cycles
  and keeps each package testable in isolation.
- Each wiring site (`up.go:85`, `sling.go:635`) is a single line that
  documents the sequence explicitly.

## Consequences in practice

The runtime precondition IS exercised at both production call sites
with a clear sequence: `NewManager` → `SetWorktree` → `Start`. Tests
follow the same sequence. So far, no integration failure has been
traced to a missing `SetWorktree` — the runtime cost of the tradeoff
has been zero.

## Corpus links

- `history.md#theme-3` — identity + worktree cluster
- `invariants.md` — R-ENF-1 is tracked as a documented tradeoff, not a
  MUST-hold
- Call sites: `internal/cmd/up.go:85`, `internal/cmd/sling.go:635`
- Related: this is the class of decision where the team chose
  ergonomic+clean over compile-safe, and documented the choice so it
  isn't silently reversed later
