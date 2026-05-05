# Security

## Summary
The primary security concern is path traversal: `AF_SOURCE_ROOT` is an environment variable that controls where `agent-gen` writes files. If set maliciously, it could write `.md.tmpl` files to arbitrary locations. However, the threat model is local developer tooling — the user controls their own environment. The secondary concern is ensuring the go.mod check isn't spoofable in a way that causes template files to be written to an attacker-controlled directory.

## Constraint Check
- [x] Templates in AF_SRC: Must validate the path is actually an af source tree before writing
- [x] Self-hosted case: go.mod check must be robust
- [x] External project case: AF_SOURCE_ROOT value must be validated

## Options Explored

### Option 1: Validate source root before writing (recommended)
- **Description**: After resolving the source root (via go.mod, build-time var, or env var), validate that `$srcRoot/internal/templates/roles/` exists (or can be created) and that the directory looks like an af source tree. This prevents writing to arbitrary paths via a misconfigured AF_SOURCE_ROOT.
- **Constraint Compliance**: All pass
- **Pros**: Catches misconfigured AF_SOURCE_ROOT early with a clear error; prevents writing templates to random directories
- **Cons**: Slightly more validation code
- **Effort**: Low
- **Reversibility**: Easy

### Option 2: No validation, trust the env var
- **Description**: Whatever AF_SOURCE_ROOT says, write there.
- **Constraint Compliance**: Technically passes all constraints
- **Pros**: Simpler code
- **Cons**: Misconfigured AF_SOURCE_ROOT writes templates to wrong location silently
- **Effort**: Low
- **Reversibility**: Easy
- REJECTED: Silent misplacement is the exact problem we're solving — we shouldn't introduce a new variant of it

### Option 3: Require go.mod validation for all paths (including env var)
- **Description**: Even when AF_SOURCE_ROOT is set, verify it contains an agentfactory go.mod.
- **Constraint Compliance**: All pass
- **Pros**: Strongest validation; prevents ALL misconfiguration
- **Cons**: Adds one file read for env-var-sourced paths
- **Effort**: Low
- **Reversibility**: Easy

## Recommendation
**Option 3**: Validate go.mod for all resolution paths. The cost is one file read. The benefit is catching misconfigured AF_SOURCE_ROOT before writing templates to the wrong place. This means `ResolveSourceRoot()` always validates the returned path.

### Validation Logic
```go
func ResolveSourceRoot(factoryRoot string) (string, error) {
    candidates := []struct{ path, source string }{
        {factoryRoot, "factory root"},
        {buildTimeSourceRoot, "build-time source root"},
        {os.Getenv("AF_SOURCE_ROOT"), "AF_SOURCE_ROOT env var"},
    }
    for _, c := range candidates {
        if c.path != "" && isAgentFactorySourceTree(c.path) {
            return c.path, nil
        }
    }
    return "", fmt.Errorf(/* list all checked paths */)
}
```

Every candidate is validated with `isAgentFactorySourceTree()` before being accepted.

## Dependencies Produced
- Validation logic is part of `config.ResolveSourceRoot()` from the API dimension

## Risks Identified
- **Legitimate non-standard source tree**: If someone forks agentfactory and changes the module path, go.mod validation will fail. Severity: Very Low. Mitigation: They can override with a renamed module that still contains "agentfactory" in the path, or the detection can be extended.

## Constraints Identified
- AF_SOURCE_ROOT value must point to a directory with a valid agentfactory go.mod.
