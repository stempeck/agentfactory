# ADR-016: No skill provenance annotations in formulas

**Status:** Accepted
**Date:** 2026-05-01

## Context

The formula-create skill (`formula-create/skillmd-mode.md:79`,
`skillmd-mode.md:137`) instructs formula generators to embed
`**Source skill:** .claude/skills/<name>/SKILL.md (Phase N)` lines in
every domain work step's description field.

These annotations are vestigial. A formula is the agent's identity —
the agent executes the formula's steps and never reads, references,
or uses the skill that authored the formula. The skill is the
authoring tool; the formula is the runtime artifact. Once generated,
the formula is self-contained (ADR-015 documents the formula lifecycle;
skills are not part of that lifecycle).

The annotations create two problems:

1. **False coupling.** When a formula is published without its
   originating skill, the annotations become dead references. Furthermore, 
   agents never follow these references. The annotations mislead reviewers 
   into thinking a dependency exists where none does.

2. **Noise in formula diffs.** Skill paths change (renames, directory
   restructuring). Every such change would require updating annotations
   in every formula generated from that skill, despite the annotations
   having zero runtime effect.

## Decision

1. **Remove the generation directives** from `formula-create/skillmd-mode.md`
   (lines 79 and 137) that instruct generators to emit `Source skill:`
   annotations.

2. **Strip existing annotations** from all formula TOML files in
   `internal/cmd/install_formulas/`.

3. **Do not replace with an alternative provenance mechanism.**
   Formula authorship history is in git (`git log --follow <formula>`).
   Embedding it in the formula content duplicates what git already
   provides and introduces a maintenance burden git does not have.

## Consequences

**Earned properties:**
- Formulas are fully self-contained. No reference to external skill
  paths that may or may not exist in a given deployment context.
- Published formulas carry no references to unpublished private
  skill directories.
- Future formula generation produces cleaner output with no
  dead-reference risk.

**Accepted costs:**
- A reader of a formula step cannot see which skill phase it was
  derived from without checking git history. This is acceptable because
  the agent executing the formula never needs this information, and
  human readers can use `git log --follow`.

## Corpus links

- ADR-015 — formula three-location lifecycle (this ADR removes a
  non-load-bearing annotation from the formula content that ADR-015's
  lifecycle does not depend on)
- `.claude/skills/formula-create/skillmd-mode.md:79` — generator
  directive (removed by this ADR)
- `.claude/skills/formula-create/skillmd-mode.md:137` — generator
  directive (removed by this ADR)
