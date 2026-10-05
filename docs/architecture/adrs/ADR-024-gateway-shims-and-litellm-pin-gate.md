# ADR-024: Gateway protocol shims are af-owned LiteLLM callbacks; the LiteLLM pin moves only on a session-shaped probe

**Status:** Proposed (the embodying artifacts are uncommitted on branch `af/rapid-soldesign-plan-d25689`; the operator ratifies at commit)
**Date:** 2026-09-22 (commit SHA to be recorded at commit; the pre-decision state is anchored to `4eb42139`)

## Context

The ChatGPT-subscription route puts LiteLLM between Claude Code and the Codex
backend: every seeded lane is `chatgpt/responses/<id>` (`quickstart.sh:1260-1281`
at `4eb42139`) and the gateway runs `litellm[proxy]==1.93.0` (`quickstart.sh:55`).
Two behaviours of that route live outside af's code:

- The Codex backend rejects a system-role input item outright. LiteLLM folds a
  *string* system prompt into Responses `instructions` but forwards a
  *block-array* system prompt as a system-role item
  (`litellm/completion_extras/litellm_responses_transformation/transformation.py:216-231`
  in the 1.93.0 wheel, reached from the Anthropic adapter at
  `litellm/llms/anthropic/experimental_pass_through/adapters/transformation.py:856-888`).
  Claude Code sends a block array on every turn, so every session turn was
  refused. Upstream declined to change it (BerriAI/litellm#21420 closed as not
  planned; PR #22967 closed stale).
- The backend's `response.completed` event carries an empty `output`, so
  LiteLLM's non-streaming bridge fails where its streaming path works
  (BerriAI/litellm#37039, open). Sessions stream; only af's probe did not.

Before this decision the probe sent a bare, non-streamed user message and
reported the status line alone (`internal/cmd/config_models.go:925-946` and
`:1040` at `4eb42139`), so bootstrap certified a gateway no session could use,
and printed `500 Internal Server Error` for a defect whose message was the
searchable key.

The counterfactuals on the table:

- **Move the pin to 1.88.1**, the last release with a working non-streaming
  bridge (#37039). Measured against a live subscription on 2026-09-22: the
  system-role refusal is identical there (the report in #21420 predates
  1.88.1), and all seven provider, bridge and adapter files differ from 1.93.0,
  which would un-anchor the credential and refresh analysis in
  `.designs/686/codebase-snapshot.md:4446`. The measurement has no in-repo
  anchor and cannot run in CI (ADR-018).
- **Patch the installed wheel** after install, behind a checksum. It proved
  the mechanism, but it edits site-packages on every install and tracks the
  wheel's internals.
- **An af-owned wrapper yaml that `include:`s the operator's yaml** and adds
  `callbacks`. `_process_includes` (`litellm/proxy/proxy_server.py:3727-3760`,
  1.93.0) overwrites dict keys, so the operator's `litellm_settings` would
  erase the wrapper's.
- **Refuse subscription mode until upstream fixes it.** Ships nothing.

## Decision

Protocol defects between Claude Code and the LiteLLM route are absorbed by
af-owned adapters at LiteLLM's documented extension points, never by editing
the installed wheel and never by moving the pin to chase upstream: the
subscription seed names `litellm_settings.callbacks:
af_codex_compat.proxy_handler_instance` (`quickstart.sh:1389`),
`_write_codex_compat_module` (`quickstart.sh:994`) writes that module beside
the yaml on every bootstrap, and `_ensure_codex_compat_wiring`
(`quickstart.sh:1062`) inserts the one line into a yaml seeded before the hook
existed. The probes that decide a pin move or a shim's retirement send the
shape a session sends, streamed with a block-array system prompt, and carry the
gateway's own error text (`internal/cmd/config_models.go:925-1060`).

## Consequences

**Accepted costs:**
- An af module executes inside the gateway process, and a `callbacks:` line
  sits in an operator-owned yaml; bootstrap edits that file exactly once, with
  a save-aside (`quickstart.sh:1071-1078`), and refuses an operator-managed
  `callbacks` entry rather than merging into it.
- A changed hook body stops the running gateway so the reconcile relaunches it
  (`quickstart.sh:1049-1052`): the launch identity hashes only the yaml.
- The flattening drops system-prompt cache markers. Nothing is lost on this
  route: prompt caching does not transfer through it (USING_LITELLM.md,
  subscription gotchas).
- Every probe is a real streamed inference, so `--live` spends one turn of
  plan quota per class.
- Non-streaming clients of the gateway still meet #37039. af is no longer one.

**Earned properties:**
- Claude Code completes turns, tool calls included, on the pinned 1.93.0 with
  no LiteLLM file modified; the shim is retired by deleting one yaml line.
- A pin move is a measurement, not a guess: the candidate passes the
  session-shaped probe and the AC-4 runbook, or the pin does not move.
- A failing bootstrap names the upstream defect in the gateway's own words
  (`TestFirstProbeSurfacesTheGatewayErrorText`,
  `internal/cmd/config_models_first_probe_test.go:129`), and a probe pass means
  the client's shape works (`TestFirstProbeSendsASessionShapedTurn`, `:155`).
- The migration is idempotent and never destroys an operator file
  (`internal/cmd/quickstart_codex_compat_test.go:81`), and the hook's behaviour
  is pinned under python3 without LiteLLM installed (`:154`).

## Corpus links

- Pin: `quickstart.sh:55`. Shim: `quickstart.sh:994`, `quickstart.sh:1062`,
  `quickstart.sh:1389`, `quickstart.sh:1401-1402`.
- Probe: `internal/cmd/config_models.go:925` (`smokeSystemPrompt`), `:937`
  (`doLiveSmokeRequest`), `:994` (`checkStreamedMessage`), `:1038`
  (`gatewayErrorSuffix`), `:1097` (`firstProbe`); transport seam `:198`
  (`modelsMessagesDo`, ADR-009).
- Operator runbook: USING_LITELLM.md, "Subscription mode" and its gotchas.
- Superseded lead: `.designs/686/data.md:142` told the seed to use a
  LiteLLM-registry id; the backend rejects `gpt-5.3-codex` and serves the
  seeded `gpt-5.6-sol` and `gpt-5.6-luna`.
- Related ADRs: [ADR-009](ADR-009-package-var-seams.md) (the probe's transport
  seam), [ADR-012](ADR-012-python-preflight.md) (runtime-dependency precedent),
  [ADR-017](ADR-017-no-customer-repo-mutations.md) (save-aside, never delete),
  [ADR-018](ADR-018-tests-never-disturb-running-factory.md) (why the 1.88.1
  measurement is not a test).
- Upstream: BerriAI/litellm #21420, PR #22967, #37039, #25429.
