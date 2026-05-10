# Problem Summary

## Core Requirement
Gate hook scripts (quality-gate.sh, fidelity-gate.sh) must not silently pass when they fail to acquire their concurrency lock — the failure must be retried, reported, or result in a verdict reflecting the inability to evaluate.

## User Needs
- Agent operators need gate evaluations to actually enforce quality/fidelity standards, not silently skip them
- System administrators need lock paths that cannot be tampered with by external processes
- Developers need test coverage for lock acquisition, contention, and failure paths to prevent regressions

## Constraints (HARD LIMITS - solutions MUST respect these)
- [ ] C1: Both `hooks/quality-gate.sh` and `hooks/fidelity-gate.sh` must be modified in tandem
- [ ] C2: `internal/cmd/install_hooks/` copies must remain byte-identical to `hooks/` (enforced by `install_hooks_drift_test.go`)
- [ ] C3: Scripts are bash — no language change permitted
- [ ] C4: Lock purpose (preventing concurrent haiku evaluations per agent) must be preserved
- [ ] C5: Agent names are regex-constrained to `[a-zA-Z][a-zA-Z0-9_-]*` — lock paths can rely on this
- [ ] C6: `ROLE` is derived from `AF_ROLE` env var with `basename "$(pwd)"` fallback — lock path derivation must use same source
- [ ] C7: The `trap` cleanup must only run when the lock is acquired (current behavior)
- [ ] C8: Existing gate toggle behavior unchanged (fidelity ON by default, quality OFF by default)
- [ ] C9: Existing env var fallback patterns must be preserved (`TestHookScripts_UseEnvVarFallback`)
- [ ] C10: Existing smoke test must continue to pass (`TestHookPair_SequentialSmoke`)
- [ ] C11: Each agent has `.runtime/` directory for session state — available as alternative lock location
- [ ] C12: `/tmp` has `noexec` in container environments — lock mechanism must not depend on execution from `/tmp`
- [ ] C13: Scripts receive event JSON on stdin from Claude Code Stop hook — stdin handling unchanged
- [ ] C14: Scripts must exit with `echo '{"ok": true}'` and `exit 0` on all paths (Claude Code Stop hook contract)

## Scope: medium

## Scope Calibration
- Dimensions to analyze: Data, API, Security, Integration, Performance, Testing
- Depth per dimension: standard
