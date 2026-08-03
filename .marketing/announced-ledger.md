# Announced-Features Ledger — stempeck/agentfactory

Cross-run state for marketing-cycle. One row per feature publicly told. Features in git
history but absent here are the untold backlog.

| Feature | Venue | Date | URL |
|---|---|---|---|
| agentfactory launch: formulas, SKILL.md→agent pipeline, crash/compression recovery, inter-agent mail, fidelity gates | Medium article "95% reliable agents give you 86% reliable workflows" | 2026-07-11 | https://medium.com/@glennstempeck/95-reliable-agents-give-you-86-reliable-workflows-b264170eb66c |
| Same launch story, short form (SKILL.md→agents, formula separation, docker factory floor) | LinkedIn post (Glenn's hook) | 2026-07-11 | (post URL not captured — lnkd.in shortlink used) |
| v0.1.0 release: 19 formulas, web console, dispatch pipeline, themed CHANGELOG | GitHub release + README landing page | 2026-07-11 | https://github.com/stempeck/agentfactory/releases/tag/v0.1.0 |
| agentfactory web console + Run telemetry (observability: the Floor view, per-step timing + per-run token/cost, honest degradation banner) | Medium article "I run a factory of AI agents. Here's the window into it." | 2026-08-03 | https://medium.com/@glennstempeck/i-run-a-factory-of-ai-agents-heres-the-window-into-it-ad0321bfce9f |
| Same story, short form (web console = the control room; telemetry; continuous improvement loop) | LinkedIn post (Glenn's edited short version) | 2026-08-03 | https://www.linkedin.com/posts/glenn-stempeck_i-run-a-factory-of-ai-agents-heres-the-share-7490182070434951168-Jwbi/ |
| v0.2.0 release: Run telemetry, multi-provider agents (OpenAI via gateway + gpt-* formulas), operator-only factory teardown, reliable improvement self-edits, web console Telemetry view, +5 formulas (24 total), +7 skills documented (10 total) | GitHub release + README/CHANGELOG refresh (PR #95) | 2026-08-03 | https://github.com/stempeck/agentfactory/releases/tag/v0.2.0 |

## Untold backlog (updated cycle-2, top candidates)
Now told (cycle-2): web console (Floor view) + telemetry, multi-provider agents, operator-only
teardown, self-improving agents (mentioned). Still untold:
- Fable agent family: fable-implement / fable-increment / fable-review / fable-secure (#83) — named in the formula table only, never a story
- Autonomous dispatch pipeline: label matching, issue→PR handoff, cycle locking, phase advancement (#38, #79)
- Per-agent model selection + in-session gate continuation (`af done --phase-complete --gate`) (#81)
- Browser formula authoring in the web console (#83) — mentioned in the cycle-2 article's tour, never its own story
- Marketing-cycle itself (the dogfooding story — an agent that markets its own repo; surfaced obliquely in the cycle-2 article but never told head-on)
- Machine-readable JSON contracts (`af agents/dispatch/formula ... --json`), `af handoff`, `af watchdog`
