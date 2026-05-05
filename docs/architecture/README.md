# Architecture docs

This directory is the **code-grounded source of architectural truth** for
the agentfactory project. Every claim in every file here is anchored to
a `file:line` or a commit SHA, or explicitly labelled
`"unknown — needs review"`.

These docs exist so design-review authors and implementers cannot
silently drift away from established patterns. When you're about to
propose a fix for a cross-cutting concern, the relevant idiom or
invariant is a lookup here, not a guess.

---

## Read this first (overview layer)

These four files are the high-level entry points. Start here to see
the shape before diving into citations.

| File | Purpose |
|------|---------|
| [`overview.md`](./overview.md) | C4 L1 system-context diagram + trust-direction sketch + index into the corpus. |
| [`containers.md`](./containers.md) | C4 L2 container diagram: `af` process, Python MCP server, agent sessions, shared filesystem. |
| [`flows/`](./flows/) | Sequence diagrams for the three load-bearing flows: `af up`, `af sling --agent` dispatch, and the `af done` WORK_DONE cascade. |
| [`adrs/`](./adrs/) | 12 Nygard-format ADRs capturing the key decisions (MCP over SQLite, `IncludeAllAgents` idiom, no identity overrides, loopback-no-auth, hooks-never-block, etc.) with Context / Decision / Consequences and links back to the corpus. |

## Detailed corpus

| File | Purpose |
|------|---------|
| [`vocabulary.md`](./vocabulary.md) | Canonical terms (entity types, enum values, identity terms) with code anchors, and a Banned-paraphrases list. **Use this when writing any design or plan**: prefer the exact code symbol or enumeration over a summary adjective. |
| [`idioms.md`](./idioms.md) | Cross-cutting patterns with every call site enumerated. **Read first** when designing any change that touches identity, actor scoping, config loading, embedded assets, or the issue store. |
| [`invariants.md`](./invariants.md) | System-wide MUST-holds, with the mechanism that enforces each (type / test / convention) and the consequences of violation. |
| [`trust-boundaries.md`](./trust-boundaries.md) | What is trusted where. Identity-derivation trust anchors, override policy, cross-process trust. |
| [`seams.md`](./seams.md) | Cross-subsystem contracts: wire protocols, shared types, package-var test seams, embed trees. |
| [`subsystems/*.md`](./subsystems/) | Per-subsystem shape, seams, contracts, formative commits, load-bearing invariants, gaps. |
| [`history.md`](./history.md) | Timeline of architecturally formative commits, grouped by theme. |
| [`gaps.md`](./gaps.md) | Drift, dead code, missing enforcement, anchor mismatches. Never empty — a growing gaps list is a drift signal. |

## Subsystems

| Subsystem doc | Scope |
|---------------|-------|
| [`cmd`](./subsystems/cmd.md) | The cobra CLI layer (`internal/cmd/`) — 17 subcommands, identity resolver, seam owners |
| [`config`](./subsystems/config.md) | `internal/config/` — factory/agents/messaging/dispatch config + path conventions |
| [`issuestore`](./subsystems/issuestore.md) | `internal/issuestore/` + mcpstore + memstore — the Store interface and both adapters |
| [`formula`](./subsystems/formula.md) | `internal/formula/` — TOML parsing, DAG, variable resolution |
| [`mail`](./subsystems/mail.md) | `internal/mail/` — issuestore-backed mail |
| [`session`](./subsystems/session.md) | `internal/session/` + `internal/tmux/` — agent session runtime |
| [`fs-primitives`](./subsystems/fs-primitives.md) | `internal/worktree/` + `internal/lock/` + `internal/checkpoint/` + `internal/fsutil/` |
| [`embedded-assets`](./subsystems/embedded-assets.md) | `internal/claude/` + `internal/templates/` — go:embed trees |
| [`py-issuestore`](./subsystems/py-issuestore.md) | `py/issuestore/` — Python MCP server |
| [`hooks`](./subsystems/hooks.md) | `hooks/` — quality + fidelity gates |

---

## How to use these docs during design review

A design that changes cross-cutting behavior MUST cross-reference these
files before finalizing its "options considered" section. Use this
checklist:

### 1. Before writing the problem statement
- [ ] Search `idioms.md` for the concern (e.g. "actor scoping",
      "identity", "config loading"). If an idiom exists, name it in the
      problem statement.
- [ ] Search `invariants.md` for any MUST-hold in the affected area.
      Every option must preserve it, or explicitly justify the weakening.

### 2. Before enumerating options
- [ ] For each candidate option, walk through every relevant idiom's
      *deviation detector* in `idioms.md`. If the option trips a
      detector, it is a **deviation**, not an accident — name it as
      such in the option write-up.
- [ ] For each relevant invariant, state how the option preserves it.
      If it doesn't, state that explicitly; then the invariant entry
      needs to be updated or the option is wrong.

### 3. Before choosing an option
- [ ] Check `trust-boundaries.md` for the trust direction of any
      identity-bearing flow the option touches. Does the option add a
      new writer? A new reader outside the cmd layer?
- [ ] Check `seams.md` for the contract shape you're about to change.
      Contract changes propagate further than their local scope suggests.
- [ ] Check `history.md` for prior designs in the same area. Commit
      `63307bb` is specifically a design that was *deleted* because it
      bypassed an idiom — check whether your proposal looks like it.

### 4. Before merging
- [ ] If your change introduces a new pattern that appears in ≥2 sites,
      propose adding it to `idioms.md` in the same PR.
- [ ] If your change adds a new MUST-hold, add it to `invariants.md` with
      the enforcement mechanism.
- [ ] If your change resolves a gap, remove the entry from `gaps.md`.

### Red flags during review

- "I'll just bypass the overlay at the adapter seam" — see idiom #1.
  Commit `63307bb` was the specific deletion.
- "Let's add an `--as` / `--actor` flag for this edge case" — INV-2,
  idiom #2. Hard wall.
- "We can read `os.Getenv` here, it's fine" — in `internal/cmd/` or
  `internal/session/`, yes. In any other `internal/` package, INV-3.
- "The hook will block this tool call" — hooks never block
  (see `subsystems/hooks.md`). Feedback is mail.
- "Compare `status == \"closed\"` for the fast path" — INV-5,
  idiom #7. Use `Status.IsTerminal()`.

---

## How to read

- Citation convention: `file:line` means "the current HEAD of this file
  at that line". `commit SHA` (7 chars) is a permalink. If a memory is
  stale, the anchors may have drifted — re-run the skill to refresh.
- `"unknown — needs review"` is a literal phrase. When you see it,
  the rationale could not be anchored to a commit or code comment and
  is a genuine hole — not summarizable, not fudgeable.
- Design docs under `.designs/` are **secondary sources**. Claims in
  these files that disagree with `.designs/` are trusting the code.
  This was deliberate: commit `63307bb` is a specific case where a
  design doc's recommendation was wrong against the code's idiom.

## How to refresh

Run `/architecture-docs` from repo root. The skill re-runs Phases 0–7
and regenerates every file here. Re-running on an unchanged tree
produces identical files (idempotent); re-running after changes
produces a reviewable diff — which is itself the drift signal.

To refresh a single subsystem file:
```
/architecture-docs <subsystem-name>
```
(e.g. `/architecture-docs issuestore`). This skips inventory +
cross-cutting phases.

## How to contribute

- Changes to `idioms.md` or `invariants.md` require the deviation / new
  invariant to be visible in the code first. Docs follow code, not the
  other way around.
- Changes to `gaps.md` are welcome from anyone — if you find a drift
  the skill missed, add it.
- `subsystems/*.md` are regenerated; don't hand-edit (changes will be
  overwritten on next refresh). If a subsystem doc is wrong, fix the
  code or the comment the skill is reading.

## Ownership

These docs have no single owner. The skill `/architecture-docs`
regenerates them; every contributor is responsible for keeping anchors
true when they change code that is cited here.
