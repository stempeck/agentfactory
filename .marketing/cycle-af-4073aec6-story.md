# Cycle af-4073aec6 — Story proposal (release-only)

## Pick

**The v0.5.0 GitHub release**, covering #135 (the only user-facing merge since v0.4.0):
a `## v0.5.0` CHANGELOG section on a branch → PR the operator merges → tag `v0.5.0` on the
merged commit → `gh release create` with themed notes → prebuilt `af` + `webui` binaries
(linux amd64/arm64) and `SHA256SUMS`, as v0.4.0 shipped.

**No marketing story, no Tier B.** No Medium article, no LinkedIn post, no README marketing
refresh.

## Override note

`flagship_hint` is empty, but the assignment carries the operator's pre-pick. Bead
af-4073aec6, the operator's words relayed verbatim by manager on 2026-11-14: "sling
marketing-cycle but have it skip straight to the part where it publishes a release, no actually
medium article or marketing this time." That instruction narrows the cycle to the release, so
the ranking below orders the release notes and does not choose a story.

## Ranking (orders the release notes, not a story)

Criteria from the runbook: (a) user pain killed, (b) 60-second demonstrability, (c) fit to the
Positioning target phrases, (d) reach for Claude Code practitioners.

| # | #135 item | (a) pain | (b) demo | (c) fit | (d) reach | Release-notes slot |
|---|---|---|---|---|---|---|
| 1 | Agents run on the context window they really get (recorded window, `WINDOW` alarm, short-window launch refusal, `--accept-window`, `af config models check` measures it) | High: a session silently gets a smaller window than declared, so figures and step bounds are wrong | High: one alarm line + one refused `af up` | Claude Code, autonomous agents | Every Claude Code user | Lead |
| 2 | Advisory findings become healing pull requests | High: a CVE in a pin sits until someone notices | Medium | Security positioning the operator set (#125) | Anyone running agents on their credentials | 2 |
| 3 | af's own alarms reach you + dispatcher workflows finish and report stalls | High: silent stalls | Medium | Multi-agent orchestration | Dispatcher users | 3 |
| 4 | New factories start from current configs | Medium: a new factory starts on stale configs | Medium | Agentic workflows | New installs | 4 |
| 5 | Startup turns are measured + pin your rulings into every session start | Medium | Medium | Autonomous agents | Long-running factories | 5 |
| 6 | Sturdier formulas, updated pins, docs | Low–medium | Low | — | All | Tail |

## Rationale

The release-notes order follows the title the operator merged #135 under ("Agents run on the
context window they really get, af's own alarms reach you, advisory findings become healing
pull requests, and new factories start from current configs"), and the ranking agrees with it.
The version is **v0.5.0**: #135 adds new commands and flags (`af telemetry startup`,
`af memory pin|unpin`, `--accept-window`), which under pre-1.0 semver is a minor bump, the
same step v0.3.0 → v0.4.0 took for new features.

## Operator Decision
- Decision: APPROVE
  (APPROVE to proceed with the pick as written; REORDER: <feature> to swap the flagship;
   END-CYCLE to stop after an audit-only report. Leave blank = not yet decided.)
- Notes: Transcribed by marketing-cycle, not typed by the operator. Source: the operator's
  instruction in bead af-4073aec6, relayed verbatim by manager on 2026-11-14 — "sling
  marketing-cycle but have it skip straight to the part where it publishes a release, no
  actually medium article or marketing this time." The instruction was given to manager, not
  on GitHub, so it cannot be checked with `gh`. Notify issue #136
  (https://github.com/stempeck/agentfactory/issues/136) tells the operator exactly what was
  recorded and how to override it (REORDER / END-CYCLE). Same instruction → Tier B SKIP at GATE 4.
