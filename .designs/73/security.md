# D5 — Security (security.md)

Owner of (verification.md): C-3 (with Integration, API), C-5 (with UX,
Integration), C-6 (with Data, Integration), AC-5 (with Integration). The security
surface of this change is small but real: it introduces, for the first time,
UNTRUSTED INPUT (a git remote URL / `gh` output) into the install path, and that
input is written into a config file the dispatcher later feeds to `gh`.

Threat model context: install runs in a per-user docker container
(quickdocker.sh, source.md:87) — not a multi-tenant boundary. The realistic threats
are (a) a malformed/malicious git remote producing a bad `repos` entry, and (b) the
default mappings causing a cross-file validation failure (a correctness/DoS-of-
dispatch issue, C-6). There is no credential or PII handling in this change.

---

## SEC1. Trust of the discovered repo string

The repo string comes from `git remote get-url origin` or `gh repo view` (D1/A2).
In the container scenario the remote was set by `quickdocker.sh <repo-path>`
(operator-supplied, source.md:87-88), so it is semi-trusted — but the value flows
into a config that is later passed to `gh --repo <repo>` (dispatch.go:300,
verified) and split on `/` (dispatch.go:537, verified). A crafted value could
inject unexpected `gh` flags or terminal escapes.

### Option SEC1.1 — Validate the discovered repo against a strict `owner/name` allowlist regex before writing — RECOMMENDED

Before placing the discovered string in `repos`, require it to match a strict
pattern (e.g. `^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$` — one slash, no whitespace, no
shell metacharacters, no `..`). On mismatch, do NOT write it; fall to the degraded
default (A3.1) with a warning.

- Trade-offs: Cheap, deterministic, and matches the EXACT shape the dispatcher
  requires (`strings.Cut(repo,"/")` expecting non-empty owner and name,
  dispatch.go:537-539, verified — it already errors on a non-`owner/name` form).
  Validating at the WRITE boundary means a bad value never reaches the dispatcher.
  Rejects URL-parse failures, multi-slash paths, and any embedded shell/escape
  characters.
- Reversibility: Easy.
- Constraints: satisfies C-3 (a VALID actual repo, not just any string), supports
  AC-5 (the dispatched work targets the right repo). Recommended.

### Option SEC1.2 — Pass the raw discovered string through unvalidated — REJECTED

Write whatever `git remote`/`gh` returns directly into `repos`.

- REJECTED: fails [C-3] (a malformed value is not "the repository-name … included
  appropriately") and introduces an injection surface. `gh` is invoked with the
  repo as `--repo <repo>` via exec (dispatch.go:300, verified) — argument-vector
  exec mitigates shell injection, but a crafted value (e.g. containing a leading
  `-`) could still be mis-parsed as a `gh` flag, and an unvalidated value would be
  echoed into the UX banner (U1.2), enabling terminal-escape injection. Reject.

---

## SEC2. Cross-file validity of the default (C-6) as an availability concern

A default that references agents absent from agents.json causes
`ValidateDispatchConfig` to fail (dispatch.go:100-104, verified) at dispatch-start —
a self-inflicted denial of the dispatch service on a fresh factory. This is the
C-6 security/availability surface.

### Option SEC2.1 — Guarantee the default is cross-file-valid by construction: provision referenced specialists during bootstrap — RECOMMENDED

Ensure the 4 referenced specialists are in agents.json before
`ValidateDispatchConfig` runs, by having the bootstrap run `af install --agents`
(which provisions every shipped formula's agent via `af formula agent-gen`,
agent-gen-all.sh:134-153, verified). The cross-file validator then passes.

- Trade-offs: The default is valid-by-construction (C-5: "valid-by-construction …
  no doctor --fix"). This is owned jointly with Data (C-6 rider) and Integration
  (the bootstrap-sequence change). Cost: `af install --agents` rebuilds af and
  provisions ~15 agents — a heavier bootstrap, but a one-time setup cost.
- Reversibility: Moderate (touches the bootstrap sequence, not just one function).
- Constraints: satisfies C-6, C-5 (no doctor dependency), AC-5. Recommended;
  see Integration (D6) for the precise sequencing.

### Option SEC2.2 — Make the dispatcher TOLERATE unknown-agent mappings (skip-and-warn instead of fail) — RECOMMENDED (defense-in-depth, but a behavior change owned by Integration)

Change the dispatch path so an unknown-agent mapping is SKIPPED with a warning
rather than failing the whole cycle. The known-good mappings still dispatch.

- Trade-offs: Resilient even if provisioning is incomplete; the autonomous path
  degrades per-mapping instead of all-or-nothing. BUT this is a behavior change to
  `ValidateDispatchConfig`/the dispatch cycle (dispatch.go:93-138 currently RETURNS
  an error on unknown agent, dispatch.go:102, verified) and could MASK a genuine
  config typo. Must be paired with a loud per-mapping warning. This is a real
  cross-dimension decision: SEC2.1 (provision) vs SEC2.2 (tolerate). They are not
  mutually exclusive; SEC2.1 is the primary, SEC2.2 is defense-in-depth.
- Reversibility: Moderate (alters a validation contract).
- Constraints: supports C-6/AC-2 robustness; must NOT silently swallow real errors
  (pair with warning). Recommended as defense-in-depth, decision deferred to D6.

### Option SEC2.3 — Ship the default but let it fail validation, expecting `doctor --fix` to repair it — REJECTED

- REJECTED: violates [C-5]. C-5 explicitly states `doctor --fix` is "acceptable
  only as a bandaid/fix for legacy broken behaviors, not as an ongoing operational
  dependency." Designing a default that REQUIRES `doctor --fix` on every fresh
  install makes doctor an ongoing operational dependency — the precise thing C-5
  prohibits. Reject.

---

## SEC3. Preserving customer data (ADR-017)

### Option SEC3.1 — Reuse the existing idempotent write-if-absent guard; never overwrite an existing dispatch.json — RECOMMENDED

The default is written only when dispatch.json does NOT yet exist
(`os.Stat … os.IsNotExist`, install.go:152, verified). An operator-edited file is
never clobbered.

- Trade-offs: Honors ADR-017 ("af infrastructure commands must not delete customer
  data") and C-2 ("first created"). Zero new write-path risk. The repo-discovery
  read (`git remote`/`gh`) is read-only, also ADR-017-clean.
- Reversibility: Easy.
- Constraints: satisfies C-2, complies with ADR-017. Recommended.

## Reversibility (this dimension): Easy–Moderate

Repo validation and the write guard are Easy. The cross-file-validity guarantee
(SEC2.1) touches the bootstrap sequence and is Moderate; the dispatcher-tolerance
change (SEC2.2) alters a validation contract and is Moderate.

## Dependencies produced

- PROVIDES to **API (D1)**: the `owner/name` allowlist regex the discovered string
  must pass before being written.
- PROVIDES to **UX (D3)**: the guarantee that the repo string echoed in the banner
  is escape-safe (post-validation).
- PROVIDES to **Integration (D6)**: the C-6 resolution choice (SEC2.1 provision vs
  SEC2.2 tolerate) — escalated to D6 to sequence.
- REQUIRES from **Data (D2)**: the list of agents the default references (to
  guarantee they are provisioned / handled).
- REQUIRES from **Integration (D6)**: the exact point in the bootstrap where
  `ValidateDispatchConfig` runs relative to specialist provisioning.

## Risks identified

| Risk | Severity | Mitigation |
|------|----------|------------|
| Crafted git remote URL injects a bad `repos` value (flag-injection into `gh`, terminal-escape into banner) | Medium | SEC1.1 strict `owner/name` allowlist at the write boundary; reject + degrade on mismatch |
| Default references unprovisioned agents → dispatch-start validation fails (self-inflicted DoS of dispatch, C-6) | HIGH | SEC2.1 provision specialists in bootstrap (primary) + SEC2.2 dispatcher skip-and-warn (defense-in-depth) |
| A future design papers over C-6 by mandating `doctor --fix` on every install | Medium | SEC2.3 REJECTED on C-5; encode the valid-by-construction requirement in a golden test (D2) |
| Re-running install overwrites an operator-curated dispatch.json | Low | SEC3.1 write-if-absent guard (install.go:152) preserves the existing file (ADR-017) |
