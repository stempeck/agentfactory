# dependencies.md — Dependency Graph

Code-level component dependencies for the dispatch.json baked-in default, plus the
critical-path build order. All cited paths/lines are verified this session or in
codebase-snapshot.md.

---

## Components (the units this design adds or touches)

| Id | Component | Location (verified) | New / Modified |
|----|-----------|---------------------|----------------|
| K1 | `DefaultDispatchConfigJSON(repo string) string` | `internal/config` (new fn, beside `DefaultFactoryConfigJSON`, config.go:111) | NEW |
| K2 | Repo-discovery helper (`gh repo view` / `git remote` → validated `owner/name`) | `internal/cmd/install.go` (new, inside/near `runInstallInit`, :97+) | NEW |
| K3 | Repo-string validator (strict `owner/name` allowlist) | `internal/cmd` or `internal/config` (new) | NEW |
| K4 | `runInstallInit` starter-config wiring | `internal/cmd/install.go:139-157` (modify :145 to call K1) | MODIFIED |
| K5 | quickstart specialist provisioning (`af install --agents`) | `quickstart.sh` `configure_factory` (:414-471) | MODIFIED |
| K6 | Dispatcher unknown-agent tolerance (skip-and-warn, dispatch-loop caller only) | `internal/cmd/dispatch.go` dispatch loop (:160+) + `ValidateDispatchConfig` caller boundary | MODIFIED (optional, defense-in-depth) |
| K7 | Golden/round-trip + cross-file tests | `internal/config/*_test.go`, `internal/cmd/*_test.go` | NEW |
| EX1 | `validateDispatchConfig` (struct validation) | `internal/config/dispatch.go:141-185` | EXISTING (consumed, unchanged) |
| EX2 | `ValidateDispatchConfig` (cross-file) | `internal/config/dispatch.go:93-138` | EXISTING (consumed; strict at write path, tolerant at dispatch loop via K6) |
| EX3 | `af install --agents` / `agent-gen-all.sh` | `internal/cmd/install.go:620+`, `agent-gen-all.sh:134-153` | EXISTING (invoked by K5) |
| EX4 | write-if-absent guard | `internal/cmd/install.go:150-157` | EXISTING (reused by K4) |

---

## Component dependencies (code-level)

Arrow `A → B` reads "A depends on / requires B":

```
K4 (install wiring)        → K1 (default builder)
K4                         → K2 (repo discovery)
K2 (discovery)             → K3 (repo validator)
K1 (default builder)       → EX1 (validateDispatchConfig — output must pass struct validation)
K4                         → EX4 (write-if-absent guard — reused, unchanged)
K5 (quickstart provision)  → EX3 (af install --agents / agent-gen-all.sh)
K6 (dispatcher tolerance)  → EX2 (ValidateDispatchConfig — relaxes only the dispatch-loop caller)
K7 (tests)                 → K1, K4, K5, K6, EX1, EX2 (asserts behavior of all)

Runtime (not build) ordering at install/dispatch time:
  K2 → K3 → K1 → K4 (discover → validate → build default → write)   [install]
  K5 (provision specialists) MUST complete before the dispatcher runs EX2 [bootstrap]
  EX2 (cross-file) consumes the result of K4 (the written default) + K5 (provisioned agents.json)
```

Note: K5 and K1/K4 have NO compile-time dependency on each other (K5 is a shell
script; K1/K4 are Go). Their dependency is a RUNTIME SEQUENCING one: the default
(K4) and the provisioned agents (K5) must BOTH be in place before the dispatcher
runs the cross-file check (EX2). This is the C-6 sequencing the synthesis must pin.

---

## Component build order (critical path)

| Order | Component | Depends on (must exist first) | On critical path? | Risk |
|-------|-----------|-------------------------------|-------------------|------|
| 1 | K3 repo validator (allowlist) | — (pure function) | YES | LOW — pure regex/string check |
| 2 | K1 `DefaultDispatchConfigJSON` | EX1 (struct + validator, existing) | YES | LOW — mirrors existing `DefaultFactoryConfigJSON` |
| 3 | K2 repo discovery | K3 (validates its output) | YES | MED — git/gh I/O, URL-shape handling, gh-auth timing |
| 4 | K4 install wiring | K1, K2, EX4 | YES | LOW — one call-site change in an existing map |
| 5 | K5 quickstart provisioning | EX3 (existing `af install --agents`) | **YES — C-6 critical** | MED — heavier bootstrap; must run before dispatch; relies on `af install --agents` succeeding |
| 6 | K6 dispatcher tolerance (optional) | EX2 | NO (defense-in-depth) | MED — must NOT weaken the write-path validator |
| 7 | K7 tests | K1, K4, K5, (K6) | YES — gates AC-1/AC-3/C-6 | LOW |

**Critical path:** K3 → K1 → K4 (the default itself), in parallel with K5 (C-6
provisioning). The two strands converge at the dispatcher's cross-file check (EX2)
at runtime: the default (K4) and the provisioned agents.json (K5) must both be
present. K6 is off the critical path (the design is correct without it; it only adds
graceful degradation).

---

## Circular-dependency check

NO cycles. Verified by inspection of the arrows above:
- K1 depends only on EX1 (existing); EX1 depends on nothing in this set.
- K2 → K3; K3 depends on nothing.
- K4 → {K1, K2, EX4}; none of K1/K2/EX4 depend back on K4.
- K5 → EX3; EX3 (the existing `af install --agents`) does not depend on K1/K4/K5.
- K6 → EX2; EX2 does not depend on K6 (K6 changes how a CALLER uses EX2, not EX2's
  own dependencies).
- K7 depends on everything but nothing depends on K7.

A topological order exists: **K3, K1, K2, K4, K5, K6, K7** — a DAG, no cycle.

Potential cycle that was AVOIDED (verified): `ValidateDispatchConfig` deliberately
does NOT import `internal/formula` to inspect phase agents' formulas, because
"internal/formula imports internal/config, so importing it here would create an
import cycle" (dispatch.go:130-136, verified). Any new validation in K1/K6 MUST
likewise avoid importing `internal/formula` from `internal/config` to preserve the
acyclic package graph.

---

## Critical-path risk flags

| Risk on critical path | Component | Severity | Mitigation |
|-----------------------|-----------|----------|------------|
| C-6 sequencing: K5 (provision) must finish before the dispatcher's EX2 runs, or the autonomous path fails on a fresh factory | K5 ↔ EX2 | HIGH | Run `af install --agents` in quickstart's configure_factory (after init, before `af up`/dispatch); K6 backstops a partial provision with skip-and-warn |
| K2 discovery depends on gh-auth/git-remote timing (auth may post-date init in the setup flow, source.md:90) | K2 | MED | Layer `git remote` (no auth) under `gh repo view`; warn-don't-abort (A3.1) degrades to a loadable default |
| K1 output must pass EX1 exactly, or the default is unloadable | K1 → EX1 | LOW (caught early) | Build K1 from the `DispatchConfig` struct (compile-time field safety) + K7 golden test |
| K6 over-broad relaxation weakens `af config dispatch set` strictness | K6 → EX2 | MED | Scope tolerance to the dispatch-loop caller ONLY; write path keeps strict EX2 (config_set.go unchanged) |
| New `internal/config` validation accidentally imports `internal/formula` → import cycle | K1 / K6 | MED | Preserve the existing no-formula-import discipline (dispatch.go:130-136, verified) |
| Future formula rename breaks the default's agent references | K1 + EX3 | MED | ADR-008 drift test ties formula names to embedded files; K7 cross-file test pins the default against shipped agents |
