# ADR-029: Prediction and observation; the record is memory, not an authority

**Status:** Proposed
**Date:** 2026-11-13 (issue #754, `.designs/754/design-doc.md` — cited as text: the OSS repository
does not carry `.designs/`)

## Context

A session's context window has more than one source today. A profile can declare it
(`CLAUDE_CODE_MAX_CONTEXT_TOKENS`, `internal/config/models.go` `DeclaredContextWindow`), the host
reports the window it applied (`context_window_size` in the statusline payload), and a foreign
model with neither falls back to a built-in figure (`PredictContextWindow`). The declaration and
what the host applies can disagree, and nothing records which one a launched identity actually got.

No ADR covers this: 0 of 28 ADRs mention a context window. #668 set a hard constraint on any
mechanism built over the chain, "No fifth window authority" (`.designs/668/design-doc.md:48` —
cited as text: the OSS repository does not carry `.designs/`). Every mechanism takes its window
operand from the existing chain; none may add a new place a window comes from.

#754 needs to remember what the host applied, so that a detector, a launch gate and a probe can
compare it with what the profile declared. It also gives the operator a way to make a ruling reach
every session start (the pinned block, ADR-022's amendment). Both are new durable records, and both
could be misread as a new authority.

## Decision

**A profile's declared window is a prediction. The host-applied window, recorded per identity
(profile, model id, harness version, declaration), is an observation.**

- The record is memory that a detector, gate or probe consults. It never rewrites the declaration
  and it is not a fifth authority: the window a session runs with is still the one the existing
  chain resolves. A disagreement between the record and the declaration is reported to the
  operator, who changes the declaration if it is wrong.
- An operator-pinned note is likewise memory delivered by operator choice, not an instruction
  authority. It reaches every session start under a header that says who pinned it
  (`internal/memory/pinned.go` `PinnedBlockHeader`), and the injection writes, per session, how
  many pins it rendered (`internal/cmd/memory_injected.go`) so that delivery is checked against a
  record rather than assumed.

## Consequences

**Earned properties:**
- One window authority chain stays the only answer to "what window does this session have";
  observations can make a wrong declaration visible but cannot silently replace it.
- An observation is bound to the identity it was made under, so a harness upgrade or a changed
  declaration starts a new observation rather than inheriting a stale one.
- Whether a session received its operator's pins is a recorded fact per session, not an inference
  from transcript text.

**Accepted costs:**
- A wrong declaration keeps being used until the operator corrects it wherever it decides: every
  prediction, every foreign-class session, and any session whose host applies more than declared.
  A not-foreign session whose host applies less runs on the smaller figure, its own host report,
  through the existing chain (`ObservedContextWindow` takes `min(D, H)`, D declared and H
  host-reported), never through the record. The record only reports the disagreement.
- An agent can write its own vault's files, so a hand edit that sets `standing: true` is served
  under the operator's header. The pin verbs are the guard, and it is advisory between same-user
  processes, as the rest of the vault's curation is.

## Flip condition

If an observed window proves reliable enough that operators always copy it into the declaration by
hand, revisit whether the record may supply the declaration. That is a deliberate change to the
authority chain, made in its own ADR, never a fallback inside a reader.

## Corpus links

- ADR-022 — the memory vault, amended by the standing mark
- ADR-023 — the SessionStart context surface the pinned block is delivered through
- `internal/config/models.go` — `DeclaredContextWindow`, `PredictContextWindow`, `ObservedContextWindow`
- `internal/memory/pinned.go` — the pinned block, its one measure and the shared sentinel fence
- `internal/cmd/memory_injected.go` — the per-session injection record
