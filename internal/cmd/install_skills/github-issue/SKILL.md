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
- [ ] Phase 3: Reconcile findings with reported symptoms
- [ ] Phase 4: Draft the GitHub issue
- [ ] Phase 5: Validate the draft (all four checklist items return "none")
- [ ] Phase 6: Post via gh api
- [ ] Phase 7: Create bead & dispatch (if requested)
```

### Phase 1: Understand the Problem as Reported

1. If a GitHub issue URL is provided, fetch it via `gh api` to get the full body and
   any comments
2. Read the problem description carefully — note the user's exact symptoms, what they
   expected, and what actually happened

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
- **Contextual gotchas**: Things that aren't broken but are relevant — orphan fields,
  naming mismatches, environment differences (staging vs prod), format differences
  (stored codes vs display labels), etc.

### Phase 3: Reconcile Findings with Reported Symptoms

**This phase is critical.** Before writing anything up, verify that your codebase
findings actually explain the user's reported experience.

Enumerate every symptom the user reported and the specific finding that explains it:

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

**Adjacent findings expand the deliverable by default.** If the investigation surfaces
problems beyond the reported one — especially systemic issues where a local fix would
leave the system exposed — the default is to include them in this write-up. Scoping a
finding out requires asking the user first, with specific framing: *"Investigation
found X, Y, Z. I'm planning to scope Z out because [reason] — is that right?"* Do
not unilaterally split a finding into a separate issue to narrow the deliverable.

### Phase 4: Draft the GitHub Issue

Draft the body locally. Do not post yet — Phase 5 validates the draft and Phase 6
posts it.

**If a GitHub issue already exists:** The draft will be posted as a comment in Phase 6.
**If no GitHub issue exists:** The draft will become the body of a new issue in Phase 6.

Structure:

```markdown
## Investigation Notes

[1-2 sentence summary of what's happening and where the break occurs]

### Affected Layers & Files

**[Layer Name]**
- `path/to/file` — What this file does in context of the problem

[Repeat for each layer: Frontend, Backend, Infrastructure, Tests, Docs, etc.]

### Additional Context

- [Gotcha 1: something non-obvious the implementer should know]
- [Gotcha 2: environment differences, data format issues, etc.]
- [Gotcha 3: related but not broken things worth being aware of]

### Acceptance Criteria

- [Observable behavior that defines "done"]
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

Any "yes" on 1-4 requires removing the offending content. Re-read the revised draft
and re-run the checklist until all four return "none."

### Phase 6: Post

- **Existing issue comment**: `gh api repos/{owner}/{repo}/issues/{number}/comments -X POST -f body="..."`
- **New issue**: `gh api repos/{owner}/{repo}/issues -X POST -f title="..." -f body="..."`

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
The anti-patterns below are the ones that checklist doesn't catch — structural
investigation failures that happen before the draft exists.

| Anti-Pattern | Why It's Bad | Instead |
|-------------|-------------|---------|
| "This is a one-line fix" | Minimizes scope, misses environments/tests | List all affected layers |
| Naming specific deploy scripts | Assumes deploy path, misses staging/prod parity | Note that multiple environments exist |
| Skipping test files | Implementer might skip test updates | Always list test files in affected layers |
| Skipping artifact examination | Screenshots and pastes often contain the diagnosis | Examine every attached image, log, and config screenshot before exploring the codebase |
