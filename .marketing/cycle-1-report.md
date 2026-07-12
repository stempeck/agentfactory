<!-- 2026-07-12: this folder moved from marketing-drafts/ to .marketing/ and files were
renamed to marketing-cycle formula naming (00-audit→cycle-1-audit, 99-report→cycle-1-report,
etc.) when cycle 1's outcome was reproduced under the generic formula's layout. Path
references below are historical. -->

# Visibility Plan — Execution Report
*2026-07-11 — executed by Claude Code. Autonomous scope: Tier A. Tier B staged as drafts.*

## Changed autonomously (Tier A — live now)

### Identity (Phase 2)
- **Created [stempeck/stempeck](https://github.com/stempeck/stempeck)** — profile README with name in H1, positioning statement (agentic systems, multi-agent orchestration, Claude Code), featured agentfactory section, verified links to LinkedIn, Medium, and @gstempeck ("earlier work lives here"). This is the page that ranks for name searches.
- Profile **name** was already "Glenn Stempeck" — no change needed.

### Repo metadata (Phase 3)
- **Description**: "Multi-agent orchestration CLI for Claude Code — declarative TOML workflows, autonomous agents, context-compression recovery, inter-agent mail." (was: "A Factory of Agents - for Enterprises")
- **Topics**: 14 → 18. Added `multi-agent-systems`, `llm-orchestration`, `agent-framework`, `golang`, `cli`, `workflow-automation`, `anthropic`, `autonomous-agents`. Removed 4 zero-discovery-value topics (`agentfactory-af`, `agentfactory-cli`, `enterprise-software`, `enterprise-solutions`) to stay under GitHub's 20-topic cap.
- **Homepage**: set to the README anchor as a placeholder — swap to the Medium article URL after publishing (checklist step 3).
- **Social preview**: 1280×640 PNG generated → `marketing-drafts/social-preview.png` (upload is settings-page-only; see manual tasks).

### Content (Phase 4) — merged via [PR #87](https://github.com/stempeck/agentfactory/pull/87), all CI green
- **README rewritten as a landing page**: prose "Why agentfactory" opening, badges (CI/Go/license/release), native Mermaid architecture diagram, honest comparison table (LangGraph / CrewAI / raw Claude Code subagents), roadmap linking real issues, author line binding the repo to name searches. Fixed two stale claims found during verification: formula table said 5 formulas (19 ship), and the manual `.gitignore` instruction (install --init already writes `.git/info/exclude`).
- **Three practitioner docs pages** — `docs/formulas.md`, `docs/agent-lifecycle.md`, `docs/recovery-model.md`. Every command verified against `internal/cmd/` source; one invented gate type caught and removed before commit.

### Health signals (Phase 5)
- **CI**: already existed (7 checks) — added badges to README instead of a new workflow.
- **[Release v0.1.0](https://github.com/stempeck/agentfactory/releases/tag/v0.1.0)** — first formal release with capability-summary notes.
- **CHANGELOG.md** seeded from the full PR history, grouped by theme.
- **Issue templates**: standard bug report + feature request added alongside the agent-dispatch default.
- **Three new real issues** from the roadmap and docs/architecture/gaps.md: [#84](https://github.com/stempeck/agentfactory/issues/84) GoReleaser binaries (enhancement), [#85](https://github.com/stempeck/agentfactory/issues/85) BD_ACTOR→AF_ACTOR rename (good first issue), [#86](https://github.com/stempeck/agentfactory/issues/86) stale CLAUDE.md role list (good first issue). 6 open issues total.

### Maintenance (Phase 9)
- `.github/workflows/visibility-health.yml` — monthly check (3rd of each month) that topics, description, release, and README badges haven't regressed; opens exactly one issue on regression. Read-only; never commits.

## Verified (Phase 7)
- Repo metadata confirmed live: 18 topics, v0.1.0 release, new description.
- `gh api /search/repositories?q=topic:claude-code+agentfactory` → **stempeck/agentfactory already indexed** under the topic.
- stempeck/stempeck live; 6 open issues; AGPL-3.0 detected.

## Staged for approval (Tier B — in this folder, NOT published, NOT committed)
| File | Content |
|---|---|
| 01-linkedin-announcement.md | ~160-word first-person post + alt hook |
| 02-medium-article.md | ~1,350 words: "Designing a multi-agent orchestrator for Claude Code: what breaks and why" |
| 03-show-hn.md | 3 title options + author first comment with honest limitations |
| 04-reddit.md | r/ClaudeAI + r/LocalLLaMA variants tuned to each community |
| 05-awesome-list-prs.md | 4 verified targets with exact ready-to-run PR commands (⛔ run on approval); awesome-go honestly excluded — its quality bar (coverage report, Go Report Card) isn't met yet |
| 06-publish-checklist.md | The ordered 30-minute publish sequence |

This folder and the plan file are excluded from git via `.git/info/exclude` — marketing strategy stays off the public repo.

## Only Glenn can do these
1. ~~Profile bio/blog/location~~ — **DONE 2026-07-11** after Glenn ran `gh auth refresh -s user`. Bio, blog (Medium), and location set via `PATCH /user` and confirmed by the API response. The previous bio ("Computer Scientist, Dad, Husband, Leader…") is preserved in 00-audit.md if you ever want to blend the two.
2. ~~Pin repos~~ — **DONE 2026-07-12** (verified via API: `agentfactory` and `stempeck` are pinned).
3. ~~Social preview~~ — **DONE 2026-07-12** (uploaded by Glenn).
4. ~~gstempeck cross-link~~ — **DECLINED 2026-07-12**: account is work-connected; Glenn chose not to cross-link from it. (Open preference: the stempeck/stempeck README still links *to* gstempeck — remove if full separation is ever wanted.)
5. **Publish Tier B** — PARTIALLY DONE 2026-07-12: LinkedIn post published (Glenn's own hook) and Medium article live at https://medium.com/@glennstempeck/95-reliable-agents-give-you-86-reliable-workflows-b264170eb66c (title: "95% reliable agents give you 86% reliable workflows"); repo homepage points at it. Glenn also added github.com/stempeck to his LinkedIn profile. Show HN, Reddit, and awesome-list PRs deliberately skipped — drafts remain in this folder if ever wanted.

## 2-week check (after Google recrawl)
- Google: `"Glenn Stempeck" github` — expect the stempeck profile and agentfactory to surface.
- Google: `site:github.com "Glenn Stempeck"` — expect both accounts.
- GitHub topic pages for `claude-code`, `ai-agents`, `multi-agent-systems` — already indexed via API; spot-check the web UI.
