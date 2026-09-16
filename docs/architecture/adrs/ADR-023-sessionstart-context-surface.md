# ADR-023: SessionStart context is three independent writers, one hook entry each; identity is delivered by every carrier except that hook

**Status:** Accepted
**Date:** 2026-09-13

## Context

Everything an agent knows at the moment its session opens arrives through the Claude Code
`SessionStart` hook, or not at all. Before #675, one hook entry carried all of it: a single
`&&`-chained command that ran `af prime --hook`, which rendered the role template (identity), then
shelled out to `af mail check --inject`, with memory appended after. One command, one output string,
one budget, four writers competing inside it.

Three facts made that topology fail, and it failed silently.

**The harness truncates a hook's output string.** Measured across the full transcript population
(8,949 hook-output rows, recipe `.analysis/675/scratch-15/hooksweep.jq`), the boundary between
"delivered whole" and "spilled to a file and replaced by a preview" falls between the largest
observed inline value and the smallest observed spilled one: **(9,949, 10,128] Unicode code points**,
or **(9,992, 10,259] bytes** for the same rows. Both brackets are gap-free and both straddle 10,000,
so both are consistent with a ~10,000 cap.

**The corpus does not determine which unit the cap counts**, and it cannot: UTF-8 byte length is
always ≥ code-point length, so no observation separates the two hypotheses. Treat the unit as
unknown. What follows from that is a choice, not a measurement — **af's budgets are stated and
enforced in bytes** (`len(Emit(n))` against `TotalBytes`, `internal/memory/slice.go:178-186`;
`len(renderMailEntry(…))` against `mailInjectTotalBytes`, `internal/cmd/mail.go:611-615`) because
bytes are the conservative unit under either hypothesis: a block that fits a byte budget fits the
same number of code points, but not the reverse. Where this ADR states a size in code points — the
K12 figures below — it does so because that is the *smaller* of the two numbers and therefore the one
that could still be under a cap the byte count exceeds; each is given with its byte count beside it.

**One of the writers cannot be bounded.** A formula step's `description` is the formula author's
contract and af reproduces it verbatim (C-2). It is not af's text to trim. With everything in one
string, a large step description did not degrade gracefully — it evicted the *other* writers.

**Nothing noticed.** There was no per-writer budget to exceed, no assertion over the composed
output, and `af telemetry` records no SessionStart context fields at all. An agent that opened a
session with its mail silently dropped looked exactly like an agent with no mail.

The compounding problem: identity was the largest and least necessary occupant of that string. The
harness already loads the agent's own `CLAUDE.md` at session start. Prime was re-sending, into a
capped channel, text the model was about to receive anyway — and crowding out mail and memory,
which have no second carrier, to do it.

## Decision

**(a) SessionStart context is three independent writers, one hook entry each.** *(design decision
D-1 — what SessionStart context is.)* `af prime --hook`,
`af mail check --inject`, and `af memory check --inject` are registered as three separate entries on
the same `SessionStart` matcher, for **both** role types, emitting through the structured
`hookSpecificOutput.additionalContext` channel
(`internal/claude/config/settings-autonomous.json:49-67` and the interactive twin; the two are held
command-for-command equal on this block by `internal/cmd/using_hook_table_test.go`, which compares
the commands after stripping each entry's `export PATH=… && ` preamble — not the raw bytes). There
is **no shared budget**:
the harness's unit of truncation is the output string, so one entry per writer makes each writer's
ceiling its own. Mail's block is capped at **4,608 B** (`mailInjectTotalBytes = 4096` +
`mailInjectFrameBytes = 512`, `internal/cmd/mail.go:522,530`); memory's at **4,736 B**
(`memory.DefaultTotalBytes = 4096`, `internal/memory/slice.go:24`, + `memoryInjectFrameBytes = 640`,
`internal/cmd/memory.go:54`). Neither can now be evicted by the other or by the step body.

**(b) Identity delivery is deliberately multi-carrier, and the SessionStart hook is not one of the
carriers.** *(design decision D-2 — who owns identity delivery.)* Identity reaches the agent by three paths. Two are unconditional: the harness's own load of the
agent's `CLAUDE.md`, and af's re-render of that file at every provisioning site, including recycles
(`internal/cmd/install.go:607,1118`, `internal/worktree/worktree.go:574`,
`internal/cmd/recovery.go:1110`). The third — a **plain** `af prime`, the tool-result kind an agent
runs itself, which renders the role template whole — is *conditional*, and the condition is the
whole of the gate at `internal/cmd/prime.go:287`, `!primeHookMode && !slimIdentity`. `slimIdentity`
is #678's re-prime reduction: when the tokenomics interview mechanism is on it slims every prime
past the session's first (`prime.go:269-271`). That mechanism is umbrella-gated and comes on *by
default* once the umbrella is on (`internal/tokenomics/policy.go:157,175-177`), and the SessionStart hook
prime is itself counted, so with the umbrella on the first plain prime an agent runs is already
prime #2 and is slimmed. OD-1's "identity in every tool-result prime" therefore holds exactly in the
shipped default — umbrella off — and not under a configuration an operator is expected to choose.
The hook path carries none of it in any configuration, because a channel that truncates is a
fragment channel, and a fragment of an identity is worse than none. **No on-demand identity flag
exists** and none was added: the recovery path is the two unconditional carriers, which is why it
matters that they are the unconditional ones. The deployed corpus is
held byte-equal to the embedded templates by
`TestDeployedAgentIdentityMatchesEmbeddedTemplate` (`internal/cmd/identity_parity_test.go:49`) — the
gate that makes "the harness loads `CLAUDE.md`" a guarantee rather than a hope.

**(c) Mail delivered-state is session-scoped and never enters the store.** *(design decision D-4 —
delivered-state location and key. Rejected: a label or `Read` flag on the message, which makes
delivery a durable mutation; and a creation-time watermark via `CreatedAfter`, dominated by the
inclusive bound and by status transitions.)* What a session has
already been shown is recorded in `.runtime/mail_delivered`, keyed on the session id the hook itself
supplies. Mail is not mutated, so nothing about a message's durable state depends on a hook having
run. Deleting the file re-delivers everything once — fail-open, because the failure mode of
over-delivery is noise and the failure mode of under-delivery is an agent that never learns it has
work.

**(d) The formula step body is the one unbounded writer, and the tool-result `af prime` is its
guaranteed channel.** af does not trim a step description. `af prime --hook`'s entry may therefore
be truncated for a large step — and after (a), that costs nothing from mail or memory. See the K12
rule below.

**(e) The external harness dependencies this design rests on**, each labelled by evidence class.
None is a repository contract; all are re-verified on harness upgrade (C-9).

| | Dependency | Evidence class |
|---|---|---|
| E1 | Hook output strings are capped at ~10,000 characters; over-limit output spills to a file and is replaced by a preview | **Observed only.** A 2026-09-12 re-check of hooks.md, hooks-guide.md, settings-reference.md and the changelog found *no* stated hook-output size limit. Rests entirely on the full-population bracket above; recipe `.analysis/675/scratch-15/hooksweep.jq` |
| E2 | When several hooks return `additionalContext` for the same event, all values are received | **Field documented, delivery UNVERIFIED.** The `additionalContext` field shape is documented; the "receives all of the values" sentence was *not* found on re-check. The K11 rollout spike that would have confirmed whole-delivery was **never run** — no `spike-k11-transcript.md` exists — so this row rests on assumption, not observation. Re-run the spike (per the re-verification rule below) before trusting it |
| E3 | Whether the cap is per-command or aggregate for **plain stdout** from several hooks on one event | **Undocumented.** Observed per-attachment at `Stop` only. This is *why* (a) uses the structured channel rather than plain stdout |
| E4 | Project-root `CLAUDE.md` is re-read and re-injected after compaction | **Documented** (memory.md). The one dependency in this table with documentation behind it, and (b) leans on it |
| E5 | SessionStart `source` ∈ {startup, resume, clear, compact, fork}; fires after compaction with `source: "compact"`, session id unchanged | Observed |
| E6 | All matching hooks for an event run in parallel | Observed. Consequence: (a)'s three entries have **no guaranteed order** — no writer may depend on another having run |
| E7 | Hooks run under `claude -p` unless `--bare` or `disableAllHooks` | Observed; consistent with the `internal/cmd/prime.go` pane-guard comment |
| E8 | The `UserPromptSubmit` payload carries `session_id` | Observed. The repo has no independent reader to confirm it; (c)'s per-prompt delivery degrades to emit-without-recording if it is ever false, which is the fail-open direction |

**Re-verification rule:** on any Claude Code upgrade, re-run the `hooksweep.jq` recipe against the
new transcript corpus before trusting E1's bracket. If the bracket moves, the budgets in (a) move
with it. E1 and E2 are the two this design would actually change shape for; E4 turning false would
cost identity after compaction and is the one to watch.

**(f) `9278bfd` is superseded for hook-mode identity only.** That commit
(`docs/architecture/subsystems/cmd.md:68`) made prime's context injection unconditional because
"agents lost formula context on every compaction". Its **formula-context** rationale stands
unchanged and is why (d) keeps the step body in the hook entry. Its **identity** half is superseded:
identity no longer rides the hook, because E4 plus (b)'s re-render make it unnecessary there.

**(g) Sub-agent and grader sessions fire the chain too** — measured, not assumed. They are excluded
from delivered-state by construction: (c) keys on the session id the hook supplies, and delivery is
additionally refused when the payload's transcript path contains `/subagents/`. Whether a sub-agent's
id is ever the parent's is UNPROVEN — the K11 payload-capture check that would settle it was not run —
so the `/subagents/` refuse guards the residual case (a sub-agent SessionStart carrying the parent's
session_id) rather than relying on the ids differing. A session with no id claim emits without recording.

**(h) The telemetry blind spot is accepted and named.** Half the instrumentation exists and is worth
knowing about: `recordPrimeCost` (`internal/cmd/prime_economics.go:75-104`) persists prime's own
block size and token estimate per session. But it covers **prime only** — mail and memory record
nothing — and it is **write-only**: no read surface selects those fields, so no report, threshold or
alert consumes them. The practical consequence stands: **no runtime signal exists** for a
SessionStart-size regression, and the K9 CI test is the only catch. Anyone widening a SessionStart
writer without extending that test is working without instrumentation. Closing this properly means
giving mail and memory the same recording and then building a reader — the second half being the part
that has never existed.

### The K12 step-size rule

**A formula step description longer than the per-string cap is delivered whole only by the
tool-result `af prime`** — the one an agent runs itself. `af prime --hook`'s entry may be truncated
for such a step; because of (a), mail and memory are unaffected. Treat a step description that
approaches the cap as a formula-authoring smell, not a delivery guarantee. This rule is documentation
only: the authoring-time warning, the truncation detector and the largest-step calibration report
were all considered and cut (OD-4), so nothing enforces it mechanically.

Sizes are `len(description)` in **Unicode code points**: the smaller of the two counts and so the
more forgiving reading of E1's undetermined unit, since a step that is over the cap in code points is
over it either way. When this ADR was written, several step-bearing formulas in
`internal/cmd/install_formulas/` had a first step over 4,000 code points and the largest single step
was over twice the cap. The figures themselves are deliberately not recorded: they drifted materially
between the design and this ADR, inside a single issue's lifetime, and that drift is the argument for
the rule. Recompute them over the live corpus (parse every `*.formula.toml`, measure each `[[steps]]`
description) rather than transcribing any stated number.

## Consequences

**What this buys.** Mail and memory now have ceilings that hold regardless of what any other writer
does — the property the single-chain design could not offer at any budget. An empty vault or an empty
mailbox costs nothing. Identity survives compaction through E4 and the re-rendered `CLAUDE.md`
without spending a byte of the capped channel, and the file it survives through is now gated
(`identity_parity_test.go:49`) rather than assumed.

**What it costs.**

- **Three entries to keep in step, not one.** Settings templates, both role types, and the operator
  manual's hook table must agree. `internal/cmd/using_hook_table_test.go` holds the doc against the
  JSON; nothing holds the JSON against a future fourth writer.
- **A wider derivation surface.** The Memory Protocol text agents read is a Go const
  (`internal/templates/memory_protocol.go`) baked into 41 generated templates, hand-mirrored into 2
  built-ins, and rendered into 43 deployed `CLAUDE.md` files — 86 derived artifacts from one const,
  three byte-equality gates, two packages. #515 shipped exactly this edit to 40 of 42 deployed files and left the
  built-ins stale for a month. The third gate exists because of that incident.
- **No ordering guarantee** (E6). Any future writer that needs to read another's output is
  incompatible with this topology.
- **A documentation-only rule** (K12) with no enforcement, by decision.
- **The blind spot in (h)**, carried deliberately.

**Rejected alternatives** *(design decisions D-1 and D-3 — topology and output channel).* *One owned
verb with one budget* reverses `.designs/515:151,159` and
cannot fit a step body beside mail and memory at any split. *One `&&` chain with three writers and a
shared sum* leaves memory's survival dependent on prime's size, which C-2 forbids bounding — it is
the design that failed. *Plain stdout per entry* is the recorded fallback if E2 ever proves false;
it is not the default because E3 leaves per-command separation of plain stdout undocumented, while
the `additionalContext` field shape is documented and has three encoder precedents in the tree.

## Flip condition

If E1's bracket moves far enough that the cap stops binding — a harness that delivers 100 KB hook
strings whole — the *reason* for one-entry-per-writer weakens, and folding the entries back together
becomes arguable on simplicity grounds. Re-run `hooksweep.jq` before believing it, and note that E6
(no ordering guarantee) and (d) (the unbounded step body) are independent of the cap and would still
argue for separation.

If E4 proves false — `CLAUDE.md` is *not* re-injected after compaction — then (b) has lost a carrier
and hook-mode identity must be reconsidered on its merits, not restored reflexively: the plain-prime
render and the re-render at every provisioning site both still stand.

## Corpus links

- `internal/claude/config/settings-autonomous.json:49-67` — the three SessionStart entries (and the
  interactive twin, held byte-equal on this block)
- `internal/cmd/prime.go:287` — the `!primeHookMode && !slimIdentity` gate: never for the hook, and
  for the tool-result prime only while `slimIdentity` is false (`prime.go:269-271`)
- `internal/cmd/mail.go:511-530` — mail's 4,608 B ceiling and why it is measured on rendered text
- `internal/memory/slice.go:24`, `internal/cmd/memory.go:50-54` — memory's 4,736 B ceiling
- `internal/cmd/identity_parity_test.go:49` — the deployed-`CLAUDE.md` parity gate that makes (b)'s
  harness carrier a guarantee
- `internal/cmd/using_hook_table_test.go` — the operator manual's hook table held against the
  installed JSON
- `internal/templates/memory_protocol.go` — the shared const at the root of the 86-artifact
  derivation tree
- `docs/architecture/subsystems/cmd.md:14,51,68` — the `af prime` contract row, the self-exec row
  (prime no longer self-execs), and the `9278bfd` row superseded by (f)
- `.analysis/675/scratch-15/hooksweep.jq` — E1's reproduction recipe
- `.designs/675/design-doc.md:126,172` — the channel choice and items (a)-(h) as decided
- ADR-007 — a hook's failure must not break the session (why the injectors swallow errors)
- ADR-022 — the memory vault whose top notes writer (a) delivers
