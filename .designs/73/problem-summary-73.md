# Problem Summary — Issue #73

**Issue:** [New dispatch workflow default to be included with agentfactory](https://github.com/stempeck/agentfactory/issues/73)
**Number:** 73
**Labels:** (none)
**Fetched:** 2026-06-28T16:35:12Z

---

## Before You Proceed (issue author's framing)

Read `USING_AGENTFACTORY.md` first. Before doing any investigation or code
changes, state the Vision, Mission, and summarize the current usage flow in
your own words — including how agents start, how they receive work, and how
formulas drive execution. Do not proceed until you have done this.

## Requirements / Context

When the agentfactory project is newly installed in a docker container, the
typical setup-and-start flow is:

1. `./quickdocker.sh <github-repo-path>` is run (with appropriate parameters);
   the docker image is built and the user lands in a bash with immediate access
   to agentfactory.
2. `claude` is run and authenticated manually.
3. `~/projects/agentfactory/quickstart.sh` is executed to build, install &
   initialize the factory.
4. `af up manager` starts the manager.
5. `af attach manager` enters and issues commands.

Then either:

6. `af up design-v3` starts an agent (for direct interaction), and
7. `af attach design-v3` attaches to interact directly with the agent.

OR (recommended):

6. `af sling --agent design-v3 "<github-issue>"` initiates autonomous agent work.

Problems are encountered at various points; the scenario below is the next one
to solve.

## Scenario (the core problem)

We need to update `dispatch.json` to include a **baked-in default** for dispatch
with agentfactory, so that when someone new to the project uses
`./quickdocker.sh repo-link` and gets their container bootstrapped — landing in
`/af/repo` with their newly bootstrapped repository ready to use agentfactory —
they could opt to **just start tagging their GitHub issues with labels** to kick
off work, **without ever needing to visit the manager**.

The `dispatch.json` should be bootstrapped **when the dispatch.json is first
created during initial setup of the repository**, so we know the repository name
and can include it appropriately in the dispatch.json. The author proposes
something like the following default (verbatim from the issue, including its
typos/unclosed brackets — the *intent* is what matters, not the literal JSON):

```json
{
  "repos": [
    "org/repository"        // <- update this with the ACTUAL org/repository during the install
  ],
  "trigger_label": "agentic",
  "notify_on_complete": "manager",
  "interval_seconds": 300,
  "retry_after_seconds": 1800,
  "remove_trigger_after_dispatch": true,
  "mappings": [
    { "labels": ["rapid-plan"],     "source": "issue", "agent": "rapid-soldesign-plan" },
    { "labels": ["rapid-engineer"], "source": "issue", "agent": "rapid-implement" },
    { "labels": ["pr-review"],      "source": "pr",    "agent": "ultra-review" },
    { "labels": ["pr-iterate"],     "source": "pr",    "agent": "rapid-increment" }
  ],
  "workflows": [
    { "label": "feature-workflow", "phases": ["rapid-plan", "rapid-engineer"] }
  ]
}
```

## Source-of-truth guidance

- Assume `*.md` documents may contain outdated information. Treat the **codebase
  as the only real source of truth**; use docs only to aid the search.
- Rely on `./USING_AGENTFACTORY.md` and other `./.designs/*.md` files to
  understand the **architectural intentions** of agentfactory before making
  recommendations.

## Acceptance Criteria

The ONLY successful outcome of agentfactory is that we can follow the documented
setup steps on a fresh agentfactory with a clean repo, and when we reach the
step where we ask the manager to `run af sling --agent <some-agent> "task
description"`, the work is executed **autonomously** using the step-by-step
formula that represents the **IDENTITY** of that agent, respecting the formula's
rigid step-by-step process up to the point where human interaction is genuinely
necessary for next steps. All code branches created must be **pushed as PRs
against the main branch** without doctor fixes or human interaction (unless
absolutely necessary; a `doctor --fix` is acceptable only as a bandaid for
legacy broken behaviors, not an ongoing operational dependency).

Agents should follow the known-working formula process that IS their IDENTITY,
so we get consistent successful outcomes from each agent. The mission when
addressing any problem scenario is to seek to understand how to achieve this
desired outcome with **systemic improvements** while addressing the scenario.
