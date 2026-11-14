# Cycle af-4073aec6 — Audit (release-only cycle)

Scope set by the operator (bead af-4073aec6): "skip straight to the part where it publishes a
release, no actually medium article or marketing this time." This audit exists to define the
release's contents and the stale claims the release must fix. It does not feed a marketing story.

## Snapshot (live, 2026-11-14)

- Description: "Multi-agent orchestration CLI for Claude Code — declarative TOML workflows,
  autonomous agents, context-compression recovery, inter-agent mail." (unchanged from last cycle)
- Topics: 18 — agentic, agentic-ai, agentic-coding, agentic-workflow, claude, claude-code,
  claude-skills, enterprise, agentfactory, ai-agents, agent-framework, anthropic,
  autonomous-agents, cli, golang, llm-orchestration, multi-agent-systems, workflow-automation.
  The four runbook primary discovery topics (claude-code, ai-agents, multi-agent-systems,
  agentic-ai) are all present.
- Latest release: **v0.4.0**, published 2026-11-01T21:04:31Z (tag → `eab2b02c`).
- Homepage: https://medium.com/@glennstempeck/three-things-my-agent-factory-couldnt-do-last-month-4c98d4a7c1b4
  (passes the runbook `### homepage-allowlist` prefix `https://medium.com`).
- Pinned repositories (`stempeck`): agentfactory, stempeck.

## Development since the last cycle

Boundary: the ledger's last row is the v0.4.0 release (2026-11-01). `git log v0.4.0..origin/main`:

| Commit | PR | What it is | Ships to users? |
|---|---|---|---|
| `3c50873f` | #133 | Marketing cycle af-a6b105d1 close-out state (`.marketing/` ledger, report, verify) | No — cycle records only |
| `bec32c58` | #135 (closes #134) | Agents run on the context window they really get; af's own alarms reach you; advisory findings become healing pull requests; new factories start from current configs; sturdier formulas; pin updates; new docs and ADRs | **Yes** |

## NEW (untold features — none of these has a ledger row)

All from #135. Checked: `grep -niE 'window|startup|advisory-heal|healing|config-seed|#135|#134' announced-ledger.md`
matches one line only, ledger:11, where "window" is the 2026-08-03 Medium title ("…here's the
window into it"). `pin` occurs on 2 lines, both earlier plugin/security wording ("pinned
commits", "its pin"). No row claims any item below.

1. **Context window agreement** — the real window each session gets is recorded per identity;
   `WINDOW_MISMATCH` mail + `WINDOW` alarm; `unobserved` state; launches on a short recorded
   window refused by `af up` / `af sling --agent` / `af sling --formula` unless `--accept-window`;
   `af config models check` measures the window; `af config models set` warns; user-scope `env`
   overrides flagged; `af agents list --json` gains `window_declared` / `window_applied`;
   Claude Code pin 2.1.322.
2. **Startup-turn measurement** — `af telemetry startup [--agent manager|supervisor] [--json]`,
   90,000-token budget, `STARTUP` alarm cleared by `af recovery reset`; startup no longer reads the
   whole memory vault.
3. **Pinned rulings** — `af memory pin|unpin`, 3,072 B pinned block; `af memory status` pin
   listing; `af memory list` ordering, 8,192 B cap, `--type` / `--formula` / `--all-rows`.
4. **af's own mail arrives** — `WORKFLOW_COMPLETE`, `WORKFLOW_STALL`, `RECOVERY HALTED`, `WINDOW`,
   `STARTUP` filed in-process from sender `af`; `manager` receives operator escalations.
5. **Dispatcher completion and stalls** — reliable `WORKFLOW_COMPLETE`; one-account PR-consuming
   final phase; `WORKFLOW_STALL` mail + `stall` field / STALL column / web console; `hold` /
   `drop` / `skip` cycle lines.
6. **Advisory findings become healing pull requests** — `.github/advisory-audit.sh` records and
   exit codes 0/1/2/3, `excused-fixable`; red scheduled `advisory-decay` files locked
   `agentic` + `advisory-heal` issues; `advisory-heal` dispatcher workflow; new
   USING_ADVISORYLOOPS.md.
7. **New factories start from current configs** — `af install --init` seeds six configs from the
   source clone (ADR-028), `.agentfactory/.config-seed.log`; `quickstart.sh` follows `models.json`
   and works with BSD sed; an imported ChatGPT-subscription handle decides the gateway auth mode.
8. **Sturdier formulas** — design-v7, rapid-soldesign-plan, fable-review, fable-increment,
   improve-agent.
9. **Pins** — Claude Code 2.1.322, Go 1.27.4, Node.js 22.23.5, `oauthlib` 4.0.1, `filelock` 4.2.9.

## Claim-verification pass (README + CHANGELOG)

Method per the runbook Claim Verification Map, run against `bec32c58`.

| Claim | Where | Evidence | Verdict |
|---|---|---|---|
| "Twenty-six formulas ship with the factory" | README.md:266 | `ls internal/cmd/install_formulas/*.toml \| wc -l` = 26 | VERIFIED |
| "Thirteen skills are embedded" | README.md:279 | `ls -d .claude/skills/*/ \| wc -l` = 13 | VERIFIED |
| Every command in the README Command Reference and Observability block (README.md:365–372, 414–470): `af up/down/attach/install`, `af mail send/inbox/read/reply`, `af sling`, `af formula agent-gen`, `af prime`, `af done`, `af telemetry report/usage/band/compare/rebuild`, `af tokenomics`, `af improvement`, `af config models show`, `af config fingerprint`, `af recovery reset`, `af memory add/list/show/status`, `af statusline`, `af fidelity`, `af plugin list/install/verify`, `af gateway auth import/status` | README.md | Binary built from `bec32c58` (`go build ./cmd/af`): `af <cmd> --help` exits 0 for all 37 command paths checked (including the new `telemetry startup`, `memory pin`, `memory unpin`, `config models check`) | VERIFIED |
| `af down [--all]` | README.md:415 | `af down --help` lists `--all` | VERIFIED |
| CHANGELOG records what shipped | CHANGELOG.md:6 | Top section is `## v0.4.0 — 2026-11-01`; `grep -n '#135\|#134' CHANGELOG.md` → no match | **STALE** |

## STALE (each with its fix)

- **S1 — CHANGELOG has no entry for #135.** CHANGELOG.md's newest section is
  `## v0.4.0 — 2026-11-01`; #135 (the only user-facing merge since) is unrecorded.
  **Fix (this cycle, required for the release):** add a `## v0.5.0 — <release date>` section
  above v0.4.0 covering #135, every item verified against source at GATE 3, then tag v0.5.0
  on the merged commit.

Not stale, out of scope this cycle: the README Command Reference is a curated subset; it does
not list #135's new verbs (`af telemetry startup`, `af memory pin|unpin`, `--accept-window`,
`af config models check`). Every line it does carry is true. Adding them is README marketing
work, which the operator excluded from this cycle; it goes to the next cycle's backlog.

## STORY-WORTHY (for Phase 2)

Release-only cycle: the operator pre-decided the deliverable. The single candidate is
**the v0.5.0 GitHub release** (CHANGELOG section + tag + release notes + prebuilt binaries)
covering #135. No marketing story is proposed, and no Tier B piece is drafted.

## Carried to deliver-tier-a

Main CI for `bec32c58` was red at preflight (integration timeout, bead af-7346a737; rerun
requested). The tag waits for a green main.
