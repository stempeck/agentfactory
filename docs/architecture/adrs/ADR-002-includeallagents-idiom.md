# ADR-002: Actor-scoping is the default; cross-actor visibility is an operator-only opt-out

**Status:** Accepted
**Date:** 2026-04-18

## Context

The issue store applies a **default actor-scoping overlay** at the
mcpstore adapter: `List`, `Ready`, and `Get` filter by the current
actor unless the caller opts out. This overlay is what makes agent
inboxes, ready queues, and bead state private by default — it is the
runtime enforcement of RBAC for every agent-scoped process in the
system.

RBAC is a hard invariant. An agent must not be able to see another
agent's mail, read another agent's ready queue, or reason about
another agent's bead state — regardless of how convenient it would
be to do so. The trust model depends on this being true unconditionally
at the store boundary.

A design proposal reviewed on 2026-04-18 (deleted in commit `63307bb`
so it cannot be cited as prior art) recommended **removing the default
actor-scoping filter at the mcpstore seam**, on the grounds that
several call sites "legitimately need cross-actor visibility." That
framing confuses escape-hatch use with default policy and inverts the
safety direction: weakening the default to accommodate specific call
sites leaks cross-actor data on every caller that forgets to
re-constrain.

## Decision

**Actor-scoping at the store boundary is the default, and the default
does not move.** The mcpstore adapter's overlay is the enforcement
point for RBAC; it is not a convenience the store offers — it is a
contract the store owes its callers.

There is exactly one sanctioned opt-out: `Filter{IncludeAllAgents:
true}` explicitly set at the call site. This flag exists only for
**operator-level tooling** — human-invoked CLI commands and
administrative processes that exist above the per-agent trust layer
(e.g. a global `--all` flag on a user-facing inspection command). Its
presence at a call site is the audit signal that a cross-actor read
is happening; that signal must remain meaningful.

**Agent-scoped code paths must not use `IncludeAllAgents: true`.** If
an agent-scoped process appears to "need" cross-actor visibility, that
is a defect — in one of four forms:

1. **Missing data-model ownership** — a producer creating records
   without an owner field, forcing consumers to scan across actors to
   find them. Fix: populate the owner at creation.
2. **Misplaced decision** — an agent-scoped process deciding a
   system-scoped question that belongs in an orchestrator, a
   dependency-closure trigger, or the subject record's own state
   machine. Fix: move the decision off the agent path.
3. **Adapter divergence** — different store adapters applying the
   overlay with different semantics, so a caller supplying an explicit
   identity filter gets different answers against different adapters.
   Fix: pin the semantics in a shared contract test; make every
   adapter agree.
4. **Identity / address mismatch** — a caller addressing its own data
   by an identifier different from the one the store uses for the
   overlay, making self-reads look cross-actor. Fix: align the
   identity derivation or the addressing scheme so self-reads pass
   the overlay without an opt-out.

Each of these has a structural fix. None of them is a legitimate use
of `IncludeAllAgents: true` at the agent layer. A design that
proposes to add the flag at an agent-scoped call site — or to weaken
the overlay to avoid having to add it — is proposing to weaken RBAC
and must be rejected on that basis.

## Consequences

**Accepted costs:**

- When the four defect classes above exist in the codebase, the
  symptom is empty query results at agent-scoped call sites. Empty is
  the correct failure direction: it is loud (tests fail, work
  visibly doesn't happen) and it forces the structural fix rather
  than masking it.
- Writing new agent-scoped features requires thinking about ownership
  and addressing up front rather than reaching for the cross-actor
  escape hatch late in design.
- Operator-level tooling that genuinely needs cross-actor visibility
  must declare that intent explicitly at every call site. This is
  friction by design — the friction is the audit trail.

**Earned properties:**

- **RBAC default-safe.** A bug that forgets to add `IncludeAllAgents:
  true` where it belongs produces an empty result (loud, caught by
  tests). A bug that weakened the overlay's default would produce
  silent cross-actor leaks (quiet, detected only in incident
  forensics). The failure direction matters more than the failure
  rate.
- **Textual auditability.** Grepping for `IncludeAllAgents: true`
  enumerates every cross-actor reader in the system. If that flag
  appears outside operator-level code, it is a defect visible at the
  lexical level — no dataflow analysis required.
- **No hidden bypasses.** Cross-actor visibility cannot be configured,
  environment-variabled, or flag-toggled into existence at the
  adapter layer. The only path to seeing another actor's data is an
  explicit, grep-able opt-out, and that opt-out is disallowed at the
  agent layer by this ADR.
- **Adapter swap is safe.** Because every adapter is contractually
  bound to the same overlay semantics, swapping storage backends
  cannot silently change who sees what.

**Non-consequence:** This ADR does not enumerate specific current call
sites, because those are a moving target. Defects will be fixed and
the set of legitimate opt-outs will shrink over time. The durable
content of this decision is the principle (actor-scoping is default;
agent-layer opt-outs are defects), not the census of today's
violations.

## Corpus links

- `invariants.md` — actor-scoping at the store boundary entry
- `trust-boundaries.md#override-policy`
- `idioms.md` — the `IncludeAllAgents: true` idiom entry, including
  its deviation detector
- Related ADRs: [ADR-003](ADR-003-no-identity-overrides.md) — the
  companion principle on the identity side of the trust boundary
  (no caller-supplied identity overrides). Together, ADR-002 and
  ADR-003 define the two halves of the RBAC contract: identity
  cannot be spoofed in, visibility cannot be spoofed out.
