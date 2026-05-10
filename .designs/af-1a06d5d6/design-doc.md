# Design: Fix Gate Lock Silent Bypass

## Executive Summary

The quality gate and fidelity gate hook scripts use `mkdir` on a predictable `/tmp` path to prevent concurrent haiku evaluations per agent. When `mkdir` fails — for any reason including pre-created directories, permission issues, or genuine contention — the script emits `{"ok": true}` and exits without evaluating the response. The gate silently passes.

This design relocates the lock from `/tmp` to the agent's `.runtime/` directory (eliminating the predictable-path vulnerability), adds a retry loop with bounded delay (handling transient contention), and sends a mail notification when evaluation is skipped (making bypass events visible). The change affects 2 shell scripts (quality-gate.sh, fidelity-gate.sh) plus their byte-identical embedded copies, with new tests covering the previously untested lock paths.

## Constraints Respected

All proposals in this design respect:
- ✓ C1: Both scripts modified in tandem with identical lock logic
- ✓ C2: install_hooks/ copies remain byte-identical (drift test enforced)
- ✓ C3: All logic expressed in bash
- ✓ C4: Lock prevents concurrent haiku evaluations per agent
- ✓ C5: Agent names are regex-safe for path construction
- ✓ C6: ROLE derivation unchanged (AF_ROLE with basename fallback)
- ✓ C7: Trap cleanup only runs when lock is acquired
- ✓ C8: Gate toggle defaults unchanged (fidelity ON, quality OFF)
- ✓ C9: Env var fallback patterns preserved
- ✓ C10: Smoke test continues passing (output contract unchanged)
- ✓ C11: .runtime/ directory used as lock location
- ✓ C12: No /tmp dependency
- ✓ C13: Stdin handling unchanged
- ✓ C14: All exit paths emit `{"ok": true}` and exit 0

## Problem Statement

Gate hook scripts silently bypass evaluation when lock acquisition fails. The lock path (`/tmp/af-{type}-gate-$ROLE.lock`) is predictable and pre-creatable by any process, enabling permanent gate bypass. No tests cover lock behavior.

## Proposed Design

### Overview

Replace the single-attempt `mkdir` on a predictable `/tmp` path with a 3-attempt retry loop using `mkdir` on an agent-scoped `.runtime/` path, with mail notification to the agent when all retries are exhausted.

### Key Components

1. **Lock path relocation**: `/tmp/af-quality-gate-$ROLE.lock` → `$FACTORY_ROOT/.agentfactory/agents/$ROLE/.runtime/quality-gate.lock` (and equivalent for fidelity gate)
2. **Retry loop**: 3 attempts with 1-second sleep between attempts
3. **Failure notification**: `af mail send "$ROLE" -s "GATE_LOCK_CONTENTION"` on persistent failure
4. **Directory safety**: `mkdir -p` ensures `.runtime/` exists before lock attempt

### Component Dependency Graph

```
Lock path relocation ──► Retry loop ──► Failure notification
                                              │
                         Script sync ◄────────┘
                              │
                         Test additions
```

### Code Change (quality-gate.sh, lines 37-41)

**Before:**
```bash
LOCKFILE="/tmp/af-quality-gate-$ROLE.lock"
if ! mkdir "$LOCKFILE" 2>/dev/null; then
    echo '{"ok": true}'
    exit 0
fi
trap "rmdir $LOCKFILE 2>/dev/null" EXIT
```

**After:**
```bash
AGENT_DIR="$FACTORY_ROOT/.agentfactory/agents/$ROLE"
RUNTIME_DIR="$AGENT_DIR/.runtime"
mkdir -p "$RUNTIME_DIR" 2>/dev/null
LOCKFILE="$RUNTIME_DIR/quality-gate.lock"
LOCK_ACQUIRED=false
for attempt in 1 2 3; do
    if mkdir "$LOCKFILE" 2>/dev/null; then
        LOCK_ACQUIRED=true
        break
    fi
    sleep 1
done
if [ "$LOCK_ACQUIRED" = "false" ]; then
    af mail send "$ROLE" -s "GATE_LOCK_CONTENTION" -m "quality-gate evaluation skipped: lock contention after 3 retries" 2>/dev/null
    echo '{"ok": true}'
    exit 0
fi
trap "rmdir $LOCKFILE 2>/dev/null" EXIT
```

**Equivalent change in fidelity-gate.sh** (lines 44-49), with `fidelity-gate.lock` and `fidelity-gate` in the mail message.

### Data Model

Lock artifact changes from:
- **Location**: `/tmp/` → `.runtime/` (agent-scoped, not world-writable)
- **Format**: Directory (unchanged — `mkdir` is atomic)
- **Lifecycle**: Created by gate script, removed by `trap` on exit (unchanged)
- **Name**: `quality-gate.lock` / `fidelity-gate.lock` (simplified from `af-quality-gate-$ROLE.lock` since ROLE is already in the directory path)

## Cross-Dimension Trade-offs

| Conflict | Resolution | Rationale |
|----------|------------|-----------|
| UX (mail notification) vs Scale (delay budget) | UX wins — always send mail | 0.5s overhead is negligible for async Stop hook; notification value exceeds latency cost |

## Trade-offs and Decisions

### Decisions Made

| Decision | Options Considered | Chosen | Rationale | Reversibility |
|----------|-------------------|--------|-----------|---------------|
| Lock location | /tmp, /tmp+nonce, .runtime/ | .runtime/ | Eliminates predictability; consistent with existing .runtime/ usage | Easy |
| Lock mechanism | mkdir, flock | mkdir | Portable; no new dependencies; already proven in codebase | Easy |
| Contention handling | Silent pass, immediate notify, retry+notify | Retry+notify | Handles transient contention; reports persistent failure | Easy |
| Retry parameters | 1x, 3x1s, 5x1s, adaptive | 3x1s | Covers typical haiku eval time (2-5s); bounded worst case (3s) | Easy |
| Notification channel | Mail only, stderr only, both | Mail only | Consistent with existing gate verdict pattern; agents check mail | Easy |

### Open Questions

None — all decisions are made and all constraints verified.

## Risk Registry

| Risk | Severity | Likelihood | Mitigation | Owner |
|------|----------|------------|------------|-------|
| .runtime/ not existing for non-formula agents | Low | Medium | `mkdir -p` before lock attempt | Implementer |
| Stale lock after agent crash | Low | Low | Existing `trap` handles normal exit; manual `rmdir` for crashes | Operator |
| Retry delay slowing agent | Low | Low | Stop hooks are async; agent already responded | N/A |
| Mail notification failing | Low | Low | Best-effort (stderr redirect); gate still passes per contract | N/A |

## Implementation Plan

### Phase 1: Lock Logic Fix (Effort: Low)

**Deliverables:**
1. Modified `hooks/quality-gate.sh` with new lock logic
2. Modified `hooks/fidelity-gate.sh` with new lock logic
3. Synced `internal/cmd/install_hooks/quality-gate.sh`
4. Synced `internal/cmd/install_hooks/fidelity-gate.sh`

**Acceptance Criteria:**
- [ ] Lock path uses `.runtime/` not `/tmp`
- [ ] Lock failure triggers retry (3 attempts, 1s interval)
- [ ] Persistent failure sends mail notification
- [ ] `mkdir -p` ensures `.runtime/` directory exists
- [ ] Trap set only after successful lock acquisition
- [ ] All exit paths emit `{"ok": true}` and exit 0
- [ ] Drift test passes (byte-identical copies)
- [ ] Smoke test passes
- [ ] Env var test passes

**Dependencies:** None — first phase
**Risks addressed:** Silent pass, predictable lock path

### Phase 2: Test Coverage (Effort: Low)

**Deliverables:**
1. New test functions covering lock acquisition, contention, and failure paths
2. Test verifying lock path uses `.runtime/` not `/tmp`

**Acceptance Criteria:**
- [ ] Lock contention path tested (pre-created lock directory)
- [ ] Lock failure notification tested (af mail send called)
- [ ] Lock path verification tested (not /tmp)
- [ ] Lock cleanup tested (trap removes directory)

**Dependencies:** Phase 1 (scripts must be modified first)
**Risks addressed:** Regression prevention

### Phase 3: Documentation (Effort: Minimal)

**Deliverables:**
1. Updated `USING_AGENTFACTORY.md` lock path references (line 554)

**Acceptance Criteria:**
- [ ] Documentation reflects `.runtime/` paths instead of `/tmp` paths

**Dependencies:** Phase 1
**Risks addressed:** Documentation accuracy

## Appendix: Dimension Analyses

- [API Design](api.md)
- [Data Model](data.md)
- [User Experience](ux.md)
- [Scalability](scale.md)
- [Security](security.md)
- [Integration](integration.md)
- [Conflict Matrix](conflicts.md)
- [Constraint Audit](constraint-audit.md)
- [Dependencies](dependencies.md)
