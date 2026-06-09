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

## Sanctioned Exemption: Test-Support Process-Env Isolation (issues #317, #327)

`internal/testsupport/tmuxisolation` is permitted to read and write a defined set
of environment variables **because it is a test-support package, not library
code**. It isolates two env families, once per test-binary startup inside `Setup`
before `m.Run()`: the `TMUX_TMPDIR`/`TMUX` socket family (issue #317) and the
`AF_*`/`CLAUDE_*` identity family (issue #327). Neither violates the decision
above: these env vars are **not read by any `internal/*` library package** (the
invariant ADR-004 protects).

The `TMUX_TMPDIR` mechanism (issue #317) is **unavoidable** for the out-of-process
backstop: a `-L`/`-S` socket flag would not propagate to a spawned production
binary that builds its own `tmux` command line, whereas `TMUX_TMPDIR` is inherited
across the exec boundary. It is set once per test-binary startup in a `TestMain`
and consumed only by the spawned `tmux` subprocess (via environment inheritance on
`exec`). The accompanying `os.Unsetenv("TMUX")` is **load-bearing** (when the suite
runs inside a tmux session, `$TMUX` would otherwise take precedence over
`TMUX_TMPDIR` and point a child `tmux` back at the operator's real socket — the
exact #316 scenario); see the `internal/testsupport/tmuxisolation` package
doc-comment.

The `AF_*`/`CLAUDE_*` prefix wipe (issue #327) is `NeutralizeAFEnv()`: it
`os.Unsetenv`s every `AF_`/`CLAUDE_`-prefixed key (keeping the `AF_TEST_*`
test-infra set) before `m.Run()`. This **clears** env so each test falls back to
its own setup; it never **consumes** a named value into program logic, so it does
**not** re-introduce a library env read and does **not** violate the ADR-004
invariant. Without it, a default-suite test running inside a live agent inherits an
ambient `AF_SOURCE_ROOT`, which `resolveAFSource` ranks above the test's own
`compiledSourceRoot`, leaking agent-gen template writes into the operator's real
checkout. It is a **prefix wipe, not a named-member loop**, so it also neutralizes
dynamic `${AF_*}` formula-variable reads and any future family member; the exported
`AFEnvFamily` is the documented inventory (the assertion/drift target), not the
runtime wipe set. See the `internal/testsupport/tmuxisolation` package doc-comment.

**Audit mechanism:** `TestNoEnvReadsInLibraryPackages`
(`internal/cmd/env_hermetic_test.go`) skips `internal/testsupport/` via a
directory exemption, alongside the existing `internal/cmd/` exemption. The scan
continues to flag any env read/write elsewhere under `internal/`.

**Corpus links:** issue #317 (Phase 2b, TMUX socket isolation), issue #327
(`AF_*`/`CLAUDE_*` prefix wipe — `NeutralizeAFEnv`),
`internal/testsupport/tmuxisolation/tmuxisolation.go`,
`internal/cmd/env_hermetic_test.go` (the `testsupportDir` `SkipDir`).
