---
name: improve-agent
description: Improve an agent's formula TOML based on post-execution learnings. Use after a formula run required manual intervention, produced incorrect artifacts, or left cleanup work for the operator. Classifies the failure type, scans for sibling vulnerabilities, selects the appropriate fix pattern, and surgically inserts corrective steps into the formula.
---

# Improve Agent Formula

Surgical improvement of a formula TOML based on observed execution failures or required manual intervention.

## Invocation

`/improve-agent <formula-path>` or `/improve-agent <agent-name>`

If agent-name given, resolve to `$AF_ROOT/.agentfactory/store/formulas/<agent-name>.formula.toml`.

## Phase 1: Gather Evidence

Collect what went wrong. Ask the user if not already clear from conversation context:

1. **What manual work was required after the formula ran?**
2. **Which files/paths were affected?**
3. **What was the expected state vs actual state?**

Record as a structured gap:

```
GAP: <one-line description>
EXPECTED: <what should have happened>
ACTUAL: <what happened instead>
MANUAL FIX: <what the operator had to do>
AFFECTED PATHS: <specific files/directories>
```

## Phase 2: Read and Understand the Formula

Read the full formula TOML. For each step, note:
- What it produces (artifacts, signals, commits)
- Where it writes (paths relative to `$AF_ROOT`)
- What it commits and pushes

**Output** — state explicitly:
- **Insertion point**: Step ID, action number
- **Why here**: What precedes it (must be after X) and what follows it (must be before Y)

## Phase 3: Classify the Failure

Before designing any fix, categorize the gap. State which type:

| Category | Symptoms | Pattern file section |
|----------|----------|---------------------|
| **Artifact pollution** | Unwanted files in PR, wrong paths committed, intermediate outputs visible | `## Artifact Pollution` |
| **Missing step** | Agent skipped work the formula assumed would happen, no enforcement | `## Missing Step` |
| **Wrong output location** | Artifacts written to wrong path (relative vs absolute, variable resolution) | `## Wrong Output Location` |
| **Signal/ordering failure** | Agent didn't send required signal, steps ran out of order, race condition | `## Signal Ordering` |
| **Enforcement gap** | Step instructions exist but agent can bypass without consequence | `## Enforcement Gap` |

State: "This is a **<category>** failure because <one sentence>."

Then read the corresponding section in [PATTERNS.md](./PATTERNS.md) for the fix template.

## Phase 4: Sibling Scan

The same vulnerability rarely exists in only one place. Scan ALL other steps in the formula for the same pattern:

1. **Identify the pattern**: What structural weakness allowed this failure? (e.g., "commit enforcement without cleanup", "signal polling without artifact fallback", "path resolution without absolute prefix")
2. **Scan every step**: For each step in the formula, does it exhibit the same structural weakness?
3. **Record siblings**: List step IDs that share the vulnerability

**Output**: "Sibling vulnerabilities found in steps: [list]" or "No siblings — isolated to step [X]."

If siblings are found, the fix in Phase 5 should address ALL instances, not just the reported one.

## Phase 5: Design the Fix

Using the pattern from PATTERNS.md:

1. Adapt the pattern template to the specific gap
2. Replace placeholder paths/variables with actual formula variables
3. If siblings were found in Phase 4, design a fix that covers all affected steps

### Validation Gate (MANDATORY)

Before finalizing, enumerate the impact. Run mentally or actually:

```
For each path my fix touches:
  - Does this path contain files on the base branch? [YES/NO]
  - If YES: does my fix PRESERVE them? [MUST BE YES]
  - If NO: safe to remove/modify
```

If any base-branch file would be destroyed, STOP and redesign.

## Phase 6: Surgical Insertion

1. Write the new action as a numbered step within the identified insertion point
2. Renumber subsequent actions if needed
3. Update the step's `**Exit criteria:**` to include the new guarantee
4. Do NOT modify other steps unless the gap spans multiple steps (or siblings require it)

### Insertion Template

```toml
N. **<Title describing what this action prevents>:**
    <One sentence explaining WHY this exists — what went wrong without it.>
    ```bash
    <commands>
    ```
```

## Phase 7: Validate (Simulation)

Walk through the fix as if executing it. Produce this checklist — all must pass:

```
[ ] FRESH BRANCH: Runs without error on a branch with no pipeline artifacts
[ ] NO-OP SAFE: Silently passes when the problematic artifacts don't exist
[ ] BASE PRESERVED: Permanent files (CLAUDE.md, configs, settings) untouched
[ ] IDEMPOTENT: Running twice produces the same result
[ ] UNSKIPPABLE: Executing agent cannot misinterpret or skip this action
```

For the UNSKIPPABLE check, attempt these escape paths against your fix:
- **Skip**: Can the agent proceed to the next action without executing this one?
- **Misinterpret**: Can the instruction be read a different way than intended?
- **Context loss**: Is this action far from related actions? (>50 lines = risk)

If any check fails, return to Phase 5 and redesign.

## Phase 8: Present and Apply

Present findings to the user interactively:

1. **Summary**: The gap, classification, and sibling scan results
2. **Proposed changes**: List each insertion/modification with before→after
3. **Validation results**: The Phase 7 checklist (all passing)
4. **Ask**: "Which improvements should I apply?"

Apply accepted changes. Do NOT commit — leave as local modification for user to handle agent regeneration and formula sync.

## Anti-Patterns

| Anti-Pattern | Why it fails | Correct approach |
|--------------|-------------|------------------|
| Designing a fix before classifying the failure | May apply wrong pattern (git cleanup for a signal problem) | Classify first (Phase 3), then select pattern |
| Fixing only the reported instance | Same vulnerability exists in sibling steps — will recur next run | Sibling scan (Phase 4) catches all instances |
| `git rm -r <directory>` to "clean up" | Destroys permanent files that exist on the base branch | Use zero-diff pattern from PATTERNS.md |
| Targeting paths without checking base branch state | May delete agent identity files, configs, or shared resources | Always enumerate impact in Phase 5 validation gate |
| Adding cleanup as a separate formula step | Adds DAG complexity; cleanup belongs in the finalize step | Insert as an action within existing step |
| Fixing the symptom without understanding the flow | Agents commit during intermediate steps — later steps can't undo earlier pushes | Accept earlier commits happened; add corrective action after all agent work |
| Skipping validation simulation | Fix may break when target paths are empty or base branch evolves | Phase 7 checklist is mandatory, not advisory |
| Writing a fix without checking if agent can skip it | Agent may never execute the new action if it's advisory-only | Escape path check in Phase 7 catches this |
