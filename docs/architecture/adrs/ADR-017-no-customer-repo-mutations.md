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

## Amendment (2026-09-28): Integration external writes are recorded and listed, never deleted

**Status:** Proposed. Records operator ruling D5; the operator ratifies this
text (ruling R3) at merge. Extends — does not supersede — the original
decision; "af infrastructure commands must not delete customer data" is
unchanged.

### Context

An integration (ADR-025) is a plugin repository whose `af-integration.toml`
manifest can declare an `[install]` script. That script is third-party code
the operator consents to by running `af plugin install`, and it may write
outside the factory: for example, a command-line tool in `~/.local/bin/<tool>`
and its state in `~/.<tool>/`. The manifest declares every such path as
`[install] external_writes` (`internal/config/integration.go:43`).
Those paths are neither af's artifacts nor provably the customer's, and an
uninstall that deleted them could take a host-global install another factory,
or the customer, still uses.

### Decision

`af plugin install` and `af plugin remove` join the infrastructure commands
this ADR governs. For the paths an integration writes outside the factory:

1. **Declared and recorded.** The manifest lists them as
   `[install] external_writes`. Install verifies they are present after the
   script runs and records the list in `plugins.json`, with a sha256 for every
   regular file.
2. **Listed, never deleted.** `af plugin remove <name>` deletes only af's own
   state for that integration — its `plugins.json` entry, its snapshots under
   `.agentfactory/store/integrations/<name>/` and its `.runtime` check and
   service records — prints every declared external write, and deletes none of
   them. Removing them is the operator's decision (rule 3).

### Consequences

- A removed integration can leave binaries and state on the host; the remove
  output names each path, so the operator can delete them deliberately.
- A `shared = true` integration adopts an existing host-global install instead
  of reinstalling it, so one version per host is the accepted residual.
- Writes an installer makes that it did not declare are not covered: af neither
  records nor removes them.
