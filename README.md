# Homelab MCP

An [MCP](https://modelcontextprotocol.io) server that exposes the health of a Linux server.
CPU, memory, disk and host info as tools an AI assistant can call.

It is a single static Go binary that speaks MCP over **stdio**. Point any MCP client at it
(Claude Code, Claude Desktop, the MCP Inspector) and you can ask *"is my server running out
of disk?"* instead of SSH-ing in to run `df -h`.

## Tools

| Tool | Input | What it answers |
| --- | --- | --- |
| `system_host_info` | — | hostname, OS, kernel version, architecture, uptime, process count |
| `system_cpu_cores` | `sample_ms` (optional, default `500`, max `5000`) | per-core usage split into user / kernel / nice / IRQ / I/O wait — htop's per-core bars |
| `system_memory_stats` | — | RAM and swap usage, with `available` and `used_percent` as the pressure signals |
| `system_disk_usage` | `include_all` (optional, default `false`) | disk usage per mountpoint, fullest first, plus inode usage |
| `system_service_status` | `units` (optional), `include_all` (optional, default `false`) | systemd service state — failed, stuck starting, or crash-looping units, worst first |
| `docker_container_status` | `names` (optional), `include_all` (optional, default `false`) | Docker container state — OOM kills, crash loops, failing healthchecks, worst first |
| `docker_container_logs` | `container`, `tail` / `since_seconds` / `timestamps` (optional) | what a container wrote to stdout and stderr — the "why" behind any finding above |
| `docker_container_exec` | `container`, `command`, `timeout_seconds` (optional) | runs a command in an allowlisted container — **opt-in, and asks you before every run** (see below) |
| `docker_container_restart` | `container`, `stop_timeout_seconds` (optional) | restarts an allowlisted container and reports whether it came back — **opt-in, and asks you before every run** |

Every tool returns both a compact text rendering and structured JSON, so a client can use the
table or the raw numbers. Sizes in the JSON are plain byte counts on `*_bytes` fields; the
human-readable units are rendered once, in the text, rather than duplicated per value.

Details the tools handle that a plain `df -h` / `free -h` / `systemctl status` will not:

- **Inode exhaustion.** A filesystem can fail with `no space left on device` while still showing
  free bytes. `system_disk_usage` warns above 80% inode usage.
- **Hung network mounts.** `statfs` on an unreachable NFS/SMB mount blocks in the kernel and
  ignores context cancellation. Each mountpoint is queried with a 2s timeout, so one dead mount
  degrades to a warning instead of hanging the whole call.
- **Crash loops that look healthy.** A service restarting every few seconds is `active` in any
  point-in-time check. `system_service_status` reports the restart count and how long the unit
  has actually held its current state, so "up" and "up for 9 seconds after 800 restarts" are
  told apart — and it names the reason systemd recorded, including OOM kills.
- **Containers killed for their memory limit.** `docker ps` shows a container that exceeded its
  limit as `Exited (137)`, which reads like an application error. `docker_container_status`
  reports the OOM flag directly, alongside healthcheck results and restart counts.
- **Logs that exist nowhere on disk.** Most images write to stdout, which the daemon captures —
  so there is no log file inside the container, and looking for one with a shell finds nothing.
  `docker_container_logs` reads them from the daemon, which is the only place they exist.

## Acting on containers (opt-in)

Two tools can change something, and both are **off unless you turn them on**:

- `docker_container_exec` — runs a command inside a container, returning stdout, stderr and
  exit code.
- `docker_container_restart` — restarts a container, then waits and reports whether it
  actually came back. A container that crashes on boot returns to `exited` within seconds, and
  that outcome is reported rather than assumed.

Each is guarded by three independent layers.

**1. An allowlist you set, naming the containers it may touch.** The two tools have
**separate** lists, because debugging inside a service and taking it offline are different
grants. Each holds a comma-separated list of container names (the `NAMES` column of
`docker ps` — not the image, not the id):

```json
{
  "mcpServers": {
    "homelab": {
      "command": "/absolute/path/to/Homelab-MCP/bin/server",
      "env": {
        "HOMELAB_MCP_EXEC_ALLOW_CONTAINER_NAMES": "jellyfin,sonarr",
        "HOMELAB_MCP_RESTART_ALLOW_CONTAINER_NAMES": "jellyfin"
      }
    }
  }
}
```

A tool whose variable is unset is **not registered at all** — it does not appear in
`tools/list`, so the model cannot call it. With neither set, the server is entirely read-only.
Nothing the model or the client does can widen these lists.

**2. Your approval, for every single command.** The server requests confirmation through the
protocol itself (SEP-2322 input requests, which degrade to elicitation on older clients) rather
than relying on your client's own prompting — so approving the tool once does not turn it into
a blank cheque. You see the command verbatim before deciding. **If your client cannot ask you,
the command does not run**: this fails closed.

**3. A fingerprint tying the approval to the command.** The approved container and argument
vector are hashed into the request state and re-checked before execution, so what runs is
provably what was shown.

Two further properties of `exec`: the command is an **argument vector, not a shell line** — it
is executed directly, so pipes and redirection do nothing unless you explicitly ask for a shell
with `["sh","-c","…"]`, which the confirmation then flags. And output is capped, so a command
that dumps a log file cannot flood the model's context.

A note on restart timing: a container that ignores `SIGTERM` is killed only after Docker's
stop timeout (10s by default — lower it with `stop_timeout_seconds`), and the tool then watches
it for a few seconds before reporting. A healthy restart typically takes ten to fifteen seconds
end to end; that is the container's shutdown, not overhead.

## What the rest can and cannot do

**Every other tool is read-only.** Nothing else starts, stops, restarts, writes, or deletes
anything — they observe and report.

Most data comes from reading the kernel through [gopsutil](https://github.com/shirou/gopsutil).
Two tools reach further, and each is bounded:

**`system_service_status`** invokes `systemctl`:

- **No shell.** The binary is executed through `execve` with an argument vector, not through
  `sh -c`. Shell metacharacters in any input are inert — there is no string a caller can supply
  that becomes a second command.
- **Fixed verbs, validated arguments.** Only `list-units` and `show` are ever invoked, both
  read-only, and neither is caller-selectable. Unit names supplied by the caller are validated
  against systemd's own naming rules before execution, so an argument cannot be smuggled in as
  an option (`systemctl --host=…` would otherwise open an outbound SSH connection).

**`docker_container_status`** talks to the Docker daemon over its unix socket:

- **Read-only endpoints only.** It issues `GET /containers/json` and `GET /containers/{id}/json`.
  Nothing starts, stops, or removes a container.
- **No caller-supplied string reaches a request path.** Filtering by name is applied to the
  listing in Go; every container id interpolated into a URL came from Docker itself.
- Note that the docker socket is root-equivalent by design. This tool only reads through it,
  but anything that can reach that socket can control the daemon — so grant access to it the
  same way you would grant root.

The server runs with the privileges of whoever launches it, which is normally your MCP client,
not root. The read-only system tools need no elevation; Docker access requires membership in
the `docker` group.

## Requirements

- **Go 1.26+** to build ([install](https://go.dev/dl/))
- **Linux** for the intended target. It compiles and runs on macOS and Windows too, but the
  filtering heuristics (snap packages, Docker layers) and the CPU breakdown assume a Linux host.
- **Node.js** only if you want to run the MCP Inspector (`make inspect`)

## Clone and build

```sh
git clone https://github.com/DeLucca990/Homelab-MCP.git
cd Homelab-MCP
make build
```

That produces the binary at `bin/server`. Equivalent without `make`:

```sh
go build -o bin/server ./cmd/server
```

## Running it

The server talks JSON-RPC over stdin/stdout and logs to **stderr** with the `[homelab-mcp]`
prefix. Running it by hand just leaves it waiting for a client on stdin:

```sh
./bin/server
# [homelab-mcp] MCP server running on transport stdio
```

It shuts down cleanly on `Ctrl+C` (SIGINT) and on SIGTERM, so it behaves under a process
supervisor like systemd. Normally you do not launch it yourself — the MCP client spawns it.

### With the MCP Inspector

The fastest way to see it working, no client configuration needed:

```sh
make inspect        # or: make inspect-open, which opens the browser for you
```

This runs `npx @modelcontextprotocol/inspector go run ./cmd/server` and gives you a UI to list
and call the tools.

### With Claude Code

```sh
claude mcp add homelab -- /absolute/path/to/Homelab-MCP/bin/server
```

### With Claude Desktop

Add the server to `claude_desktop_config.json` (macOS:
`~/Library/Application Support/Claude/claude_desktop_config.json`):

```json
{
  "mcpServers": {
    "homelab": {
      "command": "/absolute/path/to/Homelab-MCP/bin/server"
    }
  }
}
```

Use an absolute path — the client does not resolve the binary against your shell's `PATH` or
working directory. Restart the client after editing the config.

### Monitoring a remote server

Because the transport is stdio, you can put SSH in front of the binary and monitor a machine
that is not the one running the client:

```json
{
  "mcpServers": {
    "homelab": {
      "command": "ssh",
      "args": ["user@homelab", "/opt/homelab-mcp/bin/server"]
    }
  }
}
```

This requires key-based SSH auth — there is no terminal to type a password into.

## Example output

`system_cpu_cores`:

```
cpu0    30.0%  (usr 16.0  sys 14.0  io 0.0)
cpu1    18.0%  (usr 10.0  sys 8.0  io 0.0)
cpu2    12.2%  (usr 8.2  sys 4.1  io 0.0)
cpu3    10.0%  (usr 4.0  sys 6.0  io 0.0)
```

The full per-core breakdown — including nice, IRQ and steal — is in the structured
JSON alongside this summary.

`system_memory_stats`:

```
             total        used        free      shared  buff/cache   available
Mem:          24Gi        16Gi       272Mi          0B        4.2Gi       7.7Gi
Swap:        3.0Gi       1.7Gi       1.3Gi
```

`system_disk_usage`:

```
Filesystem      Size  Used  Avail  Use%  Mounted on
/dev/sda2       916G  871G    45G   95%  /
/dev/sda1       511M   34M   478M    7%  /boot/efi

warning: / is at 92% inode usage (54983168 of 59768832) — it can fail with
"no space left on device" even with free space

(14 mounts filtered out; use include_all to see them)
```

`system_service_status`:

```
UNIT               LOAD    ACTIVE      SUB     RESTARTS  FOR
jellyfin.service   loaded  failed      failed         5  1m
sonarr.service     loaded  failed      failed         0  2m
nginx.service      loaded  activating  start          0  2m

warning: jellyfin.service failed — was killed by the OOM killer, after 5 restarts
(systemd stopped retrying)

warning: sonarr.service failed — exited with code 3

warning: nginx.service has been starting for 122s without reaching active — it may be stuck

(41 healthy units omitted; use include_all to see them)
```

On a healthy host it answers plainly rather than returning an empty list:

```
no services needing attention (41 units, 11 active, 0 failed)
```

`docker_container_status`:

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

The `PORTS` column lists only **published** mappings — the ports that actually reach the
container from the host. Ports that are merely exposed appear in the structured JSON with no
`host_port`, but are left out of the table, where `docker ps` lists them in a way that reads as
reachable. IPv4 and IPv6 bindings of the same mapping are collapsed into one entry.

## Project layout

```
cmd/server/         entrypoint: signal handling, stdio transport
internal/mcp/       MCP layer — tool registration, schemas, text rendering
internal/system/    collection layer — gopsutil calls, no MCP types
internal/services/  reserved for systemd unit inspection (not implemented yet)
```

The split is deliberate: `internal/system` knows nothing about MCP, so the collectors stay
testable and reusable, and `internal/mcp` owns everything the model sees — tool descriptions,
JSON schemas and the rendered tables.

## Built with

- [modelcontextprotocol/go-sdk](https://github.com/modelcontextprotocol/go-sdk) — MCP protocol
- [shirou/gopsutil](https://github.com/shirou/gopsutil) — system metrics

## Contributions
Feel free to reach me out or open a PR!