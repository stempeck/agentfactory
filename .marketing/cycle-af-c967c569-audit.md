<!-- Cycle audit — af-c967c569 (cycle-3). Author: marketing-cycle agent. Date: 2026-08-23. -->
# Cycle-3 Audit — stempeck/agentfactory (af-c967c569)

**Boundary:** last cycle closed at **v0.2.0** (2026-08-03, cycle af-50cdad6b). This audit
mines `v0.2.0..origin/main`.

**Main health:** GREEN — `make test` passes (root module, 20 packages ok, 2026-08-23).

**Development since v0.2.0 (the raw log):**

```
ae0a18cd Self-recovering agents, durable memory, live statuslines, trustworthy config, and an honest console (#104)
58c6b780 marketing-cycle af-50cdad6b: cycle-2 close-out state (ledger, report, log, CHANGELOG v0.2.0) (#97)
```

One substantive product PR — **#104** (merged 2026-08-21, closes #103). #97 is the
cycle-2 marketing close-out (already in the ledger). So the entire untold list this cycle
lives in #104, a large multi-feature drop.

**Public-surface snapshot (2026-08-23):**
- Description: "Multi-agent orchestration CLI for Claude Code — declarative TOML workflows, autonomous agents, context-compression recovery, inter-agent mail." (intact)
- Homepage: cycle-2 Medium article (`i-run-a-factory-of-ai-agents…`) — intact
- Latest release: v0.2.0 (no v0.3.0 cut yet)
- Topics: 18 present, core set (claude-code, ai-agents, multi-agent-systems, agentic-ai) covered — no drift
- Pinned items: agentfactory, stempeck — intact

---

## NEW — untold features (all from #104, none in the ledger)

Each verified present against source this cycle.

1. **Automatic context-exhaustion recovery (self-recovering agents)** — the watchdog now
   watches every agent's context occupancy and recycles an exhausted agent with its state
   checkpointed, so a filled window no longer wedges the agent burning tokens. A durable
   circuit breaker halts recycle loops and escalates to the operator; `af recovery reset <agent>`
   re-arms it after investigation. Supervision is always on (default factories no longer
   unsupervised; a heartbeat file proves the watchdog itself is alive; hung agents detected
   even with a busy statusline). Steps ending near a full window hand off to a fresh session
   cooperatively. **Verified:** `af recovery reset` exists (`af recovery --help`);
   `internal/checkpoint`, watchdog subsystem present; tests green.

2. **Durable memory vault** — `af memory add/list/check/status/export/graduate/expire/show`.
   Survives step close, teardown, reset, and worktree removal; re-injected at session start,
   bounded so it never bloats context; `quickdocker.sh` can bind the vault to a host dir so
   `docker rm` can't take learnings with it; improvement sessions read past learnings first.
   **Verified:** `af memory --help` lists all subcommands; `internal/memory` package tests green;
   this agent recorded its own note this cycle.

3. **Live statuslines** — `af statusline on|off|status|render` renders model, directory, git
   branch, diff size, elapsed time, a context-fill bar, and session/daily spend with accurate
   token accounting (no negative totals, no fabricated $0.00, survives compaction + transcript
   rotation). `af statusline status` self-diagnoses its pipeline and warns about config drift
   that would mis-aim recovery. **Verified:** `af statusline --help`; `internal/statusline`
   package tests green.

4. **Config editing you can trust** — setters reject unknown top-level keys loudly instead of
   silently erasing them on write-back, validate agent-name references across files, and accept
   an optional `--if-content-hash` compare-and-set precondition so concurrent edits conflict
   instead of clobbering. New setters for messaging + statusline config. `af config fingerprint`
   lets any consumer detect config-schema skew before writing. `af config models check` verifies
   per-class model coverage before a dead sub-agent does. **Verified:** `af config fingerprint --json`
   returns `{"state":"ok","fingerprint":"…"}`; strict-setter + CAS + crosscheck tests present in
   `internal/cmd/`.

5. **Quality gates that judge fairly** — gate judges now see exactly the turn being graded
   (calls paired with their results, in order, sub-agent noise excluded, gaps disclosed) and
   block only on contradiction, never on absence of proof. `af fidelity off --agent <name>`
   exempts a single misfiring agent instead of disabling oversight fleet-wide; every toggle is
   attributed in a provenance log; `af fidelity status` shows per-agent gate activity; agents
   can no longer switch off their own grader. **Verified:** `af fidelity --help`; fidelity tests
   present in `internal/cmd/`.

6. **Telemetry that tells the truth** — step records now carry context occupancy, consumption,
   and budget verdicts; interrupted steps show as INTERRUPTED instead of vanishing; and
   "not measured" is always distinct from "zero" in the table, the JSON, and the web console.
   **Verified:** `internal/telemetry` tests green. (Refines the already-told telemetry story.)

7. **A more capable, safer web console** — Settings covers more files (messaging, statusline,
   model-profile pins) and can no longer silently erase keys it doesn't understand (round-trip
   + secret-canary tests). Per-panel saves with write preconditions and an audit line; absent
   files stay absent instead of being materialized with invented defaults. The Floor now shows
   context-fill and recovery badges, so an exhausted or halted agent is visibly distinct from a
   healthy one. **Verified:** web module tests (`web/internal/server/settings_write_test.go`,
   formula-write audit line at `server.go:1237`). (Refines the already-told console story.)

8. **Sharper formulas, docs, and CI** — no more literal `{{placeholder}}` text in operator mail
   / PR titles; artifacts survive to the PR; an unavailable review sub-agent is recorded as
   unavailable, never as a clean pass; operator manual restructured with the destructive-teardown
   warning in plain text; README/CLAUDE.md match the real command surface; CI now runs the web
   module, client-JS conformance lanes, and the hook end-to-end tests it was silently skipping.

### Still-untold backlog (carried from cycle-2, NOT in #104)
- Fable agent family: fable-implement / fable-increment / fable-review / fable-secure (#83) — named in the formula table only, never a story.
- Autonomous dispatch pipeline: label matching, issue→PR handoff, cycle locking, phase advancement (#38, #79).
- Per-agent model selection + in-session gate continuation (`af done --phase-complete --gate`) (#81).
- Marketing-cycle itself (the dogfooding story — an agent that markets its own repo; surfaced obliquely in the cycle-2 article, never told head-on).
- Machine-readable JSON contracts (`af agents/dispatch/formula … --json`), `af handoff`, `af watchdog`.

---

## STALE — claims failing verification (each with proposed fix)

1. **Roadmap contradicts shipped reality — "formula authoring in the browser".**
   README line 410 (Roadmap) lists "Web console growth — deeper agent detail, **formula
   authoring in the browser**" as a *future* item, but browser formula authoring is SHIPPED:
   README line 270 already describes it in present tense, and the web console exposes a
   `PUT /api/formulas/<name>` write path (`web/internal/formschema/reader.go`, audit line
   `web/internal/server/server.go:1237`).
   **FIX:** drop "formula authoring in the browser" from the Roadmap line; keep "deeper agent
   detail." (Tier A, this branch.)

2. **CHANGELOG has no entry for #104.** Top entry is v0.2.0 (2026-08-03). An entire release of
   features — self-recovery, memory, statuslines, config integrity, fair gates, honest telemetry
   + console — is unrecorded.
   **FIX:** add a new version section (proposed `v0.3.0 — self-recovery & honesty`, or `Unreleased`
   pending the release decision) capturing #104. (Tier A, this branch.)

3. **docs/recovery-model.md is incomplete post-#104.** It documents crash recovery,
   `af prime` re-injection, PreCompact/compact-handoff, checkpoints, identity locks, and zombie
   killing — but NOT the new *automatic context-exhaustion recovery*: watchdog context-occupancy
   recycling, the durable recovery breaker + `af recovery reset`, always-on supervision +
   heartbeat, or cooperative near-full-window handoff. Section 3 ("Compaction boundaries are
   intercepted") stops at PreCompact.
   **FIX:** add a section on proactive context-exhaustion recovery (the watchdog watches
   occupancy, recycles with checkpoint, breaker halts loops, `af recovery reset` re-arms).
   This doc is also the natural anchor for the flagship story. (Tier A, this branch.)

4. **README Command Reference omits all five #104 command families.** `af memory`, `af statusline`,
   `af recovery`, `af fidelity`, and `af config fingerprint` are shipped but appear nowhere in the
   README's Command Reference (verified by grep). This is a completeness gap, not a false claim.
   **FIX:** surface the new commands in the Command Reference (grouped: recovery, memory,
   statusline, config integrity). (Tier A, this branch.)

5. **(Soft) Roadmap "reduce fidelity-gate false positives on passive steps (#75)".** #104
   substantially addresses this (turn-scoped evidence; block only on contradiction, not absence),
   but issue #75 is still OPEN, so the roadmap entry is not strictly false.
   **FIX:** confirm #75's status with the operator; if #104 resolved it, move the item from the
   Roadmap into the CHANGELOG. Do NOT claim it fixed until #75 is closed. (Verify-first.)

**Not stale (re-verified this cycle):** "Twenty-four formulas ship" = 24 `.toml` in
`internal/cmd/install_formulas/` ✓; "Ten skills are embedded" = 10 dirs in `.claude/skills/` ✓;
every formula and skill named in the README tables exists ✓.

---

## STORY-WORTHY — candidates for Phase 2

No `flagship_hint` was supplied, so Phase 2 ranks and proposes. Preliminary shortlist:

- **A. Self-recovering agents (context-exhaustion recovery)** — highest user pain (a wedged
  agent burning tokens on a full context window), most 60-second-demonstrable (context-fill bar
  → automatic recycle → resume on the open step), genuinely novel vs. the README comparison
  table's recovery column, and squarely on target phrases (autonomous agents, Claude Code).
  **Strongest flagship candidate.**
- **B. The honesty release (silent-failure elimination)** — "every surface tells you the truth,
  including when it has no data": not-measured≠zero, gate judges block only on contradiction,
  setters reject unknown keys, honest console. A trust/quality story; compelling but more
  abstract and less demoable than A.
- **C. Durable memory vault (agents that remember across teardowns)** — a strong second beat
  that pairs naturally with A ("recover AND remember"); could be the flagship if the operator
  prefers a more concrete, tangible feature over the recovery narrative.

**Framing note:** #104's own title and body already unify these as one arc — *self-recovering,
self-remembering, honest with its operator*. The likely Phase-2 proposal is flagship **A** with
**C** as the supporting beat and **B** as the framing, but the ranking + operator HOLD is Phase 2.
