# API & Interface Design

## Summary
The core interface change is how `agent-gen` resolves the af source tree for template placement. The CLI interface (`af formula agent-gen <name>`) must remain unchanged, but internally the command needs to resolve two distinct paths: the factory root (for workspace artifacts) and the af source root (for .md.tmpl files). The resolution should follow the existing `AF_SOURCE_ROOT` pattern already used by mcpstore.

## Constraint Check
- [x] Templates in AF_SRC: This dimension directly addresses where templates land
- [x] Workspace in factory root: API changes don't affect workspace path logic
- [x] quickstart.sh single entry: CLI interface stays the same
- [x] --delete cleans both: --delete path must also resolve source root
- [x] -o works without source tree: -o never writes files, unaffected
- [x] Self-hosted case: Factory root with agentfactory go.mod IS the source root
- [x] External project case: AF_SOURCE_ROOT provides the path
- [x] Reuse AF_SOURCE_ROOT: Yes, extending existing pattern
- [x] No CLI breaking changes: Interface stays identical

## Options Explored

### Option 1: Shared `config.ResolveSourceRoot(factoryRoot)` function
- **Description**: Extract source root resolution from mcpstore into `internal/config/` as a shared function. The function checks: (1) factory root has `go.mod` with "agentfactory" → use factory root, (2) build-time sourceRoot variable, (3) `AF_SOURCE_ROOT` env var. `agent-gen` calls this to determine where to write `.md.tmpl` files.
- **Constraint Compliance**: All constraints pass
- **Pros**: Reuses existing pattern; single source of truth for source root resolution; testable in isolation; no CLI changes needed
- **Cons**: Requires threading build-time sourceRoot from cmd/af/main.go to config package (new package-level var or init function)
- **Effort**: Medium
- **Reversibility**: Easy — function is additive, old behavior is still available

### Option 2: New `--source-root` flag on agent-gen
- **Description**: Add explicit `--source-root` flag to agent-gen that overrides the template output directory.
- **Constraint Compliance**: Technically passes all, but violates the spirit of "no CLI changes" and "reuse AF_SOURCE_ROOT"
- **Pros**: Explicit, easy to understand
- **Cons**: Another flag to manage; doesn't help agent-gen-all.sh unless flag is threaded through; parallel to AF_SOURCE_ROOT rather than reusing it
- **Effort**: Low
- **Reversibility**: Easy
- REJECTED: Violates "reuse existing AF_SOURCE_ROOT pattern" — creates parallel mechanism

### Option 3: Auto-detect via `go.mod` walk-up
- **Description**: Walk up from the `af` binary's location to find the source tree by looking for `go.mod` with "agentfactory".
- **Constraint Compliance**: Fails for installed binaries where source tree isn't at binary location
- **Pros**: Zero configuration needed
- **Cons**: Fragile — binary may be in `~/.local/bin/`, not in source tree; doesn't work for go-installed binaries
- **Effort**: Low
- **Reversibility**: Easy
- REJECTED: Violates self-hosted + external case constraints — binary location != source location

## Recommendation
**Option 1**: Shared `config.ResolveSourceRoot()` function. It aligns with the existing `AF_SOURCE_ROOT` pattern, requires no CLI changes, and handles both self-hosted and external cases.

### Function Signature
```go
// config/source_root.go
func ResolveSourceRoot(factoryRoot string) (string, error)
```

Resolution order:
1. If `factoryRoot` contains `go.mod` referencing "agentfactory" → return factoryRoot
2. If build-time `sourceRoot` is set → return sourceRoot
3. If `AF_SOURCE_ROOT` env var is set → return its value
4. Return error with message listing all three paths checked

### Changes to formula.go
```go
// In runFormulaAgentGen(), after line 109 (factory root found):
srcRoot, err := config.ResolveSourceRoot(root)
if err != nil {
    return fmt.Errorf("cannot determine af source tree for template placement: %w\n"+
        "Set AF_SOURCE_ROOT to the agentfactory source directory", err)
}

// Line 200 changes from:
tmplDir := filepath.Join(root, "internal", "templates", "roles")
// to:
tmplDir := filepath.Join(srcRoot, "internal", "templates", "roles")
```

## Dependencies Produced
- config.ResolveSourceRoot() must be available before formula.go can use it
- Build-time sourceRoot variable must be accessible from config package
- agent-gen-all.sh must export AF_SOURCE_ROOT=$AF_SRC before calling agent-gen

## Risks Identified
- **Build-time var threading**: sourceRoot is set in cmd/af/main.go via ldflags; need to make it accessible to config package. Severity: Low. Mitigation: Package-level setter function.
- **go.mod detection false positive**: A target project could theoretically have a go.mod referencing "agentfactory" as a dependency. Severity: Low. Mitigation: Check for module path match, not just any reference.
