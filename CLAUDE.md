# Agentfactory

Agentfactory — standalone multi-agent orchestration CLI.

## Build Commands

- `make sync-formulas` - sync build formulas from `internal/cmd/install_formulas` to `.agentfactory/store/formulas`
- `make build` — build the `af` binary
- `make test` — run unit tests
- `make install` — build and install to `~/.local/bin/af`
- `make test-integration` — run integration tests (should only run in CI)
- `make clean` — remove build artifacts

## Dependencies

- Go 1.24+
- Python 3.12 — required by the in-tree MCP issue-store server (`py/issuestore/`); install.go's `checkPython312` enforces the version at runtime
- jq — used by quality gate hook
- claude CLI — optional, for quality gate evaluation

## Project Structure

```
cmd/af/              Entry point
internal/
  checkpoint/        Session crash recovery (formula/step state, git state, timestamps)
  claude/            Claude Code settings templates (autonomous, interactive)
  cmd/               CLI commands (see below)
  config/            Config loading (factory.json, agents.json, messaging.json, dispatch.json)
  formula/           Formula system (TOML parsing, DAG validation, topo sort, variable resolution)
  issuestore/        Store interface over any underlying datastore
  lock/              Identity lock with PID-based stale detection
  mail/              Mail system (mailbox, router, issuestore-backed storage)
  session/           Agent session lifecycle (tmux start/stop, Claude launch, zombie detection)
  templates/         Role templates (manager, supervisor, and many more specialist roles)
  tmux/              tmux subprocess wrapper (create, attach, send-keys, capture, readiness)
hooks/               Quality gate scripts (source copies)
```

## CLI Commands

- `af root` — print factory root path
- `af prime [--hook]` — inject role context into Claude Code session at startup
- `af mail send|inbox|read|delete|check|reply` — inter-agent messaging via beads
- `af install --init` / `af install <role>` — factory and agent workspace setup
- `af up [agents...]` — start agent tmux sessions (all or specified)
- `af down [agents...] [--all]` — stop agent tmux sessions
- `af attach <agent>` — attach to a running agent's tmux session
- `af done` — close current formula step, advance workflow
- `af formula agent-gen <file>` — generate agent shell from formula TOML
- `af sling --formula <name>` — instantiate a formula (create step beads, resolve DAG, optionally launch)
- `af sling --agent <name> "task"` — dispatch a task to a specialist agent

## Architecture

`./USING_AGENTFACTORY.md` includes up-to-date current usage information with commands that all exist and are in active use.
