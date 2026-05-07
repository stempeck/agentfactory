---
name: formula-create
description: >
  Create a new agentfactory formula from a description or SKILL.md file. Generates a properly
  structured TOML formula with steps, dependencies, inputs, and iteration mechanisms.
  When given a SKILL.md, preserves phase gates as separate formula steps with enforcement language.
allowed-tools: "Write(.beads/formulas/*.formula.toml),Read,Glob,Grep"
version: "1.0.0"
author: "Glenn Stempeck"
---

# Formula Create - Generate agentfactory Workflow Formulas

Create new formulas from natural language descriptions, following agentfactory conventions.

## Usage

```
/formula-create "description of what the formula should accomplish"
/formula-create .claude/skills/<name>/SKILL.md
```

## Examples

```
/formula-create "Review a PR for security vulnerabilities and generate a report"
/formula-create "Convert markdown docs to HTML and deploy to S3"
/formula-create .claude/skills/rapid-implement/SKILL.md
```

## Implementation

When invoked with a description, create a formula following these rules:

### 1. Determine Formula Type

**Use step-based workflow (default)** when:
- Steps have sequential dependencies (output of one feeds into next)
- Work must happen in order
- Iteration/looping may be needed

**Use convoy type** only when:
- Multiple agents can work on the SAME input in parallel
- Each leg examines the input from a different perspective
- A synthesis step combines parallel outputs

### 2. Formula Structure

```toml
description = """
One-sentence summary ending with a period.

Remaining paragraphs describe the workflow overview and expected outcomes.
The first sentence (up to the first period) becomes the agent's short description
in agents.json — it MUST be a plain sentence, not a heading or markdown.
"""
formula = "<name>"
version = 1

# Inputs - parameters provided at runtime
[inputs]
[inputs.example_input]
description = "What this input is for"
type = "string"  # string, number, boolean
required = true  # or false with default

[inputs.optional_input]
description = "Optional parameter"
type = "string"
required = false
default = "default-value"

# Steps - sequential workflow with dependencies
[[steps]]
id = "step-id"
title = "Human-readable step title"
description = """
**Entry criteria:** What must be true before this step runs.

**Actions:**
1. First action to take
2. Second action to take
3. Third action to take

**Exit criteria:**
- What must be true when step completes
- Verifiable outcomes
"""

[[steps]]
id = "next-step"
title = "Next step title"
needs = ["step-id"]  # CRITICAL: Enforce sequencing
description = """..."""

# Variables - template substitution
[vars]
[vars.example_var]
description = "Variable description"
required = true
```

### 3. Step Design Rules

**Every step MUST have:**
- `id`: lowercase-kebab-case identifier
- `title`: Short human-readable name
- `description`: Detailed instructions with entry/exit criteria

**Sequential steps MUST have:**
- `needs = ["previous-step-id"]` to enforce ordering

**Step descriptions should include:**
```
**Entry criteria:** [preconditions]

**Actions:**
1. [specific action]
2. [specific action]
...

**Exit criteria:**
- [verifiable outcome]
- [verifiable outcome]
```

### 4. Common Patterns

**Quality Gate with Iteration:**
```toml
[[steps]]
id = "quality-gate"
title = "Assess quality and decide iteration"
needs = ["validation-step"]
description = """
**Entry criteria:** Validation complete.

**Actions:**
1. Run quality checklist
2. Count issues found

3. **Decision logic:**
   - IF all checks pass → proceed to finalize
   - ELSE IF iteration < max_iterations → loop back to review step
   - ELSE → force completion with warnings

**Exit criteria:**
- Quality assessment complete
- Next action determined
"""
```

**Load/Parse Input:**
```toml
[[steps]]
id = "load-input"
title = "Load and parse input"
description = """
**Entry criteria:** Input source available.

**Actions:**
1. Read input from {{input_path}} or {{input_bead}}
2. Parse and extract structured data
3. Store parsed data in step bead for downstream steps

**Exit criteria:**
- Input fully loaded and parsed
- Structured data available for next steps
"""
```

**Finalize/Commit:**
```toml
[[steps]]
id = "finalize"
title = "Finalize and commit results"
needs = ["quality-gate"]
description = """
**Entry criteria:** Quality gate passed.

**Actions:**
1. Write final output to {{output_path}}
2. git add && git commit with descriptive message
3. git push to remote
4. Create PR with summary

**Exit criteria:**
- Output committed and pushed
- PR created
"""
```

### 5. Convoy Formula Structure (Parallel Execution)

Only use when legs genuinely work in parallel on same input:

```toml
formula = "convoy-<name>"
type = "convoy"
version = 1

[prompts]
base = """
Context injected into all legs.
"""

[[legs]]
id = "perspective-1"
title = "First perspective"
focus = "What this leg focuses on"
description = """Instructions for this parallel worker."""

[[legs]]
id = "perspective-2"
title = "Second perspective"
focus = "Different focus area"
description = """Instructions for this parallel worker."""

[synthesis]
title = "Combine results"
description = """Combine all leg outputs into final result."""
depends_on = ["perspective-1", "perspective-2"]
```

### 6. Naming Conventions

- Formula name: `<descriptive-name>` for molecules, `convoy-<name>` for convoys
- Step IDs: `lowercase-kebab-case`
- Input names: `snake_case`
- File: `<formula-name>.formula.toml`

### 7. Output Location

Write the formula to:
```
.beads/formulas/<formula-name>.formula.toml
```

This is the runtime formula directory for the current project. Formulas here are discoverable
by `af sling` and the formula system at runtime.

### 8. Post-Creation

After creating the formula, inform the user:

```
Formula created: .beads/formulas/<name>.formula.toml

To inspect:
  bd formula show <name>           # View formula details

To use immediately (current workspace):
  af sling --formula <name> --var x=y   # Execute the formula

To embed in agentfactory binary:
  make build && make install             # Rebuild with new formula embedded

To test: Create a sample input and run af sling --formula <name> --var input=path
```

### 9. Reference Existing Formulas

Before creating a new formula, examine existing formulas for patterns:

```bash
ls .beads/formulas/                                  # List available formulas
cat .beads/formulas/*.formula.toml                   # View formula patterns
```

**Key reference formulas:**
- `gherkin-breakdown.formula.toml` - Iteration pattern with quality gates
- `factoryworker.formula.toml` - Standard work execution pattern
- `mergepatrol.formula.toml` - Patrol/monitoring pattern
- `design.formula.toml` - Human-in-the-loop review pattern

### 10. SKILL.md Input Mode

When invoked with a path to a SKILL.md file (instead of a prose description), read
and follow the instructions in `skillmd-mode.md` in this skill directory.

**Detection:** If the argument is a file path ending in `SKILL.md`, use this mode.

## Anti-Patterns to Avoid

1. **Convoy with sequential legs** - If legs depend on each other's output, use steps instead
2. **Missing `needs`** - Steps without `needs` may run out of order
3. **Vague descriptions** - Always include specific actions and exit criteria
4. **No iteration mechanism** - For review workflows, add a quality gate
5. **Hardcoded paths** - Use input variables for flexibility
6. **Writing outside .beads/formulas/** - Formulas must be written to `.beads/formulas/`
7. **Claiming completion without verification** - Always verify the formula file exists after creation
8. **Assuming a proposal file exists** - Agents get requirements from beads, which may contain inline text, a file path, or a GitHub issue link. Use source-agnostic language ("requirements" not "proposal")
9. **Omitting invariant steps** - Every work execution formula MUST include all 10 invariant steps from factoryworker: load-context, branch-setup, validate-contract, preflight-tests, self-review, run-tests, self-verify, cleanup-workspace, prepare-for-review, submit-and-exit. Skills don't know about agentfactory architecture — that's formula-create's job to inject
