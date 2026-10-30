# Captured OpenObserve v1.1.2 backend responses

These files are real HTTP exchanges with OpenObserve **v1.1.2**, the release issue #743 moves the
telemetry backend to. They were captured read-only on 2026-10-27 during the C-14 copy proof:

1. the live data directory of this factory's v0.91.3 backend was copied at 01:31:50Z, with
   `db/metadata.sqlite` replaced by an online `sqlite3 .backup` snapshot;
2. v0.91.3 started on the copy, replayed its `wal/`, and was stopped with SIGTERM;
3. the sha256-verified v1.1.2 (`openobserve-v1.1.2-linux-arm64.tar.gz`, checked against the vendor's
   published `.sha256sum`) started on the same copy, migrated its metadata (`DB_SCHEMA_VERSION`
   45 → 97), and answered these requests at `http://127.0.0.1:15080/api/default`.

The live backend was never stopped. The copy and its scratch root were deleted afterwards.

They sit beside `../openobserve-v0.91.3/` rather than replacing it: the readers in
`internal/cmd/telemetry_usage_test.go` and `internal/cmd/telemetry_evidence_record_test.go` run over
both directories, so the parser has to keep reading the version installs run today and the version
they move to.

## Same request, same data, a different answer

Every request is the v0.91.3 one, re-sent with a new time window. The data underneath is the same
factory's own telemetry. So where the two directories differ, the difference is the backend version:

- `tokens_search_response_query_failed.json` is still HTTP 400 with `"code":20004`, which classifies
  as `query_failed`. The envelope changed, though. v0.91.3 said `Search field not found: Schema error:
  No field named af_overhead. Valid fields are …` and carried an `error_detail` key. v1.1.2 says
  `unknown field 'af_overhead'`, drops `error_detail`, and adds a `hint` key. A reader that keyed on
  the message text or on `error_detail` would break here; the usage verb reads the status code first.
- `metrics_promql_response_error.json` keeps the `{"status":"error","errorType":"bad_data",…}` shape;
  the `Execution error: ` message prefix is gone.
- `search_response_401.json` is byte-identical to v0.91.3's.
- `tokens_search_response_ok.json` has the same top-level keys as v0.91.3's.

## The shipped view's SQL still fails — read the other record before concluding why

`tokens_search_request.json` is the shipped `agent-model-step-tokens` view's SQL, byte for byte the
query `../openobserve-v0.91.3/tokens_search_request.json` sent. It fails on v1.1.2 for the same reason
it failed on v0.91.3: the logs stream carries no `af_overhead` / `input_tokens` / `output_tokens`
columns. That is a property of the ingested data, not of the backend version.

The view's own `af_assumptions` block (`internal/cmd/install_telemetry_views/agent-model-step-tokens.json`)
records the contradicting observation: during the review of PR #567 a pinned backend ran this query
verbatim and reproduced the token totals. `../openobserve-v0.91.3/README.md` explains why the two are
not symmetric. In short, #567 ingested the synthetic native fixture, so **#567 governs the
traces-side spellings and the join rules**, and the captures govern the native event shape. Nothing
here re-litigates that. This capture adds one fact: the gap survives the version move.

The logs-schema discrepancy recorded in the v0.91.3 README is still open. That README's schema call
listed six fields, and that day's 20004 response listed four, including `service_name`. v1.1.2's 20004
no longer enumerates valid fields at all, so this capture cannot settle it.

## DELTA series arrive as twins

`metrics_promql_response.json` holds 12 series for two runs. One run has eight, paired series that
differ only in `start_time`. Claude Code exports its counters with DELTA temporality, so each export
interval is its own series, and an instant query's lookback returns every interval it overlaps. The
v0.91.3 backend does the same: on the same copy, at the same instant, it answered with the same
twins. The committed v0.91.3 capture just happened to hold none.

That is why `start_time` is in the usage verb's label allowlist (`telemetryUsageMetricLabels`).
Without it, the twins become rows that `TestTelemetryUsage_MetricRowsCarryTheirDistinguishingLabels`
rejects as indistinguishable.

The query was sent with `time=1793064743.049` (2026-10-27T01:32:23Z), the instant the v0.91.3
baseline read used. The copy receives no new data, so a `time`-less query sent later returns an
empty vector once the lookback passes the copy's last sample.

## Contents

| File | Exchange |
|---|---|
| `tokens_search_request.json` | The shipped view's SQL as a real `POST /api/default/_search` body |
| `tokens_search_response_query_failed.json` | Its answer: HTTP 400, `{"code":20004,…,"hint":…}` — a query error |
| `tokens_search_request_ok.json` | A `_search` body that the logs schema does satisfy |
| `tokens_search_response_ok.json` | Its answer: HTTP 200, the success envelope |
| `metrics_promql_response.json` | `GET /api/default/prometheus/api/v1/query?query=claude_code_token_usage&time=…` → HTTP 200 vector |
| `metrics_promql_response_error.json` | A malformed PromQL query → HTTP 400 |
| `search_response_401.json` | `_search` with no credential → HTTP 401 |

## Request shape

Unchanged from `../openobserve-v0.91.3/README.md` ("Request shape"): `POST <endpoint>/_search` with
`{"query":{"sql":…,"start_time":<µs>,"end_time":<µs>,"from":0,"size":<n>}}`, and
`GET <endpoint>/prometheus/api/v1/query?query=<promql>`. Both carry `Authorization: <the
telemetry.auth value>`. The `_search` window here is the 60 days ending 2026-10-27T01:34:00Z.

The credential reached curl through a hard link to the live `.agentfactory/secrets/telemetry.auth`,
streamed into `curl -K -`. It never appeared in an argument list, a file, a log or a terminal.

## Scrubbing

The v0.91.3 procedure, with one change. `user_email`, `user_id`, `user_account_id`,
`user_account_uuid`, `organization_id` and every `trace_id` were replaced with the v0.91.3
placeholders. Each distinct `session_id` was replaced with its own placeholder
(`00000000-0000-0000-0000-000000000001`, `…002`). That keeps the evidence that the twins belong to
one session without keeping the session's identity. No `prompt_id`, `message_uuid` or `prompt`
appeared in these responses.

Checks run before these files entered the tree: zero matches for every original identity value,
zero matches for the live credential (`grep -c -F -f` against the hard link, counts only), and no
email address but the placeholder.

Re-capturing after another `OPENOBSERVE_VERSION` bump means re-running the same read-only calls
against a copy, never against the live backend, and re-running the scrub. Nothing here is
generated by a test.
