# Docker tools

Four tools over the Docker Engine API on the unix socket. Two read and are always
registered; two act and exist **only when you name the containers they may
touch**. Design notes: [docs/modules/docker.md](../docs/modules/docker.md).

| Tool | Input | What it answers | Needs allowlist |
| --- | --- | --- | --- |
| `docker_container_status` | `names`, `include_all` | container state — OOM kills, crash loops, failing healthchecks, worst first | – |
| `docker_container_logs` | `container`, `tail`, `since_seconds`, `timestamps` | what a container wrote to stdout and stderr | – |
| `docker_container_exec` | `container`, `command`, `timeout_seconds` | runs a command inside a container | **yes** |
| `docker_container_restart` | `container`, `stop_timeout_seconds` | restarts a container and reports whether it came back | **yes** |

Access needs membership in the `docker` group. **The socket is root-equivalent
by design** — anything that can reach it can control the daemon, so grant it the
way you would grant root.

---

## `docker_container_status`

| Parameter | Type | Default | Meaning |
| --- | --- | --- | --- |
| `names` | string[] | — | specific containers, always returned whatever their state |
| `include_all` | boolean | `false` | also return containers that stopped cleanly |

With no `names`, running containers plus anything broken are returned; the
cleanly-exited ones a homelab accumulates from one-shot runs are hidden.

```
NAME         IMAGE                     STATE         HEALTH     RESTARTS  FOR    PORTS
jellyfin     linuxserver/jellyfin      exited (OOM)  -                 0  2m     -
sonarr       linuxserver/sonarr        restarting    -                11  34s    8989/tcp
radarr       linuxserver/radarr        running       unhealthy         0  2m     7878/tcp
qbittorrent  linuxserver/qbittorrent   running       healthy           0  92d1h  8080/tcp

warning: jellyfin was killed for exceeding its memory limit (20M) — raise the limit or fix the leak

warning: sonarr is restarting right now (11 restarts so far) — it is in a crash loop

warning: radarr is running but its healthcheck is failing (67 consecutive failures) —
the process is up while the service behind it is not

(6 cleanly-stopped containers omitted; use include_all to see them)
```

Beyond what `docker ps` shows:

- **OOM kills, named as such.** A container that exceeded its memory limit shows
  as `Exited (137)` in `docker ps`, which reads like an application error. The
  OOM flag is reported directly, with the limit it hit.
- **Healthcheck results and failing streaks**, so "running" and "running while
  the service behind it is down" are told apart.
- **Restart counts next to how long the current state has held.** Up right now
  plus 11 restarts in the last minute is a crash loop, not a healthy container.

The `PORTS` column lists only **published** mappings — the ones that actually
reach the container from the host. Merely exposed ports appear in the JSON with
no `host_port` but are left out of the table, where `docker ps` lists them in a
way that reads as reachable. IPv4 and IPv6 bindings of one mapping are collapsed.

---

## `docker_container_logs`

| Parameter | Type | Default | Meaning |
| --- | --- | --- | --- |
| `container` | string | required | container name |
| `tail` | integer | last lines | how many lines from the end |
| `since_seconds` | integer | — | only lines newer than this |
| `timestamps` | boolean | `false` | prefix each line with its timestamp |

stdout and stderr interleaved in order. This is the follow-up to any finding
from `docker_container_status`: the status says a container is broken, the logs
say why.

**Most images log to stdout**, which the daemon captures — so the log exists
nowhere in the container's own filesystem, and looking for a file with a shell
command would find nothing. This reads it from the only place it exists.

Output is capped at 16 KiB, and truncation is reported.

---

## The two that act

Both are **off unless you turn them on**, and both are guarded by three
independent layers: an allowlist, your approval, and a fingerprint tying that
approval to the exact operation. The full reasoning is in
[docs/modules/docker.md](../docs/modules/docker.md).

### Turning them on

`HOMELAB_MCP_ALLOW_CONTAINER_NAMES` is a comma-separated list of container names
— the `NAMES` column of `docker ps`, not the image and not the id:

```json
{
  "mcpServers": {
    "homelab": {
      "command": "/absolute/path/to/Homelab-MCP/bin/server",
      "env": { "HOMELAB_MCP_ALLOW_CONTAINER_NAMES": "jellyfin,sonarr" }
    }
  }
}
```

One list covers both tools: a shell inside a container already carries the power
to take that container down, so "may run commands in X" and "may restart X" are
not meaningfully separable grants.

With the variable unset neither tool is **registered at all**. Nothing the model
or the client does can widen the list, and **it is read once, at startup** —
restart your MCP client after changing it.

> ⚠️ **Running over SSH? The `env` block above does not reach the server.** It
> sets variables for the local `ssh` process, and SSH does not forward arbitrary
> variables. Put them in the remote command instead, or use a `.env` on the
> remote machine — see the [README](../README.md#configuration).

### `docker_container_exec`

| Parameter | Type | Default | Meaning |
| --- | --- | --- | --- |
| `container` | string | required | must be in the allowlist |
| `command` | string[] | required | argument vector |
| `timeout_seconds` | integer | `30` | max `120` |

Returns stdout, stderr and the exit code.

**`command` is an argument vector, not a shell line.** It is executed directly,
so pipes and redirection do nothing unless you explicitly ask for a shell with
`["sh","-c","…"]` — which the confirmation then flags in so many words. Output
is capped at 16 KiB so a command that dumps a log file cannot flood the model's
context.

### `docker_container_restart`

| Parameter | Type | Default | Meaning |
| --- | --- | --- | --- |
| `container` | string | required | must be in the allowlist |
| `stop_timeout_seconds` | integer | `10` | grace period before SIGKILL, max `120` |

Restarts the container, then **waits and reports whether it actually came back**.
A container that crashes on boot returns to `exited` within seconds; that
outcome is reported rather than assumed.

A note on timing: a container that ignores `SIGTERM` is killed only after the
stop timeout, and the tool then watches it for a few seconds before reporting. A
healthy restart typically takes ten to fifteen seconds end to end — that is the
container's shutdown, not overhead.
