<!-- DRAFT — Glenn's own hook, mechanics corrected 2026-07-11. Tier B: publish by pasting into LinkedIn yourself. -->
<!-- Copy everything below the line. Plain text only — LinkedIn does not render markdown, so there is deliberately no *emphasis* here. -->

---

Ever give an agent a long series of steps and find that somewhere around step 7 it skipped one or the context compressed before it finished and the agent decided to do some improvising? Skipping steps, building on earlier mistakes, or sessions running out of context = predictability gone.



I built agentfactory to solve this and open-sourced the CLI (with --web option) that turns your SKILL.md files into autonomous agents. The workflow no longer lives inside the agent's persona. It lives in a declarative TOML "formula" (if you've used gastown it will feel like familiar constructs). An agent persona can be independently useful, so an agentfactory agent keeps CLAUDE.md for its persona and asks the runtime for its next step, and executes it. 



Context compression? The runtime re-injects identity and step state. Crash? It resumes from the last unclosed step. Try to skip a step? A fidelity gate catches and corrects it. Agents coordinate through inter-agent mail and you can create an autonomous agent for any purpose and coordinate agents on your factory floor - all running in a docker container you can shut down to sleep well at night.



Figured I'd share in case anyone else finds it as useful as I have. https://lnkd.in/gRvz9j_q



If you're running agents on long workflows - what's your failure mode? Context loss, improvisation, access permissions or something else? Happy to help.

---

<!-- REFERENCE ONLY — earlier staged versions, kept for comparison. Do not paste these. -->

## Previous staged draft (superseded by Glenn's version above)

I kept hitting the same wall with long-running Claude Code agents: give one a 12-step procedure, and somewhere around step 7 the context compresses and the agent starts improvising. It skips steps. It builds on its own earlier mistakes. And when the session dies, everything is gone.

So I built **agentfactory** — an open-source CLI that turns your SKILL.md files into autonomous agents. The core idea: **the workflow shouldn't live inside the agent's persona.** It lives in a declarative TOML "formula." The agent asks the runtime for its current step, executes it, and advances. Context compression? The runtime re-injects identity and step state. Crash? It resumes from the last unclosed step. Agents coordinate through inter-agent mail.

It's Go, AGPL-3.0, and young — I'd rather share it early than polish it in private.

https://github.com/stempeck/agentfactory

If you're running agents on long workflows: what's your failure mode — context loss, improvisation, or something I haven't hit yet?

## Alt hook (from the previous staged draft)

Your SKILL.md is a great instruction set — right up until the model's context compresses and it quietly starts improvising. I built a harness where the workflow survives the agent, instead of living inside it.

## Operator Decision
- Decision: EDITED — operator rewrote with his own hook and additions (docker/"sleep well
  at night", lnkd.in link, "Happy to help" closer); agent applied mechanics-only fixes
  (4, enumerated above) — resolved 2026-07-11. PUBLISHED (URL not captured).
