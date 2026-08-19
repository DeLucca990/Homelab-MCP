# Homelab MCP

An [MCP](https://modelcontextprotocol.io) server that exposes a Linux home server
to an AI assistant: system health, Docker containers, Radarr, Sonarr and Jellyfin.

It is a single static Go binary that speaks MCP over **Streamable HTTP**. Run it on the machine
you want to watch, point any MCP client on your tailnet at it, and you can ask *"is my server
running out of disk?"* — or *"why hasn't Dune downloaded?"* — instead of SSH-ing in.

## Tools

**30 tools — one overview and five families — and never all at once.** A default install
registers 8; the rest appear only when the environment authorises them. Full specifications,
one page per family:

| Family | Tools | Reference |
| --- | --- | --- |
| **Overview** | one call that checks every family below and reports only what is wrong | [tools/README.md](tools/README.md#homelab_overview) |
| **System** | host info, CPU, memory, disk, systemd units | [tools/SYSTEM.md](tools/SYSTEM.md) |
| **Docker** | container status, logs, and — opt-in — exec and restart | [tools/DOCKER.md](tools/DOCKER.md) |
| **Radarr** | library, queue, lookup, health, and four writes | [tools/RADARR.md](tools/RADARR.md) |
| **Sonarr** | library, missing episodes, queue, lookup, health, and five writes | [tools/SONARR.md](tools/SONARR.md) |
| **Jellyfin** | who is watching what and what it costs, and the server's own health | [tools/JELLYFIN.md](tools/JELLYFIN.md) |

What they are for, in one line each: [tools/README.md](tools/README.md).

It also exposes two surfaces that are not tools: **prompts**, which state the order to
diagnose something in — *[triage](tools/PROMPTS.md#triage)* and
*[why-no-download](tools/PROMPTS.md#why-no-download)* — and **resources**, which hold the
reference data the tools accept: the [quality profiles and root
folders](tools/RESOURCES.md) each `*arr` actually has, and
[what this install registered](tools/RESOURCES.md#homelabserverconfiguration) along with the
variable that would enable whatever it did not.

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
- **A pinned CPU that is not a fault.** A media server transcoding and one in trouble look
  identical from a load average — and "transcoding" itself covers a container rewrite costing
  nothing and a re-encode costing a whole core, per viewer.
- **A stream playing to nobody.** A client that lost its network is still *playing* as far as
  Jellyfin is concerned, and the transcode behind it is still running.
- **A library scan that has been failing.** Radarr reports the film imported, the file is on
  disk, and it is in no library anyone can see.

## Requirements

- **Go 1.26+** to build ([install](https://go.dev/dl/))
- **Linux** for the intended target. It compiles and runs on macOS and Windows too, but the
  filtering heuristics (snap packages, Docker layers) and the CPU breakdown assume a Linux host.
- **Node.js** only for the MCP Inspector (`make inspect`) or the `mcp-remote` bridge

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
| `SERVER_URL` + `JELLYFIN_API_KEY` | both Jellyfin tools | [tools/JELLYFIN.md](tools/JELLYFIN.md#configuration) |
| `HOMELAB_MCP_TRUST_CLIENT_CONFIRMATION` | acting on clients that cannot show a server confirmation | [below](#approving-actions) |
| `HOMELAB_MCP_HTTP_ADDR` + `HOMELAB_MCP_HTTP_TOKEN` | **required** — the address it listens on and the token it demands | [below](#over-http-instead) |

### A `.env` file

The simplest way to configure a server, and the only one that works the same on every machine
with a clone: a `.env` at the root of the repo, read on startup.

```sh
# .env — chmod 600, and already in .gitignore
SERVER_URL=http://localhost
RADARR_API_KEY=your-radarr-key
SONARR_API_KEY=your-sonarr-key
JELLYFIN_API_KEY=your-jellyfin-key
HOMELAB_MCP_ALLOW_CONTAINER_NAMES=jellyfin,sonarr,radarr
```

One `SERVER_URL` serves all three services because each fills in its own port — Radarr's 7878,
Sonarr's 8989, Jellyfin's 8096 — so keep it a bare host. Written with a port
(`http://nas:7878`) it can only reach one of them.

`make build` and run — no wrapper, no shell. The rules:

- **Where it is looked for**, in order: next to the executable, then one directory above it
  (`bin/server` → the root of the repo), then the working directory. The cwd is last and cannot
  be relied on — a systemd unit without `WorkingDirectory=` runs with `/` — but it is what lets
  `go run ./cmd/server` from the repo root find the file, since `go run` builds the binary into
  a temporary directory.
- **The environment always wins.** A variable already set is left alone, so a systemd
  `EnvironmentFile` or `VAR=x ./bin/server` still overrides the file. It is a fallback, not an authority.
- **Syntax**: `KEY=VALUE`, one per line, parsed by
  [`joho/godotenv`](https://github.com/joho/godotenv). `#` comments, blank lines and a leading
  `export` are fine. Only the first `=` splits, so a base64 key ending in `=` survives. Quote a
  value to protect spaces or a `#`. **A `$` is expanded**, so single-quote any secret containing
  one — `KEY='a$b'`. Unquoted or in double quotes it is read as a variable reference and the
  value arrives truncated. A malformed line is an error quoting the text it choked on.
- Finding no file at all is not an error — most installs configure the server another way.

Prefer a wrapper? The same file is valid shell:

```sh
#!/bin/sh
set -a; . /home/ubuntu/repos/Homelab-MCP/.env; set +a
exec /home/ubuntu/repos/Homelab-MCP/bin/server
```

**What to avoid is the `VAR=value command` prefix for an API key.** The whole command line is
world-readable through `/proc`, so `ps aux` on that machine shows it to any local user. A
container allowlist is not a secret and is fine there; a key is not. For the same reason, keep
the `.env` itself `chmod 600` — the server does not check the permissions for you.

> ⚠️ **An `env` block in your client's config does not reach this server.** The client speaks
> HTTP to a process that was already running, with its own environment — nothing is handed over
> at connect time. Configure the **machine being monitored**: a `.env` in its clone, or a
> systemd `EnvironmentFile`.

### Checking whether it took

The server states what it registered on startup, on stderr — `journalctl -u homelab-mcp` for a
systemd install:

```
[homelab-mcp] radarr at http://localhost:7878: read and write
[homelab-mcp] sonarr at http://localhost:8989: read and write
[homelab-mcp] jellyfin at http://localhost:8096: read-only
[homelab-mcp] MCP server running on transport streamable http at http://100.101.102.103:3000/mcp
```

A service missing from those lines was not configured, and its tools were never registered. If
the assistant says it has no way to act on something, that is why: it is telling the truth.

The `.env` itself is read silently. Only a failure to read one is logged, and it is not fatal:

```
[homelab-mcp] env file: /home/ubuntu/repos/Homelab-MCP/.env: unexpected character "\n" in variable name near "BROKEN LINE" — continuing without it
```

So a file that seems to have had no effect is either off the search path, or holds a variable
that was already set in the environment and lost to it. Neither case is reported — check the
service lines above against the `.env` you expected to be read.

## Approving actions

Eleven tools change something, and none of them act on the first call. They describe the
operation, wait for a decision, and bind the approval to that exact operation with a
fingerprint — so approving `ls /config` cannot execute `rm -rf /config`, approving one
film cannot add another, and approving a search of season 3 cannot search all nine seasons.

Which side asks depends on the client:

- **Clients that support MCP elicitation** — Claude Code since 2.1.76, March 2026 — get the
  request from the server, showing the exact operation. This is per-command, and setting the
  tool to always-allow does not bypass it.
- **Clients that do not** — Claude Desktop still among them as of August 2026 — cannot be asked
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

The server speaks MCP over **Streamable HTTP** and logs to **stderr** with the `[homelab-mcp]`
prefix. It is a long-lived process: one of it serves every client that can reach the address,
so it is started by a service manager, never by a client.

Two variables are required, and it refuses to start without either:

```sh
# .env, on the machine being monitored
HOMELAB_MCP_HTTP_ADDR=100.101.102.103:3000   # this host's tailscale address
HOMELAB_MCP_HTTP_TOKEN=<openssl rand -hex 32>
```

```sh
./bin/server
# [homelab-mcp] MCP server running on transport streamable http at http://100.101.102.103:3000/mcp
```

**Write the host part explicitly.** A bare `:3000` binds every interface the machine has,
including the one facing your LAN; the server says so at startup when you do.

Every request must carry `Authorization: Bearer <token>`, and anything else gets a 401 without
reaching a tool. The token is not optional and there is no flag that makes it optional. A
private bind address is not authentication: it is a bet that nothing hostile is on that
network, and a tailnet holds every device of every user your ACLs admit, plus whatever runs on
this host.

It shuts down cleanly on `Ctrl+C` (SIGINT) and on SIGTERM, so it behaves under systemd.

### Under systemd

Nothing execs this binary, so something has to keep it alive across logout and reboot. Run it
in a terminal and it dies with your SSH session:

```ini
# /etc/systemd/system/homelab-mcp.service
[Unit]
Description=Homelab MCP server
After=network-online.target tailscaled.service
Wants=network-online.target

[Service]
ExecStart=/home/ubuntu/repos/Homelab-MCP/bin/server
User=ubuntu
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

```sh
sudo systemctl daemon-reload && sudo systemctl enable --now homelab-mcp
journalctl -u homelab-mcp -f
```

No `EnvironmentFile` is needed: the binary finds the `.env` itself, next to the executable and
one directory above it. That also avoids the differences between systemd's parser and
[this one](#a-env-file). The `User=` must be in the `docker` group for the container tools.

Binding a tailscale address races with `tailscaled` on boot, and the bind fails with
`can't assign requested address` until the interface exists. `Restart=always` rides it out — a
failed start or two right after a reboot is expected. Binding `127.0.0.1` behind
[`tailscale serve`](#behind-tailscale) avoids the race entirely.

To update: `git pull && make build && sudo systemctl restart homelab-mcp`. Clients need no
attention, because there is no session for a restart to invalidate.

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
itself the symptom. Set `HOMELAB_MCP_TRUST_CLIENT_CONFIRMATION=1` to accept the approval
prompt that client shows before calling a tool — which is what Claude Desktop needs today.

Note also that there is no connect-time line in the log to consult: it belonged to the old
handshake, which a sessionless server never performs. The refusal above is the whole signal —
and, once the variable is set, the line the server writes each time it proceeds on the client's
prompt instead of its own.

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

#### With the MCP Inspector

The fastest way to see it working, no client configuration needed:

```sh
make inspect        # or: make inspect-open, which opens the browser for you
```

It starts with nothing to spawn — this server is connected to, not exec'd. Pick **Streamable
HTTP** in the UI, paste the URL from the startup line, and add an `Authorization` header with
`Bearer <token>`.

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
tools/     what each tool, prompt and resource takes and answers — the specification
docs/      how it is built and why — the design
```

| | |
| --- | --- |
| [tools/README.md](tools/README.md) | the tool index, the overview tool, and the conventions every tool shares |
| [tools/PROMPTS.md](tools/PROMPTS.md) · [tools/RESOURCES.md](tools/RESOURCES.md) | the two surfaces that are not tools |
| [tools/SYSTEM.md](tools/SYSTEM.md) · [tools/DOCKER.md](tools/DOCKER.md) · [tools/RADARR.md](tools/RADARR.md) · [tools/SONARR.md](tools/SONARR.md) · [tools/JELLYFIN.md](tools/JELLYFIN.md) | per-family reference |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | layering, tool registration, the confirmation round trip, the fingerprint |
| [docs/modules/](docs/modules/) | one page per integration: [system](docs/modules/system.md), [docker](docs/modules/docker.md), [radarr](docs/modules/radarr.md), [sonarr](docs/modules/sonarr.md), [jellyfin](docs/modules/jellyfin.md) |

## Project layout

```
cmd/server/          entrypoint: .env loading, signal handling, serving
internal/dotenv/     reads a .env into the environment before anything is registered
internal/mcp/        MCP layer — tool registration, schemas, text rendering, confirmation,
                     prompts, resources, and the HTTP transport with its bearer auth
internal/overview/   every cheap check at once, composed from the collectors below
internal/system/     collection layer — gopsutil calls, no MCP types
internal/services/   systemd units, over systemctl
internal/containers/ docker, over the Engine API on the unix socket
internal/radarr/     radarr, over its v3 HTTP API
internal/sonarr/     sonarr, over its v3 HTTP API
internal/jellyfin/   jellyfin, over its HTTP API
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

The Jellyfin client is covered the same way, against a mock Jellyfin: the authorization header
it sends — which is the one thing it cannot borrow from the `*arr` clients, and a regression
there would look exactly like an expired key — the tick and timestamp conversions, the
four-way split of what a stream actually costs, a stale session told apart from a paused one,
and a health call that degrades into warnings rather than failing when the key turns out not
to be an administrator key.

It also covers the surfaces added around those: the overview, including that a service being
down does not withhold the checks that worked, and — over the SDK's in-memory transport, so
what is asserted is what a client sees — that the prompts and resources appear only where the
tools they name were registered.

## Built with

- [modelcontextprotocol/go-sdk](https://github.com/modelcontextprotocol/go-sdk) — MCP protocol
- [shirou/gopsutil](https://github.com/shirou/gopsutil) — system metrics

## Contributions
Feel free to reach me out or open a PR!
