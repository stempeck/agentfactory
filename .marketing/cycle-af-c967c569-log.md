<!-- Cycle log — af-c967c569 (cycle-3). Gate evidence + running narrative. -->
# Cycle-3 Log — stempeck/agentfactory (af-c967c569)

## GATE-1 — Audit checklist (pre-selection)

Evidence recorded 2026-08-23. Each item quotes from `cycle-af-c967c569-audit.md` and the
verification commands behind it.

**1. Is every claim in the STALE list backed by quoted evidence (grep/file:line), not memory? — YES**

- STALE #1 (roadmap contradiction): README `Roadmap` line 410 lists "formula authoring in the
  browser" as future, while README line 270 already ships it and the web console exposes
  `PUT /api/formulas/<name>` — evidence: `web/internal/server/server_test.go` +
  `settings_write_test.go` reference `/api/formulas/foo`, formula-write audit line
  `web/internal/server/server.go:1229`, backend `web/internal/formschema/reader.go`. (grep verified)
- STALE #2 (CHANGELOG missing #104): `head CHANGELOG.md` → top section is `## v0.2.0 — 2026-08-03`;
  no #104 / v0.3.0 section exists. (command output)
- STALE #3 (recovery-model.md incomplete): full read of `docs/recovery-model.md` — its "recovery
  chain" stops at §3 "Compaction boundaries are intercepted" (PreCompact/compact-handoff); no
  context-occupancy watchdog recycling, no recovery breaker, no `af recovery reset`. (file read)
- STALE #4 (README command-reference omissions): `grep "af memory|af statusline|af recovery|af
  fidelity|af config fingerprint" README.md` → all return "NO" (absent). (grep verified)
- STALE #5 (soft, #75): `gh issue view 75` → state OPEN; roadmap entry not strictly false, flagged
  verify-first. (gh output)

**2. Is every NEW feature absent from announced-ledger.md (checked, not assumed)? — YES**

Grep of `.marketing/announced-ledger.md` for NEW-feature keywords returns 0 hits each:
`context-exhaustion` 0, `recovery breaker` 0, `af recovery` 0, `af memory` 0, `memory vault` 0,
`af statusline` 0, `statusline` 0, `af config fingerprint` 0, `fingerprint` 0, `if-content-hash` 0,
`af fidelity` 0, `not measured` 0, `recovery badge` 0. Control (already-told, expected present):
`telemetry` 4, `web console` 6, `multi-provider` 2, `teardown` 2, `self-improving` 1. The ledger
discriminates correctly — the NEW list is genuinely untold.

**3. Does the snapshot record description, topic count, latest release, and homepage as of NOW? — YES**

From `gh repo view --json description,repositoryTopics,latestRelease,homepageUrl` (2026-08-23),
recorded in the audit's snapshot section: description intact ("Multi-agent orchestration CLI for
Claude Code — …"); topics = 18 (core set claude-code / ai-agents / multi-agent-systems / agentic-ai
present); latest release = v0.2.0 (no v0.3.0 cut); homepage = cycle-2 Medium article
(`i-run-a-factory-of-ai-agents-heres-the-window-into-it-ad0321bfce9f`). Pinned items: agentfactory,
stempeck.

**4. Were any gh calls skipped due to auth/scope errors? — NO**

`gh api user -q .login` → `stempeck` (matches runbook Operator). All audit gh calls succeeded:
`gh repo view`, `gh api graphql` (pinnedItems), `gh pr view 104`, `gh pr list`, `gh issue view
75/73/103`. No 404s, no scope hints, nothing skipped.

GATE-1 VERDICT: PASS

## PHASE-3 — Tier A refresh

Applied on branch `af/marketing-cycle-9972f0`.

**STALE fixes (all four hard items from the audit):**
1. README Roadmap — removed "formula authoring in the browser" (shipped); now reads "deeper agent detail and richer per-screen views". (grep confirms no leftover)
2. CHANGELOG — added `## v0.3.0 — 2026-08-23` section for the #104 wave (self-recovery, durable memory, statuslines, config integrity, fair gates, honest observability, formulas/docs/CI). Was stopping at v0.2.0.
3. docs/recovery-model.md — added "## Automatic context-exhaustion recovery" (watchdog watches occupancy + heartbeat; recycles exhausted agent with checkpoint; durable breaker + `af recovery reset`; `af statusline status` config-drift warning). Was stopping at PreCompact.
4. README Command Reference — added `af config fingerprint`, and a new "Recovery, memory & session health" block (`af recovery reset`, `af memory`, `af statusline`, `af fidelity`).
Plus honest enhancements tying the flagship screens to features: comparison-table recovery cell now names automatic context-exhaustion recovery; Web Console section notes the Floor's context-fill bar + recovery badges.
STALE #5 (soft, roadmap #75): left untouched — #75 is still OPEN; #104 made real progress but the item is not falsely claimed. Verify #75 status with operator before moving it.

**Every command written was verified** against `--help`/source this cycle: `af recovery reset`, `af memory add|list|show|status`, `af statusline on|off|status|render`, `af fidelity on|off|status [--agent]`, `af config fingerprint --json`.

**Issue-filing:** none warranted. No code bugs surfaced; the only gaps found were doc gaps, fixed in this PR. Filing busywork issues would violate the "never manufacture activity" rule.

**Release decision (NOTED for deliver-tier-a, not executed):** #104 IS a meaningful boundary — a large multi-feature merged wave. Proposed release **v0.3.0** (title e.g. "self-recovery & honesty"), cut only AFTER the Tier A PR merges and only on green main (main is green now). The CHANGELOG entry is dated 2026-08-23; adjust the date/version at step 17 if the operator's release call differs.
