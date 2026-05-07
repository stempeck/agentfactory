# ADR-017: af infrastructure commands must not delete customer data

**Status:** Accepted
**Date:** 2026-05-07

## Context

The factory root is the customer's project. af creates `.agentfactory/`
and `.beads/` inside it, but even within those trees customers create
their own artifacts (formulas, agents). Outside those trees, everything
belongs to the customer.

This principle has been violated three times — designs 170, 173, and
the current fix — each time by code that constructed a path inside the
factory root and deleted whatever it found there.

## Decision

**af infrastructure commands (`af install`, `af up/down`,
`af formula agent-gen`, `agent-gen-all.sh`, `make sync-formulas`,
`quickstart.sh`) must not delete customer data.**

This does **not** constrain agents. Agents operate on the customer's
repo as their formulas require. Infrastructure manages the factory.

Rules:

1. **Outside af directories:** read-only. No creates, modifies, or
   deletes.

2. **Inside af directories:** af may manage its own artifacts. Customer
   content (customer formulas, customer agents, customer hook
   modifications) must not be deleted. Track provenance to distinguish
   (`formula` field in `agents.json`, source comparison in
   `agent-gen-all.sh:89-100`).

3. **When in doubt, don't delete.** A stale file is less harmful than
   silent destruction. Warn and let the customer decide.

## Consequences

- Customer repos safe from silent deletion regardless of directory
  structure.
- Stale artifacts from prior af versions require manual cleanup.
  Correct tradeoff.

## Corpus links

- `internal/cmd/formula.go:102-108` — `sameDir()` helper
- `TestFormulaAgentGen_DeleteDoesNotTouchFactoryRootTemplates`
- `agent-gen-all.sh:89-100` — customer formula preservation (design 173)
- `.designs/170/design-doc.md` — origin of the fallback cleanup bug
- `.designs/173/design-doc.md` — fixed bash-side, missed Go-side
- ADR-008, ADR-015
