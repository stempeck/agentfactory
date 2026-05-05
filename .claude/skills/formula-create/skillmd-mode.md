# SKILL.md Input Mode — Formula Create

When invoked with a path to a SKILL.md file, convert the skill's phase/gate
structure into a formula that preserves enforcement rigor.

## 10.1 Read and Parse the SKILL.md

1. Read the file completely
2. Parse YAML frontmatter (between `---` markers) — extract `name`, `description`
3. Identify these sections by markdown headers:
   - `## Persona` — persona/identity for the formula description
   - `## Inputs` / `## Required Proposal Contents` — map to `[vars]`
   - `## Outputs` — artifact list for final gate verification
   - `## Phase Gate Enforcement` — table of gates and what they verify
   - `### Phase N:` — work phases (become work steps)
   - `### GATE N:` — verification gates (become gate steps)
   - `## Anti-Patterns` — inject into formula description and relevant steps
   - `## Success Criteria` — inject into final step exit criteria

## 10.2 Build the Step List

**Formula name:** `<skill-name>` (from YAML frontmatter `name` field)

Every generated formula has three zones: pre-work invariant steps, domain steps from the
skill, and post-work invariant steps. The invariant steps come from `factoryworker` and
are the same for every agent work execution formula regardless of the skill being converted.

**Pre-work invariant steps** (always present, in this order):

| # | Step ID | Purpose |
|---|---------|---------|
| 1 | `load-context` | Prime environment, read hook, extract requirements |
| 2 | `branch-setup` | Clean feature branch, rebase on main |
| 3 | `validate-contract` | Gate: incoming design contract inspection (Poka-yoke) |
| 4 | `preflight-tests` | Verify tests pass on main before starting |

**Domain steps** (derived from the SKILL.md's phases and gates):

| # | Step ID pattern | Source |
|---|-----------------|--------|
| 5..N | `phase-N-<kebab>`, `gate-N-<kebab>` | SKILL.md phases and gates, interleaved |

**Post-work invariant steps** (always present, in this order):

| # | Step ID | Purpose |
|---|---------|---------|
| N+1 | `self-review` | Review own diff before testing |
| N+2 | `run-tests` | Full test suite, terraform validation if applicable |
| N+3 | `self-verify` | Gate: verify implementation matches design contract (Jidoka) |
| N+4 | `cleanup-workspace` | Skill artifact removal, workspace hygiene, push branch |
| N+5 | `prepare-for-review` | `bd update` with completion notes |
| N+6 | `submit-and-exit` | `af done`, become recyclable |

**The `needs` chain must be unbroken.** The first domain step needs `["preflight-tests"]`.
The `self-review` step needs `["<last-domain-step-or-gate>"]`. Every step between has a
`needs` pointing to the previous step.

**The `load-context` step MUST include requirement source detection.** Polecats receive
requirements through their assigned bead, which may provide them in different forms.
Include this three-branch pattern in every `load-context` step:

```
**Extract requirements from the bead:**
The bead is your source of truth. It may provide requirements in one of three forms:

- **Inline requirements**: The bead description itself contains the spec. Extract requirements directly.
- **Proposal document path**: The bead references a file (e.g., `docs/feature-proposal.md`). Read it completely.
- **GitHub issue link**: The bead contains a URL (e.g., `https://github.com/org/repo/issues/123`).
  Fetch the issue via `gh issue view <number> --repo <org/repo>` and extract requirements from it.

Whichever form, capture the full requirements — this is your spec.
```

**For each Phase in the SKILL.md, create a domain work step:**
- `id`: `phase-N-<kebab-case-title>` (or a descriptive short name)
- `title`: `"Phase N: <title>"`
- `needs`: `["<previous-gate-or-step-id>"]`
- `description`: Phase content with enforcement language preserved verbatim.

**For each GATE in the SKILL.md, create a domain gate step:**
- `id`: `gate-N-<kebab-description>`
- `title`: `"GATE N: <description>"`
- `needs`: `["<phase-step-this-gate-follows>"]`
- `description`: Use the skill-derived gate step template (Section 10.3)

**Full step sequence** showing all three zones:
```
load-context → branch-setup → validate-contract → preflight-tests
  → phase-1 → gate-0 → phase-2 → phase-3 → gate-1 → ...
  → self-review → run-tests → self-verify
  → cleanup-workspace → prepare-for-review → submit-and-exit
```

## 10.3 Gate Step Template

There are two types of gates in a generated formula:

1. **Architecture gates** (from factoryworker): `validate-contract` and `self-verify`.
   These are copied verbatim from factoryworker and are NOT derived from the skill's
   GATE sections. See Section 10.9 for how to source their content.

2. **Skill-derived gates** (from the SKILL.md's Phase Gate Enforcement table).
   These use the template below.

Do NOT omit architecture gates just because the skill doesn't mention design contracts.
Both architecture gates include graceful degradation (skip if no contract exists).

**Skill-derived gate template.** Extract the bash command from the skill's fenced code
block within the GATE section.

```
**MANDATORY GATE CHECK** — Do NOT close this step until the check passes.

Run this command:
\`\`\`bash
<bash command extracted from SKILL.md gate section>
\`\`\`

**If FAIL**: <failure action from SKILL.md, or "Go back and fix. Do NOT proceed.">
**If PASS**: Close this step and continue.
```

## 10.4 Consolidation Rules

- **Short phases** (< 3 lines of instructions): Merge with the adjacent phase
- **Related TDD phases** (e.g., "TDD Loop" + "Implement Each Feature"): Merge into one step
- **Conditional gates** ("if applicable"): Include escape clause in description:
  `"If not applicable, close this step immediately with reason 'N/A'."`
- **Enforcement language**: Lines containing STOP, MUST, MANDATORY, "Do NOT" —
  preserve these VERBATIM in the step description. Do not paraphrase or soften.

## 10.5 Work Step Content Strategy

Step descriptions are **self-contained**:
- Embed the relevant phase content directly (the polecat can execute without reading external files)
- Preserve all bash code blocks from the skill phase
- Preserve all enforcement language
- Include relevant anti-patterns from the skill's Anti-Patterns section
- Use source-agnostic language for requirements. Skills often say "proposal" or "spec document",
  but polecats receive work through beads. Replace "read the proposal" with "read the requirements
  (from bead description, proposal file, or GitHub issue)". Never assume a specific file exists —
  the bead is always the starting point.

## 10.6 Variable Inference

- Always add `[vars.issue]` with `source = "hook_bead"` (polecats always have an assigned issue)
- If the skill references `BUILD_CMD`, `TEST_CMD`, `TEST_PATTERN_CMD`: these are
  discovered per-project during the pre-implementation phase, NOT formula variables.
  Document them in the phase description, not as `[vars]`.
- If the skill has explicit input parameters, add them as `[vars]` with appropriate sources

## 10.7 Formula Description

Build the top-level `description` from:
1. **Mandatory execution directive** (always first, verbatim):
   ```
   ## MANDATORY: Exact Step Execution
   Execute each formula step EXACTLY as written, in order, with no modifications.
   Do NOT skip steps, combine steps, or "optimize" the process. Each step exists
   because it is part of a known working process. For example, if a step says to
   restart a polecat, restart it — do not reason that "keeping existing context
   would be better." Your job is faithful execution of these steps, not improvement
   of them.
   ```
2. Skill's `description` from frontmatter (first paragraph) — rewrite to be source-agnostic
   (e.g., "from a proposal document" → "requirements come from the assigned bead — which may
   contain inline requirements, a path to a proposal document, or a link to a GitHub issue")
3. Skill's `## Persona` section (if present)
4. Variables table
5. Failure modes table (standard: tests fail → fix; stuck → mail Witness; context filling → af handoff)
6. Anti-patterns section from the skill — rewrite "proposal" references as "requirements"

## 10.8 Reference Formulas

Before generating, examine BOTH reference formulas:

**1. factoryworker.formula.toml** — the invariant step source:
```bash
cat internal/cmd/install_formulas/factoryworker.formula.toml
```

This defines all 10 invariant steps. Copy step descriptions for:
`load-context` (adapted), `branch-setup`, `validate-contract`, `preflight-tests`,
`self-review`, `run-tests`, `self-verify`, `cleanup-workspace`, `prepare-for-review`,
`submit-and-exit`.

**2. design-v3.formula.toml** — a complete skill-derived formula:
```bash
cat internal/cmd/install_formulas/design-v3.formula.toml
```

Shows the full 18-step structure with all 10 invariant steps, domain phase interleaving,
`af handoff` between major phases, and failure mode tables.

Key patterns to follow:
- All 10 invariant steps present (not a subset)
- `af handoff` instructions between major domain phases (for context management)
- Failure mode table in the formula description
- `[vars.issue]` with `source = "hook_bead"`
- Graceful degradation in architecture gates (skip if no contract exists)
- Cleanup separated from submit-and-exit

## 10.9 Invariant Step Content

The 10 invariant steps (`load-context`, `branch-setup`, `validate-contract`, `preflight-tests`,
`self-review`, `run-tests`, `self-verify`, `cleanup-workspace`, `prepare-for-review`,
`submit-and-exit`) must have their descriptions populated from `factoryworker.formula.toml`.
`load-context` is adapted per Section 10.2; the remaining 9 are copied verbatim except for
the two skill-specific adaptations below.

**Process:**

1. Read `factoryworker.formula.toml` (already done in Section 10.8)
2. For each invariant step, copy its `description` field verbatim into the generated formula
3. Apply the skill-specific adaptations below (only these four are allowed)

**Adaptation 1 — `run-tests`:** If the skill defines a specific TEST_CMD (e.g.,
`go test ./...`, `pytest`), replace the generic test commands in the copied description
with the skill's commands. If the skill doesn't define one, keep factoryworker's
generic commands unchanged.

**Adaptation 2 — `cleanup-workspace`:** Prepend skill artifact removal before the standard
workspace cleanup content. Each skill creates different working artifacts that should NOT
be merged to main. Add `rm -f` commands at the beginning of the description for these files.

Example for a rapid-implement derived formula:
```
**Skill artifact cleanup:**
The rapid-implement process creates working files that should NOT be merged to main.
```bash
rm -f test_results.txt
```
```

Derive the artifact list from the skill's `## Outputs` section. Only remove files that
the skill creates as working artifacts — not production code or test files.

**Adaptation 3 — `prepare-for-review`:** Replace the generic `bd update` notes with a
skill-specific completion summary describing the artifacts produced. For example, a
design formula should mention the design-doc.md and dimension analyses; a TDD formula
should mention the fix and test files.

**Adaptation 4 — `preflight-tests` and handoff messages:** Replace generic "implement step"
references with the actual first domain step ID (e.g., "phase-1-problem-analysis step").
This applies to the handoff message and exit criteria text, not the test commands.

**`submit-and-exit`:** Copy from factoryworker. Since cleanup now happens in
`cleanup-workspace`, the submit-and-exit step should focus on `af done` and verification,
not workspace cleanup. Do NOT bundle artifact removal or git push into submit-and-exit.

## 10.10 Post-Creation Verification

**MANDATORY:** Run this checklist before reporting success. Do NOT skip any check.

1. **File exists:**
   ```bash
   ls -la internal/cmd/install_formulas/<formula-name>.formula.toml
   ```

2. **Step count:** Count `[[steps]]` entries. Must equal: pre-work (4) + domain (N) + post-work (6).

3. **Invariant steps present:** Verify all 10 step IDs exist in the formula:
   `load-context`, `branch-setup`, `validate-contract`, `preflight-tests`,
   `self-review`, `run-tests`, `self-verify`, `cleanup-workspace`,
   `prepare-for-review`, `submit-and-exit`.

4. **Needs chain unbroken:** Every step except `load-context` has a `needs` field.

5. **Mandatory directive:** The `description` field starts with `## MANDATORY: Exact Step Execution`.

6. **No hardcoded test commands:** Invariant steps must NOT hardcode stack-specific test
   commands (`go test ./...`, `npm test`, `pytest`, etc.). Instead, instruct the agent to
   discover the project's test command from its build configuration (CLAUDE.md, Makefile,
   package.json, etc.) and run it.

If ANY check fails, fix the formula and re-run the checklist before reporting success.

## 11. Post-Creation for Skill-Derived Formulas

After creating a skill-derived formula, inform the user:

```
Formula created: internal/cmd/install_formulas/<name>.formula.toml
Derived from: .claude/skills/<name>/SKILL.md

Steps: N domain steps + M skill gates + 10 invariant steps
Invariant: load-context, branch-setup, validate-contract, preflight-tests,
           self-review, run-tests, self-verify, cleanup-workspace,
           prepare-for-review, submit-and-exit
Skill gates preserved: GATE 0 through GATE N (as separate formula steps)

To use immediately:
  af sling --formula <name> --agent <agent>

To embed in agentfactory binary:
  make build && make install    # Rebuilds binary with new formula embedded
```
