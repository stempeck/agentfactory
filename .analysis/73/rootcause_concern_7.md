# Concern #7 Investigation: Dispatcher auto-activation on fresh setup

**Investigated by**: Sub-agent
**Date**: 2026-06-28

## Verdict: VALIDATED

## Summary
On a fresh `af install --init`, the shipped `dispatch.json` default has **empty
`repos` and empty `mappings`** (`internal/cmd/install.go:145`). `startup.json`
correctly ships `start_dispatch:true` (`internal/cmd/install.go:147`), and a
bare `af up` DOES call `startDispatch` because of it
(`internal/cmd/up.go:330-331`). However, `startDispatch` loads and validates
`dispatch.json`; the empty-`repos`/empty-`mappings` default fails
`validateDispatchConfig` with `ErrMissingField`
(`internal/config/dispatch.go:142-150`), and `startDispatch` treats
`ErrMissingField` as the "not configured" friendly-skip — it prints
`skipping dispatch (dispatch.json not configured)` and returns `nil` WITHOUT
ever creating the dispatcher tmux session or starting the polling loop
(`internal/cmd/dispatch.go:1327-1339`). Therefore the goal in issue #73 ("a new
user can dispatch by labeling GitHub issues without visiting the manager") is
NOT met by the current defaults: the dispatcher is not polling after a fresh
setup. To close the gap the ship-default `dispatch.json` must be POPULATED
(non-empty `repos` AND at least one `mapping`, with a non-empty
`trigger_label`). Once populated, `af up` (blanket) would auto-launch the
polling loop with no manual step. Two residual gaps remain even with a populated
default: (a) the dispatcher only auto-starts on the **blanket** `af up` path —
`af up <names>` (e.g. `af up manager`) is gated out by the `blanket` flag
(`internal/cmd/up.go:92,306,330`), so it would NOT start dispatch; and (b) a
shipped repo string and label-→-agent mapping cannot be known at install time
(the repo is user-specific), so a literal populated default risks pointing at a
wrong/placeholder repo unless install personalizes it.

## 5-Whys Analysis

### Why #1: Why is the dispatcher not polling after a fresh setup, even though `start_dispatch:true`?
Because `af up` honors `start_dispatch` and calls `startDispatch`
(`internal/cmd/up.go:330-331`), but `startDispatch` refuses to launch the
session when the dispatch config is unconfigured.

### Why #2: Why does `startDispatch` refuse to launch with the fresh default config?
Because it loads `dispatch.json` and, when the loader returns `ErrNotFound` OR
`ErrMissingField`, it prints `skipping dispatch (dispatch.json not configured)`
and returns `nil` without launching
(`internal/cmd/dispatch.go:1327-1332`).

### Why #3: Why does loading the fresh-default `dispatch.json` return `ErrMissingField`?
Because the install default is
`{"repos":[],...,"mappings":[],...}` (`internal/cmd/install.go:145`) and
`validateDispatchConfig` rejects a config with zero repos (and would also reject
zero mappings) with `ErrMissingField`
(`internal/config/dispatch.go:142-150`). `LoadDispatchConfig` runs this
validator and propagates the error (`internal/config/dispatch.go:61-63`).

### Why #4: Why does the install ship an EMPTY (placeholder) dispatch.json rather than a populated one?
Because the dispatcher target — the GitHub `repos` and the label→agent
`mappings` — is user/repo-specific and cannot be known generically at install
time; the scaffold ships a structurally-valid-but-unfilled template
(`internal/cmd/install.go:145`) intended to be edited (the comment at
`internal/cmd/install.go:140-141` notes the configs are starter templates). The
empty default is precisely the "HIGH-1" case pinned by
`TestStartDispatch_EmptyDefaultConfigFriendlySkip`
(`internal/cmd/startdispatch_test.go:70-84`).

### Why #5: Why does an empty dispatch config friendly-skip instead of erroring (and is that the right design root)?
By deliberate design: the empty install default must not turn every fresh
`af up` into a noisy error or abort, so `ErrNotFound`/`ErrMissingField` are
folded into a single friendly "not configured" skip
(`internal/cmd/dispatch.go:1328-1332`), distinct from real misconfiguration
(malformed JSON / `ErrInvalidType`) which warns
(`internal/cmd/dispatch.go:1333-1336`; tests at
`internal/cmd/startdispatch_test.go:90-142`). ROOT CAUSE for issue #73: the
skip discriminator keys on config *completeness*, and the shipped default is
intentionally INCOMPLETE — so auto-activation is structurally impossible until
the shipped default is made complete (populated). Populating the config flips
the load result from `ErrMissingField` to a valid `*DispatchConfig`, which makes
`startDispatch` fall through to `launchDispatchSession` and actually start the
polling loop (`internal/cmd/dispatch.go:1338-1339`,
`internal/cmd/dispatch.go:1297-1307`).

## Evidence Gathered

| Finding | Source | Evidence |
|---------|--------|----------|
| `start_dispatch` ships `true` in default startup.json | `internal/cmd/install.go:147` | `"startup.json": {... "start_dispatch":true ...}` |
| Default dispatch.json ships EMPTY repos + EMPTY mappings | `internal/cmd/install.go:145` | `"dispatch.json": {"repos":[],"trigger_label":"agentic","notify_on_complete":"manager","mappings":[],"interval_seconds":300,"retry_after_seconds":1800}` |
| `start_dispatch` is read in exactly one place | grep `\.StartDispatch` (non-test) | only `internal/cmd/up.go:330` |
| `af up` calls `startDispatch` when `start_dispatch:true` | `internal/cmd/up.go:330-335` | `if startupCfg.StartDispatch { if dErr := startDispatch(cmd, root, t); ... }` |
| Auto-start is gated to the **blanket** `af up` path only | `internal/cmd/up.go:92,306,330` | `blanket := len(args) == 0`; the dispatch block is inside `if blanket {`; so `af up manager` (positional) skips it |
| `startDispatch` friendly-skips on `ErrNotFound` OR `ErrMissingField` | `internal/cmd/dispatch.go:1327-1332` | `if errors.Is(err, ErrNotFound) || errors.Is(err, ErrMissingField) { print "not configured"; return nil }` |
| Empty `repos` → `ErrMissingField` (also empty mappings, empty trigger_label) | `internal/config/dispatch.go:142-150` | `if len(cfg.Repos)==0 { return ErrMissingField }`; same for trigger_label and mappings |
| `LoadDispatchConfig` runs the validator and propagates its error | `internal/config/dispatch.go:48-64` | reads file, unmarshals, `if err := validateDispatchConfig(&cfg); err != nil { return nil, err }` |
| A VALID config falls through to actually launch the loop | `internal/cmd/dispatch.go:1338-1339` + `1297-1307` | `return launchDispatchSession(...)` → `t.NewSession` + `t.SendKeys(loopCmd)` (the polling loop) |
| Already-running dispatcher is a benign no-op | `internal/cmd/dispatch.go:1322-1325` | `if running { print "already running"; return nil }` |
| Test pins empty-default friendly-skip (no session created) | `internal/cmd/startdispatch_test.go:72-84` | `{"repos":[],...,"mappings":[]}` → no error, asserts NO `NewSession` op |
| Test pins valid-config launch (session + send-keys) | `internal/cmd/startdispatch_test.go:202-217` | populated config → asserts `NewSession` AND `SendKeys` ops |
| The dispatch block is best-effort (never aborts af up) | `internal/cmd/up.go:330-335`, `dispatch.go:1311-1320` | all config outcomes return nil; only a real launch failure sets `allOK=false` |

## Tests Performed

| Test | Command | Result |
|------|---------|--------|
| Confirm `start_dispatch` read sites | `grep -rn "\.StartDispatch" internal/ (non-test)` | Exactly one: `internal/cmd/up.go:330` |
| Confirm `startDispatch`/`launchDispatchSession` callers | `grep -rn "launchDispatchSession\|startDispatch(" internal/cmd` | `startDispatch` called only from `up.go:331`; launch only from `startDispatch` (`dispatch.go:1339`) and `runDispatchStart` (`dispatch.go:1279`) |
| Confirm shipped install defaults | Read `internal/cmd/install.go:139-148` | dispatch.json empty repos+mappings; startup.json `start_dispatch:true` |
| Confirm validation thresholds | Read `internal/config/dispatch.go:140-185` | empty repos / empty trigger_label / empty mappings → `ErrMissingField` |
| Run `TestStartDispatch_EmptyDefaultConfigFriendlySkip` + `..._ValidConfigLaunches` | `go test ./internal/cmd -run ...` | BLOCKED: environment denies test-binary exec (`fork/exec ...: permission denied`, noexec build tmp). Behavior verified by reading the tests, which assert exactly the conclusions above. |

## Conclusion

**VALIDATED.** The concern is real and is the central blocker for issue #73's
goal.

- With the CURRENT shipped defaults, the dispatcher does NOT auto-start polling
  after a fresh setup. `start_dispatch:true` causes a bare `af up` to call
  `startDispatch`, but the empty-`repos`/empty-`mappings` default
  (`internal/cmd/install.go:145`) fails validation with `ErrMissingField`
  (`internal/config/dispatch.go:142-150`), which `startDispatch` treats as the
  friendly "not configured" skip — no session, no polling
  (`internal/cmd/dispatch.go:1327-1332`).
- With a POPULATED default (non-empty `repos`, non-empty `mappings`, non-empty
  `trigger_label`), the load succeeds, `startDispatch` falls through to
  `launchDispatchSession`, and the polling loop IS started by a bare `af up`
  with NO manual `af dispatch start` step
  (`internal/cmd/dispatch.go:1338-1339`, `1297-1307`;
  `internal/cmd/startdispatch_test.go:202-217`). Populating the config does NOT
  change the skip-vs-error machinery — it simply moves the config out of the
  `ErrMissingField` bucket and into the valid bucket.

**Remaining gaps between "populated default config exists" and "dispatcher is
actually polling after fresh setup":**
1. **Path gating** — auto-start fires only on the blanket `af up` (no
   positional args). `af up manager` / `af up <names>` is gated out by the
   `blanket` flag (`internal/cmd/up.go:92,306,330`) and would NOT start
   dispatch. A fresh user who runs `af up manager` instead of `af up` still gets
   no dispatcher.
2. **Repo/mapping cannot be generically shipped** — the `repos` value and the
   label→agent `mappings` are user/repo-specific. A literal populated default in
   `install.go` would point at a placeholder/wrong repo unless the install flow
   personalizes `dispatch.json` (e.g., detects the origin repo) or prompts for
   it. So "populated default" likely requires install-time population, not just
   editing the static literal at `internal/cmd/install.go:145`.
3. **Cross-file agent validity** — a populated mapping must reference agents
   that exist in `agents.json`; `ValidateDispatchConfig`
   (`internal/config/dispatch.go:93-104`) enforces this at the CLI/write path,
   so a shipped mapping must use agents present in the default `agents.json`
   (`manager`/`supervisor`).
