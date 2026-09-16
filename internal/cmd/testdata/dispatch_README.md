# internal/cmd/testdata — dispatch fixtures

Fixtures for `TestDispatchDeny_FixtureReplay` (issue #673, Phase 4 — LIVE-PROBE). They exist so the
release ladder's platform assumptions are replayable in the DEFAULT suite, on a machine with no
`claude` CLI and no network, through the real decision code rather than through the
`subagentQuietEvidence` seam.

## Provenance classes — never mix these

| Class | Meaning | Where |
|-------|---------|-------|
| **captured** | Taken from a real Claude Code payload during Phase 4's Spike S and carried verbatim, byte for byte. Its value is that it records what upstream ACTUALLY emits, so it may not be regenerated from our own code. | `dispatch_stop_payload_2_1_258.json` |
| **captured-layout** | A captured on-disk SHAPE — names, ids, directory structure — with an authored timeline applied on top, because git carries no mtimes and the ladder reads nothing else. The layout is a platform fact; the ages and the scenarios built from them are ours, and the stamp says so rather than borrowing the credibility of the class above. | `dispatch_sidechain_timeline_2_1_258.json` |
| **in-code** | Hand-authored in the test for exact arithmetic and for the malformed-proposal cases. Written to `t.TempDir()`, never committed. | `dispatch_deny_fixture_replay_test.go` |

Each file carries its own `cli_version`, `provenance`, `captured_at` and `capture_method`, and the
version is in the filename, because a captured fixture whose upstream version is unrecorded cannot be
re-judged when the platform moves. There is no `-update` flag: these are re-captured by hand from a
real run, never regenerated.

## What "captured" claims, and what it does not

`captured` means the payloads are the run's own bytes, unedited. It does NOT mean the top-level key
set is the CLI's whole contract. Field presence is conditional on session settings: the same
`2.1.258` has been observed emitting a top-level `"effort":{"level":...}` object that this capture
does not carry. So the key set here is a FLOOR — every key present is one the host really sent, and a
key the host stops sending is a regression the replay would not notice on its own.

That asymmetry is enforced, not just written down. `assertProbeC7Fold` in the integration probe holds
a payload the host produced minutes earlier and re-judges this fixture against it
(`rejudgeCapturedFixture`): a key present here and absent live is a FAILURE, an extra key live is a
logged observation. It is the only thing in the tree that can falsify these files.

The `background_tasks[].description` values (`one`, `two`) are the spike stub's own prompts, not
anything the CLI invented.

## Manifest

| Fixture | Purpose |
|---------|---------|
| `dispatch_stop_payload_2_1_258.json` | Both verbatim `SubagentStop` stdin payloads from one spike run — one per child of the same session. Pins the wire shape `dispatchRetirePayload` decodes, pins `captureDispatchStopPayload`'s redaction against a real document, and records two facts the design had only reasoned about: `transcript_path` is the **parent** session transcript (the child's lives in the unmodelled `agent_transcript_path`), and `background_tasks[].status` reads `running` for **both** children at stop time — the observation that keeps E0 dark. |
| `dispatch_sidechain_timeline_2_1_258.json` | The sub-agent sidechain layout the same run wrote — `agent-<id>.jsonl` beside its `agent-<id>.meta.json` sidecar — expressed as `{name, age_seconds}` timelines because git cannot carry mtimes. Class `captured-layout`: only `active-two-children` reproduces the file set the run had, and even there the ages are authored. Each case names the E1 verdict it must produce, including the three "cannot tell" cases whose default must stay RETAIN. |

## Why ages, not mtimes

The E0–E3 evidence ladder is stat/glob only — no ladder evidence file is ever opened — so for those rungs
file *contents* are irrelevant and file *times* are everything. Git preserves neither, so the timeline is
data: the test creates each named file empty and applies `os.Chtimes(now - age_seconds)`. The one
exception, ranked ahead of the quiet compare, is the completion-record rung, which does a single
bounded, regular-file-gated content read of the parent
transcript; its fixtures ARE captured by content and live in `dispatch_completion_enqueue_*.json`.
