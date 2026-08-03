# Recorded-real logs-plane schema (fable-implement Step 6, #329 Phase 6b)

`logs_schema_response.json` is a real HTTP response — `GET
/api/default/streams/default/schema?type=logs` — captured read-only against this
factory's own pinned OpenObserve backend, live, during this implementation run
(2026-07-28, `internal/cmd`'s working tree at the time of capture: `af/fable-implement`
worktree `wt-4b309d`; the backend was the *shared factory's* instance at
`127.0.0.1:5080`, the same host `.agentfactory/secrets/telemetry.auth` authenticates
against).

This is the P6b deliverable `.designs/329/implementation-plan/IMPLREADME_PHASE6b.md`
specified and that `rootcause_analysis.md`'s Step 6 requires: a real capture, not a
synthetic fixture, replacing `internal/telemetry/testdata/synthetic/`'s hand-authored
"design hypothesis" as the basis for `tokensViewLogsPlaneColumns()`'s pre-flight check.

## What it shows

74 fields on the logs stream (matching the peer-review's independently-cited count in
`.analysis/af-47a938dc/rootcause_analysis.md`'s R7/G-1 section exactly). The six
logs-plane columns the shipped `agent-model-step-tokens` view's `billable_requests` CTE
references:

| Column | Present in this capture? |
|---|---|
| `af_formula_instance` | yes |
| `af_agent` | yes |
| `_timestamp` | yes |
| `input_tokens` | **no** |
| `output_tokens` | **no** |
| `af_overhead` | **no** |

This independently reconfirms Root Cause B's evidenced arm (E-4): the three token
columns the view's `billable_requests` CTE needs do not exist on this stream under any
name this capture carries. The three that DO exist (`af_formula_instance`, `af_agent`,
`_timestamp`) are join/filter keys, not token data — their presence does not help the
query.

## An unplanned, live corroboration of Root Cause A

Between this capture and a follow-up query moments later (an `event_name` census
attempted for a P6b field-shape check, not committed here since it never completed),
the backend stopped answering entirely — `curl http://127.0.0.1:5080/healthz` began
returning connection-refused, and the `telemetry` tmux session that had been listed
moments earlier (alongside `af-dispatch`, `af-manager`, `af-watchdog`) was gone from
`tmux ls`. Repeated checks over the following ~15 seconds showed no self-recovery.

This was not induced by anything this implementation run did to the backend or its
tmux session — only read-only HTTP GETs were issued against it. It is recorded here
because it is a live, unprompted instance of exactly the failure Root Cause A
describes ("any routine event eventually kills an unsupervised process, and the design
then guarantees the outage persists indefinitely") occurring on the very backend this
capture was taken from, during the implementation of the fix for it. It was not used
to validate `ensureTelemetryBackend` against the *shared* factory (that backend sits
outside this worktree's boundary — WORKTREE ISOLATION — so this implementation run's
code changes do not apply there without a separate deploy), but it is independent,
first-hand, live evidence for Root Cause A's structural claim, gathered incidentally
rather than staged.

## Known-absent set

See `tokensKnownAbsentColumns()` in `internal/cmd/telemetry_where_test.go` — the three
columns confirmed absent above, each with an inline citation to this capture.
