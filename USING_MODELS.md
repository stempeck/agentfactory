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

Beyond validating the document, `af config models set` enforces three extra rules; each names the
offending profile and the fix:

- **A profile still pinned by `dispatch.json` cannot be dropped or renamed.** Define the profile,
  or drop the pin with `af config dispatch set` first.
- **`****` is refused as an auth token** — it is the placeholder `af config models show` prints, not
  a real secret. Send the real value or a `file:` reference.
- **A rewritten profile loses its fitness attestation.** Re-run `af config models attest <profile>`
  after verifying the new endpoint. Editing only the `agents` map or `default` clears nothing.

### One key a profile does not own outright: `CLAUDE_CODE_EFFORT_LEVEL`

Every other key in a profile is exported verbatim. This one is filtered: a launch carries it only
when the tokenomics effort arm is on — `af tokenomics on` for the umbrella, plus `"effort"` not set
to `"off"` in `startup.json`'s `tokenomics` block. And when a launch does carry it, the value is
not always the one the profile declared: with the arm on, the effort actuator can replace it in
place with a lower level chosen from the next step's learned history — it never raises the declared
level, and the profile's declaration stands only when the actuator selects nothing. With the arm off
the key is dropped from the launch env and, on a reused pane, actively unset. `af tokenomics status`
names which half is dark.

It is filtered because it is one arm of a running experiment (#668 D16), and an experiment whose
control group receives the treatment measures nothing. The filter applies to every launch path — `af
sling`, `af up`, and every handoff / compact / watchdog relaunch — not only the relaunch the arm
acts on, so an agent cannot carry the treatment in from its first turn and never be relaunched out
of it.

If you want a fixed effort level for reasons unrelated to tokenomics, set it in your shell rc rather
than a profile: with the effort arm off the session inherits it untouched. With the arm on, though,
the actuator can append its own chosen level to the launch and override the inherited value for that
session — shell rc is not a way around the experiment.
