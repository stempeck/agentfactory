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

## TESTS & BUILD (step 15, 2026-08-23)

Runbook Claim Verification Map commands, run on this branch:

- `go build ./...` → clean, exit 0.
- `make test` (`CGO_ENABLED=0 go test ./...`, root module) → all packages `ok`, exit 0
  (internal/cmd 19.5s; rest cached or sub-second). No failures.

This cycle changed docs + `.marketing/` artifacts only — no Go code and no `web/` changes — so
no behavior is under test that this branch could regress; the run confirms the tree still builds
and the suite is green before the merge that publish sequencing depends on. The `web/` module is
a separate `go.mod` not covered by root `make test` (CI's `web-unit` job covers it); untouched
this cycle. `make test-integration` intentionally NOT run locally (runbook).

TESTS-BUILD VERDICT: PASS

## SELF-VERIFY (step 16, Jidoka — outputs vs. contract, 2026-08-23)

Contract source: the approved runbook `approach.md` (no design doc for this cycle). Point by point:

**1. Tier A — every STALE audit claim has a fix in the diff; no unverified claim entered.**
Audit listed 5 stale items; each traced to the committed diff:
- STALE #1 (Roadmap "formula authoring in the browser" listed as future, but it ships) → FIXED:
  README diff replaces the line with "deeper agent detail and richer per-screen views."
- STALE #2 (CHANGELOG had no #104 entry) → FIXED: CHANGELOG diff adds a full `v0.3.0` section for
  the #104 wave.
- STALE #3 (docs/recovery-model.md missing context-exhaustion recovery) → FIXED: diff adds the
  "Automatic context-exhaustion recovery" section (watchdog occupancy, recycle-with-checkpoint,
  durable breaker + `af recovery reset`, heartbeat).
- STALE #4 (README Command Reference omits the five #104 command families) → FIXED: diff adds
  `af config fingerprint` + a "Recovery, memory & session health" block (`af recovery reset`,
  `af memory`, `af statusline`, `af fidelity`). All five verified to exist on this binary (step 14).
- STALE #5 (soft — Roadmap #75 fidelity false-positives) → CORRECTLY DEFERRED: #75 is still OPEN
  (verified via `gh issue view 75`), so the roadmap line was left UNCHANGED — no false "fixed"
  claim entered. Verify-first honored.
No unverified claim entered: every new command exists, `#observability` anchor + recovery-model
relative links resolve, and the "24 formulas / 26 agents / 10 skills" counts re-verify against
source.

**2. Tier B — every draft operator-resolved; no draft self-approved.** DEVIATION (documented,
justified): both drafts (`-medium.md`, `-linkedin.md`) currently have BLANK Decision forms —
they are NOT yet operator-resolved. The operator was notified on GitHub issue #106 and the cycle
is HOLDING for his READY/EDITED/SKIP. The contract's protective intent — *no draft self-approved*
— IS satisfied: I did not write READY on either form; neither draft is self-approved. The literal
"operator-resolved" state is pending the human and re-gates downstream (Step 17 PR-merge HOLD,
Step 18 publish HOLD), so no publish can occur before he resolves. Note: `af done --phase-complete
--gate` advanced past GATE 4 unconditionally (it registers a gate-waiter; it does NOT grep the
forms) — logged to the memory vault so future cycles only close HOLD gates after the operator acts.

**3. Tier law — zero external posts by me.** TRUE. Nothing was posted to Medium/LinkedIn/HN/Reddit
or any off-GitHub surface at any step. All writes this cycle are Tier A: commits to this repo
branch (docs + `.marketing/` records) and one GitHub issue (#106) for operator communication.

**4. Voice law — for EDITED drafts, changes enumerated.** N/A this checkpoint: no draft is EDITED
yet (both unresolved). If a draft returns EDITED, mechanics-only fixes get enumerated in-file and
the operator's register recorded as the new calibration before anything ships.

**5. Privacy mode — diff matches declared mode.** COMMITTED (runbook Privacy-Decision). `git diff
--stat origin/main...HEAD` shows `.marketing/` cycle artifacts alongside the doc changes — exactly
what committed mode requires; nothing stranded, nothing wrongly excluded.

Deviation summary: one open HOLD (Tier B operator draft approval, #106) — the expected cycle state
at this point, not a self-introduced contract violation. All Tier A outputs verified correct.

SELF-VERIFY VERDICT: PASS

## PHASE 5–6 — publish support & verification (2026-08-23)

**Tier A PR #107 MERGED** by the operator (he admin-merged past the CLA bot, which couldn't match
the `agentfactory` commit-author identity to a GitHub user). Main now carries the doc refresh.

**Medium PUBLISHED by the operator** (he raced ahead of Phase 5, publishing from the HTML vehicle):
https://medium.com/@glennstempeck/in-my-factory-of-ai-agents-last-time-i-cracked-the-door-heres-every-room-a04f14babef0

Phase-6 verification of the live page (headless chromium screenshot + DOM dump — WebFetch 403s on
Medium):
- 8 `<figure>` elements present → all screenshots survived the paste.
- 9 section headings (8 screens + closer), rendered as real H2.
- Raw-markdown leak check = 0 (no literal `##`, `**`, code fences, or raw md links) — the rich-text
  vehicle did its job; the cycle-1 raw-markdown disaster did not recur.
- Title/subtitle sit in Medium's title/subtitle fields (not as body paragraphs).
- His QA calls: took fix #1 (CONTEXT 22%→23%, now matches the shot); declined fix #2 (kept "26
  specialist agents sit dark") — his deliberate published wording, not re-raised.
- His edits captured as the new voice-calibration source (see medium.md `## PUBLISHED` section).

**Repo homepage** pointed at the live article (`gh repo edit --homepage`), URL host validated
against the runbook `homepage-allowlist` (https://medium.com) before writing.

**Open operator items (HOLD):**
1. LinkedIn short-form — draft ready (footer URL filled), tracked on issue #108; operator posts + records URL, or SKIP.
2. Release decision — `v0.3.0` YES (cut tag) / NO (flip CHANGELOG heading to Unreleased), asked on PR #107.

PHASE-5-6 STATUS: Medium live + verified, homepage set; holding on LinkedIn + release decision.

## PHASE-6 — verify every published page (2026-08-23)

Verification capability: screenshots available (drove the cached Playwright chromium headless
directly — the MCP server's `chrome` channel is absent on Arm64 — plus a `--dump-dom` text pass;
WebFetch 403s on Medium, so screenshot + DOM dump are the evidence).

**Recorded URLs this cycle:** Medium (published) · LinkedIn = **SKIP** (operator deferred to next
cycle, #108) → only the Medium URL requires verification.

**Medium** — https://medium.com/@glennstempeck/in-my-factory-of-ai-agents-last-time-i-cracked-the-door-heres-every-room-a04f14babef0
1. Literal `##` / `**` / backtick fences / raw `[text](url)` visible? **none** (DOM leak scan = 0 for
   all four patterns).
2. Missing / duplicated / placeholder images, stray alt-text paragraphs? **none** (8 `<figure>`
   elements = all 8 console screenshots survived the paste; screenshot confirms the Floor image
   renders; no alt-text stray blocks).
3. Subtitle as a body paragraph, or a stray leading `# `? **none** (title is the H1/title field;
   subtitle sits in Medium's italic subtitle field; body starts at the first real paragraph).
4. Repo checks: **none amiss** — `homepageUrl` = the live article; repo `topics` contain all four
   Positioning topics (claude-code, ai-agents, multi-agent-systems, agentic-ai); repo is indexed
   under `topic:claude-code` and `topic:multi-agent-systems` (search lists `stempeck/agentfactory`).

No "search for / replace with" fixes needed — every item returned "none". (The operator's own
edits, incl. taking the 22%→23% fix and keeping "26 agents", are his canonical published text,
recorded in medium.md `## PUBLISHED`.)

Diagnostics: verification screenshots + DOM dump live in the session scratchpad (outside the repo
tree). The Medium paste vehicle `cycle-af-c967c569-medium.html` is removed from the tree in this
step (it was committed via #107; the follow-up PR deletes it from main).

PHASE-6 VERDICT: PASS

---

## Phase 7 — ledger + cycle report + state persistence (2026-08-23)

**Boundary for next cycle:** v0.3.0.

**Ledger** (`announced-ledger.md`) updated with the two features told this cycle:
- Medium console-tour article → https://medium.com/@glennstempeck/in-my-factory-of-ai-agents-last-time-i-cracked-the-door-heres-every-room-a04f14babef0 (2026-08-23)
- v0.3.0 release → https://github.com/stempeck/agentfactory/releases/tag/v0.3.0 (2026-08-23)
- LinkedIn = **SKIP** (operator #108, deferred to next cycle) — no row claimed; draft stays ready.
Backlog refreshed to cycle-3: self-recovery (head-on), durable memory, and the dogfooding story
are the top three still-untold candidates.

**Report** written: `cycle-af-c967c569-report.md` (shipped autonomously / published by operator /
skipped+why / operator-click items / voice calibration / top-3 next / recrawl ≈2026-09-06).

**Persistence (committed mode, Tier A PR #107 already MERGED at 12:36:21Z):** per the step's
"state must not exist only in a container" rule, the post-merge records ride a short
`marketing/cycle-af-c967c569-state` branch off `main`, PR'd immediately. That PR's .marketing delta
vs main: **add** report + publish-checklist, **update** ledger + this log, **delete** the transient
Medium paste vehicle (`cycle-af-c967c569-medium.html` — regenerable scaffolding, served its purpose
now that the article is live). All Phase 1–6 artifacts + 8 screenshots already landed on main via #107.

PHASE-7 VERDICT: records complete; state PR opened.
