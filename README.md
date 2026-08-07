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

## What it can and cannot do

**Every tool is read-only.** Nothing here starts, stops, restarts, writes, or deletes
anything — the server observes and reports.

Most data comes from reading the kernel through [gopsutil](https://github.com/shirou/gopsutil).
The one exception is `system_service_status`, which invokes `systemctl`. Two properties bound
what that can do:

- **No shell.** The binary is executed through `execve` with an argument vector, not through
  `sh -c`. Shell metacharacters in any input are inert — there is no string a caller can supply
  that becomes a second command.
- **Fixed verbs, validated arguments.** Only `list-units` and `show` are ever invoked, both
  read-only, and neither is caller-selectable. Unit names supplied by the caller are validated
  against systemd's own naming rules before execution, so an argument cannot be smuggled in as
  an option (`systemctl --host=…` would otherwise open an outbound SSH connection).

The server runs with the privileges of whoever launches it, which is normally your MCP client,
not root. Read-only tools need no elevation.

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