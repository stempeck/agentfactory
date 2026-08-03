# Cycle-2 Report — stempeck/agentfactory (bead af-50cdad6b)

*2026-08-03. Flagship: the agentfactory web console + its new Telemetry view. Boundary: v0.1.0.*

## Summary

Audited development since v0.1.0, found the #92 feature wave (telemetry, multi-provider agents,
operator-only teardown, self-improvement) largely untold and three stale README claims. Proposed
telemetry as the flagship; the operator approved, then on draft review reframed the story around
the **web console** (the human's control room) with telemetry as the exciting new part, and
asked for broader feature coverage and screenshots. Shipped a Tier A docs refresh + v0.2.0
release, and the operator published a Medium article and LinkedIn post. Every published page was
verified; one subtitle fix was applied and re-verified.

## Shipped autonomously (Tier A — merged via PR #95)

- **3 stale README claims fixed:** `af version`→`af --version`; formula count 19→**24**; skills
  3→**10** — each verified against source.
- **New coverage:** README Observability section + `af telemetry` command reference; Web Console
  section now surfaces the Floor view and Telemetry view; multi-provider (`af config models`,
  `gpt-*`) and `af improvement` noted; formula/skills tables expanded. CHANGELOG `[Unreleased]`
  (retitled to v0.2.0 in this state PR).
- **Release:** [v0.2.0 — observability & multi-provider agents](https://github.com/stempeck/agentfactory/releases/tag/v0.2.0), themed notes, cut from merged main (main was green).
- Gates: GATE-1, GATE-3 (+web-console addendum), SELF-REVIEW, SELF-VERIFY, PHASE-6 all PASS.

## Published by the operator (Tier B — staged by agent, published by Glenn)

- **Medium:** "I run a factory of AI agents. Here's the window into it." —
  https://medium.com/@glennstempeck/i-run-a-factory-of-ai-agents-heres-the-window-into-it-ad0321bfce9f
  (built on real telemetry from this cycle's own run; two live-console screenshots). Repo homepage
  now points at it.
- **LinkedIn:** operator's edited short-form version —
  https://www.linkedin.com/posts/glenn-stempeck_i-run-a-factory-of-ai-agents-heres-the-share-7490182070434951168-Jwbi/

Delivery mechanics: rendered the Medium article to a self-contained rich-text page (Artifact) so
the operator could copy-paste past Medium's markdown-eating; provided the LinkedIn plain text +
screenshots. Post-publish, caught and fixed the subtitle sitting as a body paragraph.

## Skipped / not done, and why

- **No new issues filed:** the audit surfaced no genuine new gaps (main green; existing
  #84/#75/#73/#85/#86 already track known work). Did not invent issues.
- **Multi-provider was deliberately NOT the flagship:** leading with "runs on OpenAI too" would
  contradict the README's honest-scope line. Shipped as documented capability instead.

## Operator-click items (no API reaches these)

- None outstanding this cycle. (Profile pins, social preview were resolved in cycle-1; the CLA on
  the bot-authored PR was cleared by the operator's admin-merge — noted for next time: commits are
  authored `agentfactory <dev@agentfactory.local>`, which the CLA assistant can't match, so a
  Tier A PR needs an admin-merge or a CLA-signed author identity.)

## Voice calibration learned this cycle

- The operator wants LinkedIn posts **short** ("keep it short and concise or … everyone will
  suspect its AI written"). Recorded in the runbook Voice section; his ~140-word edited post is
  the new short-form calibration source.
- Human-operator perspective beats agent-narrating-itself; honest limitations should be woven in
  (or dropped) rather than dumped as a "what's rough" list.

## Top 3 candidates for next cycle

1. **The dogfooding story, told head-on** — an agent that runs its own repo's marketing cycle
   (this very process). Surfaced obliquely this cycle; it's a unique, shareable narrative.
2. **Multi-provider agents** — now that it's documented, a focused piece: "your specialist agents
   don't all have to run on Claude" (gpt-* formulas + the gateway), framed to not undercut the
   Claude-Code-first positioning.
3. **Browser formula authoring** — the web console lets you author formulas in the browser; a
   short visual piece (screenshots/gif) would land well after this cycle introduced the console.

## Recrawl check (~2 weeks, ≈2026-08-17)

- Web search `"Glenn Stempeck" agentfactory` and `"I run a factory of AI agents"` — expect the
  Medium article + the repo to surface.
- `site:medium.com glennstempeck telemetry` — expect the new article.
- Spot-check GitHub topic pages (claude-code, ai-agents) still list the repo (API-confirmed live).
- Confirm the repo homepage still resolves to the Medium article (visibility-health workflow
  alarms on drift; don't duplicate it).
