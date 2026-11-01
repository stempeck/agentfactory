<!-- PUBLISH CHECKLIST — cycle af-a6b105d1. Tier B = YOU publish; I handle mechanics and NEVER post.
     Operator: Glenn Stempeck (Medium @glennstempeck, LinkedIn /in/glenn-stempeck/). Record each
     published URL (or SKIP) in the form at the bottom — on the notify issue or here — and I update
     the repo homepage, verify every page (Phase 6), and update the ledger (Phase 7). -->

# Publish checklist — cycle af-a6b105d1

Flagship: **Security — bootstrap convergence: pinned, integrity-verified installs that fail the build
rather than run drifted** (#124).

Tier A is **merged to `main`** (PR #129, `0bf9f381`; CHANGELOG fold #131, `eab2b02c`) and **v0.4.0 is
released** (https://github.com/stempeck/agentfactory/releases/tag/v0.4.0). Both drafts were resolved
**EDITED** on PR #129 and reconciled to your review. They are byte-identical on `main`, and the only
link they carry is the repo itself, so you can publish now. Neither piece names a version. If you want
to mention v0.4.0, that's your call; it is not in the copy you approved.

## Order — Medium FIRST, then LinkedIn

The LinkedIn footer links to the Medium article, so Medium has to exist first.

### 1. Medium (long-form) — publish FIRST
- **Reference copy:** `.marketing/cycle-af-a6b105d1-medium.md`
- **Paste vehicle:** `.marketing/cycle-af-a6b105d1-paste.html` — READY. It holds the approved copy and
  the convergence figure (`cycle-af-a6b105d1-screen-convergence.png`, same folder), and its 18 blocks
  match the `.md` one for one. No URL refresh is needed: the only link is the repo. Medium ignores
  pasted markdown, so paste from the **HTML vehicle**, never the `.md`.

Mechanics:
1. Open `cycle-af-a6b105d1-paste.html` in a browser, keeping the PNG in the same folder → **select
   everything BELOW the dashed line → Copy → paste over the Medium story BODY**. Not Select-All: the
   note, title and subtitle above the line go in their own fields (steps 2–3), not the body.
2. **Title:** `My agents run unattended with my credentials. So I made the install prove itself.`
   (two alternates are in the draft's trailing comment if you prefer one).
3. **Subtitle** (small-T field — this is also the Google meta description): paste the italic line —
   *"Reproducible, integrity-verified installs for a multi-agent Claude Code factory - converge to the
   pins or fail the build, and prove exactly what ran."*
4. **Topics (exactly 5):** `AI Agents`, `Claude`, `Artificial Intelligence`, `Software Engineering`,
   `Agentic AI`.
5. **Verify before you hit publish:**
   - the convergence figure survived the paste;
   - no stray `#`, `**`, backtick fence or alt text came along;
   - the inline code (`quickstart.sh`, `af install --agents`, `convergence.json`, `fable-secure`) shows
     as code, not as literal backticks;
   - the subtitle is in the subtitle field, not a body paragraph.
6. Publish → **record the Medium URL in the form below.**

### 2. LinkedIn (short-form) — publish SECOND
- **Source:** `.marketing/cycle-af-a6b105d1-linkedin.md` — **plain text** (LinkedIn renders NO
  markdown; emphasis is CAPS by design, not asterisks).
- Reminder from PR #129: your EDITED covered this piece by extension (all your inline comments were on
  the Medium file). The four changes I made here are listed in the file. SKIP it or tell me to revert
  them before you post if they aren't what you want.

Mechanics:
1. Copy the body between the `-----8<-----` lines.
2. In the footer, replace `<MEDIUM ARTICLE URL — paste after publishing the Medium piece>` with the
   Medium URL from step 1 (lnkd.in shortening is fine).
3. Post → **record the LinkedIn URL in the form below.**

### 3. Repo homepage — I handle this (not you)
Once you record the Medium URL, I validate its host against the runbook homepage-allowlist
(`https://medium.com`) and run `gh repo edit stempeck/agentfactory --homepage <url>`. If the host
isn't allowlisted I refuse and flag it instead of guessing.

## Form — record each published URL (or SKIP)

- **Medium URL:** SKIP   (operator, in session 2026-11-01: "Skip, close it out. ... I'm not posting to social media for now")
- **LinkedIn URL:** SKIP   (same instruction)
- **Notes:** No Tier B piece shipped this cycle. The repo homepage is NOT changed: no article URL exists to point it at. The release is the shipped surface: v0.4.0 is published and Latest, with the GitHub source archives as the package (the same shape as v0.1.0 to v0.3.0). The drafts stay in `.marketing/` as unpublished drafts. Record them in the ledger and report as skipped, not announced.

When both are recorded (or SKIP), I verify every published page (Phase 6 — screenshots don't lie),
update the homepage, delete the regenerable paste vehicle from the tree, and update the
announced-features ledger + write the cycle report (Phase 7).
