# Using Integrations

An integration brings a third-party tool into agentfactory sessions: Claude Code hooks, skills and MCP
servers, a long-running local service, and the environment keys they need. It ships as a plugin
repository with an `af-integration.toml` manifest at its top level. You install it with the same
`af plugin` verbs as a formula plugin, and nothing it carries runs until you install it.
[USING_PLUGINS.md](USING_PLUGINS.md) is the reference for those verbs. This guide covers the operator
flow, the manifest, which tools agentfactory works with and how, what fails and what you see when it
does, and what you are trusting when you install one.

## The operator flow

```bash
af plugin acquire <name>                     # copy a reference integration compiled into af into the store
af plugin install <name>                     # consent: fetch, run [install], verify, stage a read-only snapshot
af plugin install <name> --factory-wide      # required instead when the manifest says scope = "factory"
af plugin check <name>                       # run its [check] and record the result
af plugin verify <name>                      # re-hash the snapshot and name every changed file
af plugin list --json                        # each integration's scope, snapshot dir and last check
af plugin remove <name>                      # drop the record and unpinned snapshots; external writes stay
```

- **Acquire** is only needed for the references `af` ships (below). A third-party integration is cloned
  into `.agentfactory/store/plugins/<name>/` like any plugin repository. `acquire` and `install` are
  operator-only: an agent session is refused.
- **Install** is the consent step. Every refusal fires before anything is written. Install fetches the
  pinned `[upstream]` commit, runs `[install] run` with stdin closed under its timeout, checks that every
  declared external write exists, and stages the manifest, the `[claude]` dirs and the run scripts as
  `.agentfactory/store/integrations/<name>/<content sha256>/`. Each launch binds from that snapshot,
  never from `store/plugins/`.
- **`--factory-wide`** is a second act for anything that binds to every agent. A `scope = "factory"`
  integration is refused without it, and a `scope = "formula"` one is refused with it.
- **Check** can be run by agents. Run it after install and after anything it depends on changes. A
  launch only reads the recorded result. Admission of a formula that declares the integration re-runs
  the check unless an `ok` record for the same content is younger than the manifest's `fresh_for`.
- **Remove** prints every `external_writes` path and deletes none of them. Removing what the install
  script put outside the factory is yours to do.

### The references compiled into `af`

| Name | Scope | What it binds | Install |
|---|---|---|---|
| `aws-agents` | formula | the AWS Agent Toolkit plugin (skills and the remote AWS Knowledge MCP server) | `af plugin acquire aws-agents && af plugin install aws-agents` |
| `playwright` | formula | the Playwright MCP server, on the integration's own locked CLI and chromium build | `af plugin acquire playwright && af plugin install playwright` |

## Binding integrations to formulas

A formula names the integrations it uses:

```toml
integrations = ["aws-agents"]              # required: sling refuses when it is missing or unhealthy
integrations_optional = ["playwright"]     # bound when installed and healthy, otherwise skipped and named
```

- **Required** (`integrations`). `af sling` and the `af up` formula block admit each name before any bead
  exists. A missing, drifted or failing integration refuses the instantiation, naming the cause and the
  verb that fixes it.
- **Optional** (`integrations_optional`). A missing or unhealthy integration is skipped. The manager is
  told once (`INTEGRATION_SKIPPED`), and `af prime` prints `integration <name>: skipped (optional): <reason>`
  in the agent's session.
- **Factory scope** ignores the formula's lists. A `--factory-wide` integration is bound to every agent
  the factory launches, whatever its formula declares.
- **The pin.** Admission records what it bound in the agent's `.runtime/integration_bindings`. Every
  launch and respawn of that formula instance delivers exactly those snapshots, so installing or
  re-installing mid-run does not change a running instance.

Whenever a pin exists, af's session guard is on. An autonomous session cannot answer a runtime
permission or MCP elicitation prompt, so the guard denies it and mails `INTEGRATION_GUARD_DENIED` once
per integration. Installing any factory-scope integration therefore turns the guard on for every agent.

## Manifest reference

```toml
name = "example"                     # must equal the directory name
description = "one line"
scope = "formula"                    # or "factory" (binds to every agent; install needs --factory-wide)

[upstream]                           # fetched at install into .upstream/
repo = "https://example.com/org/repo"
commit = "<40 lowercase hex>"        # a commit sha, never a branch or tag

[install]
run = "af/install.sh"                # relative, inside the plugin dir; stdin is closed
timeout = "10m"                      # default 10m
external_writes = ["~/.local/bin/example", "~/.example/"]
shared = false
# [[install.artifacts]]
# url = "https://example.com/example.tar.gz"
# sha256 = "<64 lowercase hex>"

[check]
run = "af/check.sh"
timeout = "30s"                      # default 30s
fresh_for = "0"                      # how long an ok record is reused at admission; default 0

[service]
session = "example"                  # tmux session; may not start with af-, telemetry or litellm
run = "af/serve.sh"                  # runs in the foreground; the tmux session is the process
probe = "http-healthz"               # or "tmux-session"
healthz_url = "http://127.0.0.1:8080/health"

[claude]
plugins = ["claude-plugin"]          # Claude Code plugin dirs passed with --plugin-dir; at most 8
hook_fail_mode = "open"              # or "closed"; default open

[env]
EXAMPLE_TOKEN = "file:.agentfactory/secrets/example.token"
```

| Key | What it does |
|---|---|
| `scope` | `formula` binds only where a formula declares the name. `factory` binds to every agent and needs `--factory-wide`. A factory-scope and a formula-scope integration cannot be installed in one command. |
| `[upstream]` | A pinned commit, fetched without prompting (ssh runs with `-o BatchMode=yes`). A `[claude]` dir can point into it as `.upstream/<path>`. |
| `[install] external_writes` | Every path the install script writes outside the factory, absolute or `~/`-relative, never a glob. A trailing `/` means a directory. Install fails if one was not written. Regular files are hashed into the record. |
| `[[install.artifacts]]` | A download the script fetches, with its checksum. Install fails unless the sha256 matches one of the declared external-write files. |
| `[install] shared` | When `true` and every declared external-write file already exists on this host, install adopts them and skips `run`. Use it for a tool a second factory under the same HOME already installed. |
| `[check]` | A health probe. A timeout counts as a failure. Output is recorded with secrets masked. |
| `[service] probe` | `tmux-session` means the session exists. `http-healthz` means `healthz_url` (loopback only, redirects not followed) answers 200 within 2 s. A service is started when absent and never killed, and its relaunches back off. |
| `[claude] hook_fail_mode` | Exported to the session as `AF_INTEGRATION_HOOK_FAIL_MODES` (`name=mode`, comma-joined), for the integration's hooks to honour. |
| `[env]` | Keys exported at launch. A `file:` value is read from under `.agentfactory/secrets/` or the integration's own directories. Literal credentials, toolchain keys (`PATH` and the like) and the `ANTHROPIC_*` redirect family are refused. Each key has one owner. |

Install also refuses a `[claude]` dir whose `settings.json` sets `agent`, a `plugin.json` with
`defaultEnabled: false` or a `userConfig` option without a default, a `bin/` name that would shadow
the af toolchain, and a plugin name another integration or a user-scope Claude Code plugin already uses.

## Using agentfactory with other tools

| Repository | Direction | How | What you type |
|---|---|---|---|
| AWS Agent Toolkit — https://github.com/aws/agent-toolkit-for-aws | inbound | the `aws-agents` reference: the toolkit's `plugins/aws-agents` at a pinned commit; its check probes the Knowledge MCP endpoint; formula scope. The toolkit's `aws-core` plugin needs a browser login that expires after 12 hours, so no reference ships for it and it should never be bound to an unattended formula | `af plugin acquire aws-agents && af plugin install aws-agents` |
| Foundry security spec — https://github.com/CiscoDevNet/foundry-security-spec | inbound | not a shipped reference. Package the spec as a `[claude]` plugin with one skill indexing it (keep its CC BY 4.0 attribution), in your own plugin repository | `af plugin install <your-repo-name>` |
| Gas Town — https://github.com/gastownhall/gastown | neither | both are orchestrators of the same sessions, so they are not run inside each other. Formulas can be ported by hand | none |
| claude-squad — https://github.com/smtg-ai/claude-squad | none | it has no headless mode, API or hooks to integrate with; `af attach` and the web console cover watching sessions | none |

## When something fails

| What you see | Where | What it means | What to do |
|---|---|---|---|
| `plugin "<name>" declares scope = "factory"; binding a guard to every agent needs --factory-wide` | `af plugin install` | a factory-scope install without the second act | re-run with `--factory-wide` |
| `--factory-wide applies only to scope = "factory" integrations; plugin "playwright" is scope = "formula"` | `af plugin install` | the flag on a formula-scope integration | re-run without `--factory-wide` |
| `cannot install "<name>" (factory) and "aws-agents" (formula) in one command; run: …` | `af plugin install` | a mixed batch | run the two commands it prints |
| `plugin name "playwright" is already provided by a user-scope plugin; rename it; or remove it: claude plugin uninstall … --scope user` | `af plugin install` | a Claude Code plugin enabled for your whole user already has that name | run the printed uninstall (see the migration below), then install again |
| `af-integration.toml: unknown key …` | `af plugin acquire` / `install` | a misspelt manifest key | fix the manifest; the message suggests the nearest key |
| `[install] run af/install.sh exited 1; nothing was staged or recorded. Last … lines:` | `af plugin install` | the install script failed (or `timed out after …`) | read the tail it prints; nothing needs cleaning up in the factory |
| `[install] declared external write … was not written` | `af plugin install` | the script did not produce a path the manifest promised | fix the script or the manifest |
| `… required by formula "x" not installed: aws-agents — run af plugin list, then af plugin install aws-agents` | `af sling`, `af up` | a required integration is missing | install it |
| `integration "x" check failed at <time>: <output>` | `af sling`, `af up`, or the dispatcher's `INTEGRATION_REFUSED` mail | a required integration failed its check twice | fix what the output names, then `af plugin check <name>` |
| `integration <name>: skipped (optional): <reason>` | `af prime`; `INTEGRATION_SKIPPED` mail to the manager | an optional integration was not bound for this instance | fix it; the next formula instance binds it |
| `integration "aws-agents" not bound: content changed since consent (3 files; run af plugin verify aws-agents)` | `INTEGRATION_NOT_BOUND` mail; `af prime` | someone edited the read-only snapshot | `af plugin verify <name>` names the files; `af plugin install <name>` re-consents |
| `INTEGRATION_DEGRADED <name>` | mail to the manager, once per state change | a bound factory-scope integration's check is failing; it stays bound | `af plugin check <name>` shows the cause |
| `INTEGRATION_GUARD_DENIED <name>: permission …` | mail to the manager, once per integration | a bound plugin raised a prompt nobody can answer, and the guard denied it | grant the operation in the plugin's settings, or run it interactively |
| `scan-gate: tool absent (defenseclaw not bound)` | `fable-secure`'s stage artifacts | no DefenseClaw integration is bound to that instance; af does not currently ship a DefenseClaw reference | nothing; the stage records the missing scan and carries on |
| a service keeps restarting, then one supervisor mail | `af up`, the watchdog | the service exits soon after launch; relaunches have reached their backoff cap | attach to its tmux session and read why it exits |

## What you are trusting

Installing an integration grants code execution as the agent user (hooks, MCP servers, `bin/`, the
service) and an instruction channel (skills, session-start context) into every session it binds to.
The controls are: an explicit operator verb; `--factory-wide` as a second act for anything that binds
everywhere; pinned upstream commits and artifact checksums; a consumed copy hashed at consent and
re-hashed at every launch; denied `[env]` keys; reserved `bin/` names; and operator-only install and
remove.

There is no sandbox. Every check runs with the same privileges as what it checks. Identity cannot be
replaced (install refuses a plugin that sets `agent`), but it can be added to through skills and session
context, and that is accepted by installing. What the launch-time hash guarantees is narrow: the
declared content is byte-identical to what you installed. It does not cover:

- remote MCP servers (the AWS Knowledge server) and anything an `npx` or `uvx` launcher fetches when the
  session starts, which is why the references pin those package versions;
- `external_writes`: their hashes are recorded at install and never re-checked, so a binary changed
  afterwards in `~/.local/bin` is not detected;
- state the tool keeps for itself (a virtualenv, a database, a token file, its own configuration), which
  is observed only through the integration's `[check]`.

Uninstalling never deletes `external_writes`.

Each reference's third-party licence notice is in [THIRD-PARTY-LICENSES](THIRD-PARTY-LICENSES).

## Where is my configuration?

- **What was consented to**: `.agentfactory/plugins.json` (the record) and
  `.agentfactory/store/integrations/<name>/<sha256>/` (the snapshot). `af plugin list --json` prints both.
- **Last check result**: `.runtime/integration_check/<name>.json` at the factory root.
- **What an agent's formula instance bound**: `.runtime/integration_bindings` in the agent dir. `af prime`
  in its session prints one line per pinned integration.
- **What the running session actually received**: the process environment of its `claude`.
  `--plugin-dir` arguments and `[env]` keys are not copied into tmux, so read the process:

  ```bash
  tr '\0' '\n' < /proc/$(ps -o tpgid= -p $(tmux display -p -t af-<agent> '#{pane_pid}') | tr -d ' ')/environ \
    | grep -E '^(AF_INTEGRATION_HOOK_FAIL_MODES|<KEY>)='
  ```

  `<KEY>` is one of the integration's `[env]` keys. `file:` values are already dereferenced there, so
  redact secrets before sharing the output.
- **Service state**: `.runtime/integration_service/<name>.json` (launch time, relaunch count) and the
  tmux session the manifest names.

## Migrating Playwright off the user-scope plugin

Older quickstarts enabled the official Playwright Claude Code plugin for the whole user. It is now an
integration that a formula opts into, on the integration's own locked CLI and chromium build.
Quickstart no longer installs the user-scope plugin, and af never deletes it for you. In each existing
container:

```bash
claude plugin uninstall playwright@claude-plugins-official --scope user
af plugin acquire playwright && af plugin install playwright
af up
```

Run the install without `--factory-wide`, because `playwright` is a formula-scope integration. Until the
uninstall runs, `af plugin install playwright` refuses and prints that uninstall command, and a bare
`af up` names the leftover plugin in its warning line. After the migration, that warning no longer
lists `playwright`.
