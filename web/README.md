# Web Console — local access runbook (`--web` / `--shell`)

This is the operator runbook for reaching the agentfactory **web console** (`webui`) from your
laptop browser. The console is an optional binary that lives in this module (`web/cmd/afweb` →
`webui`); building and installing it is covered in the root [`README.md`](../README.md#web-console-optional).
This document covers **how you open it** once a factory container is running.

The console binds **loopback only** inside the container (`127.0.0.1`) and is **never** exposed to
the network. The standard way to reach it is a one-command local bridge:

```bash
quickdocker.sh <user/repo> --web      # -> 🔗 Open your factory:  http://127.0.0.1:<HOSTPORT>/
quickdocker.sh <user/repo> --shell    # -> opens a shell in the same container
```

No SSH, no reading port files by hand, no container rebuild. `quickdocker-pro.sh` (the
registry-image variant) is a thin wrapper that defers to `quickdocker.sh`, so the same two
flags work there too — the bridge lives in one place.

> **Need a real docker host.** The bridge pipes browser traffic through `docker exec`, so it only
> works where the Docker daemon and your browser are on the same machine (your laptop *is* the docker
> host). For a headless or remote docker host, use the SSH-forward **alternative** documented in the
> root [`README.md`](../README.md#web-console-optional).

---

## How the bridge works (and why it needs no auth change)

`webui` keeps its default **loopback bind inside the container** (`127.0.0.1:<internal>`), untouched.
`--web` stands up a forking listener on your laptop at `127.0.0.1:<HOSTPORT>`. Each browser connection
is piped through `docker exec` into the container, where a tiny inline `python3` relay connects to
`webui`'s loopback.

Because the relay reaches `webui` **from inside the container**, `webui` sees a loopback client — so
its existing `authOK` (no token needed on loopback) and `originOK` (a `127.0.0.1` Origin) checks
already pass. **No `server.go` / auth / origin change was required.** The control plane's loopback-only
security posture is preserved end to end: the only ingress is a `127.0.0.1`-pinned bridge on your own
machine.

---

## Access — `quickdocker.sh <repo> --web`

```bash
quickdocker.sh user/myrepo --web
# -> 🔗 Open your factory:  http://127.0.0.1:<HOSTPORT>/
```

`--web` means **"reveal my URL"**, not "start a session":

- It prints the clickable link exactly as `🔗 Open your factory:  http://127.0.0.1:<HOSTPORT>/`
  (the two spaces before the URL are intentional) and **returns immediately** — the bridge runs
  detached in the background.
- On macOS it also auto-opens the URL in your default browser (via `open`). On Linux, click or paste
  the printed link.
- It is **idempotent**: re-running `--web` for the same container reprints the **same** URL and never
  starts a second listener. (A second listener would collide on the port and shift the URL.)

If the target container is not running, `--web` stops with a hint instead of recreating anything:

```
ERROR: container 'af_user_myrepo' is not running — start it (docker start 'af_user_myrepo') or create it first, then retry.
```

If `webui` is not installed inside the container, `--web` does **not** build it on the fly:

```
ERROR: webui not installed in this container — pull latest and run quickstart.sh inside it once, then retry.
```

---

## Shell — `quickdocker.sh <repo> --shell`

```bash
quickdocker.sh user/myrepo --shell
```

`--shell` opens an interactive shell **in the same container** that serves the console:

```bash
docker exec -it -u dev <container> bash
```

Use it to tail logs, run `af` commands, or inspect `.runtime/webui_server.json` by hand.

---

## Host prerequisite — one listener tool on the laptop

`--web` builds the laptop-side listener from the first available of three tools, **auto-detected in
this order**:

| Order | Tool      | Notes |
|-------|-----------|-------|
| 1     | `socat`   | preferred — clean forking listener |
| 2     | `nc`      | works with GNU/`ncat` (`-e`); BSD/OpenBSD `nc` lacks `-e`, so prefer `socat` or `python3` |
| 3     | `python3` | pure-stdlib fallback forking server |

If **none** of the three is present, `--web` fails loudly rather than silently doing nothing:

```
ERROR: need one of: socat / nc / python3 on this machine — install one and retry.
```

Install any one of them (`brew install socat`, `apt-get install socat`, …) and re-run.

**In-container side:** the relay that reaches `webui`'s loopback is an inline `python3` snippet, and
the per-connection handler runs under `bash`. The agentfactory image already ships `python3`
(`Dockerfile`) on its Ubuntu base, so stock factories need nothing extra. If you run the console in a
**minimal custom image**, that image must provide `python3` (and `bash`) or the relay cannot start.

---

## Multiple factories — distinct ports, bookmarkable tabs

Each factory container has a distinct name derived from its repo path (`af_<user>_<repo>`), and each
name derives a **distinct, deterministic** laptop port:

```
HOSTPORT = 20000 + (cksum(container_name) % 10000)      # range 20000–29999
```

So multiple factories give you multiple stable URLs — one independent, bookmarkable browser tab per
factory, each reaching its own console:

```bash
quickdocker.sh user/repo-a --web   # -> http://127.0.0.1:21337/
quickdocker.sh user/repo-b --web   # -> http://127.0.0.1:28042/
```

**Caveat — free-port fallback.** If the computed port is already in use, `--web` advances to the next
free port within the range. So the **printed** port is authoritative — bookmark the URL `--web`
actually prints, not the one you computed by hand.

---

## Restart — after `docker stop` / `docker start`

The bridge lives exactly as long as the container: a background watcher self-exits (and clears its
pidfile marker `${TMPDIR:-/tmp}/af-web-bridge-<container>.pid`) the moment the container stops.

After you bring the container back with `docker start <container>`:

1. `webui` **relaunches inside** the container automatically (the login-init guard restarts it; the
   bridge also re-launches `webui` if it finds it not yet up).
2. The old bridge is gone, so **re-run `--web`** to stand up a fresh bridge and reprint your URL:

```bash
docker start af_user_myrepo
quickdocker.sh user/myrepo --web      # re-bridge; same URL, ready again
```

---

## Security — the control plane stays loopback-only

The console can stop and sling agents and edit factory config, so an exposed socket would be a
remote-code-execution and irreversible-loss risk (cross-review **CR-1**). The bridge is designed so
that risk never appears:

- **Nothing is ever published.** The control plane is **never published** to the LAN or the internet:
  the container is started without any host-port mapping, and that stays true — the bridge does not
  expose a container port; it relays through `docker exec`.
- **The only ingress is `127.0.0.1`.** The laptop listener binds `127.0.0.1:<HOSTPORT>` explicitly,
  so it is reachable only from your own machine — never the LAN or the internet.
- **`webui` keeps its in-container loopback bind.** Traffic arrives from inside the container, which
  is *why* its auth/origin checks pass unchanged — there is no weaker code path to attack.

Do **not** try to make the port reachable from another machine by exposing it directly; that would
turn an unauthenticated loopback control plane into an open one. For legitimate remote access, use the
SSH local-forward alternative in the root [`README.md`](../README.md#web-console-optional), which keeps
the socket on loopback at both ends.

---

## Troubleshooting

| Symptom | Cause | Fix |
|---------|-------|-----|
| `ERROR: need one of: socat / nc / python3 …` | No listener tool on the laptop | Install one (`socat` recommended), then re-run `--web` |
| Relay never connects in a custom image | Minimal image lacks the in-container interpreter | Ensure the image provides `python3` and `bash` |
| Printed port isn't the one you expected | The deterministic port was in use → **free-port** fallback advanced it | Use the port `--web` actually printed; it is authoritative |
| `ERROR: webui not installed in this container …` | `webui` was never built/installed inside | Run `quickstart.sh` inside the container once (it builds + installs `webui`), then retry |
| `ERROR: container '…' is not running …` | Container is stopped | `docker start <container>`, then re-run `--web` |
| `webui … did not publish its loopback address … within 5s` | `webui` is installed but slow/failed to bind | Check `/tmp/webui.log` inside the container |

---

## Scope / limitations

- The bridge is **integration-tier**: it is fully observable only on a real docker host with the
  daemon and browser co-located. There is no way to exercise it end-to-end in a docker-less sandbox.
- This runbook documents the **shipped** behavior of `quickdocker.sh` / `quickdocker-pro.sh`; it adds
  no new flags or paths. The build/install steps for `webui` itself live in the root
  [`README.md`](../README.md#web-console-optional).
