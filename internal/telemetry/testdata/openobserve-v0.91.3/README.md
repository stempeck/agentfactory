# Captured OpenObserve v0.91.3 backend responses

These files are real HTTP exchanges with the pinned telemetry backend
(`OPENOBSERVE_VERSION="v0.91.3"`, `quickstart.sh:55`), captured read-only on 2026-07-27 against a
development host's live instance at `http://127.0.0.1:5080/api/default`.

They exist because CI provisions no backend (`six_sigma_gaps.md:18-42`), so every test that asserts
on the backend's wire contract has to assert against something committed. The sibling
`telemetry-dto/` directory holds the same kind of pin for `af`'s own JSON output; this directory
holds the *foreign* half — what OpenObserve actually answers.

## Why captures rather than hand-authored fixtures

A hand-authored fixture encodes what the author believed the API does. Three of the shapes below
were **not** what the design assumed:

- a 404 for a dropped org segment returns an **empty body**, and a 404 for a wrong org returns
  **plain text** (`Organization not found`) — neither is JSON, so a reader that decodes before it
  classifies would mis-report both;
- the credential-rejected envelope (`{"code":401,…}`) is shaped differently from the query-error
  envelope (`{"code":20004,"message":…,"error_detail":…,"trace_id":…}`), which is different again
  from the PromQL error envelope (`{"status":"error","errorType":…,"error":…}`);
- the shipped `agent-model-step-tokens` view's SQL **fails** on this backend with code 20004,
  because the logs stream carries no `af_overhead` / `input_tokens` / `output_tokens` columns.

## The other half of that last point — read it before concluding the query is broken

The same repository holds a **contradicting first-hand observation**: the view's own
`af_assumptions` block (`internal/cmd/install_telemetry_views/agent-model-step-tokens.json`) records
that during the review of PR #567 a pinned v0.91.3 was stood up, both planes were ingested, and this
query ran verbatim, reproducing the token totals column for column.

The two are not symmetric, and `install_telemetry_views/README.md` says why in the same paragraph as
the verification: what #567 ingested on the **native** side was the synthetic fixture
(`../synthetic/api_request_events.json`), not recorded Claude Code output. So **#567 governs the
traces-side spellings and the join rules**, and **this capture governs the native event shape**.

Nothing here says the SQL is simply wrong, and this capture is not on its own grounds to re-author a
contract-tested view. What it establishes is narrower and worth stating exactly:

- the native plane **did** reach this host — 2,192 records for one agent sit in `logs.default`
  (`tokens_search_request_ok.json` / `tokens_search_response_ok.json`), and `af` writes no logs at
  all (`probe.go:19-21`: traces are the af side's own plane), so those records are Claude Code's;
- the token-bearing **columns** the query reads are the part that is absent;
- the metrics plane carries `claude_code_token_usage` for the same runs
  (`metrics_promql_response.json`) — but its label set contains **no step attribute of any kind**,
  so it cannot answer a per-step question. Re-pointing the view there would quietly turn a per-step
  table into per-run totals.

**UNRESOLVED:** why those columns are absent for Claude Code 2.1.220 on this host. Until a
recorded-real native capture defines them, the per-step token table is unverified against real
Claude Code output. `internal/cmd/telemetry_usage.go:80-81` already says as much in its own words.

## Contents

| File | Exchange |
|---|---|
| `tokens_search_request.json` | The shipped view's SQL (`install_telemetry_views/agent-model-step-tokens.json`) as a real `POST /api/default/_search` body |
| `tokens_search_response_query_failed.json` | Its answer: HTTP 400, `{"code":20004,…}` — a query error, not a transport or credential failure |
| `tokens_search_request_ok.json` | A `_search` body that the live schema does satisfy |
| `tokens_search_response_ok.json` | Its answer: HTTP 200, the success envelope (`took`, `hits`, `total`, `from`, `size`, `trace_id`, …) |
| `metrics_promql_response.json` | `GET /api/default/prometheus/api/v1/query?query=claude_code_token_usage` → HTTP 200 vector, af-labelled |
| `metrics_promql_response_error.json` | A malformed PromQL query → HTTP 400 |
| `search_response_401.json` | `_search` with no credential → HTTP 401 |

## Request shape (verified, not assumed)

```
POST <endpoint>/_search                     GET <endpoint>/prometheus/api/v1/query?query=<promql>
Authorization: <the telemetry.auth value>   Authorization: <the telemetry.auth value>
Content-Type: application/json

{"query":{"sql":"…",
          "start_time":<microseconds>,
          "end_time":<microseconds>,
          "from":0,"size":<n>}}
```

`start_time`/`end_time` are **microseconds**, matching the shipped view's note that `_timestamp` is
microseconds while `start_time`/`end_time` on spans are nanoseconds. `<endpoint>` already carries
the `/api/default` org segment and is joined onto, never rebuilt (`probe.go:97-99`).

## Ingest spellings, confirmed against the live schemas

`GET /api/default/streams/default/schema?type=traces` carries `service_af_agent`,
`service_af_formula_instance`, `service_af_formula`, `service_af_worktree_id`, `af_step_id`,
`af_step_seq`, `af_model`, `start_time`, `end_time`.

`GET /api/default/streams/default/schema?type=logs` carries `af_agent`, `af_formula_instance`,
`af_factory_id`, `af_model_profile`, `af_worktree_id`, `_timestamp` — **no `service_` prefix**.

Two records of that one schema disagree, and the discrepancy is left visible rather than reconciled
by picking a favourite: the schema call above enumerates **six** fields, while the 20004 error
captured the same day against the same backend enumerates **four** and includes `service_name`,
which this list omits. Both are first-hand. A reader deciding what the logs stream actually holds
should treat the intersection as certain and the difference as open.

That is the dual-spelling rule the WHERE builder implements, verified first-hand rather than
inherited from the view's assumptions block.

## Scrubbing

Live telemetry carries operator identity. Before these files entered the tree, every
`user_email`, `user_id`, `user_account_id`, `user_account_uuid`, `organization_id`, `session_id`,
`prompt_id`, `message_uuid`, `prompt` and `trace_id` was replaced with a placeholder. No
`Authorization` value and no secret path appears in any file — checked by grepping the fixtures
against the live credential itself.

Re-capturing after an `OPENOBSERVE_VERSION` bump means re-running the same read-only calls and
re-running the scrub. Nothing here is generated by a test.
