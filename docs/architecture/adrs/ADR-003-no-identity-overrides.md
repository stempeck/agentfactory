# ADR-003: No user-facing identity override flags

**Status:** Accepted
**Date:** Multiple; the invariant is stated explicitly in
`feedback_no_agent_overrides.md` (2026-04-13) and pinned as INV-2.

## Context

Every agent command needs a notion of "who is calling?" for actor
scoping, mail routing, and WORK_DONE delivery. The context for that
identity is derived by `resolveAgentName`:

1. cwd path detection (`DetectAgentFromCwd`) → parts[2] candidate
2. Membership check against `agents.json`
3. `AF_ROLE` env fallback (written by `session.Manager` only)

A common request from implementers is to add a user-facing override —
`--as`, `--actor`, `--from`, `--role` — for the case where detection
fails or you want to act as a different agent for debugging.

## Decision

**No user-facing identity override flags exist or may be added.**
Identity is derived from trusted context only. If detection genuinely
can't resolve, the operation fails loudly — it does not accept a
caller-supplied identity claim.

The reason, in the user's words: *"agents grep for flags and use them,
subverting the trust model. Overrides introduced for 'edge cases'
become standard workarounds."*

## Consequences

**Accepted costs:**
- Some legitimate debugging workflows require the debugger to operate
  from inside the correct agent's directory (or set `AF_ROLE`
  temporarily in a shell, which is still a session-layer operation).
- Error messages need to explain the derivation failure clearly.

**Earned properties:**
- Trust model is **tamper-evident**: any identity claim passing through
  the system is either (a) derived from cwd + agents.json membership,
  or (b) from `AF_ROLE` which only `session.Manager` writes. Both are
  auditable by reading the code; neither is influenced by caller input.
- Agents running inside Claude cannot "pretend to be another agent"
  even if prompted to — there's no flag to invoke.

## Corpus links

- `invariants.md#INV-2` — the invariant statement
- `trust-boundaries.md#override-policy` — allowed / forbidden writes
- `idioms.md#2` — `resolveAgentName` three-tier derivation
- Tests enforcing this: the env-hermetic regression test family
  (commit `e4cb7a0`, `7875acc`)
- Related ADRs: [ADR-002](ADR-002-includeallagents-idiom.md) (companion
  RBAC decision), [ADR-004](ADR-004-library-env-hermeticity.md)
