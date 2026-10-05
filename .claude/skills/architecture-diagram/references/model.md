# model.json contract (`architecture-diagram.model/1`)

Contents: [Citations](#citations) · [meta](#meta) · [elements](#elements) · [relationships](#relationships) · [boundaries](#boundaries) · [flows](#flows) · [deployments](#deployments) · [open, glossary, exclusions](#open-glossary-exclusions) · [inventory, arrow_ledger](#inventory-arrow_ledger) · [view_decisions, views](#view_decisions-views)

`check_model.mjs` enforces every rule below. Every top-level key is required; use `[]` when empty. A citation field with nothing to cite (`assertion_src` for a null assertion, `technology_src` for null technology, `delta_src` for `unstated`, `evidence` or `headings_searched` for a produced decision) is `[]`.

## Citations

A citation is `{"lines": [a, b], "quote": "..."}`. `quote` must appear verbatim in design-doc lines a..b after both sides drop backticks and `**` bold markers and collapse whitespace (a single `*` is kept, because it is often code). Lines must fall inside `meta.source.body`. Cite the narrowest line range that holds the quote. `node ${CLAUDE_SKILL_DIR}/scripts/sections.mjs --quote <doc> <a> [b]` prints lines exactly as they are compared.

## meta

```json
"schema": "architecture-diagram.model/1",
"meta": {
  "source": {"path": "<absolute path of the design doc>", "sha256": "<from sections.mjs>", "lines": 289, "body": [1, 289]},
  "system_element": "<id of the software_system in scope>",
  "queue_style": "container"
}
```
`sha256`, `lines` and `body` are copied from the `sections.mjs` output. `queue_style` is `container` when the doc treats a broker or queue as a part of the design, `via` when it only mentions messages travelling through one (then put "via <topic>" in the relationship `technology`), `none` when it mentions no broker or queue. Unknown top-level keys are rejected.

## elements

| Field | Rule |
|---|---|
| `id` | `^[a-z][a-z0-9_]*$`, ≤40 chars, unique across all ids, not a Mermaid keyword (`end`, `click`, `style`, `legend`, …) |
| `kind` | `person`, `software_system`, `container`, `component`, `external_system`, `data_store`, `queue` |
| `name` | ≤48 chars, verbatim (case-insensitive) inside its `src` quotes |
| `summary` | `null`, or ≤80 chars verbatim inside its `src` quotes; may be cut at a word and end with `…` |
| `technology` + `technology_src` | `null`, or verbatim inside `technology_src` |
| `parent` | component → its container; container, data_store, queue → the system (or `null` when outside it); person, external_system, software_system → `null` |
| `delta` + `delta_src` | `new`, `changed`, `removed`, `existing`, `unstated`; anything but `unstated` needs a `delta_src` quote in which one sentence, semicolon clause or table cell names the element (its `name`, an alias or a `doc_ids` entry) and holds a word of that class not negated by no/not/without/never in the three words before it. Tags drawn: `[NEW]`, `[CHANGED]`, `[REMOVED]`, `[EXISTING]`; `unstated` draws no tag |
| `doc_ids` | the doc's own ids (`K13`); shown in the details panel only, never in labels |
| `aliases` | optional: other names the doc uses for this element, including functions or files lifted into it (`identityToAddress()`); each verbatim in `src`; a change quote may name the element by an alias |
| `cluster_of` | optional; see [views.md](views.md#over-cap) |
| `src` | ≥1 citation (a cluster may have `[]`) |

## relationships

| Field | Rule |
|---|---|
| `id`, `from`, `to` | element ids; `from` is the initiator, `to` the responder |
| `verb` | 1–6 words that read `from → to`; never starts with uses, has, contains, manages, supports, relates, integrates, interacts, communicates, connects, talks |
| `technology` + `technology_src` | as for elements |
| `interaction` | `sync` (request) or `async` (message) |
| `data` | `null`, or the data item, verbatim in `src` |
| `sensitive` | `true` only when the doc names a credential, key, token, personal data or privilege in this flow; then `data` is required |
| `data_direction` | optional `forward` (default) or `reverse` (data moves `to → from`, as in a fetch) |
| `delta` + `delta_src`, `src` | as for elements; the `delta_src` quote names the `from` or `to` element |

## boundaries

`{id, kind, name, technology, parent, members, src}` plus `rule` for trust boundaries.
- `kind`: `trust`, `deployment_node`, `group`. The system and container boundaries are drawn from element `parent` links and are never listed here.
- `name` and `technology` verbatim in `src`; `parent` is another boundary id or `null`; `members` are element ids and cover their descendants (listing a container puts its components inside too); deployment nodes take their members from placements and use `[]`.
- `trust` needs `rule`: the verbatim part of a `src` quote that says what must not cross, containing a restriction word (never, not, no, must, only, cannot, without, outside, refuses).

## flows

`{id, name, state, src, steps}`; `name` ≤60 chars verbatim in `src`; `state` is `proposed` or `current` (both are drawn; current flows are titled as current behaviour). Each step is `{from, to, message, interaction, src, when}` with `interaction` `sync`, `async` or `reply`, `message` 1–120 chars verbatim in the step's `src`, and optional `when`: the verbatim condition (≤80 chars) under which the step happens, such as a refusal path; consecutive steps with the same `when` are drawn inside one `opt` box. Every non-reply step between two different elements needs a relationship between the same elements or their ancestors; a step with `from` = `to` (work inside one element) needs none.

## deployments

`{id, environment, environment_src, placements}`; `environment` is `null` or verbatim in `environment_src`. Each placement is `{element, node, src}` where `node` is a `deployment_node` boundary.

## open, glossary, exclusions

- `open`: `{text, element_ids, src}`, `text` verbatim in `src` — only what the doc leaves unresolved: open questions, undecided contradictions, claims it marks unverified or TBD. Limits and caveats the doc accepts or decides are not open. Linked elements get `[OPEN]`.
- `glossary`: `{term, expansion, src}` for every acronym in a label that is not on the allowlist; `term` is the exact token including digits (`WSL2`). A literal all-capitals name that is not an acronym (a file such as `REVOKED`) gets `expansion: null` with a citation and is left out of the key (API, CA, CI, CLI, CPU, CSS, DNS, HTML, HTTP, HTTPS, ID, JSON, OS, PDF, PR, REST, SQL, SSH, TCP, TLS, UDP, UI, URL, XML, YAML). `src` is citations, or the string `"standard"` for a well-known expansion.
- `exclusions`: `{what, reason, src, ids}` with `what` verbatim in `src` and `reason` one of `deliverable`, `withdrawn-from-design`, `code-level`, `current-state`, `build-order`, `control-flow`, `file-relation`, `plumbing`, `no-relationship-stated`, `view-limit`, `ambiguous-target`, `out-of-scope`, `not-a-relationship`, `not-in-any-view`. `ids` (optional) names the model ids the exclusion covers; `view-limit` and `not-in-any-view` exclusions need it.

## inventory, arrow_ledger

- `inventory`: one record per `component_items[].line` from `sections.mjs`: `{line, element_ids: [...]}` or `{line, exclusion: <index into exclusions>}`. Every listed element, or the exclusion, must cite that line.
- `arrow_ledger`: one record per `arrow_lines[].line`: `{line, relationship_ids}`, `{line, flow_ids}` or `{line, exclusion: <index>}`. Every listed relationship (in `src`), flow (in its `src` or a step's `src`), or the exclusion must cite that line.

These prove nothing in the doc's component list or arrows was dropped silently. Separately, every element and relationship in the model must appear in at least one produced view or be covered by a `not-in-any-view` exclusion.

## view_decisions, views

- `view_decisions`: exactly one record per type `context`, `container`, `component`, `dynamic`, `deployment`, `security`: `{type, decision, reason, evidence, headings_searched}`. `decision` is `produced`, `skipped`, or `not-produced` (context and container only), and it must equal what the model supports ([views.md](views.md)); the gate names the expected value. A non-produced decision needs a `reason` and `evidence` citations or `headings_searched` (heading texts as plain strings); a produced decision may leave `reason` empty.
- `views`: `{type, of, assertion, assertion_src}`. `of` is the container id (component), flow id (dynamic), deployment id (deployment), else `null`. The set of views must equal what the model supports; the gate prints the expected `of` values. `assertion` is `null` or one sentence ≤200 chars verbatim in `assertion_src` stating the view's takeaway. Titles, questions, ids, membership, edges and legends are generated; never write them.
