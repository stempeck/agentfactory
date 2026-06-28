# conflicts.md — Cross-Dimension Conflict Matrix

Dimensions: D1 API, D2 Data, D3 UX, D4 Scale, D5 Security, D6 Integration.
Legend: **(none)** no conflict · **T** tension (trade-off needed) · **X** direct
conflict (resolution required).

## NxN Matrix (upper triangle; symmetric)

|        | D1 API | D2 Data | D3 UX | D4 Scale | D5 Security | D6 Integration |
|--------|--------|---------|-------|----------|-------------|----------------|
| **D1 API** | —      | T       | (none)| (none)   | T           | T              |
| **D2 Data**|        | —       | (none)| (none)   | T (via C-6) | X (C-6)        |
| **D3 UX**  |        |         | —     | (none)   | T           | T              |
| **D4 Scale**|       |         |       | —        | (none)      | (none)         |
| **D5 Sec** |        |         |       |          | —           | X (C-6 resolution) |
| **D6 Int** |        |         |       |          |             | —              |

---

## Cell-by-cell justification

### D1 API × D2 Data — **T** (tension)
- **Nature:** API owns the `DefaultDispatchConfigJSON(repo)` SIGNATURE and the
  discovery/injection point; Data owns the CONTENT (which mappings/workflows). The
  tension: the function's output must EXACTLY satisfy `validateDispatchConfig`
  (dispatch.go:141-185, verified) — if API marshals a shape Data didn't fully
  specify (e.g. omits a required label), the default fails to load.
- **Impact:** A mismatch produces an unloadable default (struct validation error).
- **Resolution options:** (a) API builds from the `DispatchConfig` struct so the
  compiler enforces the field set; (b) a golden test pins the exact output.
- **Chosen resolution:** Both — A1.1 builds from the struct (compile-time field
  safety) AND a golden round-trip test (D6 test list) pins content. Rationale: the
  struct guarantees well-formedness; the golden test guarantees the specific
  mappings Data chose are present. Tension resolved by construction.

### D1 API × D3 UX — **(none)**
The API change (a new Go function + one call site) is invisible to the operator; UX
recommends an install banner and stderr hints that consume the SAME default the API
writes. They operate on disjoint surfaces (internal function vs operator-visible
output) and the banner derives from the written default, so they cannot diverge.

### D1 API × D4 Scale — **(none)**
API adds one bounded install-time discovery call; Scale's only ask of API is that
the call have a timeout (S1.1). That is a complementary refinement, not a conflict —
no AC, constraint, or recommendation pulls them in opposite directions.

### D1 API × D5 Security — **T** (tension)
- **Nature:** API wants discovery to be frictionless (just take whatever
  `gh`/`git remote` returns and write it); Security requires the discovered string
  be validated against a strict `owner/name` allowlist before it is written
  (SEC1.1), adding a rejection path.
- **Impact:** Without validation, a crafted remote URL injects a bad `repos` value
  (flag-injection into `gh --repo`, dispatch.go:300; terminal-escape into the UX
  banner).
- **Resolution options:** (a) validate at the write boundary in API's discovery
  helper; (b) validate later in the dispatcher.
- **Chosen resolution:** Validate at the WRITE boundary (SEC1.1) inside the
  discovery step API owns. Rationale: a bad value never reaches disk or the
  dispatcher; the dispatcher's own `strings.Cut` guard (dispatch.go:537-539) becomes
  a second line of defense, not the only one. Tension resolved.

### D1 API × D6 Integration — **T** (tension)
- **Nature:** API proposes the function lives in `internal/config`; Integration
  proposes the call site is `runInstallInit` and the discovery ordering
  (discover → validate → build → write). The tension is sequencing: discovery must
  complete (and validate) BEFORE the starterConfigs map is built (install.go:139).
- **Impact:** Wrong ordering writes an empty/placeholder repos.
- **Resolution options:** (a) discover before building the map; (b) build the map
  with a placeholder then patch — rejected (two writes).
- **Chosen resolution:** Discover-then-build (I2.1): the discovery+validation runs
  before the starterConfigs map literal so the default carries the real repo in one
  write. Tension resolved by ordering.

### D2 Data × D3 UX — **(none)**
Data defines the mapping labels; UX's banner (U1.2) simply lists those labels for
discoverability. UX consumes Data's output read-only; there is no competing
requirement. The only shared fact (the label set) flows one direction.

### D2 Data × D4 Scale — **(none)**
Data picks the mappings/workflows content; Scale picks the cadence (300/1800) and
single-repo default. They touch different fields of the same struct
(`mappings`/`workflows` vs `interval_seconds`/`repos`) and both defer to the
verified struct defaults, so there is no contention.

### D2 Data × D5 Security — **T** (tension via C-6)
- **Nature:** Data wants to ship the faithful 4-mapping default (D2.2-A); Security
  flags that those 4 agents are absent from a fresh agents.json, making the default
  a cross-file-validation FAILURE (C-6) — an availability concern Security owns.
- **Impact:** Dispatch-start fails on a fresh factory until specialists exist.
- **Resolution options:** (a) provision specialists (SEC2.1); (b) dispatcher
  tolerate unknown agents (SEC2.2); (c) ship a manager/supervisor-only default
  (rejected — fails AC-2).
- **Chosen resolution:** SEC2.1 (provision) primary + SEC2.2 (tolerate) defense-in-
  depth. Rationale: provisioning makes the default valid-by-construction (C-5);
  tolerance backstops a partial provision. Data keeps its faithful default; Security
  gets validity. Tension resolved — escalated to D6 for sequencing (see D2×D6).

### D2 Data × D6 Integration — **X** (direct conflict — C-6, THE design crux)
- **Nature:** DIRECT conflict. Data's recommended default (D2.2-A) references 4
  specialists. Integration's verified finding: the bootstrap (`quickstart.sh`)
  provisions ONLY manager+supervisor (quickstart.sh:448-470, verified), so those 4
  agents do NOT exist in a fresh agents.json (install.go:143, verified), and
  `ValidateDispatchConfig` (dispatch.go:100-104, verified) FAILS on the first
  unknown agent at dispatch-start. Shipping Data's default WITHOUT an Integration
  change is a broken-on-arrival autonomous path — a real, not theoretical, conflict.
- **Impact:** HIGH. AC-2 ("kick off work without visiting the manager") and AC-4/
  AC-5 silently fail on every fresh install — the exact scenario the issue targets.
- **Resolution options:** (1) Integration adds `af install --agents` to quickstart
  to provision all shipped specialists before dispatch (I3.1). (2) Integration
  changes the dispatch loop to skip-and-warn on unknown agents (I3.2). (3) Data
  ships a degraded default referencing only manager/supervisor (D2.2-B / I3.3) —
  REJECTED (fails AC-2/AC-4). (4) Data ships mappings-only without the workflow
  (D2.2-C) — does NOT resolve C-6 (the mappings themselves reference the absent
  agents), so insufficient alone.
- **Chosen resolution:** **I3.1 (provision specialists in quickstart) as primary +
  I3.2 (dispatcher skip-and-warn) as defense-in-depth.** Rationale: I3.1 makes the
  default valid-by-construction (satisfies C-5/C-6 cleanly), and I3.2 ensures a
  partially-provisioned factory degrades gracefully rather than failing the whole
  cycle. Data ships its faithful default (D2.2-A) unchanged. THIS is the resolution
  the synthesis must carry — it is the single most important decision in the design.

### D3 UX × D4 Scale — **(none)**
UX's banner and error messages are install-time/operator-facing text; Scale's
cadence and single-repo default are runtime-loop concerns. They share no surface and
no competing requirement.

### D3 UX × D5 Security — **T** (tension)
- **Nature:** UX wants to ECHO the discovered repo in a friendly "next steps"
  banner (U1.2); Security requires that any echoed value be escape-safe to prevent
  terminal-escape injection from a crafted remote URL.
- **Impact:** An unsanitized repo string in the banner could inject terminal
  escapes.
- **Resolution options:** (a) validate the repo before storing (SEC1.1) so anything
  echoed is already `owner/name`-clean; (b) sanitize at echo time.
- **Chosen resolution:** (a) — SEC1.1 validates at the write boundary, so the value
  UX echoes is already a strict `owner/name` (no escapes possible). UX echoes the
  stored value, not the raw discovery output. Tension resolved upstream.

### D3 UX × D6 Integration — **T** (tension)
- **Nature:** UX's zero-touch happy path (U1.1) ASSUMES the dispatcher auto-starts
  and the specialists exist; Integration owns whether both are true. If Integration
  does NOT resolve C-6 (D2×D6), UX's "just tag an issue" promise is false.
- **Impact:** UX's headline ergonomic claim depends entirely on Integration's C-6
  resolution and the verified `start_dispatch:true` (install.go:147).
- **Resolution options:** (a) Integration resolves C-6 (I3.1+I3.2) so UX's promise
  holds, with U2.1 actionable errors as the fallback when it doesn't; (b) UX softens
  the promise.
- **Chosen resolution:** (a) — Integration's I3.1+I3.2 makes the zero-touch path
  real; U2.1 actionable errors (naming `af install --agents`) cover the residual
  failure. Tension resolved by Integration delivering the precondition UX assumes.

### D4 Scale × D5 Security — **(none)**
Scale's single bounded discovery call and inherited cadence introduce no new attack
surface; Security's validation/provisioning concerns are orthogonal to performance.
Neither pulls against the other.

### D4 Scale × D6 Integration — **(none)**
Scale defers entirely to the existing dispatch cadence, 50-result cap
(dispatch.go:182, verified), and 24h TTL — all of which Integration leaves
unchanged. Scale asks Integration only to confirm these are untouched (they are).
No competing requirement.

### D5 Security × D6 Integration — **X** (direct conflict — the C-6 RESOLUTION mechanism)
- **Nature:** DIRECT conflict over HOW to resolve C-6. Security's SEC2.1 says
  "provision specialists so the default is valid-by-construction"; SEC2.2 says
  "make the dispatcher tolerate unknown agents." Integration must decide WHERE this
  lives and in what ORDER, AND there is a sub-conflict: SEC2.2 (relax
  `ValidateDispatchConfig`) collides with the WRITE-path guarantee — `af config
  dispatch set` (config_set.go:85-91, verified) MUST stay strict so a human typo is
  caught, while the dispatch LOOP may tolerate. A blanket relaxation would weaken
  the write-path contract.
- **Impact:** HIGH. Getting this wrong either leaves the default broken (no
  resolution) or weakens validation everywhere (over-broad relaxation).
- **Resolution options:** (1) Provision-only (SEC2.1/I3.1) — strict validation
  everywhere, default valid-by-construction. (2) Tolerate-only (SEC2.2/I3.2) — relax
  the dispatch loop. (3) Both, with the relaxation scoped ONLY to the dispatch-loop
  caller, write path stays strict.
- **Chosen resolution:** **(3)** — I3.1 provisions specialists (primary), AND I3.2
  scopes the skip-and-warn tolerance to the DISPATCH-LOOP caller only; `af config
  dispatch set` keeps the strict `ValidateDispatchConfig` (config_set.go unchanged).
  Rationale: the default is valid-by-construction via provisioning; the dispatch
  loop degrades gracefully on a partial provision; the human write path retains its
  typo-catching strictness. This split is the precise mechanism that resolves both
  the C-6 conflict and its write-path sub-conflict. Resolution required and chosen.

---

## ELEVATION-LEVEL CONFLICTS

After the NxN matrix, examine whether any NEW abstraction/component proposed by one
dimension conflicts with another dimension's recommendation.

New abstractions/components proposed across dimensions:
1. **`DefaultDispatchConfigJSON(repo)`** (D1/API) — a new Go function.
2. **A repo-discovery helper** (`gh repo view` / `git remote` normalizer) (D1/API).
3. **A bootstrap provisioning step** (`af install --agents` in quickstart) (D6/I3.1).
4. **A dispatcher unknown-agent tolerance** (skip-and-warn) (D6/I3.2, D5/SEC2.2).
5. **An install "next steps" banner** (D3/U1.2).

### EL-1 — `DefaultDispatchConfigJSON` (new fn) vs the single-source pattern — NO conflict
The new function is the OPPOSITE of an abstraction-conflict: it removes the one
remaining inline-literal exception (codebase-snapshot Decision History, issue #371
Gap-6) by making dispatch.json's default match the `factory.json` pattern
(`DefaultFactoryConfigJSON`, config.go:111, verified). It aligns every dimension; no
dimension is contradicted.

### EL-2 — Repo-discovery helper (new component) vs ADR-014 and Security — RESOLVED, NO residual conflict
The discovery helper introduces git/gh I/O into a previously git-agnostic Go path.
This could conflict with ADR-014 (no interactive prompting) — resolved because
discovery is NON-interactive (git remote / gh) with fail-loud degradation, never a
prompt. It could conflict with Security (untrusted input) — resolved by SEC1.1
validation at the write boundary. No dimension's recommendation is left
contradicted.

### EL-3 — `af install --agents` provisioning step (D6/I3.1) vs Scale (bootstrap cost) — TENSION, resolved
Provisioning all shipped specialists at bootstrap is a NEW, heavier setup step that
Scale (D4) would otherwise want minimal. Tension: heavier one-time bootstrap vs
valid-by-construction default. Resolution: the cost is one-time at setup (not a hot
path), and it is the ONLY clean way to satisfy C-6/AC-2; Scale's concern is bounded
because discovery and provisioning both run once. No runtime-scale impact. The
elevation choice (provision) wins over the minimal-bootstrap preference because the
AC requires it.

### EL-4 — Dispatcher unknown-agent tolerance (D6/I3.2) vs the write-path validation contract (D5/D6) — DIRECT, resolved by scoping
This is the elevation-level form of the D5×D6 X-conflict: introducing a TOLERANCE
behavior in the dispatcher conflicts with the existing STRICT validation that
`af config dispatch set` relies on (config_set.go:85-91, verified). A new
"tolerate" abstraction must NOT leak into the write path. Resolution (carried from
D5×D6): scope the tolerance to the dispatch-loop caller ONLY; the write-path
validator stays strict. This keeps two callers of `ValidateDispatchConfig` with
deliberately different strictness — a documented split, not an accidental
divergence.

### EL-5 — Install banner (D3/U1.2) vs no-new-output minimalism — NO conflict
The banner is additive operator-facing text derived from the just-written default;
no dimension proposes suppressing install output, and Security's only concern (echo
safety) is resolved by SEC1.1. No conflict.

---

## Summary of required resolutions (for synthesis to carry)

| Conflict | Type | Resolution |
|----------|------|------------|
| D2×D6 (C-6: default references unprovisioned specialists) | X | I3.1 provision via `af install --agents` in quickstart (primary) + I3.2 dispatcher skip-and-warn (defense-in-depth) |
| D5×D6 (C-6 resolution mechanism + write-path strictness) | X | Provision + scope dispatcher tolerance to the dispatch-loop caller ONLY; write path stays strict |
| D1×D5 / D3×D5 (untrusted repo string) | T | SEC1.1 validate owner/name at the write boundary |
| D1×D2 (function output must satisfy validation) | T | Build from the struct + golden test |
| D1×D6 (discovery ordering) | T | Discover→validate→build→write in runInstallInit |
| D3×D6 (zero-touch path depends on C-6 + auto-start) | T | Integration delivers C-6 resolution + verified start_dispatch:true; U2.1 fallback errors |
