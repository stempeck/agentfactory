---
name: perfeval-agent
description: Measures where one agentfactory agent's wall clock and tokens go (per formula step, child agent, in-step sub-agent, wake and fidelity-gate evaluation), ranks the cost centres, and names the one lever with the largest measured saving, its controller, and the query that proves it after a change. On request it applies a formula-controlled lever under the formula-edit rules. Use when the user says "/perfeval-agent <agent>", "why is <agent> slow", "where do the tokens go", "make <formula> faster or cheaper", or "find the slow steps". It never auto-files an issue.
---

# Evaluate Agent Performance

The deliverable is a **Verdict**: the cost centre with the largest measured saving, the mechanism
behind it, who controls it (formula, factory config or Go code), the script output every number
comes from, and the query that proves or disproves a change. Run every measurement yourself.

**Argument:** an agent name, or a formula file name (then use the agent whose `.formula` in
`af agents list --json` matches it).

## Setup

Run every command inside the factory environment (the container or host the agent runs in).
From another host, run each command through that environment's shell
(`docker exec -u <user> <container> bash -c 'cd <ROOT> && …'`). Never run `af` on a machine that
is not the factory, and never run `claude` inside the factory environment: a launch there can
purge the old session transcripts this skill measures.

The factory root is the bootstrapped project at `~/af/<project>`; every agent runs there and
every measurement comes from there. `~/projects/<agentfactory>` is the agentfactory source that
installs af and generates agents: it is not a factory root, so never measure, read config or
edit there, even though it has an `.agentfactory/` directory of its own.

```bash
AF=$(command -v af || echo "$HOME/.local/bin/af")
AGENT=<name>
for f in "$HOME"/af/*/.agentfactory/telemetry/steps/"$AGENT".jsonl; do      # several projects: most records wins
  [ -f "$f" ] && echo "$(wc -l < "$f" | tr -d ' ') ${f%/.agentfactory/*}"; done | sort -rn
ROOT=<the first directory listed>; cd "$ROOT" && $AF root          # must print ROOT; none listed: stop
W=$(mktemp -d "$HOME/.cache/perfeval.XXXXXX")     # the only directory this skill writes to or deletes
# copy <this skill's directory>/scripts/perfeval.py to $W (from macOS into a container:
#   COPYFILE_DISABLE=1 tar --no-xattrs -cf - -C <skill dir>/scripts perfeval.py | docker exec -i -u <user> <container> tar -C <W> -xf -)
for py in /usr/bin/python3 python3; do command -v "$py" >/dev/null && "$py" "$W/perfeval.py" selftest | tail -n 1; done
P="python3 $W/perfeval.py"                        # every selftest line must say "all checks passed"; if not, stop
STEPS=$ROOT/.agentfactory/telemetry/steps/$AGENT.jsonl
FORMULA=$($AF agents list --json | jq -r --arg a "$AGENT" '.[] | select(.name==$a) | .formula')
STORE=.agentfactory/store/formulas/$FORMULA.formula.toml          # the running copy, relative to ROOT
$AF telemetry status
$AF telemetry report --agent "$AGENT" --json > "$W/report.json"
C="--report $W/report.json --agent $AGENT --steps-log $STEPS --transcripts $HOME/.claude/projects"
# add --transcripts <dir> for each transcript backup root; a session in several roots counts once
```

Stop and say so if telemetry is off, the report has no rows, or `$FORMULA` is empty. At the end,
`rm -rf "$W"` and nothing else.

Copy this checklist; tick an item only when its output is in your notes:

```
perfeval progress:
- [ ] 0 Setup: selftest passed; telemetry on; FORMULA resolved
- [ ] 1 steps + runs: step profile, every run's identity, the text each run ran, today's text
- [ ] 2 Ownership: sling grep on both texts; children for each child agent
- [ ] 3 tokens: the ranked cost-centre table
- [ ] 4 The runs' text and today's text read for the top centres; their diff checked
- [ ] 5 time on the top wall-clock step: main, --subagents, pauses of the 2 slowest covered runs
- [ ] 6 tokens --detail (and --held-file) if sub-agent share >= 20%
- [ ] 7 gate if the gate-cost share >= 5%
- [ ] 8 runs --values if an artifact's size may explain a change
- [ ] 9 Verdict with every field; offer apply and /github-issue; stop
- [ ] 10 Apply, only when asked; end with the AFTER plan
```

## Hard rules

1. **Every measured number comes from `perfeval.py` or `af telemetry` output pasted into
   LEDGER.** When a question needs a measurement neither provides, write the script in `$W`
   (never anywhere else, `/tmp` included), quote it in REPRODUCTION, and cross-check one of its
   totals against a `perfeval.py` figure before using it. The only arithmetic in the Verdict is
   PROJECTED SAVING's, written out on pasted figures (`rank-2 row per-run 1.72M × 0.5 = 0.86M`).
2. **Rank before you name.** The VERDICT's centre is a row of `tokens`' ranked table (or `steps`'
   table for wall clock), quoted with its rank. Call it largest or dominant only if it is rank 1.
   Call a centre context-bound only if the table's dominant part is cache_read, output-bound only
   if it is output; "mixed" means neither. Call a step wait-, model- or tool-bound only if `time`
   names that category dominant (>= 50%).
3. **List every lever, then choose by expected saving.** LEVERS has one line per candidate:
   every mechanism found inside the rank-1 centre, each other ranked row >= 10%, and the gate
   lever when the gate cost is >= 5%. Each line gives the rows it removes × the fraction removed =
   the saving per run that has it, the base rate (runs that have it / runs, from `runs` or the
   ledger), the expected saving per run (saving × base rate), the controller, and whether today's
   text already changes it. The VERDICT's lever is the line with the largest expected saving; the
   runner-up is named with its figure. When two lines remove the same cost, keep the one that
   keeps every check today's text requires. No rows, no saving.
4. **Runtime claims cite code or a count**: the greps in "Run model" below, or a count a command
   printed. Reading the formula shows what the agent was told, not what it did.
5. **A correlation or a change between texts is a lead.** Before it reaches the Verdict, check the
   confounds on runs either side: text, model, af build and host version (`runs`), and workload
   size. Say what you checked.
6. **Median, not mean; state coverage** (N profiled / N existing) for every population figure.
   When coverage is under half, compare the covered runs' medians with `steps`' population
   medians and say in the VERDICT line whether they are typical.
7. **The harness always replies.** Every wake produces a response turn; a lever removes wakes or
   makes the response short and inside the contract the grader sees, never "do not reply".
8. **Thinking text is persisted empty**; use `time --pairs` to see what preceded long pauses.
9. **If a rule was broken, retract it in the same response** with the corrected figure.

## Phases

**1. Profile and identity.**

```bash
$P steps --report "$W/report.json" --steps-log "$STEPS"      # a renamed step stays one row (step id)
$P runs --report "$W/report.json" --steps-log "$STEPS" --step "<top step fragment>" --repo "$ROOT" --formula "$STORE"
# when the project is agentfactory itself, also --formula internal/cmd/install_formulas/$FORMULA.formula.toml
```

`steps` ranks steps by median span with each step's share of a median run and the run holding
its maximum, and lists excluded rows (INTERRUPTED) and rows with no start (the improvement hook,
not part of the median run). `runs` lists every run from telemetry alone (model, formula digest,
af build, host version, sub-agent launches, gate flags), groups runs by the commit whose file
matches their digest ("uncommitted" means a store edit), states whether today's text has run, and
lists the commits since the last run. If the project's git does not track the store copy, it says
so: older runs' text can then only be compared by digest, not read.

**2. Ownership.** Wait-shaped steps (Dispatch, Await) mean a child agent owns the time.

```bash
grep -n "sling --agent" "$ROOT/$STORE"                                              # today's text
git -C "$ROOT" show <commit the runs ran>:<its path> | grep -n "sling --agent"        # the runs' text
$AF telemetry report --agent <child> --json > "$W/<child>.json"
$P children --report "$W/report.json" --child "<child>=$W/<child>.json"
```

Each child run is tied to the parent step it ended in, the step that waited for it. A step whose
median matches the child's span with an end delta near zero is the child's run: start over with
`AGENT=<child>`. No sling lines means no children; quote the empty grep in OWNERSHIP. In-step
Agent-tool sub-agents are not children; they are measured in phases 3 and 6.

**3. Tokens.** `$P tokens $C`. The ranked table's rows (main-session steps, the tail after the
last step, the sub-agents of each step, graders) sum to TOTAL, each with its share, per-run IE,
dominant part and controller. Sub-agents are ranked by the step they ran in because their
descriptions are free text; the role table below the ranking splits a step's sub-agents. Below it: the gate cost (graders plus turns started by gate mail), main session
by step, turns by the prompt that started them (a turn's IE is all its work until the next
prompt), step × prompt, and sub-agent roles with their IE split. If coverage is under half, add
backup roots to `$C`.

**4. Read both texts.** The text the runs ran (phase 1 names its commit) and today's text, which
phase 10 would edit:

```bash
git -C "$ROOT" show <commit>:<path> > "$W/ran.toml"
git -C "$ROOT" diff --no-index "$W/ran.toml" "$ROOT/$STORE"
```

If today's text has never run, a mechanism the diff changed is not measured on today's text; its
saving is re-derived against today's text or reported as unknown in TEXT. For each top centre,
note what the text asks for: actions, sub-agents and what each writes, the turns it causes,
exit criteria. Checks earn their runtime: the lever is never "verify less".

**5. Time.**

```bash
$P time $C --step "<fragment>"                    # main session; also states time no transcript covers
$P time $C --step "<fragment>" --subagents        # agent-minutes: parallel sub-agents summed
$P time $C --step "<fragment>" --instances <the 2 slowest covered runs> --pairs
```

The gap before an entry is charged to it: model time before thinking/text/tool_use, command
execution before a tool result, waiting before a prompt or queued-prompt record, hook execution
before a hook attachment (the fidelity grader runs in the Stop hook). If runs disagree, look at
the divergent ones before emitting anything.

**6. Sub-agents.** `$P tokens $C --detail` lists every sub-agent (step, start and end clock time,
minutes, calls, output, write share, peak context, IE), which shows which ran in parallel and
which waited for another, and the files they read, largest first; "tool output" entries are
command output the harness saved to a file, not inputs the formula supplies. Then
`$P tokens $C --held-file <largest file read that is not a tool output>` prices keeping that
file in context.

**7. Gate.** `$P gate --agent "$AGENT" --transcripts <same roots>` prints evaluations and flags per
step (truncated = the grader saw only the first 4096 bytes), clusters flags by the directive the
grader quoted, and counts wakes from gate mail versus echoes of the agent's own replies. Add
`--phrase "<sentence the flagged turns relied on>"` to see whether that sentence was inside the
text each grader saw, and `--formula <file>` (today's `$ROOT/$STORE`, or the runs' `$W/ran.toml`)
to see at which byte of each step that sentence sits in a text no grader has seen yet. For a cluster of in-contract turns, the lever is step text inside the window;
its saving is the cluster's flags × (grader IE ÷ grader sessions + gate-mail turn IE ÷ gate-mail
wakes). Clusters can name one instruction in two wordings; merge them only after reading the text.

**8. Artifact size.** Add `--values <TSV: ISO_TIME<TAB>VALUE>` to `runs`; each value lands on the
run whose window holds it, others are dropped. Build it with `wc -c` per run's artifact, or from
git when worktrees are gone: `git -C "$ROOT" log --all --format='%H %cI' -- '*/<name>'`, then
`git -C "$ROOT" cat-file -s <commit>:<path>`.

## The Verdict (phase 9)

Deliver exactly one finding with every field:

```
VERDICT: <agent>'s <centre, quoted from the ranked table with its rank> is <X>-bound, not <Y>-bound.
TEXT: <digest/commit measured; today's digest and its run count; whether their diff touches the mechanism, quoted>
MECHANISM: <one falsifiable sentence: what structurally causes the cost>
CONTROLLER: <formula | factory config | Go code>; on the critical path (before WORK_DONE)? yes/no
OWNERSHIP: <children table, or the empty sling grep>
STATUS SEMANTICS: <the code behind every telemetry status relied on>
LEDGER: <the pasted rows the verdict and saving rest on, the ranked table's top 5; coverage>
LEVERS: <one line per candidate: rows × fraction = saving per run that has it; base rate; expected per run; controller; changed in today's text?>
EVIDENCE: <dominant category and share, r, two exemplar runs, the confounds checked>
PROJECTED SAVING: <the chosen LEVERS line restated: a ceiling; against today's text, or "unknown" and why>
INCIDENTS: <outlier rows and their causes, apart from the profile>
OTHER CENTRES: <every other ranked row >= 10%, with controller; reported, not acted on>
AFTER: <the phase 10 commands and how both arms will be produced>
REPRODUCTION: <every command above, verbatim>
```

Then stop and offer: apply the lever (only if CONTROLLER is formula), or `/github-issue` with the
Verdict. Never apply or file without the operator saying so.

## Applying a lever (phase 10, only when asked)

- **Edit in the factory root only.** The running copy is `$ROOT/$STORE`. When the project is
  agentfactory itself it also holds the source, `internal/cmd/install_formulas/<f>.formula.toml`:
  `diff` the two first and stop on a difference (the store may hold a self-edit); otherwise edit
  the source, run `make sync-formulas` in `$ROOT`, and `diff` again. In any other project, edit
  the store copy and report that promotion into the agentfactory source and `af install --agents`
  are the operator's (USING_AGENTFACTORY.md); never edit `~/projects/<agentfactory>`.
- **Validate the edit directly, not with Go tests**: the template-parity tests turn red on any
  description or title change until the operator regenerates the agent. TOML:
  `python3 -c 'import sys, tomllib; tomllib.load(open(sys.argv[1], "rb"))' <file>` (Python 3.11+)
  or `$AF formula show <f> --json` in the factory. Run every changed shell block under `bash`
  against a fixture; awk must work in mawk and BSD awk (no `{m,n}`); commands must exist on Linux
  and vanilla macOS.
- **Instructions only.** No dates, run or issue ids, durations, measured figures or "(Why: …)"
  notes, in prose or TOML comments: step text is in context on every turn of every run. State the
  bytes added and removed (`wc -c`).
- **No stale text.** Update the description, titles, anti-pattern tables, exit criteria,
  deliverable lists and later steps the change contradicts. Name `TestFormulaTemplateDrift` and
  `TestDeployedAgentIdentityMatchesEmbeddedTemplate` as red until agent-gen. Never edit role
  templates, an agent's `CLAUDE.md` or agent-gen output.
- **The grader's window.** Describe every turn the step causes (launch, waits, later Agent calls,
  gate-mail handling) inside the first 4096 bytes of its description.
- **Never verify less**: a lever may move, pipeline or shard a check, never drop or narrow it.
  Every sub-agent a step launches finishes before that step's `af done`.
- **Scope.** Apply every measured formula lever or decline it with a ledger row. Centres controlled
  by config or code go in OTHER CENTRES, unchanged. Never commit.
- **AFTER plan.** Both arms are fresh runs, five on the text without the change and five with it,
  under the same af build, host and checkout (`compare` voids anything else):

```bash
$AF telemetry compare --formula "$FORMULA" --surface a --before <5 ids> --after <5 ids> --json
$P steps --report "$W/report.json" --steps-log "$STEPS" --instances <5 before ids>   # and the 5 after ids
$P tokens $C --instances <5 after ids>                            # the removed rows must be gone
$P gate --agent "$AGENT" --report "$W/report.json" --steps-log "$STEPS" --transcripts <roots> --instances <ids>
```

`compare` judges output plus sub-agent tokens over closed steps and passes only when
median(after) < min(before); wall clock is judged with the two `steps` arms by the same bar. A
cost present in some runs only is judged by its presence per run. Nothing is measured until both
arms exist.

## Run model

Line numbers drift; re-run a grep before citing it.

| Fact | Where it is decided |
|---|---|
| The factory root is `~/af/<project>`; `~/projects/<agentfactory>` only installs af and generates agents | USING_AGENTFACTORY.md, "Directory layout" and "Batch regeneration" |
| `closed` and `gate-waiting` are the same full span (gate-waiting = `af done --phase-complete`) | `grep -n StatusGateWaiting internal/cmd/done.go`; span: `grep -n "func telemetryStepSpan" internal/cmd/*.go` |
| `INTERRUPTED` is a report-time label for an open step whose session was recycled | `grep -n "statusInterrupted =" internal/cmd/telemetry.go` |
| Every run records its formula digest, model, af build; step closes record host version, sub-agent launches, gate flags | `grep -n "FormulaDigest string\|HostVersion  *string" internal/telemetry/event.go` |
| The Stop hook runs the fidelity gate at every turn end | `grep -n fidelity-gate.sh internal/claude/config/settings-autonomous.json` |
| The gate grades only turns whose step is ready; it logs each exit in the worktree's `.runtime/fidelity_debug.log` | `grep -n 'EXIT[0-9]*:\|AGENT_RUNTIME=' hooks/fidelity-gate.sh` |
| The grader is haiku and sees the step title, the first 4096 bytes of the description, the response and the turn's tools | `grep -n "head -c 4096\|model haiku" hooks/fidelity-gate.sh` |
| A flag mails STEP_FIDELITY to the agent itself (a wake); the threshold escalates to the supervisor once per step | `grep -n "STEP_FIDELITY\|ESCALATION_THRESHOLD" hooks/fidelity-gate.sh` |
| The watchdog nudges a pane unchanged for `--silence-timeout` | `grep -n "doing your job\|silence-timeout" internal/cmd/watchdog.go` |
| WORK_DONE goes to the dispatcher before the improvement hook fires: the tail is off the critical path | `grep -n "sendWorkDoneMail(\|Continuous-improvement hook" internal/cmd/done.go` |
| A boundary handoff respawns the pane, so sub-agents do not survive a step boundary | `grep -n "boundaryHandoffExec = func\|return respawnSession" internal/cmd/done.go` |
| `compare` metric and pass rule; it voids arms whose build, host, checkout or digest differ | `grep -n "m.Metric =" internal/telemetry/protocol.go`; `grep -n "func ProtocolCompare" internal/tokenomics/protocol.go`; `grep -n 'compareCheck.*= "' internal/cmd/telemetry_compare.go` |

IE (input equivalent) = input + 0.1 × cache_read + 1.25 × cache_create + 5 × output, each response
counted once at its final usage. Transcripts: `~/.claude/projects/<dir>/<session>.jsonl`, `<dir>`
ending `<worktree>--agentfactory-agents-<agent>`; sub-agents under `<session>/subagents/`; grader
sessions are those whose first prompt starts "Current step:".

## Anti-patterns

| Anti-pattern | Instead |
|---|---|
| Naming a centre "dominant" or "the largest" without its rank | Rule 2: quote the ranked table |
| "Context-bound", "output-bound" or "wait-bound" from judgement | Only when the table or `time` prints that category dominant |
| A saving with no rows, or on a centre the lever does not touch | Rule 3 |
| Choosing a lever without the others' expected savings beside it | Rule 3: the LEVERS field |
| Proposing a lever measured on text that today's text already changed | Phase 4 diff; the TEXT field |
| Claiming a wake or a reply loop, or "the agent should not reply" | Count it with `tokens` or `gate`; Rule 7 |
| Counting the after-last-step tail as the formula's cost | It runs after WORK_DONE; its controller is config or the late mail's sender |
| Writing or deleting anything outside `$W`, `/tmp` included | `$W` is the only directory this skill creates; ad-hoc scripts live there |
| Measuring or editing in `~/projects/<agentfactory>` | The factory root is `~/af/<project>` |
