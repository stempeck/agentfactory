# Architectural history

Timeline of formative commits, grouped by theme. Routine refactors are
excluded — this file documents the commits that *changed the architecture*,
not every commit that touched it. Every entry is anchored to a SHA.

Dates: YYYY-MM-DD.

---

## Theme 1 — The bd → MCP/SQLite backend migration (Issue #80)

This is the single largest architectural transition visible in git: the
issue store backend moved from an external `bd` CLI (shelling out to a
Go binary) to an in-process Python MCP server speaking JSON-RPC to the
Go adapter layer. The migration spanned 11 explicit phases.

| SHA | Date | Phase | What changed |
|-----|------|-------|--------------|
| `fdebd03` | 2026-04-10 | pre-Phase | Root cause analysis for #80 — articulated why bd had to go |
| `f174c5e` | 2026-04-10 | Design | design-v5 artifacts for #80 (the winning design) |
| `039110c` | 2026-04-16 | Phase 0 | Baseline latency benchmark for bdstore (pre-migration data) |
| `1a117c2` | 2026-04-16 | Phase 1 | `Notes` and `CloseReason` fields added to `Issue` — previously mutated `Description` |
| `f6eb6ae` | 2026-04-16 | Phase 2 | 9 new contract sub-tests in `RunStoreContract` |
| `f959234` | 2026-04-16 | Phase 3 | memstore `Patch/Close` stop mutating `Description`; `NewWithActor` threads actor through constructor instead of env |
| `ba77510` | 2026-04-16 | **Phase 4** | **Python MCP server created greenfield** — aiohttp + SQLAlchemy + SQLite, 4-table schema, loopback bind, endpoint-file rendezvous |
| `ef0411c` | 2026-04-16 | **Phase 5** | **Go mcpstore adapter** — HTTP JSON-RPC client, lazy lifecycle, `internal/lock.NewWithPath` added for startup serialization |
| `c93f9ef` | 2026-04-16 | Phase 6 | Wired mcpstore into production: `newIssueStore` returns mcpstore + takes actor; `checkPython312` runs before any filesystem mutation |
| `5b07735` | 2026-04-17 | Phase 10 | Integration tests migrated to mcpstore; `lifecycle_test.go` adds crash-recovery + SIGTERM-shutdown coverage |
| `7acd617` | 2026-04-17 | **Phase 7** | **bdstore deleted** — 2097 lines, 9 files. Sole production adapter is now mcpstore. |
| `fc4f703` | 2026-04-17 | Phase 8 | CI/Docker/docs cleanup — Python 3.12 install, C19 lint rule dropped, `bd` binary removed from quickstart/quickdocker |
| `c6431e2` | 2026-04-17 | Phase 9 | mcpstore concurrency test (8 goroutines × 200 ops × 10 loops) + latency benchmark pinned to AC-12 targets |
| `63307bb` | 2026-04-18 | post-migration | **Deleted a design** whose recommended RBAC fix bypassed the actor overlay at the mcpstore seam. User rejected it because it violated idiom #1; deleted (rather than archived) so it can't be cited as prior art. |

**Architectural consequences preserved to today:**
- `issuestore.Store` interface is the stable seam (not bd-specific anymore).
- `BD_ACTOR` env var still exported by session.go:159 for backward
  compatibility (see gaps.md).
- The 4-table SQLite schema is authoritative for bead state.

---

## Theme 2 — Library env-var hermeticity (Issue #98)

A multi-phase cleanup that made the library layer stop reading
environment variables directly, because tests were leaking state across
packages and CI was flaky.

| SHA | Date | Phase | What changed |
|-----|------|-------|--------------|
| `acac05b` | 2026-04-11 | Review | Peer review of the implementation plan |
| `28d3427` | 2026-04-13 | Design | design-v5 artifacts |
| `cc07334` | 2026-04-14 | Design | Sling auto-creation of assignment beads |
| `4123f8d` | 2026-04-14 | — | All phases of implementation landed |
| `d020a5e` | 2026-04-11 | **Phase 1** | **`os.Getenv` removed from library-layer code** — formula, issuestore, config |
| `137737f` | 2026-04-11 | Phase 2 | Command-layer callers updated to thread env through parameters |
| `5bf925f` | 2026-04-11 | Phase 3 | `t.Setenv` isolation in dispatch tests |
| `e4cb7a0` | 2026-04-11 | Phase 4 | Regression-test scan to catch future env reads in library layer |
| `7875acc` | 2026-04-17 | Follow-up | Broadened scan to ALL named env values (not just `AF_ROLE`) |

**Architectural consequences:** `INV-3` (library layer reads no env vars)
is enforceable mechanically, not only by convention. `formula.ResolveVars`
takes an injected `EnvLookup` function; mcpstore's actor comes from the
constructor.

---

## Theme 3 — Worktree inheritance + identity trust model

A cluster of fixes that tightened how agent identity is resolved and how
git worktrees are tied to agent sessions.

| SHA | Date | What |
|-----|------|------|
| `8cff1bf` | (pre-Apr-11) | Resolved #88: path-detection failure falls back to AF_ROLE (not an error) |
| `eff2387` | (pre-Apr-11) | Resolved #89: follow-up to #88 |
| `800ab5c` | (pre-Apr-11) | Centralized `resolveAgentName` — the three-tier implementation |
| `7fab214` | (pre-Apr-11) | Fixed leaky tests (#84) |
| `69f8693` | 2026-04-13 | Worktree inheritance fix plan |
| `775f434` | 2026-04-13 | `worktree.ResolveOrCreate` exists + tested; `af up <agent>` creates a worktree owned by that agent |
| `88a4a44` | 2026-04-13 | `launchAgentSession` in sling.go receives worktree values from `ResolveOrCreate`; old env-only inheritance block deleted |
| `b78e24f` | 2026-04-13 | **R-ENF-1 mitigation** — runtime precondition instead of type-level interlock. Documented tradeoff: type-level would have inverted `session → worktree` package layering. |
| `757895a` | 2026-04-13 | Fixed `TestConcurrentRemoveAgent_NoCorruption` via `fsutil.WriteFileAtomic` |
| `d053e5e` | (pre-Apr-16) | **C12** constraint — hook scripts use `${AF_ROLE:-…}` / `${AF_ROOT:-…}` for worktree env-var fallback |
| `c3cf1f1` | — | `launchAgentSession` package-var seam introduced (dispatch tests hanging 4m without it) |

**Architectural consequences:**
- Identity resolution (`resolveAgentName`) is the canonical trust anchor,
  enforced structurally + by tests.
- R-ENF-1 documents a deliberate tradeoff: accept runtime precondition over
  type-level guarantee to avoid package-layer inversion.
- Worktree ownership is a runtime property stored as `.runtime/worktree_owner`.

---

## Theme 4 — Formula / molecule system (pre-agent-factory)

Extracted from a prior "gastown" codebase into its current shape.

| SHA | Date | What |
|-----|------|------|
| `34c47f5` | 2026-03-27 | Phase 6A — `internal/formula/` package extraction begins |
| `29a7b3c` | 2026-03-27 | Phase 6B — extraction completes |
| `ce9fac2` | 2026-03-27 | sourceless-var crash fix |
| `643982e` | 2026-03-30 | Bead sources + **CR-1** universal CLI override at `vars.go:65` |
| `c40d91d` | 2026-04-? | Inputs-to-vars merge + `deferred` source |

**Architectural consequences:**
- Formula is a pure library; execution lives in `internal/cmd/`.
- CR-1 is the only formal constraint tag in the formula package.
- `deferred` source lets `{{name}}` placeholders survive template
  expansion unresolved.

---

## Theme 5 — Config tree relocation + path-helper rollout

| SHA | Date | What |
|-----|------|------|
| `c4712c5` | — | `internal/config` package birth |
| `d0f5a81` | — | Agent-gen schema + atomic writes |
| `2d214d3` | — | Dispatch config + `reservedNames["dispatch"]` |
| `9b6e0bd` | — | `notify_on_complete` semantics |
| `ef0ecd9` | — | **Config tree relocated under `.agentfactory/`** — `paths.go` introduced |
| `c21f270` | — | Path-helper rollout across all callers |
| `837c920` | — | `FindLocalRoot` + worktree redirect |

**Architectural consequences:**
- `INV-12` — no string-literal paths; every caller uses
  `internal/config/paths.go` helpers.

---

## Theme 6 — Hooks subsystem

| SHA | Date | What |
|-----|------|------|
| `dac416a` | 2026-03-23 | quality-gate introduced |
| `6f9a777` | — | quality-gate off-by-default |
| `96ec834` | — | Linux transcript parsing fix |
| `871e9f9` | 2026-04-10 | **fidelity-gate** introduced + drift test + sequential-pair smoke test |
| `d053e5e` | — | AF_ROLE/AF_ROOT fallback in hook scripts (C12) |

**Architectural consequences:**
- Hooks never block (always exit 0); enforcement is via mail-to-inbox.
- Drift test (INV-8) is the mechanical guarantee that source and embed
  stay aligned.

---

## Theme 7 — Template regeneration workflow

The role-template directory is not hand-edited for specialist agents; it
is regenerated from formula TOML via `af formula agent-gen`. A build test
enforces formula ↔ template parity.

| SHA | Date | What |
|-----|------|------|
| `26245eb` | — | Pinned regen-then-rebuild invariant |
| `8d64e6d` | 2026-03-28 | **Deleted deacon, refinery, witness** (CLAUDE.md is stale — see gaps.md) |
| `33fdf85` | — | Scenario agent added with real formula-based execution; |
| `38b7fdd` | — | Mass regen event |
| `9f24d60` | 2026-04-17 | Re-ran agent-gen after bdstore → mcpstore migration |
| `a2516f1` | 2026-04-17 | Re-ran agent-gen (same reason) |

---

## Theme 8 — Recent fixes still in flight

| SHA | Date | What |
|-----|------|------|
| `f992bc1` | 2026-04-18 | Issue #121 ready-actor-scoping design (**not yet implemented** — currently a design-doc addition; must be validated against idiom #1 before implementation) |
| `63307bb` | 2026-04-18 | Deleted rejected design (see Theme 1) — HEAD is the current state of main |

---

## What's *not* documented here

- Routine refactors, typo fixes, test flake patches — see `git log`.
- Formula authoring for individual agent roles — see `internal/templates/roles/`
  and the formula files under `.beads/formulas/`.
- Merge commits (they're in `git log` and rarely carry architectural change).

Re-run this skill to refresh; entries should only be added when a commit
materially changed what any subsystem is or how it contracts with another.
