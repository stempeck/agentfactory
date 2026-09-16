---
name: perfeval-agent
description: Evaluate any agentfactory agent's runtime performance from telemetry and session transcripts, walking time down through child agents to the true consumer, and produce ONE evidence-backed highest-impact optimization with verbatim reproduction queries. Use when the user says "/perfeval-agent <agent>", "evaluate performance of <agent>", "why is <agent> slow", "where does the time go", or "find the slow steps". Ends by offering /github-issue for the finding — it never auto-files.
---

# Evaluate Agent Performance

Data-first performance evaluation of one agentfactory agent. The deliverable is a
**Verdict**: the single highest-impact optimization target, the mechanism behind it,
the evidence, and the exact queries that re-derive the outcome — so the operator can
decide whether to run `/github-issue` on it.

**You run this yourself.** Every data surface (telemetry, formula TOML, session
transcripts) is directly queryable. Do not dispatch a specialist to "investigate" and
do not hand-wave toward `/improve-agent` — the outcome of this skill is a *specific,
measured target*, not a referral.

**Argument:** the agent name (e.g. `/perfeval-agent design-plan-impl`). Export it:
`AGENT=<name>`. Scratch files go in YOUR agent directory, never `/tmp` (containment);
delete them when done.

Copy this checklist and track progress:

```
perfeval progress:
- [ ] Phase 0: Preconditions (telemetry on; agent + formula name resolved)
- [ ] Phase 1: Step-level profile (median, INTERRUPTED excluded)
- [ ] Phase 2: Walk time down to the true consumer (reconciliation stated)
- [ ] Phase 3: Read the top step's formula definition
- [ ] Phase 4: Transcript gap attribution on the 2 slowest instances
- [ ] Phase 5: Confirm at scale (all instances; split, r, coverage)
- [ ] Phase 6: Verdict emitted; /github-issue offered, not run
```

## Hard rules (each one earned by a real failure)

1. **Never interpret a telemetry status by its name.** Statuses are labels written by
   Go code; read the emitting code before building any claim on one, and quote the
   emitting `file:line` in the Verdict's STATUS SEMANTICS field. Known traps:
   - `INTERRUPTED` rows are crash artifacts whose durations are bogus (hundreds of
     hours — session died, close recorded much later). Exclude them from all stats.
   - `gate-waiting` is NOT queue idle. See `StatusGateWaiting` in
     `internal/telemetry/event.go` and its write site in `internal/cmd/done.go`:
     a step closed via `af done --phase-complete` records its normal span with this
     label instead of `closed`. It is the step's whole duration, not extra wait.
   Publishing an interpretation before reading the code produced a wrong "idle queue"
   claim that had to be retracted. If you catch yourself doing it, retract explicitly.
2. **Median, not mean.** Outliers are operational incidents (stalls, credit limits,
   auth blocks), not typical cost. Report them separately as incidents.
3. **Machine-readable from the start.** Use `--json` + `jq`. Eyeballing truncated
   human tables wastes turns and misses rows.
4. **Report coverage honestly.** Transcripts rotate mid-step and old worktrees get
   purged — some instances will be unprofilable. Say how many. Never present a
   partial population as the whole.
5. **Do not try to read extended-thinking content.** It is persisted zero-length
   (timing and count only). The method below recovers what deliberation was *about*
   without the text.

## Phase 0 — Preconditions

```bash
af telemetry status        # expect: telemetry on, endpoints reachable
ROOT=$(git rev-parse --show-toplevel)
FORMULA=$(af agents list --json | jq -r --arg a "$AGENT" '.[] | select(.name==$a) | .formula')
echo "agent=$AGENT formula=$FORMULA root=$ROOT"
```

If telemetry is off there is no data; stop and tell the operator. If `$FORMULA` is
empty the agent has no formula (nothing step-level to profile); stop and say so.
`$FORMULA` names the TOML file in later phases — it is not always equal to `$AGENT`.

## Phase 1 — Step-level profile

```bash
af telemetry report --agent $AGENT --json > profile.json
jq -r '.rows | map(select(.status=="closed")) | group_by(.step)
  | map({step: .[0].step, runs: length,
         median_min: (map(.duration_ms)|sort|.[length/2|floor]/60000*10|round/10),
         min_min: (map(.duration_ms)|min/60000*10|round/10),
         max_min: (map(.duration_ms)|max/60000*10|round/10)})
  | sort_by(-.median_min)' profile.json
# Non-closed rows are excluded artifacts and open steps — list them separately:
jq -r '.rows | map(select(.status!="closed")) | .[] | [.status, .step, (.duration_ms/60000|round), .started] | @tsv' profile.json
```

Output of this phase: a ranked table (median / range / runs) and a bucket summary —
which fraction of a median run is which kind of work.

## Phase 2 — Walk the time down to the true consumer

Orchestrators mostly wait. If the top steps are await/dispatch-shaped (their titles
say "Await…", "Dispatch <other agent>…"), the time lives in a **child agent**:

```bash
af agents list --json | jq --arg a "$AGENT" '.[] | select(.name==$a) | .inputs'   # child names often appear here
grep -n "sling --agent" "$ROOT/internal/cmd/install_formulas/$FORMULA.formula.toml"
```

Re-run Phase 1 on each child that backs a dominant await step, one child at a time,
deepest-first. Recurse until the step whose duration is the agent's *own work* is
found. State the reconciliation (child step-sum ≈ parent wait) explicitly — it goes
in the Verdict's OWNERSHIP field. No reconciliation, no verdict.

## Phase 3 — Understand the top step before profiling it

Read the step's definition in the formula source before interpreting any timing:

```bash
grep -n -A60 "<step title fragment>" "$ROOT/internal/cmd/install_formulas/<owning-formula>.formula.toml"
```

Know what the step *asks for* (how many discrete actions, what judgment it demands,
what its exit criteria are). Note any per-check history comments — checks that were
added one-per-incident earn their runtime; the target is never "verify less."

## Phase 4 — Transcript gap attribution (the core method)

Pick the slowest 2 instances of the top step from the per-instance rows:

```bash
jq -r '.rows | map(select(.step|contains("<fragment>")))
  | .[] | [.instance_id, .status, .started, (.duration_ms/1000|round)] | @tsv' profile.json
```

Locate sessions covering each window under
`/home/dev/.claude/projects/*<owning-agent>*/<session>.jsonl` (exclude `subagents/`
for the main line; a window can span 2+ files because sessions rotate on handoff —
index candidate files by first/last timestamp, then select files overlapping the
window plus ~90s slack). Then attribute time:

```bash
jq -r --arg s "$START" --arg e "$END" '
  select(.timestamp != null) | select(.timestamp >= $s and .timestamp <= $e)
  | [(.timestamp|sub("\\.[0-9]+Z$";"Z")|fromdateiso8601),
     (if .type=="assistant" then ((.message.content[0].type) // "other")
      elif .type=="user" then "result" else "meta" end)] | @tsv' SESSION.jsonl \
| sort -n | awk -F'\t' 'NR>1{gap=$1-prev; if(gap>=0 && gap<600) sum[$2]+=gap} {prev=$1}
  END{for(c in sum) print c, sum[c]}'
# gap<600 discards session-rotation / dead-air jumps between files so one boundary
# cannot masquerade as ten minutes of work.
```

**Attribution rule:** a gap belongs to the LATER entry. Gaps before
`thinking`/`tool_use`/`text` are model time; gaps before `result` are command
execution time.

**Pause-context pairing** (recovers what zero-length thinking was about): for every
gap ≥ ~55s, print the entry before (what the model had just seen) and after (what it
did next). The "before" content names the deliberation topic.

## Phase 5 — Confirm at scale

A 2-run profile is a hypothesis. Loop the Phase 4 attribution over **every** instance
window that still has transcripts, then compute:

- category split of total attributed time (thinking / tool_use / text / result)
- blocks-per-run distribution (min / median / max)
- Pearson r between step duration and the dominant category's seconds
- coverage: instances profiled vs instances existing

The claim survives only if the dominant category holds across the population and r is
strong. If instances disagree, return to Phase 4 on the divergent instances and find
out why (some workloads are heavier — that nuance belongs in the verdict); do not
emit a verdict the population contradicts.

## Phase 6 — The Verdict (output contract)

Deliver exactly one finding, with every field present:

```
VERDICT: <agent>'s <step> is <X>-bound, not <Y>-bound.
MECHANISM: <one falsifiable sentence — what structurally causes the cost>
OWNERSHIP: <proof the profiled step is the agent's own work: the Phase 2
  reconciliation (child step-sum ≈ parent wait), or "no child agents involved">
STATUS SEMANTICS: <file:line of the emitting code for every telemetry status this
  verdict relied on>
EVIDENCE: <population size, category split %, r, the two exemplar runs, coverage
  N-profiled / N-existing>
INCIDENTS (separate from the profile): <outlier rows and their operational causes>
PROJECTED LEVER: <the single highest-impact target and the ceiling of the saving —
  stated as a diagnosis precise enough to design against, without prescribing the fix>
REPRODUCTION: <the Phase 1/4/5 queries, verbatim, parameterized>
```

Then stop and offer: run `/github-issue` to file it. **Never auto-file.** If the
operator says go, hand `/github-issue` the verdict plus reproduction queries and let
that skill's cartographer rules govern the write-up (diagnosis in, prescription out;
any behavior-preservation requirements go in acceptance criteria as existing behavior,
not as invented scope constraints).

## Anti-patterns (observed, not hypothetical)

| Anti-pattern | What to do instead |
|---|---|
| Dispatching a specialist to "investigate performance" | Run the queries yourself — every surface is directly queryable |
| Publishing a status interpretation before reading the emitting code | Rule 1; quote file:line in STATUS SEMANTICS; retract explicitly if violated |
| Profiling only the orchestrator | Phase 2 — walk waits down to children; OWNERSHIP field forces the reconciliation |
| Trying to read thinking text | Zero-length by design; use pause-context pairing |
| Mean-based stats, INTERRUPTED rows included | Median; exclude and report artifacts separately |
| "Enable /improve-agent" or "add more telemetry" as the outcome | The outcome is one measured, falsifiable target |
| Presenting partial transcript coverage as the population | State N-profiled / N-existing every time |
