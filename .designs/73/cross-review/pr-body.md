## Issue

Design for #73 — New dispatch workflow default to be included with agentfactory
https://github.com/stempeck/agentfactory/issues/73

A freshly bootstrapped factory cannot drive autonomous work from GitHub labels:
`af install --init` writes an **empty** `dispatch.json` (`repos:[]`, `mappings:[]`,
no `workflows` — `internal/cmd/install.go:145`), so a new user must hand-edit the
file before label-triggered dispatch works. The issue asks for a useful baked-in
default, populated with the real `owner/repo` discovered at install time.

> This is a **design + cross-review** PR (no production code yet). It produces the
> validated design and a 3-phase implementation plan; implementation is a follow-up.

## Root cause

- `runInstallInit` ships an empty `dispatch.json` and performs **no repo-name
  discovery**, even though quickstart has already `cd`'d into the repo (the fact is
  discarded). — `install.go:145`, verified.
- The naive fix (ship the proposed default as-is) is **necessary but not
  sufficient**: the default's four mappings reference specialist agents
  (`rapid-soldesign-plan`, `rapid-implement`, `ultra-review`, `rapid-increment`)
  that a fresh `agents.json` does **not** contain (install ships only
  `manager`+`supervisor`), and `ValidateDispatchConfig` **hard-fails the entire
  dispatch cycle** on the first unknown mapped agent (`internal/config/dispatch.go`,
  invoked unconditionally at `internal/cmd/dispatch.go:146`). So the default alone
  would break dispatch on every fresh factory.

## Proposed solution

A single **systemic** change (valid-by-construction), not just a literal default:

- **K1** `DefaultDispatchConfigJSON(repo)` — single-source builder beside
  `DefaultFactoryConfigJSON()` (`config.go:111`), emitting the 4 label→agent
  mappings + `feature-workflow` + `trigger_label:"agentic"`.
- **K2** Non-interactive repo discovery (`gh repo view --json nameWithOwner`, with
  `git remote get-url origin` fallback) feeding `repos`; warn-don't-abort.
- **K3** Strict `owner/name` validator at the write boundary (guards flag/escape
  injection from a crafted remote).
- **K5** `DefaultAgentsConfigJSON()` — **seeds the 4 specialists into the default
  `agents.json` within `runInstallInit` itself** (the role templates are already
  embedded), making the default valid on **every** init path, including the bare
  `af install --init` "hard way" — not just quickstart.
- **K9** Hoist dispatcher auto-start out of the `blanket`-only gate so the
  documented `af up manager` (positional) also starts the polling loop (idempotent).
- **K6/K7/K8** Defense-in-depth: dispatch-loop skip-and-warn tolerance (K6) with
  **mandatory** observability (K8), plus a golden cross-file drift test (K7).

Architecture-elevation verdict: **Frame correct** (the dispatch fields must exist;
deleting `dispatch.json` only relocates them) with **one offered lift adopted** —
repo self-derivation (K2).

## Cross-review (round 1)

Analyst (`rootcause-all`) reviewed the design grounded in its root-cause analysis
(9 concerns; 8 validated). It **converged with the design on every primary point**
and raised, the designer (`design-v7`) incorporated, all findings:

- **C1 (CRITICAL):** "valid-by-construction" held only on the quickstart path; bare
  `af install --init` left the populated default referencing unregistered agents.
  → Fixed by **K5** seeding specialists in `runInstallInit` itself.
- **C2 (CRITICAL):** the documented `af up manager` (positional) does **not**
  auto-start the dispatcher (gated to blanket `af up`). → Fixed by **K9** hoisting
  the auto-start.
- **H1 (HIGH):** commit to the minimal provisioning mechanism (drop heavyweight
  `agent-gen-all.sh`). → K5 now commits to direct `agents.json` seeding.
- **H2 (HIGH):** make observability **K8 mandatory** wherever the K6 tolerance is
  enabled (else a "running-but-dispatching-nothing" loop hides the failure).
- **L1–L4 (LOW):** agreements/nits; no change.

Both CRITICAL items were genuine entry-point/sequencing gaps; both are small,
localized, and **strengthen** the valid-by-construction thesis rather than redirect
it. With C1+C2 incorporated, the design satisfies AC-2 and AC-3 on every documented
setup path. (design-doc.md grew 319 → 365 lines.)

## Artifacts

- `.designs/73/design-doc.md` — full design (dimension analyses, AC traceability
  6/6, elevation verdict, risk registry, 3-phase implementation plan)
- `.designs/73/cross-review/analyst-review-design.md` — analyst round-1 review
- `.designs/73/cross-review/designer-update-log.md` — incorporation log
- `.designs/73/problem-summary-73.md`, `.designs/73/design-refinement-progress.md`
