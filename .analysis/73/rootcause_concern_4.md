# Concern #4 Investigation: Mapped agents may not be provisioned on a fresh install

**Investigated by**: Sub-agent
**Date**: 2026-06-28

## Verdict: VALIDATED

## Summary
A fresh `af install --init` registers ONLY `manager` and `supervisor` in `agents.json`
and lists only `manager` in `startup.json`. The four agents the proposed default
dispatch.json maps labels to — `rapid-soldesign-plan`, `rapid-implement`, `ultra-review`,
`rapid-increment` — are NOT registered in agents.json on a fresh factory. The dispatcher
gates on agent presence at TWO points: (1) `ValidateDispatchConfig` runs at the very start
of every dispatch cycle and **aborts the entire cycle** if any mapping references an agent
absent from agents.json (`internal/config/dispatch.go:101-102`), and (2) even past that gate,
`af sling --agent <name>` calls `resolveSpecialistAgent`, which fails with
`agent %q not found in agents.json` (`internal/cmd/sling.go:261`). Neither `af sling`
nor the dispatcher auto-provisions or auto-registers an absent agent. The only code path that
registers a formula-derived agent into agents.json is `af formula agent-gen` (driven by
`agent-gen-all.sh`, which is invoked only by `af install --agents` — NOT by `af install --init`
and NOT by `quickstart.sh`). Therefore, with the proposed default mappings, dispatch would
FAIL on a fresh factory before it ever queries GitHub or slings anything.

## 5-Whys Analysis

### Why #1: Why might dispatch fail to sling the mapped agents on a fresh install?
Because the mapped agents are not present in `agents.json`. `af install --init` writes a
literal starter `agents.json` containing only `manager` and `supervisor`
(`internal/cmd/install.go:143`):
```
"agents.json": `{"agents":{"manager":{"type":"interactive",...},"supervisor":{"type":"autonomous",...}}}`
```
`startup.json` lists only `["manager"]` in its agents array (`internal/cmd/install.go:147`):
```
"startup.json": `{"agents":["manager"],"quality":"default","fidelity":"default","start_dispatch":true,"watchdog_agents":["manager","supervisor"]}`
```
The default dispatch.json on `--init` has empty mappings (`internal/cmd/install.go:145`);
the proposed mappings to rapid-soldesign-plan / rapid-implement / ultra-review / rapid-increment
are the change being evaluated, and none of those four agents is in the default agents.json.

### Why #2: Why does an absent mapped agent break dispatch (rather than being skipped)?
Because the dispatcher validates agent presence as a hard precondition. At the start of every
dispatch cycle, `runDispatch` loads agents.json and calls `ValidateDispatchConfig`
(`internal/cmd/dispatch.go:138-148`). That validator iterates every mapping and returns an error
the moment one references an unknown agent (`internal/config/dispatch.go:100-103`):
```
for _, m := range disp.Mappings {
    if _, ok := agents.Agents[m.Agent]; !ok {
        return fmt.Errorf("dispatch mapping references unknown agent %q", m.Agent)
    }
```
`runDispatch` returns this error immediately (`internal/cmd/dispatch.go:146-148`), BEFORE the
gh-auth check and BEFORE acquiring the dispatch-cycle lock — so the whole cycle aborts; no issue
is queried and nothing is slung.

### Why #3: Why doesn't `af sling --agent` create/register the agent on demand?
Because sling is a strict lookup, not a provisioner. `af sling --agent <name> --reset <url>`
(the exact argv the dispatcher runs, `internal/cmd/dispatch.go:422`) routes to
`dispatchToSpecialist` → `resolveSpecialistAgent`, which loads agents.json and fails if the
agent is missing (`internal/cmd/sling.go:259-266`):
```
entry, ok := agentsCfg.Agents[agentName]
if !ok {
    return ..., fmt.Errorf("agent %q not found in agents.json", agentName)
}
if entry.Formula == "" {
    return ..., fmt.Errorf("agent %q is not a specialist (no formula field in agents.json)", agentName)
}
```
There is no auto-add, auto-provision, or auto-up branch. (Sling does NOT require a running tmux
session — it launches one — and does NOT require a pre-existing on-disk agent dir — it creates the
worktree/agent dir. The ONE thing it strictly requires is an agents.json entry with a `formula` field.)

### Why #4: Why isn't the agents.json entry created during a fresh install / quickstart?
Because the only writer of formula-derived agents.json entries is `af formula agent-gen`, and it
is never invoked by `--init` or by the quickstart bootstrap.
- `af install --init` writes the formula TOMLs into `store/formulas/` (`internal/cmd/install.go:252-257`,
  `writeFormulas` from the embedded `install_formulas/` set, which DOES include all four formulas),
  but a formula sitting in the store does NOT register an agent. Registration is a separate step.
- `af formula agent-gen <name>` is what loads agents.json, adds an entry with `Formula: f.Name`,
  and saves it back (`internal/cmd/formula.go:214-246`). It also writes the role template.
- `agent-gen-all.sh` is the only thing that runs `af formula agent-gen` for every formula in the
  store (`agent-gen-all.sh:134-153`), and it is invoked only by `af install --agents`
  (`runInstallAgents` → `runAgentGenScript`, `internal/cmd/install.go:692-696`).
- `quickstart.sh` runs `af install --init` then provisions ONLY manager and supervisor via
  `af install manager` / `af install supervisor` (`quickstart.sh:441-470`). It does NOT run
  `agent-gen-all.sh` and does NOT call `af install --agents`.

### Why #5: Why can't the user just run `af install rapid-implement` to provision it?
Because `af install <role>` is a provisioner that itself REQUIRES the role to already exist in
agents.json. `runInstallRole` loads agents.json and aborts if the role is absent
(`internal/cmd/install.go:509-512`):
```
entry, ok := agents.Agents[role]
if !ok {
    return fmt.Errorf("agent %q not found in agents.json", role)
}
```
So on a fresh factory `af install rapid-implement` fails. The user must FIRST register the agent
(via `af formula agent-gen rapid-implement` or `af install --agents`, which registers all of them),
and only then can dispatch's mapping resolve. The root cause is that registration of the mapped
specialist agents into agents.json is a manual/`--agents` step that is NOT part of the `--init`
or quickstart fresh-install path.

## Evidence Gathered

| Finding | Source | Evidence |
|---------|--------|----------|
| `--init` agents.json contains only manager + supervisor | `internal/cmd/install.go:143` | `"agents.json": {"agents":{"manager":...,"supervisor":...}}` (no rapid-* / ultra-review) |
| `--init` startup.json agents list is `["manager"]` | `internal/cmd/install.go:147` | `"startup.json": {"agents":["manager"],...,"watchdog_agents":["manager","supervisor"]}` |
| `--init` dispatch.json default has empty mappings | `internal/cmd/install.go:145` | `"dispatch.json": {...,"mappings":[],...}` |
| `--init` DOES write all formula TOMLs (incl. the 4 mapped) to the store | `internal/cmd/install.go:252-257`, `install_formulas/` listing | writeFormulas copies embedded `install_formulas/*.formula.toml`; dir contains rapid-soldesign-plan, rapid-implement, ultra-review, rapid-increment |
| Formula in store ≠ registered agent; agent-gen is the registrar | `internal/cmd/formula.go:214-246` | LoadAgentConfig → build entry with `Formula:` → `AddAgentEntry` → `SaveAgentConfig` |
| Dispatch cycle aborts if a mapping agent is absent | `internal/config/dispatch.go:100-103`; `internal/cmd/dispatch.go:146-148` | `dispatch mapping references unknown agent %q`; returned before gh-auth/lock |
| ValidateDispatchConfig runs on the dispatch path (not just config-write) | `internal/cmd/dispatch.go:144-148` | same validator as `config_set.go:89` — "rule cannot drift" |
| `af sling --agent` requires agents.json entry; no auto-provision | `internal/cmd/sling.go:259-266` | `agent %q not found in agents.json` / not a specialist if no `formula` field |
| Dispatcher invokes exactly `af sling --agent <name> --reset <url>` | `internal/cmd/dispatch.go:422` | `args := []string{"sling", "--agent", agent, "--reset"}` |
| `af install <role>` requires the role to pre-exist in agents.json | `internal/cmd/install.go:509-512` | `agent %q not found in agents.json` |
| Only `af install --agents` runs agent-gen-all.sh (registers all formula agents) | `internal/cmd/install.go:692-696`; `agent-gen-all.sh:134-153` | runInstallAgents → runAgentGenScript; loop runs `af formula agent-gen` per formula |
| quickstart.sh provisions ONLY manager + supervisor; no --agents / agent-gen-all | `quickstart.sh:441-470` | `af install --init`, then `af install manager`, `af install supervisor` only |

## Tests Performed

| Test | Command | Result |
|------|---------|--------|
| What `--init` writes to agents.json | Read `internal/cmd/install.go:139-157` (starterConfigs map) | Only manager + supervisor; confirmed |
| What `--init` writes to startup.json | Read `internal/cmd/install.go:147` | `agents:["manager"]`; confirmed |
| Whether the 4 mapped formulas ship in the embedded set | `ls internal/cmd/install_formulas/` | All 4 present (rapid-soldesign-plan, rapid-implement, ultra-review, rapid-increment) |
| Whether sling auto-provisions an absent agent | Read `internal/cmd/sling.go:128-266` | No; hard fail at resolveSpecialistAgent (sling.go:261) |
| Whether dispatch pre-validates mapping agents | grep `ValidateDispatchConfig` + read `internal/config/dispatch.go:93-103` & `internal/cmd/dispatch.go:144-148` | Yes; aborts cycle on unknown agent |
| Whether quickstart registers the mapped agents | Read `quickstart.sh:414-471` | No; only manager + supervisor provisioned |
| Whether agent-gen-all.sh registers them and what invokes it | Read `agent-gen-all.sh:134-153`, `internal/cmd/install.go:692-696` | Yes via `af formula agent-gen`; invoked only by `af install --agents` |

## Conclusion

**Verdict: VALIDATED.**

On a fresh `af install --init` (the path quickstart.sh drives), the four agents the proposed
default dispatch mappings target — `rapid-soldesign-plan`, `rapid-implement`, `ultra-review`,
`rapid-increment` — are NOT registered in `agents.json` and NOT provisioned on disk. Only
`manager` and `supervisor` are. Although `--init` does copy all four formula TOMLs into
`.agentfactory/store/formulas/`, a formula in the store does not register an agent.

**Exact dependency for dispatch to sling the mapped agents on a fresh factory:** each mapped
agent must have an entry in `.agentfactory/agents.json` with a non-empty `formula` field. The
ONLY code path that creates such entries is `af formula agent-gen <name>` — run en masse by
`agent-gen-all.sh`, which is itself invoked only by `af install --agents`. Until that runs,
`ValidateDispatchConfig` (`internal/config/dispatch.go:101-102`) rejects the dispatch.json with
`dispatch mapping references unknown agent`, aborting the dispatch cycle at the top
(`internal/cmd/dispatch.go:146-148`) before any GitHub query — and even past validation,
`af sling --agent` would fail at `internal/cmd/sling.go:261`. So with the proposed default
mappings, dispatch would FAIL (not silently no-op) on a fresh factory. The proposed default
dispatch.json is only viable if the fresh-install path also registers/provisions those four
agents (e.g., by adding their agents.json entries to the `--init` starter config, or by making
quickstart run `af install --agents`).
