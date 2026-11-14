# Cycle af-4073aec6 — Execution Log

## Assignment (bead af-4073aec6, urgent)

Operator instruction, relayed verbatim by manager on 2026-11-14:
"sling marketing-cycle but have it skip straight to the part where it publishes a release, no
actually medium article or marketing this time."

Reading applied to every step of this cycle: **release-only.** The deliverable is a published
GitHub release covering what merged to `main` since v0.4.0. No Medium article, no LinkedIn post,
no other Tier B drafts, no story or marketing narrative. Steps that exist to produce marketing
are executed as records of that decision, not as content work.

## Step 1 — Load context and verify assignment

- Authenticated identity: `gh api user -q .login` → `stempeck` (matches runbook `## Operator`:
  GitHub `stempeck`). No mismatch.
- Repository: `gh repo view` → `stempeck/agentfactory`, description "Multi-agent orchestration CLI
  for Claude Code — declarative TOML workflows, autonomous agents, context-compression recovery,
  inter-agent mail."
- Runbook: `.marketing/approach.md` present and read in full; `Runbook-Decision: APPROVED`;
  `Privacy-Decision: COMMITTED`.
- Ledger: `.marketing/announced-ledger.md` present; last row = v0.4.0 release (2026-11-01, tag →
  `eab2b02c`, first release with prebuilt binaries).
- Delta since v0.4.0 (`git log v0.4.0..origin/main`): two commits — `3c50873f` (#133, marketing
  close-out state) and `bec32c58` (#135, context windows / alarms / healing PRs / current configs).
- Artifact location: `af root` resolves to this worktree
  (`/home/dev/af/agentfactory/.agentfactory/worktrees/wt-169e5e`), so `$(af root)/.marketing/`
  is the worktree's committed `.marketing/`. All cycle artifacts are written there, never the
  shared parent checkout.
- Mail: one MEMORY_HYGIENE notice (af-ba6ef125) — the 3 due notes were graduated to
  `doc:.marketing/approach.md` (the runbook §0 review-surface rule and the Standing Assets note
  carry both learnings) and the mail was deleted.

## Step 2 — Bootstrap runbook

Runbook present and approved: `grep '^Runbook-Decision: APPROVED$'` matches,
`grep -E '^Privacy-Decision: (COMMITTED|PRIVATE)$'` matches (COMMITTED), and
`git check-ignore --no-index -v .marketing/approach.md` returns nothing, so git agrees with
committed mode. No interview needed.

## Step 3 — Working branch

Worktree mode: stayed on the worktree's own branch `af/marketing-cycle-169e5e` (a worktree is
pinned to its branch; no `marketing/cycle-*` branch created). `git fetch origin` then
`git rebase origin/main` → HEAD = origin/main = `bec32c58`, up to date. The one dirty file at
start was the launch-regenerated `.agentfactory/agents/marketing-cycle/CLAUDE.md` (a one-line
Working-directory path rewrite) — restored with `git checkout --` so it cannot ride a PR. The
only stash entry (`wt-52363c-pr733-cruft-claudemd`) belongs to another session on the shared
stash stack and was left alone. Remaining change: this log (cycle artifact).

## Step 4 — Design contract

`ls .designs/af-4073aec6/design-doc.md` → no design contract found. The approved runbook plus
the operator's instruction in bead af-4073aec6 are the contract. Verification points for
self-verify:
1. Release-only: no Medium, LinkedIn, or other Tier B draft is produced this cycle.
2. The release covers exactly what merged to `main` since v0.4.0 (#133, #135), and every
   claim in its notes is checked against source (runbook "Honesty").
3. Branch → PR; only the operator merges; never push to `main` (runbook §0).
4. The release carries prebuilt binaries + `SHA256SUMS` like v0.4.0 (operator expectation
   recorded cycle af-a6b105d1).
5. CHANGELOG gains a section for the new version so the tag and the CHANGELOG agree.

## Step 5 — Tests on main

HEAD = origin/main = `bec32c58`, so no checkout or stash was needed.
- Local: `make test` on `bec32c58` → exit 0 (all root-module packages `ok`; log kept in the
  git-ignored agent dir as `preflight-test.log`).
- CI: main push run 43554587130 for `bec32c58` → **failure**, one job: `integration`
  (`panic: test timed out after 4m0s`, `FAIL github.com/stempeck/agentfactory/internal/cmd
  240.013s`). The test running at the alarm was `TestWindowFacts_ClaimSerialisesReadModifyWrite`
  (1s in), so the whole package exhausted `-timeout=4m` (Makefile:101) rather than one test
  hanging. All other jobs (unit, web-unit, toml-conformance, regen, supply-chain-lint,
  advisory-decay) passed.
- Same code passed: `git rev-parse bec32c58^{tree}` = `8e766dda^{tree}` = `59ddaff8` (PR #135
  head), and that PR's integration job passed with `internal/cmd` at **221.840s** — 92% of the
  4m budget. Diagnosis: timing flake on a package close to its budget, not a code defect.
- Also seen: the daily scheduled `advisory-decay` job failed on main 2026-11-06 → 11-13 (runs
  on `3c50873f`); it passed on the `bec32c58` push run.
- Filed: bead **af-7346a737** (bug, P1) for the 4m budget; supervisor mailed. Requested a
  rerun of the failed job (attempt 2).
- **Release consequence:** per this step, no release is published while main is red. The
  attempt-2 result is carried into deliver-tier-a; the tag waits for a green main.

## GATE-1

**Item 1 — Every STALE claim backed by quoted grep/file:line evidence, not memory? YES.**
- S1 (CHANGELOG missing #135): `grep -n '#135\|#134' CHANGELOG.md` → `NO MATCH - CHANGELOG has
  no #135/#134 entry`; `grep -n '^## ' CHANGELOG.md` → `6:## v0.4.0 — 2026-11-01` is the newest
  section. Audit quotes the same: "Top section is `## v0.4.0 — 2026-11-01`; `grep -n '#135\|#134'
  CHANGELOG.md` → no match | **STALE**".
- Claims judged VERIFIED (so not on the STALE list) are also command-backed: README.md:266
  "Twenty-six" vs `ls internal/cmd/install_formulas/*.toml | wc -l` = 26; README.md:279
  "Thirteen" vs `ls -d .claude/skills/*/ | wc -l` = 13. README command paths: originally
  evidenced by `--help` exit 0, which GATE 3 found proves nothing (`af telemetry bogusverb
  --help` also exits 0). Re-proven at GATE 3 from the binary's `Available Commands` lists and
  positional usage lines. All still VERIFIED, and the audit row is amended.

**Item 2 — Every NEW feature absent from announced-ledger.md (checked, not assumed)? YES.**
- `grep -niE 'window|startup|advisory-heal|healing|config-seed|#135|#134' announced-ledger.md`
  → one hit, ledger:11, where "window" is the 2026-08-03 Medium title ("…here's the window into
  it"), not #135's context-window work. `pin` hits ledger:16 ("install from pinned commits") and
  ledger:18 ("brings each component to its pin"), both earlier plugin/security rows. No row
  claims any of the 9 NEW items (audit "NEW" section).

**Item 3 — Snapshot records description, topic count, latest release, homepage RIGHT NOW? YES.**
- Live `gh repo view stempeck/agentfactory --json description,repositoryTopics,latestRelease,homepageUrl`
  this session: description "Multi-agent orchestration CLI for Claude Code — declarative TOML
  workflows, autonomous agents, context-compression recovery, inter-agent mail."; topics = **18**;
  latestRelease = **v0.4.0** (2026-11-01T21:04:31Z); homepage = the
  …three-things-my-agent-factory-couldnt-do-last-month-4c98d4a7c1b4 Medium URL. All four are in
  the audit "Snapshot" section.

**Item 4 — Any gh calls skipped due to auth/scope errors? NO.**
- `gh api user -q .login` → `stempeck`. `gh repo view`, `gh api graphql` pinnedItems, `gh pr view
  135`, `gh run list/view`, and `gh run rerun --failed` all returned without a 404 or scope hint.

GATE-1 VERDICT: PASS

## GATE-2 (Story approval) — RESOLVED from the operator's instruction

- Form appended to `cycle-af-4073aec6-story.md`; Decision recorded **APPROVE** (pick: v0.5.0
  release, release-only), transcribed from bead af-4073aec6's verbatim operator instruction. It is
  marked as transcribed, with its source.
- Notify: `gh issue create` → **#136** https://github.com/stempeck/agentfactory/issues/136 (states
  the recorded decisions, the CI caveat, and the REORDER / END-CYCLE override). No operator
  answer is required, because the operator already decided. #136 stays open until GATE 4 records the
  Tier B SKIP from the same instruction, then closes.
- Why the gate does not wait for a fresh keyword: the operator asked this cycle to "skip
  straight to the part where it publishes a release". Holding for a second approval of the
  same choice would defeat that. An override on #136 is still honoured if one arrives.

## Step 10 — Phase 3: Tier A (release-only scope)

1. STALE S1 fixed: `CHANGELOG.md` gains `## v0.5.0 — 2026-11-14` above v0.4.0, covering #135.
   Every command, flag, number and file in it was checked against source on this branch
   (evidence table in GATE-3). #135's PR body was the starting point; four of its claims could
   not be confirmed in source quickly and were **omitted, not guessed**: the `af config models set`
   window warning, "a respawn is never refused", "`af up` warns when manager cannot receive
   escalations", and the `af up` warning for user-scope window keys. Two were narrowed to what
   the code shows: the `WINDOW` alarm (confirmed in `af statusline status` alerts, not confirmed
   at `af up`) and the web console stall display (only the read-model's `stall` field confirmed,
   so the web console mention was dropped).
2. README: no change. Release-only cycle; the README's existing claims all VERIFIED (audit).
   #135's new verbs not yet in the README Command Reference are next-cycle backlog.
3. Issues: the one real gap found (main CI integration budget) is tracked as bead af-7346a737
   and with supervisor (who dispatches engineering), and reported to the operator on #136. No
   duplicate GitHub issue filed.
4. Release decision noted for deliver-tier-a: **v0.5.0 is a meaningful boundary** (new commands
   and flags → minor bump). Tag only after the operator merges the CHANGELOG PR **and** main is
   green. Binaries + `SHA256SUMS` as v0.4.0.
5. Commits on `af/marketing-cycle-169e5e`: the CHANGELOG, then the cycle records (committed
   mode).

## GATE-3

Scope: `git diff origin/main...HEAD` = CHANGELOG.md (+78) and the three cycle records under
`.marketing/`. Proof source: a binary built from this branch's base (`bec32c58`) plus greps of the
tree.

**Item 1 — Any command, flag or subcommand not proven this session? NONE.**
- Subcommands proven by the binary's `Available Commands` lists: root (`up`, `sling`, `agents`,
  `dispatch`, `memory`, `recovery`, `install`, `config`, `statusline`, `tokenomics`, `telemetry`),
  `config` → `models`; `config models` → `check`; `agents` → `list`; `dispatch` → `status`;
  `recovery` → `reset`; `memory` → `add list pin show status unpin`.
- Positional verbs proven by usage lines: `af telemetry [on|off|status|report|band|usage|compare|rebuild|startup]`,
  `af tokenomics [on|off|status]`, `af statusline [on|off|status|render]`.
- Flags proven by flag lists or source: `--accept-window` (`af up --help` flags; `internal/cmd/sling.go:113`);
  `af sling --agent` / `--formula` (sling flags); `af telemetry startup --agent` limited to manager and
  supervisor (`internal/cmd/telemetry_startup.go:51` "the startup turn is measured for manager and
  supervisor only") and `--json` (telemetry flags); `af memory pin --agent` (pin flags);
  `af memory list --all-rows` / `--type` / `--formula` (list flags); `af dispatch status --json`,
  `af agents list --json` (flags); `af install --init` (flags).
- Behaviour claims checked in source: `WINDOW_MISMATCH` (`internal/cmd/window_text.go:47`); `WINDOW`
  alarm in the statusline alerts block (`internal/cmd/statusline.go:593`); `unobserved`
  (`internal/cmd/window_detector.go:219`); `window_declared` / `window_applied`
  (`internal/cmd/agents.go:108-109`); `af tokenomics status` "prints the resolved context window
  TOGETHER with where that number came from" (its help); `af config models check` "every profile
  whose model id is not foreign … an unrecognised id, a window that differs from the declaration, or
  a probe that cannot complete fails the check" (its help); `STARTUP` alarm
  (`internal/cmd/startup_signal.go:13`) cleared by `af recovery reset` ("cleared the startup
  measurement (STARTUP alarm)", `internal/cmd/recovery.go:209`); old directive warning
  (`internal/cmd/up.go:589` `staleDirectiveFullList = "(af memory list)"`); sender `af`
  (`internal/cmd/dispatch.go:1224`); `WORKFLOW_COMPLETE` / `WORKFLOW_STALL` (`dispatch.go:1230,1240`);
  `stall` JSON field (`dispatch.go:134`); STALL column (`dispatch.go:2670`); `hold` / `drop` / `skip`
  lines (`dispatch.go:1686`, `:992`, `:339`); advisory exit codes (`.github/advisory-audit.sh`:
  `finding) exit 1`, `infrastructure) exit 3`, `exit 2` on setup errors, `exit 0`); `excused-fixable`
  (`advisory-audit.sh:378`); labels `agentic,advisory-heal` (`.github/workflows/test.yml:333`), filing
  only in the repository the filing steps' `if:` names (`test.yml:327`, mirrored by the CHANGELOG
  wording); checksum-verified `gh` (`.github/install-gh.sh:31`); `Resolves …#N`
  (`fable-implement.formula.toml:1291`); seeded config names and manager+supervisor-only
  `agents.json` (`internal/cmd/seed_configs.go:23,32-33`); `AF_SOURCE_ROOT` (`seed_configs.go:154`);
  `.config-seed.log` in the config dir `.agentfactory` (`install.go:427`, `config/paths.go:13`); BSD
  sed (`quickstart.sh:2176`).
- Removed rather than verified: the four #135 claims listed in the Step 10 log entry, the
  web-console stall display, and "at `af up`" for the `WINDOW` alarm.

**Item 2 — Any count not recounted from the filesystem? NONE.**
- Version and size figures were each read from the file that sets them: Claude Code `2.1.322`
  (`quickstart.sh:105`); 90,000-token budget (`internal/cmd/startup_budget.go:21`
  `startupTurnBudgetTokens = 90000`); 3,072-byte pinned block (`internal/memory/slice.go:25,92`
  `DefaultStandingBytes = 3072` "bounds the rendered pinned block"); 8,192 bytes (`af memory list`
  `--all-rows` help); Go 1.27.4 (`go.mod:5`, `web/go.mod:5`, `test.yml:72`); Node.js 22.23.5
  (`quickstart.sh:87` `NODE_APT_VERSION="22.23.5-1nodesource1"`); `oauthlib==4.0.1`, `filelock==4.2.9`
  (`py/litellm-requirements.txt:1647,694`); ADR-028/029 (`ls docs/architecture/adrs/`). The CHANGELOG
  names the six seeded files individually and asserts no total.

**Item 3 — Any URL not fetched or built from a verified pattern? NONE.**
- `https://github.com/stempeck/agentfactory/issues/136`: `gh issue view 136` → OPEN, same URL.
- `https://medium.com/@glennstempeck/three-things-my-agent-factory-couldnt-do-last-month-4c98d4a7c1b4`:
  live `gh repo view --json homepageUrl` returns exactly this URL (snapshot record only).
- `https://medium.com`: the runbook's homepage-allowlist prefix, quoted.
- `[USING_ADVISORYLOOPS.md](USING_ADVISORYLOOPS.md)`: relative link; the file exists at repo root.

**Item 4 — Any unshipped promise outside a Roadmap section? NONE.**
- `git diff origin/main...HEAD | grep '^+' | grep -iE 'coming soon|will ship|planned|future release|soon'`
  → no output. Everything in the v0.5.0 section is merged in `bec32c58`.

GATE-3 VERDICT: PASS

## Step 12 — Phase 4: Tier B drafts — NOT PRODUCED (operator instruction)

No long-form article, short-form post, figures or paste vehicle were produced this cycle. The
operator's instruction for this cycle (bead af-4073aec6, relayed verbatim) is "no actually medium
article or marketing this time". Drafting them anyway would spend the operator's review time on
pieces they have already declined. The formula's own outcome for a declined piece is GATE 4
`SKIP`, recorded there from the same instruction. So no `cycle-af-4073aec6-medium.md`,
`-linkedin.md`, `-screen-*.png` or `-paste.html` exists, and the cleanup manifest has nothing
from this step to account for.

## GATE-4 (Draft approval) — RESOLVED: every Tier B piece SKIP

- No drafts exist (Step 12), so the per-draft forms are recorded in `cycle-af-4073aec6-story.md`
  `## Tier B — Operator Decision (GATE 4)`: Medium SKIP, LinkedIn SKIP, sourced to the operator's
  instruction (bead af-4073aec6).
- Notify: the operator was told on **#136** at GATE 2 ("Tier B: SKIP. No Medium article, no
  LinkedIn post, no README marketing refresh."). A second "drafts awaiting your edit" issue
  would announce drafts that do not exist, so none was opened. #136 is closed now that both gates
  are resolved. The release discussion (including the CI blocker) continues on the CHANGELOG PR.
- SKIP handling: marked skipped for the cycle report; no argument.

## SELF-REVIEW

`git log --oneline origin/main..HEAD`: 4 commits (CHANGELOG; cycle records; GATE-3 evidence;
GATE-4 SKIP). `git diff --stat origin/main...HEAD`: CHANGELOG.md plus the three
`.marketing/cycle-af-4073aec6-*.md` records.

Findings and fixes:
1. CHANGELOG grammar: "Over budget or missing rulings raises a `STARTUP` alarm" → "A startup
   over budget, or one missing its rulings, raises a `STARTUP` alarm". Fixed.
2. Stale claims reintroduced: none. The section adds only #135 content verified at GATE 3, and
   no existing CHANGELOG line changed (`+78`, `-0` apart from the fix above).
3. Relative links: one, `[USING_ADVISORYLOOPS.md](USING_ADVISORYLOOPS.md)`. The file exists at
   repo root, the same directory as CHANGELOG.md, so it resolves on GitHub.
4. Host rendering: headings, bullets and inline code only, the same constructs as the v0.4.0
   section. No tables, HTML or nested fences. Two lines run to 100 columns, which is cosmetic in
   markdown.
5. Cruft: `git ls-files --others --exclude-standard` → empty. No TODO/FIXME, screenshots, paste
   vehicles or debug files in the diff. Scratch files (`af-head` binary, `preflight-test.log`)
   live in the git-ignored agent dir and are removed at cleanup.
6. The release date `2026-11-14` in the heading assumes the tag goes out today. If main CI keeps
   the tag waiting past today, the heading date is corrected on this branch before merge.
7. Privacy mode: COMMITTED. `.marketing/` cycle records appear in the diff alongside the doc
   change, as committed mode requires.

SELF-REVIEW VERDICT: PASS

## Step 15 — Tests and build on the branch

- `make test` (runbook test command) on `af/marketing-cycle-169e5e` → exit 0; every root-module
  package `ok`, no `FAIL` lines.
- `go build ./...` (runbook build command) → OK; `cd web && go build ./...` → OK.
- Not run locally, per the runbook ("never `make test-integration` locally"): the integration tier
  is CI's job. Main's integration lane is red on the 4m budget (bead af-7346a737), so this PR's own
  integration job may fail the same way although the PR changes no code.

## SELF-VERIFY

Contract: the approved runbook plus the operator's instruction (Step 4 verification points 1–5).

- **Tier A — every STALE claim fixed, no unverified claim entered: YES.** S1 (CHANGELOG missing
  #135) → `## v0.5.0 — 2026-11-14` in CHANGELOG.md (commit `5e02e522`, grammar fix after). Every
  claim in it is evidenced in GATE-3. Unverifiable #135 claims were omitted (Step 10 entry).
- **Tier B — every draft operator-resolved, none self-approved: YES (no drafts exist).** Both
  pieces are SKIP, transcribed from the operator's own instruction and stated to the operator on
  #136. That is a decline the operator made, not an approval by me. Deviation, documented: the
  per-draft forms live in the story file because no draft files exist (Step 12, GATE-4 entries).
- **Tier law — zero external posts by me: YES.** Outward actions this cycle: GitHub issue #136
  (create, one comment, close) on the operator's own repo, and a rerun of main's failed CI job.
  Nothing was posted, scheduled or submitted off GitHub.
- **Voice law — EDITED drafts enumerated: N/A.** No operator text was edited; no EDITED decision.
- **Privacy mode — diff matches declared mode: YES.** `Privacy-Decision: COMMITTED`;
  `git diff --stat origin/main...HEAD` lists the three `.marketing/cycle-af-4073aec6-*.md` records
  with CHANGELOG.md.
- **Step-4 points:** (1) release-only, no Tier B: met. (2) release covers exactly #133/#135, and
  only #135 ships to users: the CHANGELOG covers #135, and #133 is cycle records. (3) branch → PR,
  operator merges: next step. (4) binaries + `SHA256SUMS`: at release, after merge. (5) CHANGELOG
  section for the version: met.
- **Open deviation carried forward:** main CI is red (bead af-7346a737). Per preflight-tests, no
  release is published until main is green or the operator explicitly overrides. Handled at
  deliver-tier-a.

SELF-VERIFY VERDICT: PASS

## Step 17 — Deliver (HOLD)

- PR #137 pushed and open (head `fc0b8170`). The body has the release recommendation and the main-CI blocker.
- PR CI, run 43556893600: every check passed except `integration`. Attempt 1:
  `TestAdvisoryDecay_FailsOnFixtureAdvisory/shipped_locks_and_register_pass_at_the_earliest_recheck_by`
  (`py/__pycache__` appeared during the `py/` snapshot, `internal/cmd` 233.051s). Attempt 2 (rerun):
  `panic: test timed out after 4m0s`, `internal/cmd` 240.012s, the same failure as main. Not rerun again.
- Main health: still red. Push run 43554587130 on `bec32c58` failed `integration` on both attempts. The
  2026-11-14 scheduled run (43561155995) shows success but ran only `advisory-decay`.
- Operator surface: PR comment #issuecomment-6872443854 (CI status, main health, merge and release
  decisions). Manager org record sent, with a routing request for af-7346a737; its notes are updated
  with the PR evidence.
- Release decision: v0.5.0 recommended. Tag only on the merge commit, and only once main's `integration`
  passes or the operator replies `RELEASE NOW` on #137.
- Fidelity correction (STEP_FIDELITY af-a5854077): my first PR comment and manager mail left the merge
  to the operator while `integration` was red. That broke the green-before-merge directive. I edited
  the comment to withdraw the merge request and sent the manager a CORRECTION mail. The merge will be
  requested only after the af-7346a737 fix lands on main and a rerun of this PR's CI is green.
- Routing: manager (af-ca130dcb) says dispatching an engineer for af-7346a737 is the operator's
  decision. Manager raised it at 04:30Z, 04:40Z and 05:10Z with no answer, so the decision is now also
  on PR #137's comment.
- HOLD: the gate stays open until #137 is merged.
