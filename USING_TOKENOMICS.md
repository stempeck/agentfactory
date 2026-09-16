# Using Agentfactory: Token Economics

Operator guide for the factory's token economics: what a run costs its own context window, the
policy surface that may act on that accounting, and the `af tokenomics` behavior contract — its
guarantees, the closed vocabulary of interventions, and the per-mechanism record each one leaves.
Split out of [USING_AGENTFACTORY.md](USING_AGENTFACTORY.md).

## Token economics

The factory's token economics is the accounting of what a run costs its own context window, plus a
policy surface that may act on that accounting. It serves **two objectives**, and every intervention
record carries an `objective` field naming which of them fired. The vocabulary is closed at two.

`capacity` is window-driven, and it exists for constrained model profiles: a formula that fits
comfortably in a 1M-token cloud window will thrash a local backend with a 262,144-token pool, and
thrashing shows up as re-prefill, mid-step compaction and lost work rather than as an error anyone
sees. That declared backend pool is the `AF_BACKEND_POOL_TOKENS` key on a profile in
`.agentfactory/models.json`; the sub-agent-dispatch gate reads only that key for a backend's pool,
never the per-request context window. Where no capacity fact is declared — which is every cloud
profile — the capacity arm is structurally inert.

`efficiency` is baseline-driven, and it applies on *every* profile, roomy ones included. It asks a
different question: not whether the next step will fit, but what that step has historically
**generated** — output tokens, exact thinking tokens, sub-agent tokens, and how often it re-read a
file it had already read. A step that fits its window with room to spare can still cost twice what
it needs to, and no occupancy reading can see that. Guarantee 7 is why a roomy window cannot switch
this arm off.

It is a closed loop — **observe** what a run held and what it generated, **evaluate** a pure
arithmetic policy, **act** through a fixed vocabulary of mechanisms, **record** every firing as an
auditable intervention. This section is the contract for the third and fourth steps.
`af tokenomics --help` and `af tokenomics status` both point here.

```bash
af tokenomics status              # Policy table, resolved window, arithmetic inputs, why it is inert
af tokenomics status --json       # The same, machine-readable (always exits 0 — branch on .state)
af tokenomics on                  # Never gated: oversight fails toward being on
af tokenomics off                 # Operator-only, from a host shell
af telemetry report --json        # Per-step occupancy AND what each step generated
af telemetry band                 # Observed figures against learned medians, with the band stated
af turn interventions --since <ts> --agent <name>   # What the harness did to one agent this turn
af telemetry compare --formula <f> --surface a|b --before <ids> --after <ids>   # Did a change help?
```

### What the harness guarantees

1. **No step is ever blocked; one launch may be, by one enumerated gate.** No mechanism refuses a
   *step*, fails a hook, or holds a session, and every hook in the loop still exits 0 (ADR-007) — the
   advisory mechanisms can only counsel, hand off at a boundary, or start the *next* session at a
   reduced effort level. The single enumerated exception (ADR-007 amendment, 2026-08-31) is the
   pre-act sub-agent-dispatch capacity gate: it may deny *a new sub-agent launch* — never a step,
   never any other tool call — through the PreToolUse permission-decision channel, not a non-zero
   exit. It refuses by arithmetic over an operator-declared backend pool — including a
   child-footprint floor (`AF_BACKEND_CHILD_FLOOR_TOKENS`, default 50k) that guards the first child
   near the ceiling and counsels `af handoff` when the launcher's own session alone leaves no room —
   or, where the profile sets `AF_DISABLE_PARALLEL_SUBAGENTS`, by a hard semaphore of one that
   refuses a second sub-agent while a sibling still runs. It fails open on any resolution error, and
   is structurally inert where no capacity fact is declared (every cloud profile).
2. **No tokenomics mechanism interrupts work in flight.** When one of these mechanisms recycles a
   session it does so only at a step or formula boundary, after the closing record is written and
   before the next step opens, so there is no partial work to lose. This is a claim about *this*
   loop and not about the whole harness: the context-exhaustion ladder does recycle mid-step — under
   its own `context_exhaustion` trigger, which the two respawn-attribution classes below deliberately
   exclude — and `af prime`'s economics block tells an agent to expect exactly that when a handoff
   would not help.
3. **Every firing leaves a record, or the contract says it leaves none.** A mechanism that acted and
   left nothing behind is indistinguishable from one that never ran; the table below names the record
   for each mechanism, and says "none today" where there is none.
4. **No verdict is ever stored.** Records carry figures; every judgement — over-occupancy,
   over-consumption, within-baselines — is recomputed at read time from those figures alone. A
   figure cannot lie and a verdict computed under one version of the rules can.
5. **The umbrella is a conjunction and either half silences everything.** `af tokenomics off` (the
   `.agentfactory/.tokenomics` toggle) and `tokenomics.enabled: "off"` in `startup.json` each veto
   the whole surface, including a mechanism an operator switched on explicitly. Every toggle write is
   appended to `.agentfactory/.tokenomics.log` as one `ts actor source state` line.
6. **Off is the default.** A factory that has never run `af tokenomics on` runs none of this, and its
   per-verb cost is one gate-file read.
7. **Efficiency actuators read no window operand.** The predicate the efficiency arm consults takes
   an aggregate of what a step has historically generated, whether that aggregate was found, and the
   resolved policy — and nothing else. It is handed no context window, no live occupancy, no declared
   pool, and it has no way to ask for one. That *absence* is the guarantee, not an omission from it:
   a roomy profile cannot silence this arm, because the function never learns how much room there
   is. Every mechanism the capacity arm owns goes quiet on a cloud profile by design; the efficiency
   arm is the half that does not, and it stays that way only for as long as the operand list does.
8. **No efficiency intervention edits a formula.** The efficiency mechanisms counsel a session,
   recycle it at a boundary and start the next one at a lower effort level — they change how a step
   runs, never what the step *is*. Nothing under this umbrella writes a formula. The one loop that
   does change a formula is the continuous-improvement loop under its own umbrella, where the edit is
   made by the agent under the improvement hook's static instruction and closed by
   `af improvement complete`, which validates it and mails the verdict. Where the two umbrellas meet,
   tokenomics contributes exactly one sentence of preference to that instruction and no number — it
   can ask the improvement loop to favour edits that remove re-reads, and it cannot make one.

### The permitted actions

The design names four advisory interventions — defer the step, serialize a fan-out, recycle the
session at a boundary, inject guidance — and they map onto three recorded actions. The ADR-007
amendment (2026-08-31) then enumerated a fourth thing the harness may do: refuse a sub-agent launch.
The vocabulary of things it may *do* is therefore deliberately **closed at four**, because there are
exactly four things this harness can do — tell a session something, end its turn, change how hard it
thinks, or deny it a launch. A fifth `Action` constant, `observe`, records the opposite of an act.
The mapping is the contract:

| Intervention (design language) | Recorded action | What actually happens |
|---|---|---|
| `defer` the step | `advise` | The agent is told, at prime, that the step's learned appetite does not fit the free window. It decides — nothing holds the step. |
| `serialize` a fan-out | `advise` | Either advisory text at prime, or one urgent self-addressed `TOKENOMICS_DISPATCH` bead relaying a refusal the `af dispatch-admit` gate has already recorded, counselling that sub-agents go out one at a time. The observer that delivers the bead computes nothing: it restates the gate's recorded figures. |
| `guidance`, injected | `advise` | Advisory text rendered into the prime output and recorded as a firing. |
| `recycle` at a boundary | `handoff` | The session is replaced at a step or formula boundary, carrying a resume brief in the checkpoint. |
| *(not among the design's four)* | `reduce_effort` | The D16 arm: the relaunched session inherits a declared, lower effort level. |
| *(ADR-007 amendment; not among the design's four)* | `refuse` | The pre-act sub-agent-dispatch capacity gate (`af dispatch-admit`) denies a `Task` launch that would oversubscribe the backend's declared pool, through the PreToolUse permission-decision channel. The refused agent is shown the arithmetic and counselled to launch one at a time; the refusal is recorded regardless of the telemetry toggle, carrying `pool_tokens` and `summed_occupancy_tokens`. |
| *(not an intervention at all)* | `observe` | An armed site reached its decision point and changed nothing, and says so rather than leaving silence. Two write it: the pre-act gate when it fails open because the arithmetic could not be assembled — carrying no operands, because the missing operands *are* the fact — and the efficiency boundary when a relaunch history warranted was refused by the per-instance relaunch bound. `af tokenomics status` tallies these separately and never as firings, and attributes them to no objective: a gate that could not judge must not be counted as one that did. |

The fourth `Action` constant, `refuse`, exists because the ADR-007 amendment (2026-08-31) widened what
the harness may do by exactly one enumerated act — denying a sub-agent launch — not because prose
needed reconciling. It is the one and only widening of what the harness may *do*. `observe` is a fifth
constant and not a fifth act: it is the record for having declined to act, which is why introducing it
did not require an amendment and why an actual new act still would.

### Per-mechanism contract

Each mechanism is named here by its code constant (`internal/tokenomics/policy.go`), which is the
same string `startup.json`, `af tokenomics status` and the intervention records use.

Three of the six fire at more than one site, and the sites do different things. Where that is so the
row names each of them, because an operator grepping intervention records will find each.

Naming a second site is never a second verdict. Where a mechanism gates something, exactly one site
computes the verdict and the others either counsel from a different question or relay what that site
recorded. For `dispatch` this is the whole of issue #673 item 1: the pre-act gate computes, the prime
advisory asks a different question (does this *step* fit its own window), and the `Task`/`Agent`
observer relays. Two computers would mean two answers, and the divergence would be invisible because
neither logs the other's operands.

The `Objective(s)` column names which arm of the accounting each mechanism answers to, spelled as the
`objective` field on its intervention records. A row naming both fires for two independent reasons and
one arm going quiet does not silence the other.

| Mechanism | Trigger | Guarantee | Permitted action | Audit record | Objective(s) |
|---|---|---|---|---|---|
| `budget` | The admission predicate, at `af prime` (open time) and `af done` (close time): the step's learned appetite projected against the free window exceeds the admission margin | Never refuses the step; recycles only at a boundary, and only when a fresh window would actually fit | Advise at prime; hand off at a step or formula boundary | Two record classes — `mechanism=budget` with `action=advise` at prime (`prime_economics.go`), and with `action=handoff` at the boundary (`done.go`) | `capacity` |
| `thrift` | Two independent triggers at prime. The capacity trigger keys on the session's **current occupancy alone** being at or above the admission ceiling — no learned history is required, which is the point: it is the one capacity trigger that still applies in a factory that has learned nothing. The efficiency trigger keys on the step's learned median `repeat_reads` crossing the policy's threshold — once the efficiency arm is on and the step has a trusted aggregate with generation figures in it, which are the predicate's preconditions for answering anything — and it reads no occupancy, no window and no pool at all | Text only — it changes nothing about the run. The efficiency counsel is gated on the efficiency plan alone and not additionally on the thrift switch, so an operator who turned capacity-thrift off still gets the re-read counsel: the two answer different questions and neither switch speaks for the other | Advise: prefer targeted reads, do not re-read what is already in context | One `intervention` record per firing, `mechanism=thrift`, `action=advise` (`prime_advisory.go`) — both firings carry the same mechanism, so the `objective` field is the only thing that tells them apart. The efficiency counsel is deduped once per step under the composite `thrift\|efficiency` key rather than under the bare mechanism, because one spelling would let the capacity thrift suppress it | `capacity`, `efficiency` |
| `dispatch` | Three sites, one verdict. At prime, when the step fits the free window but only just (learned appetite ≤ free < 2 × appetite) — that is step-appetite counsel, a different question from pool capacity, and it is why a third `mechanism=dispatch` emitter does not make a third decider. At every `Task`/`Agent` completion, through the `af subagent-observe` PostToolUse hook, when the gate has left a recorded refusal newer than the 15-minute fan-out latch window — the observer relays that recorded decision and computes no capacity verdict of its own. And pre-act, through the `af dispatch-admit` PreToolUse `Task\|Agent` gate — the one site that computes the capacity verdict — when a launch would oversubscribe the backend's declared shared pool, leave less than the child-footprint floor (`AF_BACKEND_CHILD_FLOOR_TOKENS`, default 50k) free, or — where `AF_DISABLE_PARALLEL_SUBAGENTS` is set — attempt a second concurrent sub-agent | The two advisory sites never block and always exit 0; the observer is latched, so one fan-out produces one counsel. `TOKENOMICS_DISPATCH` beads FOLLOW recorded refusals rather than anticipate them: the pre-act deny reason absorbed the pre-warning duty, so the bead is an after-the-fact restatement of a decision already made, not a prediction of one. The prime advisory reads the step's marginal appetite (its learned growth), so it is the design's soft, residual first rung; the PostToolUse observer and the pre-act gate — both appetite-free — are the deterministic serialization carriers. The pre-act gate is the ADR-007-amendment exception — it may deny the launch, and only that, by arithmetic over a declared pool (or by the sequential semaphore), failing open and staying silent otherwise (cloud-inert by construction). The sequential cap holds its slot for the child's whole lifetime: SubagentStop proposes; release follows verified sidechain quiet, never the stop signal alone | Advise: launch sub-agents one at a time and wait for each to return (prime renders this as text; the observer delivers it as a bead restating the figures the gate recorded). Pre-act: `refuse` the launch and tell the agent the arithmetic — counselling `af handoff` on a launcher-caused floor breach, otherwise to launch one at a time; a sequential-cap refusal names `AF_DISABLE_PARALLEL_SUBAGENTS` and says one sub-agent is already running | `mechanism=dispatch` with `action=advise` from `prime_advisory.go` or `subagent_observer.go`; and, on a refusal, `action=refuse` carrying `pool_tokens` (and `summed_occupancy_tokens` on a headroom or child-floor refusal; nil on the sequential-cap refusal, which is a semaphore, not token arithmetic) from `dispatch_admit.go` (recorded regardless of the telemetry toggle). The same refusal also leaves a `.runtime/dispatch_admit_last_refusal.json` breadcrumb, which is what the observer relays — it is a courier, not a second record. And where the gate fires open because the arithmetic could not be assembled, `action=observe` carrying no operands at all — the missing operands *are* the fact, and what the record is worth is the evidence that the gate ran | `capacity` |
| `interview` | Three sites. A boundary handoff, where the resume brief is composed for the replacement session — unconditional, and the only one of the three that is not policy-gated. At prime, when the interview arm is on and this session has been primed before, or the brief on disk was written for the step being started — the reduction withholds output the session already has (a slimmed identity block, a slimmed successor brief) and consults no efficiency plan, so turning the efficiency arm off does not silence it. And at a boundary relaunch the efficiency plan asked for a clean start on, where no coincident level change outranks the naming | The brief is written to the checkpoint before the pane is replaced. The prime reduction is recorded once per session and not once per invocation, so a session primed many times reads as one firing | Compose the resume brief; withhold output the session already holds; start the next session clean at a boundary | Three record classes, all `objective=efficiency` — `action=advise` at prime, latched once per session (`prime.go`); `action=handoff` at a clean-start boundary (`done.go`); and `action=observe` where the relaunch bound refused the recycle that boundary warranted, so a refusal is on the record as a non-event rather than as silence. The resume brief itself still writes nothing: it is not policy-gated and its evidence is the checkpoint | `efficiency` |
| `effort` | Three sites, two objectives. The actuator itself runs at the three launch legs (`af up`, `af sling`, and the respawn leg), where the efficiency predicate reads what the step has historically **generated** and chooses the level the next session will run at — the arm's primary trigger, and one that consults no window. Where that predicate declines, a capacity last resort may still select the policy's level for a step no session on the profile can hold. And at a boundary relaunch, where a level change is warranted — which outranks a coincident clean start in the naming, being the more specific fact and the only one of the two with a level to record | Nothing is re-levelled in flight: the level applies to the *next* session only, and the running one is never touched. The chosen level is clamped never to exceed what the profile itself declared, so where a profile declares a rankable level the arm can only ever ask for less; where it declares none, or declares `auto`, there is no ceiling to stay under and the plan stands | Choose the relaunched session's effort level; advise matching depth to headroom | Four record classes across three files, and exactly one of them reads its objective back rather than stamping it — so one prime can leave a `reduce_effort` stamped `efficiency` beside an `advise` stamped `capacity`, and a reader who folded the second into the efficiency arm would be counting a step nothing on the profile can hold as evidence that reducing generation pays. `done.go` writes two — `action=handoff` carrying `effort_level` at a boundary relaunch, and `action=observe` where the relaunch bound refused one — and stamps both `efficiency` outright. `prime_economics.go` writes the third, `action=advise` — the line printed at prime when the step failed admission, would not fit a fresh session either, **and** a level was already chosen for it, all three, and with the arm still on at the read site so that a session treated before the switch was thrown is not told it was treated — and stamps it `capacity` outright, because the sentence it prints is about a step no session can hold. `prime.go` writes the fourth, `action=reduce_effort` carrying `effort_level`, at prime, where the treatment was actually applied rather than at the boundary that scheduled it — a session that applied a level and recorded nothing would leave half the arm's firings out of the data the arm is judged on — and it is the one read off the launch breadcrumb, so it says `capacity` when the capacity last resort chose the level and `efficiency` when the predicate did | `efficiency`, `capacity` |
| `escalate` | Policy-routed escalation (K14) — deferred, and the one mechanism that defaults **off** even under an enabled umbrella | Cannot fire in this release | *(none)* | **none today** — nothing writes `mechanism=escalate`, so `af telemetry band` honestly reports zero escalated steps rather than implying it measured and found none | *(none)* |

`effort` left the advisory registry, and the pairing that once made it and `dispatch` mutually
exclusive at prime went with it: the level it applies is chosen from the step's generation history at
the launch leg, not from what is left of the window, so it is no longer a window-pressure mechanism to
be exclusive *with*. The one thing it still says at prime is the capacity last resort's line, printed
by the economics block rather than by the registry — and that line reports a decision already taken at
launch rather than counselling one. `thrift`'s capacity arm can still accompany `dispatch`: "you are nearly full" and
"this step is large" are two different facts and a session can be in both — and the efficiency thrift
can accompany either, because it is again asking a third question. `budget` has a template in the
advisory registry and is
deliberately excluded from the prime counsel set, because its own economics block renders the same
arithmetic at the same moment and firing both would say one thing twice.

### How improvement is proven

A change to this surface is claimed to have worked only when `af telemetry compare` says so. Every
other reading of the record log — `af telemetry report`, `af telemetry band`, `af tokenomics status` —
is a diagnostic, and none of them is the bar.

The bar is **`median(after) < min(before)`**, strictly, over two arms of 5 runs each. Not mean against mean,
which one long run dominates, and not median against median, which passes on a coin flip: requiring
the after arm's median to fall below the *cheapest* before run is what makes it hard to clear by luck.
Under a pure-noise null it takes the three cheapest of the ten runs all landing in the after arm —
**21 of 252 splits**, about 8.3 % — and the verb prints those odds beside the verdict whenever the arms
hold counted runs, rather than leaving a reader to look them up; on empty arms it says instead that
there is no false-pass rate to state. A short arm is not a `fail`: nothing was disproved, so nothing
may be claimed.

The metric is Σ over closed steps of output tokens plus sub-agent tokens, and nothing else is ever
added to it. Thinking volume travels beside the verdict as a diagnostic and is deliberately not summed
in, because a pass metric that included it would measure how hard the model thought rather than what
it produced.

The verdict is one of `pass`, `fail` or `void`, and a failed precondition **voids rather than fails**:
`fail` is a claim about the intervention, `void` is a claim about the comparison, and a comparison
whose arms were not held fixed says nothing in either direction. Each void names the checks that
caused it, from a closed vocabulary — `distinct_runs`, `arm_size`, `formula_matches`,
`run_completion`, `steps_closed_constant`, `measured_steps_cover_closed`, `af_commit_constant`,
`model_constant`, `host_version_constant`, `checkout_commit_constant`, `input_digest_constant`,
`input_digest_verified`, `base_commit_matches_checkout`, `formula_digest_constant`,
`tokenomics_state_per_arm`, `nesting_comparable` — because a "void" with no named check is exactly the
unfalsifiable refusal the verdict exists to replace.

Two surfaces can be measured and `--surface` says which:

- **Surface B** holds the formula fixed and moves the posture. One formula digest spans all ten runs;
  the before arm runs `af tokenomics off`, the after arm `af tokenomics on`.
- **Surface A** holds the posture fixed and moves the formula. The arms run two *different* formula
  digests, with tokenomics on and `af improvement off` throughout — the arm measures the edit, not a
  live loop, because a digest that moved mid-arm would leave nothing fixed to compare.

The `--input-digest` attestation is part of this procedure and nothing else: the **operator** runs
it **by hand**, **during these measured-verification runs** — at sling time (step 3) recording the
canonical `sha256` of the frozen inputs on each run, and at compare time (step 7) re-checking it with
`--verify-input-digest` — **so that** both arms are proven to have run on byte-identical inputs and
`compare` voids any run whose inputs drifted rather than reporting a difference the drift could explain.

The procedure, in the order an operator runs it:

1. Build once, so both arms record the same `af_commit`, and `af telemetry on` for both. Pin
   `tokenomics.learned_min_runs` in `startup.json` to at most the arm size of five and record the
   value: the shipped default is 2, but a factory that had raised it would run its whole after arm
   dark.
2. Pick a **closed** issue and freeze the input. Commit the problem summary into the pinned SHA so the
   formula takes its coordinator-input path and never fetches live, and attest the issue body and its
   comments with a canonical `sha256` — field order and comment order pinned, so two honest
   attestations of an unchanged issue cannot differ.
3. Freeze the remote. Clone a bare mirror outside every worktree, point its `main` at the pinned SHA,
   and set each run's `origin` to that mirror, so the formula's own fetch and rebase land on the pin.
   Each run gets a fresh worktree from the pin in its own agent directory, and is slung with
   `af sling --formula <name> --input-digest <digest>`.
4. **Surface B**: five runs with `af tokenomics off`, then five with `af tokenomics on`,
   `af improvement off` throughout. The before arm is what seeds the digest, which is why step 1 caps
   the trust floor at the arm size — with it, every key is trusted from after-run 1. `compare` prints
   `trusted_keys` per run, so a dark arm cannot pass unnoticed.
5. **Surface A**: one intersection run with both loops on produces the edit; the operator reads the
   outcome mail and promotes it or keeps the store edit. Then five **counted** runs on each digest,
   improvement off and tokenomics on in both. Budget `learned_min_runs` further runs at the head of
   the after arm: they seed the new digest partition, they are recorded and excluded, and they are
   *additional to* the five — the `arm_size` check voids any arm that does not hold exactly five
   counted runs, so warm-ups taken out of the five produce a `void` rather than a result. The after
   arm's keys are therefore only just trusted where the before arm's are long trusted, an asymmetry
   that favours the *before* arm and must not be read as a marginal after-arm effect.
6. Before committing to an arm, three pilot runs on the pinned fixture establish the same-issue spread
   and test a stated prediction. `compare` prints that spread as `before_spread_pct` and the reduction
   the bar actually demands as `required_reduction_pct`, because a 1.6× within-arm spread and a 2.5×
   cross-issue spread ask very different things of the same intervention.
7. `af telemetry compare … --verify-input-digest <recomputed digest>` after the last run. What that
   flag re-checks is the attestation `af sling --input-digest` recorded on the run as `sling_digest`,
   surfaced by `compare` as `input_digest`; a `void` names what to re-run.

Reading a `fail` is part of the protocol rather than an exception to it. If the actuators fired and
the targeted steps show `outside_baselines` with `direction: below` under `af telemetry band` while the total still misses, the
bar was missed — an accepted residual, and the intervention worked as far as it reaches. If the
actuators were dark, or the targeted steps did not move at all, it is a design defect and the arm
sizes have nothing to do with it. The two readings call for entirely different work, which is why the
per-step direction is printed beside the total instead of folded into it.

### Residual ceilings and disclosures

These are the limits of what the surface can tell you. They are written here rather than left to be
discovered, because each one has a failure mode where a reader would otherwise draw a stronger
conclusion than the data supports.

- **The learning plane is local, and permanently so.** Every figure the policy reasons about comes
  from `.agentfactory/telemetry/` and from Claude Code session transcripts on the same host. The
  telemetry *backend* cannot supply them: `af telemetry usage` returns `query_failed` for the
  per-step token columns because the backend's logs stream does not carry them. That is a known gap
  in the backend, not a fault in the factory or the console — and it means a factory whose telemetry
  export is healthy still learns nothing from the backend.
- **Fail-open means ADMIT.** When there is no occupancy reading, no resolved window, or no learned
  data for a step, the admission predicate answers `observe` rather than `no-fit`, and the step
  proceeds. "We have no basis to judge" and "we judged, and it fits" are kept as different answers
  in the record, but they have the same effect on the run: nothing is held. An operator who expects
  a step to be blocked when the harness is blind will be surprised in the safe direction.
- **The fidelity grader also fails open, and does so more often on offline profiles.**
  `hooks/fidelity-gate.sh` invokes the grader through an `env -i` allowlist that deliberately strips
  cloud credentials; when the grader returns an empty verdict the gate notifies once and never
  blocks. On a profile with no reachable grader, turns pass ungraded rather than failing.
- **Respawn attribution has two named funnel classes and they are not interchangeable.** A session
  replaced out of a channel that had already gone quiet records `backend_stall_respawn`; one replaced
  out of a healthy channel records `unattributed_respawn`. A count that merges them cannot tell a
  backend dropping sessions from something else entirely, and the two call for different
  investigations.
- **`bound_tokens` is a telemetry annotation, not the denominator.** The `step_context.bound_tokens`
  value in `startup.json` is recorded on each step as `ctx_bound_tokens` so a reader knows what the
  step was judged against, but the window every occupancy fraction is divided by resolves **per
  agent** — declaration, then the host's report, then the fallback — and travels with a source field
  saying which of the three it was. On a mixed-profile factory a single `bound_tokens` is not the
  window for every agent, and a report that treated it as one would be comparing agents against a
  number none of them ran under.
- **Escalated-step counts read zero on every honest factory today.** There is no escalation marker in
  the record schema; the only derivation is counting intervention records whose mechanism is
  `escalate`, and nothing writes one. `af telemetry band` states that reason beside the zero.
- **Statusline profile resolution is config-only.** It passes no CLI model and does not read
  `.runtime/model_override`, while the prime/done path is marker-aware. On a session whose model was
  overridden at launch, the statusline's window and the record's window can name different profiles.
- **The pass metric cannot see input-side savings.** It is Σ of output and sub-agent tokens, so an
  intervention that removes *input* — the re-prime slimming that withholds output a session already
  holds, or counsel that stops a file being read twice — scores exactly zero on the bar unless it also
  shortens what the model then generates. That is the intended shape of the metric and not an
  oversight: input savings are cheap to fake by truncating context, and a bar that rewarded them would
  reward the truncation. It does mean a `fail` is not evidence that a slimming change did nothing,
  only that it did nothing the bar measures. Where that question is actually answered is
  `af telemetry compare --json`, which carries `in_tokens` and `cache_read_tokens` beside the verdict
  — per **run**, not per step, so the answer is coarser than the verdict it qualifies.
  `af telemetry report --json` carries neither of those two columns. The nearest it does carry per
  step is `cum_tokens_delta`, which *is* a spend figure — the difference across the step, and the
  direct answer to what the step cost — but a total one, so it cannot separate input from output and
  cannot tell a slimming change from a shorter answer; and `ctx_tokens_start`, which is occupancy,
  what the window held rather than what was paid to fill it.
- **Effort is session-granular, and the metric is per run.** A chosen level applies to the *next*
  session, so a step that runs start to finish inside one session never sees the level move, and a
  session that spans several steps applies one level to all of them. The arm therefore cannot be
  credited or blamed per step: `af telemetry band` will show `direction` per step, but the attribution
  from a level change to any one of those steps is an inference the records do not carry.

### Where the numbers come from

`af telemetry report --json` carries, per step, four different things and it is worth keeping them
apart: what the window *held* (`ctx_tokens_start`, `ctx_tokens_end`, `ctx_tokens_total`,
`ctx_used_pct`), what the step *generated* (`out_tokens`, `think_tokens_est`, `thinking_share`,
`peak_ctx_tokens`, `subagent_tokens`), what the step *cost* (`cum_tokens_delta`, a total spend
figure), and what it was *judged against* (`ctx_bound_tokens`). The last two are why the row carries
two verdicts rather than one: `over_occupancy` compares what was held to the bound,
`over_consumption` compares what was spent to it. Every one
is an explicit `null` when it was not measured, never a `0` — "nobody measured this" and "this was
zero" are different facts and the schema keeps them apart.

Token counts are derived under one counting rule, and it is not obvious: Claude Code writes **one
record per content block** and stamps the whole message's usage on every one, so records are reduced
per `message.id` with **MAX per field** — not summed, which over-counts by roughly 2.2×, and not
first-wins, which keeps an in-flight partial and drops the completed count. Any figure quoted about
this factory that was produced by summing transcript lines is wrong by about that factor.

The figures a measured verification run is graded against are registered in
`internal/telemetry/verification_baselines.go` — each with its unit, whether it is a wall cost or a
share, and the exact file and line it was derived at — together with that counting rule and the
sha256 procedure for proving a run's artifacts are the ones it reports. They live in ordinary source
rather than in a test so they can be read and cited without running anything, and they are pinned
before a run rather than chosen after one.

`af telemetry rebuild` folds closed step records into a learned digest — per (formula, step, model)
medians, maxima and thinking share — which is what the admission predicate's "learned appetite" reads
and what `af telemetry band` judges a run against. `af tokenomics status` reports how many aggregates
that digest holds, and says so explicitly when it holds none rather than printing a bare zero.

## The efficiency knobs in `startup.json`

Efficiency is the eighth tokenomics switch and deliberately **not** a seventh mechanism
(`internal/config/startup.go`): the six mechanisms are capacity behaviours keyed on window pressure,
while efficiency is keyed on a step's learned generation history and fires at any pressure at all.
Five keys under the `tokenomics` block in `startup.json` configure it — set them with
`af config startup set`. Every numeric reads a written `0` as "absent" and is coerced to its default;
an out-of-range value is rejected loudly, naming the on-disk key.

| `startup.json` key | Type | Default | Effect |
|---|---|---|---|
| `efficiency` | string (`on` / `off` / `default`) | `default` (resolves ON under the umbrella) | Master switch for the efficiency arm. With it off, the effort and relaunch actuators downstream of the policy do not fire. |
| `efficiency_effort_level` | string, one of the host's effort levels | `medium` | The reasoning-effort level the actuator selects when it reduces a session. Validated against the host's own vocabulary, never silently dropped — an unrecognised value is rejected. |
| `efficiency_thinking_share_pct` | int, 1–100 | `80` | Threshold share of a step's *exact* generation spent thinking; at or above it, the reduced arm is warranted for that step. |
| `efficiency_repeat_read_floor` | int, ≥ 0 | `1` | The learned re-read count at which the thrift counsel is worth its own tokens and gets rendered. |
| `efficiency_max_relaunches` | int, ≥ 0 (`0` = unset) | `6` | Caps how many extra session recycles one instance may be given for efficiency's sake, so a step that always looks worth relaunching cannot spend a run doing nothing else. A written `0` reads as this block's "unset" spelling and fills to the default — it is **not** a "disable relaunches" value. To stop efficiency relaunches entirely, set `tokenomics.efficiency off`. |
