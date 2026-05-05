# ADR-013: Minimal direct-dependency policy for go.mod

**Status:** Accepted
**Date:** 2026-04-21 (policy codification; precedent anchored to Phase 4 commit `bab1271` and Phase 8 commit `fc4f703` — the `bd`-removal work that drove ADR-001)

## Context

Agentfactory operates inside an enterprise environment with a specific,
approved dependency allowlist. New direct `go.mod` entries are not
automatically acceptable — each one must be weighable against that list
and justified against stdlib or existing subsystem alternatives.

The repo's history already shows dependency minimization as a
load-bearing architectural driver:

- **ADR-001** (MCP over direct SQLite library) chose an out-of-process
  Python server partly to avoid introducing a Go SQLite dependency.
  Two commits in that migration are decisive for the dep-reduction
  pattern: `bab1271` (Phase 4, `internal/bd/` deleted; Makefile `C19`
  lint drops two legacy allowlist entries) and `fc4f703` (Phase 8,
  `quickstart.sh`/`quickdocker.sh` lose `check_bd`/`install_bd`, CI
  drops the bd build step).
- **Current `go.mod` direct-require set on `main` (HEAD `f0c481d`)**
  is two entries: `github.com/BurntSushi/toml v1.6.0` and
  `github.com/spf13/cobra v1.10.2`. This minimal surface is the
  grandfathered baseline.

The policy was implicit — every reviewer relied on remembering it.
A near-miss surfaced during peer review of
`.designs/issue-126-three-way-disagreement/implementation-plan/implementation_plan_outline.md`:
Phase 2 (UX-3 TTY-aware prompt) proposed adding `golang.org/x/term`
for `term.IsTerminal(int(os.Stdin.Fd()))`, despite
`internal/cmd/prime.go:244-249` already using the stdlib-only idiom
`(stat.Mode() & os.ModeCharDevice) != 0` inside the same `internal/cmd`
package. The addition propagated through design-doc L572 → plan L497,
L530, L628 → review without challenge, because no document
anchored the dep-policy concern. The peer-review pass that validated
40+ file:line claims did not interrogate the `go.mod` delta.

This ADR codifies the policy so every future proposal touching `go.mod`
must engage with it.

## Decision

**Any new direct entry in `go.mod`'s `require` block requires an
explicit ADR entry justifying why stdlib and existing subsystem idioms
are insufficient.** The justification must:

1. Name the feature or concern that needs the package.
2. Enumerate stdlib alternatives and explain why each is inadequate for
   the concrete call site (not in the abstract).
3. Grep the repo for existing idioms inside the same subsystem that
   solve the same concern. If one exists, it must be reused; otherwise
   the justification must state that no same-subsystem idiom was found
   and name the greps performed.
4. State whether the dep appears on the enterprise allowlist.

Indirect (`// indirect`) entries pulled transitively by already-approved
direct dependencies are **not** in scope for this gate.

### Grandfathered direct-require set

No new ADR entry is required for these; they are the baseline:

| Package | Version | First approved | Use |
|---------|---------|----------------|-----|
| `github.com/BurntSushi/toml` | v1.6.0 | `34c47f5` (Phase 6A, formula-package introduction) | Formula TOML parsing |
| `github.com/spf13/cobra` | v1.10.2 | `c4712c5` (Phase 1 Foundation) | CLI framework |

Adding a new row to this table requires a new ADR (or an amendment to
this one); silent `go mod tidy`–driven additions are forbidden.

## Consequences

**Accepted costs:**
- Friction on anything proposing a new direct dep; every such change
  requires a dedicated ADR or an amendment adding a row to the
  grandfathered table.
- Reviewers must grep for equivalent idioms before accepting a dep
  addition — adds a step to review and design.
- Code may be slightly more verbose when using stdlib idioms over
  ergonomic third-party equivalents (e.g.,
  `(stat.Mode() & os.ModeCharDevice) != 0` vs.
  `term.IsTerminal(int(os.Stdin.Fd()))`).

**Earned properties:**
- `go.mod` direct-require surface stays small by default; the
  enterprise allowlist stays satisfiable without per-change exception
  handling.
- Existing idioms get reused, improving intra-repo coherence (one TTY
  idiom, not two).
- Design docs and plans have a citeable policy to defer to; drift from
  the policy is a **named deviation**, not an accident.

## Scope

Applies to:
- Any edit to `go.mod`'s `require` block that adds a new non-indirect
  entry.
- Any design doc or implementation plan that prescribes an external Go
  package in imports.

Does not apply to:
- Transitive (`// indirect`) deps pulled in by approved direct deps.
- Version bumps of already-approved direct deps (separate review
  concern — supply-chain / SemVer).
- The Python `py/requirements.txt` (separate dep surface; the Python
  3.12 runtime lock lives in ADR-012).
- System binaries invoked via `os/exec` (tmux, git, claude, python3.12).
  Those are installation preconditions, not Go-module deps.

## Enforcement

Convention-plus-review; no mechanical interlock yet. Specifically:

- review Phase 6 gains a claim-check: "Does this fix touch
  `go.mod`? If yes, does it cite this ADR with a grandfathered entry or
  a justification?"
- design Phase 1 options-considered step must reference this ADR
  when any option introduces a new import path starting with a
  non-stdlib, non-`github.com/stempeck/agentfactory` prefix.

A mechanical interlock (e.g., a pre-commit hook diffing `go.mod` and
requiring an ADR citation in the commit message) is a candidate future
enforcement but is out of scope for this ADR.

## Corpus links

- `invariants.md` — to be extended with an "approved `go.mod`
  direct-require set" invariant on the next `/architecture-docs`
  regeneration.
- `history.md#theme-1` — the `bd`-removal history that demonstrates
  dep-removal as an architectural driver (`bab1271`, `fc4f703`).
- Near-miss example:
  `.designs/issue-126-three-way-disagreement/implementation-plan/implementation_plan_outline.md:497,530,628`
  and parent design-doc L572.
- Stdlib TTY idiom precedent: `internal/cmd/prime.go:244-249`.
- Related ADRs: [ADR-001](ADR-001-mcp-over-sqlite.md) (precedent: dep
  removal as architectural driver),
  [ADR-012](ADR-012-python-preflight.md) (Python 3.12 as the only
  accepted runtime-level dep for the MCP server).
