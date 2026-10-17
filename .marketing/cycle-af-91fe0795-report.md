# Cycle af-91fe0795 Report — stempeck/agentfactory

*2026-10-06. Flagship: the three things #117 fixed — third-party agent plugins behind a trust
boundary, the ChatGPT-subscription model gateway, and relaunches that run on exactly the config
you set. Assignment: PR #117. Operator: Glenn Stempeck (`stempeck`).*

## Summary

The assignment bead carried the URL of **PR #117** ("Plugins, ChatGPT-subscription gateway, and
relaunches that never run on stale config") — a 383-file, +61k-line wave with three distinct,
untold, story-worthy facets. Ranked them, proposed **Plugins** as the flagship with a three-beat
arc (extend it → run it on your own subscription → trust every relaunch); the operator approved
as written (#118). Shipped a Tier A docs refresh (README + CHANGELOG), drafted both Tier B pieces
in the operator's voice, and held for approval — the operator marked both **READY** as written
(#120). The operator merged the Tier A PR (#119 → `main` `82afc5a7`) with no release decision, then
published the Medium article and chose **Medium only** (LinkedIn skipped). Every published page was
verified; no defects found.

## Shipped autonomously (Tier A — merged via PR #119, no release cut)

- **Two stale README counts fixed:** formulas 24→**26** (added `lineage` and `rapid-soldesign`
  rows), skills 12→**13** (added `/architecture-diagram`) — each verified against source
  (`internal/cmd/install_formulas/*.toml` = 26; `.claude/skills/` = 13).
- **New coverage for the flagship:** README "Plugins & the model gateway" command block —
  `af plugin list/install/verify/check/remove` and `af gateway auth import/status` — plus the
  quickstart flags `--litellm --litellm-auth=codex-subscription`. Every command proved live or
  against source before writing (GATE-3 PASS).
- **CHANGELOG:** `## Unreleased` section for #117 (plugins / gateway / stale-config fidelity).
- **No release cut:** the operator merged #119 with a bare "MERGED" and gave no A/B/C release
  decision, so #117 stays under `## Unreleased`. The pre-existing `## v0.4.0 — 2026-09-17`
  CHANGELOG-vs-no-tag gap (token economics, #111) was surfaced to the operator and left as his
  call — not reconciled by the agent.
- **No issues filed:** the audit found doc staleness (fixed) and the release gap (operator
  decision) — no product bugs; `main` was green. No manufactured activity.
- Gates all PASS: GATE-1 (audit), GATE-3 (claims verified), SELF-REVIEW, SELF-VERIFY, Step-15
  (tests + build), Step-16 (contract), PHASE-6 (published-page verify).

## Published by the operator (Tier B — staged by agent, published by Glenn)

- **Medium:** "Three things my agent factory couldn't do last month." —
  https://medium.com/@glennstempeck/three-things-my-agent-factory-couldnt-do-last-month-4c98d4a7c1b4
  Three real CLI figures embedded (`af plugin verify --all`, `af gateway auth status`,
  `af config models check`). Approved READY as written (#120); no edits, so no voice reconciliation.
  Repo homepage now points at it.
- **LinkedIn:** **SKIPPED** — operator chose "Medium only" this cycle. The short-form draft
  `cycle-af-91fe0795-linkedin.md` was approved READY (#120) and stays reusable with the Medium URL
  in its footer. Nothing posted ⇒ no ledger row claimed.

Delivery mechanics: all operator review routed through GitHub (review PR #119, notify issues
#118/#120/#121) per the runbook — the operator does not open worktree files. Provided the rich-text
paste vehicle so Medium wouldn't eat the markdown; it was deleted after publishing (regenerable).

## Skipped / not done, and why

- **LinkedIn:** operator decision ("Medium only"). Draft stays ready for reuse.
- **No release:** operator gave no release decision at merge; #117 remains `## Unreleased`. The
  Medium article deliberately claims **no version** as a result.
- **No new issues:** audit surfaced no genuine product gaps (main green); did not invent any.
- `lineage` / `rapid-soldesign` / `/architecture-diagram` were **documented** (Tier A) but not
  storied — one flagship per cycle; they go to the backlog, not a manufactured second story.

## Operator-click items (no API reaches these)

- **Release decision + tag (optional):** reconciling the `## v0.4.0 — 2026-09-17` CHANGELOG line
  against the missing `v0.4.0` tag, and whether to cut a release for #117, is the operator's call
  — no agent action pending. If a release is later cut, a Medium version anchor becomes available.
- Profile pinned items and the repo social-preview image have **no API** (GraphQL has no mutation;
  upload is UI-only). Neither needs a change this cycle — listed only so they aren't mistaken for
  agent to-dos.

## Voice calibration learned this cycle

- Nothing new to recalibrate: both drafts were approved **READY as written** (no operator edits),
  which confirms the current calibration (lead with the commands you type + the outcome; spaced
  hyphens; CAPS for emphasis; honest limits stated inline, not as a set piece; no self-congratulatory
  emotion). The approved finals `cycle-af-91fe0795-medium.md` / `-linkedin.md` join the calibration
  sources.

## Top 3 candidates for next cycle

1. **Token economics** (v0.4.0 / #111) — `af tokenomics`, `af telemetry compare`/`band`, the
   operator `--input-digest` attestation. Still fully untold, the strongest ranked candidate, and
   telling it is the natural moment to close the standing release gap with a real v0.4.0 tag.
2. **The dogfooding story, told head-on** — an agent that runs its own repo's marketing cycle (this
   very process). Surfaced obliquely for several cycles; a unique, shareable narrative.
3. **The durable memory vault** (`af memory`) — agents that remember across teardowns; pairs with
   self-recovery as "recover AND remember." Never told head-on.

## Recrawl check (~2 weeks, ≈2026-10-20)

- Web search `"Glenn Stempeck" agentfactory` and `"Three things my agent factory couldn't do"` —
  expect the new Medium article + the repo to surface.
- `site:medium.com glennstempeck plugin` — expect the new article.
- Spot-check GitHub topic pages (claude-code, ai-agents, multi-agent-systems, agentic-ai) still list
  the repo (API-confirmed live this cycle via `/search/repositories?q=topic:claude-code+agentfactory`).
- Confirm the repo homepage still resolves to the Medium article (the visibility-health workflow
  alarms on drift; don't duplicate it).
