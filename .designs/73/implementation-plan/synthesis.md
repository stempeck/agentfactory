# Synthesis — Issue #73 (Phase 3: Cross-Reference)

Merges the design spec (`design-doc.md`) with firsthand ground truth
(`codebase-investigation.md`). Per-phase: adjusted scope, gotchas, refined ACs (bash),
design refs, workstream class, deployment coverage. This is the blueprint for the
implementation_plan_outline.

## Global synthesis decisions

- **Phase 0: NONE.** `make test` is green on main; no latent bug exists in the code the
  design modifies. The only "surprise" (existing test `install_integration_test.go:66–72`
  breaks on the new defaults) is repaired INSIDE Phase 1, not before it.
- **Dependency order = design narrative order P1 → P2 → P3.** No renumbering.
  - P1 (default builders + discovery + seed) is foundational; depends on nothing new.
  - P2 (K9/K6/K8) has NO compile dependency on P1 (K9 is a pure `up.go` gate refactor;
    K6/K8 consume the existing `ValidateDispatchConfig`). Its semantic dependency is that
    P1's seeded default makes the common path valid, so K6/K8 only handle the degraded
    path — therefore P2's acceptance is validated AFTER P1. Code-level, P1 and P2 could be
    built in parallel; the outline keeps them sequential for clean acceptance.
  - P3 (K7 + docs) depends on BOTH P1 (default+seed exist to pin) and P2 (behaviors to test).
- **No separate parity phase.** The audit found bare-init AND quickstart both ship invalid
  defaults today (C1). The design's K5 seed lives in `runInstallInit`, fixing BOTH paths in
  ONE write — parity is intrinsic to P1. The parity ASSERTION is the updated bare-init
  integration test (P1) + the K7 cross-file golden test (P3).
- **Workstream classification:** all three phases are **Backend (Go)** → **`*implement`**.
  Phase 3 also edits docs (markdown, no design exploration) → still **`*implement`**.
  NO Infrastructure/Terraform (none in repo), NO Frontend UI, NO Manual/Human steps.
  (See routing table at end.)

---

## PHASE 1 — Single-source default builder + repo discovery + agents seed

**Adjusted scope (CREATE vs MODIFY, with reuse):**
| Comp | Action | Where (current) | Reuse / model |
|------|--------|-----------------|---------------|
| K1 | CREATE `DefaultDispatchConfigJSON(repo string) string` | `internal/config/config.go` (beside 108–124) | mirror `DefaultFactoryConfigJSON` (struct→marshal→string); build from `DispatchConfig` struct (dispatch.go:17–27) |
| K2 | CREATE repo-discovery helper + call in `runInstallInit` | `internal/cmd/install.go` (runInstallInit @97; call before map @139) | **mirror `detect_default_branch.go`**: `runGitDetect` seam (34–43), `context.WithTimeout` 5s, `gh repo view --json nameWithOwner` (cf :67 uses `defaultBranchRef`), `git remote get-url origin` normalization fallback; warn-don't-abort |
| K3 | CREATE strict `owner/name` validator | `internal/config` or `internal/cmd` | extend allowlist idiom (`detect_default_branch.go:21`) → `^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$` |
| K4 | MODIFY: replace inline dispatch.json literal | `internal/cmd/install.go:145` | reuse write-if-absent (150–156) |
| K5 | CREATE `DefaultAgentsConfigJSON()` + MODIFY inline agents.json literal | `internal/config/config.go` + `install.go:143` | mirror `DefaultFactoryConfigJSON`; seed 4 `{"type":"autonomous","formula":"<name>"}` (templates embedded) |
| **K-test** | **MODIFY existing test (NEW — not in design)** | `internal/cmd/install_integration_test.go:66–72` | assertions `repos:[]`/`mappings:[]` now wrong; update to assert the valid default + 4 seeded agents on the bare-init path |
| (banner) | ADD escape-safe install line echoing discovered repo (optional, UX U1.2) | `runInstallInit` stdout | derive from the validated (K3) repo |

**Gotchas (investigation-found):**
- ⚠️ Existing `install_integration_test.go:66–72` WILL fail unless updated in this phase (CI reddens otherwise).
- Import cycle: K1/K5 in `internal/config` MUST NOT import `internal/formula` (dispatch.go:131–136).
- `validateDispatchConfig` already fills `NotifyOnComplete`→"manager" at runtime (dispatch.go:181–183) → K1 OMITS the field (Gap-7).
- K3 must reject leading `-` (gh flag-injection into `gh --repo`) + shell/terminal-escape BEFORE the value hits disk/banner/gh.
- `gh repo view` needs `gh auth`; `git remote get-url origin` is the auth-free fallback (3 URL shapes: `git@…:o/r.git`, `https://…/o/r.git`, `https://…/o/r`).
- Discovery timing OK on both paths: quickstart `cd`s into the repo (quickstart.sh:428) before `af install --init` (:441); bare-init runs in the user's git repo.
- Design line-error: dispatch.json literal is at install.go:**145** ONLY (`:176` is `mcpstore.New`).

**Refined acceptance criteria (bash, executable):**
```bash
# AC1.a struct validity (unit)
go test ./internal/config/ -run 'TestDefaultDispatchConfigJSON' -count=1   # PASS
# AC1.b bare init in a temp repo with a known remote
R=$(mktemp -d); git -C "$R" init -q; git -C "$R" remote add origin git@github.com:acme/widget.git
( cd "$R" && af install --init >/dev/null )
jq -e '.repos==["acme/widget"]'                "$R/.agentfactory/dispatch.json"   # true
jq -e '.mappings|length==4'                    "$R/.agentfactory/dispatch.json"   # true
jq -e '.workflows[0].label=="feature-workflow"' "$R/.agentfactory/dispatch.json"  # true
jq -e 'has("notify_on_complete")|not'          "$R/.agentfactory/dispatch.json"   # true (omitted)
jq -e '.agents|keys|index("rapid-soldesign-plan")!=null and index("rapid-implement")!=null and index("ultra-review")!=null and index("rapid-increment")!=null' "$R/.agentfactory/agents.json"  # true
# AC1.c idempotent re-run does not clobber
cp "$R/.agentfactory/dispatch.json" /tmp/d.bak; ( cd "$R" && af install --init >/dev/null )
diff -q /tmp/d.bak "$R/.agentfactory/dispatch.json"   # identical
# AC1.d crafted remote rejected → empty repos + loud warning
R2=$(mktemp -d); git -C "$R2" init -q; git -C "$R2" remote add origin 'https://x/--evil/$(touch pwned)'
( cd "$R2" && af install --init 2>/tmp/warn.txt >/dev/null ); jq -e '.repos==[]' "$R2/.agentfactory/dispatch.json"  # true
grep -qi 'warn' /tmp/warn.txt   # warning emitted
# AC1.e updated existing integration test passes
go test ./internal/cmd/ -run 'TestInstall' -count=1   # PASS
```

**Design references:** design-doc.md:307–323 (Phase 1), :160–219 (default JSON + agents seed),
:150–158 (K1 interface); data.md:160–185 (intent-corrected default), D2.1 schema rules;
api.md A1.1/A2.1/A2.2; integration.md I1.1/I2.1; security.md SEC1.1 (regex)/SEC3.1;
scale.md S1.1 (timeout); codebase-investigation.md §1 (anchors), §2 (D-a,D-b,D-d,D-e).

**Workstream:** Backend (Go) → **`*implement`**.
**Deployment coverage:** one `runInstallInit` write fixes bare-init AND quickstart (parity
intrinsic); AC1.b/AC1.e assert the bare path.

---

## PHASE 2 — Dispatcher auto-start + graceful degradation + observability

**Adjusted scope:**
| Comp | Action | Where (current) | Notes |
|------|--------|-----------------|-------|
| K9 | MODIFY: hoist `StartDispatch` block out of the blanket gate | `internal/cmd/up.go:330–335` (gate @306; `blanket:=len(args)==0` @92) | positional `af up <name>` also auto-starts; `startDispatch` idempotent (dispatch.go:1322–1325) |
| K6 | MODIFY: dispatch-loop skip-and-warn on unknown agent | `internal/cmd/dispatch.go:146–148` (caller only) | MUST NOT relax `config_set.go:89–90` (write path stays strict) |
| K8 | CREATE pre-flight validation + extend status JSON | `internal/cmd/up.go` (pre-flight, warn-never-abort) + `dispatch.go:1356–1405` / `dispatchStatusJSON` (1458–1461) | distinguish "empty by design" vs "discovery failed" vs "references unprovisioned agents" (reuse `ErrNotFound`/`ErrMissingField` idiom @1328–1331) |

**Gotchas:**
- K6 scoped to the dispatch-loop caller ONLY (dispatch.go:146); `config_set.go:89–90` MUST remain hard-fail.
- K8 is MANDATORY wherever K6 ships (cross-review H2 — else K6 hides "configured-but-broken").
- K9 hoist is safe: audit found only the one blanket-gated `startDispatch` caller; idempotency prevents double-start.
- K8 status field is additive to the existing `af dispatch status --json` contract (don't break `DispatcherRunning`/`Entries`).

**Refined acceptance criteria (bash):**
```bash
# AC2.a positional auto-start + idempotent
af up manager   # -> launches dispatch tmux session when start_dispatch:true & default valid
af up manager   # -> "Dispatcher already running" (benign no-op)
tmux has-session -t af-dispatch 2>/dev/null && echo RUNNING   # RUNNING
# AC2.b degrade: one mapped agent absent -> loop skips+warns, dispatches the rest
go test ./internal/cmd/ -run 'TestDispatch.*Unknown(Agent|Mapping)' -count=1   # PASS (skip-and-warn)
# AC2.c write path stays strict
echo '<config with unknown agent>' | af config dispatch set; echo $?   # non-zero
# AC2.d status observability
af dispatch status --json | jq -e '.config_state'   # e.g. "references_unprovisioned_agents" | "empty_by_design" | "ok"
```

**Design references:** design-doc.md:325–339 (Phase 2), :123–126 (K6/K8/K9), :243–244
(cross-review C2/H1/H2), :273 (risk), :282–285 (K8); integration.md I3.2; security.md
SEC2.2; ux.md U2.1; codebase-investigation.md §1 (up.go/dispatch.go/config_set.go anchors),
§5 (hidden issues 3–4).

**Workstream:** Backend (Go) → **`*implement`**.
**Deployment coverage:** K9 fixes the documented `af up manager` path (C2); K8 makes
fresh-install dispatch state visible (Gap-8).

---

## PHASE 3 — Drift/golden test gate + docs

**Adjusted scope:**
| Comp | Action | Where (current) | Model |
|------|--------|-----------------|-------|
| K7 | CREATE golden + cross-file test | `internal/config/*_test.go` (+ `internal/cmd/*_test.go`) | mirror `dispatch_workflow_test.go:212–257` (cross-file table); drift like `formula_drift_test.go` (ADR-008) |
| docs-1 | MODIFY: state two-label requirement (`agentic` + mapping/workflow label) | `USING_AGENTFACTORY.md` (dispatch section; example at ~:225) | Gap-2 |
| docs-2 | MODIFY: state net-new-install scope (existing factories opt in via `af config dispatch set`) | `USING_AGENTFACTORY.md` | Gap-6 / ADR-017 |

**Gotchas:**
- K7 must accept `notify_on_complete` omitted-OR-"manager" (runtime fills it).
- No CI job exercises `af install --init` dispatch validity today → K7 lands in the `make test`
  unit tier (test.yml unit job 42–68) to gate AC-1/C-6 against drift.
- Two-label requirement (Gap-2): trigger-label query is a hard pre-filter (dispatch.go ~301/320);
  tagging only a mapping label dispatches nothing — docs-only fix (query-widening out of scope).

**Refined acceptance criteria (bash):**
```bash
# AC3.a drift gate: shipped default parses AND cross-validates vs default-seeded agents.json
go test ./internal/config/ -run 'TestDefaultDispatch.*(Golden|CrossFile|Drift)' -count=1   # PASS
# AC3.b a deliberate mutation of the default OR a renamed mapping label fails the test
# (manually verify the test reddens when DefaultDispatchConfigJSON is edited)
# AC3.c docs
grep -qiE 'agentic.*(and|plus|\+).*label|two label|both labels' USING_AGENTFACTORY.md   # two-label requirement stated
grep -qiE 'net-new|new install|af config dispatch set' USING_AGENTFACTORY.md             # net-new scope + opt-in stated
```

**Design references:** design-doc.md:340–352 (Phase 3), :278 (formula-rename risk),
:298–299 (gaps closed); six_sigma_gaps.md Gap-2/Gap-4/Gap-6; codebase-investigation.md
§1 (test models), §3 (CI gap).

**Workstream:** Backend (Go test) + docs → **`*implement`**.
**Deployment coverage:** closes the CI gap (no current `af install --init` validity gate).

---

## Workstream routing table

| Phase | Domain | Recommended skill/agent | Rationale |
|-------|--------|-------------------------|-----------|
| 1 | Backend (Go: config builders, install wiring, discovery, validator) + test update | `*implement` (e.g. `rapid-implement`) | clear spec, no design exploration; pure Go |
| 2 | Backend (Go: up.go gate, dispatch loop, status JSON) | `*implement` | clear spec; localized Go edits |
| 3 | Backend (Go test) + docs (markdown) | `*implement` | test code + doc edits, same repo/skill |

No `*terraform` (no infra), no `*design` (no UI/design exploration), no `manual`
(fully autonomous Go + docs) workstreams exist for this change.

## Deployment-gap verdict
- Environments in repo: bare-init path, quickstart/quickdocker path, CI (test.yml), web/ module.
- Parity gap (bare-init vs quickstart invalid default) is CLOSED by P1's single-write seed — no
  parity phase needed; asserted by P1's updated integration test + P3's K7 cross-file test.
- CI coverage gap (no init-validity gate) CLOSED by P3 K7 in the unit tier.
- web/ module, MaxWorktrees, MCP store: correctly untouched by this change.
