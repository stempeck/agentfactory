---
name: Agentfactory Issue
about: Report a problem encountered during agentfactory setup or operation Or a write-up a new feature
title: ''
labels: ''
assignees: ''
---

## Before You Proceed

Read `USING_AGENTFACTORY.md` first. Before doing any investigation or code changes, state the Vision, Mission, and summarize the current usage flow in your own words — including how agents start, how they receive work, and how formulas drive execution. Do not proceed until you have done this.

## Requirements

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

At any point during the processes problems are encountered, and this is the problem we need to solve next described in the scenario below.

---

### scenario

[Describe your scenario here]

---

Assume *.md documents might have outdated information and you should review the codebase as the only real source-of-truth and only use them to help in your search. You should also rely on ./USING_AGENT_FACTORY.md and other ./.designs/*.md files for understanding the archtictural intentions of agentfactory before making recommendations.

### Acceptance criteria
The ONLY successfull outcome of agentfactory is that we're able to follow the process steps described to setup a fresh agentfactory with clean repo and when we get to the step where we ask the manager,`run af sling --agent <some-agent> "task description"`, we should have the work executed autonomously using the step-by-step formula that represents the IDENTITY of that agent and respects the formulas rigid step-by-step process up to the point where human interaction is necessary for next steps and all the code branches created should have been pushed as PR's against the main branch without doctor fixes or human interaction (unless absolutely necessary, or in the case of doctor - a `doctor --fix` is acceptable only as a bandaid/fix for legacy broken behaviors, not as an ongoing operational dependency). The agents should follow the known working formula process that IS their IDENTITY to perform their work so that we have consistent successful outcomes out of each agent. Your mission when addressing any problem scenario is to seek to understand how to achieve this desired outcome with systemic improvements while addressing the scenario.
