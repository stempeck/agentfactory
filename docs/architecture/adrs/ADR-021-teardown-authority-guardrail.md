# ADR-021: Agent-initiated factory-wide teardown is foreclosed by an in-process authority guardrail

**Status:** Accepted
**Date:** 2026-07-14
**Updated:** 2026-07-18 (#548 — the scoped `af down <agent>` axis elevated from the
#541 binary model into three tiers; factory-wide axis unchanged)

## Context

An agent runs as an autonomous Claude process inside an af-managed tmux session
(`af-<agent>`); the human operator drives the factory from a host shell. The two
share one uid and one working tree. Twice, an *agent* has torn down the whole
factory — issue #541's incident, and the earlier `make test-integration` crash
recorded at `internal/testsupport/tmuxisolation/ciguard.go:16-18`. Both were the
*accidental* class: a factory-wide `af down --all` / `af install --agents` /
`af dispatch stop` (or a raw kill) reached from inside an agent session, killing
that session, every sibling agent, and the interactive manager.

The only true capability removal — per-agent OS users — is barred as a mandatory
change by ADR-019 (no af change may require recreating an existing container) and
by the shared-state topology both trust domains read-write. Every other
process/privilege option fails on mechanism at the same uid: uid-keyed tmux
sockets, PID namespaces that don't block socket-mediated `KillSession`, a
same-uid broker that is itself killable. So a hard security boundary is not
reachable in-repo under ADR-019.

**Elevation (#548).** #541 admitted only a binary Operator/Agent split, which was
too coarse for the *scoped* question — *who may run `af down <agent>` against one
named target*, as opposed to factory-wide teardown. Two roles the factory depends
on were misclassified: a genuine dispatcher could not release the specialist it
dispatched (its provenance was borrowed from a mail-routing artifact whose lifetime
is wrong for authorization), and the interactive manager — the operator's own fleet
proxy — collapsed into the Agent class and could stop only itself. #548 elevates the
scoped axis into three tiers — **self**, **dispatcher-scoped**, and
**manager-scoped** — while the factory-wide axis (bare `af down` / `--all` /
`--reset` / `af install --agents` / `af dispatch stop`) stays **operator-only** and
byte-identical. The tiers live only inside the per-target scoped check; the
factory-wide disqualifiers are evaluated first, so a tier can never widen a
factory-wide shape.

## Decision

**Foreclose agent-initiated factory-wide teardown as a class with an in-process,
fail-closed authority guardrail — accepted deliberately as a guardrail, not a
boundary — and authorize the scoped `af down <agent>` axis by a three-tier
caller/target model.**

1. **Classify the caller (K3).** `callerAuthority` (`internal/cmd/authority.go:75`)
   returns `AuthorityAgent` if either signal fires: `AF_ROLE` is set, or the
   caller's own tmux session name is an af production identity (the signal that
   survives an `env -u AF_ROLE` scrub). It is fail-closed — only a no-signal
   caller resolves `AuthorityOperator`. `requireOperatorTeardown`
   (`internal/cmd/authority.go:101`) is the single gate every factory-wide
   teardown-class surface calls; it returns `nil` for an operator (byte-for-byte
   unchanged) and the K1 refusal (`internal/cmd/authority.go:25-32`) for an agent,
   appending a best-effort K4 forensic breadcrumb.

2. **Gate the command surfaces (K5/K6/K7) with a three-tier scoped carve-out (K11).**
   `runDown` (`internal/cmd/down.go`, `downSelfScoped`), `runInstallAgents`
   (`internal/cmd/install.go:699`), and `runDispatchStop`
   (`internal/cmd/dispatch.go`) refuse factory-wide shapes in agent context. The
   scoped per-target check is one fail-closed decision function,
   `scopedStopAllowed` (`internal/cmd/authority.go:165-177`), with three ordered
   tiers (any resolution error refuses):
   - **self** — the target is the caller's own session (`isSelfSession`, unchanged);
   - **dispatcher-scoped** — the target is a formula-backed specialist the caller
     dispatched (`isSpecialistTarget && callerDispatched`; predicate unchanged,
     substrate repaired — see Decision 5);
   - **manager-scoped** — the caller resolves to an **interactive**-type entry AND
     the target is an **autonomous, formula-backed** worker specialist
     (`isSpecialistTarget(target) && target.Type == "autonomous" && callerIsInteractive`).
     Keyed on the interactive type only — no `"manager"` name literal.

   The `sling --reset` running-session stop reuses the same function
   (`internal/cmd/sling.go:160-175`), so AC-3's sibling-refusal invariant holds on
   **every** surface that can stop a named target. Bare / `--all` / `--reset` / a
   non-dispatched sibling still refuse. On the allow path an agent-context scoped
   stop appends a best-effort **granted-stop** breadcrumb (ts, caller, granting
   tier, target, argv) to the caller's `.runtime/teardown_granted`
   (`writeTeardownGrantedArtifact`, `internal/cmd/authority.go:292-316`), mirroring
   the K4 refusal-breadcrumb idiom.

3. **Backstop the primitives structurally (K8/K9/K10) so a missing gate is
   non-fatal (C-1).** The `authKillGuard` decorator wraps every cmd-layer
   `KillSession` (`internal/cmd/helpers.go:67-86`); `Manager.Stop` refuses to stop
   an interactive agent from agent context (`internal/session/session.go:715`) —
   which is exactly why the manager tier targets autonomous entries only; the
   orphan-sweep `pkill` is behind the `runPkill` seam
   (`internal/cmd/down.go:269-294`). A structural scanner
   (`internal/cmd/teardown_scanner_enforce_test.go`, K13) fails the build if a
   future surface reaches a teardown primitive without a gate. The watchdog's
   untargetability is hardened from a naming convention to an interlock:
   `"watchdog"` is now a reserved name (`internal/config/config.go:75`), rejected at
   load time by `ValidateAgentName` (`:314-315`) symmetric with the `"dispatch"`
   reservation, so no roster entry can alias the `af-watchdog` control-plane session.

4. **The interactive manager is agent-class for factory-wide teardown only.** It
   runs `AF_ROLE=manager` inside `af-manager` and, for factory-wide shapes, exempting
   it would create a confused deputy — teardown typed into the manager's Claude pane
   is refused and redirected; the operator channel is a host shell (or any non-af
   context). The **manager-scoped** tier of Decision 2 is a deliberate,
   ADR-recorded extension of this classification, not a silent reversal: the manager
   may run a *scoped* `af down <worker>` against an autonomous formula-backed worker
   (dispatched or not) so the human can direct fleet teardown through the manager in
   a crisis, but factory-wide teardown remains operator-only. Scoped `--reset` rides
   the tier grant (amended post-#553): every tier already carried the identical
   stop + state-reset authority via `sling --reset`, so a down-side refusal only
   forced the sling spelling — the authorization matrix below governs both surfaces.

5. **Provenance handle separation (Decision 2, superseded).** #541 recorded
   dispatch provenance as `.runtime/formula_caller`. That file is a **mail-routing**
   artifact whose formula-scoped delete-at-completion lifecycle
   (`internal/cmd/done.go:574`) is correct for WORK_DONE mail but wrong for
   authorization, which needs session-scoped lifetime. Authorization gets its own
   datum, **`.runtime/dispatch_owner`** (`internal/cmd/sling.go:249`), written only
   on the dispatch path (never by self-instantiation — a self-slung formula must not
   mint stop-rights), surviving formula completion (that survival **is** AC-1),
   destroyed when the session stops (`finishDispatchedSession`,
   `internal/cmd/done.go:333`; `af down` stop cleanup; `resetAgentState`) and cleared
   on relaunch (`internal/cmd/up.go:235,238`). `formula_caller` is left byte-identical
   for mail. `callerDispatched` reads `dispatch_owner` first and **falls back to
   `formula_caller`** at the same resolved dir when it is absent
   (`internal/cmd/authority.go:242-247`), keeping mid-formula releases for
   specialists dispatched by a pre-upgrade binary.

6. **Boundary decisions and the resolver escape.**
   - **C-3 CONFIRMED for factory-wide shapes; scoped `--reset` narrowed post-#553** —
     bare `af down`, `--all`, no-target `--reset`, and the transitive
     install/regen/dispatch-stop paths stay refused for agents *and* the manager.
     Scoped `--reset` was initially kept operator-only, then aligned with the
     authorization matrix below: a granted tier covers `--reset` on its targets,
     exactly as `sling --reset` already did, so the down and sling surfaces share one
     authority model. The kept-worktree hint points at `af down <agent> --reset`
     again.
   - **C-4 safe reading** — boundary decision #2 preserves control-plane protection:
     the manager tier reaches only autonomous, formula-backed worker specialists. The
     manager itself and any future interactive agent (type check), the supervisor
     (`Formula == ""` — not a specialist target), and the watchdog/dispatcher
     (control-plane sessions, never agents.json entries in the scoped path) stay
     operator-only. Control-plane reach was proposed and **rejected**.
   - **T-INT-4** — `callerDispatched` gains an **enclosing-root fallback**
     (`config.FindEnclosingRoot`, `internal/cmd/authority.go:229`): on a local-root
     miss it retries the read at the target's own agent dir, so a target registered
     in its own worktree is found from any caller location. This is a fallback **read
     location**, not a resolver unification (the four resolvers stay distinct per
     commit `4ff8d457`) — recorded here as a deliberate resolver decision.

**Authorization matrix (scoped `af down <target>` / `sling --reset`).** Factory-wide
shapes are refused for every non-operator caller and are not in this table.

| Caller ↓ / Target → | Own session (self) | Own dispatched specialist | Non-dispatched autonomous worker | Control plane (manager / supervisor / watchdog / dispatcher) |
|---|---|---|---|---|
| Operator (host shell / non-af) | allow | allow | allow | allow |
| Interactive manager | allow | allow | **allow (manager tier)** | refuse |
| Dispatcher agent | allow | **allow (dispatcher tier)** | refuse | refuse |
| Non-dispatching sibling agent | allow | refuse | refuse | refuse |

**This is a guardrail against accidental invocation, never a security boundary.**
The phrase is normative and appears unqualified here and in
`USING_AGENTFACTORY.md`. No doc may call the guardrail a "boundary" or say it
"prevents" teardown. It applies to the new tiers exactly as to the factory-wide
axis: every tier signal (`AF_ROLE`, cwd-derived identity, the agents.json roster,
the provenance datum) is forgeable by a determined same-uid process, so the tiers
raise the guardrail against *accidental* scoped stops, they do not bound a
malicious one.

## Consequences

- The accident class — the actor that has actually caused harm — is stopped at
  the point of action for factory-wide shapes, and the fail-closed guardrail
  deploys binary-only across the existing fleet (the only ADR-019-compatible
  unconditional rollout).
- Every **operator** path (host shell, CI, `make check-regen`) and every
  **sanctioned** scoped stop — an orchestrator releasing its own dispatched worker,
  or the manager stopping an autonomous worker in a crisis — keep working
  byte-for-byte. The regen path is the sharpest deployment risk and is pinned by an
  operator-context acceptance test.
- **Owned residuals, accepted explicitly (none silently):** R1 `env -u AF_ROLE`
  outside any af pane; R2 raw `pkill`/`kill`/`tmux kill-server` at the same uid;
  R3 a loopback call to the web console's down endpoints; R4 a hermetic harness
  that scrubs env and runs outside a pane. **R5 (mail-driven confused deputy) —
  reopened-narrow-accepted (C-5).** The manager-scoped tier means a manager acting on
  a forged or mistaken mailed request can scope-stop a worker it was told to stop.
  Corrected blast radius: scoped multi-target enumeration can reach the whole
  **worker fleet** in one command, **never the control plane** (manager/supervisor/
  watchdog/dispatcher are unreachable in the scoped path); the state is recoverable
  via re-sling / `af up` plus the surviving worktree / bead / `hooked_formula`
  substrate — the watchdog does **not** bring a cleanly-downed session back
  (`pollAgents` skips non-running sessions, `internal/cmd/watchdog.go:481-484`, and
  monitors only `startup.json.watchdog_agents`). Mitigants: human supervision, the
  factory-wide refusals still intact, and the granted-stop audit trail
  (`.runtime/teardown_granted`) that makes an enumeration reconstructable. The
  manager-side mitigation is guidance to verify mailed teardown requests
  independently (`af agents list`), not a change to the verbatim K1 refusal text.
- **Additional owned residuals:**
  - **Tier-spoof (Gap 4).** `AF_ROLE`, the cwd-derived identity, a runtime
    `agents.json` edit, and `sling --caller` (which mints `dispatch_owner` directly,
    `internal/cmd/sling.go:240,249`) are all forgeable at the same uid, so any tier
    signal can be spoofed. Uncloseable under ADR-019/ADR-021; this is the accepted
    ceiling, not a defect.
  - **Stale `dispatch_owner` after abnormal session death (RS-D1).** If a dispatched
    session dies without running its stop/reset/relaunch cleanup, its `dispatch_owner`
    can briefly outlive it. The window is bounded by the mandatory relaunch-clear
    (`up.go:235,238`), stop cleanup, and `--reset`.
  - **`sling --reset` stop surface (RS-3).** Now gated by `scopedStopAllowed`, with a
    not-running carve-out (first-dispatch / redispatch-after-crash proceed) and a
    **dispatch-daemon carve-out** — the `af dispatch` daemon re-slings specialists it
    did not dispatch and is admitted by its own session identity
    (`CurrentSessionName() == DispatchSessionName()`), whose same-uid spoofability is
    Gap 4's ceiling, no worse than the tier signals.
  - **Runtime roster edits (Gap 13).** A same-uid edit of `agents.json` between load
    and check is ungoverned by the CI type-pin. The **watchdog-aliasing half** of this
    class is **CLOSED**, not a residual: `"watchdog"` is a reserved name
    (`config.go:75`, H-2 interlock), so no roster entry can alias the control-plane
    session. *Upgrade note:* because `ValidateAgentName` runs on every `LoadAgentConfig`,
    a pre-existing `agents.json` entry literally named `watchdog` now fails to load —
    every `af` command errors until it is renamed; none is expected to exist (the
    reservation closes exactly that latent alias).
  - **R6 (a future ungated surface)** is **not** accepted — it is closed structurally
    by K8/K9/K10 + the K13 scanner.

  See `.designs/541/design-doc.md` (Same-User Capability Boundary Decision, R1–R6
  table) and `.designs/548/design-doc.md` (tier model, corrected R5 blast radius) —
  the guardrail must not be documented as closing R1–R5.
- Because the guardrail is in-process at the same uid, it can be circumvented by a
  determined same-user process; that is the accepted ceiling, not a defect.

## Alternatives considered

- **Store-altitude provenance** (dispatch provenance as a durable issuestore record
  instead of a `.runtime` datum). Rejected: the formula-instance epic is *closed* at
  completion (`done.go`), so lifetime must be redefined regardless, and it would put a
  new authorization-time dependency on the Python MCP server (client/start timeouts)
  inside a refusal path that is zero-op today. The session-scoped file datum plus the
  enclosing-root fallback read buys the same root-invariance without that coupling.
- **A capability/authority enum** (a third `Authority` value). Rejected: existing
  `== AuthorityAgent` consumers would fail *open* if a new value slipped past them;
  the tiers live in the decision function and this ADR, not in the `Authority` type.

## Corpus links

- ADR-006 — loopback console trust ceiling (same machine = same user); governs R3
- ADR-009 — package-var seams for shell-outs (the K10 `runPkill` idiom)
- ADR-019 — no container recreation; bars per-agent-uid as a mandatory change
- `internal/cmd/authority.go:25-32` — K1 refusal message (normative body)
- `internal/cmd/authority.go:75,101` — K3 classifier + single factory-wide teardown gate
- `internal/cmd/authority.go:165-177` — `scopedStopAllowed` three-tier decision function
- `internal/cmd/authority.go:229,242-247` — enclosing-root fallback (T-INT-4) + `formula_caller` compat read
- `internal/cmd/authority.go:292-316` — `writeTeardownGrantedArtifact` (granted-stop audit)
- `internal/cmd/down.go` — K5 `runDown` gate + K11 carve-out; K10 `runPkill` seam (:269-294)
- `internal/cmd/sling.go:160-175,249` — `sling --reset` gate + `dispatch_owner` write
- `internal/cmd/done.go:333,574` — `dispatch_owner` session-end delete; `formula_caller` completion delete (unchanged)
- `internal/cmd/up.go:235,238` — relaunch-clear of `dispatch_owner`
- `internal/cmd/watchdog.go:481-484` — `pollAgents` skips non-running sessions (R5 basis)
- `internal/config/config.go:75,314-315` — `"watchdog"` reserved-name interlock (H-2)
- `internal/cmd/install.go:699` — K6 `af install --agents` refusal
- `internal/cmd/dispatch.go` — K7 `af dispatch stop` refusal
- `internal/cmd/helpers.go:67-86` — K8 `authKillGuard` KillSession decorator
- `internal/session/session.go:715` — K9 `Manager.Stop` interlock
- `internal/cmd/teardown_scanner_enforce_test.go` — K13 structural scanner
- `.designs/541/design-doc.md` — Same-User Capability Boundary Decision; R1–R6 residuals
- `.designs/548/design-doc.md` — scoped-stop tier model; corrected R5 blast radius
