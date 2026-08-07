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

Every tool returns both a human-readable text rendering and structured JSON, so the model can
read the table or the raw numbers.

Two details the tools handle that a plain `df -h` / `free -h` will not:

- **Inode exhaustion.** A filesystem can fail with `no space left on device` while still showing
  free bytes. `system_disk_usage` warns above 80% inode usage.
- **Hung network mounts.** `statfs` on an unreachable NFS/SMB mount blocks in the kernel and
  ignores context cancellation. Each mountpoint is queried with a 2s timeout, so one dead mount
  degrades to a warning instead of hanging the whole call.

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
cpu0   [|||||||||                     ]  30.0%  (usr 16.0  sys 14.0  io 0.0)
cpu1   [|||||                         ]  18.0%  (usr 10.0  sys 8.0  io 0.0)
cpu2   [||||                          ]  12.2%  (usr 8.2  sys 4.1  io 0.0)
cpu3   [|||                           ]  10.0%  (usr 4.0  sys 6.0  io 0.0)
```

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