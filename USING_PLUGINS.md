# Using Agentfactory: Plugin Repositories

Operator guide for plugin repositories: adding third-party specialist agents and integrations to a
factory, the `af plugin list|install|verify|acquire|check|remove` verbs, the load-bearing
`.agentfactory/plugins.json` manifest, and how to update or fork an installed plugin. Split out of
[USING_AGENTFACTORY.md](USING_AGENTFACTORY.md) — start there for factory setup and day-to-day
operation.

## Plugin repositories

A plugin repository is a git repository whose top level holds `*.formula.toml` files, or an `af-integration.toml` manifest (an *integration*; see [Integrations](#integrations-af-integrationtoml)). It lets you add third-party specialist agents and tools to a factory without promoting them into the AF source tree. `af --help` and `af plugin --help` list the six verbs: `list`, `install`, `verify`, `acquire`, `check` and `remove`.

**Prerequisite: the AF source tree.** `af plugin install` runs the same script pipeline as `af install --agents` (`agent-gen-all.sh`, then `quickstart.sh`), so it needs the agentfactory source tree (resolved the same way: `AF_SOURCE_ROOT`, or the compiled source root). An operator who only has a `go install`ed binary and no source tree cannot install plugins: the install is refused before anything is written. `af plugin list` and `af plugin verify` are read-only and work without it. An integration-only install (every plugin in the batch has a manifest and no formulas) does not rebuild agents, so it needs no source tree either.

### Happy path

Run all three from the main project checkout's factory root. Do not run them from a worktree, a subdirectory, or from inside the cloned plugin repo:

```bash
cd ~/af/myproject
git clone https://github.com/acme/agentfactory-plugin-agents .agentfactory/store/plugins/acme
af plugin list                  # review what the plugin would install; nothing changes yet
af plugin install acme          # consent: validate, stage, record, rebuild, verify
af up acme-triage               # start one of its agents
```

The directory name under `.agentfactory/store/plugins/` is the plugin name, and it must be a valid agent name (`[a-zA-Z][a-zA-Z0-9_-]*`). Cloning is inert: nothing in the factory changes until `af plugin install`.

### `af plugin list [--json]`

Read-only. For each directory under `.agentfactory/store/plugins/` it prints the clone's git source and commit and, per formula, the agent name and an install status:

- `ok` — installable.
- `changed` — a formula this plugin already installed whose bytes differ from the sha256 recorded in `plugins.json` (an upstream update). Still installable; the detail shows the recorded and the new sha256.
- `removed` — a formula this plugin recorded that the clone no longer ships. Nothing is staged for it; see [Updating a plugin](#updating-a-plugin-track-or-fork).
- `out-of-contract` — a formula below the clone's top level. Ignored, never installed.
- Refused by `af plugin install`: `invalid-name`, `name-mismatch` (the TOML `formula` field differs from the file name; set it to the file name without `.formula.toml`), `parse-error`, `missing-skills`, and the collision classes `collides-with-store-formula`, `collides-with-manual-agent` and `collides-with-embedded-agent` (see **Collisions** below).

A directory that cannot be installed (an invalid name, or unreadable) is listed as `NOT INSTALLABLE` with the reason instead of being left out. Third-party strings (names, file names, source) that contain control characters are printed quoted, so a hostile name cannot drive your terminal.

`--json` always prints an array (`[]` when there are no plugins, and every `formulas` is an array too). A directory that cannot be installed is a row with an `error` field and `"formulas": []`. An infrastructure error prints `{"state":"error","error":"..."}` and still exits 0. An `agents.json` that exists but cannot be loaded is such an error for both `list` and `install`; a factory without an `agents.json` simply has no agents to collide with.

### `af plugin install <plugin> [<plugin>...]`

The explicit consent verb (no prompt; running it is the consent). It is operator-only: inside an agent session it is refused, because the pipeline runs `af down --all`. In order, it:

1. Refuses a plugin named more than once on the command line.
2. Validates the whole batch, then checks the preconditions (worktree, missing AF source tree or scripts, agent context, a cwd that is not the factory root). A validation refusal comes first and prints the set it refused; a failed precondition refuses before anything is printed. Both happen **before the first write**, so a refused install leaves the store and `plugins.json` untouched.
3. Prints the set it is about to install (each formula's agent and status, each `removed` formula with its remediation, and a third-party-content note), then stages each formula into `.agentfactory/store/formulas/` and records the plugin in `.agentfactory/plugins.json`. The record is replaced with what the clone ships now, so a `removed` formula drops out of it.
4. Runs the `af install --agents` pipeline (agents are left stopped; run `af up`). `--no-build` behaves as it does there. Unlike `af install --agents`, it runs quickstart with `--no-telemetry` and leaves the telemetry gate unchanged, and it never passes `--litellm`: on a factory with a LiteLLM gateway the gateway is left as deployed, and `af install --agents --litellm` reconciles it.
5. Runs `af plugin verify <plugins> --json` in the freshly rebuilt `af` on your `PATH`, and exits non-zero if any agent is not registered, embedded and hash-clean. Once verify passes, it repeats each `removed` formula's remediation. If that `af` is stale or does not know `af plugin`, the error names the `af` on `PATH` and the source tree it was built from.

**Collisions.** A plugin formula is refused when its agent name is already taken: by another plugin that recorded it, by a hand-authored agent in `agents.json`, by a formula af ships, by a different store formula, or by a built-in af identity, `manager` or `supervisor`, that no plugin records and `agents.json` does not register (`collides-with-embedded-agent`; rename it in the plugin repo). A plugin another factory on this host installed is not a collision here, even though the shared `af` binary already embeds its template. A byte-identical store copy is accepted only when this plugin recorded it, or when nobody recorded it and it is not registered in `agents.json` (the resume case after an interrupted install). An identical formula you wrote yourself and have not registered looks the same, so the plugin adopts it; run `af formula agent-gen <name>` on it first if you want the install to refuse. Re-installing the same plugin (for example after `git pull`) is a normal update: formulas whose bytes changed list as `changed` and install.

**Not the same as `af install --agents`.** Plugin install does not run the class-wide `af plugin verify --all` report; its own post-install verify covers the plugins it installed.

**Provenance.** `plugins.json` records each plugin's `source` (its git remote), `commit`, `installed_at` and the sha256 of every staged formula. Source and commit are recorded only when the plugin directory is the top of its own git repository; otherwise both are empty, never the enclosing factory repo's values. Credentials are stripped from the remote URL (user info, query and fragment) before it is recorded or printed. A `plugins.json` written before this stripping existed is not rewritten: if one recorded a credential, rotate it.

### `af plugin verify [<plugin>... | --all] [--json]`

- No arguments: every agent a recorded plugin installed.
- Named plugins: only their agents.
- `--all`: the recorded plugin agents **plus every formula agent in `agents.json`**, checked for registered and embedded in this binary. `hash_clean` applies only to plugin rows. A non-plugin agent that is not embedded is remediated with `af install --agents`, a plugin agent with `af plugin install <plugin>`.

Plugin names and `--all` are mutually exclusive; giving both is an error. A plugin agent fails when its `agents.json` entry is missing or runs a different formula, when its role template is not embedded, when its store copy is missing (re-run `af plugin install <plugin>`), or when the store copy's bytes differ from the recorded sha256 (drift). Any failing agent is a non-zero exit. `--json` prints the per-agent `results` with a top-level `"state"` of `ok`, `fail` or `error`, and still exits non-zero on `fail` or `error`, because `af plugin install` reads the exit code. An unreadable `plugins.json`, or under `--all` an unreadable `agents.json`, is an error, never a clean report. After a successful `af install --agents`, a factory with a `plugins.json` gets a report-only `af plugin verify --all`.

### `.agentfactory/plugins.json` is load-bearing

The redeploy's orphan passes (`agent-gen-all.sh`) consult it to keep plugin formulas and role templates that have no counterpart in the AF source tree. In a source-repo factory, commit the files `af plugin install` lists at the end (it is allowlisted in `.gitignore`); deleting `plugins.json` means the next redeploy deletes those plugin formulas and templates.

- **Absent** means zero plugins: every plugin behavior stays dormant.
- **Present but not usable** (a merge conflict, a truncated write, `{}`, `null`, a non-object `plugins`) is an error, never "zero plugins". `af plugin verify` fails with the parse error. While ownership cannot be read, every agent whose role template is not embedded in the binary — plugin agents and hand-authored agents alike — is refused until plugins.json is repaired, both at launch (`af sling`, `af up`) and at respawn (handoff, compact handoff, `af done`, the watchdog, recovery). Agents whose templates are embedded (manager, supervisor, and the formula agents af ships) still launch. `agent-gen-all.sh` preserves every orphan formula and template for that run and prints one `WARNING` naming the file.
- **Schema version.** It carries `"version": 2` (version 2 adds the `integration` block of an installed integration). Versions 1 and 2 both load, and a file without a version reads as version 1. A newer version is refused with an error naming both versions (upgrade `af`), which is also what an older `af` says about a version 2 file.

A plugin agent whose role template is not embedded in the running binary is refused at launch and at respawn with a message naming the owning plugin (`af plugin install <plugin>`, then `af plugin verify <plugin>`), instead of starting under a substituted generic identity. A refused respawn leaves the current session in place and is recorded as `respawn_failed` in the recovery log.

### Updating a plugin: track or fork

- **Track upstream:** `git -C .agentfactory/store/plugins/<name> pull`, review the change with `af plugin list`, then `af plugin install <name>`. A formula upstream dropped lists as `removed`: the install stops recording it (in a source-repo factory the rebuild then deletes its store formula and role template as orphans) and prints `af formula agent-gen <agent> --delete`, which removes its `agents.json` entry and workspace (run `af down <agent>` first if it is running).
- **Fork:** clone your own fork into `.agentfactory/store/plugins/<name>/` instead, and pull upstream into the fork only after reviewing it. This keeps what runs in your factory under your control.

The clone is its own git repository nested inside your factory. A `git` command run from inside `.agentfactory/store/plugins/<name>/` acts on the plugin's repository, not your factory's, which is why the commands above use `git -C`.

**Editing an installed plugin formula forks it.** If you edit `<factory-root>/.agentfactory/store/formulas/<agent>.formula.toml` after installing it (by hand or through the continuous-improvement hook), `af plugin verify` reports the agent as not hash-clean, and re-running `af plugin install <name>` refuses to overwrite your edit. Pick a side:

- **Track the plugin:** delete your edited copy, then run `af plugin install <name>`.
- **Keep your edit:** remove that formula's entry from the plugin's `formulas` in `.agentfactory/plugins.json`. The formula is then yours, and verify no longer checks it. In a source-repo factory, also promote it into `internal/cmd/install_formulas/`, because the redeploy deletes store formulas that are neither in the source tree nor recorded by a plugin. A later `af plugin install <name>` refuses that agent name as a collision.

### Integrations (`af-integration.toml`)

An integration is a plugin repository that carries a strict `af-integration.toml` manifest at its top level instead of formulas. The manifest can declare a pinned `[upstream]` commit, an `[install]` script with the paths it writes outside the factory (`external_writes`), a `[check]` health probe, a long-running `[service]`, Claude Code plugin directories under `[claude]`, and `[env]` keys. Unknown keys are refused. It is acquired and installed with the same verbs as a formula plugin, and the same rule holds: nothing runs until `af plugin install`.

```bash
af plugin acquire <name>                        # or: git clone <repo> .agentfactory/store/plugins/<name>
af plugin install <name>                        # add --factory-wide when the manifest says scope = "factory"
af plugin check <name>                          # run its [check] and record the result
```

- **`af plugin acquire <name>`** (operator-only) copies a reference integration compiled into `af` into `.agentfactory/store/plugins/<name>/` and records it as `embedded`, with the `af` version as its commit. It refuses a name `af` does not embed, and a `store/plugins/<name>/` that already exists. The `af` binary cannot carry file modes, so acquire makes a file executable (0755) when it is a run script, sits in the top-level `bin/` or a `[claude]` dir's `bin/`, or starts with `#!`; every other file is 0644.
- **`af plugin install <name>`** of an integration is the consent step. Every refusal (unknown manifest keys, a `scope = "factory"` integration without `--factory-wide`, a batch that mixes integrations and formula plugins, denied `[env]` keys, reserved `bin/` names, a plugin name another integration or a user-scope Claude Code plugin already uses) fires before any write. It then fetches the pinned upstream without prompting (ssh runs with `-o BatchMode=yes` added to your own ssh command, so an ssh upstream's host key must already be in `known_hosts`), runs `[install] run` with stdin closed under its timeout, verifies the declared external writes and artifacts, and stages the manifest, the `[claude]` dirs and the run scripts as a read-only snapshot, `.agentfactory/store/integrations/<name>/<content sha256>/`. Each staged file keeps its source's exec bit (recorded as 0755 when any exec bit is set, otherwise 0644), and the run scripts and those `bin/` files are always executable. The `plugins.json` record is written last. It does not rebuild agents or stop the factory, and it needs no AF source tree. Re-installing changed content adds a sibling snapshot and leaves the old one in place. The snapshots are ignored by `.agentfactory/store/.gitignore`; the record stays tracked.
- **`af plugin check [<name>... | --all] [--json]`** can be run by agents. It runs each integration's `[check] run` with stdin closed under its timeout and records the result in `.runtime/integration_check/<name>.json`, with secrets in the output masked. It re-hashes the snapshot first: if the content changed since consent, the check is not run and is recorded as `error` naming the drift. A timeout counts as a failure, and any failure exits non-zero. An intact integration without `[check]` records nothing. The report also names user-scope Claude Code plugins (`enabledPlugins` in `settings.json` under `CLAUDE_CONFIG_DIR`, default `~/.claude`) that no installed integration accounts for.
- **`af plugin list`** lists each installed integration with an `integration` object (kinds, scope, probe, snapshot dir and last check), read from the record, so it is listed even after its `store/plugins/<name>/` is gone. **`af plugin verify`** re-hashes the snapshot, names every changed file, and exits non-zero on drift.
- **`af plugin remove <name>`** (operator-only, integrations only) deletes the record entry, the integration's snapshots that no agent's pin names, and its `.runtime` check and service state. It prints every `external_writes` path and deletes none of them (ADR-017): removing what the install script put outside the factory is yours to do. A running service session keeps running.

**Services.** `af up` (bare or with agent names), a sling launch, a respawn and the watchdog start each factory-scope integration's `[service]` session, and each formula-scope one a formula instance's pin names, when the session is absent, back off between relaunches, and mail the supervisor once when the backoff reaches its cap. They never kill a live service: a failing `[check]` is reported, not acted on. A service is shared by every instance that binds it, so it starts from the snapshot `plugins.json` currently records, not from a pin's. A service whose snapshot changed since consent is not started, and is reported to the manager as `INTEGRATION_NOT_BOUND`. A bare `af up` also prints one warning line naming the unaccounted user-scope Claude Code plugins. It leaves out `playwright@claude-plugins-official` while quickstart installs that plugin itself; `af plugin check` still lists it.

**Binding to formulas.** A formula declares the integrations it uses with `integrations = [...]` (required) and `integrations_optional = [...]`. A skill of an integration's Claude Code plugin is named `<plugin>:<skill>` in `skills`.

- **Admission.** `af sling` and the `af up` formula block admit the declared names before any bead, `hooked_formula` or pin exists. A required name must be recorded in `plugins.json`, its snapshot must still hash to the recorded digest, its `[check]` must pass (a fresh `ok` record for the same content is reused; otherwise the check runs, once more on failure), and its `[service]` session must be up. A failure refuses the instantiation with one class: `integration-missing`, `integration-drifted`, `integration-snapshot-invalid`, `integration-check-failed`, `integration-service-down` or `namespaced-skill-unresolved`. An optional name that fails is skipped instead, with an `INTEGRATION_SKIPPED <name>: <reason>` report. `af sling --agent` runs a read-only pre-check first, so a refusal happens before the agent's session or worktree is touched; the dispatcher records the refusal class, backs off, and `af dispatch status` shows it.
- **The pin.** Admission writes `.runtime/integration_bindings` in the agent dir: the formula name and each bound integration's snapshot and digest, factory-scope ones included. Every launch and respawn of that instance delivers the pinned snapshots (their `[claude]` plugin dirs and `[env]` keys), so installing or re-installing an integration mid-run does not change a running instance. `af plugin remove` keeps snapshots that a pin still names. A new `af sling --agent` dispatch, `af sling --reset`, `af reset` and formula completion clear the pin.
- **Reports.** Each launch reports `INTEGRATION_NOT_BOUND` (a pinned snapshot no longer binds), `INTEGRATION_DEGRADED` (a failed `[check]` of a bound factory-scope integration, or its service failing health) and `INTEGRATION_SKIPPED`, and mails each condition once to the manager after the launch succeeds; the marker re-arms when the condition clears. `af prime` prints one line per pinned integration (`ok`, `check failed at <time>`, `not bound …` or `skipped …`).
- **Guard and containment.** With a pin present, the autonomous `PermissionRequest` and `Elicitation` hooks deny MCP prompts, which an unattended session cannot answer, and mail `INTEGRATION_GUARD_DENIED` once per integration. The containment hook allows `cd`, `git -C`, Write and Edit into a pinned snapshot dir.

### v1 limits

- There is no uninstall or update-diff verb for formula plugins yet (issue #525): `af plugin remove` handles integrations only, and `changed` reports hashes, not content, so read the change with `git -C` before installing it.
- Formula plugins do not install skills. A formula that uses a skill missing from the factory's `.claude/skills/` is refused as `missing-skills`; skills that are present are not covered by the hash and embed checks.
- The home formula store (`~/.agentfactory/store/formulas/`, searched after the factory store) is outside the plugin model: formulas there get none of the consent, provenance or verify checks.
