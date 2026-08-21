# Using OpenAI models via LiteLLM

Agentfactory agents run the Claude Code CLI, which only speaks the Anthropic Messages
API. To run an agent on an OpenAI model, you deploy a LiteLLM proxy that translates
Anthropic Messages → OpenAI, and register a `models.json` profile whose
`ANTHROPIC_BASE_URL` points at it. `./quickstart.sh --litellm` automates the whole
setup; this file explains what it does, how to pick models, and how to launch
agents on the profile.

## Setup

Run inside the agentfactory container (OpenAI **API** billing — a ChatGPT
subscription cannot be used):

```bash
./quickstart.sh --litellm
```

On an already-initialized factory you can do this through a redeploy instead:
`af install --agents --litellm`. It asks for your OpenAI API key the first time
and reuses the stored key on later runs.

The normal bootstrap runs first; the gateway setup runs last, is idempotent, and
can be rerun anytime. It:

- installs `litellm[proxy]==1.93.0` (a recreated container loses pip installs —
  rerun the flag after recreating)
- collects your OpenAI API key — from `$OPENAI_API_KEY` if set, else an
  interactive prompt — and stores it at `.agentfactory/secrets/openai.key`
  (0600; only the litellm process ever reads it, agents never see it)
- generates the LiteLLM **master key** once, at
  `.agentfactory/secrets/litellm.key` — the bearer token agents present to the
  gateway; without it the proxy would be an open relay for your OpenAI credits
- seeds `.agentfactory/litellm.yaml` and the `codex` profile in
  `.agentfactory/models.json` when absent (your edits are never overwritten)
- starts the proxy in a detached tmux session named `litellm` on loopback
  port 4000, then smoke-tests the full translation path (`/v1/messages` →
  OpenAI, `max_tokens=16`) and finishes with `af config models check codex`.
  The 16 is a floor, not a rounding: a model routed through OpenAI's Responses
  API rejects a smaller `max_output_tokens` outright, and a rejected smoke test
  reads exactly like a model the gateway does not serve
- installs a login-shell guard so the gateway relaunches after a container
  restart

## Pick your models

Two files, both factory-root-relative, and their model ids must stay in sync:

- `.agentfactory/litellm.yaml` — what the gateway serves (`model_list`). API
  keys are referenced from env (`api_key: os.environ/OPENAI_API_KEY`), never
  literals in the file.
- `.agentfactory/models.json` — the `codex` profile, a plain map of environment
  exports. Every model id it names must match a `model_name` entry in
  `litellm.yaml`.

Naming one model in both files is necessary and **not sufficient**. A session
asks for models by *class*, not only by the id you configured: a sub-agent
spawn, a slash command or a plan step can each ask for an opus-, sonnet-,
haiku- or fable-class model, and Claude Code answers by reading a per-class
environment key. **A class key it finds unset falls back to the host's own
built-in `claude-…` id** — which a gateway with a closed `model_list` refuses,
killing whatever asked for it. That is issue #598, and it can happen on a
factory whose two configured ids are perfectly in sync.

### Every class, and what covers it

| Class | Profile key | What asks for it | If you leave the key unset |
|---|---|---|---|
| main | `ANTHROPIC_MODEL` | every ordinary turn of the session | Nothing derives it. Declare it — it is the id every row below falls back to |
| small/background | `ANTHROPIC_SMALL_FAST_MODEL` (deprecated) | cheap background work the host does on its own | Copied from `ANTHROPIC_DEFAULT_HAIKU_MODEL`, else from `ANTHROPIC_MODEL` |
| opus | `ANTHROPIC_DEFAULT_OPUS_MODEL` | anything requesting the opus tier by name — a sub-agent, a slash command, `--model opus` | Copied from `ANTHROPIC_MODEL` |
| sonnet | `ANTHROPIC_DEFAULT_SONNET_MODEL` | anything requesting the sonnet tier by name | Copied from `ANTHROPIC_MODEL` |
| haiku | `ANTHROPIC_DEFAULT_HAIKU_MODEL` | anything requesting the haiku tier by name | Copied from `ANTHROPIC_SMALL_FAST_MODEL`, else from `ANTHROPIC_MODEL` |
| sub-agent default | `CLAUDE_CODE_SUBAGENT_MODEL` | every sub-agent spawn that names no model of its own | **Never derived.** It stays unset, and each spawn resolves through the class keys above |
| fable | *(no key exists)* | a fable-class spawn, e.g. a `/…-fable-…` sub-agent | **No profile key can cover it.** Only a gateway alias for the exact `claude-fable-…` id answers this class |

Four of those rows — small/background, opus, sonnet and haiku — are filled
automatically for any profile carrying an `ANTHROPIC_BASE_URL`, by the ladder in
the last column. `ANTHROPIC_MODEL` is the source it copies from, so declare that
yourself. `CLAUDE_CODE_SUBAGENT_MODEL` is never derived — it overrides a
sub-agent's own model choice, so filling it would route every deliberately cheap
spawn to your main model; set it yourself only if you want one model for all
sub-agents. The fable class has no key at all — it is covered gateway-side or not
at all.

**Derivation cannot rescue a profile with no main model.** If an endpoint
profile declares no `ANTHROPIC_MODEL`, the opus and sonnet rows have no source
to copy from and are left unset — the profile launches with those classes empty
and requests for them reach your gateway as `claude-…` ids.

`af config models set` tells you which of the two you have, and the wording
distinguishes them:

- **`endpoint profile declares no ANTHROPIC_MODEL, so <classes> are left
  uncovered`** — the bad case above. Nothing derives them.
- **`endpoint profile leaves <classes> to derivation from ANTHROPIC_MODEL`** —
  the ordinary case, and not a complaint. It is telling you which classes will
  answer with your main model, so you can declare one yourself if that is the
  wrong answer.

Both shapes end by reminding you that fable-class requests are served only if
the gateway aliases `claude-fable-…` ids, because no profile edit can change
that. Neither shape ever mentions the sub-agent default — it is never derived,
so there is nothing to warn about.

`ANTHROPIC_SMALL_FAST_MODEL` is **deprecated upstream**; prefer
`ANTHROPIC_DEFAULT_HAIKU_MODEL` as the key you actually teach and set. The two
cross-derive from each other, so declaring either covers both — which is why
the deprecated key never needs to appear in a profile you write today.

### A profile that covers every class

Declaring only a main and a small model is enough, because the ladder fills the
rest. Declare a class key yourself when derivation's answer is wrong for you —
a cheaper backend for haiku-class traffic, a stronger one for opus-class:

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
      "ANTHROPIC_API_KEY": "",
      "ANTHROPIC_MODEL": "gpt-4o",
      "ANTHROPIC_DEFAULT_HAIKU_MODEL": "gpt-4o-mini"
    },
    "codex-explicit": {
      "ANTHROPIC_BASE_URL": "http://localhost:4000",
      "ANTHROPIC_AUTH_TOKEN": "file:.agentfactory/secrets/litellm.key",
      "ANTHROPIC_API_KEY": "",
      "ANTHROPIC_MODEL": "gpt-4o",
      "ANTHROPIC_DEFAULT_OPUS_MODEL": "gpt-4o",
      "ANTHROPIC_DEFAULT_SONNET_MODEL": "gpt-4o",
      "ANTHROPIC_DEFAULT_HAIKU_MODEL": "gpt-4o-mini",
      "ANTHROPIC_SMALL_FAST_MODEL": "gpt-4o-mini"
    }
  },
  "agents": {
    "rapid-implement": "codex"
  }
}
```

`codex` is the profile `./quickstart.sh --litellm` seeds — main and small only,
every other class derived. `codex-explicit` is the same profile with nothing
left to the ladder. Both cover all four derivable classes; neither covers the
fable class, because no key can. That is a whole document, not a fragment, and
`af config models set` replaces the file with what it reads on stdin — edit the
registry you already have rather than pasting this over it.

### Alias the claude ids on the gateway

Two things still send `claude-…` ids at your gateway however carefully you fill
the profile: the fable class, which has no key, and any request that names a
literal model id rather than a class. A gateway with a closed `model_list`
refuses those by name. Aliasing them is what closes the gap.

A factory bootstrapped by `./quickstart.sh --litellm` gets these written for
you. Check rather than assume: the seed only writes `litellm.yaml` **when the
file is absent**, so that your edits are never overwritten — which also means a
factory bootstrapped before the alias block existed keeps its old
`model_list`, and re-running quickstart will not add them. Compare yours
against this — entry for entry; the seeded file also carries a comment block
above the aliases that is not reproduced here:

```yaml
model_list:
  - model_name: gpt-4o                # substitute the OpenAI model id you want agents on
    litellm_params:
      model: openai/gpt-4o
      api_key: os.environ/OPENAI_API_KEY
  - model_name: gpt-4o-mini           # small model for Claude Code's background calls
    litellm_params:
      model: openai/gpt-4o-mini
      api_key: os.environ/OPENAI_API_KEY

  - model_name: claude-opus-5
    litellm_params:
      model: openai/gpt-4o
      api_key: os.environ/OPENAI_API_KEY
  - model_name: claude-sonnet-5
    litellm_params:
      model: openai/gpt-4o
      api_key: os.environ/OPENAI_API_KEY
  - model_name: claude-opus-4-8
    litellm_params:
      model: openai/gpt-4o
      api_key: os.environ/OPENAI_API_KEY
  - model_name: claude-fable-5
    litellm_params:
      model: openai/gpt-4o
      api_key: os.environ/OPENAI_API_KEY
  - model_name: claude-haiku-4-5       # haiku-class requests go to the small backend
    litellm_params:
      model: openai/gpt-4o-mini
      api_key: os.environ/OPENAI_API_KEY
```

If you run your own gateway on different backends, the aliases are the same
five ids pointed at whatever your main and small models are called. For a
gateway serving the `gpt-5.6-*` family:

```yaml
model_list:
  - model_name: gpt-5.6-sol            # main/agentic model
    litellm_params:
      model: openai/gpt-5.6-sol
      api_key: os.environ/OPENAI_API_KEY
  - model_name: gpt-5.6-luna           # background/haiku-class calls
    litellm_params:
      model: openai/gpt-5.6-luna
      api_key: os.environ/OPENAI_API_KEY

  - model_name: claude-opus-5
    litellm_params:
      model: openai/gpt-5.6-sol
      api_key: os.environ/OPENAI_API_KEY
  - model_name: claude-sonnet-5
    litellm_params:
      model: openai/gpt-5.6-sol
      api_key: os.environ/OPENAI_API_KEY
  - model_name: claude-opus-4-8
    litellm_params:
      model: openai/gpt-5.6-sol
      api_key: os.environ/OPENAI_API_KEY
  - model_name: claude-fable-5
    litellm_params:
      model: openai/gpt-5.6-sol
      api_key: os.environ/OPENAI_API_KEY
  - model_name: claude-haiku-4-5       # haiku-class requests go to the small backend
    litellm_params:
      model: openai/gpt-5.6-luna
      api_key: os.environ/OPENAI_API_KEY
```

Point the `claude-…` aliases at your main backend — only the haiku-class alias
belongs on the small one, because the host sizes its context window from the
`claude-` id it asked for. Enumerate ids one per line (a wildcard matches
nothing), and alias every `claude-…` id your registry's direct profiles name:
those are exactly the ids `af config models check` demands. Keep the
`claude-haiku-4-5` alias even though no profile names it — it is defence-in-depth
for a host that asks for a haiku id by name.

### Reading the verdict

`af config models check codex` prints one line per class, one per `claude-…` id
your registry names, and exits non-zero if any of them cannot be served. On a
factory whose registry still has the seeded direct profiles — so it names
`claude-fable-5` — and a gateway that has not been aliased yet:

```
af config models check — transport-level only (necessary, not sufficient for fitness).
profile "codex": reachable; model "gpt-4o" present at http://localhost:4000
profile "codex": class small/background → "gpt-4o-mini" (derived from ANTHROPIC_DEFAULT_HAIKU_MODEL): served
profile "codex": class opus → "gpt-4o" (derived from ANTHROPIC_MODEL): served
profile "codex": class sonnet → "gpt-4o" (derived from ANTHROPIC_MODEL): served
profile "codex": class haiku → "gpt-4o-mini" (declared): served
profile "codex": alias claude-opus-5 → NOT SERVED — alias the id on the gateway (LiteLLM: add a model_name entry to litellm.yaml, see USING_LITELLM.md)
profile "codex": alias claude-sonnet-5 → NOT SERVED — alias the id on the gateway (LiteLLM: add a model_name entry to litellm.yaml, see USING_LITELLM.md)
profile "codex": fable-class → no gateway alias for "claude-fable-5": NOT SERVED — alias the id on the gateway (LiteLLM: add a model_name entry to litellm.yaml, see USING_LITELLM.md)
profile "codex": alias claude-opus-4-8 → NOT SERVED — alias the id on the gateway (LiteLLM: add a model_name entry to litellm.yaml, see USING_LITELLM.md)
```

Each line's parenthesis is where the id came from: `declared` if you wrote it,
`derived from <KEY>` if the ladder copied it. A non-zero exit means at least one
class or `claude-…` alias is unserved — alias it on the gateway and re-check.

`--live` adds a real `/v1/messages` call at `max_tokens=16` for each served id;
without it the check is transport and coverage only, which is necessary and not
sufficient for fitness.

After editing `litellm.yaml`, restart and revalidate:

```bash
tmux kill-session -t litellm && ./quickstart.sh --litellm
```

**Tell the host your backend's real context window.** Claude Code sizes
its context accounting from the model id, and for an id it does not
recognise as its own — anything not prefixed `claude-` — it assumes
`200000` tokens. That is almost never what your gateway actually serves.
Two more keys in the same profile correct it:

```json
"codex": {
  "ANTHROPIC_BASE_URL": "http://localhost:4000",
  "ANTHROPIC_AUTH_TOKEN": "file:.agentfactory/secrets/litellm.key",
  "ANTHROPIC_MODEL": "gpt-4o",
  "ANTHROPIC_DEFAULT_HAIKU_MODEL": "gpt-4o-mini",
  "ANTHROPIC_API_KEY": "",
  "CLAUDE_CODE_AUTO_COMPACT_WINDOW": "360000",
  "CLAUDE_CODE_MAX_CONTEXT_TOKENS": "400000"
}
```

`CLAUDE_CODE_MAX_CONTEXT_TOKENS` declares the model's real window — the
number the host divides by for the occupancy percentage the statusline
shows and context recovery reads. `CLAUDE_CODE_AUTO_COMPACT_WINDOW` is
the token count at which the host auto-compacts. Every value is a quoted
decimal string, and `af config models set` rejects an auto-compact window
outside `[100000, 1000000]` rather than saving a number that would not
take effect. Set the pair to your backend's smallest real window; a
foreign id whose auto-compact window has no companion silently caps at
`200000`. Apply them like any other profile edit:

```bash
jq '.models.codex.CLAUDE_CODE_MAX_CONTEXT_TOKENS = "400000"
    | .models.codex.CLAUDE_CODE_AUTO_COMPACT_WINDOW = "360000"' \
  .agentfactory/models.json | af config models set
```

Both keys are optional and per-profile: omit them and the host keeps its
own assumption. Prefer omitting over setting `""` — an empty value is
still an emitted export, not an absent one.

## Use it

```bash
af sling --agent rapid-implement --model codex "task"    # one-off
af up <agent> --model codex                              # session with the profile
```

Sticky assignment — map the agent in `models.json`:

```bash
jq '.agents["rapid-implement"] = "codex"' .agentfactory/models.json | af config models set
```

Precedence (highest wins): `--model` flag → `.runtime/model_override` marker →
`models.json` agents map → legacy `agents.json` endpoint → `models.json` default →
shell-rc globals.

Inside a running session, `/status` should show the gateway as the Anthropic base
URL. Re-run the transport probe anytime with `af config models check codex`.

## Gotchas

- **Never enable `set -x` in an agent pane** — the trace would expand and print
  the dereferenced secret.
- **Expect degraded agent behavior.** Claude Code's prompts and tool loop are
  tuned for Claude models; OpenAI models measurably underperform in this harness
  vs their own (Codex CLI). Start with low-stakes tasks.
- **A degraded backend looks exactly like context exhaustion, and burns context
  while it does.** Every turn comes back with a useless, token-consuming reply,
  so the agent's context occupancy climbs steadily while nothing advances. The
  factory never reads that growth as progress — only a step-anchored signal
  confirms a recovery — so an agent wedged behind a bad gateway will be recycled,
  and if it keeps re-stalling its recovery breaker latches instead of looping.
  A `RECOVERY HALTED` mail naming a gateway-backed agent is worth reading as a
  gateway symptom first. See
  [Context exhaustion recovery](USING_RECOVERY.md#context-exhaustion-recovery).
- **A misdeclared context window has two failure shapes, and both hide
  behind that same wedge.** Declare a window smaller than the backend
  really serves and compaction fires early and repeatedly — churn, where
  the agent keeps shedding context it still needed. Declare one larger,
  or leave a foreign id above 200k unpaired so it caps, and the backend
  overflows before the host ever compacts — blind, where nothing fires
  at all. For agents holding a live session, `af statusline status`
  reports both: each one whose declared window disagrees with the window
  the host reports back, and each profile behind them whose declared
  window will silently cap. Setting the keys
  is not the same as verifying them — af emits the exports, and only a
  per-profile measurement shows whether the host honored them.
- **Deleting a compaction key from *every* profile does not clear it from a live
  session.** The launch line only `unset`s keys some profile still declares, so a running
  agent keeps the old value across handoff/compact/watchdog respawns until you fully
  recycle it — run `af down && af up` for that agent.
- **An out-of-range window fails the whole registry on load, not just at `set`.** After an
  upgrade, a `models.json` written before this validation existed that carries a window
  outside `[100000, 1000000]` fails to load — a selecting `af sling --model P` fails fast,
  every other launch warns and falls back to the global default model. Fix the value to
  restore per-profile launches.
- **Moving the gateway off loopback changes the rules:** a non-loopback endpoint
  requires a fitness attestation before a selecting launch (`af config models
  attest codex`, or `--skip-fitness` to override loudly), and literal tokens are
  rejected in favor of `file:` references.
- The watchdog recognizes gateway failures (connection refused, 502/503/504,
  LiteLLM error prefixes) and names the endpoint in its escalation mail.
- Prompt caching, streaming, and tool use are confirmed working through LiteLLM's
  OpenAI route (LiteLLM claude-code compatibility matrix, v1.91.1); extended
  thinking / `--effort` is the known-degraded area.
- Anthropic-profile agents are untouched by any of this — the profile mechanism is
  per-agent.
