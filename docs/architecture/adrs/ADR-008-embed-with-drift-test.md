# ADR-008: `go:embed` source-of-truth with mechanical drift test

**Status:** Accepted
**Date:** 2026-04-10 (fidelity-gate + drift test commit `871e9f9`);
pattern is older for Claude settings and role templates.

## Context

agentfactory ships hooks, Claude Code settings, and role templates as
files embedded into the `af` binary via `go:embed`. The embedded copies
live under `internal/cmd/install_hooks/`, `internal/claude/config/`,
and `internal/templates/roles/`.

The problem: the *source* files used during development and the
*embed* files compiled into the binary can drift. A bug-fix in
`hooks/quality-gate.sh` that isn't mirrored to
`internal/cmd/install_hooks/quality-gate.sh` silently produces a
binary that installs the old version.

## Decision

Treat the development source (`hooks/`, not-yet-in-embed templates)
as authoritative, and enforce parity with the embedded copies via a
**mechanical drift test** (`internal/cmd/install_hooks_drift_test.go:30-56`).

The drift test reads both the source tree and the embed tree, compares
byte-for-byte, and fails CI if they differ.

## Consequences

**Accepted costs:**
- Every hook change requires editing two files (source + embed copy)
  and re-running the test to confirm parity.
- The drift test is CI-only coverage — running `go build` locally
  will happily produce a binary with stale embeds.

**Earned properties:**
- Binary is self-contained: agents installed via `af install` get the
  exact files shipped in the binary, not whatever is on disk.
- Drift is impossible to ship silently. The test is mechanical and
  cannot be forgotten (no "add a test" discipline required).
- The authoritative-source / embed-mirror pattern extends cleanly to
  Claude settings and role templates, with the same drift guarantee.

## Template regeneration extension

Role templates under `internal/templates/roles/` are a *generated*
embed tree — regenerated from formula TOML via `af formula agent-gen`.
A separate build test enforces formula ↔ template parity
(`history.md#theme-7`). The invariant is the same: source and embed
cannot drift.

## Corpus links

- `invariants.md#INV-8` — drift-test invariant
- `subsystems/embedded-assets.md` — full embed-tree shape
- `history.md#theme-6` — hooks subsystem timeline (fidelity-gate
  introduction)
- `history.md#theme-7` — template regeneration workflow
- Source vs embed: `hooks/` (source) vs `internal/cmd/install_hooks/`
  (embed)
- Related ADRs: [ADR-007](ADR-007-hooks-never-block.md)
