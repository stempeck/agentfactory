# `internal/statusline/testdata/transcript` — Gate K2-V findings and fixtures

Issue #600, Phase 2 ("truthful token spine"). These fixtures and the findings below are the
**K2-V gate**: the design required capturing a REAL Claude session transcript and empirically
verifying the usage-record shape, record **multiplicity**, and **sidechain** appearance BEFORE
any reader code was written (`design-doc.md:567-571`). The fixtures were committed first; the
reader was written against them, not the other way round (the provenance rule
`internal/telemetry/testdata/README.md` states for the same reason).

## Verdict: B1.1 (transcript accumulation) CONFIRMED — three design clauses amended

The transcript is the right source. It is append-only in-session, its `usage` records are
per-message API-reported spend (not context occupancy), and the deduplicated running sum is
monotonic across a real compaction boundary. **Fallback B1.2 (`current_usage` + `prompt_id`) was
NOT needed and was not taken.**

Empirical scope: 1,948 real transcripts, 1.12 GB, 138,436 usage records, 19 Claude Code versions
(2.1.202–2.1.223), 7 model families, plus a live append-only proof (3 snapshots of a growing file,
byte-identical SHA-256 over the first 400 KB each time) and a 31,484-render-tick policy simulation.

### (a) Usage-record shape — CONFIRMED

`usage` lives at **`.message.usage`**, and **only** on records where `.type == "assistant"`. There
is no top-level `.usage`.

Only four fields are present in **100 %** of records (16,736/16,736) across every version and model
observed, and they are the only ones this reader may depend on:

| field | note |
|---|---|
| `input_tokens` | non-cached input for THIS request |
| `output_tokens` | output for THIS request — **partial on in-flight records** |
| `cache_creation_input_tokens` | not part of the headline figure |
| `cache_read_input_tokens` | not part of the headline figure |

**Never sum `cache_creation.*` or `iterations[].*`.** Both are exact roll-ups of the flat scalars
(verified 461/461 and 1,359/1,359 respectively); adding them double-counts. The headline figure is
`input_tokens + output_tokens` only (`design-doc.md:420`).

### (b) Multiplicity — CONFIRMED, and it is the DOMINANT case

Claude Code writes **one JSONL record per content block** of an assistant message and stamps the
whole message's `usage` on every one of them. `(message.id, requestId)` pairs repeat in **99.6 %**
of transcripts (1,932 of 1,940), covering **59.1 %** of all usage records.

- Naive summation over-counts by **+122.2 %** (measured end-to-end over 249 files).
- `main_excerpt.jsonl` here: 30 usage records → **9** distinct `message.id` (**3.16×**).

**The dedup key is `message.id`, reduced with `max` per field.** This is the "K2-V-verified
equivalent" the IMPLREADME authorises in place of a bare `(message.id, requestId)` set-membership
skip. Reasons, all measured:

- **First-wins / skip-if-seen is WRONG.** 19,093 repeated pairs carry *different* numbers
  (99.96 % differing in `output_tokens` only): an in-flight record holds a partial count and the
  completed record the real one. First-wins drops **26,009,812 output tokens** in the sample. In
  **100 %** of conflicts the maximum `output_tokens` belongs to the record whose `stop_reason` is
  non-null. `subagent_excerpt.jsonl` here reproduces this exactly: max-wins yields **2941**,
  first-wins yields **31** — a 95× gap, which is what makes the pin discriminating rather than
  tautological.
- **`requestId` must never be REQUIRED.** It is absent on 1,736 records from non-Anthropic gateway
  models. It stays in the key (normalised `null → ""`) because it matches the design wording and
  costs nothing, but `message.id` does all the work: it is present on 100 % of usage records and
  never spans two `requestId`s (0 violations in 1,940 files).
- **Never key on `.uuid`** — it is unique per content block and dedups nothing.

### (c) Sidechain — the design's assumption is FALSIFIED as written

The design assumed sub-agent turns appear inline as records with `isSidechain: true`. **They do
not.** Across all **597** main transcripts spanning Claude Code 2.1.202–2.1.223, **zero** main
transcripts contain a usage-bearing `isSidechain: true` record. Sub-agent spend lives in sibling
files:

```
<projects>/<slug>/<sessionId>.jsonl                       <- transcript_path (main); isSidechain always false
<projects>/<slug>/<sessionId>/subagents/agent-<id>.jsonl  <- isSidechain: true; NOT referenced by transcript_path
```

**Magnitude of the omission**: 37.2 % of all corpus tokens live in sub-agent files; reading
`transcript_path` alone captures **58.3 %** of a sub-agent-using session's spend.

**Decision recorded (consensus DEC-3): sub-agent files are OUT OF SCOPE for Phase 2.** Grounds, each
from a document that outranks an implementation-time preference:

1. `data.md` B2.3 already **REJECTED** a sidecar cursor file ("doubles files, doubles prune/rollover
   logic, doubles the atomicity story"), and per-file offsets cannot live in the snapshot: up to
   **122** sub-agent files per session ≈ 3–4 KB of cursor state would breach the 4 KB
   `maxSnapshotBytes` cap and make `readSnapshot` return `errSnapshotTooLarge`, silently zeroing the
   session.
2. Up to 122 `stat`s and 25.5 MB of reads per session would land on the render hot path, which is
   measured by the 500 ms budget pin.
3. The IMPLREADME's K2 spec and K3 schema describe a **single-file** reader with a **single**
   `transcript_offset`.

**What IS implemented**: the reader applies **no sidechain filter** — an `isSidechain: true` record
is counted wherever it is encountered. The C-R1 policy ("sub-agent tokens are session-caused
spend") is therefore honoured literally in code and becomes effective with no code change if
upstream inlines sidechain records or a later phase widens the glob.
`TestTokenCounter_DuplicateRecordsCountOnce/SidechainRecordsCount` pins this by proving that
removing the sidechain records LOWERS the total.

> **Carried forward to Phase 3 (blocker):** the session token figure is *main-transcript* spend on
> current Claude Code. Phase 3 must not label it as a complete session total.

### (d) Offset advance — "past the last complete line" is INSUFFICIENT

A single logical message is a **contiguous run of 1–N records** spanning **7.0 s of wall clock on
average** (74 % ≥ 2 s, max 437 s) against a 10 s throttle window, so a read lands *inside* a run
most of the time and the next read re-counts that message. Simulated over 249 transcripts and
31,484 render ticks:

| offset policy | error vs ground truth | files wrong |
|---|---|---|
| no dedup | +122.2 % | 249/249 |
| last-complete-line + per-window dedup (design as written) | **+28.9 %** | 208/249 |
| `stop_reason`-based holdback | +20.3 % | 41/249 |
| **hold back the trailing `message.id` run** | **0.0000 %** | **0/249** |

The reader therefore advances `transcript_offset` only to the byte at which the trailing run of
records sharing the last-seen `message.id` began, and excludes that key from the window's sum.
Pinned by `TestTokenCounter_StraddledMessageCountsOnce`. Cost: the in-flight message's tokens lag
by one throttle tick — a temporary undercount, never the wrong direction (`scale.md` S3.1).

### (e) Other verified facts the reader depends on

- **Compaction appends, never rewrites.** It adds a `{"type":"system","subtype":"compact_boundary"}`
  record and a `{"type":"user","isCompactSummary":true}` record mid-file (real example: boundary at
  line 1079 of 1965, all 1,078 preceding lines intact). Neither carries `usage`. Across that
  boundary `cache_read_input_tokens` collapses (465,032 → 21,164) — that collapse **is** finding F1
  seen from the other side — while the deduplicated running sum shows **0 decreases** over 404
  messages.
- **A single line can be 2.4 MB.** A per-line buffer must skip-and-advance, never re-attempt, or the
  byte offset deadlocks permanently and the counter freezes.
- **`transcript_path` is absolute snake_case**, and can be missing, deleted mid-session, or point at
  the wrong directory (two upstream bugs on record). Fail open, keep the counter.

## Manifest

| Fixture | Provenance | Purpose |
|---|---|---|
| `recorded-real/main_excerpt.jsonl` | **recorded-real** — first 80 lines of a real main transcript (agent `manager`, v2.1.x), redacted | Multiplicity on real data: 30 usage records → 9 distinct `message.id` (3.16×). Interleaved non-`assistant` records prove foreign types are skipped. Ground truth: naive `31761`, deduped `10040`, held-back-window `7357`. |
| `recorded-real/subagent_excerpt.jsonl` | **recorded-real** — first 40 lines of a real `subagents/agent-*.jsonl`, redacted | The **partial-vs-final** shape and the **sidechain** case: 24 usage records (all `isSidechain: true`) → 7 distinct ids, 6 groups where an in-flight record's `output_tokens` is lower than the completed one's. Ground truth held-back window: **max-wins `2941`, first-wins `31`**. This is the fixture that kills a first-wins implementation. |

**Redaction rule applied to both** (`recorded-real` provenance class — captured, then scrubbed):
every `content` block is reduced to its `{"type": …}`, all `text`/`thinking`/tool payloads, `cwd`
and `gitBranch` are dropped, and `sessionId` is replaced with `sess-K2V-redacted`. Message ids,
request ids, timestamps, `isSidechain`, `stop_reason` and the full `usage` object are preserved
verbatim — those are the structural facts the reader depends on, and none of them is conversation
content. Verified: no non-empty `text`/`thinking`/`description`/`content`/`command`/`prompt` string
survives in either file.

## Fixtures built in-code (not on disk)

Exact-integer arithmetic cases (offset reset, compaction append, over-long line, missing
`requestId`, straddled message, mixed-era snapshots) are written to `t.TempDir()` by
`tokens_test.go` and `daily_test.go`, per the `t.TempDir()` convention documented in
`../README.md`. Only the two **recorded-real** captures are committed, because only they carry
provenance that cannot be reconstructed from code.
