<!-- Cycle story proposal — marketing-cycle af-91fe0795 — 2026-10-05 -->
# Cycle af-91fe0795 — Story Proposal

**Assignment signal.** `flagship_hint` was empty, but the assignment bead's body is the URL
of **PR #117**. That is the operator naming the subject of this cycle, so the flagship is
drawn from #117's three facets (plugins, ChatGPT-subscription gateway, stale-config
fidelity). Token economics (v0.4.0, #111) is still untold but stays in the backlog — one
flagship per cycle; cadence beats volume.

## Ranking (runbook criteria, 1–5)

| Candidate (all #117) | (a) pain killed | (b) 60-sec demo | (c) phrase fit | (d) reach | Total |
|---|:--:|:--:|:--:|:--:|:--:|
| **Plugins — third-party agents & tools, safely** | 5 | 4 | 5 | 4 | **18** |
| ChatGPT-subscription gateway | 4 | 5 | 3 | 5 | 17 |
| Relaunches never run on stale config | 5 | 2 | 4 | 3 | 14 |

Scoring notes:
- **Plugins.** Pain=5: before #117 an agent could enter only by being hand-copied into the
  store, was then trusted wholesale, and was deleted on the next redeploy; tools had no way
  in at all. Demo=4: `af plugin list → install → verify` (consent + provenance + drift) is a
  clean under-a-minute sequence (needs a sample plugin repo to point at). Fit=5: lands
  squarely on "multi-agent orchestration", "Claude Code", extensibility/agentic. Reach=4:
  a platform-maturity milestone — strong, slightly less of a grab than a money hook.
- **ChatGPT-subscription gateway.** Demo=5 and Reach=5: one flag
  (`--litellm-auth=codex-subscription`) with an immediately visible outcome — "run your
  agent factory on the ChatGPT subscription you already pay for" is the most clickable hook
  in the PR. Fit=3: it is a provider/cost feature, a little off the core phrasing. This is
  the obvious operator reorder option.
- **Stale-config fidelity.** Pain=5 (a real correctness bug: reduced effort with no record,
  a stale auth token readable from any tmux window, a build host you removed still used) but
  Demo=2 — a reliability guarantee is hard to show in 60 seconds. Best told as the trust
  close of the arc, not its own set piece.

## Proposed pick

**Flagship: Plugins — "extend your factory with third-party agents and tools, safely."**
One arc, three beats:
1. **Extend it** (flagship) — `af plugin install` with a real trust boundary: consent on
   install, recorded provenance, `af plugin verify` drift detection, refusal of a name that
   would shadow a shipped/`manager`/`supervisor` agent or a role template not built into the
   binary.
2. **Run it on your own ChatGPT subscription** — `--litellm-auth=codex-subscription`, the
   strong second beat / hook.
3. **Trust every relaunch** — relaunches now run on exactly the config you set (effort,
   identity, build host, no leaked token), the quiet reliability close.

**Why Plugins leads over the gateway hook:** it is the headline of the PR title, the biggest
positioning advance (agentfactory becomes an *extensible platform* with a trust model, not
just a CLI), and it carries the target phrases. The gateway is the more clickable single
line and is a legitimate reorder — if the operator prefers to lead on cost, beats 1 and 2
simply swap and the title leads with the subscription.

**Supporting Tier-A refresh (ships regardless of flagship, from the audit's STALE list):**
- S1: README formula count 24 → **26**; add `lineage`, `rapid-soldesign` rows.
- S2: README skills count 12 → **13**; add `architecture-diagram` row.
- S3: add `af plugin` family to README (the flagship's own command surface).
- S4: add `af gateway auth` + `--litellm-auth=codex-subscription` to README/quickstart.
- S5: add a CHANGELOG section for #117.
- S6 (**operator release decision**, deferred to deliver-tier-a): the CHANGELOG already
  carries `## v0.4.0 — 2026-09-17` but no v0.4.0 tag/release exists. Reconcile — e.g. cut
  v0.4.0 (token economics) and v0.5.0 (#117), or fold both into one release. A real release
  gives the Medium article a version anchor.

## If nothing ranked
Not the case this cycle — #117 is a 383-file, +61k-line wave with three distinct
story-worthy facets, none told. (Recorded per the runbook's "never invent a story" rule:
had nothing ranked, this file would say so and the cycle would end at GATE 2 with an
audit-only report.)

---

Reorder options if you don't take the proposed pick: lead with the **ChatGPT-subscription
gateway** hook (`REORDER: gateway`), or pick a flagship outside #117 such as token
economics v0.4.0 (`REORDER: token-economics`). Your reorder is final. If you have a
preference now on the S6 release gap (v0.4.0 CHANGELOG vs no tag), note it — it's formally
decided later at deliver-tier-a.

## Operator Decision
- Decision: APPROVE
  (APPROVE to proceed with the pick as written; REORDER: <feature> to swap the flagship;
   END-CYCLE to stop after an audit-only report. Leave blank = not yet decided.)
- Notes: Operator `stempeck` commented "APPROVE" on issue #118 (2026-10-05T22:14:47Z,
  https://github.com/stempeck/agentfactory/issues/118#issuecomment-6004215814). Flagship =
  Plugins, three-beat arc as written; no reorder. S6 release gap left to deliver-tier-a.
