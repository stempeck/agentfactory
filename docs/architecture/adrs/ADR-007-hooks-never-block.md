# ADR-007: Hooks never block; enforcement is via mail to agent inbox; no escalation into a void

**Status:** Accepted (extended 2026-06-15 — "no escalation into a void"; amended
2026-08-31 — one enumerated exception: the dispatch capacity-admission gate,
#672; see *Amendments* below)
**Date:** 2026-03-23 (quality-gate `dac416a`); 2026-04-10
(fidelity-gate `871e9f9`); extended 2026-06-15 (no-escalation-into-a-void);
amended 2026-08-31 (enumerated exception — dispatch capacity-admission gate,
operator decision — #668/#672)

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

## PreCompact exception: compaction-boundary recycling

The `af compact-handoff` command replaces `af prime` in the PreCompact hook
(design #288). When context compaction is imminent, it checkpoints state and
recycles the session via `tmux respawn-pane -k` to prevent compaction from
corrupting signed extended-thinking blocks.

This does not violate ADR-007's principle:

- **Not hook-level blocking**: The command never returns a non-zero exit code.
  It kills the session via process-level termination (`tmux respawn-pane -k`),
  which is fundamentally different from a hook returning non-zero to block a
  tool call.
- **Graceful fallback**: If any step fails (not in tmux, can't find factory
  root, checkpoint write fails), the command returns exit 0 — compaction
  proceeds as before. Agent progress is never blocked by infrastructure
  failure.
- **The recycling IS agent progress**: Preserving signed thinking blocks and
  maintaining a working session is itself forward progress, not enforcement of
  a gate.

Reference: `.designs/288/design-doc.md`, Gap 6 in
`.designs/288/six_sigma_gaps.md`.

## Amendment (2026-06-15): No escalation into a void

**Status:** Accepted. Extends — does not supersede — the original decision;
"hooks never block" and gate→own-inbox routing are unchanged.

### Context

The original decision covered *gate* feedback (to the agent's own inbox) but not
**escalation to a third party**. Leaving that unspecified admits two failures:

- An escalation sent fire-and-forget to a fixed agent that may not be running
  lands in an unread inbox and is silently lost — the alert is never seen.
- An escalation channel that can re-enter the condition it reports recurses,
  delivering nothing and consuming resources without bound.

### Decision

An escalation MUST NOT go into a void:

1. **No fire-and-forget to a recipient that may not be running.** Either target a
   recipient guaranteed to be present, or make non-delivery a detectable, handled
   condition — never silently discard the send result.
2. **No self-referential channel.** The escalation path must not re-enter the
   condition it reports; one failure yields at most one notification, never a
   recursion or storm.

This constrains the **delivery contract** of an escalation — it must reach
something or fail loudly. It does not decide block-vs-inform or any enforcement
mechanism.

### Consequences

- An escalation path may no longer assume its recipient exists: escalating to a
  fixed agent requires either guaranteeing that agent is running or treating a
  failed send as an error, not a no-op.
- Enforcement that escalates must route through a channel that cannot trigger the
  condition it reports.
- "Hooks never block" and gate→own-inbox routing are unchanged.

## Amendment (2026-08-31): One enumerated exception — the sub-agent dispatch capacity-admission gate (#672)

**Status:** Accepted (operator decision, 2026-08-31). The original decision is
**broad by intent — every hook-shaped mechanism exits 0 — and it remains
broad.** This amendment grants ONE exemption, named below. It defines no exempt
class and no process for adding one: every other hook-shaped mechanism exits 0,
without exception.

### Context

The #668 design applied the broad rule as written — correctly — and therefore
demoted deterministic capacity admission for sub-agent dispatch to a deferred
contingency (PR #669). The operator's acceptance run (2026-08-31, instance
`af-2dc03367`) then measured this ADR's accepted cost — "some damage may be
done before the agent can respond" — at full price: three sub-agents launched
against a 262,144-token shared backend pool, the orchestrator starved behind
its own children, the run wedged for hours, and zero interventions fired
(#672). Post-act mail cannot prevent a resource commitment; for this one
decision, the prohibition costs more than the property it protects.

### Decision

One exception is enumerated:

**The sub-agent dispatch capacity-admission gate (#672)** — pre-act
interception of Agent/Task dispatch — may refuse a launch. The exception is
valid only while ALL of the following hold; violating any of them makes the
gate non-conforming — it does not widen this exception:

1. **No evaluator in the decision path.** The verdict is arithmetic over
   operator-declared backend capacity facts and harness-observed live-context
   state. No LLM call, no external judgment service.
2. **Fail-open on any input-resolution error**: the launch is admitted and an
   observe record written. The property the broad rule protects — agent
   progress is never blocked by gate infrastructure problems — is preserved
   verbatim.
3. **Structurally inert where no capacity fact is declared** for the profile's
   backend: refusal is impossible there by construction, not by tuning.
4. **Every refusal is a recorded intervention**, retrievable through the
   standard read surfaces and carrying the arithmetic that justified it.
5. **Scope is the dispatch of new sub-agent work only.** No other tool call
   may be refused under this amendment; mail remains the channel for all
   advisory and evaluative feedback.

### Consequences

- The broad rule stands for everything else, exactly as before.
- This amendment is precedent for nothing. It grants one exemption and no
  procedure for granting another. A design citing the "deterministic" or
  "arithmetic" character of some other hook to justify blocking is out of
  order: conditions 1–5 bound this one exemption; they are not a template.
- The #672 rework proceeds under this exemption rather than around this ADR.

Reference: #668 (measured incident), #672 (rework acceptance criteria),
PR #669 (acceptance-failed implementation under rework).

## Corpus links

- `subsystems/hooks.md` — full hooks shape
- `seams.md#9` — "Quality gate → agent inbox (out-of-band mail)"
- `history.md#theme-6` — hooks subsystem timeline
- `invariants.md#INV-8` — drift test invariant (separate from this ADR)
- `../gaps.md#gap-18` — #386 worktree-containment Practical Ceiling / accepted
  residual: the interlock is bounded detect-and-correct (inform-not-block), not a
  hermetic sandbox; consequence (c) of this amendment (guard logged-not-alerted,
  no supervisor dependency) is recorded there.
- Related ADRs: [ADR-008](ADR-008-embed-with-drift-test.md)
