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
  `web/internal/server/server.go:1237`, backend `web/internal/formschema/reader.go`. (grep verified)
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

## GATE-3 — Public-claim verification (pre-draft)

Checked the full branch diff `git diff origin/main...HEAD`. Evidence 2026-08-23.

**1. Commands/flags/subcommands not proven this session — NONE.**
Every `af` command in the docs diff was verified against `--help`/source this session:
`af config fingerprint`, `af config models`, `af fidelity on|off|status [--agent]`,
`af memory add|list|check|status|export|show`, `af recovery reset <agent>`,
`af statusline on|off|status|render`, `af statusline status`, `af prime`. (The grep hit
"af up does" is prose in recovery-model.md, not a command claim; `af up` is a real command.)

**2. Counts not recounted — NONE.** The diff asserts no new counts. Existing counts re-verified
from the filesystem this cycle: 24 formulas (`ls internal/cmd/install_formulas/*.toml`),
10 skills (`ls -d .claude/skills/*/`).

**3. URLs not fetched/constructed from a verified pattern — NONE.** The only URL added anywhere
in the diff is `https://github.com/stempeck/agentfactory/issues/105`, which this agent created
this session. (#104/#105 shorthands are issue/PR references, not URLs; #104 is verified merged.)

**4. Unshipped promises outside a Roadmap section — NONE.** Grep for
"coming soon|will ship|planned|not yet|todo" in the docs diff returns nothing. The CHANGELOG
v0.3.0 entry, README command block, and recovery-model section all describe shipped #104
behavior; future items live only in the README Roadmap section.

**Correction applied during this gate:** the audit/story/log cited the formula-write audit line
as `server.go:1229` (copied from a stale test comment); the actual line is
`web/internal/server/server.go:1237` (`log.Printf("audit: formula write …")`). All four
occurrences corrected. Other code refs verified to exist: recoverybadge_test.go, sling_test.go,
formschema/reader.go, web/static/index.html.

GATE-3 VERDICT: PASS

## SELF-REVIEW (step 14, 2026-08-23)

Reviewed `git diff origin/main...HEAD` — 3 Tier A commits + the Phase 4 artifact commit, 16
files: README / CHANGELOG / docs/recovery-model.md plus every `cycle-af-c967c569-*` artifact.

Findings and fixes:

1. **New README command block — all five commands verified to exist on this binary.**
   `af recovery reset`, `af memory`, `af statusline`, `af fidelity`, `af config fingerprint` each
   return help. `af memory export` (listed in the CHANGELOG memory line) also exists. No fix
   needed — no invented command shipped.
2. **Stale roadmap claim NOT reintroduced.** The "formula authoring in the browser" future item
   (it ships, and it is a flagship screen this cycle) was correctly removed from the Roadmap and
   replaced with "deeper agent detail and richer per-screen views."
3. **Links resolve.** README added-line link `[Observability](#observability)` targets
   `## Observability` (README:325). recovery-model.md relative links `agent-lifecycle.md` and
   `formulas.md` both resolve to real files. No broken links.
4. **No cruft.** TODO/FIXME/XXX/console.log/debug grep over the Tier A doc diff returns nothing.
   The eight `screen-*.png` are the flagship article's real console screenshots (committed mode),
   not stray debug captures.
5. **Flagship hard numbers re-verified against source:** 24 formulas in `.agentfactory/store/formulas/`,
   26 agents from `af agents list --json` — both land exactly as written in the medium draft.
6. **recovery-model.md `af recovery reset` parenthetical matches the command's own contract**
   ("operator-only; clears the breaker, does not relaunch — `af up` does").

Privacy mode: **COMMITTED** (runbook Privacy-Decision). `git diff --stat origin/main...HEAD`
shows `.marketing/` cycle artifacts appearing alongside the doc changes — correct for committed
mode; no accidental exclusion, no artifact stranded uncommitted.

Watch items (not defects — reconcile downstream, no fix now):

- **CHANGELOG heading `## v0.3.0 — 2026-08-23` pre-commits a version + date ahead of the
  operator's release decision (Step 17).** Standard for a release PR, but if the operator
  declines to cut v0.3.0 on merge, flip the heading to `## Unreleased` at PR time.
- **GATE 4 (operator draft approval) substance is still PENDING.** Both Tier B drafts are
  committed as WIP cycle records with blank Decision forms; operator notified on issue #106.
  Committing the draft FILES is Tier A (repo records) and publishes NOTHING off-GitHub — the
  publish HOLD at Step 18 stands until the operator writes READY/EDITED/SKIP, and any EDITED
  draft gets mechanics-only voice-law reconciliation before anything is published.

SELF-REVIEW VERDICT: PASS
