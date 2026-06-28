# Agentfactory
**Why do I need Agentfactory?**
1. You can give LLM steps to follow and at times it will use a heuristic and improvise, skipping steps
2. Recency bias drives every prompt
3. Improvization and Recency bias lead to bugs the next LLM builds around, compounding false assumptions
4. No crash recovery for agents doing long-running or multi-agent operations

SKILLs aren't enough on their own to solve this. You need an Agent with a better harness.

Agent Factory can easily harness your existing skills

**Vision:**
You have SKILLs, now turn your SKILL.md's into your autonomous workforce.

**Mission:**
Create an instruction set workflow (formula) with `/formula-create /path/to/your/SKILL.md` and generate an autonomous agentfactory agent from it with `af formula agent-gen name-of-your-formula` with simple steps or multi-agent coordination.

**Multi-agent orchestration CLI for Claude Code.**
Turn your SKILL.md files into autonomous agents that execute structured workflows with context handoffs, inter-agent messaging, and crash recovery.

## Quick Start

### Prerequisites

- Go 1.24+
- Python 3.12
- Node.js 18+
- tmux 3.0+
- jq
- git 2.20+
- GitHub CLI (`gh`)
- Docker (optional, for containerized setup)

### Installation

#### From Source

```bash
git clone https://github.com/stempeck/agentfactory.git
cd agentfactory
make build
make install    # installs af to ~/.local/bin
```

Verify: `af version`

#### Using Docker

```bash
git clone https://github.com/stempeck/agentfactory.git
cd agentfactory
./quickdocker.sh <github-repo-path>
```

This builds a container with all prerequisites, clones your target repo, and runs `quickstart.sh` inside it. When it finishes, the container is ready for `af up`.

#### Using quickstart.sh (inside the docker container @ docker exec -it -u dev "af_ghusername_repo" bash)

```bash
cd ~/projects/agentfactory/
./quickstart.sh           # full setup — installs af, Claude Code, configures workspace
```

### Authenticate Claude Code

After installation, run `claude` once to authenticate. Agents require an authenticated Claude Code session to function.

## Usage

### 1. Initialize a factory in your project (unnecessary if you run quickstart.sh)

Every repository gets its own factory. Run from your project root:

```bash
cd ~/src/myproject
af install --init
af install manager
af install supervisor
```

Add factory directories to `.gitignore`:

```bash
cat >> .gitignore << 'EOF'
.agentfactory/
.agentfactory/hooks/
.agentfactory/store/
EOF
```

### 2. Start agents

```bash
af up manager           # launch manager in a tmux session
af attach manager       # attach to interact with it
```

Or start the supervisor for autonomous work:

```bash
af up supervisor        # runs independently, picks up mail
```

### 3. Dispatch work to agents (the REAL value)

From any context:

```bash
af sling --agent supervisor "Fix the auth bug in login.go"
```

Or talk to the manager directly after attaching:

```bash
af attach manager
# now you're in the manager's Claude session — just talk to it
```

The manager can sling agents or delegate to agents via mail:

```bash
af mail send supervisor -s "Fix auth bug" -m "The login handler in login.go is not checking token expiry."
```

## Creating Custom Agents from Skills (the REAL value)

This is the core workflow: turn a SKILL.md into an autonomous agent.

### 1. Create a formula from your skill

```bash
claude
/formula-create "/path/to/your/SKILL.md"
# e.g. ./.claude/skills/rapid-implement/SKILL.md")
```

This generates a `.formula.toml` file in `.agentfactory/store/formulas/`. Be patient. It can take some time.

NOTICE: `.claude/skills/rapid-implement/SKILL.md` was provided in case you want to try creating your first coding agent.

### 2. Generate an agent from your formula (your-agent-name.formula.toml -> your-agent-name)

```bash
af formula agent-gen your-agent-name
```

This creates the agent's workspace, CLAUDE.md template, hook configuration, and registers it in `agents.json`.

### 3. Rebuild af with the new agent template

```bash
make install
```

Required because `af prime` reads templates from the compiled binary (`go:embed`). Without this, the agent falls back to the generic supervisor template on context compression.

### 4. Start the agent

```bash
af up your-agent-name
```

Or dispatch work to it directly:

```bash
af sling --agent your-agent-name "do the thing"
```

### Batch regeneration

To regenerate all specialist agents from formulas and re-bootstrap the factory, run the redeploy command from the main project checkout:

```bash
af install --agents          # regenerate all + rebuild, then bootstrap
af up                        # agents are stopped during regen — restart them
```

See `USING_AGENTFACTORY.md` for preconditions, data-safety, and `--no-build` notes.

## Included Formulas

| Formula | Purpose |
|---------|---------|
| `design-v3` | Structured design exploration with constraint verification |
| `design` | Basic design workflow |
| `factoryworker` | General-purpose factory worker |
| `gherkin-breakdown` | Break work into Gherkin scenarios |
| `mergepatrol` | PR review and merge workflow |

## Included Skills

| Skill | Purpose |
|-------|---------|
| `/formula-create` | Create a formula TOML from a SKILL.md |
| `/github-issue` | Create well-documented GitHub issues from current (or specified) context |
| `/documentation-update` | Audit and update a documentation file (.md) against the codebase |

## Web Console (optional)

Agentfactory ships an **optional** web console for managing the factory (the Floor view, slinging
tasks, dispatch status, settings, and design prototypes). It is a separate Go module under `web/`
and is **not** required to run `af`.

**Build and install (best-effort):**

```bash
make build-webui          # builds ./web/cmd/afweb -> ./webui at the repo root
make install              # installs af and, if present, copies webui to ~/.local/bin/webui
```

Inside the container, `quickstart.sh` launches the console **iff** the `webui` binary is present
(`$HOME/.local/bin/webui`) and stays silent when it is absent — the factory bootstrap never depends
on it. A second launch finds the already-running server (via its `.runtime/webui_server.json`
rendezvous + start-lock) and no-ops, so relaunching is safe.

### Remote access — loopback-only (do NOT publish the port)

The console binds **loopback only** (`127.0.0.1`). This is deliberate and load-bearing: the control
plane can stop/sling agents and edit config, so an exposed socket is a remote-code-execution and
irreversible-loss risk (cross-review **CR-1**).

**Do not** publish the port. The container is started **without** `-p`/`--publish`
(`quickdocker.sh`/`Dockerfile` never expose it), and that must stay true — adding `-p` turns an
unauthenticated loopback control plane into an open one.

**Standard path — `quickdocker.sh <repo> --web`.** When your laptop is also the docker host, just run:

```bash
quickdocker.sh user/myrepo --web    # -> 🔗 Open your factory:  http://127.0.0.1:<HOSTPORT>/
```

This stands up a detached, idempotent, `127.0.0.1`-only bridge to the console's in-container loopback
and prints a clickable URL — no SSH, no reading port files, no container rebuild. The full operator
runbook (access, `--shell`, prerequisites, multiple factories, restart, security, troubleshooting) is
[`web/README.md`](web/README.md).

**Alternative — operator SSH local-forward (headless / remote docker hosts).** When the docker host is
*not* your workstation (a remote or headless server, where the `--web` bridge cannot reach a local
browser), reach the console with an operator **SSH local-forward**, which keeps the socket on loopback
at both ends:

```bash
# Find the console's port from inside the container (printed at startup, or read the rendezvous file):
cat .runtime/webui_server.json     # -> {"transport":"tcp","address":"127.0.0.1:PORT",...}

# From your workstation, forward a local port to the container's loopback console port:
ssh -L 127.0.0.1:8888:127.0.0.1:PORT user@host
# then open http://127.0.0.1:8888/ in your browser
```

When the bind is ever non-loopback (not the default), the console additionally requires a
session token (printed at startup) as defense-in-depth — but that is **not** a license to publish
the port; the socket stays loopback whether you reach it via the `--web` bridge or the SSH forward.

## Architecture

Agentfactory has three layers:

1. **Agent templates** (`.md.tmpl`) — thin persona shells that define identity, startup protocol, and available commands
2. **Formulas** (`.formula.toml`) — declarative workflow definitions with steps, DAG dependencies, variables, and gates
3. **`af` runtime** — bridges the two: instantiates formulas as beads, injects context via `af prime`, tracks progress via `af done`

Agents don't need to know their full workflow. They run `af prime` to get the current step, execute it, run `af done` to advance, and repeat. On context compression, `af prime` re-injects identity and step context automatically.

### Key directories

```
.agentfactory/
  factory.json              # root marker
  agents.json               # agent registry
  messaging.json            # mail groups
  agents/<name>/            # per-agent workspace
    CLAUDE.md               # role template
    .claude/settings.json   # hooks
.agentfactory/store/
  formulas/                 # formula TOML files
.agentfactory/hooks/                      # quality/fidelity gate scripts
```

## Command Reference

### Agent lifecycle

```bash
af up [agents...]                  # start agent tmux sessions
af down [agents...] [--all]        # stop sessions
af attach <agent>                  # attach to a running session
af install --init                  # initialize factory
af install <role>                  # provision an agent
```

### Messaging

```bash
af mail send <to> -s <subj> -m <msg>   # send mail
af mail send @all -s <subj> -m <msg>   # broadcast
af mail inbox                           # list unread
af mail read <id>                       # read message
af mail reply <id> -m <msg>             # reply
```

### Agent & Formula execution

```bash
af sling --agent <name> "task"                            # dispatch task (common/simple use)
af formula agent-gen <name>                               # generate your own specialist agent
af sling --formula <name> --var key=val --agent <agent>   # instantiate formula (uncommon/complex use)
af prime                                                  # inject identity, get next step instruction (used by agents)
af done                                                   # complete and advance to next step (used by agents)
```

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines, CLA requirements, and development setup.

## License

AGPL-3.0. See [CONTRIBUTING.md](CONTRIBUTING.md) for commercial licensing inquiries.

### Disclaimer
The contributors to this project take no responsibility for your agent (or their respective LLMs) actions.

Good luck, and enjoy your Factory of Agents!