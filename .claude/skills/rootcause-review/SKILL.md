---
name: rootcause-review
description: >
  Perform scientific peer review of a rootcause_analysis.md document, validating all claims
  with independent verification. Appends review findings without modifying original content.
allowed-tools: "Read,Grep,Glob,Bash,TodoWrite,Edit"
version: "2.0.0"
author: "Glenn Stempeck"
---

# Root Cause Review Skill

## Trigger

This skill is invoked when the user runs `/rootcause-review` followed by a path to a rootcause_analysis.md file.

Example: `/rootcause-review todos/rootcause_analysis.md`

## Purpose

Perform rigorous scientific peer review of a root cause analysis document. This skill independently verifies all claims, tests all evidence, and appends a comprehensive review section to the original document. The goal is to validate or invalidate findings with the same scientific rigor applied to peer-reviewed research.

**Critical Rule**: NEVER modify existing content in the analysis file. Only APPEND the review section at the end.

## Inputs

- **Analysis file path** (required): Path to the rootcause_analysis.md file to review
- The analysis file should contain:
  - Summary of the problem and claimed root cause
  - 5-whys analysis with evidence claims
  - Code references (file:line)
  - Evidence table with sources
  - Proposed solution

## Outputs

- **Updated rootcause_analysis.md**: Original content PRESERVED, with review section APPENDED
- Review section includes:
  - Validation status for each claim
  - Independent verification results
  - Confidence assessment
  - Gaps or errors identified
  - Final verdict

## Review Principles

### Scientific Rigor Standards

1. **Independent Verification**: Don't trust claims - verify them yourself
2. **Reproducibility**: Can you reproduce the same findings from the evidence?
3. **Falsifiability**: Actively try to disprove claims, not just confirm them
4. **Evidence Chain**: Every claim must trace back to verifiable evidence
5. **Null Hypothesis**: Assume the analysis could be wrong until proven right

### Review Categories

Each claim falls into one of these validation states:

| Status | Meaning |
|--------|---------|
| **VALIDATED** | Independently verified with matching evidence |
| **PARTIALLY VALIDATED** | Core finding correct but details differ |
| **INCONCLUSIVE** | Unable to verify (missing access, environment, etc.) |
| **INVALIDATED** | Evidence contradicts the claim |
| **SUPERSEDED** | Claim was correct but situation has changed |

## Process

### Phase 1: Analysis Intake (DO NOT SKIP)

1. **Read the analysis file completely**
   - Extract all claims made
   - Note all evidence sources cited
   - Identify the proposed root cause
   - Note the proposed solution

1b. **Architecture Elevation pre-check (MANDATORY)**

    Peer review validates claims INSIDE the frame of the analysis. It does NOT interrogate the frame itself. Before starting claim validation, verify the frame has been validated by `/architecture-elevation`.

    **If an "Architecture Elevation Assessment" section exists in the analysis file:**

    | Elevation verdict | Reviewer action |
    |-------------------|-----------------|
    | **Frame correct** | Note in review intake and proceed to claim validation |
    | **Frame-lift offered** | Confirm the analysis captured the lift as an "Alternatives considered" entry. If missing, flag as a gap in the review's Gaps section |
    | **Frame-lift required** AND Solution reflects the lift | Proceed — the analysis is already frame-corrected |
    | **Frame-lift required** AND Solution does NOT reflect the lift | **Surface this as the FIRST finding in the review.** Review verdict CANNOT be "Proceed with fix" until the lift is addressed. The reviewer's Recommendation must be "Needs revision" with the lift as the required change. |

    **If NO "Architecture Elevation Assessment" section exists:**

    Use a sub-agent to invoke `/architecture-elevation` on the analysis file in gate mode BEFORE continuing to step 2. The resulting assessment becomes part of the review input. Apply the verdict actions above to its output.

    Rationale: without this pre-check, a surface-level fix can pass peer review because every reviewer-validated claim is true WITHIN the stated (but wrong-altitude) frame. The elevation check is the only gate that interrogates the frame.

2. **Create review todo list**
   ```
   Use TodoWrite to track:
   - [ ] Extract and catalog all claims
   - [ ] Verify each code reference exists
   - [ ] Test each evidence claim independently
   - [ ] Attempt to falsify the root cause theory
   - [ ] Verify proposed solution addresses root cause
   - [ ] Document review findings
   - [ ] Append review section to analysis
   ```

3. **Build claim inventory**
   Create a mental (or written) list of every verifiable claim:
   - Code exists at file:line
   - Function X calls function Y
   - Config is read from location Z
   - Error message contains specific text
   - Behavior differs between A and B

### Phase 2: Code Reference Verification

4. **Verify every file:line reference**
   ```
   For each code reference in the analysis:
   - Read the file at the specified line
   - Verify the code matches what's described
   - Check if line numbers are still accurate (code may have changed)
   - Note any discrepancies
   ```

5. **Verify function/method claims**
   - Does the function exist where claimed?
   - Does it do what the analysis says?
   - Are the call chains accurate?

6. **Check for stale references**
   - Has the code changed since analysis was written?
   - Are referenced functions still in use?
   - Have line numbers shifted?

### Phase 3: Evidence Table Validation

7. **For each row in the Evidence table**
   ```markdown
   | Finding | Source | Evidence | REVIEW STATUS |
   |---------|--------|----------|---------------|
   | [Claim] | [file:line] | [snippet] | [VALIDATED/INVALIDATED/etc] |
   ```

8. **Independent evidence gathering**
   - Don't just read the cited lines - understand the context
   - Search for counter-evidence that might disprove the claim
   - Look for edge cases the analysis might have missed

### Phase 4: 5-Whys Validation

9. **Validate each "Why" independently**
   ```
   For each Why in the chain:
   - Is the question accurately stated?
   - Is the answer supported by evidence?
   - Could there be an alternative explanation?
   - Does the logic flow correctly to the next Why?
   ```

10. **Check for logical gaps**
    - Are there missing steps in the causal chain?
    - Does each Why actually explain the previous one?
    - Is the final root cause truly "root" or is it a symptom?

### Phase 5: Environment Verification (CRITICAL)

11. **Test in the actual environment if available**
    ```bash
    # If Docker/environment is specified in the analysis
    # Reproduce the exact conditions described
    docker exec -u dev container_name bash -c 'verification command'
    ```

12. **Verify environment-specific claims**
    - Database contents match claims
    - Config files contain what's described
    - Error messages reproduce as stated

13. **Attempt falsification**
    - Try to make the "broken" code work
    - Try to break the "working" code
    - Look for conditions where the analysis would be wrong

### Phase 6: Solution Verification

14. **Verify the proposed fix addresses root cause**
    - Does the fix target what the analysis identified?
    - Could the fix introduce new problems?
    - Is there a simpler fix that wasn't considered?

15. **Check for completeness**
    - Are all affected files identified?
    - Are there other places with the same bug pattern?
    - Would the fix prevent recurrence?

16. **Enforce systemic fix standard (MANDATORY)**
    - Is the fix permanent and architectural, or is it a one-off workaround?
    - Does the fix solve the problem for ALL affected cases, not just the one that triggered the analysis?
    - Would a new instance of the same pattern (e.g., a new formula, a new variable) work automatically without additional code changes?
    - If the fix hardcodes specific values (variable names, file paths, etc.), it is NOT systemic - flag it
    - If the fix only addresses the immediate symptom without fixing the underlying architecture, flag it
    - If the analysis proposes both a "quick fix" and a "proper fix", the review MUST recommend only the proper fix

### Phase 7: Document Review Findings

16. **Append review section to analysis file**

    **CRITICAL**: Use Edit tool to APPEND, never modify existing content.

    The review section should follow this structure:

    ```markdown
    ---

    # Peer Review

    **Review Date**: YYYY-MM-DD
    **Reviewer**: Claude Code (rootcause-review skill)
    **Original Analysis Date**: [from document]

    ## Review Summary

    **Overall Verdict**: [VALIDATED | PARTIALLY VALIDATED | INVALIDATED | INCONCLUSIVE]
    **Confidence Level**: [High | Medium | Low]

    [1-2 sentence summary of review findings]

    ## Claim-by-Claim Validation

    ### Claim 1: [Restate the claim]
    **Status**: [VALIDATED | INVALIDATED | etc.]
    **Verification Method**: [How you verified]
    **Evidence**: [What you found]
    **Notes**: [Any discrepancies or concerns]

    ### Claim 2: [Next claim]
    ...

    ## Code Reference Verification

    | Reference | Claimed | Actual | Status |
    |-----------|---------|--------|--------|
    | `file.go:72` | Uses GetConfiguredPrefix() | [what you found] | [status] |

    ## 5-Whys Logic Chain Review

    | Why # | Logical Flow | Evidence Support | Status |
    |-------|--------------|------------------|--------|
    | Why 1 | [assessment] | [evidence check] | [status] |
    | Why 2 | ... | ... | ... |

    ## Environment Verification

    **Environment Tested**: [Docker container name, local, etc.]
    **Tests Performed**:
    1. [Test and result]
    2. [Test and result]

    ## Falsification Attempts

    **Attempted to disprove**: [What you tried]
    **Result**: [Did it succeed or fail to disprove?]

    ## Gaps Identified

    1. [Any gaps in the original analysis]
    2. [Missing considerations]

    ## Errors Found

    1. [Any factual errors in the analysis]
    2. [Incorrect claims]

    ## Solution Assessment

    **Proposed Fix Adequacy**: [Adequate | Partially Adequate | Inadequate]
    **Systemic**: [Yes | No - explain what's missing]
    **Completeness**: [Does it fix all instances, including future ones?]
    **Risk Assessment**: [Any risks with the proposed fix?]

    ### Enforcement Analysis

    Answer each question explicitly:
    1. **Can the original failure still occur after this fix?** [YES/NO and explain how]
    2. **What type of enforcement does the fix use?**
       - [ ] Mechanical interlock (code prevents the action)
       - [ ] Runtime guard (code detects and blocks at runtime)
       - [ ] Instruction/configuration (relies on correct interpretation)
       - [ ] Advisory only (comments, docs, warnings)
    3. **Enforcement Score** (1-10): 10=impossible to fail, 7-9=code guard, 4-6=instruction, 1-3=advisory
    4. **What would a mechanical interlock look like for this problem?**
       [Describe the code-level enforcement that would make the failure mode impossible]

    **GATE**: If Enforcement Score < 7, Recommendation MUST be "Needs revision" and MUST include the mechanical interlock from question 4 as the recommended fix.

    ## Final Verdict

    **Root Cause Claim**: [VALIDATED | INVALIDATED | etc.]
    **Solution Claim**: [VALIDATED | INVALIDATED | etc.]
    **Recommendation**: [Proceed with fix | Needs revision | Investigate further]

    ## Reviewer Notes

    [Any additional observations, concerns, or recommendations]
    ```

## Implementation

When invoked with an analysis file, execute this workflow:

### Step 1: Intake
```bash
# Read the analysis file
Read todos/rootcause_analysis.md

# Create tracking todos
TodoWrite [
  "Extract and catalog all claims from analysis",
  "Verify each code reference (file:line)",
  "Validate evidence table entries",
  "Test 5-whys logic chain",
  "Verify in actual environment",
  "Attempt falsification",
  "Append review section"
]
```

### Step 2: Extract Claims
```
From the analysis, identify:
- All file:line references
- All function/method names mentioned
- All behavior claims ("X does Y")
- All comparison claims ("A differs from B")
- The stated root cause
- The proposed solution
```

### Step 3: Verify Code References
```bash
# For each file:line reference
Read file.go  # at the specified lines

# Verify function exists
Grep "func FunctionName" --path=internal/

# Check if code matches description
# Compare what analysis says vs what code actually does
```

### Step 4: Environment Testing
```bash
# If environment is available
docker exec -u dev container_name bash -c 'verification command'

# Reproduce error
docker exec -u dev container_name bash -c 'command from reproduction steps'

# Verify database/config claims
docker exec -u dev container_name bash -c 'bd config get issue-prefix'
```

### Step 5: Falsification
```bash
# Try to disprove the root cause
# Example: If claim is "function A is wrong", check if function A ever works
Grep "FunctionA" --path=. -A 5

# Look for counter-examples
# Look for cases where the "broken" pattern works elsewhere
```

### Step 6: Append Review
```bash
# Use Edit to append review section
# CRITICAL: old_string should be the last line(s) of the file
# new_string should be those lines PLUS the review section

Edit todos/rootcause_analysis.md
  old_string: "[last lines of file]"
  new_string: "[last lines of file]\n\n---\n\n# Peer Review\n..."
```

## Example Invocation

```
User: /rootcause-review todos/rootcause_analysis.md

Claude: I'll perform a scientific peer review of the root cause analysis.

[Reads rootcause_analysis.md]
[Creates review tracking todos]
[Extracts all claims: 12 code references, 5 evidence entries, 5 whys]
[Verifies each code reference - reads actual files]
[Tests evidence claims in Docker environment]
[Validates 5-whys logic chain]
[Attempts falsification - tries to disprove root cause]
[Documents findings]
[Appends review section to rootcause_analysis.md]
```

## Success Criteria

The skill is complete when:
1. Every claim in the analysis has been independently verified
2. All code references checked against actual codebase
3. Environment testing performed (if environment available)
4. Falsification attempted
5. Review section appended to original document
6. Clear verdict provided (VALIDATED/INVALIDATED/etc.)

## Anti-Patterns to Avoid

1. **Confirmation Bias**: Actively try to disprove, not just confirm
2. **Trusting Line Numbers**: Code changes - verify content, not just location
3. **Skipping Environment Tests**: Real environment reveals truths unit tests miss
4. **Modifying Original Content**: ONLY append - preserve the audit trail
5. **Binary Thinking**: "Partially Validated" is a valid and often accurate verdict
6. **Assuming Accuracy**: Even well-reasoned analysis can be wrong

## Key Learnings from Prior Reviews

### Common Issues Found in Root Cause Analyses

1. **Stale Code References**: Line numbers shift as code evolves
2. **Incomplete Search**: Fix applied to one file but not all affected files
3. **Environment Mismatch**: Analysis done in different environment than production
4. **Missing Edge Cases**: Root cause correct for main case but not all cases
5. **Symptom vs Cause**: What's identified as root cause is actually a symptom

### Red Flags to Watch For

- Claims without file:line references
- "Obviously" or "clearly" without evidence
- Single point of failure assumed when multiple could exist
- Fix addresses symptom without preventing recurrence
- No falsification attempts in original analysis

## Review Checklist

Before completing the review, verify:

- [ ] Every code reference in analysis has been read and verified
- [ ] Every row in Evidence table has been independently checked
- [ ] Each Why in 5-whys has been evaluated for logic and evidence
- [ ] Environment testing performed (or marked INCONCLUSIVE if unavailable)
- [ ] At least one falsification attempt made
- [ ] Clear verdict assigned to root cause claim
- [ ] Clear verdict assigned to solution claim
- [ ] Solution is systemic and permanent (no quick fixes, no hardcoded workarounds)
- [ ] If analysis contains non-systemic fixes, review flags them and recommends the architectural alternative
- [ ] Review section appended (NOT replacing) to original document
- [ ] No modifications made to original analysis content

## Systemic Fix Enforcement (MANDATORY)

After appending the review section, perform a final pass on the analysis's Solution section. If any proposed fix is a quick fix, one-off workaround, or hardcoded patch rather than a permanent systemic solution, you MUST:

1. **Flag it in the review** under Solution Assessment with `**Systemic**: No`
2. **Explain why it's not systemic** (e.g., "hardcodes a specific variable name", "only fixes one formula", "requires manual changes for each new case")
3. **Recommend the architectural alternative** that solves the general case permanently
4. **Update the Recommendation** to "Needs revision" if the only proposed fix is non-systemic

### What makes a fix systemic:

- Works for ALL current and future instances of the pattern (not just the triggering case)
- New additions (formulas, variables, modules) work automatically without code changes
- Changes are in source, deployed through the pipeline, survive rebuilds
- Addresses the architectural gap, not just the immediate symptom

### What makes a fix NON-systemic (reject these):

- Hardcodes specific values (variable names, bead IDs, file paths)
- Labeled "MINIMUM VIABLE FIX" or "quick fix" or "workaround"
- Only fixes the one case that triggered the analysis
- Requires a new code change for each new instance of the same pattern
- Manually patches live state instead of fixing the source
