# Cycle af-a6b105d1 Report — stempeck/agentfactory

*2026-10-30 to 2026-11-01. Flagship: security, meaning bootstrap convergence: pinned, integrity-verified
installs that fail the build rather than run drifted (#124). Operator: Glenn Stempeck (`stempeck`).*

## Summary

The audit ranked token economics (#111) first. The operator REORDERED at the story gate (#125): token
economics is "played-out and old", and the story is security. The positioning they set: agentfactory is
moving from "a factory of agents" to "the most secure way you can run multi-agent orchestration". The cycle
shipped a README security section and the CHANGELOG entry (PR #129). It drafted Medium and LinkedIn in the
operator's voice; the operator EDITED them through five inline review comments, and the edits were
reconciled. Then it cut **v0.4.0**: a CHANGELOG fold (PR #131), the tag on green main, and, at the
operator's direction, the repo's first prebuilt binary package. The operator chose not to post either
social piece. The release page was verified and is clean.

## Shipped autonomously (Tier A)

- **README "Security & reproducible installs"** (PR #129 → `0bf9f381`). It covers:
  - ADR-026 (every change keeps or strengthens every security control);
  - bootstrap convergence (digests, signed apt, `--require-hashes`, exit non-zero on a required
    component that can't converge);
  - pending Ubuntu security updates installed for the original container build's packages;
  - `convergence.json` (ADR-027) and the daily CI advisory audit;
  - trust-bounded plugins (ADR-025) and the `fable-secure` optional scanner scan-gate.

  Every claim was verified against source at GATE-3. The post-review edit was re-verified at SELF-REVIEW
  (install.go:974-979, quickstart.sh:636-660).
- **No operator-facing `--check`** in the README. The operator's direction on #129: operators shouldn't
  need diagnostic commands; it stays documented for the manager (USING_AGENTFACTORY.md, ADR-027).
- **CHANGELOG** (PR #131 → `eab2b02c`): `## Unreleased` (#124, #117) and the never-tagged
  `## v0.4.0 — 2026-09-17` (#111) are folded into one `## v0.4.0 — 2026-11-01`, security first, plus #127.
  This closes the tag gap that two earlier cycles carried.
- **v0.4.0 release** (Latest, tag → `eab2b02c`):
  https://github.com/stempeck/agentfactory/releases/tag/v0.4.0
  - Themed notes, checked claim by claim against the CHANGELOG.
  - **The first release with binaries:** `agentfactory_v0.4.0_linux_{amd64,arm64}.tar.gz` (`af` +
    `webui`, Go 1.26.10 = the CI pin, `CGO_ENABLED=0`, `-trimpath`, deterministic tar) plus `SHA256SUMS`.
  - Verified end to end from GitHub: `sha256sum -c` passes and `af --version` prints `v0.4.0`.
  - The build machine's path is kept out of the binaries: the Makefile's `main.sourceRoot` ldflag is omitted.
- **Issue #128 filed** (a real gap, open): ship a reference security-scanner integration for
  `fable-secure`'s scan-gate. The copy claims no bundled scanner until it exists.
- Gates PASS: GATE-1, GATE-3, SELF-REVIEW, Step 15 (tests), SELF-VERIFY, PHASE-6.

## Published by the operator (Tier B)

- **Nothing.** Medium = SKIP, LinkedIn = SKIP. The operator: "Skip, close it out. Make sure we have a
  release package published. I'm not posting to social media for now, no one reads it."
- The drafts stay in the tree as unpublished (`cycle-af-a6b105d1-medium.md`, `-linkedin.md`), with the
  operator's EDITED text reconciled. The convergence figure stays as a Standing Asset. The paste vehicle
  was deleted (regenerable).
- The repo homepage is unchanged: no new article exists. It still points at the af-91fe0795 Medium piece.

## Skipped / not done, and why

- **Social posts:** operator decision (above). No ledger row is claimed for either.
- **Homepage update:** this cycle's exit criterion is "if an article shipped". None did.
- **Release CI wait:** the tag was cut before the post-merge main run finished, because main's tree was
  byte-identical to the PR #131 head that had already passed 7/7. The main run then passed 7/7 too.

## Operator-click items (no API reaches these)

- **Channel strategy:** the operator paused social posting "for now". Before the next cycle drafts Tier B,
  decide whether social stays paused, and whether a different off-GitHub venue (Hacker News "Show HN",
  r/ClaudeAI) is worth a draft. Otherwise future cycles should stop at Tier A + release.
- **Release automation (optional):** no release workflow exists. This cycle built the binaries by hand
  with a recorded recipe (agent memory). A tag-triggered workflow would make every release ship binaries
  without an agent. That would be a product change for an implementer, not marketing.
- Profile pins and the social-preview image still have no API. Neither needs a change this cycle.

## Voice calibration learned this cycle

- The operator's #129 review is the new calibration source (runbook Voice, 2026-11-01): lead with what
  the reader can DO; no counter-arguments or gotchas in public copy (limits belong in records and PRs);
  no operator-facing diagnostic commands; make the security implication explicit.
- Drafts that are approved and edited are still not guaranteed to be posted. Treat the operator's
  posting appetite as a gate input, not an assumption.

## Top 3 candidates for next cycle

1. **A reference scanner integration for `fable-secure`** (#128), once it ships: the next visible proof
   of the "most secure way to run multi-agent orchestration" positioning.
2. **The dogfooding story**: an agent that runs its own repo's marketing cycle, this time including
   cutting and packaging the release.
3. **The durable memory vault** (`af memory`), "recover AND remember", never told head-on.

## Recrawl check (~2 weeks, ≈2026-11-15)

- Web search `"Glenn Stempeck" agentfactory` and `agentfactory "v0.4.0"`: expect the repo and the
  release page.
- Web search `agentfactory "most secure" multi-agent`: expect the README security section to start
  surfacing.
- Spot-check that the GitHub topic pages (claude-code, ai-agents, multi-agent-systems, agentic-ai) still
  list the repo. All 4 were API-confirmed this cycle via `/search/repositories?q=topic:<t>+agentfactory`.
- `gh release view v0.4.0 --json assets`: the asset download counts show whether anyone takes the binaries.
