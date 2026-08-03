# `internal/telemetry/testdata` — OTLP conformance fixtures

This directory is the phase's answer to a specific prior failure: a telemetry wire format
that was never validated against the OpenTelemetry specification, only against one tolerant
receiver. Passing against one tolerant receiver demonstrates that receiver's tolerance, not
conformance. Everything here exists so the encoder is proven against the spec instead, with
**no backend, no service and no container** involved — the suite reads these files from disk
and opens no socket.

The fixtures were committed **before** `otlp.go` was written. That ordering is a binding
design rule, not a style preference: an encoder written first, with fixtures generated from
its own output afterwards, reproduces the original defect exactly while turning every
acceptance check green.

## Provenance classes — never mix these

| Directory | Class | What it means |
|---|---|---|
| `otlp-upstream/` | **upstream bytes** | Downloaded verbatim from the pinned upstream release. Never hand-edited. Byte-provenance is recorded and re-verified by the suite. |
| `reject/` | **derived from upstream** | Each file is `otlp-upstream/trace.json` with **exactly one** normative rule violated. Generated mechanically, so the diff against upstream *is* the rule. |
| `synthetic/` | **hand-authored** | af-side records and stand-in native events. Not upstream artifacts, not evidence of what a backend emits. |
| `telemetry-dto/` | **hand-authored** | Golden `af telemetry status\|report --json` payloads (issue #580). Not OTLP at all — a different wire contract, shared with the web module. See `telemetry-dto/README.md`. |
| `openobserve-v0.91.3/` | **captured backend** | Real HTTP answers from the pinned telemetry backend (issue #580, R2), captured read-only and PII-scrubbed. Not OTLP either — this is the *foreign* half of the contract, what OpenObserve replies rather than what `af` sends. Scrubbed, so not byte-provenanced like `otlp-upstream/`. See `openobserve-v0.91.3/README.md`. |
| `otlp-schema.json` | **derived (transcribed)** | Per-message key allowlists, enum ranges and field-encoding classes transcribed by hand from the pinned release's `.proto` files. Not upstream bytes. |

`manifest.json` records the class and the expected verdict for every fixture — including the
non-OTLP ones, which carry `"verdict": "not-otlp"` so the conformance sweep skips them. That
registration is mandatory: the suite walks this entire tree and fails any unrecorded `.json`.

## The pin

`OTLP_PROTO_VERSION` holds the release tag on line 1 and that tag's commit on line 2:

```
v1.11.0
790608c4d51e6ffc12210b541e8514cbed9e91a4
```

Chosen because it was the latest published release at authoring time (2026-07-21). Note the
example payload is byte-identical across several adjacent tags, so the pin identifies the
*specification revision the fixtures were read against*, which is the thing that matters.

## `otlp-upstream/trace.json` — how it was obtained and how to re-verify it

Retrieved on 2026-07-23 with:

```
curl -fsSL -o internal/telemetry/testdata/otlp-upstream/trace.json \
  https://raw.githubusercontent.com/open-telemetry/opentelemetry-proto/v1.11.0/examples/trace.json
```

Byte-provenance, verified at authoring time and re-verifiable at any time:

| Check | Value |
|---|---|
| size | 1229 bytes |
| `git hash-object` (git blob SHA-1) | `41130ff1aa0d7379812a2c7d7f89bbf80d72fe21` |
| `sha256sum` | `f8f2870852b247f734a53ca7f022d4d942bd29732df54440494948af181bd373` |

The blob SHA-1 is the decisive one: it matches the SHA that GitHub's contents API reports for
`examples/trace.json` at tag `v1.11.0`, so these bytes are provably the upstream bytes rather
than a proxy or CDN mutation. Re-verify with:

```
git hash-object internal/telemetry/testdata/otlp-upstream/trace.json
curl -sS 'https://api.github.com/repos/open-telemetry/opentelemetry-proto/contents/examples?ref=v1.11.0'
```

The conformance suite re-checks the recorded `sha256` on every run, so this file silently
becoming hand-edited is a test failure rather than a discovery years later.

## The normative rules the fixtures pin

Quoted from `docs/specification.md` §"JSON Protobuf Encoding" at the pinned tag
(<https://raw.githubusercontent.com/open-telemetry/opentelemetry-proto/v1.11.0/docs/specification.md>).
OTLP/JSON is proto3 standard JSON mapping **with deviations**, and the deviations are exactly
where a hand-rolled encoder goes wrong:

1. *"The `traceId` and `spanId` byte arrays are represented as case-insensitive hex-encoded
   strings; they are not base64-encoded as is defined in the standard Protobuf JSON Mapping."*
2. *"Values of enum fields MUST be encoded as integer values … only integer enum values are
   allowed in OTLP JSON Protobuf Encoding; the enum name strings MUST NOT be used."*
3. *"OTLP/JSON receivers MUST ignore message fields with unknown names."*
4. *"The keys of JSON objects are field names converted to lowerCamelCase. Original field
   names are not valid to use as keys for JSON objects."*
5. *"64-bit integer numbers in JSON-encoded payloads are encoded as decimal strings, and
   either numbers or strings are accepted when decoding."*
6. *"The client and the server MUST set `Content-Type: application/json` … when sending JSON
   Protobuf encoded payload."* Default traces path: `/v1/traces`.

Two consequences worth stating, because both have bitten before:

- **Rule 1 says case-INsensitive.** The upstream example carries UPPERCASE ids
  (`5B8EFFF798038103D269B633813FC60C`). This project emits lowercase. Both are conformant, so
  the validator must not care about case — while a byte-for-byte comparison of our output
  against the upstream file would fail on case alone and invite someone to "fix" it by
  abandoning the project's own convention. The suite therefore validates *rules*, and asserts
  lowercase separately as a producer-side convention.
- **Rule 5 is asymmetric.** Emitting a 64-bit field as a decimal string is required; but
  a decoder that insists on the string form violates the "either … are accepted when
  decoding" half. Fixtures are therefore decoded loosely, never through the emission structs.

## The reject corpus

Each `reject/` fixture violates one rule and only that rule, so a validator that fails to
catch it has a precisely identifiable hole. They were generated from the upstream file with
single-property mutations; `diff <(jq -S . otlp-upstream/trace.json) <(jq -S . reject/X.json)`
shows the rule and nothing else.

The corpus is the anti-vacuity mechanism. A validator that accepts everything passes the
upstream fixture; a validator that rejects everything fails it. Only a validator that gets
both classes right passes the suite.

## Convention note

This repository had no golden-file convention before this directory. The shape follows
`internal/formula/testdata/parity/`: one file per scenario plus a `manifest.json` index whose
leading `_comment` states the contract in prose. Two guards inherited from that precedent are
load-bearing here — the suite fails if the manifest lists zero fixtures, and it fails if a
file on disk has no manifest entry. Without them a fixture corpus quietly becomes decorative.

Nothing in this directory is generated by this repository's encoder, and nothing in it is
read at any time other than test execution.
