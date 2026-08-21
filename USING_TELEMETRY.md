# Using Agentfactory: Telemetry & Statusline

Operator guide for run measurement: the telemetry backend and its dashboards, and the
per-session statusline. Split out of [USING_AGENTFACTORY.md](USING_AGENTFACTORY.md) —
start there for factory setup and day-to-day operation.

## Telemetry

**What it gives you.** Two questions you cannot answer today: *which step of a formula run is
actually slow*, and *what each agent, model and step costs*. `quickstart.sh` installs a
measurement dashboard alongside the factory and seeds six views into it: **Step duration by
formula run**, **Tokens per agent, model and step**, **Cost per agent and model**, **Spend
outside steps**, **Does the accounting add up**, and **Steps that recorded no usage**. The
last two are honesty views — they show you when the numbers do not reconcile rather than
quietly rounding the gap away.

**Two levers, and installing one is not pulling the other.** `quickstart.sh` installs the
dashboard by default; `./quickstart.sh --no-telemetry` skips that install entirely and leaves
you a factory that works exactly the same, minus the timing and cost views. **Recording stays
off until you turn it on**, whether or not the dashboard is installed. Nothing about how
agents work changes either way — no new prompts, no new gates, no change to any formula.

### Turning it on

```bash
af telemetry on        # start recording, factory-wide
af telemetry status    # is it on, where is data going
af telemetry off       # stop recording; existing records stay readable
```

The toggle is a file — `<factory-root>/.agentfactory/.telemetry-gate` containing `on` — so the
equivalent manual form is `echo on > "$(af root)/.agentfactory/.telemetry-gate"`. It is never
created by `af install --init`; a fresh factory is always off. You can also set
`"telemetry": "on"` in `.agentfactory/startup.json`, which a bare `af up` applies.

**Turning it on takes effect at the next session launch.** An agent carries the measurement
environment it was given when its session started, and a running process's environment cannot
be changed from outside. Agents already up when you flip the toggle keep running without it —
restart them with `af down <agent>` then `af up <agent>` to pick it up. The same is true in
reverse: turning it off stops new sessions from recording, but a session already running keeps
going until it is restarted.

**Which run an agent's costs are filed under is also fixed when its session starts.** So if one
agent begins a second piece of work without its session being restarted, that second run's token
costs are still filed under the first one, and the second run looks free. The normal flow does not
hit this — starting work with `af sling` and then `af handoff` gives the agent a fresh session,
which files it correctly. It is worth knowing if you ever start a second run by hand in a session
that is already up: restart the agent first, and the numbers will land where you expect.

### Where the data goes

Settings live in `.agentfactory/telemetry.json`, seeded by `quickstart.sh`:

```json
{
  "endpoint": "http://127.0.0.1:5080/api/default",
  "otlp_http_path_traces": "/v1/traces",
  "headers": { "Authorization": "file:.agentfactory/secrets/telemetry.auth" },
  "protocol": "http/json",
  "export_timeout_ms": 500,
  "resource_attributes_extra": {}
}
```

**`endpoint` is the one value you are expected to touch.** Leave it alone to use the bundled
dashboard, or paste your company's OpenTelemetry address to send everything to your own stack
instead. Both are fully supported — the bundled dashboard is a convenience, not a dependency,
and a factory installed with `--no-telemetry` that later points `endpoint` at an existing
stack works fine. `headers` is the password the connection uses, kept in a file rather than
written here; `quickstart.sh` sets it up and you normally never touch it. The file is not
tracked by git, so a factory-specific address and a secret never end up in a commit.

One caveat if you point at your own stack. The bundled dashboard is reached at a sub-path of its
own, which is why the address above ends in `/api/default`. Your own stack almost certainly does
not use that, so replace the whole address with yours and drop that trailing part:

```json
  "endpoint": "https://otel.your-company.example",
```

The line below it is already the standard value every other destination expects, so leave it
alone — or delete it, which means the same thing.

Then run `af telemetry status`. It contacts every address this factory sends to and tells you
which ones answered, so you do not have to guess whether you got it right.

**The bundled dashboard.** OpenObserve, pinned and checksum-verified, running in a tmux
session named `telemetry` on `127.0.0.1:5080`. Log in as `root@agentfactory.local` with the
password in `.agentfactory/secrets/telemetry.root`.

**Reaching the dashboard itself from your own browser is not guaranteed.** It binds to loopback
deliberately, and `quickdocker.sh` publishes no ports, so on a container you will need to set up a
port-forward or tunnel yourself. From inside the container it always works — a browser there,
`curl`, or `af telemetry report` in the terminal. **You no longer need any of that to read your
telemetry from a host browser:** the web console has a Telemetry panel that reads the same data
over the console's existing connection. See *Reading telemetry from the console* below.

**`af up` and the watchdog relaunch it for you when the recording gate is on — for the bundled
backend on this machine.** There is still no service manager, but `af up`'s telemetry step and the
watchdog's periodic tick (roughly every 30 seconds, when `watchdog_agents` is non-empty) both check
the backend and relaunch it if the tmux session is gone. `quickstart.sh`'s guard on
`~/.bash_profile` remains as a manual fallback for the one case neither can autonomously clear — a
session that is alive but wedged, not exited: start a login shell (`bash -l`), or check it directly
with `tmux attach -t telemetry`.

If you pointed `endpoint` at your own stack instead, none of that applies: agentfactory never
starts, stops, or watches a backend it did not install, so a remote address that stops answering
stays down until you bring it back. `bash -l` would start the bundled backend here rather than
reach yours.

### Reading telemetry from the console

Open the console the way you already do — a clean `./quickdocker.sh <github-repo-path>` reveals it
for you, and `./quickdocker.sh <github-repo-path> --web` re-opens it later, printing the loopback
URL `http://127.0.0.1:<HOSTPORT>/`. Click **Telemetry** in the console's navigation: the panel reads
this factory's telemetry over the connection the console already has, so there is nothing extra to
forward or tunnel.

**What the three panes show.** *Step timings* is the same per-step duration table
`af telemetry report` prints, read from the records `af` writes locally. *Token usage* is what
`af telemetry usage` returns from the backend, broken down by agent, model, and step **when the
backend holds the per-request records that breakdown is built from** — see the note below, because
on a factory where it does not, the pane says so rather than showing an empty table. *Session
metrics* is an instant reading of the current counters — it does not honour the time-window control
above it, and the panel says so on screen.

**The per-step token breakdown is not available on this backend, and the pane checks before it
asks.** It would join the step windows `af` records against the per-request records Claude Code
sends, but every real capture taken against the pinned backend — kept in
`internal/telemetry/testdata/openobserve-v0.91.3/` and `internal/telemetry/testdata/recorded-real/`
— shows the columns that join needs are not carried under any name the schema has. Rather than send
a query the schema pre-flight already knows will fail, the pane checks the backend's own schema
first and reports `query_failed` with the gap named in its own words — never the backend's raw
error text. Whole-run totals and the session counters are unaffected. If your Token usage pane
reports a failed query, that is the state you are in; it is a known, permanent gap and not a fault
in your factory or in the console.

**The banner stack tells you what to fix, one line per problem.** The panel checks three things in a
fixed order — whether telemetry is installed, whether recording is on, and whether the backend
answers — and prints a line only for the ones that are degraded. When all three are healthy it
prints a single reassurance line instead, ending in `Nothing to act on.`

| Banner line | What it means | Next step it gives you |
|---|---|---|
| Telemetry is not configured: no `telemetry.json` found | This factory was never set up to export; step timing is still recorded locally while recording is on | Run `quickstart.sh` with telemetry, or create `.agentfactory/telemetry.json` |
| The telemetry configuration could not be read | `telemetry.json` exists but will not parse, so nothing can be resolved from it | Same — repair or recreate `.agentfactory/telemetry.json` |
| No endpoint is configured in `.agentfactory/telemetry.json` | Records stay local because there is no address to query | Same — add an `endpoint` |
| Recording is off | The current recording state; records already written stay readable below | `af telemetry on` |
| The backend was not probed | No measurement was taken, usually because the credential could not be read — so no state shown is a verdict | Check `.agentfactory/secrets/telemetry.root`, or re-run the quickstart credential step |
| A backend address answered `401` or `403` | The address was reachable but rejected the credential | Check `.agentfactory/secrets/telemetry.root`, or re-run the quickstart credential step |
| A backend address answered `404` | The address is reachable but the path is wrong | Check that the configured endpoint ends in `/api/default` |
| A backend address did not answer at all | Nothing is listening — usually the backend died | For the bundled backend on this machine: `af up` (cold start) or the next watchdog tick (~30s) relaunches it automatically when the recording gate is on. Still down after that? Start a login shell (`bash -l`) as a manual fallback, or `tmux attach -t telemetry` if it is alive but unresponsive. If `endpoint` is your own stack, nothing here restarts it — check that the address is up and reachable from this machine |

Two states are deliberately not reported as healthy. An unprobed backend prints "no measurement was
taken" rather than a green verdict, because a check that never ran is not a check that passed. A log
whose every record is corrupt reports zero rows *with* a non-zero error count, so it can never be
mistaken for "no records yet".

**The Token usage and Session metrics panes speak for themselves**, and what they say is not the
banner lines above. The banner describes this factory's setup; these describe what happened to the
one query the pane asked for. Each is a sentence in the pane where the table would be, so the pane
is never blank and never shows an empty table in place of an explanation.

| What the pane says | What it means | Next step it gives you |
|---|---|---|
| No telemetry endpoint is configured, so nothing was queried | There is no address to ask, so no query was sent. The pane is empty because nothing was measured, not because the answer was zero | Run `quickstart.sh` with telemetry, or create `.agentfactory/telemetry.json` |
| The backend did not answer the query | The address was asked and nothing came back — usually the backend died | For the bundled backend on this machine: `af up` (cold start) or the next watchdog tick (~30s) relaunches it automatically when the recording gate is on. Still down after that? Start a login shell (`bash -l`) as a manual fallback, or `tmux attach -t telemetry` if it is alive but unresponsive. If `endpoint` is your own stack, nothing here restarts it — check that the address is up and reachable from this machine |
| The backend was reachable but the credential was rejected | The address is right and the password is not | Check `.agentfactory/secrets/telemetry.root`, or re-run the quickstart credential step |
| The query reached the backend and failed (`query_failed`) | For the Token usage pane, this is almost always the known per-step gap: a schema pre-flight checked before sending the query and found the columns it needs are not on this backend. For other queries the text in brackets is the backend's own answer | If the pane names the missing-column gap, use the whole-run totals and the bundled dashboards for anything finer. Otherwise, read the bracketed cause |
| The query did not complete | Any other reason the query produced no answer | Run `af telemetry usage` from a shell: it prints the same payload as JSON, and its `state` field carries this verdict with any cause the backend gave |
| No session metric returned a value, though every one was queried | The pane no longer leaves this ambiguous: it asks the backend's series API whether each silent metric has ever existed. **Idle — history exists**: the factory really was idle at this instant. **Never recorded here, or the names have moved**: no series has ever existed under that name — Claude Code may have renamed it | For "idle — history exists," nothing to do — query a wider window or wait for activity. For "never recorded... names have moved," the query needs updating |

**The six bundled dashboards live in the backend, not in the console.** The console panel is the
supported way to read this data from a browser on your host machine; the dashboards remain available
inside the container at `127.0.0.1:5080`, as described above.

**A rebuilt console does not replace one that is already running.** `make build-webui` writes a new
binary, but starting it finds the healthy address the previous one published, reports that the web
UI is already running, and exits without serving. Your browser keeps loading the **old build** — so
a panel added by an update is simply absent, with nothing anywhere to explain why.

The running console names itself in `.runtime/webui_server.json` at your factory root, which records
the address it is serving on and the **process id** that owns it. There is no `af` verb that stops
it: end that process yourself, then start the console again and it will be the new build that comes
up.

### Reading the data into a decision

`af telemetry report` prints how long each step took, from records `af` writes locally. It
works with no dashboard installed and it keeps working after `af telemetry off` — records
already on disk stay readable:

```
AGENT   STEP        STATUS  DURATION  STARTED               MODEL     VERB_MS
manager plan        closed  4m12s     2026-07-23T09:14:02Z  opus-4-8  38
Latency only. Token and cost figures live in the telemetry backend; af records step windows, never tokens.
```

Narrow it with `--agent NAME` or `--instance ID`, and use `--export` to push the local backlog
to the dashboard before rendering.

**Token counts come from the backend, not from disk.** `af` records how long each step took; it
never records tokens. To read those without opening a dashboard, ask the backend directly:

```
af telemetry usage
af telemetry usage --agent solver --instance af-4e894132
```

It answers with machine-readable JSON and **always exits 0** — a dead backend, a rejected
credential and a refused query are all reported in the `state` field rather than as a failed
command, so a script branches on the answer instead of on the exit code. Because the numbers live
in the backend and not on disk, this is the one telemetry verb that needs the dashboard reachable;
`af telemetry status` will tell you whether it is. Recording being switched off does not hide
anything here — data already collected stays readable, exactly as the local table does.

**The decision loop.** Open **Tokens per agent, model and step** and **Cost per agent and
model**, find the agent-and-model pairing that spends a lot for what it produces, then change
**that agent's** profile in `.agentfactory/models.json`. That is the whole loop.
**There is no per-step model setting** — the model is chosen per agent, so a slow expensive
step is fixed by re-profiling the agent that runs it, or by changing the formula. What a profile can
say about the *tiers* an agent's sub-agents reach for is in [Model profiles and
classes](USING_MODELS.md#model-profiles-and-classes).

**The local table is bounded by rotation.** `af` keeps the current record file plus exactly one
previous generation per agent, so on a busy factory the oldest formula runs eventually fall out
of `af telemetry report` even though they already reached the dashboard. Anything dropped is
counted and printed in the report — the loss is never silent.

### Privacy

| What is recorded | Contains your content? | Default | Where it goes |
|------------------|------------------------|---------|---------------|
| `af`'s own step records — step names, timings, IDs, agent and model names | No | On when telemetry is on | Local file, and forwarded to the backend |
| Token counts and model names, per request | No | On when telemetry is on | Backend |
| Session, cost and lines-changed counters | No | On when telemetry is on | Backend |
| Your prompts, the assistant's replies, tool inputs and tool results | Yes | **Off** — `af` never turns these on | Nowhere |
| Error text returned by the model provider | Possibly | On, and covered by no content switch | Backend |

**The five content switches are named, and `af` sets none of them.** Claude Code can be made
to record conversation content through `OTEL_LOG_USER_PROMPTS`,
`OTEL_LOG_ASSISTANT_RESPONSES`, `OTEL_LOG_TOOL_DETAILS`, `OTEL_LOG_TOOL_CONTENT` and
`OTEL_LOG_RAW_API_BODIES`. `af` sets none of the five and offers no setting that turns any of
them on — the absence is the posture. If you set one yourself, your prompts, files and tool
results go to whatever `endpoint` points at.

**Who you are travels with the measurements.** Claude Code's own measurements carry your
account email, organisation ID, account UUIDs and a session ID by default. That is harmless
against a dashboard on your own machine; if you point `endpoint` at a remote stack, those
identifiers leave the host with every measurement. Decide that deliberately.

**One field can carry free text.** When a request to the model provider fails, the provider's
error message is recorded as-is. No content switch covers it, and a provider is free to put
whatever it likes in an error string.

**`af`'s own records cannot carry content.** The record format is a fixed list of fields — IDs,
titles, timings, model names — and step descriptions, formula variables and the text of a
dispatched task have no field to travel in. There is no redaction step, because there is
nothing to redact.

### If it isn't working

Start with `af telemetry status`, which answers the layers in order:

```
telemetry: on
step context: bound_tokens 200000 (per-step budget), handoff_pct 75 (cooperative boundary), recovery context_threshold_pct 85 (forceful recovery)
config: .agentfactory/telemetry.json (endpoint http://127.0.0.1:5080/api/default, 1 configured headers)
endpoint: step timings: reachable (HTTP 200)
endpoint: token usage: reachable (HTTP 200)
endpoint: session metrics: reachable (HTTP 200)
```

Line one is the toggle. Line two is the context ladder, printed with each number's role because
reading one without the others cannot tell a measurement knob from an intervention knob:
`bound_tokens` is the budget a step's token spend is *measured* against after the fact,
`handoff_pct` is where a step *cooperatively* recycles its own session at the boundary, and
recovery's `context_threshold_pct` is where the watchdog stops asking and acts. Line three is your
settings — it names the address and counts the headers, never printing a header's name or value.
The lines after it are the answer to "can anything I record actually arrive": `af` contacts each
address it sends to and reports what came back. There are three because the timing of your formula
steps and the token counts from your agents' own sessions travel to different addresses, and they
can fail independently — one reachable and another not is the normal shape of a half-working
setup, and it is worth seeing rather than averaging away.

What the verdicts mean for you:

- **reachable** — the address answered and accepted the check. Data sent there arrives.
- **not served** — something is listening and your credential is fine, but nothing handles that
  address, so anything sent to it is discarded. Check that `endpoint` ends in `/api/default` if
  you are using the bundled dashboard.
- **credential was rejected** — the address is right and the password is not. The two files that
  have to agree are `.agentfactory/secrets/telemetry.root` (the dashboard's password) and
  `.agentfactory/secrets/telemetry.auth` (the header built from it); if you changed the password in
  the dashboard, or replaced one file and not the other, they have drifted apart. Re-running
  `quickstart.sh` only regenerates them when the stored password is one the backend would refuse
  outright, so for an ordinary mismatch delete both files and re-run — that rebuilds the pair. Note
  the dashboard keeps the password it was first started with, so you may also need to remove
  `.agentfactory/telemetry/openobserve` to start clean, which discards previously collected data.
- **refused the data** — the address answered and the credential was accepted, but it rejected the
  check itself. Usually a destination that speaks a different protocol version than expected; the
  status line quotes the code it returned.
- **unreachable** — nothing answered. The backend is probably not running; see
  **`af up` and the watchdog relaunch it for you when the recording gate is on — for the
  bundled backend on this machine** above.
- **not probed** — no address was contacted at all, because a credential named in `headers` could
  not be resolved first. The line says which of the four causes it was: the file could not be read,
  it is empty, its `file:` reference has no path, or it resolves outside the factory root and was
  refused. That last one is a deliberate refusal rather than a fault — a reference is only followed
  inside the factory, so a path pointing elsewhere is declined even when the file is perfectly
  readable. The header's name is never printed here; this line reports counts only.

This is the one place `af telemetry status` reaches out over the network: it sends an empty check to
each address in turn. Each check waits between two and ten seconds — your `export_timeout_ms`,
raised to two seconds if it is lower and capped at ten if it is higher — so an address that accepts
the connection and then never answers holds the command for that long, and three such addresses hold
it for up to thirty seconds. Nothing else in `af` waits on the network; the checks are deliberately
empty so that asking the question cannot add to what you are measuring. Every other `af` command
reads local files only.

If line two says `config: none`, `af` is recording step timing locally and sending nothing; if it
says `telemetry: off`, nothing is being recorded at all. See **Telemetry dashboard is empty**
under Troubleshooting for what to check next.

### Costs for non-Anthropic endpoints

Token counts stay exact no matter which provider a model profile points at — they are counted
by the same client that makes the call. **Dollar figures do not.** Where a request does not
report its own cost, the figure is worked out from token counts using Anthropic pricing, so a
profile redirected to another provider or to a local model produces a dollar number computed
on the wrong basis. A model with no listed price shows an empty cost rather than a misleading
zero. The price list is yours to edit — `.agentfactory/telemetry/views/pricing.json`, the one
seeded file `quickstart.sh` never overwrites — then re-run `quickstart.sh` to republish the
views. Treat tokens as the number to trust and dollars as a guide.

## Session statusline

Every Claude Code session in the factory can render a two-line **statusline** at the bottom of its
pane — a compact readout of what the agent is doing and what it is costing. It is on by default: a
fresh `af install --init` seeds it and provisions each agent's session to render it. Turning it on or
off changes nothing about how agents work.

**What it shows.** Two lines. The first is the identity/activity row — `model`, `dir`, `branch`,
`diff`, `elapsed`; the second is the context/cost row — `context`, `session`, `daily`. Those eight
elements are the default set, in that order, and they are also the *complete* set: every element the
statusline knows how to render is rendered by default.

| Element | Row | What you see | When it is dropped |
|---|---|---|---|
| `model` | 1 | the model's display name | the session reports no model |
| `dir` | 1 | the project directory | no directory in the session payload |
| `branch` | 1 | the current git branch | detached HEAD, or not a repository |
| `diff` | 1 | `+10 -2` — lines added and removed this session | nothing added and nothing removed |
| `elapsed` | 1 | `T 1h 06m` — how long this session has been going, in the #591 screenshot format (`T <H>h <MM>m`; under an hour, `T <N>m`) | the session reports no duration |
| `context` | 2 | `███░░░░░░░ 25% (1k/200k)` — context-window occupancy; the denominator is the window the host believes for this model, which a gateway profile can raise with `CLAUDE_CODE_MAX_CONTEXT_TOKENS` | the session reports no percentage (a genuine **0%** still renders) |
| `session` | 2 | `$ 1.23 · 12k tok` — this session's cost and cumulative tokens | both halves are zero (each drops on its own) |
| `daily` | 2 | `D $ 4.56 · 1.2M tok` — every statusline session's cost and tokens today | both halves are zero (each drops on its own) |

Any element whose source is missing or zero is dropped rather than shown as a hollow figure, and its
separator goes with it — you never see a stranded `|` where a number should have been.

### Turning it on and off

```bash
af statusline status   # is it on, which elements are shown, a self-test, the token counter, stale agents
af statusline on       # render the statusline, factory-wide
af statusline off      # stop rendering it
```

`af statusline on|off` takes effect on the very next render — nothing to restart.

### Choosing which elements are shown

Settings live in `.agentfactory/statusline.json`. A fresh factory has no such file, and that is not a
gap: an **absent file means the full default**, so you only ever create one in order to depart from it.
`elements` is an ordered list, not a set — reordering it reorders the pane, and dropping a name stops
rendering it. Send a complete document with `af config statusline set`:

```json
{
  "elements": ["model", "dir", "branch", "diff", "elapsed", "context", "session", "daily"]
}
```

```bash
echo '{"elements":["model","dir","branch","diff","elapsed","context","session","daily"]}' \
  | af config statusline set
```

Send the whole document, not just the key you want to change: a document with no `"elements"` key is
rejected rather than applied, so a misspelled key cannot silently blank every pane. Read the effective
configuration back — defaults included — with `af config statusline get`; `get | set` is a round trip.
`{"elements":[]}` is the explicit "render nothing".

### Upgrading an existing factory

`af install --init` re-provisions every configured agent to wire the statusline hook and seeds the
gate. `af statusline status` lists any agent still on an older binary's settings; those self-heal on
their next dispatch, or run `af install --init` again.

### Reading the cost and token figures

The cost and token figures are conservative: an absent or zero cost shows nothing (never `$0.00`), and
a `~` prefixes the cost when `ANTHROPIC_BASE_URL` is set — a redirected endpoint means the dollar
figure is estimated from token counts. The `daily` figure is factory-wide but scoped to sessions that
render a statusline, so it is a floor on the day's spend, not a full total. As with telemetry, treat
tokens as the number to trust and dollars as a guide. If a figure looks wrong, `af statusline status`
runs the same pipeline loudly and names whatever the pane is silent about.

### Color

The bar and the figures render in ANSI color by default — the cyan model, blue directory, magenta
branch, green/red diff, the green context bar, and the gray secondary figures of the #591 screenshot.
Color is **on** whenever the `color` key is absent, so a fresh factory is colored with no configuration.

Two ways to turn it off, for a terminal or a captured log that mangles escape sequences:

- Set `NO_COLOR` in the environment (to any value) — the standard per-terminal kill-switch, honored at
  render time without touching the stored config.
- Send `"color": false` alongside `"elements"` in the document you give `af config statusline set` — a
  persisted, factory-wide choice. Omitting the key (or `"color": true`) leaves color on.

Color is applied only *after* untrusted strings (branch, directory) are sanitized, so a hostile branch
name can never smuggle in its own escape sequences — the only escape bytes in a rendered line are the
statusline's own fixed palette.

## Troubleshooting

### Telemetry dashboard is empty

Work down the layers. `af telemetry status` first: `telemetry: off` means nothing is being
recorded — run `af telemetry on`. `config: none` means there is no `.agentfactory/telemetry.json`,
so step timing is kept locally and nothing is sent anywhere — re-run `quickstart.sh` without
`--no-telemetry`, or write the file yourself. Next, **agents started before you turned telemetry
on keep running without it** — that is the single most common cause of a factory that looks
enabled and reports nothing; restart them with `af down <agent>` then `af up <agent>`. Then
check the backend is actually up: `tmux has-session -t telemetry` and
`curl -s http://127.0.0.1:5080/healthz`; for the bundled backend on this machine, `af up` and the
watchdog's periodic tick relaunch it automatically when the gate is on, so a reboot should
self-heal within the next `af up` or the next ~30s tick — if it is still down after that, fall
back to a login shell (`bash -l`). If `endpoint` points at your own stack, none of that applies —
nothing here restarts it, and `bash -l` would start the bundled backend instead. If the
timing views are empty while the token and
cost views have data, read step timing locally with `af telemetry report` and look for a
`warning: telemetry export failed:` line on stderr from the last `af done` — that message names
the reason `af`'s own step records did not reach the backend. `af telemetry report` is never
gated, so it works even when everything above is misconfigured.

