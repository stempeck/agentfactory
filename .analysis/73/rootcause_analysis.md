# Root Cause Analysis: Default dispatch workflow for fresh agentfactory installs (Issue #73)

**Date**: 2026-06-28
**Status**: Synthesized — solution documented
**Problem File**: GitHub issue [stempeck/agentfactory#73](https://github.com/stempeck/agentfactory/issues/73); design contract `.designs/73/problem-summary-73.md`

## Problem Statement

A freshly bootstrapped agentfactory does **not** support label-driven dispatch out of the
box. `af install --init` writes a placeholder `dispatch.json` with empty `repos` and empty
`mappings` (`internal/cmd/install.go:145`), so tagging a GitHub issue dispatches nothing and
the repo name is never injected. A new user must visit the manager and hand-configure
dispatch before any label does anything.

Issue #73 asks for a **baked-in default `dispatch.json`**, populated with the **actual
`org/repository`** at install time plus sensible default label→agent mappings and a workflow,
so a new user can dispatch work purely by labeling GitHub issues — without ever visiting the
manager. The acceptance criterion is broader: a clean setup must let `af sling --agent <x>
"task"` (and the label path) run **autonomously to a pushed PR**, with no `doctor --fix` and
no human intervention, each agent following the rigid step-by-step formula that IS its
identity. The issue explicitly asks for **systemic improvements while addressing the
scenario**.

## Concerns from Problem File

All concerns were independently investigated by a dedicated sub-agent (Phase 2). Verdicts are
evidence-based; full per-concern investigations are in the linked files.

| # | Concern | Verdict | Evidence Link |
|---|---------|---------|---------------|
| 1 | Install-time default `dispatch.json` ships empty `repos` + empty `mappings` (`install.go:145`), so a fresh factory dispatches nothing by label. | **VALIDATED** | [rootcause_concern_1.md](rootcause_concern_1.md) |
| 2 | The actual `org/repository` is never discovered/injected at setup (static literal; no env interrogation). | **VALIDATED** | [rootcause_concern_2.md](rootcause_concern_2.md) |
| 3 | No default label→agent `mappings`; the proposed mappings are struct-valid + semantically correct but the agent names must be **registered in agents.json**. | **VALIDATED** | [rootcause_concern_3.md](rootcause_concern_3.md) |
| 4 | A fresh `af install --init` registers only `manager`+`supervisor`; the four mapped specialist agents are **not registered/provisioned**, so dispatch aborts on validation. | **VALIDATED** | [rootcause_concern_4.md](rootcause_concern_4.md) |
| 5 | Dispatch input-compatibility: do mapped formulas accept a single issue/PR input? | **INVALIDATED** | [rootcause_concern_5.md](rootcause_concern_5.md) |
| 6 | Workflow block validity: does the proposed `feature-workflow` pass `dispatch.go` validation? | **VALIDATED** | [rootcause_concern_6.md](rootcause_concern_6.md) |
| 7 | Dispatcher auto-activation: empty default friendly-skips; a populated default auto-starts on **bare** `af up`, but `af up <names>` (the documented `af up manager`) is gated out. | **VALIDATED** | [rootcause_concern_7.md](rootcause_concern_7.md) |
| 8 | Default semantics of `trigger_label:"agentic"` AND-matching + `remove_trigger_after_dispatch:true` are safe; caveats: issue JSON has a syntax error; dual-label requirement is a UX trap. | **VALIDATED** | [rootcause_concern_8.md](rootcause_concern_8.md) |
| 9 | Broader acceptance — systemic gaps to a **pushed PR** (origin remote + `gh`/push auth) live at the container layer, not in `af install --init`; `doctor` is not a real command. | **VALIDATED** | [rootcause_concern_9.md](rootcause_concern_9.md) |

## Synthesized Root Cause(s)

Based on investigation of 9 concerns:
- **8 concerns VALIDATED** as contributing factors (1, 2, 3, 4, 6, 7, 8, 9)
- **1 concern INVALIDATED** (5 — input-compatibility is a non-issue: all four mapped formulas
  accept a single issue/PR URL input, so none needs an extra `--var`; the label path can run
  them as-is).

The eight validated concerns are **not eight independent bugs** — they form a single causal
chain rooted in one architectural decision, plus two registration/activation gaps that any
"populated default" must close to be coherent.

### Primary Root Cause — The fresh-install config set is a static, structurally-incomplete literal

`af install --init` emits `dispatch.json` from a **hardcoded compile-time string** in the
`starterConfigs` map (`internal/cmd/install.go:145`):
```go
"dispatch.json": `{"repos":[],"trigger_label":"agentic","notify_on_complete":"manager","mappings":[],"interval_seconds":300,"retry_after_seconds":1800}`,
```
A constant string has no access to runtime state, which forces two failures at once and a
cascade behind them:

1. **It cannot contain the repo** → `repos` is empty even though the init cwd is the cloned
   target repo with `origin` set (`quickstart.sh:428`→`:442`, `getWd()`/`os.Getwd()`), and the
   factory already ships a read-only git/gh detection idiom (`runGitDetect`/`detectDefaultBranch`,
   `internal/cmd/detect_default_branch.go:34-73`) that was never generalized to repo-slug
   detection (Concern 2; `grep nameWithOwner` → zero hits).
2. **It is empty (`repos:[]`, `mappings:[]`)** → `validateDispatchConfig` rejects it with
   `ErrMissingField` on both the empty-repos and empty-mappings checks
   (`internal/config/dispatch.go:142-150`) (Concern 1).
3. **Cascade → the dispatcher never starts.** `startup.json` correctly ships `start_dispatch:true`
   (`install.go:147`), so a bare `af up` calls `startDispatch` (`up.go:330-331`), but
   `startDispatch` folds `ErrMissingField` into a friendly "not configured" skip and returns
   without launching the polling loop (`dispatch.go:1327-1339`), proven by
   `TestStartDispatch_EmptyDefaultConfigFriendlySkip` (Concern 7). Net effect: labeling a GitHub
   issue causes **zero dispatch** until an operator hand-edits the file (Concern 1).

This single static literal is the substrate the issue is really about: the default is shipped
*intentionally incomplete* because a static string cannot be personalized, and everything
downstream skips/aborts on that incompleteness.

### Contributing Factor A — Mapped specialist agents are not registered on the fresh-install path

Even if `dispatch.json` were populated with the proposed mappings, dispatch would still **fail**,
not no-op. `af install --init` registers only `manager`+`supervisor` in `agents.json`
(`install.go:143`); the four mapped agents (`rapid-soldesign-plan`, `rapid-implement`,
`ultra-review`, `rapid-increment`) are never registered — the only registrar,
`af formula agent-gen` (via `agent-gen-all.sh`), runs only under `af install --agents`, which the
fresh path never invokes (Concern 4). The cross-file validator `ValidateDispatchConfig` rejects
any mapping referencing an unknown agent and **aborts the entire dispatch cycle**
(`internal/config/dispatch.go:101-102`, `internal/cmd/dispatch.go:146-148`), and even past that,
`af sling --agent` fails at `internal/cmd/sling.go:261`. The mappings are otherwise correct —
struct-valid, right issue/PR source, agent-name == formula-name by convention (Concern 3), and
the proposed `feature-workflow` passes every workflow rule **once the agents exist** (Concern 6).
So the dispatch.json default and the agent roster must be made consistent **by construction**:
shipping the mappings *requires* seeding the four agents into the default `agents.json`.

### Contributing Factor B — Dispatcher auto-start is gated to the bare `af up` path

Auto-start of the dispatcher fires **only** on the blanket `af up` (no positional args); the
block is inside `if blanket {` (`internal/cmd/up.go:92,306,330`). The documented setup flow uses
`af up manager` (positional), which is gated out — so even with a valid populated config and
registered agents, a user who follows the documented step gets **no dispatcher** and must still
visit the manager. To honor "tag issues without visiting the manager" via the documented flow,
auto-start must fire on that path too (the launch is idempotent — already-running is a benign
no-op, `dispatch.go:1322-1325`) (Concern 7).

### Validated supporting facts (defaults are safe, with two caveats)

- The default **semantics are correct and safe** (Concern 8): `trigger_label:"agentic"` gates the
  GitHub query and mapping labels AND-match (so a user applies `agentic` + a mapping label);
  `remove_trigger_after_dispatch:true` removes only the trigger as a safe one-shot; all field
  names map 1:1 to the `DispatchConfig` json tags. **Two caveats**: (a) the issue's proposed JSON
  literal is malformed (`"labels": ["rapid-plan` is missing its closing quote/bracket) and must
  be corrected; (b) tagging only a mapping label *without* `agentic` does nothing — a non-obvious
  UX trap to document.

### Out-of-scope-but-named systemic gaps (per the "systemic improvements" mandate — Concern 9)

Fixing `dispatch.json` is **necessary but not sufficient** for the broadest reading of acceptance.
The formula tail steps reach a PR via `git push -u origin` + `gh pr create`
(`rapid-implement.formula.toml:803/:881`, `rapid-soldesign-plan.formula.toml:496/:527`), which
hard-depend on an `origin` remote and `gh`/push auth. These are provisioned at the **container
layer** by `quickdocker.sh` (`gh auth login --with-token` + `gh auth setup-git`, `:557/:567`),
**not** by `af install --init`. On the *intended* quickdocker fresh-container path these are
already satisfied, so the only remaining gap is the dispatch config + agent registration (this
fix). On a bare `af install --init`-only repo (no remote, no auth), autonomy to a PR is also
blocked by those two gaps — named here, recommended as follow-ups, not solved by this change.
Note: **"doctor" is not a real command** in the codebase; the acceptance clause "no `doctor
--fix`" is a test-guarded property (`e2e_sling_test.go:67-69`), not a dependency to remove — so
that worry is moot.

## Fishbone Diagram

```
                                          [Fresh agentfactory cannot dispatch
                                           work by GitHub label out of the box]
                                                          ▲
        ┌──────────────────────────┬──────────────────────┼───────────────────────┬─────────────────────────┐
        │                          │                       │                       │                         │
  [VALIDATED]                [VALIDATED]             [VALIDATED]             [VALIDATED]               [INVALIDATED]
  C1/C2  Static, empty       C3/C4  Mapped agents    C7  Auto-start gated    C8  Default semantics    C5  Input-compatibility
  dispatch.json literal      not registered in       to bare `af up` only    safe (2 caveats)         (ruled out — all 4
  (install.go:145):          agents.json             (up.go:92/306/330);     • issue JSON malformed   formulas take a single
  • cannot inject repo       (install.go:143;        documented `af up       • agentic+label dual     issue/PR URL input;
    (Concern 2)              only `--agents`          manager` skips it       requirement is a UX      sling.go:472-483)
  • empty → ErrMissingField  registers specialists)  → no dispatcher on      trap to document
    (dispatch.go:142-150)    → ValidateDispatchConfig the documented path
  • startDispatch friendly-  aborts cycle
    skips, never launches    (config/dispatch.go:
    (dispatch.go:1327-1339)  101-102; sling.go:261)
        │                          │                                                                          │
        └─ root: config born from a static compile-time string that cannot be personalized ─┘        [ruled out — not a blocker]

   [Out-of-scope-but-named — Concern 9]: origin remote + gh/push auth provisioned only at the
   container layer (quickdocker.sh:557/567), not by `af install --init`. "doctor" is not a real command.
```

## Solution

**One approach (chosen).** Bootstrap a **complete, repo-personalized** dispatch configuration at
factory-init, keep it consistent with a **seeded agent roster** by construction, and **activate**
it on the documented startup path — using the in-code drift-proof constructor idiom already
established by `factory.json` (`DefaultFactoryConfigJSON()`). Repo detection degrades safely to
today's empty `repos:[]` when there is no GitHub remote, so non-GitHub repos see no regression.

This is the single solution; rejected alternatives are noted inline (e.g., making `quickstart.sh`
run `af install --agents` to register the specialists is rejected because it only fixes the
quickstart path, not bare `af install --init`, and is heavier; threading the slug through
`quickdocker.sh → quickstart.sh → af install --init` is rejected because the binary can detect it
from a cwd it already resolves — fewer files, covers every init path).

### Files to Modify

| File | Change |
|------|--------|
| `internal/config/dispatch.go` | Add `DefaultDispatchConfigJSON(repoSlug string) string` — marshals a `DispatchConfig` with the default 4 mappings + `feature-workflow` + `remove_trigger_after_dispatch:true`, and `repos:[repoSlug]` (or `[]` if empty). Single on-disk-default source (mirrors `DefaultFactoryConfigJSON`). |
| `internal/cmd/detect_default_branch.go` | Add `var detectRepoSlug = func(workDir string) string` on the existing `runGitDetect` ADR-009 seam: `gh repo view --json nameWithOwner -q .nameWithOwner`, fallback to parsing `git remote get-url origin`; validate `owner/repo` shape, else `""`. |
| `internal/cmd/install.go` | In `runInstallInit` (`:139-157`): replace the static `dispatch.json` literal (`:145`) with `config.DefaultDispatchConfigJSON(detectRepoSlug(getWd()))`; replace the `agents.json` literal (`:143`) with a `config.DefaultAgentsConfigJSON()` that also registers the four dispatch-referenced specialists. Keep write-if-absent idempotency. |
| `internal/cmd/up.go` | Move the `if startupCfg.StartDispatch { startDispatch(...) }` block (`~:330`) out of the `blanket`-only gate (`:92,306`) so the dispatcher auto-starts on **any** `af up` (incl. documented `af up manager`); launch is idempotent (`dispatch.go:1322-1325`). |
| `internal/cmd/install_integration_test.go` (or new `dispatch_default_test.go`) | Drift-interlock test (see Enforcement). |
| `USING_AGENTFACTORY.md` | Document the populated default + the **dual-label** requirement (`agentic` + a mapping label) to defuse the UX trap; document the label set and `feature-workflow`. |

### Implementation Steps

**1. Drift-proof default-dispatch constructor** — `internal/config/dispatch.go` (verify field
names against the `DispatchConfig`/`DispatchMapping`/`Workflow` json tags at `:19-45`):
```go
// DefaultDispatchConfigJSON returns the on-disk default dispatch.json, personalized with the
// detected repo slug. An empty slug yields repos:[] so a non-GitHub repo still loads as the
// friendly "not configured" skip (no regression vs. today's behavior).
func DefaultDispatchConfigJSON(repoSlug string) string {
    repos := []string{}
    if repoSlug != "" {
        repos = []string{repoSlug}
    }
    cfg := DispatchConfig{
        Repos: repos, TriggerLabel: "agentic", NotifyOnComplete: "manager",
        IntervalSeconds: 300, RetryAfterSeconds: 1800, RemoveTriggerAfterDispatch: true,
        Mappings: []DispatchMapping{
            {Labels: []string{"rapid-plan"},     Source: "issue", Agent: "rapid-soldesign-plan"},
            {Labels: []string{"rapid-engineer"}, Source: "issue", Agent: "rapid-implement"},
            {Labels: []string{"pr-review"},      Source: "pr",    Agent: "ultra-review"},
            {Labels: []string{"pr-iterate"},     Source: "pr",    Agent: "rapid-increment"},
        },
        Workflows: []Workflow{{Label: "feature-workflow", Phases: []string{"rapid-plan", "rapid-engineer"}}},
    }
    b, _ := json.Marshal(cfg) // struct is statically valid; marshal cannot fail
    return string(b)
}
```
This also fixes the issue's malformed JSON (`"labels": ["rapid-plan` → proper `["rapid-plan"]`)
by construction — the literal no longer exists.

**2. Repo-slug detection** — `internal/cmd/detect_default_branch.go`, mirroring `detectDefaultBranch`
(`:52-73`); the parse step reuses the exact pattern already in `quickdocker.sh:41-50`:
```go
var detectRepoSlug = func(workDir string) string {
    if s := runGitDetect(workDir, "gh", "repo", "view", "--json", "nameWithOwner", "-q", ".nameWithOwner"); isRepoSlug(s) {
        return s // canonical org/repo
    }
    s := parseRepoSlug(runGitDetect(workDir, "git", "remote", "get-url", "origin")) // strip scheme/host/.git
    if isRepoSlug(s) {
        return s
    }
    return "" // no GitHub remote → caller ships repos:[] (friendly-skip)
}
```

**3. Init wiring** — `internal/cmd/install.go`, in the `starterConfigs` block (`:139-157`):
```go
"agents.json":   config.DefaultAgentsConfigJSON(), // manager+supervisor + the 4 dispatch specialists
"dispatch.json": config.DefaultDispatchConfigJSON(detectRepoSlug(getWd())),
```
where `DefaultAgentsConfigJSON()` adds, alongside manager/supervisor, four `{"type":"autonomous",
"formula":"<name>"}` entries named exactly `rapid-soldesign-plan`, `rapid-implement`,
`ultra-review`, `rapid-increment` (agent-name == formula-name per the `agent-gen` convention).
Their role templates are already embedded (`internal/templates/roles/{rapid-soldesign-plan,
rapid-implement,ultra-review,rapid-increment}.md.tmpl`), so `af prime` renders each specialist
identity with no further provisioning. (Optional: add the four to `messaging.json`'s `all` group.)

**4. Activate on the documented path** — `internal/cmd/up.go`: hoist the dispatch-start out of the
`blanket` gate so `af up manager` (and any `af up`) starts the dispatcher when `start_dispatch:true`:
```go
// was inside `if blanket { ... }`; now runs for every `af up` (idempotent — already-running no-ops)
if startupCfg.StartDispatch {
    if dErr := startDispatch(cmd, root, t); dErr != nil { allOK = false }
}
```

**5. Drift-interlock test + docs** — see Enforcement and Files to Modify.

### Enforcement Level

| Step | Level | Notes |
|------|-------|-------|
| Build `dispatch.json` from `DefaultDispatchConfigJSON` (no static literal) | **Interlock** | Struct + `json.Marshal` enforce shape; a typo'd field or malformed JSON cannot ship |
| Repo-slug detection → empty-slug falls back to `repos:[]` | **Runtime guard** | Best-effort; degrades to today's friendly-skip — no regression for non-GitHub repos |
| Seed the 4 specialists into default `agents.json` | **Interlock (by construction)** | dispatch.json mappings resolve because the agents exist in the same install output |
| Drift-interlock unit test | **Interlock (Poka-yoke)** | Test asserts every mapping/phase agent in `DefaultDispatchConfigJSON` exists in `DefaultAgentsConfigJSON`, and that the populated default round-trips `LoadDispatchConfig`+`ValidateDispatchConfig`; and empty-slug still yields the friendly-skip. Makes it impossible to merge a default that references an unregistered agent or fails validation. |
| Ungate dispatcher auto-start on positional `af up` | **Interlock** | Documented `af up manager` flow now activates dispatch deterministically |
| Document dual-label requirement | **Advisory** | Defuses the UX trap (see code-level enforcement below) |

**Code-level enforcement for the one Advisory item (the dual-label UX trap):** rather than relying
only on docs, add a runtime hint in the dispatch cycle — when an item carries a known *mapping*
label but lacks `trigger_label`, log `issue #N has label 'rapid-plan' but not 'agentic' — not
dispatched` (a Poka-yoke nudge), or optionally treat a configured mapping label as a sufficient
trigger. The interlock above already guarantees the *config* is valid; this guards the *operator's
labeling* mistake.

### Verification Steps

1. `make build && make test` — all pass, including the new `detectRepoSlug`, `DefaultDispatchConfigJSON`,
   and drift-interlock tests; existing `TestStartDispatch_EmptyDefaultConfigFriendlySkip` and the
   install-integration "dispatch.json is valid JSON" test stay green.
2. Unit (mirror `detectDefaultBranch` tests): canned `runGitDetect` outputs → `detectRepoSlug`
   returns the slug / `""` correctly across `gh` and `git remote` fallback paths.
3. Unit: `DefaultDispatchConfigJSON("org/repo")` → `LoadDispatchConfig` + `ValidateDispatchConfig`
   succeed against `DefaultAgentsConfigJSON()`; `DefaultDispatchConfigJSON("")` → `repos:[]` →
   friendly-skip preserved.
4. Behavioral (quickdocker/quickstart fresh container): after `af up`, `af dispatch status --json`
   shows the loop running with `repos:["<actual org/repo>"]`; labeling an issue `agentic`+`rapid-plan`
   slings `rapid-soldesign-plan`; `agentic`+`feature-workflow` walks `rapid-plan`→`rapid-engineer`.
5. Confirm the documented `af up manager` step now starts the dispatcher (previously gated out).

### Code Convention Issues

- **Inconsistent default-config sourcing.** The fresh-install set mixes a drift-proof constructor
  (`factory.json` via `DefaultFactoryConfigJSON()`) with brittle inline literals (`agents.json`,
  `dispatch.json`, `messaging.json`, `startup.json`). This solution migrates `agents.json` and
  `dispatch.json` to `config.Default*JSON()` constructors (same issue #371 Gap-6 rationale already
  cited at `install.go:140-141`); `messaging.json`/`startup.json` are recommended follow-ups.
- **`LoadDispatchConfig` lacks `DisallowUnknownFields`** (Concern 8): a future typo'd key is silently
  dropped. One-line robustness follow-up; not required for this fix.

### Out-of-scope follow-ups (named per the "systemic improvements" mandate — Concern 9)

Reaching a *pushed PR* additionally needs an `origin` GitHub remote and `gh`/push auth, which are
provisioned at the **container layer** (`quickdocker.sh:557/:567`), **not** by `af install --init`.
On the intended quickdocker path these are already satisfied — so this fix completes the
out-of-the-box label→autonomy chain there. On a bare `af install --init`-only repo, file follow-ups
to surface a clear preflight error (or provisioning) for missing remote/auth. **"doctor" is not a
command** in this codebase (the "no `doctor --fix`" acceptance clause is a test-guarded property,
`e2e_sling_test.go:67-69`), so there is no doctor dependency to remove.
