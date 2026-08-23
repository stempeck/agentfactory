# Cycle-3 Story Proposal — stempeck/agentfactory (bead af-c967c569)

*Prepared 2026-08-23 by marketing-cycle. `flagship_hint` was empty, so the agent first
proposed a ranked pick (self-recovering agents). The operator **REORDERED** on issue
[#105](https://github.com/stempeck/agentfactory/issues/105) — this file is now rewritten
around the operator's pick, which is final.*

## The operator's directive (verbatim, issue #105 comment, 2026-08-23)

> "I think it would be ideal to show EVERY screen in the "--web" interface. In our last post
> we talked about a couple screens (factory floor, telemetry) but we have MANY more screens we
> haven't talked about. I think with each of these marketing cycles we need to take the
> opportunity to talk about "--web" interface screens that humans can use for each purpose,
> teaching a little about agentfactory usage as we display the stunning screens and also relate
> them to new features that have been introduced."

## The pick (REORDER)

**Flagship story: A guided tour of the agentfactory web console — every screen, what it's
for, and the new feature it surfaces.** The `--web` console is the human control room for a
factory of autonomous agents. Cycle-2 showed two rooms (Floor, Telemetry); this cycle walks
the whole floor plan — each screen introduced by the job it does for a human operator, shown
as a stunning screenshot, and tied to a feature (especially the #104 wave: self-recovery,
honest surfaces, trustworthy config, browser formula authoring).

This also establishes a **recurring cadence** the operator asked for: every future cycle
takes the console-screen angle for whatever shipped, teaching usage while it markets.

## The screen inventory (grounded in source — the tour's spine)

Seven top-level nav routes (`web/internal/web/static/index.html`) + agent detail + the formula
editor. Each row: screen → what a human uses it for → the feature it showcases this cycle.

| Screen (route) | What a human uses it for | Feature it surfaces this cycle |
|---|---|---|
| **Floor** (`floor`) | The live skyline — every running agent is a lit sign showing its honest status | **Self-recovering agents:** context-fill bar + recovery badges make an exhausted/halted agent visibly distinct from a healthy one (`recoverybadge_test.go`) |
| **Agent detail** (from Floor) | Drill into one agent: status, activity, operator mail | Honest read-model (Phase-0 status + tmux liveness); operator↔agent mail from the browser |
| **Sling** (`sling`) | Dispatch a task to an agent from the browser | The one-command dispatch path (`af sling`) with a UI (`sling_test.go`) |
| **Dispatch** (`dispatch`) | Watch dispatch status + history | The autonomous dispatch pipeline surfaced (still-untold backlog gets a first visual) |
| **Formulas** (`formulas`) | Author/edit formula TOML in the browser | **Browser formula authoring** — the shipped feature the README Roadmap still lists as "future"; per-panel save with write precondition + audit line (`server.go:1229`) |
| **Telemetry** (`telemetry`) | Per-step timing + per-run token/cost | **Honest telemetry:** "not measured" is rendered distinct from "zero"; a banner reports backend degradation as data, never an invented number |
| **Settings** (`settings`) | Edit factory config (messaging, statusline, model pins, …) | **Trustworthy config:** unknown keys ride through untouched instead of being silently erased; per-panel saves with preconditions + an audit line; absent files stay absent |
| **Prototypes** (`prototypes`) | Browse design prototypes (`.designs/`) | The design-review surface (proto server) — a screen never shown before |

*(README lines 266–270 already enumerate this exact set: Floor, slinging tasks, dispatch
status, browser formula authoring, agent detail with operator mail, design prototypes,
settings, Telemetry — so the tour reflects shipped reality, not aspiration.)*

## Supporting refresh items (ship on the same Tier A PR — from the audit STALE list)
- **CHANGELOG**: add a section for the #104 wave (proposed `v0.3.0`, or `Unreleased` pending the release decision) — currently stops at v0.2.0.
- **README**: surface the five new command families the Command Reference omits (`af memory`, `af statusline`, `af recovery`, `af fidelity`, `af config fingerprint`); **fix the Roadmap line that still lists "formula authoring in the browser" as future** (it ships — and it's now a flagship screen); keep the recovery narrative honest.
- **docs/recovery-model.md**: add the context-exhaustion recovery section (anchors the Floor screen's recovery-badge story).

## Original agent ranking (preserved for the record — superseded by the operator REORDER)

| Candidate | Pain | Demo | Fit | Reach | Total |
|---|---|---|---|---|---|
| Self-recovering agents (context-exhaustion recovery) | 5 | 5 | 5 | 5 | 20 |
| Durable memory vault | 4 | 4 | 4 | 4 | 16 |
| Live statuslines | 3 | 5 | 3 | 3 | 14 |
| The honesty release (fair gates + config integrity + honest telemetry/console) | 4 | 2 | 3 | 4 | 13 |

The operator's console-tour pick absorbs the strongest of these as *content within the tour*:
self-recovery → the Floor screen; honest telemetry → the Telemetry screen; trustworthy config
→ the Settings screen. Nothing verified is lost; it's reorganized around the screens.

## Operator Decision
- Decision: REORDER: web console screen tour (every --web screen, purpose + new feature)
  (APPROVE to proceed with the pick as written; REORDER: <feature> to swap the flagship;
   END-CYCLE to stop after an audit-only report. Leave blank = not yet decided.)
- Notes: Operator (stempeck) reordered via issue #105 comment on 2026-08-23. Directive quoted
  verbatim above: show EVERY --web screen, teach usage per screen, display stunning screens, and
  relate each to newly introduced features — and make this a recurring per-cycle theme. Agent's
  original self-recovery pick is folded into the Floor screen. Proceeding to Phase 3 (Tier A
  refresh) and Phase 4 (the console-tour article + short-form post, screenshots per screen).
