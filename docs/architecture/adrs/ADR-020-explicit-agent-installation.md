# ADR-020: Agent installation is explicit and customer-owned

**Status:** Accepted
**Date:** 2026-07-09

## Context

One source tree feeds two distributions: this repo, and the OSS repo
published through an allowlist (`todos/public_repo_files.md:87-106`
carves pro-only formulas out of an otherwise-wholesale `internal/`
sync). The publish script hard-fails if `install_formulas/` exceeds the
expected formula count (`todos/stempeck_publish_oss.sh:208-213`), so
every shipped formula is a deliberate pro/OSS decision.

Embedded formulas are a distribution channel (ADR-015): they place
capability into a customer factory's store. Registration is a separate,
explicit act. A fresh factory seeds exactly manager + supervisor
(`internal/cmd/install.go:161`); every other agent enters `agents.json`
through an explicit verb — `af formula agent-gen`
(`internal/cmd/formula.go:252`), `af install --agents`
(`internal/cmd/install.go:69-70`), and `af install <role>` refuses roles
not already registered (`internal/cmd/install.go:532`). Dispatch
likewise refuses unregistered agents (`internal/cmd/sling.go:273`).

The gap between "formula embedded" and "agent registered" is tempting
to close automatically at init. Because `internal/` ships to OSS
wholesale (`todos/public_repo_files.md:36`), that would register every
embedded formula as an agent in the customer's repository on first
install — agents the customer never chose, source-controlled in their
tree.

## Decision

**Agent installation is explicit and customer-owned.**

1. **`af install --init` bootstraps manager + supervisor only**
   (`internal/cmd/install.go:161`). It must not register additional
   agents, regardless of how many formulas are embedded.

2. **Embedded formulas are a distribution channel, not a registration
   source.** A formula's presence in `install_formulas/` or the store
   grants nothing until the customer runs an explicit verb
   (`af formula agent-gen`, `af install --agents`). The
   formula-present-but-unregistered gap is the customer's choice point,
   not a defect.

3. **The pro/OSS boundary is governed by the publish allowlist.** Every
   new shipped formula must be dispositioned in
   `todos/public_repo_files.md` — included or excluded — when it is
   created. The formula-count guard
   (`todos/stempeck_publish_oss.sh:208-213`) turns a forgotten
   disposition into a loud publish failure instead of a silent leak.

## Consequences

- A fresh factory cannot `af sling` a specialist until an explicit
  install verb runs. Correct tradeoff: registration is consent, and
  agents live in the customer's repo under the customer's source
  control (ADR-017).
- Init-time auto-registration of embedded formulas is rejected, even as
  a convenience, because `internal/` ships to OSS wholesale and the
  behavior would reach every customer install.
- Each new shipped formula costs one allowlist line. The publish guard
  makes forgetting it fail loudly.

## Corpus links

- ADR-015 — embedded formulas as distribution (three-location lifecycle)
- ADR-017 — customer-owned repo; af must not mutate uninvited
- `internal/cmd/install.go:161` — init seeds manager + supervisor only
- `internal/cmd/formula.go:252` — agent-gen registers via `AddAgentEntry`
- `internal/config/config.go:357-360` — `AddAgentEntry` refuses to clobber manual agents
- `internal/cmd/sling.go:273` — dispatch requires prior registration
- `todos/public_repo_files.md:87-106` — pro/OSS formula disposition list
- `todos/stempeck_publish_oss.sh:208-213` — formula-count publish guard
