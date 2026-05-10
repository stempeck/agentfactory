# Scalability

## Summary
The gate scripts run on every agent response (Stop hook). Scale concerns center on: lock contention frequency with multiple agents, retry delay impact on agent throughput, and lock cleanup under agent crashes. The current single-attempt-and-pass approach was likely designed for scale (don't block the agent), but at the cost of correctness.

## Constraint Check (BEFORE exploring options)
- [x] C4 (lock purpose): Scale optimizations must not remove locking
- [x] C7 (trap only on acquire): Lock cleanup timing must be preserved
- [x] C14 (exit contract): Scale handling cannot change exit behavior

## Options Explored

#### Option 1: Short retry with bounded delay (recommended by API dimension)
- **Description**: 3 retries, 1-second sleep between attempts. Total worst-case delay: 3 seconds per gate, 6 seconds if both gates hit contention simultaneously.
- **Constraint Compliance**: ✓ C4, ✓ C7, ✓ C14
- **Pros**: Bounded delay; handles the common case (previous evaluation finishing); doesn't block indefinitely
- **Cons**: 3 seconds is noticeable but acceptable for a Stop hook (runs asynchronously after response)
- **Effort**: Low
- **Reversibility**: Easy — tune retry count/interval

#### Option 2: No retry, immediate notification
- **Description**: Skip retry entirely. On lock failure, immediately notify and pass.
- **Constraint Compliance**: ✓ C4, ✓ C7, ✓ C14
- **Pros**: Zero latency impact; simplest implementation
- **Cons**: Every contention event means a skipped evaluation — higher miss rate
- **Effort**: Low
- **Reversibility**: Easy

#### Option 3: Adaptive retry based on lock age
- **Description**: Check lock directory mtime. If lock is recent (<10s), retry. If stale (>10s), remove and re-acquire (previous process likely crashed).
- **Constraint Compliance**: ✓ C4, ✓ C7, ✓ C14
- **Pros**: Handles both contention and stale locks; smarter than blind retry
- **Cons**: `stat` on a directory for mtime is platform-dependent; adds complexity; stale lock detection is a separate concern from the retry mechanism
- **Effort**: Medium
- **Reversibility**: Moderate — more moving parts

### Recommendation
**Option 1 (short retry with bounded delay)** — 3 retries at 1-second intervals is a reasonable tradeoff. Haiku evaluations typically complete in 2-5 seconds, so a 3-second retry window covers most contention cases. The 6-second worst case (both gates contending) is acceptable for an asynchronous Stop hook.

**REJECTED: Option 3** — over-engineering for the scope. Stale lock detection via mtime adds platform-dependent complexity. The existing `trap` cleanup on normal exit handles the common case; manual cleanup (e.g., `rmdir`) handles the rare crash case.

## Dependencies Produced
- Retry count (3) and interval (1s) are tunable — API dimension uses these values
- Performance budget: 3s per gate worst-case, 0s best-case

## Risks Identified
- Agent responses delayed by retry loop: Severity Low — Stop hooks are async; agent has already responded
- Lock starvation under very rapid responses: Severity Low — haiku takes 2-5s, Claude Code response cadence is typically 10-30s
