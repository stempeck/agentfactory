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

## Amendment (2026-09-29): One enumerated exception — the af-owned session guard for integration-bound sessions

**Status:** Proposed (awaiting operator ruling R1). Nothing below takes effect
as an exception to this ADR until the operator records R1. Like the 2026-08-31
amendment, it would grant ONE exemption, named below, and define no exempt class
and no process for adding one.

### Context

Sometimes Claude Code stops in the middle of a task to ask the person at the
terminal a question, and does nothing else until someone answers. There are two
kinds of these prompts, and Claude Code tells hooks about each one as an event:

- A **permission prompt** asks "may this tool run, yes or no?". Its hook event
  is `PermissionRequest`.
- An **input request** comes from an MCP server (an add-on tool server) that
  asks the person to type an answer. Its hook event is `Elicitation`.

Do these prompts block? Yes. Both stop the agent until someone answers.

af starts Claude Code for every agent with `--dangerously-skip-permissions`
(`internal/session/session.go:739`), a mode that turns off most permission
prompts. This amendment works on the assumption that some prompts still appear
in that mode: a hook of an installed plugin can still ask for permission, and an
MCP server can still ask for input. That assumption has not yet been measured on
a live session.

Nobody watches an autonomous agent's terminal. A prompt there is never answered,
so the agent stops working while it still looks "running". Mail cannot help: an
agent that is waiting on a prompt reads no mail.

The af session guard is af's own hook on these two events. The hook itself never
blocks: it answers at once and always exits successfully
(`internal/cmd/plugin_guard.go:44-78`). Once an agent's session is bound to any
integration, the guard answers "no" to every permission prompt and every input
request in that session, whoever raised it. A refusal that af cannot trace to an
integration's plugin is reported under the name `unknown`.

That answer is why an exception is needed. With nobody watching, the only way to
keep the agent moving is for af to answer the prompt itself, and the only safe
answer is "no". But "no" refuses the action that asked: for a permission prompt,
af's answer is a deny decision (`internal/cmd/plugin_guard.go:149-150`), and the
tool call that asked does not run. This ADR says af hooks never refuse anything;
they let the agent carry on and report by mail. So af answering "no" is an
exception, and this ADR grants exceptions only by naming them one at a time, as
the 2026-08-31 amendment did. This amendment names this one.

### Decision

One exception is enumerated:

**The af session guard** — the hidden command `af plugin guard-event
<permission|elicitation>` (`internal/cmd/plugin_guard.go:15-21`), installed as
the `PermissionRequest` and `Elicitation` hooks of every autonomous agent's
settings (`internal/claude/config/settings-autonomous.json:38`, `:49`) — may
deny a permission request (answer "no" to "may this tool run?") and decline an
elicitation (refuse to give an MCP server the input it asked for).

- **Purpose:** keep an unattended agent from waiting forever on a question
  nobody will answer.
- **Why these two events:** they are where Claude Code, about to wait for a
  person, lets a hook give the answer instead. Hooking them is how af answers in
  the absent person's place.
- **Why "no" and not "yes":** "yes" would let a third-party plugin do something
  no person approved, in a session nobody watches. "No" costs one refused
  action. The agent is told the action was refused, with af's reason, and can
  carry on. af mails the manager `INTEGRATION_GUARD_DENIED <integration>: ...`
  once, so a person can run that operation from an interactive session or allow
  it in the plugin's settings (`internal/cmd/plugin_guard.go:70-72`).

The exception is valid only while ALL of the following hold; violating any of
them makes the guard non-conforming, and it does not widen this exception:

1. **No evaluator in the decision path.** The answer depends only on whether the
   session's integration pin exists: the file `.runtime/integration_bindings`
   that af writes into the agent's directory when it starts the agent with
   integrations. No LLM call and no judgment service is involved
   (`internal/cmd/plugin_guard.go:60-63`).
2. **Does nothing without a pin.** In a session with no pin the guard does
   nothing and lets Claude Code show the prompt as usual, so a factory with no
   integrations behaves exactly as before (`internal/cmd/plugin_guard.go:60-63`).
   af writes the pin only when it started the agent with at least one
   integration, or skipped an optional one (`internal/cmd/integration_pin.go:139`),
   so a pin that holds only skipped optional integrations also turns the guard
   on, and its refusals are reported under `unknown`. A pin written when the
   agent starts names every integration bound to it, factory-wide integrations
   included (`internal/cmd/integration_pin.go:171`).
3. **The hook process never fails.** Its answer is the JSON it prints. Every
   path returns success, so the process exits 0, and a session af cannot match
   to one of its agents gets no answer: Claude Code shows the prompt as usual
   (`internal/cmd/plugin_guard.go:45-47`, `:56-59`).
4. **Every refusal is reported.** af mails the manager
   `INTEGRATION_GUARD_DENIED <integration>: <event> <tool>` once per integration
   across the factory (`internal/cmd/plugin_guard.go:72`); writing a new pin lets
   that mail go out again for the integrations the pin binds
   (`internal/cmd/integration_pin.go:159-162`). Every refusal is still decided
   and printed; only the mail is sent once. A refusal that no pinned plugin can
   be traced to is reported under the one name `unknown`
   (`internal/cmd/plugin_guard.go:109`).
5. **Scope is these prompts, in autonomous sessions only.** The guard acts only
   when Claude Code is about to stop and wait for a person in an autonomous
   session. It never refuses a tool call that would have run without asking. The
   interactive template, whose pane a human answers, carries no guard. That
   exclusion is itself a provisional choice awaiting Supervisor confirmation,
   like R1.

### Consequences

- The broad rule, and the 2026-08-31 exception, stand exactly as before.
- This amendment is precedent for nothing. Conditions 1–5 bound this one
  exemption; they are not a template for another deny hook.
- Consented third-party hooks shipped inside an integration are the
  integration's own. They are outside this ADR's af-hooks rule and gain nothing
  from this amendment.
- Residual: a stall the guard cannot see (a prompt path other than these two
  events) is detected only by occupancy recovery.
- Residual: a session that receives factory-scope integrations without a pin
  (`af up` of an agent whose formula declares no integrations, or a respawn of an
  instance pinned before this change) passes through, because the pin is written
  only where a formula declares integrations (`internal/cmd/up.go:319`,
  `:335`). Closing it means writing a pin outside instantiation or keying the
  guard on something other than the pin, both of which depart from the
  IMPLREADME's contract and are escalated to the Supervisor.
- Residual: the pin lives in the agent's own dir, so a session that deletes
  `.runtime/integration_bindings` switches its guard off. The guard stops an
  unwatched pane from stalling; it is not a boundary against the agent itself.
- Residual, outside the guard: the dispatch pre-check refuses only on a check
  record it can read, so a first-ever failing `[check]` is refused by admission
  after `--reset` has already stopped the agent (`internal/cmd/sling.go:247`).
  Report markers are keyed per (integration, condition) factory-wide, as the
  IMPLREADME specifies, so one agent's intact launch re-arms a condition another
  agent still has. Both are escalated to the Supervisor.

Reference: ruling R1; `internal/cmd/plugin_guard.go:15-153` (the command),
`internal/claude/config/settings-autonomous.json:38-59` (its hook wiring).

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
