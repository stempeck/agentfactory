# Cross-Dimension Conflict Matrix

|              | API | Data | UX | Scale | Security | Integration |
|--------------|-----|------|----|-------|----------|-------------|
| **API**      | -   | ○    | ○  | ○     | ○        | ○           |
| **Data**     | ○   | -    | ○  | ○     | ○        | ○           |
| **UX**       | ○   | ○    | -  | ⚠     | ○        | ○           |
| **Scale**    | ○   | ○    | ⚠  | -     | ○        | ○           |
| **Security** | ○   | ○    | ○  | ○     | -        | ○           |
| **Integration** | ○ | ○   | ○  | ○     | ○        | -           |

Legend: ○ No conflict, ⚠ Tension (trade-off needed), ✗ Direct conflict (resolution required)

## Analysis

The 6 dimensions converge strongly on a single coherent solution. All dimensions independently arrived at compatible recommendations:
- API + Scale: Both chose retry with bounded delay
- Data + Security: Both chose .runtime/ as lock location
- UX: Mail notification fires only after retry exhaustion (compatible with Scale)
- Integration: Follows established patterns (no new architectural components)

## Conflict: UX vs Scale

- **Nature**: The mail notification on persistent lock failure (UX recommendation) adds an `af mail send` call on the failure path, consuming a small amount of additional time beyond the retry delay.
- **Impact**: In the worst case, the failure path is: 3 retries (3s) + mail send (~0.5s) = ~3.5s. Without mail, it would be 3s.
- **Resolution Options**:
  1. UX wins: Always send mail on lock failure, accept the ~0.5s overhead
  2. Scale wins: Skip mail notification, keep pure retry-and-pass behavior
  3. Hybrid: Send mail asynchronously (background `af mail send &`) to avoid blocking
- **Chosen Resolution**: Option 1 (UX wins) — the 0.5s overhead is negligible in context (Stop hooks are async; the agent has already responded). The notification value far outweighs the marginal latency. Background mail (Option 3) adds complexity for a 0.5s saving that is imperceptible. Scale's 6s worst-case budget already includes this.

## Considered But Not Conflicts

| Pair | Why Not A Conflict |
|------|--------------------|
| API ↔ Scale | Both independently chose the same retry parameters (3x1s) |
| Data ↔ Security | Both independently chose .runtime/ as the lock location |
| API ↔ Security | Retry addresses the security concern (silent bypass); they're complementary |
| Integration ↔ All | Integration is pattern-following, not opinionated about implementation choices |
| Scale ↔ Security | Moving lock to .runtime/ has no performance impact vs /tmp |

## Result

One minor tension (UX mail overhead vs Scale delay budget) resolved in favor of UX with documented rationale. No direct conflicts. All dimensions are compatible and ready for synthesis.
