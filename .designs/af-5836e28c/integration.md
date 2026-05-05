# Integration

## Summary
This change touches three integration surfaces: (1) `formula.go` — the Go command that writes templates, (2) `agent-gen-all.sh` — the batch script that calls agent-gen repeatedly, and (3) `quickstart.sh` — the bootstrap script that builds af and provisions agents. The `agent-gen-all.sh` script already has `$AF_SRC` and sets working directory correctly; it just needs to export `AF_SOURCE_ROOT`. The `quickstart.sh` already exports `AF_SOURCE_ROOT` on line 419.

## Constraint Check
- [x] Templates in AF_SRC: integration.go changes route templates there
- [x] Workspace in factory root: No changes to workspace routing
- [x] quickstart.sh single entry: quickstart.sh already sets AF_SOURCE_ROOT
- [x] --delete cleans both: --delete must resolve source root for template removal
- [x] agent-gen-all.sh: Must export AF_SOURCE_ROOT=$AF_SRC
- [x] Reuse AF_SOURCE_ROOT: Same env var used everywhere

## Options Explored

### Option 1: Minimal changes — export env var in agent-gen-all.sh + use in formula.go
- **Description**: 
  - `agent-gen-all.sh`: Add `export AF_SOURCE_ROOT="$AF_SRC"` before the agent-gen loop
  - `formula.go`: Use `config.ResolveSourceRoot()` for template path, keep factory root for workspace path
  - `formula.go` `--delete`: Also use `config.ResolveSourceRoot()` to find template to delete
  - `quickstart.sh`: Already correct (sets AF_SOURCE_ROOT on line 419)
- **Constraint Compliance**: All pass
- **Pros**: Minimal changes; uses existing infrastructure; agent-gen-all.sh already has $AF_SRC; quickstart.sh already exports AF_SOURCE_ROOT
- **Cons**: None significant
- **Effort**: Low
- **Reversibility**: Easy

### Option 2: Make agent-gen-all.sh pass --source-root flag
- **Description**: Add a --source-root flag and thread it through the script.
- **Constraint Compliance**: Violates "reuse AF_SOURCE_ROOT"
- **Pros**: Explicit
- **Cons**: Parallel mechanism; more script changes; doesn't help standalone agent-gen
- **Effort**: Medium
- **Reversibility**: Easy
- REJECTED: Parallel to AF_SOURCE_ROOT

### Option 3: Embed source root in agents.json
- **Description**: Store resolved source root in agents.json so --delete can find it later.
- **Constraint Compliance**: Passes but adds data model complexity
- **Pros**: --delete always knows where templates went
- **Cons**: Couples runtime data to build-time paths; paths go stale on directory moves
- **Effort**: Medium
- **Reversibility**: Moderate
- REJECTED: Unnecessary complexity — --delete can resolve source root the same way --create does

## Recommendation
**Option 1**: Minimal changes. The pieces are already in place:
- `agent-gen-all.sh` knows `$AF_SRC` — just export it as `AF_SOURCE_ROOT`
- `quickstart.sh` already exports `AF_SOURCE_ROOT`
- `formula.go` needs one additional function call to resolve source root

### Changes Required

**`agent-gen-all.sh`** (1 line added):
```bash
# After line 49 (PROJECT="$(pwd)"), add:
export AF_SOURCE_ROOT="$AF_SRC"
```

**`formula.go` `runFormulaAgentGen()`** (3 lines changed):
```go
// After finding factory root (line 109), add:
srcRoot, err := config.ResolveSourceRoot(root)
if err != nil {
    return fmt.Errorf("cannot determine af source tree for template placement: %w\n"+
        "Set AF_SOURCE_ROOT to the agentfactory source directory", err)
}

// Line 200: change root to srcRoot
tmplDir := filepath.Join(srcRoot, "internal", "templates", "roles")

// Line 208: Update output message to show full path when different from factory root
if srcRoot != root {
    fmt.Fprintf(cmd.ErrOrStderr(), "✓ Role template written: %s/%s.md.tmpl\n", tmplDir, agentName)
} else {
    fmt.Fprintf(cmd.ErrOrStderr(), "✓ Role template written: internal/templates/roles/%s.md.tmpl\n", agentName)
}
```

**`formula.go` `runFormulaAgentGenDelete()`** (2 lines changed):
```go
// After finding factory root (line 283), add:
srcRoot, err := config.ResolveSourceRoot(root)
if err != nil {
    return fmt.Errorf("cannot determine af source tree for template cleanup: %w", err)
}

// Line 307: change root to srcRoot
tmplPath := filepath.Join(srcRoot, "internal", "templates", "roles", agentName+".md.tmpl")
```

**`config/source_root.go`** (new file):
```go
package config

func ResolveSourceRoot(factoryRoot string) (string, error) { /* ... */ }
func isAgentFactorySourceTree(dir string) bool { /* ... */ }

// SetBuildSourceRoot is called from cmd/af/main.go to inject the build-time value.
var buildSourceRoot string
func SetBuildSourceRoot(root string) { buildSourceRoot = root }
```

**`cmd/af/main.go`** (1 line added):
```go
config.SetBuildSourceRoot(sourceRoot)
```

## Dependencies Produced
- `config.SetBuildSourceRoot()` must be called before any command runs
- `AF_SOURCE_ROOT` must be documented in USING_AGENTFACTORY.md

## Risks Identified
- **Circular dependency**: cmd/af imports config, config must receive build-time var from cmd/af. Severity: Low. Mitigation: Use a package-level setter function (same pattern as mcpstore).
- **agent-gen-all.sh forgetting export**: If AF_SOURCE_ROOT isn't exported, agent-gen falls back to go.mod check. Severity: Low. Mitigation: Self-hosted case (af source IS the project) works without the env var.

## Constraints Identified
- `config.SetBuildSourceRoot()` must be called in cmd/af/main.go before rootCmd.Execute().
