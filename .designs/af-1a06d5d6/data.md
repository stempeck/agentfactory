# Data Model

## Summary
The "data" in this design is the lock artifact itself — its location, format, and lifecycle. Currently locks are `mkdir`-created directories in `/tmp` with predictable paths. The core data question is: where should locks live, and how should they be structured to resist tampering while preserving the concurrency guard?

## Constraint Check (BEFORE exploring options)
- [x] C4 (lock purpose preserved): All options maintain per-agent mutual exclusion
- [x] C5 (agent name safety): Lock paths embed ROLE, which is regex-safe
- [x] C6 (ROLE derivation): Lock path uses same ROLE variable already computed
- [x] C11 (.runtime/ available): Agent workspace has `.runtime/` for session state
- [x] C12 (noexec /tmp): mkdir-based locks don't require exec — informational only

## Options Explored

#### Option 1: Move lock to `.runtime/` directory
- **Description**: Change lock path from `/tmp/af-quality-gate-$ROLE.lock` to `$AGENT_DIR/.runtime/quality-gate.lock` where `AGENT_DIR` is `$FACTORY_ROOT/.agentfactory/agents/$ROLE`. The `.runtime/` directory is agent-scoped and already used for formula state.
- **Constraint Compliance**: ✓ C4, ✓ C5, ✓ C6, ✓ C11, ✓ C12
- **Pros**: Not predictable to external processes; scoped to agent workspace; already exists when formula is active; consistent with existing `.runtime/` usage pattern
- **Cons**: `.runtime/` may not exist for agents not running formulas (quality gate fires regardless); need `mkdir -p` to ensure parent exists
- **Effort**: Low
- **Reversibility**: Easy — change path string

#### Option 2: Keep in `/tmp` but add random nonce
- **Description**: Generate a per-session nonce (e.g., from `AF_SESSION_ID` or `$RANDOM`) and include it in the lock path: `/tmp/af-quality-gate-$ROLE-$NONCE.lock`
- **Constraint Compliance**: ✓ C4 (partially — concurrent evaluations from same session still blocked, but cross-session pre-creation still possible if nonce is guessable), ✓ C5, ✓ C6
- **Pros**: Minimal change from current approach
- **Cons**: Nonce must be consistent within a session but unpredictable externally — fragile; stale locks from crashed sessions harder to detect; doesn't fundamentally solve the /tmp predictability problem
- **Effort**: Medium
- **Reversibility**: Easy

#### Option 3: Move lock to `.runtime/` with mkdir -p fallback
- **Description**: Same as Option 1, but create `.runtime/` directory if it doesn't exist before attempting lock. This handles quality gate firing for agents without active formulas.
- **Constraint Compliance**: ✓ C4, ✓ C5, ✓ C6, ✓ C11, ✓ C12
- **Pros**: All benefits of Option 1; works for all agents regardless of formula state; `.runtime/` creation is idempotent and harmless
- **Cons**: Creates `.runtime/` as a side effect even for non-formula agents (minor — directory is already gitignored)
- **Effort**: Low
- **Reversibility**: Easy

### Recommendation
**Option 3 (move to .runtime/ with mkdir -p fallback)** — addresses the core vulnerability (predictable /tmp paths) while being robust for all agent configurations. The `mkdir -p` ensures the parent directory exists without failing. The lock path becomes `$FACTORY_ROOT/.agentfactory/agents/$ROLE/.runtime/quality-gate.lock` (and `fidelity-gate.lock`), which is:
- Not predictable to external processes (requires knowing the factory root)
- Scoped to the agent's workspace
- Consistent with existing `.runtime/` usage patterns

**REJECTED: Option 2** — doesn't fundamentally solve the predictability problem; adds complexity without addressing the root cause.

## Dependencies Produced
- API dimension's retry logic must use the new lock path
- Integration dimension must verify `.runtime/` creation doesn't conflict with existing formula state management
- Testing dimension must cover the new lock paths

## Risks Identified
- `.runtime/` directory creation race condition: Severity Low — `mkdir -p` is idempotent
- Stale lock directories in `.runtime/` after agent crash: Severity Low — same risk as current `/tmp` approach; `trap` cleanup handles normal exit
