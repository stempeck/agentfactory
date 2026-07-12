<!-- DRAFT — staged by visibility plan Phase 6. Review before publishing. Tier B: requires Glenn's explicit go-ahead. -->

# Show HN Submission

**URL to submit:** https://github.com/stempeck/agentfactory

## Title options (pick one — plain, no hype, ≤80 chars)

1. `Show HN: Agentfactory – multi-agent orchestration CLI for Claude Code`
2. `Show HN: Turn SKILL.md files into crash-recoverable Claude Code agents`
3. `Show HN: Agentfactory – TOML-defined workflows that survive agent context loss`

## First comment (post immediately after submitting)

Agentfactory is a Go CLI that runs Claude Code agents against declarative TOML workflows ("formulas") instead of keeping the plan in the agent's prompt. Each agent runs `af prime` to get its identity plus only its current step, executes it, and runs `af done` to advance. Workflow state lives on disk, so context compression can't mangle the plan and a crashed agent resumes from its last unclosed step instead of starting over. Agents get mailboxes for coordination (hook-injected, no polling), sessions run in plain tmux so you can attach and watch, and there's a `/formula-create` skill that converts an existing SKILL.md into a formula plus a generated specialist agent.

I built it because long-running agents kept failing the same four ways: context compression eats the plan, recency bias outcompetes the original instructions, the model improvises when it loses the thread (and then builds on its own improvisation), and a dead session loses everything. Known limitations, honestly: it's Claude Code–only today; setup needs Go 1.24+, tmux, Python 3.12, and jq (a Docker path softens this); regenerating a specialist agent requires rebuilding the binary because identity templates are `go:embed`-compiled; and it's a young project — expect churn. License is AGPL-3.0. Happy to answer questions about the design, especially the context-recovery mechanics.

## Norms reminders (do not skip)

- Post the comment from the submitting account right away; HN expects the author present.
- Respond to every substantive comment, including critical ones, without defensiveness. Concede real limitations plainly.
- Don't ask anyone to upvote, don't share the HN link asking for support (HN detects and penalizes voting rings).
- Best submission window: weekday morning US Eastern. One shot — resubmitting the same URL soon after is penalized.
- Read https://news.ycombinator.com/showhn.html before posting.
