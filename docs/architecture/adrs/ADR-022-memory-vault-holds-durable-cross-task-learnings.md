# ADR-022: The memory vault holds durable cross-task learnings; ephemeral per-task and handoff state stays in mail and checkpoints

**Status:** Accepted
**Date:** 2026-08-18

## Context

The memory subsystem (#515/#626/#323) gives every agent a durable learnings vault at
`<factory-root>/.agentfactory/memory/<agent>/` — the one writable surface that outlives a worktree
and every teardown path. A reviewer, reading the context-advisory rework, asked the load-bearing
governance question: if we now have a *durable* store an agent can write, what stops it filling
with issue/task-specific handoff information that is ephemeral and short-lived? Memory is meant to
be a persistent mechanism that benefits the agent on *every* task, not a per-task, per-step, or
per-handoff scratchpad.

The concern is real because the three other surfaces an agent can write are all wrong for durable
learnings, which is exactly why the vault was built (`#626`, quoted at
`.designs/515/design-doc.md:66`): the worktree is ephemeral (deleted on formula completion);
checkpoints are machine-only (the `Notes` field has no agent-facing writer and a silent 24-hour
TTL); mail is rent-charging (re-injected in full on every prompt) and agent-destructible. The old
CONTEXT ADVISORY told agents to "write checkpoint notes, and mail summaries" — mail *was* named as
a writable survivor, but it is the ephemeral handoff channel, not a durable learnings store. So the
boundary between the two must be stated, or the vault erodes into a second documentation system
(design Risk **R9**, `.designs/515/design-doc.md:178`).

## Decision

**The memory vault holds durable, cross-task, agent-general LEARNINGS. Ephemeral per-task, per-step,
and handoff state stays where it already lives — mail and checkpoints.**

- **IN (belongs in the vault):** operational learnings that will help the same agent on a *future,
  different* task. The `--type` vocabulary is the machine-readable IN-list and is closed on
  purpose: `gotcha`, `model-behavior`, `ops`, `outcome`, `improvement`
  (`internal/memory/note.go:18-24`). "Closed on purpose: it is what keeps the vault a journal of
  operational learnings rather than a second documentation system" (`internal/memory/note.go:15-17`;
  design principle **C-515-P1**, `.designs/515/design-doc.md:16`).
- **OUT (does not belong in the vault):**
  - per-task / per-step / handoff state → **mail** (one-time handoff; ephemeral by design);
  - machine crash-recovery state → **checkpoints** (written for the agent by the recovery path);
  - solutions and designs → `.designs/` / `.analysis/` (design principle **C-515-P4**,
    `.designs/515/design-doc.md:19`).

This records a boundary the code already lives (Status Accepted) — the context advisory itself
already routes durable learnings to `af memory add … --type gotcha` and conversation-only state to
"mail summaries" (`internal/cmd/recovery.go` `contextAdvisoryBody`), and the command surface calls
the vault a "Durable per-agent learnings vault … learnings that outlive a worktree"
(`internal/cmd/memory.go:80-83`).

## Consequences

The reviewer's failure mode — durable memory filling with ephemeral handoff — is held OUT by four
mechanisms already in the subsystem, not by discipline alone:

- **Closed type vocabulary.** Handoff/task state has no `--type` that fits; an unrecognised type
  keys the shortest TTL and is flagged rather than served (`internal/memory/note.go:18-24,26-30`).
- **Per-type TTL.** A note that is not a durable learning ages out of what is served: `gotcha`/
  `outcome` expire at 90 days, `ops`/`model-behavior` at 180, `improvement` carries no TTL and is
  nagged toward graduation instead (`internal/memory/slice.go:33-37,51`; `.designs/515/design-doc.md:85`).
- **Graduation + read-time suppression.** When a learning lands somewhere durable it graduates to a
  real destination and is suppressed from the injected slice at read time — correct even if no
  periodic job runs (`internal/memory/note.go` status transitions; `.designs/515/design-doc.md:85`).
- **`af memory status` flags + nag review**, with the H-D curation formula as the named backstop
  when boundary-flag counts stay above zero (`.designs/515/design-doc.md:19,178`).

Cost: agents and operators must learn one distinction (learning vs handoff). The `--type` prompt,
the advisory wording, and this ADR make it explicit so the choice is not left implicit.

## Flip condition

If a durable-but-non-learning artifact class (for example, long-lived cross-task pointers that are
not operational learnings) proves it genuinely needs the vault, revisit the IN-list deliberately —
add a `--type` and its TTL — rather than widening usage silently under an existing type.

## Corpus links

- `internal/memory/note.go:15-24` — the closed `--type` vocabulary (the IN-list) and why it is closed
- `internal/memory/slice.go:33-37,51` — per-type TTLs and the graduation-due clock
- `internal/cmd/memory.go:80-83` — command help: "Durable per-agent learnings vault"
- `internal/cmd/recovery.go` — `contextAdvisoryBody`: durable learnings → `af memory add`, ephemeral → mail summaries
- `.designs/515/design-doc.md:66` — #626's four writable-surface economics (why mail/checkpoints are the ephemeral channels)
- `.designs/515/design-doc.md:16,19` — C-515-P1 (journal, not reference) and C-515-P4 (content boundaries)
- `.designs/515/design-doc.md:85,178` — the enforced note lifecycle and Risk R9 (boundary erosion) mitigations
- ADR-019 — no container recreation (the vault is container-local; export is the durability seam)
