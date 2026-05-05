# Problem Summary

## Core Requirement
`af formula agent-gen` must place generated role templates (`.md.tmpl` files) in the agentfactory source tree's `internal/templates/roles/` directory rather than in the target project's factory root, so that running `quickstart.sh` is sufficient to build and use agents without manual file copying.

## User Needs
- As a newcomer, I need `agent-gen` + `quickstart.sh` to produce a working setup without knowing the internal file layout
- As a developer, I need `agent-gen-all.sh` to regenerate all agents without manual `mv`/`cp` steps
- As a developer, I need the same `af formula agent-gen` command to work both when the af source tree IS the factory root and when it's a separate project

## Constraints (HARD LIMITS - solutions MUST respect these)
- [ ] Templates must end up in `$AF_SRC/internal/templates/roles/` for `//go:embed` compilation
- [ ] Workspace dirs, CLAUDE.md, agents.json, and settings.json must stay in the factory root (target project)
- [ ] `quickstart.sh` must remain the single entry point for build + setup
- [ ] `agent-gen --delete` must still clean up both template files and workspace artifacts
- [ ] `agent-gen -o` (dry run) must continue working without needing source tree detection
- [ ] The solution must work when af source tree == factory root (self-hosted case)
- [ ] The solution must work when af source tree != factory root (external project case)
- [ ] Existing `AF_SOURCE_ROOT` env var pattern (used by mcpstore) should be reused, not a parallel mechanism
- [ ] `make build` must still be required after template changes (templates are compiled into binary)
- [ ] No breaking changes to `agent-gen` CLI interface

## Scope: medium

## Scope Calibration
- Dimensions to analyze: Data, API, Integration, Error Handling, Security, Testing
- Depth per dimension: standard
