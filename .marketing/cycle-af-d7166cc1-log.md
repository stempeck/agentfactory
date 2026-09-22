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

## Phase 4 — Tier B drafts (operator's voice)

Voice recalibrated FIRST against the operator's published finals (cycle-3 Medium published version +
cycle-3 LinkedIn approved-as-written + cycle-2 edited-short LinkedIn) before writing a word.

- `cycle-af-d7166cc1-medium.md` — long-form, flagship token economics. Structure: failure-mode hook →
  See / Bound / Prove → honest limits ("won't do, on purpose") → repo + USING_TOKENOMICS.md → motto
  close. No fragile numeric claim in the title (so no number-landing obligation). Alt titles staged.
- `cycle-af-d7166cc1-linkedin.md` — short-form, ~140 words, plain text (CAPS not asterisks — LinkedIn
  renders no markdown), multi-question hook, `Read more: … | Get it: …` footer. Medium URL is a
  publish-time placeholder (Phase 5 sequences Medium first).
- `cycle-af-d7166cc1-diagram.png` — optional see→bound→prove card, rendered via bundled Playwright
  chromium (branded chrome absent in this Linux container) and pixel-checked (no label collisions,
  nothing clipped). Filename matches the cleanup gate's `cycle-*-diagram.png` pattern.

Every command/flag/limit in both drafts is source-verified (same evidence as GATE-3). Nothing
published — Tier B is operator-published.

## GATE-4 — Operator draft approval (HOLD)

- Both drafts end with a blank `## Operator Decision` form (READY / EDITED / SKIP), one per draft.
- Operator notified: GitHub issue **#113** (https://github.com/stempeck/agentfactory/issues/113).
- State: **UNRESOLVED — HOLDING.** Per the cycle-3 gotcha, `af done --phase-complete --gate af-c97a09b7`
  advances UNCONDITIONALLY (registers a waiter; does NOT grep the forms), so the HOLD is behavioral.
  NOT advancing until BOTH draft Decision lines are resolved. On EDITED: their text is canonical —
  fix mechanics only, enumerate, preserve replaced text as reference (committed mode still: keep the
  audit trail), and record their edit as the new Voice calibration source in approach.md. Close #113
  when both are resolved.
- **CORRECTION 2026-09-17 — delivery method changed to a PR.** The operator rejected the worktree-form
  mechanism (verbatim via manager relay, verified: "I'm not going to go into the agent branch, it
  needs to communicate what I need via PR or Github issues. It should probably have put up a PR for me
  to review the content and whatever edits happen up until approval can happen in that branch/push/PR
  process."). FIXED: pushed `af/marketing-cycle-cd14d3`, opened **PR #114**
  (https://github.com/stempeck/agentfactory/pull/114) as the single review surface — Tier A changes +
  both Tier B drafts presented inline in the PR body. Closed #113, redirected to the PR. Review + edits
  now happen on the PR (READY/EDITED/SKIP as PR replies, or pushes to the branch). Runbook updated
  (approach.md §0 + failure-modes) so future cycles open the review PR early and never point at a
  worktree path. Formula-improvement candidate recorded in memory (GATE 2/4 + notify steps hardcode
  worktree Decision forms). STILL HOLDING at GATE 4 for the operator's PR review; NOT advancing
  af-c97a09b7 until both drafts are resolved on the PR.

- **RESUME 2026-09-22 (session recycled at context pressure; af prime → GATE 4).** Operator stempeck
  DID review PR #114: 10 inline review comments (2 on LinkedIn 2026-09-17, 8 on Medium 2026-09-21/22),
  all "COMMENTED" (empty top-level bodies — decisions live in the line comments). This is an **EDITED**
  resolution with substantial directional feedback, NOT untouched and NOT a bare READY/SKIP. Verbatim
  themes: kill self-congratulatory emotion ("proudest of," "favorite thing," "honesty valve" =
  "randomized happiness" / "AI slopping emotions alongside environment variables"); stop celebrating
  non-features ("what it won't do," "honest about what it can't measure," null-vs-zero — "not a win");
  "in my factory of AI agents" is over-used; lead with the switches operators type
  (`af telemetry on` / `af tokenomics on` / `af improvement on`) and the collect→improve→prove loop;
  and "SHOW IT" — real screenshots of outcomes, not "AI on repeat" generalities.
- **Action taken (rev.2, this session):** rewrote both drafts against all 10 comments (exact wording
  used verbatim where given: new Medium title, new LinkedIn ending, subtitle cut). Embedded 2 REAL
  live captures in Medium (`af telemetry report` per-step CTX_PCT+DELTA; `af tokenomics status`
  mechanisms) — captured on this machine 2026-09-22, offered as PNGs on approval. Enumerated every
  change at the foot of each draft (voice law) and recorded the new calibration in approach.md Voice §.
  Honesty guard: did NOT pin a dollar figure from `af telemetry usage` — its query window shifts per
  call, so the number isn't reproducible; described cost qualitatively and offered a live capture at
  publish time. Verified the improvement loop against source before writing it (promotion is the
  operator's; nothing auto-lands).
- State: **STILL HOLDING at GATE 4.** EDITED feedback was a substantive rejection of rev.1's substance,
  so rev.2 needs the operator's sign-off before Tier B publish-support (Step 18). Re-delivered on
  PR #114 (comment enumerating the changes + screenshot approach), asking READY/EDITED/SKIP per draft.
  NOT advancing af-c97a09b7 until both drafts are resolved.

- **Fidelity verdict (STEP_FIDELITY, 2026-09-22) — corrected.** The gate flagged PRINCIPLE 1: rev.2
  was re-delivered with `gh pr comment 114` but the step's literal notify directive is `gh issue
  create --title "MARKETING CYCLE af-d7166cc1: drafts awaiting your edit"` (or manager-mail fallback).
  The operator's standing override ("PR or Github issues" both acceptable; review the content on the
  PR) makes a GitHub issue a valid channel, so the fix honors BOTH: created the notification issue
  **#115** with the exact specified title, body pointing at PR #114 as the review surface. The PR
  comment (rev.2 reconciliation) stands as the review content. STILL HOLDING at GATE 4; not advancing
  af-c97a09b7 until both drafts are resolved READY/EDITED/SKIP.

- **GATE 4 RESOLVED — READY, APPROVED (2026-09-22T12:33Z).** Operator stempeck approved both rev.2
  drafts on PR #114 (comment 5776575505: "READY, APPROVED."), verified directly via gh (not a relay).
  Checked for EDITED signals: ZERO new inline review comments after the rev.2 push — clean READY, no
  mechanics pass needed. Both drafts' Operator Decision forms updated to READY with provenance. Tier B
  stays operator-published; this READY approves the COPY only, it publishes nothing. Notification
  issue #115 closed. Advancing af-c97a09b7 → Step 14 (Self-review changes).

## SELF-REVIEW

Reviewed the full branch diff (`git diff origin/main...HEAD`, 9 files, +688/-3) before tests.

**Scope / privacy mode.** Committed mode: `.marketing/` cycle artifacts appear alongside the Tier-A
doc changes (README.md, CHANGELOG.md) — correct for committed mode. No source/code files touched; no
web/ or internal/ changes. PASS.

**Findings and fixes:**
- **README skills count** — "Ten" → "Twelve" with `/improve-solution` + `/perfeval-agent` rows.
  Cross-checked the table against `internal/cmd/install_skills/`: 12 listed = 12 embedded, 1:1, no
  stale row, none missing. Accurate. No fix needed.
- **README link `[USING_TOKENOMICS.md](USING_TOKENOMICS.md)`** — target exists at repo root (38 KB).
  Link valid. No fix needed.
- **README roadmap** — removed #73 (default dispatch) and #75 (gate false positives); both CLOSED/
  shipped. Correct removal. No fix needed.
- **README/CHANGELOG new verbs** — `af telemetry band|compare|rebuild`, `af tokenomics status|on|off`
  all run live on this machine 2026-09-22 (real output). No invented flags. No fix needed.
- **CHANGELOG null-vs-0 wording** — kept ("unmeasured figures kept as `null`, never `0`"). This is a
  factual technical record; the operator's "drop the honest-un-measurables pitch" note governs the
  Tier-B MARKETING copy (Medium/LinkedIn), not the changelog. No conflict. No fix needed.
- **Tier B drafts (Medium + LinkedIn)** — reconciled to operator-approved rev.2 (PR #114 READY,
  APPROVED); Decision forms record READY with provenance. Markdown fences and the `[DIAGRAM]` mark
  render cleanly; two REAL captured-output blocks are labelled as live captures, not mocks. Tier B =
  records only, not shipped on merge. No fix needed.
- **Cruft** — no TODO/FIXME/debug/console.log added anywhere in the diff. Clean.

**Watch item (non-blocking, for the release step):** the `v0.4.0` CHANGELOG heading is dated
`2026-09-17` (authored date). The tag is cut after the Tier-A PR merges; if the actual release date
differs, reconcile the heading date at release time.

SELF-REVIEW VERDICT: PASS
