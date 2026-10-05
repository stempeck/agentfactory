<!-- cycle-af-d7166cc1 audit — Phase 1. Author: marketing-cycle agent. -->
# Cycle af-d7166cc1 — Audit & delta detection

**Date:** 2026-09-16 · **Operator:** Glenn Stempeck (`stempeck`) · **Repo:** stempeck/agentfactory
**Last cycle boundary:** v0.3.0 (2026-08-23) / cycle-3 (`af-c967c569`), close-out PR #107 + #109.

## Snapshot of the public surface (unchanged since cycle-3 unless noted)

- **Description:** "Multi-agent orchestration CLI for Claude Code — declarative TOML workflows,
  autonomous agents, context-compression recovery, inter-agent mail." — still accurate.
- **Homepage:** cycle-3 Medium article ("...here's every room") — will repoint if a new article publishes.
- **Latest release:** v0.3.0 (2026-08-23). No release since.
- **Topics:** 18, covers the core discovery set (claude-code, ai-agents, multi-agent-systems,
  agentic-ai, autonomous-agents, llm-orchestration, …) — healthy, no drift.
- **Pinned:** `agentfactory`, `stempeck` (profile repo) — intact.

## Development since the boundary

`git log v0.3.0..origin/main --oneline` returns exactly **two** commits:

| Commit | PR | What |
|---|---|---|
| `8d1a0066` | #111 | **Token economics: see, bound, and prove what a run costs its own context window** (closes #110) |
| `15d2e466` | #109 | marketing-cycle af-c967c569: cycle-3 post-merge records — *already in the ledger; not new* |

So there is **one** untold development this cycle: **Token economics (#111)**. It is a large,
singular flagship — 350 files, ~57k insertions, a whole new `internal/tokenomics/` package.

---

## NEW — untold features (absent from `announced-ledger.md`)

**Flagship: Token economics (#111)** — the factory can now see, bound, and prove what a run costs
its own context window. Verified sub-features (each command grep-confirmed in `internal/cmd/`, each
mechanism confirmed in `internal/tokenomics/` and `USING_TOKENOMICS.md`):

- **Sub-agent admission control (`capacity` arm).** A model profile can declare its shared backend
  pool (`AF_BACKEND_POOL_TOKENS` in `.agentfactory/models.json`); a sub-agent launch that would
  oversubscribe the pool is refused *before it starts*, with the arithmetic shown to the agent —
  through the PreToolUse permission channel, never a non-zero exit (ADR-007 amendment 2026-08-31).
  A child-footprint floor (`AF_BACKEND_CHILD_FLOOR_TOKENS`, default 50k) guards the first child near
  the ceiling; `AF_DISABLE_PARALLEL_SUBAGENTS` enforces a hard semaphore of one. Fails open on any
  resolution error; structurally inert on every cloud profile (no pool declared ⇒ refusal impossible).
- **Adaptive effort (`efficiency` arm).** A step that historically *generates* more than it needs
  gets a reduced effort level on its next session, chosen from history (not remaining room), never
  above the profile's declared ceiling, bounded so a run can't spend itself relaunching. Applies on
  *every* profile, roomy ones included. Self-edits from the improvement loop are checked so a formula
  cannot buy tokens by deleting a gate.
- **Session-start context budgets.** Formula context, mail, and memory each get their own budget, so
  a large step body can no longer evict your mail; each message reaches a session once.
- **Generation-figure telemetry.** Every closed step records what it *generated* (output, thinking,
  peak, sub-agent spend) beside its timing, with unmeasured figures as `null` (not zero). New verbs,
  all grep-confirmed present: `af telemetry band` (judge a step against learned medians, prints the
  band used), `af telemetry compare` (the one verb allowed to claim an improvement — two arms of
  runs; voids rather than fails when arms weren't held constant), `af telemetry rebuild` (keeps
  learned data alive across record rotation), `af tokenomics status` (names every reason the surface
  is inert), `af tokenomics on|off` (off is default; off is operator-only), `af turn interventions`.
- **Recurring dispatch (crons).** `dispatch.json` accepts a `crons` list (name, agent, cadence,
  vars); schedules survive restarts, back off with a bound, validate against the target formula at
  write time, fire without GitHub access, and appear in `af dispatch status`.
- **Harness stops fighting itself.** The fidelity grader is shown every intervention taken during a
  turn and grades compliance as compliant; grader sub-processes no longer fire the agent's hooks or
  take its session id; the watchdog does not recycle an agent told to wait; halted breakers / dark
  channels / a dead watchdog show on the pane, in `af statusline status`, and at `af up`.
- **mergepatrol trusts GitHub, not local git.** A PR counts as merged only when GitHub says so.
- **Two new skills (10 → 12):** `improve-solution` (propagates design decisions into derived docs),
  `perfeval-agent` (finds an agent's single highest-impact cost from telemetry). Both embedded under
  `internal/cmd/install_skills/`, so they ship via `af install`.
- **New operator guide `USING_TOKENOMICS.md`** (349 lines) + updates to USING_AGENTFACTORY/MEMORY/
  MODELS/TELEMETRY and an ADR on the session-start context surface.

*Standing untold backlog (from cycle-3, unchanged — none new this cycle):* self-recovering agents
head-on, durable memory vault head-on, fable family, dispatch pipeline head-on, multi-provider
head-on, the marketing-cycle dogfooding story, machine-readable JSON contracts.

---

## STALE — public claims failing verification (each with a fix for Phase 3 Tier A)

#111 changed no README and no CHANGELOG, so both still describe the v0.3.0 factory. Three provable
staleness defects, all in README.md:

1. **`README.md:249` — "Ten skills are embedded…"** → FALSE. **Twelve** skills ship
   (`ls .claude/skills/*/` = 12; `internal/cmd/install_skills/` embeds all 12, incl. the two new
   ones). **FIX:** change "Ten" → "Twelve"; add table rows for `/improve-solution` and
   `/perfeval-agent`.
2. **`README.md:420` — Roadmap "Default dispatch workflow included with the factory (#73)"** → #73
   is **CLOSED / COMPLETED** (2026-09-16). Listing a shipped item as roadmap is stale (this is the
   exact defect class cycle-1 caught). **FIX:** remove the line from Roadmap.
3. **`README.md:419` — Roadmap "Gate quality improvements — reduce fidelity-gate false positives on
   passive steps (#75)"** → #75 is **CLOSED / COMPLETED** (2026-09-16); #111's grader-shown-
   interventions work addresses it. **FIX:** remove the line from Roadmap.

**Verified-OK (not stale):** `README.md:236` "Twenty-four formulas ship" matches 24 `.toml` files
in `internal/cmd/install_formulas/` and all 24 appear in the family table. Description and topics
are accurate. USING_AGENTFACTORY/MEMORY/MODELS/TELEMETRY were refreshed by #111 and are current.

**GAPS (untold, not false — additive Phase-3 work, not a stale-claim fix):** README Observability
(`:325`) and Command Reference (`:363`) list `af telemetry on|off|status|report|usage` but not the
new `band` / `compare` / `rebuild` verbs, nor `af tokenomics status`. CHANGELOG has no v0.4.0 entry.

---

## STORY-WORTHY — candidates for Phase 2 ranking

1. **Token economics (#111) — the flagship, and effectively the only new story this cycle.** Kills
   a real, nameable pain ("the factory cannot see, bound, or prove what a run costs its own context
   window" — issue #110). Demonstrable in 60s (`af tokenomics status`, `af telemetry band`). Hits
   target phrases (multi-agent orchestration, autonomous agents, Claude Code) and speaks to the
   cost-anxiety every practitioner running long agents feels. Honest-limitation angle is built in
   (capacity arm inert on cloud; `af telemetry compare` voids rather than lies). **Rank #1.**
2. Supporting Tier-A refresh (rides the same PR, not its own story): the three STALE fixes above +
   README token-economics documentation + CHANGELOG v0.4.0 entry.

**Nothing else ranks** — the only other commit since the boundary is cycle-3's own close-out record.
No invented story. Recommend Phase 2 propose Token economics as the flagship and HOLD for operator
approval.
