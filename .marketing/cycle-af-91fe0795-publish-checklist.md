<!-- PUBLISH CHECKLIST — cycle af-91fe0795. Tier B = YOU publish; I handle mechanics and NEVER post.
     Operator: Glenn Stempeck (Medium @glennstempeck, LinkedIn /in/glenn-stempeck/). Record each
     published URL (or SKIP) in the form at the bottom — on the notify issue or here — and I update
     the repo homepage, verify every page (Phase 6), and update the ledger (Phase 7). -->

# Publish checklist — cycle af-91fe0795

Flagship: **Plugins + ChatGPT-subscription gateway + relaunches that run on exactly the config you set** (#117).

Tier A is **merged to `main`** (PR #119, merge commit `82afc5a7`). Both drafts were approved
**READY**. Every link in the drafts points at merged `main`, so you can publish now. No release was
cut this cycle, so neither piece claims a version.

## Order — Medium FIRST, then LinkedIn

The short-form (LinkedIn) footer links to the long-form (Medium) article, so Medium has to exist
first. (This is the runbook's "each artifact feeds links to the next" principle; the literal
"LinkedIn-first" list predates the cross-link.)

### 1. Medium (long-form) — publish FIRST
- **Reference copy:** `.marketing/cycle-af-91fe0795-medium.md`
- **Paste vehicle:** `.marketing/cycle-af-91fe0795-paste.html` — READY (approved copy + all 3 figures
  embedded; no URL refresh needed, the repo links are stable). Medium ignores pasted markdown, so
  paste from the **HTML vehicle**, never the `.md`.

Mechanics:
1. Open `cycle-af-91fe0795-paste.html` in a browser → **Select-All → Copy → paste over the Medium
   story BODY**.
2. **Title:** `Three things my agent factory couldn't do last month.` (two alternates are in the
   draft's trailing comment if you prefer one).
3. **Subtitle** (small-T field — this is also the Google meta description): paste the italic line —
   *"Install a third-party agent behind a trust boundary, run the model gateway on a ChatGPT
   subscription, and make every relaunch use the config I actually set - agentfactory, the
   multi-agent orchestration CLI for Claude Code."*
4. **Topics (exactly 5):** `AI Agents`, `Claude`, `Artificial Intelligence`, `Software Engineering`,
   `Agentic AI`.
5. **Verify before you hit publish:** all 3 figures survived the paste; no stray `#` / `**` /
   backtick-fence or alt text rode along; the subtitle is in the subtitle field, not a body paragraph.
6. Publish → **record the Medium URL in the form below.**

### 2. LinkedIn (short-form) — publish SECOND
- **Source:** `.marketing/cycle-af-91fe0795-linkedin.md` — **plain text** (LinkedIn renders NO
  markdown; emphasis is CAPS by design, not asterisks).

Mechanics:
1. Copy the body between the `---` lines.
2. In the footer, replace `<MEDIUM ARTICLE URL — paste after publishing the Medium piece>` with the
   Medium URL from step 1 (lnkd.in shortening is fine).
3. Post → **record the LinkedIn URL in the form below.**

### 3. Repo homepage — I handle this (not you)
Once you record the Medium URL, I validate it against the runbook homepage-allowlist
(`https://medium.com`) and run `gh repo edit stempeck/agentfactory --homepage <url>`. If the host
isn't allowlisted I refuse and flag it instead of guessing.

## Form — record each published URL (or SKIP)

- **Medium URL:** https://medium.com/@glennstempeck/three-things-my-agent-factory-couldnt-do-last-month-4c98d4a7c1b4   (operator `stempeck`, verified on #121, 2026-10-06T00:21Z)
- **LinkedIn URL:** SKIP   (operator chose "Medium only" this cycle)
- **Notes:** Medium published; repo homepage updated to the article URL (host allowlist-validated against `https://medium.com`). LinkedIn skipped — record it in the ledger/report as skipped, not published.

When both are recorded (or SKIP), I verify every published page (Phase 6 — screenshots don't lie),
update the homepage, delete the regenerable paste/figure scaffolding from the tree, and update the
announced-features ledger + write the cycle report (Phase 7).
