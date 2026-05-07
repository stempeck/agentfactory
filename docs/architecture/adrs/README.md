# Architecture Decision Records

Nygard-format ADRs for the architecturally load-bearing decisions
visible in the codebase. Each ADR states the Context, Decision,
Consequences, and links back to the corpus (`idioms.md`,
`invariants.md`, `history.md`, subsystem docs) where the rationale is
anchored to file:line or commit SHA.

**Status codes:** `Accepted` (live in code), `Superseded`
(decision changed), `Deprecated` (left for history).

---

## Index

| # | Title | Status | Primary anchor |
|---|-------|--------|----------------|
| [001](ADR-001-mcp-over-sqlite.md) | MCP server over direct SQLite library for the issue store | Accepted | `history.md#theme-1`, `f174c5e` |
| [002](ADR-002-includeallagents-idiom.md) | `IncludeAllAgents: true` opt-out idiom over adapter-seam bypass | Accepted | `idioms.md#1`, commit `63307bb` (deleted counter-design) |
| [003](ADR-003-no-identity-overrides.md) | No user-facing identity override flags | Accepted | `invariants.md#INV-2`, `trust-boundaries.md` |
| [004](ADR-004-library-env-hermeticity.md) | Library layer reads no env vars | Accepted | `invariants.md#INV-3`, commit `d020a5e` |
| [005](ADR-005-runtime-precondition-over-types.md) | Runtime precondition, not type-level interlock, for session ↔ worktree | Accepted | `history.md` R-ENF-1, commit `b78e24f` |
| [006](ADR-006-loopback-no-auth.md) | Python MCP server binds loopback only, no auth | Accepted | `invariants.md#INV-4`, `subsystems/py-issuestore.md` |
| [007](ADR-007-hooks-never-block.md) | Hooks never block; enforcement via mail to agent inbox | Accepted | `subsystems/hooks.md`, `seams.md#9` |
| [008](ADR-008-embed-with-drift-test.md) | `go:embed` source-of-truth with mechanical drift test | Accepted | `invariants.md#INV-8`, commit `871e9f9` |
| [009](ADR-009-package-var-seams.md) | Package-var seams for test swapping of binary-dependent callouts | Accepted | `idioms.md#9`, commits `c93f9ef`, `c3cf1f1` |
| [010](ADR-010-endpoint-file-rendezvous.md) | MCP endpoint rendezvous via file under `.runtime/` | Accepted | `seams.md#2`, commit `ef0411c` |
| [011](ADR-011-status-istermial-gate.md) | 6-value `Status` enum with `IsTerminal()` as the single gate | Accepted | `invariants.md#INV-5`, D11/C-1 |
| [012](ADR-012-python-preflight.md) | Python 3.12 enforced before any filesystem mutation | Accepted | Constraint C-16, commit `c93f9ef` |
| [013](ADR-013-minimal-go-mod-dependencies.md) | Minimal direct-dependency policy for `go.mod` | Accepted | Precedent `bab1271`, `fc4f703`; grandfathered set at HEAD `f0c481d` |
| [014](ADR-014-no-interactive-prompting.md) | No interactive prompting in agent-runtime code paths | Accepted | Near-miss: `internal/cmd/sling.go:389-401` (#126 staged); permitted shape: `internal/cmd/prime.go:232-253` |
| [015](ADR-015-formula-three-location-lifecycle.md) | Three-location formula lifecycle with ordered sync | Accepted | Extends ADR-008; `discover.go:18-42`, `install.go:22-23`, issue #139 |
| [016](ADR-016-no-skill-provenance-in-formulas.md) | No skill provenance annotations in formulas | Accepted | `formula-create/skillmd-mode.md:79,137`; 60 annotations across 8 formulas |
| [017](ADR-017-no-customer-repo-mutations.md) | af infrastructure commands must not delete customer data | Accepted | `internal/cmd/formula.go:102-108`; designs 170/173 incident history |

---

## How ADRs are updated

- An ADR is **Accepted** when the decision is reflected in code and
  cited by at least one corpus file.
- An ADR is **Superseded** when a later commit reverses the decision;
  the superseding ADR is linked from both directions.
- Never delete an ADR. If a decision is reversed, add a successor —
  deletion loses the history of what was tried (see commit `63307bb`
  for the project's rationale about preserving rejected designs visibly).
