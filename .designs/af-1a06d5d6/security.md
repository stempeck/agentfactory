# Security

## Summary
The primary security concern is that the current `/tmp` lock paths are predictable and pre-creatable. Any process that creates `/tmp/af-quality-gate-$ROLE.lock` or `/tmp/af-fidelity-gate-$ROLE.lock` before the gate script runs will permanently bypass that gate for the named agent. This is a denial-of-service vector against gate enforcement — not a direct code execution risk, but it undermines the integrity guarantees that gates are supposed to provide.

## Constraint Check (BEFORE exploring options)
- [x] C5 (agent name regex): Lock paths can embed ROLE safely
- [x] C6 (ROLE derivation): Lock must use same ROLE as script computes
- [x] C11 (.runtime/ available): Agent workspace is a less-exposed location than /tmp

## Options Explored

#### Option 1: Move locks from /tmp to agent's .runtime/ directory
- **Description**: Change lock paths to `$FACTORY_ROOT/.agentfactory/agents/$ROLE/.runtime/{quality,fidelity}-gate.lock`. The `.runtime/` directory is under the agent's workspace, owned by the agent's user, and not world-writable.
- **Constraint Compliance**: ✓ C5, ✓ C6, ✓ C11
- **Pros**: Lock path requires knowledge of FACTORY_ROOT (not predictable from outside); `.runtime/` is not world-writable like `/tmp`; permission model matches agent ownership
- **Cons**: If FACTORY_ROOT is compromised, locks are also compromised (but at that point, the entire factory is compromised)
- **Effort**: Low — path string change
- **Reversibility**: Easy

#### Option 2: Keep /tmp but use mktemp for unique directory names
- **Description**: Use `mktemp -d /tmp/af-quality-gate-$ROLE.XXXXXX` to create unpredictable lock paths. Store the path in `.runtime/` for cleanup.
- **Constraint Compliance**: ✓ C5, ✓ C6
- **Pros**: Unpredictable paths; harder to pre-create
- **Cons**: Unique names defeat the locking purpose — each invocation gets its own directory, so there's no mutual exclusion. Would need a two-level scheme (fixed path pointing to unique lock), which is overly complex.
- **Effort**: High
- **Reversibility**: Moderate

#### Option 3: Keep /tmp but validate lock ownership
- **Description**: After `mkdir` succeeds, write the current PID into a file inside the lock directory. On failure, read the PID from the existing lock and check if that process is alive.
- **Constraint Compliance**: ✓ C5, ✓ C6
- **Pros**: Detects pre-created locks (no PID file inside) and stale locks (dead PID)
- **Cons**: Race condition between mkdir and PID write; /tmp is still world-writable; adds complexity
- **Effort**: Medium
- **Reversibility**: Moderate

### Recommendation
**Option 1 (move to .runtime/)** — the simplest and most effective fix. Moving locks out of `/tmp` into the agent's workspace eliminates the predictable-path vulnerability entirely. The `.runtime/` directory is already agent-scoped, not world-writable, and semantically correct (it holds runtime state).

**REJECTED: Option 2** — fundamentally incompatible with the locking purpose (unique names prevent mutual exclusion).

**REJECTED: Option 3** — adds complexity to compensate for /tmp's world-writable nature when the simpler fix is to not use /tmp.

## Threat Model Update
| Threat | Current | After Fix |
|--------|---------|-----------|
| Pre-created lock directory bypasses gate | Exploitable — any process can `mkdir /tmp/af-*-gate-$ROLE.lock` | Mitigated — requires write access to agent workspace |
| Stale lock prevents gate from running | Causes silent pass (bypass) | Causes retry + notification (visible) |
| Lock path guessable | Yes — pattern is `/tmp/af-{type}-gate-$ROLE.lock` | Mitigated — requires knowing FACTORY_ROOT |

## Dependencies Produced
- Data dimension confirms `.runtime/` as lock location
- Integration dimension verifies `.runtime/` directory lifecycle

## Risks Identified
- Factory root compromise exposes lock paths: Severity Low — factory root compromise means all agent state is compromised anyway; this is not a new risk surface
