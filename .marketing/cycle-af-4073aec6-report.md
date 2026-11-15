# Cycle af-4073aec6 Report — stempeck/agentfactory

*2026-11-14 to 2026-11-15. Release-only: **v0.5.0**, covering #135. Operator: Glenn Stempeck (`stempeck`).*

## Summary

The operator scoped this cycle to the release: "skip straight to the part where it publishes a release,
no actually medium article or marketing this time". The cycle added a `## v0.5.0` CHANGELOG section for
#135 (PR #137), with every claim checked against source at GATE-3. It then held the release for about
a day because main's `integration` job was red. The cause was a 4m `go test` timeout that #135's tests
outgrew, which this PR did not introduce. The operator filed the blocker as #138 (labelled
`advisory-heal`). The factory's fable chain fixed it in PR #139 (`-timeout=6m`): fable-implement wrote
the fix, fable-increment addressed review threads T1–T5, and it merged as `ef5287e7`. At the operator's
instruction, #137 was updated from main, and the operator merged it (`637fa507`). v0.5.0 was cut
with binaries once main CI on that exact commit passed all seven jobs. The release page is verified clean.

## Shipped autonomously (Tier A)

- **CHANGELOG `## v0.5.0 — 2026-11-14`** (PR #137 → `637fa507`). It covers #135 in six themed sections:
  real context windows, startup measurement and pinned rulings, af's own mail and dispatcher stalls,
  healing advisory PRs, current starter configs, and "Also in this release". Every item was verified
  against source at GATE-3.
- **v0.5.0 release** (Latest, tag → `637fa507`, published 2026-11-15T15:28:32Z):
  https://github.com/stempeck/agentfactory/releases/tag/v0.5.0
  - The notes are the merged CHANGELOG section plus a Downloads section, the same shape as v0.4.0.
  - Binaries: `agentfactory_v0.5.0_linux_{amd64,arm64}.tar.gz` (`af` + `webui` + CHANGELOG + LICENSE;
    Go 1.27.4 = the CI pin, `CGO_ENABLED=0`, `-trimpath`, deterministic tar) and `SHA256SUMS`.
  - Verified from GitHub: `sha256sum -c` passes and `af --version` prints `v0.5.0`.
  - Tagged only after main push run 43729432001 on `637fa507` passed 7/7. The last PR run on that tree
    was cancelled by the workflow's concurrency rule, so no green PR run covered it.
- **Main-CI blocker routed, not worked around.** This PR's `integration` failed with
  `panic: test timed out after 4m0s`, the same failure as main. I withdrew my merge request (a
  STEP_FIDELITY correction: no merge request while CI is red), routed the blocker to the manager and
  supervisor (af-7346a737 → operator issue #138), and posted the first-attempt evidence on #138. That
  evidence was a separate `TestAdvisoryDecay` `py/__pycache__` failure that a longer timeout does not fix.
- Gates PASS: GATE-1, GATE-3, SELF-REVIEW, SELF-VERIFY, PHASE-6. GATE-2 and GATE-4 were resolved from the
  operator's recorded instruction (#136).

## Published by the operator (Tier B)

- **Nothing**, by instruction: no Medium article and no LinkedIn post. No drafts were written, so no
  paste vehicle was made. The publish checklist (#140) recorded the release URL and SKIP for both social
  pieces, and was closed as resolved.
- The repo homepage is unchanged. It still points at the af-91fe0795 Medium article: no new article exists.

## Skipped / not done, and why

- **Social posts:** the operator's instruction for this cycle. It continues the pause from af-a6b105d1
  ("I'm not posting to social media for now, no one reads it").
- **README refresh:** out of scope for a release-only cycle. #135 shows up in the CHANGELOG and the
  release notes but has no README section yet. See the backlog.
- **CHANGELOG date:** the heading reads `2026-11-14`, but the release shipped 2026-11-15 after the CI hold.
  I left it as merged, because fixing it needs a new PR. It's a one-line fix if you want it.

## Operator-click items (no API reaches these)

- **`TestAdvisoryDecay` `py/__pycache__` race** (`advisory_decay_test.go:788`): still unfixed. #139
  fixed only the timeout, and this flake can still turn a docs-only PR red. Evidence is on #138
  (attempt-1 log, job 132331688314). Decide whether to file it for the dispatcher.
- **Channel strategy:** social has been paused for two cycles. Before the next cycle, decide whether
  it stays paused. If it does, future cycles should be scoped to Tier A + release from the start.
- **Release automation (optional, carried):** there is still no tag-triggered release workflow, so
  binaries depend on an agent-run recipe. v0.4.0 has 1 download of each tarball and 0 of `SHA256SUMS`.
- Profile pins and the social-preview image still have no API. Neither needs a change this cycle.

## Top 3 candidates for next cycle

1. **Advisory findings become healing pull requests** (#135). This fits the security positioning set
   on #125. This cycle showed the same fable chain end to end: #138 (`advisory-heal`) → PR #139
   (`af/fable-implement-fd5e3f`, review threads T1–T5 addressed by fable-increment) → merged `ef5287e7` →
   #138 closed. Before telling it, confirm which dispatcher route launched #139: dispatch status
   holds no entry for it. #138 was a CI-timeout
   fix, not an advisory, so tell the mechanism with an honest example.
2. **Agents run on the context window they really get** (#135). It ranked first for pain and reach this
   cycle and is demonstrable in 60 seconds (one `WINDOW` alarm, one refused `af up`).
3. **A reference security-scanner integration for `fable-secure`** (#128, OPEN): tell it when it ships.

## Recrawl check (~2 weeks, ≈2026-11-29)

- Web search `"Glenn Stempeck" agentfactory` and `agentfactory "v0.5.0"`: expect the repo and the v0.5.0
  release page.
- Spot-check that the GitHub topic pages (claude-code, ai-agents, multi-agent-systems, agentic-ai) still
  list the repo. All 4 were API-confirmed on 2026-11-15 via
  `/search/repositories?q=topic:<t>+agentfactory`.
- `gh release view v0.5.0 --json assets`: asset download counts, compared with v0.4.0's (1 / 1 / 0 as of
  2026-11-15).
- The af-a6b105d1 recrawl fell due today. Its API parts are recorded above (topics, v0.4.0 downloads).
  Its web searches are left to the next cycle's audit, together with this cycle's.
