# Constraint Verification Checklist

## C1: Both hook scripts must be modified in tandem
- **Prohibits:** Changing only one script; divergent lock behavior between quality and fidelity gates
- **Requires:** Identical lock acquisition/failure logic in both quality-gate.sh and fidelity-gate.sh
- **Verification:** Diff the lock sections of both scripts after modification; confirm structural parity
- **Relaxation Impact:** Would allow inconsistent gate behavior — one gate silently passes while the other doesn't

## C2: Embedded copies must remain byte-identical to source
- **Prohibits:** Modifying hooks/ without updating internal/cmd/install_hooks/, or vice versa
- **Requires:** After any change to hooks/*.sh, the corresponding internal/cmd/install_hooks/*.sh must be an exact copy
- **Verification:** `TestInstallHooks_NoDrift` will fail if copies diverge
- **Relaxation Impact:** Fresh factory installs would ship outdated hook scripts — the very bug we're fixing wouldn't be deployed

## C3: Scripts must remain bash
- **Prohibits:** Rewriting gates in Go, Python, or any other language
- **Requires:** All lock logic expressed in bash-compatible commands (mkdir, flock, trap, etc.)
- **Verification:** Scripts start with `#!/bin/bash` and use only POSIX/bash builtins + af/jq/claude CLIs
- **Relaxation Impact:** Would open implementation options but break the embedded-script deployment model

## C4: Lock prevents concurrent haiku evaluations per agent
- **Prohibits:** Removing locking entirely; allowing parallel haiku calls for the same agent
- **Requires:** At most one haiku evaluation per agent at any time, across both quality and fidelity gates
- **Verification:** Run both gates simultaneously for the same ROLE and confirm only one haiku call executes
- **Relaxation Impact:** Would cause redundant haiku API costs and potentially race conditions in mail delivery

## C5: Agent names are regex-constrained `[a-zA-Z][a-zA-Z0-9_-]*`
- **Prohibits:** Lock paths that break with special characters (but this won't happen given the constraint)
- **Requires:** Lock path construction can safely embed ROLE without escaping
- **Verification:** ROLE values used in lock paths are always safe for directory/file names
- **Relaxation Impact:** Would require path escaping and more defensive lock path construction

## C6: ROLE derivation from AF_ROLE with basename fallback
- **Prohibits:** Changing how ROLE is determined; using a different identity source for lock paths
- **Requires:** Lock path must use the same ROLE variable already computed in the script
- **Verification:** `TestHookScripts_UseEnvVarFallback` validates the env var pattern
- **Relaxation Impact:** Would break worktree isolation where AF_ROLE is exported by the session manager

## C7: Trap cleanup only runs when lock is acquired
- **Prohibits:** Setting trap before lock acquisition; cleaning up locks that belong to another process
- **Requires:** The `trap ... EXIT` line must only execute after successful lock acquisition
- **Verification:** Code review confirms trap is set only in the success path after mkdir/flock succeeds
- **Relaxation Impact:** Would cause one process to remove another's lock, defeating the concurrency guard

## C8: Gate toggle behavior unchanged
- **Prohibits:** Changing default on/off state; altering toggle file paths or semantics
- **Requires:** Fidelity gate ON by default (.fidelity-gate contains "on"), quality gate OFF by default
- **Verification:** No changes to the toggle-checking code sections of either script
- **Relaxation Impact:** Would change operational behavior for all existing factory installations

## C9: Env var fallback patterns preserved
- **Prohibits:** Removing `AF_ROLE:-` or `AF_ROOT:-` fallback patterns
- **Requires:** Both scripts maintain the `${AF_ROLE:-$(basename "$(pwd)")}` and `${AF_ROOT:-$(af root)}` patterns
- **Verification:** `TestHookScripts_UseEnvVarFallback` tests for these patterns
- **Relaxation Impact:** Would break worktree isolation and manual invocation fallback

## C10: Smoke test continues passing
- **Prohibits:** Changes that cause scripts to emit non-JSON output or non-zero exit codes
- **Requires:** Both scripts always emit `{"ok": true}` on stdout and exit 0
- **Verification:** `TestHookPair_SequentialSmoke` runs both scripts and checks output
- **Relaxation Impact:** Would break Claude Code Stop hook contract — hooks that fail crash the agent session

## C11: .runtime/ directory available for lock storage
- **Prohibits:** Nothing — this is an enabler, not a restriction
- **Requires:** If using .runtime/ for locks, ensure the directory exists before lock acquisition
- **Verification:** Check that .runtime/ is created during agent provisioning (af install) and persists
- **Relaxation Impact:** N/A — this is an option, not a constraint

## C12: /tmp has noexec in container environments
- **Prohibits:** Lock mechanisms that require executing files from /tmp (not relevant for mkdir-based locks)
- **Requires:** Lock mechanism uses only directory/file creation, not execution
- **Verification:** mkdir-based locks don't require exec — this is informational context about the environment
- **Relaxation Impact:** N/A — the current mkdir approach doesn't depend on exec

## C13: Stdin handling unchanged
- **Prohibits:** Reading stdin more than once; changing the order of stdin reads
- **Requires:** `INPUT=$(cat)` reads all stdin exactly once, early in the script
- **Verification:** No changes to the stdin-reading section
- **Relaxation Impact:** Would break event JSON parsing from Claude Code

## C14: Scripts must exit with ok:true and exit 0
- **Prohibits:** Exiting with non-zero codes; emitting non-JSON output; emitting `{"ok": false}`
- **Requires:** Every exit path outputs `echo '{"ok": true}'` and `exit 0`
- **Verification:** All code paths end with the standard exit; no path emits ok:false
- **Relaxation Impact:** Would crash the agent session — Claude Code treats hook failures as fatal

---

## Checkpoint: Can I explain each constraint?

YES — All 14 constraints are understood. The key tension is between C4 (must lock) and the core requirement (must not silently pass on lock failure). The solution must handle lock contention by either retrying or reporting, while keeping all exit paths compliant with C14 (ok:true, exit 0). The lock location should move from predictable /tmp paths to agent-scoped .runtime/ paths (enabled by C11) to address the predictability/pre-creation vulnerability.
