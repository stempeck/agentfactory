# source.md — Verbatim Source Capture

## Source References
- [x] Source 1: `https://github.com/stempeck/agentfactory/issues/73` — fetched at `2026-06-28T16:35:12Z` (re-fetched verbatim via `gh issue view 73 --json body` on 2026-06-28)
- Title: **New dispatch workflow default to be included with agentfactory**
- Labels: (none)

## Problem Statement (verbatim)

```
### scenario

We need to update dispatch.json to include a baked-in default for dispatch with agentfactory, so that when someone new to the project uses `./quickdocker.sh repo-link` and gets their container bootstrapped and lands in `/af/repo` with their newly bootstrapped repository ready to use agentfactory, they could opt to just start tagging their github issues with tags instead to kick off work without ever needing to visit the manager.

The dispatch.json should be boostrapped when the dispatch.json is first created during initial setup of the repository so we know the repository-name and can include that appropriately in the dispatch.json, with something like this:
```

## Acceptance Criteria (verbatim)

Each AC below is a clause extracted verbatim from the source. The source presents
AC-1..AC-3 in the `### scenario` section (the concrete deliverable) and AC-4..AC-6
in the `### Acceptance criteria` section (the end-to-end success condition).

### AC-1
> "We need to update dispatch.json to include a baked-in default for dispatch with agentfactory"

### AC-2
> "they could opt to just start tagging their github issues with tags instead to kick off work without ever needing to visit the manager."

### AC-3
> "The dispatch.json should be boostrapped when the dispatch.json is first created during initial setup of the repository so we know the repository-name and can include that appropriately in the dispatch.json"

### AC-4
> "when we get to the step where we ask the manager,`run af sling --agent <some-agent> \"task description\"`, we should have the work executed autonomously using the step-by-step formula that represents the IDENTITY of that agent and respects the formulas rigid step-by-step process up to the point where human interaction is necessary for next steps"

### AC-5
> "all the code branches created should have been pushed as PR's against the main branch without doctor fixes or human interaction (unless absolutely necessary, or in the case of doctor - a `doctor --fix` is acceptable only as a bandaid/fix for legacy broken behaviors, not as an ongoing operational dependency)."

### AC-6
> "The agents should follow the known working formula process that IS their IDENTITY to perform their work so that we have consistent successful outcomes out of each agent. Your mission when addressing any problem scenario is to seek to understand how to achieve this desired outcome with systemic improvements while addressing the scenario."

## Constraints (verbatim)

### C-1
> "update dispatch.json to include a baked-in default for dispatch with agentfactory"
>
> Inference: the default dispatch configuration must ship *with agentfactory* (baked in to the tool), not be hand-authored by the new user.

### C-2
> "The dispatch.json should be boostrapped when the dispatch.json is first created during initial setup of the repository"
>
> Inference: the bootstrap of the default MUST occur at the moment dispatch.json is first created during initial repository setup — not lazily, not on a later command.

### C-3
> "so we know the repository-name and can include that appropriately in the dispatch.json"
>
> and from the proposed JSON: `"org/repository" <-update this with the ACTUAL org/repository during the install`
>
> Inference: the `repos` entry must be populated with the ACTUAL `org/repository` discovered during install, not left as a literal placeholder.

### C-4
> "Assume *.md documents might have outdated information and you should review the codebase as the only real source-of-truth and only use them to help in your search. You should also rely on ./USING_AGENT_FACTORY.md and other ./.designs/*.md files for understanding the archtictural intentions of agentfactory before making recommendations."
>
> Inference: the codebase is the only authoritative source of truth; markdown docs may be stale and are search aids only. Architectural intentions are drawn from USING_AGENTFACTORY.md and .designs/*.md.

### C-5
> "without doctor fixes or human interaction (unless absolutely necessary, or in the case of doctor - a `doctor --fix` is acceptable only as a bandaid/fix for legacy broken behaviors, not as an ongoing operational dependency)."
>
> Inference: the autonomous path must work without `af doctor --fix` and without human intervention as an ongoing operational dependency. `doctor --fix` is tolerated only as a temporary bandaid for legacy broken behaviors.

### C-6 [inferred]
> from the proposed JSON `mappings` / `workflows`: agents `rapid-soldesign-plan`, `rapid-implement`, `ultra-review`, `rapid-increment`; labels `rapid-plan`, `rapid-engineer`, `pr-review`, `pr-iterate`; workflow phases `["rapid-plan", "rapid-engineer"]`.
>
> Inference: every agent named in a `mappings[].agent` and every label/phase referenced by `workflows` must correspond to an agent/formula that actually exists and is dispatchable, or label-triggered dispatch will fail at runtime (this is required for AC-4, which demands the slung work actually execute).

## Additional Context (verbatim)

### "Before You Proceed" directive (verbatim)
```
## Before You Proceed

Read `USING_AGENTFACTORY.md` first. Before doing any investigation or code changes, state the Vision, Mission, and summarize the current usage flow in your own words — including how agents start, how they receive work, and how formulas drive execution. Do not proceed until you have done this.
```

### Setup flow described in the issue (verbatim)
```
When the /agentfactory/ project is newly installed in a docker container the process usually goes something like this to setup and start working on a new repository:
1. `./quickdocker.sh <github-repo-path>` is run (with appropriate parameters), and then the docker image is ready we're put into a bash with immediate access to agentfactory
2. `claude` is run and authenticated manually
3. `~/projects/agentfactory/quickstart.sh` is executed to build, install & initialize the factory
4. `af up manager` is run to start the manager
5. `af attach manager` is run to enter and issue commands
Like
6. `af up design-v3` is run to start an agent like design-v3 (for direct interaction)
7. `af attach design-v3` is run (for direct interact with an agent)
OR (recommended)
6. `af sling --agent design-v3 "<github-issue>"` is run to initiate the autonomous agent work
```

### Proposed default dispatch.json (verbatim — includes the source's typos/unclosed brackets; the *intent* matters, not the literal JSON)
```
{
  "repos": [
    "org/repository" <-update this with the ACTUAL org/repository during the install
  ],
  "trigger_label": "agentic",
  "notify_on_complete": "manager",
  "interval_seconds": 300,
  "retry_after_seconds": 1800,
  "remove_trigger_after_dispatch": true,
  "mappings": [
    {
      "labels": [
        "rapid-plan
      ],
      "source": "issue",
      "agent": "rapid-soldesign-plan"
    },
    {
      "labels": [
        "rapid-engineer"
      ],
      "source": "issue",
      "agent": "rapid-implement"
    },
    {
      "labels": [
        "pr-review"
      ],
      "source": "pr",
      "agent": "ultra-review"
    },
    {
      "labels": [
        "pr-iterate"
      ],
      "source": "pr",
      "agent": "rapid-increment"
    }
  ],
  "workflows": [
    {
      "label": "feature-workflow",
      "phases": ["rapid-plan", "rapid-engineer"]
    }
  ]
}
```

## Scope
**Determined scope: medium** — all 6 dimensions, standard depth (2-3 options each).
Rationale: the change is narrow in surface (a bootstrap of one config file) but
touches install/setup code, the dispatch config schema, repo-name discovery, and
correctness of agent/label references — multiple dimensions with real trade-offs.

## Sign-off
- [x] Every AC from the source is quoted above under its own heading.
- [x] Every constraint from the source is quoted or marked [inferred] with its source text.
- [x] Nothing in this file has been summarized or paraphrased (verbatim blocks only; inferences explicitly labelled).
