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
| Guided tour of the web console — every `--web` screen (Floor, Agent detail, Sling, Dispatch, Formulas, Telemetry, Settings, Prototypes), each tied to a #104-wave feature: self-recovering agents (Floor recovery badges), browser formula authoring (Formulas — was Roadmap "future"), honest telemetry (not-measured≠zero), trustworthy config (unknown keys ride through). 8 real Playwright screenshots. | Medium article "I run a factory of AI agents. Last time I cracked the door — here's every room." | 2026-08-23 | https://medium.com/@glennstempeck/in-my-factory-of-ai-agents-last-time-i-cracked-the-door-heres-every-room-a04f14babef0 |
| v0.3.0 release: self-recovery, durable memory & honest surfaces — README five new command families (`af memory`/`af statusline`/`af recovery`/`af fidelity`/`af config fingerprint`), Roadmap "browser formula authoring" corrected to shipped, `docs/recovery-model.md` context-exhaustion section, CHANGELOG v0.3.0 (PR #107) | GitHub release + README/CHANGELOG/docs refresh (PR #107) | 2026-08-23 | https://github.com/stempeck/agentfactory/releases/tag/v0.3.0 |

*LinkedIn (short-form) was **SKIPPED** this cycle by operator decision (#108: "I'll post the next
one to LinkedIn. Not this one."). The draft `cycle-af-c967c569-linkedin.md` stays ready with the
live Medium URL in its footer for reuse next cycle — no row is claimed because nothing was posted.*

## Untold backlog (updated cycle-3, top candidates)
Now told (cycle-3): the full web console screen tour (all 8 `--web` screens); self-recovering
agents (surfaced via the Floor screen only, not head-on); browser formula authoring (told as the
Formulas screen + Roadmap line corrected); honest telemetry/config as framing. Still untold:
- **Self-recovering agents, head-on** (context-exhaustion recycle → resume on the open step) — only shown obliquely via the Floor screen this cycle; the strongest ranked candidate, never its own story
- **Durable memory vault** (agents remember across teardowns) — never told; pairs naturally with self-recovery ("recover AND remember")
- Fable agent family: fable-implement / fable-increment / fable-review / fable-secure (#83) — named in the formula table only, never a story
- Autonomous dispatch pipeline: label matching, issue→PR handoff, cycle locking, phase advancement (#38, #79) — got a first visual via the Dispatch screen, never told head-on
- Multi-provider agents (`gpt-*` formulas + gateway) — documented cycle-2, never its own story
- Per-agent model selection + in-session gate continuation (`af done --phase-complete --gate`) (#81)
- Marketing-cycle itself (the dogfooding story — an agent that markets its own repo; surfaced obliquely, never told head-on)
- Machine-readable JSON contracts (`af agents/dispatch/formula ... --json`), `af handoff`, `af watchdog`
