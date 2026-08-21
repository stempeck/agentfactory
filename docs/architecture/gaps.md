# Gaps

Things that look invariant, aren't enforced, or don't match reality.
A genuine architecture doc always has gaps — hiding them is a failure
mode this doc must not reproduce. This is the to-do list for the next
iteration of `/architecture-docs`.

Each entry is: **what the gap is**, **where it's anchored**, **why it
matters**, **recommended resolution**.

---

## Drift: documented but not enforced

### GAP-1 — `BD_ACTOR` env var exported after bdstore removal

**What:** `internal/session/session.go:159` exports `BD_ACTOR` alongside
`AF_ROLE`, `AF_ROOT`, `BEADS_DIR`. Commit `7acd617` (Phase 7) deleted
`internal/issuestore/bdstore/`. The `bd` binary is no longer invoked
anywhere. But `BD_ACTOR` is not vestigial: the session sub-agent verified
12 consumer sites in `internal/cmd/` (bead.go, done.go, handoff.go,
mail.go, prime.go, sling.go, step.go) read it and pass it to
`newIssueStore(…, actor)` as the mcpstore actor default.

**Anchors:** `internal/session/session.go:159`; consumers listed in
`subsystems/session.md` Gaps. `mcp_server_problem.md:71` and
`.designs/80/security.md:63-64` preserve it in the "stable agent contract".

**Why it matters:** A developer reading `session.go` sees `BD_ACTOR` and
assumes it is bd-era dead code. Deleting it would silently break mcpstore
actor-scoping in every command that runs outside an agent session.

**Recommended resolution:** Rename to `AF_ACTOR` (or collapse into
`AF_ROLE` — the two values are always equal in practice: `session.go:159`
sets both to `m.agentName`). This cleanup was not done during Phase 7
deletion because changing the contract was out of scope for the deletion
PR. Owner: whoever picks up the bdstore cleanup tail.

---

### GAP-2 — Root `CLAUDE.md` lists removed role templates

**What:** The project-root `CLAUDE.md` documents `deacon`, `refinery`,
`witness` as role templates. They were deleted in commit `8d64e6d`
(2026-03-28). Only `manager`, `supervisor`, and 16 specialist templates
exist today.

**Anchors:** `CLAUDE.md` (root); `subsystems/embedded-assets.md` (verified
present role set); commit `8d64e6d`.

**Why it matters:** Agents read `CLAUDE.md` at every session start. The
stale role list is confidently wrong.

**Recommended resolution:** Update `CLAUDE.md` role section to match
the current `internal/templates/roles/` tree.

---

### GAP-3 — Hook asymmetry rationale unanchored

**What:** `internal/claude/config/settings-autonomous.json:36-50` wires
both `quality-gate` and `fidelity-gate` to fire on Stop. The interactive
variant at `settings-interactive.json:36-46` fires only `quality-gate`.
No commit message or code comment explains WHY interactive skips
fidelity.

**Anchors:** `subsystems/hooks.md` Gaps; settings files under
`internal/claude/config/`.

**Recommended resolution:** Either document the rationale in a comment
in the settings JSON files (or adjacent .md) or unify the two — one of
the two is a silent choice.

---

### GAP-4 — `worktree.GC` may never match live sessions

**What:** `internal/worktree/worktree.go:302` calls
`exec.Command("tmux", "has-session", "-t", meta.Owner)` to check whether
the worktree's owning session is still alive before GC. BUT
`internal/session/session.go` creates sessions prefixed with `af-` (per
`names.go`). If `meta.Owner` is stored as the bare role name (not
prefixed), the `has-session` check always fails → GC always removes →
worktrees of live agents get destroyed.

The integration test `worktree_integration_test.go:263` pins the bare
name in a comment, which is self-inconsistent with the `af-` convention
elsewhere.

**Anchors:** `subsystems/session.md` Gaps;
`subsystems/fs-primitives.md#cross-cutting`; `worktree.go:302`.

**Why it matters:** Either GC is broken and nobody's noticed (silent
worktree loss on live agents) OR there's an unverified path that stores
`meta.Owner` pre-prefixed. Not clear which. Flagged "unknown — needs
review".

**Recommended resolution:** Read `meta.Owner` at a write site (e.g.
`worktree.WriteMeta`) and confirm what string is actually stored. If
bare, fix either the write site or the GC check.

---

### GAP-5 — `FactoryConfig` version gate is test-only dead code

**What:** `internal/config/config.go:103-104` defines `LoadFactoryConfig`;
`config_test.go:229, 246` pin version-0 and future-version rejection.
But there are **no production callers** of `LoadFactoryConfig` —
`internal/config/root.go:15, 31` uses `os.Stat(FactoryConfigPath(dir))`
to detect the factory root without parsing the file.

**Anchors:** `subsystems/config.md` Gaps; `invariants.md#INV-11`.

**Why it matters:** A future-version factory.json file passes all
production code paths silently. The version gate is declared but not
held.

**Recommended resolution:** Either wire `LoadFactoryConfig` into
`root.FindLocalRoot` (preferred — enforce version at discovery time) or
delete the loader + the version-gate tests.

---

## Inconsistencies within an invariant

### GAP-6 — H-4/D15 "atomic-write invariant" is two mechanically-separate things

**What:** The phrase "H-4/D15 atomic-write invariant" appears in both
`internal/cmd/sling.go:578-590` (write-ordering: caller file before bead
creation) AND in the purpose of `internal/fsutil/WriteFileAtomic`
(byte-level: no partial file on crash). They share a name but not a
mechanism. `persistFormulaCaller` at `sling.go:598` uses raw
`os.WriteFile`, not `fsutil.WriteFileAtomic`.

**Anchors:** `subsystems/fs-primitives.md#fsutil`;
`invariants.md#INV-6`.

**Why it matters:** A reader sees "H-4/D15" and assumes both halves are
satisfied. They aren't in the same call path.

**Recommended resolution:** Rename one of the two. The write-ordering
half is better called `H-4-ordering`; the byte-level half is better
called `H-4-atomic`. Update the comment anchors to say which half is
which.

---

### GAP-7 — `fsutil.WriteFileAtomic` has exactly one production caller

**What:** `internal/fsutil/atomic.go:11-17` was added (commit `757895a`)
to fix `TestConcurrentRemoveAgent_NoCorruption`. Used at
`internal/worktree/worktree.go:66` (meta file). Everywhere else that
writes under concurrency (`done.go`, `sling.go`, `checkpoint.go`,
`lock.go`) uses raw `os.WriteFile`.

**Anchors:** `subsystems/fs-primitives.md#fsutil`.

**Why it matters:** If the rationale was "atomic write prevents torn
files under concurrency", that rationale should apply uniformly. The
current state is "we fixed the one file the test caught".

**Recommended resolution:** Audit the remaining write sites. Either
promote `WriteFileAtomic` to a default, or document why each site
doesn't need it.

---

### GAP-8 — `worktree.RemoveAgent` lockless read-modify-write (R-INT-3)

**What:** `internal/worktree/worktree.go:257-276` does read-modify-write
on meta files without a lock. `WriteFileAtomic` prevents byte-level
corruption but not lost updates under concurrent RemoveAgent calls.

**Anchors:** `subsystems/fs-primitives.md#worktree`. The `.designs/`
history explicitly accepts this tradeoff, so it's a known-and-accepted
gap rather than a bug.

**Recommended resolution:** None at the architecture-doc level — the
design accepted this. Document it so future reviewers don't "fix" it
without seeing the accepted tradeoff.

---

## Missing defense in depth

### GAP-9 — mcpstore client does not verify endpoint is loopback

**What:** `INV-4` requires the Python server to bind `127.0.0.1` only.
The Go client at `internal/issuestore/mcpstore/client.go` reads the
host:port from `.runtime/mcp_server.json` and connects without verifying
the host is `127.0.0.1`.

**Anchors:** `subsystems/py-issuestore.md` Gaps;
`trust-boundaries.md#cross-process`; `invariants.md#INV-4`.

**Why it matters:** A future misconfiguration (or malicious rewrite of
`.runtime/mcp_server.json` by another local process) could point the
client at a non-loopback host. INV-4 is held by the producer; the
consumer does not verify.

**Recommended resolution:** Add a check in the client: if endpoint host
is not `127.0.0.1` or `::1`, refuse to connect. One-line hardening.

---

### GAP-10 — `ErrNotFound` mapping is brittle substring match

**What:** `internal/issuestore/mcpstore/client.go:80-84` maps the Python
server's `KeyError` into `issuestore.ErrNotFound` by substring match of
`"issue not found:"`. Python code change to the error message format
would silently break not-found semantics across the system.

**Anchors:** `subsystems/py-issuestore.md#brittle-mapping`.

**Recommended resolution:** Return a structured error code from the
server (e.g. `{"error": {"code": "not_found", ...}}`) and match on the
code, not the message string.

---

## Dead or orphaned code

### GAP-11 — `internal/formula/` orphans

**What (all cited in `subsystems/formula.md#gaps`):**
- `BackoffConfig` / `ParseBackoffConfig` — no production consumer in
  `internal/`.
- `GetAllIDs`, `GetDependencies` — no non-test callers.
- `Input.Type`, `Input.RequiredUnless` — parsed, never read.
- `Gate` struct — declared, no validator or consumer in
  `internal/formula/`.
- `.formula.json` discovery path — `discover.go` accepts the extension
  but `Parse` only decodes TOML. Broken/aspirational.
- `Formula.Version` — no validation on load.

**Recommended resolution:** Either wire these in or delete them. Each
is a small follow-up; together they indicate the package has grown
speculative surface area.

---

### GAP-12 — `internal/mail/` orphans

**What (cited in `subsystems/mail.md#gaps`):**
- `ErrEmptyInbox` — declared but unreferenced within the package.
- `notifyRecipient` — silently swallows tmux errors (no logging, no
  surface).

**Recommended resolution:** Either expose/wire or delete.

---

### GAP-13 — `session` package orphans

**What (cited in `subsystems/session.md#gaps`):**
- ~~`IsAvailable`, `ClearHistory`, `RespawnPane` appear unreferenced.~~
  **Closed 2026-08-05 (#596).** All three have production callers, so the
  orphan observation no longer holds: `ClearHistory` (`tmux.go:516`) and
  `RespawnPane` (`tmux.go:525`) are called by `respawnSession`
  (`helpers.go:192`, `:196`) — the single funnel every pane recycle passes
  through, and the anchor point for the #596 recovery log write and recycle
  fence (`helpers.go:197`); `IsAvailable` (`tmux.go:202`) is called by the
  pre-launch availability checks at `up.go:90` and `sling.go:909`. The mirror
  of this claim in `subsystems/session.md` needs the same correction.
- `SetEnvironment` errors silently discarded at `session.go:116`.
- Hardcoded 5s sleep in `AcceptBypassPermissionsWarning` — anchor
  unknown.

**Recommended resolution:** Audit; delete unused or add callers.

---

### GAP-14 — `py/issuestore` unused schema columns

**What (cited in `subsystems/py-issuestore.md#gaps`):**
- `actor` column is written by `issuestore_create` (`store.py:126`) but
  never read back — `_issue_from_row` (`store.py:50-71`) doesn't reference
  it; patch doesn't accept it; list filter doesn't use it. Write-only.
- `metadata` table declared in `schema.py` but no handler in `store.py`
  references it.

**Why it matters:** The server is storing data nobody reads. Either the
data will be needed later (and the consumer is missing) or the columns
are dead.

**Recommended resolution:** Either wire a consumer or drop the columns.
Prefer the latter unless a design doc says otherwise.

---

## Anchor drift between designs and code

### GAP-15 — Mail translate.go R-INT-1 label-sort cite predates bd removal

**What:** `internal/mail/translate.go:77` comments say labels "must be
sorted alphabetically because bd sorts them that way at read time".
After commit `7acd617`, bd is gone; mcpstore sorts differently or not
at all. Verify whether the label sort still matters.

**Anchors:** `subsystems/mail.md`; `translate.go:77`.

**Recommended resolution:** Grep for label-order-dependent code in
`mcpstore/client.go` and `py/issuestore/store.py`. If order is
irrelevant now, update the comment.

---

### GAP-16 — Mail self-mail guard commented out

**What:** `internal/mail/router.go:67-73` has a self-mail recursion
guard that is intentionally commented out because a Stop Hook uses
`af mail send` to self. Recursion prevention is punted to the LLM.

**Anchors:** `subsystems/mail.md#gaps`.

**Why it matters:** A bug in a hook could produce infinite self-mail
with no mechanical stop.

**Recommended resolution:** Either implement a non-recursion guard that
distinguishes hook-sent from agent-sent mail, or document the threat
model that makes the punt acceptable.

---

## Missing test coverage for known-risky paths

### GAP-17 — AC3.11 hook concurrency not validated

**What:** `subsystems/hooks.md#test-coverage`. AC3.11 (Claude Code fanning
both quality-gate and fidelity-gate concurrently) is documented as a
manual operator check. Whether it has been run since April 2026 is
unknown.

**Recommended resolution:** Promote to a scripted smoke test if
feasible, or document the last-run date.

---

## Accepted residuals (documented trade-offs)

### GAP-18 — Worktree-containment interlock is bounded detect-and-correct, not a sandbox (#386 Practical Ceiling / accepted residual)

**What:** The #386 worktree-containment interlock — the `PreToolUse` hook
`af containment-check` (`internal/cmd/containment.go`) backed by the pure
`worktree.Contains` primitive — is a *runtime location interlock for an
autonomous LLM agent*, not a hermetic sandbox. Its **Practical Ceiling** is
**bounded prevention** plus reliable post-hoc detection, and that ceiling is an
**accepted residual**, not a defect. The accepted residuals:

- **(a) Undecidable shell escapes are out of scope.** Only *literal*
  `cd`/`pushd`/`git -C` and `Write`/`Edit` `file_path` targets are decidable
  (`parseBashTarget`/`literalTarget`). `cd $(computed)`, `eval`, `bash -c`, and
  `${VAR}` expansions are not — the guard raises the cost of escape and catches
  the *habitual* drift that caused the incident, but it is a containment guard,
  not a sandbox.
- **(b) One action may execute before the same-loop correction.** Under
  ADR-007's inform-not-block posture, the corrective lands *after* the drifting
  call returns. AC-1's "remains within boundary throughout" is therefore met as
  *detect + correct (bounded one-action latency)* — **bounded prevention**, not
  hard pre-emptive prevention. A blocking PreToolUse `deny` was rejected for
  contradicting ADR-007.
- **(c) A silently-broken or absent guard is logged, not actively alerted.**
  Failures are recorded to `<agentDir>/.runtime/containment_debug.log` + stderr
  (`failObservable`), never escalated, because active alerting would require the
  supervisor dependency that constraint C-3 forbids.
- **(d) Effect-based crossings beyond the two declared EXPECTED cases**
  (`git push origin <branch>`, `$HOME/.cache/...` writes) are out of scope.
- **(e) The fix is NOT retroactive to a live, currently-running session.** An
  agent picks up the hook only on **reprovision** (`af install --init`) **and** a
  subsequent session **recycle** (restart). The very agent type that caused the
  incident stays unprotected until it is reprovisioned and its session is
  recycled — the interlock does not retrofit a live session in flight.
- **(f) Persisted drift is covered only when the subsequent command carries a
  resolvable path.** `resolveAgainst` (`containment.go:270-281`) uses the payload
  `cwd` only as the resolution *base* for a relative target, never as the
  effective target itself. A subsequent command from an already-drifted location
  that carries a path resolvable against the drifted `cwd` (`git -C . status`, a
  relative `Write`/`Edit` `file_path`) IS detected; a truly path-less subsequent
  command (`af done`, bare `git status`, bare `make`) yields an empty effective
  target and is silent. This path-less gap is the accepted H1 residual —
  documented here, not "fixed" in `containment.go` (out of #386 scope).
- **(g) Deliberate dual env+disk spoofing remains possible.** The
  `AF_WORKTREE_ID` ↔ on-disk `worktree_id` cross-check WAS implemented in Phase 2
  (`containment.go:115-134`, against `worktree.go:600`); it fails observably on a
  mismatch and falls back to the filesystem-derived boundary. A residual remains
  only for a *deliberately adversarial* agent that rewrites both the environment
  anchor and the on-disk file in lockstep.

**Anchors:** `internal/cmd/containment.go` (effective-target parse
`parseBashTarget`/`literalTarget`; `resolveAgainst` L270-281; env-spoof
cross-check L115-134; `failObservable`); `internal/cmd/containment_e2e_integration_test.go`
(end-to-end production-path proof, incl. the persisted-drift and
path-less-residual cases); `.designs/386/design-doc.md` L357-367 (Practical
Ceiling, residuals a-d), L131-136 (Residual on AC-1 "throughout"), L125 (AC-8
own-session delivery); [ADR-007](adrs/ADR-007-hooks-never-block.md) (hooks never
block; 2026-06-15 "no escalation into a void" amendment).

**Why it matters:** AC-1's "remains within boundary throughout" is met as
detect + correct, not hard prevention. A future self-referential or six-sigma
gate must not mistake "habitual drift caught" for "all escape paths closed" and
"fix" the deliberate sub-hard-block posture — doing so would contradict ADR-007.
This entry records the trade-off so the ceiling is read as designed, not as a
regression.

**Recommended resolution:** None at the architecture level — these are accepted
trade-offs of the ADR-007 inform-not-block posture. The only operator follow-up
is (e): reprovision (`af install --init`) then recycle the affected sessions so
the hook is actually live for them. Residuals (a)/(f)/(g) are out-of-scope
hardening, not open bugs.

---

### GAP-19 — Context-exhaustion recovery is bounded autonomous recovery, not a guarantee (#596 Practical Ceiling / accepted residual)

**What:** Issue #596 gives the factory a way to observe that an agent's model
context window has filled up and to act on it — the occupancy channel
(`statusline.WriteSnapshot` / `ReadObservations`, `observation.go:305`), the
trigger (`evaluateExhaustion`, `recovery.go:769`), the sweep (`pollOccupancy`,
`recovery.go:861`), the executor (`recoverExhausted`, `recovery.go:1420`) and
the durable breaker. What it is **not** is a guarantee that a wedged agent
always recovers. Four constraints are irreducible, and all four are **accepted
residuals**, not open defects:

- **(a) The root of the supervision tree is unsupervised.** Occupancy recovery
  is hosted by `af watchdog`. If the watchdog itself dies, nothing recycles
  anything until the next `af up`. The residual is bounded by making the
  observer's own absence loud rather than self-healing: each tick touches
  `.runtime/watchdog_heartbeat`, and its staleness is surfaced. The last
  supervisor is always unsupervised; the design chooses visible absence over a
  second supervisor with the same problem one level up.
- **(b) A context-reducing recycle is lossy by construction.** The in-session
  conversation does not survive — a lossless context-reducing recycle is a
  contradiction in terms. The loss is bounded to work since the last natural
  artifact (commit, checkpoint note, mail, closed step) and named explicitly in
  the persistent-session contract table in `USING_AGENTFACTORY.md`. The K14 soft
  advisory at `context_advisory_pct` raises what gets externalized while the
  session is still capable of acting, which shrinks the residual but cannot
  remove it.
- **(c) The telemetry is volunteered by a host the factory does not control.**
  Occupancy is whatever Claude Code reports through the statusline payload. A
  gateway profile that under-reports payload accounting can keep an agent below
  `context_threshold_pct` while it is in fact wedged. The backstop
  (`backstopFires`, `recovery.go:1145`) bounds this for agents holding an open
  step, and `escalateNoStep` (`recovery.go:1239`) bounds the post-formula
  persistent class — but neither can perfectly separate "a legitimately long
  step" from "no useful output", so the window is a trade-off, not a decision
  procedure. The reader compensates where it can: it exports no
  healthy-without-a-datum constructor, so a suppressed channel reads `dark`,
  never healthy.
- **(d) CI cannot prove a real model resumes real work.** The end-to-end lane
  (`recovery_e2e_integration_test.go`) drives the real store, the real
  `pollOccupancy`, the real `recoverExhausted` and the real `respawnSession`
  funnel, and asserts the respawned pane's startup command carries `af prime`
  and that `af prime` re-prints the seeded step. The final `tmux respawn-pane`
  hop is substituted at the `doRespawn` seam (`recovery.go:1505`) because CI has
  no model credentials — the boundary is **declared in that file's doc comment**
  rather than hidden, which is the residual's mitigation: the seam asserts on
  the command that would have run instead of no-oping silently.

A fifth, milder residual: the terminal escalation state is durably discoverable
(breaker file, `RECOVERY HALTED` mail, `.runtime/recovery_halt_undelivered`
breadcrumb, `recovery` field on `af agents list --json`), but human
acknowledgment is unprovable from inside the factory. `escalation_sent` and
`recipient_session_live` are recorded separately so that a send whose outcome is
unknown is never counted as arrival — the ceiling is a discoverable,
recycling-blocking terminal state, not a confirmed read.

**Anchors:** `internal/cmd/recovery.go` (trigger `evaluateExhaustion:769`; fence
`recycleFenceBlocks:576`; sweep `pollOccupancy:861`; executor
`recoverExhausted:1420`; backstop `backstopFires:1145`; no-step escalation
`escalateNoStep:1239`; halt `haltRecovery:1356`; funnel seam `doRespawn:1505`);
`internal/cmd/helpers.go:148-199` (the `respawnSession` funnel — `af prime` at
`:150`, `ClearHistory`/`RespawnPane` at `:192`/`:196`, recovery-log write and
fence arm at `:197`); `internal/statusline/observation.go:305`
(`ReadObservations`, the three-state reader); `internal/config/startup.go:44-59`
(the 14-key `recovery` block); `internal/cmd/up.go:662`
(`warnUnobservableAgents`, the provisioning pre-check);
`internal/cmd/recovery_e2e_integration_test.go` (the declared substitution
boundary); `.designs/596/design-doc.md` (AC-9; Practical Ceiling; Six-Sigma
Caveats Gap 11); `USING_AGENTFACTORY.md` § "Context exhaustion recovery".

**Why it matters:** the surrounding machinery reads as a guarantee — a durable
breaker, an append-only event log, an automatic recycle — and a future
self-referential or six-sigma pass could mistake "recovery exists" for "an
exhausted agent always recovers" and try to close (a)–(d) as bugs. They are the
shape of the problem, not defects in the solution: (a) is the halting nature of
supervision trees, (b) is definitional, (c) is a trust boundary the factory does
not own, and (d) is a property of CI. Recording them here keeps the ceiling
legible so the residuals are read as designed.

**Recommended resolution:** None at the architecture level. Two follow-ups are
recorded rather than done: the longer-window occupancy-delta / mail-activity
backstop for the no-step persistent class (a wider net for (c)), and the
"single health chokepoint" endgame for the occupancy reader once the additive
`context_state` / `recovery` fields have more consumers. The only operator
follow-up is provisioning: agents whose settings carry no `statusLine` key write
no snapshots and are invisible to this surface — `af up` warns about them, and
`af up` / `af sling` (**not** `af install --init`, which reprovisions
factory-root agent dirs only) is what delivers the key.

---

### GAP-20 — The memory vault is durable against teardown, not against the container (#515 R1 / accepted residual)

**What:** Issue #515 gives agents a learnings vault that outlives a worktree:
plain Markdown under `<factory-root>/.agentfactory/memory/<agent>/`, written
through `af memory add`, sliced into a bounded block at session start, and sited
outside every directory a teardown reaches. The durability claim is now held by
a test matrix rather than by prose — `TestMemoryDurability_NoteSurvivesEveryTeardownPath`
drives a real CLI write from a worktree cwd through all seven destruction paths
and reads the note back through the SessionStart verb. What the vault is **not**
is durable against the container that hosts it, and three residuals are
**accepted**, not open defects:

- **(a) The vault is container-local and git-invisible.** It is gitignored by
  design (`data.md:86-93`): notes are agent-authored, high-churn and often
  wrong, and committing them would put unreviewed model output into the
  project's history. The consequence is that `docker rm` takes every recorded
  learning with it, and nothing inside the factory can observe that it is about
  to happen. ADR-019 forbids closing the hole by requiring container recreation,
  so the compensating control is loudness plus an exit: `af up` and factory-wide
  `af down --all` print the note count and the age of the last export
  (`warnVaultExportStaleness`, `memory_export.go`), and `af memory export` /
  `af memory import` move the vault across a container boundary with no mount.
  The `AF_MEMORY_HOST_DIR` bind mount in `quickdocker.sh` removes the residual
  for containers **created** with it and is deliberately unavailable to
  containers that already exist.
- **(b) Delivery of a note into a session is not confirmable.** `af memory check
  --inject` emits the slice on the SessionStart hook's stdout and is
  contractually silent and exit-0 on every failure path (ADR-007), because a
  hook that errors is a session that starts wrong. That contract means an
  injection that was truncated, dropped by the host, or never read by the model
  is indistinguishable from one that landed. The bound is that the slice is
  reconstructible on demand — `af memory check` without `--inject` reports what
  *would* be served, and `af memory list` is always available to the agent — so
  a suspected miss is diagnosable even though it is not detectable.
- **(c) Capture is voluntary, so absence proves nothing.** Nothing compels an
  agent to record a learning, and the identity on a note is claimed rather than
  proven (derived from cwd or `AF_ROLE`, design R12). An empty vault therefore
  cannot be read as "nothing was learned", and a note's `agent:` cannot be read
  as an authenticated attribution. The mitigation is that every note carries its
  own provenance into the injected block, framed as a recorded observation
  rather than an instruction (security.md T4), which is what makes an anomalous
  note visible to the operator curating the vault.

**Anchors:** `internal/cmd/memory.go` (the nine-verb surface; the
resolveInvokerRoot rule at `:1-21`; `runMemoryCheck`'s silent-and-exit-0
contract); `internal/cmd/memory_export.go` (`writeVaultTarball`,
`warnVaultExportStaleness`, the `.runtime/memory_export.json` marker);
`internal/memory/` (`store.go`, `codec.go`, `slice.go`, `report.go` — the
library-only core that refuses to derive a root); `internal/cmd/up.go` /
`internal/cmd/down.go` (the two staleness chokepoints);
`internal/cmd/memory_durability_test.go` (the seven-path matrix and its
non-vacuity guards); `quickdocker.sh` (`AF_MEMORY_HOST_DIR`, creation-time
only); `docs/architecture/adrs/ADR-019-no-container-recreation.md`;
`.designs/515/design-doc.md` (Risk Registry R1); `.designs/515/data.md:86-93`
(why the vault is git-invisible); `USING_AGENTFACTORY.md` § "The memory vault".

**Why it matters:** the subsystem reads as durable storage — a mark-only
lifecycle, an index, an export verb, teardown paths that announce what they
preserved — and a later pass could mistake "the vault survives teardown" for
"the vault survives everything" and either trust it as a system of record or try
to close (a) as a bug by committing the vault or mandating a mount. (a) is a
deliberate trade against putting unreviewed model output in git, bounded by
ADR-019; (b) is the price of a hook contract that must never darken a session;
and (c) is definitional for a channel agents opt into. Recording them keeps the
boundary legible: the vault is a durable *aid to recall*, not an authoritative
record.

**Recommended resolution:** None at the architecture level. Two follow-ups are
recorded rather than done: a periodic host-side export (a scheduled
`af memory export` on the host, which needs no factory change), and, if the
vault ever becomes an input to an automated decision rather than to an agent's
own reading, a proven-identity attribution to replace the claimed one in (c).
The only operator follow-up is cadence: export before removing a container, and
treat the `af up` staleness line as the reminder it exists to be.

---

## Meta

This list is not exhaustive — these are the gaps surfaced by the current
pass of `/architecture-docs`. Re-running the skill should find fewer
gaps as they are resolved. A growing gaps.md is a drift signal.
