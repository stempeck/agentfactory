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
