# Dependency Graph

## Component Dependencies

```
config.isAgentFactorySourceTree()     [Data dimension]
         │
         ▼
config.ResolveSourceRoot()            [API dimension]
    │         │
    ▼         ▼
cmd/af/main.go                     formula.go: runFormulaAgentGen()
(SetBuildSourceRoot)               (template routing)
                                        │
                                        ▼
                                   formula.go: runFormulaAgentGenDelete()
                                   (template cleanup)
                                        │
                                        ▼
                                   agent-gen-all.sh
                                   (exports AF_SOURCE_ROOT)
```

## File Dependencies

| Component | File | Depends On | Provides To |
|-----------|------|------------|-------------|
| isAgentFactorySourceTree() | internal/config/source_root.go | os, strings, filepath (stdlib) | ResolveSourceRoot() |
| ResolveSourceRoot() | internal/config/source_root.go | isAgentFactorySourceTree(), buildSourceRoot var | formula.go (both create and delete paths) |
| SetBuildSourceRoot() | internal/config/source_root.go | (none) | cmd/af/main.go calls it |
| runFormulaAgentGen() | internal/cmd/formula.go | config.ResolveSourceRoot() | Users, agent-gen-all.sh |
| runFormulaAgentGenDelete() | internal/cmd/formula.go | config.ResolveSourceRoot() | Users, agent-gen-all.sh |
| agent-gen-all.sh | agent-gen-all.sh | AF_SOURCE_ROOT env var | Users, quickstart.sh |
| quickstart.sh | quickstart.sh | (already sets AF_SOURCE_ROOT) | Users |

## Circular Dependency Check
No circular dependencies. The dependency graph is a DAG:
1. `isAgentFactorySourceTree()` depends only on stdlib
2. `ResolveSourceRoot()` depends on #1 and a package-level var
3. `formula.go` depends on #2
4. `agent-gen-all.sh` depends on `af` binary (which includes #3)
5. `quickstart.sh` depends on `agent-gen-all.sh` indirectly

## Critical Path (Implementation Order)

1. **config/source_root.go**: Create `isAgentFactorySourceTree()` and `ResolveSourceRoot()` with tests
2. **cmd/af/main.go**: Add `config.SetBuildSourceRoot(sourceRoot)` call
3. **internal/cmd/formula.go**: Update `runFormulaAgentGen()` and `runFormulaAgentGenDelete()` to use `config.ResolveSourceRoot()`
4. **agent-gen-all.sh**: Add `export AF_SOURCE_ROOT="$AF_SRC"` line
5. **Tests**: Unit tests for source_root.go, update existing formula_test.go tests

Steps 1-2 must happen before 3. Step 4 is independent. Step 5 can happen after 1.

## Minimum Viable Implementation
Steps 1-3 alone fix the core problem for the self-hosted case (af source IS factory root). Step 4 is needed for the external project case. Step 5 validates correctness.
