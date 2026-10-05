# Using Agentfactory: Model Profiles

Operator guide for the model registry (`.agentfactory/models.json`): profiles, model
classes, and gateway coverage. Split out of [USING_AGENTFACTORY.md](USING_AGENTFACTORY.md).
For the gateway-side runbook see [USING_LITELLM.md](USING_LITELLM.md); for the context-window
keys a profile can declare (`CLAUDE_CODE_MAX_CONTEXT_TOKENS`, `CLAUDE_CODE_AUTO_COMPACT_WINDOW`),
see [USING_RECOVERY.md](USING_RECOVERY.md#declaring-a-backends-real-context-window). A profile may
also declare `AF_BACKEND_POOL_TOKENS` — the operator-set size of the shared backend pool the
sub-agent-dispatch gate divides among concurrent sessions, distinct from the per-request context
window above (see [Token economics](USING_TOKENOMICS.md#token-economics)). Two companion keys ride
beside it and tune the same gate: `AF_BACKEND_CHILD_FLOOR_TOKENS` — the minimum free pool a launch
must leave behind so the next child still has room to seat (defaults to 50,000 tokens when a pool is
declared but the floor is not; a positive decimal, never zero); and `AF_DISABLE_PARALLEL_SUBAGENTS`
— set to `"1"`, a hard cap that runs sub-agents strictly one at a time on that backend (a second is
refused while any sibling still runs) instead of dividing the pool arithmetically. Both are inert
wherever `AF_BACKEND_POOL_TOKENS` is absent.

## Model profiles and classes

A profile in `.agentfactory/models.json` is a plain map of environment exports — a new model is a
config edit, not a code change. A launch emits the keys the selected profile carries.

**A session asks for models by class, not only the one id you set.** A sub-agent, slash command or
plan step can request an opus-, sonnet-, haiku- or fable-class model, and Claude Code answers each
from a per-class environment key. Any class key it finds unset falls back to the host's built-in
`claude-…` id. On an Anthropic-direct profile that is correct; on a **gateway profile** (one
declaring a non-empty `ANTHROPIC_BASE_URL`) it is a live failure — a gateway serving a closed
`model_list` refuses a `claude-…` id by name, and whatever asked for that class dies.

| Class | Profile key | If you leave the key unset (gateway profiles) |
|---|---|---|
| main | `ANTHROPIC_MODEL` | Nothing derives it — declare it; every row below falls back to this id |
| small/background | `ANTHROPIC_SMALL_FAST_MODEL` (deprecated) | Copied from `ANTHROPIC_DEFAULT_HAIKU_MODEL`, else from `ANTHROPIC_MODEL` |
| opus | `ANTHROPIC_DEFAULT_OPUS_MODEL` | Copied from `ANTHROPIC_MODEL` |
| sonnet | `ANTHROPIC_DEFAULT_SONNET_MODEL` | Copied from `ANTHROPIC_MODEL` |
| haiku | `ANTHROPIC_DEFAULT_HAIKU_MODEL` | Copied from `ANTHROPIC_SMALL_FAST_MODEL`, else from `ANTHROPIC_MODEL` |
| sub-agent default | `CLAUDE_CODE_SUBAGENT_MODEL` | **Never derived** — it overrides a sub-agent's own model choice, so filling it would route every deliberately cheap spawn to your most expensive backend |
| fable | *(no key exists)* | **No profile key can cover it** — only a gateway alias for the exact `claude-fable-…` id answers this class |

So **a gateway profile emits more than the keys you wrote**: the four derivable class keys are
filled from the ladder above before launch, so a profile declaring just a main and a small model
covers every *derivable* class. Two gaps remain — a profile with no `ANTHROPIC_MODEL` leaves opus
and sonnet empty, and no profile key reaches the fable class at all.

Three surfaces tell you where a gateway profile stands, and none of them requires launching an
agent into the failure:

- `af config models set` names, as you save, which classes a profile leaves to derivation — and
  says "left uncovered" instead when there is no `ANTHROPIC_MODEL` for them to derive from. The
  first is information, not a complaint; only the second is a problem.
- `af config models check <profile>` prints one verdict per class — the effective id, where it came
  from (`declared` or `derived from <KEY>`), and whether your gateway serves it — and exits non-zero
  if any class or alias is unserved.
- A selecting launch warns when the profile's last coverage check is missing, stale or failing.

For the gateway-side half — which `claude-…` ids to alias, where to point them, and why the fable
class can only be covered there — see [USING_LITELLM.md](USING_LITELLM.md).

### What else `af config models set` refuses

Beyond validating the document, `af config models set` enforces these extra rules; each names the
offending profile and the fix:

- **A profile still pinned by `dispatch.json` cannot be dropped or renamed.** Define the profile,
  or drop the pin with `af config dispatch set` first.
- **`****` is refused as an auth token** — it is the placeholder `af config models show` prints, not
  a real secret. Send the real value or a `file:` reference.
- **A rewritten profile loses its fitness attestation.** Re-run `af config models attest <profile>`
  after verifying the new endpoint. Editing only the `agents` map or `default` clears nothing.
- **A profile cannot name an upstream gateway credential.** `OPENAI_API_KEY`, `CHATGPT_TOKEN_DIR`,
  `CHATGPT_AUTH_FILE`, `CHATGPT_API_BASE` and `CODEX_HOME` are reserved — the gateway reads them
  from `.agentfactory/secrets/` (see [USING_LITELLM.md](USING_LITELLM.md)), never from a profile,
  because a profile key rides into every agent's launch line. **Migration note:** this is enforced
  at *load*, not only at write, so an existing `models.json` that already names one of these five
  keys makes every `af` verb fail until you remove the key from the offending profile.
- **A profile cannot name `CLAUDE_CODE_PLUGIN_DIRS` or `CLAUDE_CONFIG_DIR`**, whatever the value
  (even `""`). The plugin channel owns them: plugin dirs reach a session only through an installed
  integration (see [USING_PLUGINS.md](USING_PLUGINS.md)), and a profile key would ride into every
  agent's launch line and replace the agent's plugins or Claude config dir. Install an integration
  instead. **Migration note:** this is enforced at *load*, not only at write. An existing
  `models.json` that names either key is rejected: a launch that selects a profile (`--model`)
  fails, and any other launch warns `ignoring models.json` and starts with the global default
  model — no profile applies — until you remove the key from the offending profile.

`attest` is a transport claim only — it records that the operator verified a non-loopback
endpoint's transport, nothing about upstream auth. A subscription-mode gateway's credential health
is measured live by `af config models check` / `af gateway auth status`, never by `attest`.

### One key tokenomics may lower: `CLAUDE_CODE_EFFORT_LEVEL`

A declared `CLAUDE_CODE_EFFORT_LEVEL` is exported verbatim like every other key, on every launch
path — `af sling`, `af up`, and every handoff / compact / watchdog relaunch — with one exception:
while the tokenomics effort arm is on (`af tokenomics on` for the umbrella, plus `"effort"` not set to
`"off"` in `startup.json`'s `tokenomics` block), the effort actuator can replace it in place with a
lower level chosen from the next step's learned history. It never raises the declared level, and the
declaration stands whenever the actuator selects nothing. `af tokenomics status` names which half is
dark.

With the arm off the declared level is untouched, whichever way it is off: no
`.agentfactory/.tokenomics` file (a fresh factory), `af tokenomics off`, `tokenomics.enabled: "off"`,
or `tokenomics.effort: "off"`. A `startup.json` that cannot be read also leaves the arm off for any
launch that still happens — `af sling` or a handoff — but `af up` refuses to start any agent until it
loads. The arm gates the actuator's reduction, never your configuration (#707). Omit the key rather
than declaring it `""`: an empty value is exported as `CLAUDE_CODE_EFFORT_LEVEL=''` whenever the
actuator selects nothing, how the host reads an empty level is unverified, and omission is what
defers to the host. Telemetry keeps the two apart without touching the key: `session_start` and
`step_end` carry the level the session ran at, and a `reduce_effort` record is written only for a
launch at which the actuator chose the level. That launch also exports `AF_EFFORT_OBJECTIVE`,
`AF_EFFORT_STEP_LABEL` and `AF_EFFORT_FORMULA` beside the level; they are factory-owned names, and a
profile that declares one is rejected as reserved for the session manager.

A fixed effort level belongs in the profile, not your shell rc. Once any profile in `models.json`
declares the key, an agent whose own profile does not declare it has it `unset` at launch — the same
factory-owned-key hygiene as the compaction keys ([USING_RECOVERY.md](USING_RECOVERY.md)) — so a
shell-rc value reaches only a factory where no profile declares it. `quickstart.sh` writes one
(`export CLAUDE_CODE_EFFORT_LEVEL="${CLAUDE_CODE_EFFORT_LEVEL:-xhigh}"`) into your shell rc; it is
the default only until a profile declares the key, after which every launch replaces it with the
agent's declared level or unsets it. With the arm on, the actuator can
also export a level it chose for an agent whose profile declares none: there is no ceiling to stay
under.
