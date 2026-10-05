<!-- Cycle audit — marketing-cycle af-91fe0795 — written 2026-10-05 -->
# Cycle af-91fe0795 — Audit

**Assignment:** bead `af-91fe0795`, whose body is the URL of **PR #117**
(`stempeck/agentfactory`), MERGED to main 2026-10-05 (squash `135794f0`), closes #116:
"Plugins, ChatGPT-subscription gateway, and relaunches that never run on stale config."
383 files changed, +61,474 / −2,801.

**Mining boundary.** Last *released* and last *told* state is **v0.3.0** (tag + release
2026-08-23; the final ledger row). Everything in `v0.3.0..origin/main` is therefore the
untold delta. Two feature waves live in that window:

| Wave | PR(s) | CHANGELOG | Release tag | Ledger | Status |
|---|---|---|---|---|---|
| Token economics (v0.4.0) | #111 (+ Tier-A refresh #114) | `## v0.4.0 — 2026-09-17` present | **none** (latest tag still v0.3.0) | **no row** | documented, never tagged, never told; prior cycle's drafts unpublished |
| Plugins / ChatGPT gateway / stale-config | **#117** (assignment) | **absent** | **none** | **no row** | merged; not in CHANGELOG, not released, not told |

`flagship_hint` was not set, but the assignment bead *is* the #117 URL — the operator's
pre-pick for the flagship is #117.

**Surface snapshot (2026-10-05).**
- description: "Multi-agent orchestration CLI for Claude Code — declarative TOML
  workflows, autonomous agents, context-compression recovery, inter-agent mail." (intact)
- homepage: the cycle-3 Medium article
  (`…in-my-factory-of-ai-agents-last-time-i-cracked-the-door-heres-every-room…`)
- latestRelease: **v0.3.0** · tags: v0.1.0, v0.2.0, v0.3.0 only
- topics (18): agentfactory, claude-code, claude, ai-agents, multi-agent-systems,
  autonomous-agents, agentic-ai, agentic, agentic-coding, agentic-workflow, claude-skills,
  agent-framework, anthropic, cli, golang, llm-orchestration, workflow-automation,
  enterprise — core discovery set (claude-code, ai-agents, multi-agent-systems, agentic-ai)
  all present, healthy.
- pinned repos (stempeck): `agentfactory`, `stempeck` (profile) — intact.

**Claim Verification Map coverage.** The runbook's map (commands/flags → grep
`internal/cmd/`; formula count → `internal/cmd/install_formulas/`; skills →
`.claude/skills/`; test `make test`; build `go build ./...`) was sufficient for every
claim below. No extension required this cycle. Main health: **GREEN** — `go build ./...`
clean, `make test` all packages `ok` (2026-10-05).

---

## NEW — untold features (the backlog; not all are this cycle's assignment)

### Wave B — PR #117 (the assignment) — every command VERIFIED against source

**Plugins — add third-party agents from a git repo, safely.**
- `af plugin list | install <name>… | verify | acquire | check | remove | guard-event`
  — all seven VERIFIED: `internal/cmd/plugin.go:70/84/91`, `plugin_acquire.go:19`,
  `plugin_check.go:24`, `plugin_remove.go:15`, `plugin_guard.go:16`.
- Install is the consent step; provenance (source, commit, content hashes) recorded;
  `af plugin verify` detects drift; installed agents survive redeploys; a name shadowing a
  shipped/hand-written/`manager`/`supervisor` agent is refused; an agent whose role
  template is not built into the binary is refused at launch. (`USING_PLUGINS.md`.)

**Give agents tools (Claude Code plugins, env, services).**
- Integrations install from a pinned upstream commit, stored as a read-only snapshot;
  literal credentials / reserved env keys / toolchain names refused; formulas declare
  required/optional integrations and refer to a plugin's skills as `plugin:skill`; a
  formula whose required tool is missing/drifted/unhealthy/down is refused **before** any
  bead or worktree exists; a running instance keeps its admitted snapshot; `af plugin
  check` runs health checks; `af up`/watchdog supervise integration services without ever
  killing a session.

**Unattended prompt-guard.** In a session bound to an integration, permission prompts and
MCP input requests are auto-answered "no" and the manager is mailed what was refused
(`af plugin guard-event permission|elicitation`, `plugin_guard.go:16` — these are the
`PermissionRequest`/`Elicitation` hooks the launcher now writes into agent `settings.json`).

**Run the gateway on your ChatGPT subscription.**
- `quickstart.sh --litellm --litellm-auth=codex-subscription` (or `af install --agents
  --litellm --litellm-auth=codex-subscription`) installs the Codex CLI with consent, runs
  the device login before any agent is taken down, then routes every lane through the
  subscription. A compatibility callback fixes the "System messages are not allowed"
  rejection on that route.
- `af gateway auth import [--from <path>]` / `af gateway auth status [--json]` — VERIFIED
  `internal/cmd/gateway_auth.go:392/409` (flags `:419/:420`). Credential stored readable
  by owner only; neither command prints a token.

**A gateway check you can trust.**
- `af config models check --live` sends a real session-shaped request (streamed,
  block-array system prompt); timeout reported separately; quotes the gateway's own error
  instead of a bare 500. `--first` gives one quick verdict line. VERIFIED
  `internal/cmd/config_models.go:143/144`.
- Bootstrap restarts the gateway when config/auth-mode/credential/litellm-version/port
  changed; a port held by a foreign process is refused; `af up`/watchdog revive a missing
  gateway; expired/refresh-less credential reported as a credential failure; upstream
  gateway credentials can no longer reach an agent session.

**Every relaunch runs on exactly the config you set** (the "stale config" headline).
- Handoffs, watchdog/recovery respawns, `af sling` and the dispatcher now build the launch
  the same way `af up` does; removing a build host or setting a git identity takes effect
  on the next recycle; build delegation reaches `af sling`/dispatcher agents; no auth token
  in the tmux environment; a hand-opened window no longer inherits the agent's
  endpoint/model; a malformed `build-host.json` warns instead of aborting every agent.

**Effort levels mean what they say.**
- Turning tokenomics off no longer wipes the declared effort level; a reduced-effort launch
  is recorded on the session, survives formula completion, cannot leak into a later session
  or be forged by a profile; the grader is told of a reduction only when the session really
  ran reduced; `af fidelity status` gains `interventions` and `effort` columns (VERIFIED
  `internal/cmd/fidelity.go:292`); the statusline shows effort next to the model.

**`af up` leaves running agents alone.** On a live agent it prints `already running` and
changes nothing; with `--model` it says the model was not applied; a zombie (tmux alive,
claude dead) is still relaunched. VERIFIED `internal/cmd/up.go:277-279`.

**The dispatcher stops retrying refusals every tick.** A refused dispatch is recorded with
its reason, backs off, and shows as `refused (<class>, Nx)` in `af dispatch status` without
counting toward the attempts ceiling. VERIFIED `internal/cmd/dispatch.go:2604` (exact
format), `:418-429` (increments refusals, not Attempts), `:431-434` (backoff).

**Formulas no longer run their own prose as shell.** A test reads the shell embedded in
every shipped formula and fails on constructs that would execute/break when the
orchestrator runs them. VERIFIED `internal/cmd/formula_step_shell_test.go:387`
(`TestFormulaStepShell_RejectsDefectClass` → "every shipped formula is clean", rule cites
#710). rapid-soldesign-plan, design-plan-impl, design-v7, fable-increment, fable-review
measured-failure fixes.

**New shipped formulas & skill.** `rapid-soldesign`, `rapid-soldesign-plan`, `lineage`
(`internal/cmd/install_formulas/*.toml`, VERIFIED) and the `architecture-diagram` skill
(`.claude/skills/architecture-diagram/`, VERIFIED).

### Wave A — PR #111 token economics (v0.4.0; still untold, see STALE on the release gap)
See/bound/prove run cost: `af telemetry band|compare|rebuild|usage`
(`internal/cmd/telemetry.go:20/178-184`), `af tokenomics [on|off|status]`
(`tokenomics.go:23`), env budgets `AF_BACKEND_POOL_TOKENS` / `AF_BACKEND_CHILD_FLOOR_TOKENS`
/ `AF_DISABLE_PARALLEL_SUBAGENTS` (`internal/config/models.go:61/71/81`), adaptive effort,
session-start context budgets, recurring dispatch `crons` (`internal/config/dispatch.go:28`),
intervention-aware grader, GitHub-truth mergepatrol, +2 skills (improve-solution,
perfeval-agent). CHANGELOG `## v0.4.0 — 2026-09-17` covers these; README 334–342 already
documents the commands.

---

## STALE — claims failing verification (each with its fix)

| # | Claim / surface | Evidence | Status | Fix (Tier A, this cycle unless noted) |
|---|---|---|---|---|
| S1 | `README.md:236` "**Twenty-four** formulas ship with the factory" | real count **26** (`install_formulas/*.toml`; `store/formulas` matches); table omits `lineage`, `rapid-soldesign` | STALE | change 24→26; add `lineage` and `rapid-soldesign` rows to the Included Formulas table |
| S2 | `README.md:249` "**Twelve** skills are embedded…" | real count **13** (`.claude/skills/`); table omits `architecture-diagram` | STALE | change 12→13; add `architecture-diagram` row to the Included Skills table |
| S3 | README documents the `af plugin` family | grep `plugin` in README = 0 hits | ABSENT | README must introduce the #117 plugin surface (`af plugin list/install/verify/check`), the headline capability of the assignment |
| S4 | README documents `af gateway auth` / ChatGPT-subscription billing | README "gateway" only at `:244` (gpt-* formulas); no `af gateway auth` | ABSENT | add `af gateway auth import/status` + `--litellm-auth=codex-subscription` to README/quickstart surface |
| S5 | CHANGELOG has a section for #117 | CHANGELOG top is `## v0.4.0 — 2026-09-17`, then v0.3.0 — nothing for plugins/gateway/stale-config | MISSING | add a CHANGELOG section for #117 (version per operator's deliver-tier-a decision) |
| S6 | CHANGELOG `## v0.4.0 — 2026-09-17` vs releases | tags/releases stop at v0.3.0; no v0.4.0 tag | RELEASE GAP | **operator decision** at deliver-tier-a: cut v0.4.0 (token-econ) and a v0.5.0 (#117), or fold both into one release. CHANGELOG is ahead of the release surface — reconcile it |

Nothing in README's token-economics section (334–342), the topics set, the description,
the homepage, or the pinned items failed verification — those are intact.

---

## STORY-WORTHY — candidates for Phase 2 (ranked)

Ranked by the runbook's criteria: (a) user pain killed, (b) 60-second demonstrability,
(c) fit to target phrases, (d) audience reach. All three top candidates are facets of the
assignment PR #117 and form one natural arc.

1. **Plugins — extend your factory with third-party agents and tools, safely.**
   *Pain:* before #117 an agent could only enter by being hand-copied into the store, was
   then trusted wholesale, and was deleted on the next redeploy; tools had no way in at all.
   *Demo:* `af plugin list` → `af plugin install <name>` → `af plugin verify` (consent +
   provenance + drift) in under a minute. *Fit:* extensibility / ecosystem / Claude Code
   plugins / multi-agent orchestration. *Reach:* high — "a trust boundary for third-party
   agents" is a fresh, concrete capability story. **Lead flagship candidate.**

2. **Run the gateway on your ChatGPT subscription.**
   *Pain:* the gateway could bill only an OpenAI API key. *Demo:* one flag —
   `--litellm-auth=codex-subscription` — then every lane runs on the subscription the
   operator already pays for. *Fit:* cost / multi-provider. *Reach:* very high — a direct
   "use what you already pay for" hook. Strong flagship or the second beat of the arc.

3. **Relaunches never run on stale config.**
   *Pain:* respawns ran on whatever an earlier launch left behind — a reduced effort level
   with no record, a stale auth token readable from any tmux window, a build host you
   removed. *Demo:* less visual (a reliability guarantee) — best told as the trust close of
   the arc, not its own set piece. *Fit:* reliability / autonomous agents. *Reach:* medium.

**Supporting Tier-A refresh items regardless of flagship:** S1–S5 (README counts + tables,
`af plugin`/`af gateway` coverage, CHANGELOG section) and the S6 release reconciliation.

**Backlog (not proposed as flagship this cycle):**
- **Token economics / v0.4.0** (#111) — still untold and still unreleased; its own drafts
  (`cycle-af-d7166cc1-medium.md` / `-linkedin.md`) sit ready in this directory. Worth a
  dedicated cycle, but the operator has moved to #117. Flag the v0.4.0 release gap (S6).
- Durable memory vault, self-recovering agents head-on, autonomous dispatch pipeline,
  multi-provider `gpt-*` formulas, JSON contracts — carried from the cycle-3 ledger backlog.
