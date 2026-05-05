# formula subsystem

**Covers:** internal/formula

## Shape
`internal/formula/` is a pure library for parsing, validating, and resolving TOML-defined workflow descriptions into structured objects (`internal/formula/parser.go:11`, `internal/formula/types.go:29`). It models four kinds of workflow: `convoy`, `workflow`, `expansion`, and `aspect` (`internal/formula/types.go:17-26`), each projecting the same `Formula` struct but populating different fields. The package owns the formula type model (`types.go`), TOML decoding (`parser.go`), filesystem discovery (`discover.go`), DAG validation (`validate.go`) and ordering (`sort.go`), variable resolution and `{{name}}` expansion (`vars.go`), and a wait-step backoff config parser (`backoff.go`). Execution is not performed here — that is owned by `internal/cmd/` (notably `sling.go` for instantiation and `formula.go` for `agent-gen`). The subsystem was extracted from an earlier "gastown" project in commits `34c47f5` (types + vars, 2026-03-27) and `29a7b3c` (parser/validate/sort/discover, 2026-03-27).

## Public surface

Types (`types.go`):
- `FormulaType string` constants `TypeConvoy|TypeWorkflow|TypeExpansion|TypeAspect` — `types.go:17-26`.
- `Formula` — union-of-kinds struct; common (`Name/Description/Type/Version`), workflow (`Steps/Vars`), convoy (`Prompts/Output/Legs/Synthesis`), expansion (`Template`), aspect (`Aspects`), plus shared `Inputs` — `types.go:29-54`.
- `Input` — declares CLI-supplied parameter with `Required`, `RequiredUnless`, `Default`, `Type` — `types.go:65-71`. `RequiredUnless` is parsed but never consumed inside the package (grep finds no readers).
- `Output`, `Leg`, `Synthesis`, `Gate`, `Step`, `Template`, `Aspect`, `Var` — `types.go:74-134`.
- `Var.Source` enumerates `"cli"|"env"|"literal"|"hook_bead"|"bead_title"|"bead_description"` (and `"deferred"`, see below) — `types.go:126-134`, `validate.go:184-188`.
- `(*Formula).GetDependencies(id)` — returns `Needs` for workflow/expansion, `Synthesis.DependsOn` when `id == "synthesis"` for convoy, nil for aspect — `types.go:149-170`.
- `(*Formula).GetAllIDs()` — enumerates step/leg/template/aspect IDs — `types.go:173-194`. No in-repo caller (grep `formula.GetAllIDs`, `formula.GetDependencies` returns no non-test hits).

Functions (`parser.go`, `discover.go`, `sort.go`, `validate.go`, `vars.go`, `backoff.go`):
- `ParseFile(path string) (*Formula, error)` — reads disk + `Parse` — `parser.go:11-17`.
- `Parse(data []byte) (*Formula, error)` — TOML-decodes via BurntSushi/toml, infers `Type` from content if unset, then `Validate` — `parser.go:20-34`.
- `(*Formula).inferType()` — picks type from whichever collection is non-empty (`Steps` → workflow, `Legs` → convoy, `Template` → expansion, `Aspects` → aspect) — `parser.go:37-51`.
- `FindFormulaFile(name, workDir string) (string, error)` — `discover.go:18-42` (see "Discovery").
- `(*Formula).TopologicalSort() ([]string, error)` — Kahn's BFS; convoy/aspect short-circuit because legs/aspects are parallel — `sort.go:7-87`.
- `(*Formula).ReadySteps(completed map[string]bool) []string` — returns items whose `Needs` are all in `completed` — `sort.go:91-142`.
- `(*Formula).GetStep/GetLeg/GetTemplate/GetAspect(id)` — lookups by ID — `sort.go:145-182`.
- `(*Formula).Validate()` — dispatched per-type; also runs `validateVars` and `validateInputVarCollision` — `validate.go:6-37`.
- `(*Formula).checkCycles()` — DFS cycle detector in addition to the Kahn check in sort — `validate.go:144-181`.
- `ExpandTemplateVars(text string, ctx map[string]string) string` — substitutes `{{name}}` using regex `\{\{(\w+)\}\}`; unknown vars are left as-is — `vars.go:9-26`.
- `ResolveContext` — resolution inputs: `CLIArgs`, `EnvLookup func(string)string`, `HookedBeadID`, `BeadTitle`, `BeadDescription` — `vars.go:29-41`.
- `ResolveVars(vars, ctx) (map[string]string, error)` — resolves each declared `Var`, skipping `deferred` — `vars.go:47-62`.
- `MergeInputsToVars(inputs, vars) (map[string]Var, error)` — converts `Input` entries into `Var{Source:"cli"}` entries, erroring on name collisions — `vars.go:147-170`. Lossy: `Input.Type` and `Input.RequiredUnless` are dropped (comment at `vars.go:144-146`).
- `BackoffConfig` + `ParseBackoffConfig(string) *BackoffConfig` — parses `base=30s, multiplier=2, max=10m` strings; returns nil if `base` is missing — `backoff.go:9-58`. No in-repo production consumer (grep across `internal/` shows only declarations and its own tests); the design doc for Phase 6A described this as carried over from gastown for future wait-type steps (commit `34c47f5` message).

## Variable resolution rules

Vars are resolved in `ResolveVars` → `resolveVar` (`vars.go:47-142`). Precedence per declared variable:

1. **Universal CLI override (CR-1)** — `vars.go:65-70`: if `ctx.CLIArgs[name]` is set, it is returned regardless of the variable's declared `Source`. Comment at `vars.go:65`: "Universal CLI override: --var key=val takes precedence regardless of source type (CR-1)." Introduced in commit `643982e` (2026-03-30): "Adds a universal CLI override so --var takes precedence regardless of source type (CR-1)."
2. **Source-specific path** when CLI did not supply the value — `vars.go:72-141`:
   - `"cli"` — fall back to `Default`, else error if `Required`, else empty string (`vars.go:74-82`).
   - `"env"` — call `ctx.EnvLookup(name)`; if empty, fall back to `Default`, then `Required` error, then empty (`vars.go:84-98`). `EnvLookup` being a field rather than a direct `os.Getenv` is the library-boundary invariant from commit `d020a5e` (2026-04-15) "remove os.Getenv from library-layer code (Phase 1)." When `EnvLookup` is nil, env-sourced vars resolve to empty (`vars.go:33-34, 85-88`).
   - `""` and `"literal"` — return `Default` (`vars.go:100-101`). Sourceless vars being accepted as literal was fixed in commit `ce9fac2` (2026-03-29) after they previously hit the unknown-source default case and crashed `af sling` pre-bead-creation.
   - `"hook_bead"` / `"bead_title"` / `"bead_description"` — read `ctx.HookedBeadID` / `BeadTitle` / `BeadDescription`, then `Default`, then `Required` error with guidance "Use --var ...=<value>" (`vars.go:103-137`). Bead sources added in commit `643982e`.
   - `"deferred"` — **skipped entirely** by `ResolveVars` (`vars.go:51-53`). Unexpanded `{{name}}` survives template substitution because `ExpandTemplateVars` leaves unknown vars as-is (`vars.go:23-24`). Added in commit `c40d91d` (2026-04-01) "implement formula inputs→vars merge and deferred source (issue #31 phase 1)."
   - Any other string — `fmt.Errorf("unknown variable source %q...")` (`vars.go:139-140`).

Template expansion uses the resolved map: callers (e.g. `internal/cmd/sling.go:451-464`) pass each step's title and description through `ExpandTemplateVars`. Unknown `{{name}}` placeholders survive expansion (`vars.go:23-24`) — this is how `deferred` variables remain for later runtime substitution.

Inputs vs. vars:
- `MergeInputsToVars` projects `Inputs` into `Var{Source:"cli", Required, Default, Description}` entries before resolution (`vars.go:147-170`). Caller-side, `internal/cmd/sling.go:375-381` only runs the merge when `f.Type == formula.TypeWorkflow`.
- `validateInputVarCollision` rejects any formula where an `Inputs` key shadows a `Vars` key (`validate.go:197-207`). `MergeInputsToVars` has a belt-and-braces second check that also errors on collision (`vars.go:158-160`).

## DAG validation + topo sort

Validation (`validate.go`):
- `Validate` requires non-empty `Name` and a valid `Type` (`validate.go:7-13`).
- Vars validation restricts `Source` to the closed set `cli|env|literal|hook_bead|bead_title|bead_description|deferred` (empty string also allowed as implicit literal) — `validate.go:183-195`.
- Input/var collision check — `validate.go:197-207`.
- Per-type validators (`validateConvoy/Workflow/Expansion/Aspect`, `validate.go:39-141`) enforce: at least one item of the relevant kind; non-empty IDs; unique IDs; and for `workflow`/`expansion`, every `Needs` entry must refer to a known ID. Convoy additionally checks `Synthesis.DependsOn` references exist (`validate.go:55-61`). Aspect has no dependency model.
- `checkCycles` (`validate.go:144-181`) runs a DFS-based cycle detector over workflow `Steps`. There is **no cycle check for `expansion` Templates** — `validateExpansion` only checks reference integrity (`validate.go:97-122`).

Topological sort (`sort.go`):
- Workflow and expansion use Kahn's algorithm with in-degree counting and a reverse adjacency map (`sort.go:42-86`). Cycles surface as `result-length != items-length` → `fmt.Errorf("cycle detected in dependencies")` (`sort.go:82-84`). For workflow this is redundant with `checkCycles` at validate time; for expansion it is the only cycle check.
- Convoy and aspect return the items in declaration order, bypassing sort (`sort.go:28-37`).
- `ReadySteps(completed)` is an independent scheduler view: for workflow/expansion it filters to items whose `Needs` are all in `completed`; for convoy/aspect it returns all incomplete items (`sort.go:91-142`).

## Discovery

`FindFormulaFile(name, workDir)` (`discover.go:18-42`) imposes this filesystem contract:

1. Search `<factoryRoot>/.beads/formulas/` first, where `factoryRoot` comes from `config.FindFactoryRoot(workDir)` — `discover.go:22-24`.
2. Fall back to `$HOME/.beads/formulas/` — `discover.go:27-29`.
3. In each directory, try filename `<name>.formula.toml` first, then `<name>.formula.json` — `discover.go:31-39`. The `.formula.json` branch exists but `Parse` only decodes TOML (`parser.go:22`), so a `.formula.json` file would be found and then fail to parse. No test exercises the JSON path; grep finds no `.formula.json` files in the repo.
4. Returns `formula %q not found in search paths` if nothing matches — `discover.go:41`.

The formula directory lives under `.beads/` because formulas were originally co-located with the beads datastore; the path is hard-coded in this function.

## Seams

| Seam | Direction | Contract | Anchor |
|------|-----------|----------|--------|
| BurntSushi/toml | OUT | `toml.Decode(string(data), &f)` decodes TOML bytes into `Formula` via struct tags | `internal/formula/parser.go:22` |
| `internal/config.FindFactoryRoot` | OUT | Factory root lookup to anchor `.beads/formulas/` search | `internal/formula/discover.go:22` |
| `os` stdlib (file I/O, `UserHomeDir`) | OUT | Read formula files; resolve `~/.beads/formulas/` | `internal/formula/parser.go:12`, `internal/formula/discover.go:27` |
| Caller-supplied `EnvLookup` | OUT (inverted) | Library never calls `os.Getenv` directly; caller injects env reader via `ResolveContext.EnvLookup` | `internal/formula/vars.go:33-34, 86-88`; invariant established by commit `d020a5e` |
| `internal/cmd/formula.go` (agent-gen) | IN | Consumes `FindFormulaFile` + `ParseFile`; reads `f.Name`, `f.Description`, `f.Type`, `f.Version`, `f.Steps`, `f.Vars`, `f.Legs`, `f.Synthesis`, `f.Template`, `f.Aspects` to generate agent `CLAUDE.md` | `internal/cmd/formula.go:84,89,365` |
| `internal/cmd/sling.go` (formula instantiation) | IN | `FindFormulaFile` → `ParseFile` → `parseCLIVars` → `MergeInputsToVars` (workflow only) → `ResolveVars` → `TopologicalSort` → `ExpandTemplateVars` over step/leg/template titles and descriptions | `internal/cmd/sling.go:303,309,377,383,394,400,451-464` |
| `internal/cmd/install.go` tests | IN | Smoke-parses every embedded `*.formula.toml` | `internal/cmd/install_test.go:232` |

## Formative commits

| SHA | Date | Subject | Why |
|-----|------|---------|-----|
| `34c47f5` | 2026-03-27 | feat: add internal/formula package — formula types and variable system (Phase 6A) | Initial extraction from gastown: `types.go`, `vars.go`, `backoff.go`. Establishes four formula kinds and the `cli|env|literal` resolution model. Bead sources were stubs. |
| `29a7b3c` | 2026-03-27 | feat: add formula parser, validation, DAG sort, and discovery (Phase 6B) | Adds `parser.go`, `validate.go` (including DFS cycle check), `sort.go` (Kahn + ReadySteps), `discover.go` (factory → home search). |
| `ce9fac2` | 2026-03-29 | Fix sourceless-var crash | Accept `Source == ""` as implicit literal (`vars.go:100`). Without this, any formula with late-bound placeholder vars crashed `af sling` before bead creation. |
| `643982e` | 2026-03-30 | Bead variable sources + CR-1 universal CLI override | Replaces bead-source stubs with working `hook_bead`/`bead_title`/`bead_description` resolution. Adds the universal `--var` override as CR-1. Expands `ResolveContext` with bead metadata. Resolves issue #12. |
| `c40d91d` | 2026-04-01 | feat: formula inputs→vars merge + deferred source (issue #31 phase 1) | Adds `MergeInputsToVars`, the `deferred` source (skipped by resolver, passes through template expansion untouched), and input/var collision validation. |
| `d020a5e` | 2026-04-15 | fix: remove os.Getenv from library-layer code (Phase 1) | Replaces a direct `os.Getenv` in `vars.go` with `ResolveContext.EnvLookup`. Enforces invariant: env reads only at cobra-command boundary. |

## Load-bearing invariants

- **DAG must be acyclic for workflow.** Enforced twice: `checkCycles` DFS in `Validate` (`validate.go:144-181`) and Kahn's in `TopologicalSort` (`sort.go:82-84`).
- **DAG acyclicity for `expansion` is only enforced at sort time.** `validateExpansion` checks reference integrity but not cycles (`validate.go:97-122`); `TopologicalSort` is the only line of defence (`sort.go:82-84`). This is unstated in comments — unknown — needs review whether this is deliberate.
- **IDs are unique within their kind.** `validateConvoy/Workflow/Expansion/Aspect` all maintain `seen` maps and reject duplicates (`validate.go:44-53, 71-80, 102-111, 129-138`).
- **All `Needs` entries must resolve to known IDs** in workflow/expansion (`validate.go:82-88, 113-119`); same for convoy `Synthesis.DependsOn` (`validate.go:55-61`).
- **CLI `--var` always wins.** `resolveVar` checks `CLIArgs` before dispatching on `Source` (`vars.go:65-70`, CR-1).
- **Input keys cannot shadow Var keys.** Checked in `Validate` (`validate.go:197-207`) and again defensively in `MergeInputsToVars` (`vars.go:158-160`).
- **Unknown `{{name}}` placeholders are preserved**, not replaced with empty (`vars.go:23-24`). This is what makes the `deferred` source work end-to-end.
- **`deferred` vars are never resolved here.** `ResolveVars` skips them (`vars.go:51-53`) and relies on `ExpandTemplateVars` leaving unknown placeholders intact for downstream substitution.
- **Library layer does not call `os.Getenv`.** Env reads must flow through `ResolveContext.EnvLookup` (`vars.go:33-34, 86-88`, invariant from commit `d020a5e`).
- **`Formula.Type` is mandatory post-parse.** If absent in TOML, `inferType` fills it from non-empty collection; otherwise `Validate` rejects with invalid-type error (`parser.go:37-51`, `validate.go:11-13`).

## Cross-referenced idioms

- **Inverted-environment access.** `ResolveContext.EnvLookup func(string) string` (`vars.go:33-34`) is an instance of the "caller injects env accessor, library never touches `os.Getenv`" idiom introduced repo-wide by commit `d020a5e`. Parallel sites per that commit's body: `checkpoint/checkpoint.go`, `tmux/tmux.go`.
- **Type-tagged union struct with per-kind dispatch.** `Formula` carries fields for all four kinds; every traversal (`GetDependencies`, `GetAllIDs`, `TopologicalSort`, `ReadySteps`, `Validate`) switches on `f.Type` (`types.go:149-194`, `sort.go:11-40, 93-139`, `validate.go:25-34`). Adding a fifth kind requires updating all dispatch switches.
- **Placeholder preservation for late binding.** `{{name}}` that is neither provided via CLI nor declared as a non-deferred `Var` survives template expansion untouched (`vars.go:18-25`), enabling downstream runtime substitution (e.g. at the `af done` / agent execution boundary).
- **Two-tier discovery: factory root, then home.** `discover.go:22-29` mirrors the config-search idiom elsewhere in the codebase (factory-root-first with home fallback).

## Formal constraint tags

- **CR-1** — Universal CLI override: `--var key=val` takes precedence regardless of declared `Source`. `internal/formula/vars.go:65-70`. Commit `643982e` message.
- No other C-n / CR-n / D-n / R-* tags found in-package. Grep `CR-|C-\d|D-\d|R-\d` inside `internal/formula/` returns only the CR-1 comment.

## Gaps

- **`BackoffConfig` has no production consumer.** `ParseBackoffConfig` is exported and unit-tested (`backoff_test.go`) but grep across `internal/` finds no caller outside the package. It was extracted in Phase 6A (`34c47f5`) "for wait-type steps," but wait-type step execution is not in the codebase. unknown — needs review whether it is still pending integration or dead code.
- **`GetAllIDs` and `GetDependencies` have no in-repo callers.** Grep for `formula.GetAllIDs` / `formula.GetDependencies` returns no non-test matches. unknown — needs review whether these are unused API surface or intended for external consumers.
- **`Input.Type` and `Input.RequiredUnless` are parsed but unused.** `MergeInputsToVars` drops both (`vars.go:144-146` comment). `RequiredUnless` has no reader anywhere in the package. unknown — needs review whether callers read these directly off `f.Inputs` or whether they are vestigial.
- **`.formula.json` discovery but TOML-only parser.** `discover.go:31` accepts `.formula.json` as a fallback extension, but `Parse` only calls `toml.Decode` (`parser.go:22`). A `.json` file would be discovered and then fail to parse. unknown — needs review whether JSON support was planned and dropped, or the extension list is aspirational.
- **Expansion cycles only caught at sort time.** Unlike workflow, `validateExpansion` does not run `checkCycles`; a cyclic expansion formula passes `Validate` and only fails later inside `TopologicalSort`. unknown — needs review whether deliberate.
- **`Gate` type is declared on `Step` but not referenced by this package's logic.** `types.go:97-102, 110` define it; no validation, resolution, or sort code in `internal/formula/` reads it. Consumers appear to be in `internal/cmd/` (grep finds it in `internal/cmd/formula_test.go`). unknown — needs review whether `internal/formula/` should validate gate fields (e.g. `Type`, `Timeout`).
- **`Formula.Version` is decoded but never validated or read** inside the package. It is emitted by agent-gen (`internal/cmd/formula.go:365`). No minimum/maximum/enum check here.
