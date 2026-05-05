# embedded-assets subsystem (claude settings + role templates)

**Covers:** internal/claude, internal/templates

## Shape

Two sibling packages, `internal/claude/` and `internal/templates/`, each use `go:embed` to bake a static asset tree into the `af` binary and expose a tiny render-and-write surface to the install flow. `internal/claude/settings.go:12-13` embeds `config/*.json` (exactly two files — `settings-autonomous.json` and `settings-interactive.json`) and ships a single public writer `EnsureSettings` (`settings.go:36-56`). `internal/templates/templates.go:10-11` embeds `roles/*.md.tmpl` (18 files as of today) and exposes `New`, `RenderRole`, `HasRole` for `text/template`-based CLAUDE.md rendering (`templates.go:27-46`). Both packages are consumed by `internal/cmd/install.go`, `internal/cmd/prime.go`, and `internal/cmd/formula.go` — they own the "install-time render to agent workspace" seam. Both trees are source-of-truth inside the binary; nothing is fetched at runtime.

## claude (internal/claude/)

### Surface

- `RoleType` enum with `Interactive = iota` and `Autonomous` (`internal/claude/settings.go:18-21`).
- `RoleTypeFor(role string, agents *config.AgentConfig) RoleType` — returns `Autonomous` when `agents.json` entry's `Type == "autonomous"`, otherwise `Interactive` (including the unknown-role case) (`settings.go:24-33`).
- `EnsureSettings(workDir string, roleType RoleType) error` — `mkdir -p workDir/.claude`, reads `config/settings-autonomous.json` or `config/settings-interactive.json` from the embed FS, writes it to `workDir/.claude/settings.json` at mode 0644 (`settings.go:36-56`).

### config/ embed tree

Only two files (`ls internal/claude/config/`):

- `settings-autonomous.json` — Claude Code hooks config for autonomous agents. Adds `af mail check --inject` to the `SessionStart` command and installs a second `Stop` hook running `hooks/fidelity-gate.sh` alongside `hooks/quality-gate.sh` (`internal/claude/config/settings-autonomous.json:9, 42-47`).
- `settings-interactive.json` — Claude Code hooks config for interactive agents. `SessionStart` runs `af prime --hook` only (no mail inject). Single `Stop` hook runs `hooks/quality-gate.sh` (`internal/claude/config/settings-interactive.json:9, 42`).

Both files wire four hooks: `SessionStart`, `PreCompact` (always `af prime`), `UserPromptSubmit` (always `af mail check --inject`), and `Stop`. Both `Stop` commands use `"${AF_ROOT}/hooks/..."` so they resolve against tmux-exported env for worktree isolation (`settings-interactive.json:42`, `settings-autonomous.json:42,46`).

### Hook-rendering contract

Note: the hook scripts on disk (`hooks/quality-gate.sh`, `hooks/fidelity-gate.sh`) are NOT embedded by this package — they are referenced by path from the settings JSON via `${AF_ROOT}/hooks/...`. The env-var fallback contract applies to those bash scripts, pinned by `internal/cmd/hook_envvar_test.go`:

- `ROLE=${AF_ROLE:-$(basename "$(pwd)")}` — verified in `hooks/quality-gate.sh:9` and `hooks/fidelity-gate.sh:14` (test: `hook_envvar_test.go:38`).
- `FACTORY_ROOT=${AF_ROOT:-$(af root)}` — test enforces no bare `FACTORY_ROOT=$(af root` and that `AF_ROOT:-` must appear (`hook_envvar_test.go:43-49`).

The embedded `settings-*.json` files participate in this contract by using `${AF_ROOT}` to locate the hook script paths (`settings_test.go:83-85, 135-137`, constraint labelled "C12" in both source and tests). Absence of `$(af root)` and presence of `${AF_ROOT}` is asserted by `settings_test.go:79-85, 131-137`.

## templates (internal/templates/)

### Surface

- `RoleData` struct: `Role`, `Description`, `RootDir`, `WorkDir` — the only fields templates can reference (`internal/templates/templates.go:13-19`).
- `New() *Templates` — parses all `roles/*.md.tmpl` via `template.ParseFS`, panics on parse error via `template.Must` (`templates.go:27-30`).
- `(*Templates).RenderRole(role string, data RoleData) (string, error)` — looks up `{role}.md.tmpl`, executes, returns rendered string (`templates.go:33-40`).
- `(*Templates).HasRole(role string) bool` — existence check via `Lookup` (`templates.go:43-46`).

### roles/ embed tree

Eighteen `*.md.tmpl` files (`ls internal/templates/roles/`). Grouped:

Base role types:
- `manager.md.tmpl` — interactive-type default. Identity, workspace, mail commands, startup protocol (`internal/templates/roles/manager.md.tmpl:1-40`).
- `supervisor.md.tmpl` — autonomous-type default. Adds "act independently" directive (`supervisor.md.tmpl:5, 42`).

Starter Specialist agents (each a standalone workflow recipe):
- `design-v3.md.tmpl`
- `design.md.tmpl`
- `gherkin-breakdown.md.tmpl`
- `investigate.md.tmpl`
- `factoryworker.md.tmpl`
- `mergepatrol.md.tmpl`

NOTE: `CLAUDE.md` (project-root) still lists `deacon`, `refinery`, `witness` under `internal/templates/`. Those were deleted in commit `8d64e6d` (2026-03-28, "removed the unused templates that could confuse implementation") after being added in `f70fb71` (2026-03-27). The project CLAUDE.md is stale.

## Seams

| Seam | Direction | Contract | Anchor |
|------|-----------|----------|--------|
| install.go (install command) | IN | renders CLAUDE.md via `templates.New().RenderRole(...)`, writes `settings.json` via `claude.EnsureSettings(...)`; falls back specialist→type-default (`manager` or `supervisor`) when `HasRole(role)` is false | `internal/cmd/install.go:239-268` |
| prime.go (prime command) | IN | re-renders role template at session start; identical specialist→type-default fallback; injects rendered string to stdout | `internal/cmd/prime.go:130-157` |
| formula.go (agent-gen) | IN | writes rendered CLAUDE.md and calls `claude.EnsureSettings(wsDir, roleType)` where `roleType` is chosen from `--type` flag string, not agents.json | `internal/cmd/formula.go:118-124, 237-245` |
| renderTemplateString | INTERNAL | formula agent-gen renders an already-generated template string against the same `RoleData` shape | `internal/cmd/formula.go:450` |

## Formative commits

| SHA | Date | Subject |
|-----|------|---------|
| `1b0e2d3` | 2026-03-23 | feat: Phase 5 Install & Settings — settings templates, role type resolution, factory init, agent provisioning (origin of `internal/claude/`) |
| `f0c9f16` | 2026-03-23 | feat: Phase 3 Prime System — role detection, templates, identity lock, prime command (origin of `internal/templates/`) |
| `f70fb71` | 2026-03-27 | feat: add deacon, refinery, witness role templates (later removed) |
| `8d64e6d` | 2026-03-28 | "removed the unused templates that could confuse implementation" — deletes deacon/refinery/witness (-1096 lines) |
| `46637f8` | 2026-03-28 | Formula-specific compaction fix — notes `af prime` re-injects from `supervisor.md.tmpl`, motivating formula context injection in prime.go |
| `7101f3a` | 2026-04-10 | Phase 3 worktree isolation merge (updates settings.json env-var handling) |
| `d053e5e` | 2026-04-11 | "preparing for git worktree isolation" — likely the `${AF_ROOT}` switch in settings JSON |
| `26245eb` | 2026-04-11 | Declares the regenerate-then-rebuild invariant: "each template must be regenerated by running `af formula agent-gen` with the just-built binary, then the binary must be rebuilt to bake the new templates into the `go:embed` filesystem" |
| `1ab9102` | 2026-04-14 | Added design-v5 agent |
| `4123f8d` | 2026-04-14 | Issue 98 fix — all phases (mass specialist regeneration) |
| `9f24d60` | 2026-04-17 | Re-ran agent-gen now that we have mcp server instead of bd (current specialist content generation) |

## Load-bearing invariants

- Rendered hook bash scripts MUST resolve `ROLE` via `${AF_ROLE:-...}` env fallback and `FACTORY_ROOT` via `${AF_ROOT:-...}` env fallback (`internal/cmd/hook_envvar_test.go:38, 43`).
- Embedded `settings-*.json` Stop-hook commands MUST reference `${AF_ROOT}` (never `$(af root)`) so worktree-running agents resolve the factory root via tmux-exported env (`internal/claude/settings_test.go:80-85, 132-137`; constraint tag "C12").
- Autonomous `SessionStart` MUST chain `af prime --hook && af mail check --inject`; interactive `SessionStart` MUST NOT contain `af mail check` (`settings_test.go:70-72, 110-124`).
- Both settings files MUST reference `quality-gate.sh` in a Stop hook (`settings_test.go:75, 127`). Autonomous additionally has `fidelity-gate.sh` (`settings-autonomous.json:46`).
- `RoleTypeFor` default for unknown roles is `Interactive`, not an error (`settings.go:25-28`; test at `settings_test.go:37-45`).
- Template-fallback order: agent-specific template → type default (`manager` for interactive, `supervisor` for autonomous). Implemented identically in install (`install.go:239-248`) and prime (`prime.go:130-139`).
- Templates MUST NOT leave `{{ .` tokens unresolved after rendering (`templates_test.go:81-83`).

## Cross-referenced idioms

- **go:embed-as-source-of-truth**: Template content lives only in the embed tree; `install.go` / `formula.go` render to disk on install (`install.go:260, formula.go:226`). To add a new role, drop a `roles/<name>.md.tmpl` file and rebuild the binary — no registry update needed, `template.ParseFS` picks it up at `New()` (`templates.go:28`).
- **Regenerate-then-rebuild**: When a formula definition changes, `af formula agent-gen` rewrites the `*.md.tmpl` file from the formula TOML, then the binary must be rebuilt so the updated template enters the embed FS (commit `26245eb`; workflow visible in commits `4123f8d`, `38b7fdd`, `9f24d60`).
- **Specialist→type-default fallback**: `HasRole(role)` check with fallback to `manager`/`supervisor` lets any role name in `agents.json` prime successfully without a bespoke template (`install.go:241-248`, `prime.go:132-139`).
- **Tiny surface, small data shape**: Templates get only 4 fields (`Role`, `Description`, `RootDir`, `WorkDir`) — forces templates to stay declarative and avoids threading config state through the render seam (`templates.go:13-19`).

## Formal constraint tags

- **C12** — worktree isolation: `${AF_ROOT}` in embedded settings JSON; `${AF_ROLE:-...}` / `${AF_ROOT:-...}` in hook scripts. Referenced by name in `settings_test.go:79, 131` and `hook_envvar_test.go:19`.

## Gaps

- `CLAUDE.md` (project root, lines ~36-38) still documents `deacon`, `supervisor`, `refinery`, `witness` as extant role templates. Only `supervisor` survives; the others were deleted in `8d64e6d`. Unknown — needs review whether the doc should be updated or whether their reintroduction is planned.
- The `PreCompact` and `UserPromptSubmit` hook bodies in the embedded settings JSON are not covered by `settings_test.go` — only `SessionStart` and `Stop` have assertions. Unknown whether this is intentional or a test gap.
- No test verifies that every `agents.json` role has a template (agent-specific or falls through to a type default). Fallback logic exists in install/prime but is not asserted at factory-validation time.
- Permissions model: `settings-*.json` embeds only a `hooks` block — no `permissions`, no `env` keys. Unknown — needs review whether Claude Code's default permissions are what's wanted, or whether this is an explicit minimal-contract choice.
- No documented policy on whether `RenderRole`'s output must be idempotent across `af prime` reinjections vs initial `af install`. Both call sites pass the same `RoleData` shape, but `prime.go` additionally appends formula/checkpoint/mail output (`prime.go:160-176`) that `install.go` does not — divergence is real but uncodified.
