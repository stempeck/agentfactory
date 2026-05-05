# Cross-cutting idioms

This document is the canonical list of code idioms that cut across multiple
subsystems. **Before proposing a design that touches any of these concerns,
look here first.** An option that deviates from an idiom is a named
deviation, not an accident — the design must justify it.

Rules for entries:
- Each idiom has ≥2 call sites (single-site patterns are demoted to
  subsystem-local, not idioms).
- Every call site is enumerated with a file:line anchor + axis leveraged.
- Each idiom ends with a *deviation detector* so reviewers can spot drift.

---

## 1. `Filter{IncludeAllAgents: true}` — the actor-overlay opt-out

**Shape:**
```go
_, err := store.List(ctx, issuestore.Filter{
    Assignee:         explicitActor,      // may be empty
    IncludeAllAgents: true,                 // bypass default overlay
    // ... other fields
})
```

**Semantic:** When `*MCPStore` is constructed with an actor (the typical
production path — `internal/issuestore/mcpstore/mcpstore.go:199-214`), `List`
auto-scopes to that actor's issues unless the caller explicitly sets
`IncludeAllAgents: true`. Set it when the caller **already** applies its
own scoping (an explicit `Assignee`, a cross-agent operational query like
step discovery, or a user-invoked `bead --all`). Leave it false in every
per-agent view. Contract pinned at
`internal/issuestore/contract.go:324-352`
(`Filter_IncludeAllAgents_admits_other_agents` + `Filter_IncludeAllAgents_false_hides_other_agents`).

**Enforcement:** Partially mechanical, partially convention.
- Mechanical: `internal/cmd/step_test.go:398-479`
  (`TestStepCurrent_IncludeAllAgentsRequired`) is a white-box spy that
  fails if `step.go` forgets `IncludeAllAgents: true` — the **C14 regression
  pin**.
- Mechanical: `internal/cmd/done_integration_test.go:17-109` runs an
  end-to-end multi-step formula against mcpstore and asserts `WORK_DONE`
  fires only on the final step — would misfire without the opt-out in
  `done.go:100, 136, 369`.
- Mechanical: the adapter contract suite
  (`internal/issuestore/contract.go:324-352, 840-847`) asserts the axis in
  both directions across every Store implementation.
- Convention at every other call site.

**Call sites (production):**

| File:line | Axis leveraged | Notes |
|-----------|----------------|-------|
| `internal/cmd/done.go:100` | Cross-agent done-cascade: close all steps blocking this formula, regardless of assignee. | Commit `63307bb` (2026-04-18) deleted a design that tried to bypass the overlay at the mcpstore seam instead of using this idiom. |
| `internal/cmd/done.go:136` | Same cascade, second phase. | — |
| `internal/cmd/done.go:369` | WORK_DONE-eligibility discovery for sling-created steps (no Assignee). | Without this, multi-step formulas misfire WORK_DONE at step 1 (commit `e41342d`). |
| `internal/cmd/step.go:139` | Step discovery for the active formula — step beads are created by `sling.instantiateFormula` without an Assignee. | C14 regression pin. |
| `internal/cmd/prime.go:422` | Role-context formula lookup at agent startup — finds the current step regardless of who authored it. | — |
| `internal/cmd/bead.go:294` | Conditional: set to true only when the CLI caller passed `--all`. | Default is per-actor view. |

**Call sites (tests + contract):**

| File:line | Role |
|-----------|------|
| `internal/issuestore/contract.go:233, 258, 287, 311, 328, 488, 517, 558, 573, 615, 840` | RunStoreContract sub-tests |
| `internal/issuestore/mcpstore/concurrency_test.go:61, 188, 205` | Concurrency flake detection |
| `internal/issuestore/mcpstore/benchmark_test.go:188, 221, 272` | AC-12 latency benchmarks |
| `internal/cmd/integration_test.go:286` | E2E mail round-trip |
| `internal/cmd/done_integration_test.go:109` | Multi-step formula E2E |

**Deviation detector:**
- A new call to `store.List` / `store.Ready` / `store.RenderList` that omits
  `IncludeAllAgents` in any code path where the caller does not supply an
  explicit `Assignee`: if the query is logically cross-agent, the omission
  is a bug.
- A proposal that "bypasses the overlay at the adapter seam" instead of
  passing `IncludeAllAgents: true` at the call site — that design was
  tried and rejected (commit `63307bb`, one day old at time of writing).
- Changing the mcpstore default-overlay logic at
  `mcpstore.go:199-214` is a redesign of this idiom, not an extension.
- After issue #125, `internal/mail/mailbox.go` no longer uses this idiom:
  an own-mailbox read scoped by an explicit `Assignee` is not a cross-actor
  query, because both adapters now agree that the explicit `Assignee`
  suppresses the actor overlay (pinned by
  `RunStoreContract.ExplicitAssigneeWinsOverActorOverlay` in
  `internal/issuestore/contract.go`). A future `Assignee` +
  `IncludeAllAgents: true` co-occurrence is therefore a defect signal, not
  a prior-art pattern; it must carry an explicit cross-actor justification
  in the preceding comment.

**If you are proposing to extend/bypass this idiom, you must:**
1. Name the call site that currently uses the idiom and why your new shape
   can't.
2. Explain what axis (other than the Filter opt-out) scopes the cross-agent
   query.
3. Update `internal/issuestore/contract.go` so the new invariant is pinned
   across all adapters.
4. Demonstrate the C14 regression pin still passes
   (`go test ./internal/cmd/ -run TestStepCurrent_IncludeAllAgentsRequired`).

---

## 2. `resolveAgentName` three-tier identity derivation

**Shape:**
```go
name, err := resolveAgentName(cwd, factoryRoot)
// Tier 1: cwd path → candidate name
// Tier 2: validate candidate against agents.json membership
// Tier 3: on membership miss OR path-detection error → consult AF_ROLE env var
```

**Semantic:** Agent identity is derived from *trusted context*, not from
caller-supplied flags. The canonical implementation is
`internal/cmd/helpers.go:55-102` (`resolveAgentName`). AF_ROLE is populated
only by `internal/session/session.go:116` (via tmux `SetEnvironment`) and
`internal/session/session.go:159` (via shell export in the session-launch
`claude` command) — both trusted sources. There is **no** user-facing
`--as` / `--actor` / `--from` flag. Adding one is forbidden.

**Enforcement:**
- `internal/cmd/helpers.go:46-54` — load-failure of `agents.json` is treated
  as a membership miss so AF_ROLE is consulted (issue #88, commit
  `8cff1bf`).
- `internal/cmd/bead_test.go:121-158` and `internal/cmd/sling_test.go:230-282`
  pin the "wrong-but-no-error → AF_ROLE wins; no AF_ROLE → error" behavior.
- No configuration mechanism exists to override (verified: no CLI flag
  accepts `--as`, no env var named `AF_FORCE_*` is honored).

**Call sites:**

| File:line | Axis / reason |
|-----------|---------------|
| `internal/cmd/helpers.go:55` | Owner / canonical implementation |
| `internal/cmd/prime.go:185` | Startup role injection — "who am I priming?" |
| `internal/cmd/sling.go:161` | Formula instantiation — "who is creating these step beads?" |
| `internal/cmd/sling.go:270` | Dispatch — "who is dispatching this task?" |
| `internal/cmd/sling.go:604` | `detectAgentName` wrapper for the sling command |
| `internal/cmd/bead.go:224` | Bead creation — "who is creating this issue?" |
| `internal/cmd/up.go:68` | Session start — "who is the caller?" (for parent-context) |
| (also: `mail.go`, `handoff.go` via the same path — see cmd.md) | — |

**Deviation detector:**
- A new command that takes an `--as <name>` flag or `AF_FORCE_ROLE` env var.
- Any code path that bypasses `resolveAgentName` and reads `os.Getenv("AF_ROLE")` directly for identity.
- A proposal that "lets the caller override identity for this edge case"
  — the memory
  (`feedback_no_agent_overrides.md`) and user statement
  (*"agents find them and use them"*) make this a hard wall.

**If you are proposing to extend this idiom:**
1. AF_ROLE written by a **new** trusted source (not a user-facing
   CLI flag) — e.g. a new harness that sets the env var before launching
   `claude`. Document the trust path at the source.
2. Never accept caller-supplied identity. Structural fixes only.

---

## 3. Config-load pair: `LoadAgentConfig` then `LoadMessagingConfig`

**Shape:**
```go
agentsCfg, err := config.LoadAgentConfig(config.AgentsConfigPath(root))
if err != nil { ... }
msgCfg, err := config.LoadMessagingConfig(msgPath, agentsCfg)
```

**Semantic:** `LoadMessagingConfig(path, agents *AgentConfig)` takes the
loaded agents config as a second arg because it cross-validates group
members against agents.json at load time
(`internal/config/config.go:81-82`). The load order is forced by the
function signature — there is no way to load messaging.json without
agents.json already in hand.

**Call sites (AgentsConfigPath pair used in 13 places, full list in
`subsystems/cmd.md`):**

| File:line | Notes |
|-----------|-------|
| `internal/mail/router.go:32, 38` | The canonical pair — both loaders called back-to-back |
| `internal/cmd/prime.go:83, 112, 191` | Three independent prime paths |
| `internal/cmd/install.go:223` | Install validation |
| `internal/cmd/sling.go:223, 623` | Formula + dispatch |
| `internal/cmd/up.go:44`, `down.go:42`, `attach.go:39`, `formula.go:165, 287`, `mail.go:396`, `dispatch.go:111, 115, 327`, `helpers.go:79`, `worktree.go:157` | All use `LoadAgentConfig(AgentsConfigPath(root))` exactly |

**Enforcement:**
- Type system: `LoadMessagingConfig` signature forces the pair order.
- `internal/config/config_test.go:130-145`
  (`TestLoadMessagingConfig_GroupMemberNotInAgents`) pins the cross-validation.

**Deviation detector:**
- A new caller that reads `messaging.json` via its own loader (bypassing
  `LoadMessagingConfig`) — the group-member cross-validation is lost.
- A new caller that constructs an `AgentsConfigPath` as a path literal
  rather than via the helper — silent drift when the path convention
  changes (e.g. the `ef0ecd9` `.agentfactory/` relocation).

---

## 4. Package-var seam for test-swappable collaborators

**Shape:**
```go
// package-level indirection
var newIssueStore = func(ctx, dir, actor string) (issuestore.Store, error) {
    return mcpstore.New(ctx, dir, actor)
}
// tests override this var; production uses the default
```

**Semantic:** A small number of production seams are package-level vars,
not direct calls. Tests swap them for fakes. This is the codebase's
idiom for "keep the production wiring direct; keep test override cheap."

**Call sites:**

| File:line | Seam | Origin | Notes |
|-----------|------|--------|-------|
| `internal/cmd/helpers.go:17-24` | `newIssueStore` | commit `c93f9ef` | The issuestore seam. Production returns mcpstore; tests return memstore or fakes. |
| `internal/cmd/sling.go:616` | `launchAgentSession` | commit `c3cf1f1` | The tmux/claude seam. Without this, dispatch tests hang for 4 minutes. |

**Enforcement:** Convention — no gate stops a developer from calling
`mcpstore.New` directly.

**Deviation detector:**
- A new cross-subsystem seam in `internal/cmd/` that imports the dependency
  directly (e.g. `mcpstore.New`, `tmux.NewSession`) without a package-var
  indirection, AND has tests that need to swap it. Check `install.go:126`
  first — that is the **one documented bypass** (bootstrap banner, no test
  swap needed).

**If you are adding a new seam:** Follow this shape. Name the var after the
operation, not the dependency (`newIssueStore`, not `makeMCPStore`).

---

## 5. Embedded-assets source-of-truth + drift test

**Shape:**
- Asset lives on disk under a source directory (`hooks/`,
  `internal/claude/config/`, `internal/templates/roles/`).
- Sibling directory is embedded at build time via `//go:embed`.
- A drift test asserts byte-equality between the source and the embed.

**Semantic:** Installable assets must have a single source of truth, but
Go's `embed` directive requires the file be inside the Go package tree.
The convention is to keep the *editable* source outside the package and
mirror it into the package with a drift-detection test.

**Call sites:**

| Pair | Embed | Drift test |
|------|-------|------------|
| `hooks/*.sh, *.txt` ↔ `internal/cmd/install_hooks/*` | `internal/cmd/install.go:18` (`//go:embed install_hooks/*`) | `internal/cmd/install_hooks_drift_test.go:30-56` (`TestInstallHooks_NoDrift`) — byte-equality assertion |
| `internal/claude/config/*.json` | self-embedded (`internal/claude/settings.go`) | `internal/claude/settings_test.go` |
| `internal/templates/roles/*.md.tmpl` | self-embedded | `internal/templates/templates_test.go` |

**Enforcement:** Mechanical (drift test) for hooks; convention for the
claude/templates trees (self-embedded, so drift is impossible for those).

**Deviation detector:**
- A new asset added to `hooks/` without a corresponding mirror in
  `internal/cmd/install_hooks/` — `TestInstallHooks_NoDrift` will catch it.
- A proposal to read hooks from disk at runtime instead of embedding —
  breaks the `go install` single-binary deploy model.

---

## 6. `${AF_ROLE:-<fallback>}` pattern in rendered shell hooks

**Shape (bash):**
```bash
ROLE="${AF_ROLE:-$(basename "$(pwd)")}"
FACTORY_ROOT="${AF_ROOT:-$(af root)}"
```

**Semantic:** Rendered shell hooks (quality-gate, fidelity-gate) must
prefer the trusted-context env var (`AF_ROLE`, `AF_ROOT` — written by
`session.Manager`) before falling back to cwd-derived or CLI-derived
values. The fallback exists so a hook running outside an `af up`-launched
session (e.g. direct user invocation) still works; the primary path is
the env var.

**Call sites:**

| File:line | Variable |
|-----------|----------|
| `hooks/quality-gate.sh:9` | `AF_ROLE` + `AF_ROOT` |
| `hooks/fidelity-gate.sh:14` | `AF_ROLE` + `AF_ROOT` |

**Enforcement:**
- `internal/cmd/hook_envvar_test.go:21-57`
  (`TestHookScripts_UseEnvVarFallback`) enforces four substring rules:
  `AF_ROLE:-` must be present, `AF_ROOT:-` must be present, bare
  `FACTORY_ROOT=$(af root` is forbidden, `af root` must remain as fallback.
- Drift test (see idiom #5) ensures the rendered copies match the source.

**Deviation detector:**
- A new rendered hook that does not use the `${VAR:-fallback}` pattern —
  `TestHookScripts_UseEnvVarFallback` must be extended to cover it.
- A proposal that hardcodes an assumption about cwd (e.g.
  `basename $(pwd)` without the env var preference) — fails in worktree
  subdirs and under `af dispatch` (constraint C12, commit `d053e5e`).

---

## 7. `Status.IsTerminal()` as the single "is-done?" gate

**Shape:**
```go
if iss.Status.IsTerminal() { ... }   // closed or done
```

**Semantic:** The 6-status enum (open, hooked, pinned, in_progress,
closed, done) has two terminal states. `Status.IsTerminal()` is the only
sanctioned way to ask "is this issue finished?" — no caller compares
against a sentinel value like `"closed"`. This is the D11/C-1 fix.

**Call sites:**

| File:line | Role |
|-----------|------|
| `internal/issuestore/store.go` | Owner / enum definition, `IsTerminal()` impl |
| `internal/mail/translate.go:25` | `Read: iss.Status.IsTerminal()` — mail read/unread flag |
| `internal/issuestore/store_test.go:10` | `TestStatusIsTerminal` pinning the policy |
| `internal/mail/translate_test.go:13` | Six-status matrix |
| `internal/issuestore/contract.go:22` (Gotcha 9 / D11 / C-1) | Contract test  |

**Enforcement:** Convention + targeted tests. No lint rule blocks a string
comparison, but the D11/C-1 fix (commit `045c1e1`) explicitly replaced
`bm.Status == "closed"` with `Status.IsTerminal()` — reintroducing a
string comparison is the regression.

**Deviation detector:**
- `string(iss.Status) == "closed"` or `iss.Status == StatusClosed` without
  also covering `StatusDone`.
- A new status added to the enum without updating `IsTerminal()`.

---

## 8. Endpoint-file rendezvous under `.runtime/`

**Shape:**
- Server writes a JSON file at a well-known path (`.runtime/mcp_server.json`)
  containing its host:port + secret.
- Client reads the file to locate the server.
- Companion lockfile (`.runtime/mcp_start.lock`) serializes server startup
  via PID-based `internal/lock`.

**Semantic:** Cross-process rendezvous without requiring hardcoded ports
or service discovery. Works on any posix filesystem.

**Call sites:**

| File:line | Role |
|-----------|------|
| `py/issuestore/server.py` (bind + endpoint file emission) | Writer |
| `internal/issuestore/mcpstore/client.go` (endpoint read) | Reader |
| `internal/issuestore/mcpstore/lifecycle.go:49-60` | Startup serialization via `lock.NewWithPath(".runtime/mcp_start.lock")` |

**Enforcement:** Convention for the endpoint file; mechanical for the
lock (PID-based with dead-reclaim — `internal/lock/lock.go:17-115`).

**Deviation detector:**
- A new in-process or cross-process seam under `.runtime/` that doesn't
  use the lock for startup serialization.
- A new MCP-like server added with a hardcoded port instead of
  endpoint-file emission.

**Note:** Currently 1 live pair (py-issuestore ↔ mcpstore). Included here
because the lock primitive (`NewWithPath`) was added explicitly to enable
this pattern (commit `ef0411c`) and the runtime-dir convention is a
promoted idiom for any future cross-process seam.

---

## 9. Issue-tag anchors in comments (`C-n`, `D-n`, `H-n`, `R-*`, `Gate-n`, `AC-n`)

**Shape:** Comments cite formal tags from design docs:
```go
// C-14 regression pin: step.go must pass IncludeAllAgents: true
// H-4/D15 atomic-write invariant: persistFormulaCaller must run first
// R-SEC-1: bind loopback only
```

**Semantic:** Design-doc anchors that survive in code so a reader can
trace a piece of code back to its originating constraint. **These anchors
are load-bearing — do not delete them during refactors.** They look like
noise but they're the only durable link between the code and its design
rationale.

**Tag schemes observed:**

| Scheme | Meaning (derived) | Examples |
|--------|-------------------|----------|
| `C-\d+` / `C\d+` | Constraint | C-1, C-7, C-8, C-10, C12, C13, C14, C-16, C-18 |
| `D\d+` | Design decision | D1, D6, D11, D13, D14, D15 |
| `H-\d+` | Hazard | H-2, H-3, H-4 |
| `R-[A-Z]+-\d+` | Risk by category | R-API-2, R-API-5, R-DATA-3, R-INT-3, R-INT-6, R-INT-9, R-INT-10, R-SEC-1 |
| `Gate-\d+` | Gate (enforcement point) | Gate-4 |
| `AC-\d+` | Acceptance criterion | AC-1, AC-2, AC-5, AC-8, AC-11, AC-12 |
| `CR-\d+` | Change requirement | CR-1 |
| `Gotcha \d+` / `Gotcha #\d+` | Documented pitfall | Gotcha 3, 9, 11, 12, 22 |

**Call sites:** Pervasive across `internal/cmd/`, `internal/issuestore/`,
`internal/mail/`, `internal/formula/vars.go:65` (CR-1), and the mcpstore
adapter. Every subsystem doc's "Formal constraint tags" section enumerates
them.

**Deviation detector:**
- A refactor that deletes a `// C-14` or `// H-4/D15` anchor while the
  referenced invariant is still enforced: silent loss of traceability.
- A new design that introduces a constraint and gets merged without
  anchoring it in the code it constrains.

**If you are proposing to reorganize a subsystem:** Before deletion,
grep for the anchor (`grep -rn 'C-14\|H-4' internal/`) and confirm the
referenced invariant is either (a) still enforced and the anchor should
move with it, or (b) no longer applicable and the anchor should be
deleted *along with a note in `gaps.md`*.

---

## Not-promoted patterns (demoted to subsystem-local)

These appeared similar across subsystems but on inspection only occur once
in production; listed here so future investigations don't chase them as
idioms:

- **Worktree cotenancy adoption (C-7):** one production site
  (`internal/worktree/worktree.go:326-356`). Subsystem-local invariant.
- **memstore empty-assignee carve-out:** one site
  (`internal/issuestore/memstore/memstore.go:168`). Explicitly test-only;
  see the caveat in `subsystems/issuestore.md`.
- **PID-liveness pattern:** shared shape (`processExists(pid)` check) but
  only two uses (`internal/lock/lock.go:25, 115` and mcpstore lifecycle).
  Borderline; promoted above as part of idiom #8.
