# Changelog

Notable changes to agentfactory. The project began 2026-05-01; snapshot tags `V001`–`V012`
mark pre-release checkpoints. `v0.1.0` is the first formal release.

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
