# ADR-025: Plugin repositories are acquired inert outside the consumed store; plugins.json is a load-bearing provenance manifest whose readers fail closed

**Status:** Proposed (introduced by PR #539 for issue #538; the operator ratifies at merge)
**Date:** 2026-09-25 (commit SHA to be recorded at commit; the pre-decision state is anchored to `0c1f2220`)
**Amended:** 2026-09-28 in place for integrations (§1, §2, §3 and Consequences). Permitting `[install]`/`[check]`/`[service]` execution awaits the operator's ruling R2 with this ADR's ratification.

## Context

Before issue #538 every formula in `.agentfactory/store/formulas/` was one of two
kinds: shipped (a counterpart in the AF source tree's
`internal/cmd/install_formulas/`) or operator-authored. `af install --agents`
installs whatever sits in that store (ADR-020: installation is explicit and
customer-owned), and in a source-repo factory `agent-gen-all.sh`'s orphan passes
delete any store formula and role template without a source-tree counterpart.

Third-party agent repositories add a third kind, and with it a trust boundary
the factory did not have:

- Content arriving by `git clone` must not become installable merely by being on
  disk. If a clone landed in the flat store, the next factory-wide
  `af install --agents` would install it without consent.
- Once installed, a plugin formula has no source-tree counterpart, so the orphan
  passes would delete it and its generated role template on the next redeploy.
- At launch, `worktree.SetupAgent` renders a manager or supervisor identity for
  a role whose template is not embedded in the binary. A plugin agent whose
  template never made it into the binary would run under a substituted identity.
- The origin of a plugin (remote URL, commit) is the only audit trail for
  unsigned third-party content, and remote URLs can carry credentials.

The design is recorded under `.designs/538/`. The PR #539 review found that the
first implementation read a present-but-unparseable manifest as "zero plugins"
in both Go and shell (so a merge conflict in `plugins.json` silently removed the
protection), recorded the enclosing factory repo's origin for a plugin directory
that was not its own git repository, recorded credential-bearing remote URLs
verbatim, and named an arbitrary owner when several plugins recorded one stem.

## Decision

1. **Acquisition is inert and outside the consumed store.** Plugins are cloned
   into `.agentfactory/store/plugins/<name>/` (`config.PluginsDir`), or copied
   there from a reference integration embedded in `af` by the operator-only
   `af plugin acquire <name>`. Nothing reads that directory except the
   `af plugin` verbs (`internal/cmd/plugin.go`). `af plugin install <name>` is
   the only path from there into a consumed store, and running it is the
   consent: there is no prompt (ADR-014). A plugin's formulas go to
   `.agentfactory/store/formulas/`. An integration (a plugin whose top level
   holds an `af-integration.toml` manifest) goes to the consumed tree
   `store/integrations/<name>/<sha>/` (`config.IntegrationsDir`): a read-only
   snapshot of the manifest, its declared `[claude]` dirs and its run scripts,
   where `<sha>` is the canonical content hash recorded at consent. A re-install
   with changed content adds a sibling snapshot and never overwrites one; the
   snapshots are untracked (`.agentfactory/store/.gitignore`). Install is
   operator-only and validates the whole batch, then runs every refusal, before
   its first write.

2. **`.agentfactory/plugins.json` is a load-bearing provenance manifest, not a
   cache.** For each installed plugin it records the source, commit, install time
   and the sha256 of every staged formula (`internal/config/plugins.go`). The
   orphan passes in `agent-gen-all.sh` keep a formula or template it records; the
   launch guard (`refusePluginAgentWithoutTemplate`, `internal/cmd/sling.go`) and
   `af plugin verify` read ownership from it. It is git-tracked through the root
   `.gitignore` allowlist. It carries `"version": 2`: version 2 adds an
   installed integration's `integration` block (manifest and content hashes, the
   per-file hash map, scope, the redacted upstream, env key names but never
   values, service and probe, external writes and their hashes, the snapshot
   dir). The loader accepts versions 1 and 2, a versionless file reads as
   version 1, and a newer version is refused, so an older `af` refuses a
   version 2 file instead of rewriting it without the block. Its only writers
   are `af plugin install` and `af plugin remove`.

3. **Every reader fails closed on a present manifest it cannot read.** Absent
   means zero plugins and every plugin behavior stays dormant. A present file
   that `config.LoadPluginsConfig` cannot decode is corrupt, never empty: not a
   JSON object, `plugins` not an object, a plugin entry, its `formulas` or its
   `integration` not an object (or null), a string field of another type, or a
   `version` that is not an integer from 1 to the current schema version (2).
   All readers apply that same shape rule:
   - `config.LoadPluginsConfig` returns an error naming the file.
   - The launch guard refuses an agent whose template is not embedded while
     ownership cannot be read. Embedded agents (manager, supervisor, shipped
     specialists) still launch, because substitution only happens for a
     non-embedded role.
   - `af plugin verify` returns the error (as `{"state":"error"}` under
     `--json`) and exits non-zero, never a clean report.
   - `agent-gen-all.sh` checks the shape once with a `jq -e` filter that
     mirrors that decode, captures its exit status explicitly, and on failure
     preserves every orphan candidate for that run with one WARNING naming the
     file. `TestPluginManifestShapeParity` runs one shape table through both
     readers and requires them to agree row for row. The one gap it cannot
     cover: jq normalizes number literals, so it cannot tell `1.0` or `1e0` from
     `1`, which Go's integer decode rejects. Without jq it cannot check the shape; it greps
     for either key form the Go reader accepts (`"<stem>"` or
     `"<stem>.formula.toml"`) and preserves on a match.

4. **Provenance is recorded only when it is trustworthy, and never carries a
   credential.** Source and commit are recorded only when the plugin directory
   is the top level of its own git repository (compared after resolving
   symlinks, with `GIT_DIR`, `GIT_WORK_TREE` and `GIT_COMMON_DIR` removed from
   the git environment); otherwise both are empty. User info, query and fragment
   are stripped from the remote URL (both `scheme://` and scp-like
   `user@host:path` forms) at one point after the git read, before the value is
   recorded or printed.

5. **Ownership has one deterministic answer.** A stem belongs to at most one
   owner. Installing refuses a formula whose stem another plugin records (naming
   the owners in sorted order), a hand-authored agent, a shipped formula, or a
   different store formula. A byte-identical store copy is accepted only when
   this plugin recorded it, or when nobody recorded it and `agents.json` does not
   register it (the resume case after an interrupted install).
   `PluginsConfig.OwnsAgent` iterates plugin names in sorted order, so every
   message that names an owner is stable.

## Consequences

- A corrupt `plugins.json` blocks launching non-embedded agents and makes
  `af plugin verify` fail until it is repaired, instead of silently dropping
  protection. Redeploys keep running but preserve every orphan candidate, so
  genuinely orphaned files survive until the manifest is fixed.
- A plugin cloned somewhere that is not its own repository has empty provenance
  in the manifest. That is honest, but it leaves no audit trail for that plugin.
- A manifest written before credential stripping existed is not rewritten. An
  operator whose manifest recorded a credential must rotate it.
- The content a plugin ships is still unauthenticated: there is no signing. The
  manifest makes each transition explicit, hashed and auditable, not trusted.
  Skills an integration ships inside its declared `[claude]` dirs are part of
  the snapshot's content hash, which `af plugin verify` re-checks, naming every
  changed file. A formula plugin still installs no skills, and skills in the
  factory's `.claude/skills/` stay outside the hash.
- Installing an integration permits third-party code to execute on the host
  as the agent user: `[install]` at `af plugin install` (stdin closed, bounded
  by its timeout), `[check]` at `af plugin check`, and `[service]`, which bare
  `af up` and the watchdog start when its session is absent and never kill.
  There is no sandbox. Consent covers the hashed bytes of the snapshot, not
  what those programs fetch or write at runtime.
- `af plugin remove <name>` removes an integration: it deletes the record
  entry and the integration's snapshots, prints every `external_writes` path
  and deletes none of them (ADR-017, Amendment 2026-09-28). Formula plugins
  still have no removal or update-diff (issue #525).
- When several plugins record a stem, both readers name the sorted-first
  plugin as the owner.
- An operator-authored store formula that is byte-identical to the plugin's
  formula and not yet registered in `agents.json` cannot be told apart from a
  copy staged by an interrupted install, so the plugin adopts it and records it
  as its own. Once removal exists (issue #525), removing that plugin would take
  the operator's file with it. Registering the agent first
  (`af formula agent-gen <name>`) makes the install refuse the collision
  instead.

## Corpus links

- `internal/config/plugins.go` (`LoadPluginsConfig`, `SavePluginsConfig`, `OwnsAgent`)
- `internal/config/integration.go`, `integration_hash.go` (`LoadIntegrationManifest`, `IntegrationContentHash`)
- `internal/cmd/plugin.go` (`runPluginInstall`, `classifyCollision`, `runGitProvenance`, `verifyPlugins`)
- `internal/cmd/plugin_acquire.go`, `plugin_check.go`, `plugin_remove.go`, `integration_services.go`
- `internal/cmd/sling.go` (`refusePluginAgentWithoutTemplate`)
- `agent-gen-all.sh` (`plugin_owner_of_stem`)
- `USING_PLUGINS.md`
- Related: ADR-014 (no interactive prompting), ADR-017 (no customer data deletion), ADR-020 (explicit agent installation)
