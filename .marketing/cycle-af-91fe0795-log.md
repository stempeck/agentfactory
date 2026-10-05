<!-- Cycle log — marketing-cycle af-91fe0795 — started 2026-10-05 -->
# Cycle af-91fe0795 — Log

Assignment: PR #117 ("Plugins, ChatGPT-subscription gateway, and relaunches that never run
on stale config"). Operator: Glenn Stempeck (`stempeck`). Repo: stempeck/agentfactory.

## GATE-1 — Audit checklist (pre-selection interlock)

Each item below quotes evidence from `cycle-af-91fe0795-audit.md` and the verification pass.

**1. Is every STALE claim backed by quoted evidence (grep / file:line), not memory? — YES**
- S1: `README.md:236` "Twenty-four formulas ship with the factory" vs real **26**
  (`internal/cmd/install_formulas/*.toml`; `.agentfactory/store/formulas` matches); table
  omits `lineage`, `rapid-soldesign` (grep in README = 0).
- S2: `README.md:249` "Twelve skills are embedded…" vs real **13** (`.claude/skills/`);
  table omits `architecture-diagram` (grep in README = 0).
- S3: `af plugin` family — grep `plugin` in README.md = **0 hits** → ABSENT.
- S4: `af gateway auth` — README "gateway" only at `:244` (gpt-* formulas), no
  `af gateway auth` → ABSENT.
- S5: CHANGELOG top is `## v0.4.0 — 2026-09-17` then `## v0.3.0`; no section for #117.
- S6: `git tag -l 'v*'` = v0.1.0, v0.2.0, v0.3.0 only; `gh release list` latest = v0.3.0 —
  yet CHANGELOG carries `## v0.4.0 — 2026-09-17` → release gap.
- Every #117 command in the NEW list carries a file:line proof (e.g. `af plugin` family
  `internal/cmd/plugin.go:70/84/91`; `af gateway auth` `gateway_auth.go:392/409`;
  `config models check --live/--first` `config_models.go:143/144`;
  `fidelity status` columns `fidelity.go:292`; `af up` `up.go:277-279`; dispatch
  `refused (<class>, Nx)` `dispatch.go:2604`). None from memory.

**2. Is every NEW feature absent from `announced-ledger.md` (checked, not assumed)? — YES**
- Grep of `announced-ledger.md`: `plugin` 0, `codex` 0, `token economics` 0, `tokenomics`
  0, `telemetry band` 0, `telemetry compare` 0, `lineage` 0, `rapid-soldesign` 0,
  `architecture-diagram` 0, `v0.4.0` 0, `stale config` 0, `guard-event` 0.
- Nuance, recorded so it isn't mistaken later: `gateway` returns 2 hits and `telemetry`
  several — but all are in PRIOR TOLD ROWS (v0.2.0 row line 13: "multi-provider agents
  (OpenAI via gateway…)", "Run telemetry"; cycle-3 tour line 14). Those are the OLD
  OpenAI-API-key gateway and base telemetry. The NEW items this cycle — #117's
  ChatGPT-**subscription** gateway + `af gateway auth`, and the token-economics evolution
  (`af telemetry compare`/`band`, `af tokenomics`) — have 0 told-row hits. Confirmed untold.

**3. Does the snapshot record description, topic count, latest release, and homepage as
they are RIGHT NOW? — YES** (captured 2026-10-05, audit "Surface snapshot" section)
- description: "Multi-agent orchestration CLI for Claude Code — declarative TOML workflows,
  autonomous agents, context-compression recovery, inter-agent mail."
- topics: **18** (core set claude-code, ai-agents, multi-agent-systems, agentic-ai present).
- latestRelease: **v0.3.0**.
- homepage: cycle-3 Medium article (`…heres-every-room…`).

**4. Were any gh calls skipped due to auth/scope errors? — NO**
- `gh api user -q .login` → `stempeck` (matches runbook Operator).
- `gh repo view`, `gh api graphql` (pinnedItems), `gh release list`, `gh pr view 114/117`,
  `gh issue view 116` all returned successfully. No 404/scope hint encountered.

GATE-1 VERDICT: PASS

## GATE-2 — story approval (HOLD) + handling correction

- Story proposed (flagship = Plugins); operator notified on **issue #118** with pick +
  ranking + decision keywords inline; artifacts committed `1c16afe2`.
- **Handling correction (fidelity PRINCIPLE 2).** I ran `af done --phase-complete --gate
  af-c28d24be` while the Decision form was still `______` and #118 was freshly open. That
  CLOSED the gate bead (`af bead show af-c28d24be` → Status: closed) before the HOLD
  condition was met — premature closure. No downstream work was executed: Phase 3 was not
  run, no public surface touched. No reopen/rewind verb exists, so the HOLD is enforced
  substantively — Phase 3 will not run until the story Decision line carries
  APPROVE / REORDER:<feature> / END-CYCLE (transcribed from #118). Corrected protocol
  recorded to memory; flagged for an improve-agent pass (step text should gate
  `--phase-complete` on the recorded decision keyword).

GATE-2 STATUS: RESOLVED — operator `stempeck` replied APPROVE on #118
(2026-10-05T22:14:47Z). Verified author + keyword via `gh issue view`. Flagship = Plugins
as written; transcribed into story Decision form. Proceeding to Phase 3.

## Phase 3 — Tier A refresh (committed 35ec58c5)

Flagship APPROVED = Plugins. Changes (all commands verified against source before writing):
- **S1** README formula count 24 → 26; added `rapid-soldesign` (Design) and `lineage`
  (Utility) with source-verified purposes (TOML: rapid-soldesign "ends at the design PR";
  lineage "Read what was inherited — without acting on it").
- **S2** README skills count 12 → 13; added `/architecture-diagram`.
- **S3/S4** README new "Plugins & the model gateway" command block (`af plugin
  list/install/verify/check/remove`, `af gateway auth import/status`) + quickstart
  `--litellm --litellm-auth=codex-subscription` (verified install.go:88/92/102, quickstart.sh:865-869).
- **S5** CHANGELOG `## Unreleased` section for #117 (extend / gateway / stale-config / also).
- **S6 (release decision — NOTED, not executed):** CHANGELOG carries `## v0.4.0 — 2026-09-17`
  with no tag, and #117 is now under `## Unreleased`. Version labeling for both is the
  operator's call at deliver-tier-a (issue #118 asked; no preference given yet).
- Issues filed: NONE — audit found doc staleness (fixed) + the release gap (operator
  decision), no product bugs; main is green. No manufactured activity.
- Doc tests green (`go test ./internal/cmd -run 'PR724|IntegrationDocs|Doc'`).

## GATE-3 — every public claim verified against source

Checklist over `git diff origin/main...HEAD` (public product claims = README.md + CHANGELOG.md):

1. **Unproven commands/flags/subcommands? — NONE.** Every command in the diff was proved
   this session: `af plugin list/install/verify/check/remove` (plugin.go + live `af plugin
   --help`), `af gateway auth import [--from]/status [--json]` (gateway_auth.go:392/409/419/420
   + live help), `af config models check --live/--first` (config_models.go:143/144), `af
   fidelity status` columns (fidelity.go:292), `af dispatch status` `refused (<class>, Nx)`
   (dispatch.go:2604), `af up` already-running (up.go:277-279), `quickstart.sh --litellm
   --litellm-auth=codex-subscription` (quickstart.sh:832/865-869), `af install --agents
   --litellm --litellm-auth` (install.go:88/92/102 + live help).
2. **Uncounted counts? — NONE.** "Twenty-six formulas" = `internal/cmd/install_formulas/*.toml`
   recount (26, store/formulas matches); "Thirteen skills" = `.claude/skills/` recount (13).
3. **Unverified URLs/links? — NONE.** No external http URL added to README/CHANGELOG. All
   relative doc links referenced exist (verified present): USING_PLUGINS/LITELLM/MODELS/
   TOKENOMICS/AGENTFACTORY/RECOVERY/TELEMETRY.md, docs/formulas.md, ADR-024, ADR-025.
4. **Unshipped promises outside Roadmap? — NONE.** grep of added lines for
   coming-soon/will/planned = empty. The CHANGELOG `## Unreleased` section describes features
   already merged to main (via #117) but not yet in a tagged release — accurate, not a promise.

GATE-3 VERDICT: PASS

## Phase 4 — Tier B drafts (voice-calibrated)

Voice recalibrated against the approved finals: `cycle-af-d7166cc1-medium.md` (approved as
written on PR #114) and the operator-edited `cycle-af-d7166cc1-linkedin.md` / cycle-2 short
form. Rules held: lead with the switches an operator types + the outcome; spaced hyphens
" - "; CAPS for emphasis; no self-congratulatory emotion; honest limitations stated.

Artifacts produced (all under `.marketing/`, committed per runbook — not privacy mode):
- **cycle-af-91fe0795-medium.md** — long-form flagship. Title "Three things my agent factory
  couldn't do last month." claims a number (three); body lands exactly three numbered items.
  Subtitle carries the Positioning search phrases. Hook → what shipped → honest limits →
  repo link. Three `[FIGURE:]` marks.
- **cycle-af-91fe0795-linkedin.md** — short-form, ~150 words, question hook, PLAIN TEXT
  (CAPS not asterisks — LinkedIn renders no markdown), closing question invites replies.
- **cycle-af-91fe0795-screen-plugin-verify.png / -gateway-auth.png / -models-check.png** —
  the three marked figures, real CLI captures (valid PNGs, visually verified).
- **cycle-af-91fe0795-paste.html** — rich-text paste vehicle; renders the full long-form
  draft with all three figures embedded via relative `<img src>`; no not-yet-live URL.

Each draft carries an HTML-comment DRAFT header and an Operator Decision form.

## GATE-4 — HOLD for operator draft approval (review surface = GitHub PR)

Runbook routing (lines 16-22, failure-mode 187): the operator reviews ALL on GitHub and
will NOT read a worktree file path. So drafts are delivered INLINE on the review PR; the
operator records READY / EDITED / SKIP per draft as a PR reply or push. A notify issue is
the ping.

Branch note: `af/marketing-cycle-cd14d3` is reused; its origin ref is stale (prior-cycle
commits + orphan improve-agent commit 41616766, NOT in main — see memory note). To avoid
orphaning 41616766 I pushed this cycle's clean history (origin/main + 6 cycle commits +
drafts) to `af/marketing-cycle-af-91fe0795` and opened the PR from there.

Delivered 2026-10-05:
- **Review PR #119** (base `main`, head `af/marketing-cycle-af-91fe0795`) — both drafts
  inline; three figure raw-URLs verified HTTP 200; paste vehicle in Files tab.
- **Notify issue #120** — the ping, points to #119. Close when both drafts resolve.

GATE-4 STATUS: HOLDING — drafts delivered on PR #119; awaiting operator READY/EDITED/SKIP
per draft. `af done --phase-complete --gate af-829d0f7e` will NOT run until the operator's
decision keyword is recorded on #119 (per fidelity PRINCIPLE 2 — do not close a HOLD gate
bead early; learned this cycle at GATE 2).
