# Agentfactory

Agentfactory — standalone multi-agent orchestration CLI.

## Build Commands

- `make sync-formulas` - sync build formulas from `internal/cmd/install_formulas` to `.agentfactory/store/formulas`
- `make build` — build the `af` binary
- `make build-webui` — build the optional web console (`webui`) from its own module (`web/go.mod`)
- `make test` — run unit tests (root module only — `go test ./...`)
- `make install` — build and install to `~/.local/bin/af`
- `make test-integration` — run integration tests (should only run in CI)
- `make clean` — remove build artifacts

The web console is a **separate Go module** (`web/go.mod`), so root `make test` does NOT run its
tests — run them directly with `cd web && go test ./...` (CI covers them via the `web-unit` job).

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
web/                 Optional web console — SEPARATE Go module (web/go.mod)
  cmd/afweb/         Web console entry point (builds the `webui` binary)
  internal/
    server/          Loopback HTTP server, routing, handlers
    exec/            Command-injection-safe `af` exec wrapper + --var validation
    readmodel/       Honest agent read-model (Phase-0 status + tmux liveness)
    config/          Settings read/write over the af config-set CLI
    dispatch/        Dispatch-status reader
    formschema/      Formula input-schema reader
    proto/           On-disk prototype server (.designs/)
    rendezvous/      Singleton-launch rendezvous (.runtime/webui_server.json)
    feedback/        Gate-verify feedback writer
    entrypoint/      Launch-guard helpers
    web/             Embedded static assets (index.html, app.js)
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
- `af agents list --json` — list configured agents with live status as a JSON array (machine-readable contract)
- `af formula show --json` — print a formula's inputs and vars as JSON
- `af dispatch status --json` — show dispatcher status and dispatch history as JSON (always exits 0; branch on `.state`)
- `af config dispatch set` / `af config startup set` — replace dispatch.json / startup.json from a JSON document on stdin
- `af improvement [on|off] [--agent <name>] | complete` — toggle/show the continuous-improvement hook (AND-gated, default off); `complete` finishes a pending improvement session

## Architecture

`./USING_AGENTFACTORY.md` includes up-to-date current usage information with commands that all exist and are in active use.

## Code comments

Code should "read like a book". Code comments should ONLY exist when it makes sense to explain WHY the code is doing what it does. Having to maintain comments in-addition to code is a maintenance burden (and comments diverge), code is truth. Comments should be minimal or ideally, not exist at-all UNLESS they add VALUE to the code. IFF they add VALUE to the code, we should still ask the question, WHY? Is the code not self-describing? Are we introducing a hack or work-around that someone needs to know about? WHY? ONLY IFF you can reason about WHY the comment should exist, then it can exist.
