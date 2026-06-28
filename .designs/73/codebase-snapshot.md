# codebase-snapshot.md — Verified Ground Truth (Phase 1)

Captured by the main context for issue #73. Sub-agents MUST treat this as
authoritative. Claims here were verified by Read/Grep/Bash against the actual
codebase at `/home/dev/af/agentfactory/.agentfactory/worktrees/wt-7605ca`.

Dynamic (runtime) values are marked ⚡ — they change over time; do not state them
as fixed facts.

---

## 1. Package tree (Go, root module)

Total Go files (root module, excl. vendor): **267**. Top-level package dirs:

```
./cmd/af
./internal/checkpoint
./internal/claude
./internal/cmd                      # CLI commands (install.go, dispatch.go, sling.go, config_set.go, ...)
./internal/config                   # config schema + loaders (dispatch.go, config.go, paths.go, startup.go, agents.go)
./internal/formula
./internal/fsutil                   # WriteFileAtomic
./internal/issuestore
./internal/issuestore/mcpstore
./internal/issuestore/memstore
./internal/lock
./internal/mail
./internal/session
./internal/templates
./internal/tmux
./internal/worktree
web/                                 # SEPARATE Go module (web/go.mod) — not in root `make test`
```

Most-relevant files for this design:
- `internal/cmd/install.go` — `af install --init` / `<role>` / `--agents`
- `internal/config/dispatch.go` — DispatchConfig schema, loader, writer, validators
- `internal/config/config.go` — `DefaultFactoryConfigJSON()` (the single-source default pattern)
- `internal/config/paths.go` — config path helpers
- `internal/cmd/dispatch.go` — dispatcher loop + workflow engine
- `internal/cmd/sling.go` — `af sling --agent` specialist dispatch
- `internal/cmd/config_set.go` — `af config dispatch set` / `af config startup set`
- `quickstart.sh`, `quickdocker.sh` — bootstrap scripts (repo root)

## 2. Module identity

```
module github.com/stempeck/agentfactory
go 1.24.2
```

## 3. Referenced formulas / agents (existence verified)

The proposed default `dispatch.json` references 4 agents in `mappings[].agent`.
Each EXISTS as a formula in BOTH `.agentfactory/store/formulas/` and
`internal/cmd/install_formulas/` (version 1), and is a configured specialist
(has a `formula` field) in THIS factory's `.agentfactory/agents.json`:

| Agent (mappings[].agent) | Formula file (both dirs) | Version | In this factory's agents.json? |
|--------------------------|--------------------------|---------|-------------------------------|
| `rapid-soldesign-plan`   | `rapid-soldesign-plan.formula.toml` | 1 | YES (type autonomous, formula=rapid-soldesign-plan) |
| `rapid-implement`        | `rapid-implement.formula.toml`      | 1 | YES |
| `ultra-review`           | `ultra-review.formula.toml`         | 1 | YES |
| `rapid-increment`        | `rapid-increment.formula.toml`      | 1 | YES |

Full formula dir listing (both locations identical, 15 formulas): design,
design-plan-impl, design-v3, design-v7, factoryworker, gherkin-breakdown,
investigate, mergepatrol, minimalworker, rapid-implement, rapid-increment,
rapid-soldesign-plan, rootcause-all, ultra-review, web-design.

**CRITICAL CAVEAT (verified):** the existence above is for THIS already-fully-
provisioned factory. The DEFAULT `agents.json` written by `af install --init`
contains ONLY `manager` + `supervisor` (see §6, install.go:143). A FRESH install
does NOT have the 4 specialists in agents.json. The issue's setup flow mentions
`design-v3`; the proposed mappings reference `rapid-*` agents — both classes of
specialist are absent from a fresh agents.json until separately provisioned.

`design-v3` formula EXISTS (version 1). No name mismatches were found between the
proposed `mappings[].agent` values and existing formula/agent names.

## 4. Referenced CLI commands (verified via --help)

### `af install --help` (key facts)
- `--init` — Initialize a new factory workspace (creates config dir, starter configs, issue store, hooks).
- `[role]` — Provision a single agent role (renders CLAUDE.md, writes settings.json).
- `--agents` — Regenerate+reinstall all formula-derived agents (runs `agent-gen-all.sh` then `quickstart.sh`); refuses to run from a worktree; requires an already-initialized factory.
- `--no-build` — skip agent-gen-all.sh's duplicate rebuild.
- `runInstallInit` takes NO repo argument (verified §6).

### `af config dispatch set --help`
> "Read a complete DispatchConfig as JSON on stdin, validate it (struct-level plus a cross-file check that every referenced agent exists in agents.json), and write it atomically to dispatch.json. On any validation failure the command exits non-zero and leaves the existing file untouched."
Flags: `-h, --help` (reads stdin; no other flags).

### `af dispatch --help` (key facts)
Dispatch cycle: (1) Load `.agentfactory/dispatch.json` and `.agentfactory/agents.json`; (2) check `gh auth`; (3) query each repo for open issues/PRs with the trigger label; (4) match labels to mappings; (5) skip already-dispatched (24h TTL) or busy agents; (6) dispatch via `af sling --agent <name> --reset <url>`; (7) save state to `.runtime/dispatch-state.json`. Subcommands: `start`, `stop`, `status`. Flag: `--dry-run`.

### `af sling --help` (key facts)
> "Specialist dispatch mode: when --agent names a specialist (an agent with a formula field in agents.json), the agent's formula is instantiated with the task injected as a variable…"
Resolution (sling.go): `resolveSpecialistAgent(root, agentName)` loads agents.json; errors `"agent %q not found in agents.json"` if absent, or `"agent %q is not a specialist (no formula field…)"` if no formula.

## 5. Referenced file verification

| Path | Status |
|------|--------|
| `internal/cmd/install.go` | EXISTS |
| `internal/config/dispatch.go` | EXISTS |
| `internal/config/config.go` | EXISTS (`DefaultFactoryConfigJSON` at line 111) |
| `internal/config/paths.go` | EXISTS |
| `internal/cmd/dispatch.go` | EXISTS |
| `internal/cmd/sling.go` | EXISTS |
| `internal/cmd/config_set.go` | EXISTS |
| `.agentfactory/dispatch.json` (runtime path) | `DispatchConfigPath(root)=<root>/.agentfactory/dispatch.json` (paths.go:19) |
| `quickstart.sh` | EXISTS (repo root) — calls `af install --init` after `cd` into discovered repo |
| `quickdocker.sh` | EXISTS (repo root) — takes `<github-repo-path>` arg; delegates to quickstart.sh |
| `docs/architecture/adrs/` | EXISTS (19 ADRs) |

## 6. Referenced types and functions (verified signatures, file:line)

**`DispatchConfig`** — `internal/config/dispatch.go:17-27`:
```go
type DispatchConfig struct {
	Repos                      []string          `json:"repos"`
	TriggerLabel               string            `json:"trigger_label"`
	NotifyOnComplete           string            `json:"notify_on_complete"`
	Mappings                   []DispatchMapping `json:"mappings"`
	IntervalSecs               int               `json:"interval_seconds"`
	RetryAfterSecs             int               `json:"retry_after_seconds"`
	RemoveTriggerAfterDispatch bool              `json:"remove_trigger_after_dispatch"`
	Workflows                  []Workflow        `json:"workflows,omitempty"`
}
```
Every field name in the proposed JSON matches EXACTLY. Defaults applied in
`validateDispatchConfig`: `interval_seconds`→300, `retry_after_seconds`→1800,
`notify_on_complete`→"manager" (const `defaultNotifyAgent`, dispatch.go:15),
`mappings[].source`→"issue".

**`DispatchMapping`** — `internal/config/dispatch.go:30-35`:
```go
type DispatchMapping struct {
	Label  string   `json:"label,omitempty"`   // deprecated singular; auto-migrates to Labels
	Labels []string `json:"labels,omitempty"`
	Source string   `json:"source,omitempty"`  // "issue"|"pr", default "issue"
	Agent  string   `json:"agent"`
}
```

**`Workflow`** — `internal/config/dispatch.go:42-45`:
```go
type Workflow struct {
	Label  string   `json:"label"`
	Phases []string `json:"phases"`
}
```

**`LoadDispatchConfig(root) (*DispatchConfig, error)`** — dispatch.go:48-65. Reads
`<root>/.agentfactory/dispatch.json`; returns `ErrNotFound` if missing; runs
**struct-level** `validateDispatchConfig` ONLY (NOT the cross-file agents.json check).

**`SaveDispatchConfig(path, cfg)`** — dispatch.go:72-82. Struct-validates then
`fsutil.WriteFileAtomic` (temp+rename). Does NOT do the cross-file check.

**`ValidateDispatchConfig(disp, agents)`** — dispatch.go:93+. **Cross-file**: every
`Mapping.Agent` and an explicitly-set `NotifyOnComplete` must exist in agents.json.
Empty `NotifyOnComplete` left unvalidated (defaults to "manager"). Called by
`af config dispatch set` (config_set.go) and the dispatcher path — NOT by
`LoadDispatchConfig`.

**`runInstallInit`** — `internal/cmd/install.go`. Verified at install.go:138-157,
the `starterConfigs` map (write-if-absent, idempotent):
```go
starterConfigs := map[string]string{
	"factory.json":   config.DefaultFactoryConfigJSON(),
	"agents.json":    `{"agents":{"manager":{"type":"interactive",...},"supervisor":{"type":"autonomous",...}}}`,
	"messaging.json": `{"groups":{"all":["manager","supervisor"]}}`,
	"dispatch.json":  `{"repos":[],"trigger_label":"agentic","notify_on_complete":"manager","mappings":[],"interval_seconds":300,"retry_after_seconds":1800}`,
	"startup.json":   `{"agents":["manager"],"quality":"default","fidelity":"default","start_dispatch":true,"watchdog_agents":["manager","supervisor"]}`,
}
```
- dispatch.json default today: **empty `repos`, empty `mappings`, no `workflows`** (install.go:145).
- `runInstallInit` takes NO repo argument and does NO git-remote lookup — repo identity is unknown to it today.
- Write is idempotent write-if-absent (`os.Stat … os.IsNotExist`, install.go:152) → "first created" (C-2) == this write.
- dispatch.json is the ONLY starter config still an inline literal; `factory.json` uses `DefaultFactoryConfigJSON()` (install.go:142) — established single-source pattern (issue #371 Gap-6).

**`DefaultFactoryConfigJSON() string`** — `internal/config/config.go:111`. The model
to mirror for a `DefaultDispatchConfigJSON()`.

**Path helpers** — `internal/config/paths.go`: `dotDir = ".agentfactory"` (:10);
`ConfigDir(root)` (:13); `AgentsConfigPath(root)` (:17); `DispatchConfigPath(root)` (:19).

**Bootstrap scripts (verified by Agent B):**
- `quickdocker.sh`: takes `<github-repo-path>` as `$1`; derives container name; clones repo into `$WORKSPACE_DIR/$REPO_NAME`; delegates bootstrap to quickstart.sh.
- `quickstart.sh`: discovers the repo dir by finding the first `.git` under `$WORKSPACE_DIR`, `cd`s into it, then calls `af install --init` (so a git remote IS present in CWD at that moment, but install doesn't read it), then `af install manager` / `af install supervisor`.

## 7. Dynamic values ⚡

- ⚡ `af agents list --json` at snapshot time: this factory has 17 configured
  agents incl. all 4 specialists. Running now: `design-v7` (me, Phase 1),
  `rapid-soldesign-plan` (orchestrator, awaiting analysis mail), `rootcause-all`
  (Phase 2), `manager` (idle). `rapid-increment` shows `all_complete` for PR #67.
  These statuses change continuously — do NOT cite as fixed.
- ⚡ This is a fully-provisioned dev factory; a fresh `af install --init` factory
  has only manager+supervisor. Reason about the FRESH-install default, not this tree.

---

## Decision History

### ADRs (searched `docs/architecture/adrs/`; 19 ADRs present)

**ADR-014 — No interactive prompting in agent-runtime code paths** (Accepted 2026-04-22). Key decision (verbatim):
> "No code path under `cmd/` or `internal/` (Go) or `py/` (Python) may prompt for user input at runtime."
> Required shape for caller discretion: "fail loud with a structured error naming the exact flag that expresses the intent. No prompt, no fallback."
> Exemption (verbatim): "One-time bootstrap / installation scripts invoked manually by a human operator at setup time: `quickdocker.sh`… The operator persona is real at the bootstrap moment. These scripts are exempt but should be minimized over time and not proliferated."
**Implication:** repo-name substitution during `af install --init` (Go code) MUST be non-interactive — discover via git remote, or fail-loud-with-flag — never prompt. A prompt in quickdocker.sh/quickstart.sh is permitted (operator present) but discouraged.

**ADR-017 — af infrastructure commands must not delete customer data** (Accepted 2026-05-07). Key decision (verbatim):
> "af infrastructure commands (`af install`, `af up/down`, `af formula agent-gen`, `agent-gen-all.sh`, `make sync-formulas`, `quickstart.sh`) must not delete customer data."
> "Outside af directories: read-only. No creates, modifies, or deletes." "Inside af directories: af may manage its own artifacts."
**Implication:** writing/overwriting `.agentfactory/dispatch.json` is INSIDE an af directory → permitted. Reading `git remote` is read-only → permitted. The existing idempotent write-if-absent must be preserved so a customer-edited dispatch.json is never clobbered.

**ADR-008 — `go:embed` source-of-truth with mechanical drift test** (Accepted 2026-04-10). Key decision: embedded assets (`internal/cmd/install_formulas/`, role templates) must stay byte-parity with source via a mechanical drift test.
**Implication:** a baked-in dispatch default expressed as a Go string/func (`DefaultDispatchConfigJSON()`) is inherently single-source (no embed-vs-source drift). If a default references formula names, those formula files are themselves embedded with a drift test — the names must match real formula files.

**ADR-015 — formula three-location lifecycle** (Accepted). Formulas live/flow across `internal/cmd/install_formulas/` (embed/ship), `.agentfactory/store/formulas/` (installed), and customer edits; shipped formulas are edited in `install_formulas/`.
**Implication:** any agent named by the default dispatch must correspond to a formula shipped in `install_formulas/`. All 4 referenced formulas are shipped there (§3) ✓.

**ADR-002 — actor-scoping / `IncludeAllAgents`** — SEARCHED, NOT relevant to this domain (RBAC visibility at the issue-store boundary; unrelated to dispatch.json bootstrapping).

(Other ADRs searched by title — 001,003,004,005,006,007,009,010,011,012,013,016,018,019 — none govern dispatch-config bootstrapping directly. ADR-019 "no-container-recreation" and ADR-012 "python-preflight" touch setup but do not constrain dispatch defaults.)

### Prior Designs (`ls .designs/`)
- Only `.designs/73/` exists (this issue). No prior design-doc.md for the dispatch-bootstrap domain. The sibling orchestration produced `problem-summary-73.md` (verbatim issue capture) and `design-refinement-progress.md` (orchestration tracker) — inputs, not prior designs.

### Recent Deliberate Changes (git log on affected files)
- `internal/config/dispatch.go`: **#38** (`555f97d3`) "Extend af dispatch to support PR label matching alongside issues, add multi-label AND semantics to mappings, introduce source-based grouping with `remove_trigger_after_dispatch`, dispatch cycle locking (Fixes #37)" — established the current mappings/source schema. The `Workflow` type comment cites **issue #378** as the origin of the multi-phase `workflows` feature (dispatch.go:38). So the proposed JSON's `workflows`/`mappings`/`source`/`remove_trigger_after_dispatch` are all backed by deliberate recent additions — the design must use them as-defined, not redefine them.
- `internal/cmd/install.go`: **#58** (`9a27609d`) "Add startup.json-driven `af up`… dispatcher auto-start…" — `startup.json` default already sets `start_dispatch:true`, so the dispatcher auto-starts on `af up`. Reversing that is a deliberate-change reversal and must be justified.
- No commit has yet added a non-empty default to dispatch.json — this issue is the first to do so.
