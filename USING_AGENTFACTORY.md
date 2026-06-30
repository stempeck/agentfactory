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
- **Formula TOML files** — for formula-driven workflows, place `.formula.toml` files in `.agentfactory/store/formulas/` (local to the project) or `~/.agentfactory/store/formulas/` (global)

## Setup container and install agentfactory alongside repo (the easy way)
1. IFF you haven't setup AgentFactory, run: `./quickdocker.sh <github-repo-path>`
1a. IFF you haven't setup AgentFactory, when the above completes, run: `claude` and make sure to authenticate.
2. IFF you have AgentFactory setup, run: `docker exec -it -u dev "af_ghusername_repo" bash`, then: `./quickstart.sh`
2a. To **redeploy** agents after that initial setup (regenerate every specialist template and re-bootstrap the factory in one command), run from your project root (e.g. `~/af/myproject`): `af install --agents`. This is the one-command replacement for the manual two-script ritual — it runs **both** `agent-gen-all.sh` then `quickstart.sh`, non-interactively, so quickstart's usual terminal `exec bash` / manual `exit` is handled automatically (no manual `exit` needed). It operates on an **already-initialized factory**: `agent-gen-all.sh` runs first and aborts if `.agentfactory/store/formulas/` is absent, *before* `quickstart.sh` could bootstrap a cold factory — so for a first-time / cold-start setup run steps 1–2 (`quickdocker.sh` / `quickstart.sh`) first, then use `af install --agents` for subsequent redeploys. It is the **same command** described under *Batch regeneration with `af install --agents`* below — see there for the `af up` restart reminder, data-safety rule, and `--no-build` semantics.
3. (optionally) enable the quality gate: `af quality on` (the `fidelity` gate is on by default and only fires when an agent is running a formula to keep it honest)

### iOS Projects

For iOS projects that need remote Mac builds:

    ./quickdocker.sh user/myiosapp --platform ios

You'll be prompted for the SSH build host (user@host). The script generates a dedicated SSH keypair, authorizes it on the build host, and copies it into the container. No pre-loaded keys or agent configuration required.
After setup, `af up` automatically configures SSH-based build delegation — no additional commands needed.
Note: Existing iOS containers created before this change must be recreated with `--platform ios` to use key-based auth.
For CI/automation, set `AF_BUILD_HOST_USER` and `AF_BUILD_HOST_HOST` environment variables or use `--build-host user@host` flag to skip the interactive prompt.

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

### 3. Factory dirs are already excluded from git

No manual `.gitignore` editing is needed. `af install --init` (step 1) automatically
adds the factory-managed paths to `.git/info/exclude` under a
`# agentfactory managed paths` sentinel:

```
.agentfactory/*
.runtime/
AGENTS.md
.claude/
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
- Sling agents to do work: `af sling --agent rapid-implement "<my-github-issue-link>"`

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

### Dispatch Commands & Configuration

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
  "interval_seconds": 300,
  "retry_after_seconds": 1800,
  "remove_trigger_after_dispatch": true,
  "mappings": [
    {
      "labels": ["bug"],
      "source": "issue",
      "agent": "rapid-implement"
    },
    {
      "labels": ["reviewer"],
      "source": "pr",
      "agent": "ultra-review"
    },
    {
      "labels": ["incremental-fix"],
      "source": "pr",
      "agent": "rapid-increment"
    }
  ]
}
```

| Field | Required | Default | Description |
|-------|:--------:|---------|-------------|
| `repos` | Yes | — | GitHub repos to poll (e.g. `["owner/repo"]`) |
| `trigger_label` | Yes | — | Label used to query GitHub; only items with this label are fetched |
| `notify_on_complete` | No | `"manager"` | Agent to notify (via `--caller`) when dispatched work finishes |
| `interval_seconds` | No | `300` | Polling interval (seconds) when running `af dispatch start` |
| `retry_after_seconds` | No | `1800` | Time (seconds) before re-dispatching the same issue if the agent has gone idle |
| `remove_trigger_after_dispatch` | No | `false` | Remove the `trigger_label` from the issue/PR after dispatching |
| `mappings[].labels` | Yes | — | Labels the item must have (AND semantics — all must match) |
| `mappings[].source` | No | `"issue"` | Resource type: `"issue"` or `"pr"` |
| `mappings[].agent` | Yes | — | Agent to dispatch when labels match |

#### Workflows (multi-phase pipelines)

A **workflow** turns a single operator-applied label into an ordered, multi-phase
pipeline: the dispatcher walks the item through each phase, slings the phase's agent,
waits for that formula instance to complete, then swaps the label to the next phase —
all autonomously, with no formula edits. Add a `workflows` array alongside `mappings`.
Each phase is just an existing **mapping label**, so `mappings[]` remains the single
source of truth for which agent runs each phase.

```json
{
  "repos": ["myorg/myrepo"],
  "trigger_label": "agentic",
  "notify_on_complete": "manager",
  "interval_seconds": 300,
  "retry_after_seconds": 1800,
  "remove_trigger_after_dispatch": true,
  "mappings": [
    {
      "labels": ["design"],
      "source": "issue",
      "agent": "design-v3"
    },
    {
      "labels": ["build"],
      "source": "issue",
      "agent": "rapid-implement"
    }
  ],
  "workflows": [
    {
      "label": "feature-workflow",
      "phases": ["design", "build"]
    }
  ]
}
```

With this config, label an issue `agentic` + `feature-workflow`: the dispatcher adds the
`design` label and slings `design-v3`; when that instance completes it swaps `design`→
`build` and slings `rapid-implement`; when the final phase completes it removes the last phase
label and `agentic`, then notifies `notify_on_complete`.

| Field | Required | Default | Description |
|-------|:--------:|---------|-------------|
| `workflows[].label` | Yes | — | The operator-applied GitHub label that starts the pipeline. Must NOT equal `trigger_label` or any `mappings[].label` |
| `workflows[].phases` | Yes | — | Ordered list of existing mapping labels, one per phase. Each must resolve to an agent on the phase label **alone** (a single-label mapping), and all phases must share the same `source` |

Validation rules (enforced when the config loads): every phase must back a single-label
mapping whose agent has a formula; phases run top-to-bottom; a phase label may not equal
the `trigger_label` or the workflow's own label; and in v1 all phases of one workflow
must be the same `source` (all `issue` or all `pr`).

### Watchdog

The watchdog (`af watchdog`) is a long-lived polling loop that monitors agent tmux
sessions for Claude crashes, known error patterns, and silence timeouts, then nudges
or respawns the affected session (a circuit breaker stops respawning after repeated
failures and escalates to the supervisor). `af up` launches it best-effort; you
rarely run `af watchdog` by hand.

**Scope comes solely from `startup.json.watchdog_agents`** — the explicit, bounded
list of agents to monitor. There is no "watch all" mode.

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

Add mail groups in `.agentfactory/messaging.json`:

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
| `PreCompact` | Context compaction | `af compact-handoff` (interactive agents: `af compact-handoff --interactive`) — checkpoint and recycle the session so the fresh session re-injects identity and formula step context. |
| `UserPromptSubmit` | Each prompt | `af mail check --inject` — deliver new mail |
| `Stop` | Each response | `quality-gate.sh` — haiku grades against 7 generic principles, mails verdict on failure. **Off by default** — `af quality on` (or `echo on > "$(af root)/.agentfactory/.quality-gate"`) to enable. |
| `Stop` | Each response | `fidelity-gate.sh` — haiku grades against the *current formula step's* title + description (ground truth from the step bead, not `af prime` output). Mails `STEP_FIDELITY` verdict on failure. Self-gates on `.runtime/hooked_formula` — generic supervisors with no active formula are unaffected. **On by default** (`af install --init` creates `.agentfactory/.fidelity-gate` with "on") — `af fidelity off` to disable. |

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
  .agentfactory/hooks/
    quality-gate.sh
    quality-gate-prompt.txt
    fidelity-gate.sh
    fidelity-gate-prompt.txt
  .agentfactory/store/
    ...                          # Issue store (SQLite)
    formulas/                    # Formula TOML files
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

Steps execute in dependency order (`needs`). Variables (`{{environment}}`) are substituted at instantiation time from `--var` flags. The `source` field controls where variable values come from: `cli` (from `--var`), `env` (environment variable), `literal` (hardcoded in TOML), `hook_bead` (the hooked bead's ID), `bead_title` (the hooked bead's title), `bead_description` (the hooked bead's description), or `deferred` (resolved later — excluded from the initial resolved map).

### Gate Steps

Some steps have a **gate** — a structural interlock that prevents the step from closing until an external condition is met (e.g., approval, external dependency).

When an agent hits a gate step it will:

1. Complete the work described in the step (push code, send review request, etc.)
2. Run `af done --phase-complete --gate <gate-id>`
3. Session ends. A fresh agent is dispatched when the gate resolves.

The agent does NOT poll or wait in a loop. The gate mechanism handles the waiting externally.

### Formula File Locations

- **Project-local:** `.agentfactory/store/formulas/<name>.formula.toml` (in the project repo)
- **Global:** `~/.agentfactory/store/formulas/<name>.formula.toml` (shared across projects)

The `af sling` command searches both locations.

### Runtime State

Runtime state lives in the agent's `.runtime/` directory:

| File | Written by | Purpose |
|------|-----------|---------|
| `hooked_formula` | `af sling` | Bead ID of the current formula instance |
| `formula_caller` | `af sling` | Address of who dispatched the formula (for WORK_DONE mail) |
| `session_id` | `af prime --hook` | Claude session ID (persisted at SessionStart) |

This state enables crash recovery: when an agent restarts, `af prime` reads the
hooked formula ID and resumes from the last unclosed step.

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
- The entire `.runtime/` directory (including `hooked_formula`, `formula_caller`, `dispatched`, `session_id`, and any other runtime state)
- The entire `.agent-checkpoint.json` file (all crash-recovery state, not just the formula reference)

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

Or, generate and rebuild in one step with `--build`:

```bash
af formula agent-gen investigate --build
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

The template does NOT contain **operational state** — which step the agent is on right now. That comes from `af prime`, which injects both identity and current formula context automatically. After context compression, the PreCompact hook runs `af compact-handoff`, which checkpoints and recycles the session; the fresh session's SessionStart then runs `af prime`, restoring both the specialist identity and the current step instructions. No manual command is needed.

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

### Source tree and build flags

`--af-src` overrides where the template `.md.tmpl` file is written. Resolution chain: `--af-src` flag > `AF_SOURCE_ROOT` environment variable > compiled source root > factory root fallback.

```bash
af formula agent-gen my-agent --af-src ~/projects/agentfactory
```

`--build` runs `make install` after writing the template, so the new template is compiled into the binary immediately.

```bash
af formula agent-gen my-agent --build
```

Neither flag is needed with `agent-gen-all.sh`, which handles source resolution and does a single build at the end.

### Creating a new agent

Paths assume: AF source at `~/projects/agentfactory`, target project at `~/af/myproject`.

```bash
# 1. Create the formula from a skill (writes to .agentfactory/store/formulas/my-agent.formula.toml)
cd ~/af/myproject
claude -p "/formula-create /path/to/my-agent-SKILL.md"

# 2. Generate the agent and rebuild in one step
af formula agent-gen my-agent --af-src ~/projects/agentfactory --build

# 3. Promote the formula TOML to ship with agentfactory
cp .agentfactory/store/formulas/my-agent.formula.toml ~/projects/agentfactory/internal/cmd/install_formulas/

# 4. Start the agent
af up my-agent
```

Step 2 writes the template directly to the AF source tree (`--af-src`) and rebuilds the binary (`--build`). The agent functions immediately via its workspace CLAUDE.md even before the rebuild completes — `--build` ensures `af prime` uses the specialist template instead of falling back to `supervisor.md.tmpl` on context compression.

Step 3 is the reverse flow (ADR-015): promoting the formula TOML to ship with agentfactory. The template is already in the AF source tree from step 2 thanks to `--af-src`.

### Batch regeneration with `af install --agents`

Regenerates all specialist agents from promoted formulas and re-bootstraps the factory in one command. Run from the **main project checkout** (not a worktree — `af install --agents` refuses to run from one), e.g. `~/af/myproject`:

```bash
cd ~/af/myproject
af install --agents
af up
```

`af install --agents` runs **both** scripts in order — `agent-gen-all.sh` (regenerate every specialist template + rebuild) **then** `quickstart.sh` (full bootstrap) — non-interactively, so quickstart's terminal `exec bash` exits on its own (no manual `exit`). It operates on an **already-initialized factory**: `agent-gen-all.sh` runs first and aborts if `.agentfactory/store/formulas/` is absent, so for a first-time / cold-start setup run `quickdocker.sh` / `quickstart.sh` first (see the setup section above) — it is the *same command at both moments*.

**Agents are stopped during regeneration — run `af up` to restart them.** The wrapped `agent-gen-all.sh` runs `af down --all` and nothing restarts the agents, so even on full success they are left down; once `af install --agents` finishes you bring them back up with `af up`.

**Customer formulas are safe — with one rule.** The redeploy loop is data-safe for **new** customer formulas (those not in the AF source's `internal/cmd/install_formulas/` are preserved). But **edits to shipped formulas must be made (and promoted) in `internal/cmd/install_formulas/`** (ADR-015) — otherwise the `-nt` sync overwrites your edits with the AF source copy on the next redeploy.

**About `--no-build`.** `quickstart.sh` always rebuilds and reinstalls the `af` binary (it has no build-skip flag), so every successful `af install --agents` lands a fresh binary and `af prime`'s embedded identity is always current — a reliability win, not a stale-identity risk. `--no-build` skips **only** `agent-gen-all.sh`'s *duplicate* rebuild (the binary is then built once by quickstart instead of twice); it is not a "skip the rebuild" lever.

**Behavioral verification (what the unit tests do not cover).** A green unit test confirms `af install --agents` *dispatched* to the scripts, not that the factory is healthy, and the command is **not transactional** — a mid-run failure can leave agents down and the factory half-regenerated, so check the streamed exit code and end-state. To verify behavior end-to-end after a redeploy on a cold-started factory: run `af up`, dispatch work with `af sling`, and confirm an agent produces a PR using its current identity. This e2e check cannot run in CI because the scripts are non-hermetic.

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

The quality gate is OFF by default. Create `<factory-root>/.agentfactory/.quality-gate` containing `on` (or run `af quality on`) to enable it. Also requires `claude`, `jq`, and `af` on PATH — exits silently if missing (non-fatal). Check: `which claude && which jq && which af`. WARNING: Quality gate can be very noisy because it catches every mis-step claude takes, which happens surprisingly often.

### Fidelity gate not running

The fidelity gate is ON by default — `af install --init` creates `.agentfactory/.fidelity-gate` containing "on". To disable: `af fidelity off` or `echo off > "$(af root)/.agentfactory/.fidelity-gate"`. Also requires `claude`, `jq`, and `af` on PATH. Additionally, the fidelity gate self-gates on `.runtime/hooked_formula` — if no formula is active in the agent's working directory, the hook exits silently regardless of toggle state. Confirm with `af step current --json` (output should have `state == "ready"` for the gate to fire). The two gates use distinct PID-file locks (`.runtime/fidelity-gate.lock` vs `.runtime/quality-gate.lock`) and run independently — stale locks from dead processes are automatically recovered via PID-based detection. NOTICE: The Fidelity gate is MUCH less noisy because it only fires when claude doesn't properly follow a formula step, which doesn't happen very often.

### Agent can't see project files

Agent working directory is `<project>/.agentfactory/agents/<agent-name>/`. The role template injects the factory root and working directory as absolute paths.

### Mouse wheel scrolls Claude, and I can't select text by dragging

This is expected (Issue #412). Agent sessions are started with tmux `mouse on` so the
wheel scrolls **Claude's own conversation view** (its scrollback) instead of being
translated into arrow keys by the outer terminal. The trade-off is that `mouse on`
captures click-drag, so a normal drag no longer makes a native terminal text
selection. **To select/copy text the usual way, hold `Shift` while you click and
drag** — this bypasses tmux's mouse handling and gives you your terminal's native
selection. (If you ever attach and the wheel does *not* scroll Claude, check the
session: `tmux show-options -t af-<agent> -v mouse` should report `on`; `af up`
also prints a `warning:` to stderr if the option failed to apply.)

### Disclaimer
The contributors to this project take no responsibility for your agent (or their respective LLMs) actions.

Good luck, and enjoy your Factory of Agents!