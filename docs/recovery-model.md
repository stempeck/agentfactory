# Recovery model: how agents survive crashes and context compression

Long-running LLM agents fail in ways ordinary programs don't. The context window fills up and
gets compressed, silently deleting the instructions the agent was following. Sessions crash
mid-step. An agent that "remembers" its workflow only in conversation history loses it at
exactly the wrong moment. Agentfactory's answer is to keep identity and workflow state
**outside** the model — on disk and in the issue store — and re-inject it at every session
boundary, so a recovered agent picks up the current step instead of improvising.

## What breaks, concretely

- **Context compression** — Claude Code summarizes the conversation when the window fills.
  Step directives, variable values, and role framing can be lost or distorted in the summary.
- **Crashes and disconnects** — the Claude process dies but the tmux session survives (a
  zombie), or the whole session is gone and uncommitted work sits in the worktree.
- **Recency bias** — after enough turns, early instructions stop steering behavior even
  without compression. Improvised shortcuts compound into wrong work.

## The recovery chain

### 1. State lives outside the session

A formula instantiation creates one bead (work item) per step, with DAG dependencies, in the
issue store. The agent's hook (`.runtime/hooked_formula`) records which formula instance it
is working. None of this is in the context window, so none of it can be compressed away.

### 2. `af prime` re-injects identity on every session start

The `SessionStart` hook runs `af prime --hook`. Prime outputs the agent's role template
(embedded in the `af` binary via `go:embed`), session metadata, and the current step's full
directives, computed from the ready-step frontier in the issue store. A fresh session —
whether after a crash, a restart, or a compaction recycle — starts with the same authoritative
context as the first one. This is why `make install` matters after `af formula agent-gen`:
the template a recovered agent gets is the one compiled into the binary.

### 3. Compaction boundaries are intercepted

The `PreCompact` hook runs `af compact-handoff`, which handles the compaction boundary
(including preventing thinking-block corruption) and hands the session off cleanly. The
checkpoint records `compaction_handoff: true` and the compaction time, so the next session
knows a recycle happened.

### 4. Checkpoints leave breadcrumbs

Whenever a session ends, `.agent-checkpoint.json` in the agent directory captures
(`internal/checkpoint/`):

| Field | Contents |
|---|---|
| `formula_id`, `current_step`, `step_title` | What was being worked |
| `hooked_bead` | The work item on the agent's hook |
| `modified_files`, `last_commit`, `branch` | Git state at checkpoint time |
| `timestamp`, `session_id`, `notes` | Provenance and free-form context |

Checkpoints are deliberately **informational** — breadcrumbs for the next session to read,
not a database that drives recovery. Recovery decisions come from the issue store's ready
steps and the hooked formula; the checkpoint tells the recovered agent what the previous
session had in flight (e.g. "3 modified files on branch X, step `implement` open").

### 5. Identity locks prevent split-brain

Each agent workspace holds a PID-based lock at `.runtime/agent.lock`
(`internal/lock/`). A second session claiming the same identity gets `ErrLocked` — unless the
recorded PID is dead, in which case the lock is **stale** and is cleaned up automatically.
Crash recovery therefore never requires manually deleting lock files.

### 6. Zombie sessions are killed, not trusted

`af up` distinguishes "tmux session exists" from "Claude is running in it." A tmux session
whose Claude process died is killed and recreated. See
[agent lifecycle](agent-lifecycle.md).

## The operator's view

Recovery is invisible in the normal case: restart the agent and it resumes.

```bash
af up worker            # zombie or fresh — either way, a working session
af attach worker        # watch it prime itself and continue the open step
af agents list --json   # confirm status
```

If a formula instance is wedged beyond repair, reset it explicitly rather than hand-editing
state:

```bash
af sling --agent worker "…" --reset   # close beads, remove worktree, clean runtime state
```

## Design intent

The invariant behind all of this: **the context window is a cache, never the source of
truth.** Everything an agent needs to continue — who it is, what step it is on, what the
step requires — must be reconstructible from disk. See
[docs/architecture/overview.md](architecture/overview.md) for how this fits the wider
system design.

## Further reading

- [Formulas](formulas.md) — where workflow state comes from
- [Agent lifecycle](agent-lifecycle.md) — session start, work loop, shutdown
- [README](../README.md) · [Using Agentfactory](../USING_AGENTFACTORY.md)
