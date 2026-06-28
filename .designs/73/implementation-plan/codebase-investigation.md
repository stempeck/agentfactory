# Codebase Investigation — Issue #73 (Phase 2 ground-truth)

Firsthand current-state verification from 4 parallel Explore agents (3 phases +
deployment audit). The design describes TARGET state; this records CURRENT state.
**All paths relative to repo root** `/home/dev/af/agentfactory/.agentfactory/worktrees/wt-7605ca`.

**Implementation status: K1–K9 are ALL unimplemented.** Every pattern they must
follow already exists. Below: verified anchors, design discrepancies, reuse assets,
and hidden issues.

---

## 1. Verified anchors (CURRENT line numbers)

### `internal/config/config.go` (K1/K5/K2 models)
- `DefaultFactoryConfigJSON()` — **108–124** (struct → `json.Marshal` → string; fallback literal on err). **The exact K1/K5 model.** (design said :111 ✓ within range)
- `DefaultGitIdentity()` — **88–91** (constant-sourced in-code default). Model for K2's "in-code default" idiom. (design :89 ✓)
- Constants `DefaultGitUserName`/`DefaultGitUserEmail` — 35–37.

### `internal/config/dispatch.go` (schema + validators)
- `DispatchConfig` struct — **17–27**: `Repos[]string`(19), `TriggerLabel`(20), `NotifyOnComplete`(21), `Mappings`(22), `IntervalSecs`(23), `RetryAfterSecs`(24), `RemoveTriggerAfterDispatch`(25), `Workflows []Workflow`(26, omitempty).
- `DispatchMapping` struct — **29–35**: `label`(deprecated), `labels`, `source`(default "issue"), `agent`.
- `Workflow` struct — **42–45**: `label`, `phases`.
- `defaultNotifyAgent = "manager"` const — **15** (shared by both validators so they can't drift; comment 11–14).
- `validateDispatchConfig` (struct-level, private) — **140–185**: rejects empty `Repos`(142–143), empty `TriggerLabel`(145–146), empty `Mappings`(148–149); fills `Source`→"issue", `IntervalSecs`→300, `RetryAfterSecs`→1800 (175–179), **`NotifyOnComplete`→"manager" (181–183)**.
- `ValidateDispatchConfig` (cross-file, public) — **84–138**, signature at **93**: `func ValidateDispatchConfig(disp *DispatchConfig, agents *AgentConfig) error`. Mapping-agent existence check **100–103** (`"dispatch mapping references unknown agent %q"`); explicit-`NotifyOnComplete` check 105–108; workflow-phase agents must exist + have `formula` 115–129; **import-cycle comment 131–136** (`internal/formula` imports `internal/config`, so K1/K5/K6 in `internal/config` MUST NOT import `internal/formula`).

### `internal/cmd/install.go` (K4/K5 wire points)
- `runInstallInit` — starts **97**; `--agents` refuses to run from a worktree (checks `.factory-root`) **103–104**.
- Starter-config map (`map[string]string`, 5 keys) — **139–148**.
- **agents.json inline literal — line 143** (manager+supervisor ONLY; design :143 ✓). K5 replaces with `DefaultAgentsConfigJSON()`.
- **dispatch.json inline literal — line 145 ONLY** (`{"repos":[],"trigger_label":"agentic","notify_on_complete":"manager","mappings":[],"interval_seconds":300,"retry_after_seconds":1800}`; design :145 ✓). K4 replaces with `config.DefaultDispatchConfigJSON(repo)`.
- Write-if-absent guard (`os.Stat`/`os.IsNotExist`) — **150–156** (reused by K4/K5, unchanged; ADR-017).
- `af install` command flags (`--init`, `[role]`, `--agents`, `--no-build`) — 38–94.

### `internal/cmd/detect_default_branch.go` ⭐ (K2/K3 reuse template — NEW FINDING)
- `runGitDetect` package-level seam (ADR-009 injectable for tests) — **34–43**.
- `detectBranchTimeout = 5 * time.Second` — **14**; uses `context.WithTimeout` + `exec.CommandContext` (35–37).
- `gh repo view --json defaultBranchRef -q ...` — **67** (err → "" no-abort convention).
- `branchNameAllowlist = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)` — **21**. K3 needs the `owner/name` form `^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$`.
- Same exec+timeout idiom also at `prime.go:355–358` (mail check).

### `internal/cmd/up.go` (K9)
- `blanket := len(args) == 0` — **92**; `startupCfg` read at **82**.
- `if blanket {` — **306**; quality/other gates 312–324; **`if startupCfg.StartDispatch { startDispatch(...) }` — 330–335** (inside the blanket gate; warning-on-fail, sets `allOK=false`). K9 hoists this block out of the `if blanket` so positional `af up <name>` also reaches it. Design :306,330 ✓.

### `internal/cmd/dispatch.go` (K6/K8 + idempotency)
- Dispatch-loop `ValidateDispatchConfig` call — **146–148** (`if err := ... { return err }`; comment 143–145 notes it's the same validator as the write path). K6 relaxes ONLY this caller. Design :146 ✓.
- `startDispatch` — **1321–1340**: already-running benign no-op **1322–1325**; unconfigured skip (`ErrNotFound`/`ErrMissingField`) **1328–1331**; `launchDispatchSession` 1339. Idempotent ✓ (enables K9). Design :1322–1325 ✓.
- `runDispatchStatus` — **1356–1405**; `dispatchStatusJSON{ DispatcherRunning bool; Entries []dispatchStatusEntry }` — **1458–1461**. K8 adds config-validation-state fields here (additive to `--json` contract).
- Trigger-label hard pre-filter in issue/PR queries — ~**301/320** (Gap-2 substrate).

### `internal/cmd/config_set.go` (write path — must STAY strict)
- `af config dispatch set` calls strict `ValidateDispatchConfig` — **89–90** (`{ return err }`), then `SaveDispatchConfig` 94. K6 MUST NOT touch this. Design :85–91 ✓.

### `internal/config/startup.go`
- `StartDispatch bool` `json:"start_dispatch"` — **18**. install.go:147 ships `start_dispatch:true`.

### `internal/templates/roles/` (K5 — all present, embedded)
- `rapid-soldesign-plan.md.tmpl`, `rapid-implement.md.tmpl`, `ultra-review.md.tmpl`, `rapid-increment.md.tmpl` — all exist. K5 needs no `agent-gen`/rebuild.

### Tests (Phase 3 models + a hidden break)
- `internal/config/dispatch_workflow_test.go` — cross-file validation table pattern at **212–257**. **K7 model.**
- `formula_drift_test.go` — ADR-008 embedded-vs-installed drift test. K7-drift may mirror.
- ⚠️ **`internal/cmd/install_integration_test.go:66–72`** — existing test asserts `af install --init` writes **`repos:[]` + `mappings:[]`**. **K1/K5 change these defaults → this test WILL FAIL and MUST be updated** (not called out in the design). Also currently does NOT assert agents.json specialists.

---

## 2. Design ↔ codebase discrepancies (must be reflected in the outline)

| # | Design claim | Ground truth | Action |
|---|--------------|--------------|--------|
| D-a | dispatch.json empty literal at `install.go:176` (alt citation) | **`:176` is `mcpstore.New(...)`**, NOT a dispatch literal. Literal is at **:145 ONLY** | Use :145; drop :176 |
| D-b | "Today NO layer in `internal/` reads a git remote (0 grep hits)" | `git remote get-url` indeed absent, BUT **`gh repo view --json` already exists** in `detect_default_branch.go` | K2 has a real precedent to mirror; soften the "0 hits" framing |
| D-c | K5 = provision via `af install --agents` in quickstart (dependencies.md/integration.md/security.md) | **Superseded** by design-doc K5 = `DefaultAgentsConfigJSON()` seed in `runInstallInit`. `af install --agents` would RECURSE (calls quickstart) + refuses from worktree | Follow design-doc; companion docs stale |
| D-d | (implicit) new tests only | **Existing `install_integration_test.go:66–72` breaks** on the new defaults | Add "update existing test" to Phase 1 |
| D-e | `notify_on_complete` handling | Current literal explicitly sets `"manager"`; `validateDispatchConfig` fills it at runtime (181–183) anyway | K1 omits it (smaller validation surface, Gap-7); K7 must accept omitted-or-manager |

---

## 3. Deployment / environment parity (audit)

- **Bootstrap path A (bare `af install --init`)** and **path B (quickstart.sh → quickdocker.sh)** BOTH currently produce an invalid-by-construction default: empty `dispatch.json` + agents.json with only manager+supervisor. **C1 parity gap confirmed firsthand.**
- `quickstart.sh`: `cd` into target repo **428**; `af install --init` **441–445**; `af install manager`+`af install supervisor` **449–470**; **does NOT run `af install --agents`**. (717 lines)
- `quickdocker.sh`: clones target repo, delegates to quickstart. (694 lines)
- `af install --agents` → `runInstallAgents` → `agent-gen-all.sh` + `quickstart.sh`; refuses from worktree; requires initialized factory → confirms K5-seed-in-`runInstallInit` is the correct mechanism (no recursion).
- **CI (`.github/workflows/test.yml`, 157 lines):** unit `make test` (42–68), integration `make test-integration` +Python3.12/tmux (96–124), regen `make check-regen` (126–140), web-unit (70–94). **No job exercises `af install --init` dispatch validity** → K7 golden/cross-file test lands in the `make test` unit tier (gates AC-1/C-6 against drift).
- Out of scope (design doesn't touch, correctly): web/ module bootstrap, MaxWorktrees, MCP issue-store server (mcpstore.New at install.go:176).

---

## 4. Reuse assets (don't reinvent)

1. `DefaultFactoryConfigJSON()` (config.go:108–124) → K1, K5 (struct→marshal→string).
2. `detect_default_branch.go` (runGitDetect seam + 5s timeout + `gh repo view` + allowlist regex) → K2 discovery & K3 validator.
3. Write-if-absent guard (install.go:150–156) → K4, K5 (unchanged).
4. `dispatch_workflow_test.go` (212–257) → K7 cross-file test; `formula_drift_test.go` → K7 drift.
5. `startDispatch` idempotency (dispatch.go:1322–1325) → K9 (safe to hoist; no double-start).
6. `ErrNotFound`/`ErrMissingField` skip idiom (dispatch.go:1328–1331) → K8 "empty by design" vs "discovery failed" vs "unprovisioned" distinction.

---

## 5. Hidden issues / latent risks

1. **Existing test breakage** (D-d): `install_integration_test.go:66–72` must be updated in the SAME phase that changes the defaults, or Phase 1 reddens CI.
2. **Import cycle** (dispatch.go:131–136): K1/K5/K6 code in `internal/config` MUST NOT import `internal/formula`.
3. **K6 without K8** masks "configured-but-broken" as "running" (cross-review H2) → K8 mandatory with K6.
4. **K9 hoist** is safe (idempotent), but verify no second `startDispatch` caller would now double-fire — audit found only the up.go blanket-gated caller; hoisting changes only its gate.
5. **K3 regex** must reject leading `-` (gh flag-injection into `dispatch.go` `gh --repo`) and shell/terminal-escape chars before the value reaches disk/banner/`gh`.
6. **Two-label requirement** (Gap-2): trigger-label query pre-filter (dispatch.go ~301/320) means tagging only a mapping label dispatches nothing — docs-only fix in Phase 3.
