# Cycle-2 Audit — stempeck/agentfactory

*Captured 2026-08-03 by marketing-cycle (bead af-50cdad6b). Boundary: v0.1.0 (commit
70c015a6, #87, 2026-07-11) — the cycle-1 release. Authenticated as `stempeck`.*

## Public-surface snapshot (RIGHT NOW)

| Item | Value |
|---|---|
| description | "Multi-agent orchestration CLI for Claude Code — declarative TOML workflows, autonomous agents, context-compression recovery, inter-agent mail." |
| homepage | https://medium.com/@glennstempeck/95-reliable-agents-give-you-86-reliable-workflows-b264170eb66c |
| latest release | v0.1.0 — first formal release (2026-07-12) |
| topics (18) | agentic, agentic-ai, agentic-coding, agentic-workflow, claude, claude-code, claude-skills, enterprise, agentfactory, ai-agents, agent-framework, anthropic, autonomous-agents, cli, golang, llm-orchestration, multi-agent-systems, workflow-automation |
| pinned (stempeck) | agentfactory, stempeck |
| formulas shipped | **24** (`ls internal/cmd/install_formulas/*.toml \| wc -l` = 24) |
| skills shipped | **10** (`install_skills/` embedded via `//go:embed install_skills/*`, install.go:32) |

Positioning topics from the runbook (claude-code, ai-agents, multi-agent-systems,
agentic-ai) are all present. No gh calls were skipped for auth/scope reasons — the token
authenticates as `stempeck` and every audit call returned data.

## Development since last cycle (git log v0.1.0..origin/main)

Only two commits landed on main since the v0.1.0 boundary:

- **#92** `0df7e2e5` (2026-08-02) — "Run telemetry, operator-only factory teardown,
  reliable improvement self-edits, multi-provider agents" (240 files, +44,133).
- **#89/#90** `e3cd797d` (2026-07-12) — "marketing-cycle: persist operator-curated
  cycle-1 state" (marketing infrastructure, not a user-facing product feature).

The bulk of the untold list is therefore *carried forward* from the cycle-1 backlog:
features that merged **before** v0.1.0 but were never specifically announced (the launch
story told them only at a high level).

---

## NEW — untold features (in git history, absent from announced-ledger.md)

Checked each against `announced-ledger.md`; none has a ledger row.

### Landed since v0.1.0 (#92)
1. **Run telemetry** — `af telemetry on|off|status|report|usage`. Factory-wide recording
   of per-step latency and token usage; a local timing table (`af telemetry report`) and a
   backend usage query (`af telemetry usage`). Verified: `af telemetry --help`; subsystem
   `internal/telemetry/`, `web/internal/telemetryview/`. **Story-worthy.**
2. **Multi-provider agents** — run agents on non-Claude models. `af config models`
   (attest/check/set/show), `models.json`, `af install --agents --litellm` (OpenAI gateway),
   per-launch `af sling --model`, a fitness-attestation gate, and `gpt-*` formulas
   (gpt-fable-review, gpt-rootcause-all). Verified: `af config models --help`,
   `af sling --help` (`--model`, `--skip-fitness`). **Story-worthy.**
3. **Operator-only factory teardown** — factory-wide `af down` is now operator-gated (an
   agent can no longer tear down the whole floor). Verified: CLAUDE.md CLI section; #92 title.
4. **Reliable improvement self-edits** — the continuous-improvement hook
   (`af improvement on|off|complete`, AND-gated) hardened so agents can self-edit their
   own formula reliably. Verified: `af improvement --help`.

### Carried forward from cycle-1 backlog (still untold)
5. **Web console** specifics — browser formula authoring, the Floor view, operator mail,
   agent-detail, and (new in #92) the telemetry view/banner. Loopback-only control plane.
   (#72, #81, #83, #92). Told only as "web console" in the v0.1.0 ledger row.
6. **Fable agent family** — fable-implement, fable-increment, fable-review, fable-secure. (#83)
7. **Autonomous dispatch pipeline** — label matching, issue→PR handoff, cycle locking,
   phase advancement, completion-mail reliability. (#38, #79)
8. **Per-agent model selection** + **in-session gate continuation**
   (`af done --phase-complete --gate`). (#81)
9. **marketing-cycle formula** — an agent that runs the full marketing cycle for the repo
   its factory serves (this very run). The dogfooding story. **Story-worthy.**
10. **`af handoff`** — recycle a session while preserving context for the next.
11. **Machine-readable contracts** — `af agents list --json`, `af dispatch status --json`,
    `af formula show --json`, `af config dispatch/startup set`.
12. **`af watchdog`** — monitor agent sessions and auto-recover failures.

---

## STALE — public claims failing verification (each with a fix)

All STALE claims are in **README.md**; `docs/formulas.md`, `docs/agent-lifecycle.md`, and
`docs/recovery-model.md` were scanned and contain **no** stale count/command claims (every
`af` command they cite resolves in `af --help`).

### STALE-1 — `af version` is not a command (README.md:93)
- **Claim:** "Verify: `af version`"
- **Evidence:** `af version` → `Error: unknown command "version" for "af"`. The working
  form is `af --version` → `af version v0.1.0-3-gb164332`.
- **Fix:** change `af version` → `af --version`.

### STALE-2 — formula count understated: says 19, ships 24 (README.md:236, table 238–244)
- **Claim:** "Nineteen formulas ship with the factory"; the table lists 19.
- **Evidence:** `ls internal/cmd/install_formulas/*.toml | wc -l` = **24**. Five ship but
  are absent from the table: `fable-secure`, `gpt-fable-review`, `gpt-rootcause-all`,
  `multi-agent`, `marketing-cycle`.
- **Fix:** update the count to twenty-four and add the five missing formulas to the table
  (gpt-* → the multi-provider variants; fable-secure → security review; multi-agent →
  orchestration; marketing-cycle → the self-marketing agent).

### STALE-3 — skills count understated: lists 3, ships 10 (README.md:246–253)
- **Claim:** "Included Skills" table lists 3 (`/formula-create`, `/github-issue`,
  `/documentation-update`).
- **Evidence:** `install_skills/` embeds **10** skill dirs via `//go:embed install_skills/*`
  (install.go:32) and `writeSkills` (install.go:387) writes all of them to every factory's
  `.claude/skills/`. Seven ship but are unlisted: `agentic-skill-eval`, `architecture-docs`,
  `architecture-elevation`, `improve-agent`, `rapid-implement`, `rootcause-review`,
  `six-sigma-challenge`. (The "pro" manifest referenced in install_test.go is a private
  overlay absent from this public tree — the OSS build ships exactly these 10.)
- **Fix:** expand the Included Skills table to the 10 shipped skills.

---

## STORY-WORTHY — candidates for Phase 2 (ranked in cycle-af-50cdad6b-story.md)

| Candidate | User pain killed | 60-sec demo | Fit to positioning | Reach |
|---|---|---|---|---|
| **Run telemetry** | "I can't see what my autonomous agents cost or where they stall" | `af telemetry report` timing table + `af telemetry usage` tokens | observability for autonomous agents / agentic workflows | high — every agent operator wants cost + latency visibility |
| **Multi-provider agents** | "agentfactory is Claude-only" | `af config models` + a gpt-* formula run | broadens reach beyond Claude Code (nuance: positioning is Claude-Code-specific) | high but off-message risk |
| **marketing-cycle dogfooding** | "does this thing actually work autonomously?" | this cycle IS the demo | autonomous agents / the "I built this" proof | high human-interest; unique narrative |
| **Reliable self-improving agents** | "my agent repeats the same mistake every run" | improvement hook self-edits a formula | autonomous agents that improve themselves | medium-high |
| **Web console** | "orchestration is invisible in a terminal" | Floor view + telemetry banner screenshots | multi-agent orchestration made visible | medium (visual, needs screenshots) |

Nothing here is invented — every candidate maps to shipped code cited above. This is a
story-worthy cycle; the flagship pick and full ranking are in the story file.
