# D4 — Scalability (scale.md)

Owner of (verification.md): AC-6 (with Integration, UX). AC-6: "consistent
successful outcomes out of each agent … systemic improvements." The scalability
question for THIS change is narrow: a one-time bootstrap of a single config file
adds essentially no runtime hot path. The relevant "scale" dimensions are
(a) bootstrap-time cost (one git/gh call at install), and (b) the steady-state
dispatch-loop load that the now-populated default ENABLES — i.e. the dispatcher
will, for the first time on a fresh repo, actually poll and dispatch.

CONSTRAINT-SENSITIVE NOTE (simplicity): No option below adds a caching layer, a
queue, a daemon, or any new dependency. C-4 (codebase is truth) and the overall
"narrow surface" scope (source.md:150-154) make heavyweight scale machinery
inappropriate and out of scope. Any such proposal is REJECTED on scope grounds.

---

## S1. Bootstrap-time cost of repo discovery

### Option S1.1 — Single best-effort `gh repo view` / `git remote` call at install — RECOMMENDED

D1/A2 adds exactly ONE subprocess call (`gh repo view --json nameWithOwner`, or
`git remote get-url origin`) to `runInstallInit`. This runs once, at install, never
on a hot path.

- Trade-offs: Negligible cost (one process spawn during a setup that already spawns
  `gh`, builds Go, and starts an MCP server — quickstart.sh phases, verified). No
  steady-state impact. Bounded by a timeout so a hung `gh` (e.g. network) cannot
  stall install indefinitely.
- Reversibility: Easy.
- Constraints: satisfies AC-6 (systemic, one-time); no simplicity violation.
  Recommended.

### Option S1.2 — Cache the discovered repo and re-validate it on every install run — REJECTED

Persist discovery results and re-check/refresh them across runs.

- REJECTED: violates simplicity (no constraint requires it) and conflicts with
  [C-2]/[ADR-017]. Discovery is a once-at-first-create concern (C-2); a caching/
  refresh layer is unjustified complexity for a value written once
  (install.go:152 write-if-absent, verified) and risks re-writing a customer-edited
  `repos` (ADR-017). No AC or constraint asks for repeated re-discovery.

---

## S2. Steady-state dispatch load enabled by the populated default

### Option S2.1 — Inherit the existing poll cadence and TTLs unchanged (interval 300s, retry 1800s, 24h dedup TTL) — RECOMMENDED

The default ships `interval_seconds:300`, `retry_after_seconds:1800` (matching the
struct defaults, dispatch.go:176,179, verified), and the dispatcher's 24h
already-dispatched TTL (codebase-snapshot §4, dispatch.go cycle step 5) is
unchanged. The populated default simply means the dispatcher now has repos+mappings
to act on, instead of polling nothing.

- Trade-offs: No new tuning surface; the cadence is the same the system already
  uses everywhere. A 5-minute poll against one repo via `gh` is trivial load. The
  query is capped at 50 results per repo (dispatch.go:182, verified) with a
  truncation warning, so a label-flood on one repo degrades gracefully rather than
  unboundedly.
- Reversibility: Easy (the values are just defaults the operator can override).
- Constraints: satisfies AC-6 (consistent, predictable cadence). Recommended.

### Option S2.2 — Lower the default interval for snappier first-run feedback — REJECTED

Ship a smaller `interval_seconds` so the new operator sees dispatch happen sooner.

- REJECTED: violates simplicity and [C-1]'s "default … with agentfactory" intent
  of a sensible, baked-in default. The struct default IS 300s
  (dispatch.go:176, verified); shipping a different value creates a second source
  of truth for the cadence and a faster poll multiplies `gh` API calls for no AC
  benefit (no AC mentions latency). Diverging the baked default from the validated
  struct default is exactly the drift the single-source pattern (D1/A1.1) avoids.

---

## S3. Multi-repo / fleet scaling of the default

### Option S3.1 — Default `repos` holds exactly the one discovered repo; multi-repo is operator-extended — RECOMMENDED

The discovered `owner/name` is the sole entry (D2/D2.3-A). The dispatcher already
iterates `dispatchCfg.Repos` (dispatch.go:172, verified), so an operator who later
adds repos gets fan-out for free with no code change.

- Trade-offs: The baked-in default is correct for the single-repo bootstrap
  scenario the source describes (one container, one cloned repo — source.md:87-92).
  Scaling to many repos is a deliberate operator action, not a default — keeping
  the default minimal and correct (AC-3: the ACTUAL repo).
- Reversibility: Easy.
- Constraints: satisfies AC-3, C-3 (the actual discovered repo). Recommended.

## Reversibility (this dimension): Easy

No hot-path code, no new infra; one bounded install-time call and reuse of existing
dispatch cadence.

## Dependencies produced

- PROVIDES to **API (D1)**: the requirement that the discovery call be bounded by a
  timeout (so install cannot hang).
- PROVIDES to **Data (D2)**: confirmation that the struct-default cadence values
  (300/1800) should be the shipped values (no divergence).
- REQUIRES from **Integration (D6)**: confirmation that the dispatcher's existing
  50-result cap and 24h TTL remain unchanged (they do — codebase-snapshot §4).

## Risks identified

| Risk | Severity | Mitigation |
|------|----------|------------|
| `gh repo view` hangs at install (network), stalling `af install --init` | Medium | Bound the discovery call with a context timeout; on timeout, fall to A3.1 degraded default (warn-don't-abort) |
| Populated default + dispatcher auto-start means a fresh repo now polls `gh` every 5 min where before it polled nothing | Low | This is the intended AC-2 behavior; 300s cadence + 50-result cap (dispatch.go:182) bound the load |
| Many labeled issues on first run dispatch a burst of slings | Low | Dispatcher's busy-agent skip + 24h dedup TTL (codebase-snapshot §4) throttle re-dispatch; worktree cap (factory.json MaxWorktrees=4, config.go:116 verified) bounds concurrent agent work |
