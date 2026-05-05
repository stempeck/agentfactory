# Flow: `af done` — WORK_DONE cascade

Closes the current formula step and, if all steps are complete, mails
WORK_DONE to the dispatcher, cleans up runtime state, and auto-terminates
dispatched sessions.

**Entry point:** `internal/cmd/done.go:runDone` → `runDoneCore`.

---

## Sequence

```mermaid
sequenceDiagram
    participant Agent as Agent (Claude)
    participant Done as done.go
    participant Cfg as config
    participant Store as mcpstore
    participant AF as af mail send (subprocess)
    participant Chk as checkpoint
    participant Lock as identity lock
    participant WT as worktree
    participant Tmux as tmux

    Agent->>Done: af done
    Done->>Cfg: FindFactoryRoot(cwd)
    Done->>Done: readHookedFormulaID(cwd) [.runtime/hooked_formula]
    alt no active formula
        Done-->>Agent: error "no active formula"
    end

    Done->>Store: newIssueStore(cwd, beadsDir, BD_ACTOR)
    Done->>Store: Ready(Filter{MoleculeID=instanceID}) [default actor-scoped]
    alt Ready returned no steps
        Done->>Store: List(Parent=instance, Open, IncludeAllAgents=true)
        alt open children exist
            Done-->>Agent: error "blocked, no actionable steps"
        else all complete
            Done->>Done: sendWorkDoneAndCleanup
        end
    else step[0] is current
        Done->>Store: Close(step.ID, "")
        alt --phase-complete --gate
            Done->>Store: Close(gate, "")
            Done-->>Agent: phase complete; session ends
        end

        Done->>Store: List(Parent=instance, Open, IncludeAllAgents=true)
        alt more open children
            Done->>Store: Ready(Filter{MoleculeID=instanceID})
            Done-->>Agent: print "Next step: ..."
        else all complete
            Done->>Done: sendWorkDoneAndCleanup
        end
    end

    Note over Done: sendWorkDoneAndCleanup
    Done->>Store: countAllChildren (IncludeAllAgents, IncludeClosed)
    Done->>Store: Get(instanceID) [formula name]
    Done->>Done: readFormulaCaller(cwd) [.runtime/formula_caller]
    alt caller present
        Done->>AF: exec.Command("af mail send caller -s WORK_DONE -m ...")
        AF->>Store: mail create (via af mail send)
    else no caller file
        Done-->>Agent: warn, skip mail [D1 / H-4]
    end

    Done->>Done: isDispatchedSession [.runtime/dispatched]
    Done->>Chk: checkpoint.Remove(cwd)
    Done->>Done: cleanupRuntimeArtifacts (hooked_formula, formula_caller, dispatched)
    Done->>Lock: lock.New(cwd).Release()

    alt worktree exists
        Done->>WT: RemoveAgent(root, wtID, AF_ROLE)
        alt owner && empty
            Done->>WT: Remove(root, meta)
        end
    end

    alt dispatched && mail ok
        Done->>Tmux: selfTerminate → KillSession(af-<role>)
        Note over Done,Tmux: ignore SIGHUP before kill<br/>so tmux server's SIGHUP<br/>to process group is non-fatal
    end
```

---

## Call-site anchors

| Step | File:line |
|------|-----------|
| Entry | `done.go:runDone` (line 48) → `runDoneCore` (line 58) |
| `FindFactoryRoot` | `done.go:60` |
| Read `.runtime/hooked_formula` | `done.go:69` |
| `newIssueStore` | `done.go:75` |
| `store.Ready` (default actor-scoped) | `done.go:82` |
| **`IncludeAllAgents: true` opt-out for open-children check** | `done.go:97-101` |
| `store.Close(step)` | `done.go:112` |
| `--phase-complete --gate` branch | `done.go:118-126` |
| Completion re-check with `IncludeAllAgents: true` | `done.go:133-137` |
| `sendWorkDoneAndCleanup` | `done.go:154-235` |
| `readFormulaCaller` | `done.go:237-245` |
| `sendWorkDoneMail` (subprocess boundary) | `done.go:247-265` |
| `isDispatchedSession` | `done.go:298-301` |
| `cleanupRuntimeArtifacts` | `done.go:271-275` |
| `lock.New(cwd).Release()` | `done.go:196` |
| Worktree cleanup | `done.go:201-221` |
| `selfTerminate` (SIGHUP ignore) | `done.go:315-340` |
| `countAllChildren` with `IncludeAllAgents, IncludeClosed` | `done.go:361-375` |

---

## Invariants active in this flow

- **Idiom #1 — `IncludeAllAgents: true` opt-out** — three call sites in
  this flow alone (`done.go:97-101, 133-137, 361-375`). Step beads are
  created with no `Assignee`; default actor-scoped `List` returns zero
  hits and silently declares the formula complete after step 1. This
  was the idiom commit `63307bb` violated at the adapter seam — and
  the deletion rationale is preserved in the comment at
  `done.go:87-96`. ADR-002 is canonical.
- **H-4 / D15** — a missing `formula_caller` means there's no dispatcher
  waiting; skip WORK_DONE mail, don't synthesize a fallback recipient
  (`done.go:165-170`, `sling.go:579-590`).
- **INV-5 — `Status.IsTerminal()` single gate** — code here uses
  `issuestore.StatusOpen` filters, relying on the enum/IsTerminal
  convention rather than string comparisons.
- **Self-invoked `af` seam** — mail delivery shells out to `af mail
  send` (`done.go:247-265`). Rationale unknown — flagged in `gaps.md`;
  consequence is `sendWorkDoneMail` is only integration-testable.
- **INV-7 — identity lock lifecycle** — `lock.Release()` runs on exit
  regardless of PID, because the lock PID belongs to the Claude process
  from `af prime`, not to `af done` (`done.go:194-196`).

---

## State cleanup matrix

| File | Removed by | When |
|------|-----------|------|
| `.runtime/hooked_formula` | `cleanupRuntimeArtifacts` | On completion only |
| `.runtime/formula_caller` | `cleanupRuntimeArtifacts` | On completion only |
| `.runtime/dispatched` | `cleanupRuntimeArtifacts` | On completion only |
| checkpoint (`.agent-checkpoint.json`) | `checkpoint.Remove` | On completion only |
| identity lock | `lock.New(cwd).Release()` | On completion only |
| worktree | `worktree.RemoveAgent` + `Remove` | On completion if owner + empty |
| tmux session | `selfTerminate` | Only if dispatched + mail OK |
