# Changelog

Notable changes to agentfactory. The project began 2026-05-01; snapshot tags `V001`–`V012`
mark pre-release checkpoints. `v0.1.0` is the first formal release.

## Unreleased

Plugins, a ChatGPT-subscription gateway, and relaunches that run on exactly the config you
set. Extend the factory with third-party agents and tools behind a trust boundary, bill the
model gateway to a ChatGPT subscription, and get every relaunch to honor the configuration
you declared instead of whatever an earlier launch left behind. (#117)

### Extend — plugins and integrations

- Add a third-party agent from a git repository: `af plugin install <name>` validates,
  stages, records and verifies it; `af plugin list` reviews what's acquired; `af plugin
  verify` reports drift against recorded provenance (source, commit, content hashes).
  Install is the consent step, installed agents survive redeploys, and a name that would
  shadow a shipped / `manager` / `supervisor` agent — or a role template not built into the
  binary — is refused. New operator guide `USING_PLUGINS.md`. (#117)
- Give agents tools: an integration installs from a pinned upstream commit as a read-only
  snapshot; literal credentials, reserved env keys and toolchain names are refused; formulas
  declare required/optional integrations and reference a plugin's skills as `plugin:skill`;
  a formula whose required tool is missing, drifted, unhealthy or down is refused before any
  bead or worktree exists; `af plugin check` runs health checks; `af up` and the watchdog
  supervise integration services without ever killing a session. (#117)
- Unattended agents stop hanging on prompts: in a session bound to an integration,
  permission prompts and MCP input requests are answered "no" at once and the manager is
  mailed what was refused. (#117)

### Bill the model gateway to a ChatGPT subscription

- `quickstart.sh --litellm --litellm-auth=codex-subscription` (or `af install --agents
  --litellm --litellm-auth=codex-subscription`) installs the Codex CLI with consent, runs
  the device login before any agent is taken down, then routes every lane through the
  subscription; a compatibility callback fixes the "System messages are not allowed"
  rejection that otherwise breaks every Claude Code turn on that route. (#117)
- `af gateway auth import` / `af gateway auth status` manage and audit the credential,
  stored readable by its owner only; neither command ever prints a token. (#117)
- `af config models check --live` now sends the same kind of request a real session sends
  (streamed, block-array system prompt), reports a timeout separately, and quotes the
  gateway's own error text instead of a bare 500; `--first` gives one quick verdict line.
  Bootstrap restarts the gateway whenever its config, auth mode, credential, litellm version
  or port has changed; a port held by a process af did not start is refused; `af up` and the
  watchdog bring back a gateway that has gone missing. (#117)

### Every relaunch runs on exactly the config you set

- Handoffs, watchdog and recovery respawns, `af sling` and the dispatcher now build the
  launch the same way `af up` does. Removing a build host or setting a git identity takes
  effect on the next recycle; build delegation reaches agents started by `af sling` and the
  dispatcher; no auth token sits in the tmux environment; a window opened by hand no longer
  inherits an agent's endpoint or model; a malformed `build-host.json` warns instead of
  aborting every agent. (#117)
- Effort levels mean what they say: turning tokenomics off no longer wipes the effort level
  declared in `models.json`; a reduced-effort launch is recorded on the session, survives
  formula completion, never leaks into a later session and cannot be forged by a profile;
  the grader is told of a reduction only when the session it is grading actually ran reduced;
  `af fidelity status` gains `interventions` and `effort` columns, and the statusline shows
  effort next to the model. (#117)

### Also in this change

- `af up` leaves a running agent alone — it prints `already running` and changes nothing
  (with `--model` it tells you the model was not applied); a zombie session (tmux alive,
  claude dead) is still relaunched. (#117)
- The dispatcher records a refused dispatch with its reason, backs off before any retry, and
  shows it in `af dispatch status` as `refused (<class>, Nx)` without counting toward the
  attempts ceiling. (#117)
- Shipped formulas no longer run their own prose as shell: a test reads the shell embedded
  in every shipped formula and fails on constructs that would execute or break when the
  orchestrator runs them; rapid-soldesign-plan, design-plan-impl, design-v7, fable-increment
  and fable-review fix their measured failures. (#117)
- New formulas `rapid-soldesign` (a rapid solution design that ends at the design PR, with no
  implementation plan) and `lineage` (an inheritance audit that tests the memory-vault notes
  an agent inherited before it acts on them, keeping or expiring each), and a new
  `architecture-diagram` skill (a design doc → C4 and Mermaid diagrams in which every element
  cites a line of the document). Shipped formulas 24 → 26; skills 12 → 13. (#117)
- New ADR-024 (gateway protocol shims and the LiteLLM pin gate) and ADR-025 (the plugin
  repository trust boundary); updates to `USING_LITELLM.md`, `USING_MODELS.md`,
  `USING_TOKENOMICS.md`, `USING_AGENTFACTORY.md`, `USING_RECOVERY.md`, `USING_TELEMETRY.md`
  and `CLAUDE.md`. (#117)

## v0.4.0 — 2026-09-17

Token economics. The factory can now **see, bound, and prove** what a run costs its own
context window — the questions you couldn't answer before: is this run normal, and did my
change actually help? Off by default. (#111)

### See — generation telemetry

- Every closed step records what it *generated* (output, thinking, peak, sub-agent spend)
  beside its timing, with unmeasured figures kept as `null`, never `0` (#111)
- `af telemetry band` judges each closed step against its learned medians and prints the
  tolerance band it used; `af telemetry rebuild` re-derives that learned data from the records
  so it survives record rotation (#111)

### Bound — admission control, adaptive effort, context budgets

- Sub-agent admission control: a model profile can declare its shared backend pool
  (`AF_BACKEND_POOL_TOKENS`), and a child launch that would oversubscribe the pool is refused
  *before it starts*, with the arithmetic shown — through the permission channel, never a
  non-zero exit. A child-footprint floor (`AF_BACKEND_CHILD_FLOOR_TOKENS`) guards the first
  child near the ceiling and `AF_DISABLE_PARALLEL_SUBAGENTS` enforces a hard semaphore of one.
  Fails open on any resolution error; structurally inert on any profile that declares no pool
  (every cloud profile) (#111)
- Adaptive effort: a step that historically over-generates gets a reduced effort level on its
  next session, chosen from history, never above the profile's ceiling, and bounded so a run
  can't spend itself relaunching; self-edits from the improvement loop can't buy tokens by
  deleting a gate (#111)
- Session-start context budgets: formula context, mail, and memory each get their own budget,
  so a large step body can no longer evict your mail, and each message reaches a session once
  (#111)

### Prove — the compare verb

- `af telemetry compare` is the only verb allowed to claim a change worked — it weighs two
  arms of runs and *voids* the verdict rather than fabricating one when the arms weren't held
  constant; `af tokenomics status` names every reason the surface is inert (#111)

### Also in this release

- Recurring dispatch: `dispatch.json` accepts a `crons` list (name, agent, cadence, vars);
  schedules survive restarts, back off with a bound, validate against the target formula at
  write time, fire without GitHub access, and appear in `af dispatch status` (#111)
- The fidelity grader is shown every intervention taken during a turn and grades compliance
  accordingly; grader sub-processes no longer fire the agent's hooks or take its session id;
  the watchdog no longer recycles an agent told to wait (#111)
- mergepatrol counts a PR merged only when GitHub says so, not local git (#111)
- Two new skills (10 → 12): `/improve-solution` and `/perfeval-agent`, both shipped by
  `af install`; new operator guide `USING_TOKENOMICS.md` (#111)

## v0.3.0 — 2026-08-23

Self-recovery, durable memory, and honest surfaces. The factory now recovers from context
exhaustion on its own, keeps what its agents learn across teardowns, and tells the operator
the truth on every surface — including when it has no data. (#104)

### Self-recovering agents

- Automatic context-exhaustion recovery: the watchdog watches every agent's context
  occupancy and recycles an exhausted agent with its state checkpointed, so a filled window
  no longer wedges the agent burning tokens indefinitely (#104)
- A durable recovery breaker halts repeated re-stall loops and escalates to the operator
  instead of destroying work over and over; `af recovery reset <agent>` re-arms it after you
  investigate (#104)
- Supervision is always on: default factories are no longer unsupervised, a heartbeat file
  proves the watchdog itself is alive, and steps ending near a full window hand off to a
  fresh session cooperatively (#104)

### Durable memory

- A memory vault (`af memory add|list|check|status|export`) survives step close, teardown,
  reset, and worktree removal; learnings are re-injected (bounded) at session start; and
  `quickdocker.sh` can bind the vault to a host directory so `docker rm` can't take it (#104)

### Live statuslines

- `af statusline` renders model, directory, git branch, diff size, elapsed time, a
  context-fill bar, and session/daily spend with accurate token accounting; `af statusline
  status` self-diagnoses its pipeline and warns about config drift (#104)

### Config integrity

- Config setters reject unknown keys instead of silently erasing them on write-back,
  validate agent-name references across files, and accept an optional `--if-content-hash`
  compare-and-set precondition so concurrent edits conflict instead of clobbering; new
  setters for messaging and statusline config (#104)
- `af config fingerprint` reports a digest of the config schema this binary speaks, so any
  consumer can detect schema skew before writing; `af config models check` verifies per-class
  model coverage before a dead sub-agent does (#104)

### Fair quality gates

- Gate judges now see exactly the turn being graded — calls paired with their results,
  sub-agent noise excluded — and block only on contradiction, never on absence of proof;
  `af fidelity off --agent <name>` exempts one misfiring agent instead of the whole factory,
  every toggle is recorded in a provenance log, and agents can no longer disable their own
  grader (#104)

### Honest observability

- Telemetry step records carry context occupancy, consumption, and budget verdicts;
  interrupted steps show as INTERRUPTED instead of vanishing; and "not measured" is always
  distinct from "zero" in the table, the JSON, and the web console (#104)
- The web console Settings surface covers messaging, statusline, and model-profile pins
  without silently erasing keys it doesn't understand, saves per panel with write
  preconditions and an audit line, and leaves absent files absent instead of inventing
  defaults; the Floor shows context-fill and recovery badges (#104)

### Formulas, docs & CI

- Formula fixes: no literal `{{placeholder}}` text in operator mail or PR titles, artifacts
  survive to the PR, and an unavailable review sub-agent is recorded as unavailable rather
  than as a clean pass (#104)
- CI now runs the web module, client-JS conformance lanes, and the hook end-to-end tests it
  was silently skipping (#104)

## v0.2.0 — 2026-08-03

Observability and multi-provider agents.

### Observability

- Run telemetry: `af telemetry on|off|status|report|usage` records per-step latency and
  token usage for every agent and formula instance — a local timing table plus a backend
  usage query. Off by default; opt-in factory-wide, taking effect at the next session
  launch (#92)

### Multi-provider agents

- Agents can run on non-Claude models: `af config models` (show/set/check/attest) over a
  `models.json` registry; `af install --agents --litellm` sets up an OpenAI gateway;
  `af sling --model` overrides the model per launch; a fitness-attestation gate guards
  non-loopback profiles; `gpt-fable-review` and `gpt-rootcause-all` run review and
  root-cause formulas on OpenAI models (#92)

### Reliability & safety

- Operator-only factory teardown: factory-wide `af down` is now gated so an agent cannot
  tear down the whole floor (#92)
- Reliable improvement self-edits: the AND-gated continuous-improvement hook
  (`af improvement`) hardened so agents apply post-run formula edits reliably (#92)

### Formula & agent library

- New shipped formulas: `fable-secure` (security-program review), `multi-agent`
  (multi-perspective architecture consultation), and `marketing-cycle` (a self-marketing
  cycle for the repository the factory serves) (#92)

## v0.1.0 — 2026-07-11

First formal release, consolidating ten weeks of development.

### Orchestration core

- Formula system: declarative TOML workflows with steps, DAG dependencies, variables, and
  gates; `af sling --formula` instantiation and `af prime`/`af done` step tracking (#2)
- Agent generation from formulas: `af formula agent-gen` creates workspace, role template,
  and hook configuration with no manual file moves (#8)
- Skill-to-formula pipeline: `/formula-create` turns a `SKILL.md` into a runnable formula;
  built-in skills embedded and extracted during `af install --init` (#16)
- Prime-before-done enforcement with velocity tracking, formula skill validation, and
  per-agent model/endpoint configuration (#43, #81)
- Startup.json-driven `af up` with declarative agent subset selection, dispatcher
  auto-start, and scoped watchdog (#58)

### Reliability & recovery

- Mandatory step execution block and fidelity hook corrections for agents drifting off
  formula steps (#26, #28)
- Worktree isolation hardening: dispatched agents get independent worktrees; teardown gated
  on session termination; branch-committed skills preserved (#30, #32, #61, #69)
- Unified reset semantics: `af sling --reset` and `af down --reset` perform identical full
  cleanup — worktrees, open work items, runtime state, checkpoints (#40)
- Gate locks migrated to `.runtime` with stale-PID recovery (#43)
- Test/production tmux isolation with build-tag-gated constructor guard; compact-handoff
  PreCompact hook for context-compression safety (#52)
- Agents made default-branch-agnostic; regen/lint CI gates (#63)

### Multi-agent coordination

- Inter-agent mail over the issue store, with broadcast groups and reply threading
- Autonomous dispatch: PR/issue label matching, multi-label AND semantics, dispatch cycle
  locking, idle back-off, phase advancement, and issue→PR handoff (#36, #38, #79)
- MergePatrol PR-review agent with label-based discovery (#36)

### Formula & agent library

- rapid-implement, rapid-increment, ultra-review formulas (#65); web-design agent with
  consensus gate (#68); minimalworker (#52); the fable agent family — fable-implement,
  fable-increment, fable-review (#83)

### Web console

- Loopback-only web console: Floor view, task slinging, dispatch status, settings, design
  prototypes; singleton-launch rendezvous; agent detail and operator mail (#72, #81, #83)
- Browser formula authoring (#83)

### Platform & tooling

- Containerized setup via `quickdocker.sh` + `quickstart.sh`; stack-agnostic customer repo
  discovery (#56); iOS build-host configuration with ssh-agent forwarding (#48, #50)
- Server-wide tmux mouse/clipboard UX at session creation (#77)
- CI: unit, integration, template-regen, and supply-chain-lint jobs

[Full commit history](https://github.com/stempeck/agentfactory/commits/main)
