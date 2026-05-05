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
- `IsAvailable`, `ClearHistory`, `RespawnPane` appear unreferenced.
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

## Meta

This list is not exhaustive — these are the gaps surfaced by the current
pass of `/architecture-docs`. Re-running the skill should find fewer
gaps as they are resolved. A growing gaps.md is a drift signal.
