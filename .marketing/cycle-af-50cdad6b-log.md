# Cycle-2 Log — stempeck/agentfactory (bead af-50cdad6b)

Gate and verification evidence for cycle-2. Sections appended in step order.

## GATE-1 — Audit checklist (pre-selection)

Evidence quoted from `cycle-af-50cdad6b-audit.md` and re-run this session.

**1. Is every claim in the STALE list backed by quoted Claim-Verification-Map evidence
(grep output / file:line), not memory?** — **YES.**
- STALE-1 `af version`: `af version` → `Error: unknown command "version" for "af"`;
  `af --version` → `af version v0.1.0-3-gb164332`. (commands re-run this session)
- STALE-2 formula count: `ls internal/cmd/install_formulas/*.toml | wc -l` → `24`
  (README claims "Nineteen"). Missing five named in audit.
- STALE-3 skills count: `internal/cmd/install.go:32` `//go:embed install_skills/*`;
  `ls internal/cmd/install_skills/ | wc -l` → `10` (README lists 3).

**2. Is every feature in the NEW list absent from `announced-ledger.md` (checked, not
assumed)?** — **YES.** `grep -inE "telemetry|multi-provider|litellm|models.json|..."`
against the ledger returns hits ONLY in the header (line 3) and the "Untold backlog"
section (lines 15–18) — never in the announced rows (lines 8–10). telemetry / multi-provider
/ litellm / models.json return zero hits anywhere. NEW list confirmed untold.

**3. Does the snapshot record description, topic count, latest release, and homepage as
they are RIGHT NOW?** — **YES.** From `gh repo view --json
description,repositoryTopics,latestRelease,homepageUrl` this session: description = current
one-liner; topics = 18; latest release = v0.1.0 (2026-07-12); homepage = the Medium article
URL. All recorded in the audit snapshot table.

**4. Were any gh calls skipped due to auth/scope errors?** — **NO.** Authenticated as
`stempeck` (`gh api user -q .login`); every audit gh call (repo view, graphql pinnedItems,
release list) returned data. No auth/scope failure encountered; nothing deferred.

GATE-1 VERDICT: PASS

## GATE-3 — Every public claim verified against source

Checklist run over the full branch diff (`git diff origin/main...HEAD`: README.md,
CHANGELOG.md, and the three cycle-2 `.marketing` artifacts).

**1. Commands/flags/subcommands not proven this session via the Claim Verification Map?**
— **none.** Every command asserted was run or `--help`-verified this session:
- `af --version` → `af version v0.1.0-3-gb164332` (the stale `af version` was removed).
- `af telemetry on|off|status|report|usage` + `--agent`/`--instance`/`--json` → `af telemetry --help`.
- `af improvement on|off [--agent <name>]` → `af improvement --help` (AND-gated).
- `af config models show` (set/check/attest) → `af config models --help`.
- `af install --agents --litellm` → `af install --help` (`--litellm`: "gateway for running agents on OpenAI models").
- `af sling --model` → `af sling --help` (`--model`).
- `af down` factory-wide operator-only → CLAUDE.md CLI section + #92 title "operator-only factory teardown".
- `gpt-fable-review`/`gpt-rootcause-all` "run on OpenAI models via the gateway" → models.json
  maps both agents to the `codex` profile (`ANTHROPIC_BASE_URL: http://localhost:4000`,
  `ANTHROPIC_MODEL: gpt-5.6-terra`, litellm key) — confirmed, not assumed.

**2. Counts not recounted from the filesystem?** — **none.**
- "Twenty-four formulas": `ls internal/cmd/install_formulas/*.toml | wc -l` = 24; README table
  sums to 24 (Impl 4 + Design 7 + Review 4 + Root cause 2 + Multi-provider 2 + Utility 5).
- "Ten skills": `ls -d internal/cmd/install_skills/*/ | wc -l` = 10; README table lists 10.

**3. URLs not fetched/constructed from a verified pattern?** — **none.** The full diff
contains two URLs, both verified live this session:
- `https://github.com/stempeck/agentfactory/issues/93` — the gate-2 issue created this session (live).
- `https://medium.com/@glennstempeck/95-...-b264170eb66c` — the repo's current homepage,
  read from `gh repo view --json homepageUrl` this session.

**4. Unshipped promises ("coming soon") outside a Roadmap section?** — **none.** Every
README/CHANGELOG addition describes shipped, source-verified features; a grep for
future-tense/coming-soon language over the README+CHANGELOG diff returned nothing. The
Roadmap section is unchanged.

GATE-3 VERDICT: PASS

## GATE-3 addendum — web-console claims added in the #94 revamp

Operator direction on #94 asked to surface the web console + telemetry. New README claims and
their source evidence (verified via a code read of the web module this session):
- Floor "lit sign = honest status": web/internal/web/static/app.js FloorViewModel + index.html
  Floor section/filters (All/Working/Gate/Waiting/Attention/Stopped).
- Telemetry view = per-step Duration + per-run token usage (Input/Output/Total) + session
  metrics: web/internal/telemetryview/telemetryview.go (ReportRowDTO.DurationMS; UsageRowDTO
  Input/Output/Total) and app.js render; nav tab index.html "Telemetry".
- Degradation-as-data banner + honest per-step-token refusal ("token counts attributed per run,
  not per step"): app.js renderTelemetryBanner + the Step-timings "Token data" column strings.
- Browser formula authoring, agent detail + operator mail: index.html nav "Formulas"/agent
  section; web/internal/server/server.go routes (/api/formulas, /api/agents/{name}/detail|mail).
Screenshots of the live console (Floor + Telemetry) captured this session confirm the rendered
UI matches these claims. No cost-per-step or per-step-token claim was made (the UI refuses both).

GATE-3 addendum VERDICT: PASS

## SELF-REVIEW

Reviewed `git diff origin/main...HEAD` (4 commits: Tier A docs refresh, cycle artifacts,
web-console revamp, GATE-4 resolution).

Findings and fixes:
- Stale claims reintroduced? None — the three fixes (af --version, 24 formulas, 10 skills) hold;
  no reverted counts.
- Broken links? None — README anchors `#observability` and `#web-console-optional` both resolve
  to existing headers; every relative doc link (docs/*.md, docs/architecture/overview.md,
  web/README.md, CONTRIBUTING.md, LICENSE) exists on disk.
- Repo-host markdown rendering? Tables and fenced blocks balanced; the new Multi-provider table
  row and skills rows render as normal GitHub tables.
- Cruft (debug files, screenshots, scripts, TODOs)? None committed — all diagnostics
  (shot*.js, shot-*.png, gate*-issue.md) live in the gitignored .runtime/ and are absent from
  `git diff --name-only`. The two committed PNGs are intentional article assets.
- Privacy mode (COMMITTED): the diff includes .marketing/ cycle artifacts alongside the doc
  changes, exactly as committed mode requires. The pre-existing agent CLAUDE.md edit and runtime
  files were deliberately excluded via scoped `git add`.
- Manifest pre-check: every .marketing file matches a formula pattern (cycle-*.md,
  cycle-*-diagram.png) or a declared Standing Asset.

SELF-REVIEW VERDICT: PASS

## SELF-VERIFY

Verified cycle outputs against the approved runbook (the standing contract).

- **Tier A — stale claims fixed, no unverified claim entered:** the diff contains all three
  fixes (`af --version`, "Twenty-four formulas", "Ten skills"), and GATE-3 (+ addendum) proved
  every command/flag/count/URL/web-console claim against source. PASS.
- **Tier B — every draft operator-resolved, none self-approved:** Medium `Decision: READY`
  (operator approved as written, #94); LinkedIn `Decision: EDITED` (operator supplied canonical
  text). No draft carries a blank or agent-written decision. PASS.
- **Tier law — zero external posts by the agent:** the only outward calls were GitHub issues
  (#93 story, #94 drafts) and a push to the operator's own repo branch. Nothing was posted to
  Medium, LinkedIn, or any external platform; both drafts remain staged for the operator to
  publish. No PR merged (operator merges). PASS.
- **Voice law — EDITED changes enumerated:** the LinkedIn file lists the mechanics-only changes
  applied to the operator's text (Worflow->Workflow, GitHub URL filled, Medium URL placeholder),
  prior draft preserved as reference, and the new short-form register recorded in the runbook
  Voice section. PASS.
- **Privacy mode — diff matches COMMITTED:** 8 .marketing/ cycle artifacts appear in the diff
  alongside the doc changes, as committed mode requires; the pre-existing agent CLAUDE.md edit
  and runtime files were excluded. PASS.

No deviations.

SELF-VERIFY VERDICT: PASS
