# synthesis-checklist.md — GATE B: Pre-Synthesis Source Re-grounding + Fidelity

Re-opened `source.md` top to bottom RIGHT NOW and re-copied each AC verbatim below
(not via verification.md or any sub-agent output). Every clause is enumerated and
mapped to a specific design component.

Component legend (from dependencies.md / synthesis):
- **K1** `DefaultDispatchConfigJSON(repo)` in `internal/config` (the baked-in default builder)
- **K2** repo-discovery helper in `runInstallInit` (`gh repo view` / `git remote get-url origin`)
- **K3** strict `owner/name` repo validator (security)
- **K4** `runInstallInit` starter-config wiring (replace install.go:145 literal; reuse write-if-absent EX4)
- **K5** quickstart specialist provisioning (the 4 referenced agents into agents.json)
- **K6** dispatch-loop unknown-agent skip-and-warn (defense-in-depth; write path stays strict)
- **K7** golden + cross-file tests
- **EX** existing machinery: `af sling --agent` specialist dispatch, formula step engine, `start_dispatch:true` auto-start (unchanged)

## Gate 3.0 — Source Re-grounding (AC clause map)

| AC key | Verbatim text (RE-COPIED from source.md right now) | Clauses (enumerated) | Each clause satisfied by which component? |
|--------|----------------------------------------------------|----------------------|-------------------------------------------|
| AC-1 | "We need to update dispatch.json to include a baked-in default for dispatch with agentfactory" | (i) update dispatch.json; (ii) a baked-in default; (iii) for dispatch with agentfactory | (i) K4 (replace install.go:145 literal); (ii) K1 (`DefaultDispatchConfigJSON`, single-source Go func); (iii) K1 content (the 4 mappings + feature-workflow + trigger_label "agentic") |
| AC-2 | "they could opt to just start tagging their github issues with tags instead to kick off work without ever needing to visit the manager." | (i) tag github issues with tags; (ii) to kick off work; (iii) without ever needing to visit the manager | (i) K1 mappings + trigger_label (NOTE: requires BOTH `agentic` AND a mapping label — see Risk/Gap-2); (ii) EX dispatcher sling + K5 (specialists must exist); (iii) EX `start_dispatch:true` auto-start (install.go:147) + K5 provisioning makes it real |
| AC-3 | "The dispatch.json should be boostrapped when the dispatch.json is first created during initial setup of the repository so we know the repository-name and can include that appropriately in the dispatch.json" | (i) bootstrapped when first created; (ii) during initial setup of the repository; (iii) so we know the repository-name; (iv) include that appropriately | (i) K4 + EX4 write-if-absent (install.go:152 = "first created"); (ii) K4 runInstallInit (called by quickstart configure_factory); (iii) K2 discovery (`git remote`/`gh`); (iv) K3 validate owner/name → K1 writes it into `repos` |
| AC-4 | "when we get to the step where we ask the manager,`run af sling --agent <some-agent> \"task description\"`, we should have the work executed autonomously using the step-by-step formula that represents the IDENTITY of that agent and respects the formulas rigid step-by-step process up to the point where human interaction is necessary for next steps" | (i) `af sling --agent <agent> "task"`; (ii) executed autonomously via the formula; (iii) the formula that IS the agent's IDENTITY; (iv) respects the rigid step-by-step process; (v) up to where human interaction is necessary | (i) EX `af sling --agent` specialist dispatch (sling.go, unchanged); (ii) EX formula instantiation + af prime/af done; (iii) K5 (the routed agents are formula-bearing specialists); (iv) EX formula step engine; (v) EX gate machinery |
| AC-5 | "all the code branches created should have been pushed as PR's against the main branch without doctor fixes or human interaction (unless absolutely necessary, or in the case of doctor - a `doctor --fix` is acceptable only as a bandaid/fix for legacy broken behaviors, not as an ongoing operational dependency)." | (i) branches pushed as PRs against main; (ii) without doctor fixes as ongoing dependency; (iii) without human interaction (unless absolutely necessary) | (i) property of the 4 routed FORMULAS (K5 routes; formula-internal push is OUT of this change's scope — see Six-Sigma Gap-5 scoping); (ii) K1 valid-by-construction + K5 provision → no `doctor --fix` needed (SEC2.3 rejected); (iii) EX zero-touch happy path; degraded path is the only-when-necessary exception |
| AC-6 | "The agents should follow the known working formula process that IS their IDENTITY to perform their work so that we have consistent successful outcomes out of each agent. Your mission when addressing any problem scenario is to seek to understand how to achieve this desired outcome with systemic improvements while addressing the scenario." | (i) follow the known working formula process that IS their identity; (ii) consistent successful outcomes out of each agent; (iii) systemic improvements while addressing the scenario | (i) K5 routes to existing formula-bearing specialists, NO formula edits; (ii) K1 single-source default + K5 provisioned specialists + K7 drift/golden test = repeatable validity; (iii) the whole design (K1–K7: single-source builder + repo discovery + provisioning fix + test gate) is systemic, not a one-off patch |

**Gate 3.0 result:** Every AC in source.md (AC-1..AC-6) is listed; every clause is
individually enumerated; every clause maps to a specific component. No cell reads
"covered generally". One clause (AC-5 clause i) is explicitly scoped to the
formula layer (necessary-but-not-sufficient), NOT silently claimed — see Six-Sigma
Caveats in design-doc.

## Gate 3.0b — Fidelity Gate (corrections to apply during synthesis)

From `verification-report.md`:
- **23 of 24 load-bearing claims VERIFIED.** The single most load-bearing claim —
  `ValidateDispatchConfig` hard-fails the entire dispatch cycle on the first unknown
  mapped agent, invoked at `internal/cmd/dispatch.go:146` — is **VERIFIED firsthand**.
  The C-6 resolution (K5 provision + K6 scoped tolerance) rests on solid ground.
- **1 INACCURATE (minor, non-load-bearing):** the existing workflow test is at
  **`internal/config/dispatch_workflow_test.go`**, NOT `internal/cmd/`. When the
  design references the model for the new golden test (K7), it MUST cite
  `internal/config/dispatch_workflow_test.go`. No design decision changes.
- No other INACCURATE claims. design-doc.md must therefore contain ZERO claims
  flagged INACCURATE — the only correction needed is the test path above.

**Addendum — line-number fidelity note for synthesis:** quickstart.sh line numbers
(~:428/:442/:448-470) and a few deep dispatcher lines were read by the Phase-2.5
sub-agent (approximate); the load-bearing struct/validation/install lines were
re-read firsthand by the main context and are exact. Synthesis should phrase
quickstart line numbers as approximate ("~") and the dispatch.go/install.go/config.go
lines as exact.

**Gate B result: PASS** (Gate 3.0 AC clause-map complete + count-equal; Gate 3.0b
corrections recorded).
