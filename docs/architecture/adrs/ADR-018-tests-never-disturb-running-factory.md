# ADR-018: Tests must never disturb a running factory

**Status:** Accepted
**Date:** 2026-06-05

## Amendment (2026-06-07 — issue #317)

The Decision below was implemented and then **superseded in its *ordering of controls*** by
issue #317, after incident #316 showed the #309 hermetic-by-default seam (Decision bullet 1)
could still be bypassed. This is an **amendment, not a status reversal** — the ADR remains
**Accepted**; only which control is **primary** changes.

**New PRIMARY control (behavioral, fail-closed, env-free, build-tag-gated):**

1. **Constructor GUARD.** In the default (non-`integration`) test build, `tmux.NewTmux()`
   (`internal/tmux/tmux.go`) returns a guarded client that **panics — naming the offending
   test — on any destructive op against a production identity** (`isProductionIdentity`:
   `af-` prefix and **not** `af-test-`) and no-ops read-only probes to a benign zero value.
   Production and the `integration` build return the unguarded real client via a
   `guard_default.go` / `guard_integration.go` **build-tag split**. A production-reaching tmux
   client is therefore **unconstructable in the default test build** — no author discipline,
   no environment variable, and no per-test wiring required.
2. **`TMUX_TMPDIR` ISOLATE.** Every package's `//go:build !integration` `TestMain` (via
   `internal/testsupport/tmuxisolation`) redirects `TMUX_TMPDIR` to a private throwaway
   directory, unsets `$TMUX`, and reaps at exit — routing the whole test process tree's tmux
   to a disposable server, so even a raw `exec.Command("tmux", …)` cannot reach the operator's
   real socket.

Both are **env-free and build-tag-gated** — selected at compile time by `-tags=integration`,
never read from the environment at runtime. That is precisely why they satisfy the ADR-004
hermeticity invariant that the originally-drafted `os.Getenv("AF_TMUX_SOCKET")` runtime guard
violated (see the narrowly-superseded alternative under *Alternatives Considered*, below).

**Demoted to SECONDARY (defense-in-depth):** the #309 hermetic-by-default seam (Decision
bullet 1, originally labeled "primary") and the **source-scan**
`TestNoRawDestructiveTmuxInUntaggedTests` (Decision bullet 4) are retained as
belt-and-suspenders layers beneath the new primary — they catch a regression early but are no
longer the load-bearing interlock. The test file's own header comment carries the matching
"secondary / defense-in-depth" framing.

**Enabling behaviors that let #316 happen** (enumerated for the six-sigma root-cause record):

- **#303 / #316 untagged tests** issued real `tmux kill-session`/`new-session` against
  production-class names from the default suite.
- **#300 `GOTMPDIR`** redirected the test temp dir to an **exec-allowed** location, removing the
  *accidental* `noexec`-`/tmp` safety that had been silently masking the hazard.
- **#315 opt-in seams + the source-scan blind spot.** The hermetic seams were opt-in (a test had
  to install the fake), and the scan bans only *raw* destructive literals — neither stops a test
  that obtains a real client through a seam default or a direct `NewTmux()`.
- **Ten non-test `tmux.NewTmux()` construction sites** funnel into the real client — **7 direct
  call sites plus 3 seam factory vars**: `newCmdTmux` (`internal/cmd/helpers.go`),
  `newWatchdogTmux` (`internal/cmd/watchdog.go`), and `newManagerTmux`
  (`internal/session/session.go`). The **un-swapped watchdog seam** (`newWatchdogTmux`) was in
  particular never wired to the fake. Because all ten route through `NewTmux()`, guarding the
  **constructor** covers every one of them automatically — the single chokepoint the new
  primary exploits.

## Context

agentfactory is operated as a long-lived multi-agent system *and* developed on the
same machine. It is normal for a developer (or an autonomous implementation loop) to
run `make test` / `go test ./...` on a host that is simultaneously running a live
factory (`af up manager`, agents, `af-watchdog`, `af-dispatch`).

Two structural facts let the test suite reach into and destroy a co-tenant factory
(issue #309, reproduced live):

1. **Global, un-namespaced session names.** `SessionName(agent)` =
   `session.Prefix + agent` = `"af-" + agent` (`internal/session/names.go:7,10-12`),
   with no factory-root/test differentiation. Production and tests compute
   *byte-identical* names. The watchdog and dispatcher names bypass `SessionName`
   entirely as literals (`up.go:182` `Prefix+"watchdog"`, `down.go:102`,
   `dispatch.go:431` `const dispatchSessionName = "af-dispatch"`).
2. **No seam between test and production tmux.** The test suite reaches the global
   namespace through the **same** concrete `*tmux.Tmux` client production uses.
   Default-suite (untagged) tests issue real `tmux new-session`/`kill-session`
   against production-class names — e.g. `dispatch_test.go:558` unconditionally runs
   `tmux kill-session -t af-dispatch`, and the real `runUp` always launches the global
   `af-watchdog` (`up.go:181-193`). `t.TempDir()` then deletes the temp factory,
   stranding sessions in a `(deleted)` cwd that die on the unconditional first-line
   `getWd()` (`watchdog.go:210`, `helpers.go:91`), and detached
   `python3 -m py.issuestore.server` children leak (185 orphans observed live;
   `lifecycle.go:165-187`).

This is the same class of "code reached into shared space and destroyed whatever it
found" that **ADR-017** forbids for customer data — here applied to **running factory
state**.

## Decision

**Automated tests must never stop, kill, replace, or otherwise disturb a concurrently
running factory's agent sessions, monitoring session, or background helper processes —
even when agent names collide and even under a redirected `TMPDIR`/`GOTMPDIR` that lets
test binaries exec.** A test run must be safe to execute in the same container as a
live factory.

This is enforced by **defense-in-depth via existing house mechanisms (ADR-009 package-var
seams), not author discipline:**

1. **Hermetic-by-default tmux (capability removal — primary).** Promote the
   side-effecting tmux client to ADR-009-style package-var seams — `newManagerTmux`
   (`internal/session`) and `newCmdTmux` (`internal/cmd`) over a minimal
   `tmuxClient`/`cmdTmux` interface — whose **defaults return the real
   `tmux.NewTmux()`** (production unchanged). The default test suite installs an
   in-memory **fake** so it issues **zero** real tmux operations. Capability is
   *removed*, not merely redirected (least privilege). A `var _ tmuxClient =
   (*tmux.Tmux)(nil)` compile-assertion + an interface-drift test keep the fake honest.

2. **Test-only per-run namespace (second, independent layer).** A `sessionPrefixFn`
   package-var seam in `internal/session/names.go` (default returns `Prefix`) lets any
   test that *does* use real tmux run under an `af-test-<hex>-` prefix that can never
   equal a production `af-<agent>` name. **Production names stay byte-identical.** The
   seam is **set directly by tests — never read from the environment** (see the ADR-004
   constraint below).

3. **One naming authority.** Route the watchdog and dispatcher names (`up.go:182`,
   `down.go:102`, `dispatch.go:431`) through a single authority so no literal escapes
   the namespace fix (closes the duplicated-ownership gap).

4. **Structural enforcement.** An untagged source-scan test
   (`TestNoRawDestructiveTmuxInUntaggedTests`) bans raw `exec.Command("tmux",…)` /
   `new-session` / `kill-session` / `pkill` in non-integration test files (allowlist
   `//go:build integration`), plus a pure-fake behavioral isolation test and an
   integration real-sentinel test. Doc-only enforcement is insufficient — it failed once
   already (#303).

5. **Lifetime-bounded children.** The issuestore server is **memstore-by-default** in
   tests (via the existing `newIssueStore` seam), so the default suite spawns no
   `py.issuestore.server` child to orphan. For integration tests / real `af` runs that
   *do* spawn it, give the process its own process group (`SysProcAttr{Setpgid:true}`)
   and reap the group on teardown (and/or have the server self-exit when its
   cwd/endpoint file disappears), so an interrupted run cannot leak it
   (`lifecycle.go:165-187`).

6. **Monitor robustness.** The watchdog launch is gated on **health** (a
   watchdog-specific liveness check — *not* `IsClaudeRunning`, since the watchdog pane
   runs `af`, not `claude`), so a present-but-dead `af-watchdog` is killed and
   relaunched. `runWatchdog` resolves its factory root from `AF_ROOT` before `getWd()`
   and `af up` exports `AF_ROOT` into the watchdog session (it does not today), so the
   monitor no longer dies on a deleted cwd.

This does **not** weaken integration coverage: integration-tagged tests still hit the
real `af up`/dispatch/session/tmux paths (on the test-only namespace) and still verify
that a genuinely dead production session is recreated — they simply must run in CI so the
real path keeps coverage.

## Scope

Applies to every test that can create, kill, recreate, or send keys to a tmux session,
or spawn a long-lived child process (`internal/cmd`, `internal/session`, `internal/tmux`,
`internal/worktree`, `internal/issuestore/mcpstore`). It governs the **test harness**, not
agents at runtime — agents legitimately manage real sessions.

## Consequences

- A developer or autonomous loop can run the full suite on a live-factory host without
  losing the operator's manager/watchdog/dispatch sessions or leaking helper processes
  (#309 AC-1..AC-3).
- Production session naming and operator ergonomics are unchanged — isolation lives in the
  seam/test layer, not the name scheme (AC-5).
- A new isolation test + source-scan lint fail CI if a future change lets a test reach real
  tmux or a production-named session (AC-6).
- Cost: tmux-touching tests must use `setupHermeticSessions(t)`; the package-var seams are
  mutable, so seam-mutating tests must restore on `t.Cleanup` and must **not** `t.Parallel`.
- Accepted residual: `t.Cleanup` does not run on SIGKILL (integration tests are quarantined
  and reaped); `pkill -9 -f "claude…"` on an operator-run `af down --all` still reaps
  co-tenants on a shared host (ADR-017-sanctioned for infra commands; made unreachable from
  the default suite via a seam).

## Alternatives Considered (rejected)

- **Per-test tmux server socket (`-L`/`AF_TMUX_SOCKET`) + runtime guard inside
  `internal/tmux`.** *(This was the mechanism in the first draft of this ADR; rejected.)*
  Reading `os.Getenv("AF_TMUX_SOCKET")` inside `internal/tmux` **fails the enforced
  hermeticity invariant**: `TestNoEnvReadsInLibraryPackages`
  (`internal/cmd/env_hermetic_test.go:42-53`) walks `internal/` and skips **only**
  `internal/cmd/`, flagging any `os.Getenv` elsewhere — so an env-read in `internal/tmux`
  (or `internal/session`) violates **ADR-004**. (Note ADR-004's *prose* also exempts
  `internal/session`, but the *enforcement test* exempts only `internal/cmd`; the test is the
  binding contract.) Putting `testing.Testing()` in a library package is independently a
  test-awareness smell. A *parameter-plumbed* socket (env read at the `internal/cmd`
  boundary, passed down) would be ADR-004-compliant but converges toward the seam approach
  while still adding a per-test tmux-server **orphan-reaping** class — strictly worse than
  the hermetic fake. **Superseded by the seam.**
  *(#317 narrow supersession — see the Amendment above. The rejection of an **`os.Getenv`
  runtime guard inside `internal/tmux`** still stands; #317's primary control reaches the same
  fail-closed goal by a **different, ADR-004-compliant mechanism**: an **env-free, server-free,
  build-tag-gated** constructor GUARD selected at compile time, which reads **no** environment
  variable and adds **no** per-test tmux server. The rejection of the **per-test / library-package
  tmux-server socket** (and of library-package `os.Getenv` reads) is **NOT reversed** — only the
  *runtime env-guard* framing is superseded, and even then by a build-tag default rather than by
  the socket this ADR rejected.)*
- **Production per-factory-root namespace** (`SessionName(root, agent)`). Changes
  operator-visible names, breaks pinned `names_test.go`, contradicts the intentional
  `af-<agent>` inter-agent addressing scheme (commit `32af131`), and risks mis-addressing if
  a reader/writer resolve different roots. Held in reserve only if production-side
  defense-in-depth is ever required (seed derived from `FindFactoryRoot`, plumbed as a
  parameter — never env-read).
- **Build-tag-only gating / noexec-`/tmp`-dependent skipping.** Relies on the *accidental*
  safety that broke when the harness redirected `TMPDIR`/`GOTMPDIR` to an exec-allowed dir;
  not a real interlock.
- **Auto-handling the "Do you trust the files in this folder?" dialog.** Out of scope: it is
  a *downstream symptom* of relaunch-into-a-foreign-dir (eliminating the collision removes the
  trigger), and auto-advancing a trust prompt for an unvetted directory risks **ADR-014**
  (no interactive prompting / no auto-trust). Closed indirectly.

## Corpus links

- `internal/session/names.go:7-12` — the global, un-namespaced name function (the seam target)
- `internal/config/config.go:48-53` — `"dispatch"` reserved (names are a global address)
- `internal/cmd/up.go:181-193` — unconditional global `af-watchdog` launch (#288)
- `internal/cmd/dispatch_test.go:527,532,558` — real `af-dispatch` create/kill (default suite)
- `internal/cmd/env_hermetic_test.go:42-53` — `TestNoEnvReadsInLibraryPackages` (the ADR-004
  enforcement that rejects the socket/env-read mechanism)
- `internal/cmd/watchdog.go:210` — fatal first-line `getWd()` (monitor death)
- `internal/issuestore/mcpstore/lifecycle.go:165-187` — detached server spawn (no
  process-group / `Pdeathsig`)
- `.designs/309/` — issue #309 problem statement, acceptance criteria, the root cause analysis
  (12 concerns; 11 VALIDATED, 1 PARTIAL), the design-doc, and the analyst cross-review
- Related ADRs: [ADR-009](ADR-009-package-var-seams.md) (the package-var seam house style this
  decision builds on), [ADR-004](ADR-004-library-env-hermeticity.md) (the env-hermeticity
  invariant that selects the seam over the socket), [ADR-017](ADR-017-no-customer-repo-mutations.md)
  (don't destroy what you don't own — same principle, customer data), [ADR-014](ADR-014-no-interactive-prompting.md)
  (why the trust-dialog handler is out of scope)
- Related issues: #297 (watchdog self-recovery), #288 (watchdog introduction), #303 (doc-only
  enforcement proved insufficient)
