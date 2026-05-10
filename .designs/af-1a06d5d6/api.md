# API & Interface Design

## Summary
The gate scripts are bash shell scripts invoked as Claude Code Stop hooks. The "API" is the lock acquisition mechanism (currently `mkdir` on a `/tmp` path) and the failure behavior (currently silent pass). The interface changes are internal to the scripts — no CLI flags, subcommands, or user-facing APIs change. The key design question is how lock contention/failure is communicated back to the agent.

## Constraint Check (BEFORE exploring options)
- [x] C1 (tandem modification): Both scripts share lock logic structure — changes apply to both
- [x] C2 (drift test): Only affects hook script content, not the test itself
- [x] C3 (bash only): All options use bash primitives
- [x] C4 (lock purpose): All options preserve the concurrency guard
- [x] C14 (exit contract): All options maintain `{"ok": true}` / `exit 0` on every path

## Options Explored

#### Option 1: Retry loop with mkdir
- **Description**: On `mkdir` failure, retry 2-3 times with 1-second sleeps before giving up. On persistent failure, mail the agent a notification and pass.
- **Constraint Compliance**: ✓ C1, ✓ C2, ✓ C3, ✓ C4, ✓ C14
- **Pros**: Simple bash, no new dependencies, handles transient contention
- **Cons**: Adds 2-3 seconds latency on contention; persistent failure still passes (but with notification)
- **Effort**: Low
- **Reversibility**: Easy

#### Option 2: flock with timeout
- **Description**: Replace `mkdir` lock with `flock -w <timeout> <lockfile>`. flock provides kernel-level file locking with built-in blocking and timeout.
- **Constraint Compliance**: ✓ C1, ✓ C2, ✓ C3, ✓ C4, ✓ C14
- **Pros**: Atomic, handles contention natively, well-understood semantics, stale locks auto-release on process death
- **Cons**: `flock` may not be available on all platforms (missing on some minimal containers); requires a file not a directory; slightly more complex bash (subshell pattern)
- **Effort**: Medium
- **Reversibility**: Easy

#### Option 3: mkdir with immediate mail notification (no retry)
- **Description**: On `mkdir` failure, immediately send mail notification to the agent and pass. No retry, no delay.
- **Constraint Compliance**: ✓ C1, ✓ C2, ✓ C3, ✓ C4, ✓ C14
- **Pros**: Zero latency, agent is informed of skipped evaluation
- **Cons**: No contention handling — every collision means a skipped evaluation with notification
- **Effort**: Low
- **Reversibility**: Easy

### Recommendation
**Option 1 (retry loop with mkdir)** — best balance of simplicity and effectiveness. The retry handles genuine contention (previous evaluation still running), and the mail notification on persistent failure ensures the agent knows when evaluation was skipped. flock (Option 2) is cleaner but adds a platform dependency; the current `mkdir` approach is already portable.

## Dependencies Produced
- Lock failure path requires `af mail send` to be available (already checked earlier in scripts via `command -v af`)
- Lock path construction depends on Data dimension's decision on lock location
- Retry count/interval may interact with Performance dimension

## Risks Identified
- Retry delay (2-3s) slowing down agent responses: Severity Low — gate is async, agent continues
- `af mail send` failing in the lock-failure path: Severity Low — best-effort notification, gate still passes per C14
