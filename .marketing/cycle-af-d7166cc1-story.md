<!-- cycle-af-d7166cc1 story proposal — Phase 2. Author: marketing-cycle agent.
     The feedback form at the bottom is for the OPERATOR. Nothing below the form line is
     an instruction to the agent; only the Story-Decision keyword advances the gate. -->
# Cycle af-d7166cc1 — Story proposal

**Operator pre-pick (`flagship_hint`):** *none* — ranked and proposed below.

## Proposed flagship

> **Token economics — see, bound, and prove what a run costs its own context window (#111).**

This is the **only** new development since cycle-3 (v0.3.0); `git log v0.3.0..origin/main` returns
one feature commit (#111) plus cycle-3's own close-out record. It is also, on the merits, a strong
flagship — not a fill-the-slot pick.

## Ranking

Criteria from the runbook's Positioning section: (a) user pain killed, (b) 60-second
demonstrability, (c) fit to target phrases *(multi-agent orchestration, Claude Code, autonomous
agents, agentic workflows)*, (d) reach for the named audience *(Claude Code practitioners; hiring
evaluators reading the operator's name)*.

| Candidate | (a) Pain | (b) Demo | (c) Fit | (d) Reach | Verdict |
|---|---|---|---|---|---|
| **Token economics (#111)** | **High** — "the factory cannot see, bound, or prove what a run costs its own context window" (#110). Every operator running long agents feels context/cost anxiety; nobody could answer "is this run normal? did my change help?" | **High** — `af tokenomics status`, `af telemetry band`, `af telemetry compare` each show real numbers in <60s | **High** — multi-agent orchestration, autonomous agents, Claude Code all land | **High** — universal cost pain; and a maturity signal (observe → bound → prove, honest nulls, a verb that *voids* rather than lies) | **FLAGSHIP** |
| Self-recovering agents, head-on *(backlog)* | High | Med | High | High | Defer — not new this cycle; told obliquely in cycle-3 |
| Durable memory vault, head-on *(backlog)* | Med | Med | Med | Med | Defer — not new this cycle |
| Marketing-cycle dogfooding *(backlog)* | Med | Low | Med | Med | Defer — not new this cycle |

**Nothing new this cycle competes with the flagship** — the standing backlog items are all from
prior cycles. Cadence beats volume: one flagship, told well.

## Rationale

Token economics is the honest maturity story a skeptical reader respects: it is *off by default*,
its capacity arm is *structurally inert on every cloud profile* (told plainly, not hidden), it
reports unmeasured figures as `null` not `0`, and `af telemetry compare` is the only verb allowed to
claim a win — and it *voids* rather than fabricates one when the two arms weren't held constant. The
failure-mode hook writes itself: you gave an agent a window, it filled it, and you had no way to see
what it spent getting there, no way to stop a sub-agent from starving its orchestrator on a cramped
local backend, and no way to prove a "fix" actually helped. Now there is.

## Supporting refresh items (ride the same Tier-A PR — not their own story)

1. **STALE fix** — README skills count "Ten" → "Twelve"; add `/improve-solution`, `/perfeval-agent` rows.
2. **STALE fix** — remove Roadmap lines for #73 (default dispatch) and #75 (gate false positives), both CLOSED/COMPLETED.
3. **GAP fill** — document the new token-economics verbs (`af telemetry band|compare|rebuild`, `af tokenomics status`) in README Observability + Command Reference; link `USING_TOKENOMICS.md`.
4. **CHANGELOG** — add a v0.4.0 entry for token economics.
5. **Release** — cut v0.4.0 at this boundary (main is green) if the operator approves.

## Recommendation

Proceed with **Token economics** as the flagship. HOLD for operator approval below.

---

## Operator Decision
- Decision: APPROVE
  (APPROVE to proceed with the pick as written; REORDER: <feature> to swap the flagship;
   END-CYCLE to stop after an audit-only report. Leave blank = not yet decided.)
- Notes: Operator stempeck (repo OWNER) recorded the decision via GitHub, commenting "APPROVED"
   on issue #112 at 2026-09-17T00:02:23Z
   (https://github.com/stempeck/agentfactory/issues/112#issuecomment-5706340238). Verified
   directly with `gh issue view 112 --json comments`. Proceeding with Token economics as the
   flagship plus the supporting Tier-A refresh items as written.
   Release: operator stempeck CONFIRMED YES to a v0.4.0 release this cycle — issue #112 comment at
   2026-09-17T00:08:28Z, verbatim "Cut a v0.4.0 release this cycle? Yes." (verified via
   `gh issue view 112 --json comments`). Per Tier-A discipline the tag is cut only AFTER the PR
   merges (never on a red main), at the deliver gate (step 17).
