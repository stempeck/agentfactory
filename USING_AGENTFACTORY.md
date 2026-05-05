# Using Agentfactory
**Vision: **
You have SKILLs, now turn your SKILL.md's into your autonomous workforce that can reliably execute your instruction set with context handoffs.

**Mission:**
Create an instruction set workflow (formula) with `/formula-create /path/to/your/SKILL.md` and generate an autonomous agentfactory agent from it with `af formula agent-gen name-of-your-formula` with simple steps or multi-agent coordination.

## Prerequisites

- **Go 1.24+** — `go version`
- **jq** — used by quality gate hook
- **claude CLI** — optional, for quality gate evaluation (haiku)
- **tmux** — required for agent session management (`af up`, `af down`, `af attach`, `af sling`)
- **Python 3.12** — required by the in-tree MCP issue-store server that `af install --init` spawns (`python3.12 --version`)
- **af** — `make install` from the agentfactory repo (installs to `~/.local/bin/af`)
- **Formula TOML files** — for formula-driven workflows, place `.formula.toml` files in `.beads/formulas/` (local to the project) or `~/.beads/formulas/` (global)

## Setup container and install agentfactory alongside repo (the easy way)
1. IFF you haven't setup AgentFactory, run: `./quickdocker.sh <github-repo-path>`
1a. IFF you haven't setup AgentFactory, when the above completes, run: `claude` and make sure to authenticate.
2. IFF you have AgentFactory setup, run: `docker exec -it -u dev "af_ghusername_repo" bash`, then: `./quickstart.sh`
3. (optionally) enable the quality gate: `af quality on` (the `fidelity` gate is on by default and only fires when an agent is running a formula to keep it honest)

## The Flow (per repository)

Every repository you want agents on gets its own factory. Repeat these steps for each repo.

### 1. Initialize the factory at your project root (if not using quickstart.sh, the hard way)

```bash
cd ~/src/myproject
af install --init
```

### 2. Provision agents

```bash
af install manager
af install supervisor
```

### 3. Add factory dirs to .gitignore

```bash
cat >> .gitignore << 'EOF'
.agentfactory/
hooks/
.beads/
EOF
```

### 4. Start and attach to the manager

```bash
af up manager           # Launch manager in a tmux session
af attach manager       # Attach to it
```

`af up` creates a worktree owned by that agent. Children dispatched from that
agent via `af sling --agent` inherit the same worktree — the parent and its
children all share a single tree.

### 5. Start a supervisor

```bash
af up supervisor        # Launch supervisor in a tmux session
```

The supervisor picks up mail and begins autonomous work. No need to attach — it runs independently.

### 6. Give the manager work

From the manager's Claude session, just talk to it. The manager can:
- Read and modify any file in the project (the role template injects the factory root as an absolute path)
- Send tasks to the supervisor: `af mail send supervisor -s "Fix auth bug" -m "..."`
- Broadcast to all agents: `af mail send @all -s "Status" -m "..."`
- Check for replies: `af mail inbox`

Mail delivery is automatic — the `UserPromptSubmit` hook injects new mail on every prompt.

## Example: Setting Up myproject

```bash
cd ~/src/myproject
af install --init
af install manager
af install supervisor
af up manager
af attach manager
```

You're now the manager agent working on myproject.

## Quick Reference

### Agent Commands

```bash
af mail send <to> -s <subject> -m <message>   # Send mail
af mail send @all -s <subject> -m <message>    # Broadcast
af mail inbox                                   # List unread
af mail read <id>                               # Read message
af mail reply <id> -m <message>                 # Reply
af mail delete <id>                             # Delete/acknowledge
af mail check                                   # Check for mail (exit 0/1)
af prime                                        # Re-inject identity + formula context (automatic when active)
af root                                         # Print factory root
```

### Formula Commands

```bash
af sling --formula <name> --var key=val --agent <agent>    # Instantiate a formula on an agent
af sling --agent <name> "task description"                 # Dispatch a task to a specialist agent
af done                                                    # Close current formula step and advance
af done --phase-complete --gate <id>                       # Complete a gate step (session ends)
af formula agent-gen <name>                                # Generate specialist agent (template + workspace)
af formula agent-gen <name> -o                             # Dry run — print rendered CLAUDE.md to stdout
af formula agent-gen <name> --delete 
```

### Dispatch Commands

```bash
af dispatch                          # Run one dispatch cycle (check GitHub issues, dispatch to agents)
af dispatch start [--interval 300]   # Start background dispatch polling loop
af dispatch stop                     # Stop background dispatch polling loop
af dispatch status                   # Show dispatch state and agent availability
af dispatch --dry-run                # Show what would be dispatched without acting
```

Configuration lives in `.agentfactory/dispatch.json` (created by `af install --init`). Edit it to add repos, trigger label, and label-to-agent mappings before starting the dispatcher.

```json
{
  "repos": ["myorg/myrepo"],
  "trigger_label": "agentic",
  "notify_on_complete": "manager",
  "mappings": [
    {"label": "bug",     "agent": "factoryworker"},
    {"label": "feature", "agent": "supervisor"}
  ]
}
```

### Adding more agents manually (not recommended. use: agent-gen or agent-gen-all.sh)

Edit `.agentfactory/agents.json` at the project root:

```json
{
  "agents": {
    "manager":    { "type": "interactive", "description": "Human-supervised agent" },
    "supervisor": { "type": "autonomous",  "description": "Independent task executor" },
    "researcher": { "type": "autonomous",  "description": "Research and analysis agent" }
  }
}
```

Add groups in `.agentfactory/messaging.json`:

```json
{
  "groups": {
    "all": ["manager", "supervisor", "researcher"],
    "workers": ["supervisor", "researcher"]
  }
}
```

Then: `af install researcher && af up researcher`

### What the hooks do

| Hook | Trigger | Action |
|------|---------|--------|
| `SessionStart` | Session opens | `af prime --hook` — inject identity. Autonomous agents also run `af mail check --inject` to pick up queued work. |
| `PreCompact` | Context compaction | `af prime` — re-inject identity and formula step context (automatic when formula active). |
| `UserPromptSubmit` | Each prompt | `af mail check --inject` — deliver new mail |
| `Stop` | Each response | `quality-gate.sh` — haiku grades against 7 generic principles, mails verdict on failure. **Off by default** — `af quality on` (or `echo on > "$(af root)/.quality-gate"`) to enable. |
| `Stop` | Each response | `fidelity-gate.sh` — haiku grades against the *current formula step's* title + description (ground truth from the step bead, not `af prime` output). Mails `STEP_FIDELITY` verdict on failure. Self-gates on `.runtime/hooked_formula` — generic supervisors with no active formula are unaffected. **On by default** (`af install --init` creates `.fidelity-gate` with "on") — `af fidelity off` to disable. |

### Directory layout (after setup)

```
~/af/myproject/                  # Agent Factory root = project root
  .agentfactory/
    factory.json                 # Root marker
    agents.json                  # Role registry
    messaging.json               # Groups
    dispatch.json                # GitHub dispatch configuration
    agents/
      manager/
        CLAUDE.md                # Role template
        .claude/settings.json    # Hooks
        .agent-checkpoint.json   # Crash recovery (created at runtime by af prime, gitignored)
        .runtime/                # Formula execution state (created at runtime, gitignored)
          hooked_formula         # Current formula instance bead ID
          formula_caller         # Who dispatched this formula
          session_id             # Current Claude session ID
          dispatched             # Dispatch marker (present if dispatched via af sling --agent)
          worktree_id            # Worktree ID (if agent runs in a worktree)
          worktree_owner         # Ownership flag (if this agent owns the worktree)
      supervisor/
        CLAUDE.md
        .claude/settings.json
        .agent-checkpoint.json
        .runtime/
  hooks/
    quality-gate.sh
    quality-gate-prompt.txt
    fidelity-gate.sh
    fidelity-gate-prompt.txt
  .beads/
    ...                          # Beads issue store (SQLite)
    formulas/                    # Formula TOML files (17 shipped by default)
      investigate.formula.toml
      factoryworker.formula.toml
      ...
  ... your project source ...
```

## Formula-Driven Workflows

Formulas are TOML files that define multi-step workflows with DAG dependencies. Instead of ad-hoc instructions, a formula encodes the full execution plan — steps, ordering, variables, and gates — in a declarative file.

### The Three-Way Architecture

1. **Agent `.md`** — thin persona shell (identity, startup protocol, which commands to run)
2. **Formula `.toml`** — workflow logic (steps, dependencies, variables, gates)
3. **`af` runtime** — bridges the two (instantiates steps as beads, injects context, tracks progress)

The agent doesn't need to know the full workflow. It runs `af prime` to get its current step, executes it, runs `af done` to advance, and repeats.

### Formula Types

| Type | Structure | Use Case |
|------|-----------|----------|
| `workflow` | Sequential steps with DAG dependencies | Most common — multi-step tasks |
| `convoy` | Parallel legs with synthesis | Parallel analysis (e.g., code review) |
| `expansion` | Template-based step generation | Repeating patterns across inputs |
| `aspect` | Multi-aspect parallel analysis | Specialized parallel investigation |

### Basic Flow (an agent typically utilizes)

```bash
# 1. Instantiate the formula (creates step beads with DAG deps)
af sling --formula investigate --var issue=ag-xyz --agent supervisor

# 2. Cycle to a clean session (prevents pre-sling context from contaminating step execution)
af handoff

# 3. Agent loads step context (automatic at SessionStart; manual refresh anytime)
af prime
# Output: formula name, progress (Step 2 of 8), current step instructions, gate warnings

# 4. Agent executes the step instructions, then advances
af done
# Output: "Next step: Run tests and verify coverage"

# 5. Repeat steps 3-4 until all steps complete
# On final step: af done sends WORK_DONE mail to the dispatcher
```

### Minimal Formula Example

```toml
formula = "deploy-check"
description = "Verify deployment readiness"
version = 1

[vars]
[vars.environment]
description = "Target environment"
required = true
source = "cli"

[[steps]]
id = "check-config"
title = "Validate configuration"
description = """
Verify that config files for {{environment}} exist and are valid.
Run: validate-config --env {{environment}}
"""

[[steps]]
id = "run-smoke"
title = "Run smoke tests"
needs = ["check-config"]
description = """
Execute smoke test suite against {{environment}}.
Run: gt test --smoke --env {{environment}}
"""

[[steps]]
id = "report"
title = "Generate readiness report"
needs = ["run-smoke"]
description = """
Summarize results and mail the dispatcher.
"""
```

Steps execute in dependency order (`needs`). Variables (`{{environment}}`) are substituted at instantiation time from `--var` flags. The `source` field controls where variable values come from: `cli` (from `--var`), `env` (environment variable), `literal` (hardcoded in TOML), or `hook_bead` (from the hooked bead).

### Gate Steps

Some steps have a **gate** — a structural interlock that prevents the step from closing until an external condition is met (e.g., approval, external dependency).

When an agent hits a gate step it will:

1. Complete the work described in the step (push code, send review request, etc.)
2. Run `af done --phase-complete --gate <gate-id>`
3. Session ends. A fresh agent is dispatched when the gate resolves.

The agent does NOT poll or wait in a loop. The gate mechanism handles the waiting externally.

### Formula File Locations

- **Project-local:** `.beads/formulas/<name>.formula.toml` (in the project repo)
- **Global:** `~/.beads/formulas/<name>.formula.toml` (shared across projects)

The `af sling` command searches both locations.

### Runtime State

Runtime state lives in the agent's `.runtime/` directory:

| File | Written by | Purpose |
|------|-----------|---------|
| `hooked_formula` | `af sling` | Bead ID of the current formula instance |
| `formula_caller` | `af sling` | Address of who dispatched the formula (for WORK_DONE mail) |
| `session_id` | `af prime --hook` | Claude session ID (persisted at SessionStart) |

This state enables crash recovery — if an agent restarts, `af prime` reads the hooked formula ID and resumes from the last unclosed step.

## Formula Succession

When you run `af sling --formula <name>` in a workspace that already has an active
formula (`.runtime/hooked_formula` exists), sling refuses with an error:

```
prior formula <instance-id> is still active; use --reset to clean runtime state and re-sling
```

This prevents accidentally overwriting a running formula's state. The prior formula
may be abandoned (the agent crashed, was stopped, or the operator moved on) — but
sling cannot distinguish "abandoned" from "actively running," so it always errors.

### Resolving with --reset

Pass `--reset` to clean the stale runtime state and proceed:

```bash
af sling --formula my-workflow --var issue=bd-42 --agent supervisor --reset
```

`--reset` removes:
- `.runtime/hooked_formula` (prior formula instance ID)
- `.runtime/formula_caller` (prior dispatcher address)
- `.runtime/dispatched` (prior dispatch marker)
- `.agent-checkpoint.json` FormulaID field (crash-recovery formula reference)

In the dispatch path (`af sling --agent`), `--reset` additionally removes:
- The agent's tmux session (if running)
- The agent's worktree (if present)

After cleanup, sling proceeds normally — instantiating the new formula fresh.

!IMPORTANT! LLM don't read between <ignore></ignore> blocks! Skip to after the </ignore><-here
<ignore>
!IMPORTANT! WARNING for Human eyes only:

`af down --reset` will KILL all worktrees and CLOSE all beads! 
Use it when you're completely done with a session and don't want to continue any work to clean up all state and start fresh.

`af down <agent-name> --reset` will more safely tear down a specific agent and close all associated beads/worktree.
</ignore>

### Dispatch path

When a manager dispatches work via `af sling --agent <specialist> "task"`, the
dispatch path handles succession unconditionally. It removes `hooked_formula` and
`formula_caller` before instantiating the new formula, so the operator never sees
the succession error. This is by design: dispatch implies intent to replace.

### Input bridging

When you dispatch with `af sling --agent <name> "text"`, the quoted text is automatically assigned to the formula's single unsatisfied required input. If the formula has multiple required inputs, use `--var` to satisfy all but one — the remaining one receives the text.

```bash
# Single required input — text fills it automatically
af sling --agent plan "https://github.com/org/repo/issues/42"

# Multiple required inputs — satisfy all but one with --var
af sling --agent engineer --var outline_path=implementation_plan_outline.md "factoryworker"
```

If multiple required inputs are unsatisfied and no `--var` flags are provided, the command errors listing which inputs need `--var` flags.

### No interactive prompt

Sling never prompts for confirmation (y/N). Agent-runtime code paths must work
non-interactively (see ADR-014). The error-and-reset model keeps humans in control
without requiring TTY detection or interactive input.

*Related: [#126](https://github.com/stempeck/agentfactory/issues/126)*

## Generating Specialist Agents from Formulas

A generic supervisor can execute any formula, but it has a problem: when Claude's context fills up and compresses, `af prime` re-injects the supervisor's identity — which knows nothing about the formula. The agent forgets its sling command, step structure, gate protocol, and behavioral discipline. It stalls.

`af formula agent-gen` solves this by creating a **specialist agent** — one whose identity IS the formula. The agent's role template contains the full operational playbook (sling command, step structure, gate protocol, behavioral discipline) plus standard agent capabilities (mail, startup, constraints). Context compression re-injects this specialist template, so the agent never forgets what it is or how to work.

### When to create a specialist

- **Do create one** when a formula will be executed repeatedly, has complex behavioral discipline, or runs long enough to hit context compression.
- **Don't bother** for one-off formulas or short workflows that complete in a single context window. A generic supervisor works fine for those — `af prime` automatically injects formula context.

### How it works

```bash
# 1. Generate the specialist agent (writes template + provisions workspace)
af formula agent-gen investigate

# 2. Rebuild the binary so af prime can use the new template
make build

# 3. Start the agent
af up investigate
```

Step 1 does four things:
- Writes a Go template to `internal/templates/roles/investigate.md.tmpl` — the formula's identity baked into the template system
- Renders that template to `.agentfactory/agents/investigate/CLAUDE.md` — the workspace is immediately usable
- Writes `.agentfactory/agents/investigate/.claude/settings.json` — hooks for identity injection, mail delivery, and quality gate
- Registers the agent in `.agentfactory/agents.json` with its formula name

Step 2 compiles the template into the `af` binary. This is required because `go:embed` is compile-time — `af prime` reads templates from the compiled binary, not from disk. Skip this step and the agent falls back to `supervisor.md.tmpl` on context compression.

Step 3 starts the agent. On SessionStart, `af prime` detects `investigate.md.tmpl` in the embedded template set and renders it instead of `supervisor.md.tmpl`. On every PreCompact (context compression), the same specialist template is re-injected.

### What the specialist knows (and doesn't)

The specialist template gives the agent **procedural identity** — what it is and how it works:
- Its sling command with the correct formula name, required `[inputs]` as `--var` flags, and required CLI-sourced `[vars]` as `--var` flags. Non-CLI variables (e.g., `hook_bead`, `deferred`, `env`) are excluded from the sling command but listed in the Variables table so the agent knows they exist
- The full step structure (step table, gate markers)
- Gate protocol (if the formula has gates)
- Behavioral discipline (the formula's `description` field, verbatim)
- Standard agent capabilities (mail protocol, startup protocol, constraints)

The template does NOT contain **operational state** — which step the agent is on right now. That comes from `af prime`, which injects both identity and current formula context automatically. After context compression, the PreCompact hook runs `af prime`, restoring both the specialist identity and the current step instructions. No manual command is needed.

### Dry run

Preview the rendered CLAUDE.md without provisioning:

```bash
af formula agent-gen investigate -o
```

### Name override

Create a specialist with a different name than the formula:

```bash
af formula agent-gen investigate --name detective
```

This creates `.agentfactory/agents/detective/` workspace and `detective.md.tmpl` template, but the sling command still references the `investigate` formula.

### Creating a new agent

Paths assume: AF source at `~/projects/agentfactory`, target project at `~/af/myproject`.

```bash
# 1. Create the formula from a skill (writes to .beads/formulas/my-agent.formula.toml)
cd ~/af/myproject
claude -p "/formula-create /path/to/my-agent-SKILL.md"

# 2. Generate the agent (workspace, CLAUDE.md, settings, agents.json entry)
af formula agent-gen my-agent

# 3. Promote formula + template to the AF source
cp .beads/formulas/my-agent.formula.toml ~/projects/agentfactory/internal/cmd/install_formulas/
cp internal/templates/roles/my-agent.md.tmpl ~/projects/agentfactory/internal/templates/roles/

# 4. Rebuild af with the new template embedded
cd ~/projects/agentfactory
./quickstart.sh

# 5. Start the agent (quickstart put you in ~/af/myproject)
af up my-agent
```

Steps 3–4 are the reverse flow (ADR-015): promoting a project-local formula to ship with agentfactory. Without them, the agent runs but `af prime` falls back to the generic supervisor template on context compression.

### Batch regeneration with agent-gen-all.sh

Regenerates all specialist agents from promoted formulas. Run from your target project — the script auto-detects the AF source from its own location.

```bash
cd ~/af/myproject
~/projects/agentfactory/agent-gen-all.sh
af up
```

**Promote new formulas first.** The script syncs formulas from the AF source and removes any in your project that don't exist there. Unpromoted formulas get deleted. Pass `--no-build` to skip the `make install` step.

## Important: One Factory Per Repo

Each repository is its own independent factory. Agents in `~/src/myproject/.agentfactory/agents/manager/` cannot mail agents in `~/src/mysecondproject/.agentfactory/agents/supervisor/` — they have separate mail stores. If you have 5 repos, you run `af install --init` in each one.

## Troubleshooting

### "not in an agentfactory workspace"

You're not under a directory containing `.agentfactory/factory.json`. Run `af install --init` at the project root first.

### "agent X not found in agents.json"

The directory name must match a key in `.agentfactory/agents.json`. Add the agent there, then `af install <name>`.

### "identity lock" warning

Another session is running as this agent. Lock is PID-based and stale-safe — dead sessions release automatically.

### Quality gate not running

The quality gate is OFF by default. Create `<factory-root>/.quality-gate` containing `on` (or run `af quality on`) to enable it. Also requires `claude`, `jq`, and `af` on PATH — exits silently if missing (non-fatal). Check: `which claude && which jq && which af`. WARNING: Quality gate can be very noisy because it catches every mis-step claude takes, which happens surprisingly often.

### Fidelity gate not running

The fidelity gate is ON by default — `af install --init` creates `.fidelity-gate` containing "on". To disable: `af fidelity off` or `echo off > "$(af root)/.fidelity-gate"`. Also requires `claude`, `jq`, and `af` on PATH. Additionally, the fidelity gate self-gates on `.runtime/hooked_formula` — if no formula is active in the agent's working directory, the hook exits silently regardless of toggle state. Confirm with `af step current --json` (output should have `state == "ready"` for the gate to fire). The two gates use distinct lock files (`/tmp/af-fidelity-gate-$ROLE.lock` vs `/tmp/af-quality-gate-$ROLE.lock`) and run independently. NOTICE: The Fidelity gate is MUCH less noisy because it only fires when claude doesn't properly follow a formula step, which doesn't happen very often.

### Agent can't see project files

Agent working directory is `<project>/.agentfactory/agents/<agent-name>/`. The role template injects the factory root and working directory as absolute paths.

### Disclaimer
The contributors to this project take no responsibility for your agent (or their respective LLMs) actions.

Good luck, and enjoy your Factory of Agents!