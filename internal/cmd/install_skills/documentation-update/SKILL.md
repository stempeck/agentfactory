---
name: documentation-update
description: Audits a documentation file line-by-line against the actual codebase, proving every factual claim with source file and line number citations. Produces a structured evidence table, applies corrections for inaccuracies, then verifies corrections are themselves accurate. Use when a user asks to review, audit, refresh, or update a documentation file, or says a doc is outdated.
---

# Documentation Audit and Update

Systematically verify every factual claim in a target documentation file against the actual codebase. No heuristics, no skimming — every line gets a source citation or an inaccuracy flag.

## Workflow

Copy this checklist and track progress:

```
Audit Progress:
- [ ] Phase 1: Read and chunk the target document
- [ ] Phase 2: Launch parallel verification agents (one per chunk)
- [ ] Phase 3: Compile findings and spot-check subagent verdicts
- [ ] Phase 3.5: Cross-reference scan (grep full doc for inaccurate entities)
- [ ] Phase 4: Present findings to user, get approval
- [ ] Phase 5: Apply corrections
- [ ] Phase 5.5: Ripple scan (grep full doc for every changed term)
- [ ] Phase 6: Verify corrections against source code
```

### Phase 1: Read and chunk the target document

Read the target file. Split it into chunks of ~50 lines. Record the line ranges.

The goal is to produce chunks small enough that a verification agent can be exhaustive about every claim in its range, and large enough to avoid excessive agent overhead.

If the user asks about a specific section, chunk that section. Phase 3.5 will catch the same claims appearing elsewhere in the document — you do not need to audit the entire file to get full coverage.

### Phase 2: Launch parallel verification agents

Launch one `Explore` subagent per chunk, **all in parallel**. Each agent gets this mandate:

```
You are auditing <FILE> lines <START>-<END> against the actual codebase at <ROOT>.
For EACH factual claim on each line, find the source code that confirms or denies it.
Read actual source files — do not guess or rely on memory.

For EACH claim, report:
- LINE: the line number
- CLAIM: what the document asserts
- SOURCE: exact file path and line number(s) that confirm or deny it
- VERDICT: ACCURATE, INACCURATE, or PARTIALLY INACCURATE
- EVIDENCE: what the source code actually says (quote relevant code/values)
- FIX: if inaccurate, what the line should say instead

Be exhaustive. Check command names, flag names, flag defaults, file paths,
directory names, field names, struct fields, function behavior, error messages,
example values, and operational descriptions. If a line has no factual claim
(e.g., blank line, section header with no assertion), skip it.
```

**Critical rules for this phase:**
- Every agent must READ source files, not assume.
- Every claim gets a verdict. No "this looks fine" without a source citation.
- Agents must check things that seem obviously correct — those are where errors hide.

### Phase 3: Compile findings and spot-check

Collect all agent results. Build a single evidence table of EVERY inaccuracy and partial inaccuracy:

```
| Line | Claim | Source | Verdict | Fix |
|------|-------|--------|---------|-----|
```

Also note any claims where agents disagreed or couldn't find source evidence.

**Spot-check ACCURATE verdicts on paths and locations.** For every subagent claim that a file path, directory name, or structural location is ACCURATE, independently read the source function that creates or resolves that path. Subagents get things wrong — especially structural claims like "X lives under Y/" where the answer depends on a helper function two calls deep. Do not accept a path verdict without reading the defining code yourself.

### Phase 3.5: Cross-reference scan

For every entity found INACCURATE (file path, directory name, command, flag, config key), grep the **entire** document for other occurrences of that entity. Any match outside the already-audited range gets added to the evidence table with the same verdict and fix.

This catches the most common doc-rot pattern: the same stale claim repeated in multiple sections. Fixing it in one place while leaving it in five others is not a fix.

### Phase 4: Present findings to user

Show the user:
1. Total claims verified and accuracy rate
2. The full inaccuracy table with source citations
3. Ask which fixes to apply (all, some, or none)

Do NOT silently apply fixes. The user may have context about intended-but-unimplemented behavior, future plans, or deliberate simplifications.

### Phase 5: Apply corrections

Apply approved fixes using the Edit tool. Batch independent edits where possible.

### Phase 5.5: Ripple scan

After applying all edits, grep the full document for every term that was changed (old value and new value). For each old-value match still in the document: it's a stale reference that the edit missed. Fix it.

For each new-value match: verify it's consistent with the correction (not a false positive from an unrelated use of the same term).

This is not optional. The most common failure mode is fixing a term in one place while the same stale term survives in N other places.

### Phase 6: Verify corrections

Re-read the full updated document. For each fix applied, confirm:
1. The new text is factually accurate against the source code
2. The edit didn't break surrounding context
3. The fix doesn't introduce a NEW inaccuracy
4. No other section became inconsistent as a consequence of this fix

If a correction is itself wrong, fix it before reporting done.

## Rules

- **No heuristics.** Do not skim the document for "obvious" issues. Verify every line.
- **Source or it didn't happen.** Every verdict needs a file path and line number.
- **Check the boring stuff.** Flag names, default values, file paths, struct fields, example filenames — these are where docs rot.
- **Don't add content.** This skill corrects inaccuracies. It does not add missing documentation, rewrite for style, or restructure sections. If the user wants additions, that's a separate task.
- **Intended behavior is not a bug.** If the doc describes behavior that's designed but not yet implemented, that's a code bug, not a doc bug. Flag it for the user to decide — don't silently remove it.
- **Verify your fixes.** A correction that introduces a new inaccuracy is worse than the original error.
