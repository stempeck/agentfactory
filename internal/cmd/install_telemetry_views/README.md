# Shipped telemetry views

Six curated views plus an editable price list. `quickstart.sh` (`setup_telemetry`) seeds them; this
directory is the one authored copy, mirroring the `install_hooks/` and `install_formulas/` idiom.

Each view carries an `af_join` block stating how it treats overhead-marked events and which window
rules it encodes. That block is not documentation: `internal/telemetry/views_contract_test.go`
checks it against the constants and the attribution function in `internal/telemetry/window.go`, and
runs the committed fixture through both to prove the per-step view and the overhead view partition
the events between them. The acceptance criteria for this phase are greps, and a grep cannot tell
`af_overhead IS NULL` from `af_overhead IS NOT NULL` - the direction is the whole rule, so it is
tested rather than asserted.

## Operator decision O-4 - how views reach the backend

**Chosen: hybrid.** The files are copied to `.agentfactory/telemetry/views/` before the backend is
started, and a best-effort dashboard push runs afterwards if it came up.

Recorded rather than self-adjudicated, because neither half is sufficient alone:

- **File-only does not work.** OpenObserve v0.91.3 has no file-based dashboard provisioning. No
  `ZO_*` setting points at a dashboard directory, nothing in its dashboard service reads from disk,
  and its own `import` subcommand loads stream data rather than dashboards. Dashboards exist only
  as objects created over the API; the UI's Import button is a thin client over that same call. A
  file-only seed would ship JSON that nothing ever consumes.
- **Push-only loses the artifact.** `setup_telemetry` returns early - by design, so a failure never
  costs the operator a factory - on an unsupported architecture, a failed download, a failed
  checksum, an occupied port, a failed launch, and a readiness timeout. On every one of those paths
  a push-only seed would leave nothing behind at all.

So the copy runs first and unconditionally, and the push is layered on top. The push is idempotent:
the dashboards API has no upsert, so an existing dashboard of the same title is left alone rather
than duplicated on each run.

## Why one file is embedded and the rest are not

This directory long carried the opposite note — that nothing in the `af` binary consumed these
views, so an embed would be dead weight. That stopped being true when `af telemetry usage` landed:
it executes the per-step tokens query against the backend, and it has to send the query that was
authored here rather than a copy of it.

So `agent-model-step-tokens.json` alone is embedded, by `internal/cmd/telemetry_where.go`. The
other views keep the original arrangement: `quickstart.sh` provisions the backend and reads them
from the source tree it is already running from, and an embed with no reader really would be dead
weight.

Two alternatives were rejected for the embedded one, and the reasons matter more than the choice.
Transcribing the SQL into a Go string produces exactly the divergent duplicate the contract tests
below exist to catch — and they could not catch it, because they read this JSON rather than what
the command sends. Reading the seeded copy from `.agentfactory/telemetry/views/` at runtime is
worse: `quickstart.sh` treats that directory as operator-editable, so it would quietly turn an
operator-editable file into the SQL a credentialed client executes.

## What is not verified

The join and the column spellings have now been executed against a running backend — the review
of PR #567 stood up the pinned v0.91.3, ingested both planes, and ran the shipped query verbatim,
reproducing the token totals derived by hand from the acceptance criteria, column spelling for
column spelling. What that run did not cover is stated rather than implied: of the six
dashboards carrying assumptions, five were not executed, the pricing arithmetic behind derived dollars was not audited, and
the native event shape remains the synthetic fixture's hypothesis until a real capture replaces
it. CI still has no backend to re-verify any of it — every job runs on a self-hosted runner with
no container support — so these files can drift from a real backend without a test noticing. Each
view lists its own assumptions in `af_assumptions`.

Two delivery notes an operator will otherwise meet by surprise. A dashboard whose title is already
published is skipped on a re-run, so changing a query here does not update a factory that has
already imported it; re-import the file by hand. And `af telemetry status` contacts every address
this factory sends to, which is the fastest way to tell an empty dashboard apart from an
unreachable one.
