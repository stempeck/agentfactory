<!-- DRAFT v2 — cycle-2 flagship, REVAMPED 2026-08-03 per operator direction on issue #94:
     human-operator perspective (not the agent narrating itself), the WEB CONSOLE as the lead
     with telemetry as the exciting new part, screenshots, BOTH time and tokens, broader
     feature coverage, cut the bland "what's rough" dump, and fix the tokens claim (tokens +
     cost ARE recorded). Written in Glenn's voice. Tier B: you publish by pasting into Medium.
     Your edits are canonical - I only fix mechanics after. -->
<!-- The italic line under the title goes in Medium's SUBTITLE field, not the body. -->
<!-- Two screenshots staged in .marketing/, insert at the [SCREENSHOT] marks:
       1) cycle-af-50cdad6b-console-floor-diagram.png   (the Floor view)
       2) cycle-af-50cdad6b-telemetry-diagram.png       (the Telemetry view) -->

# I run a factory of AI agents. Here's the window into it.

*The agentfactory web console - a loopback control room for your Claude Code agents - and the
telemetry view I just added, so you can finally SEE where an agent's time and tokens go*

Here's a thing nobody tells you about running autonomous agents: the moment they actually work,
you lose sight of them. You dispatch three of them, close the laptop to go make coffee, and now
there are three Claude Code sessions off doing multi-hour work in tmux panes you're not
watching. Are they moving? Stuck at a gate? Burning tokens on a loop? For a long time my honest
answer was "I'll find out when it's done." That's a bad answer.

agentfactory has had a fix for a while and I've barely talked about it = a web console. It's
optional, it's a separate little Go binary, and it binds to loopback ONLY (127.0.0.1 - the
control plane can start and stop agents and edit config, so it is deliberately NOT something
you publish to a network). One command brings it up next to your factory
(`quickdocker.sh <your-repo> --web`) and it prints a clickable URL. Then you get this:

[SCREENSHOT 1 - the Floor view: cycle-af-50cdad6b-console-floor-diagram.png]

This is the Floor. Every agent that's running is a lit sign, and the glow IS its honest status -
working, parked at a gate, waiting, needs attention. You scan the skyline, you spot the one
that's stuck, you click into it. From the browser you can read exactly what it's doing, send it
mail, or sling it a fresh task (the task form builds itself from the agent's formula variables).
No SSH, no tailing logs, no guessing. The agent lit up pink in that shot? That's the one that
wrote this article - I'll come back to it.

What I actually want to talk about is the new tab: Telemetry.

Doesn't measure what you can't see, and I couldn't see the one number I most wanted - where does
the time go? People kept asking me a fair question about the fidelity gate (the interlock that
catches an agent when it drifts off its steps): doesn't grading every step slow the whole thing
down? I had a hunch. I didn't have a number. So I built telemetry into af - it records a window
around every formula step for every agent - and I turned it on and opened the console while the
agent that markets this repo ran its whole cycle. Here is its actual run:

[SCREENSHOT 2 - the Telemetry view: cycle-af-50cdad6b-telemetry-diagram.png]

Look at the durations. GATE 1 - the gate everyone worries about - closed in 12 MILLISECONDS.
Because that kind of gate doesn't DO the work, it just refuses to advance until the work exists.
The real cost was Phase 1 (auditing the repo) at 288 seconds, and Phase 3 (writing the docs) at
247. The reads and writes are the job. The gate is the guardrail, and the guardrail is
basically free. I'd have bet the other way from memory and been wrong - which is the whole point
of measuring instead of guessing.

And it's not just time. The console tracks token usage and dollar cost per agent too, so you can
answer "what is this thing costing me" with a number instead of a shrug. One honest detail I'm
actually proud of: when a backend can't attribute tokens down to the individual step, the
console SAYS so, in plain language, right in the table - it will not paint a fake per-step
number just to look complete. Degradation gets reported as data. That honesty is the feature.

While I had the hood up, here's the rest of what shipped that I've never written about - because
"cadence over volume" is no excuse for leaving weeks of work invisible:

 - The console is more than a dashboard - you can author formulas in the browser, watch the
   dispatcher, review the design prototypes your agents build, and manage dispatch routes and
   startup settings. It's the operator's cockpit, not a read-only status page.
 - Multi-provider agents. Your specialists don't all have to run on Claude - af can point a
   formula at an OpenAI model through a gateway (there are `gpt-` review and root-cause
   formulas that do exactly this), with a fitness check before a non-default model flies.
 - Operator-only factory teardown. An agent can stop its own session, but it can no longer tear
   down the whole floor - that switch belongs to the human. (You can see those big red
   START/SHUT DOWN controls in the shots - they're mine, not the agents'.)
 - Self-improving agents. An optional hook lets an agent fold what it learned on a run back into
   its own formula, so next time it doesn't repeat the mistake.

The thread through all of it is the same one this whole project keeps pulling: the human runs
the factory, the agents run the work, and you should be able to SEE the difference. The Floor
tells you what's happening. Telemetry tells you what it took. Both live outside any single
agent's head, so a dead session doesn't take your visibility with it.

The code is on GitHub: github.com/stempeck/agentfactory. If you're running agents on long
workflows - do you actually have a window into them, or are you closing the laptop and hoping
like I was? Happy to help if you're stuck.

Learn it, Live it, Share it!

<!-- Alt titles if this one isn't your taste:
     - "The control room I never showed anyone"
     - "Your agents are working. Can you see what they're doing - and what they cost?" -->

## Operator Decision
- Decision: READY
  (READY to approve for publishing; EDITED — I changed the text, re-check mechanics only;
   SKIP to drop this piece this cycle.)
- Resolution: Operator (stempeck) approved the Medium article as written via issue #94 on
  2026-08-03 ("I'm good with the Medium Article: APPROVE"). No text changes requested.
