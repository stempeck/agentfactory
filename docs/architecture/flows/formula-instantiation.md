# Flow: `af sling --agent <specialist> "<task>"` (specialist dispatch)

Dispatches work to a specialist agent by instantiating the agent's
formula, creating bead DAG, persisting caller identity for WORK_DONE
routing, and launching the session.

**Entry point:** `internal/cmd/sling.go:runSling` → `dispatchToSpecialist`.

---

## Sequence

```mermaid
sequenceDiagram
    participant User
    participant Sling as sling.go
    participant Cfg as config
    participant WT as worktree
    participant Sess as session.Manager
    participant Form as formula
    participant Store as mcpstore
    participant MCP as Python MCP server
    participant Launch as launchAgentSession

    User->>Sling: af sling --agent <name> "<task>"
    Sling->>Sling: validateSlingArgs
    Sling->>Cfg: FindFactoryRoot(wd)

    Sling->>Cfg: resolveSpecialistAgent (LoadAgentConfig, check formula field)
    Cfg-->>Sling: AgentEntry

    alt --reset
        Sling->>Sess: NewManager + Stop
        Sling->>WT: FindByOwner + Remove
        Sling->>Sling: os.Remove runtime files
    else not --reset + not --no-launch
        Sling->>Sess: tmux.HasSession(af-<name>)
        alt already running
            Sling-->>User: error "use --reset"
        end
    end

    Sling->>Sling: resolveAgentName(callerWd, root) [caller identity]
    Sling->>WT: ResolveOrCreate(root, name, creator, AF_WORKTREE, AF_WORKTREE_ID)
    WT-->>Sling: wtPath, wtID, created
    Sling->>WT: SetupAgent(root, wtPath, name, created)

    Sling->>Sling: os.Remove formula_caller [clear stale]
    Sling->>Sling: persistFormulaCaller(agentDir, callerIdentity) [H-4/D15]
    Sling->>Sling: writeDispatchedMarker(agentDir, callerIdentity)

    Sling->>Form: FindFormulaFile(entry.Formula)
    Sling->>Form: ParseFile
    Sling->>Form: parseCLIVars + inject task=<task>

    Sling->>Store: newIssueStore(wd, beadsDir, BD_ACTOR)
    Store->>MCP: lazy start if needed (lock-guarded)
    MCP-->>Store: endpoint at .runtime/mcp_server.json

    alt task given, no issue var
        Sling->>Store: Create assignment bead (type=task, label=assignment)
        Store->>MCP: issuestore_create
    end

    Sling->>Form: MergeInputsToVars + ResolveVars
    Sling->>Form: TopologicalSort
    Sling->>Form: expandStepVars

    Sling->>Store: Create parent formula-instance bead (type=epic)
    loop each sorted step
        Sling->>Store: Create step bead (type=task, parent=instance, label=formula-step)
    end
    loop each dependency
        Sling->>Store: DepAdd(step, dep)
    end

    Sling->>Sling: persistFormulaInstanceID (.runtime/hooked_formula)

    alt not --no-launch
        Sling->>Launch: launchAgentSession(root, name, wtPath, wtID)
        Launch->>Sess: NewManager + SetWorktree + Start
        Sess->>Sess: tmux new-session + claude launch
        Note over Sess: af prime nudge reads<br/>.runtime/hooked_formula +<br/>dispatched + formula_caller
    end
```

---

## Call-site anchors

| Step | File:line |
|------|-----------|
| Entry | `internal/cmd/sling.go:runSling` (line 71) |
| `validateSlingArgs` | `sling.go:96-107` |
| `config.FindFactoryRoot` | `sling.go:81` |
| Dispatch branch | `sling.go:88` → `dispatchToSpecialist` (line 114) |
| `resolveSpecialistAgent` | `sling.go:221-238` |
| `--reset` handling (Stop, FindByOwner, Remove, runtime cleanup) | `sling.go:125-147` |
| Already-running pre-flight | `sling.go:148-156` |
| Worktree resolution | `sling.go:159-177` |
| Stale `formula_caller` clear | `sling.go:183` |
| `persistFormulaCaller` (H-4/D15 ordering invariant) | `sling.go:591-599` |
| `writeDispatchedMarker` | `sling.go:191, 667-671` |
| `instantiateFormulaWorkflow` | `sling.go:299-435` |
| Auto-create assignment bead | `sling.go:343-355` |
| `persistFormulaInstanceID` | `sling.go:544-548` |
| `launchAgentSession` (package-var seam) | `sling.go:216`, definition at `sling.go:616-650` |

---

## Invariants active in this flow

- **H-4 / D15 atomic-write ordering** — `persistFormulaCaller`
  runs BEFORE `instantiateFormulaWorkflow` creates the formula bead, so
  `af done` never sees a formula bead without a caller file
  (`sling.go:579-590` docstring; pinned by
  `TestDone_NoCallerFile_NoMail`).
- **Idiom #1 — `IncludeAllAgents: true` opt-out** — step beads are
  created with no `Assignee`; downstream `af done` MUST use
  `IncludeAllAgents: true` to see them (see `done.go:97-101, 133-137`
  and the WORK_DONE flow).
- **Idiom #9 — package-var seam** — `launchAgentSession` is a `var`
  so tests swap it with a no-op to avoid blocking on claude readiness
  (`sling.go:607-615` docstring).
- **INV-3** — library layer reads no env; `BD_ACTOR` is read by the
  cmd layer (`sling.go:336`) and injected into the mcpstore constructor.

---

## State written to `.runtime/`

| File | Writer | Purpose | Anchor |
|------|--------|---------|--------|
| `.runtime/formula_caller` | `persistFormulaCaller` | WORK_DONE recipient | `sling.go:591-599` |
| `.runtime/dispatched` | `writeDispatchedMarker` | Marks session for auto-terminate on completion | `sling.go:667-671` |
| `.runtime/hooked_formula` | `persistFormulaInstanceID` | Active formula instance bead ID for `af prime` / `af done` | `sling.go:544-548` |
| `.runtime/worktree_id`, `worktree_owner` | `worktree.SetupAgent` | Worktree cleanup coordination in `af done` | `worktree.SetupAgent` |
