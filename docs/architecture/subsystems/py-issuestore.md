# py-issuestore subsystem

**Covers:** py/issuestore

## Shape

The `py/issuestore/` package is an in-tree Python 3.12 MCP server that speaks JSON-RPC 2.0 over HTTP and backs the Go-side `Store` contract with SQLite via SQLAlchemy (`py/issuestore/server.py:1-9`, `py/issuestore/store.py:1-13`). It was introduced greenfield as Phase 4 of design 80 to replace the previous `bd` binary; commit `ba77510` landed the server, 9 JSON-RPC tools, the 4-table schema, loopback-only bind, ephemeral port, the endpoint-file rendezvous at `.runtime/mcp_server.json`, and SIGTERM-clean shutdown in a single patch (commit `ba77510` 2026-04-16). The server is module-invoked as `python3 -m py.issuestore.server --db-path PATH --endpoint-file PATH [--port N]` (`py/issuestore/server.py:8`) and is lazy-spawned by the Go adapter at `internal/issuestore/mcpstore/lifecycle.go:130-133`. The runtime dependency surface is exactly two pinned libraries: `aiohttp==3.12.15` and `sqlalchemy==2.0.31` (`py/requirements.txt:1-2`). Both `py/__init__.py` and `py/issuestore/__init__.py` are zero-byte package markers (confirmed empty).

## Wire contract (JSON-RPC methods)

Method dispatch lives in `handle_jsonrpc` at `py/issuestore/server.py:157-214`. The three protocol-level methods are `initialize` (server.py:169-174), `tools/list` (server.py:176-181), and `tools/call` (server.py:183-208). `tools/call` dispatches by tool name via `getattr(store, name, None)` with a hard prefix guard `name.startswith("issuestore_")` (server.py:186-187) and runs the handler on a thread via `asyncio.to_thread` (server.py:194). Successful results are wrapped in `{"content": [{"type": "text", "text": json.dumps(result)}]}` (server.py:195-199); exceptions are returned with `isError: true` and the stringified exception (server.py:200-208). The Go client unwraps this double envelope at `internal/issuestore/mcpstore/client.go:68-93` and matches the `isError` text against `"issue not found:"` to map to `ErrNotFound` (client.go:80-84).

| Method | Params | Returns | Anchor | Go client anchor |
|--------|--------|---------|--------|------------------|
| `initialize` | — | `{"serverInfo":{"name":"issuestore","version":"0.1.0"}}` | server.py:169-174 | unknown — needs review |
| `tools/list` | — | `{"tools": TOOLS}` (9 entries) | server.py:176-181, 25-149 | unknown — needs review |
| `tools/call` | `{name, arguments}` | content-envelope(text=json) | server.py:183-208 | client.go:40-94 |
| `issuestore_get` | `{id}` (required) | issue DTO | server.py:26-34; store.py:141-144 | mcpstore.go:49 |
| `issuestore_list` | `{parent?, statuses?, type?, assignee?, labels?, include_all_agents?, include_closed?}` | `[]issue` | server.py:35-50; store.py:210-217 | mcpstore.go:62 |
| `issuestore_ready` | `{molecule_id?, parent?, assignee?, include_all_agents?}` | `{steps:[], total_steps:int, molecule_id:str}` | server.py:51-63; store.py:222-244 | mcpstore.go:75 |
| `issuestore_create` | `{title, type, description?, assignee?, priority?, parent?, labels?, actor?}` (title+type required) | issue DTO | server.py:64-81; store.py:89-136 | mcpstore.go:110 |
| `issuestore_patch` | `{id, notes?, title?, status?, priority?, assignee?, parent?, type?, labels?}` (id required) | `null` | server.py:82-100; store.py:249-299 | mcpstore.go:125, 175 |
| `issuestore_close` | `{id, reason?}` (id required) | `null` | server.py:101-112; store.py:304-320 | mcpstore.go:131 |
| `issuestore_dep_add` | `{issue_id, depends_on_id}` (both required) | `null` | server.py:113-124; store.py:325-333 | mcpstore.go:140 |
| `issuestore_render` | `{id}` (required) | string | server.py:125-133; store.py:385-389 | mcpstore.go:147 |
| `issuestore_render_list` | `{parent?, statuses?, type?, assignee?, include_all_agents?, include_closed?}` | string | server.py:134-148; store.py:392-394 | mcpstore.go:158 |

Issue DTO shape (wire-level, from `_issue_from_row`, store.py:50-71): `id, title, description, assignee, type, status, priority:int, labels:[str], notes, close_reason, created_at, updated_at` always present; `parent` and `blocked_by:[{"id":...}]` conditionally included. `priority` is int on the wire, 0=urgent / 1=high / 2=normal / 3=low (store.py:23).

## Server lifecycle

- **Loopback-only bind (R-SEC-1):** `web.TCPSite(runner, host="127.0.0.1", port=port)` with an inline comment forbidding `0.0.0.0` or `"localhost"` (server.py:267-268). R-SEC-1 is re-asserted in the module docstring (server.py:3).
- **Ephemeral port:** CLI `--port` defaults to `0` (server.py:292); actual bound port is resolved post-`site.start()` via `site._server.sockets[0].getsockname()[1]` (server.py:271).
- **Endpoint file atomic publish:** `_write_endpoint` writes `{transport:"http", address:"127.0.0.1:PORT", pid:int, started_at:iso8601Z}` to `<file>.tmp` then `os.replace` into place (server.py:221-231). File path is supplied by the Go spawner as `<factoryRoot>/.runtime/mcp_server.json` (`internal/issuestore/mcpstore/lifecycle.go:120`).
- **SIGTERM / SIGINT handling:** `_install_signal_handlers` registers an asyncio shutdown trigger for both signals, with a `signal.signal` fallback for platforms that don't support `loop.add_signal_handler` (server.py:242-251).
- **Shutdown ordering (AC-8 b, WAL flush):** on shutdown event the server drains aiohttp (`runner.cleanup()`), then calls `_engine.dispose()` which closes the pool and triggers SQLite's last-connection-close WAL checkpoint, then removes the endpoint file (server.py:279-284; docstring server.py:3-5; store.py-level comment server.py:280).
- **Health endpoint:** `GET /health` → `{"status":"ok"}` (server.py:217-218, registered at server.py:263). Used by Go-side `healthCheck` during start (`internal/issuestore/mcpstore/lifecycle.go:154`).

## Schema

Defined in `py/issuestore/schema.py`. Four tables — `issues`, `labels`, `deps`, `metadata` — declared both as SQLAlchemy `Table` objects (schema.py:23-61) and as raw DDL in `_create_all` (schema.py:101-138) because SQLAlchemy's `Table()` does not emit inline composite PRIMARY KEY DDL for `labels (issue_id, position)` and `deps (issue_id, depends_on_id)` (schema.py:94-98). Three indexes: `idx_issues_parent_status`, `idx_issues_assignee`, `idx_issues_created_at` (schema.py:63-65 and schema.py:139-141).

Four PRAGMAs are applied on every connection via a `connect` event listener (schema.py:68-74, 84): `journal_mode=WAL`, `busy_timeout=5000`, `foreign_keys=ON`, `synchronous=NORMAL`. The module docstring flags these as load-bearing for CASCADE + WAL semantics under R-DATA-2, C-12 (schema.py:1-7). `labels` and `deps` both declare `ON DELETE CASCADE` on their `issue_id` foreign keys (schema.py:120, 128-129) — the `foreign_keys=ON` PRAGMA is what makes CASCADE live.

Engine is created with `pool_size=4, max_overflow=4, pool_pre_ping=True` (schema.py:78-83).

## Seams

| Seam | Direction | Contract | Anchor |
|------|-----------|----------|--------|
| SQLite file (`<factoryRoot>/.beads/issues.sqlite`) | OUT | SQLAlchemy + 4 PRAGMAs (WAL/busy_timeout/foreign_keys/synchronous) | schema.py:68-74, 84; db path chosen by Go spawner at lifecycle.go:119 |
| Endpoint file (`<factoryRoot>/.runtime/mcp_server.json`) | OUT | JSON `{transport, address, pid, started_at}`, atomic-replace write, removed on SIGTERM | server.py:221-231, 234-239, 281-284 |
| Go mcpstore client | IN | JSON-RPC 2.0 over HTTP POST `/` + GET `/health` | `internal/issuestore/mcpstore/client.go:40-94`; dispatch server.py:157-214 |
| install.go (indirect) | — | Python 3.12.x version check at `af install --init`; aborts before any filesystem mutation | `internal/cmd/install.go:280-290` |
| lifecycle.go spawner (indirect) | IN | `python3 -m py.issuestore.server --db-path ... --endpoint-file ...`, cwd=factoryRoot, detached from caller context | `internal/issuestore/mcpstore/lifecycle.go:126-148` |

## Formative commits

| SHA | Date | Subject | Why |
|-----|------|---------|-----|
| `ba77510` | 2026-04-16 | Phase 4: Python MCP server (aiohttp + SQLAlchemy + SQLite) that replaces bd. 9 JSON-RPC tools, 4-table schema with WAL/foreign-keys PRAGMAs, loopback-only bind, ephemeral port, endpoint file at `.runtime/mcp_server.json`, SIGTERM-clean shutdown. Greenfield `py/issuestore/` — consumed by mcpstore adapter in Phase 5. | Sole formative commit — created all five files in scope. No subsequent commits have touched `py/issuestore/` or `py/requirements.txt` (verified via `git log ba77510..HEAD -- py/issuestore/ py/requirements.txt` → empty). |

## Load-bearing invariants

- **Loopback-only bind (R-SEC-1):** literal `127.0.0.1`, never `0.0.0.0`, never `"localhost"`. server.py:267-268, re-asserted server.py:3.
- **WAL + foreign_keys + busy_timeout + synchronous PRAGMAs on every connection (R-DATA-2, C-12):** schema.py:68-74, 84; docstring schema.py:1-7.
- **SIGTERM flush-before-exit (AC-8 b):** aiohttp drain → `engine.dispose()` (forces WAL checkpoint via last-connection-close) → remove endpoint file → `sys.exit(0)`. server.py:279-285; docstring server.py:3-5.
- **Patch / Close MUST NOT mutate `description` (C-10):** `issuestore_patch` whitelists `notes, title, status, priority, assignee, parent, type, labels` only; `description` is deliberately not accepted (store.py:249-299). `issuestore_close` updates only `status`, `close_reason`, `updated_at` (store.py:309-319). store.py:10 and 251-254 call this out explicitly.
- **Ready ordering and dep-completeness rule:** `ready` returns non-terminal issues where NO dep target has status outside `('closed','done')`, ordered `created_at ASC, id ASC` (store.py:226-236). Called out in docstring store.py:11-13.
- **Wire-level Priority is int, not string:** `priority` is serialized as `int(row["priority"])` (store.py:58). `PRIORITY_NAMES` dict (store.py:23) is only used for render output.
- **`blocked_by` is `[{"id": "af-..."}]`, not `[string]`:** store.py:42-47; called out store.py:8.
- **Python 3.12 enforced at install time:** `checkPython312` rejects any `python3 --version` that doesn't contain `"Python 3.12"`. `internal/cmd/install.go:280-290` (check itself at line 286).
- **Endpoint-file atomicity:** tmp-then-`os.replace` so a concurrent reader never sees a partial file (server.py:228-231).

## Cross-referenced idioms

- **Endpoint-file rendezvous:** server writes `{transport, address, pid, started_at}` to `<factoryRoot>/.runtime/mcp_server.json` atomically after the socket is listening (server.py:221-231, called at server.py:272); Go client reads and PID-probes it via `readEndpoint` (`internal/issuestore/mcpstore/lifecycle.go:167-181`). This is the sole handshake mechanism — no environment variables, no stdout parsing.
- **Module-invocation for package-relative imports:** spawner uses `python3 -m py.issuestore.server` with `cmd.Dir = factoryRoot` so the relative import `from . import store` (server.py:21) resolves against repo's `py/` package (lifecycle.go:126-134; server.py:8).
- **Double-envelope tool response:** JSON-RPC `result` carries a `content[]` array whose first element has a `text` field that contains the tool's JSON-encoded payload. The Go client's `call()` documents and unwraps both layers (client.go:16-22, 75-91).
- **Detached subprocess lifetime:** spawner uses `exec.Command` (not `CommandContext`) and reaps via background `cmd.Wait()` goroutine — server outlives any single RPC (lifecycle.go:129, 144-148).

## Formal constraint tags

- **R-SEC-1** — loopback-only bind (server.py:3, 267).
- **R-DATA-2, C-12** — WAL + foreign_keys PRAGMAs on every connection (schema.py:1-7).
- **AC-8 b** — SIGTERM flush-before-exit ordering (server.py:3-5, 279-284).
- **C-10** — Patch/Close never mutate `description` (store.py:10, 251-254, 305).
- **Phase 5 RunStoreContract** — wire invariants store.py:6-13 depends on (Issue DTO keys, BlockedBy shape, int priority, Ready ordering). Store.go line references in store.py:7, 149-157 are to the pre-Phase-4 Go `store.go`; this doc doesn't cross-check those numbers.

## Gaps

- **`actor` column semantics:** `issues.actor` exists in schema (schema.py:34, 111) and is written by `issuestore_create` (store.py:101, 126) but is NOT read back by `_issue_from_row` (store.py:50-71), NOT filterable by `_list_filter_clause` (store.py:149-207), and NOT an accepted field in `issuestore_patch` (store.py:249-299). **unknown — needs review** whether this is intentional (actor is provenance-only, write-once) or a wiring gap. No commit message or inline comment explains the discard.
- **`metadata` table:** declared (schema.py:56-61, 133-137) but no handler in `store.py` reads or writes it. **unknown — needs review** — likely reserved for schema versioning or future use; no current reader or writer in-tree.
- **`labels` NOT deduplicated:** `issuestore_create` inserts labels positionally with no UNIQUE constraint on `value` (store.py:131-135; schema.py:118-124 PK is `(issue_id, position)`). Duplicate label values at different positions are allowed by the schema. **unknown — needs review** whether this is intended.
- **Empty `isError` payload and KeyError repr coupling:** Go client relies on substring `"issue not found:"` to map to `ErrNotFound` (client.go:82), but the Python side raises `KeyError(f"issue not found: {issue_id}")` (store.py:83), whose `str(e)` is `"'issue not found: af-xyz'"` (with surrounding single quotes). Client comment acknowledges this (client.go:80-81). Brittle but currently correct.
- **`initialize` result minimal:** no `capabilities` or `protocolVersion` returned (server.py:169-174). **unknown — needs review** whether full MCP handshake fields are required by any consumer; current Go client doesn't appear to call `initialize`.
- **No commit history beyond `ba77510`:** `git log ba77510..HEAD -- py/issuestore/ py/requirements.txt` is empty. If any later refactor touches this subsystem, this doc needs regeneration.
- **`py/__init__.py` and `py/issuestore/__init__.py` are empty:** confirmed zero-byte. Present only as package markers.
