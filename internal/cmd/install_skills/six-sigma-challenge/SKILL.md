---
name: six-sigma-challenge
description: Challenges the quality of any completed review or assessment to determine what would achieve six-sigma (99.9999%) quality outcome. Works with scored reviews (X/10), qualitative reviews (PASS/FAIL), or unstructured assessments. Identifies gaps, independently discovers missed issues, stress-tests each proposed improvement for feasibility including logical impossibilities, and appends a "Six-Sigma Caveats" section. Use when a review is less than perfect, when the user asks "what would make this perfect," or after any quality gate completes.
---

# Six-Sigma Challenge

Challenge any quality assessment to find the gap between current outcome and perfection.

## Inputs

| Input | Required | Source |
|-------|----------|--------|
| Review file | Yes | First argument, or `todos/ultra-implement/blind_review.md` |
| Target document | Yes | Second argument, or auto-detected (see below) |

**Invocation**: `/six-sigma-challenge <review-file> <target-document>`
**Default**: `/six-sigma-challenge` (uses ultra-implement defaults)

**Auto-detection for target document** (when no second argument):
1. Read `todos/ultra-implement/investigation_report.md` and extract the `Problem File:` path
2. If that file exists, use it as the target document
3. Fallback: `todos/ultra-implement/COMPLETION.md`

This ensures the caveats append to the original problem file regardless of what `/ultra-implement` was invoked with.

## Phase 1: Extract Quality Gaps

Read the review file. Identify every area where the outcome falls short of perfection.

**For scored reviews** (X/10 criteria): Record each criterion below 10/10, the deficit, and the reviewer's reasoning. Rank by deficit size.

**For qualitative reviews** (PASS/FAIL, APPROVED with notes): Extract every caveat, concern, minor finding, suggestion, and "PASS with notes" qualification. Each is a gap.

**For unstructured assessments**: Identify every hedging phrase ("mostly," "generally," "with the exception of"), every risk noted, every follow-up suggested. Each is a gap.

## Phase 2: Analyze Each Gap

For each gap, determine:

| Field | Description |
|-------|-------------|
| **Current state** | What was delivered (quote the review) |
| **Ideal state** | What perfection would concretely look like |
| **Gap type** | See table below |

Gap types (use the most specific that fits):

| Type | Applies to | Example |
|------|-----------|---------|
| Fundamental constraint | Any domain | Logical impossibility prevents improvement |
| Implementation gap | Code, systems | Something could be built but wasn't |
| Test/verification gap | Code, processes | Verification is structural when it should be behavioral |
| Operational gap | Deployments, processes | Runtime/environment concern not addressed |
| Environmental gap | Infrastructure | External factor outside fix scope |
| Design gap | UI, architecture | Design decision leaves a weakness |
| Coverage gap | Docs, tests, reviews | Something is partially addressed |

## Phase 3: Independent Gap Discovery

Do NOT rely solely on what the reviewer flagged. Investigate independently.

**For code/system fixes**, ask:
1. **Escape paths**: Can the failure still occur through a path the fix doesn't cover?
2. **Test type mismatch**: Are tests structural (file contains X) when behavioral (system does X) is needed?
3. **Sibling vulnerabilities**: Does the same bug class exist in related code that wasn't fixed?
4. **Version/config drift**: Are there hardcoded values that could silently drift?
5. **Scope gaps**: Were any clauses in the original spec not implemented?

**For any domain**, ask:
1. **Unstated assumptions**: What does the fix assume that could be wrong?
2. **Failure mode coverage**: What failure modes exist beyond the one that was fixed?
3. **Dependency fragility**: Does the fix depend on something that could change?
4. **Observability**: If the fix silently fails, would anyone know?

**Read the actual implementation** (code, config, infrastructure) to understand enforcement boundaries. Do not reason purely from the review document — the review may have missed things or mischaracterized enforcement strength.

## Phase 4: Feasibility Challenge

For EACH proposed "10/10 ideal," actively try to disprove it using this procedure:

### Step 4.1: Describe the ideal concretely

Write out what the improvement would look like as a concrete implementation. What changes? What's the mechanism? What are the steps?

### Step 4.2: Walk through the implementation step by step

For each step in the proposed implementation, ask:
- Does this step require something that doesn't exist yet?
- Does this step require the output of a later step?
- Does this step require the thing it's supposed to gate/prevent/validate?

If YES to any: **logical impossibility detected**. Common patterns:
- **Chicken-and-egg**: X requires Y, but Y requires X (e.g., validating a demo requires the demo to exist, so you can't gate demo existence on validation)
- **Infinite regress**: The solution requires its own output as input
- **Observer effect**: Measuring/gating the thing changes the thing

### Step 4.3: Check architectural ceilings

- Can an LLM agent bypass this by simply not following the workflow?
- Can advisory text be ignored?
- Does runtime validation require the artifact to exist first?
- Is there an enforcement ceiling? (instruction can never reach interlock)

### Step 4.4: Check diminishing returns

Does the improvement cost dramatically more than the residual risk it addresses?

### Step 4.5: Classify

- **Feasible**: Implementable with reasonable effort
- **Partially feasible**: Achievable with caveats or architectural changes
- **Infeasible**: Logically impossible or fundamentally constrained (document the specific constraint)

## Phase 5: Present to User

Present the full analysis conversationally BEFORE appending. Structure as:

1. Table of where the outcome falls short
2. Each gap with current state, ideal state, and feasibility verdict
3. Summary table: gap / impact / feasibility / priority
4. The philosophical pattern if one emerges (e.g., "workflow enforcement vs system enforcement")

Ask: **"Does this analysis match your understanding? Any constraints or insights to add before I append?"**

The user often has domain knowledge that catches logical impossibilities the analysis missed. This step is a critical safety net for Phase 4.

## Phase 6: Append to Target Document

After user confirmation (incorporating any user insights), append:

```markdown
---

# Six-Sigma Caveats

The implemented fix achieved [score/status]. The following analysis documents what
would be required for a theoretical 10/10 (six-sigma, 99.9999% quality outcome)
and why some improvements are fundamentally constrained.

## Gap N: [Title]

[What the current state is vs what perfection requires]

### Feasibility: [Feasible | Partially Feasible | Infeasible]

[For infeasible: the specific logical constraint. For feasible: what implementation
would look like.]

## Summary

| Gap | Impact | Feasibility | Priority |
|-----|--------|-------------|----------|
| ... | ...    | ...         | ...      |

[Closing paragraph: the practical ceiling for this class of problem and what
residual risk must be accepted.]
```

## Anti-Patterns

1. **Accepting the reviewer's frame uncritically** — Challenge both directions: the reviewer may have missed gaps OR proposed infeasible ideals.
2. **Proposing improvements without walking through implementation** — Phase 4 requires concrete step-by-step description, not just category labels.
3. **Reasoning only from the review document** — Read the actual code/system to understand enforcement boundaries. Reviews can mischaracterize strength.
4. **Ignoring the practical ceiling** — Some gaps are fundamental constraints. Name them explicitly rather than proposing impossible improvements.
5. **Skipping user presentation** — The user often has domain insights that catch what automated analysis misses. Always present before appending.
