---
name: architecture-diagram
description: "Turns one design document (a design-doc.md, for example .designs/NNN/design-doc.md) into architecture diagrams only: a C4 system context and container view, plus component, sequence, deployment and security data-flow views when the document states that content. Every element and relationship cites a line of the document, and every name, summary, technology, data item, flow message and assertion is a verbatim quote, checked by bundled gates; relationship verbs are short phrasings of a cited statement. Writes model.json, Mermaid files, Miro-paste Mermaid and one interactive index.html into an architecture-diagrams folder beside the document. Use when asked to diagram, visualize, draw, map or picture a design doc, to show a CTO or reviewer how a proposed design fits together, or to put a design on a Miro board, even if the request never says diagram, C4 or Mermaid. Not for documenting the current codebase from code (that is architecture-docs); never writes prose and never edits the design document."
argument-hint: "[path/to/design-doc.md]"
allowed-tools: "Read, Grep, Glob, Write, Edit, Bash(node ${CLAUDE_SKILL_DIR}/scripts/*)"
compatibility: "Claude Code. Needs node 22.13+ or 24+ and npm. The validator installs pinned mermaid 12.1.0 and jsdom 29.1.1 into ~/.cache/architecture-diagram once (about 225 MB, network once). The page loads mermaid 12.1.0 from cdn.jsdelivr.net with subresource integrity and needs a 2024-or-newer browser (Safari 17.4+)."
metadata:
  version: "1.0.0"
---

# Architecture diagrams from a design doc

Input: `$ARGUMENTS` — a design-doc.md path, or a directory holding one. Empty means discover.

## Hard rules

1. **Diagrams only.** Output is `model.json`, Mermaid files and `index.html`. Never write a `.md` file, a report, or an ADR; never edit the design doc.
2. **The design doc is the only source.** Every element, relationship, boundary, technology, change marker and assertion comes from a verbatim, line-cited quote in it; relationship verbs restate a cited sentence in at most six words, using the doc's own words where it has them. When the doc is silent, the model is silent (`null`, `unstated`, a skipped view).
3. **Never invent to fill a view.** A view without stated facts is skipped or marked not-produced with its reason. A placeholder actor, a guessed technology or a descriptive cluster name is a defect.
4. **Scripts render; you never hand-write Mermaid or HTML.** Every fix goes into `model.json`, then render again. Generated files are never edited by hand.
5. **Gates are not optional.** Run every gate on every doc, small or large. Each gate allows at most 3 repair rounds after its first run, then HALT with the gate output quoted verbatim. Passing gates is necessary, not sufficient: read the generated `views/*.mmd` against the doc, and if they misstate it, fix `model.json` and run P3–P5 again (this uses the same per-gate budget).
6. **Never ask the operator a question.** Ambiguity becomes an `open` item or a recorded skip.
7. Never run `af`, `git add` or `git commit`.

## Phases

| Phase | Precondition | Does | Gate | Blocks |
|---|---|---|---|---|
| P0 Input | — | resolve the design doc | `sections.mjs` exits 0 | everything |
| P1 Prepare | P0 passed | create or empty `architecture-diagrams/` beside the doc, preserving prior output | `render.mjs --prepare` exits 0 | P2 |
| P2 Extract | P1 passed | read the doc and references, write `model.json` | P3 | P3 |
| P3 Model gate | `model.json` written | 12 checks: citations, verbatim text, hierarchy, change words, coverage of the doc, view decisions, caps, coverage of the model, acronyms | `check_model.mjs` exits 0 | P4 |
| P4 Render | P3 passed | generate views, Miro files, `index.html` | `render.mjs` exits 0 | P5 |
| P5 Validate | P4 passed | parse every diagram with pinned Mermaid; lint; compare to the model; check the page | `validate.mjs` exits 0 | P6 |
| P6 Report | P5 passed, or a HALT | final report | — | — |

Copy this checklist into your response and tick each item only when its command has exited 0 in this run:

```
- [ ] P0 design doc resolved; sections.mjs output read
- [ ] P1 output directory prepared
- [ ] P2 references/extraction.md, references/model.md, references/views.md read; model.json written (no command; tick when the file exists)
- [ ] P3 check_model.mjs exit 0
- [ ] P4 render.mjs exit 0
- [ ] P5 validate.mjs exit 0 (final gate output quoted in the report)
```

## P0 Input

Run `node ${CLAUDE_SKILL_DIR}/scripts/sections.mjs <path>` with the argument. With no argument, run `node ${CLAUDE_SKILL_DIR}/scripts/sections.mjs --discover`; it exits 0 only when exactly one `design-doc.md` exists under the working directory. If it exits 2, report the candidates it printed and stop with `STATUS: HALT input-ambiguous` (or `input-missing`), writing nothing.

Keep its JSON. `sha256`, `lines` and `body` go into `meta.source`. `component_items` and `arrow_lines` are what the coverage checks hold you to: every entry needs an inventory or ledger record. `where_to_look` and heading `hint`s only say where headings suggest content; they classify nothing. To see a line exactly as citations are compared, run `node ${CLAUDE_SKILL_DIR}/scripts/sections.mjs --quote <doc> <a> [b]`.

## P1 Prepare

Run `node ${CLAUDE_SKILL_DIR}/scripts/render.mjs --prepare <design-dir>/architecture-diagrams`. It copies existing output that git does not hold unchanged to `~/.cache/architecture-diagram/prev/…` before emptying the directory, and prints that path; carry it into the report.

## P2 Extract

Read [references/extraction.md](references/extraction.md) and [references/model.md](references/model.md) before writing anything, and [references/views.md](references/views.md) before deciding views. Then read the design doc body in full with Read.

Write `<design-dir>/architecture-diagrams/model.json` with Write, following the contract. Order of work:
1. `meta` from the `sections.mjs` output; `system_element` per extraction E1.
2. Elements from every `component_items` entry (inventory record each), then persons, externals and stores from the rest of the body.
3. Relationships from every `arrow_lines` entry (arrow_ledger record each, citing that line), then from prose.
4. Flows (proposed and current), deployments, trust boundaries, open items, glossary.
5. The six `view_decisions`, then `views`. The gate states the decision and view set the model supports when yours differ.
6. An exclusion `not-in-any-view` for anything modelled that no view shows.

## P3 Model gate

Run `node ${CLAUDE_SKILL_DIR}/scripts/check_model.mjs <design-dir>/architecture-diagrams/model.json`. Each failing check lists its errors; repair `model.json` with Edit and run it again. A failure that would need a quote the doc does not contain is fixed by removing or nulling the claim, never by rewording the quote. After 3 failing rounds: `STATUS: HALT model-gate`.

## P4 Render

Run `node ${CLAUDE_SKILL_DIR}/scripts/render.mjs <design-dir>/architecture-diagrams/model.json`. It refuses unless the model gate passes.

## P5 Validate

Run `node ${CLAUDE_SKILL_DIR}/scripts/validate.mjs <design-dir>/architecture-diagrams`. The first run installs the pinned validator packages; an install or node-version failure exits 3 with npm's own error text: report it verbatim as `STATUS: HALT environment`. For any other failure, repair `model.json`, then run P3, P4 and P5 again; after 3 failing rounds: `STATUS: HALT validate`.

## Banned moves

- **Writing Mermaid or HTML by hand, or editing generated files.** The validator compares every file with a fresh render and fails on any difference.
- **Paraphrasing a name, summary, technology, assertion or data item.** The model gate requires each to be a verbatim part of its citation; paraphrase is how invented claims enter a diagram.
- **Drawing a design option the doc withdrew as `[REMOVED]`.** "Removed from earlier versions" means design history: exclusion `withdrawn-from-design`.
- **Promoting a heading prefix (`Host:`, `Container:`) into a trust boundary.** A trust boundary needs a sentence saying what must not cross it.
- **Taking facts from files next to the design doc, from code, or from memory.** The doc may have rejected what they say.
- **Dropping an awkward component or arrow.** Every component item and arrow line needs an inventory or ledger record; use an exclusion with its reason.
- **Skipping a gate because the doc is small, or claiming a gate passed without running it in this run.**

## Gotchas

- Headings inside fenced blocks are quoted text; `sections.mjs` ignores them, so do not cite them as structure.
- Text after the body (a later H1 such as an appended peer review) is not the design.
- Ids must avoid Mermaid keywords (`end`, `click`, `style`, `class`, `legend`, …); the gate lists the reserved set.
- A Docker container or host is a deployment node, not a C4 container.
- An AI agent or automated job is a container or system, never a person.
- Miro lays diagrams out again after paste; the layout on the page is not preserved there.

## Glossary

- **design doc**: the input markdown file; only its body counts.
- **model**: `model.json`, the single source every output is generated from.
- **element / relationship / boundary**: a box, an arrow, a dashed grouping (trust boundary, deployment node, or a system or container scope drawn from `parent`).
- **view**: one diagram; **view decision**: the recorded produce, skip or not-produced choice for a view type.
- **canonical source**: `views/NN-*.mmd`; **Miro source**: `miro/NN-*.mmd`.

## Final report

Report exactly this, with nothing claimed that a command in this run did not show:

```
STATUS: COMPLETE | COMPLETE-WITH-GAPS | HALT <input-ambiguous|input-missing|model-gate|validate|environment>
Design doc: <path> (sha256 <first 12>)
Open: <design-dir>/architecture-diagrams/index.html
Views: <id — title> for each produced view
Not drawn: <type — decision — reason> for each non-produced view; <n> exclusions listed on the page
Final gate: <the "[n/N] … PASS|FAIL" lines from the last validate.mjs run, verbatim>
Previous output preserved at: <path, or none>
The generated files are new and untracked; nothing was staged or committed.
```

COMPLETE-WITH-GAPS means the context or container view is `not-produced`. On HALT, quote the failing gate's output verbatim and list what was written.
