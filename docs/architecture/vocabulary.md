# Vocabulary

Canonical terms used in agentfactory's code, docs, and design. Every
entry cites the exact `file:line` where the term is defined in code.

## Purpose

Prose paraphrases introduce interpretation surface that the code
symbols themselves don't have. When a doc says "terminal prior
formula" instead of "prior formula whose Status is `closed` or `done`",
it invites the collision that produced the
[ADR-014 near-miss](adrs/ADR-014-no-interactive-prompting.md). This
document is the mechanical anchor: **if a term isn't in this list,
don't abstract it — use the explicit code symbol or the explicit
enumeration**.

## How to use this doc

- **Writing a design or plan**: before introducing an adjective that
  summarizes multiple Status values (or any other enum), check this
  list. If the summary adjective isn't present, use the explicit
  enumeration instead.
- **Reviewing a design or plan**: grep the proposal for any of the
  terms under "Banned paraphrases" — each hit is a drift signal.
- **Updating this doc**: every entry MUST carry a `file:line` anchor
  or the literal phrase `unknown — needs review`. Added terms without
  anchors will fail the next `/architecture-docs` validation pass.

---

## Entity types

Every entity term in code. Prose should use these exact names; do not
substitute "record", "object", "item".

| Term | Definition | Anchor |
|------|------------|--------|
| `Store` | Neutral interface every issue-store backend implements | `internal/issuestore/store.go:20-36` |
| `Issue` | Neutral DTO every `Store` backend produces | `internal/issuestore/store.go:41-56` |
| `IssueRef` | Thin reference used in `BlockedBy` edges | `internal/issuestore/store.go:60-62` |
| `CreateParams` | Input struct for `Store.Create` | `internal/issuestore/store.go:172-182` |
| `Filter` | Query struct for `Store.List` / `Store.Ready` | `internal/issuestore/store.go:147-169` |
| `Patch` | Input struct for `Store.Patch` (partial update) | `internal/issuestore/store.go:186-188` |
| `ReadyResult` | Structured output of `Store.Ready` | `internal/issuestore/store.go:191-195` |
| `Formula` | Parsed `formula.toml` root | `internal/formula/types.go:29-55` |
| `Step` | Sequential unit in a workflow formula | `internal/formula/types.go:107-114` |
| `Leg` | Parallel unit in a convoy formula | `internal/formula/types.go:82-88` |
| `Template` | Step template in an expansion formula | `internal/formula/types.go:117-123` |
| `Aspect` | Parallel analysis unit in an aspect formula | `internal/formula/types.go:58-63` |
| `Synthesis` | Combines leg outputs in a convoy formula | `internal/formula/types.go:91-95` |
| `Gate` | Blocking gate on a formula step | `internal/formula/types.go:99-104` |
| `Input` | Formula-level input parameter declaration | `internal/formula/types.go:66-72` |
| `Output` | Formula-level output placement config | `internal/formula/types.go:75-79` |
| `Var` | Variable declaration inside a workflow formula | `internal/formula/types.go:126-138` |
| `Checkpoint` | Session recovery state for an agent | `internal/checkpoint/checkpoint.go:25-55` |
| `Message` | Inter-agent mail message | `internal/mail/types.go:22-34` |
| `Mailbox` | Per-agent mail container | `internal/mail/mailbox.go:21` |
| `AgentConfig` | Contents of `agents.json` | `internal/config/config.go:26-28` |
| `AgentEntry` | Single agent's declaration inside `agents.json` | `internal/config/config.go:31-36` |
| `MessagingConfig` | Contents of `messaging.json` (group definitions) | `internal/config/config.go:48-50` |
| `FactoryConfig` | Contents of `factory.json` (root marker) | `internal/config/config.go:53-57` |
| `DispatchConfig` | Contents of `dispatch.json` | `internal/config/dispatch.go:10` |

## Enumerated values

When referring to these values in prose, use the **exact enum
identifier** or the **exact lowercase string**. Do not paraphrase.

### `Status` (Issue lifecycle) — `internal/issuestore/store.go:81-88`

| Go symbol | Wire value |
|-----------|------------|
| `StatusOpen` | `"open"` |
| `StatusHooked` | `"hooked"` |
| `StatusPinned` | `"pinned"` |
| `StatusInProgress` | `"in_progress"` |
| `StatusClosed` | `"closed"` |
| `StatusDone` | `"done"` |

**`IsTerminal()` gate** — `internal/issuestore/store.go:100-102` —
returns `true` iff the Status is `closed` or `done`. This is a code
symbol and may be used as-is in prose (e.g., "the call sites that
gate on `IsTerminal()`"). It must **not** be nominalized into the
adjective "terminal" outside this single symbol — see Banned
paraphrases.

### `IssueType` — `internal/issuestore/store.go:67-73`

| Go symbol | Wire value |
|-----------|------------|
| `TypeTask` | `"task"` |
| `TypeEpic` | `"epic"` |
| `TypeBug` | `"bug"` |
| `TypeFeature` | `"feature"` |
| `TypeGate` | `"gate"` |

### `Priority` — `internal/issuestore/store.go:108-113`

| Go symbol | Int value | String form |
|-----------|-----------|-------------|
| `PriorityUrgent` | `0` | `"urgent"` |
| `PriorityHigh` | `1` | `"high"` |
| `PriorityNormal` | `2` | `"normal"` |
| `PriorityLow` | `3` | `"low"` |

### `FormulaType` — `internal/formula/types.go:19-25`

| Go symbol | Wire value |
|-----------|------------|
| `TypeConvoy` | `"convoy"` |
| `TypeWorkflow` | `"workflow"` |
| `TypeExpansion` | `"expansion"` |
| `TypeAspect` | `"aspect"` |

### `MessageType` — `internal/mail/types.go:15-19`

| Go symbol | Wire value |
|-----------|------------|
| `TypeTask` | `"task"` |
| `TypeNotification` | `"notification"` |
| `TypeReply` | `"reply"` |

### `Var.Source` — `internal/formula/types.go:130-137`

| Wire value | Meaning |
|------------|---------|
| `"cli"` | From `--var key=val` command line |
| `"env"` | From environment variable |
| `"literal"` | Hardcoded in TOML (uses `Default`) |
| `"hook_bead"` | Read from the hooked bead |
| `"bead_title"` | Title of the hooked bead |
| `"bead_description"` | Description of the hooked bead |

## Identity terms

The system has three distinct identity concepts. Conflating them in
prose is a common drift source; use the exact term for the exact
thing.

| Term | What it is | Where it lives |
|------|-----------|----------------|
| **Agent** | Named role declared in `agents.json` (e.g., `manager`, `factoryworker`). String identifier. | `internal/config/config.go:31-36` (`AgentEntry`) |
| **Actor** | Identity the issue store's actor-overlay scopes by. Set from the `BD_ACTOR` env var at session start. | `internal/session/session.go:117` (export); `internal/issuestore/store.go:180` (`CreateParams.Actor`) |
| **Assignee** | Explicit owner field on an `Issue` / `CreateParams`. String. When non-empty, suppresses the actor overlay. | `internal/issuestore/store.go:45` (`Issue.Assignee`), `L175` (`CreateParams.Assignee`), `L151-158` (`Filter.Assignee` semantics) |

An agent named `manager` runs with `BD_ACTOR=manager` (making its
actor `"manager"`) and its beads carry `Assignee: "manager"` by
default. The three values can diverge — the ADR-002 idiom
`IncludeAllAgents: true` and the `Filter.Assignee`-explicit path
(pinned by `RunStoreContract.ExplicitAssigneeWinsOverActorOverlay`,
issue #125) exist specifically because they can.

## Operator-facing aliases

These terms are used in CLI output, help text, and
`USING_AGENTFACTORY.md`, but map to a different Go type name. Both
names are acceptable in their respective contexts; **do not mix them
in the same paragraph**.

| Operator-facing | Code symbol | Anchor |
|-----------------|-------------|--------|
| `bead` (CLI: `af bead`, prose: "hooked bead") | `Issue` | `internal/cmd/bead.go:17-21` (CLI cobra group) vs `internal/issuestore/store.go:41-56` (type) |
| `beads dir` / `.beads/` | Store backend filesystem location | `internal/session/session.go:159` (export of `BEADS_DIR`) |
| `hook` / `hooked bead` | `Issue` whose `Status == StatusHooked` | `internal/checkpoint/checkpoint.go:44-45` (`HookedBead` field) |

## Banned paraphrases

These words have appeared in recent designs/docs and produced drift.
Any doc review that finds one of these outside its narrow allowed use
should flag it.

| Banned | Replace with | Why banned |
|--------|--------------|------------|
| "terminal" (as prose adjective for a status or bead, e.g. "terminal prior formula") | Explicit: `closed` or `done`. Or use the code symbol `IsTerminal()` verbatim with backticks. | Collides with "terminal" meaning TTY. Direct cause of the ADR-014 near-miss at `.designs/issue-126-three-way-disagreement/design-doc.md:147` and `L262`. |
| "non-terminal" (as prose adjective) | Explicit: `open`, `hooked`, `pinned`, `in_progress`, or the enumeration `{open, hooked, pinned, in_progress}`. | Same collision. Additionally, the enumeration is clearer about which of the four non-`IsTerminal()` values applies. |
| "final", "finished", "completed" (as adjectives for Status) | `closed` or `done` — specify which. | Imprecise: doesn't distinguish `closed` (manually closed) from `done` (workflow-completed). These are two different wire values with different consequences in `IsTerminal()` consumers. |
| "active" (as adjective for Status) | `open`, `in_progress`, or the enumeration `{open, hooked, pinned, in_progress}`. | Ambiguous: "active" could mean "not closed", "currently assigned", or "being worked". The status set is the precise answer. |
| "pending", "waiting" | `open` (if not yet started) or `hooked` / `pinned` (if gated). | Imprecise and doesn't map to a single Status. |
| "task" (as prose noun for a step or bead) | `Step`, `Leg`, `Template`, `Aspect`, or `Issue` — pick the one the code uses at the call site. | `TypeTask` is an `IssueType` value. Using "task" as a generic English word creates ambiguity with the enum. |
| "owner" | `Assignee` — a specific field, not a concept. | "Owner" doesn't distinguish `Assignee` from `Actor`. The distinction matters (see Identity terms). |
| "user" | `operator` (human) or name the specific agent/actor. | agentfactory has no "user" concept at runtime. Using "user" imports a human-in-the-loop assumption — the same failure class as ADR-014. |
| "prompt" / "prompts the user" | There is none at runtime. Remove the concept. | Forbidden by ADR-014. If the prose describes existing runtime code that prompts, that code is itself a violation. |

## How this doc is maintained

- **Idempotent**: re-running `/architecture-docs` on an unchanged
  tree must leave this file byte-identical (Core Principle 5).
- **Code-anchored**: every entry MUST have a `file:line` citation or
  the literal phrase `unknown — needs review`.
- **Additive on new entities**: when a new exported type is added
  under `internal/` or a new enum value is added to one of the above
  enums, an entry MUST be added here in the same PR.
- **Subtractive on removals**: when an entity or enum value is
  removed, the corresponding entry MUST be removed or marked
  `unknown — needs review` in the same PR.
- **Banned paraphrases list grows with incidents**: each time a
  design/plan review catches a new collision-prone paraphrase, add
  a row to the Banned paraphrases table with the incident citation.

## Out of scope

- External-library terms (`cobra.Command`, `sqlalchemy.Table`,
  `sqlite3`) — not agentfactory vocabulary.
- Implementation-internal identifiers that don't appear in prose or
  at package boundaries (unexported helper names, local variables).
- Test-helper names unless they define a contract (e.g.,
  `RunStoreContract` at `internal/issuestore/contract.go:66` IS in
  scope — but covered under the subsystem doc, not here).
- Shell-script-only terms from bootstrap scripts (`quickdocker.sh`),
  per the ADR-014 scope carve-out for bootstrap.

## Known gaps

Entries deliberately skipped on this pass because their anchors were
not re-verified against code at write-time. Each is a candidate for
the next pass.

| Term | Reason skipped | Re-verification required |
|------|----------------|--------------------------|
| `Session` / `session` | Referenced throughout `internal/session/` and in checkpoint / tmux code, but the exact canonical type (if any) and its `file:line` were not read before this doc was written. Prose uses "session" heavily (e.g., "agent session", "tmux session") and both meanings need to be pinned or banned. | Read `internal/session/session.go` for the canonical type; read `internal/tmux/tmux.go` for the "tmux session" usage; decide whether one term covers both or they need separate entries. |
| `worktree` | A subsystem (`internal/worktree/`) and an operator-facing concept. Not a single code type. May belong in the subsystem doc, not here. | Read `internal/worktree/*.go` for any canonical type; decide scope. |
| `dispatch` | Used as a verb (`dispatchToSpecialist`), a config name (`dispatch.json` → `DispatchConfig`), and a concept ("dispatch chain"). Multiple meanings — needs disambiguation before adding. | Read `internal/cmd/sling.go` dispatch path and `internal/config/dispatch.go` to enumerate the meanings; add either one disambiguated row or multiple rows. |
| `hook` / `hooks` | `Status == StatusHooked` is one meaning; the `hooks/` directory with shell-script quality gates is another; cobra "hooks" inside cobra commands is a third. Three meanings, no current anchor disambiguating them. | Read `hooks/` contents and grep for `PreRun` / `PostRun` hook uses in cobra wiring. |
