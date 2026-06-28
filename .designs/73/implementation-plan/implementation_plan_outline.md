# Implementation Plan Outline: Baked-in default `dispatch.json` for zero-touch label-triggered autonomy

**Date**: 2026-06-28
**Source**: `.designs/73/design-doc.md` (issue #73 / PR #74)
**Purpose**: Self-contained phase extraction guide for creating focused
             IMPLREADME_PHASE{X}.md files
**Usage**: Phases are extracted one at a time by the design-phase-impl agent
           (`af sling --agent design-phase-impl "<PR-or-branch with this file>"`),
           which always extracts the NEXT pending phase

---

## How To Use This Document

Each phase below is a **self-contained extraction unit**. Workflow:

1. `af sling --agent design-plan-impl "<PR-or-branch with design-doc.md>"` — produces this outline (Mode A) ✓ (this document)
2. Human reviews this outline (optionally `/peer-review {this_file}` first)
3. `af sling --agent design-phase-impl "<PR-or-branch containing this file>"` — extracts the NEXT pending phase into an IMPLREADME (Mode B)
4. Use the phase's **Recommended Skill** to implement
5. Repeat steps 3-4 until every phase has an IMPLREADME and an implementation
   (Skill-level equivalent of step 3, used by automated loops like soldesign-engineer: `/design-plan-impl {this_file} {N}`)

**Phase dependency chain:**

```
        ┌─────────────────────────────────────────────────────────────┐
        │ PHASE 1  Single-source default builder + repo discovery +    │
        │          agents seed   (K1 K2 K3 K4 K5 + fix existing test)  │
        │          foundational — depends on nothing new               │
        └───────────────┬─────────────────────────────────────────────┘
                        │ (semantic: seeded default makes the common path valid,
                        │  so Phase 2's tolerance only handles the degraded path)
                        ▼
        ┌─────────────────────────────────────────────────────────────┐
        │ PHASE 2  Dispatcher auto-start + graceful degradation +      │
        │          observability   (K9 K6 K8)                          │
        └───────────────┬─────────────────────────────────────────────┘
                        │ (needs the valid default + the runtime behaviors to pin)
                        ▼
        ┌─────────────────────────────────────────────────────────────┐
        │ PHASE 3  Drift/golden test gate + docs   (K7 + docs)         │
        │          depends on Phase 1 AND Phase 2                      │
        └─────────────────────────────────────────────────────────────┘
```

**Parallelism:** Phases 1 and 2 share **no compile-level dependency** — K9 is a pure
`up.go` gate refactor and K6/K8 consume the existing validator, so a two-agent split
could build them concurrently. They are sequenced here only so Phase 2's acceptance can
rely on Phase 1's valid default. **Phase 3 must run last** — it pins Phase 1's emitted
default and exercises Phase 2's behaviors. Within Phase 1, the topo order of components is
**K3 → K1 → K2 → K4 → K5** (K3 validator and K1 builder are leaf functions; K4 wires K1/K2;
K5 seeds in the same write).

## Deployment Coverage

| Target | Scripts/Config | Covered By Phase | Gap? |
|--------|---------------|-----------------|------|
| Bare `af install --init` ("hard way") | `internal/cmd/install.go` `runInstallInit` (97+) | Phase 1 (K1–K5 in the one write) | No |
| Container path (quickstart/quickdocker) | `quickstart.sh` (428, 441–470), `quickdocker.sh` | Phase 1 (SAME `runInstallInit` write — no script edit) | No |
| Documented startup `af up manager` | `internal/cmd/up.go` (92, 306, 330–335) | Phase 2 (K9 hoist) | No |
| Fresh-install dispatch visibility | `af up` / `af dispatch status` | Phase 2 (K8) | No |
| CI validity gate | `.github/workflows/test.yml` unit job (42–68, `make test`) | Phase 3 (K7 golden/cross-file) | No — closed |
| web/ module, `MaxWorktrees`, MCP issue-store | `web/`, `factory.json`, `mcpstore.New` (install.go:176) | — | Out of scope (correctly untouched) |

> **Scope boundary (do NOT build a phase for this):** AC-5 ("all branches pushed as PRs
> without doctor/human") is a **formula-layer property, scoped OUT of this change** (design-doc
> Six-Sigma Caveats L291; six_sigma_gaps Gap-5). This change routes work to the four existing
> formulas (via K5) and removes the doctor dependency (valid-by-construction default), but it
> cannot and does not verify a formula's internal PR-push — that is verified separately. There
> is intentionally no phase for AC-5.
>
> **Cross-doc note for implementers:** the `design-doc.md` synthesis is authoritative.
> Companion docs `dependencies.md`, `integration.md` (I3.1), and `security.md` (SEC2.1)
> describe an OLDER K5 mechanism (provision via `af install --agents` in `quickstart.sh`).
> That was **superseded by cross-review C1**: K5 now seeds the four specialists into the
> default `agents.json` inside `runInstallInit` (templates already embedded; `af install
> --agents` would recurse into quickstart and refuses to run from a worktree). Follow this
> outline + `design-doc.md`, not the stale companion docs.

---

## Phase 1: Single-source default builder + repo discovery + agents seed

### Objective
Make a freshly-initialized factory ship a **valid-by-construction** default `dispatch.json`
(real `owner/name`, 4 label→agent mappings, `feature-workflow`) AND a default `agents.json`
that already contains the four referenced specialists — both written in the single existing
`runInstallInit` write so the result is valid on **every** init path (bare init and quickstart).

### Prerequisites
None (foundational). Internal component order: K3 → K1 → K2 → K4 → K5.

### Recommended Skill or Agent
`*implement` (e.g. `rapid-implement`) — clear, fully-specified backend Go change with no
design exploration; all patterns to mirror already exist in-tree.

### Design References
| Document | Section | Lines | What It Specifies |
|----------|---------|-------|-------------------|
| design-doc.md | Implementation Plan → Phase 1 | L307–323 | K1–K5 deliverables + phase ACs |
| design-doc.md | Data Model (from data.md) | L160–219 | intent-corrected default JSON + default agents.json seed |
| design-doc.md | Interface (from api.md) | L150–158 | `DefaultDispatchConfigJSON(repo string) string`; no new flags; banner |
| design-doc.md | Key Components K1–K5 | L118–122 | per-component scope + locations |
| data.md | Data Model / D2.1 schema rules + D2.2-A | L160–185 (proposed default), schema-consistency rules | mapping/workflow validity (single-label phases, same source, no label collisions) |
| api.md | A2.1/A2.2 discovery + A3.1 degrade | discovery helper | `gh repo view --json nameWithOwner` + `git remote` normalization, warn-don't-abort |
| security.md | SEC1.1 / SEC3.1 | validator + write guard | `^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$` at write boundary; preserve write-if-absent |
| scale.md | S1.1 | discovery cost | bound the discovery call with a context timeout |
| codebase-investigation.md | §1 anchors, §2 D-a/D-b/D-d/D-e, §4 reuse | full | verified current lines + discrepancies + reuse assets |

### Current State (files to read for context)
| File | Lines | What's There |
|------|-------|-------------|
| `internal/config/config.go` | L108–124 | `DefaultFactoryConfigJSON()` — the struct→`json.Marshal`→string pattern K1 & K5 MIRROR |
| `internal/config/config.go` | L88–91, 35–37 | `DefaultGitIdentity()` + constants — in-code-default idiom |
| `internal/config/dispatch.go` | L17–27, 29–35, 42–45 | `DispatchConfig` / `DispatchMapping` / `Workflow` structs (build K1 from these) |
| `internal/config/dispatch.go` | L140–185 | `validateDispatchConfig` (struct rules; fills `NotifyOnComplete`→"manager" at 181–183) |
| `internal/config/dispatch.go` | L84–138 | `ValidateDispatchConfig` (cross-file; import-cycle comment 131–136) |
| `internal/config/dispatch.go` | L15 | `const defaultNotifyAgent = "manager"` |
| `internal/cmd/install.go` | L97, 139–148 | `runInstallInit` + starter-config map |
| `internal/cmd/install.go` | **L143** | agents.json inline literal (manager+supervisor ONLY) — K5 replaces |
| `internal/cmd/install.go` | **L145** | dispatch.json inline literal (`repos:[]`,`mappings:[]`) — K4 replaces (ONLY location; `:176` is `mcpstore.New`) |
| `internal/cmd/install.go` | L150–156 | write-if-absent guard (reused unchanged) |
| `internal/cmd/detect_default_branch.go` | L14, 21, 34–43, 67 | ⭐ K2/K3 template: 5s timeout, allowlist regex, `runGitDetect` seam, `gh repo view --json` |
| `internal/templates/roles/` | — | `rapid-soldesign-plan/rapid-implement/ultra-review/rapid-increment.md.tmpl` (all embedded) |
| `internal/cmd/install_integration_test.go` | **L66–72** | existing test asserts `repos:[]`/`mappings:[]` — WILL break; must be updated here |

### Required Changes

**File 1 (NEW): `internal/config/config.go`** — add `DefaultDispatchConfigJSON` (K1) and `DefaultAgentsConfigJSON` (K5), mirroring `DefaultFactoryConfigJSON` (108–124).
```go
// K1 — build from the struct so the field set is compiler-checked; OMIT NotifyOnComplete
// (validateDispatchConfig fills it → "manager" at runtime, dispatch.go:181-183).
func DefaultDispatchConfigJSON(repo string) string {
    repos := []string{}
    if repo != "" { repos = []string{repo} }
    cfg := DispatchConfig{
        Repos: repos, TriggerLabel: "agentic",
        IntervalSecs: 300, RetryAfterSecs: 1800, RemoveTriggerAfterDispatch: true,
        Mappings: []DispatchMapping{
            {Labels: []string{"rapid-plan"},     Source: "issue", Agent: "rapid-soldesign-plan"},
            {Labels: []string{"rapid-engineer"}, Source: "issue", Agent: "rapid-implement"},
            {Labels: []string{"pr-review"},      Source: "pr",    Agent: "ultra-review"},
            {Labels: []string{"pr-iterate"},     Source: "pr",    Agent: "rapid-increment"},
        },
        Workflows: []Workflow{{Label: "feature-workflow", Phases: []string{"rapid-plan", "rapid-engineer"}}},
    }
    b, err := json.Marshal(cfg)
    if err != nil { return `{"repos":[],"trigger_label":"agentic","mappings":[]}` } // unreachable fallback
    return string(b)
}
// K5 — seed the 4 specialists alongside manager+supervisor.
func DefaultAgentsConfigJSON() string { /* mirror DefaultFactoryConfigJSON: build AgentConfig, marshal */ }
```
- MUST NOT import `internal/formula` (cycle — dispatch.go:131–136).

**File 2 (MODIFY): `internal/cmd/install.go`**
- **Before L139** (in `runInstallInit`, after the `cd`-into-repo context is available): add K2 repo discovery + K3 validation, mirroring `detect_default_branch.go` (seam + 5s `context.WithTimeout` + `gh repo view --json nameWithOwner`, fallback `git remote get-url origin` normalized, then K3 validate; on any failure → `repo=""` + a loud stderr warning naming `af config dispatch set`).
- **L143**: replace the agents.json literal with `config.DefaultAgentsConfigJSON()` (K5).
- **L145**: replace the dispatch.json literal with `config.DefaultDispatchConfigJSON(discoveredRepo)` (K4).
- Optional UX banner: echo the validated repo (escape-safe via K3).

**File 3 (NEW): K3 validator** — package-level `regexp.MustCompile(`^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$`)` (in `internal/config` beside K1, or `internal/cmd` beside discovery). Rejects empty, leading `-`, whitespace, `..`, shell/escape chars BEFORE the value reaches disk / the banner / `gh --repo`.

**File 4 (MODIFY): `internal/cmd/install_integration_test.go` L66–72** — update the `repos:[]`/`mappings:[]` assertions to expect the new valid default (discovered repo, 4 mappings, `feature-workflow`) and assert the 4 seeded agents in agents.json on the bare-init path.

### Acceptance Criteria
```bash
# 1. K1 output passes struct validation (unit)
go test ./internal/config/ -run 'TestDefaultDispatchConfigJSON' -count=1
# Expected: ok (PASS)

# 2. Bare `af install --init` in a temp repo with a known remote yields the valid default
R=$(mktemp -d); git -C "$R" init -q; git -C "$R" remote add origin git@github.com:acme/widget.git
( cd "$R" && af install --init >/dev/null )
jq -e '.repos==["acme/widget"]'                 "$R/.agentfactory/dispatch.json"   # Expected: true
jq -e '.mappings|length==4'                     "$R/.agentfactory/dispatch.json"   # Expected: true
jq -e '.workflows[0].label=="feature-workflow"' "$R/.agentfactory/dispatch.json"   # Expected: true
jq -e 'has("notify_on_complete")|not'           "$R/.agentfactory/dispatch.json"   # Expected: true (omitted)
jq -e '.agents|has("rapid-soldesign-plan") and has("rapid-implement") and has("ultra-review") and has("rapid-increment")' "$R/.agentfactory/agents.json"  # Expected: true (all 4 specialists seeded)

# 3. cross-file validity on the bare-init artifacts (no unknown agent)
go test ./internal/config/ -run 'TestValidateDispatchConfig' -count=1
# Expected: ok (PASS) — default mappings resolve against the seeded agents.json

# 4. Idempotent re-run does not clobber
cp "$R/.agentfactory/dispatch.json" /tmp/d.bak; ( cd "$R" && af install --init >/dev/null )
diff -q /tmp/d.bak "$R/.agentfactory/dispatch.json"   # Expected: files identical (no output)

# 5. Crafted/garbage remote is rejected -> empty repos + loud warning (no malformed write)
R2=$(mktemp -d); git -C "$R2" init -q; git -C "$R2" remote add origin 'https://x/--evil/$(touch pwned)'
( cd "$R2" && af install --init 2>/tmp/warn.txt >/dev/null )
jq -e '.repos==[]' "$R2/.agentfactory/dispatch.json"  # Expected: true
grep -qi 'warn\|could not' /tmp/warn.txt              # Expected: exit 0 (warning emitted)

# 6. Updated existing integration test passes
go test ./internal/cmd/ -run 'TestInstall' -count=1
# Expected: ok (PASS)

# 7. Full root-module suite stays green
make test
# Expected: all packages ok
```

### Gotchas (from codebase investigation)
- ⚠️ **Existing test breaks:** `internal/cmd/install_integration_test.go:66–72` asserts the
  OLD empty defaults; update it IN THIS PHASE or CI reddens.
- **Import cycle:** new code in `internal/config` (K1/K5) MUST NOT import `internal/formula`
  (dispatch.go:131–136 documents why).
- **Omit `notify_on_complete`:** `validateDispatchConfig` fills it to "manager" at runtime
  (181–183); shipping it explicitly only adds a brittle cross-file check (Gap-7).
- **Reuse `detect_default_branch.go`,** don't reinvent: it already does `gh repo view --json`
  with a `runGitDetect` seam + 5s timeout (need field `nameWithOwner` instead of
  `defaultBranchRef`); the design's "0 git-remote grep hits" is only true for `git remote`.
- **K3 ordering:** validate BEFORE the value touches disk, the banner, or `gh --repo`
  (flag-injection via leading `-`; terminal-escape in the banner).
- **`gh` auth may be absent at install:** `git remote get-url origin` is the auth-free
  fallback (handle `git@host:o/r.git`, `https://host/o/r.git`, `https://host/o/r`).
- **Line citation:** the empty dispatch.json literal is at `install.go:145` ONLY (the design's
  `:176` is `mcpstore.New`).

---

## Phase 2: Dispatcher auto-start + graceful degradation + observability

### Objective
Make the documented `af up manager` (positional) path actually start the dispatcher, let the
dispatch LOOP degrade gracefully (skip-and-warn) on a partially-provisioned factory without
weakening the strict write path, and surface dispatch-config validity so a broken loop is
visible instead of silently spinning.

### Prerequisites
None at compile level. **Validated against Phase 1** (the seeded valid default means the
common path needs no degradation; K6/K8 cover the edited/partial factory). Treat Phase 1 as
a logical predecessor for acceptance.

### Recommended Skill or Agent
`*implement` — localized backend Go edits across `up.go` and `dispatch.go`; no design exploration.

### Design References
| Document | Section | Lines | What It Specifies |
|----------|---------|-------|-------------------|
| design-doc.md | Implementation Plan → Phase 2 | L325–339 | K9/K6/K8 deliverables + phase ACs |
| design-doc.md | Key Components K6/K8/K9 | L123–126 | per-component scope + locations |
| design-doc.md | Cross-Perspective Conflicts (C2/H1/H2) | L243–244 | hoist auto-start; commit minimal K5; K8 mandatory with K6 |
| design-doc.md | Risk Registry + K8 note | L273, L282–285 | positional-`af up` risk; K8 observability hardening |
| integration.md | I3.2 (dispatcher tolerance) | tolerance scope | skip-and-warn dispatch-loop caller only; write path strict |
| security.md | SEC2.2 | defense-in-depth | tolerate unknown agents with loud warning |
| ux.md | U2.1 | error messages | name remedy `af install --agents` / `af config dispatch set` |
| codebase-investigation.md | §1 (up.go/dispatch.go/config_set.go), §5 (3–4) | full | verified anchors + hoist-safety |

### Current State (files to read for context)
| File | Lines | What's There |
|------|-------|-------------|
| `internal/cmd/up.go` | L92 | `blanket := len(args) == 0`; `startupCfg` read at L82 |
| `internal/cmd/up.go` | L306, 330–335 | `if blanket { … if startupCfg.StartDispatch { startDispatch(...) } }` — K9 hoists the StartDispatch block out of the `blanket` gate |
| `internal/cmd/dispatch.go` | L146–148 | dispatch-loop `ValidateDispatchConfig` call (hard-fail) — K6 relaxes THIS caller only |
| `internal/cmd/dispatch.go` | L1321–1340 | `startDispatch` — already-running no-op (1322–1325), unconfigured skip (1328–1331); idempotent (enables K9) |
| `internal/cmd/dispatch.go` | L1356–1405, 1458–1461 | `runDispatchStatus` + `dispatchStatusJSON{ DispatcherRunning, Entries }` — K8 extends this |
| `internal/config/dispatch.go` | L93, 100–103 | `ValidateDispatchConfig` signature + unknown-agent error (the rule K6 wraps) |
| `internal/cmd/config_set.go` | L89–90 | strict `ValidateDispatchConfig` on the WRITE path — MUST stay hard-fail |
| `internal/config/startup.go` | L18 | `StartDispatch bool` (`start_dispatch`); install.go:147 ships `true` |

### Required Changes

**File 1 (MODIFY): `internal/cmd/up.go`** — K9: move the `if startupCfg.StartDispatch { startDispatch(...) }` block (currently 330–335) OUT of the `if blanket {` block (306) so it runs for positional `af up <name>` too. `startDispatch` is idempotent (no double-start). K8: add a read-only pre-flight `ValidateDispatchConfig` that WARNS (never aborts `af up`) and classifies the state.

**File 2 (MODIFY): `internal/cmd/dispatch.go`** — K6: at the dispatch-loop call site (146–148), replace the hard `return err` with skip-and-warn for the unknown-agent case — drop the offending mapping(s), emit a loud per-mapping warning naming `af install --agents`, and continue dispatching the rest. Scope strictly to this caller. K8: extend `dispatchStatusJSON` (1458–1461) with a config-validity field (e.g. `config_state: "ok" | "empty_by_design" | "discovery_failed" | "references_unprovisioned_agents"`), derived by reusing the `ErrNotFound`/`ErrMissingField` distinction (1328–1331).

**File 3 (UNCHANGED, guard): `internal/cmd/config_set.go` L89–90** — confirm the write path keeps the strict `ValidateDispatchConfig`. K6 MUST NOT touch it.

### Acceptance Criteria
```bash
# 1. Positional auto-start + idempotency (needs a valid default from Phase 1)
af up manager
# Expected: dispatch tmux session launched (e.g. "Dispatcher started")
af up manager
# Expected: "Dispatcher already running (session: ...)" (benign no-op)
tmux has-session -t af-dispatch 2>/dev/null && echo RUNNING
# Expected: RUNNING

# 2. Dispatch LOOP skips an unknown-agent mapping with a warning and dispatches the rest
go test ./internal/cmd/ -run 'TestDispatch.*(Unknown|Skip|Tolerat)' -count=1
# Expected: ok (PASS) — offending mapping skipped+warned, others dispatched

# 3. Write path stays STRICT (K6 did not leak)
printf '%s' '{"repos":["o/r"],"trigger_label":"agentic","mappings":[{"labels":["x"],"source":"issue","agent":"does-not-exist"}]}' | af config dispatch set; echo "exit=$?"
# Expected: exit=1 (non-zero), file unchanged

# 4. Status observability distinguishes the failure modes
af dispatch status --json | jq -e 'has("config_state")'
# Expected: true   (value in: ok | empty_by_design | discovery_failed | references_unprovisioned_agents)

# 5. Suite green
make test
# Expected: all packages ok
```

### Gotchas (from codebase investigation)
- **Scope K6 to the dispatch-loop caller ONLY** (dispatch.go:146); `config_set.go:89–90`
  MUST remain hard-fail (human-typo guard).
- **K8 is MANDATORY with K6** (cross-review H2): without it, K6 turns a clean "not configured"
  skip into a silently-warning loop that looks active but dispatches nothing.
- **K9 hoist is safe:** investigation found only the one blanket-gated `startDispatch` caller;
  idempotency (1322–1325) prevents double-start.
- **Don't break the existing `--json` contract:** add `config_state` ADDITIVELY; keep
  `dispatcher_running` and `entries`.
- **Two-label pre-filter (context):** the trigger-label query (dispatch.go ~301/320) is a hard
  pre-filter — K6/K8 do not change it; the user-facing two-label caveat is a Phase-3 docs item.

---

## Phase 3: Drift/golden test gate + docs

### Objective
Mechanically gate the shipped default against drift (so a future label/phase/formula rename
fails CI), and document the two operational caveats — the two-label requirement and the
net-new-install scope.

### Prerequisites
Phase 1 (the emitted default + seeded agents to pin) AND Phase 2 (the runtime behaviors to assert).

### Recommended Skill or Agent
`*implement` — Go test code plus markdown doc edits in the same repo; no design exploration.

### Design References
| Document | Section | Lines | What It Specifies |
|----------|---------|-------|-------------------|
| design-doc.md | Implementation Plan → Phase 3 | L340–352 | K7 golden/cross-file test + docs deliverables + phase ACs |
| design-doc.md | Risk Registry (formula-rename) | L278 | drift test ties default to shipped agents |
| six_sigma_gaps.md | Gap-2 / Gap-4 / Gap-6 | — | two-label requirement; drift gate; net-new scope |
| codebase-investigation.md | §1 (test models), §3 (CI gap) | full | `dispatch_workflow_test.go` model; no current init-validity gate |

### Current State (files to read for context)
| File | Lines | What's There |
|------|-------|-------------|
| `internal/config/dispatch_workflow_test.go` | L212–257 | cross-file validation table — the K7 model |
| `internal/config/formula_drift_test.go` | Full file | ADR-008 embedded-vs-installed drift pattern (model for a golden/drift assertion) |
| `internal/cmd/install_integration_test.go` | L66–72 | bare-init assertions (updated in Phase 1; K7 complements at the config layer) |
| `.github/workflows/test.yml` | L42–68 (unit), 96–124 (integration), 126–140 (regen) | CI tiers; `make test` runs the unit tier where K7 lands |
| `USING_AGENTFACTORY.md` | dispatch section (~L225 example) | documents label-triggering; two-label requirement NOT stated; net-new scope NOT stated |

### Required Changes

**File 1 (NEW): `internal/config/dispatch_default_test.go`** (or extend `dispatch_workflow_test.go`) — K7: assert `DefaultDispatchConfigJSON("acme/widget")` (a) parses + passes `validateDispatchConfig`, and (b) cross-validates via `ValidateDispatchConfig` against `DefaultAgentsConfigJSON()`'s parsed agents (so the 4 mappings resolve). Pin the exact mapping label→agent set + the `feature-workflow` phases, and accept `notify_on_complete` omitted-OR-"manager". Mirror `dispatch_workflow_test.go:212–257`.

**File 2 (MODIFY): `USING_AGENTFACTORY.md`** — docs-1 (Gap-2): state the **two-label requirement** — an issue/PR must carry BOTH the `agentic` trigger label AND a mapping/workflow label to dispatch (the trigger label is a hard query pre-filter). docs-2 (Gap-6): state that the baked-in default ships for **net-new installs only**; existing factories opt in via `af config dispatch set` (write-if-absent never clobbers customer config — ADR-017).

### Acceptance Criteria
```bash
# 1. Golden + cross-file drift gate passes
go test ./internal/config/ -run 'TestDefaultDispatch.*(Golden|CrossFile|Drift)' -count=1
# Expected: ok (PASS)

# 2. The gate actually catches drift: a renamed mapping label / agent breaks it
#    (verify by temporarily editing DefaultDispatchConfigJSON -> the test must FAIL)
# Expected: test reddens on any mismatch vs the pinned default + seeded agents

# 3. Docs state the two-label requirement and the net-new scope
grep -qiE 'agentic.*(and|plus|\+|both).*label|two[ -]label|both labels' USING_AGENTFACTORY.md
# Expected: exit 0
grep -qiE 'net[ -]new|new install|af config dispatch set' USING_AGENTFACTORY.md
# Expected: exit 0

# 4. Suite green (K7 runs in the unit tier)
make test
# Expected: all packages ok
```

### Gotchas (from codebase investigation)
- **No CI job exercises `af install --init` dispatch validity today** — K7 must live where
  `make test` runs it (unit tier, test.yml:42–68), so a drift reaches CI, not the customer.
- **Accept omitted-OR-"manager"** for `notify_on_complete` (runtime fills it).
- **Two-label requirement is docs-only** (Gap-2): widening the trigger-label query is out of
  scope (expands the dispatch blast radius); the only safe fix is documentation.
- **Net-new scope is a policy, not a bug** (ADR-017): do not add an auto-migration of existing
  empty `dispatch.json` installs; name the opt-in path instead.
