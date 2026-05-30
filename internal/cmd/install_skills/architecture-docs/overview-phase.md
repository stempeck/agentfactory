# Phase 8 — Overview layer

This phase produces the **high-level navigable layer** on top of the
anchor-dense corpus (idioms, invariants, trust-boundaries, seams,
subsystems, history, gaps). The corpus is optimized for drift detection;
the overview layer is optimized for reader comprehension and design-review
entry. Both are needed.

**PRECONDITION:** Phases 0–7 complete. All required files from
`success criteria` present. `validate.sh` reported PASS.

**Outputs written by this phase:**

```
docs/architecture/
├── overview.md              # C4 L1 system context + trust direction sketch + index
├── containers.md            # C4 L2 container diagram + cross-container seams table
├── flows/
│   ├── <primary-flow-1>.md  # Sequence diagram with file:line anchors
│   ├── <primary-flow-2>.md
│   └── <primary-flow-3>.md
└── adrs/
    ├── README.md            # ADR index
    └── ADR-NNN-<slug>.md    # Nygard-format ADRs, one per major decision
```

---

## Step 8.1 — C4 L1 (System context)

Produce `overview.md`:

1. One-paragraph "what this system is." Must include file:line anchor
   to the main entry point (e.g., `cmd/af/main.go`).
2. One mermaid `flowchart TB` showing: the user, the main CLI/service
   process, external dependencies (databases, subprocesses, APIs),
   persistent stores. Use real wire protocols on the arrows
   (e.g., "JSON-RPC over loopback", "tmux subprocess").
3. A **trust-direction sketch** (ASCII, not mermaid — it must be
   diff-able line-by-line). Sole writers of identity-bearing context
   are named with the file:line that writes them.
4. A **"where to read next" table** pointing into the corpus
   (one row per corpus file, one-line purpose).
5. A **subsystem-at-a-glance table** (one row per subsystem, linking to
   `subsystems/<name>.md`).
6. A **"key decisions at a glance" table** listing every ADR with a
   one-line summary and a link to `adrs/ADR-NNN-*.md`.

Every arrow label and every claim must either anchor or link to the
ADR/subsystem file that does.

## Step 8.2 — C4 L2 (Containers)

Produce `containers.md`:

1. One mermaid `flowchart TB` with **subgraphs per runtime process and
   per persistent store**. Each inner node is a package/module with a
   one-line purpose. Arrows use the real wire protocol, not prose
   ("HTTP JSON-RPC 127.0.0.1:ephemeral", not "talks to").
2. A **container inventory table** (type, lifetime, anchor).
3. A **cross-container contracts table** — one row per seam; every row
   links to the corresponding entry in `seams.md`.
4. A **trust-boundaries table** (From → To, trust model, anchor). Every
   row links to `trust-boundaries.md`.
5. A short "why this shape" subsection — exactly one paragraph that
   references the ADR(s) justifying the process split (e.g., ADR-001 for
   MCP-over-library).

## Step 8.3 — Sequence flows

Identify the **load-bearing flows** — the command paths whose failure
would be catastrophic for users. Typical candidates:
- The primary "start work" command (e.g., `af up`, `start`, `launch`).
- The primary "dispatch work" command.
- The primary "complete work" cascade including any subprocess, cleanup,
  notification chain.

For each flow, produce `flows/<flow-name>.md`:

1. A mermaid `sequenceDiagram` of the actual call order. Participants
   are packages/modules, not individual functions. Notes beside arrows
   carry the file:line anchor for the call.
2. A **call-site anchors table** — one row per step, file:line.
3. An **invariants-active table** — which invariants from
   `invariants.md` and idioms from `idioms.md` this flow must preserve.
   Link each to the corresponding corpus entry.
4. A **failure modes table** — each failure condition, where it's
   handled, what the user sees.
5. (For flows that write `.runtime/` state) a **state-written table** —
   each file, writer, purpose, anchor.

**Do not invent flows.** Start from the cmd-layer entry points listed
in `subsystems/cmd.md` (or equivalent). If a flow's sequence cannot be
anchored end-to-end to real code, label the unanchored step
`unknown — needs review` and flag it in `gaps.md`.

## Step 8.4 — ADR extraction

ADRs are **distilled from the corpus**, not invented. Read through
`invariants.md`, `idioms.md`, `history.md`, and `trust-boundaries.md`
and extract the **decisions** — moments where the codebase chose one
path over an alternative. A decision is ADR-worthy if:

- It has a visible counterfactual (another option was on the table), and
- Reversing it would cost real work, and
- At least one idiom, invariant, or subsystem in the corpus leans on it.

Expected yield: ~10-15 ADRs for a mature codebase. Fewer than 8 means
you're missing some — re-read `history.md` for themes.

Write `adrs/README.md` — one-table index: number, title, status
(Accepted / Superseded / Deprecated), primary anchor. Number ADRs
sequentially from 001; never renumber. Never delete an ADR — supersede
with a successor and link both directions.

For each ADR, write `adrs/ADR-NNN-<slug>.md` in **Nygard format**:

```markdown
# ADR-NNN: <Title>

**Status:** Accepted | Superseded by ADR-MMM | Deprecated
**Date:** <SHA-anchored date, e.g., "2026-04-16 (commit ba77510)">

## Context

<What situation forced the decision. Include the counterfactual — what
was the other option? Anchor to real code or commit messages.>

## Decision

<What was decided, stated in one or two sentences. If an artifact
embodies the decision — a specific commit, a specific test, a specific
invariant number — name it here.>

## Consequences

**Accepted costs:**
- <Concrete bad thing we tolerate as a result.>

**Earned properties:**
- <Concrete good thing we get as a result.>

## Corpus links

- <link to the idiom, invariant, seam, or subsystem file that
  implements this decision>
- <link to history.md timeline entry>
- Related ADRs: <ADR-MMM, ADR-PPP>
```

**Rules for ADRs:**
- Every Context statement must anchor to code or a commit.
- Every Consequence must be concrete — no "improved maintainability"
  without evidence.
- Every ADR must have at least one "Corpus links" entry pointing into
  the corpus.
- If you can't write a concrete Context with a counterfactual, it is
  not an ADR — it's a fact about the system and belongs in `invariants.md`.

## Step 8.5 — Update `docs/architecture/README.md`

The overview layer changes the reading order. Rewrite the top of
`README.md` to present overview files first (overview.md,
containers.md, flows/, adrs/) and the corpus second
(invariants/idioms/trust-boundaries/seams/subsystems/history/gaps).

After writing, re-run `validate.sh` to confirm:
- All overview files pass citation-density checks.
- `adrs/README.md` indexes every ADR file present.
- No dangling links into the corpus.

---

## Anti-patterns for Phase 8

1. **Diagram-first.** Don't draw a diagram and then backfill anchors.
   Collect the anchors first (from the corpus), then draw only what they
   support.
2. **ADR invention.** ADRs capture *existing* decisions. Writing
   "ADR-013: adopt new pattern X" for something you wish were true is
   out of scope — that's a design doc, not an ADR.
3. **Overview contradicting corpus.** If a mermaid label says "direct
   SQLite access" but `subsystems/py-issuestore.md` says MCP over
   JSON-RPC, the corpus wins and the diagram is wrong. Fix the diagram.
4. **Flow diagrams with no anchors.** A sequence diagram whose steps
   are not traceable to file:line is fiction. If you can't anchor a
   step, mark it `unknown — needs review` and flag in gaps.md.
5. **ADRs in past tense without dates.** Nygard ADRs are dated
   documents. The date anchors them to a specific commit or period so
   later readers can tell what the world looked like when the decision
   was made.

---

## Exit criteria for Phase 8

- `overview.md`, `containers.md`, at least 2 flow files, and at least
  8 ADR files exist.
- `validate.sh` exits 0 with the overview layer in place.
- `docs/architecture/README.md` points at the overview layer first.
- Every ADR has at least one corpus backlink and every corpus file it
  backlinks to exists.
