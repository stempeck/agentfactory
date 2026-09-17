<!-- DRAFT — cycle-af-d7166cc1 flagship (Token economics, #111). Long-form / Medium.
     Written in Glenn's voice (calibration: cycle-af-c967c569-medium.md published final,
     cycle-af-50cdad6b finals, live articles). Tier B: YOU publish by pasting into Medium
     yourself — I never post. Your edits are canonical; if you change the text I re-check
     mechanics only and enumerate every change. Every command/flag/limit below was verified
     against source this cycle (see cycle-af-d7166cc1-log.md GATE-3). -->
<!-- The italic line under the title goes in Medium's SUBTITLE field, not the body (it is also
     Google's meta description — it carries the search phrases). -->
<!-- Optional visual: a see→bound→prove diagram is staged at cycle-af-d7166cc1-diagram.png.
     Insert it at the [DIAGRAM] mark if you want it; drop the mark if you don't. -->

# In my factory of AI agents, I finally know what a run costs - and whether my "fixes" actually help.

*Token economics for autonomous Claude Code agents: see what a run generates, bound what a sub-agent can spend, and prove a change helped. Off by default, honest about what it can't measure.*

You hand an autonomous agent a context window and walk away. It fills it. And for the longest
time, that was the end of what I actually knew.

I couldn't tell you what a run GENERATED to get where it got. I couldn't stop one greedy
sub-agent from starving the orchestrator that launched it on a cramped local backend. And when I
"fixed" a formula to make it leaner, I couldn't prove the fix did anything - I squinted at two
runs and told myself a story I wanted to believe. You gave an agent a window, it filled it, and
you had no instrument panel at all.

This release is me refusing to squint. The factory can now SEE, BOUND, and PROVE what a run costs
its own context window. It's off by default - you turn it on when you want the numbers, and the
factory never gets to quietly decide to throttle itself.

## See - what a run actually generated

Every closed step now records what it GENERATED - output, thinking, peak occupancy, and what its
sub-agents spent - right next to how long it took. And the figure it never had before is honest:
if something wasn't measured, it reads `null`, not `0`. A zero means "I measured, and it was
zero." A null means "I don't know, and I won't pretend." That distinction is the whole ballgame
when you're deciding whether to trust a number.

Then `af telemetry band` takes that history and tells you whether a step's latest run sits inside
its own learned range - and it PRINTS the band it's judging against, so the verdict isn't a black
box you're asked to trust. `af telemetry rebuild` keeps that learned range alive even after the
raw records rotate away.

## Bound - a spend limit an agent can't argue with

This is the part I'm proudest of. On a shared backend, a model profile can declare its pool
(`AF_BACKEND_POOL_TOKENS`). When an agent tries to launch a sub-agent that would oversubscribe
that pool, the launch is refused BEFORE it starts - and the agent is shown the arithmetic,
through the same permission channel it already understands, never a mystery crash or a silent
non-zero exit. A floor (`AF_BACKEND_CHILD_FLOOR_TOKENS`) protects the first child near the
ceiling; a switch (`AF_DISABLE_PARALLEL_SUBAGENTS`) forces one-at-a-time when you want it. And it
fails OPEN: if it can't resolve the numbers, it gets out of the way rather than blocking your
work over its own confusion.

Beside it, adaptive effort quietly trims the effort level of a step that has a history of
generating more than it needs - never above the ceiling you set, and bounded so a run can't burn
itself relaunching. And session-start context budgets mean a giant step body can no longer shove
your mail out of the window: formula context, mail, and memory each get their own room, and every
message reaches the session once.

[DIAGRAM - see→bound→prove: cycle-af-d7166cc1-diagram.png]

## Prove - the one verb allowed to say "it worked"

Here's the honesty valve, and it's my favorite thing in the release. `af telemetry compare` is
the ONLY command in the whole system permitted to claim a change helped. You hand it two arms of
runs - before and after - and it returns a verdict. But if the two arms weren't actually
comparable (different inputs, mixed conditions), it does not fudge a win to please you. It VOIDS
the comparison and tells you why. A measurement tool that would rather say "I can't prove this"
than hand you a flattering lie is the only kind I'll trust near a decision.

## What it won't do (on purpose)

Two honest limits, told plainly, because a maturity story that hides its edges isn't one:

- The bounding half is structurally INERT on any profile that declares no backend pool - which is
  every cloud profile. No pool, no ceiling, no refusal; on a managed cloud backend this whole arm
  simply never fires. `af tokenomics status` will tell you exactly that, by name, rather than
  leaving you to guess whether it's working.
- The policy surface is off by default, and turning it on is an operator-only move. An agent
  can't arm its own throttle, and - because self-edits from the improvement loop are checked - a
  formula can't buy itself more tokens by quietly deleting one of its own gates.

## Get it

It's on GitHub, Go, AGPL-3.0: github.com/stempeck/agentfactory. The full model - every knob,
every failure mode, every "here's why it's inert" - is in `USING_TOKENOMICS.md`.

If you're running autonomous agents on long workflows and you've ever closed the laptop wondering
what they were spending in there - this is the instrument panel I wish I'd had a year ago. Can you
prove your last optimization actually worked? Happy to help if you're stuck.

Learn it, Live it, Share it!

<!-- Alt titles if this one isn't your taste:
     - "I run a factory of AI agents. I finally gave it a fuel gauge."
     - "In my factory of AI agents, 'did that fix help?' used to be unanswerable. Not anymore."
     - "Token economics for autonomous agents: see it, bound it, prove it - or void the claim." -->

## Operator Decision
- Decision: ______
  (READY to approve for publishing; EDITED — I changed the text, re-check mechanics only;
   SKIP to drop this piece this cycle.)
- Notes: ______
  (Anything you want reframed, cut, or dialed up/down. If you rewrite any passage, your text
   becomes canonical and the new voice-calibration source - I only fix mechanics after, enumerated.)
