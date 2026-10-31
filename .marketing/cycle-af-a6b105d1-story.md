<!-- DRAFT — cycle af-a6b105d1 story proposal. Operator reviews on GitHub (issue/PR), not in this worktree. -->
# Cycle af-a6b105d1 — Story Proposal

`flagship_hint` was empty → ranked by the runbook's four criteria. No operator pre-pick.

## Ranking (NEW / story-worthy list)

| Rank | Candidate | (a) User pain killed | (b) 60-sec demo | (c) Fit to target phrases | (d) Audience reach | Notes |
|---|---|---|---|---|---|---|
| **1** | **Token economics (#111)** — `af telemetry on/band/compare`, `af tokenomics on/status`, `--input-digest` | **HIGH** — "runs blow the budget; I can't see what a run costs; I can't prove a change helped." Everyone pays for tokens. | **HIGH** — live output: `af telemetry compare` (did it help?), `af telemetry band`, `af tokenomics status`. Serves "SHOW IT." | MED-HIGH — autonomous agents / agentic workflows running leaner. | **HIGH** — cost + observability is universal for Claude Code practitioners. | Ledger #1; **closes the standing v0.4.0 release gap**; Tier A already written (#114). Drafted once, **never published**. |
| 2 | Bootstrap convergence / supply-chain reproducibility (#124) | MED-HIGH — "agents behave differently across containers; version drift broke a run; I don't know what's installed." | HIGH — `quickstart.sh --check` drift report + `convergence.json`. | LOWER — infra/supply-chain, off the core discovery topics. | MED — enterprise/ops lean, narrower than cost. | **Freshest** (merged today `ce8639e5`); never drafted. Strong fresh alternative. |
| 3 | Dogfooding — an agent that runs its own repo's marketing cycle | LOW-MED (credibility/wow, not a pain-killer) | MED | HIGH (meta: autonomous agents) | HIGH if framed well; risky without operator's voice | Standing backlog (ledger #2); not a feature that shipped this cycle. |
| 4 | Durable memory vault (`af memory`) | MED | MED | MED | MED | Standing backlog (ledger #3); not new this cycle. |

## ✅ RESOLVED (2026-10-31) — operator REORDERED the flagship to the SECURITY story (#124)

The operator reviewed this proposal on issue #125 and **rejected Token economics** ("played-out
and old; we've already talked about tokenomics and efficiency") in favor of the **security story
built on bootstrap convergence (#124)**. The flagship for this cycle is therefore the one offered
below as the "fresh alternative."

**Flagship (operator-selected): Bootstrap convergence → the security posture of agentfactory (#124).**

**Operator's framing (canonical):** AgentFactory is moving from a "factory of agents" to **"the
most secure way you can run multi-agent orchestration."** The story: digest-verified convergence,
pinning of CVE-free modules and versions, and a plugin surface that links security products
(DefenseClaw named; "others to come") — delivering security **and** visibility, not just token
economics, toward optimizing for quality and security when building with agents.

**Verification discipline (unchanged):** every capability named above enters public docs only
after the Claim Verification Map proves it against source. The plugin-link-to-security-products
capability (DefenseClaw) is treated as claim-to-verify: documented only if it exists in-tree
today, otherwise flagged as roadmap and omitted from feature docs. Nothing is written from the
operator's framing alone.

**Release note for the deliver gate (updated):** #124 (bootstrap convergence) is merged (ce8639e5)
and carries the headline security features; it is unreleased. Whether this cycle's story is a
release boundary (and whether #117/#111 fold into the same tag) remains the operator's call at
the deliver gate — now weighed around a SECURITY release, not v0.4.0/token-economics.

---

## ~~Proposed flagship: Token economics (#111)~~ — SUPERSEDED by operator REORDER (see above)

**Pick: Token economics.** It ranks highest on the pain + demonstrability + reach the runbook
weights, it is the ledger's #1 untold candidate, and it is the one story that **closes the open
v0.4.0 release gap** (the `## v0.4.0 — 2026-09-17` CHANGELOG section is written but never tagged).
It is also the most "SHOW IT"-friendly story we have: the whole arc is real command output —
turn it on, read the band, run `compare`, prove a change paid for itself.

**Angle (not final copy — drafted at Phase 4 in your voice):** you run agents; agents cost
tokens; until now you were flying the meter blind. `af telemetry on` starts recording; `af
telemetry band` judges each step against what it has historically cost; `af tokenomics on`
bounds a run before it overruns (capacity) and flags steps that generate more than their
baseline (efficiency); `af telemetry compare` is the only verb that will tell you a change
actually helped, over two arms of runs. Lead with the switches and the outcome; show the output.

**Supporting Tier A this cycle (regardless of flagship):**
- Add the missing **CHANGELOG entry for #124** (bootstrap convergence) — S1 in the audit.
- If token economics is the flagship: add **`af tokenomics on/off/status`** to the README command
  reference (S2) so the Bound half is documented, not just `af telemetry`.

**Release note for the deliver gate:** cutting **v0.4.0** with token economics as the headline
closes the standing gap; #117 (plugins/gateway) and #124 (convergence) are also unreleased and
could fold into that tag or wait for v0.5.0. That is your call at the deliver gate — flagged here
so the decision isn't a surprise.

## Fresh alternative you may prefer: Bootstrap convergence (#124)

If you'd rather story what **just landed today** than a carryover, #124 is the pick: "every
container runs exactly the versions you pinned, digest-verified, or the build fails — and CI
tells you daily when a pinned package gets a new advisory." Fresh, demonstrable, never drafted.
Say so in the decision and I'll rebuild the proposal around it.

## If nothing ranks

Not the case this cycle — two genuine, verified, untold features are available. No story is being
invented to fill a slot.

---

## Operator Decision
- Decision: REORDER → Bootstrap convergence / security (#124) as flagship; Token economics (#111) REJECTED.
  (APPROVE to proceed with the pick as written; REORDER: <feature> to swap the flagship;
   END-CYCLE to stop after an audit-only report. Leave blank = not yet decided.)
- Source: GitHub issue #125 comment by `stempeck` (OWNER), 2026-10-31T02:20:16Z
  (https://github.com/stempeck/agentfactory/issues/125#issuecomment-6537715241). Verified live
  against `gh issue view 125`; author matches runbook operator. Manager relay af-041bdf86 confirmed.
- Operator's words (VERBATIM — canonical, do not smooth):
  > REORDER - "You run agents; agents cost tokens; until now you flew the meter blind.", this
  > message is played-out and old. We've already talked about tokenomics and efficiency. There is
  > so much more to agentfactory at this point we can certainly find some new content to talk
  > about. The security features that were introduced via "bootstrap convergance" is a better
  > example.
  >
  > AgentFactory is moving from a "factory of agents" to "the most secure way you can run
  > multi-agent orchestration", how about that!? We are introducing the convergance, pinning of
  > CVE-free modules and versions and plugins that can link products like DefenseClaw and (others
  > to come) to introduce a high level of security AND visibility beyond tokenomics toward
  > optimizing for quality and security when building with agents.
- Notes: New flagship = the SECURITY story on bootstrap convergence (#124). Angle the operator set:
  "factory of agents" → "the most secure way to run multi-agent orchestration" — digest-verified
  convergence, pinning of CVE-free module/version pins, and the plugin surface that links security
  products (DefenseClaw named; "others to come"). Security AND visibility, not just tokenomics.
  EVERY claim still runs through the Claim Verification Map before it enters public docs — the
  DefenseClaw/plugin-link capability in particular is verified-or-omitted, never assumed from the
  operator's framing (operator spells it "convergance"/"DefenseClaw"; docs use correct spelling).
