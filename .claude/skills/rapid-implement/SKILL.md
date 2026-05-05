---
name: rapid-implement
description: Lean, adaptive implementation skill that classifies task complexity and scales process accordingly. Uses native Claude Code sub-agents and checkpoints for speed and token efficiency while maintaining quality gates.
disable-model-invocation: true
---

# Rapid Implement

Implement the assigned task with adaptive rigor. Process weight scales to task complexity.

## Phase 0: Classify (< 1 minute)

Read the task requirements. Classify complexity:

| Signal | Trivial | Moderate | Complex | Epic |
|--------|---------|----------|---------|------|
| Files touched | 1-2 | 2-4 | 5-10 | 10+ |
| New models/tables | 0 | 0-1 | 1-3 | 3+ |
| Concurrency needed | No | No | Maybe | Yes |
| Cross-component | No | No | Yes | Yes |
| Design decisions | 0 | 1-2 | 3-5 | 5+ |

**Trivial/Moderate**: Note classification mentally. Skip to Phase 1.

**Complex/Epic**: Write classification to `todos/rapid/PLAN.md`:

```markdown
## Classification: [Complex|Epic]
## Rationale: [1-2 sentences with signals observed]
## Files to modify: [list]
## Key risks: [list]
## Verification: [test command from requirements]

## Spec Checklist (enumerate EVERY item)
### Components/Subcommands/Endpoints:
- [ ] [list each one from the spec]
### Architectural Constraints:
- [ ] [e.g., "must call REST API, not direct DB access"]
### Flags/Options per component:
- [ ] [list each flag from the spec]
```

**GATE 0** (Complex/Epic only): PLAN.md exists with classification AND complete spec checklist. Every subcommand, endpoint, flag, and architectural constraint from the spec must be enumerated. Missing items in the checklist = missing features in the implementation.

## Phase 1: Investigate (Adaptive)

**Trivial**: Read the target file(s) directly. No investigation phase.

**Moderate**: Read target files + related tests. Note patterns to follow.

**Complex**: Use a subagent to investigate:
```
Read [relevant directories], understand the architecture, identify patterns for
[feature type], and report: (1) files to modify, (2) patterns to follow,
(3) potential risks, (4) existing test patterns.
```

**Epic**: Use two parallel subagents:
```
Subagent 1: Investigate the data layer — models, stores, database schema.
            Report: existing patterns, naming conventions, migration approach.

Subagent 2: Investigate the API/service layer — handlers, services, routing.
            Report: existing patterns, middleware, error handling conventions.
```

**Anti-stall rule** (Complex/Epic): Investigation is subordinate to implementation. One subagent pass for Complex, two parallel passes for Epic. If investigation is incomplete after that, proceed with what you have — compile errors teach faster than a third investigation pass.

**GATE 1** (Complex/Epic only): Investigation findings documented in PLAN.md.

## Phase 2: Test First

Write failing tests BEFORE implementing. Test EVERY behavior you will implement:

**Trivial**: Happy path + one error/edge case.

**Moderate**: Happy path + all error cases mentioned in requirements + edge cases. For each validation rule in the spec (URL format, allowed values, required fields), write a dedicated test that verifies the rejection response and status code.

**Complex/Epic**: Happy path + all error cases + edge cases + integration test.

**Coverage rule**: If you implement a code path (e.g., method guard returning 405, nil-check returning error), write a test for it. Untested code paths are invisible to reviewers and judges.

```bash
# Verify tests fail (proves they test something real)
go test ./[path] -run [TestName] -v 2>&1 | tee test_fail_output.txt | tail -10
# Expected: FAIL (compile error or test failure)
```

**CRITICAL**: Save the failing test output to `test_fail_output.txt`. This proves test-first discipline.

**GATE 2**: Tests exist AND fail AND `test_fail_output.txt` shows failure. If all tests pass before implementation, the tests are not testing new code. Fix the tests.

## Phase 3: Implement

Implement the solution. Follow patterns found in investigation (or target files).

Rules:
- Follow existing code conventions exactly (naming, error handling, imports)
- Use package-level types for response/request structs, not inline definitions
- Consolidate duplicate error-return paths into helper functions or early returns
- No over-engineering — implement what the requirements ask, nothing more
- If requirements are ambiguous, pick the simplest interpretation
- **Spec-literal compliance**: If the spec names a specific type or format (UUID, RFC3339, HTTPS URL), implement EXACTLY that. Do not substitute custom alternatives (e.g., use `uuid.New()` not `generateID("prefix-")`)
- **Standard library validation**: Use `url.Parse`/`url.ParseRequestURI` for URLs, `uuid` package for UUIDs, `time.Parse` for timestamps. Never hand-roll prefix checks or regex for standard formats
- **Response field cross-check**: Before moving on, verify every field in the spec's data model appears correctly in your handler response. Missing or misnamed fields are correctness bugs
- **Test naming**: Name each test to directly map to an acceptance criterion (e.g., `TestWebhookCreateRejectsInvalidURL` for "POST rejects invalid URLs"). This makes coverage visible to reviewers
- **Priority ordering**: Implement in architectural dependency order: data structures → business logic → API/handlers → secondary features. Each layer should compile before starting the next
- **Ship over DNF**: A working implementation covering 70% of requirements with passing tests is infinitely better than no implementation. If Phase 0+1 consumed significant time, reduce scope to core requirements and ship working code

**If tests fail**: Read the error. Fix the code. Do not change the test to match wrong behavior.

**If tests fail 3 times on same issue**: Stop. Re-read requirements. Check if the approach is wrong.
Use a subagent to review: `"Review my implementation against the requirements. What am I missing?"`

**GATE 3**: Target tests pass.

## Phase 4: Verify and Save Results

```bash
# Run full suite and SAVE OUTPUT to file
go test ./... -v 2>&1 | tee test_results.txt
```

**CRITICAL**: The `test_results.txt` file MUST exist with full test output. This is a judging artifact. Do NOT skip this.

**If regressions found**: Fix them. Do not skip failing tests. Do not delete tests. Re-run and re-save to `test_results.txt`.

**Concurrency tasks**: If the implementation uses goroutines, channels, or mutexes, run with race detector:
```bash
go test -race ./[path] -v 2>&1 | tee test_results.txt
```

**GATE 4**: Full test suite passes AND `test_results.txt` exists with passing output.

## Phase 5: Commit and Complete

```bash
# Stage all changes
git add -A
git commit -m "[description of what was implemented]"
```

**Complex/Epic only** — before committing, use a subagent for blind review:

```
Review this diff against the original requirements. You have NOT seen the
investigation or implementation process — judge the output on its own merits.

Requirements: [paste from problem.md]
Diff: [paste git diff]

Score 1-10 on: Correctness, Completeness, Quality, Risk.
Minimum passing score: 8/10.
```

**GATE 5** (Complex/Epic): Blind review score >= 8/10. If not → address feedback, re-review.

Push branch when done.

**GATE 6**: All tests pass, `test_results.txt` exists, changes committed and pushed and pull request created.

## Anti-Patterns

1. **Never skip Gate 2** (failing test). A test that passes before implementation proves nothing.
2. **Never weaken a test to make it pass**. Fix the code, not the test.
3. **Never over-classify**. If it's trivial, treat it as trivial. Process overhead kills speed.
4. **Never under-classify**. If it's complex, investigate first. Skipping investigation causes rework.
5. **Never forget test_results.txt**. Save full `go test` output every time. No exceptions.
6. **Never implement untested paths**. If you add a code path (405 guard, nil check, error branch), it MUST have a test. No exceptions.
7. **Never DNF when partial work is possible**. A 70%-complete implementation with passing tests scores non-zero. No code scores 0. Ship something.
8. **Never over-investigate**. One investigation pass for Complex, two parallel for Epic. After that, start coding.

## Gate Summary

| Gate | Applies To | Check | Blocks |
|------|-----------|-------|--------|
| G0 | Complex, Epic | PLAN.md with classification | Phase 1 |
| G1 | Complex, Epic | Investigation documented | Phase 2 |
| G2 | All | Failing test exists | Phase 3 |
| G3 | All | Target tests pass | Phase 4 |
| G4 | All | Full suite passes + test_results.txt saved | Phase 5 |
| G5 | Complex, Epic | Blind review >= 8/10 | Commit |
| G6 | All | Committed + pushed + test_results.txt exists + pull request created | Done |
