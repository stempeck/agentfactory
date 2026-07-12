<!-- DRAFT — staged by visibility plan Phase 6. Review before publishing. Tier B: requires Glenn's explicit go-ahead. -->

# Publish Checklist (~30 minutes, in this order)

Each step feeds links or credibility into the next — don't reorder.

## 0. Prep (5 min)
- [ ] Export the README architecture diagram as an image for Medium: open https://github.com/stempeck/agentfactory#readme in a browser, let GitHub render the Mermaid diagram, and screenshot it (or click the diagram's expand icon and screenshot the full-size render). Save as `architecture.png`.
- [ ] Read each draft below once, end to end, and edit anything that doesn't sound like you. These are drafts, not scripture.

## 1. LinkedIn (~3 min) — `01-linkedin-announcement.md`
Paste and post. **Why first:** it's the lowest-risk audience, it starts the engagement clock with your own network, and the post URL is a citable link for later steps.

## 2. Medium (~10 min) — `02-medium-article.md`
Paste into a new Medium story, insert `architecture.png` at the marked placeholder, publish. **Why second:** the article is the durable SEO asset — every later step should be able to link to it, so it must exist before HN/Reddit.

## 3. Repo homepage URL (~1 min)
```bash
gh repo edit stempeck/agentfactory --homepage "<your-medium-article-url>"
```
**Why third:** GitHub visitors from HN/Reddit should land on a repo whose homepage link points at the deep-dive, closing the loop between the repo and the article.

## 4. Show HN (~5 min) — `03-show-hn.md`
Submit the repo URL with your chosen title at https://news.ycombinator.com/submit, then immediately post the prepared first comment. Read https://news.ycombinator.com/showhn.html first. **Why fourth:** HN is the highest-variance, highest-reward channel — by now the repo, article, and homepage link are all in place for the traffic spike, and you have the whole comment thread pre-thought.

## 5. Reddit (~5 min) — `04-reddit.md`
Post Variant A to r/ClaudeAI, Variant B to r/LocalLLaMA (check each sub's current flair options). **Why fifth:** Reddit threads benefit from being able to reference "discussed on HN today" if the HN post gets traction, and posting after HN avoids looking like a coordinated blast while HN is still deciding.

## 6. Awesome-list PRs — `05-awesome-list-prs.md`
Run the prepared commands (each is one `gh` invocation) or tell Claude "go" on the staged PRs. **Why last:** list maintainers check whether a project looks alive — arriving after a day of visible activity (release, article, discussion) measurably improves merge odds.

## Afterward
- Reply to comments on all channels for the first 48 hours; that's where the compounding happens.
- Two-week check: Google `"Glenn Stempeck" github` and `site:github.com stempeck agentfactory`.

## Recorded URLs (gate: published — resolved 2026-07-12)
- Medium article: https://medium.com/@glennstempeck/95-reliable-agents-give-you-86-reliable-workflows-b264170eb66c
- LinkedIn post: PUBLISHED, URL not captured (lnkd.in shortlink used in-post)
- Homepage updated: yes → article URL (verified)
- Show HN / Reddit / awesome-lists: SKIP (operator decision)
