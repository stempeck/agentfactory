// Package telemetry owns the af-side step record: its schema, its durable storage, its
// OpenTelemetry wire format, and the bounded exporter that ships it.
//
// The package is deliberately stdlib-only. The dependency policy forbids adopting an
// OpenTelemetry SDK, and the encoder here is the hand-rolled consequence of that. A previous
// attempt at this wire format was validated only against one tolerant receiver, which
// demonstrates that receiver's tolerance rather than conformance; the answer is not a bigger
// dependency but a better proof, so the conformance fixtures under testdata/ were extracted
// from a pinned upstream release and committed BEFORE the encoder was written. See
// testdata/README.md.
//
// The package reads no environment variable. Every input arrives as a parameter, including
// the factory root that the telemetry directory is derived from, so a caller inside a
// worktree and a caller at the outer root cannot silently disagree about where records live.
// LaunchEnv constructs an environment for a child process; it never consults its own.
//
// Nothing here is wired into a command. Recording at the lifecycle verbs and injecting the
// launch environment are later phases; this package exists to be correct first.
//
// Three properties are load-bearing and each has a test that fails loudly if it regresses:
//
//   - The record struct IS the privacy boundary. There is no redaction step downstream, so a
//     field capable of holding free-form text is a leak by construction. The schema is a
//     closed allowlist of scalars.
//   - Rotation never discards records the export cursor has not passed. Where a hard ceiling
//     forces a discard, the count is computed before the rename and persisted, because a hole
//     in the data that nothing reports is worse than a hole that does.
//   - Trace and span identifiers are pure functions of the formula instance and step. Export
//     retries after a failure, and without stable identifiers each retry would create
//     duplicate spans in the backend rather than replacing the originals.
package telemetry
