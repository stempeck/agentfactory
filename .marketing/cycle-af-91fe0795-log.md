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

GATE-4 STATUS: RESOLVED — operator `stempeck` commented **READY** on issue #120
(2026-10-05T23:47:30Z). Author + keyword verified via `gh issue view`. Bare READY =
approve-as-written for BOTH drafts (Medium + LinkedIn); no edits, so no voice reconciliation
and no paste.html re-render needed. Keyword transcribed into both draft Decision forms. Open
calls resolve to defaults (primary title, keep figure 3); release-tag question carried to
step 17. Issue #120 closed. Proceeding: `af done --phase-complete --gate af-829d0f7e`.

## SELF-REVIEW

Reviewed `git diff origin/main...HEAD` (9 commits) before tests.

Findings and fixes:
1. **Privacy mode / diff shape — OK.** Committed mode: `.marketing/` cycle artifacts
   (audit, story, log, medium, linkedin, paste.html, 3 screenshots) appear alongside the
   README/CHANGELOG changes, as expected. No source code, no debug files, no stray binaries.
2. **CLAUDE.md regeneration noise — EXCLUDED (fix).** `af prime` rewrote the agent CLAUDE.md
   working-directory path to the worktree path; that machine-generated 1-line change was kept
   OUT of every commit (it is not cycle content).
3. **Stale claims reintroduced — NONE.** The two stale counts the audit caught are the only
   count edits: "Twenty-four formulas"→"Twenty-six", "Twelve skills"→"Thirteen" — both match
   the GATE-3 source recount (`install_formulas/*.toml`=26, `.claude/skills/`=13).
4. **Broken relative links — NONE.** Verified present on HEAD: USING_PLUGINS/LITELLM/MODELS/
   TOKENOMICS/AGENTFACTORY/RECOVERY/TELEMETRY.md, docs/formulas.md; ADR-024/ADR-025 files
   exist under docs/architecture/adrs/.
5. **Host-markdown rendering — OK.** README tables well-formed (3 columns intact after the
   longer Design/Utility rows); CHANGELOG uses standard `##`/`###`/list/inline-code — renders
   on GitHub. No literal-markdown hazards in the committed Tier A text.
6. **Cruft (TODO/FIXME/placeholder/TBD) — NONE** in the README/CHANGELOG diff. The 3 PNGs are
   intended figure artifacts, not cruft.
7. **Tier B drafts — resolved READY** (operator-verified #120); Decision forms transcribed.
   No EDITED reconciliation outstanding, so paste.html matches the approved copy.

SELF-REVIEW VERDICT: PASS

## Step 15 — Tests + build

Runbook Claim Verification Map test command = `make test` (never `make test-integration`
locally). Ran from the worktree root (inside the boundary):
- **`make test`** — all packages `ok`, no FAIL. `internal/cmd` (doc-consistency tests:
  formula/skill counts, doc links) green in 51.2s.
- **`make build`** — exit 0; "Formulas in sync", "Skills in sync"; `./af` built (gitignored).

Working tree clean apart from the excluded CLAUDE.md regeneration line.

STEP-15 VERDICT: PASS (tests pass, build clean).

## SELF-VERIFY (Jidoka — outputs vs. runbook contract)

Contract re-read from the approved runbook (`approach.md`); no design doc for this cycle.
Point by point, each with evidence:

**Tier A — every STALE audit claim fixed; no unverified claim entered.**
- S1 (STALE, README 24→26 formulas + lineage/rapid-soldesign rows) — FIXED: README diff shows
  "Twenty-four"→"Twenty-six", `rapid-soldesign` added to Design, `lineage` added to Utility.
- S2 (STALE, README 12→13 skills + architecture-diagram row) — FIXED: "Twelve"→"Thirteen",
  `/architecture-diagram` row added.
- S3 (ABSENT, `af plugin` family) — FIXED: new "Plugins & the model gateway" block
  (`af plugin list/install/verify/check/remove`).
- S4 (ABSENT, `af gateway auth` + ChatGPT-subscription billing) — FIXED: `af gateway auth
  import/status` in the block; quickstart gains `--litellm --litellm-auth=codex-subscription`.
- S5 (MISSING, CHANGELOG #117 section) — FIXED: `## Unreleased` section for #117.
- S6 (RELEASE GAP) — by design an OPERATOR decision at deliver-tier-a. Carried to step 17;
  #117 sits under `## Unreleased` with no premature version tag. Documented deviation, not a
  defect. No unverified claim entered (GATE-3 VERDICT: PASS — every command/count/link sourced).

**Tier B — every draft operator-resolved; none self-approved.**
- medium.md + linkedin.md both carry `Decision: READY — operator stempeck, verified #120`.
  Verified via `gh issue view 120` (author=stempeck, keyword=READY). No self-approval.

**Tier law — zero external posts by the agent.**
- All agent actions were GitHub (operator-owned Tier A) or agent-comms: review PR #119,
  notify issue #120 (closed), one PR comment. ZERO Medium/LinkedIn/external posts. Tier B
  publishing remains the operator's; drafts are paste-ready only.

**Voice law — EDITED enumeration.**
- N/A: both drafts READY (approved as written); no EDITED reconciliation, so nothing to
  enumerate. paste.html already matches the approved copy.

**Privacy mode — diff matches declared mode.**
- Runbook declares `Privacy-Decision: COMMITTED` (approach.md:236). Diff includes the
  `.marketing/` cycle artifacts alongside README/CHANGELOG — matches committed mode.

SELF-VERIFY VERDICT: PASS

## PHASE-6 — Verify every published page (2026-10-06)

Recorded URLs (from `cycle-af-91fe0795-publish-checklist.md`): Medium = published;
LinkedIn = SKIP (operator chose "Medium only", verified on #121). LinkedIn carries no page,
so nothing to verify there — recorded SKIP below; the cycle is not all-SKIP, so this step runs.

**Verification Capability (runbook): "Screenshots available (Playwright)." Degraded this run —
noted, compensated per step directive.** Live-page screenshot could NOT be captured:
- Playwright MCP has no Chrome binary here (`/opt/google/chrome/chrome` not found).
- The prior session's scripted chromium reached a Cloudflare interlock ("Sorry, you have
  been blocked — You are unable to access medium.com"; captured in `.phase6-medium.png`,
  now deleted) — the live article page refuses automated browsers.
- `WebFetch` on the live URL returns **HTTP 403 Forbidden** (server-side fetch also blocked).
Compensation (per step: "note the degraded mode and compensate with full-content fetch"):
Medium RSS `content:encoded` (`https://medium.com/feed/@glennstempeck`), re-fetched FRESH
this session — **HTTP 200, 45586 bytes**, article present — which returns the COMPLETE
published article HTML (more complete than a logged-out metered live view), plus direct
HTTP probes that every content image resource resolves.

### URL 1 — Medium (long-form)
`https://medium.com/@glennstempeck/three-things-my-agent-factory-couldnt-do-last-month-4c98d4a7c1b4`
Full-content source: feed `content:encoded`, title matched = "Three things my agent factory
couldn't do last month" (curly apostrophe rendered, not literal). HTML_LEN 7402.

1. **Literal `##`, `**`, backtick fences, or raw `[text](url)` visible? — none.**
   Scanned both the raw `content:encoded` HTML and the tag-stripped visible text:
   `**` = 0, `## … #{2,6}` heading markers = 0, ` ``` ` fences = 0, raw `[text](url)` = 0
   (all four, both views). The 3 command listings render as proper `<pre>` code blocks, the
   section headers as proper `<h3>` ("1. af plugin install …", "2. af gateway auth import …",
   "3. Relaunches that run on exactly the config you set", "Get it"), and the repo/doc links
   render as auto-linked plain URLs ("github.com/stempeck/agentfactory", "USING_PLUGINS.md",
   "USING_LITELLM.md") — not raw markdown link syntax. `&quot;`/`&#39;` in the raw feed are
   HTML entities that render as ordinary quotes/apostrophes, not visible markup.
2. **Missing, duplicated, or placeholder images? Stray alt-text paragraphs? — none.**
   Exactly 3 content `<figure>` images, all distinct Medium CDN assets, each probed live:
   - `1*w-EfxrBWJ7UDAsqnMtPAdg.png` → HTTP 200, image/png, 96954 bytes (plugin verify)
   - `1*PCTJhYh1Jtd2cekojqviOA.png` → HTTP 200, image/png, 87152 bytes (gateway auth)
   - `1*Lex0jvkOgYKT9CdIRBpu8g.png` → HTTP 200, image/png, 203324 bytes (models check)
   None broken/placeholder; no duplicate CDN IDs. `alt=""` on all three (empty — no stray
   alt-text paragraph rode along). The only other `<img>` is Medium's 1×1 `stat?event=…`
   tracking pixel, not content.
3. **Subtitle sitting as a body paragraph, or a stray leading `# `? — none.**
   The subtitle ("Install a third-party agent behind a trust boundary … the multi-agent
   orchestration CLI for Claude Code.") is the leading `<p><em>` italic — Medium's RSS
   standard export of the small-T subtitle field. Its distinctive phrase "behind a trust
   boundary" occurs EXACTLY ONCE in the whole content (so it is not also duplicated as a body
   paragraph). Content begins with the subtitle word ("Install…"), no literal leading `# `.
4. **Repo surface checks — all green.**
   - `gh repo view --json homepageUrl` = the Medium article URL above (exact match).
   - `gh api repos/stempeck/agentfactory --jq .topics` contains every Positioning discovery
     topic: `claude-code`, `ai-agents`, `multi-agent-systems`, `agentic-ai` (18 topics total).
   - `gh api "/search/repositories?q=topic:claude-code+agentfactory"` lists
     `stempeck/agentfactory` (indexed under the primary topic).

Findings this URL: NONE → no "search for / replace with" pairs to post on the publish issue
(#121); no post-publish edit required.

### URL 2 — LinkedIn (short-form)
Recorded **SKIP** (operator: "Medium only" this cycle, verified #121). No published page
exists, so no fetch/screenshot/markdown audit applies — nothing to verify. Recorded as
skipped (carried to the ledger/report as skipped, not published).

### Diagnostics removed before close
Deleted from the working tree (regenerable; stale copies fail the manifest gate):
`.phase6-verify.js`, `.phase6-feed-audit.js`, `.phase6-feed.xml`, `.phase6-medium.png`
(all untracked diagnostics), and the paste vehicle `.marketing/cycle-af-91fe0795-paste.html`
(regenerable from `cycle-af-91fe0795-medium.md`). Scratch fetch dump kept only under the
session scratchpad, outside the tree.

PHASE-6 VERDICT: PASS
