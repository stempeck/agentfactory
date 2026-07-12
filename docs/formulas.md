# Formulas: declarative TOML workflows for Claude Code agents

A **formula** is a declarative workflow definition in TOML. It describes the steps of a job —
their order, their dependencies, their inputs, and the gates that block completion — separately
from the agent that executes them. The agent doesn't memorize its workflow; the `af` runtime
feeds it one step at a time via [`af prime`](agent-lifecycle.md) and advances via `af done`.
That separation is what makes agent behavior repeatable: the workflow lives in a file you can
review and version, not inside a persona prompt that drifts.

## Where formulas live

```
.agentfactory/store/formulas/     # project-local (per repository)
~/.agentfactory/store/formulas/   # global (shared across projects)
```

Files are named `<name>.formula.toml`. Agentfactory ships with formulas for
implementation, review, design, and issue workflows (see `internal/cmd/install_formulas/`).

## Formula types

| Type | Execution model |
|------|-----------------|
| `workflow` | Sequential steps with explicit dependencies (the common case) |
| `convoy` | Parallel legs, then a synthesis step that depends on all of them |
| `expansion` | Template-based step generation |
| `aspect` | Multi-aspect parallel analysis |

## Anatomy of a workflow formula

```toml
formula = "my-workflow"
description = "What this workflow does and how it progresses."
version = 1

[inputs]
[inputs.issue_url]
description = "GitHub issue to implement"
type = "string"
required = true

[vars]
[vars.branch_prefix]
description = "Branch naming prefix"
source = "cli"          # cli | env | literal | hook_bead | bead_title | bead_description
default = "feature"

[[steps]]
id = "investigate"
title = "Investigate the issue"
description = """
What the agent must do in this step, written as directives.
Reference variables as {{issue_url}}.
"""

[[steps]]
id = "implement"
title = "Implement the fix"
needs = ["investigate"]     # DAG dependency — this step is not ready until investigate closes
```

Key fields:

- **`[[steps]]`** — each has an `id`, `title`, `description` (the actual instructions), optional
  `agent` (per-step owner overriding the formula-level `agent`), optional `needs` (dependencies),
  and an optional `gate`.
- **`needs`** — forms a directed acyclic graph. Validation rejects unknown IDs and cycles;
  execution order is a topological sort (Kahn's algorithm, `internal/formula/sort.go`).
- **`[inputs]`** — parameters callers must supply at instantiation (`--var key=val`).
- **`[vars]`** — resolved at instantiation from the declared `source`: the command line, the
  environment, a literal default, or the hooked work item.
- **`gate`** — an optional blocking gate on a step (`internal/formula/types.go`); a step with
  a gate cannot be closed until the gate resolves. Gate steps complete with
  `af done --phase-complete --gate <id>`, which registers the session as a gate waiter.

Inspect any formula's inputs and vars without opening the file:

```bash
af formula show <name> --json
```

## Creating a formula from a SKILL.md

The `/formula-create` skill (run inside Claude Code) converts a SKILL.md — a procedural
skill document — into a formula:

```
/formula-create /path/to/your/SKILL.md
```

It preserves the skill's phase gates as separate formula steps and writes the result to
`.agentfactory/store/formulas/<name>.formula.toml`.

## Turning a formula into an agent

```bash
af formula agent-gen <name>        # generate workspace, CLAUDE.md template, hooks, registry entry
af formula agent-gen <name> -o     # dry run — print the rendered CLAUDE.md to stdout
af formula agent-gen <name> --delete
make install                       # required: af embeds role templates in the binary (go:embed)
```

Without the rebuild, the new agent falls back to the generic supervisor template after
context compression — see [the recovery model](recovery-model.md).

## Running a formula

```bash
af sling --formula <name> --var key=val --agent <agent>   # instantiate on an agent
af sling --agent <name> "task description"                # simple task dispatch (no formula)
af sling --formula <name> --agent <agent> --no-launch     # create step beads only, don't launch
```

Instantiation creates one work item ("bead") per step with the DAG encoded as dependencies,
resolves variables, and (unless `--no-launch`) starts the agent's tmux session. The agent then
loops: `af prime` → execute step → `af done` → next ready step.

## Further reading

- [Agent lifecycle](agent-lifecycle.md) — how sessions start, work, and stop
- [Recovery model](recovery-model.md) — how formulas survive crashes and context compression
- [README](../README.md) · [Using Agentfactory](../USING_AGENTFACTORY.md) · [Architecture overview](architecture/overview.md)
