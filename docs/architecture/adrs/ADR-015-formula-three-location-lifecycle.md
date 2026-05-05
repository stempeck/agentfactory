# ADR-015: Three-location formula lifecycle with ordered sync

**Status:** Accepted
**Date:** 2026-04-28 (codified after issue #139 staleness incident)

## Context

Formula TOML files exist in three locations, each serving a distinct
consumer that cannot be collapsed:

1. **Source** (`internal/cmd/install_formulas/*.formula.toml`) — the
   authoring location. Embedded into the `af` binary via `//go:embed
   install_formulas/*` (`internal/cmd/install.go:22-23`). This is the
   source of truth for formulas that ship with agentfactory.

2. **AF repo mirror** (`.beads/formulas/*.formula.toml`) — git-tracked
   copies in the agentfactory repo. Required because `go install
   github.com/stempeck/agentfactory/cmd/af@latest` users don't have
   the source tree, and shell consumers (`agent-gen-all.sh:65`) use
   filesystem globs that can't access Go's `embed.FS`. Tracked in git
   (`.gitignore` lines 47-54 exclude other `.beads/` paths but not
   `.beads/formulas/`).

3. **Target project** (`~/af/myproject/.beads/formulas/*.formula.toml`)
   — the runtime copy in each project that uses agentfactory.
   Populated by `af install --init` (`internal/cmd/install.go:186-208`)
   from the binary's embedded formulas. This is the **primary formula
   source at runtime**: `FindFormulaFile()` (`internal/formula/discover.go:18-42`)
   searches factory root `.beads/formulas/` (line 22-24), and every
   formula consumer — `sling.go:315`, `formula.go:84`,
   `agent-gen-all.sh:65` — reads from this location.

The three-location model exists because no single location can serve
all consumers:

- The **embed** (`install.go:22`) can't serve shell scripts or runtime
  `FindFormulaFile()` — Go's `embed.FS` is compile-time only.
- The **af repo mirror** can't serve target projects — they're separate
  directory trees.
- The **target project copy** can't serve as the embed source — it's
  outside the Go module.
- Eliminating the mirror breaks `go install @latest` (no source tree to
  embed from) and shell-script consumers.

ADR-008 documents the general embed+drift-test pattern. This ADR
documents the formula-specific extension: a third location (target
project), bidirectional flow, and ordered sync requirements that ADR-008
does not cover.

### Incident that motivated this ADR

Issue #139: `quickstart.sh` called `make build` without first running
`make sync-formulas`, causing `.beads/formulas/` to go stale relative
to source. Agents generated from stale formulas dispatched `design-v3`
instead of `design-v7` during issue #136 processing. The root cause was
undocumented sync ordering requirements.

## Decision

### Three locations, two sync directions, ordered

The formula data flows in two directions:

**Forward (source → consumers):**
```
Source (internal/cmd/install_formulas/)
    ↓  make sync-formulas (Makefile:67-69)
AF repo mirror (.beads/formulas/)
    ↓  af install --init (install.go:186-208, via //go:embed)
Target project (.beads/formulas/)
    ↓  FindFormulaFile() (discover.go:22-24)
Runtime consumers (sling.go:315, formula.go:84, agent-gen-all.sh:65)
```

**Reverse (target project → source):**
New formulas authored in a target project (`~/af/myproject/.beads/formulas/new.formula.toml`)
are generated via `af formula agent-gen` (which finds them via
`FindFormulaFile()`), then manually copied back to
`internal/cmd/install_formulas/` to become permanent. This reverse flow
is manual and intentional — target projects are formula authoring
environments, not just consumers.

### Sync ordering: before consumption, always

Each sync point must execute **before** its downstream consumer reads
formulas. Sync-after-consume is the bug class this ADR prevents.

| Consumption point | Sync mechanism | Ordering constraint |
|-------------------|---------------|---------------------|
| `make build` (Makefile:28-29) | `make sync-formulas` | Before `check-formulas` gate (Makefile:58-65) |
| `agent-gen-all.sh` formula loop (line 65) | Inline `-nt` guarded copy from `$AF_SRC/internal/cmd/install_formulas/` | Before the `for f in $FORMULA_DIR/*.formula.toml` loop |
| `af install --init` (install.go:186-208) | Writes from `embed.FS` | Embed content is fixed at build time; sync-formulas before build ensures the embed is current |

### Drift detection is CI-only (accepted cost, per ADR-008)

`check-formulas` (Makefile:58-65) is a build gate that detects
source↔mirror drift. `TestFormulaDriftSourceVsInstalled`
(`formula_drift_test.go:21-62`) catches it in `go test`. Neither
mechanism runs at formula-consumption time — a local `go build` (not
`make build`) will happily embed whatever is committed. This is the
same accepted cost documented in ADR-008.

### The mirror cannot be eliminated

1. **Target projects don't have `internal/cmd/install_formulas/`.**
   The only formula location available to `FindFormulaFile()` in a
   target project is `.beads/formulas/` (`discover.go:22-24`).
2. **New formulas exist only in `.beads/formulas/`.** A formula
   authored in `~/af/myproject/.beads/formulas/` has no source-tree
   counterpart until manually copied.
3. **Shell consumers can't access `embed.FS`.** `agent-gen-all.sh:65`
   uses a filesystem glob; Go's `embed.FS` is invisible to it.
4. **`go install @latest` has no source tree.** Without the
   git-tracked mirror, remote-install users get no formulas.

## Consequences

**Accepted costs:**
- Formula changes require updating two locations in the af repo (source
  + mirror via `make sync-formulas`) and a rebuild to propagate to
  target projects. This dual-maintenance cost is shared with the
  hooks pattern (ADR-008).
- Sync ordering is enforced by script-level automation, not by the type
  system. A script edit that moves sync after consumption reintroduces
  the #139 failure mode. Behavioral tests mitigate this (issue #139
  Phase 3).

**Earned properties:**
- Target projects are self-contained formula authoring environments.
  New formulas work immediately via `FindFormulaFile()` without any
  source-tree access.
- The binary is self-contained: `af install --init` populates a
  project's formulas from the embed, requiring no network or source
  tree.
- Drift between source and mirror is mechanically detected by CI
  (`formula_drift_test.go:21-62`). Silent staleness can't ship.

## Corpus links

- ADR-008 — general embed+drift-test pattern (this ADR extends it)
- `invariants.md#INV-8` — drift-test invariant
- `subsystems/embedded-assets.md` — embed-tree shape
- `internal/formula/discover.go:18-42` — `FindFormulaFile()` search order
- `internal/cmd/install.go:22-23` — formula embed directive
- `internal/cmd/install.go:186-208` — formula extraction in `runInstallInit()`
- `Makefile:58-69` — `check-formulas` and `sync-formulas` targets
- `internal/cmd/formula_drift_test.go:21-62` — mechanical drift test
- Issue #139 — staleness incident that motivated this ADR
