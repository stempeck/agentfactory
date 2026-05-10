# Integration

## Summary
The gate script changes must integrate with: (1) the existing test infrastructure (drift tests, smoke tests, env var tests), (2) the installation pipeline (embedded hooks in install.go), (3) the agent lifecycle (`.runtime/` directory creation), and (4) the Claude Code Stop hook contract. The integration surface is well-defined and bounded.

## Constraint Check (BEFORE exploring options)
- [x] C1 (tandem modification): Both scripts modified identically
- [x] C2 (drift test): Source and embedded copies kept in sync
- [x] C9 (env var patterns): AF_ROLE/AF_ROOT patterns preserved
- [x] C10 (smoke test): Hook output contract unchanged
- [x] C13 (stdin): Input handling unchanged

## Options Explored

#### Option 1: Modify hooks/ source, copy to install_hooks/, add lock tests
- **Description**: Edit `hooks/quality-gate.sh` and `hooks/fidelity-gate.sh` with the new lock logic (retry + .runtime/ path + mail notification). Copy byte-for-byte to `internal/cmd/install_hooks/`. Add new test functions for lock contention and failure paths.
- **Constraint Compliance**: ✓ C1, ✓ C2, ✓ C9, ✓ C10, ✓ C13
- **Pros**: Follows the established modification pattern; drift test automatically verifies sync; new tests cover the previously untested paths
- **Cons**: Must remember to sync both copies (but drift test catches forgetting)
- **Effort**: Low
- **Reversibility**: Easy

#### Option 2: Refactor lock logic into a shared helper script
- **Description**: Extract the lock logic into a separate `gate-lock.sh` helper script that both gate scripts source. This eliminates the need to keep two identical lock implementations in sync.
- **Constraint Compliance**: ✗ VIOLATES C2 — drift test checks specific files; adding a new file requires test updates. ✗ VIOLATES install.go — embedded FS only knows about 4 files.
- **Pros**: DRY — lock logic in one place
- **Cons**: Requires changes to install.go embed directives, drift test file list, settings.json hook commands, and the scripts themselves. Introduces a sourcing dependency. Over-engineering for a ~10 line change.
- **Effort**: High
- **Reversibility**: Difficult

### Recommendation
**Option 1 (direct modification + test addition)** — follows the established pattern exactly. The drift test is the safety net for sync. New test functions should be added to `internal/cmd/hook_pair_smoke_test.go` or a new `hook_lock_test.go` file.

**REJECTED: Option 2** — over-engineering. The lock logic is ~10 lines. Extracting it to a shared helper requires touching 5+ files for minimal DRY benefit.

### Integration Points

1. **Drift test**: After modifying `hooks/*.sh`, copy to `internal/cmd/install_hooks/*.sh`. `TestInstallHooks_NoDrift` validates.

2. **Smoke test**: `TestHookPair_SequentialSmoke` runs both scripts in a tempdir with no factory — scripts will hit the FACTORY_ROOT check and exit silently before reaching lock logic. This test should continue to pass unchanged.

3. **Env var test**: `TestHookScripts_UseEnvVarFallback` checks script content for env var patterns. New lock logic uses `$FACTORY_ROOT` (already in the script) and `$ROLE` (already in the script). No new env var patterns needed.

4. **.runtime/ directory lifecycle**: Created by `af sling` (formula instantiation). For quality gate on non-formula agents, the script should `mkdir -p` the `.runtime/` directory before attempting lock. This is safe and idempotent.

5. **New lock tests**: Should exercise:
   - Lock acquisition succeeds when no contention
   - Lock contention triggers retry behavior
   - Persistent lock failure triggers mail notification
   - Lock cleanup on normal exit (trap)
   - Lock path uses .runtime/ not /tmp

### Documentation Update
`USING_AGENTFACTORY.md` line 554 documents `/tmp/af-fidelity-gate-$ROLE.lock` and `/tmp/af-quality-gate-$ROLE.lock`. This documentation should be updated to reflect the new `.runtime/` paths.

## Dependencies Produced
- Testing dimension receives the integration point list for test design
- Data dimension's `.runtime/` choice is confirmed as compatible

## Risks Identified
- Forgetting to sync install_hooks/: Severity None — drift test catches this automatically
- New tests failing in CI due to environment differences: Severity Low — tests should use t.TempDir() to avoid environment dependencies
