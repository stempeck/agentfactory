# Cycle-2 Story Proposal — stempeck/agentfactory (bead af-50cdad6b)

*Prepared 2026-08-03 by marketing-cycle. `flagship_hint` was empty, so this is a ranked
proposal for your approval. Nothing ships until you write a Decision below.*

*Operator notification: GitHub issue [#93](https://github.com/stempeck/agentfactory/issues/93)
(Gate 2 story-approval). Reply there or write the Decision keyword in the form at the bottom.*

## The pick

**Flagship story: Run telemetry — agent observability for the factory.**
`af telemetry` records per-step latency and token usage across every agent, then renders a
local timing table (`af telemetry report`) and a backend usage query (`af telemetry usage`).
It answers the two questions every operator of autonomous agents actually has: *what are my
agents costing me, and where do they stall?*

**Supporting refresh items (ship regardless of flagship, on the same Tier A PR):**
- Fix the three STALE README claims: `af version` → `af --version`; formula count
  19 → 24; skills list 3 → 10 (all from the audit).
- Document the new commands the README/docs don't mention yet: `af telemetry`, and a
  lighter mention of `af improvement` and multi-provider (`af config models`) so the
  command reference stops trailing the code.
- CHANGELOG entry for the #92 wave.

## Ranking (untold list, scored 1–5 on the runbook's four criteria)

| Candidate | Pain killed | 60-sec demo | Fit to positioning | Reach | Total |
|---|---|---|---|---|---|
| **Run telemetry** | 5 — cost/latency of autonomous agents is invisible today | 5 — `af telemetry report`/`usage` print tables | 5 — observability for autonomous / agentic workflows | 4 | **19** |
| marketing-cycle (dogfooding) | 3 — proves autonomy works | 5 — this cycle *is* the demo | 4 — "I built this" proof | 5 | 17 |
| Web console | 4 — orchestration invisible in a terminal | 4 — Floor view + telemetry banner screenshots | 4 — multi-agent orchestration made visible | 3 | 15 |
| Reliable self-improving agents | 4 — agent repeats mistakes each run | 3 — self-edit is hard to show fast | 4 — agents that improve themselves | 3 | 14 |
| Multi-provider agents | 4 — "it's Claude-only" | 4 — `af config models` + a gpt-* run | 2 — **off-message**: README's honest scope is "Claude Code specifically, not model-agnostic" | 4 | 14 |

## Rationale

- **Telemetry wins on message-fit and demonstrability.** It's the newest headline feature
  (#92), it fills the most universal gap for anyone running autonomous agents, and it demos
  in one command with real numbers — which the runbook's Medium-title rule (a number that
  forces a double-take) can build on, using this repo's own `af telemetry report` output.
- **Multi-provider is deliberately NOT the flagship.** Leading with "now runs on OpenAI
  too" contradicts the README's own honest-scope statement ("agentfactory orchestrates
  Claude Code specifically — it is not a model-agnostic framework"). It ships as a
  documented capability, not the headline — leading with it would undercut the positioning.
- **The dogfooding angle stays a hook, not the headline.** "The agent that wrote this also
  runs the factory" is a great *framing* line for the Tier B pieces, but the voice law makes
  a full meta-story risky (it must read as you, not as an agent describing itself). I'd weave
  it in one sentence, not build the article on it — unless you want it as the flagship instead.
- **Cadence beats volume:** one flagship + the stale-claim cleanup this cycle; web console,
  self-improvement, and the dogfooding story stay top of the backlog for next cycle.

## Operator Decision
- Decision: APPROVE
  (APPROVE to proceed with the pick as written; REORDER: <feature> to swap the flagship;
   END-CYCLE to stop after an audit-only report. Leave blank = not yet decided.)
- Notes: Operator (stempeck) approved via issue #93 comment on 2026-08-03 ("APPROVE").
  Transcribed here by marketing-cycle. Flagship confirmed: af telemetry. Proceeding to Phase 3.
