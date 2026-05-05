# Scalability

## Summary
Scale is not a primary concern for this problem. `agent-gen` is a developer tool run locally, not a hot path. The number of formulas is small (currently 6) and unlikely to exceed ~50. The primary scale consideration is whether the source root resolution adds meaningful latency (it doesn't — it's a single file read).

## Constraint Check
- [x] Templates in AF_SRC: Scale doesn't affect where files go
- [x] Reuse AF_SOURCE_ROOT: Single env var lookup is O(1)
- [x] No CLI breaking changes: No performance-motivated interface changes

## Options Explored

### Option 1: Simple sequential resolution (recommended)
- **Description**: Check go.mod, then build-time var, then env var — sequentially, stopping at first hit. Total cost: one file read + two string checks.
- **Constraint Compliance**: All pass
- **Pros**: Simple; ~1ms total; no caching needed; no complexity
- **Cons**: Reads go.mod on every invocation (negligible)
- **Effort**: Low
- **Reversibility**: Easy

### Option 2: Cache resolution result
- **Description**: Cache the resolved source root path in a temp file or env var for subsequent calls.
- **Constraint Compliance**: All pass but adds unnecessary complexity
- **Pros**: Saves one go.mod read on subsequent calls
- **Cons**: Cache invalidation complexity; stale cache if user moves directories; overkill for microsecond operation
- **Effort**: Medium
- **Reversibility**: Moderate
- REJECTED: Over-engineering — reading go.mod takes <1ms, caching adds complexity for no benefit

## Recommendation
**Option 1**: Simple sequential resolution. No scale concerns at the volumes this tool operates at.

## Dependencies Produced
- None — scale dimension doesn't add requirements.

## Risks Identified
- None — this is a CLI dev tool, not a production service.

## Constraints Identified
- None new.
