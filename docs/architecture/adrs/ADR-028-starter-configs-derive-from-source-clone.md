# ADR-028: Starter configs derive from the bootstrapping source clone; af ships no copy of them

**Status:** Proposed
**Date:** 2026-10-18 (issue #654, `.designs/654/design-doc.md` — cited as text: the OSS repository
does not carry `.designs/`; revised by the PR #655 review)

## Context

`af install --init` used to write a new factory's starter configs from Go literals in
`internal/cmd/install.go`. Those literals drifted from the tracked `.agentfactory/` configs the
agentfactory repo actually runs on, so every fresh factory started from a 2-agent scaffold far
behind the tracked roster and dispatch configs, and keeping them current took dedicated re-sync
commits — a Go edit per config change.

There is more than one agentfactory: the pro and OSS variants track different agents in their
`.agentfactory/` configs (`models.json` pins, `startup.json` `watchdog_agents`, `dispatch.json`
mappings). A repository bootstrapped from either must get that variant's configs. And
`agents.json` is not a starter config at all: `af install --agents` generates it from the formulas
installed in the repository being bootstrapped, so its contents are unique to that repository.

The configs are also transplanted into a *foreign* project. Bytes that are right for the
agentfactory repo (its own repos, cron triggers, factory name, gateway-bound model pins) are
wrong — and in the dispatcher's case actively harmful — anywhere else.

## Decision

1. **Derived from the clone, never copied into af.** `resolveAFSource` (`internal/cmd/formula.go`)
   picks `AF_SOURCE_ROOT` when set, else the source tree `af` was built from (`-X main.sourceRoot`,
   `Makefile`). The seed reads that clone's working-tree `.agentfactory/` (uncommitted edits
   included; no runtime git dependency), so a factory bootstrapped from pro gets pro's configs and
   one bootstrapped from OSS gets OSS's. The binary carries no mirror of those files: a mirror holds
   one variant's agent names and has to be re-synced by hand whenever the tracked files change.

2. **`agents.json` is af's own.** A fresh factory's `agents.json` holds only the built-in manager
   and supervisor roles (`builtinRoleEntries`, `internal/cmd/seed_configs.go`), per ADR-020 D1; the
   rest of the roster is what `af install --agents` generates in that repository. No clone's
   `agents.json` is ever read.

3. **No source tree, no derived configs.** When no clone resolves, only the files a factory cannot
   run without and af itself owns are written — `agents.json`, `messaging.json` (`all` = the
   built-in roles) and `factory.json` (`config.DefaultFactoryConfigJSON`). Every other member is
   logged `not-seeded (no agentfactory source tree)` and one notice names them and
   `AF_SOURCE_ROOT`.

4. **A closed name list is the manifest and the allowlist.** `seedConfigNames`
   (`internal/cmd/seed_configs.go`) names every seeded file. Only those names are ever read from a
   clone, so machine-local files in a source checkout (`telemetry.json`, `secrets/`, gate files)
   can never be transplanted. `.agentfactory/litellm.codex-subscription.yaml` is deliberately
   absent: quickstart writes it only for a subscription-mode factory.

5. **Stat first, all-or-nothing over what will be written.** A name that already exists in the
   target factory is `kept-existing`: it is never resolved, sanitized or validated, so a drifted
   clone cannot break `--init` on a populated factory. `litellm.yaml` is seeded only when
   `models.json` is seeded with it, because the two are a coherent pair. Every to-be-written
   candidate is resolved and validated before anything is written; one refusal writes nothing.

6. **Hard errors under a resolved clone.** For a to-be-written name, a missing file, invalid JSON,
   an unknown key (strict decode — this binary does not know the key, so the clone is newer than
   the binary), or a failed validation is a hard error carrying the remedy. The one exception is a
   `messaging.json` or `factory.json` the clone lacks: af owns content for both (decision 3), so it
   writes that content and the provenance line says the clone lacked the file; a member that is
   present but unreadable is still a hard error. Agent references are
   checked against the roster the factory will hold: `startup.json` and `dispatch.json` against the
   built-in roles plus one agent per formula `--init` installs (the agents `af install --agents`
   generates), `messaging.json` against the `agents.json` actually written, because the mail router
   rejects an unknown member when it loads. A reference that only fails against an operator's
   pre-existing `agents.json` is a warning, because that file is the operator's, not the seed's.

7. **The sanitizer is a property, not a key list.** A seeded config must not carry the agentfactory
   repo's project identity or machine substrate, and must not arm the dispatcher:
   - identity and substrate keys are rewritten — the factory `name` is re-derived from the project
     directory, model pins to non-portable profiles are dropped, startup `telemetry` `"on"`
     becomes `"default"` when the factory has no `telemetry.json`, and startup `improvement` `"on"`
     becomes `"default"` unless the factory's `.improvement-hook` already reads `on` (`--init`
     never arms the improvement hook);
   - dispatcher triggers are neutralised — `repos` is emptied and `crons` dropped — so the seeded
     `dispatch.json` loads as not-configured and the dispatcher friendly-skips until the operator
     opts in;
   - every effective rewrite is reported as a `sanitized:` record on stdout and in
     `.agentfactory/.config-seed.log`.

   The dispatcher half of the property is enforced at seed time, not only in CI. The seam
   re-parses its sanitized `dispatch.json` with `config.ParseDispatchConfig`, the loader's own
   validation, and refuses to write it unless the verdict is the `ErrMissingField`
   not-configured skip. A trigger section added to `DispatchConfig` later therefore fails the seed
   loudly instead of slipping through because the sanitizer's list does not name it.

**Rejected alternative:** an embedded mirror of the tracked configs (`internal/cmd/install_configs/`)
kept by a `make sync-configs` target and a drift test, as the no-clone fallback. It shipped one
variant's agent names into every binary built from the tree, including the OSS one, and stayed
current only by hand.

## Consequences

**Accepted costs:**
- An `af` built by hand (no `-X main.sourceRoot`) and run without `AF_SOURCE_ROOT` gives a new
  factory no `dispatch.json`, `startup.json`, `models.json` or `litellm.yaml` until `--init` is
  re-run with a clone; the documented flows (quickstart, `make install`) always resolve one.
- Between the first `--init` and `af install --agents`, a seeded `startup.json` can name formula
  agents the factory does not have yet; `af up` warns about them until `--agents` runs.
- A clone newer than the binary that adds a formula agent to `startup.json` or `dispatch.json` is
  refused until `af` is rebuilt from that clone, the same remedy as an unknown key.

**Earned properties:**
- A fresh factory starts from the configs of the agentfactory variant it was bootstrapped from,
  with no hand-maintained literal or copy to drift.
- A populated or legacy factory's `--init` writes nothing it already has and is not blocked by
  clone anomalies in files it will not write.
- Where every seeded file came from, and what was rewritten in it, is auditable in
  `.agentfactory/.config-seed.log`.

The status stays Proposed until a corpus file cites this ADR (see README "How ADRs are updated").

## Corpus links

- ADR-020 — `af install --init` bootstraps manager + supervisor only
- `.designs/654/design-doc.md` (cited as text) — seeding mechanism, source-granularity,
  anomaly-policy and sanitizer decision rows
- `internal/cmd/install.go` — `seedStarterConfigs`
- `internal/cmd/seed_configs.go` — `seedConfigNames`, `builtinRoleEntries`, `baseConfigContent`,
  `futureRoster`, `sanitizeSeed`, `validateSeed`
- `internal/cmd/formula.go` — `resolveAFSource`
- `internal/cmd/install_configs_seedworthy_test.go`, `internal/cmd/starter_config_source_pinning_test.go`
