# issuestore subsystem

**Covers:** internal/issuestore, internal/issuestore/mcpstore, internal/issuestore/memstore

## Shape

`internal/issuestore/` defines the neutral `Store` interface + DTOs (`Issue`, `Filter`, `CreateParams`, `Patch`, `ReadyResult`) that every issue-tracking backend must implement (`internal/issuestore/store.go:20-36`). Two adapters live under sub-packages: `mcpstore/` (production — HTTP JSON-RPC 2.0 to the in-tree Python server at `py/issuestore/`, declared at `internal/issuestore/mcpstore/mcpstore.go:1-11`) and `memstore/` (in-process mutex-protected map used by unit tests, declared at `internal/issuestore/memstore/memstore.go:1-7`). `contract.go` carries the cross-adapter behavioral suite `RunStoreContract` — intentionally NOT a `_test.go` file so sub-packages can import it (`internal/issuestore/contract.go:30-33`). Per the Phase 7 commit (7acd617, 2026-04-17), `mcpstore` is the sole production adapter; the historical `bdstore/` subdirectory (9 files, 2097 lines) was deleted and all `BD_ACTOR` fallbacks were stripped from seam call sites.

## Interface surface (`internal/issuestore/store.go`)

### `Store` interface — 9 methods (`store.go:20-36`)
| Method | Contract | Anchor |
|---|---|---|
| `Get(ctx, id) (Issue, error)` | Returns wrapped `ErrNotFound` when absent. | `store.go:21`, `store.go:187` |
| `List(ctx, Filter) ([]Issue, error)` | Adapter-specific order; memstore sorts by `mem-N` id (`memstore.go:112-115`); mcpstore server-ordered. | `store.go:22` |
| `Ready(ctx, Filter) (ReadyResult, error)` | Non-terminal issues whose deps are all terminal; ordered by `CreatedAt` ASC with ID tie-break (pinned by `contract.go:467-534`). `Filter.MoleculeID` scopes to children of that parent (`mcpstore.go:71-73`, `memstore.go:210-212`). | `store.go:23` |
| `Create(ctx, CreateParams) (Issue, error)` | New issues are always `StatusOpen` (`memstore.go:66`). Labels preserve insertion order with no dedup (C13, `store.go:40`, `mcpstore.go:104-108`). | `store.go:24` |
| `Patch(ctx, id, Patch) error` | Notes-only today (Gotcha 11, `store.go:173-176`). Last-writer-wins. MUST NOT mutate Description (C-10, `mcpstore.go:117-119`; contract pin `contract.go:661-710`). | `store.go:25` |
| `Close(ctx, id, reason) error` | Writes `CloseReason` to dedicated column, never Description (C-10, `memstore.go:258-261`; contract pin `contract.go:712-741`). | `store.go:26` |
| `DepAdd(ctx, issueID, dependsOnID) error` | Records forward edge; both issues must exist (`memstore.go:281-286`). | `store.go:27` |
| `Render(ctx, id) (string, error)` | Display-only text. **DO NOT PARSE** (R-API-5, `doc.go:23-32`). | `store.go:31` |
| `RenderList(ctx, Filter) (string, error)` | Display-only text. **DO NOT PARSE** (R-API-5, `doc.go:23-32`). | `store.go:35` |

### `Filter` fields (`store.go:147-157`)
| Field | Semantic | Anchor |
|---|---|---|
| `Parent` | Exact-match on `Issue.Parent`; returns only children, not the parent itself (contract pin `contract.go:357-413`). | `store.go:148` |
| `Statuses` | `nil` → all non-terminal (open/hooked/pinned/in_progress); non-empty → OR of listed statuses. Adapters MUST NOT collapse nil to empty (H-A R2, D14, Gotcha 12; contract pin `contract.go:229-305`). | `store.go:136-143`, `store.go:149` |
| `Type` | Exact-match on `IssueType` (task/epic/bug/feature/gate). | `store.go:150` |
| `Assignee` | Explicit assignee; when non-empty, overrides the client-side actor overlay (`mcpstore.go:209-214`). | `store.go:151` |
| `Labels` | ANDed (all must match, `memstore.go:172-176`). | `store.go:152` |
| `MoleculeID` | For `Ready`: scopes to children of that parent (`memstore.go:210-212`). | `store.go:153` |
| `IncludeAllAgents` | Bypasses the default actor overlay (Gate-4). | `store.go:155` |
| `IncludeClosed` | Admits closed/done in addition to whatever `Statuses` matched. H-2/D13 split bd's overloaded `--all` into two axes. | `store.go:146`, `store.go:156` |

### `Status` enum — 6 values (D11/C-1, `store.go:77-88`)
`StatusOpen`, `StatusHooked`, `StatusPinned`, `StatusInProgress`, `StatusClosed`, `StatusDone`. Wire-format literal strings pinned by `store_test.go:108-125`. `IsTerminal() bool` returns true iff status is `closed` OR `done` (`store.go:100-102`); this is the single "done?" gate — mail's read/unread translation routes through it to fix the C-1 bug where `Read: bm.Status == "closed"` silently re-surfaced done/in_progress/hooked/pinned mail as unread (045c1e1, 2026-04-08).

### Other DTOs
- `Issue` (`store.go:41-56`): `Notes` and `CloseReason` are dedicated columns added in Phase 1 (1a117c2, 2026-04-15) with explicit non-`omitempty` JSON tags pinned by `store_test.go:40-101`.
- `CreateParams` (`store.go:160-170`), `Patch` (`store.go:174-176`), `ReadyResult` (`store.go:179-183`), `IssueRef` (`store.go:60-62`), `Priority` (`store.go:106-132`).
- `ErrNotFound` sentinel (`store.go:187`) — callers detect with `errors.Is`.

## Contract test suite (`internal/issuestore/contract.go`)

`RunStoreContract(t, factory, setStatus)` at `contract.go:66` is the cross-adapter behavioral suite both backends must pass. Not a `_test.go` file so sub-packages can import it (`contract.go:30-33`). `factory` returns a fresh empty Store per call; `actor` flows in as an explicit parameter (no process env — #98 invariant, `contract.go:37-39`). `setStatus` is a per-adapter test-only helper that places a seeded issue into any of the 6 Status values per Gotcha 9 (`contract.go:23`, `memstore.go:339`, `mcpstore.go:170-176`).

### Sub-tests — every one is a contract
| Sub-test | Line | Pins |
|---|---|---|
| `IsTerminal_policy` | `contract.go:69-83` | closed/done terminal; other 4 non-terminal (D11/C-1). |
| `Get_missing_returns_ErrNotFound` | `contract.go:85-95` | `errors.Is(err, ErrNotFound)` match. |
| `Create_then_Get_round_trips` | `contract.go:97-121` | ID populated; fields survive round-trip. |
| `Labels_round_trip_no_dedup_no_canonicalization` | `contract.go:123-153` | Set-equal: no labels lost or added (C13). |
| `Filter_nil_Statuses_returns_all_non_terminal` | `contract.go:229-250` | Nil Statuses → 4 non-terminals only; terminals excluded (H-A R2). |
| `Filter_single_status_open_returns_only_open` | `contract.go:252-279` | `[StatusOpen]` excludes hooked/pinned/in_progress (mail's C8 preservation). |
| `Filter_multi_status_OR_semantics` | `contract.go:281-305` | `[StatusOpen, StatusInProgress]` returns OR (D14/H-3). |
| `Filter_IncludeClosed_admits_terminal` | `contract.go:307-322` | Closed/done surfaced. |
| `Filter_IncludeAllAgents_admits_other_agents` | `contract.go:324-336` | `IncludeAllAgents=true` returns non-BD_ACTOR assignees. |
| `Filter_IncludeAllAgents_false_hides_other_agents` | `contract.go:338-355` | With actor="BD_ACTOR", other-agent fixture hidden. |
| `Filter_Parent_returns_only_matching_children` | `contract.go:357-413` | Parent filter excludes parent itself and unrelated children. |
| `All_nine_methods_callable` | `contract.go:415-460` | Smoke-test every Store method. |
| `Ready_ordering_by_created_at_ASC` | `contract.go:467-534` | Ready.Steps ordered by CreatedAt ASC; ID tie-break when CreatedAt equal. |
| `Ready_dependency_filtering` | `contract.go:536-587` | Blocked issue absent while dep open; appears after dep closed. |
| `Ready_orphan_deps_dont_block` | `contract.go:589-629` | Closing a dep before any Ready call does not block the dependent. |
| `TotalSteps_ge_steps_len` | `contract.go:631-659` | `ReadyResult.TotalSteps >= len(Steps)`. |
| `Patch_notes_populates_Notes_field` | `contract.go:661-710` | Patch writes Notes column, never Description (C-10); last-writer-wins. |
| `Close_reason_populates_CloseReason_field` | `contract.go:712-741` | Close writes CloseReason column, never Description (C-10). |
| `Labels_preserve_insertion_order` | `contract.go:743-771` | Strict insertion-order preservation across ALL adapters (C13). |
| `Render_semantic_parity` | `contract.go:773-819` | Render output contains title/status/type/priority/label/description/notes/close_reason (semantic, not exact format). |
| `Actor_scoping` | `contract.go:821-854` | `List(Filter{IncludeAllAgents:false, Assignee:""})` with non-empty actor returns only that actor's issues (Gate-4). |

### Adapters that run it
- `internal/issuestore/memstore/memstore_test.go:15-23` → `TestMemStoreContract`
- `internal/issuestore/mcpstore/mcpstore_test.go:27-68` → `TestMCPStoreContract` (build tag `integration`; skips when python3/aiohttp/sqlalchemy absent)

## mcpstore adapter (`internal/issuestore/mcpstore/`)

### Lifecycle (`lifecycle.go`)
`discoverOrStart(factoryRoot)` at `lifecycle.go:42` produces a `http://127.0.0.1:<port>` base URL:
1. **Fast-path probe** (`lifecycle.go:45`): `tryLiveEndpoint` reads `.runtime/mcp_server.json`, kill(pid,0) liveness check (`lifecycle.go:186-189`, mirrors `internal/lock/lock.go` pattern; EPERM means alive but different owner), and 500ms HTTP GET `/health` (`lifecycle.go:192-200`).
2. **Lock-guarded serialize** (`lifecycle.go:49-60`): acquires `.runtime/mcp_start.lock` via `lock.NewWithPath` (extended from `internal/lock` in Phase 5, ef0411c). On `ErrLocked`, falls into `pollForEndpoint` (`lifecycle.go:93-104`, 10s deadline).
3. **Re-probe under the lock** (`lifecycle.go:63-66`): a peer may have spawned between first probe and lock acquisition.
4. **Spawn** (`lifecycle.go:109-165`): deletes stale endpoint file (`lifecycle.go:124`), runs `python3 -m py.issuestore.server --db-path .beads/issues.sqlite --endpoint-file .runtime/mcp_server.json` with `cmd.Dir=factoryRoot`. Stdout/Stderr left nil — inheriting parent stderr causes `go test` to hang on `exec: WaitDelay expired` because the long-lived server retains pipe ownership (`lifecycle.go:135-140`). `go func(){ cmd.Wait() }()` reaps the eventual exit status (`lifecycle.go:148`). NO `exec.CommandContext` — server lifetime is detached from any single RPC caller's ctx (`lifecycle.go:128-130`). On health-timeout, sends SIGTERM (`lifecycle.go:159-161`).

Constants: `startWaitTimeout=10s`, `startPollEvery=50ms`, `healthTimeout=500ms` (`lifecycle.go:31-36`).

### Wire contract (`client.go`)
- **JSON-RPC 2.0** over HTTP POST to endpoint base URL + "/". Headers: `Content-Type: application/json` (`client.go:51-56`).
- **Method**: `tools/call`, params `{name, arguments}` (`client.go:41-46`). JSON-RPC `id` is monotonic atomic counter (`client.go:25`, `client.go:43`).
- **Double-encoded envelope**: outer JSON-RPC response's `result.content[0].text` carries the tool's JSON-encoded payload; `call()` unwraps both layers (`client.go:16-22`, `client.go:75-92`).
- **Error semantics** (`client.go:36-92`):
  - Python `KeyError` repr like `'issue not found: af-xyz'` → wrapped `ErrNotFound` (detected via `strings.Contains`, not `HasPrefix`, because single-quote repr; `client.go:80-84`).
  - Other `isError:true` tool errors → `rpcToolError`.
  - JSON-RPC protocol errors → `rpcProtocolError`.
- **Tool names** (9): `issuestore_get`, `issuestore_list`, `issuestore_ready`, `issuestore_create`, `issuestore_patch`, `issuestore_close`, `issuestore_dep_add`, `issuestore_render`, `issuestore_render_list` (dispatched in `mcpstore.go:49-162`; schema in `py/issuestore/server.py:25-149`).
- **Endpoint file**: `.runtime/mcp_server.json` — JSON payload `{transport, address, pid, started_at}` (`lifecycle.go:19-24`; Python writer at `py/issuestore/server.py:221-231` uses atomic `tmp + os.replace` for H-4/D15 atomic-write semantics).
- **Loopback-only bind**: `py/issuestore/server.py:267-268` literal `host="127.0.0.1"`; comment explicitly forbids `0.0.0.0`/`"localhost"` (R-SEC-1, also cited at `mcpstore.go:7`).
- **Persistence**: SQLite at `.beads/issues.sqlite`. WAL/foreign-keys PRAGMAs set by `py/issuestore/schema.py` (Phase 4 commit ba77510). SIGTERM handler in `py/issuestore/server.py:242-284` drains aiohttp, calls `engine.dispose()` (forces WAL checkpoint via last-connection-close), removes endpoint file, `sys.exit(0)`. Durability across SIGKILL pinned by `TestCrashRecovery` (`lifecycle_test.go:90-146`); SIGTERM WAL flush pinned by `TestSIGTERMCleanShutdown` (`lifecycle_test.go:153-205`).

### Actor-scoping injection site (`mcpstore.go:181-217`, critical region `mcpstore.go:199-214`)
`listArgs(filter, actor)` is the **single seam** where Gate-4 client-side scoping is applied; called by `List` (`mcpstore.go:60`), `Ready` (`mcpstore.go:70`), `RenderList` (`mcpstore.go:156`). Critical switch at `mcpstore.go:209-214`:

```go
switch {
case filter.Assignee != "":
    args["assignee"] = filter.Assignee          // explicit caller value wins
case actor != "" && !filter.IncludeAllAgents:
    args["assignee"] = actor                    // Gate-4 default overlay
}
```

Semantics: when the store's `actor` is non-empty AND the caller passed `IncludeAllAgents=false` AND did not supply an explicit `Filter.Assignee`, the adapter injects `assignee=actor` into the wire call. Any of the three disabling conditions suffices. This is the RBAC default — the Phase 5 commit (ef0411c, 2026-04-16) moved it here from `bdstore.go:269-276` (deleted in Phase 7, 7acd617). The recent commit 63307bb (2026-04-18) **rejected** a design that proposed weakening this overlay by bypassing it at the mcpstore seam; the correct opt-out is the `IncludeAllAgents: true` idiom — see "Cross-referenced idioms" below.

`actor` flows in as an explicit `New(factoryRoot, actor)` constructor argument (`mcpstore.go:33-44`); the adapter never reads process env (#98 env-isolation invariant, enforced by `env_hermetic_test.go` broadened in Phase 3, f959234).

## memstore adapter (`internal/issuestore/memstore/`)

In-memory, mutex-protected (`memstore.go:22`) map of `map[string]Issue` and forward dep edges `map[string][]string`. Not production — `doc.go:10-13` and `memstore.go:1-7` state tests-only; Render/RenderList are a tests-only approximation not byte-compatible with any production renderer (`memstore.go:291-293`, `memstore.go:319-321`).

Constructors: `New()` disables actor scoping (`memstore.go:31-37`); `NewWithActor(actor)` enables it (`memstore.go:42-46`). Actor moved to a `Store.actor` field in Phase 3 (f959234) — previously memstore/bdstore had diverged and PR #116 had used `os.Setenv` to paper over the gap, which was the defect that triggered #98.

Filter semantics mirror the contract exactly:
- Nil Statuses → all non-terminal unless `IncludeClosed` (`memstore.go:130-134`).
- Non-empty Statuses → OR semantics; `IncludeClosed` admits terminal-and-not-already-matched (`memstore.go:135-151`).
- Parent, Type, Assignee, Labels match as documented (`memstore.go:153-177`).
- Actor-scoping gate (`memstore.go:165-170`):
  ```go
  if !f.IncludeAllAgents && iss.Assignee != "" && s.actor != "" && iss.Assignee != s.actor {
      return false
  }
  ```

### Carve-outs / asymmetries vs mcpstore
- **Empty-assignee items pass the gate** (`memstore.go:168`): the condition requires `iss.Assignee != ""`. A sling-created step bead with no Assignee flows through regardless of the gate; this MASKS the historical `bd` adapter divergence where empty-assignee beads were hidden when `IncludeAllAgents=false`. Called out explicitly in `step.go:130-135` — "memstore's R-INT-9 carve-out … MASKS the historical divergence — so the test suite can't catch a regression that drops this flag." The integration test `TestDone_MultiStepFormula_ProgressesCorrectly` (`done_integration_test.go:37-120`) runs against **mcpstore** precisely because memstore would not catch the regression.
- **Seed helpers** (`seed.go`): `Seed(fixtures...)` and `SeedAt(statuses, fixtures...)` — test-only bulk loaders that share `createLocked` with `Create`.
- **SetStatus** (`memstore.go:339-350`): test-only, matches mcpstore's SetStatus (`mcpstore.go:170-176`); production MUST NOT call.
- **Render output** is NOT byte-compatible with any production renderer (`memstore.go:291-293`); only the semantic-contains checks of `Render_semantic_parity` apply.

ID format: synthetic `"mem-N"` (`memstore.go:51-53`), distinct from mcpstore's server-assigned IDs.

## Seams

| Seam | Direction | Contract | Anchor |
|---|---|---|---|
| Python MCP server (`py/issuestore/`) | OUT | JSON-RPC 2.0 over HTTP; loopback-only 127.0.0.1 (R-SEC-1); ephemeral port; `.runtime/mcp_server.json` endpoint file (atomic `tmp`+`os.replace` publish); SIGTERM flushes WAL via `engine.dispose()` | `client.go:40-94`, `lifecycle.go:109-165`, `py/issuestore/server.py:221-231`, `py/issuestore/server.py:242-285` |
| `internal/lock` | OUT | `NewWithPath(".runtime/mcp_start.lock")` serializes concurrent server starts; `ErrLocked` losers fall into `pollForEndpoint` | `lifecycle.go:49-60` |
| cmd layer | IN | `Store` interface via `newIssueStore(cwd, beadsDir, actor)` factory | `internal/cmd/bead.go:271`, many other call sites |
| mail subsystem (`internal/mail/`) | IN | `Store` + `Filter` via constructor injection `NewMailbox(identity, store)` | `internal/mail/mailbox.go:21-30` |
| formula/sling layer | IN | Creates parent epic + child step beads with NO Assignee; children discovered via `Filter{Parent:epicID, IncludeAllAgents:true}` | `done.go:97-101`, `step.go:136-140`, `prime.go:419-423` |
| SQLite `.beads/issues.sqlite` | OUT (Python side) | WAL + foreign-keys PRAGMAs; 4-table schema | `py/issuestore/schema.py` (Phase 4, ba77510) |

## Formative commits

| SHA | Date | Subject | Why it matters |
|---|---|---|---|
| `039110c` | 2026-04-16 | Added Phase 0 for posterity. | Baseline marker for the #80 bdstore→mcpstore swap. |
| `d020a5e` | 2026-04-15 | fix: remove os.Getenv from library-layer code (Phase 1, #98) (#108) | Founded the env-isolation invariant that issuestore inherits. |
| `6e39b09` | 2026-04-08 | Phase 1 — interface, adapters, contract test, CI/lint scaffolding | Created the neutral Store interface and initial contract. |
| `045c1e1` | 2026-04-08 | migrates internal/mail/ — the first consumer — off internal/mail/bd.go onto issuestore.Store | First production consumer; C-1 bug fix (`Read: Status=="closed"` → `IsTerminal()`). |
| `e41342d` | 2026-04-09 | remaining text-scraping command entry points now read typed issues from issuestore.Store | Ends all `bd show` output parsing; Gotcha #22 pinned (ctx flows through every migrated signature). |
| `96e5d7e` | 2026-04-15 | Phase 0, #80 — bdstore baseline latency benchmark (PR #114) | Source of `phase0Baseline` p50/p95 map pinned in `benchmark_test.go:24-37`; file itself deleted in Phase 7 but numbers survive in commit. |
| `1a117c2` | 2026-04-15 | Phase 1, #80 — Notes and CloseReason fields on Issue (PR #115) | Dedicated columns (C-10); ends Description mutation by Patch/Close. |
| `f6eb6ae` | 2026-04-15 | Phase 2, #80 — 9 new RunStoreContract sub-tests (PR #116) | Expanded the cross-adapter contract to pin ordering, dep-filter, orphan-deps, Patch/Close column semantics, label order, Render parity, Actor_scoping. |
| `f959234` | 2026-04-16 | Phase 3 — memstore Patch/Close write Notes/CloseReason; actor scoping moves to Store.actor field via NewWithActor | Fixed PR #116's `os.Setenv` defect by threading actor through the factory; broadened env_hermetic_test.go. |
| `ba77510` | 2026-04-16 | Phase 4 — Python MCP server (aiohttp + SQLAlchemy + SQLite) | Created `py/issuestore/`; 9 JSON-RPC tools, 4-table schema, WAL+FK PRAGMAs, loopback-only, SIGTERM-clean. |
| `ef0411c` | 2026-04-16 | Phase 5 — mcpstore Go adapter over HTTP JSON-RPC to Phase 4 server | Lazy start + PID+health liveness + crash recovery + start-race via `internal/lock.NewWithPath`. Client-side actor default via constructor — no library-layer env reads (#98). |
| `c93f9ef` | 2026-04-16 | Phase 6 — wire mcpstore into production seams | `newIssueStore` returns mcpstore, takes actor as third arg; `storeForMail` returns `issuestore.Store`; `install.go` runs `checkPython312` before any mutation. |
| `7acd617` | 2026-04-17 | Phase 7 — delete internal/issuestore/bdstore/ (9 files, 2097 lines) | Strips last BD_ACTOR fallbacks from seam call sites; scrubs bd/bdstore references from contract.go, store.go, doc.go, memstore, mail. mcpstore is sole production adapter. |
| `fc4f703` | 2026-04-17 | Phase 8 — CI/build/docs cleanup | Drops C19 lint rule and bd build step; CI/Dockerfile install Python 3.12 + `py/requirements.txt`. |
| `c6431e2` | 2026-04-17 | Phase 9 — concurrency and latency benchmarks | 8×200 ops mix (AC-11); p50<20ms Get/Create, p50<50ms Ready (AC-12). |
| `5b07735` | 2026-04-17 | Phase 10 — migrate integration tests off bdstore/bd | done/install/integration/worktree tests use mcpstore via newIssueStore seam; adds `lifecycle_test.go` (crash recovery + SIGTERM shutdown). Makefile builds venv. |
| `63307bb` | 2026-04-18 | RBAC-weakening design rejection | Deleted a design proposal that would have bypassed the default actor overlay at the mcpstore seam; the canonical opt-out is `IncludeAllAgents: true` (`mail/mailbox.go:47`, `done.go:100,136`). |

## Load-bearing invariants (this subsystem's contribution)

1. **Default actor overlay (Gate-4)**: when `store.actor != ""` AND `Filter.IncludeAllAgents=false` AND `Filter.Assignee == ""`, `List`/`Ready`/`RenderList` inject `assignee=actor` on the wire. Cited at `mcpstore.go:209-214` (single seam for all three list-like methods); memstore mirrors at `memstore.go:165-170`. Pinned across adapters by `Actor_scoping` sub-test (`contract.go:821-854`). Rejection precedent: 63307bb.
2. **Status set is fixed (D11/C-1)**: 6 values, wire format pinned by `store_test.go:108-125`. `IsTerminal()` is the single "done?" gate — mail's read/unread predicate routes through it (fixed in 045c1e1).
3. **Library MUST NOT read env (#98)**: `internal/issuestore/` has ZERO `os.Getenv` calls (verified). Actor flows in via `New(factoryRoot, actor)` constructor (`mcpstore.go:33`) and `NewWithActor(actor)` (`memstore.go:42`). Foundation commit d020a5e; regression scan in `env_hermetic_test.go` broadened in f959234.
4. **Loopback-only MCP server bind (R-SEC-1)**: `py/issuestore/server.py:267-268` literal `host="127.0.0.1"`; explicit comment forbids `0.0.0.0`/`"localhost"`. Adapter package doc re-asserts at `mcpstore.go:7`.
5. **Atomic endpoint-file publish (H-4/D15)**: `py/issuestore/server.py:228-231` writes `<path>.tmp` then `os.replace` to the real path — never a partial-read window for discoverers. SIGTERM handler also removes the file (`server.py:234-239`, verified by `TestSIGTERMCleanShutdown`).
6. **Labels preserve insertion order with no dedup (C13)**: `store.go:40` doc; mcpstore sends slice as-is (`mcpstore.go:104-108`); memstore copies via `append([]string(nil), p.Labels...)` (`memstore.go:68`). Pinned strictly by `Labels_preserve_insertion_order` (`contract.go:743-771`) and loosely by the set-equality `Labels_round_trip` (`contract.go:123-153`).
7. **Patch/Close never mutate Description (C-10)**: dedicated `Notes`/`CloseReason` columns. mcpstore enforces by never sending `description` from `Patch`/`Close` (`mcpstore.go:117-126`, `mcpstore.go:129-132`). Pinned by `Patch_notes_populates_Notes_field` and `Close_reason_populates_CloseReason_field` (`contract.go:661-741`).
8. **`Statuses=nil` means "all non-terminal", NOT "all" (H-A R2, D14, Gotcha 12)**: empty/nil slice selects open+hooked+pinned+in_progress; adapters must NOT collapse to zero-filter. Pinned by `Filter_nil_Statuses_returns_all_non_terminal` (`contract.go:229-250`).

## Cross-referenced idioms

### `IncludeAllAgents: true` — the canonical Gate-4 opt-out

When a caller needs to bypass the default actor overlay, the established idiom is `Filter.IncludeAllAgents = true`. The recent 63307bb rejection explicitly pins this as THE canonical pattern; alternative approaches (e.g. bypassing the overlay at the adapter seam) are rejected prior art.

**Call sites, grouped by subsystem:**

Mail — explicit Assignee already scopes the query, so the actor overlay would double-scope and reject every non-actor message:
- `internal/mail/mailbox.go:47` (`listFilter` — inbox query; comment cites Gotcha #3 and explains that without it `memstore.matchesFilter:159` would reject every non-BD_ACTOR assignee).

Sling formula step discovery — sling creates step beads with NO Assignee; actor-scoped Lists would return zero and misfire WORK_DONE one step in:
- `internal/cmd/done.go:100` (runDoneCore — check for open children when Ready is empty; prevents premature "formula complete").
- `internal/cmd/done.go:136` (runDoneCore — completion check after closing a step).
- `internal/cmd/done.go:369` (`countAllChildren` — total step count for WORK_DONE mail body).
- `internal/cmd/step.go:139` (step command — empty-ready branch distinguishes "all_complete" vs "blocked").
- `internal/cmd/prime.go:422` (prime — formula-context line "Step N of M").

Done-close cascade regression tests:
- `internal/cmd/done_integration_test.go:109` (asserts the shape of post-fix `List` call — regression catcher for the BD_ACTOR filter bug).
- `internal/cmd/step_test.go:478-479` (`TestStepCurrent_IncludeAllAgentsRequired` — C14 pin; mechanically asserts `List` receives `IncludeAllAgents: true`).

bead CLI — administrative `--all` flag (D13 splits bd's overloaded `--all`):
- `internal/cmd/bead.go:294-295` (`--all` sets both `IncludeAllAgents` AND `IncludeClosed`).

Integration tests and benchmarks (test-level cross-agent visibility):
- `internal/cmd/integration_test.go:286` (cross-agent listing in test setup).
- `internal/issuestore/mcpstore/benchmark_test.go:188, 221, 272` (benchmark warmup/measurement for List and RenderList — avoids actor-scoping skew).
- `internal/issuestore/mcpstore/concurrency_test.go:61, 188, 205` (concurrency test warmup, per-goroutine Ready, and dep-graph consistency check at the end — comment at L203-205 explicitly notes "so actor scoping doesn't hide the dependent for an orthogonal reason").

Contract suite:
- `internal/issuestore/contract.go:233, 258, 287, 311, 328, 488, 517, 558, 573, 615` (all multi-agent fixture setups across the contract sub-tests).

### Constructor-injected Store
The `Store` interface is passed in explicitly to consumers (`NewMailbox(identity, store)` at `mail/mailbox.go:28`; `runDoneCore(ctx, workDir, ...)` constructs `store` before calling helpers). No global state, no package-level singleton, no env reads below the cmd/ boundary.

### `NewWithActor(actor)` mirrored by `New(factoryRoot, actor)`
Both adapters take actor as an explicit constructor parameter (`memstore.go:42`, `mcpstore.go:33`). `""` disables Gate-4 scoping.

## Formal constraint tags referenced in this subsystem

| Tag | Where cited | Meaning |
|---|---|---|
| C-1 | `store.go:95`, `store_test.go:10`, `doc.go:18`, `memstore.go:338`, `contract.go:22,48` | closed AND done are terminal; fixed the mail Read-predicate regression in 045c1e1. |
| C-10 | `mcpstore.go:118`, `store_test.go:81` | Patch/Close never mutate Description; dedicated columns. |
| C13 | `store.go:40`, `memstore_test.go:93`, `contract.go:63,126,150` | Labels preserve wire order, no dedup, no canonicalization. |
| C14 | `step_test.go:470` | `step.go` must pass `IncludeAllAgents: true` to store.List; mechanical regression pin. |
| C18 | `store.go:12` | SIGTERM cleanliness — ctx flows through every JSON-RPC call. |
| C8 | `mailbox.go` (referenced) | af CLI surface unchanged; mail must filter `[StatusOpen]` explicitly, not nil. |
| D6 / R-INT-6 | `store.go:59` | `IssueRef.BlockedBy` populated from underlying blocked-by relation. |
| D11 | `store.go:77`, `doc.go:17`, `contract.go:22,48`, `memstore.go:338` | Fixed 6-status set. |
| D13 / H-2 | `store.go:146`, `bead.go:292-295` | `IncludeAllAgents` and `IncludeClosed` split bd's overloaded `--all` into two axes. |
| D14 / H-3 | `store.go:136`, `contract.go:55` | `Statuses` OR semantics, nil ≠ empty. |
| D15 / H-4 | — | Atomic-write invariant. Endpoint file publish uses `tmp + os.replace` (`server.py:228-231`). H-4 is also cited in `done.go` for its "no fallback when caller file missing" rule. |
| H-A R2 | `store.go:136`, `mailbox.go:32-36`, `contract.go:51-52` | nil-Statuses → all non-terminal; critical for mail's C8 preservation. |
| Gate-4 | `mcpstore.go:31,55,179,207` | Default actor overlay for `List`/`Ready`/`RenderList` at `mcpstore.go:209-214`. |
| R-API-2 | `step.go:147-149` (cross-reference) | `result.Steps[0]` is "next ready step" convention. |
| R-API-5 | `doc.go:32` | Render output is for display only — DO NOT PARSE. |
| R-INT-9 | `step.go:130-135` | memstore's empty-assignee carve-out (`memstore.go:168`) masks the historical BD_ACTOR divergence; motivates running the regression test against mcpstore. |
| R-SEC-1 | `mcpstore.go:7`, `server.py:3,267` | Loopback-only bind (127.0.0.1). |
| AC-8 (a/b) | `lifecycle_test.go:86-205` | Crash recovery + SIGTERM clean shutdown. |
| AC-11 | `concurrency_test.go:22,40` | ~30/25/15/30 Create/Patch/Close/Ready mix. |
| AC-12 | `benchmark_test.go:23,44-49,277-306` | p50<20ms Get/Create, p50<50ms Ready; no regression vs Phase 0 bdstore baseline. |
| Gotcha 3 | `mailbox.go:47` | mail's explicit-Assignee + `IncludeAllAgents:true` pairing. |
| Gotcha 9 | `mcpstore.go:169`, `memstore.go:338`, `contract.go:22,51` | SetStatus must seed all 6 Status values. |
| Gotcha 11 | `store.go:173`, `mcpstore.go:117` | Patch today is Notes-only; future fields additive. |
| Gotcha 12 | `store.go:136`, `contract.go:51` | Adapters must treat `Statuses=nil` as "all non-terminal", not empty. |
| Gotcha 22 | — (e41342d commit message) | ctx flows from cobra through every migrated signature. |

## Gaps — unknown, needs review

- **`BD_ACTOR` env var lingering in `internal/session/session.go:117,159` and `session_test.go:40-41,194` after bdstore removal**: session manager still exports `BD_ACTOR` into every spawned tmux session alongside `AF_ROLE`/`AF_ROOT`/`BEADS_DIR`. With bdstore deleted, the in-tree `mcpstore` does not read `BD_ACTOR` — actor flows in via the Go constructor. Callers (`done.go:74`, `step.go:111`, `prime.go:365,527`, `bead.go:109`, `handoff.go:135,178`) still read `os.Getenv("BD_ACTOR")` at the cobra-command boundary to pass into `newIssueStore(cwd, beadsDir, actor)`. Whether `BD_ACTOR` remains a load-bearing **external** contract (e.g. for operators inspecting tmux sessions, or for an out-of-tree adapter) vs. a vestigial name now that bd is gone — **unknown, needs review**. `mcp_server_problem.md:71` states "No agent-facing changes. … session env vars (BEADS_DIR, BD_ACTOR stay)" — so the intent was to preserve the external surface, but the source-of-truth (a documented contract) is not anchored in code comments.
- **`BEADS_DIR` similarly**: exported by `session.go:159`, no reader in `internal/issuestore/`. Same question class as `BD_ACTOR`. **unknown, needs review**.
- **`py/issuestore/store.py` and `schema.py` not read in this pass**: the Go-side wire contract is anchored, but the Python-side implementation of the 9 tools (field names, null handling, dep-graph query) was not cross-checked. The contract suite covers behavior end-to-end via `TestMCPStoreContract`, so adapter drift would fail the suite, but static reading of the Python code was deferred. **unknown, needs review**.
- **4-table schema**: Phase 4 commit (ba77510) claims a "4-table schema with WAL/foreign-keys PRAGMAs". The tables are not enumerated in the Go side; would need `py/issuestore/schema.py` reading. **unknown, needs review**.
- **Loopback-bind is enforced by the Python server only**: the Go adapter trusts `.runtime/mcp_server.json` contents at `lifecycle.go:19-24,168-181` without verifying the address literally begins with `127.0.0.1:`. A caller or test harness that wrote a non-loopback address into the endpoint file would be followed. In practice `server.py:224,268` hard-codes `127.0.0.1`, so this is a defense-in-depth gap, not an active vulnerability. **unknown — needs review** whether a Go-side assertion is warranted.
