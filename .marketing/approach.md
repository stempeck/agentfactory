# approach.md — Continuous Marketing Runbook for agentfactory

**Hand-off guide.** You are taking over the marketing cycle for agentfactory. This document is
the complete process: exact commands, decision criteria, platform traps, and what "done" looks
like. It was written by the engineer who ran the first cycle (2026-07-11/12); every warning in
here is a scar, not a hypothetical. Follow the steps in order. When a step says HOLD, you hold.

## 0. Standing context (read once, verify every cycle)

- **Operator:** Glenn Stempeck. GitHub `stempeck` (active; `gstempeck` is his work-connected
  account — never touch it, never cross-link from it). Medium `@glennstempeck`. LinkedIn
  `/in/glenn-stempeck/`.
- **Repo:** github.com/stempeck/agentfactory. Branch protection on main. You open PRs;
  ONLY the operator merges them. Never merge a PR, never push to main — no exceptions,
  regardless of urgency or CI status.
- **Auth preflight:** `gh api user -q .login` must return `stempeck`. Repo edits need `repo`
  scope; profile edits need `user` scope. If a call 404s with a scope hint, tell the operator to
  run `gh auth refresh -h github.com -s <scope>` — you cannot do the browser dance for him.
- **This directory (`.marketing/`) is committed to the repo** — committed mode, detected
  2026-07-12 by the formula's `git check-ignore` test. The cycle records must survive
  container rebuilds and travel with clones. This repo is public, so everything in here
  is public, drafts included.
- **No API exists** for: profile pinned items (GraphQL schema has no mutation — verified by
  introspection), social-preview upload. Those are operator-clicks; put them in the report, not
  in your todo loop.

## 1. THE LAW: two tiers, one voice

**Tier A (you act):** anything on GitHub surfaces Glenn owns — README, docs, CHANGELOG,
releases, issues, topics, description, homepage, profile-repo README. Ship it autonomously via
branch → PR → CI → merge.

**Tier B (you draft, Glenn publishes):** anything appearing under Glenn's name off-GitHub —
LinkedIn, Medium, HN, Reddit, PRs to repos he doesn't own. You produce paste-ready drafts and
mechanics support. You never post, never schedule, never "just this once."

**Voice (the rule that got relearned twice):** Glenn's words are canonical. On text he wrote,
you fix ONLY spelling, broken grammar, and literal-markdown rendering — then enumerate every
change you made so he can audit you. You do NOT smooth his cadence. Concretely, his register:
spaced hyphens " - " (never em dashes), "=" shorthand, CAPS for emphasis, parenthetical asides,
and-chained long sentences, question hooks, Q&A rhythm ("Crash? It resumes."), closers like
"Happy to help" and "Learn it, Live it, Share it!". Polished parallel triads and tidy aphorisms
are DEFECTS in his content — they read as AI and undercut the "I built this" claim. When he
edits your draft, his edit becomes the new calibration source. Before drafting anything, reread
his latest published finals (cycle-1-linkedin.md top block, the live Medium article).

**Honesty:** verify every URL, flag, command, and number against code or a live fetch before it
enters content; omit and flag what you can't verify. State limitations plainly. Be precise, not
harsh, about other tools ("no workflow-aware crash recovery", not "no crash recovery" — Claude
Code resumes sessions; it doesn't resume your place in a procedure). Never manufacture activity.
Skip venues whose bar isn't met (awesome-go wants coverage badges) instead of burning the
one-shot submission.

## 2. The cycle

Trigger: each release tag, or monthly, whichever first. A cycle that finds nothing story-worthy
ends after Step 2 with an audit report — never invent a story.

### Step 1 — Audit & delta detection

1. Snapshot: `gh repo view stempeck/agentfactory --json description,repositoryTopics,latestRelease,homepageUrl`
   and `gh api graphql` for pinnedItems. Diff against last cycle's audit file.
2. Mine development: `git log <last-cycle-tag>..HEAD --oneline` + merged PR titles. Read
   CHANGELOG.md for what's already recorded.
3. Diff against `announced-ledger.md` (feature → where told → date → URL). Everything in the log
   but not the ledger is the **untold list** — expect it to be long; that's the backlog, not the
   assignment.
4. Claim-verification pass on public docs: for every command/flag/count the README and docs
   assert, grep the source (`internal/cmd/`, `internal/cmd/install_formulas/`). Stale claims go
   on the Tier A fix list. (First cycle caught: formula table said 5, code ships 19; a manual
   .gitignore step the installer already automates.)
5. Write `cycle-NN-audit.md` in this directory: new / stale / story-worthy.

**Exit:** audit file exists; untold list enumerated; every stale claim has a fix note.

### Step 2 — Select the story (HOLD for operator)

Rank untold features by: (a) user pain it kills, (b) 60-second demonstrability, (c) fit to
target phrases (multi-agent orchestration, Claude Code, autonomous agents), (d) audience reach.
Pick ONE flagship + supporting refresh items. Present pick and rationale to Glenn. **HOLD until
he confirms or reorders — his reorder is final.** If nothing ranks: say so, write the report,
end the cycle.

### Step 3 — Tier A refresh

1. Branch (never commit to main directly). Update README/docs/CHANGELOG for the selected
   features; verify each command against source *before* writing it.
2. Release only at a meaningful boundary: semver tag + themed notes
   (`gh release create vX.Y.Z --target main --title ... --notes ...`).
3. File real issues from real gaps (docs/architecture/gaps.md is a legitimate source); label
   good-first-issue only when genuinely scoped.
4. `gh pr create`, then `gh pr checks <n> --watch`; when green, notify the operator on the
   PR and HOLD — the operator merges, never you.
5. Confirm standing surface: topics ≥ the core set, badges render, description/homepage intact
   (the monthly visibility-health workflow alarms on drift; don't duplicate it, just don't
   break it).

**Exit:** PR merged, CI green, release (if cut) live, no stale claim from Step 1 survives.

### Step 4 — Tier B drafting (voice-critical)

1. Recalibrate: reread Glenn's newest published finals FIRST. Not optional.
2. **Medium article** (flagship story): title sells the destination or forces a double-take
   with a number ("95% reliable agents give you 86% reliable workflows"); if the title claims a
   number, the body must land that exact number. Subtitle carries the search phrases (~140
   chars before feed truncation). Body: failure-mode hook → what shipped → honest limitations →
   repo link → motto close. Export any diagram as PNG: screenshot GitHub's Mermaid render
   (watch for label collisions in the render — fix the diagram first, don't ship an occluded
   label).
3. **LinkedIn post**: 150–200 words, question hook, PLAIN TEXT — LinkedIn renders no markdown;
   any `*emphasis*` ships as literal asterisks. Blank lines between short paragraphs. Ends with
   a genuine question + "Happy to help."
4. Every draft starts with an HTML-comment DRAFT header and lands in this directory. **HOLD:**
   Glenn edits; incorporate his edits verbatim (mechanics-only fixes, enumerated); his version
   supersedes yours in the file, with your prior text preserved below as reference.

**Exit:** drafts marked ready by Glenn, not by you.

### Step 5 — Publish support (Glenn clicks, you handle mechanics)

Sequencing matters — each artifact feeds links to the next:

1. **LinkedIn post** goes out (repo link; lnkd.in shortening is his habit — fine).
2. **Medium**: Medium IGNORES pasted markdown — raw `##`/`**`/fences will ship as visible
   garbage. Provide the rich-text vehicle: render the article to a local HTML file (semantic
   h2/strong/a/pre/code, image via relative src), `open` it, have him select-all → copy → paste
   over the story body. Then: subtitle via the small-T style (it's also Google's meta
   description), exactly 5 topics (formula: 2 precise [AI Agents, Claude] + 1 giant reach
   [Artificial Intelligence] + 2 professional framing [Software Engineering, Agentic AI]),
   verify the image survived the paste, and check no `# ` or alt-text strays rode along.
3. **Repo homepage** → the live article URL: `gh repo edit stempeck/agentfactory --homepage <url>`.
4. Post-publish edits keep the same URL — deliver fixes as exact "search for / replace with"
   pairs he can run in Medium's editor.

### Step 6 — Verify the published surface

Fetch AND screenshot every published page (text-fetch alone missed the raw-markdown disaster
once — screenshots don't lie). Look for: literal `##`/`**`, raw `[text](url)`, backtick fences
as text, missing/duplicated images, stray alt text, subtitle sitting as a body paragraph.
Confirm via API: metadata intact, repo indexed under its topics
(`gh api '/search/repositories?q=topic:claude-code+agentfactory'`). Delete your screenshot
artifacts from the repo tree afterward.

### Step 7 — Ledger & report

Update `announced-ledger.md` (feature → venue → date → URL). Write the cycle report: shipped
autonomously / published by Glenn / skipped and why / top 3 candidates for next cycle. Queue the
+2-week search check (`"Glenn Stempeck" github`, topic pages).

**Cycle is done when:** report written, ledger current, no HOLD outstanding, no diagnostic
files left in the working tree.

## 3. Known failure modes (all hit once already)

| Symptom | Cause | Fix |
|---|---|---|
| `gh api PATCH /user` → 404 + scope hint | token lacks `user` scope | operator runs `gh auth refresh -s user`; you re-run |
| PR "base branch policy prohibits merge" | branch protection | `gh pr merge --squash --admin` after green CI |
| SVG→PNG renders square/zoomed | qlmanage thumbnails are square | wrap art in a square canvas SVG, render, `sips -c` center-crop |
| Published Medium page shows `##`, `**`, raw links | markdown pasted as plain text | rich-text HTML vehicle, repaste body |
| Draft file lost content after your edit | this dir has no git history | additive replacement only; keep superseded text as reference |
| Edit tool rejects: "file modified since read" | Glenn edited while you worked | re-read, apply against HIS text; his edits win |
| Your rewrite "sounds like AI" | you smoothed his cadence | revert to his words; mechanics-only; enumerate changes |

## Machine-read sections (marketing-cycle v2 compatibility)

The generic formula greps the exact headers below. Content mirrors §0–§2 above.

## Operator
Glenn Stempeck — GitHub `stempeck`. Medium `@glennstempeck`. LinkedIn `/in/glenn-stempeck/`.

## Repository
stempeck/agentfactory — multi-agent orchestration CLI for Claude Code. AGPL-3.0.

## Positioning
Target phrases: multi-agent orchestration, Claude Code, autonomous agents, agentic workflows.
Primary discovery topics: claude-code, ai-agents, multi-agent-systems, agentic-ai.
Audience: Claude Code practitioners; hiring evaluators reading the operator's name.

## Channels
Medium (long-form; ignores pasted markdown — use the rich-text HTML vehicle; subtitle via
small-T; 5 topics: 2 precise + 1 giant + 2 professional). LinkedIn (short-form; renders no
markdown — plain text only; lnkd.in links are fine).

### homepage-allowlist
https://medium.com

## Voice
See §1 "The voice law" above — spaced hyphens " - ", "=" shorthand, CAPS emphasis,
parenthetical asides, and-chained sentences, question hooks, Q&A rhythm, "Happy to help",
"Learn it, Live it, Share it!". Calibration sources: cycle-1-linkedin.md top block,
the live Medium article.

## Claim Verification Map
Commands/flags: grep `internal/cmd/`. Shipped formulas: count `internal/cmd/install_formulas/`.
Skills: `.claude/skills/`. Test command: `make test` (never `make test-integration` locally).
Build: `go build ./...`.

## Verification Capability
Screenshots available (Playwright browser tools on this Mac).

## Privacy Mode
Explicit operator choice (2026-07-12): artifacts are committed and ride cycle PRs. This
repo is public, so everything in `.marketing/` is public, drafts included.
Privacy-Decision: COMMITTED

## Standing Assets
Files here that no formula step produces, declared deliberately (the cleanup manifest
gate fails any undeclared file):
- social-preview.png (committed — repo social-preview image source)
- social-preview.svg (committed — editable source for the above)
- architecture-diagram.png (committed — cycle-1 diagram export, predates cycle-N-diagram.png naming)

## Sign-off
Blank the line below to force re-approval of this runbook before the next cycle.
Runbook-Decision: APPROVED
