# Homelab MCP

An [MCP](https://modelcontextprotocol.io) server that exposes a Linux home server
to an AI assistant: system health, Docker containers, Radarr and Sonarr.

It is a single static Go binary that speaks MCP over **stdio** or over **HTTP**. Point any MCP
client at it (Claude Code, Claude Desktop, the MCP Inspector) and you can ask *"is my server
running out of disk?"* — or *"why hasn't Dune downloaded?"* — instead of SSH-ing in.

## Tools

**27 tools in four families, and never all at once.** A default install registers 7; the
rest appear only when the environment authorises them. Full specifications, one page per
family:

| Family | Tools | Reference |
| --- | --- | --- |
| **System** | host info, CPU, memory, disk, systemd units | [tools/SYSTEM.md](tools/SYSTEM.md) |
| **Docker** | container status, logs, and — opt-in — exec and restart | [tools/DOCKER.md](tools/DOCKER.md) |
| **Radarr** | library, queue, lookup, health, and four writes | [tools/RADARR.md](tools/RADARR.md) |
| **Sonarr** | library, missing episodes, queue, lookup, health, and five writes | [tools/SONARR.md](tools/SONARR.md) |

What they are for, in one line each: [tools/README.md](tools/README.md).

The point of the whole thing is the details a plain `df -h` / `docker ps` / a glance at
Radarr will not tell you:

- **Inode exhaustion.** A filesystem can fail with `no space left on device` while still
  showing free bytes.
- **Crash loops that read as healthy.** A service or container restarting every few seconds
  is `active` and `running` in any point-in-time check.
- **Containers killed for their memory limit.** `docker ps` shows `Exited (137)`, which reads
  like an application error.
- **Logs that exist nowhere on disk.** Most images write to stdout, so looking for a log file
  inside the container finds nothing.
- **Downloads that are not downloading.** A stalled torrent and a healthy one are both a row
  with a progress bar — and a download that finished but could not be imported leaves the file
  on disk with the movie still missing.
- **A film that is late versus one that is not out.** Radarr shows both as monitored with no
  file; only one of them is a problem.
- **A series is never simply there.** "Monitored" says nothing about a show with 59 of its
  62 episodes on disk — the answer is a count, and which three are missing is a level below
  that again.
- **One download, fourteen queue rows.** A Sonarr season pack is a single file that appears
  once per episode it holds, so removing any one of those rows removes all of them.

## Requirements

- **Go 1.26+** to build ([install](https://go.dev/dl/))
- **Linux** for the intended target. It compiles and runs on macOS and Windows too, but the
  filtering heuristics (snap packages, Docker layers) and the CPU breakdown assume a Linux host.
- **Node.js** only if you want to run the MCP Inspector (`make inspect`)

Docker access needs membership in the `docker` group. The read-only system tools need no
elevation, and the server runs with the privileges of whoever launches it — normally your MCP
client, not root.

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

## Configuration

Everything is environment variables, read **once, at startup** — restart your MCP client after
changing any of them.

| Variable | Enables | Reference |
| --- | --- | --- |
| `HOMELAB_MCP_ALLOW_CONTAINER_NAMES` | `docker_container_exec`, `docker_container_restart` | [tools/DOCKER.md](tools/DOCKER.md#turning-them-on) |
| `SERVER_URL` + `RADARR_API_KEY` | the whole Radarr family | [tools/RADARR.md](tools/RADARR.md#configuration) |
| `HOMELAB_MCP_RADARR_READONLY` | drops Radarr's four writes | [tools/RADARR.md](tools/RADARR.md#configuration) |
| `SERVER_URL` + `SONARR_API_KEY` | the whole Sonarr family | [tools/SONARR.md](tools/SONARR.md#configuration) |
| `HOMELAB_MCP_SONARR_READONLY` | drops Sonarr's five writes | [tools/SONARR.md](tools/SONARR.md#configuration) |
| `HOMELAB_MCP_TRUST_CLIENT_CONFIRMATION` | acting on clients that cannot show a server confirmation | [below](#approving-actions) |
| `HOMELAB_MCP_ENV_FILE` | an explicit path to the `.env` | [below](#a-env-file) |
| `HOMELAB_MCP_HTTP_ADDR` + `HOMELAB_MCP_HTTP_TOKEN` | the HTTP transport, instead of stdio | [below](#over-http-instead) |

### A `.env` file

The simplest way to configure a server, and the only one that works the same on every machine
with a clone: a `.env` at the root of the repo, read on startup.

```sh
# .env — chmod 600, and already in .gitignore
SERVER_URL=http://localhost
RADARR_API_KEY=your-radarr-key
SONARR_API_KEY=your-sonarr-key
HOMELAB_MCP_ALLOW_CONTAINER_NAMES=jellyfin,sonarr,radarr
```

One `SERVER_URL` serves both services because each fills in its own port — Radarr's 7878,
Sonarr's 8989 — so keep it a bare host. Written with a port (`http://nas:7878`) it can only
reach one of them.

`make build` and run — no wrapper, no shell. The rules:

- **Where it is looked for**, in order: next to the executable, then one directory above it
  (`bin/server` → the root of the repo), then the working directory. The cwd is last and cannot
  be relied on: a client that execs this binary sets it to whatever it happens to be. Point at
  a file elsewhere with `HOMELAB_MCP_ENV_FILE=/path/to/file`, which then must exist.
- **The environment always wins.** A variable already set is left alone and logged as such, so
  an ssh command prefix, a systemd `EnvironmentFile` or `VAR=x ./bin/server` still override the
  file. It is a fallback, not an authority.
- **Syntax**: `KEY=VALUE`, one per line. `#` comments, blank lines and a leading `export` are
  fine. Only the first `=` splits, so a base64 key ending in `=` survives. Quote a value to
  protect spaces or a `#`. A malformed line is an error naming the line number rather than a
  variable that silently never arrives.
- Finding no file at all is not an error — most installs configure the server another way.

Prefer a wrapper? The same file is valid shell:

```sh
#!/bin/sh
set -a; . /home/ubuntu/repos/Homelab-MCP/.env; set +a
exec /home/ubuntu/repos/Homelab-MCP/bin/server
```

**What to avoid is the `VAR=value command` prefix for an API key.** The whole command line is
world-readable through `/proc`, so `ps aux` on that machine shows it to any local user. A
container allowlist is not a secret and is fine there; a key is not. For the same reason the
server warns at startup if the `.env` it read is not `chmod 600`.

> ⚠️ **Running over SSH?** An `env` block in your client config sets variables for the local
> `ssh` process, and SSH does not forward arbitrary variables (that would need `SendEnv` plus a
> matching `AcceptEnv` in the remote `sshd_config`). Configure the **remote** machine — a `.env`
> in its clone, a wrapper, or a systemd `EnvironmentFile`.

### Checking whether it took

The server states what it found on startup, on stderr, in your client's MCP log:

```
[homelab-mcp] env file /home/ubuntu/repos/Homelab-MCP/.env: set SERVER_URL, RADARR_API_KEY, SONARR_API_KEY
[homelab-mcp] env file /home/ubuntu/repos/Homelab-MCP/.env: SERVER_URL already set in the environment, left alone
[homelab-mcp] radarr at http://localhost:7878: read and write
[homelab-mcp] sonarr at http://localhost:8989: read and write
[homelab-mcp] client connected: name="claude-ai"; confirmations for actions: server-side, per command
```

Variable **names** only — the values are the reason the file has restrictive permissions. If
the assistant says it has no way to act on something, these lines are why: the tools were never
registered, so it is telling the truth.

## Approving actions

Eleven tools change something, and none of them act on the first call. They describe the
operation, wait for a decision, and bind the approval to that exact operation with a
fingerprint — so approving `ls /config` cannot execute `rm -rf /config`, approving one
film cannot add another, and approving a search of season 3 cannot search all nine seasons.

Which side asks depends on the client:

- **Clients that support MCP elicitation** get the request from the server, showing the exact
  operation. This is per-command, and setting the tool to always-allow does not bypass it.
- **Clients that do not** — Claude Desktop among them at the time of writing — cannot be asked
  anything by the server, so it refuses to act. They prompt for tool approval themselves, and
  you can tell the server to accept that with `HOMELAB_MCP_TRUST_CLIENT_CONFIRMATION=1`.

That second setting is you vouching for your client, and it has to be: the identity a client
reports is self-declared and unauthenticated, so a server cannot recognise a trustworthy client
— it can only be told. Know what you are accepting: the client's prompt is per-tool rather than
per-command, and a client set to always-allow stops asking. The Docker allowlist still holds in
every case.

The mechanism, and what the fingerprint does and does not protect:
[docs/ARCHITECTURE.md §3](docs/ARCHITECTURE.md#3-waiting-for-a-user-response).

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

This requires key-based SSH auth — there is no terminal to type a password into. Configure the
remote machine itself, not the `env` block here.

### Over HTTP instead

SSH costs a whole server process per client, and the client has to be able to exec a binary on
the other machine. The alternative is the MCP **Streamable HTTP** transport: one long-lived
process serving every client that can reach the address, with the `.env` staying on the server
where the key belongs.

Set an address and a token, and the transport changes:

```sh
# .env on the server
HOMELAB_MCP_HTTP_ADDR=100.101.102.103:8080   # this host's tailscale address
HOMELAB_MCP_HTTP_TOKEN=<openssl rand -hex 32>
```

```
[homelab-mcp] MCP server running on transport streamable http at http://100.101.102.103:8080/mcp (sessionless, protocol 2026-07-28, bearer token required)
```

Every request must carry `Authorization: Bearer <token>`; anything else gets a 401 and never
reaches a tool. **Write the host part explicitly.** A bare `:8080` binds every interface the
machine has, including the one facing your LAN.

The token is not optional and there is no flag that makes it optional — the server refuses to
start without one. A private bind address is not authentication: it is a bet that nothing
hostile is on that network, and a tailnet holds every device of every user your ACLs admit,
plus whatever runs on this host.

#### It is sessionless, and that needs a current client

Revision **2026-07-28** of the MCP spec removed protocol-level sessions, the GET stream and
server-to-client requests. Client identity and capabilities now ride in the `_meta` of every
request, and the server asks the user for input by returning `inputRequests` for the client to
retry — which is how the [confirmation round trip](#approving-actions) here already worked, so
it needs nothing a session would hold.

The HTTP transport implements that revision and only that one. This is not a setting: the SDK
rejects a `2026-07-28` request outright on a server that keeps sessions, so serving the current
protocol and keeping sessions are mutually exclusive, and there is no switch for the other side.
Restarting the process invalidates nothing, since there is no session id for a client to lose.

A client on an older protocol still connects, and every read-only tool answers it. What it
cannot do is the [eleven writes](#approving-actions): it declares its capabilities once, in
`initialize`, and a sessionless server keeps nothing from that — so the server cannot tell
whether it could be shown a confirmation, and it does not act without one. It refuses with
`this client ("") cannot show a confirmation coming from the server`, where the empty name is
itself the symptom. Reach that client over stdio instead, where the session exists.

Note also that the `client connected:` startup line belongs to the old handshake, so over HTTP
it never appears.

Run it under systemd, since nothing execs it any more:

```ini
[Service]
ExecStart=/opt/homelab-mcp/bin/server
EnvironmentFile=/opt/homelab-mcp/.env
Restart=always
```

#### Behind Tailscale

Binding to the tailnet address, as above, is already enough: the port exists only inside your
tailnet, and nothing on your LAN or the internet can route to it. Restrict it further with a
Tailscale ACL — device authentication is not user authorisation, which is the other reason the
bearer token is required.

For a real TLS certificate and a name instead of an IP, bind loopback and put `tailscale serve`
in front:

```sh
# HOMELAB_MCP_HTTP_ADDR=127.0.0.1:8080
tailscale serve --bg --https=443 --set-path=/mcp http://127.0.0.1:8080/mcp
```

That gives you `https://homelab.your-tailnet.ts.net/mcp`, reachable from your tailnet only, with
the `Authorization` header passed straight through. Never `tailscale funnel` — that publishes
these tools to the open internet.

#### Pointing clients at it

**Claude Code**, and any client that speaks HTTP natively:

```sh
claude mcp add --transport http homelab https://homelab.your-tailnet.ts.net/mcp \
  --header "Authorization: Bearer $HOMELAB_MCP_HTTP_TOKEN"
```

**Claude Desktop** needs a bridge. Its custom connectors will not work here: that connection is
made from Anthropic's servers rather than from your machine, so it cannot reach a tailnet
address — and `claude_desktop_config.json` itself accepts stdio servers only. `mcp-remote` is a
stdio server that speaks HTTP on the other side, and it runs on your machine, which is the one
on the tailnet:

```json
{
  "mcpServers": {
    "homelab": {
      "command": "npx",
      "args": [
        "-y", "mcp-remote",
        "https://homelab.your-tailnet.ts.net/mcp",
        "--header", "Authorization: Bearer ${HOMELAB_MCP_TOKEN}"
      ],
      "env": { "HOMELAB_MCP_TOKEN": "your-token" }
    }
  }
}
```

Note that the `env` block *does* work here, unlike over SSH — the variable is read by a local
process, so nothing has to survive a hop.

## Documentation

```
tools/     what each tool takes and answers — the specification
docs/      how it is built and why — the design
```

| | |
| --- | --- |
| [tools/README.md](tools/README.md) | the tool index and the conventions every tool shares |
| [tools/SYSTEM.md](tools/SYSTEM.md) · [tools/DOCKER.md](tools/DOCKER.md) · [tools/RADARR.md](tools/RADARR.md) · [tools/SONARR.md](tools/SONARR.md) | per-family reference |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | layering, tool registration, the confirmation round trip, the fingerprint |
| [docs/modules/](docs/modules/) | one page per integration: [system](docs/modules/system.md), [docker](docs/modules/docker.md), [radarr](docs/modules/radarr.md), [sonarr](docs/modules/sonarr.md) |

## Project layout

```
cmd/server/          entrypoint: .env loading, signal handling, transport choice
internal/dotenv/     reads a .env into the environment before anything is registered
internal/mcp/        MCP layer — tool registration, schemas, text rendering, confirmation,
                     and the HTTP transport with its bearer auth
internal/system/     collection layer — gopsutil calls, no MCP types
internal/services/   systemd units, over systemctl
internal/containers/ docker, over the Engine API on the unix socket
internal/radarr/     radarr, over its v3 HTTP API
internal/sonarr/     sonarr, over its v3 HTTP API
```

The split is deliberate: the collectors know nothing about MCP, so they stay testable and
reusable, and `internal/mcp` owns everything the model sees — tool descriptions, JSON schemas
and the rendered tables.

```sh
go test ./...
```

covers the Radarr client against a mock Radarr — URL normalisation, queue classification,
missing versus unreleased, id resolution and every refusal the add path makes — and the same
for Sonarr, plus what is only true there: episode counting, season packs sharing one
download, and the three search scopes hashing apart so an approval for one season cannot run
against a whole series.

## Built with

- [modelcontextprotocol/go-sdk](https://github.com/modelcontextprotocol/go-sdk) — MCP protocol
- [shirou/gopsutil](https://github.com/shirou/gopsutil) — system metrics

## Contributions
Feel free to reach me out or open a PR!
