# Agent lifecycle: how agentfactory runs autonomous Claude Code agents

Every agentfactory agent is a Claude Code session running inside a tmux session, owned and
supervised by the `af` runtime. The lifecycle is deliberately boring: provision a workspace,
start a session, inject identity, loop over formula steps, stop. Each stage is a CLI command
you can run and inspect yourself, which is also what makes the system debuggable — attach to
any agent with `af attach` and watch it work.

## Provisioning: `af install`

```bash
af install --init          # initialize the factory in your project root
af install manager         # provision an agent workspace from a role template
af install supervisor
```

`--init` creates `.agentfactory/` (registry, config, formula store) and adds the
factory-managed paths to `.git/info/exclude` — your repository stays clean without manual
`.gitignore` edits. Each `af install <role>` creates a per-agent workspace under
`.agentfactory/agents/<name>/` containing the agent's `CLAUDE.md` role template and its
Claude Code hook configuration.

Custom specialists come from formulas: `af formula agent-gen <name>` generates the workspace
and registers the agent in `agents.json` — see [Formulas](formulas.md).

## Starting: `af up`

```bash
af up                      # start all configured agents
af up manager supervisor   # start specific agents
af up worker --model sonnet-profile   # per-agent model override from models.json
```

For each agent, `af up`:

1. Creates a git **worktree** owned by that agent (children dispatched from it via
   `af sling --agent` share the same worktree).
2. Creates a tmux session named for the agent, with mouse and clipboard behavior configured.
3. Exports factory environment (`AF_ROOT`, …) into the session.
4. Launches Claude Code in the agent's workspace.

**Zombie detection:** if the tmux session already exists, `af up` checks whether Claude is
actually running inside it. A live session with a dead Claude is a zombie — it is killed and
recreated rather than reported as "already running" (`internal/session/session.go`).

## Identity injection: `af prime`

Agents do not carry their instructions in a long-lived prompt. On every session start, a
Claude Code `SessionStart` hook runs:

```
af prime --hook && af mail check --inject
```

`af prime` outputs the agent's role template, session metadata, and — when a formula is
hooked — the current step's directives. This is the mechanism that makes agents recoverable:
identity and workflow state live on disk and in the issue store, not in the model's context
window. See [the recovery model](recovery-model.md) for what happens on context compression.

## The work loop: `af done`

An agent working a formula repeats one cycle:

1. `af prime` hands it the current step (id, title, directives, resolved variables).
2. It executes the step's directives.
3. `af done` closes the step's bead and advances to the next ready step in the DAG.
4. Gate steps end the session instead: `af done --phase-complete --gate <id>`.

Quality enforcement happens at the loop boundary — `Stop` hooks run the quality and fidelity
gates, so a step that skipped its directives is caught before the workflow advances.

## Messaging: `af mail`

Agents coordinate through mail, not shared context:

```bash
af mail send supervisor -s "Fix auth bug" -m "login.go is not checking token expiry"
af mail send @all -s "Status" -m "…"     # broadcast
af mail inbox / read <id> / reply <id> -m "…" / delete <id>
af mail check                            # exit 0/1 — is there new mail?
```

Delivery is automatic: the `UserPromptSubmit` hook injects unread mail into the agent's
context on every prompt, and `SessionStart` injects it at launch. A supervisor started with
`af up supervisor` needs no operator attention — it picks up mail and works.

## Dispatch: `af sling`

```bash
af sling --agent rapid-implement "https://github.com/org/repo/issues/42"
af sling --formula mergepatrol --var pr=123 --agent mergepatrol
af sling --agent worker "task" --reset    # force-reset: close beads, remove worktree, clean state
```

`af sling` is how work enters the factory — from an operator, from the manager agent, or from
the GitHub-issue dispatcher (`af dispatch`). Sessions started by sling auto-terminate when
their formula completes.

## Observing and stopping

```bash
af attach <agent>          # attach to the live tmux session (watch or intervene)
af agents list --json      # machine-readable status of every configured agent
af down worker             # stop specific agents
af down --all              # stop everything
```

## Further reading

- [Formulas](formulas.md) — the workflow definitions agents execute
- [Recovery model](recovery-model.md) — crash and context-compression survival
- [README](../README.md) · [Using Agentfactory](../USING_AGENTFACTORY.md) · [Architecture overview](architecture/overview.md)
