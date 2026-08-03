---
name: github-issue
description: Creates well-documented GitHub issues (or comments on existing ones) from problems discussed in conversation. Investigates the codebase to map affected layers, files, and data flow, then writes up findings with acceptance criteria — without prescribing fixes. Use when a conversation has identified a problem that needs a GitHub issue, when the user wants to document a bug or problem for an implementer, or when dispatching work. Triggers on "create a GitHub issue", "write up an issue for this", "file a bug", or "document this problem".
---

# GitHub Issue Investigation & Write-Up

Produces a write-up that gives an implementer everything they need to find and understand a problem — without telling them how to fix it.

## Your Role: Cartographer, Not Surgeon

You are not fixing this problem. You are drawing the map for someone who will.

The implementer who picks this up is skilled — they will read the codebase, trace the
data flow, form their own theory, and write the fix. What they need from you is the
terrain: which files are involved, what the data flow looks like, what's surprising
about the system, and what "done" looks like. What they don't need — and what actively
hurts them — is you pre-chewing the solution. When you prescribe specific changes, you
close off their discovery, narrow their scope, and cause them to miss things you missed.

**Cartographer of the fix — not of the problem's altitude.** The "not a surgeon" rule
governs the FIX: don't prescribe how to solve it. It does NOT license accepting the
reported problem's *altitude* uncritically. The report — and any pre-stated acceptance
criteria — is a hypothesis about where the defect lives, not scripture to be mapped. Your
job includes testing whether the reported problem IS the defect or merely a leaf of a
larger one, and stating the defect at the altitude that serves the goal. A precise,
well-anchored map of the wrong territory is the worst thing this skill can produce —
worse than a rough map of the right territory, because its precision manufactures
confidence that the wrong problem is the real one. Phase 3 forces this check; do not skip it.

**Confirmation volume is not correctness.** Anchor counts, prior approvals, downstream
cross-reviews, and "looks right" verdicts measure internal consistency with the frame you
were handed — not whether that frame is right. When everyone downstream inherits your
framing, their agreement is one frame counted many times, not many independent
confirmations. Judge the issue against the goal, never against how well-supported the
reported symptom is.

**Write every sentence as if you're briefing a colleague who will go deeper than you
did.** Your job is to save them the first hour of orientation, not to do their job for
them.

**Be honest about what you find.** If a feature was half-implemented, say "this was
never wired up." If there are no tests covering the flow, say "there are no tests
for this." If the frontend and backend API contracts don't match, say "these were
built without verifying the contract." Don't soften findings into neutral descriptions
of state — name what's broken, what's missing, and what was never done. The CEO and
the implementer both need the unvarnished picture to make good decisions.

In practice:
- **DO**: List every file and layer that touches the affected data flow
- **DO**: Note contextual gotchas an implementer might miss (env differences, data
  format mismatches, orphan fields, display vs stored values)
- **DO**: Write acceptance criteria that define "done" without defining "how"
- **DON'T**: Say "add this line to this file" or "change X to Y"
- **DON'T**: Name a specific function, flag, command substitution, or approach as
  "the correct" / "the equivalent" / "the substitute" — naming the mechanism is
  prescription even when it's offered as an example
- **DON'T**: State scope constraints ("no changes to X", "read-only on Y",
  "cleanup only") unless the USER said them — editorial scope decisions
  masquerading as user requirements bias every downstream reader
- **DON'T**: Assume which environments, scripts, or deploy steps are in scope — if
  multiple exist, list them all and let the implementer determine what's needed
- **DON'T**: Minimize the scope ("it's just one line") — that causes blind spots

**Enumeration is context, not a task list.** When listing files or occurrence sites,
say what the implementer needs to understand, not what they need to transform. If
every item in a list must be processed identically to satisfy an acceptance
criterion, you are prescribing a mechanical transform — which is prescription.
A 128-row table of occurrence sites reads to every downstream agent as "128 things
to fix," regardless of what the surrounding prose says.

## Process

Copy this checklist and track progress:

```
Investigation Progress:
- [ ] Phase 1: Understand the problem as reported
- [ ] Phase 2: Investigate the codebase
- [ ] Phase 3: Reconcile findings with symptoms AND interrogate the frame (altitude gate)
- [ ] Phase 4: Draft the GitHub issue
- [ ] Phase 5: Validate the draft (checklist items 1-4 "none", 5 covered, 6 passes)
- [ ] Phase 6: Post via gh api
- [ ] Phase 7: Create bead & dispatch (if requested)
```

### Phase 1: Understand the Problem as Reported

1. If a GitHub issue URL is provided, fetch it via `gh api` to get the full body and
   any comments
2. Read the problem description carefully — note the user's exact symptoms, what they
   expected, and what actually happened

Treat the report as the *entry point*, not the *boundary*, of the problem — Phase 3 tests
whether it is the right altitude.

**Examine all attached artifacts.** Screenshots, pasted logs, email contents, and
configuration screenshots often carry more signal than the codebase itself. These are
first-class evidence, not supplementary detail.

- Download images with authenticated requests: `curl -sL -H "Authorization: token $(gh auth token)" -o file.png "<url>"`
- Use the Read tool to visually examine downloaded images
- Note every concrete detail visible in artifacts (field values, status indicators,
  error codes, configuration states)

### Phase 2: Investigate the Codebase

Use an **Explore agent** (subagent_type=Explore) to map the technical landscape. The
prompt should ask the agent to trace the full data/control flow related to the problem.
Be thorough — request that the agent search across all relevant directories (src, test,
terraform, deploy scripts, config, etc.).

The investigation should produce:
- **Affected layers**: Frontend, backend, infrastructure, tests, docs — every layer
  the problem touches or that an implementer would need to understand
- **Affected files**: Absolute paths with brief descriptions of each file's role
- **Data flow**: How data moves through the system (form → API → handler → output)
- **The break point**: Where exactly the expected behavior diverges from actual behavior
- **The actual mechanism**: for an incident, what *actually* caused it — which is often
  not the surface the report named. Trace it to the mechanism, not the first plausible match.
- **Contextual gotchas**: Things that aren't broken but are relevant — orphan fields,
  naming mismatches, environment differences (staging vs prod), format differences
  (stored codes vs display labels), etc.

### Phase 3: Reconcile Findings with Symptoms, and Interrogate the Frame

**This phase is critical.** Before writing anything up, do BOTH: (a) verify your codebase
findings explain the reported experience, and (b) test whether the reported problem is
even the right problem. Skipping (b) is how a precise map of the wrong territory ships.

**(a) Symptom reconciliation.** Enumerate every symptom the user reported and the specific
finding that explains it:

| Reported symptom (quote) | Finding that explains it (file:line or "gap") |
|--------------------------|-----------------------------------------------|
| ...                      | ...                                           |

Any row with "gap" means the investigation is incomplete. Do not proceed to write-up.
Common gap sources: factors outside the codebase (DNS, CDN, third-party services,
environment variables, external dashboards) that Explore agents can't see. If a row
has a gap, ask the user — they often have access to information invisible to the
codebase (admin dashboards, service configurations, deployment history). A single
piece of user context can redirect the entire investigation — don't skip this step
to save time.

**If my findings say "X shouldn't happen" but the user says it does — the user is
right and my investigation is incomplete.**

**Adjacent findings — and deeper roots — expand the deliverable by default.** If the
investigation surfaces anything beyond the reported problem — a sibling defect, a deeper
root, or the actual mechanism behind the incident — the default is to widen the issue to
cover it. Deferring it to a "follow-up" so the primary issue stays narrow and closes green
is the failure this skill exists to prevent: documenting a hole is not filling it, and a
map that notes the real defect's coordinates while aiming the implementer elsewhere is
worse than one that simply missed it. Scoping anything out — sibling OR deeper root —
requires asking the operator first, with the stakes named: *"Investigation found X (the
actual mechanism of the incident / a systemic exposure). Widening the issue covers it;
deferring leaves [stakes]. Defer, or widen?"* Do not unilaterally split, defer, or narrow.
"Filed as a follow-up" without a quoted operator go-ahead is not allowed.

**(b) Interrogate the frame — the altitude gate.** The card is a hypothesis, not
scripture. Test whether the reported problem is the defect or a leaf of a larger one.
Fill this table:

| Reported problem (the "card") | The goal / incident it exists to serve | If someone fixed EXACTLY the reported problem and nothing else, is the goal fully served? (yes/no + why) | The defect restated at the altitude that WOULD serve the goal |
|---|---|---|---|

Column 3 is load-bearing. If it is "no," the reported problem sits *below* the defect:
the issue's problem statement and acceptance criteria MUST be written at column 4's
altitude, not column 1's.

Then apply the elevation test: could a change to a boundary or abstraction *delete* the
concern's substrate, rather than guard one path to it? If so, that boundary IS the frame
— name it, and pitch the issue so the implementer must DECIDE it deliberately (surface the
decision; do not prescribe its outcome — that stays cartographer, not surgeon). For a
systemic or architectural defect, invoke `/architecture-elevation` and carry its verdict
into the problem statement rather than guessing the altitude.

**FRAME GATE — quote your answers; a yes/no is not passing:**
- [ ] Column 3 is answered for every reported symptom, with the "fix exactly this → goal
  served?" reasoning shown.
- [ ] If any column-3 answer is "no," quote the problem-statement sentence that reframes
  the issue at column 4's altitude.
- [ ] Every mechanism, root, or surface the investigation surfaced beyond the report is
  present in the issue's problem statement AND acceptance criteria — OR the operator
  explicitly approved deferring it (quote their words). No quote → widen; do not defer.

### Phase 4: Draft the GitHub Issue

Draft the body locally. Do not post yet — Phase 5 validates the draft and Phase 6
posts it.

**If a GitHub issue already exists:** The draft will be posted as a comment in Phase 6.
**If no GitHub issue exists:** The draft will become the body of a new issue in Phase 6.

Structure:

```markdown
## Problem

[1-2 sentences stating the defect at the altitude that serves the goal (Phase 3 column 4),
not merely the reported symptom. Where the break occurs and — for an incident — its actual
mechanism.]

### Affected Layers & Files

**[Layer Name]**
- `path/to/file` — What this file does in context of the problem

[Repeat for each layer: Frontend, Backend, Infrastructure, Tests, Docs, etc.]

### Additional Context

- [Gotcha 1: something non-obvious the implementer should know]
- [Gotcha 2: environment differences, data format issues, etc.]
- [Gotcha 3: related but not broken things worth being aware of]
- [Any frame/elevation decision the design must make deliberately — surfaced, not decided]

### Acceptance Criteria

- [Observable behavior that defines "done" — at the goal's altitude, covering the real
  mechanism, not only the reported surface]
- [Edge cases or existing behavior that must be preserved]
- [Test coverage expectations]
```

**Writing guidelines:**
- Use a heredoc for the body to preserve formatting
- Keep it factual and terse — no filler, no opinions on difficulty
- Every file mentioned should be a real path confirmed by the investigation
- Acceptance criteria should be testable statements, not implementation steps

### Phase 5: Validate the Draft Before Posting

**Do not run `gh api` until this checklist is complete with quoted evidence from the
draft.** Answer each question by quoting the relevant passage from the draft, or
writing "none" only after reading every section of the body.

1. **Scope constraints** — Does the body constrain the scope of the fix (e.g., "no
   changes to X", "read-only on Y", "cleanup only", "no new features")? Quote every
   such constraint. For each: did the USER state this, or did I add it? If I added
   it, remove it — editorial scope decisions masquerading as user requirements bias
   every downstream reader.

2. **Prescribed mechanisms** — Does the body name a specific function, flag, file
   path, command substitution, variable, or implementation approach as "the correct"
   / "the right" / "the equivalent" / "the substitute" / the suggested replacement?
   Quote each. Remove every one — naming the mechanism is prescription, including
   when offered as an example.

3. **Mechanical transforms** — Does any list or table in the body imply the
   implementer must transform each enumerated item identically to satisfy an
   acceptance criterion (e.g., "N sites to rewrite", "zero matches after cleanup",
   a line-number table of occurrences)? Quote each. Rewrite as behavioral AC or
   keep the enumeration as context only (without a corresponding AC that forces
   transforming every row).

4. **Regex-compliance AC** — Does any AC item pass if `grep` returns specific match
   counts (zero matches, N matches, exact string present/absent)? Quote each.
   Rewrite as observable behavioral outcome. AC tests what the system does, not
   what the file contents look like.

5. **Symptom coverage** — Does every user-reported symptom from Phase 3's table
   appear in the body, tied to the finding that explains it? If a symptom is not
   covered, add it or re-open Phase 2.

6. **Frame coverage** — Does the problem statement describe the defect at the altitude
   of the goal, not merely the reported symptom? Quote the sentence that establishes the
   altitude. Would fixing exactly what this issue asks fully serve the goal (Phase 3,
   column 3)? If the issue addresses only the reported symptom while the investigation
   found a deeper root or the incident's real mechanism, that is a FAIL — widen the issue,
   or quote the operator's approval to defer. This is the check that catches shipping
   "an agent can't pass one flag" for "an agent took down the whole factory."

Any "yes" on 1-4 requires removing the offending content; a FAIL on 6 requires widening
the frame (or a quoted operator deferral). Re-read the revised draft and re-run the
checklist until 1-4 return "none", 5 is covered, and 6 passes.

### Phase 6: Post

- **Existing issue comment**: `gh api repos/{owner}/{repo}/issues/{number}/comments -X POST -f body="..."`
- **New issue**: `gh api repos/{owner}/{repo}/issues -X POST -f title="..." -f body="..."`
- **Replace an existing issue's body** (when the operator asks to rewrite the body rather
  than comment): `gh api repos/{owner}/{repo}/issues/{number} -X PATCH -F body=@<file>`

### Phase 7: Create Bead & Dispatch (if requested)

If the user wants to dispatch the work:

1. Create a bead with `af bead create`:
   - `--type bug` for defects, `--type feature` for new behavior
   - `--title "..."` summarizing the problem
   - Description should reference the GitHub issue URL for full context
2. Dispatch with `af sling`:
   - `af sling --agent <specialist> "task description"` for specialist agents
     (e.g., `design-v5`, `ultra-implement`, `rootcause-all`)
   - `af sling --formula <name>` to instantiate a formula

## Anti-Patterns to Avoid

Prescription-shaped failures (smuggling mechanisms, constraints, transforms, or
regex-compliance AC into the body) are caught by the Phase 5 validation checklist.
Frame-shaped failures (mapping the wrong altitude, deferring the real root) are caught
by Phase 3's frame gate and Phase 5 item 6. The anti-patterns below name the ones that
happen before the draft exists.

| Anti-Pattern | Why It's Bad | Instead |
|-------------|-------------|---------|
| Mapping the reported problem faithfully without testing its altitude | Ships a precise, well-anchored fix for the wrong (too-narrow) problem; the real defect survives | Run Phase 3's frame gate: if fixing exactly the report wouldn't serve the goal, reframe at the goal's altitude |
| Deferring a deeper root the investigation found to a "follow-up" | Closes a narrow issue green while the real defect is documented-but-unfixed — documenting a hole is not filling it | Widen by default; defer only with the operator's quoted go-ahead and the stakes named |
| Treating anchor counts / prior approvals as proof the frame is right | Confirmation that all inherits one frame is that frame counted N times, not N independent checks | Judge correctness against the goal, not against internal consistency |
| "This is a one-line fix" | Minimizes scope, misses environments/tests | List all affected layers |
| Naming specific deploy scripts | Assumes deploy path, misses staging/prod parity | Note that multiple environments exist |
| Skipping test files | Implementer might skip test updates | Always list test files in affected layers |
| Skipping artifact examination | Screenshots and pastes often contain the diagnosis | Examine every attached image, log, and config screenshot before exploring the codebase |
