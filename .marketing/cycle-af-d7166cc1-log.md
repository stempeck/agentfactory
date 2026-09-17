<!-- cycle-af-d7166cc1 running log — gate verdicts and cycle state. Author: marketing-cycle agent. -->
# Cycle af-d7166cc1 — running log

## GATE-1 — Audit checklist (pre-selection)

**1. Every STALE claim backed by quoted evidence (grep/gh/file:line, not memory)? — YES**
- STALE-1 (skills count): `README.md:249` "Ten skills are embedded and written to `.claude/skills/`
  during `af install`:" — refuted by `ls -d .claude/skills/*/ | wc -l` = **12** and
  `internal/cmd/install_skills/{improve-solution,perfeval-agent}/SKILL.md` (both embedded/ship).
- STALE-2 (roadmap #73): `README.md:420` "**Default dispatch workflow** included with the factory
  ([#73])" — `gh issue view 73 --json stateReason` = **COMPLETED** (closed 2026-09-16).
- STALE-3 (roadmap #75): `README.md:419` "**Gate quality improvements** — reduce fidelity-gate
  false positives on passive steps ([#75])" — `gh issue view 75 --json stateReason` = **COMPLETED**
  (closed 2026-09-16).
- Verified-OK control: `README.md:236` "Twenty-four formulas ship" = `ls internal/cmd/install_formulas/*.toml | wc -l` = **24** (matches; not stale).

**2. Every NEW feature absent from `announced-ledger.md` (checked, not assumed)? — YES**
- `grep -niE 'tokenomic|token econ|telemetry band|telemetry compare|telemetry rebuild| crons|admission|oversubscri|improve-solution|perfeval|input-digest|AF_BACKEND_POOL' .marketing/announced-ledger.md`
  returned **NONE FOUND**. Token economics (#111) and all its sub-features + the two new skills are
  absent from the ledger.

**3. Snapshot records description, topic count, latest release, homepage as they are RIGHT NOW? — YES**
- Description: "Multi-agent orchestration CLI for Claude Code — declarative TOML workflows,
  autonomous agents, context-compression recovery, inter-agent mail." (live `gh repo view`).
- Topics: **18** (live). Latest release: **v0.3.0**, publishedAt 2026-08-23T13:23:37Z (live).
- Homepage: cycle-3 Medium article `…/in-my-factory-of-ai-agents-last-time-i-cracked-the-door-heres-every-room-a04f14babef0` (live).

**4. Any gh calls skipped due to auth/scope errors? — NO (none skipped)**
- `gh api user -q .login` = `stempeck` (auth OK). All gh calls this cycle succeeded: `repo view`,
  `api graphql` pinnedItems, `pr list`, `pr view 111`, `issue view {73,75,110}`. No 404/scope errors.

GATE-1 VERDICT: PASS

## GATE-2 — Story approval (HOLD)

- Flagship proposed: **Token economics (#111)** — the only new development since v0.3.0.
- Operator notified: GitHub issue **#112** (https://github.com/stempeck/agentfactory/issues/112).
- Feedback form: `cycle-af-d7166cc1-story.md` → `## Operator Decision` (`- Decision: ______`, blank).
- State: **UNRESOLVED — HOLDING.** Not advancing the gate while the form is blank (the gate
  registers a waiter and advances unconditionally; it does NOT grep the form, so the HOLD is
  behavioral). Will re-check the Decision line and close #112 when the operator writes
  APPROVE / REORDER / END-CYCLE.
- **RESOLVED 2026-09-17 — APPROVE.** Operator stempeck (repo OWNER) commented `APPROVED` on
  issue #112 at 2026-09-17T00:02:23Z. Verified directly with `gh issue view 112 --json comments`
  (not trusted from the manager mail relay, which is a claim, not a fact — the relay said
  "APPROVE", the actual comment body was "APPROVED"; same keyword). Decision recorded on the
  `- Decision:` line of `cycle-af-d7166cc1-story.md`; issue #112 closed. Gate cleared via
  `af done --phase-complete --gate af-5efe3ce2`. Release call left unstated → story-form default
  applies (propose v0.4.0 at the deliver gate, HOLD there).

GATE-2 VERDICT: PASS (APPROVE — Token economics flagship + supporting Tier-A refresh)

## Phase 3 — Tier A refresh (README, docs, CHANGELOG)

Branch: `af/marketing-cycle-cd14d3`. Committed mode (`.marketing/` not git-ignored — verified
`git check-ignore`). Every command/flag/count below was proven against source BEFORE it entered
public text (runbook Claim Verification Map: grep `internal/cmd/`, count `internal/cmd/install_formulas/`, `.claude/skills/`).

**STALE claims fixed (all 3 from the audit):**
- STALE-1 `README.md` — "Ten skills are embedded" → "Twelve"; added table rows for
  `/improve-solution` and `/perfeval-agent`. Proof: `ls -d .claude/skills/*/ | wc -l` = 12,
  same 12 under `internal/cmd/install_skills/`, both new skills present in both trees.
- STALE-2 `README.md` Roadmap — removed "**Gate quality improvements** … (#75)". Proof:
  `gh issue view 75` = CLOSED.
- STALE-3 `README.md` Roadmap — removed "**Default dispatch workflow** … (#73)". Proof:
  `gh issue view 73` = CLOSED.

**GAP fills (additive doc, story-directed):**
- `README.md` Observability + Command Reference — documented `af telemetry band|compare|rebuild`
  and `af tokenomics on|off|status`; added a paragraph on bound-and-prove (admission control,
  adaptive effort, compare-voids-rather-than-lies) linking `USING_TOKENOMICS.md`. Proof: `Use:`
  strings in `internal/cmd/telemetry.go:20`, `internal/cmd/tokenomics.go:23`, `internal/cmd/turn.go`.
- `CHANGELOG.md` — new `## v0.4.0 — 2026-09-17` entry (See / Bound / Prove / Also). Env vars
  `AF_BACKEND_POOL_TOKENS`, `AF_BACKEND_CHILD_FLOOR_TOKENS`, `AF_DISABLE_PARALLEL_SUBAGENTS`
  verified in `internal/config/models.go`; `crons` verified in `internal/config/dispatch.go`;
  `internal/tokenomics/` package present.

**Issues filed for real gaps:** NONE. No new code gap was discovered this cycle — the audit's
"GAPS" were doc gaps, now filled here. The only standing formula-improvement (cleanup-manifest
gate not recognizing `cycle-*-screen-*.png`) is already recorded in `approach.md` Standing Assets.

**Release decision (NOTED, not executed):** v0.4.0 IS a meaningful boundary — new `internal/tokenomics/`
package, new CLI verbs, closes #110. Operator stempeck CONFIRMED YES to a v0.4.0 release this cycle
(issue #112 comment 2026-09-17T00:08:28Z, verbatim "Cut a v0.4.0 release this cycle? Yes."; verified
via `gh issue view 112 --json comments`, not trusted from the manager relay). Tag is still cut ONLY
after the PR merges (never on a red main; main greenness re-verified at step 5), at the deliver gate
(step 17) — this step notes the decision, it does not execute it.

## GATE-3 — Every public claim verified against source

Full branch diff reviewed: `git diff origin/main...HEAD` (README.md, CHANGELOG.md + 3 `.marketing`
records; 342 insertions). The mandatory 4-item pre-draft checklist was run over every added line;
each item resolves to NONE-unverified.

**1. Commands / flags / subcommands asserted but NOT proven this session — NONE.**
- `af telemetry band|compare|rebuild|usage` and `af telemetry on|off|status|report|usage` —
  `internal/cmd/telemetry.go:20` `Use: "telemetry [on|off|status|report|band|usage|compare|rebuild]"`;
  implementations `telemetry_band.go`, `telemetry_compare.go`, `telemetry_digest.go` (rebuild),
  `telemetry_usage.go`.
- `[--json]` on band/compare — `internal/cmd/telemetry.go:138`: "status, report, band and compare
  have both a human and a machine-readable form … where --json picks between them". `rebuild` is
  documented WITHOUT `--json` on purpose (not in that set).
- `af tokenomics on|off|status` — `internal/cmd/tokenomics.go:23` `Use: "tokenomics [on|off|status]"`;
  cases at `:204`/`:213`.
- `af dispatch status` — `internal/cmd/dispatch.go:30` `Use: "dispatch"` + `:76` `Use: "status"`.
- `af turn interventions` — `internal/cmd/turn.go:124` `Use: "interventions"`.
- Env keys `AF_BACKEND_POOL_TOKENS`, `AF_BACKEND_CHILD_FLOOR_TOKENS`, `AF_DISABLE_PARALLEL_SUBAGENTS`
  — `internal/config/models.go`. `crons` — `internal/config/dispatch.go`. Package `internal/tokenomics/` present.
- Skills `/improve-solution`, `/perfeval-agent` — `.claude/skills/{improve-solution,perfeval-agent}/`
  and identical dirs under `internal/cmd/install_skills/`.
- (`--gate`, `--phase-complete`, `--oneline` appearing in log prose are process/git flags in evidence
  text, not public product claims.)

**2. Counts asserted but NOT recounted from the filesystem — NONE.**
- "Twelve skills" (README) / "(10 → 12)" (CHANGELOG) — `ls -d .claude/skills/*/ | wc -l` = 12; the
  same 12 exist under `internal/cmd/install_skills/`.
- "Twenty-four formulas ship" (README context, unchanged line) — `ls internal/cmd/install_formulas/*.toml | wc -l` = 24.

**3. URLs not fetched or constructed from a verified pattern — NONE.**
- `USING_TOKENOMICS.md` (relative link, README) — file present (`ls -la` = 38307 bytes).
- `https://github.com/stempeck/agentfactory/issues/112` and `…#issuecomment-5706340238` (`.marketing`
  records) — real; fetched live via `gh issue view 112 --json comments`.
- The two stale issue URLs (#73, #75) were REMOVED from the diff, not added.

**4. Unshipped promises ("coming soon") outside a Roadmap section — NONE.**
- `grep -iE 'coming soon|will ship|planned|future|tbd|wip'` over added README/CHANGELOG lines = none.
  Every documented feature ships in #111 (merged: `8d1a0066` on origin/main). Two shipped items were
  REMOVED from Roadmap; the Roadmap retains only genuinely-future items (GoReleaser binaries, richer
  formula library, web-console growth).

GATE-3 VERDICT: PASS
