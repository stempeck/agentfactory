# ADR-014: No interactive prompting in agent-runtime code paths

**Status:** Accepted
**Date:** 2026-04-22 (policy codification; near-miss anchored to the
staged `internal/cmd/sling.go:389-401` Phase 2 changes for #126 and the
design doc that prescribed them)

## Context

agentfactory is an autonomous multi-agent orchestration CLI. Every
runtime caller of an `af` command is one of:

- Another agent, invoking via a tmux pty (the pty *is* a TTY, but no
  human is reading or typing).
- A dispatch chain — one agent subprocess-invoking another.
- A formula workflow.
- A CI test harness.
- A hook (quality gate, session start, etc.).

**None of these callers is a human at a keyboard waiting to type `y`
or `N`.** The operator writes formulas, defines agents, and inspects
state after the fact — they do not sit mid-execution answering
prompts. "Human-in-the-loop confirmation" is not a persona this
system serves at runtime.

### The near-miss

`.designs/issue-126-three-way-disagreement/design-doc.md:147-156`
prescribed a TTY-aware `y/N` prompt for `af sling` when succeeding over
a non-terminal prior formula, with a `--force` flag to bypass and a
`--reset` alias for backward compat. The pattern was design-prescribed,
plan-validated, peer-reviewed, and ALMOST implemented:

```go
stdinStat, _ := os.Stdin.Stat()
stdinIsTTY := stdinStat != nil && stdinStat.Mode()&os.ModeCharDevice != 0
if !params.Force && stdinIsTTY {
    fmt.Fprintf(w, "Agent %s has an active formula %s in state %s.\n", ...)
    fmt.Fprintln(w, "Continuing will not advance the prior formula.")
    fmt.Fprint(w, "Proceed? (y/N) [--force to skip] ")
    var resp string
    fmt.Fscanln(os.Stdin, &resp)
    if strings.ToLower(resp) != "y" {
        return "", nil, agentName, fmt.Errorf("sling aborted by operator")
    }
} else if !params.Force {
    return "", nil, agentName, fmt.Errorf("prior formula %s is %s; use --force or --reset ...", ...)
}
```

None of the three review passes challenged whether the prompt was
appropriate in the first place. The pattern was copied from
human-facing CLI tools (`git`, `npm`, `rm -i`, package managers) where
the interactive-operator persona is dominant. **agentfactory has no
such persona.** The pattern imports a human-in-the-loop assumption
that doesn't exist, and every caller subsequently carries tax for
that imported persona:

- `fmt.Fscanln` on `os.Stdin`, which hangs if stdin is a closed pipe.
- TTY detection code that exists only to gate the prompt.
- Two flags (`--force` + `--reset`) with overlapping-but-not-identical
  semantics, preserved to not break pre-existing `--reset` callers.
- `USING_AGENTFACTORY.md:355-365` documents all three to a reader who
  shouldn't have needed the documentation.
- Test surface (`internal/cmd/sling_test.go:2483-2499` —
  `forceNonTTYStdin`) pinning a behavior no caller exercises deliberately.
- Every future destructive action becomes a candidate for the same
  pattern by precedent.

## Decision

**No code path under `cmd/` or `internal/` (Go) or `py/` (Python) may
prompt for user input at runtime.** An interactive prompt appearing in
any runtime code path is a **red flag**, not a feature — the correct
response on sight is to delete it, not to add an escape hatch around
it.

### Forbidden

1. Reading `os.Stdin` (Go) or `sys.stdin` / `input()` / `raw_input` /
   `getpass` (Python) for user confirmation, choice, credential, or
   any free-form response. Specifically forbidden:
   `fmt.Scanln`, `fmt.Fscanln`, `bufio.NewReader(os.Stdin).ReadString`,
   `golang.org/x/term.ReadPassword`, Python `input()`,
   `getpass.getpass()`.
2. TTY-presence detection used to select an interactive branch.
   Patterns forbidden under this clause:
   `(stat.Mode() & os.ModeCharDevice) != 0` when the `true` branch
   leads to stdin read, `golang.org/x/term.IsTerminal`, any `isatty`
   library.
3. Introducing any Go or Python library whose primary purpose is
   interactive prompting (`survey`, `promptui`, Python `inquirer`,
   `PyInquirer`, `questionary`, etc.). Adding such a library to
   `go.mod` or `requirements.txt` is simultaneously a violation of
   [ADR-013](ADR-013-minimal-go-mod-dependencies.md).
4. Cobra flag-help text or command `Long` descriptions containing
   `(y/N)`, `[y/N]`, `Proceed?`, `Continue?`, or similar — because
   these document behavior that must not exist.

### Required shape for "caller discretion"

When an action requires operator intent (destructive, ambiguous, or
irreversible), **fail loud with a structured error naming the exact
flag that expresses the intent**. No prompt, no fallback. The caller
must be explicit on the re-run.

```go
if priorIsNonTerminal && !params.Force {
    return fmt.Errorf("prior formula %s is %s; pass --force to replace", priorID, status)
}
```

One flag. One meaning. No prompt. No alias. No TTY branch.

### Permitted uses of `os.Stdin` / TTY detection

These are **not prompts** and remain allowed:

1. **Structured-data pipes.** Reading JSON, protobuf, or other
   structured payloads piped into the process — e.g.,
   `internal/cmd/prime.go:232-253` (`readHookSessionIDFromStdin`)
   consumes a hook JSON payload from stdin. TTY detection there is
   used **defensively to reject terminal input** — the `TTY-present`
   branch returns an empty string, i.e., "nothing to read." Opposite
   shape from sling.go: rejecting the terminal, not serving it.
2. **`os.Stdin` as a descriptor passed to a child process.**
   `internal/tmux/tmux.go:138` sets `cmd.Stdin = os.Stdin` to wire
   tmux subprocess I/O. No user-input read happens in the Go layer.

The distinguishing test: **is a human (or agent acting as one)
expected to type a response that the Go/Python code reads?** If yes,
forbidden. If no (structured payload or descriptor passthrough),
allowed.

## Consequences

**Accepted costs:**

- Operators who forget the required flag on a destructive action get a
  hard error, not a prompt. The first re-run with the correct flag is
  the learning moment. One extra keystroke for the rare interactive
  case, in exchange for zero ambiguity for the common autonomous case.
- **The staged Phase 2 changes for #126 are in violation.** Specifically
  `internal/cmd/sling.go:389-401` (TTY + prompt + `Fscanln`),
  `internal/cmd/sling.go:74` (`--force` flag declaration paired with
  `--reset` alias OR-ing at `sling.go:212, 261`),
  `internal/cmd/sling_test.go:2483-2499` (TTY-manipulation test
  helper), and `USING_AGENTFACTORY.md` "Formula Succession" section.
  These must be revised before commit: delete the TTY branch, delete
  the prompt, delete the `Fscanln`, keep `--force` as a single
  explicit opt-in, restore `--reset` to its pre-126 narrower meaning
  ("stop target agent and clean runtime state before dispatch"), and
  rewrite the docs to reflect the simpler contract.

**Earned properties:**

- **Deterministic command behavior.** Same inputs, same flags, same
  result — no "depends on whether stdin is a pty" drift class.
- **No hang-on-closed-pipe bugs** from `Fscanln` or equivalents.
- **Smaller flag surface.** `--force` can be a single explicit
  opt-in; no `--reset` alias to maintain; no dual-semantics flags.
- **Citeable policy.** Future design docs proposing "prompt for
  confirmation" have something to defer to. Any proposal that
  includes `y/N` or TTY-gated interactive branching is a **named
  deviation**, not an accident.
- **Reading the codebase is easier.** Any `os.Stdin` read or
  `ModeCharDevice` check must be one of the two permitted shapes
  (structured-data pipe, defensive rejection). Their appearance is
  immediately meaningful.
- **Closes a whole class of imported-persona bug.** The
  CLI-conventions-for-humans training bias that produced the near-miss
  now has a mechanical refusal attached.

## Scope

Applies to:
- All Go code under `cmd/` and `internal/`.
- The Python MCP server under `py/`.
- Hooks under `hooks/` (already constrained by
  [ADR-007](ADR-007-hooks-never-block.md) to never block;
  interactive prompts are the canonical blocking mechanism, so this
  ADR reinforces that gate).

Does not apply to:
- **One-time bootstrap / installation scripts** invoked manually by a
  human operator at setup time: `quickdocker.sh:112,145,163`,
  `quickdockerbase.sh:98,134`. The operator persona is real at the
  bootstrap moment. These scripts are exempt but should be minimized
  over time and not proliferated.
- **Contents of a tmux session after `af attach`.** Operator ↔ agent
  interaction inside an attached tmux session is outside
  agent-runtime scope.
- **The `af` binary's subprocess of `claude`.** Claude's own
  interactive behavior is driven by the `claude` CLI and is not
  agentfactory's code.

## Enforcement

Convention plus review plus mechanical grep. Specifically:

- review Phase 6 gains a claim-check: "Does this fix add
  `os.Stdin` reads, `ModeCharDevice` checks, `Fscanln`, or any
  `(y/N)` / `Proceed?` string? If yes, cite this ADR and justify the
  exemption against the two permitted shapes."
- design Phase 1 options-considered step must reference this
  ADR when any option proposes an interactive flow or TTY-gated
  behavior branch.
- A candidate mechanical interlock (not yet installed):
  ```
  git grep -nE 'fmt\.(F)?Scanln|bufio\.NewReader\(os\.Stdin\)|ReadPassword|ModeCharDevice|\(y/N\)|\[y/N\]|Proceed\?' \
      -- 'internal/**' 'cmd/**' 'py/**'
  ```
  should return either empty, or lines in `internal/cmd/prime.go`
  (the permitted defensive shape) only. Any other hit is a violation.
  Installing this as a pre-commit hook or CI step is a candidate
  future enforcement.

## Corpus links

- `invariants.md` — to be extended with a "no interactive prompting
  at agent runtime" invariant on the next `/architecture-docs`
  regeneration.
- Near-miss example: `.designs/issue-126-three-way-disagreement/design-doc.md:147-156`,
  `internal/cmd/sling.go:389-401` (staged),
  `internal/cmd/sling.go:74` (`--force` declaration),
  `USING_AGENTFACTORY.md:355-365` ("Formula Succession" section).
- Permitted defensive shape: `internal/cmd/prime.go:232-253`.
- Permitted passthrough shape: `internal/tmux/tmux.go:138`.
- Related ADRs:
  - [ADR-003](ADR-003-no-identity-overrides.md) — no user-facing
    identity override flags (same refusal-of-imported-human-persona
    family).
  - [ADR-007](ADR-007-hooks-never-block.md) — hooks never block.
    Interactive prompts are the canonical blocking mechanism.
  - [ADR-013](ADR-013-minimal-go-mod-dependencies.md) — any
    interactive-prompt library would require both an ADR-013
    justification and an ADR-014 exemption (and neither is obtainable).
