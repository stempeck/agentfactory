# Cycle af-a6b105d1 — Execution Log

## GATE-1 (Audit checklist — pre-selection)

**Item 1 — Every STALE claim backed by quoted grep/file:line evidence, not memory? YES.**
- S1 (CHANGELOG missing #124): `grep -niE '#124|converg' CHANGELOG.md` → `ABSENT - CHANGELOG has
  no #124 entry`; `## Unreleased` holds only #117 (CHANGELOG.md:6–83, confirmed by section-header
  grep). Evidence is command output, not memory.
- S2 (README surfaces economics only partially): README documents `af telemetry` at README.md:336–343;
  `grep -niE 'tokenomic' README.md` returns nothing, while `af tokenomics` is a real verb at
  `internal/cmd/tokenomics.go:23` (`Use: "tokenomics [on|off|status]"`).
- Negative claims (no false claim) backed by: README.md:237 "Twenty-six" vs `ls
  internal/cmd/install_formulas/*.toml | wc -l` = 26 and all 26 names matched; README.md:250
  "Thirteen" vs `ls -d .claude/skills/*/ | wc -l` = 13 and all 13 names matched.

**Item 2 — Every NEW feature absent from announced-ledger.md (checked, not assumed)? YES.**
- Bootstrap convergence (#124): `grep -niE '#124|converg|bootstrap re-run|digest-verified'
  announced-ledger.md` → `ABSENT - no ledger entry`.
- Token economics (#111): `grep -niE 'tokenomic|#111|token economic' announced-ledger.md` returns
  only backlog/release-gap lines — ledger:28 "release gap (token economics, #111) is still open for
  a future cycle" and ledger:36–37 "Top 3 for next cycle (ranked): (1) Token economics … (v0.4.0/#111;
  still untold, strongest". No published-venue table row claims either feature. Confirmed untold.

**Item 3 — Snapshot records description, topic count, latest release, homepage as they are RIGHT NOW? YES.**
- From live `gh repo view --json description,repositoryTopics,latestRelease,homepageUrl` run this cycle:
  description = "Multi-agent orchestration CLI for Claude Code — declarative TOML workflows, autonomous
  agents, context-compression recovery, inter-agent mail."; topics = **18**; latestRelease =
  **v0.3.0** (2026-08-23); homepage = the last-cycle Medium URL (…three-things-my-agent-factory-
  couldnt-do-last-month-4c98d4a7c1b4). All four recorded in the audit snapshot section.

**Item 4 — Any gh calls skipped due to auth/scope errors? NO.**
- `gh api user -q .login` → `stempeck` (matches runbook Operator). `gh repo view`, `gh api graphql`
  pinnedItems, `gh pr list`, `gh issue list` all returned data with no 404/scope hint. No call skipped.

GATE-1 VERDICT: PASS

## GATE-2 (Story approval) — HOLD IN EFFECT

**State (verified 2026-10-30, this session):** the story-approval HOLD is NOT satisfied.
The formula pointer sits at Step 10 because a prior session this cycle closed GATE-2
mechanically (`af done --phase-complete --gate af-4be3feb0`) while the `## Operator Decision`
form was still blank — the structural gate does not grep the form, so the pointer advanced,
but the HOLD is behavioral and remains open.

Evidence the operator has NOT decided:
- `.marketing/cycle-af-a6b105d1-story.md` `## Operator Decision` → `Decision: ______` (blank).
- GitHub issue #125 ("story pick awaiting approval") → state OPEN, zero comments
  (`gh issue view 125 --json state,comments`). No APPROVE / REORDER / END-CYCLE reply.
- `af mail inbox` → no operator decision relayed.

**Decision this session: DO NOT execute Step 10 (Tier A refresh).** Editing README/docs/
CHANGELOG now would require guessing the flagship among APPROVE (Token economics #111),
REORDER (Bootstrap convergence #124), or END-CYCLE (audit-only). That is precisely the choice
the two-tier law and runbook §2 Step 2 reserve for the operator. `af done` on Step 10 is
withheld: its exit criteria ("story features documented") cannot be legitimately met without
an approved story. Holding until the operator replies on issue #125.

## STEP-10 — WITHHELD pending GATE-2 resolution
No Tier A edits made. No commits. Awaiting operator decision on #125.
(SUPERSEDED 2026-10-31: GATE-2 resolved below; Step 10 executed against the REORDERed flagship.)

## STEP-10 — EXECUTED 2026-10-31 (Phase 3: Tier A refresh — security flagship #124)

Flagship = security story on bootstrap convergence (#124), per operator REORDER (GATE-2 above).

**Claims verified against source BEFORE writing (Claim Verification Map):**
- Convergence: `quickstart.sh` `_converge` (:251), `_apt_converge` (:568), `convergence.json` (:55),
  `CONVERGE_FAILURES` ledger (:57/:288), `--check` read-only drift (:17), digest checks
  `sha256sum`/`sha512sum --check --strict` (:377/:398/:818). Components list confirmed from #124
  commit ce8639e5 + apt-converge (ubuntu-security, nodejs) + hash-locked Python (`py/requirements.txt`).
- Daily advisory audit: `.github/advisory-audit.sh` (exec, present) + `.github/advisory-exceptions.json`.
- ADRs present: ADR-025 (plugin trust boundary), ADR-026 (most-secure-way-to-run-an-agent, operator-
  ratified 2026-10-25), ADR-027 (bootstrap convergence contract). ADR-026 literally states the
  operator's positioning — a ratified anchor, not marketing.
- `fable-secure`: formula present, "Fixed-checklist security-program coordinator"; scan-gate is
  OPTIONAL and degrades to "tool absent" (formula :338-344/:686-692).

**DefenseClaw — VERIFIED NOT SHIPPED (honesty law applied):** `USING_INTEGRATIONS.md:152` — "af does
not currently ship a DefenseClaw reference." Docs therefore describe the plugin/integration SURFACE
and fable-secure's OPTIONAL external-scanner scan-gate, and never claim a bundled scanner. Operator's
framing (DefenseClaw, "others to come") preserved verbatim in story.md; NOT asserted as shipped.

**STALE claims:**
- S1 (CHANGELOG missing #124) — FIXED. Added #124 entry to CHANGELOG ## Unreleased (security theme);
  existing #117 content preserved below a `---` divider.
- S2 (README missing `af tokenomics`) — FALSE AUDIT FINDING on re-verification. `af tokenomics on|off|
  status` IS already documented at README:344 and README:421 (added by the earlier token-economics
  cycle #114). Audit author read only the telemetry block 336-343 and missed line 344. No edit made.

**Tier A changes (committed, addddeb3):**
- CHANGELOG.md — #124 ## Unreleased entry.
- README.md — new "## Security & reproducible installs" section after "How it compares" (ADR-026
  posture; convergence + `--check`; daily CVE audit; trust-bounded plugins/ADR-025; fable-secure
  optional scan-gate). Hero tagline left unchanged (positioning carried by the new section; a hero
  reword is an operator-voice call, flagged for the deliver PR, not taken unilaterally).

**Issues filed:** #128 — "Ship a reference security-scanner integration for fable-secure's scan-gate"
(real gap: scan-gate point ships, scanner does not; matches operator's "others to come"). No
good-first-issue label (needs integration + security knowledge).

**Release decision (NOTED, not executed — belongs to deliver gate Step 17):** #124 is a meaningful
security boundary. Candidate: cut a release with the security milestone as headline, folding the
untagged v0.4.0 (#111 token economics) and #117 (plugins/gateway) into one tag, OR hold them. Operator
decides A/B/C at Step 17; releases happen only after merge, never on red main. Flagged here, not a surprise.

**Not committed:** `.agentfactory/agents/marketing-cycle/CLAUDE.md` (M) — `af prime` rewrote the agent's
own working-dir path to the worktree path; runtime churn, not a deliverable. Left unstaged.

STEP-10 exit criteria met: S1 fixed, S2 re-verified already-satisfied, security flagship documented,
changes committed. Proceeding.

## GATE-3 (Every public claim verified against source)

Scope: full branch diff `git diff origin/main...HEAD` (5 files, +445). Public FEATURE claims that
reach a user live in README.md and CHANGELOG.md; the three `.marketing/` files are committed-mode
cycle records whose technical references are verification CITATIONS (grep/file:line output produced
this session), not user-facing assertions. Checklist run against the added (`+`) lines:

**Item 1 — commands/flags/subcommands asserted but not proven this session: NONE.**
Every token in the README/CHANGELOG additions was proven against source this session:
- "`quickstart.sh --check`" / "read-only drift report" → `quickstart.sh:17` (`--check` comment),
  `_converge_check`; convergence mechanism `quickstart.sh:168` ("Every vendor channel converges
  through _converge: probe -> compare with the pin -> install").
- "`_converge` / `_apt_converge`, `CONVERGE_FAILURES` ledger" → `quickstart.sh:251`, `:568`, `:57`/`:288`.
- "`convergence.json` (`.runtime/bootstrap/convergence.json`)" → `quickstart.sh:55`.
- "`sha256sum` / `sha512sum --check --strict`" → `quickstart.sh:377`/`:398`/`:818`; integrity routing
  `:189` (`claude|gh|go|openobserve) integrity="sha256"`), `:190` (`codex) integrity="sha512"`).
- Component names (Claude Code, Codex, gh, Go, Node.js, OpenObserve, Playwright, Ubuntu security,
  hash-locked Python) → `OPENOBSERVE_VERSION` `:76`, `PLAYWRIGHT_VERSION` `:94`, `CODEX_VERSION` `:101`,
  `NODE_APT_VERSION` `:87`, `py/requirements.txt` (`--require-hashes`, ADR-026:23); also the #124
  merge-commit message (ce8639e5) enumerates the same set.
- "`.github/advisory-audit.sh` + `advisory-exceptions.json`" → both files present (ls, exec bit).
- "`fable-secure` … optional external-scanner scan-gate … records `tool absent` and continues" →
  `fable-secure.formula.toml:338-344`/`:686-692`; absence semantics `USING_INTEGRATIONS.md:152`.
- "`design-plan-impl` / `design-v7` / `fable-review` / `fable-increment` / `perfeval-agent`" → named
  verbatim in the #124 merge-commit message; all are shipped formulas/skills (README:242,243,266).
- ADR links `ADR-025/026/027` → files present in `docs/architecture/adrs/`.
- Anchor `#plugins--the-model-gateway` → matches existing header "### Plugins & the model gateway".

**Item 2 — counts asserted but not recounted: NONE.** The additions assert no numeric count; the
component set is enumerated, not counted. (README's existing "Twenty-six"/"Thirteen" counts are
unchanged by this diff and were re-verified at GATE-1: 26 TOMLs, 13 skill dirs, names matched.)

**Item 3 — URLs not fetched/constructed from a verified pattern: NONE.** README additions use only
repo-relative ADR paths (files verified present) and one in-page anchor (header verified). No external
URLs added to Tier A docs. (The `.marketing` records cite real GitHub URLs for this repo: issue #125
comment — the live operator decision, verified via `gh issue view`; issues #128 — created this session.)

**Item 4 — unshipped promises ("coming soon") outside a Roadmap section: NONE.** #124 is merged to
main (ce8639e5); CHANGELOG `## Unreleased` means merged-but-untagged, standard usage, not a promise.
DefenseClaw is NOT claimed as shipped anywhere in Tier A — README/CHANGELOG state "agentfactory ships
the integration point; the scanner is operator-supplied." The reference-scanner roadmap item is held
in GitHub issue #128, deliberately NOT asserted in the docs. Operator's "others to come" appears only
as a quoted operator statement inside the cycle record (story.md), flagged as claim-to-verify.

GATE-3 VERDICT: PASS

## STEP-12 — EXECUTED 2026-10-31 (Phase 4: Tier B drafts in the operator's voice)

**Voice recalibration (done first, not optional):** reread runbook §1 Voice + the PR #114 cuts +
the operator's canonical finals (cycle-af-50cdad6b-medium.md approved-as-written; -linkedin.md his
edited short version). Applied register: first person, spaced " - " hyphens (NO em dashes), "="
shorthand, CAPS for the key beat, question hooks, real output ("SHOW IT"). Applied the #114 cuts:
NO "proud of"/self-congratulatory emotion, NO non-feature set piece, did NOT reopen with the spent
"factory of AI agents" phrase. The operator's REORDER line ("factory of agents -> most secure way
to run multi-agent orchestration") is used as the honest positioning arc (their own words).

**Drafts written (DRAFT header + feedback form each):**
- `cycle-af-a6b105d1-medium.md` — long-form. Title "My agents run unattended with my credentials.
  So I made the install prove itself." Subtitle carries the Positioning phrases (multi-agent, Claude
  Code). Body: failure-mode hook -> convergence/what-shipped -> `--check` + daily CVE audit -> ADR-026
  positioning + plugins/fable-secure -> ONE inline honest limit -> repo link -> motto. No number in
  the title (avoided the must-land-a-number trap). One figure marked.
- `cycle-af-a6b105d1-linkedin.md` — short-form, ~150 words, PLAIN TEXT (no markdown; LinkedIn ships
  literal asterisks/backticks), question hook + close, two-link footer. Medium URL = publish-time
  placeholder (runbook Step 5 sequences Medium first).

**Figure CAPTURED (real product evidence, not prose):**
- `cycle-af-a6b105d1-screen-convergence.png` — the real `.runtime/bootstrap/convergence.json`
  (2026-10-30 bootstrap) rendered as a terminal table; every value verbatim from the file (10
  components, pin=installed, per-component integrity, result KEPT). Declared in approach.md Standing
  Assets. Reading this file for the figure is what surfaced the integrity-mechanism precision fix
  committed at bbdbd54f (Tier A now matches the convergence.json `integrity` fields exactly).
- Render path: Playwright MCP is pinned to the branded `chrome` channel (absent here), so used the
  bundled chromium directly (`~/.cache/ms-playwright/chromium-1248/.../chrome --headless --screenshot`,
  2x scale). Render-source HTML and the paste-verify render were written INSIDE the worktree and
  deleted (worktree-containment mail af-30db958e acked; scratchpad avoided).

**Paste vehicle rendered:** `cycle-af-a6b105d1-paste.html` — semantic h1/sub/h2/p/pre/code/figure/a,
figure embedded via relative src; verified by headless render that the image embeds and layout is
clean. Regenerable; deleted at Phase 6.

STEP-12 exit criteria met: both drafts have DRAFT header + decision form; the one marked figure is
captured (real convergence.json); paste.html renders the long-form draft with the figure embedded.
Proceeding to GATE 4 (HOLD for operator draft approval).

## GATE-4 (HOLD for operator draft approval) — HOLD IN EFFECT (opened 2026-10-31T02:4xZ)

Review routed to a PR per the runbook (§0: open the review PR EARLY at the draft-approval gate;
the operator will NOT open worktree files; take READY/EDITED/SKIP as PR replies or pushes).

- Branch pushed: `origin/marketing/cycle-af-a6b105d1` (new).
- **PR #129** opened (base main): carries Tier A (README/CHANGELOG security refresh) + both Tier B
  drafts presented INLINE, with the decision asks and the merge/release options (deferred to
  Step 17). https://github.com/stempeck/agentfactory/pull/129
- **Notify issue #130** filed as the ping, pointing at #129.
  https://github.com/stempeck/agentfactory/issues/130
- Both drafts carry a `## Operator Decision` form (READY/EDITED/SKIP); both blank = HOLD.

**HOLD.** An untouched form does not advance; this is a structural gate AND a behavioral HOLD.
Exit when every draft is resolved READY, EDITED-and-reconciled (mechanics-only, enumerated,
paste vehicle re-rendered, runbook Voice updated), or SKIP. On EDITED, their text is canonical.
Not running `af done` on Step 13. Will resume the instant the operator responds on #129 / #130.
Nothing further is actionable this session — the next event is an external operator decision.

## GATE-2 (Story approval) — RESOLVED 2026-10-31T02:20Z: REORDER
**Operator decided on issue #125.** Verified live (not taken from manager relay alone):
- `gh issue view 125` → single comment, author `stempeck`, authorAssociation OWNER, 2026-10-31T02:20:16Z.
  Author matches runbook operator (`gh api user -q .login` = `stempeck`). Manager mail af-041bdf86
  (claim: REORDER) corroborated by source — acked/deleted.
- Decision = **REORDER**: Token economics (#111) REJECTED ("played-out and old"); flagship swapped to
  the **security story on bootstrap convergence (#124)**.
- Operator's canonical framing: "factory of agents" → "the most secure way you can run multi-agent
  orchestration"; convergence + CVE-free module/version pinning + security-plugin surface (DefenseClaw
  named, "others to come"); security AND visibility beyond tokenomics.
- Verbatim decision recorded in story.md `## Operator Decision`; proposal flagship rebuilt (Token
  economics section marked SUPERSEDED, security story promoted).
- #125 commented (confirmation + verification-law note) and CLOSED (completed). Behavioral HOLD lifted.
HOLD lifted. Resuming Step 10 (Tier A refresh) around flagship #124 (security). Every public claim
still runs the Claim Verification Map; DefenseClaw/security-plugin link is verify-or-flag-as-roadmap,
never asserted from the operator's framing alone.

### Re-verification (2026-10-30, resumed session)
HOLD re-checked live, still in effect ~4.5h after the issue was filed:
- `gh issue view 125 --json state,comments` → state OPEN, comments `[]` (filed 03:46Z, no reply).
- `## Operator Decision` form → `Decision: ______` (still blank).
- `af mail inbox` → no unread; no operator decision relayed by any channel.
- Open-issue sweep shows no newer decision issue/PR.
Step 10 entry criteria ("Story approved, or END-CYCLE with stale fixes remaining") is NOT met.
`af done` on Step 10 remains withheld: its exit criteria ("story features documented") cannot be
honestly met without an approved flagship. Continuing to hold per the two-tier law.

### Re-verification (2026-10-30T12:09Z, resumed session) — HOLD still in effect, ~8.4h
- `gh issue view 125 --json state,comments` → state OPEN, comments `[]` (filed 03:46Z, no reply ~8.4h later).
- Open-issue sweep → no newer decision issue/PR (newest is #125 itself).
- `## Operator Decision` form (story.md:55) → `Decision: ______` (still blank).
- `af mail inbox` → no unread; no operator decision relayed.
- Progress watchdog fired ("no step progress"); this is the EXPECTED consequence of a behavioral
  HOLD whose structural gate already advanced. Not a crash.
- MANAGER NOTIFIED (first time this cycle): `af mail send manager/` org-record/FYI relay that the
  cycle is deliberately blocked on operator decision #125 (~8.4h overdue), no action required.
  Operator inbox stays GitHub #125 — deliberately NOT re-filing a duplicate to avoid noise.
Step 10 (Tier A refresh) stays withheld. Will resume the instant APPROVE / REORDER / END-CYCLE lands.

### Re-verification (2026-10-30T1411Z, resumed session) — HOLD still in effect, ~10.4h
- `gh issue view 125` → state OPEN, comments `[]`, updatedAt == createdAt (03:46Z): never touched.
- `## Operator Decision` form (story.md:55) → `Decision: ______` (still blank).
- `af mail inbox` → only a worktree-containment self-notice (acked); no operator decision relayed.
- Open-issue sweep → #125 is the newest; no decision issue/PR.
- Per prior-run memory: NOT re-filing #125, NOT re-escalating to manager (already done), NOT running
  `af done` on Step 10. Quiet hold; watchdog no-progress is the expected consequence, not a crash.

### Re-verification (2026-10-30T1611Z, resumed session) — HOLD still in effect, ~12.4h
- `gh issue view 125 --json state,comments,createdAt,updatedAt` → state OPEN, comments `[]`,
  updatedAt == createdAt (03:46:52Z): untouched ~12.4h after filing.
- Open-issue sweep → #125 is the newest; next is #102 (2026-08-09). No decision issue/PR.
- `## Operator Decision` form (story.md:55) → `Decision: ______` (still blank).
- `af mail inbox` → no unread; no operator decision relayed.
- Quiet hold maintained: NOT re-filing #125, NOT re-escalating (manager already FYI'd at 12:09Z),
  NOT running `af done` on Step 10. Step 10 (Tier A refresh) stays withheld. Resume on
  APPROVE / REORDER / END-CYCLE.

## GATE-4 re-verification (2026-10-31, session recycled post-compaction) — HOLD still in effect, ~1h
Session was recycled at a context-pressure compaction boundary (03:40Z). Re-primed to Step 13;
GATE-4 HOLD re-checked live (not taken from the pre-compaction log alone):
- `gh pr view 129` → state OPEN, `reviews: []`, no operator comment (only a CLAassistant bot CLA
  badge at 02:46Z); `updatedAt` 02:46:40Z == creation. No READY/EDITED/SKIP reply on the PR.
- `gh issue view 130` → state OPEN, `comments: []`, `updatedAt` 02:46:22Z == creation. Ping untouched.
- Branch push check (EDITED can arrive as a push): `git fetch` then `HEAD..origin/marketing/
  cycle-af-a6b105d1` → empty; local HEAD == origin tip 17e1b7b7. No operator edit pushed.
- Both `## Operator Decision` forms → `Decision: ______` (blank): medium.md:60, linkedin.md:25.
- Open issue/PR sweep → #130 is the newest issue, #129 the newest PR. No newer decision surface.
- `af mail inbox` → compaction self-notice only (acked/deleted); no operator decision relayed.
HOLD is ~1h old and freshly notified (PR #129 + issue #130 both filed 02:46Z); NOT re-filing a
duplicate, NOT re-escalating to manager this early, NOT running `af done` on Step 13. A blank form
does not advance — closing the structural gate now would repeat the GATE-2 mechanical-advance defect.
Next event is an external operator decision on #129 / #130; nothing further is actionable this session.

## GATE-4 (draft approval) — RESOLVED 2026-11-01T17:41Z: EDITED (both drafts reconciled)

Re-checked live after session restart (2026-11-01T17:51Z), not from the log:
- `gh pr view 129` → operator `stempeck` (OWNER) top-level comment 17:41:46Z: "EDITED - See the review
  comments ..." + five COMMENTED reviews 17:42–17:48Z, all inline on `.marketing/cycle-af-a6b105d1-medium.md`
  (review comment ids 4436682732 L15, 4436688999 L17, 4436691165 L23, 4436699349 L33, 4436704697 L49).
- No operator push: `origin/marketing/cycle-af-a6b105d1` == 17e1b7b7 (local was one log commit ahead).
- Issue #130 untouched (ping only).

**Decision scope.** One PR-level EDITED, not split per draft. Medium = EDITED explicitly (all inline
comments). LinkedIn = EDITED by the same PR-level keyword; its `quickstart.sh --check` clause is exactly
what the L33 comment rejects, so the same direction was applied and flagged on the PR for SKIP/revert.

**Operator direction (verbatim in PR #129; summarized):** emphasize security implications — did the
initial-build packages get updated or are they old, outdated and open to CVEs (L15); his trust/blueprint
paragraph (L17); `quickstart.sh` runs "(which also happens on `af install --agents`)" (L23); no
operator-facing `--check` — "If we have to know about commands to operate the factory, we've already
failed the human operator" (L33); cut the "worth saying plainly" limit — "focus on what people CAN DO"
(L49).

**Claim verification of the operator's new assertions (before they enter copy):**
- `af install --agents` runs quickstart.sh → internal/cmd/install.go:974-979 (agent-gen-all.sh, then
  runQuickstartScript); quickstart.sh:3445 says "the next ./quickstart.sh or af install --agents converges".
- Initial-build packages updated → quickstart.sh:636-660: every convergence run does `apt-get update`, then
  `--only-upgrade` of every installed package with a newer `<codename>-security` build (`_pending_security`
  quickstart.sh:450-464); failure → CONVERGE_FAILURES → exit 1 (quickstart.sh:3466-3469).
- "updating them any time a CVE that could impact us arises" → the daily `advisory-decay` CI job
  (.github/workflows/test.yml:41-42, 260-273; .github/advisory-audit.sh:2-6: both Python locks, Go
  toolchain and npm pins via OSV, vendor components via GitHub advisories; fails CI without a commit).
- "the container itself enforces good security practices" / "least number of and safest packages" →
  operator positioning (principle statements, his words), not a feature claim; adjacent mechanism
  verified above. Left verbatim.

**Reconciliation (voice law):**
- medium.md — 7 enumerated changes in-file: 3 operator inserts (mechanics: "package that were"→"packages
  that were", "CVE's"→"CVEs", "tie"→"time", stray backtick space), 3 cuts (--check section, "worth saying
  plainly" paragraph, and the "Honest about where that one stands" gotcha sentence under the same L49 rule),
  1 new agent sentence answering his hook question (marked strike-freely). Replaced text kept as reference.
- linkedin.md — 4 enumerated changes: cut `--check` clause; his hook question adapted to the reader
  ("your initial container build", "your agents vulnerable"); `af install --agents` + security-update
  clause; length trim of AGENT words only (179→158) per the short-form calibration.
- paste.html re-rendered; block-by-block text diff vs medium.md = 18/18 MATCH; headless chromium render
  confirms figure embeds and the --check block is gone (verify PNG written in-worktree, then deleted).
- approach.md — §1 "Voice calibration — 2026-11-01" + `## Voice` calibration update; Step 4 body template
  ("honest limitations" → "what the operator can DO") and §1 Honesty (limits go in records/PRs, not
  public copy) aligned to it.

GATE-4 exit criteria met: every draft resolved EDITED-and-reconciled.

## SELF-REVIEW (Step 14, 2026-11-01)

Reviewed `git diff origin/main...HEAD` (8 commits; README, CHANGELOG, `.marketing/` cycle artifacts).
Privacy mode = COMMITTED (approach.md `Privacy-Decision: COMMITTED`): `.marketing/` artifacts in the diff
alongside the Tier A doc changes is the expected shape.

Findings and fixes:
1. **README featured an operator-facing `--check` command — FIXED.** The new "Security & reproducible
   installs" section showed a `./quickstart.sh --check` code block for operators. That contradicts the
   operator's PR #129 direction on this same PR (L33: "no `operator` should need to know about this ...
   The `manager` should know"). Removed the code block and the `--check` clause; the paragraph now leads
   with what runs it (`quickstart.sh` or `af install --agents`) and adds the security implication he asked
   for (initial-build packages get every pending Ubuntu security update). `--check` stays documented
   where the manager needs it: USING_AGENTFACTORY.md:781 and ADR-027:51. The CHANGELOG `--check` bullet is
   KEPT: it is a factual record of a behavior change, not an operator instruction.
   Claim re-verification for the new README text (GATE-3 discipline): `af install --agents` runs
   quickstart.sh = internal/cmd/install.go:974-979 and USING_AGENTFACTORY.md:781 ("Every run of
   `./quickstart.sh`, `af install --agents` or `af plugin install` converges ..."); pending Ubuntu
   security updates for installed packages = quickstart.sh:636-660 (`_pending_security` :450-464).
2. **Relative links — PASS.** ADR-025/026/027 paths exist; `#plugins--the-model-gateway` matches
   README `### Plugins & the model gateway` (README.md:465).
3. **Stale or unverified claims — PASS.** No DefenseClaw or bundled-scanner claim in Tier A (scanner is
   operator-supplied; roadmap gap held in #128). No TODO/FIXME in the diff.
4. **Cruft — PASS.** The paste-verify render PNG was deleted after use; there are no untracked files.
   The only `-screen-*.png` this cycle is declared in approach.md Standing Assets. `paste.html` is the
   regenerable vehicle, deleted at Phase 6.
5. **Markdown host rendering — PASS.** LinkedIn paste block has no `*` or backticks (plain text). The
   only em dashes in the drafts sit in agent placeholders that are never pasted ([FIGURE] marker, Medium
   URL placeholder); paste.html body has 0. Medium md vs paste.html = 18/18 blocks MATCH.
6. **Unrelated working-tree change — EXCLUDED.** `.agentfactory/agents/marketing-cycle/CLAUDE.md` was
   modified before this session (factory-rendered identity file, present in the session-start git
   status). It is not a cycle change and is not committed.

SELF-REVIEW VERDICT: PASS

## TESTS (Step 15, 2026-11-01)
- `go build ./...` → clean (runbook Build command).
- `make test` → exit 0; 22 packages `ok`, 0 `FAIL` (root module unit tier; never test-integration locally).
- PR #129 CI at head 0e2fb81f (in progress at step close): advisory-decay, regen, supply-chain-lint,
  toml-conformance, web-unit = pass; unit, integration = pending. license/cla = pending — the same
  pre-existing commit-identity state as when the PR was opened (CLAassistant comment 2026-10-31);
  it is not caused by this cycle's content. CI is re-checked green at Step 17 (deliver) before any merge ask.

## SELF-VERIFY (Step 16, 2026-11-01) — outputs vs the contract

Contract source: no design doc (`.designs/` absent in this checkout) → the approved runbook
`.marketing/approach.md` (`Runbook-Decision: APPROVED`, approach.md:299), as updated this cycle with
the operator's 2026-11-01 voice calibration.

1. **Tier A: every STALE audit claim fixed; no unverified claim entered — PASS.**
   - S1 (CHANGELOG missing #124) → fixed: CHANGELOG `## Unreleased` #124 entry (addddeb3, precision
     bbdbd54f).
   - S2 (README missing `af tokenomics`) → a false audit finding, re-verified at Step 10 (already at
     README:344/421); no change was needed and none was made.
   - Every Tier A claim was verified at GATE-3 (PASS). The one post-GATE-3 Tier A edit (0e2fb81f, README
     `af install --agents` + security-update sentence; `--check` block removed) was re-verified against
     source in SELF-REVIEW #1: install.go:974-979, USING_AGENTFACTORY.md:781, quickstart.sh:636-660.
     No DefenseClaw or bundled-scanner claim (gap held in #128).
2. **Tier B: every draft operator-resolved; none self-approved — PASS.**
   - medium.md → `Decision: EDITED` — operator `stempeck` (OWNER), PR #129 comment 2026-11-01T17:41:46Z +
     five inline review comments on the file.
   - linkedin.md → `Decision: EDITED`, from the same PR-level keyword. DEVIATION (documented): the
     operator's EDITED was not split per draft and no inline comment landed on this file. I applied the
     same direction, enumerated the changes, and asked him on PR #129 to SKIP or revert before posting.
     This is the operator's keyword applied, not a self-approval: the agent did not write
     READY/EDITED/SKIP on its own.
3. **Tier law: zero external posts by the agent at any step — PASS.**
   - Every write this cycle went to the operator-owned repo `stempeck/agentfactory` (Tier A): issues
     #125 (story ping, closed), #128 (real gap), #130 (draft ping, closed), PR #129, PR comments and body
     edits, and the branch pushes.
   - No LinkedIn, Medium, HN or Reddit action; no PR to a repo the operator does not own; nothing
     scheduled.
4. **Voice law: EDITED drafts enumerate every agent change in-file — PASS.**
   - medium.md: 7 numbered changes (3 operator inserts with each mechanics fix named, 3 cuts, 1 new agent
     sentence marked "strike freely"), with the replaced text kept as a reference block.
   - linkedin.md: 4 numbered changes plus a reference block.
   - The same lists were delivered on PR #129 (comment 6561717470), and the runbook Voice section is
     updated (calibration 2026-11-01).
   - `paste.html` was re-rendered; the block-by-block text diff against medium.md = 18/18 MATCH.
5. **Privacy mode matches the diff — PASS.** `Privacy-Decision: COMMITTED`; `git diff --stat
   origin/main...HEAD` shows the `.marketing/` cycle artifacts committed alongside README/CHANGELOG, as
   committed mode requires.

Deviations: only #2 (LinkedIn decision inherited from the PR-level EDITED), justified and surfaced to the
operator above. None blocks.

SELF-VERIFY VERDICT: PASS

## STEP-17 DELIVER — HOLD IN EFFECT (2026-11-01T18:1xZ) — awaiting operator merge + release decision
- No new PR was opened: PR #129 (opened at GATE 4 per runbook §0) is the Tier A PR. Branch pushed; head 1442f3bd.
- CI green on 1442f3bd: run 41334538160, 7/7 jobs success. `license/cla` pending (pre-existing commit
  identity; not content). PR is mergeable with no conflicts and blocked only by branch protection.
- main health: scheduled `test` run on 2035611f succeeded 2026-11-01T05:21Z.
- PR comment 6561840688: CI status, main health, the post-review README change (0e2fb81f), and the
  release ask A/B/C. Recommendation: (A) a CHANGELOG-fold PR → a single v0.4.0 led by the security
  milestone, tagged only on green main after that merges. No reply is treated as C (no release).
- Manager org-record mail sent.
HOLD: no merge by the agent (ever), no publish support against an unmerged branch, no tag without an
explicit A/B. Resume when the PR #129 state is MERGED.

## STEP-17 DELIVER — PR #129 MERGED; release decision YES (A); CHANGELOG fold #131 — HOLD for merge
- PR #129 MERGED 2026-11-01T19:24:56Z by `stempeck`, squash 0bf9f381. Post-merge main CI green.
- Release decision: the operator, in session after the merge, said "merged. cut the release." → YES, option (A)
  as recommended on #129. Recorded on #129 (comment 6562762564).
- (A) prerequisite: CHANGELOG-only PR #131 (branch marketing/cycle-af-a6b105d1-release-v0.4.0, 48fedf0b) folds
  `## Unreleased` (#124, #117) and the never-tagged `## v0.4.0 — 2026-09-17` (#111) into one
  `## v0.4.0 — 2026-11-01`, security first. Adds #127 (merged after the audit) to the also-list, scoped
  and verified: only `.agentfactory/` config plus one test changed; `internal/cmd/install.go` untouched, so
  shipped defaults are unchanged. No `## Unreleased` heading left (matches the v0.3.0 cut). #131 CI green
  7/7 (run 41341838698).
- Themed release notes are drafted (agent dir, git-ignored). Tag v0.4.0 on green main ONLY after #131 merges.
- Worktree containment: scratchpad writes under /tmp tripped WORKTREE_CONTAINMENT (af-eac122ab, acked). Temp
  files moved into the git-ignored agent dir; scratchpad cleared.
HOLD: step 17 exit = "PR merged; release cut iff YES". Waiting on the operator's merge of #131, then tag.
- Re-checked 2026-11-01T20:28Z after the session recycle: #131 still OPEN (head 48fedf0b), CI 7/7 green, no reviews or comments from the operator. origin/main is still 0bf9f381. HOLD continues; no tag yet.

## STEP-17 DELIVER — RESOLVED: #131 MERGED; v0.4.0 RELEASED (2026-11-01)
- PR #131 MERGED 2026-11-01T21:02:48Z by `stempeck`, squash eab2b02c.
- Release cut (decision YES, option A): **v0.4.0** published 2026-11-01T21:04:31Z, Latest, tag -> eab2b02c.
  https://github.com/stempeck/agentfactory/releases/tag/v0.4.0
  Title: "v0.4.0 — security & reproducible installs, plugins & a ChatGPT-subscription gateway, token economics".
  Notes = agent-dir release-notes-v0.4.0.md. Before publishing, every claim was checked against main's CHANGELOG v0.4.0
  section. One fix: the plugins bullet had attached "pinned commit / read-only snapshot" to agent installs; per
  CHANGELOG that describes integrations, so it was reworded. The `blob/v0.4.0/CHANGELOG.md` link returns 200.
- Green-main condition: the operator asked why the agent was waiting on CI after the merge. Main's tree at eab2b02c
  (1e3b7c46) is byte-identical to the PR #131 head tree at 48fedf0b, which passed 7/7 CI (run 41341838698), so the
  post-merge main run (41349938376) re-tests the same code. The tag was cut without waiting for it; that run's
  outcome is recorded below when it lands.
Exit criteria met: PR merged; release cut because the decision was YES.

## STEP-18 PHASE 5 PUBLISH SUPPORT — HOLD IN EFFECT (2026-11-01) — awaiting operator URLs
- Checklist written: `.marketing/cycle-af-a6b105d1-publish-checklist.md`. Order: Medium FIRST, then LinkedIn, because
  the LinkedIn footer links to the article. Title, subtitle and the 5 runbook topics are named; homepage is the agent's job.
- Paste vehicle `cycle-af-a6b105d1-paste.html`: no URL refresh needed (its only link is the repo). Drafts and vehicle
  are byte-identical to main (`git diff origin/main` clean). Vehicle code spans render as `<code>`, with 0 literal backticks.
- Correction caught while writing: the vehicle's own note says to copy only what is BELOW the dashed line (the title and
  subtitle go in their own fields). The step text's generic "select-all" would have pasted the note, title and subtitle
  into the Medium body. The checklist follows the vehicle's instruction.
- Notify issue #132 opened (gh). HOLD: the gate resolves when the Medium and LinkedIn URLs (or SKIP) are recorded. Then
  validate the Medium host against `### homepage-allowlist` → `gh repo edit --homepage`, close #132, and run Phase 6.
- Post-merge main CI run 41349938376 (eab2b02c, the v0.4.0 tag): SUCCESS, 7/7 jobs green.

## STEP-18 PHASE 5 — RESOLVED: SKIP both (2026-11-01)
- The operator, in session: "Skip, close it out. Make sure we have a release package published. I'm not posting to social
  media for now, no one reads it." → Medium = SKIP, LinkedIn = SKIP, recorded in the checklist form. #132 commented and closed.
- Homepage: unchanged. The exit criterion "homepage updated if an article shipped" does not apply (no article).
- Release package: v0.4.0 is published (not a draft or prerelease) and is `releases/latest`. The source tarball returns 200 and
  contains quickstart.sh, go.mod and CHANGELOG.md. No release in this repo has uploaded assets (v0.1.0-v0.3.0 have none, and
  there is no release workflow); install is clone + quickstart.sh, so the source archive is the package.

## RELEASE PACKAGE — v0.4.0 binaries attached (2026-11-01, operator-directed)
- The operator: "i need a release" / "we can't put in all this effort ... and not publish a release of the current main build".
  No earlier release had uploaded assets and there is no release workflow, so this is the first binary package.
- Built from `git archive v0.4.0` (eab2b02c == current main) with Go 1.26.10 (the CI-pinned toolchain), CGO_ENABLED=0,
  -trimpath, -buildvcs=false, `-s -w`. Version/Commit/BuildTime ldflags = v0.4.0 / eab2b02c / the commit time
  2026-11-01T21:02:48Z. The Makefile's `-X main.sourceRoot=$(CURDIR)` is deliberately OMITTED: it is a fallback lookup
  for py/ (after the factory root, before AF_SOURCE_ROOT), and baking in the build machine's path would ship a dead
  local path. Each of the 4 binaries contains 0 occurrences of /home/dev.
- Assets: agentfactory_v0.4.0_linux_{amd64,arm64}.tar.gz (af, webui, LICENSE, CHANGELOG.md; deterministic tar: sorted,
  uid/gid 0, mtime = commit time, gzip -n) + SHA256SUMS. Release notes gained a "Downloads" section (verify command,
  and the supported install is still clone + quickstart.sh, with how `af` finds py/).
- Verified from GitHub: GitHub's asset digests equal SHA256SUMS. A fresh `gh release download` passes `sha256sum -c`,
  the arm64 `af --version` prints `af version v0.4.0`, and `config fingerprint` equals the installed af
  (fee12aa8...). The release page lists all 3 assets. The build and verify dirs were in the git-ignored agent dir.

## PHASE-6 (Step 19, 2026-11-01) — verify every published page
Tier B URLs: none. Medium = SKIP and LinkedIn = SKIP (operator, in session), so no social page exists to verify. The one
surface published this cycle is the GitHub release, so it is verified against the same checklist. Mode: full
(screenshot via bundled headless chromium per the runbook capability, plus a full-content fetch of the rendered body).

URL: https://github.com/stempeck/agentfactory/releases/tag/v0.4.0
1. Literal `##` / `**` / fences / raw `[text](url)`: none. Rendered body_html (API, html+json), code spans excluded:
   literal # 0, ** 0, fences 0, raw links 0, stray backticks 0. 7 `<h3>` sections render as headings. Screenshot confirms.
2. Missing, duplicated or placeholder images, stray alt text: none. The notes carry no images. Screenshot shows 5 assets
   listed once each: the amd64 and arm64 tar.gz, SHA256SUMS, and the 2 source archives.
3. Subtitle as body paragraph / stray leading `# `: none. The title is in the release title field with the Latest badge,
   and the body opens with the summary paragraph.
4. Repo checks: homepageUrl is unchanged (last cycle's Medium article; no article shipped this cycle, so per the step-18
   exit criterion it is not changed). curl gets 403 (Medium's Cloudflare bot block, a known gotcha), but the article is
   live in the RSS feed (1 match). Topics contain all 4 runbook primary topics (claude-code, ai-agents,
   multi-agent-systems, agentic-ai), and topic search lists stempeck/agentfactory under each of the 4.
   Release links resolve: #111/#117/#124/#127 and blob/v0.4.0/CHANGELOG.md (200).
Findings requiring operator edits: none.
Diagnostics deleted: the screenshot and body dump (agent dir phase6/). Paste vehicle `cycle-af-a6b105d1-paste.html`
removed (`git rm`; it was committed to main via #129, so the removal lands in the close-out PR).

PHASE-6 VERDICT: PASS
