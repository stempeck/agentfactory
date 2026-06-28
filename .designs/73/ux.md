# D3 — User Experience (ux.md)

Owner of (verification.md): AC-2 (with Integration), AC-4 (with Integration),
AC-6 (with Integration, Scale), C-2 (with Integration), C-5 (with Integration,
Security). The "user" here is the NEW operator in the source's scenario: they run
`./quickdocker.sh <repo>`, land in `/af/repo`, and want to "just start tagging
their github issues with tags … without ever needing to visit the manager"
(source.md:13, AC-2).

The happy path this dimension must protect (verbatim setup flow, source.md:85-98):
quickdocker → claude auth → quickstart.sh → `af up` → tag an issue → work runs
autonomously. Today the gap is that after quickstart, dispatch.json is EMPTY
(install.go:145, verified), so tagging an issue dispatches nothing.

---

## U1. First-run discoverability of the baked-in default

### Option U1.1 — Zero-touch: default is present after `quickstart.sh`; operator just tags an issue — RECOMMENDED

After `quickstart.sh` runs `af install --init` (quickstart.sh:442, verified), the
non-empty default (D2) with the discovered repo (D1) is already on disk, and the
dispatcher auto-starts on `af up` (startup.json `start_dispatch:true`,
install.go:147, verified; codebase-snapshot Decision History notes #58 made the
dispatcher auto-start). The operator's ONLY action is to apply a mapping label
(e.g. `rapid-plan`) to a GitHub issue.

- Trade-offs: This is the literal AC-2 outcome — "without ever needing to visit the
  manager." No new command to learn, no config to edit. The discoverability cost
  shifts to "how does the operator know which labels to apply" — addressed by U2.
- Reversibility: Easy.
- Constraints: satisfies AC-2, C-2, C-5 (no human config step, no doctor). The
  dispatcher auto-start is a deliberate recent change (#58, Decision History) that
  this option RELIES ON rather than reverses. Recommended.

### Option U1.2 — Print a "next steps" banner after install listing the dispatch labels — RECOMMENDED (complementary)

After writing the default, `runInstallInit` (or quickstart's completion banner,
quickstart.sh:700-713, verified) prints the active trigger label and mapping labels
("Tag an issue with `rapid-plan`, `rapid-engineer`, `pr-review`, or `pr-iterate` to
dispatch work autonomously").

- Trade-offs: Closes the "which labels" discoverability gap from U1.1 at near-zero
  cost (a few `fmt.Fprintln`). The banner content is derived from the default the
  same run wrote, so it cannot drift. Slightly more install output.
- Reversibility: Easy.
- Constraints: satisfies AC-2 ergonomics; no constraint conflict. Recommended as a
  complement to U1.1.

### Option U1.3 — Interactive post-install prompt asking the operator to confirm/choose labels — REJECTED

- REJECTED: contradicts [ADR-014]. ADR-014 (Decision History) forbids interactive
  prompting in `cmd/`/`internal/` Go paths at runtime; `runInstallInit` is Go in
  `internal/cmd/`. A prompt here is exactly the shape ADR-014 prohibits. (A prompt
  in quickstart.sh is technically ADR-014-exempt as an operator bootstrap script,
  but the ADR says such exemptions "should be minimized over time and not
  proliferated" — and U1.1/U1.2 deliver the outcome with no prompt at all.)

---

## U2. Failure-message ergonomics when the autonomous path can't run

The new operator must not be left silently stuck. Three failure surfaces exist:
(a) repo discovery failed at install (D1/A3); (b) the trigger label was applied but
the mapped specialist isn't provisioned (C-6); (c) `gh auth` missing at dispatch.

### Option U2.1 — Each failure emits a structured, actionable stderr message naming the exact remedy flag/command — RECOMMENDED

(a) discovery fail → warn naming `af config dispatch set` (config_set.go:24-32,
verified). (b) unknown-agent mapping → the dispatcher's existing
`ValidateDispatchConfig` error already names the agent
("dispatch mapping references unknown agent %q", dispatch.go:102, verified); the
remedy message should add "run `af install --agents` to provision specialists."
(c) `gh auth` missing → the dispatcher already checks `gh auth`
(codebase-snapshot §4) and the existing query path warns per-repo
("warning: failed to query issues for %s", dispatch.go:180, verified).

- Trade-offs: Reuses existing error sites; the only NEW text is the remedy hint.
  Matches ADR-014's required shape ("fail loud with a structured error naming the
  exact flag that expresses the intent"). Honest about what's missing.
- Reversibility: Easy.
- Constraints: satisfies C-5 (no human-fix-as-dependency for the HAPPY path; these
  messages only fire on a genuinely broken setup), complies with ADR-014.
  Recommended.

### Option U2.2 — Silent best-effort: skip what can't run, no message — REJECTED

- REJECTED: fails [AC-6], violates [C-5]. AC-6 demands "consistent successful
  outcomes" and "systemic improvements"; a silent skip leaves the operator unable
  to diagnose why tagging an issue produced nothing, turning a one-time bootstrap
  gap into a recurring "why isn't this working" support burden — the opposite of
  C-5's "without human interaction as an ongoing operational dependency."

---

## U3. Keeping the autonomous formula path (AC-4) ergonomic end-to-end

AC-4: `af sling --agent <name> "task"` must run the agent's formula autonomously
"up to the point where human interaction is necessary." This is EXISTING behavior
(`af sling --agent` specialist dispatch, codebase-snapshot §4, sling.go verified:
`resolveSpecialistAgent` instantiates the formula). This dimension's UX job is to
ensure the dispatch.json default ROUTES to specialists that actually have this
behavior — which D2's 4 referenced agents do (all are formula-bearing specialists,
codebase-snapshot §3).

### Option U3.1 — Default routes only to formula-bearing specialists (no behavior change to sling) — RECOMMENDED

The 4 mapped agents are all formula-bearing (codebase-snapshot §3), so a dispatched
label drives `af sling --agent <name> --reset <url>` (dispatch.go cycle step 6,
codebase-snapshot §4), which instantiates the formula and runs it via the agent's
own `af prime`/`af done` loop until a gate. No change to sling/done/prime; the UX
is "tag → formula runs → PR appears."

- Trade-offs: Zero new UX surface; leans entirely on the verified existing
  specialist-dispatch + formula-step machinery, which is exactly what AC-6 wants
  ("the known working formula process that IS their IDENTITY"). The only
  requirement is the C-6 rider (D2): those specialists must be provisioned.
- Reversibility: Easy.
- Constraints: satisfies AC-4, AC-5 (formula path pushes PRs without doctor/human —
  an existing property of the rapid-* formulas, which Integration/D6 confirms),
  AC-6. Recommended.

## Reversibility (this dimension): Easy

UX changes are an install-time banner and stderr remedy hints — additive text,
no behavioral lock-in.

## Dependencies produced

- PROVIDES to **Integration (D6)**: the requirement that the bootstrap banner /
  remedy messages name `af install --agents` and `af config dispatch set` (so D6
  must confirm those are the real provisioning/repair commands — both verified).
- REQUIRES from **Data (D2)**: the mapping labels to print in the U1.2 banner.
- REQUIRES from **Integration (D6)**: confirmation that the dispatcher auto-starts
  on `af up` (startup.json `start_dispatch:true`, install.go:147, verified) so U1.1
  is true.
- REQUIRES from **Security (D5)**: the discovered repo string is safe to echo in a
  banner (no terminal-escape injection from a crafted remote URL).

## Risks identified

| Risk | Severity | Mitigation |
|------|----------|------------|
| Operator tags an issue but the specialist isn't provisioned (C-6) → silent no-op confuses them | HIGH | U2.1 actionable error naming `af install --agents`; D2/D6 C-6 rider provisions specialists in bootstrap |
| Operator doesn't know which labels to apply | Medium | U1.2 next-steps banner lists the active labels, derived from the just-written default |
| `gh auth` not yet done when the dispatcher first polls | Medium | Existing per-repo warning (dispatch.go:180); setup flow authenticates `gh`/claude at step 2 (source.md:90) before `af up` |
| Banner echoes a crafted repo string containing terminal escapes | Low | Security (D5) sanitizes/validates the repo string before it is stored or echoed |
