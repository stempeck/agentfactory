# Using Agentfactory
**Vision: **
You have SKILLs, now turn your SKILL.md's into your autonomous workforce that can reliably execute your instruction set with context handoffs.

**Mission:**
Create an instruction set workflow (formula) with `/formula-create /path/to/your/SKILL.md` and generate an autonomous agentfactory agent from it with `af formula agent-gen name-of-your-formula` with simple steps or multi-agent coordination.

**Audience:**
This guide is the human operator's manual: the `af` commands and configuration needed to set
up and USE agentfactory. Agents and formulas are one system, so both live here. Deep guides
for the measurement, model, and token-economics subsystems are split out — see [Feature Guides](#feature-guides).

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
1b. When that clean `./quickdocker.sh <github-repo-path>` install finishes, it now **reveals the web console automatically** — printing the loopback URL `http://127.0.0.1:<HOSTPORT>/` (and opening your browser on macOS) **before** it drops you into the shell, so you no longer have to run `--web` yourself just to first see it. To **re-open** the console later, run `./quickdocker.sh <github-repo-path> --web`. See [`web/README.md`](web/README.md) for the full web-console runbook.
2. IFF you have AgentFactory setup, run: `docker exec -it -u dev "af_ghusername_repo" bash`, then: `./quickstart.sh`
2a. To **redeploy** agents after that initial setup (regenerate every specialist template and re-bootstrap the factory in one command), run from your project root (e.g. `~/af/myproject`): `af install --agents`. This is the one-command replacement for the manual two-script ritual — it runs **both** `agent-gen-all.sh` then `quickstart.sh`, non-interactively. It operates on an **already-initialized factory**: `agent-gen-all.sh` runs first and aborts if `.agentfactory/store/formulas/` is absent, *before* `quickstart.sh` could bootstrap a cold factory — so for a first-time / cold-start setup run steps 1–2 (`quickdocker.sh` / `quickstart.sh`) first, then use `af install --agents` for subsequent redeploys. It is the **same command** described under *Batch regeneration with `af install --agents`* below — see there for the `af up` restart reminder, data-safety rule, and `--no-build` semantics.
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
af mail send <to> -s <subject> -m <message> --report-delivery  # Report whether a live session was notified
af mail inbox                                   # List unread
af mail read <id>                               # Read message
af mail reply <id> -m <message>                 # Reply
af mail delete <id>                             # Delete/acknowledge
af mail check                                   # Check for mail (exit 0/1)
af prime                                        # Re-inject identity + formula context (the SessionStart hook injects formula context only)
af root                                         # Print factory root
```

### Gate Commands

```bash
af fidelity status                                        # Toggle state, per-agent record, overrides, provenance
af fidelity off --agent <name>                            # Record an operator-scoped override for one agent
af fidelity on --agent <name>                             # Clear that agent's override
af turn evidence --transcript <path> [--format text|json] # This turn's tool-call evidence
af turn interventions --since <ts> --agent <name>         # Harness actions recorded during this turn
af subagent-observe                                       # PostToolUse Task|Agent hook: relay the gate's recorded refusal
```

`af fidelity off` is an operator action — it is refused inside an af-managed agent session, so run it
from a host shell. `af turn evidence` is what both Stop hooks call to build the evidence block they
hand the judge; it exits 0 whatever the transcript looks like and reports any shortfall in its output.

`af turn interventions` reads af's own record log — not the transcript — and prints one line per
tokenomics mechanism that fired at or after the turn boundary, so the judge does not grade a handoff
or a serialized fan-out the harness itself asked for as a deviation. It prints nothing and exits 0
when the turn had none, when `--since` names no parsable boundary, or when the log cannot be read.

`af subagent-observe` is a hook, not something to run by hand: Claude Code invokes it on every `Task`
or `Agent` completion with the hook payload on stdin. It computes no capacity verdict of its own —
when the `af dispatch-admit` gate has recorded a refusal within the last fan-out latch window, it
delivers one urgent self-addressed `TOKENOMICS_DISPATCH` bead restating the figures that refusal
recorded and counselling that further sub-agents go out one at a time. A session that the gate never
refused hears nothing from it, however full that session is. It never blocks and always exits 0
(ADR-007).

`af dispatch-admit` is its pre-act sibling and the one hook that may refuse: Claude Code invokes it on
every `Task` launch, before the sub-agent starts. It is the sole enumerated exception to "hooks never
block" (ADR-007 amendment, 2026-08-31) — when the sub-agents already sharing the launcher's declared
backend pool, plus a bounded reservation for this launch, would oversubscribe it, the gate returns a
PreToolUse `deny` telling the agent to launch one at a time. It refuses by arithmetic alone, records
the refusal with its operands regardless of the telemetry toggle, and is inert by construction on any
profile that declares no shared backend (every cloud profile) — where it admits, silently, always
exiting 0.

What each of those mechanisms is permitted to do to a run, which of the two objectives it answers to —
`capacity`, which is window-driven and inert wherever no capacity fact is declared, or `efficiency`,
which is baseline-driven and applies on every profile — and what it records when it fires, is written
down in [Token economics](USING_TOKENOMICS.md#token-economics).

### Formula Commands

```bash
af sling --formula <name> --var key=val --agent <agent>    # Instantiate a formula on an agent
af sling --formula <name> --input-digest <64 hex>          # Attest the run's on-disk inputs — see USING_TOKENOMICS.md
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

#### Crons (recurring scheduled slings)

A **cron** is an operator-owned recurring sling: on its normal polling tick the dispatcher
re-slings a named agent on a fixed cadence, with no triggering issue or PR behind it. Each
fire is a bare re-sling — the same shape as `af sling --agent <name> --bare` — so it carries
no task text and creates no assignment bead, only the `vars` the schedule declares. Cadence
belongs to the operator, so schedules live in `dispatch.json`, not in formulas. Add a `crons`
array alongside `mappings`:

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
    }
  ],
  "crons": [
    {
      "name": "daily-pr-review",
      "agent": "ultra-review",
      "every": "1d",
      "vars": {
        "pr_uri": "https://github.com/myorg/myrepo/pull/42"
      }
    },
    {
      "name": "twice-daily-increment",
      "agent": "rapid-increment",
      "every": "12h",
      "vars": {
        "pr_uri": "https://github.com/myorg/myrepo/pull/42"
      }
    }
  ]
}
```

Each schedule needs a unique `name` (its durable identity — timing state and the status row
both key off it), an `agent` that exists in `agents.json` and carries a formula, and an
`every` cadence: a positive whole number plus one unit — `m`, `h` or `d` (`30m`, `4h`, `14d`).
Put any var the target formula requires under `vars`; omit `vars` entirely for a bare wake.
Cadence is a floor rounded up to a whole `interval_seconds` tick, so `1m` under the default
300s tick fires roughly every five minutes. `af config dispatch set` validates the whole
document — agent, formula, cadence grammar and var keys — before writing, and `af dispatch
status` grows a `Schedules:` block showing each schedule's last fire and next due time.

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
| `SessionStart` | Session opens | Three independent entries, identical for both role types, listed in the order the settings declare them. `af prime --hook` — session header, worktree block, startup directive, current formula step, checkpoint, and the economics/advisory blocks; it renders **no** identity, because the harness already loaded the agent's `CLAUDE.md`. `af mail check --inject` — messages not yet delivered to this session, ≤ 4.6 KB. `af memory check --inject` — the agent's top notes from its own learnings vault, ≤ 4.7 KB; on a fresh factory it emits nothing at all. Each writer is budgeted separately because any single hook string longer than roughly 10,000 characters is spilled to a file and replaced in the session by a short preview, so its content never arrives — an observed limit, not a documented one. Matching hooks run in parallel, so declaration order is not execution order and no entry may depend on another having run ([ADR-023](docs/architecture/adrs/ADR-023-sessionstart-context-surface.md)). |
| `PreCompact` | Context compaction | `af compact-handoff` (interactive agents: `af compact-handoff --interactive`) — checkpoint and recycle the session. The fresh session gets its identity from `CLAUDE.md`, which the harness loads the way it does for any session, and the recycle prompt's `af prime` restores the current formula step. |
| `UserPromptSubmit` | Each prompt | `af mail check --inject` — deliver mail not yet delivered to this session |
| `Stop` | Each response | `quality-gate.sh` — haiku grades against 7 generic principles, mails verdict on failure. **Off by default** — `af quality on` (or `echo on > "$(af root)/.agentfactory/.quality-gate"`) to enable. |
| `Stop` | Each response | `fidelity-gate.sh` — haiku grades against the *current formula step's* title + description (ground truth from the step bead, not `af prime` output). Mails `STEP_FIDELITY` verdict on failure. Self-gates on `.runtime/hooked_formula` — generic supervisors with no active formula are unaffected. **On by default** (`af install --init` creates `.agentfactory/.fidelity-gate` with "on") — `af fidelity off` to disable, which is an operator action and is refused inside an af-managed agent session. `af fidelity off --agent <name>` records a per-agent override that `af fidelity status` lists, and every toggle write `af` makes — by `af fidelity`, by `af up` applying a startup gate, or by `af install --init` seeding a new factory — is appended to `.agentfactory/.fidelity-gate.log`. |

**Re-provision after upgrading the binary.** Because each SessionStart writer now emits its own
JSON envelope, a session that starts against a **stale** `settings.json` — one still chaining
`af prime --hook && af mail check --inject && af memory check --inject` in a single entry — puts
three concatenated JSON objects on one stdout, which is not a single JSON document; the harness then
falls back to plain-text handling of the raw envelopes (subject to the ~10 KB cap). This only
affects sessions started **without re-provisioning** after `make install` — an interactive `/clear`
or `--resume` in a worktree whose agent has not been relaunched, or a hand-launched session in a
factory-root dir before `af install --init`. Worktree agents pick up the new settings on the next
`af up` / `af sling` / recycle; after upgrading, re-provision with `af up` (or `af install --init`)
so the split-entry settings are in place ([ADR-023](docs/architecture/adrs/ADR-023-sessionstart-context-surface.md)).

### Step descriptions and the per-string cap

The harness does not deliver any single SessionStart hook string longer than roughly 10,000
characters: over-cap output is spilled to a file and replaced in the session by a short preview.
That is observed behaviour, not documented behaviour ([ADR-023](docs/architecture/adrs/ADR-023-sessionstart-context-surface.md)
E1). Whether the cap counts bytes or Unicode code points is not determined by anything observable —
the measurements are consistent with both — so af budgets in **bytes**, the conservative reading.
Mail and memory are budgeted well under the cap either way (≤ 4.6 KB and ≤ 4.7 KB), but
`af prime --hook` embeds the current formula step's `description` **verbatim** — it is the formula
author's contract, and af does not trim it. A step description long enough to push that entry past
the cap gets the entry replaced by the preview, and the step body never reaches the session.

**The rule:** a step description longer than the per-string cap is delivered whole only by the
tool-result `af prime` — the one an agent runs itself. Because the three entries are independent,
prime's entry spilling costs nothing from mail or memory. Keep step descriptions short enough to fit,
and treat a long one as a formula-authoring smell rather than a delivery guarantee.

Some shipped formulas already have a first step over the cap, so this is not hypothetical. The
measurement is deliberately not transcribed here, because it changes with every formula edit; see
[ADR-023](docs/architecture/adrs/ADR-023-sessionstart-context-surface.md) for how to recompute it.

### Continuous improvement hook

On a qualifying final `af done`, af can keep the just-finished agent's session alive and hand it an `/improve-agent` instruction so it refines its **own** formula from that session's learnings before the session tears down. This hook fires from `af done` — **not** a Claude `Stop` hook — so it lives here rather than in the hook table above.

**AND-gated, off by default.** The hook fires for an agent only when **both** toggles are on:

- the **factory** toggle — `.agentfactory/.improvement-hook` reads `on` (set with `af improvement on`), **and**
- that **agent's** `continuous_improvement` flag in `agents.json` is true (set with `af improvement on --agent <name>`).

Unlike the fidelity gate, `.improvement-hook` is **never** seeded by `af install --init` — absent means off, so the whole capability stays inert until an operator explicitly enables both sides. `af improvement` (no args) prints the factory line, a per-agent effective (AND) table, and any pending sessions.

**What fires, and what it does.** When both toggles are on and the finishing `af done` has a dispatcher (`.runtime/formula_caller`), af writes a `.runtime/improvement_pending` marker (recording the formula, caller, the formula's sha256, and whether the session would otherwise have auto-terminated), **defers** the session teardown and identity-lock release, and delivers the `/improve-agent` instruction over a redundant trio: the `af done` stdout, an urgent self-mail, and a one-line tmux nudge. The agent edits the formula at its absolute factory-root path (`<factory-root>/.agentfactory/store/formulas/<agent>.formula.toml` — never a worktree-relative path, so a dispatched agent's edit always lands on the same artifact the verdict and the promotion route below operate on), then runs `af improvement complete`, which validates the edited formula in-process, mails a `changed/unchanged` + `passed/FAILED` verdict to the caller (supervisor fallback), releases the deferred lock, and replays the deferred dispatched-session teardown.

**Promotion is the human's responsibility.** The improvement self-edit lands in the factory root's store formula (`<factory-root>/.agentfactory/store/formulas/<agent>.formula.toml`); to promote and install it, run `af install --agents`.

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
    memory/                      # Agent learnings vault — plain Markdown, outside every
      manager/                   # directory a teardown destroys, gitignored
        2026-08-15T1204Z-flaky-probe.md
        index.md                 # Derived summary, rebuilt on every write
      supervisor/
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
| `mail_delivered` | `af mail check --inject` | per-session delivered mail ids (deleting the file re-delivers everything once — fail-open) |

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

### Factory teardown is operator-only

Factory-wide teardown is an **operator action**. The commands that stop the whole
factory — `af down` with no target, `af down --all`, `af down --reset`,
`af install --agents`, and `af dispatch stop` — refuse when they are run from
inside an af-managed agent session (including the interactive manager).

Scoped `af down <agent>` is authorized in **three tiers**, all narrower than
factory-wide teardown:

- **self** — an agent may always stop its own session;
- **dispatcher-scoped** — an agent may stop a **specialist it dispatched**
  (`af down <that-agent>`);
- **manager-scoped** — the interactive manager may stop an autonomous worker
  specialist **whether or not it dispatched it**, so the human can direct fleet
  teardown through the manager in a crisis.

A granted tier covers `--reset` on its targets too: whoever may `af down <agent>`
may also `af down <agent> --reset`. This is the same stop + state-reclamation
authority that `af sling --agent <agent> --reset` already carries — the two
commands share one authority model. Only the factory-wide shapes (bare `af down`,
`--all`, `--reset` with no target) are operator-only.

**`af down --reset` (factory-wide) KILLS all worktrees and CLOSES all beads.** Run it from a
host shell, and only when you are completely done and want to clean up all state and start
fresh. `af down <agent-name> --reset` more safely tears down one specific agent and closes
its associated beads/worktree.

**"Agent-class" is scoped to factory-wide teardown only.** The manager is
agent-class **for factory-wide teardown** — a bare `af down` / `--all` / `--reset`
typed into its Claude pane is refused and redirected here — but it holds the
manager-scoped tier above for a single named worker. Run factory-wide teardown
from a host shell (or any non-af context); that is where `af down --all` and
friends actually run.

**Crisis workflow.** To stop a runaway or orphaned worker, have the manager run the
scoped `af down <worker>`; to also reclaim that worker's state (worktree + beads),
the scoped `af down <worker> --reset` — the same tier grants both.

**Manager: verify teardown requests independently.** A mailed "stop agent X" request
is a *request*, not authorization. Before acting, confirm the target and its state
yourself with `af agents list` — the confused-deputy risk is that a forged or
mistaken mail directs a stop you would not otherwise make.

An agent that attempts a factory-wide teardown sees a refusal message directing it to skip
the step and tell its operator. This is a **guardrail against accidental invocation, never a
security boundary** — a determined same-user process can still bypass it (the accepted
residual vectors are recorded in `.designs/541/design-doc.md`); the docs must not imply
those vectors are closed.

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

A generic supervisor can execute any formula, but it has a problem: when Claude's context fills up and compresses, the session recycles and comes back holding the supervisor identity the harness reloads from `CLAUDE.md` — which knows nothing about the formula. The agent forgets its sling command, step structure, gate protocol, and behavioral discipline. It stalls.

`af formula agent-gen` solves this by creating a **specialist agent** — one whose identity IS the formula. The agent's role template contains the full operational playbook (sling command, step structure, gate protocol, behavioral discipline) plus standard agent capabilities (mail, startup, constraints). That template is rendered into the agent's own `CLAUDE.md`, which the harness reloads after every compression, so the agent never forgets what it is or how to work.

### When to create a specialist

- **Do create one** when a formula will be executed repeatedly, has complex behavioral discipline, or runs long enough to hit context compression.
- **Don't bother** for one-off formulas or short workflows that complete in a single context window. A generic supervisor works fine for those — `af prime` automatically injects formula context.

### How it works

```bash
# 1. Generate the specialist agent (writes template + provisions workspace)
af formula agent-gen investigate

# 2. Rebuild the binary so the next identity render uses the new template
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
- Writes `.agentfactory/agents/investigate/.claude/settings.json` — hooks for formula-step context, mail delivery, memory delivery, and quality gate
- Registers the agent in `.agentfactory/agents.json` with its formula name

Step 2 compiles the template into the `af` binary. This is required because `go:embed` is compile-time — the identity render that writes each agent's `CLAUDE.md` reads templates from the compiled binary, not from disk. Skip this step and the next re-render falls back to `supervisor.md.tmpl`.

Step 3 starts the agent. Its `CLAUDE.md` was rendered from `investigate.md.tmpl` rather than `supervisor.md.tmpl`, and the harness loads that file at the start of every session — including the fresh one a PreCompact recycle opens. The SessionStart `af prime --hook` adds the current formula step on top of it.

### What the specialist knows (and doesn't)

The specialist template gives the agent **procedural identity** — what it is and how it works:
- Its sling command with the correct formula name, required `[inputs]` as `--var` flags, and required CLI-sourced `[vars]` as `--var` flags. Non-CLI variables (e.g., `hook_bead`, `deferred`, `env`) are excluded from the sling command but listed in the Variables table so the agent knows they exist
- The full step structure (step table, gate markers)
- Gate protocol (if the formula has gates)
- Behavioral discipline (the formula's `description` field, verbatim)
- Standard agent capabilities (mail protocol, startup protocol, constraints)

The template does NOT contain **operational state** — which step the agent is on right now. That comes from `af prime`, which reports the current formula context automatically. After context compression, the PreCompact hook runs `af compact-handoff`, which checkpoints and recycles the session; the harness loads the specialist `CLAUDE.md` into the fresh session, and that session's SessionStart `af prime --hook` restores the current step instructions on top of it. No manual command is needed.

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

Step 2 writes the template directly to the AF source tree (`--af-src`) and rebuilds the binary (`--build`). The agent functions immediately via its workspace CLAUDE.md even before the rebuild completes — `--build` ensures the next identity re-render uses the specialist template instead of falling back to `supervisor.md.tmpl`.

Step 3 is the reverse flow (ADR-015): promoting the formula TOML to ship with agentfactory. The template is already in the AF source tree from step 2 thanks to `--af-src`.

### Batch regeneration with `af install --agents`

Regenerates all specialist agents from promoted formulas and re-bootstraps the factory in one command. Run from the **main project checkout** (not a worktree — `af install --agents` refuses to run from one), e.g. `~/af/myproject`:

```bash
cd ~/af/myproject
af install --agents
af up
```

`af install --agents` runs **both** scripts in order — `agent-gen-all.sh` (regenerate every specialist template + rebuild) **then** `quickstart.sh` (full bootstrap) — non-interactively. It operates on an **already-initialized factory**: `agent-gen-all.sh` runs first and aborts if `.agentfactory/store/formulas/` is absent, so for a first-time / cold-start setup run `quickdocker.sh` / `quickstart.sh` first (see the setup section above) — it is the *same command at both moments*.

**Agents are stopped during regeneration — run `af up` to restart them.** The wrapped `agent-gen-all.sh` runs `af down --all` and nothing restarts the agents, so even on full success they are left down; once `af install --agents` finishes you bring them back up with `af up`.

**Customer formulas are safe — with one rule.** The redeploy loop is data-safe for **new** customer formulas (those not in the AF source's `internal/cmd/install_formulas/` are preserved). But **edits to shipped formulas must be made (and promoted) in `internal/cmd/install_formulas/`** (ADR-015) — otherwise the `-nt` sync overwrites your edits with the AF source copy on the next redeploy.

**About `--no-build`.** `quickstart.sh` always rebuilds and reinstalls the `af` binary (it has no build-skip flag), so every successful `af install --agents` lands a fresh binary and re-renders every agent's `CLAUDE.md` from it, which means the identity the harness loads at session start is always current — a reliability win, not a stale-identity risk. `--no-build` skips **only** `agent-gen-all.sh`'s *duplicate* rebuild (the binary is then built once by quickstart instead of twice); it is not a "skip the rebuild" lever.

**Bootstrap options.** `--litellm` also sets up the gateway for running agents on OpenAI models (see `USING_LITELLM.md`); it asks for your OpenAI API key the first time and reuses the stored key on later runs. `--no-telemetry` skips the telemetry backend and turns recording off; without it, a successful redeploy turns recording **on**. Every redeploy resets recording to match the flag — even if you toggled it by hand with `af telemetry` in between, so keep passing `--no-telemetry` on redeploys if you want it to stay off.

**Behavioral verification (what the unit tests do not cover).** A green unit test confirms `af install --agents` *dispatched* to the scripts, not that the factory is healthy, and the command is **not transactional** — a mid-run failure can leave agents down and the factory half-regenerated, so check the streamed exit code and end-state. To verify behavior end-to-end after a redeploy on a cold-started factory: run `af up`, dispatch work with `af sling`, and confirm an agent produces a PR using its current identity. This e2e check cannot run in CI because the scripts are non-hermetic.

## Important: One Factory Per Repo

Each repository is its own independent factory. Agents in `~/src/myproject/.agentfactory/agents/manager/` cannot mail agents in `~/src/mysecondproject/.agentfactory/agents/supervisor/` — they have separate mail stores. If you have 5 repos, you run `af install --init` in each one.

## Feature Guides

Deep guides for the factory's measurement, model, and token-economics subsystems live beside this one:

- [USING_TELEMETRY.md](USING_TELEMETRY.md) — run measurement: the telemetry backend, its dashboards, and the session statusline
- [USING_RECOVERY.md](USING_RECOVERY.md) — the watchdog, context-exhaustion recovery, and the step-context ladder
- [USING_MEMORY.md](USING_MEMORY.md) — the per-agent memory vault: what survives teardown, export/import, host mounts
- [USING_MODELS.md](USING_MODELS.md) — model profiles and classes (`.agentfactory/models.json`)
- [USING_LITELLM.md](USING_LITELLM.md) — running agents on non-Anthropic models through a gateway
- [USING_TOKENOMICS.md](USING_TOKENOMICS.md) — the af tokenomics behavior contract: the `capacity` and `efficiency` objectives, guarantees, permitted interventions, per-mechanism records, and how an improvement is proven
- [web/README.md](web/README.md) — the optional web console

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

Start with `af fidelity status`: it answers whether the gate is on, which agents have an override recorded, what each agent's run record has counted (evaluations, failures, last graded step) plus the step its escalation latch holds, and who last moved the switch. The fidelity gate is ON by default — `af install --init` creates `.agentfactory/.fidelity-gate` containing "on". To disable: `af fidelity off` or `echo off > "$(af root)/.agentfactory/.fidelity-gate"`. If it is off and you did not turn it off, `.agentfactory/.fidelity-gate.log` records every toggle write `af` makes as `ts actor source state`, and the `source` field names which of the three writers it was: `cli` for `af fidelity`, `af-up` for a startup gate applied by `af up`, `install` for the fresh-factory seed. Only a hand-edited toggle file leaves no trace there. An override recorded by `af fidelity off --agent <name>` is a file at `.agentfactory/fidelity-overrides/<name>` that `af fidelity on --agent <name>` removes; no hook reads that directory yet, so an override is a record of an operator decision, not a mute, and it is never the reason a gate is not firing. Also requires `claude`, `jq`, and `af` on PATH. Additionally, the fidelity gate self-gates on `.runtime/hooked_formula` — if no formula is active in the agent's working directory, the hook exits silently regardless of toggle state. Confirm with `af step current --json` (output should have `state == "ready"` for the gate to fire). The two gates use distinct PID-file locks (`.runtime/fidelity-gate.lock` vs `.runtime/quality-gate.lock`) and run independently — stale locks from dead processes are automatically recovered via PID-based detection. NOTICE: The Fidelity gate is MUCH less noisy because it only fires when claude doesn't properly follow a formula step, which doesn't happen very often.

### Improvement hook not firing

The continuous-improvement hook is AND-gated and OFF by default. If a finished agent never receives its `/improve-agent` instruction, run `af improvement` and confirm the factory line reads `on` and the agent's row shows `effective: fires` (both `af improvement on` and `af improvement on --agent <name>`; a fresh factory is always off). The hook fires only on a dispatched `WORK_DONE` `af done` (needs a non-empty `.runtime/formula_caller`), the store formula `<factory-root>/.agentfactory/store/formulas/<agent>.formula.toml` must exist (check the factory root, not a worktree copy — a worktree's git-tracked duplicate always exists and tells you nothing), and it won't re-fire while `.runtime/improvement_pending` is pending (run `af improvement complete` to clear). A stale session that never completed is auto-reaped only for agents in `startup.json`'s `watchdog_agents`; otherwise run `af improvement complete` yourself.

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