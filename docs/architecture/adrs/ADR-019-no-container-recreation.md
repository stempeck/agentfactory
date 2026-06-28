# ADR-019: af changes must never require recreating or destroying an existing factory container

**Status:** Accepted
**Date:** 2026-06-21 (commit d45ee7f)

## Context

A factory runs inside a long-lived Docker container, created by the
`docker run -dit … bash --login` at `quickdocker.sh:486-493`. Customers operate
**hundreds of factories across dozens of containers each**, and they routinely
build those containers with their **own proprietary `quickdocker.sh`-like scripts**
to satisfy their own security and environment requirements — not only with the
in-repo `quickdocker.sh`. A container holds live, irreplaceable state: running
agent tmux sessions, the unconditionally-launched `af-watchdog`/`af-dispatch`
helpers (`internal/cmd/up.go:181-193`), in-progress worktrees, and the customer's
environment. Destroying the container destroys all of it.

Docker fixes several container properties **at creation time only** — most
notably published ports: `quickdocker.sh:486-493` runs `docker run -dit … "$BASE_IMAGE" bash --login`
with **no `-p`/`--publish`**, and a published port cannot be added to an
already-running container. The only way to "add a port" is `docker rm -f` +
re-`docker run`, which is exactly the destructive recreate the existing prompt
guards behind explicit operator consent (`quickdocker.sh:459-470`,
`docker rm -f "$CONTAINER_NAME"` only after a `y/N` answer).

This tension produced a concrete near-miss (issue #428). The `--web` web-console
add-on reaches a running container's loopback `webui` through a host-side
`docker exec` relay (`_web_bridge`, `quickdocker.sh:107-281`), invoked from an
early short-circuit that acts on an **already-running** container and `exit 0`s
**before** any create/recreate logic (`quickdocker.sh:325-337`). When that relay
broke on macOS, a root-cause analysis recommended replacing it with a
**`docker run -p` published port** — a fix that **requires recreating every
existing container** and only works for containers our own script built. The
project's `.designs/425` implementation plan had already rejected that exact
approach for this exact reason: *"That plan … required recreating every existing
container … rebuilding them is not an option and defeats the point of the feature
(it is for their existing factories)"*
(`.designs/425/implementation-plan/implementation_plan_outline.md:9-15`; the
withdrawn `-p`/recreate plan is preserved in commit `0a908ad`). The counterfactual
— "just publish a port / change the create-time flags" — keeps resurfacing because
it is the simplest fix *in isolation*; this ADR exists so that altitude is fenced
off as a standing constraint rather than re-litigated per feature.

This is the running-operations analog of **ADR-017** (af must not delete customer
*data*) and **ADR-018** (tests must not disturb a *running factory*): the same
"do not destroy what you do not own / what is already running" principle, applied
to the container itself.

## Decision

**No af change — architecture, feature, fix, or otherwise — may require recreating
or destroying an existing factory container, or depend on properties that can only
be set at container-creation time (published ports via `docker run -p` at
`quickdocker.sh:486-493`, baked-in `-e` env, network mode, mounts).** Any
capability added for an existing factory must work against an **already-running**
container, through runtime channels only (`docker exec`, host-side relays such as
`_web_bridge` at `quickdocker.sh:107-281`, files under the factory root), and must
not assume the container was built by our `quickdocker.sh` rather than a customer's
own script.

The embodying precedent is the `--web`/`--shell` early short-circuit
(`quickdocker.sh:325-337`), which deliberately resolves and acts on the running
container and exits before the create/recreate path; and the Rev-2 `_web_bridge`
relay (`quickdocker.sh:107-281`), which reaches `webui` from **inside** the
container so it needs **no** create-time flag, **no** `-p`, and **no** `server.go`
change. A fix that can only be realized by `docker rm -f` + `docker run` is, by
this ADR, the wrong altitude — the policy belongs at a runtime boundary, not the
container-creation boundary.

## Scope

Applies to every af capability that targets an **existing** factory: the web
console (`--web`), shells (`--shell`), and any future add-on, migration, or fix
delivered to running factories. It does **not** forbid the operator's own
**explicit, consented** (re)creation of a container via `quickdocker.sh <repo>`
(the `y/N` recreate prompt at `quickdocker.sh:459-470` is operator-initiated and
remains valid) — the constraint is that af must never make recreation a
**prerequisite** for a change to function. It does not constrain what a brand-new
container is created with at first `docker run`.

## Consequences

**Accepted costs:**
- Some fixes are harder. Reaching an already-running container's loopback service
  requires a runtime relay (host listener tool + in-container `docker exec`
  relay, `quickdocker.sh:107-281`) instead of a one-line `-p` publish — more code
  and more failure surface (the macOS host-tool bug, issue #428).
- Capabilities cannot rely on create-time guarantees; they must rediscover state
  at runtime (e.g. polling `.runtime/webui_server.json` for `webui`'s address,
  `quickdocker.sh:156-185`) rather than pinning it at `docker run`.
- A genuinely create-time-only improvement can only land for **new** containers;
  existing factories keep the old behavior until the operator chooses to recreate.

**Earned properties:**
- Customers' long-lived factories — and the live agent sessions, worktrees, and
  proprietary environments inside them — are never destroyed by an af feature or
  fix. The add-on stays a non-destructive add-on.
- Add-ons work on **any** running container regardless of how it was built, because
  they depend on nothing our `quickdocker.sh` set at create time.
- The "just publish a port / change create flags" altitude is settled once here,
  so it is not re-proposed (and re-rejected) per feature.

## Alternatives Considered (rejected)

- **`docker run -p` published port + fixed bind + `AF_TRUST_LOCAL`** (the original
  `.designs/425` design; commit `0a908ad`). Rejected: `-p` is create-time-only, so
  delivering it to existing factories means `docker rm -f` + recreate — destroying
  running operations for hundreds of customers — and it only works for containers
  our script created. This is the precise approach Rev-2 superseded
  (`.designs/425/implementation-plan/implementation_plan_outline.md:9-15`).
- **A one-time "recreate to upgrade" migration.** Rejected: at the customer's scale
  (dozens of long-lived containers each, many built by proprietary scripts) a
  recreate is operationally catastrophic and cannot be assumed safe or scripted by
  us; it contradicts ADR-017's "don't destroy what you don't own."

## Corpus links

- `quickdocker.sh:486-493` — container creation `docker run` with no `-p` (the create-time boundary this ADR refuses to depend on)
- `quickdocker.sh:325-337` — the `--web`/`--shell` short-circuit that acts on a running container and exits before create/recreate (the embodying precedent)
- `quickdocker.sh:107-281` — `_web_bridge` runtime relay reaching `webui` from inside the container (needs no create-time flag)
- `quickdocker.sh:459-470` — operator-consented recreate prompt (the permitted, explicit exception)
- `.designs/425/implementation-plan/implementation_plan_outline.md:9-15` — Rev-2 rejection of the `-p`/recreate plan (plan of record); original in commit `0a908ad`
- `todos/rootcause_analysis.md` — issue #428 RCA whose elevation verdict was reversed by this constraint
- Related ADRs: [ADR-017](ADR-017-no-customer-repo-mutations.md) (don't delete customer data — same principle, data), [ADR-018](ADR-018-tests-never-disturb-running-factory.md) (don't disturb a running factory — same principle, sessions)
