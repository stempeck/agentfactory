# Concern #1 Investigation: Install-time default dispatch.json is empty (repos/mappings)

**Investigated by**: Sub-agent
**Date**: 2026-06-28

## Verdict: VALIDATED

## Summary
A fresh `af install --init` writes a dispatch.json with empty `repos` and empty `mappings` (`internal/cmd/install.go:145`). On every startup path, `config.LoadDispatchConfig` runs `validateDispatchConfig`, which rejects empty `repos` (line 142-143) and empty `mappings` (line 148-149) with `ErrMissingField`. The `af up` startup path (`startDispatch`, `internal/cmd/dispatch.go:1321-1340`) explicitly catches `ErrMissingField` and friendly-skips with "skipping dispatch (dispatch.json not configured)" — the dispatcher session is never launched. The strict CLI path (`runDispatchStart`, line 1258-1280) hard-errors on the same config. Either way, the dispatch loop (line 172 `for _, repo := range dispatchCfg.Repos`) never even runs because the config never loads. The net effect: on a fresh install, labeling a GitHub issue causes ZERO dispatch — there is no repo to query and no mapping to match, and the dispatcher process is not even started. The gap between "fresh install" and "label dispatches work" is total: a new user must hand-edit dispatch.json (add at least one repo and one label→agent mapping) before any label can dispatch anything. This is exactly the problem issue #73 describes.

## 5-Whys Analysis

### Why #1: On a fresh install, does labeling a GitHub issue dispatch any work?
No. The dispatcher never runs against any repo. The startup path that would launch the dispatcher refuses to launch it because the install-default config is invalid. Evidence: `startDispatch` calls `config.LoadDispatchConfig`, and on `ErrMissingField` it prints "skipping dispatch (dispatch.json not configured)" and returns nil without launching (`internal/cmd/dispatch.go:1327-1332`). Empirically confirmed: `TestStartDispatch_EmptyDefaultConfigFriendlySkip` passes and emits that skip message, and asserts `NewSession af-dispatch` is NOT recorded (`internal/cmd/startdispatch_test.go:70-84`).

### Why #2: Why does the dispatcher refuse to launch / never query a repo?
Because the dispatch config fails validation at load time. `config.LoadDispatchConfig` always calls `validateDispatchConfig` before returning (`internal/config/dispatch.go:61-63`), and that validator rejects the install default. Two independent failures fire: empty `repos` (`internal/config/dispatch.go:142-143`: `if len(cfg.Repos) == 0 { return ...ErrMissingField... }`) and empty `mappings` (`internal/config/dispatch.go:148-149`: `if len(cfg.Mappings) == 0 { return ...ErrMissingField... }`). So `LoadDispatchConfig` returns an `ErrMissingField`-wrapped error and a nil config. The dispatch loop at `internal/cmd/dispatch.go:172` (`for _, repo := range dispatchCfg.Repos`) is never reached because the cfg never loads.

### Why #3: Why is the dispatch config empty / invalid at install time?
Because that is literally what install writes. `internal/cmd/install.go:145` seeds the starter config map with:
`"dispatch.json": `{"repos":[],"trigger_label":"agentic","notify_on_complete":"manager","mappings":[],"interval_seconds":300,"retry_after_seconds":1800}``
The `repos` array is `[]` and the `mappings` array is `[]`. The trigger label IS set ("agentic"), but with no repo to query and no label→agent mapping, that is inert. Even if validation passed, `matchItemToAgent` (`internal/cmd/dispatch.go:337-355`) iterates `mappings` and returns "" when the slice is empty, so every item would be skipped (`internal/cmd/dispatch.go:222-226`).

### Why #4: Why does install write empty `repos` and empty `mappings` rather than real values?
Because install has no install-time mechanism to discover the user's org/repository or to bake sensible label→agent mappings. The starter-config map (`internal/cmd/install.go:139-148`) hard-codes literal JSON strings for each config file; `dispatch.json` is a static empty-skeleton literal with no substitution of a detected `origin` remote and no default mappings/workflows. It is only written if the file does not already exist (`internal/cmd/install.go:150-157` — idempotent stat-gate), so it is purely a placeholder the user is expected to fill in by hand. This absence of org/repo detection + default mappings is the ROOT CAUSE.

### Why #5 (root cause): Why is there no install-time population of repo + mappings?
Because the design treats dispatch.json as opt-in / manually-configured rather than self-bootstrapping. The friendly-skip logic (`internal/cmd/dispatch.go:1313-1315`, 1328-1332) and its tests (`startdispatch_test.go:70-84`) intentionally model the empty default as "not configured" and degrade gracefully so `af up` never aborts. The contract is "ship a valid-JSON placeholder, skip dispatch until a human fills it in." There is no code path that detects the current GitHub repo (e.g. via `git remote get-url origin` / `gh repo view`) at install time, nor any baked-in default `mappings`/`workflows`. ROOT CAUSE: install.go ships a deliberately-empty placeholder dispatch.json with no auto-population of `repos` (from the detected origin) and no default label→agent mappings, so a fresh factory dispatches nothing by label until the operator hand-edits the file.

## Evidence Gathered
| Finding | Source | Evidence |
|---------|--------|----------|
| Install writes empty repos + empty mappings | `internal/cmd/install.go:145` | `"dispatch.json": `{"repos":[],"trigger_label":"agentic","notify_on_complete":"manager","mappings":[],"interval_seconds":300,"retry_after_seconds":1800}`` |
| Starter config only written if absent (idempotent placeholder) | `internal/cmd/install.go:150-157` | `if _, err := os.Stat(path); os.IsNotExist(err) { ... os.WriteFile(path, []byte(content), 0644) }` |
| Load always validates | `internal/config/dispatch.go:61-63` | `if err := validateDispatchConfig(&cfg); err != nil { return nil, err }` |
| Empty repos rejected with ErrMissingField | `internal/config/dispatch.go:142-143` | `if len(cfg.Repos) == 0 { return fmt.Errorf("%w: dispatch config must have at least one repo", ErrMissingField) }` |
| Empty mappings rejected with ErrMissingField | `internal/config/dispatch.go:148-149` | `if len(cfg.Mappings) == 0 { return fmt.Errorf("%w: dispatch config must have at least one mapping", ErrMissingField) }` |
| ErrMissingField is the sentinel | `internal/config/config.go:19` | `ErrMissingField = errors.New("missing required field")` |
| af-up path friendly-skips on ErrNotFound/ErrMissingField | `internal/cmd/dispatch.go:1327-1332` | `cfg, err := config.LoadDispatchConfig(root); if errors.Is(err, config.ErrNotFound) || errors.Is(err, config.ErrMissingField) { fmt.Fprintf(..., "skipping dispatch (dispatch.json not configured)\n"); return nil }` |
| Skip happens BEFORE launchDispatchSession | `internal/cmd/dispatch.go:1338-1339` | launch only reached after the skip guard returns; empty default never reaches it |
| Strict CLI path hard-errors on same config | `internal/cmd/dispatch.go:1273-1276` | `dispatchCfg, err := config.LoadDispatchConfig(root); if err != nil { return fmt.Errorf("loading dispatch config: %w", err) }` |
| Dispatch loop keyed on Repos (never runs when empty) | `internal/cmd/dispatch.go:172` | `for _, repo := range dispatchCfg.Repos {` |
| Empty mappings ⇒ no agent match (defense in depth) | `internal/cmd/dispatch.go:337-355` | `matchItemToAgent` loops `mappings`; returns "" when empty ⇒ `stats.skipped++; continue` (lines 222-226) |
| Comment confirms empty install default is treated as "not configured" | `internal/cmd/dispatch.go:1313-1314` | "An absent dispatch.json or the empty install default skips with a friendly message" |

## Tests Performed
| Test | Command | Result |
|------|---------|--------|
| af-up startup friendly-skips empty default, never launches session | `go test ./internal/cmd/ -run TestStartDispatch_EmptyDefaultConfigFriendlySkip -v` | PASS — emits "skipping dispatch (dispatch.json not configured)"; asserts no `NewSession af-dispatch` |
| Absent config also friendly-skips | `go test ./internal/cmd/ -run TestStartDispatch_AbsentConfigFriendlySkip -v` | PASS |
| Load rejects empty repos | `go test ./internal/config/ -run TestLoadDispatchConfig_MissingRepos -v` | PASS |
| Load rejects empty mappings | `go test ./internal/config/ -run TestLoadDispatchConfig_EmptyMappings -v` | PASS |
| Confirmed install literal | `Read internal/cmd/install.go:145` | repos=[], mappings=[] verified |

(Note: tests required `GOTMPDIR`/`TMPDIR` redirected to a worktree-local dir and sandbox disabled because `/tmp` is mounted noexec; temp build dir was removed after the run. No source code was modified.)

## Conclusion
VALIDATED. The concern is confirmed end-to-end with code evidence. Install (`install.go:145`) ships a dispatch.json with `repos:[]` and `mappings:[]`. That config fails `validateDispatchConfig` on BOTH the empty-repos check (`dispatch.go:142-143`) and the empty-mappings check (`dispatch.go:148-149`), each producing `ErrMissingField`. On the `af up` startup path, `startDispatch` (`dispatch.go:1327-1332`) catches `ErrMissingField` and friendly-skips without launching the dispatcher — proven by `TestStartDispatch_EmptyDefaultConfigFriendlySkip` (passes, emits the skip line, asserts no session is created). On the strict `af dispatch start` CLI path, the same load hard-errors (`dispatch.go:1273-1276`). In neither case does the dispatch loop (`dispatch.go:172`, keyed on `Repos`) ever execute, and even if it did, the empty `mappings` would make `matchItemToAgent` (`dispatch.go:337-355`) return "" for every item. Therefore, with the install default in place, labeling a GitHub issue causes ZERO dispatch. The gap between "fresh install" and "label dispatches work" is complete and requires manual editing of dispatch.json (add ≥1 repo and ≥1 label→agent mapping) — precisely the problem issue #73 raises.
