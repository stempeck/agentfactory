# Using Agentfactory: Watchdog & Context Recovery

Operator guide for the watchdog and context-exhaustion recovery: what supervises agent
sessions, what fires a recycle, what survives one, and the tuning knobs in
`.agentfactory/startup.json`. Split out of [USING_AGENTFACTORY.md](USING_AGENTFACTORY.md).

## Watchdog

The watchdog (`af watchdog`) is a long-lived polling loop that supervises agent
sessions. `af up` launches it best-effort; you rarely run `af watchdog` by hand.

It runs **two surfaces, each with its own scope**:

- **Pane monitoring** watches agent tmux panes for Claude crashes, known error patterns
  and silence timeouts, then nudges or respawns the affected session. Its scope comes
  solely from `startup.json.watchdog_agents` — an explicit, bounded list. There is no
  "watch all" mode. Omit the key, or name only agents that do not exist, and this
  surface is **inert**: no pane is captured, no nudge is sent, and no keystrokes reach
  any pane.
- **Occupancy recovery** reads each agent's context-occupancy snapshot and recycles
  agents whose context is exhausted. It covers every agent in `agents.json` that has a
  live session under this factory root, minus `startup.json` `recovery.exclude`.

So an empty `watchdog_agents` narrows what the watchdog **does**, not whether it
**runs** — the process starts either way, and prints both scopes on startup. That
matters because an agent can exhaust its context whether or not anyone thought to add
it to the pane list.

A circuit breaker stops respawning after repeated failures and escalates to the
supervisor. Each completed tick touches `.runtime/watchdog_heartbeat`, so the
watchdog's own absence is detectable between `af up` runs.

Occupancy recovery can only see an agent whose `.claude/settings.json` carries the
`statusLine` key and whose factory has `af statusline on`. `af up` reports both gaps
before launching the watchdog; the fix is to re-run `af up` (or `af sling`) for the
affected agents, which is what installs the settings template.

## Context exhaustion recovery

An agent whose model context window fills up does not crash. It wedges. The pane keeps repainting,
the process name still matches, mail is still ingested, tools still run, and every turn completes —
producing nothing useful. None of the signals that catch a dead agent catch this one, because by
every one of them the agent is alive.

The factory reads the occupancy channel the statusline already writes (see [USING_TELEMETRY.md](USING_TELEMETRY.md)), and when
an agent's context is exhausted it **recycles the session**: checkpoint, clear residue, mail the
agent, respawn the pane with `af prime` as its opening command. `af prime` re-reads the agent's open
step from the store and reprints it, so the new session resumes the work the old one was holding.
The whole thing runs inside `af watchdog`, without operator action.

### What fires a recycle

Occupancy recovery raises three of its own triggers:

| Trigger | Fires when |
|---|---|
| `context_exhaustion` | occupancy is at or above `context_threshold_pct` for `confirm_ticks` consecutive ticks |
| `dark_at_high_occupancy` | the channel goes quiet *after* a high reading — the wedged-at-full shape, where the session stops reporting because it can no longer do anything |
| `progress_backstop` | an agent holds an open ready step but shows no step progress and no occupancy growth for `progress_backstop_secs` — the backstop for a session whose payload accounting under-reports |

Three things deliberately do **not** fire a recycle. Low occupancy never does, however long a step
has been running or however idle the session looks — a long step is exactly the work a wrong trigger
would destroy. A dark channel at *low* occupancy escalates for visibility but never recycles: "the
channel died" and "the agent is exhausted" are different claims. And occupancy growth is never read
as progress — a degraded model backend answers every turn with a useless, token-consuming reply, so
a wedged agent's occupancy climbs steadily while nothing advances. Only a step-anchored signal (a
closed step, or `af prime` advancing to a new one) confirms that a recovery took.

Five further triggers appear in the recovery log — `crash`, `error_pattern`, `compact_handoff`,
`self_handoff` and `step_boundary_handoff`. Those are the other ways a pane gets recycled (watchdog
pane monitoring, and the agent-initiated handoffs). They are recorded because the log is written at
the single funnel every recycle passes through, not by the exhaustion path alone.

`step_boundary_handoff` is the cooperative one: at the close of a step, if the agent's own session is
reporting occupancy at or above `step_context.handoff_pct` and further steps remain, `af done`
recycles the session on the spot rather than carrying a nearly-full context into the next step. It is
the only trigger the factory raises on a healthy agent's behalf without the agent asking, which is
why — like the two handoffs the agent asks for — it is exempt from the recovery-attempt rate cap:
counting a hygiene recycle toward the breaker would drive a correctly-behaving agent to
`RECOVERY HALTED`. A step that closes a formula gate never fires it, on the last step or any other:
the gate close is the boundary an operator is waiting on, not a place to spend a session recycle.

> **Gateway profiles: primary-trigger coverage is unverified.** The `context_exhaustion` and
> `dark_at_high_occupancy` triggers rest on the occupancy percentage the host reports through the
> statusline. For a non-Anthropic gateway/LiteLLM profile that percentage is computed against a
> client-side context-window constant that need not match the backend's real window, so a profile
> that under-reports can keep an agent below `context_threshold_pct` while it is in fact wedged. The
> K0 pre-build spike that would verify each shipped gateway profile's payload accounting
> (`.designs/596/implementation-plan/spikes.md`) was **not run** in this factory — so **gateway
> primary-trigger coverage is unverified, and for an under-reporting profile only the 2-hour
> `progress_backstop` is guaranteed** (and only while the agent holds an open ready step). Operators
> running a gateway profile should run spike (a) per profile and record the verdict in `spikes.md`.
>
> **Two profile keys are the per-profile levers on that constant.** `CLAUDE_CODE_MAX_CONTEXT_TOKENS`
> declares the backend's real context window — the number the host divides by when it reports
> occupancy — and `CLAUDE_CODE_AUTO_COMPACT_WINDOW` declares the token count at which the host
> auto-compacts. Both are ordinary profile exports; [Declaring a backend's real context
> window](#declaring-a-backends-real-context-window) below shows where they go. Setting the second
> one costs you the statusline's other meaning: the host's percentage always measures against the
> model's full context window, so once the window var is set, `used_percentage` no longer indicates
> compaction timing. It still says how full the window is, which is the reading recovery acts on;
> it just stops saying how close compaction is.
>
> **The in-factory signal is `af statusline status`.** It prints a `context-window drift` advisory
> naming each agent whose declared window disagrees with the window the host reports back, and a
> `pairing warning:` line for a profile whose declared auto-compact window will silently cap. Both
> go to stdout, appended last, and neither changes the exit code. The drift check needs a live
> session under this factory root and a healthy occupancy reading to compare against, so an agent
> whose channel is dark is skipped rather than named; a profile you have not launched is checked at
> write time instead, by `af config models set`.
>
> **Alignment is what restores `context_threshold_pct`'s meaning.** Recovery triggers on the
> host-reported percentage against a single factory-wide threshold — there is no per-profile
> margin — so a declared window that matches the backend is what makes that one threshold mean
> the same thing for every agent.
>
> **Scope: a profile's window governs the agent's main `claude` process only.** Grader subprocesses
> run under `env -i` with an explicit allowlist (`hooks/quality-gate.sh:137`) and never see profile
> env at all.
>
> **A compaction key defined in `models.json` is factory-owned.** The launch line sets it, and a
> launch that does not carry it `unset`s it, so a value exported from a shell rc is not what the
> agent runs with. Prefer omitting a key you do not want over setting it to `""`: an empty value is
> still an emitted export, not an absent one, and omission is what defers to the host.
>
> **One residual: a compaction key deleted from *every* profile is not auto-cleared from a live
> session.** The launch line only `unset`s keys that some profile still declares, so if you remove a
> key from the last profile that carried it, an agent already running keeps the old value — a
> respawn (handoff / compact / watchdog) inherits it, and only a full session recycle clears it:
> run `af down && af up` for that agent to drop the stale value.
>
> The spike-(a) recipe above is also the verification step for these two keys. af emits the
> exports; whether the host honors them is version-owned by Claude Code, so per-profile
> measurement is the only thing that turns "declared" into "effective".

### Declaring a backend's real context window

Both keys are set per profile in `.agentfactory/models.json` — the model registry `af install
--init` seeds and `af config models set` rewrites. A profile is a plain map of environment exports
rather than a closed schema, so these are ordinary entries: every value is a quoted JSON string, and
a launch emits every key the selected profile carries. On a gateway profile it emits a few more —
the per-class model keys derived from what you declared, which [Model profiles and
classes](USING_MODELS.md#model-profiles-and-classes) covers.

```json
{
  "default": "default",
  "models": {
    "default": {
      "ANTHROPIC_MODEL": "claude-opus-5"
    },
    "codex": {
      "ANTHROPIC_BASE_URL": "http://localhost:4000",
      "ANTHROPIC_AUTH_TOKEN": "file:.agentfactory/secrets/litellm.key",
      "ANTHROPIC_MODEL": "gpt-4o",
      "ANTHROPIC_DEFAULT_HAIKU_MODEL": "gpt-4o-mini",
      "ANTHROPIC_API_KEY": "",
      "CLAUDE_CODE_AUTO_COMPACT_WINDOW": "360000",
      "CLAUDE_CODE_MAX_CONTEXT_TOKENS": "400000"
    }
  },
  "agents": {
    "rapid-implement": "codex"
  }
}
```

Only the profile an agent actually selects contributes exports, so the `default` profile above stays
on a Claude model and only `rapid-implement` runs on the gateway. Precedence, highest first:
`--model` flag, `.runtime/model_override` marker, the `agents` map, a legacy `agents.json` endpoint,
then `default`.

That is a whole document, not a fragment, and `af config models set` replaces the file with what it
reads on stdin — so edit the registry you already have rather than pasting this over it, or you
discard every other profile and agent assignment in it:

```bash
jq '.models.codex.CLAUDE_CODE_AUTO_COMPACT_WINDOW = "360000"
    | .models.codex.CLAUDE_CODE_MAX_CONTEXT_TOKENS = "400000"' \
  .agentfactory/models.json | af config models set
```

**`CLAUDE_CODE_AUTO_COMPACT_WINDOW`** is the token count at which the host auto-compacts. Legal
values are a bare decimal in `[100000, 1000000]`, or `""`. `af config models set` rejects anything
else rather than saving it, because for an out-of-range number the value that takes effect is not
the value you wrote. That floor is a real limit, not a formality: a backend served below 100k of
context cannot be protected by this key at all.

This bound is also enforced at *load* time, which matters on upgrade: a `models.json` written
before this validation existed that carries an out-of-range window now fails the whole registry
load until the value is fixed — a selecting `af sling --model P` fails fast, and every other launch
warns to stderr and falls back to the global default model. Fix the value (or `af config models
set` a valid document) to restore per-profile launches.

**`CLAUDE_CODE_MAX_CONTEXT_TOKENS`** declares the model's real context window. The host derives a
window for its own models and assumes `200000` for any id it does not recognise as its own — that
is, any id not prefixed `claude-` — so on a gateway profile this key is what tells it the truth.
Legal values are a positive bare decimal, or `""`.

**The two are paired.** A foreign model id declaring an auto-compact window above `200000` with no
companion silently caps at `200000` and does nothing; `af config models set` saves the profile and
warns on stderr. Below `200000` the pairing earns its keep in the other direction — declaring the
real window is what fixes the occupancy denominator the statusline shows and recovery divides by.
In a mixed profile the companion raises only the foreign model's believed window, since the host
honors it for non-`claude-` ids alone; a foreign main model beside a Claude background model gets
exactly that, which is the behavior you want. And because one process carries one auto-compact
value, a profile serving several models must be set for the smallest real window among them.

The bounds and the `200000` assumption are pinned in `internal/config/models.go` against
<https://code.claude.com/docs/en/env-vars>, observed 2026-08-06 on claude 2.1.223. For the
gateway-side runbook — keeping model ids in sync, and what each kind of misalignment looks like
from the pane — see [USING_LITELLM.md](USING_LITELLM.md).

### Who is covered

The watchdog runs **two surfaces with different scopes**, and confusing them is the most common way
to believe an agent is protected when it is not:

- **Pane monitoring** — error patterns, silence timeouts, crash detection. Its scope is exactly
  `startup.json`'s `watchdog_agents`. Leave that list empty and this surface is inert: no pane is
  captured, no nudge is sent, no keystroke reaches any pane.
- **Occupancy recovery** — this feature. Its scope is **every agent in `agents.json` that has a live
  session under this factory root**, minus anything listed in `recovery.exclude`. It is derived, not
  opt-in.

So `watchdog_agents` narrows what the watchdog *does*; it never decides whether the watchdog *runs*,
and it does not bound recovery coverage. That split is deliberate: the failure this exists to
prevent was an exhausted agent sitting outside the configured pane scope with nothing watching it.

Sessions belonging to a different factory root are skipped, and `interactive` agents are never
auto-recycled — a human is present, so they are alerted instead.

### What survives a recycle

A recycle replaces the session, not the work. Durable state lives in the store and on disk; the
in-session conversation does not survive, and that loss is bounded and deliberate rather than
incidental.

| Durable state | Location | Survives a recycle because | Observable via |
|---|---|---|---|
| Assignment / task | formula-instance bead + resolved-vars carrier bead | store-persisted | `af agents list` |
| Open work state | step beads; `.runtime/hooked_formula` | the resume contract is beads, not the pointer file — `reconstructHookedFormula` (`up.go`) rebuilds a lost pointer | `af agents list` |
| Correspondence | mailbox (store-backed) | survives by construction; re-injected at SessionStart (`settings-autonomous.json`) | `af mail inbox` |
| Session breadcrumb | `.agent-checkpoint.json`, including Notes | written before the pane is killed | the file; the prime narrative |
| Work products | git worktree, commits | the respawn touches only scrollback and the pane (`ClearHistory`/`RespawnPane`, `helpers.go`) | git |
| Recorded learnings | `<factory-root>/.agentfactory/memory/<agent>/` | the vault sits outside every directory a teardown reaches, so `af done`, `af down`, `af down --reset`, `af sling --reset` and worktree GC all leave it standing; each of those says so in its own output | `af memory list`, `af memory status` |
| **NOT durable** | the in-session conversation | — | — (there is nothing to observe; this is the declared, bounded loss) |
| **NOT durable** | the vault across container *recreation* | the vault is container-local and gitignored; removing the container removes it | export it first — see [The memory vault](USING_MEMORY.md#the-memory-vault) |

Because conversation is the one thing lost, the factory mails a **`CONTEXT ADVISORY`** once per
session when occupancy crosses `context_advisory_pct` (default 70) — while the session can still act
on it — asking the agent to externalize: commit, write checkpoint notes, summarize by mail. What
gets externalized before the threshold is what survives after it.

### Watching it happen

Every factory-initiated recycle appends one line to the factory-root
`.runtime/recovery_log.jsonl`, with fields `at`, `agent`, `trigger`, `observed_pct`,
`threshold_pct`, `session_id`, `instance_id`, `resumed_step`, `attempt` and `outcome` — when it
happened, what triggered it, and what was resumed.

Live state is on the two machine-readable surfaces:

- `af agents list --json` carries `context_pct` (`-1` when unknown), `context_state`
  (`fresh` / `stale` / `dark` / `none`) and `recovery` (`none` / `recovering` / `halted`).
- `af dispatch status --json` carries the same `recovery` state for dispatch targets, so work aimed
  at an exhausted or halted agent is visibly stalled rather than silently absorbed.

The observer's own absence is visible too: each completed watchdog tick touches
`.runtime/watchdog_heartbeat`, so a dead watchdog shows up as a stale heartbeat instead of as
silence that looks like health.

### When recovery halts

If an agent keeps re-stalling, the factory **stops recycling it** and escalates. You will receive a
mail whose subject begins `RECOVERY HALTED` and names the command to run. There are two causes:

- **Re-stall** — `max_attempts` recycles inside `attempt_window_secs` without step-anchored progress.
- **Rate cap** — `rate_cap_max` recycles inside `rate_cap_window_secs`, which catches slow loops no
  attempt window is wide enough to see.

The breaker is durable (factory-root `.runtime/recovery/<agent>.json`), survives watchdog restarts,
and **never expires on its own**. That is the point: it is a terminal state that blocks further
recycling until a human has looked. If the escalation could not reach a live recipient, a breadcrumb
is left at `.runtime/recovery_halt_undelivered` — an escalation whose outcome is unknown is never
counted as having arrived.

**Runbook.** Before clearing it, find out why recycling did not help:

1. Read the recovery log for that agent — `grep '"agent":"<name>"' .runtime/recovery_log.jsonl`.
   Look at `trigger`, `attempt`, and whether `resumed_step` stayed the same across attempts. A step
   that never changes means the agent came back and re-wedged on the same work.
2. Check the breaker file `.runtime/recovery/<name>.json` for the halt cause.
3. Check `af agents list --json` for the agent's `context_state`. A `dark` or `none` channel means
   the factory was recycling blind — see provisioning below.
4. Look at the agent's `.agent-checkpoint.json` and its worktree for what was actually in flight.
5. Consider whether the real cause is outside the agent: a degraded model backend produces exactly
   this signature (see [USING_LITELLM.md](USING_LITELLM.md)).

Then clear the breaker:

```bash
af recovery reset <agent>
```

`af recovery reset` is **operator-only** and mutates breaker state only. It does not kill or start
sessions, does not touch worktrees, and does not close beads. If the agent is not running, relaunch
it separately with `af up <agent>` — the reset does not do it for you.

### Provisioning: agents the factory cannot see

Recovery can only observe an agent that writes occupancy snapshots, and an agent only writes them if
its `.claude/settings.json` carries the `statusLine` key. `af up` checks this before launch and
warns loudly about any agent missing it, and about a factory-wide statusline gate that is off.

The remediation is the part worth knowing, because the obvious guess is wrong:

- **`af up` and `af sling` are what deliver the settings key.** Both rewrite the settings file the
  session reads, on every launch, and Claude Code picks the change up live.
- **`af install --init` reprovisions factory-root agent directories only.** Agents living in
  worktrees do not get the key from it — they pick it up on their next `af up` or `af sling`.
- If the factory-wide gate is off, no agent writes snapshots at all and recovery is blind
  factory-wide. Turn it on with `af statusline on`.

### Tuning it

Recovery is configured by the `recovery` block in `.agentfactory/startup.json`, read fresh on every
watchdog tick. Every key is optional; the shipped defaults are what appear below. Run
`af watchdog --help` for the same list with each key's meaning.

```json
{
  "recovery": {
    "enabled": true,
    "context_threshold_pct": 85,
    "context_advisory_pct": 70,
    "confirm_ticks": 2,
    "staleness_secs": 180,
    "dark_grace_secs": 600,
    "post_recovery_progress_secs": 900,
    "progress_backstop_secs": 7200,
    "no_step_escalation_secs": 3600,
    "max_attempts": 3,
    "attempt_window_secs": 1800,
    "rate_cap_max": 6,
    "rate_cap_window_secs": 86400,
    "exclude": []
  }
}
```

Set `"enabled": false` to turn the surface off entirely, or list agent names in `"exclude"` to leave
individual agents alone while the rest stay covered.

### Step context bounds

Three rungs govern how full a session is allowed to get, and they are three different kinds of
thing. In ascending order:

| Rung | Key | Who acts | What happens |
|---|---|---|---|
| advisory | `recovery.context_advisory_pct` | the watchdog | one `CONTEXT ADVISORY` mail per session — a heads-up, nothing is recycled |
| cooperative handoff | `step_context.handoff_pct` | `af done`, on the agent's behalf | the session recycles at a step boundary, before the next step starts |
| forceful recovery | `recovery.context_threshold_pct` | the watchdog | the watchdog stops asking and recycles the pane where it stands |

A fourth key, `step_context.bound_tokens`, is not a rung: it is the per-step budget a step's spend is
*measured* against after the fact, and it triggers nothing. It is what makes `over_occupancy` and
`over_consumption` mean something in `af telemetry report` — the difference between a step that ran
long and a step that ran past its budget.

Read the ladder as one sentence: the advisory tells you, the handoff asks the agent to clean up at
the next safe boundary, and recovery acts whether or not that worked. Only the middle rung is
cooperative, which is why it is also the only one that costs nothing when it fires — the step is
already closed.

Both blocks live in `.agentfactory/startup.json`, beside the `recovery` block above. Every key is
optional. On a factory that is *recording*, `af telemetry status` prints three of the effective
values back with each one's role — `bound_tokens`, `handoff_pct` and
`recovery.context_threshold_pct`. It prints none of them when the gate is off, and it never prints
`recovery.context_advisory_pct` at all, so read the advisory rung out of `startup.json` itself. The
ordering is enforced at load — `recovery.context_advisory_pct` ≤ `step_context.handoff_pct` <
`recovery.context_threshold_pct` — but only against values you actually wrote. A `handoff_pct` you
simply omit is derived to fit whatever ladder you *did* write and clamped below your recovery
threshold, so tightening recovery never bricks the factory on a default you never chose. The clamp
prints a warning where the values themselves print — `af telemetry status`, gate on — so on a
gate-off factory you will not be told; compare the derived figure against your own `recovery` block
instead.

```json
{
  "step_context": {
    "bound_tokens": 200000,
    "handoff_pct": 75
  }
}
```

**Measurement and hygiene are armed by different switches, and neither one can break the other.**
The report is fed by step records behind the telemetry gate; the ladder is fed by the occupancy
snapshots the statusline writes. They fail independently, and both fail safely:

| Telemetry gate | Statusline occupancy | What the report can tell you | What the ladder does |
|---|---|---|---|
| on | present | everything: per-step figures, both verdicts, and the interrupted-step join | armed — advisory, handoff and recovery all live |
| on | absent | records exist and echo back the configured bound, but every *measured* figure — occupancy and consumption alike — reads as absent, and the report says so rather than saying zero | inert — no reading, so no handoff and no occupancy recovery |
| off | present | nothing: no step records are written at all | armed — the ladder reads the statusline, not the records |
| off | absent | nothing | inert |

The asymmetry is deliberate. Turning telemetry off costs you the evidence, never the safety; losing
statusline data costs you the ladder, never the evidence already recorded. What neither state does is
lie: an unmeasured step renders as unmeasured, never as a healthy one. That is also why
`af improvement on` warns when the telemetry gate is off, and why an improvement session that finds
no context data says so in its verdict instead of guessing — run `af telemetry on` to arm it.

**Two spellings, one mechanism.** `af compact-handoff` — hyphenated — is the command, the hook that
runs when Claude Code is about to compact (`af compact-handoff --interactive` for an interactive
session). `compact_handoff`, with an underscore, is how that same recycle is spelled as a trigger in
the recovery log, exactly as `step_boundary_handoff` and `self_handoff` are. Type the hyphen; grep
for the underscore.

> **Under a mis-declared gateway profile the boundary handoff is no stronger a guarantee than
> recovery itself.** The handoff reads the same host-reported occupancy percentage that
> `context_exhaustion` and `dark_at_high_occupancy` rest on, so a gateway/LiteLLM profile whose
> client-side context-window constant under-reports holds the reading below
> `step_context.handoff_pct` and `recovery.context_threshold_pct` *together*. The ladder's relative
> ordering survives — advisory still precedes handoff, handoff still precedes recovery — but every
> rung fires late, or never. `CLAUDE_CODE_MAX_CONTEXT_TOKENS` is the per-profile lever, and
> `af statusline status` is where you see what the host is actually reporting. See the gateway
> caveat earlier in this section for the full posture and the per-profile spike it asks for.

