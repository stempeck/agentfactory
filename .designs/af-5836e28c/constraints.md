# Constraint Verification Checklist

## Constraint: Templates must end up in `$AF_SRC/internal/templates/roles/` for `//go:embed` compilation
- **Prohibits:** Writing .md.tmpl files only to the factory root's internal/ tree when it differs from AF_SRC
- **Requires:** agent-gen must resolve the af source tree path and write templates there
- **Verification:** After agent-gen, check `$AF_SRC/internal/templates/roles/<name>.md.tmpl` exists
- **Relaxation Impact:** If relaxed, templates wouldn't compile into the binary — `af prime` and `af install` would fail to render CLAUDE.md for formula-generated agents

## Constraint: Workspace artifacts must stay in factory root
- **Prohibits:** Moving agents.json, workspace dirs, CLAUDE.md, or settings.json out of the factory root
- **Requires:** `.agentfactory/agents/<name>/`, `.agentfactory/agents.json` remain relative to factory root
- **Verification:** After agent-gen, confirm artifacts exist under factory root's `.agentfactory/`
- **Relaxation Impact:** Would break `af prime`, `af up`, `af install` which all use factory root paths

## Constraint: quickstart.sh remains single entry point
- **Prohibits:** Requiring users to run additional scripts or manual copy steps
- **Requires:** quickstart.sh + agent-gen-all.sh handle everything end-to-end
- **Verification:** Fresh clone → quickstart.sh → agents work (no manual intervention)
- **Relaxation Impact:** Increases onboarding friction; newcomers won't know the workflow

## Constraint: agent-gen --delete must clean both sides
- **Prohibits:** Orphaned templates in AF_SRC or orphaned workspaces in factory root
- **Requires:** --delete removes template from AF_SRC and workspace from factory root
- **Verification:** After `agent-gen --delete`, neither template nor workspace exists
- **Relaxation Impact:** Template orphans accumulate in binary, wasting size and causing confusion

## Constraint: -o (dry run) works without source tree detection
- **Prohibits:** Requiring AF_SOURCE_ROOT for stdout-only output
- **Requires:** -o flag renders template content to stdout without writing any files
- **Verification:** `af formula agent-gen <name> -o` succeeds without AF_SOURCE_ROOT set
- **Relaxation Impact:** Would make dry-run mode less useful for debugging/development

## Constraint: Self-hosted case must work (af source == factory root)
- **Prohibits:** Hard-coding separate paths; assuming source tree is always different from factory root
- **Requires:** When factory root contains `go.mod` with agentfactory, template path == factory root path
- **Verification:** Run agent-gen in the agentfactory repo itself — templates land in correct place
- **Relaxation Impact:** Would break development on agentfactory itself

## Constraint: External project case must work (af source != factory root)
- **Prohibits:** Assuming source tree is always the factory root
- **Requires:** AF_SOURCE_ROOT env var or build-time sourceRoot to locate the separate af source tree
- **Verification:** Run agent-gen from ~/af/myproject/ with AF_SOURCE_ROOT=~/projects/agentfactory — templates go to AF_SOURCE_ROOT
- **Relaxation Impact:** Would break the primary use case described in the problem statement

## Constraint: Reuse existing AF_SOURCE_ROOT pattern
- **Prohibits:** Creating a new env var or config mechanism for the same purpose
- **Requires:** Extending the existing `AF_SOURCE_ROOT` / build-time `sourceRoot` pattern from mcpstore to agent-gen
- **Verification:** Same env var works for both mcpstore Python path resolution and agent-gen template placement
- **Relaxation Impact:** Would create parallel discovery mechanisms, increasing cognitive load

## Constraint: make build still required after template changes
- **Prohibits:** Expecting templates to work without recompilation
- **Requires:** agent-gen output message still says "Run 'make build' to compile the new template"
- **Verification:** User message after agent-gen mentions make build requirement
- **Relaxation Impact:** N/A — this is a fundamental Go embed constraint

## Constraint: No breaking changes to agent-gen CLI interface
- **Prohibits:** Changing required arguments, removing flags, altering exit codes
- **Requires:** Existing `af formula agent-gen <name>` invocations continue to work
- **Verification:** Existing scripts (agent-gen-all.sh) work without modification beyond env var setting
- **Relaxation Impact:** Would break existing automation and documentation
