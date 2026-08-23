# Changelog

Notable changes to agentfactory. The project began 2026-05-01; snapshot tags `V001`–`V012`
mark pre-release checkpoints. `v0.1.0` is the first formal release.

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
