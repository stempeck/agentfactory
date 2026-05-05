# hooks subsystem

**Covers:** hooks

## Shape
Two Bash scripts (`hooks/quality-gate.sh`, `hooks/fidelity-gate.sh`) plus their prompt files (`hooks/quality-gate-prompt.txt`, `hooks/fidelity-gate-prompt.txt`) are Stop-hook evaluators that Claude Code invokes after every assistant turn and that grade the turn with a Haiku sub-invocation of the `claude` CLI (`hooks/quality-gate.sh:98-100`, `hooks/fidelity-gate.sh:144-146`). The Stop-hook wiring lives in `internal/claude/config/settings-autonomous.json:36-50` (both gates) and `internal/claude/config/settings-interactive.json:36-46` (quality only). The source copies in `hooks/` are the canonical runtime files; the byte-identical copies under `internal/cmd/install_hooks/` are embedded via `//go:embed install_hooks/*` at `internal/cmd/install.go:18` and rendered into a fresh factory's `hooks/` directory by `af install --init` at `internal/cmd/install.go:133-179`. On failure, each gate mails its JSON verdict back to the agent's own inbox — subject `QUALITY_GATE` (`hooks/quality-gate.sh:107`) or `STEP_FIDELITY` (`hooks/fidelity-gate.sh:153`) — and always exits `{"ok": true}` so the Stop event itself is never blocked (`hooks/quality-gate.sh:110-111`, `hooks/fidelity-gate.sh:156-157`).

## Hooks

### quality-gate
- Triggering event: Claude Code `Stop` hook (`internal/claude/config/settings-autonomous.json:42`, `internal/claude/config/settings-interactive.json:42`).
- Input contract: Stop-hook event JSON on stdin with fields `stop_hook_active`, `last_assistant_message`, `transcript_path` (`internal/cmd/hook_pair_smoke_test.go:38`; read at `hooks/quality-gate.sh:30, 45, 53`).
- Toggle: reads `$FACTORY_ROOT/.quality-gate`; runs only when the file contains `"on"` — default off (`hooks/quality-gate.sh:20-24`; toggled by `af quality on|off` at `internal/cmd/quality.go:29-68`; default-off provenance commit `6f9a777`).
- Recursion guard: bails immediately if `stop_hook_active=true` (`hooks/quality-gate.sh:30-34`). Concurrent-run guard: per-role mkdir lock at `/tmp/af-quality-gate-$ROLE.lock` with trap cleanup (`hooks/quality-gate.sh:37-42`).
- Evidence assembly: pulls the last 5 tool_use calls and tool_result outputs from the transcript file, using `tac` on Linux / `tail -r` on macOS (`hooks/quality-gate.sh:56-80`; `tac`/`tail -r` fix from commit `96ec834`).
- Evaluator: `claude -p --model haiku --max-turns 1` with system prompt `hooks/quality-gate-prompt.txt` (7 non-negotiable principles: verify outcomes, know system, warnings in deliverables, no false confidence, step back don't dig, no unverified reversals, only block material current-response violations — `hooks/quality-gate-prompt.txt:5-12`).
- Pass/fail semantic: the hook script itself always `exit 0` with stdout `{"ok": true}` (`hooks/quality-gate.sh:110-111`). The Haiku verdict is separate: if it returns `{"ok": false, "reason": ...}`, the verdict JSON is mailed to the agent's own mailbox with subject `QUALITY_GATE` (`hooks/quality-gate.sh:106-108`). Mail arrival is the enforcement channel, not the hook exit code.
- Silent-exit cases: no `$FACTORY_ROOT`, gate toggle not `on`, `stop_hook_active=true`, lock held, empty/null `last_assistant_message`, no `claude` on PATH — all emit `{"ok": true}` and return (`hooks/quality-gate.sh:13-86`).

### fidelity-gate
- Triggering event: Claude Code `Stop` hook, autonomous settings only (`internal/claude/config/settings-autonomous.json:44-47`). Absent from interactive settings.
- Input contract: same Stop-hook JSON as quality-gate (`hooks/fidelity-gate.sh:36, 52, 84`).
- Toggle: reads `$FACTORY_ROOT/.fidelity-gate`; runs only when file contains `"on"` — on by default for new factories (`af install --init` creates the file; toggled by `af fidelity on|off` at `internal/cmd/fidelity.go:35-74`).
- Recursion guard and lock: identical pattern to quality-gate but with a distinct lock path `/tmp/af-fidelity-gate-$ROLE.lock` to avoid collision (`hooks/fidelity-gate.sh:36-49`; FIDELITY-DELTA 3 comment at line 42).
- Step ground-truth fetch: calls `af step current --json` and parses `.id`, `.title`, `.description`, `.is_gate`, `.formula`; bails silently unless `.state == "ready"` (`hooks/fidelity-gate.sh:67-81`). Description is head-capped to 4096 bytes to bound Haiku input size (`hooks/fidelity-gate.sh:73-79`).
- Evaluator: `claude -p --model haiku --max-turns 1` with system prompt `hooks/fidelity-gate-prompt.txt`; prompt enforces 6 principles — on-contract work, no premature completion, no silent skips, no unauthorized `af done`, formula-level discipline, material-only blocks (`hooks/fidelity-gate-prompt.txt:10-16`).
- Pass/fail semantic: hook itself always exits `{"ok": true}` (`hooks/fidelity-gate.sh:156-157`). Failing Haiku verdicts are mailed to the agent's own mailbox with subject `STEP_FIDELITY` (`hooks/fidelity-gate.sh:152-154`). FIDELITY-DELTA markers at `hooks/fidelity-gate.sh:22, 25, 42, 59, 119` document the five substantive deltas from quality-gate.sh.

## Source-of-truth + drift contract
`hooks/` at repo root is the runtime source copy (what actually executes when `${AF_ROOT}/hooks/quality-gate.sh` fires, per `internal/claude/config/settings-autonomous.json:42`). `internal/cmd/install_hooks/` is the compile-time embed tree pulled in by `//go:embed install_hooks/*` at `internal/cmd/install.go:18` and written to a fresh factory's `hooks/` dir by `af install --init` at `internal/cmd/install.go:139-179`. The two locations must stay byte-identical — pinned by `TestInstallHooks_NoDrift` at `internal/cmd/install_hooks_drift_test.go:30-56` which compares all four files (`quality-gate.sh`, `quality-gate-prompt.txt`, `fidelity-gate.sh`, `fidelity-gate-prompt.txt`) on every `make test`. The drift test's own docstring at `internal/cmd/install_hooks_drift_test.go:21-23` states the rationale: "Drift would silently produce factories that differ from the developer's local environment."

## Environment-variable contract
Both scripts resolve identity using env-var-with-fallback:
- `ROLE=${AF_ROLE:-$(basename "$(pwd)")}` (`hooks/quality-gate.sh:9`, `hooks/fidelity-gate.sh:14`).
- `FACTORY_ROOT=${AF_ROOT:-$(af root 2>/dev/null)}` (`hooks/quality-gate.sh:12`, `hooks/fidelity-gate.sh:17`).

Pinned by `TestHookScripts_UseEnvVarFallback` at `internal/cmd/hook_envvar_test.go:21-57`, which asserts four invariants on each script: (1) `AF_ROLE:-` substring present (line 38); (2) `AF_ROOT:-` substring present (line 43); (3) bare `FACTORY_ROOT=$(af root` forbidden (line 48); (4) `af root` must still appear as the fallback (line 53). Rationale in the test docstring: worktree isolation (design constraint C12) requires hooks to resolve ROLE/FACTORY_ROOT from tmux-exported env vars when running inside a git worktree, falling back to basename/`af root` for manual invocation (`internal/cmd/hook_envvar_test.go:13-19`). The Stop-hook command in settings already uses `${AF_ROOT}` as the path prefix (`internal/claude/config/settings-autonomous.json:42,46`); worktree preparation provenance is commit `d053e5e`.

Known fragility: `basename "$(pwd)"` assumes the agent's cwd leaf dir IS the role name — breaks if cwd is any subdirectory of the agent workspace (comment at `hooks/quality-gate.sh:6-8`, `hooks/fidelity-gate.sh:11-13`, citing `.designs/32/design-doc.md:235`).

## Seams

| Seam | Direction | Contract | Anchor |
|------|-----------|----------|--------|
| `claude` CLI | OUT | `claude -p --model haiku --max-turns 1 --system-prompt <text> <eval-input>` returning JSON verdict | `hooks/quality-gate.sh:98-100`, `hooks/fidelity-gate.sh:144-146` |
| `jq` | OUT | parse Stop-hook event JSON, transcript NDJSON, verdict JSON | `hooks/quality-gate.sh:30, 45, 64-71, 106`; `hooks/fidelity-gate.sh:36, 52, 68-81, 95-102, 152` |
| `af root` | OUT | resolve factory root as fallback when `$AF_ROOT` is unset | `hooks/quality-gate.sh:12`, `hooks/fidelity-gate.sh:17` |
| `af mail send` | OUT | deliver Haiku verdict to agent's own mailbox on failure | `hooks/quality-gate.sh:107`, `hooks/fidelity-gate.sh:153` |
| `af step current --json` | OUT | pull current formula step (id/title/description/is_gate/formula) — only in fidelity-gate | `hooks/fidelity-gate.sh:67` (emits the shape defined at `internal/cmd/step.go:37`) |
| `tac` / `tail -r` | OUT | reverse transcript lines (Linux vs macOS) | `hooks/quality-gate.sh:57-61`, `hooks/fidelity-gate.sh:88-92` (fix commit `96ec834`) |
| Claude Code Stop hook | IN | JSON event on stdin with `stop_hook_active`, `last_assistant_message`, `transcript_path` | `internal/cmd/hook_pair_smoke_test.go:38` (synthetic payload pins the contract) |
| `install.go` | IN | render embedded hook files into `<factory>/hooks/` at `af install --init` | `internal/cmd/install.go:18, 133-179` |
| `$AF_ROOT`, `$AF_ROLE` | IN | tmux-exported env vars for worktree-safe path/role resolution | `hooks/quality-gate.sh:9,12`; `hooks/fidelity-gate.sh:14,17`; pinned by `internal/cmd/hook_envvar_test.go` |
| `.quality-gate` / `.fidelity-gate` files | IN | toggle files at factory root, content `"on"` to enable | `hooks/quality-gate.sh:20-24`, `hooks/fidelity-gate.sh:26-30`; writers: `internal/cmd/quality.go:54-62`, `internal/cmd/fidelity.go:60-68` |

## Formative commits

| SHA | Date | Subject |
|-----|------|---------|
| `dac416a` | 2026-03-23 | feat: Phase 4 Quality Gate — stop hook script, 7 principles prompt, verdict via mail |
| `312eb11` | 2026-03-25 | Improved quality gate alongside `af prime` — "as good as its likely going to get" |
| `96ec834` | 2026-03-25 | fix: quality-gate transcript parsing on Linux (tail -r → tac) |
| `6f9a777` | 2026-03-27 | Added `af quality on/off` toggle; default off |
| `871e9f9` | 2026-04-10 | Phase 2: introduced `hooks/fidelity-gate.sh` + `-prompt.txt`, byte-identical embedded mirrors, sequential-pair smoke test |
| `7101f3a` | 2026-04-10 | Phase 3 final code-merge for fidelity gate |
| `d053e5e` | 2026-04-11 | Worktree-isolation preparation — `${AF_ROLE:-...}`/`${AF_ROOT:-...}` fallback pattern |

## Load-bearing invariants
- `hooks/*` and `internal/cmd/install_hooks/*` must not drift — enforced by `TestInstallHooks_NoDrift` (`internal/cmd/install_hooks_drift_test.go:30-56`).
- Both scripts must use `${AF_ROLE:-...}` and `${AF_ROOT:-...}` fallback forms, must retain `af root` as fallback, must NOT use bare `FACTORY_ROOT=$(af root ...)` — enforced by `TestHookScripts_UseEnvVarFallback` (`internal/cmd/hook_envvar_test.go:38-55`).
- Stop-hook settings must reference `quality-gate.sh` and use `${AF_ROOT}` (not `$(af root)`) — enforced by `internal/claude/settings_test.go:75-84` (autonomous) and `:127-136` (interactive).
- The two hooks must co-exist sequentially without lock/stdin/trap collision — enforced by `TestHookPair_SequentialSmoke` which runs them in both orders against a synthetic Stop payload (`internal/cmd/hook_pair_smoke_test.go:30-64`).
- Both hooks must always `exit 0` with `{"ok": true}` on stdout — never block a Stop event (`hooks/quality-gate.sh:110-111`, `hooks/fidelity-gate.sh:156-157`; smoke-test assertion at `internal/cmd/hook_pair_smoke_test.go:60-63`).
- The fidelity gate's step-description passthrough (bash variable expansion must NOT trigger command substitution on `$(...)` inside the description text) is pinned by `TestStepCurrent_DescriptionPassthrough` at `internal/cmd/step_test.go:483` (referenced by the FIDELITY-DELTA 5 comment at `hooks/fidelity-gate.sh:119-123`).

## Cross-referenced idioms
- **Embed-source-of-truth drift contract**: the `hooks/` ↔ `internal/cmd/install_hooks/` pairing mirrors the same pattern used for formulas (`internal/cmd/install_formulas/`) and settings templates in `internal/claude/config/`. `install.go:18` uses `//go:embed install_hooks/*` exactly as the formula install block uses `//go:embed install_formulas/*` (visible at `internal/cmd/install.go:187`).
- **Env-var-with-fallback for identity resolution**: `${AF_ROLE:-$(basename "$(pwd)")}` / `${AF_ROOT:-$(af root ...)}` matches the tmux-export-then-fallback pattern introduced for worktree isolation (commit `d053e5e`).
- **Silent-exit-on-missing-dependency**: every missing prerequisite (`$FACTORY_ROOT` empty, `claude` not on PATH, `af` not on PATH, toggle off, lock held) produces `{"ok": true}` exit 0 rather than error — both scripts consistently apply this policy (`hooks/quality-gate.sh:13-86`, `hooks/fidelity-gate.sh:18-117`).
- **Mail-as-enforcement-channel**: gate verdicts are delivered by `af mail send "$ROLE"` to the agent's own inbox rather than surfacing as Stop-hook blocks — the agent sees the critique on its next `af mail check` (both scripts at their mail-send lines cited above; mail-check injection wired at `internal/claude/config/settings-autonomous.json:9,31`).

## Formal constraint tags
- **C12 (worktree isolation)** — cited inline at `internal/cmd/hook_envvar_test.go:18-19`: "hooks must resolve ROLE and FACTORY_ROOT from tmux-exported env vars when running inside a worktree, falling back to basename/af-root for manual invocation."
- **AC3.11 (multi-sibling hook dispatch)** — cited at `internal/cmd/hook_pair_smoke_test.go:21`: Phase 3b manual check, NOT covered by the sequential smoke test (the smoke test covers only the sequential-execution slice of the same concern, labelled R-INT-10 Q1).
- **R-INT-10** — same location; resolved at `make test` time by the sequential smoke test.
- **FIDELITY-DELTA 1-5** — inline structural tags in `hooks/fidelity-gate.sh:22, 25, 42, 59, 119` marking each substantive deviation from quality-gate.sh's otherwise-verbatim structure.

## Gaps
- Multi-sibling hook dispatch (AC3.11) is an operator-run post-merge manual check per `internal/cmd/hook_pair_smoke_test.go:21-23`; no automated test covers Claude Code fanning both Stop hooks out concurrently in production. **unknown — needs review** whether this has been validated since `7101f3a` (2026-04-10).
- The `.designs/32/design-doc.md:235` reference cited for the basename-fragility claim (`hooks/quality-gate.sh:8`) was not opened during this pass. **unknown — needs review** whether that design also documents a planned remediation.
- Whether `AF_ROLE` and `AF_ROOT` are actually exported in every path that launches the Stop hook (tmux session start, `af up`, etc.) is **unknown — needs review**; the hook-side fallback absorbs any missing export silently, which means a regression that drops the export would go unnoticed until an agent's cwd no longer matched its role name.
- Haiku invocation cost/latency in production is not instrumented in the scripts — `claude -p --model haiku` returns are parsed only for the `.ok` field (`hooks/quality-gate.sh:106`, `hooks/fidelity-gate.sh:152`); failed/malformed Haiku output is silently dropped (stderr is `2>/dev/null` at lines `98-100`/`144-146`). **unknown — needs review** whether this is intentional.
- Interactive settings (`internal/claude/config/settings-interactive.json:36-46`) deliberately omit the fidelity gate. The commit that added fidelity-gate (`871e9f9`) does not call out this asymmetry; rationale is **unknown — needs review**.
