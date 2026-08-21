// Package memory owns the agent learnings vault: the note schema, its restricted-frontmatter
// codec, the append-only store operations over it, and the pure budget-bounding read slice.
//
// The package reads no environment variable and resolves no root. Every input arrives as a
// parameter, including the factory root the vault is derived from, so a caller inside a
// worktree and a caller at the outer root cannot silently disagree about where notes live —
// which matters more here than anywhere else in the tree, because the entire promise of this
// subsystem is that the bytes land OUTSIDE every worktree-removal domain. Path construction
// lives in internal/config and only there; this package calls config.AgentMemoryDir rather
// than spelling the vault path itself (the issue #563 rule, internal/config/paths.go).
//
// It is deliberately stdlib-only. The frontmatter dialect is a restricted YAML subset — flat
// key: value pairs, ISO-8601 timestamps and one-line JSON-style string arrays — chosen so that
// Obsidian renders it as Properties natively and so that no dependency is needed to read it
// (ADR-013). The TOML decoder already in go.mod is not used: it cannot parse `key: value` at
// all, and it is all-or-nothing per document, which is incompatible with the per-field
// degradation below.
//
// Three properties are load-bearing, and each has a test that fails loudly if it regresses:
//
//   - Notes are never deleted. Graduate and Expire only MARK frontmatter; the file stays on
//     disk and the read path filters marked notes out. Destruction is an operator action, and
//     no agent-reachable code path in this package removes a note.
//
//   - Parsing is total. A hand-edited note that breaks the dialect degrades to Malformed plus
//     its body, never to an error — Parse feeds a session-start hook where ADR-007 requires
//     silence, and an error there would turn one bad file into a dark memory channel. A
//     malformed note is still surfaced to status so the operator can see it, and is never
//     served to injection.
//
//   - Injection cost is bounded by construction. Slice is the only thing that feeds the startup
//     hook and it caps the result at K notes, a per-note excerpt, and a total byte ceiling. The
//     ceiling exists because the mail subsystem's uncapped channel priced context so highly
//     that destroying messages became the only relief; under-serving is the correct failure
//     direction here, and the constants are pinned by tests rather than by convention.
package memory
