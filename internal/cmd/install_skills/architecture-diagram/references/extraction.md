# Extracting the model from a design doc

Contents: [Ground rules](#ground-rules) · [E1 Scope](#e1-scope) · [E2 Inventory](#e2-inventory) · [E3 Fields](#e3-fields) · [E4 Relationships](#e4-relationships) · [E5 Flows](#e5-flows) · [E6 Deployment](#e6-deployment) · [E7 Security](#e7-security) · [E8 Open items and acronyms](#e8-open-items-and-acronyms) · [Worked example](#worked-example)

## Ground rules

- The design doc is the only source. Never use sibling files in its directory, the codebase, `docs/architecture`, or general knowledge for any element, relationship, boundary, technology or change marker. A sibling analysis file may recommend an option the doc rejected.
- Read the whole body (`body` from `sections.mjs`). Text after `body` (an appended review or log under a later H1) is not the design.
- When the doc says nothing, the model says nothing: `technology: null`, `delta: "unstated"`, a skipped view. Never fill a gap with a plausible guess.
- Headings inside fenced blocks are quoted text, not structure; `sections.mjs` already ignores them.
- Contradictions: draw the side the doc marks as decided or superseding; if neither side is marked, record an `open` item citing both lines and link it to the affected elements.

## E1 Scope

- `system_element`: the software system the title or the purpose section names as being designed or changed. A fix or feature doc titles the change ("Mail Trailing-Slash Fix"); the system is the product it changes, named in the body. Cite the sentence that names it. If no system is named, record context and container as `not-produced` with reason "system in scope not named".
- `purpose` sections (Executive Summary, Overview, Goal) give the context view's `assertion` when one sentence states what the design achieves.

## E2 Inventory

Every `component_items` entry from `sections.mjs` gets an `inventory` record. Classify each item:

| Doc item | Model |
|---|---|
| A separately running application, service, process, CLI, worker or data store | `container` / `data_store` / `queue` inside the system |
| A module, package, library, file group or class family inside one running application | `component` with that container as `parent` |
| A function or method | not drawn: lift it to the component the doc associates it with (mention it in that component's src); with no stated association, exclusion `ambiguous-target` |
| A human role | `person`; an AI agent or automated job is never a person, it is a container or software system |
| Something outside the system the design talks to, including a separate program it runs as a command (a CLI such as `bd` or `tmux`) that the design does not ship | `external_system` (or `data_store` with `parent: null`) |
| One item spanning several running applications ("`quickdocker.sh` + repository CI") | one element per running application, each citing the row; the inventory record lists them all |
| A group of functions in one file, package or type | one component named by that file, package or type as the doc writes it; list the function names in `aliases` so change quotes that name them count |
| A third-party library or tool used inside a part of the system purely as implementation (a parser library, git inside an agent's workspace) | not an element unless the design configures or changes it; then a component of the part it runs in |
| An OS service or a store outside the system's code (OS keychain, a cloud bucket) | `external_system`, or `data_store` with `parent: null` |
| Files or stores the system itself writes | `data_store` inside the system |
| A store that is an OS service on one platform and a file the system writes on another | one `data_store` with `parent: null`, citing both statements |
| An AI agent or job the doc says the system runs | `container` inside the system; when the doc does not say the system runs it, `external_system` |
| Tests, docs, ADRs, runbooks, spikes, procedures, phases | exclusion `deliverable` |
| A row the doc says was removed from an earlier version of the design ("Removed (decided …)", "withdrawn", "rejected") | exclusion `withdrawn-from-design`; never drawn as `[REMOVED]` |
| An existing part of the system that the design deletes | element with `delta: "removed"` |
| Logging, generic load balancers, boilerplate auth not discussed in decisions or risks | exclusion `plumbing` |

A Docker container, VM or host is not a C4 container; it is a deployment node ([E6](#e6-deployment)).

## E3 Fields

- `name`: the doc's own words for the element, copied exactly (case may differ). Never the doc id (`K13`); put ids in `doc_ids`.
- `summary`: copy the doc's one-line description; cut at a word with `…` if it exceeds 80 characters; `null` if the doc gives none.
- `technology`: only words the doc uses for the element's technology (`Go`, `bash`, `python3 standard library`), cited in `technology_src`.
- `delta` from explicit words in a quote that names the element (its name or doc id), with precedence removed > new > changed > existing when one quote holds several:
  - new: new, NEW, add, adds, added, introduces, creates
  - changed: modified, Mod, changed, changes, gains, extends, amends, replaces, updates, add/adds/added (adding something to an existing element changes it), fixes, alters, rewrites, refactors, renames, moves
  - removed: removed, deleted, retired, dropped, decommissioned (parts of the system, not withdrawn options)
  - existing: unchanged, untouched, as today, existing, pre-existing, today's
  - a word negated by no, not, without or never just before it does not count ("No new functions")
  - the word and the element's name (or alias, or doc id) must be in the same sentence, semicolon clause or table cell, and the word must describe that element, not a neighbour in the sentence; the gate can check the first, only you can check the second
  - none of these about the element: `unstated`, which draws no tag. Do this even when the whole design is obviously new: the page header counts how many elements the doc left unmarked, so the reader sees it.
- `parent`: from the item's stated home ("inside the notification worker", "(`quickdocker.sh`, bash)" names the host script).

## E4 Relationships

1. Transcribe every `arrow_lines` entry (all arrow-bearing lines of the body) into `arrow_ledger`; each relationship, flow or exclusion you list there must cite that line. Expand fan-ins (`K4/K5 ──▶ K7`), ranges (`K3..K8`), sets (`K3→{K1,K4}`) and chains (`A → B → C` is two relationships).
2. Classify each arrow:
   - runtime call or data flow between running parts → relationship;
   - a numbered procedure order ("bootstrap → enroll → sign") → a flow ([E5](#e5-flows)), not a static relationship;
   - build order, phase dependency, code branch tree, file containment → exclusion `build-order`, `control-flow` or `file-relation`;
   - an arrow used as notation for a mapping or result (`ParseContainerID(id) → trust domain`, `args[0]` → value) → exclusion `not-a-relationship`.
   Calls between components inside one running application are relationships too.
   A dependency graph may point from a dependency to the part that uses it; the relationship direction is who initiates at run time, decided from what the doc says elsewhere ("X calls Y", "Y is used by X"). When nothing in the doc indicates it, keep the arrow's direction and add an `open` item citing the line.
   An arrow labelled with a file or store (`──(~/.af-identity: …)──→`) means one part writes the store and another reads it later: model the store as a `data_store` and draw two relationships, writer → store and reader → store.
   A line that is both a numbered procedure step and an arrow (`runMailSend() → args[0] = …`) is a flow step, not `not-a-relationship`.
3. Also transcribe relationships stated only in prose (a sentence saying X sends Y to Z).
4. `verb` reads initiator → responder ("publishes a shipment event to", "reads registry entries from"). Arrow labels become `technology` or `data` (`──(~/.af-identity: …)──→` is data).
5. `interaction`: `async` only when the doc says message, event, queue, publish, subscribe or fire-and-forget; otherwise `sync`.

## E5 Flows

An ordered sequence the doc states for the proposed design (numbered steps, "first … then …", a step-order arrow chain) becomes a flow with `state: "proposed"`. A sequence describing today's behaviour (a bug's current path, "today the system …") becomes a flow with `state: "current"`; it is drawn titled "current behaviour, as the design doc describes it", after proposed flows. Step `message` is a verbatim part of the step's quote (≤120 characters; take the clause that carries the action and its condition). Work inside one element is a step with `from` = `to`. A step that happens only under a condition (a refusal, an error path, "otherwise …") carries `when` with the verbatim condition. A step whose actor has no stated home maps to the container it runs in; mixing a container and its components in one sequence is acceptable when that is all the doc states. Each step's actor must map to an element; an unmappable step's flow gets an exclusion `ambiguous-target`.

## E6 Deployment

Deployment nodes are hosts, machines, VMs, Docker containers, clusters, cloud accounts, phones or browsers the doc names as places where elements run. Lines such as `Host:` / `Container:` and sentences like "X runs on Y" are the evidence. Record a placement only where the doc says the element runs there. One deployment per environment the doc names; `environment: null` when it names none.

## E7 Security

- `sensitive: true` only for a relationship that carries a credential, key, token, secret, personal data or privilege the doc names; `data` is that item in the doc's words.
- A trust boundary exists only where the doc says something must not cross between two named places ("The CA private key never enters any container", "the PAT stays on the host"). `rule` is that sentence part, verbatim; the security view's key prints it. `members` are the elements on the protected side; a member's components are inside with it. Never promote a heading prefix such as `Host:` into a trust boundary without such a sentence.

## E8 Open items and acronyms

- Every `open` item is listed on the page whether or not it names elements; link elements when the doc ties the question to them.
- `open`: only what the doc leaves unresolved — Open Questions, undecided contradictions, claims it marks unverified or TBD that decide a relationship. Limits and caveats the doc accepts or decides ("by design", "accepted", "stated limit") are not open; they stay in the doc.
- Anything you model that no produced view shows (an external tool connected only to another external, a relationship between two outside elements) needs an exclusion `not-in-any-view` with its `ids`; `check_model.mjs` lists them.
- `glossary`: every acronym that will appear in a name, summary, technology, data item, boundary name or flow step message and is not on the allowlist. Cite the doc's expansion when it gives one, else `"standard"`. Capitalised English words (AND, NOT, MUST, ONLY, NEVER) are not acronyms. Glossary `term` is the exact token, digits included (`WSL2`). A literal all-capitals name (a file such as `REVOKED`) gets `expansion: null` with a citation. Doc ids such as `K19` never belong in a label or message; choose a quote without them.

## Worked example

Design doc lines:
```
11 | K1 | Notification worker (NEW, Go) | Consumes shipment events and sends email through the mail provider. |
22 K1 Notification worker ─→ Mail provider (SMTP)
34 The mail provider API key never leaves the notification worker's host. The customer's email address is sent to the mail provider over TLS.
```
Model fragment:
```json
{"id": "worker", "kind": "container", "name": "Notification worker", "parent": "shop",
 "summary": "Consumes shipment events and sends email through the mail…",
 "technology": "Go", "technology_src": [{"lines": [11, 11], "quote": "Notification worker (NEW, Go)"}],
 "delta": "new", "delta_src": [{"lines": [11, 11], "quote": "Notification worker (NEW, Go)"}],
 "doc_ids": ["K1"], "src": [{"lines": [11, 11], "quote": "| K1 | Notification worker (NEW, Go) | Consumes shipment events and sends email through the mail provider. |"}]}

{"id": "r_mail", "from": "worker", "to": "mail_provider", "verb": "sends the shipping email through",
 "technology": "SMTP", "technology_src": [{"lines": [22, 22], "quote": "Mail provider (SMTP)"}],
 "interaction": "sync", "data": "email address", "sensitive": true, "delta": "unstated",
 "src": [{"lines": [34, 34], "quote": "The customer's email address is sent to the mail provider over TLS."}]}

{"id": "worker_host_trust", "kind": "trust", "name": "notification worker's host", "parent": null, "technology": null,
 "members": ["worker"], "rule": "The mail provider API key never leaves the notification worker's host",
 "src": [{"lines": [34, 34], "quote": "The mail provider API key never leaves the notification worker's host."}]}
```
Arrow ledger: `{"line": 22, "relationship_ids": ["r_mail"]}`. Inventory: `{"line": 11, "element_ids": ["worker"]}`.
