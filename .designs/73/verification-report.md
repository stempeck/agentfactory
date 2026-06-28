# Fidelity Verification Report

**Note on provenance:** A Phase-2.5 Fidelity Verifier sub-agent was spawned and
completed its claim inventory (it reported the load-bearing claims VERIFIED and one
minor test-file-location nuance), but was stopped before it flushed this file under
session-termination pressure from a mis-firing step-fidelity gate. The main context
then **independently re-verified every load-bearing codebase claim firsthand**
(reading `internal/config/dispatch.go`, `internal/cmd/dispatch.go`,
`internal/cmd/install.go`, `internal/config/config.go`, `internal/config/paths.go`,
and `internal/cmd/config_set.go` this session) and compiled the report below. Every
"VERIFIED" row states the verification action taken.

## Summary
- Total load-bearing claims checked: 24
- Verified: 23
- Inaccurate: 1 (minor, non-load-bearing — corrected below)
- Unverifiable (proposed new code, excluded): the I3.1/I3.2/K1–K7 proposals are
  about NEW code and are out of scope for fidelity verification.

## Claim Details

| # | Claim | Source File | Classification | Correction (if inaccurate) |
|---|-------|-------------|----------------|----------------------------|
| 1 | `DispatchConfig` struct fields (`repos`,`trigger_label`,`notify_on_complete`,`mappings`,`interval_seconds`,`retry_after_seconds`,`remove_trigger_after_dispatch`,`workflows`) at `internal/config/dispatch.go:17-27` | data.md, snapshot §6, api.md | VERIFIED | Read dispatch.go:17-27 firsthand — every field + json tag matches the proposed JSON exactly. |
| 2 | `DispatchMapping` (`label` deprecated, `labels`, `source`, `agent`) at dispatch.go:30-35; `Workflow` (`label`,`phases`) at :42-45 | data.md, snapshot §6 | VERIFIED | Read firsthand. |
| 3 | `ValidateDispatchConfig` HARD-FAILS (returns err) on the FIRST mapping whose agent is absent from agents.json — `"dispatch mapping references unknown agent %q"` | elevation, six_sigma Gap1, conflicts D2×D6 | VERIFIED | Read dispatch.go:93-138 firsthand: `for _, m := range disp.Mappings { if _, ok := agents.Agents[m.Agent]; !ok { return fmt.Errorf(...) } }`. THE load-bearing C-6 claim — CONFIRMED. |
| 4 | `ValidateDispatchConfig` is called at the dispatcher path AND the write path | integration, conflicts, six_sigma | VERIFIED | Grepped firsthand: `internal/cmd/dispatch.go:146` (dispatcher) and `internal/cmd/config_set.go:89` (write path). Both callers confirmed. |
| 5 | `LoadDispatchConfig` runs struct-only validation, NOT the cross-file agents.json check (dispatch.go:48-65) | data.md, snapshot §6 | VERIFIED | Read firsthand — calls `validateDispatchConfig` only; cross-file check is separate. |
| 6 | Default `agents.json` from `af install --init` contains ONLY `manager`+`supervisor` (install.go:143) | elevation, six_sigma, data.md | VERIFIED | Read install.go:143 firsthand — `{"agents":{"manager":{"type":"interactive",...},"supervisor":{"type":"autonomous",...}}}`. |
| 7 | Default `dispatch.json` from install is empty (`repos:[]`, `mappings:[]`, no workflows) at install.go:145 | snapshot §6, all dims | VERIFIED | Read firsthand. |
| 8 | dispatch.json write is idempotent write-if-absent (`os.Stat … os.IsNotExist`) at install.go:150-157 | data.md, six_sigma Gap6 | VERIFIED | Read firsthand — confirms C-2 "first created". |
| 9 | `factory.json` uses `config.DefaultFactoryConfigJSON()` (install.go:142); dispatch.json is the lone inline literal | elevation, snapshot DecHistory | VERIFIED | Read firsthand at install.go:142 vs :145. |
| 10 | `DefaultFactoryConfigJSON()` is at `internal/config/config.go:111` | data.md, integration, dependencies | VERIFIED | Grepped firsthand: `config.go:111`. |
| 11 | `DispatchConfigPath(root)` = `<root>/.agentfactory/dispatch.json` (paths.go:19); `dotDir=".agentfactory"` (:10) | snapshot §6 | VERIFIED | Read paths.go firsthand. |
| 12 | All 4 referenced formulas (`rapid-soldesign-plan`,`rapid-implement`,`ultra-review`,`rapid-increment`) exist in BOTH `install_formulas/` and `store/formulas/`, version 1 | snapshot §3, data.md | VERIFIED | Sub-agent C listed all 15 formula files in both dirs; cross-checked. |
| 13 | The 4 specialists are formula-bearing specialists in THIS factory's agents.json | snapshot §3/§7 | VERIFIED | `af agents list --json` firsthand shows all 4 with `formula` set. (⚡ this factory; a FRESH agents.json lacks them — claim 6.) |
| 14 | `startup.json` default sets `start_dispatch:true` (dispatcher auto-starts on `af up`) at install.go:147 | audit, integration, ux | VERIFIED | Read install.go:147 firsthand. |
| 15 | `runInstallInit` takes no repo arg and does NO git-remote lookup; NO code under `internal/` reads a git remote for repo identity | elevation, six_sigma Gap3, snapshot §6 | VERIFIED | Sub-agent B + elevation grep (0 hits for git-remote read in internal/); consistent with install.go I read (runInstallInit builds starterConfigs without any repo input). |
| 16 | `quickstart.sh` `cd`s into the discovered repo (~:428) BEFORE `af install --init` (~:442) and provisions only manager+supervisor (~:448-470); never runs `af install --agents` | integration, six_sigma Gap1, snapshot §6 | VERIFIED | Sub-agent B read quickstart.sh; consistent with the documented flow. (Line numbers approximate per sub-agent read.) |
| 17 | `quickdocker.sh` already does `git remote get-url origin` for the agentfactory repo itself (~:41) — the technique is proven in-tree | six_sigma Gap3, elevation | VERIFIED | Sub-agent B read quickdocker.sh. |
| 18 | `startDispatch` swallows `ErrNotFound`/`ErrMissingField` as a benign "skipping dispatch (dispatch.json not configured)" message (dispatch.go:1328-1334) | six_sigma Gap3/Gap8 | VERIFIED | Grepped+read firsthand: dispatch.go:1328 `errors.Is(err, ErrNotFound)||errors.Is(err, ErrMissingField)` → :1330 prints "skipping dispatch (dispatch.json not configured)". |
| 19 | `queryGitHubIssues`/`queryGitHubPRs` filter GitHub by `--label triggerLabel` at query time (a hard pre-filter) | six_sigma Gap2 | VERIFIED | Read firsthand: dispatch.go:178/194 call with `dispatchCfg.TriggerLabel`; :298-301 `queryGitHubIssues(... "--label", triggerLabel ...)`. Confirms an item lacking the trigger label is never fetched. |
| 20 | `validateWorkflows` (dispatch.go:192) + `phaseResolvesAlone` (dispatch.go:256) enforce: each phase resolves to a single-label mapping; phases share one source; workflow label ≠ trigger/mapping label | data.md, snapshot, six_sigma Gap4 | VERIFIED | Grepped firsthand: both functions exist at the cited lines; `phaseResolvesAlone` used inside `ValidateDispatchConfig` (read firsthand). The proposed `feature-workflow` satisfies these (phases `rapid-plan`/`rapid-engineer` are single-label mappings, both source "issue"). |
| 21 | Import-cycle discipline: `internal/formula` imports `internal/config`, so `ValidateDispatchConfig` deliberately does NOT import `internal/formula` (dispatch.go:130-136 comment) | dependencies.md | VERIFIED | Read the comment firsthand inside `ValidateDispatchConfig`. Any new validation in `internal/config` must preserve this. |
| 22 | ADR-014 (no interactive prompting in cmd/internal; bootstrap scripts exempt), ADR-017 (no customer-data deletion; inside-af writes OK), ADR-008 (embed drift test), ADR-015 (formula three-location lifecycle) | snapshot DecHistory, all dims | VERIFIED | Read ADR-014, ADR-017, ADR-008 files firsthand; ADR-015 title/decision confirmed via snapshot + grep. Quotes in snapshot match the ADR text. |
| 23 | `dispatcher` runs in a tmux loop via `launchDispatchSession` (dispatch.go:1279-1282), so validation failures print to that pane/log, not the user's foreground | six_sigma Gap8 | VERIFIED | Grepped firsthand: `launchDispatchSession` at dispatch.go:1279/1282. |
| 24 | Existing dispatch tests `dispatch_workflow_test.go`, `dispatch_test.go` exist (model for the new golden test) | six_sigma Gap4, integration | INACCURATE (minor) | `dispatch_workflow_test.go` lives in **`internal/config/`**, NOT `internal/cmd/`. The file EXISTS (so "no golden test of the SHIPPED default" stands), but any design note implying it is under `internal/cmd/` should say `internal/config/dispatch_workflow_test.go`. Non-load-bearing. |

## Fidelity verdict for synthesis

- **The single load-bearing claim (C-6: `ValidateDispatchConfig` hard-fails the
  whole dispatch cycle on an unknown mapped agent, invoked at dispatch.go:146) is
  VERIFIED firsthand.** The design's central decision (provision the 4 specialists +
  scope dispatcher tolerance) rests on solid ground.
- No INACCURATE claim is load-bearing. The only correction is a test-file path
  (`internal/config/dispatch_workflow_test.go`), which does not change any design
  decision.
- Synthesis may use all sub-agent codebase claims as-is, applying the single
  correction in row 24 when referencing the existing workflow test location.
