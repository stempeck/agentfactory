# View decisions

Contents: [The six views](#the-six-views) · [Caps](#caps) · [Over cap](#over-cap) · [What the renderer does](#what-the-renderer-does)

Decide all six on every run and record each in `view_decisions`. The decisions follow mechanically from the model, and `check_model.mjs` names the expected decision and view set when yours differ. Views appear in this order; the page opens on the first produced one.

## The six views

| Type | Answers | Produce when | Otherwise | Per run |
|---|---|---|---|---|
| `context` (C4 level 1) | What is the system for, who uses it, what outside it does it depend on? | a relationship connects the system (or anything inside it) to an outside element | `not-produced` (run status COMPLETE-WITH-GAPS); never pad with invented actors | 1 |
| `container` (C4 level 2) | Which applications and data stores make up the system and how do they communicate? | the system has ≥2 child containers, data stores or queues | exactly 1: `skipped` "would repeat Context"; 0: `not-produced` | 1 |
| `component` (C4 level 3) | What does this design add or change inside container X? | the container holds ≥1 component with delta `new`, `changed` or `removed`, or ≥2 components | `skipped` when no container qualifies; ranked by changed components then component count; beyond 3, exclusion `view-limit` with the container id in `ids` | ≤3 |
| `dynamic` (sequence) | In what order do the elements interact to carry out flow F? | a flow exists (proposed or current) | `skipped` "no flow stated"; ranked proposed first, then by new/changed participants, then by step count; beyond 3, exclusion `view-limit` with the flow id in `ids` | ≤3 |
| `deployment` | Where does each part run? | a deployment places elements on ≥2 deployment nodes | `skipped` "single deployable unit" or "no placement stated"; ranked by placement count; beyond 2, exclusion `view-limit` | ≤2 (one per named environment) |
| `security` (data-flow) | Where do credentials, keys or sensitive data cross a stated trust boundary? | ≥1 trust boundary with a `rule` and ≥1 sensitive relationship touching one of its members or their children | `skipped` "no trust boundary stated" — a data-flow view without a stated boundary is never drawn | 1 |

`assertion`: give one only when a single sentence of the doc, at most 200 characters, states the view's takeaway (the goal sentence for context, the trust rule for security). A longer goal sentence or a table fragment means `null`; the page then leads with the view's question.

## Caps

Skill choices, not standards: Context ≤9 element nodes; every other view ≤15 element nodes or participants; and per view, nodes + boundaries + edges + edge labels ≤80. The legend node does not count. `check_model.mjs` computes every view's membership and reports the counts.

## Over cap

1. Context: cluster external systems the doc groups (same kind, same parent) with a cluster element.
2. Other views: cluster elements with delta `existing` or `unstated` of the same kind under the same parent.
3. A cluster element is `{"id": "...", "kind": <members' kind>, "parent": <members' parent>, "cluster_of": [ids], "name": "<n> <kind label lowercase>s in <parent name or 'the environment'>", "summary": null, "technology": null, "delta": "unstated", "src": []}`, for example `"4 containers in agentfactory"`. The name is fixed by the gate; never invent a descriptive name. Relationships to members are drawn to the cluster; the details panel lists the members.
4. Still over cap: HALT with the counts.

## What the renderer does

Never author these; they come from the model:
- Membership: Context shows the system plus every person and external element connected to it, with relationships lifted to that level. Container shows the system boundary with its children plus connected outside elements. Component shows the container boundary with its components plus neighbours. Dynamic uses the flow's steps. Deployment nests deployment nodes, places element instances, and draws people and external systems those instances talk to beside the nodes. Security shows sensitive relationships in data direction inside stated trust boundaries, and its key prints each boundary's rule.
- Edges: relationships that lift onto the same pair are merged only when verb, data and interaction are identical (shown as ×n); every other relationship keeps its own arrow. Data items show on the arrow in parentheses.
- Titles, questions, ids (`NN-type[-of]`), drill-down links, breadcrumbs.
- Notation: shapes per kind (stadium person, rectangle internal, double-edged external, cylinder store, flag queue; rounded process in the data-flow view), thick border for new or changed, dotted for removed, `[NEW]`/`[CHANGED]`/`[REMOVED]`/`[OPEN]` tags, `[Kind: technology | not stated]`, solid arrows for synchronous requests and dashed for asynchronous messages, a key on every diagram, colour-blind-safe colours that never carry meaning alone.
- Outputs: `views/NN-*.mmd` (canonical Mermaid with frontmatter, accTitle, accDescr and click links), `miro/NN-*.mmd` (rectangles only, no frontmatter or links), `index.html` (navigation, details panel, element and relationship tables with design-doc line numbers, copy and SVG/PNG export, Miro paste steps).
