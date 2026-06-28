---
name: agentic-skill-eval
description: Evaluates a skill library to identify which SKILL.md files would produce valuable autonomous agents when converted via /formula-create. Use when assessing a repository of skills for agent creation candidates, prioritizing which skills to convert to formulas, or auditing an existing agent roster for gaps.
---

# Evaluating Agentic Potential

Systematically evaluates every skill in a repository to determine which would produce valuable autonomous agents via the `/formula-create` → `af formula agent-gen` pipeline.

## Prerequisites

- Target repository contains SKILL.md files (any structure)
- AgentFactory installed (`af` CLI available)
- Access to the factory's .agentfactory/AGENTS.md (current agent roster)

## Workflow

### Phase 1: Understand the Factory (do NOT skip)

Read the factory's .agentfactory/AGENTS.md to understand:
- What agents already exist and their types (autonomous vs interactive)
- What capabilities the factory already has (design, analysis, implementation, operations)
- What the dispatch pattern looks like (`af sling --agent <name> "input"`)

```bash
cat "$(af root)/.agentfactory/AGENTS.md" 2>/dev/null || echo "No AGENTS.md found"
```

**Output (required):** A numbered gap list. Enumerate every capability gap the factory has. These numbers are referenced in Phase 4 — skills that cannot cite a gap number are discarded.

Example:
```
1. Security — no agent validates code safety before merge
2. Quality — no automated quality gates
3. Shipping — nothing cuts releases or changelogs
...
```

### Phase 2: Full Skill Inventory

List EVERY skill directory. No sampling. Complete coverage.

```bash
find . -name "SKILL.md" -not -path "./.agentfactory/*" -not -path "./.codex/*" -not -path "./.gemini/*" | sort
```

Count total skills. This is the denominator for the evaluation.

### Phase 3: Evaluate Each Skill (Two-Pass)

#### Pass 1: Fast Scan (all skills)

Extract metadata from every SKILL.md via bash — do NOT read full content yet:

```bash
# For each SKILL.md, extract: path, workflow structure indicators
find . -name "SKILL.md" ... | while read f; do
  dir=$(dirname "$f")
  workflow_hits=$(grep -ciE '(step|phase|workflow|process|audit|pipeline|gate)' "$f")
  first_lines=$(head -10 "$f")
  echo "$f | workflow:$workflow_hits | first_lines: $first_lines"
done
```

**Fast-scan elimination (only these are immediately NO):**
- Index/routing file (first 10 lines contain "overview of" / "collection" / "bundle" / "index of")
- Zero workflow indicators across the ENTIRE file (not just first 60 lines)

Everything else proceeds to Pass 2. The fast scan is a coarse filter to remove obvious non-candidates — it is NOT a quality gate. When in doubt, send to Pass 2.

#### Pass 2: Deep Read (candidates only)

For each skill that passed the fast scan, read the FULL SKILL.md and apply the 8-point checklist below. Do NOT stop at 60 lines — the information needed to evaluate dispatchability is often in "Initial Assessment", "Quick Start", or "Workflow" sections deeper in the file.

**THE 8-POINT CHECKLIST (all must pass):**

**Positive criteria (all three must be YES):**

1. **Structured workflow?** — Does the SKILL.md define step-by-step processes (not just reference material or advice)? Steps that could map to formula `[[steps]]` with entry/exit criteria?

2. **Dispatchable input?** — Can the work be triggered with a concrete input at dispatch time? (`af sling --agent X "path/to/target"` or `--var key=val`) No interactive Q&A needed to start. **Specifically check**: does the skill have an "Initial Assessment", "Intake", or "Grill-Me" section that demands clarifying questions before work begins? If YES → fails this criterion.

3. **Valuable when repeated?** — Would running this autonomously on a schedule or per-event (per-PR, per-release, per-skill) produce value? Not a one-shot setup task.

**Disqualifiers (any ONE triggers a NO):**

4. **No workflow structure** — Pure reference material, knowledge bases, or advisory content with no step sequence. Cannot become formula steps.

5. **Requires conversation** — Needs clarifying questions, user preferences, or back-and-forth before it can begin. Cannot be dispatched with a single input.

6. **One-shot setup** — Useful once (scaffolding, initialization) but not repeatedly. Not worth formula overhead.

7. **Script-replaceable** — The entire value is a deterministic computation a shell script handles. No LLM judgment needed.

8. **Output is consumed?** — Does the output trigger a downstream action (issue filed, PR created, dispatch to another agent, human decision prompted)? A file written to disk that no workflow reads is not consumed. "Generates a report" is NOT state change unless something acts on that report.

**LLM-executability check (applied to every YES):**

After passing the 8-point checklist, verify the workflow is executable by an LLM operating as a formula agent:
- Does the SKILL.md contain enough procedural instruction that an LLM can execute each step without external tooling or scripts?
- Are the steps concrete actions (run command, read file, produce artifact, make decision) rather than vague guidance?
- Could you dispatch this with a single input and get a meaningful result without human intervention mid-workflow?

If the skill depends on scripts/ for core functionality, it is a WORSE formula candidate — `/formula-create` does not carry over scripts.

**Evidence requirement:** For each YES, record: the specific dispatch command (concrete `af sling` invocation), and the specific output/state change the formula would produce. For each NO, record the failing criterion number and a 3-5 word reason. Bare YES/NO without evidence is invalid.

**Output format:** `skill-name: YES <dispatchable input in 10 words> → <output/state change>` or `skill-name: NO [criterion #] <3-5 word reason>`

### Delegation Rules

If delegating Phase 3 to sub-agents (necessary for 100+ skills):

1. Sub-agents perform Pass 1 (fast scan) AND Pass 2 (deep read) — not just metadata extraction
2. For every YES, sub-agents must return **evidence**: the specific dispatch command, the output/state change produced, and whether interactive Q&A intake was detected (with line number if present)
3. **The primary agent MUST deep-read every skill that sub-agents mark YES before including it in the final table.** Sub-agent conclusions are hypotheses, not verdicts. Verify by reading the full SKILL.md and confirming LLM-executability yourself.
4. Never present sub-agent conclusions directly to the user without primary verification.

### Phase 4: Map to Factory Gaps

For each YES skill, cite which numbered gap from Phase 1 it fills:

| Gap Category | Example |
|---|---|
| Quality maintenance | Catches degradation in existing assets |
| Security | Finds and drives remediation of vulnerabilities |
| Shipping/release | Automates publish, changelog, version, distribution |
| Marketing/growth | Produces discoverable content, landing pages, SEO |
| Compliance | Validates regulatory requirements, produces action items |
| Architecture | Produces design context consumed by other agents |
| Intelligence | Monitors landscape, surfaces actionable gaps |

**Enforcement:** Every YES skill must cite a specific gap number from the Phase 1 enumerated list. If a skill cannot cite a gap number, discard it — it doesn't fill a real factory need.

### Phase 5: Produce Actionable Output

For each surviving skill, output:

```
## <skill-name>

**Formula command:** `/formula-create /path/to/SKILL.md`
**Dispatch example:** `af sling --agent <name> "<concrete-input>"`
**Fills gap:** <gap number + category>
**What it does when dispatched:** <one sentence — what decision it makes and what state it changes>
**Frequency:** per-PR | per-release | scheduled | on-demand
**LLM-executable?** <YES — workflow is self-contained in SKILL.md | PARTIAL — depends on [X]>
```

Sort by priority: skills that fill the most critical factory gap first.

## Validation Step

Before finalizing, verify each recommendation:

```bash
# Confirm skill exists and is a real skill (not an index or meta-skill)
head -5 <skill-path>/SKILL.md
# Check total line count (workflow substance)
wc -l <skill-path>/SKILL.md
```

**Discard** any recommendation where the SKILL.md is a routing index (e.g., "engineering-skills", "marketing-skills") rather than an operational workflow.

**Script dependency warning:** If a skill relies on scripts/ for core workflow execution, flag it — `/formula-create` does not carry over scripts. The formula agent will not have access to those scripts. This is a negative signal, not a positive one.

## Anti-Patterns (from lived experience)

- **DO NOT** evaluate based on the first 60 lines alone. The information needed to assess dispatchability is often buried in "Initial Assessment" or "Intake" sections deeper in the file. Read the full SKILL.md for every candidate.
- **DO NOT** trust sub-agent YES verdicts without deep-reading the skill yourself. Sub-agents apply surface heuristics. You own the final judgment.
- **DO NOT** treat scripts/ as a positive signal. `/formula-create` does not carry over scripts. Skills that depend on scripts for core functionality are WORSE formula candidates, not better.
- **DO NOT** invent composite agent names by combining multiple skills. Each skill stands or falls on its own.
- **DO NOT** evaluate a sample. Evaluate every skill. The user cannot hand-verify your coverage.
- **DO NOT** forget that `/formula-create` adds autonomy. The skill doesn't need to be autonomous today — it needs a workflow worth automating.
- **DO NOT** ask the user which skills to prioritize. Own the analysis. Present conclusions.
- **DO NOT** conflate "generates a file" with "changes state." A file written to disk that nothing downstream reads or acts on is not a state change.

## Output Format

Final deliverable is a prioritized table:

| Priority | Skill | Path | Formula Command | Dispatch Example | Gap Filled | LLM-Executable | Frequency |
|---|---|---|---|---|---|---|---|
| 1 | ... | ... | ... | ... | ... | ... | ... |

Followed by a gap analysis: which factory gaps remain unfilled by any existing skill (these represent genuinely new agents to build from scratch).
