# Cycle 1 Gate Log — stempeck/agentfactory

<!-- RECONSTRUCTED 2026-07-12: cycle 1 ran 2026-07-11/12 as an interactive Claude Code
session, before marketing-cycle.formula.toml existed. Every check below genuinely
happened; this log assembles the evidence post-hoc into the formula's artifact format.
Future cycles write this file live, section by section. -->

## GATE-1 (audit checklist)
1. STALE claims evidence-backed: yes — "Included Formulas table lists 5; `ls internal/cmd/install_formulas/` returns 19 files" (cycle-1-audit.md); ".gitignore instruction obsolete — `af install --init` writes `.git/info/exclude`" (verified against USING_AGENTFACTORY.md §3 by docs sub-agent).
2. NEW list absent from ledger: yes — no ledger existed (first cycle); entire git history was untold.
3. Snapshot current: yes — description "A Factory of Agents - for Enterprises", 14 topics, latestRelease null, homepage empty (cycle-1-audit.md, captured via `gh repo view --json`).
4. gh calls skipped on auth errors: yes-resolved — `PATCH /user` 404'd on missing `user` scope; mailed operator (reported in chat); operator ran `gh auth refresh -s user`; re-run succeeded.

GATE-1 VERDICT: PASS

## GATE-3 (claims verified against source)
1. Unproven commands in diff: none — docs pages verified per-command against `internal/cmd/` by dedicated verification pass; one invented gate type ("timer") caught and removed pre-commit.
2. Unrecounted counts: none — formula count corrected 5→19 from filesystem listing.
3. Unverified URLs: none — Medium/LinkedIn URLs web-verified before use; profile links fetched.
4. Unshipped promises outside Roadmap: none — roadmap items link real issues (#84, #85, #86).

GATE-3 VERDICT: PASS

## SELF-REVIEW
Findings: stale `.gitignore` instruction reintroduced by README rewrite — caught via docs
sub-agent contradiction report, fixed before PR. "Key directories" section mis-nested under
Web Console — fixed. Diff stat confirmed only intended files (8) in PR #87.

SELF-REVIEW VERDICT: PASS

## SELF-VERIFY (contract = visibility plan Tier A/B)
- Tier A: all stale claims fixed in PR #87 (merged, 7/7 CI checks green).
- Tier B: LinkedIn + Medium drafts operator-resolved as EDITED; Show HN/Reddit/awesome-lists resolved SKIP by operator decision.
- Tier law: zero external posts made by agent — operator pasted everything.
- Voice law: mechanics-only changes enumerated in-file after operator edits (4 changes on LinkedIn final; 5 grammar/gastown edits on Medium, operator-directed).

SELF-VERIFY VERDICT: PASS

## PHASE-6 (published-surface verification)
URL: https://medium.com/@glennstempeck/95-reliable-agents-give-you-86-reliable-workflows-b264170eb66c
1. Literal markdown artifacts: FOUND on first pass (raw `##`, `**`, `[text](url)`, fences — full-page screenshot; text-fetch had missed it) → rich-text repaste vehicle delivered → re-verified clean. Residual leading `# ` on first body line → search/replace pair delivered to operator.
2. Images: architecture diagram present after repaste; verified in screenshot.
3. Subtitle: initially a body paragraph with stray `# ` → operator fixed via small-T per instructions.
4. Repo checks: homepage → article URL (verified via `gh repo view --json homepageUrl`); repo indexed under `topic:claude-code` search (3 results incl. stempeck/agentfactory).
Diagnostics deleted: medium-article-full.jpeg, medium-recheck.jpeg, .playwright-mcp/ removed from working tree.

PHASE-6 VERDICT: PASS
