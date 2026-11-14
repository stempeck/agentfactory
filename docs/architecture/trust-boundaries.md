# Trust boundaries

What is trusted where, how identity/authority flows across boundaries,
and what is NOT trusted (even though it may look authoritative).

Governed by [ADR-026](adrs/ADR-026-most-secure-way-to-run-an-agent.md): every change keeps or
strengthens the boundaries documented here, and agentfactory's security is improved continuously,
never weakened. The bootstrap convergence controls (`_converge`, the `CONVERGE_FAILURES` ledger,
`convergence.json`, the advisory exception register and its daily decay job) are named in
[ADR-027](adrs/ADR-027-bootstrap-convergence-contract.md) and protected the same way.

---

## Trust direction summary

```
[user invocation]  ← user's CLI args, cwd, home dir
    │
    ▼
[session.Manager] ← THIS is the trust anchor for identity
    │             writes: AF_ROOT AF_ROLE AF_ACTOR AF_WORKTREE AF_WORKTREE_ID
    │             via: tmux SetEnvironment (session.go:524-529)
    │                  shell export in claude-launch cmd (session.go:586-590)
    │             config-derived env: launch-line export only
    ▼
[cobra cmd layer: internal/cmd/] ← reads env, reads cwd, validates
    │                              against agents.json membership
    │                              via resolveAgentName (helpers.go:55)
    ▼
[library layer: issuestore, config, formula, mail, ...]
    │             MUST NOT read env (INV-3)
    │             MUST NOT accept user-identity overrides
    ▼
[mcpstore adapter]
    │             injects actor-scoping at mcpstore.go:199-214
    │             actor comes from Store constructor, not env
    ▼
[wire: JSON-RPC loopback → py/issuestore server]
                  loopback bind, no auth (R-SEC-1)
                  TRUST = same machine = same user
```

---

## Trusted write paths

Only these producers may set identity-bearing context. Adding a new
producer is an architectural change requiring a trust-model justification.

| Value | Writer | Mechanism | Anchor |
|-------|--------|-----------|--------|
| `AF_ROOT`, `AF_ROLE`, `AF_ACTOR`, `AF_WORKTREE`, `AF_WORKTREE_ID` | `session.Manager` only | tmux `SetEnvironment` + shell export in claude-launch command | `internal/session/session.go:524-529, 586-590` |
| Config-derived env (model, endpoint, telemetry, git identity, build host) | `session.Manager` only | shell export in claude-launch command; never tmux | `internal/session/session.go:585` (`buildStartupCommand`) |
| `agents.json` membership | `af install` | filesystem write during setup | `internal/cmd/install.go` |
| `factory root` marker | `af install --init` | creates `.agentfactory/` directory | `internal/cmd/install.go` |

**Anti-producers (explicitly forbidden):**

- Any user-facing CLI flag that accepts identity
  (`--as`, `--actor`, `--from`, `--role`). Adding such a flag is the
  specific anti-pattern named in `feedback_no_agent_overrides.md` and
  **INV-2**.
- Any code outside `internal/session/` writing `AF_ROLE`.
- Test code using `os.Setenv("AF_ROLE", ...)` outside a `t.Setenv`
  hermetic scope — **INV-3** enforces this via the env-hermetic regression
  test (commit `e4cb7a0`, `7875acc`).

---

## Trusted context-derivation sites

These are the places where "who is calling?" / "what is my root?" get
resolved from trusted context:

### Identity (`resolveAgentName`)

- Owner: `internal/cmd/helpers.go:55-102`.
- Three-tier: cwd path → agents.json membership validation → AF_ROLE
  fallback.
- AF_ROLE is consulted on **both** path-detection error AND membership
  failure (comment at `helpers.go:72-93`, issues #88, #89).
- Called from: `prime.go:185`, `sling.go:161, 270, 604`, `bead.go:224`,
  `up.go:68`, `mail.go`, `handoff.go` (see `subsystems/cmd.md`).

### Root (`FindLocalRoot`)

- Owner: `internal/config/root.go`.
- Walks up from cwd looking for `.agentfactory/factory.json` via
  `os.Stat` (does NOT parse the file — see INV-11).
- Shell hooks fall back to `$(af root)` via `${AF_ROOT:-$(af root)}`.

### Agent name from path (`DetectAgentFromCwd`)

- Owner: `internal/config/paths.go:52`.
- **Not authoritative** — returns `parts[2]` unchecked. Callers MUST
  validate against `agents.json` before using the result
  (pattern at `internal/cmd/helpers.go:78-88`).
- Using the output without membership check is a bug class (#88, #89).

---

## Override policy

### Allowed (trusted)

- `session.Manager` writing `AF_ROLE` → cmd layer reading it via
  `os.Getenv("AF_ROLE")` inside `resolveAgentName`. Trust comes from the
  write path, not the read.
- Test harness using `t.Setenv("AF_ROLE", ...)` — scoped to a single test,
  cleaned up automatically.

### Forbidden

- User-facing flag override of identity (see Anti-producers above).
- Library-layer `os.Getenv` reads (INV-3). Env state must be plumbed as a
  parameter from the cmd/session layer down.
- Bypass of the mcpstore default-actor overlay at the adapter seam. The
  canonical opt-out is `Filter{IncludeAllAgents: true}` at the call site
  (idiom #1). Commit `63307bb` is the deleted counter-example.

### Conditional

- `bead --all` CLI flag enables `IncludeAllAgents` at the call site
  (`internal/cmd/bead.go:294`). This is a view-scope toggle for the
  user's own view, not an identity override.

---

## Cross-process trust boundaries

### Go ↔ Python MCP server (loopback HTTP/JSON-RPC)

**Trust model:** Same machine = same user. The Python server binds
`127.0.0.1` only (INV-4) and does no authentication.

**Rendezvous:** The server writes `.runtime/mcp_server.json` containing
its host:port + (likely) a session token; the Go client reads it. Any
process on the host that can read `.runtime/` can connect.

**Known gap:** The Go client does NOT validate the endpoint file's host
is literally `127.0.0.1` — defense-in-depth is unimplemented. A future
misconfiguration that writes a non-loopback host would connect without
warning. (See `gaps.md`.)

**Anchors:**
- Server bind: `py/issuestore/server.py:267-268`.
- Endpoint file read: `internal/issuestore/mcpstore/client.go`.
- Loopback rationale cite: `internal/issuestore/mcpstore/mcpstore.go:7`
  (R-SEC-1).

### Go ↔ tmux (subprocess)

**Trust model:** tmux is a local process running as the same user.
Session names collide if not prefixed; `internal/session/names.go` owns
the `af-` prefix convention.

**Known inconsistency:** `internal/worktree/worktree.go:302`'s
`tmux has-session -t meta.Owner` check uses the bare agent name, not
`af-<name>`. See `subsystems/session.md` for the reasoning; **either
`meta.Owner` is stored pre-prefixed (not verified) or GC never matches
live sessions** — needs review.

### Go ↔ claude CLI (subprocess in session)

**Trust model:** Claude Code inherits all env vars (the identity quintet
`AF_ROOT AF_ROLE AF_ACTOR AF_WORKTREE AF_WORKTREE_ID` and every
config-derived value) from the shell-export setup at `session.go:586-590`.
The agent running inside Claude sees these as the outside-world context.

**Anti-pattern:** Claude agent code reading `os.Getenv("AF_ROLE")` to
determine identity for anything other than display — the identity
resolution authority is `resolveAgentName`, not the env var directly.

### Hooks (Claude-Code-invoked shell scripts)

**Trust model:** The hooks execute in the same shell environment as the
agent. They receive:
- stdin: a JSON payload from Claude Code containing transcript + session
  metadata (exact contract — see `subsystems/hooks.md`).
- env: AF_ROLE, AF_ROOT inherited from the agent's session.

**Policy (hooks never block):** Both `quality-gate.sh` and
`fidelity-gate.sh` always `exit 0` with `{"ok": true}`. Enforcement
happens via **mail** (a fresh bead in the agent's inbox with subject
`QUALITY_GATE` or `STEP_FIDELITY`), not by blocking the tool call. This
is an architectural decision (see `subsystems/hooks.md`).

### GitHub issue → dispatcher → healing agents (advisory-heal)

**Trust model:** A red scheduled `advisory-decay` run on stempeck/agentfactory-pro files one issue
per pin, and the `advisory-heal` dispatch workflow in pro's tracked `.agentfactory/dispatch.json`
routes each issue through fable-implement, fable-review and fable-increment. The public repository
runs the same code with the install and filing steps skipped, and is healed only by a publish. What
the agents treat as work input is bounded at every hop:
- **Who can start a heal:** whoever can apply labels. The dispatcher acts on any issue that carries
  `agentic` plus the `advisory-heal` workflow label; it does not check who applied them or that the
  filing step opened the issue. GitHub grants label rights only to collaborators with triage
  access or above, and the job's own token (`issues: write`, granted to the `advisory-decay` job
  alone). The first phase label is the dedicated `heal-implement`, never a label an issue template
  applies.
- **What the issue may carry:** validated identifiers only. The filer renders advisory ids,
  aliases, `package@version` keys, versions, dates, ecosystems and repository names after checking
  each against a fixed grammar, and fails the step on any field that does not match; it never
  pastes advisory text, so no imperative from an advisory feed reaches an agent. Every key is
  rendered inside backticks. The class, the key and the fix all come from the audit's record; the
  filer decides only which findings share an issue, by a fixed sibling map over validated package
  names (Go `stdlib` and `toolchain`, npm `playwright` and `playwright-core`, at one version) that a
  test holds equal to the audit's two-package lines.
- **Which `gh` files the issue:** the release `quickstart.sh` pins. The install step reads
  `GH_VERSION` and its `GH_SHA256_*` sums from `quickstart.sh`, refuses a download whose sha256
  differs, installs under the job's temporary directory, and puts that directory first on the PATH
  of the steps after it.
- **Who can add work input:** collaborators only. The filer locks each issue as it creates it,
  and fable-implement locks the pull request it opens for an issue link when the repository is
  public, so the comments and reviews the later phases read come only from collaborators.
- **What an agent may change in the register:** on its own, an agent may move a pin, delete a
  register entry or shorten a `recheck_by`; each is a strengthening. A new entry, a `recheck_by`
  moved later, or adding or changing `fix_available` (the operator's acknowledgement of a
  published fix) is a proposal accepted only by the operator's merge of the pull request that
  proposes it, whose body starts `Operator acceptance required:`.

**Known gap (not enforced):** The agents' single GitHub identity is the admin and only
collaborator of both repositories. On stempeck/agentfactory-pro, where heals are merged, GitHub
offers no ruleset or branch protection for the repository's plan, so nothing requires a review; on
stempeck/agentfactory the `main-rules` ruleset requires a review that no second account exists to
give, so every merge on its `main` lands through the admin bypass. "Only the operator merges" and
"a register-widening change needs the operator's acceptance" are therefore conventions, backed by
the formulas (no healing formula runs `gh pr merge`) and by the dispatcher, which completes a heal
on an open pull request and never approves or merges it, rather than by GitHub. A non-bypassing
agent identity plus CODEOWNERS on the register would make them mechanical.

**Anchors:**
- gh install step: `.github/workflows/test.yml:288-290` (`.github/install-gh.sh`); filing step and
  its repository guard: `.github/workflows/test.yml:291-298`; job-level `issues: write`:
  `.github/workflows/test.yml:277-279`.
- Filer (grammar checks, backticked keys, `gh issue lock`): `.github/advisory-issue.sh`.
- Record writer and the one fix rule: `.github/advisory-audit.sh` (`ADVISORY_AUDIT_RECORD`, the
  `probe` function).
- Workflow validation (each phase resolves on its first single-label mapping, one `source`):
  `validateWorkflows`, `internal/config/dispatch.go:256`; the workflow engine:
  `handleWorkflowItem`, `terminalPROutcome` and `workflowCtx.terminal` in
  `internal/cmd/dispatch.go`.
- Closing reference, self-check and public-PR lock: the `submit-and-exit` step of
  `internal/cmd/install_formulas/fable-implement.formula.toml`; no healing formula merges:
  `TestFableImplementClosingReference` (`internal/cmd/formula_fable_implement_test.go`).
- The register rule: [ADR-027](adrs/ADR-027-bootstrap-convergence-contract.md) Decision item 4;
  the runbook: `USING_ADVISORYLOOPS.md` "Advisory healing loop".

---

## What is NOT trusted

- **cwd alone.** Must be combined with agents.json membership validation.
  `DetectAgentFromCwd` returns unchecked path segments (INV-12).
- **`.designs/**/*.md`.** Per the skill's rules and the codebase's
  lived history (commit `63307bb` — a design's recommended fix was
  rejected because it weakened RBAC). Designs are secondary.
- **CLAUDE.md.** The root `CLAUDE.md` is stale at time of writing — it
  lists removed role templates (deacon, refinery, witness) that were
  deleted in commit `8d64e6d`. See `gaps.md`.
- **Comment anchors (`C-n`, `H-n`, ...).** Anchors are pointers to design
  docs; a comment citing `H-4/D15` is not itself proof the invariant is
  enforced. Cross-check the enforcement mechanism. INV-6 has a specific
  example: the anchor is present at two sites with two different
  mechanisms.
