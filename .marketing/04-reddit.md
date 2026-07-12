<!-- DRAFT — staged by visibility plan Phase 6. Review before publishing. Tier B: requires Glenn's explicit go-ahead. -->

# Reddit Posts (two variants)

---

## Variant A — r/ClaudeAI

**Suggested flair:** "Built with Claude" (or "Coding" if that flair is retired — check at post time)

**Norms note:** r/ClaudeAI is friendly to open-source Claude tooling if you're the author, say so, and stick around in the comments — drive-by link drops get removed.

**Title:** I built an open-source CLI that turns your SKILL.md files into autonomous agents with crash recovery

**Body:**

If you've run Claude Code on long multi-step work, you've probably watched context compression eat your plan: somewhere around step 7 of 12 the session compresses, the agent starts improvising, and the next hour of work builds on whatever it invented.

I built agentfactory to fix that. The workflow doesn't live in the agent's prompt — it lives in a declarative TOML "formula" on disk. The agent runs `af prime` to get its identity and *only its current step*, does the work, runs `af done` to advance. When Claude Code compresses context, a PreCompact hook checkpoints and recycles the session, and the fresh session re-primes with identity + current step. When a session dies outright, it resumes from the last unclosed step.

The part most relevant to this sub: there's a `/formula-create` skill that converts an existing SKILL.md into a formula, and `af formula agent-gen` generates a specialist agent whose *identity is the formula* — so even after compression, the agent gets re-injected knowing its full playbook. Agents coordinate through mailboxes (hook-injected on prompt submit, no polling), and everything runs in plain tmux so you can `af attach` and watch any agent work.

Honest caveats: setup needs Go, tmux, Python 3.12, and jq (there's a Docker path); it's young; AGPL-3.0.

Repo: https://github.com/stempeck/agentfactory

Curious what failure modes others hit with long-running agents — I designed around the four I kept seeing, and I doubt the list is complete.

---

## Variant B — r/LocalLLaMA

**Suggested flair:** "Resources"

**Norms note:** r/LocalLLaMA tolerates self-promotion only for genuinely open-source tools, is openly skeptical of cloud-model tooling, and rewards architectural substance over product pitches — lead with the design, disclose the Claude dependency in the first paragraph.

**Title:** Orchestrating long-running agents when you can't trust the context window — architecture write-up + open-source implementation

**Body:**

Upfront disclosure: this is my project, it's open source (AGPL-3.0, Go), and today it only drives Claude Code — so if cloud-model tooling isn't your thing, the code won't be useful to you yet. I'm posting because the orchestration architecture is model-agnostic and that's the part worth discussing.

The problem: any long-running LLM agent eventually loses its instruction set — context gets compressed or truncated, recency bias outcompetes the original plan, and the model improvises rather than stopping. My conclusion after a year of this: durable workflows can't live inside the model's context at all.

The architecture: workflows are declarative TOML files ("formulas") — steps, DAG dependencies, variables, approval gates — living on disk. A thin runtime feeds the agent one step at a time: prime (inject identity + current step), execute, advance. Progress is disk state, so a crashed agent resumes from its last unclosed step, and context compression can't corrupt a plan the model was never carrying. Agent identity is kept separate from workflow logic, so one workflow can run under any persona. Inter-agent coordination is a mailbox with hook-based delivery instead of polling. Sessions are plain tmux — attach and watch, no dashboard layer.

Nothing here is conceptually locked to Claude: the hooks map to session lifecycle events (start, pre-compaction, prompt-submit) that any harness could expose. If you're running local models with llama.cpp/vLLM behind an agent loop, I'd be interested in whether those harnesses expose enough lifecycle to port this pattern.

Repo: https://github.com/stempeck/agentfactory
