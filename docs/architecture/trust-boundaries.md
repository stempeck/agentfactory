# Trust boundaries

What is trusted where, how identity/authority flows across boundaries,
and what is NOT trusted (even though it may look authoritative).

---

## Trust direction summary

```
[user invocation]  ← user's CLI args, cwd, home dir
    │
    ▼
[session.Manager] ← THIS is the trust anchor for identity
    │             writes: AF_ROLE, AF_ROOT, BD_ACTOR, BEADS_DIR
    │             via: tmux SetEnvironment (session.go:116)
    │                  shell export in claude-launch cmd (session.go:159)
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
| `AF_ROLE` | `session.Manager` only | tmux `SetEnvironment` + shell export in claude-launch command | `internal/session/session.go:116, 159` |
| `AF_ROOT` | `session.Manager` only | shell export | `internal/session/session.go:159` |
| `BD_ACTOR` | `session.Manager` only | shell export | `internal/session/session.go:159` |
| `BEADS_DIR` | `session.Manager` only | shell export | `internal/session/session.go:159` |
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

**Trust model:** Claude Code inherits all env vars (AF_ROLE, AF_ROOT,
BD_ACTOR, BEADS_DIR) from the shell-export setup at `session.go:159`.
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
