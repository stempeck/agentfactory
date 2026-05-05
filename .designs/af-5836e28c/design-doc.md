# Design: Agent-Gen Output Location Fix

## Executive Summary

`af formula agent-gen` currently writes all artifacts — including Go role templates (`.md.tmpl`) — relative to the factory root. When the factory root is a target project separate from the agentfactory source tree, the templates land in the wrong place. They need to be in the af source tree's `internal/templates/roles/` directory for Go's `//go:embed` to compile them into the binary.

This design introduces a shared `config.ResolveSourceRoot()` function that resolves the agentfactory source tree location using a three-tier strategy: (1) check if the factory root itself is the source tree via go.mod, (2) check a build-time embedded path, (3) check the `AF_SOURCE_ROOT` environment variable. This reuses the existing `AF_SOURCE_ROOT` pattern already used by the MCP issue-store server. The `agent-gen` command routes template writes to the resolved source root while keeping all other artifacts (workspace, CLAUDE.md, agents.json) in the factory root.

The change is minimal: one new file in `internal/config/`, small modifications to `formula.go` and `cmd/af/main.go`, and one line added to `agent-gen-all.sh`. No CLI interface changes. No breaking changes.

## Constraints Respected

All proposals in this design respect the following constraints:
- Templates end up in `$AF_SRC/internal/templates/roles/` for `//go:embed`
- Workspace artifacts stay in factory root
- `quickstart.sh` remains the single entry point (already sets `AF_SOURCE_ROOT`)
- `--delete` cleans up templates from source root and workspace from factory root
- `-o` (dry run) works without source tree detection (source root resolution happens after -o early return)
- Self-hosted case works (go.mod check detects factory root as source tree)
- External project case works (`AF_SOURCE_ROOT` env var provides the path)
- Reuses existing `AF_SOURCE_ROOT` pattern from mcpstore
- `make build` still required after template changes
- No CLI breaking changes

## Problem Statement

When `agent-gen` runs from a target project (e.g., `~/af/myproject/`), it writes `.md.tmpl` files to `~/af/myproject/internal/templates/roles/` instead of the agentfactory source tree (e.g., `~/projects/agentfactory/internal/templates/roles/`). Users must manually copy these files before running `make build`. The goal: eliminate this manual step so that `quickstart.sh` alone is sufficient.

## Proposed Design

### Overview

Add a source root resolution layer that separates "where templates go" (af source tree) from "where workspace artifacts go" (factory root). The resolution reuses the existing `AF_SOURCE_ROOT` pattern and adds go.mod auto-detection for the self-hosted case.

### Key Components

1. **`config.ResolveSourceRoot()`** — Resolves the af source tree path
2. **`config.isAgentFactorySourceTree()`** — Validates a directory is the af source tree via go.mod
3. **`formula.go` changes** — Routes template writes to source root, keeps workspace writes in factory root
4. **`agent-gen-all.sh` change** — Exports `AF_SOURCE_ROOT=$AF_SRC`

### Component Dependency Graph

```
isAgentFactorySourceTree() ──► ResolveSourceRoot() ──► formula.go (create/delete)
                                      ▲
                           SetBuildSourceRoot() ◄── cmd/af/main.go
```

### Interface

No CLI changes. The existing interface `af formula agent-gen <name>` continues to work. The only new environmental input is the existing `AF_SOURCE_ROOT` env var, which `agent-gen-all.sh` and `quickstart.sh` set automatically.

### Data Model

No new storage. The go.mod file (already present in the source tree) serves as the detection signal. The check reads the `module` line and verifies it contains "agentfactory".

## Cross-Dimension Trade-offs

| Conflict | Resolution | Rationale |
|----------|------------|-----------|
| Security (validate all paths) vs UX (minimal friction) | Security wins: validate go.mod for all paths including AF_SOURCE_ROOT | Writing templates to wrong dir is the exact problem we're solving; validation cost is one file read |

## Trade-offs and Decisions

### Decisions Made

| Decision | Options Considered | Chosen | Rationale | Reversibility |
|----------|-------------------|--------|-----------|---------------|
| Source root resolution mechanism | Shared function, --source-root flag, binary location walk-up | Shared config.ResolveSourceRoot() | Reuses AF_SOURCE_ROOT pattern; no CLI changes; handles both self-hosted and external cases | Easy |
| Source tree detection | go.mod module check, directory existence check, marker file | go.mod module path check | Precise, uses existing data, no maintenance burden | Easy |
| go.mod validation scope | Validate only auto-detected, validate all, no validation | Validate all paths | Prevents misconfigured AF_SOURCE_ROOT; cost is negligible | Easy |
| -o flag interaction | Resolve source root before or after -o | After -o early return | -o doesn't write templates; requiring source root would break dry-run usage | Easy |

### Open Questions
None — all design decisions are resolved.

## Risk Registry

| Risk | Severity | Likelihood | Mitigation | Owner |
|------|----------|------------|------------|-------|
| Build-time var threading from main.go to config | Low | Low | Package-level setter (same pattern as mcpstore) | Implementer |
| go.mod module path rename | Very Low | Very Low | Detection string "agentfactory" is broad enough | Implementer |
| AF_SOURCE_ROOT not set for external projects | Medium | Medium | Clear error message with fix instructions | Implementer |
| Self-hosted case where factory root == source root | Low | Low | go.mod auto-detection handles this transparently | Implementer |

## Implementation Plan

### Phase 1: Source Root Resolution (Effort: Small)

**Deliverables:**
1. New file `internal/config/source_root.go` with `ResolveSourceRoot()`, `isAgentFactorySourceTree()`, and `SetBuildSourceRoot()`
2. New file `internal/config/source_root_test.go` with unit tests
3. One-line addition to `cmd/af/main.go`: `config.SetBuildSourceRoot(sourceRoot)`

**Acceptance Criteria:**
- [ ] `ResolveSourceRoot()` returns factory root when it contains agentfactory go.mod
- [ ] `ResolveSourceRoot()` returns AF_SOURCE_ROOT when factory root is not source tree
- [ ] `ResolveSourceRoot()` returns error when neither is available
- [ ] All three resolution paths are validated with go.mod check

**Dependencies:** None — this is the foundation.

### Phase 2: Formula.go Integration (Effort: Small)

**Deliverables:**
1. Update `runFormulaAgentGen()` to use `config.ResolveSourceRoot()` for template path
2. Update `runFormulaAgentGenDelete()` to use `config.ResolveSourceRoot()` for template cleanup
3. Update output messages to show full path when source root differs from factory root
4. Update existing formula tests

**Acceptance Criteria:**
- [ ] Templates written to source root's `internal/templates/roles/`
- [ ] Workspace artifacts written to factory root's `.agentfactory/agents/`
- [ ] Source root resolution happens AFTER -o early return
- [ ] --delete removes template from source root
- [ ] Output messages clearly show where template was written

**Dependencies:** Phase 1 complete.

### Phase 3: Script Updates (Effort: Trivial)

**Deliverables:**
1. Add `export AF_SOURCE_ROOT="$AF_SRC"` to `agent-gen-all.sh`
2. Verify `quickstart.sh` already exports `AF_SOURCE_ROOT` (it does, line 419)

**Acceptance Criteria:**
- [ ] `agent-gen-all.sh` sets AF_SOURCE_ROOT before calling agent-gen
- [ ] `quickstart.sh` continues to work (no changes needed)

**Dependencies:** Phase 2 complete (for end-to-end testing).

## Appendix: Dimension Analyses
- [API Design](api.md)
- [Data Model](data.md)
- [User Experience](ux.md)
- [Scalability](scale.md)
- [Security](security.md)
- [Integration](integration.md)
- [Conflict Matrix](conflicts.md)
- [Constraint Audit](constraint-audit.md)
- [Dependencies](dependencies.md)
