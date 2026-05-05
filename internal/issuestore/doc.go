// Package issuestore defines the neutral Store interface every issue-tracking
// backend must implement, plus the DTOs (Issue, Filter, CreateParams, etc.)
// that flow across the boundary.
//
// Two backends live under sub-packages:
//
//   - mcpstore (production): HTTP JSON-RPC to a Python MCP server
//     (py/issuestore). Used by the af binary in production. Requires Python
//     3.12 on the host; the server is lazy-started and persists to SQLite.
//
//   - memstore (test fake): in-memory mutex-protected map. Used by unit tests
//     so they pass without any backend installed. Render output is a
//     tests-only approximation.
//
// # IsTerminal policy
//
// Status.IsTerminal returns true for both StatusClosed AND StatusDone (D11 /
// C-1). All "is this issue done with" predicates MUST go through this method
// rather than comparing against a single sentinel value, because mail's
// read/unread translation depends on both terminal states being treated
// identically. Skipping IsTerminal causes silent mail re-surfacing bugs.
//
// # Render and RenderList — DO NOT PARSE
//
// The two Render* methods on Store return human-readable structured text for
// terminal display. Programmatic callers MUST use Get / List, which return
// structured DTOs. The Render format is pinned across adapters by
// RunStoreContract's Render_semantic_parity sub-test (it asserts the output
// contains title, status, type, priority, label, description, notes, and
// close reason) — that is the contract, but it is not a parse spec. Fields
// may be added or reordered within the contract's semantic bounds without
// notice. DO NOT PARSE its contents (R-API-5).
//
// # Cross-adapter contract
//
// RunStoreContract (in contract.go, NOT contract_test.go, so sub-packages can
// import it) exercises every behavior both backends must agree on. Each
// adapter's test file calls RunStoreContract with a factory and the suite
// runs against that adapter. This is the single source of truth for "what a
// Store must do".
package issuestore
