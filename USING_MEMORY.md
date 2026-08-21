# Using Agentfactory: The Memory Vault

Operator guide for agent memory: the per-agent learnings vault, what survives teardown, and
how to export, edit, seed, or host-mount it. Split out of
[USING_AGENTFACTORY.md](USING_AGENTFACTORY.md) — start there for factory setup and
day-to-day operation.

## The memory vault

**What it is.** Every agent has a vault at `<factory-root>/.agentfactory/memory/<agent>/`: one
plain Markdown file per recorded learning, each carrying its own frontmatter. Agents write with
`af memory add` and read their own with `af memory list` and `af memory show`. The lifecycle is
mark-only: a note that became durable truth **graduates** with a destination, a note that stopped
being true **expires**, and both are frontmatter marks — the file stays on disk. Agents have no
destructive verb here at all, so curation is yours.

**What survives what.** The vault lives outside every directory a teardown reaches, so it is
untouched by `af done`, `af down`, `af down <agent> --reset`, `af sling --agent <name> --reset`,
worktree garbage collection, and regenerating an agent workspace with `af install --agents`. The
first four and the garbage collector each print a `memory preserved:` line naming the vault they
left standing, so the claim is visible at the moment it matters rather than only in this manual.

**What does not survive, and what to do about it.** The vault is a directory inside the container
and it is gitignored, so it is invisible to `git status` and it does **not** survive removing the
container. Nothing in the factory can close that hole from the inside, so the factory makes it
loud instead: `af up` and factory-wide `af down --all` print how many notes exist and how long it
has been since the last export, and tell you the remedy once the gap gets wide.

### Getting the vault onto the host

`af memory export` writes the whole vault — every agent — to stdout as a gzip tarball. It needs no
mount, no shared filesystem, and no cooperation from the container's lifecycle. It is an
**operator action**: agents are refused, because backing up and seeding a vault belongs to the
human who curates it.

Three parts of each line are load-bearing. `af` is spelled by its full path because `docker exec`
resolves the command against the **image's** `PATH`, which does not include `~/.local/bin` where
`quickstart.sh` installs the binary. `-u dev` keeps whatever the command writes owned by the user
the factory runs as. `-w` is how the factory root is found — the command resolves it from its
working directory.

```bash
# Back the vault up to the host
docker exec -u dev -w /home/dev/af/myproject af_user_myproject \
    /home/dev/.local/bin/af memory export > vault.tgz

# Read it, or edit it in Obsidian
mkdir -p ~/vaults/myproject && tar xzf vault.tgz -C ~/vaults/myproject
# -> ~/vaults/myproject/memory/<agent>/<note>.md

# Seed an agent's vault in a NEW container (also seeds from plain Claude Code memory files)
docker cp ~/vaults/myproject/memory/manager af_user_myproject:/tmp/seed
docker exec af_user_myproject chown -R dev:dev /tmp/seed
docker exec -u dev -w /home/dev/af/myproject af_user_myproject \
    /home/dev/.local/bin/af memory import /tmp/seed --agent manager
```

Notes are ordinary Markdown, so an edit made in place is served to the next session verbatim —
including a `status:` you change by hand to retire a note. A file that already carries the
frontmatter keeps its fields on import; a plain Claude Code memory file is stamped with fresh
frontmatter, so a later triage can tell a seeded note from one an agent observed itself.

**`import` seeds; it does not merge.** Every imported file is recorded as a *new* note, so
importing an export back into the container it came from duplicates every note rather than
updating it — and a note you retired on the host lands as a second, expired copy while the
original goes on being served. Import is therefore for a vault that does not already hold those
notes: rebuilding a replacement container, or moving an agent's history to a new factory. To edit
notes **in place**, either keep the vault on the host from the start (below), or copy the edited
files back over the vault directory itself:

```bash
docker cp ~/vaults/myproject/memory/manager/. \
    af_user_myproject:/home/dev/af/myproject/.agentfactory/memory/manager
docker exec af_user_myproject chown -R dev:dev /home/dev/af/myproject/.agentfactory/memory/manager
```

### Optional: keep the vault on the host from the start

When you create a container with `quickdocker.sh`, setting `AF_MEMORY_HOST_DIR` bind-mounts a host
directory and links the vault to it, so notes land on the host as they are written:

```bash
AF_MEMORY_HOST_DIR=~/vaults/myproject ./quickdocker.sh user/myproject
```

The mount lands at `/home/dev/.af-vault` and the vault becomes a symlink to it once the factory is
installed. Use a host path without spaces, and expect the directory's ownership to change: the
container's user must be able to write through the mount, so the setup step chowns it, which on
Linux leaves it owned by uid 1000 on your host too.

This applies at **container creation only**. An existing container is never modified and never
needs to be recreated — export and import above are the supported path for one, and remain the
default. See `docs/architecture/adrs/ADR-019-no-container-recreation.md`.

