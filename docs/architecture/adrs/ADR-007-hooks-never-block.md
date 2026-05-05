# ADR-007: Hooks never block; enforcement is via mail to agent inbox

**Status:** Accepted
**Date:** 2026-03-23 (quality-gate `dac416a`); 2026-04-10
(fidelity-gate `871e9f9`)

## Context

Claude Code hooks (PreToolUse, PostToolUse, etc.) can return non-zero
exit codes to **block** a tool call. agentfactory has two gates:
`quality-gate.sh` and `fidelity-gate.sh`. A natural implementation
would be: gate evaluates → if failed, exit non-zero → Claude sees the
tool call blocked.

## Decision

Both gates **always `exit 0` with `{"ok": true}`**. Enforcement
happens instead via **mail**: the hook writes a fresh bead to the
agent's own inbox with subject `QUALITY_GATE` or `STEP_FIDELITY`, which
the agent picks up on the next `af mail check`.

This means gate feedback is **asynchronous**: the Claude turn that
triggered the gate completes normally; the bead appears on the next
mail check; the agent sees it as a new message.

## Consequences

**Accepted costs:**
- Feedback latency: one agent turn passes between the offending tool
  call and the gate message arriving.
- The agent may take additional actions after the offending call before
  seeing the gate feedback. For a bad tool call, some damage may be
  done before the agent can respond.

**Earned properties:**
- Hooks are **robust to evaluator failure**: if the `claude` CLI used
  by the gate script is absent or errors out, the hook still exits 0.
  Agent progress is never blocked by gate infrastructure problems.
- Gate feedback flows through the same mechanism as all other
  inter-agent signal: mail. Agents already know how to read mail; no
  new protocol.
- Hooks become composable — multiple gates can run in sequence without
  any one of them having veto power over the turn.

## Interactive vs autonomous asymmetry

Autonomous sessions run both gates; interactive sessions run only
`quality-gate`. Rationale is unanchored — flagged in `gaps.md`.

## Corpus links

- `subsystems/hooks.md` — full hooks shape
- `seams.md#9` — "Quality gate → agent inbox (out-of-band mail)"
- `history.md#theme-6` — hooks subsystem timeline
- `invariants.md#INV-8` — drift test invariant (separate from this ADR)
- Related ADRs: [ADR-008](ADR-008-embed-with-drift-test.md)
