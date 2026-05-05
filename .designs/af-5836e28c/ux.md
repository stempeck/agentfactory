# User Experience

## Summary
The UX improvement is the core motivation for this change. Currently users must manually copy generated templates from the target project to the af source tree — a step that's undiscoverable and error-prone. The desired UX: run `agent-gen` (or `agent-gen-all.sh`), then `quickstart.sh`, and everything works. Error messages when AF_SOURCE_ROOT is missing must clearly tell the user what to do.

## Constraint Check
- [x] quickstart.sh single entry: This dimension ensures the end-to-end UX is seamless
- [x] No CLI breaking changes: Existing commands work; improvement is in output routing
- [x] Self-hosted case: No change in UX — templates already land in the right place
- [x] External project case: AF_SOURCE_ROOT eliminates manual copy steps

## Options Explored

### Option 1: Silent auto-routing with clear error on failure
- **Description**: When AF_SOURCE_ROOT is set (or factory root is the source tree), templates auto-route to the correct location silently. When it's not set and factory root isn't the source tree, emit a clear error explaining what to do.
- **Constraint Compliance**: All pass
- **Pros**: Zero friction for configured users; clear guidance for unconfigured users
- **Cons**: First-time users hit an error if AF_SOURCE_ROOT isn't set; but the error message tells them exactly what to do
- **Effort**: Low
- **Reversibility**: Easy

### Option 2: Auto-routing with verbose output showing both paths
- **Description**: Same as Option 1 but print both template and workspace paths in the success output, so users can see where things went.
- **Constraint Compliance**: All pass
- **Pros**: Transparency — users understand the dual-path routing; aids debugging
- **Cons**: More output — could be noisy for batch operations
- **Effort**: Low
- **Reversibility**: Easy

### Option 3: Interactive prompt when AF_SOURCE_ROOT is missing
- **Description**: When the source root can't be detected, prompt the user to enter it.
- **Constraint Compliance**: Violates autonomous operation — agents can't answer prompts
- **Pros**: User-friendly for interactive use
- **Cons**: Breaks in scripts, CI, and agent automation
- **Effort**: Medium
- **Reversibility**: Easy
- REJECTED: Violates non-interactive constraint (agents run autonomously, scripts run non-interactively)

## Recommendation
**Option 2**: Auto-routing with verbose output. Users need to understand the dual-path model, and showing both paths makes it transparent. The output already prints each artifact's location; we extend this to differentiate "source tree" from "workspace."

### Updated Output Example
```
✓ Formula: design-v3 (workflow, 16 steps, 2 gates)
✓ Agent entry added to .agentfactory/agents.json (formula: design-v3)
✓ Role template written: /home/user/projects/agentfactory/internal/templates/roles/design-v3.md.tmpl
✓ Workspace created: /home/user/af/myproject/.agentfactory/agents/design-v3/
✓ CLAUDE.md written (4.2 KB)
✓ .claude/settings.json written (autonomous)

Agent "design-v3" is ready. Start with: af up design-v3
Run 'make build' to compile the new template into the af binary.
```

Note: template path is now absolute and shows the source tree location, making it clear it's going to a different place than the workspace.

### Error Message for Missing AF_SOURCE_ROOT
```
Error: cannot determine af source tree for template placement

The af source tree is needed to write role templates (internal/templates/roles/).
Checked:
  1. Factory root (/home/user/af/myproject/) — not the af source tree
  2. Build-time source root — not set
  3. AF_SOURCE_ROOT env var — not set

Fix: Set AF_SOURCE_ROOT to the agentfactory source directory:
  export AF_SOURCE_ROOT=/path/to/agentfactory
```

## Dependencies Produced
- Error message format feeds into Integration dimension (agent-gen-all.sh sets AF_SOURCE_ROOT)
- Success output format must be compatible with --delete output

## Risks Identified
- **Absolute path in output**: Shows full paths which could be long. Severity: Low. Mitigation: Already the case for workspace paths.
- **User confusion about dual paths**: Users may not understand why template goes one place and workspace another. Severity: Medium. Mitigation: The output is self-documenting; add a one-liner to USING_AGENTFACTORY.md.

## Constraints Identified
- None new.
