# Cycle af-a6b105d1 — Phase 1 Audit (2026-10-30)

Operator `stempeck` verified against runbook. Repo `stempeck/agentfactory`. Branch
`marketing/cycle-af-a6b105d1` off `origin/main` @ `ce8639e5`. Main tests GREEN (`make test`).

## Public-surface snapshot (vs last cycle)

- **Description:** "Multi-agent orchestration CLI for Claude Code — declarative TOML workflows,
  autonomous agents, context-compression recovery, inter-agent mail." — intact.
- **Homepage:** `https://medium.com/@glennstempeck/three-things-my-agent-factory-couldnt-do-last-month-4c98d4a7c1b4`
  (last cycle's Medium article; allowlist host `https://medium.com`) — intact.
- **Latest release:** **v0.3.0** (2026-08-23). v0.4.0 **never cut** — the standing release gap
  is still open (token economics `## v0.4.0 — 2026-09-17` sits in CHANGELOG, untagged).
- **Topics:** 18, covering the core set (`claude-code`, `ai-agents`, `multi-agent-systems`,
  `agentic-ai`, `claude`, `autonomous-agents`, …) — healthy.
- **Pins:** `agentfactory`, `stempeck` (profile) — intact.

## Development mined since last cycle

Boundary: last storied cycle `af-91fe0795` told PR #117 (merged `135794f0`). `git log
v0.3.0..origin/main` = 8 commits; the product commits not yet told:

| Commit | PR | What |
|---|---|---|
| `ce8639e5` | **#124** | Bootstrap convergence + much more (merged **today** 2026-10-30) — **NEW, untold** |
| `8d1a0066` | **#111** | Token economics (See/Bound/Prove) — Tier A done cycle af-d7166cc1 (#114), **never published as a story** |

Everything else in the window is prior marketing-cycle bookkeeping (#114, #119, #122, #109)
or a models.json tweak (`0bc93e20`).

## NEW (untold features)

### 1. Token economics (#111) — STRONGEST carryover, closes the v0.4.0 release gap
Shipped `8d1a0066` (2026-09-16); Tier A README/CHANGELOG written in cycle af-d7166cc1 (#114,
`709d070a`); Medium/LinkedIn drafts made and operator-reviewed (voice calibration PR #114) but
**never published** — the ledger still ranks it #1 untold. Surfaces:
- `af telemetry [on|off|status|report|band|usage|compare|rebuild]` (`internal/cmd/telemetry.go:20`) —
  **See** (per-step timing, token/session usage from the backend) and **Prove** (`compare` = the
  only verb that claims a change helped, over two arms of runs; `band` judges each closed step
  against learned medians). README already documents this block (lines 336–343). VERIFIED.
- `af tokenomics [on|off|status]` (`internal/cmd/tokenomics.go:23`) — **Bound.** The token-economics
  umbrella: `capacity` (window-driven admission control, inert on cloud/roomy profiles) +
  `efficiency` (baseline-driven, applies everywhere — output/thinking/sub-agent tokens, repeat
  file reads). OFF is operator-gated (like `af fidelity`); ON is never gated. **NOT in README.**
- `--input-digest` operator input-attestation flag (`internal/cmd/sling_input_digest_test.go`,
  `internal/cmd/telemetry_compare.go:76`). VERIFIED. Documented in USING_TOKENOMICS.md.
- Backing code: `internal/tokenomics/` (`advisory.go`, `band.go`, `digest.go`, `aggregate.go`,
  `backend_capacity.go`, …). Real and tested (`internal/cmd/tokenomics_test.go`).

### 2. Bootstrap convergence & supply-chain reproducibility (#124) — FRESHEST (landed today)
Merged `ce8639e5` (2026-10-30), 152 files / +28k lines. Headline:
- **Every bootstrap re-run converges each installed component to a pinned, digest-verified
  version** — Claude Code, Codex, gh, Go, Node.js, OpenObserve, Playwright, Ubuntu security
  updates, hash-locked Python. A **required** component that cannot converge **exits 1**
  (`quickstart.sh` `_converge()` @ `:251`, `CONVERGE_FAILURES` ledger @ `:57`). VERIFIED.
- **`convergence.json`** (`.runtime/bootstrap/convergence.json`, `quickstart.sh:3424`) records
  one TAB-separated row per component — proves what a container runs without re-probing it.
- **`--check` is now a read-only drift report** (`_converge_check`, `quickstart.sh:3366`;
  `ADR-027` @ `quickstart.sh:1872`). Previously bootstrap mutated; now you can audit without touching.
- **Daily advisory audit** — `.github/advisory-audit.sh` runs every pinned package against the
  advisory databases + `.github/advisory-exceptions.json` on a daily schedule (`.github/workflows/test.yml`).
- Architecture: **ADR-026/027** (`docs/architecture/adrs/ADR-027-bootstrap-convergence-contract.md`,
  `trust-boundaries.md`) — already written in #124. Corpus is current.
- Also in #124 (secondary): Playwright + aws-agents **reference integrations**, loopback-only
  telemetry, recycle-safe `design-plan-impl`, reworked `design-v7`, `fable-review`/`fable-increment`
  enhancements, `fable-secure` package auditing, scripted `perfeval-agent` measurement
  (`.claude/skills/perfeval-agent/scripts/perfeval.py`, +1386 lines).

### Standing untold backlog (carried from ledger — context, not this cycle's assignment)
- **Dogfooding story** — an agent (this one) that runs its own repo's marketing cycle; surfaced
  obliquely for cycles, never told head-on (ledger #2).
- **Durable memory vault** (`af memory` — agents remember across teardowns) (ledger #3).
- Self-recovering agents head-on; Fable family as its own piece; autonomous dispatch pipeline;
  multi-provider `gpt-*` formulas; machine-readable JSON contracts (`--json`), `af handoff`.

## STALE (claims failing verification — each with a fix)

| # | Claim / surface | Status | Evidence | Proposed fix |
|---|---|---|---|---|
| S1 | CHANGELOG records "notable changes"; **#124 has no entry** | STALE (currency) | `grep '#124\|converg' CHANGELOG.md` → none; `## Unreleased` holds only #117 | Add a #124 entry to the CHANGELOG (Tier A). If a release is cut, fold #117+#111+#124 into the version section. |
| S2 | README surfaces the measurement/economics surface | PARTIAL | README documents `af telemetry` (336–343) but **not** `af tokenomics` (`internal/cmd/tokenomics.go:23`) | If token economics is the flagship, add the `af tokenomics on/off/status` toggle + the Bound half to the README command reference (Tier A). |

No actively **false** claim found: README formula count ("Twenty-six", line 237) = 26 TOMLs in
`internal/cmd/install_formulas/` and all 26 **names match**; README skill count ("Thirteen", line
250) = 13 dirs in `.claude/skills/` and all 13 **names match**; description/homepage/topics intact;
the `af telemetry` block matches `telemetry.go`'s verb list; `--check` appears only in the (current)
architecture docs, so #124 introduced no stale user-facing `--check` claim.

## STORY-WORTHY (ranked input to Phase 2)

1. **Token economics (#111)** — ledger #1; closes the standing v0.4.0 release gap; Tier A already
   done; maximally demonstrable with real command output (`af telemetry compare`/`band`,
   `af tokenomics status`) which the voice law demands ("SHOW IT"). Caveat: drafted once (#114),
   never published — confirm the operator still wants it rather than something fresher.
2. **Bootstrap convergence / "every container runs exactly what you pinned" (#124)** — freshest
   (landed today); strong supply-chain/reproducibility angle for the Claude-Code-practitioner and
   enterprise audience; demonstrable via `quickstart.sh --check` drift report + `convergence.json`;
   never drafted. Natural release trigger on its own.
3. **Dogfooding** (this cycle itself) and **durable memory vault** — ledger #2/#3, still untold.

**Recommendation to carry into Phase 2:** propose token economics (#111) as flagship — it is the
ledger's #1, it closes the release gap, and it is the most "SHOW IT"-friendly — with bootstrap
convergence (#124) as the fresh alternative the operator may prefer precisely because it just
landed. HOLD for the operator's pick; the reorder is final.
