# Dependency Graph

## File Dependencies

```
security.md ──► data.md (lock location decision driven by security analysis)
                  │
                  ▼
api.md ◄──────── data.md (retry logic uses the lock path from data decision)
  │
  ▼
ux.md ◄──────── api.md (notification fires when API's retry is exhausted)
  │
  ▼
scale.md ◄───── api.md (retry parameters bounded by scale analysis)
  │
  ▼
integration.md ◄── all (test design validates all other decisions)
```

## Component Dependencies

| Component | Depends On | Provides To |
|-----------|------------|-------------|
| Lock path (.runtime/) | Security (move from /tmp), Data (.runtime/ recommendation) | API (where to mkdir), Integration (test paths) |
| Retry loop | API (3x1s parameters), Scale (delay budget) | UX (triggers notification on exhaustion) |
| Mail notification | UX (notification format), API (retry exhaustion trigger) | Integration (test assertion target) |
| Script modifications | Lock path, retry loop, mail notification | Integration (drift test sync) |
| Test additions | Script modifications, Integration (test design) | Acceptance criteria (coverage requirement) |
| Documentation update | Lock path (new paths to document) | — |

## Circular Dependency Check

No circular dependencies. The graph is a DAG:
1. Security → Data (lock location) 
2. Data → API (lock path available for retry logic)
3. API → UX (retry exhaustion triggers notification)
4. API → Scale (retry params within budget)
5. All → Integration (tests validate everything)

## Critical Path (Implementation Order)

1. **Lock path change** (Security + Data) — change `LOCKFILE` variable in both scripts from `/tmp/af-*-gate-$ROLE.lock` to `$FACTORY_ROOT/.agentfactory/agents/$ROLE/.runtime/*-gate.lock`, with `mkdir -p` for `.runtime/` directory
2. **Retry loop** (API + Scale) — replace single `mkdir` attempt with 3-retry loop with 1s sleep
3. **Mail notification** (UX) — add `af mail send` on persistent lock failure
4. **Sync install_hooks/** (Integration) — copy modified scripts to embedded location
5. **Add tests** (Integration) — new test functions for lock paths
6. **Update documentation** — update `USING_AGENTFACTORY.md` lock path references

Steps 1-3 modify the same two files (quality-gate.sh, fidelity-gate.sh).
Step 4 is a file copy.
Step 5 is new test code.
Step 6 is documentation.

## Minimum Viable Implementation

Steps 1-4 are the minimum — they fix the vulnerability and maintain drift compliance. Step 5 (tests) satisfies the acceptance criteria requirement for coverage. Step 6 (docs) is a follow-up concern.
