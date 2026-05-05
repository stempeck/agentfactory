# Data Model

## Summary
This problem has minimal data model impact — no new storage, no schemas, no migrations. The only data-relevant change is the path routing for where `.md.tmpl` files land on disk. The existing file formats (Go templates, agents.json, CLAUDE.md) remain unchanged. The key data concern is the go.mod detection heuristic for identifying the af source tree.

## Constraint Check
- [x] Templates in AF_SRC: Addressed by path routing, not data model
- [x] Workspace in factory root: No data model changes needed
- [x] Reuse AF_SOURCE_ROOT: Env var is data input, no storage needed
- [x] Self-hosted case: go.mod detection is a read-only check

## Options Explored

### Option 1: go.mod module path check (recommended)
- **Description**: Read `go.mod` in factory root, parse the `module` line, check if it matches `github.com/stempeck/agentfactory` (or contains "agentfactory" in the module path).
- **Constraint Compliance**: All pass
- **Pros**: Precise — distinguishes the af source tree from projects that merely import it; uses Go standard library (`go/modfile` or simple string parsing)
- **Cons**: Slightly more complex than a simple file existence check
- **Effort**: Low
- **Reversibility**: Easy

### Option 2: Check for `internal/templates/roles/` directory existence
- **Description**: If factory root has `internal/templates/roles/`, assume it's the source tree.
- **Constraint Compliance**: Passes but fragile
- **Pros**: Simple
- **Cons**: False positives if target project happens to have that path; false negatives if directory hasn't been created yet
- **Effort**: Low
- **Reversibility**: Easy
- REJECTED: Too fragile — could match non-af projects with similar structure

### Option 3: Marker file `.agentfactory-source`
- **Description**: Place a marker file in the af source root that agent-gen checks for.
- **Constraint Compliance**: Passes
- **Pros**: Explicit, no false positives
- **Cons**: Extra file to maintain; easy to forget; not self-documenting
- **Effort**: Low
- **Reversibility**: Easy
- REJECTED: Adds maintenance burden for something go.mod already solves

## Recommendation
**Option 1**: go.mod module path check. Simple, precise, uses existing data (go.mod is always present in the source tree). Parse just the first line starting with "module " — no need for full go.mod parser.

### Detection Logic
```go
func isAgentFactorySourceTree(dir string) bool {
    data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
    if err != nil {
        return false
    }
    for _, line := range strings.SplitN(string(data), "\n", 3) {
        if strings.HasPrefix(line, "module ") {
            return strings.Contains(line, "agentfactory")
        }
    }
    return false
}
```

## Dependencies Produced
- This detection logic is used by `config.ResolveSourceRoot()` (from API dimension)

## Risks Identified
- **Module path rename**: If the agentfactory module path changes, detection breaks. Severity: Very Low. Mitigation: The detection string "agentfactory" is broad enough to survive minor path changes.

## Constraints Identified
- None new — this dimension operates within existing data structures.
