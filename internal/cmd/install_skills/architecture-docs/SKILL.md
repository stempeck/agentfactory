---
name: architecture-docs
description: >
  Generate or refresh `/docs/architecture/` — the current-state source of architectural
  truth for this codebase, grounded in code and git history (NOT in .designs/). Produces
  an anchor-dense corpus (`invariants.md`, `idioms.md`, `trust-boundaries.md`, `seams.md`,
  `subsystems/*.md`, `history.md`, `gaps.md`) plus a high-level overview layer (C4 L1/L2
  diagrams, sequence flows, Nygard ADRs). Every claim is anchored to a file:line or
  commit SHA, or labelled literally `"unknown — needs review"`. A bundled `validate.sh`
  mechanically enforces citation density and phase-completion gates. Used to ground
  future design reviews so they cannot silently drift away from established idioms.
allowed-tools: "Read,Grep,Glob,Bash,Write,Edit,Agent"
version: "1.1.0"
author: "Claude Code Factory"
changelog: |
  v1.1.0: Added mechanical validation (Phase 2.5 + Phase 9 via validate.sh), phase
          preconditions, consolidated citation rule, overview layer as Phase 8
          (see overview-phase.md), and --verify-idempotent mode.
  v1.0.0: Initial. Citation-anchored corpus so design reviews detect divergence
          from idioms/invariants by lookup rather than by guessing.
---

# Architecture Docs Skill

## Trigger

- `/architecture-docs` — full regeneration.
- `/architecture-docs <subsystem>` — refresh a single subsystem file; skips
  Phases 1, 3, 4, 5, 6, 8, 9.
- `/architecture-docs --verify-idempotent` — run the skill twice on an
  unchanged tree and `git diff --stat docs/architecture/`. Non-empty diff =
  the skill is not idempotent and produced non-deterministic output.

## Purpose

Produce a regenerable, citation-anchored set of architecture documents
under `/docs/architecture/` that capture **what the system is and why**,
grounded in code and git history — not in `.designs/` (design documents
can be wrong and have been wrong). Future designs and reviews use these
docs as the truth source for "how does this codebase already handle this
concern?" so an author cannot propose a fix that diverges from an
established idiom without naming the divergence and justifying it.

## Why this skill exists (read before running)

Design reviews routinely fail in a specific way: an author synthesizes
an "options considered" section from the problem statement without first
checking whether the codebase already has a canonical idiom for the
problem shape. When no document aggregates the existing idiom across its
call sites, the author has no efficient way to discover it, and
reviewers have no efficient way to challenge deviations from it. Designs
then ship that quietly weaken invariants the codebase was previously
enforcing through combined mechanisms.

The fix class for this failure mode is not "more review" and not "better
design docs." It is a **standing, regenerable, code-derived doc** that
every design-review step must cross-reference — so discovering the
canonical pattern is a lookup, not a guess, and deviating from it is a
named choice, not an accident.

## Core principles

1. **Code and commits are primary; everything else is secondary.**
   Design documents, comments, issue bodies, and even CLAUDE.md are all
   subject to drift or author error. When sources conflict, prefer
   code > commit message > code comment > design doc > issue body.

2. **Citation rule (THE canonical statement — all phases reference this).**
   Every claim in every output file must be anchored to one of:
   - a `file:line` reference (e.g., `internal/cmd/helpers.go:55`), or
   - a 7-char commit SHA in backticks (e.g., `` `63307bb` ``), or
   - the literal phrase `"unknown — needs review"` — used only when no
     anchor exists. Never invented.

   **Mechanical enforcement:** `validate.sh` (Phase 2.5, Phase 9) scans
   each produced file for multi-line paragraphs (> 200 chars) with zero
   anchors and fails the phase if any are found. A paragraph can only
   pass validation by containing at least one of the three forms above.

3. **Aggregate before abstracting.**
   A "pattern" or "idiom" requires **at least two distinct call sites**,
   and the output must enumerate every site with its access-control axis
   / trust boundary / invariant annotated. Single-site patterns are not
   idioms; they are implementations. Mechanically checked by
   `validate.sh` (idioms.md must have ≥ 2 call-site table rows).

4. **Bias to load-bearing cross-cutting concerns.**
   Do not document every package's internals — those belong in godocs.
   Document the things that, if broken or bypassed, will cause subtle
   system-wide failures: trust model, access control, identity
   derivation, lock/concurrency contracts, config-load order,
   error-wrap conventions, wire contracts with external processes.

5. **Idempotent and diff-able.**
   Re-running the skill on an unchanged codebase must produce identical
   output. Re-running after changes must produce a reviewable diff —
   which is itself a drift signal. Verified by
   `/architecture-docs --verify-idempotent`.

## Output structure

```
docs/architecture/
├── README.md              # Index; points at overview layer first, corpus second
├── overview.md            # C4 L1 system context + trust sketch (Phase 8)
├── containers.md          # C4 L2 containers + cross-container contracts (Phase 8)
├── flows/                 # Sequence diagrams for load-bearing flows (Phase 8)
│   └── <flow>.md
├── adrs/                  # Nygard-format ADRs (Phase 8)
│   ├── README.md          # ADR index
│   └── ADR-NNN-<slug>.md
├── invariants.md          # System-wide MUST-holds + enforcement + consequences
├── idioms.md              # Cross-cutting patterns with every call site enumerated
├── trust-boundaries.md    # What is trusted where; context provenance; override policy
├── seams.md               # Cross-subsystem contracts (wire protocols, shared types)
├── subsystems/
│   ├── <name>.md          # Per-subsystem: shape, seams, contracts, formative commits
│   └── ...
├── history.md             # Timeline of formative architectural commits
└── gaps.md                # Documented-but-unenforced + enforced-but-undocumented
```

## Phase preconditions

Each phase declares the artifacts it requires. If a precondition fails,
the phase must halt and the agent must fix the gap before proceeding.
This replaces the prior convention where phase ordering was narrative
only.

## Process

### Phase 0 — Pre-flight

**PRECONDITION:** none.

1. Verify `git log` works and the repo has a working tree.
2. If `/docs/architecture/` exists, note the current files for diffing.
3. If invoked with a subsystem arg, skip to Phase 3 for that subsystem only.
4. Create `todos/architecture-docs/` for working artifacts.

### Phase 1 — Inventory

**PRECONDITION:** Phase 0 complete. Working directory is repo root.

Produce `todos/architecture-docs/inventory.md` containing:

1. **Subsystems.** Identify major subsystems from directory structure.
   For a Go project, each top-level directory under `internal/` and each
   sibling of `cmd/` is a candidate. Non-Go subsystems (e.g. `py/`,
   `hooks/`) are first-class too. Entry points (`cmd/<name>/main.go`,
   CLI subcommands) are listed explicitly.
2. **External dependencies.** `go.mod`, `requirements.txt`, system
   binaries (invoked via `exec`). Note each with its purpose and
   invocation site.
3. **Cross-cutting concern candidates.** Grep for identity/auth
   (`actor`, `identity`, `auth`, `role`, `permission`),
   locking/concurrency (`Lock`, `lock`, `mutex`), config loading
   (`config.Load`, `LoadConfig`), invariant markers (`MUST`, `must not`,
   `invariant`, `contract`, `pinned by`), trust/seam markers (`wire`,
   `seam`, `trust`), and any project-specific constraint tags
   (e.g. `Gate-`, `C-\d+`, `H-\d+`). Each hit is a lead, not a conclusion.
4. **Surface the skill's target list.** Subsystem set, candidate
   idioms, candidate invariants — the inputs for Phases 2–6.

**EXIT CHECK:** `todos/architecture-docs/inventory.md` exists and lists
at least one subsystem. If not, halt.

### Phase 2 — Parallel subsystem archaeology

**PRECONDITION:** `todos/architecture-docs/inventory.md` exists and lists
subsystems.

Dispatch one sub-agent per subsystem in a **single message** (parallel).
Each sub-agent receives the subsystem path, the full inventory, the
Core Principles (esp. #2 — the canonical citation rule), and the output
template. Sub-agent prompt template:

```
You are the ARCHITECTURE ARCHAEOLOGIST for subsystem `<name>` at `<path>`.

## Mission
Produce `docs/architecture/subsystems/<name>.md` describing WHAT the subsystem
is and WHY it exists in its current shape, grounded in code and git history.

## Rules (non-negotiable — see SKILL.md Core Principle 2 for the canonical form)
- Every claim cites a file:line or 7-char commit SHA, or uses the literal
  phrase "unknown — needs review". No other form is acceptable.
- Prefer commit-message rationale > code-comment rationale > design-doc rationale.
- Do NOT summarize `.designs/` files as authoritative. You may reference them
  only as secondary context AFTER verifying the claim against code.

## Investigation sequence
1. Read the subsystem's top-level files. Identify public types, functions, entry points.
2. `git log --follow --format=%h%x09%s <path>` — list formative commits. Fetch the
   full message of any whose subject references an issue, an architectural term
   (invariant, contract, trust, seam, gate, or project-specific constraint tags),
   or a meaningful verb (introduce, replace, deprecate, split).
3. Identify seams — places the subsystem calls OUT to or is called IN from.
   Document the contract of each seam (types, errors, trust direction).
4. Find every code comment that references an issue (`#\d+`), a constraint tag
   (`C-\d`, `H-\d`, `D\d+`, `R-\w+`), or an invariant phrase.
5. Write the output file following the template below.

## Output template
# <Name> subsystem

**Covers:** <comma-separated list of internal/<pkg>, py/<pkg>, or hooks paths this
file is the owner for — Phase 2.5 validate.sh unions every file's Covers: line
and requires it to cover every path named in inventory.md>

## Shape
<3-5 sentences with file:line anchors>

## Public surface
<exported types/functions; one-line contract each; anchored>

## Seams (what it touches)
| Seam | Direction | Contract | Anchor |
|------|-----------|----------|--------|

## Formative commits
| SHA | Date | Subject | Why it matters |
|-----|------|---------|----------------|

## Load-bearing invariants (this subsystem's contribution)
<must cite>

## Cross-referenced idioms
<references to idioms.md; Phase 4 will fill in the back-link>

## Gaps
<literal "unknown — needs review" entries where rationale unanchorable>
```

**Wait for all sub-agents to complete.** Collect outputs in
`docs/architecture/subsystems/`.

### Phase 2.5 — Validation gate (mechanical)

**PRECONDITION:** Phase 2 dispatch returned. Subsystem files exist.

Run `.claude/skills/architecture-docs/validate.sh`. The script checks:

1. Subsystem coverage — every `internal/*`, `py/*`, or `hooks` path in
   inventory.md is claimed by some `subsystems/<name>.md` via its
   `**Covers:**` header. Grouping (one file covering multiple paths) is
   allowed; silent drops are not.
2. Citation density — claim-bearing paragraphs > 400 chars in
   non-navigation sections must anchor to `file:line`, a 7-hex commit
   SHA, or the literal `"unknown — needs review"`. Navigation / how-to
   / ADR Consequences / ADR Corpus-links sections are exempt.
3. `gaps.md` if it exists is non-trivial (> 10 lines).
4. `idioms.md` if it exists has enumerated call-site rows.
5. Required top-level files present.

**If validate.sh exits non-zero: HALT.** Do not proceed to Phase 3.
For each failure, either:
- Re-dispatch the relevant sub-agent to fix the citation gap, or
- Add an explicit `unknown — needs review` entry to the file naming the
  gap (not a blanket rewrite — a specific labeled hole).

Then re-run validate.sh until it exits 0.

### Phase 3 — Cross-cutting idiom detection

**PRECONDITION:** Phase 2.5 validation passed. All subsystem files exist
and pass citation density checks.

This phase turns individual-subsystem findings into standing,
cross-referenceable idiom documentation — the primary output future
design reviews consume. Run foreground (not parallel) — requires reading
all subsystem outputs together.

1. **Collect candidate idioms** from Phase 1 grep hits plus every
   subsystem file's "cross-referenced idioms" section.
2. **For each candidate:**
   - Enumerate every call site (`Grep` for the pattern, list every
     file:line).
   - Classify each site: what access-control axis / trust boundary /
     invariant does this site leverage? What does misuse look like?
   - If ≥ 2 sites share the same semantic, promote to idiom. If 1 site,
     demote to subsystem-local (do not include in `idioms.md`).
3. **For each promoted idiom, write a section in `idioms.md`:**

   ```
   ## <idiom name>

   **Shape:** <code shape, canonical example>
   **Semantic:** <what it means>
   **Enforcement:** <type system / lint / convention / nothing>
   **Call sites:**
   | File:line | Axis leveraged | Notes |
   |-----------|---------------|-------|

   **Deviation detector:** <how to spot a divergent design>
   **If bypassing, you must:** <checklist>
   ```

**Output:** `docs/architecture/idioms.md`.

### Phase 4 — Invariant extraction

**PRECONDITION:** Phase 3 complete. `idioms.md` exists.

Combine signals from: subsystem files' "load-bearing invariants", MUST/
invariant comments, commit messages (traced via `git log -S`), and
CLAUDE.md claims (verified against code, not taken on faith).

For each candidate invariant:

```
## <invariant name>

**Statement:** <MUST-form one-sentence statement>
**Rationale:** <anchored to commit SHA if possible>
**Enforcement mechanism:** <types / runtime checks / combination.
                              If "convention only", say so — that is
                              itself a finding.>
**Consequences if violated:** <what breaks, with real example from
                                 git history if one exists>
**Relevant idioms:** <links to idioms.md>
```

**Output:** `docs/architecture/invariants.md`.

### Phase 5 — Trust boundaries and seams

**PRECONDITION:** Phase 4 complete.

1. `trust-boundaries.md` — from every site where trust changes:
   context-derived identity sources, caller-supplied vs system-supplied
   values, override policy, cross-process trust (wire protocols,
   subprocess boundaries, hooks).
2. `seams.md` — from every cross-subsystem contract: wire protocols,
   shared types, error-propagation contracts.

### Phase 6 — History and gaps

**PRECONDITION:** Phase 5 complete.

1. `history.md` — timeline of formative commits grouped by theme. Each
   entry: SHA, date, subject, one-line architectural consequence.
2. `gaps.md` — invariants claimed but not enforced, patterns that look
   invariant-like but are unanchored, and all `"unknown — needs review"`
   entries from subsystem files. This is the to-do list for the next
   iteration and must not be empty.

### Phase 7 — Index and integration hooks

**PRECONDITION:** Phases 0–6 complete.

1. Write `docs/architecture/README.md`:
   - Overview layer first (overview, containers, flows, adrs),
     corpus second (invariants, idioms, etc.).
   - "How to use during design review" concrete checklist.
   - "How to refresh" and "How to read" sections.
2. Propose (do not auto-edit) a hook into `/design-v5` and
   `/rootcause-review` recommending authors cross-reference `idioms.md`
   and `invariants.md` before finalizing options.

### Phase 8 — Overview layer

**PRECONDITION:** Phases 0–7 complete. `validate.sh` passed at Phase 2.5.

The overview layer (C4 L1/L2 diagrams, sequence flows, Nygard ADRs) is a
reader-facing surface on top of the anchor-dense corpus. Full phase
instructions live in the companion file:

**See `.claude/skills/architecture-docs/overview-phase.md`** for the
detailed step-by-step (8.1 C4 L1, 8.2 C4 L2, 8.3 sequence flows, 8.4
ADR extraction, 8.5 README update).

Exit this phase only when `overview.md`, `containers.md`, at least 2
flow files, and ≥ 8 ADR files exist, and the updated `README.md` points
at the overview layer first.

### Phase 9 — Final verification

**PRECONDITION:** Phase 8 complete.

Re-run `validate.sh` against the complete corpus + overview layer. All
five checks must pass. If any fail, halt and report specifically which
check failed and what the agent produced.

Optional: if invoked with `--verify-idempotent`, run the full skill
again against the just-produced tree and `git diff --stat
docs/architecture/`. Any non-empty diff violates Core Principle 5.

## Rules (apply across all phases)

- **Citation rule.** See Core Principle 2. Enforced by `validate.sh`.
- **Design docs under `.designs/` are secondary.** Reference only after
  verifying against code. If a design doc asserts X and the code does Y,
  document Y and flag the drift in `gaps.md`.
- **Do not collapse patterns prematurely.** If three call sites look
  similar but leverage different axes, they are three idioms, not one.
- **Do not invent intent.** Either the commit message or the code
  comment states intent, or it is `unknown — needs review`.
- **Do not summarize.** The output's value is in the citations, not the
  prose. A paragraph with no anchor is a paragraph that shouldn't exist
  (and `validate.sh` will flag it).
- **Leave unstaged.** Produce files; do not `git add` or `git commit`.

## Anti-patterns

1. **Design-doc regurgitation.** Copying prose from `.designs/` without
   verifying against code is the specific failure this skill exists to
   prevent.
2. **"The system does X."** Passive summary prose with no anchor.
   Replace with "`<file:line>` does X (`<commit SHA>`)".
3. **Single-site idioms.** One call site is not an idiom. Either find
   the second site or demote.
4. **Inventing rationale to fill a cell.** `"unknown — needs review"`
   is always better than plausible-sounding fiction.
5. **Writing a history of refactors nobody will read.** `history.md` is
   for architecturally formative commits, not routine refactors.
6. **Scoping out hard subsystems.** If a subsystem is messy, document
   it messy with honest `gaps.md` entries. Silent omission is the
   design-doc failure mode this skill must not reproduce.
7. **Treating CLAUDE.md as ground truth.** CLAUDE.md is instructions
   for agents, not an architectural source of truth.
8. **Skipping validation.** Phase 2.5 and Phase 9 are mechanical gates.
   If `validate.sh` reports failure, the output is not done — no matter
   how finished the prose looks.

## How this skill's output is consumed

- `/design-v5` Phase 1: author cross-references `idioms.md` and
  `invariants.md` when enumerating options. Any option that bypasses
  or extends an idiom must name the deviation.
- `/rootcause-review`: reviewer checks the proposed fix against
  `idioms.md` for canonical-pattern alignment.
- `/ultra-implement` Phase 1 investigators: Code Archaeologist's
  investigation includes "does this codebase have a canonical idiom
  for this concern?" — `idioms.md` answers by lookup, not guessing.
- Overview layer consumption: new contributors and design-review
  authors start at `overview.md` → `containers.md` → the relevant flow,
  then drill into the corpus for anchors.

## Success criteria (checked mechanically by validate.sh at Phase 9)

- [ ] `/docs/architecture/` contains all files in "Output structure"
- [ ] Every claim in every file has a citation or the literal phrase
      `"unknown — needs review"` (no unanchored paragraphs > 200 chars)
- [ ] `idioms.md` contains at least one idiom with ≥ 2 call sites
- [ ] Each invariant in `invariants.md` states its enforcement mechanism
      concretely
- [ ] `gaps.md` is non-trivial (> 10 lines)
- [ ] `README.md` has the "how to use during design review" checklist
      and points at the overview layer first
- [ ] Overview layer present: `overview.md`, `containers.md`, ≥ 2 flow
      files, ≥ 8 ADR files
- [ ] `/architecture-docs --verify-idempotent` produces empty
      `git diff --stat` on unchanged tree
- [ ] All artifacts left unstaged for user review
