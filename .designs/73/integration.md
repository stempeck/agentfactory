# D6 — Integration (integration.md)

Owner of (verification.md): EVERY AC and EVERY constraint lists Integration as an
owner — this is the central dimension. It governs how the baked-in default fits the
existing install/dispatch system, which call sites change, and which tests change.

This dimension surfaces THE pivotal finding of the whole design (verified this
session): the proposed default references 4 specialists that a FRESH bootstrap does
NOT provision. `quickstart.sh` runs only `af install --init`, `af install manager`,
`af install supervisor` (quickstart.sh:442, 451, 463, verified) — it NEVER runs
`af install --agents`. So on a fresh setup, agents.json has only manager+supervisor
(install.go:143, verified), and the default's mappings reference absent agents →
`ValidateDispatchConfig` fails (dispatch.go:100-104, verified) the moment the
dispatcher starts. Resolving this (C-6) is the integration crux.

---

## I0. Verified integration map (call sites that change / are touched)

| Site | File:line (verified) | Change |
|------|----------------------|--------|
| Starter-config map | `internal/cmd/install.go:139-148` | Replace the inline `dispatch.json` literal (:145) with `config.DefaultDispatchConfigJSON(repo)` |
| Repo discovery | `internal/cmd/install.go` (`runInstallInit`, :97+) | NEW: discover `owner/name` before building starter configs |
| Default builder | `internal/config/dispatch.go` or `config.go` | NEW: `DefaultDispatchConfigJSON(repo)` (mirrors `DefaultFactoryConfigJSON`, config.go:111) |
| Bootstrap specialist provisioning | `quickstart.sh` `configure_factory` (:414-471) | C-6: add specialist provisioning, OR rely on a dispatcher-tolerance change |
| Dispatcher cross-file check | `internal/config/dispatch.go:93-138` (`ValidateDispatchConfig`) | OPTIONAL (SEC2.2): skip-and-warn on unknown agent instead of failing |
| Write path (unchanged) | `internal/cmd/config_set.go:67-99` | No change — already runs cross-file validation before writing (verified) |

The write path `af config dispatch set` already does the right cross-file check
(config_set.go:85-91, verified) and is the operator's repair command — no change
needed there.

---

## I1. WHERE the default is produced and written

### Option I1.1 — Build via `DefaultDispatchConfigJSON(repo)` in `internal/config`, called from `runInstallInit` — RECOMMENDED

Mirror the established `factory.json` pattern: `runInstallInit` discovers the repo,
calls `config.DefaultDispatchConfigJSON(repo)`, and uses the result as the
`dispatch.json` value in the `starterConfigs` map (install.go:139-148, verified).
The existing write-if-absent loop (install.go:150-157, verified) writes it.

- Trade-offs: One call site changes (the map literal), one new function added.
  Reuses the entire existing write/idempotency machinery. Honors C-2 (written at
  first-create by the same path) and the single-source pattern (codebase-snapshot
  Decision History, issue #371 Gap-6).
- Reversibility: Easy.
- Constraints: satisfies C-1, C-2, AC-1, AC-3, complies with ADR-008. Recommended.

### Option I1.2 — Write the default from quickstart.sh via `af config dispatch set` after init — REJECTED

Have quickstart pipe a JSON default into `af config dispatch set` (config_set.go,
verified) after `af install --init`.

- REJECTED: violates [C-1], [C-2]. C-1 requires the default baked into the TOOL
  (Go), not authored by a bootstrap script; C-2 requires it written when
  dispatch.json is FIRST created during init, not by a later command. A
  quickstart-authored default also reintroduces a doc/script that can drift from
  the schema (the opposite of the single-source pattern). Reject.

---

## I2. Repo discovery integration point (ADR-014 compliant)

### Option I2.1 — Discover inside `runInstallInit` (Go), non-interactive, before building starter configs — RECOMMENDED

`runInstallInit` runs after quickstart `cd`s into the repo (quickstart.sh:428, 442,
verified), so the git remote is present in CWD. Discover via `gh repo view` / `git
remote` (D1/A2), validate (D5/SEC1.1), inject into the default.

- Trade-offs: Keeps discovery in the Go path where the install logic already lives,
  non-interactively (ADR-014). The git remote is guaranteed present at this point
  (codebase-snapshot §6 confirms quickstart cd's in before init). Best-effort: on
  failure, degrade (A3.1).
- Reversibility: Easy.
- Constraints: satisfies C-3, complies with ADR-014 (no prompt), ADR-017
  (read-only). Recommended.

### Option I2.2 — Pass the repo to `af install --init` as a new flag/arg from quickstart — REJECTED

Add `af install --init --repo <owner/name>` and have quickstart compute and pass it.

- REJECTED: reverses [Decision History: install.go #58 / runInstallInit takes NO
  repo argument] without need, and pushes discovery into a bootstrap SCRIPT.
  codebase-snapshot §6 states "runInstallInit takes NO repo argument and does NO
  git-remote lookup." Adding a flag is more surface than I2.1, and ADR-014 prefers
  the Go path to be self-sufficient (discover or fail-loud) rather than depending
  on a script to feed it. The Go path can discover the remote itself (I2.1) since
  CWD is the repo. (Not strictly forbidden, but strictly worse than I2.1.)

---

## I3. THE C-6 RESOLUTION — making the default's agent references resolvable on a fresh factory

This is the decisive integration choice. Three resolutions; the design needs at
least one (and the recommended posture combines I3.1 + I3.2 as primary +
defense-in-depth).

### Option I3.1 — Provision the referenced specialists during bootstrap (run `af install --agents` in quickstart) — RECOMMENDED (primary)

Add a step to `quickstart.sh` `configure_factory` (after manager/supervisor
provisioning, :448-470) that runs `af install --agents`, which regenerates+installs
EVERY shipped formula's agent (agent-gen-all.sh iterates all `install_formulas/*.formula.toml`,
:134-153, verified), placing the 4 referenced specialists (and all others) into
agents.json. `ValidateDispatchConfig` then passes.

- Trade-offs: Makes the default valid-by-construction (C-5). `af install --agents`
  "refuses to run from a worktree; requires an already-initialized factory"
  (codebase-snapshot §4) — both conditions are met at the quickstart moment (real
  repo root, post-init). Cost: heavier bootstrap (rebuilds af, provisions ~15
  agents) — a one-time setup cost, acceptable for the "land and go" scenario the
  source describes. This is the cleanest path to AC-2/AC-4 (the specialists the
  default routes to actually exist and are formula-bearing).
- Reversibility: Moderate (bootstrap-sequence change in quickstart.sh; the ADR-014
  bootstrap-script exemption applies — quickstart is an operator setup script).
- Constraints: satisfies C-6, C-5, AC-2, AC-4, AC-5; complies with ADR-015
  (formulas flow through `install_formulas/` → installed agents) and ADR-014
  (quickstart is an exempt bootstrap script). Recommended as primary.

### Option I3.2 — Make the dispatcher skip unknown-agent mappings (skip-and-warn) — RECOMMENDED (defense-in-depth)

Change the dispatch path so a mapping whose agent is absent from agents.json is
SKIPPED with a loud warning, instead of failing the whole cycle. Today
`ValidateDispatchConfig` returns an error on the first unknown agent
(dispatch.go:102, verified) — this option relaxes that for the dispatch-loop caller
(NOT for `af config dispatch set`, which should stay strict so a human typo is
caught at write time).

- Trade-offs: The autonomous path degrades per-mapping rather than all-or-nothing,
  so a partially-provisioned factory still dispatches the agents it has. Risk:
  could mask a genuine config typo — mitigated by the loud warning. Requires
  splitting the validation contract: strict at the write boundary (config_set.go),
  tolerant at the dispatch boundary. This is a non-trivial behavior change and must
  be carefully scoped to avoid weakening the write-path guarantee.
- Reversibility: Moderate (alters a validation contract used by the dispatch loop).
- Constraints: supports C-6/AC-2 robustness, must preserve C-5's "valid by
  construction" intent (the warning names `af install --agents` as the remedy,
  per U2.1). Recommended as defense-in-depth atop I3.1.

### Option I3.3 — Ship a default whose mappings reference ONLY manager/supervisor — REJECTED

(Same as Data/D2.2-B.) REJECTED: fails [AC-2], [AC-4]. Routing trigger labels to
non-formula agents (manager/supervisor are not formula-bearing specialists,
codebase-snapshot §6) dispatches no formula work, so the autonomous outcome AC-2/
AC-4 require never happens. Reject.

---

## I4. AC-5 — slung formulas push PRs without doctor/human (verification this dimension owns)

AC-5 requires that slung agents push branches as PRs against main without
`doctor --fix` or human interaction as an ongoing dependency. This is a PROPERTY of
the referenced formulas (`rapid-implement`, `rapid-increment`, etc.), not of this
change directly. This dimension's integration obligation: confirm the default
routes only to formulas that have a push-PR terminal behavior.

### Option I4.1 — Default routes to the rapid-* / ultra-review formulas (which carry the formula-driven PR-push behavior); no change to those formulas — RECOMMENDED

The 4 referenced formulas are shipped specialists (codebase-snapshot §3) and are
the SAME formulas the factory already uses for autonomous PR work. This change does
not modify them; it only ensures the dispatch default routes to them. AC-5/AC-6's
"known working formula process that IS their IDENTITY" is satisfied by using the
existing formulas unchanged.

- Trade-offs: Zero formula edits; leans on the existing, verified specialist-
  dispatch + formula-step machinery (`af sling --agent`, codebase-snapshot §4).
  This dimension does NOT independently verify each formula's internal PR-push step
  (that is a formula-content concern outside this config-bootstrap change and
  [UNVERIFIED] here at the step level) — but the routing is correct, and the
  scope (source.md:150-154) is the config bootstrap, not formula internals.
- Reversibility: Easy (routing only).
- Constraints: satisfies AC-5, AC-6 (uses the identity formulas unchanged).
  Recommended.

---

## Reversibility (this dimension): Easy–Moderate

I1/I2/I4 are Easy (additive, localized). I3.1 (bootstrap provisioning) and I3.2
(dispatcher tolerance) are Moderate.

## Tests that change (verified test surfaces)

- A golden/unit test asserting `af install --init` (in a temp repo with a known
  `git remote`) writes a dispatch.json whose `repos` is `["<org>/<repo>"]` and
  whose mappings/workflows match `DefaultDispatchConfigJSON` (verification.md AC-1,
  AC-3 evidence rows).
- A test asserting `DefaultDispatchConfigJSON(repo)` output passes
  `validateDispatchConfig` AND (given an agents.json containing the 4 specialists)
  `ValidateDispatchConfig` — pinning C-6.
- If I3.2 is taken: a dispatch-loop test asserting an unknown-agent mapping is
  skipped-with-warning, not fatal, while `af config dispatch set` still rejects it.
- The existing `config_test.go` patterns (Save/Load round-trips, e.g. the
  BuildHostConfig round-trip at config_test.go:1163-1189, verified) are the model
  for the new default round-trip test.

## Dependencies produced

- PROVIDES to **Data (D2)** and **Security (D5)**: the resolved C-6 sequencing —
  specialists provisioned by `af install --agents` in quickstart BEFORE the
  dispatcher runs `ValidateDispatchConfig`.
- PROVIDES to **API (D1)**: the single call site (`runInstallInit` starterConfigs
  map) and the discovery ordering.
- PROVIDES to **UX (D3)**: confirmation that the dispatcher auto-starts on `af up`
  (startup.json `start_dispatch:true`, install.go:147, verified), making the
  zero-touch path (U1.1) real.
- REQUIRES from **Data (D2)**: the exact agent list the default references.
- REQUIRES from **API (D1)**: the `DefaultDispatchConfigJSON` signature.
- REQUIRES from **Security (D5)**: the validated repo string and the C-6 resolution
  choice (provision vs tolerate).

## Risks identified

| Risk | Severity | Mitigation |
|------|----------|------------|
| C-6: default references 4 specialists absent from fresh agents.json → `ValidateDispatchConfig` fails at dispatch-start | HIGH | I3.1 run `af install --agents` in quickstart (primary) + I3.2 dispatcher skip-and-warn (defense-in-depth) |
| Adding `af install --agents` to quickstart lengthens/heavies the bootstrap and could fail (rebuilds af) | Medium | It is an existing, verified command with worktree/init guards (codebase-snapshot §4); run it best-effort with a clear failure message; I3.2 backstops a partial provision |
| I3.2 dispatcher tolerance masks a real config typo | Medium | Loud per-mapping warning naming the remedy; keep `af config dispatch set` strict (config_set.go unchanged) so human edits are still validated |
| Discovery runs in runInstallInit but git remote absent (local-only repo) | Medium | Best-effort + degrade (A3.1); install still succeeds with loadable status-quo default |
| Changing `ValidateDispatchConfig` behavior risks the write-path guarantee | Medium | Scope the tolerance to the dispatch-loop caller ONLY; the write path (config_set.go:85-91) keeps the strict check |
