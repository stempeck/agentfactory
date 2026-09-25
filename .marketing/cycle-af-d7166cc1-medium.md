<!-- DRAFT (rev.2) — cycle-af-d7166cc1 flagship (Token economics, #111). Long-form / Medium.
     Rewritten 2026-09-22 against your PR #114 inline review (10 comments). Calibration now: your
     review comments themselves (they are the newest, most specific voice signal we have) + prior
     published finals. Tier B: YOU publish by pasting into Medium yourself — I never post. Your
     edits are canonical; if you change the text I re-check mechanics only and enumerate every change.
     Every command/flag/number below was run live on 2026-09-22 or verified against source (GATE-3). -->
<!-- The italic line under the title goes in Medium's SUBTITLE field, not the body (it is also
     Google's meta description — it carries the search phrases). -->
<!-- Optional visual: a see→bound→prove diagram is staged at cycle-af-d7166cc1-diagram.png.
     Insert it at the [DIAGRAM] mark if you want it; drop the mark if you don't. -->
<!-- The three fenced blocks marked "REAL OUTPUT" are live captures from this machine on
     2026-09-22 — real, not mocked. Say the word (or approve the copy) and I'll render them as
     clean PNG figures for Medium; I didn't render images of text you might still change. -->

# Knowing what a run costs and whether my "fixes" actually help.

*Token economics for autonomous Claude Code agents: see what a run generates, bound what a sub-agent can spend, and prove a change helped. Off by default.*

You hand an autonomous agent a context window and walk away. It fills it. Then the questions start.
What did that run actually cost? Was that normal for this step? And the formula tweak I shipped last
week to make it leaner - did it work, or did I just tell myself it did?

This release answers all three, and it does it with three switches you turn ON when you want the
numbers and OFF when you don't. The factory never gets to quietly decide to throttle itself - that
call stays yours:

```
af telemetry on      # collect what each run costs
af tokenomics on     # spend those numbers - bound a run before it overspends
af improvement on    # let a finished agent propose a leaner version of itself
```

Here is what each one buys a human operator.

## `af telemetry on` - see what a run actually cost

Turn on telemetry and every closed step writes down what it spent - the real figures, not a guess.
Run `af telemetry report` and you get a per-step ledger: how much of the context window each step was
holding when it closed, and how many tokens it added. Live output from this repo:

```
[REAL OUTPUT - af telemetry report, live 2026-09-22]
STEP                                                CTX_PCT   DELTA (tokens)   OCCUPANCY
Phase 1: Audit surfaces and mine development         12%          20,811       under-bound
GATE 1: Audit checklist                              12%           4,651       under-bound
Phase 2: Rank the untold list and propose the story  13%           3,657       under-bound
```

That is the number I never used to have. Not "the run finished" - but "Phase 1 alone added twenty
thousand tokens, and the whole run never crossed 13% of the window." Now I can point at the expensive
step instead of guessing which one it was. And `af telemetry usage` goes one further: it puts a real
dollar cost on each session, per model, so "what did that cost?" stops being rhetorical.

## `af tokenomics on` - spend those numbers before a run overspends

Telemetry that only reports is a receipt. `af tokenomics on` puts the numbers to work while the run is
still happening, and it exists for one reason: on a shared or local backend, one greedy sub-agent can
starve the orchestrator that launched it, and you find out only when something crashes halfway through
a workflow.

So it caps that. You set the backend's token pool once (`AF_BACKEND_POOL_TOKENS`); after that, when an
agent tries to launch a sub-agent that would oversubscribe the pool, the launch is refused BEFORE it
starts - and the agent is handed the arithmetic through the same permission prompt it already
understands, not a mystery stall. On a managed cloud backend, where there's no local pool to blow, it
stays out of your way.

The part you feel over time: adaptive effort reads each step's own history and dials down the effort on
steps that have always generated more than they needed - never above the ceiling you set. A workflow
you run every week gets cheaper as the factory learns which steps were overspending, and you never
touched the formula to make that happen.

```
[REAL OUTPUT - af tokenomics status, live 2026-09-22]
tokenomics: on
mechanisms: budget=on thrift=on dispatch=on interview=on effort=on escalate=off
admission margin: 16% (a step is admitted up to 84% projected occupancy)
interview: eligible=2  fired=3  advise=3      <- ran 3 times this cycle, all advisory
self-test: live - every gate this surface depends on is on
```

[DIAGRAM - see→bound→prove: cycle-af-d7166cc1-diagram.png]

## `af telemetry compare` - prove the fix worked, or don't claim it

This is the command I reach for after every optimization, because it's the ONLY one in the system
allowed to say a change helped. You give it two arms of runs - before your change and after - and it
returns a verdict.

The reason I trust it: if the two arms weren't actually comparable - different inputs, mixed conditions
- it does not manufacture a win to make me feel good about last week's work. It VOIDS the comparison
and tells me why. A tool that would rather say "I can't prove this" than hand me a flattering number is
the only kind I'll let near a decision about what to ship. "Did my optimization help?" finally has an
answer that isn't me squinting at two runs.

## `af improvement on` - let the run improve itself

The last switch closes the loop. With the improvement hook on, an agent that finishes a dispatched job
proposes an edit to its OWN formula based on what the run just taught it, then hands you a validated
verdict by mail - changed or unchanged, passed or FAILED. Nothing lands until you promote it. The
factory gets to say "here's how I'd run this leaner next time," and you keep the final say over every
line of every formula.

## Get it

It's on GitHub, Go, AGPL-3.0: github.com/stempeck/agentfactory. Every knob, every default, and the
exact contract for each switch is in `USING_TOKENOMICS.md`.

Turn on telemetry for one run this week and read the receipt. If you run agents on long workflows,
that's the first time "what did that cost, and was my fix real?" has an answer you can point at instead
of a shrug.

Learn it, Live it, Share it!

<!-- Alt titles if this one isn't your taste:
     - "Three switches that tell you what your AI agents cost - and whether your fixes are real."
     - "I stopped guessing what my agent runs cost. Here's the receipt." -->

## Changes applied from your PR #114 review (2026-09-21/22)
Each of your inline comments, and what I did. Your text is canonical; where you gave exact wording I
used it verbatim; the rest I rewrote to your direction and am flagging here so you can audit me.

1. **Title (was "In my factory of AI agents, I finally know...")** — you said we've over-used "in my
   factory of agents." New title is your exact wording: *Knowing what a run costs and whether my
   "fixes" actually help.*
2. **Subtitle** — removed "honest about what it can't measure," per your note. Kept "Off by default"
   (it's a real thing operators care about - no surprise throttling).
3. **Null / "honest un-measurables" (old See section)** — deleted entirely. No more "null vs zero /
   I won't pretend" pitch. You're right: "we did nothing and were honest about it" is not a win.
4. **band / rebuild** — dropped as headline verbs. The article now leads with the switches operators
   actually type - `af telemetry on`, `af tokenomics on`, `af improvement on` - and the loop: collect
   the numbers, spend them to run leaner, prove a change helped. (`compare` earns a section because
   it's the switch you reach for to answer "did my fix work?")
5. **Bound "the part I'm proudest of"** — cut. The section now says WHY it exists (a greedy sub-agent
   starving the orchestrator on a shared/local backend) and the operator benefit (predictable spend, no
   mid-workflow crash), not how I feel about an env var.
6. **Prove "honesty valve / my favorite thing"** — cut. No randomized happiness; just what the verb
   does and why VOID-not-fudge is the reason to trust it.
7. **"What it won't do (on purpose)" section** — removed. The two genuinely operator-relevant facts
   are folded in as benefits where they belong: "stays out of your way on cloud" (Bound) and "nothing
   lands until you promote it" (improvement). No more writing about non-features.
8. **"Screenshots of the amazing outcomes / SHOW IT"** — added two REAL live captures (`af telemetry
   report` per-step occupancy + token delta; `af tokenomics status` mechanisms firing). Both are real
   output from this machine on 2026-09-22, not mocks. On your OK I'll render them as clean PNGs for
   Medium - I held off rendering images of copy you might still change. A dollar-cost capture from
   `af telemetry usage` is available too; I left it out of the fixed copy because its query window
   shifts per call, so I won't pin a number I can't reproduce - happy to add a live screenshot at
   publish time.
9. **Overall "AI on repeat / no idea what we released"** — restructured the whole piece around the
   three switches so a reader finishes knowing exactly what shipped and what each switch does for them.

Open question for you: is `compare` worth its own section, or fold it into telemetry? Your call.

## Operator Decision
- Decision: **READY, APPROVED** — operator stempeck on PR #114, comment 5776575505, 2026-09-22T12:33Z
  (verified directly via gh, not a relay). No inline edits on rev.2; approved as written.
  (READY to approve for publishing; EDITED — I changed the text, re-check mechanics only;
   SKIP to drop this piece this cycle.)
- Notes: Two open questions (compare section; keep diagram) were left to my discretion — kept both as
  drafted. Tier B: still operator-published; this READY approves the COPY, it does not post anything.
