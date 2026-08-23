# Cycle-3 Report — stempeck/agentfactory (bead af-c967c569)

*2026-08-23. Flagship: a guided tour of the agentfactory web console — every `--web` screen,
its purpose, and the #104-wave feature it surfaces. Boundary: v0.2.0.*

## Summary

Audited development since v0.2.0, found the #104 feature wave (self-recovery, durable memory,
honest surfaces, browser formula authoring) largely untold and five stale/incomplete README
surfaces. Ranked and proposed **self-recovering agents** as the flagship; the operator
**REORDERED** on issue [#105](https://github.com/stempeck/agentfactory/issues/105) to a **guided
tour of every `--web` console screen** — teaching each screen's purpose while tying it to a new
feature, and establishing this console-screen angle as a recurring per-cycle theme. Shipped a
Tier A docs refresh + cut **v0.3.0**; the operator published a Medium article (8 real Playwright
screenshots of the live console) and pointed the repo homepage at it. Every published page was
verified (Phase 6 PASS: 8 images survived the paste, no raw markdown, subtitle + 5 topics in
place). LinkedIn was deferred to next cycle by operator decision.

## Shipped autonomously (Tier A — merged via PR #107)

- **README** (+18/−7): surfaced the five #104 command families the Command Reference omitted
  (`af memory`, `af statusline`, `af recovery`, `af fidelity`, `af config fingerprint`); **fixed
  the Roadmap line that still listed "formula authoring in the browser" as future** — it ships,
  and it's now a flagship screen; kept the recovery narrative honest.
- **CHANGELOG** (+66): added the **v0.3.0** section for the #104 wave (self-recovery, durable
  memory, honest surfaces) — the log previously stopped at v0.2.0.
- **docs/recovery-model.md** (+41): added the context-exhaustion recovery section that anchors
  the Floor screen's recovery-badge story.
- **Release:** [v0.3.0 — self-recovery, durable memory & honest surfaces](https://github.com/stempeck/agentfactory/releases/tag/v0.3.0),
  themed notes, cut from merged main. The CHANGELOG heading is now backed by a real tag + Release.
- Gates: GATE-1 (audit), GATE-3 (claims verified against source, one file:line fix), SELF-REVIEW,
  SELF-VERIFY (one documented HOLD), PHASE-6 (published-page verification) — all PASS.

## Published by the operator (Tier B — staged by agent, published by Glenn)

- **Medium:** "I run a factory of AI agents. Last time I cracked the door — here's every room." —
  https://medium.com/@glennstempeck/in-my-factory-of-ai-agents-last-time-i-cracked-the-door-heres-every-room-a04f14babef0
  A guided tour of all eight console screens (Floor, Agent detail, Sling, Dispatch, Formulas,
  Telemetry, Settings, Prototypes), each shown as a **real Playwright screenshot of this factory's
  live console** and tied to a shipped feature. Repo homepage now points at it.

Delivery mechanics: captured 8 real console screenshots via Playwright (operator directed actual
captures, not mockups); rendered the article to a self-contained rich-HTML paste vehicle with all
8 images embedded as base64 data URIs so they survived the copy-paste past Medium's markdown-eating.
Post-publish QA against the screenshots caught two text↔image number mismatches on the Floor shot;
operator took the `22%`→`23%` fix and kept "26" as shipped. The paste vehicle was regenerable
scaffolding and was deleted in Phase 6 cleanup once the article was live.

## Skipped / not done, and why

- **LinkedIn (short-form): SKIPPED** by operator decision (#108: "I'll post the next one to
  LinkedIn. Not this one."). The draft `cycle-af-c967c569-linkedin.md` stays ready with the live
  Medium URL already in its footer, so it can be reused or refreshed next cycle. No ledger row is
  claimed because nothing was posted.
- **No new issues filed:** the audit surfaced no genuine new gaps — the #104 wave was shipped and
  green; existing issues already track known work. Did not invent issues.
- **Self-recovery was NOT led as the flagship:** the agent's top-ranked pick was folded into the
  Floor screen at the operator's direction rather than told head-on; it stays the strongest
  candidate for a dedicated next-cycle story.

## Operator-click items (no API reaches these)

- **None outstanding.** The two surfaces without an API (repo social-preview image, profile pins)
  were resolved in cycle-1 and unchanged. Homepage was set via `gh repo edit --homepage` (API,
  done). LinkedIn is a deferred draft, not a click item.
- *Carried note (CLA):* commits here are authored `agentfactory <dev@agentfactory.local>`, which
  the CLA assistant can't match, so a Tier A PR needs an operator admin-merge or a CLA-signed
  author identity — PR #107 was cleared by the operator's merge, as expected.

## Voice calibration learned this cycle

- The operator wants **real screenshots, not mockups** — "show EVERY screen in the `--web`
  interface … display the stunning screens." Playwright captures of the live console are now the
  expected asset for console-tour pieces. Recorded in the runbook.
- The operator asked to make the **console-screen tour a recurring per-cycle angle**: each cycle
  takes whatever shipped and teaches it through the screen a human uses for that purpose. This is
  now the default framing to propose first (still subject to the story gate).
- On number-vs-image fidelity: when the piece's whole point is "the number tells the truth," any
  text↔screenshot mismatch is a publish-blocker to flag; the operator adjudicates which side moves.

## Top 3 candidates for next cycle

1. **Self-recovering agents, told head-on** — context-exhaustion recycle → resume on the open
   step. Highest user pain, most 60-second-demoable (context-fill bar → automatic recycle →
   resume), genuinely novel. Ranked #1 this cycle; deferred into the Floor screen. Overdue for
   its own story.
2. **Durable memory vault** — agents that remember across teardowns/worktrees. Pairs naturally
   with (1) as "recover AND remember"; concrete and tangible. Never told.
3. **The dogfooding story, told head-on** — an agent that runs its own repo's marketing cycle
   (this very process). Surfaced obliquely again; a unique, shareable narrative that would land
   well after two cycles of console tours built the audience.

## Recrawl check (~2 weeks, ≈2026-09-06)

- Web search `"Glenn Stempeck" agentfactory` and `"I run a factory of AI agents"` — expect **both**
  Medium articles (cycle-2 window + this cycle's "every room") + the repo to surface.
- `site:medium.com glennstempeck` — expect three articles now (launch, window, every-room).
- Spot-check GitHub **topic pages** (`claude-code`, `ai-agents`, `agents`) still list the repo.
- Confirm the repo **homepage** still resolves to this cycle's Medium article (the
  visibility-health workflow alarms on drift — don't duplicate it, just confirm no drift).
- Confirm the **v0.3.0 release** page renders with themed notes and the tag resolves.
