<!-- DRAFT — cycle-3 flagship (web console screen tour). Operator REORDERED on issue #105:
     show EVERY --web screen, teach each screen's purpose, tie each to a new feature, and make
     this a recurring per-cycle angle. Operator also directed ACTUAL screenshots (Playwright) -
     all 8 below are real captures of the live console on this factory, taken this cycle.
     Written in Glenn's voice. Tier B: YOU publish by pasting into Medium. Your edits are
     canonical - I only fix mechanics after, enumerated. -->
<!-- The italic line under the title goes in Medium's SUBTITLE field, not the body. -->
<!-- 8 real screenshots staged in .marketing/, insert at the [SCREENSHOT] marks:
       1) cycle-af-c967c569-screen-floor.png
       2) cycle-af-c967c569-screen-agentdetail.png
       3) cycle-af-c967c569-screen-sling.png
       4) cycle-af-c967c569-screen-dispatch.png
       5) cycle-af-c967c569-screen-formulas.png
       6) cycle-af-c967c569-screen-telemetry.png
       7) cycle-af-c967c569-screen-settings.png
       8) cycle-af-c967c569-screen-prototypes.png -->

# I run a factory of AI agents. Last time I cracked the door - here's every room.

*A guided tour of the agentfactory web console - every screen a human uses to run autonomous
Claude Code agents, and the feature sitting behind each one*

Last time I wrote about agentfactory I cracked the door on the web console and showed you two
rooms = the Floor and Telemetry. And then a fair number of you asked the obvious question: what
ELSE is in there? Because I'd clearly been hiding something.

Guilty. The console has grown into the actual control room for the factory, and I'd been quietly
shipping features into these screens without ever giving anyone the tour. "Cadence over volume"
is a nice excuse right up until it means weeks of work nobody can see. So this time = the whole
thing. Every screen, what a human actually uses it for, and the new feature sitting behind it.

One setup note that hasn't changed and never will: the console binds to loopback ONLY
(127.0.0.1). The control plane can start and stop agents and rewrite config, so it is
deliberately NOT something you publish to a network. One command brings it up next to your
factory (`quickdocker.sh <your-repo> --web`) and prints a clickable URL. Then you walk in.

## The Floor - where you scan the skyline

[SCREENSHOT 1 - the Floor: cycle-af-c967c569-screen-floor.png]

This is home. Every agent that's running is a lit sign, and the glow IS its honest status -
working, parked at a gate, waiting, needs attention. Under each name now sits the thing I most
wanted last time I couldn't see it: a context reading. That pink card lit up at CONTEXT 22%? That
is the agent that wrote this article, mid-run, and the percentage is how full its context window
is. That number is not decoration - it's wired to something new I'm genuinely proud of.

An agent that fills its context window used to be a dead agent - wedged, improvising, or just
burning tokens on a window too full to hold another instruction. Now the watchdog watches
occupancy the way it watches liveness, and when an agent gets close it recycles it - state
checkpointed - into a fresh session that primes itself back to the exact step it was on. If
recovery keeps failing, a durable breaker HALTS the agent and escalates to me instead of looping
forever (`af recovery reset` re-arms it after I've looked). On the Floor that shows up as a
recovery badge, so an exhausted or halted agent is visibly different from a healthy one. The
skyline doesn't just look pretty - it tells you the truth.

Everything else on the Floor is roster: 26 specialist agents sit dark until you light the ones
you need.

## Agent detail - drill in, then talk to it

[SCREENSHOT 2 - agent detail: cycle-af-c967c569-screen-agentdetail.png]

Click any lit sign and you drop into the agent. This is the same pink one - the marketer - caught
in the act: running formula, current step ("Phase 4: draft the content"), the exact variables it
was slung with (there's the issue id, af-c967c569, this very cycle). None of that lives in the
agent's head where a dead session could lose it - it's read straight off disk.

And on the right: Send Mail. You can message a running agent from the browser and it reads the
mail when it next checks its inbox. No SSH, no tmux, no tailing a log to find out what your agent
is doing. You look, and if you need to steer, you type.

## Sling - put an idle agent to work

[SCREENSHOT 3 - Sling: cycle-af-c967c569-screen-sling.png]

Pick an idle agent on the left, and the console builds its task form for you on the right - out
of that agent's own formula variables. You don't memorize flags. You choose who, the form tells
you what it needs, you tell it what to do. This is `af sling` with a face on it, and it's how you
kick off work without touching a terminal.

## Dispatch - the part that runs without you

[SCREENSHOT 4 - Dispatch: cycle-af-c967c569-screen-dispatch.png]

Slinging is you pushing work in. Dispatch is the factory pulling it in on its own: the dispatcher
watches your repos and routes newly-labeled issues to the right agent automatically. On my
factory right now it's sitting idle with a clean feed (an honest zero - it says "no dispatches
yet," it doesn't invent activity to look busy). Wire it up and this screen becomes the log of
what got routed where, hands-off.

## Formulas - author a workflow in the browser

[SCREENSHOT 5 - Formulas / Foundry Console: cycle-af-c967c569-screen-formulas.png]

This is my favorite screen and, awkwardly, the one my own README's roadmap still listed as a
someday feature until I fixed it this week. It shipped. Every card is one agent formula, and the
frame is COMPUTED from the parsed TOML - legendary lines carry a human gate, epic lines merge
parallel belts, rare lines are wired, common lines run free. 24 formulas in the store, and
"Commission a new line" opens an editor where you author a workflow in the browser. The thing
that turns a SKILL.md into an agent is now something you can build without leaving the tab.

## Telemetry - what it took, honestly

[SCREENSHOT 6 - Telemetry: cycle-af-c967c569-screen-telemetry.png]

You've seen this one, but look at what's in the table: the actual run that produced this article.
Load-context closed in 14ms, setting up the branch took 89 seconds, verifying the tests on main
took 37. The reads and the writes are the job; the fast steps are the guardrails.

But the part I want you to notice is the token column. On this backend, per-step tokens aren't
available - and instead of painting a fake number to look complete, the console SAYS so, in plain
language, right in the row: "a known gap, not a fault in this factory or in the console."
Degradation reported as data. That honesty runs through the whole release - "not measured" is
never dressed up as "zero," anywhere.

## Settings - config you can trust

[SCREENSHOT 7 - Settings: cycle-af-c967c569-screen-settings.png]

Every config document under `.agentfactory/` gets a panel here - dispatch routes, startup, mail,
statusline, model pins. Two things I care about are printed right on the screen. First: each
panel saves through `af`, the single validator and writer - the browser never hand-edits your
files. Second, and this one's the fix I'm happiest about: "keys this console has never heard of
ride through untouched." Older me could wipe a config key just by saving a form that didn't know
about it. Not anymore - unknown keys survive the round trip, and every save carries a write
precondition so two edits can't silently clobber each other.

## Prototypes - review what the agents built

[SCREENSHOT 8 - Prototypes: cycle-af-c967c569-screen-prototypes.png]

When a design agent builds something, it lands here - in an isolated viewer - and you leave
feedback the agent will actually honor on its next pass. Mine's empty in the shot (no prototype
built on this factory yet - again, it says so rather than faking one), but this is the review
loop: agents build, you look, you steer, they iterate.

## The thread

Eight screens, one idea: the human runs the factory, the agents run the work, and you should be
able to SEE the difference and act on it - recover an agent, mail it, sling it, author its
workflow, read what it cost. All of it lives outside any single agent's head, so a dead session
never takes your visibility with it.

It's on GitHub, Go, AGPL-3.0: github.com/stempeck/agentfactory. If you're running agents on long
workflows - do you have a control room, or are you closing the laptop and hoping like I used to?
Happy to help if you're stuck.

Learn it, Live it, Share it!

<!-- Alt titles if this one isn't your taste:
     - "Every screen in my AI agent factory's control room"
     - "I gave my factory of AI agents a control room. Here's the whole thing."
     - "One loopback tab runs my whole factory of AI agents. Here's every screen." -->

## Operator Decision
- Decision: ______
  (READY to approve for publishing; EDITED — I changed the text, re-check mechanics only;
   SKIP to drop this piece this cycle.)
- Resolution: ______
