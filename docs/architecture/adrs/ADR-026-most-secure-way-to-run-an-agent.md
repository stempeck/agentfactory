# ADR-026: agentfactory will be the de facto standard for running agents securely; its security is improved continuously and never weakened

**Status:** Accepted (ratified by the operator, 2026-10-25)
**Date:** 2026-10-25 (written against commit `5dbd77f`; the commit that adds this ADR is recorded when it lands)

## Context

agentfactory runs autonomous agents unattended (`internal/session/session.go:466`). Those agents
hold the operator's credentials (`quickstart.sh:1139`) and run across hundreds of factories
(`docs/architecture/adrs/ADR-019-no-container-recreation.md:10`). The af binary embeds its
formulas (`internal/cmd/install.go:36`) and writes them into every factory it installs
(`internal/cmd/install.go:424`), whatever repository that factory serves. An agent runner is only usable
if its agents can be trusted, and every repository that runs agentfactory is exactly as secure as
agentfactory is.

agentfactory's security controls were each recorded as a separate decision:
- actor-scoped store reads (ADR-002, `docs/architecture/invariants.md:16`);
- identity derived only from trusted context (ADR-003, `docs/architecture/invariants.md:50`);
- a library layer that reads no environment (ADR-004, `docs/architecture/invariants.md:81`);
- the fail-closed teardown authority guardrail (ADR-021, `internal/cmd/authority.go:75`,
  `internal/cmd/authority.go:101`);
- inert plugin acquisition with fail-closed manifest readers (ADR-025);
- hash-locked Python dependencies (`py/requirements.txt`, installed with `--require-hashes` at
  `quickstart.sh:233` and `Dockerfile:43`);
- sha256-verified downloads (`Dockerfile:28`, `quickstart.sh:1950`);
- fable-secure's disclosure invariant (`internal/cmd/install_formulas/fable-secure.formula.toml:69`).

We considered two alternatives and rejected both:
1. **Decide case by case, as most software projects do.** When a feature clashes with a
   security measure, someone decides that time which one wins. We reject this. agentfactory
   installs the same code into every factory (`internal/cmd/install.go:424`), so a security
   measure dropped for one feature is dropped for every user at once.
2. **Only promise not to make security worse.** Keep today's security measures but never add
   new ones. We reject this too: agentfactory would stay exactly as secure as it is today and
   never get better.

## Decision

**As of this date, agentfactory is committed to being the most secure way to run an agent, and
to becoming, through continuous improvement, the de facto standard for running agents securely.
Every change keeps or strengthens every security control, and a capability that cannot be built
without weakening one is not built.**

Agents run under Claude Code's own harness controls: permissions, sandbox and hooks. The
protected controls include those enumerated in Context, from `internal/cmd/authority.go:101` to
`internal/cmd/install_formulas/fable-secure.formula.toml:69`. A new control joins them when its
decision record names it, and from then on it can only be strengthened. Security defects are
tracked privately under the project's disclosure rules
(`internal/cmd/install_formulas/fable-secure.formula.toml:69`) and closed.

## Consequences

**Costs:**
- A capability that cannot meet every control is not built.
- Every design carries the work of meeting every control; none is relaxed to make a design fit.
- Security improvement is standing work and is never finished.

**Earned properties:**
- agentfactory's security baseline only rises.
- Every repository running agentfactory gets the same controls. No formula's control is relaxed
  to suit one repository.
- Every change is reviewed against one question: which control does it touch, and does it keep
  or strengthen it?

## Corpus links

- `../trust-boundaries.md`: the identity trust flow these controls protect.
- `../invariants.md`: INV-1 (actor-scoping), INV-2 (identity from trusted context), INV-3
  (environment-free library layer), INV-4 (loopback bind, R-SEC-1).
- Related ADRs: [ADR-002](ADR-002-includeallagents-idiom.md),
  [ADR-003](ADR-003-no-identity-overrides.md), [ADR-004](ADR-004-library-env-hermeticity.md),
  [ADR-019](ADR-019-no-container-recreation.md), [ADR-021](ADR-021-teardown-authority-guardrail.md),
  [ADR-025](ADR-025-plugin-repositories-trust-boundary.md).
