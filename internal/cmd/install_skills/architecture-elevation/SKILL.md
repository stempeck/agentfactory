---
name: architecture-elevation
description: >
  Validate the architectural ALTITUDE of a stated root cause or proposed fix before peer
  review or implementation. Interrogates the frame itself — whether the concern exists
  because of an abstraction that could be removed — before mapping enforcement sites
  within the concern. Tests whether a different boundary could DELETE the concern's
  substrate, not merely reorganize its enforcement. Invoked as a gate from /rootcause-all
  (after synthesis), /design-v5 (between Phase 3 and Phase 4), and /rootcause-review (at
  intake). Can also be invoked standalone.
allowed-tools: "Read,Grep,Glob,Bash,Edit"
version: "3.0.0"
author: "Glenn Stempeck"
---

# Architecture Elevation Skill

## Trigger

`/architecture-elevation <path-to-analysis-or-design-doc>`

Also invoked automatically as a gate from:
- `/rootcause-all` at end of Phase 4 (after solution documented)
- `/design-v5` between Phase 3 (synthesis) and Phase 4 (finalize)
- `/rootcause-review` at Phase 1 (intake)

## Purpose

Peer review asks "are the claims INSIDE the frame true?" This skill asks two questions in order:

1. **Does this concern exist because of an abstraction we introduced?** If the abstraction were removed, would the concern disappear? (Phase 0.)
2. **If the concern is real, is there a single boundary that would DELETE the duplication instead of add to it?** (Phases 1-3.)

The single failure mode this skill defends against: a surface-level fix is accepted because every downstream gate (peer review, six-sigma, impact-analysis) validates claims WITHIN the current frame, and none interrogates the frame. The most common form of that failure: an ADR or prior decomposition encoded the surface-level remedy, and "ADR-compliance" is then mistaken for architectural correctness.

## Bias: subtraction over addition

A proposal that adds a new layer, interface, or abstraction is almost never the answer. The answer is almost always either (a) a boundary that already exists being given the invariant it should have carried all along, such that other layers can be DELETED, or (b) an abstraction that shouldn't have been introduced being removed, such that the concern disappears entirely.

**Rule**: every Frame-lift candidate must either DELETE more lines/layers/duplicated-policy sites than it ADDS (Dimension 1), OR eliminate an entire category of runtime decision or enforcement layer (Dimension 2). A candidate that fails both dimensions is downgraded.

## Inputs

| Input | Required | Description |
|-------|----------|-------------|
| Target doc | Yes | Path to `rootcause_analysis.md`, `design-doc.md`, or equivalent |
| Mode | No | `gate` (append to target doc; block downstream) or `advisory` (sibling file). Default: `gate` |

## Outputs

A single "Architecture Elevation Assessment" section appended (gate mode) or written to a sibling `elevation_assessment.md` (advisory mode), ending in a verdict:

- **Frame artificial** — the concern exists because of an abstraction listed in Phase 0 whose removal eliminates the concern without requiring a fix within it. Requires Phase 0 evidence (named abstraction, removal sketch, counterfactual simpler system). Downstream skills MUST NOT proceed.
- **Frame-lift required** — a concrete consolidation target exists, passes the subtraction gate, and all rejection attempts fail. Downstream skills MUST NOT proceed.
- **Frame-lift offered** — a concrete consolidation target exists but the instance fix is defensible; user decision required.
- **Frame correct** — duplication count ≤ 1 AND no concerning abstraction on Phase 0's list, OR a named constraint (with citation) prevents both abstraction removal and consolidation.

## Critical rule: concrete or nothing

Generic outputs are failures. Every verdict, every proposal, every constraint must name a specific file, type, interface, schema, or language-spec section.

**Category labels are FORBIDDEN as standalone verdicts or standalone proposals.** ✗ "consider a data-layer fix"; ✗ "the type system could help."

**Category labels are REQUIRED** — paired with a concrete boundary — in:
- Phase 0's simpler-system column (e.g., "single-backend store with schema-level invariant enforcement: owner constraint carried at `<file:line>` via schema/type, no adapter-coordination layer")
- Phase 2's Altitude column. Every candidate states its altitude category: *abstraction removal / data plane (schema invariant) / handle separation / compile-time constraint / single interface method / physical partition / control plane (runtime check)*, paired with a concrete file/type/boundary.

A Phase 2 candidate without a stated altitude is rejected. The ban targets fluff-as-verdict, not categorical reasoning.

## Process

### Phase 0: Why does this concern exist?

Before mapping where the policy is enforced, interrogate whether the concern itself is structural — a symptom of an abstraction that could be eliminated. This phase runs FIRST and unconditionally.

**Q0.1 — Concerning abstractions.** List every abstraction in the codebase that EXISTS BECAUSE OF this concern, with file:line. An abstraction "exists because of the concern" if removing it would eliminate the concern's surface area. Examples of abstractions to look for:
- Interfaces with multiple implementations that must "agree" (each implementation is a divergence surface)
- Wrapper/decorator layers that coordinate behavior across implementations
- Contract tests that pin convergence across implementations
- Filter/query types that carry implicit scope semantics decoded at each call site

**Q0.2 — Counterfactual.** For each listed abstraction, answer: if this abstraction were removed, would the concern still exist? If NO, the abstraction's removal is a Frame-lift candidate — surface it as Candidate 0 in Phase 2.

**Q0.3 — Simpler-system comparison.** Name a concrete simpler or flatter system (in this codebase, in the user's history, or in a well-known public codebase) that lacks this concern. State what it does differently with a file:line or spec citation. If no simpler system can be named, record the finding — the absence of a counterfactual is evidence the concern may be genuinely structural, not artificial.

**Output — Concerning Abstractions table** (required, even if empty):

| Abstraction | file:line | Would removal eliminate the concern? (YES/NO + why) | Simpler system that lacks this pattern (named + cited) |
|-------------|-----------|------------------------------------------------------|--------------------------------------------------------|

Any YES in column 3 seeds a Candidate 0 in Phase 2. An empty table is a valid output; it must be PRESENT and EXPLICITLY empty, not omitted.

**Escape path closed**: an empty table must be produced with a one-sentence justification per row attempted, not skipped. Skipping Phase 0 is a skill failure and downstream phases are invalid.

### Phase 1: Ownership Map

For the concern being patched, enumerate **every location** in the codebase where the policy is currently enforced. Read the actual code — do not rely on the target doc's summary.

| Layer | Location (file:line) | Enforcement | Policy carried |
|-------|----------------------|-------------|----------------|
| Producer | ... | mechanical / runtime / instruction / advisory | ... |
| Adapter | ... | ... | ... |
| Consumer | ... | ... | ... |

Enforcement scale: **mechanical** (code makes failure impossible), **runtime** (code detects & blocks), **instruction** (text convention), **advisory** (hopes).

**Observation rule**: if N > 1 locations carry the same invariant, ownership is duplicated and the defect class recurs whenever a new location is added. Flag it. Proceed to Phase 2.

If N ≤ 1 AND the sole site has enforcement ≥ runtime AND Phase 0's Concerning Abstractions table is empty, verdict is `Frame correct`. Skip to Phase 4. If Phase 0 surfaced a concerning abstraction, proceed to Phase 2 regardless of N.

### Phase 2: Elimination candidates

List all candidates. Candidate 0 (if Phase 0 surfaced one) is listed FIRST and evaluated FIRST.

For remaining candidates, consider in preference order, most subtractive first:

0. **Abstraction removal** (from Phase 0) — remove the abstraction whose existence creates the concern; the concern ceases to exist.
1. **Schema / data-model invariant** — the data cannot exist in the forbidden state (NOT NULL, CHECK constraint, enum type, discriminated union, foreign-key constraint).
2. **Handle separation** — distinct types for distinct contexts, so the wrong-context value is unrepresentable in the wrong handle.
3. **Single interface method** — one boundary method all adapters must implement, removing per-adapter policy logic.
4. **Compile-time constraint** — type system feature (sealed trait, exhaustive match, phantom type).

For each candidate, produce:

- **Altitude** — category from the list above (required; candidates without altitude are rejected)
- **Target boundary** — specific file / type / interface / schema (named, not described)
- **Invariant carried** — what the boundary refuses to represent or permit
- **Deletion ledger** — lines, types, interface methods, or duplicated-policy sites the lift would REMOVE (cite each)
- **Addition ledger** — types, methods, migrations, or tests the lift would ADD (cite each)
- **Category elimination** — does the lift eliminate an entire category of runtime decision or enforcement layer? YES/NO with named category

**Subtraction gate (two dimensions — PASS if EITHER is true)**:

- **Dimension 1 (artifacts)**: deletions > additions in lines / types / fields / sites.
- **Dimension 2 (categories)**: the lift eliminates an entire category of runtime decision or an entire enforcement layer. Rubric:
  - Does the lift remove runtime decisions at ≥2 enforcement sites by making the state unrepresentable (schema, type handle, physical partition)?
  - Does the lift collapse an enforcement layer into an invariant that is unrepresentable-in-violation (e.g., handle separation replaces per-caller filter with type-level impossibility)?

A candidate that passes ONLY Dimension 2 with Dimension 1 failing must explicitly NAME the eliminated category in its rationale. A candidate that fails both is a reorganization, not an elevation — downgrade.

If no candidate passes the gate, name the specific architectural constraint preventing elimination.

**Constraint grounding rule** — the constraint must include ONE of:
- **File:line citation** for code-level or schema-level constraints (e.g., "`internal/foo/interface.go:42` — `Store` interface has no X method"), OR
- **URL + verbatim ≥10-word quote** for language / framework / external-system constraints (e.g., "Go spec https://go.dev/ref/spec#... — '<verbatim quote>'")

Ungrounded assertions ("it's too complex," "would be disruptive," "the external store can't do this") are REJECTED. Fall back to `Frame-lift offered` with the candidate flagged incomplete.

### Phase 3: Self-challenge

Before publishing the candidate, attempt to reject it. Every rejection must be grounded per the rules below. An ungrounded rejection is treated as having FAILED — the candidate stands.

**Rejection questions — attempt all five**:

- **Q1 — Does the target boundary exist in this codebase's topology?**
  Grounding: `file:line` of the boundary (exists and can receive the invariant), OR grep output showing absence. If absent AND creating it is expensive (new subsystem, cross-team), rejection succeeds; if absent BUT trivial to create, rejection fails.

- **Q2 — Does the language / framework / runtime support the proposed invariant?**
  Grounding: language/framework spec URL + verbatim ≥10-word quote, OR `file:line` of an equivalent pattern already working in this codebase. "Language can't do X" without a spec quote = ungrounded.

- **Q3 — Does an ADR or user-signed decision decline this migration?** Check `docs/architecture/adrs/` and any migration-declining artifacts.
  **Grounding (MANDATORY for any ADR citation) — ALL SIX**:
  - (a) **ADR title verbatim** — copied from the ADR file's title line (not paraphrased)
  - (b) **Direct quote ≥15 words** from the ADR's Decision or Context section
  - (c) **Verbatim Status line AND Scope / Applies-to statement** (if no explicit scope, quote the first paragraph to establish what the ADR actually addresses)
  - (d) **Scope mapping sentence**: "The ADR's scope is X. The current concern is Y. The mapping is Z."
  - (e) **Assertion with reason**: "The ADR's stated scope [encompasses | does NOT encompass] this case because [reason]."
  - (f) **Altitude check**: Does the ADR's prescribed remedy operate at a STRICTLY LOWER altitude than the current concern? Concern altitude = where the defect shows up (e.g., "adapters diverge at runtime"). ADR altitude = where the ADR prescribes enforcement (e.g., "pin adapters via contract test" = SAME altitude as the divergence — coordinating control planes, not moving to the data plane). Classify the ADR's remedy altitude in one sentence with a verbatim quote of the prescribed fix. If ADR altitude == concern altitude, the ADR is codifying a co-altitude remedy; it is NOT evidence that lifting is declined — it is evidence that the ADR itself may need revisiting. Q3 rejection FAILS the altitude sub-check and does NOT count against the candidate. If ADR altitude < concern altitude (enforces at schema/type/data layer below the concern), Q3 rejection counts.

  Missing any (a)-(f) → rejection FAILS grounding → the candidate stands. **ADR-compliance is not evidence of architectural correctness; ADRs may themselves encode the defect the skill is trying to surface. The altitude test is how the skill avoids inheriting ADR altitude.**

- **Q4 — Does the lift create a worse defect class?**
  Grounding: name the worse class concretely with a specific pattern, not a label. ✗ "tight coupling," "complexity," "technical debt." ✓ "introduces an import cycle between `package X` and `package Y` (cite imports)" or "requires a global mutable in `package foo` accessible from N callers (cite sites)." ✓ "displaces a cross-process trust boundary: server-side filter at `<file:line>` currently keeps unfiltered data from crossing the process/RPC boundary; the proposed lift would require unfiltered data to cross it." Concrete pattern with cited sites is the test.

- **Q5 — Does the lift step OUTSIDE the concern's named domain?**
  Grounding: state the concern's domain in one sentence with a named boundary (subsystem, ownership module, ADR-defined layer, CLAUDE.md-documented package). Claim "outside" only if the lift's target boundary falls outside that named boundary. Fuzzy domain assertions ("feels separate") = ungrounded.
  **Concerning-abstractions exception**: if the lift's target is on Phase 0's Concerning Abstractions list, it is NOT outside the concern — the abstraction IS part of the concern's structural cause, not a separate concern. Q5 rejection FAILS in that case, with the Phase 0 row cited. This prevents Q5 from suppressing the frame-dismantling lift it was meant to protect.

**Grounding audit**. For each question, record whether the rejection succeeds (the question says "yes, drop the candidate") or fails (the question says "no, the candidate stands") and whether the reasoning is grounded:

| # | Question | Rejection succeeds? | Grounding provided | Grounding passes? |
|---|----------|---------------------|--------------------|-------------------|
| Q1 | boundary exists | YES / NO | [citation] | YES / NO |
| Q2 | language support | ... | ... | ... |
| Q3 | ADR declines (with altitude check) | ... | ... | ... |
| Q4 | worse defect class | ... | ... | ... |
| Q5 | outside domain (with Phase 0 exception) | ... | ... | ... |

A rejection **counts against the candidate** only if `rejection-succeeds=YES AND grounding-passes=YES`. Ungrounded successes are demoted to failed rejections.

**Decision**:
- Candidate 0 (abstraction removal) passes all rejections → verdict is `Frame artificial`.
- Any candidate passes with all rejections failing → verdict is `Frame-lift required` (prefer Candidate 0 if both pass).
- ≥1 rejection counts against the only surviving candidate → downgrade to `Frame-lift offered`.
- All candidates fail the subtraction gate AND all fail the rejection survival → verdict is `Frame correct` with grounded constraint.

### Phase 4: Write the assessment

Append (gate mode) or write (advisory mode):

```markdown
---

# Architecture Elevation Assessment

**Date**: YYYY-MM-DD
**Target**: [path]
**Mode**: [gate | advisory]
**Verdict**: [Frame artificial | Frame-lift required | Frame-lift offered | Frame correct]

## Phase 0 — Concerning Abstractions

| Abstraction | file:line | Would removal eliminate the concern? | Simpler system lacking this pattern |
|-------------|-----------|--------------------------------------|--------------------------------------|
| ... | ... | YES / NO + why | ... |

(If empty, state "No abstractions surfaced; concern is not structurally artificial under this analysis" with one-sentence justification.)

## Ownership Map

| Layer | Location | Enforcement | Policy carried |
|-------|----------|-------------|----------------|
| ... | ... | ... | ... |

**Duplication count**: N. [Flagged | Not flagged]

## Elimination Candidates

| # | Altitude | Target boundary | Invariant | Deletion ledger | Addition ledger | Category elimination | Subtraction gate |
|---|----------|-----------------|-----------|-----------------|-----------------|----------------------|------------------|
| 0 | abstraction removal | ... | ... | ... | ... | YES/NO + category | PASS (D1/D2) / FAIL |
| A | ... | ... | ... | ... | ... | ... | ... |

(If no candidate passes the gate, state "No boundary identified" with grounded constraint.)

## Self-Challenge (per surviving candidate)

### Candidate [0 / A / ...]

| # | Question | Rejection succeeds? | Grounding provided | Grounding passes? |
|---|----------|---------------------|--------------------|-------------------|
| Q1 | boundary exists | YES / NO | ... | YES / NO |
| Q2 | language support | ... | ... | ... |
| Q3 | ADR declines (altitude check included) | ... | ... | ... |
| Q4 | worse defect class | ... | ... | ... |
| Q5 | outside domain (Phase 0 exception checked) | ... | ... | ... |

**Counted rejections**: [0 | 1+, which question(s)]

## Verdict

**Verdict**: [Frame artificial | Frame-lift required | Frame-lift offered | Frame correct]

**If Frame artificial**: downstream skills MUST NOT proceed. The concern is a symptom of an abstraction whose removal eliminates it. Required revisions:
1. Remove [named abstraction from Phase 0]; replace with [counterfactual pattern from Q0.3].
2. [further named revisions]

**If Frame-lift required**: downstream skills MUST NOT proceed until the analysis addresses the lift. Required revisions:
1. [named revision]
2. [named revision]

**If Frame-lift offered**: instance fix is defensible because [reason]; lifted fix is materially better because [reason]. Trade-off: [cost of lift vs residual risk of instance fix].

**If Frame correct**: [cite the constraint preventing both abstraction removal (Phase 0) and lift (Phase 2), OR cite Phase 0 empty + duplication count ≤ 1 + sole site enforcement ≥ runtime]
```

## Invocation patterns

Standalone: `/architecture-elevation <target>`.

As a gate, the calling skill branches on verdict:

| Caller | Frame correct | Frame-lift offered | Frame-lift required | Frame artificial |
|--------|---------------|---------------------|---------------------|------------------|
| `/rootcause-all` (end Phase 4) | Proceed | Record as "Alternative considered"; surface user decision | Return to synthesis with candidate as a new concern | Return to Phase 1 with the concern reframed as "remove [abstraction]" |
| `/design-v5` (between Phase 3 and Phase 4) | Proceed to Phase 4 | Record in "Alternatives considered"; proceed | Return to Phase 2; re-run synthesis with the lifted frame | Return to Phase 1 with the concern reframed; regenerate dimensions without the abstraction |
| `/rootcause-review` (Phase 1 intake) | Note and proceed | Confirm analysis captured the alternative, else flag gap | First finding: lift required — review cannot verdict "Proceed" | First finding: concern is artificial — review cannot verdict "Proceed" |

`/rootcause-review` additionally: if no Architecture Elevation Assessment section exists in the target doc, invoke this skill first; the output becomes part of the review input.

## Anti-patterns

| Anti-pattern | Why it fails | What to do instead |
|--------------|--------------|--------------------|
| Skipping Phase 0 | Skill inherits the frame and cannot surface abstraction-as-cause | Phase 0 runs unconditionally; the table is output even when empty |
| Generic "consider a lower layer" | No actionable signal | Name a specific boundary with altitude category, or cite the constraint preventing lift |
| Net-additive lift proposal without category elimination | Adds complexity without deleting anything meaningful | Subtraction gate two dimensions: artifacts OR categories must net-subtract |
| Accepting the analysis's frame as given | Defeats the purpose of the skill | Read the code yourself; build Phase 0 and the Ownership Map from source, not from the target doc's summary |
| ADR-citation laundering | Paraphrased ADR stretched to cover the case under challenge; a scoped decision generalized into a universal principle | Q3's six-part grounding rule: verbatim title, ≥15-word decision quote, verbatim status + scope, explicit scope mapping, assertion with reason, altitude check |
| **ADR-altitude inheritance** | Citing an ADR whose prescribed remedy operates at the same altitude as the concern — treating ADR-compliance as architectural correctness when the ADR itself may codify the surface-level fix | Q3(f) altitude check: the ADR must prescribe enforcement at a strictly LOWER altitude than the concern to count as a rejection. Co-altitude ADRs indicate the ADR may need revisiting, not that the lift is declined |
| Skipping Phase 3 self-challenge | Produces lift proposals that don't survive implementation | Attempt all five questions before any verdict; publish the grounding audit |
| Q5 rejecting concerning-abstraction lifts | The safety rule against scope expansion becomes the gate that prevents frame dismantling | Q5's concerning-abstractions exception: if the lift targets a Phase 0 abstraction, Q5 rejection FAILS |
| Treating category labels as forbidden everywhere | Blocks the vocabulary of root causes — "control plane vs data plane" is a category, and it's how altitude is named | Categories forbidden as standalone verdicts; REQUIRED paired with concrete boundaries in Phase 0 simpler-system and Phase 2 altitude |
| Running this skill post-implementation | Paperwork, not architectural signal | This skill runs BEFORE implementation; for post-hoc gap analysis use /six-sigma-challenge |

## Success criteria

1. Phase 0 Concerning Abstractions table published (empty permitted with per-attempt justification).
2. Ownership Map lists every policy-enforcing location with file:line; duplication count stated.
3. Every Phase 2 candidate has a stated altitude category paired with a concrete boundary.
4. Subtraction gate evaluated on both dimensions (artifacts and categories) for every candidate; passing dimension named in rationale.
5. All five self-challenge questions attempted per surviving candidate; grounding audit table published.
6. Q3 ADR citations include all SIX grounding elements (a-f); altitude check explicit.
7. Q5 checks the Phase 0 concerning-abstractions exception before claiming "outside domain."
8. Verdict is one of the four defined values with explicit rationale.
9. No generic / category-label-only outputs; every claim traces to a named file, type, interface, schema, or spec section.

## Related skills

- `/rootcause-all` — produces the analysis this skill validates the frame of; invokes this skill as a gate at end of Phase 4.
- `/design-v5` — produces the design this skill validates the frame of; invokes this skill as a gate between Phase 3 and Phase 4.
- `/rootcause-review` — validates claims WITHIN the frame; invokes this skill as a pre-review check at Phase 1.
- `/six-sigma-challenge` — validates gap-to-perfection WITHIN the fix; runs after peer review, orthogonal to this skill.
