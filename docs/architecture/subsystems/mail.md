# mail subsystem

**Covers:** internal/mail

## Shape

The mail subsystem implements inter-agent messaging as a thin translation layer
over `issuestore.Store`: a mail message is a typed issue with `mail:true` and
`from:/to:/thread:/msg-type:` labels, stored through the same Store the rest of
the system uses (`internal/mail/mailbox.go:17-24`, `internal/mail/translate.go:36-46`).
There is no bespoke persistence — send is `store.Create`, list is `store.List`
with a pinned `Filter`, read-state derives from `iss.Status.IsTerminal()`,
and mark-read is `store.Close` (`internal/mail/mailbox.go:52-95`). The Store is
injected at construction time rather than discovered from config, which means
tests swap in `memstore.New()` directly into `NewMailbox`/`NewRouter`
(`internal/mail/mailbox_test.go:14-15`, comment at `internal/cmd/mail.go:408-411`).
Routing adds group fan-out via `msgCfg.Groups` and best-effort tmux
notification banners to the recipient's session (`internal/mail/router.go:77-99`,
`internal/mail/router.go:120-131`).

## Public surface

- `Mailbox` — per-agent inbox facade; `NewMailbox(identity, store)` at `mailbox.go:28`.
- `Mailbox.List / Get / MarkRead / Delete / Count` — inbox ops at `mailbox.go:52,68,81,93,98`.
- `Router` — send / fan-out / tmux-notify; `NewRouter(workDir, store)` at `router.go:25`.
- `Router.Send` — dispatcher; group prefix `@` branches to `sendToGroup` (`router.go:53-58`).
- `Router.ResolveGroupAddress` — resolves `@all` (dynamic from agentsCfg) and named groups from messagingCfg (`router.go:102-118`).
- `Message` struct — 11 fields, JSON tagged (`types.go:22-34`).
- `MessageType` constants — `TypeTask`, `TypeNotification`, `TypeReply` (`types.go:15-19`).
- `NewMessage` / `NewReplyMessage` — constructors; reply inherits `ThreadID` and sets `ReplyTo` (`types.go:37-67`).
- `ParsePriority` / `ParseMessageType` — string → enum with `Normal`/`Notification` defaults (`types.go:82-109`).
- `ErrMessageNotFound`, `ErrEmptyInbox` — sentinel errors (`mailbox.go:12-15`).

## Semantic mapping (translate.go)

`issueToMessage` (`translate.go:16-31`) maps an `issuestore.Issue` to a
`mail.Message`. The load-bearing line is `Read: iss.Status.IsTerminal()` at
`translate.go:25`. The header comment at `translate.go:12-15` states the rule
verbatim:

> C-1 (D11): Read is set from Status.IsTerminal(), NOT from a sentinel
> comparison like `== StatusClosed`. Without this, mail in `done`,
> `in_progress`, `hooked`, or `pinned` status silently re-surfaces as
> unread — the R-DATA-3 high-severity risk.

The pre-fix form lived at `internal/mail/types.go:195` in the predecessor
`BeadsMessage` and compared `bm.Status == "closed"` — recorded in commit
`045c1e1` (2026-04-08): "The C-1 bug in `internal/mail/types.go:195`
(`Read: bm.Status == 'closed'` — which silently re-surfaces mail in `done`,
`in_progress`, `hooked`, and `pinned` as unread) is fixed by routing through
`iss.Status.IsTerminal()`." The terminal predicate itself is owned by
`issuestore.Status.IsTerminal()` at `internal/issuestore/store.go:100-102`,
whose doc-comment at `store.go:93-95` explicitly reserves this method for
mail's read/unread translation.

`messageToCreateParams` (`translate.go:36-46`) encapsulates the wire format so
the router no longer knows about bd flags; it sets `Actor: from` so the store's
actor overlay records the sender correctly.

`buildLabels` (`translate.go:80-92`) emits labels in the exact wire order
`mail:true, from:<from>, to:<to>, thread:<thread>, msg-type:<type>` with
optional trailing `reply-to:<id>`. The comment at `translate.go:74-79` warns:

> Set-equality round-trip is required to keep R-INT-1 green; bd sorts labels
> alphabetically at storage, so wire-order preservation is tested against
> buildLabels' output, not against bd's stored form.

`parseLabels` (`translate.go:53-72`) consumes `from:/thread:/reply-to:/msg-type:`
and ignores `mail:true` + `to:<x>` which are structural, not semantic.

## Seams

| Seam | Direction | Contract | Anchor |
|------|-----------|----------|--------|
| `internal/issuestore.Store` | OUT | `List(ctx, Filter) / Get / Create / Close` — mail never calls bd directly | `mailbox.go:9,23,53,69,82`; `router.go:9,18,63` |
| `internal/issuestore.Filter` | OUT | explicit `Statuses=[StatusOpen]`, `IncludeAllAgents=true`, `Assignee`, `Labels=["mail:true"]`, `Type=TypeTask` | `mailbox.go:41-49` |
| `internal/issuestore.Status.IsTerminal` | OUT | mail's Read predicate is delegated here, not implemented locally | `translate.go:25`; `internal/issuestore/store.go:100-102` |
| `internal/config` | OUT | `FindFactoryRoot`, `AgentsConfigPath`, `LoadAgentConfig`, `MessagingConfigPath`, `LoadMessagingConfig` | `router.go:26,31-38` |
| `internal/session.SessionName` | OUT | recipient tmux name resolution (`af-<agent>`) | `router.go:124`; `internal/session/names.go:10` |
| `internal/tmux.Tmux.HasSession + SendNotificationBanner` | OUT | best-effort banner send; silent skip if no session | `router.go:125-130`; `internal/tmux/tmux.go:100,205` |
| `internal/cmd/mail.go` | IN | per-command entry: `runMailSend` builds `Mailbox`/`Router` via `newMailboxForSender` + `storeForMail`; injected Store is the only production adapter seam | `internal/cmd/mail.go:116,134,368,422-428` |
| `internal/cmd/done.go` | IN | `sendWorkDoneMail` shells out to `af mail send` (not an in-process call) | `internal/cmd/done.go:247-265` |

## Formative commits

| SHA | Date | Significance |
|-----|------|--------------|
| `fd13e73` | 2026-03-23 | Phase 2 Mail System — original messaging, routing, CLI commands |
| `32af131` | 2026-03-23 | `af up` adds tmux session naming so recipients can actually receive mail notifications |
| `b2bfe5a` | 2026-03-23 | Phase 6 E2E validation — integration test pinning mail delivery |
| `0f932b3` | 2026-03-27 | Added `to:<recipient>` label to mail write path |
| `e441c2b` | 2026-03-31 | Trailing-slash normalization (`addressToIdentity`/`identityToAddress` at `types.go:111-121`), GH issue 30 |
| `c21f270` | 2026-04-07 | Replaced hardcoded path constructions with `config.*Path` helpers (upstream of `router.go:31,37`) |
| `045c1e1` | 2026-04-08 | C-1 fix: replaces `bm.Status == "closed"` with `iss.Status.IsTerminal()`; introduces `translate.go`; replaces `BeadsMessage` with Store-injected `Mailbox`/`Router`; `mail.Priority` becomes type alias of `issuestore.Priority` |
| `e41342d` | 2026-04-09 | Deletes `mail.Priority` alias (Phase 2 deprecation complete); mail no longer parses bd output |
| `7acd617` | 2026-04-17 | Phase 7 — deletes `internal/issuestore/bdstore/` entirely; mcpstore is the sole production adapter behind `storeForMail`; strips last BD_ACTOR fallbacks and bd references from mail |

## Load-bearing invariants

- **`mailbox.go:listFilter()` is an own-mailbox read scoped by an explicit
  `Assignee: identityToAddress(m.identity)`; `IncludeAllAgents` is intentionally
  NOT set.** Both adapters agree that an explicit `Assignee` suppresses the
  default actor overlay, so the explicit `Assignee` suffices on its own. The
  cross-adapter invariant is pinned by
  `RunStoreContract.ExplicitAssigneeWinsOverActorOverlay` in
  `internal/issuestore/contract.go`. Pre-#125, this call site carried
  `IncludeAllAgents: true` as a memstore-divergence workaround — memstore's
  `matchesFilter` rejected every non-actor assignee even when the caller
  supplied an explicit one, so the overlay opt-out was needed to prevent the
  own-mailbox query from returning nothing. Phase 1 of #125 added a guard in
  `internal/issuestore/memstore/memstore.go` so the memstore switch matches
  mcpstore's, and Phase 2 deleted the now-unnecessary `IncludeAllAgents: true`
  from this filter.

- **`mailbox.go:32-36`: `Statuses: []Status{StatusOpen}` must be an explicit
  single-element slice, not nil.** The comment at `:32` (verbatim):

  > Statuses is an explicit single-element slice []Status{StatusOpen}, NOT nil.
  > The nil semantics ("all non-terminal") would surface hooked/pinned/
  > in_progress mail in `af mail inbox` and violate C8 (af CLI surface
  > unchanged). See outline Gotcha #4 and the H-A R2 pin in cross-review.

  The nil-semantic is defined at `internal/issuestore/store.go:137-143`: nil
  means "all non-terminal statuses (open, hooked, pinned, in_progress)".

- **`translate.go:25`: Read derives from `Status.IsTerminal()`, not a sentinel.**
  The pre-fix code compared against `StatusClosed` only and silently re-surfaced
  mail in `done`/`in_progress`/`hooked`/`pinned` as unread (commit `045c1e1`,
  `translate.go:12-15`, test at `translate_test.go:12-47`).

- **`router.go:67-73`: self-mail is NOT guarded.** A Stop Hook uses `af mail
  send` to self for efficiency, so self-sends still trigger `notifyRecipient`.
  The guard is commented out with explicit rationale; recursion prevention is
  pushed out to the LLM.

- **`router.go:85-86`: group fan-out skips the sender.** Inside `sendToGroup`,
  members matching `msg.From` are skipped to prevent self-loops on `@all` and
  named groups.

## Cross-referenced idioms

- **`IncludeAllAgents` opt-out idiom — mail does NOT use it.** The mailbox
  list filter (`mailbox.go:listFilter()`) sets an explicit
  `Assignee: identityToAddress(m.identity)` and nothing else on the overlay
  axis; the explicit `Assignee` alone is sufficient for an own-scoped read on
  every adapter, pinned by
  `RunStoreContract.ExplicitAssigneeWinsOverActorOverlay` in
  `internal/issuestore/contract.go`. An `Assignee` +
  `IncludeAllAgents: true` co-occurrence at any new call site is now a defect
  signal, not a prior-art shape: it must carry an explicit cross-actor
  justification in the preceding comment (e.g. step-discovery, done-cascade,
  operator `--all`), because the explicit `Assignee` no longer requires the
  overlay opt-out to produce non-empty results. Callers that genuinely need
  cross-agent scope should look at `internal/cmd/done.go`,
  `internal/cmd/step.go`, `internal/cmd/prime.go`, and `internal/cmd/bead.go`
  for the sanctioned uses of the opt-out; the mailbox call site is no longer
  one of them. Commit `63307bb` (2026-04-18) records an orthogonal rejected
  design that bypassed the default actor overlay at the mcpstore seam; that
  is a separate hazard that still applies.

- **Store injection over store discovery.** `NewMailbox(identity, store)` and
  `NewRouter(workDir, store)` take the Store as a parameter rather than
  constructing it internally. The test-injection contract is documented at
  `internal/cmd/mail.go:408-411`:

  > tests inject memstore directly into mail.NewMailbox / mail.NewRouter
  > without going through this helper.

- **`translate.go:77` references R-INT-1 label-sort dependency on bd.** Wire
  order is preserved at build time; assertions test against `buildLabels`
  output, not against a store's stored form, because bd sorted labels
  alphabetically.

- **Trailing-slash normalization.** `addressToIdentity` / `identityToAddress`
  at `types.go:111-121` trim whitespace and trailing `/`. Idempotent; applied
  at every mailbox/router boundary. From commit `e441c2b` (GH issue 30).

## Formal constraint tags

| Tag | Meaning | Location |
|-----|---------|----------|
| C-1 | Read-from-IsTerminal, not sentinel equality | `translate.go:12,25`; `translate_test.go:13` |
| D11 | Terminal-status classification lives on `Status`, not at the mail seam | `translate.go:12`; `internal/issuestore/store.go:93-95` |
| D14 | `Filter.Statuses` nil = "all non-terminal"; explicit slice = OR | `internal/issuestore/store.go:136-143`; enforced by `mailbox.go:46` |
| H-A R2 | Mail inbox MUST pin `Statuses=[StatusOpen]` (no nil fallback) | `mailbox.go:32-36,46` |
| C8 | `af` CLI surface unchanged across issuestore migration | `mailbox.go:35` (cited as constraint) |
| R-DATA-3 | High-severity risk: silent-unread re-surfacing in non-terminal statuses | `translate.go:15`; `translate_test.go:13` |
| R-INT-1 | Label set-equality round-trip required despite bd alphabetical storage | `translate.go:77-79` |
| Gotcha #3 (resolved by #125) | `mailbox.go` no longer needs `IncludeAllAgents: true`; explicit `Assignee` scopes the query and the contract test pins the cross-adapter equivalence. | `internal/issuestore/contract.go` (`ExplicitAssigneeWinsOverActorOverlay`) |
| Gotcha #4 | Explicit `Statuses` slice vs. nil — nil would surface non-open mail | `mailbox.go:36` |

## Gaps

- **Self-mail recursion protection.** `router.go:67-73` delegates recursion
  prevention to "the AI/LLM." No runtime guard, no test, no documented
  failure mode. unknown — needs review: what happens if a hook bug causes
  unbounded self-send.
- **Group fan-out atomicity.** `sendToGroup` at `router.go:77-99` returns a
  partial-failure error string but continues after per-member errors. Whether
  callers (`internal/cmd/mail.go:139`) distinguish partial-failure from total
  failure is not tested at this level. unknown — needs review.
- **`ErrEmptyInbox` defined but unreferenced in this package.** `mailbox.go:14`
  declares it; no code path in mail returns it. unknown — needs review:
  whether it is returned from `internal/cmd/mail.go` or is dead code.
- **`Message.Timestamp` source.** `issueToMessage` at `translate.go:24` reads
  `iss.CreatedAt`, but `NewMessage` at `types.go:44` uses `time.Now()`. These
  are only consistent if the Store preserves creation time exactly; not
  tested at the mail layer.
- **`notifyRecipient` silent-skip on tmux errors.** `router.go:127` swallows
  both `HasSession` errors and `SendNotificationBanner` errors
  (`router.go:130`, leading `_ =`). A broken tmux install would look
  identical to a recipient with no session. unknown — needs review.
- **Actor mapping via `Actor: from` in `messageToCreateParams`
  (`translate.go:42`) vs. the store's configured actor.** The store's actor
  overlay is the default; `Actor: from` overrides it. Whether mcpstore
  honors this override consistently with memstore is not anchored in mail's
  tests; relies on issuestore contract tests.
