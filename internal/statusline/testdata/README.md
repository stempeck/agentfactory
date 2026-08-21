# internal/statusline/testdata

Fixtures for the render library's golden and escape-path tests (Issue #591, Phase 2) and for the
transcript token counter (Issue #600, Phase 2).

## Provenance classes — never mix these

| Class | Meaning | Where |
|-------|---------|-------|
| **captured** | Taken from a real Claude Code payload/transcript, then scrubbed. Its value is that it records what upstream ACTUALLY emits, so it may not be regenerated from our own code. | `payload/`, `transcript/recorded-real/` |
| **in-code** | Hand-authored in the test for exact-integer arithmetic. Written to `t.TempDir()`, never committed. | see below |

## Manifest

| Fixture | Purpose |
|---------|---------|
| `transcript/` | **Gate K2-V** for issue #600: two redacted real transcript captures plus the recorded findings on usage-record shape, record **multiplicity**, the **sidechain** location, and the offset-advance policy. See `transcript/README.md` — it is the findings document, not just a fixture index. |
| `payload/full_2_1_212.json` | A full Claude Code 2.1.212 stdin payload captured against the schema at `.designs/591/codebase-snapshot.md:404-448`. Carries BOTH `workspace.project_dir` AND `workspace.current_dir` with *different* values so a field-naming slip in the decoder surfaces as a golden-diff in CI, not as a silently blank `dir` element (design-doc.md:158-161, cross-review L1). Includes the runtime-only `cost` family (`:444-448`), a non-null `used_percentage`, and unknown fields (`version`, `exceeds_200k_tokens`, `fast_mode`, `added_dirs`) that the tolerant decoder must ignore. Drives `TestRender_GoldenFixture2_1_212`.

## Fixtures built in-code (not on disk)

- **Field-deleted / nulled variants** — constructed from the golden `Payload` struct in
  `render_test.go` so each element's fail-open (missing field ⇒ element omitted) is asserted
  one field at a time (`TestRender_PerElementFailOpen`).
- **Hostile-string variants** — branch/path/model carrying ANSI/OSC/control bytes are built
  inline (`TestRender_HostileStringsSanitized`, `TestRender_NoWatchdogNeedlesEverEmitted`).
- **500-snapshot budget directory** — generated into `t.TempDir()` at test time
  (`TestRender_Budget`); the repo convention is that test filesystem writes go to `t.TempDir()`,
  so 500 tiny files are not committed here.
