# Publish checklist — cycle-af-50cdad6b (flagship: web console + telemetry)

Tier B is yours to publish — I prepped the mechanics; you click. **Order matters:** the LinkedIn
post links to the Medium article, so Medium goes first. Record each URL in the form at the bottom
(or write SKIP). When you record the Medium URL I'll point the repo homepage at it and verify
every page.

Assets (all in `.marketing/`): `cycle-af-50cdad6b-paste.html` (Medium rich-text vehicle),
`cycle-af-50cdad6b-console-floor-diagram.png`, `cycle-af-50cdad6b-telemetry-diagram.png`,
`cycle-af-50cdad6b-medium.md`, `cycle-af-50cdad6b-linkedin.md`.

## 1. Medium (first)

1. New story. **Title field:** `I run a factory of AI agents. Here's the window into it.`
2. **Subtitle field** (the small-T line under the title — it's also Google's meta description):
   `The agentfactory web console - a loopback control room for your Claude Code agents - and the telemetry view I just added, so you can finally SEE where an agent's time and tokens go`
3. **Body:** open `cycle-af-50cdad6b-paste.html` in a browser. Ignore the yellow instruction box.
   Select from the first paragraph ("Here's a thing nobody tells you…") through
   "Learn it, Live it, Share it!", copy, and paste over Medium's story body. Medium ignores pasted
   markdown, which is why we use this rich-text file instead of the `.md`.
4. **Verify the two images survived** the paste (Floor view, then Telemetry view). If an image
   didn't come through, drag the PNG in at its `[SCREENSHOT]` spot.
5. **5 topics** (the proven mix — 2 precise + 1 giant + 2 professional):
   `AI Agents`, `Claude`, `Artificial Intelligence`, `Software Engineering`, `Agentic AI`.
6. Skim for strays: no leading `# `, no literal `##`/`**`, no alt-text sitting as a caption, the
   subtitle is in the subtitle field (not a first body paragraph).
7. Publish → copy the URL into the form below.

## 2. Repo homepage (after the Medium URL is recorded)

Leave this to me: once you record the Medium URL, I validate it against the runbook's homepage
allowlist (`https://medium.com`) and run `gh repo edit --homepage <url>`. No action from you.

## 3. LinkedIn (after Medium is live)

1. Open `cycle-af-50cdad6b-linkedin.md` and copy the plain text between the `---` lines
   (PLAIN TEXT — LinkedIn renders no markdown).
2. In the last line, replace `<MEDIUM ARTICLE URL — paste after publishing the Medium piece>`
   with the real Medium URL from step 1. (`Get it:` already points at the repo.)
3. Attach the two screenshots (Floor view + Telemetry view) as images on the post.
4. Post → copy the URL into the form below.

## Publish record (fill this in — this is what resolves the gate)

```
- Medium URL: https://medium.com/@glennstempeck/i-run-a-factory-of-ai-agents-heres-the-window-into-it-ad0321bfce9f
- LinkedIn URL: https://www.linkedin.com/posts/glenn-stempeck_i-run-a-factory-of-ai-agents-heres-the-share-7490182070434951168-Jwbi/
- Notes: Both published by operator 2026-08-03 (recorded via issue #96). Repo homepage updated
  to the Medium URL (allowlist-validated). Gate `published` resolved.
```

Once the URLs are here (or SKIP), I update the homepage, then verify every published page
(fetch + the checks in Phase 6) and post any exact "search for / replace with" fixes back here.
