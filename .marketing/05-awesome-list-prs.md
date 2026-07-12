<!-- DRAFT — Tier B. These open PRs on repos Glenn does NOT own. Do not execute without his explicit go-ahead. -->

# Awesome-List Submissions — Staged, Run on Approval

All targets verified alive on 2026-07-11 (`gh repo view --json pushedAt`). Reusable one-line
description (written as a description, not a pitch, per awesome-claude-code style rules):

> Multi-agent orchestration CLI for Claude Code. Turns SKILL.md files into autonomous agents
> that execute declarative TOML workflow DAGs, with inter-agent mail and crash recovery that
> survives context compression.

| # | List | Stars | Last push | Method | Fit |
|---|------|-------|-----------|--------|-----|
| 1 | andyrewlee/awesome-agent-orchestrators | 987 | 2026-07-04 | PR | ★★★ exact niche (orchestrators for AI coding agents) |
| 2 | e2b-dev/awesome-ai-agents | 28,696 | 2026-07-09 | PR or Google form | ★★ broad, huge audience |
| 3 | kyrolabs/awesome-agents | 2,605 | 2026-07-09 | PR | ★★ Software Development section fits |
| 4 | hesreallyhim/awesome-claude-code | 49,822 | 2026-07-12 | **Web issue form ONLY — no PR, human-submitted** | ★★★ biggest Claude Code audience |

Excluded (documented below): avelino/awesome-go, kaushikb11/awesome-llm-agents.

---

## 1. awesome-agent-orchestrators (andyrewlee) — best fit, do this one first

- **Section:** `## Multi-Agent Swarms` ("Systems for coordinating multiple specialized agents working together")
- **Format:** `- [name](url) - Description.` — alphabetical, case-insensitive. No CONTRIBUTING.md; plain PRs are the norm.
- **Insertion point:** immediately AFTER the line starting `- [Agent Teams](https://github.com/777genius/agent-teams-ai)` and BEFORE the line starting `- [agentsmesh](https://github.com/AgentsMesh/AgentsMesh)` ("agent teams" < "agentfactory" < "agentsmesh", space sorts before "f").
- **Entry line (exact):**

```markdown
- [agentfactory](https://github.com/stempeck/agentfactory) - Multi-agent orchestration CLI for Claude Code that turns SKILL.md files into autonomous agents. Declarative TOML workflow DAGs with quality gates, inter-agent mail, tmux-native sessions, and crash recovery that survives context compression.
```

⛔ RUN ON APPROVAL ONLY
```bash
gh repo fork andyrewlee/awesome-agent-orchestrators --clone --default-branch-only
cd awesome-agent-orchestrators
git checkout -b add-agentfactory
# Edit README.md: insert the entry line above between the "Agent Teams" and "agentsmesh"
# lines in the "## Multi-Agent Swarms" section.
git add README.md
git commit -m "Add agentfactory to Multi-Agent Swarms"
git push -u origin add-agentfactory
gh pr create --repo andyrewlee/awesome-agent-orchestrators \
  --title "Add agentfactory to Multi-Agent Swarms" \
  --body "Adds [agentfactory](https://github.com/stempeck/agentfactory) — a multi-agent orchestration CLI for Claude Code. Agents execute declarative TOML workflow DAGs (\"formulas\"), coordinate over built-in inter-agent mail, run in observable tmux sessions, and recover from context compression via re-injected step state. Go, AGPL-3.0.

Disclosure: I'm the author. Entry follows the existing format and alphabetical order."
```

## 2. e2b-dev/awesome-ai-agents

- **Rules (README "Have anything to add?"):** "Create a pull request or fill in this [form](https://forms.gle/UXQFCogLYrPFvfoUA). Please keep the alphabetical order and in the correct category." Their scope note: agents/assistants only (SDKs go to a sister list) — agentfactory produces autonomous agents, so it is in scope.
- **Section:** `# Open-source projects`, alphabetical. **Insertion point:** after the `## [Agent4Rec]` block's closing `</details>` and before the `## [AgentForge](https://github.com/DataBassGit/AgentForge)` heading ("agent4rec" < "agentfactory" < "agentforge").
- **Entry block (exact, matches their house format):**

```markdown
## [agentfactory](https://github.com/stempeck/agentfactory)
Multi-agent orchestration CLI for Claude Code — turn SKILL.md files into autonomous agents

<details>

### Category
Build your own, Multi-agent, Coding

### Description
- **Formulas**: declarative TOML workflow DAGs with steps, dependencies, variables, and quality gates
- **Skills → agents**: converts existing SKILL.md files into autonomous agents (`/formula-create`, then `af formula agent-gen`)
- **Crash recovery**: survives context compression — `af prime` re-injects agent identity and current step state
- **Inter-agent mail**: agents message, delegate, and broadcast with `af mail`
- **tmux-native**: every agent runs in an observable, attachable tmux session
- **Optional web console**: loopback-only browser control room for the factory

### Links
- [GitHub](https://github.com/stempeck/agentfactory)
- [Usage guide](https://github.com/stempeck/agentfactory/blob/main/USING_AGENTFACTORY.md)

</details>
```

⛔ RUN ON APPROVAL ONLY
```bash
gh repo fork e2b-dev/awesome-ai-agents --clone --default-branch-only
cd awesome-ai-agents
git checkout -b add-agentfactory
# Edit README.md: insert the block above between the Agent4Rec entry's closing </details>
# and the "## [AgentForge]" heading.
git add README.md
git commit -m "Add agentfactory"
git push -u origin add-agentfactory
gh pr create --repo e2b-dev/awesome-ai-agents \
  --title "Add agentfactory (multi-agent orchestration CLI for Claude Code)" \
  --body "Adds agentfactory to Open-source projects, in alphabetical order (between Agent4Rec and AgentForge), following the existing entry format.

agentfactory is an open-source (AGPL-3.0, Go) CLI that turns SKILL.md files into autonomous Claude Code agents: declarative TOML workflow DAGs, inter-agent mail, tmux-native sessions, and context-compression crash recovery.

Disclosure: I'm the author."
```
*(Fallback if the PR sits unreviewed: submit the same content via their Google form — that's their other accepted channel.)*

## 3. kyrolabs/awesome-agents

- **Section:** `## Software Development`. Entries use `- [Name](url): Description ![GitHub Repo stars badge]` and are appended at the END of the section (list is in append order, not alphabetical — newest entries last, e.g. Paperclip).
- **Entry line (exact), appended after the last line of the Software Development section:**

```markdown
- [agentfactory](https://github.com/stempeck/agentfactory): Multi-agent orchestration CLI for Claude Code that turns SKILL.md files into autonomous agents — declarative TOML workflow DAGs, inter-agent mail, tmux-native sessions, and context-compression crash recovery ![GitHub Repo stars](https://img.shields.io/github/stars/stempeck/agentfactory?style=social)
```

⛔ RUN ON APPROVAL ONLY
```bash
gh repo fork kyrolabs/awesome-agents --clone --default-branch-only
cd awesome-agents
git checkout -b add-agentfactory
# Edit README.md: append the entry line above as the last item of "## Software Development"
# (currently ends at the Paperclip entry).
git add README.md
git commit -m "Add agentfactory to Software Development"
git push -u origin add-agentfactory
gh pr create --repo kyrolabs/awesome-agents \
  --title "Add agentfactory to Software Development" \
  --body "Adds [agentfactory](https://github.com/stempeck/agentfactory) — open-source (Go, AGPL-3.0) multi-agent orchestration CLI for Claude Code: SKILL.md → autonomous agents, declarative TOML workflow DAGs, inter-agent mail, tmux-native sessions, crash recovery. Entry matches the section's existing format (stars badge included).

Disclosure: I'm the author."
```

## 4. awesome-claude-code (hesreallyhim) — ⚠️ NO PR. Web form, submitted by a human.

Their CONTRIBUTING.md is explicit:
- "ALL RECOMMENDATIONS MUST BE MADE USING THE WEB UI ISSUE FORM TEMPLATE, OR YOU RISK BEING RESTRICTED FROM INTERACTING WITH THIS REPOSITORY TEMPORARILY."
- "Do not open a PR." / "It is **not** possible to submit a resource recommendation using the `gh` CLI."
- "resource recommendations must be created by human beings."
- Expectation-setting: they advise *get users first, then submit* — with a brand-new repo the odds are lower; this is their stated selectivity, not a reason to skip, but don't count on acceptance.

**Glenn's manual step** (2 minutes): open
<https://github.com/hesreallyhim/awesome-claude-code/issues/new?template=recommend-resource.yml>
and paste:

- **Resource name:** agentfactory
- **URL:** https://github.com/stempeck/agentfactory
- **Description (their style rules: a description, not a sales pitch; one line; no emojis):**
  Multi-agent orchestration CLI for Claude Code. Turns SKILL.md files into autonomous agents that execute declarative TOML workflow DAGs, with inter-agent mail and crash recovery that survives context compression.
- License is auto-detected by their bot (AGPL-3.0 LICENSE file is in place, already detected by GitHub — no action needed).

If accepted, they invite adding this badge to the README:
`[![Mentioned in Awesome Claude Code](https://awesome.re/mentioned-badge.svg)](https://github.com/hesreallyhim/awesome-claude-code)`

---

## Excluded targets — and why

- **avelino/awesome-go (177k★):** their quality checklist requires ≥80% test coverage with a linked coverage report, pkg.go.dev doc comments on all public APIs, Go Report Card + coverage links in the PR body, and ≥5 months of history. agentfactory can't demonstrate the coverage/pkg.go.dev items today; submitting would burn a one-shot review. Revisit after a coverage report and doc-comment pass exist.
- **kaushikb11/awesome-llm-agents (1.5k★):** showcases established frameworks (CrewAI 54k★, LangChain 140k★, AutoGen 59k★) with auto-generated star/fork metrics per entry (`update_metrics.py`). A 0-star project would stand out badly and likely be rejected. Revisit after ~100+ stars.

## Suggested order

1 (orchestrators, exact niche) → 3 (kyrolabs) → 2 (e2b) → 4 (awesome-claude-code form, expectations managed). Space them out over a week or two; simultaneous identical PRs across lists look like a campaign.
