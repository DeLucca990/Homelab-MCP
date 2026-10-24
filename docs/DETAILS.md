# Nautilus — the details

An [MCP](https://modelcontextprotocol.io) server that exposes a Linux home server to an AI
assistant — system health, Docker containers, Radarr, Sonarr, Prowlarr, Jellyfin and Bazarr — and
a Telegram bot that is a client of it. A single static Go binary each, speaking MCP over
**Streamable HTTP**. To install the published image, see [INSTALL.md](INSTALL.md).

## Tools

**75 tools — an overview, a digest and seven families — and never all at once.** A default
install registers 9; the rest appear only when the environment authorises them. Full specifications,
one page per family:

| Family | Tools | Reference |
| --- | --- | --- |
| **Overview** | one call that checks every family below and reports only what is wrong | [docs/tools/README.md](tools/README.md#nautilus_overview) |
| **Digest** | the same over the last day: what arrived, failed, is late or restarted, and the totals to compare with tomorrow | [docs/tools/README.md](tools/README.md#nautilus_daily_digest) |
| **System** | host info, CPU, memory, disk, systemd units | [docs/tools/SYSTEM.md](tools/SYSTEM.md) |
| **Docker** | container status, logs, and — opt-in — exec and restart | [docs/tools/DOCKER.md](tools/DOCKER.md) |
| **Radarr** | library, queue, lookup, health, interactive search, import, history, calendar, and seven writes | [docs/tools/RADARR.md](tools/RADARR.md) |
| **Sonarr** | library, missing episodes, queue, lookup, health, interactive search, import, history, calendar, and nine writes | [docs/tools/SONARR.md](tools/SONARR.md) |
| **Prowlarr** | indexer status and tests, app sync, direct search, and four writes | [docs/tools/PROWLARR.md](tools/PROWLARR.md) |
| **Jellyfin** | sessions, health, users, library search, activity log, and seven writes | [docs/tools/JELLYFIN.md](tools/JELLYFIN.md) |
| **Bazarr** | subtitle status, wanted list, providers, manual search, and five writes | [docs/tools/BAZARR.md](tools/BAZARR.md) |

What they are for, in one line each: [docs/tools/README.md](tools/README.md).

It also exposes two surfaces that are not tools: **prompts**, which state the order to
diagnose something in — *[triage](tools/PROMPTS.md#triage)* and
*[why-no-download](tools/PROMPTS.md#why-no-download)* — and **resources**, which hold the
reference data the tools accept: the [quality profiles and root
folders](tools/RESOURCES.md) each `*arr` actually has, and
[what this install registered](tools/RESOURCES.md#nautilusserverconfiguration) along with the
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
- **Node.js** only for the MCP Inspector (`make inspect`)
- Or **Docker** alone, in place of Go — [docs/INSTALL.md](INSTALL.md)

Docker access needs membership in the `docker` group. The read-only system tools need no
elevation, and the server runs with the privileges of whoever launches it — normally the
`User=` of [its systemd unit](#under-systemd), not root.

## Clone and build

```sh
git clone https://github.com/DeLucca990/Homelab-MCP.git
cd Homelab-MCP
make build
```

That produces `bin/server` and `bin/telegram-bot`. Equivalent without `make`:

```sh
go build -o bin/server ./cmd/server
go build -o bin/telegram-bot ./cmd/telegram-bot
```

## Configuration

Everything is environment variables, read **once, at startup** — restart the server (and the
bot, for its own variables) after changing any of them.

| Variable | Enables | Reference |
| --- | --- | --- |
| `NAUTILUS_ALLOW_CONTAINER_NAMES` | `docker_container_exec`, `docker_container_restart` | [docs/tools/DOCKER.md](tools/DOCKER.md#turning-them-on) |
| `SERVER_URL` + `RADARR_API_KEY` | the whole Radarr family | [docs/tools/RADARR.md](tools/RADARR.md#configuration) |
| `NAUTILUS_RADARR_READONLY` | drops Radarr's seven writes | [docs/tools/RADARR.md](tools/RADARR.md#configuration) |
| `SERVER_URL` + `SONARR_API_KEY` | the whole Sonarr family | [docs/tools/SONARR.md](tools/SONARR.md#configuration) |
| `NAUTILUS_SONARR_READONLY` | drops Sonarr's nine writes | [docs/tools/SONARR.md](tools/SONARR.md#configuration) |
| `SERVER_URL` + `PROWLARR_API_KEY` | the whole Prowlarr family | [docs/tools/PROWLARR.md](tools/PROWLARR.md#configuration) |
| `NAUTILUS_PROWLARR_READONLY` | drops Prowlarr's four writes | [docs/tools/PROWLARR.md](tools/PROWLARR.md#configuration) |
| `SERVER_URL` + `JELLYFIN_API_KEY` | the whole Jellyfin family | [docs/tools/JELLYFIN.md](tools/JELLYFIN.md#configuration) |
| `NAUTILUS_JELLYFIN_READONLY` | drops Jellyfin's seven writes | [docs/tools/JELLYFIN.md](tools/JELLYFIN.md#configuration) |
| `SERVER_URL` + `BAZARR_API_KEY` | the whole Bazarr family | [docs/tools/BAZARR.md](tools/BAZARR.md#configuration) |
| `NAUTILUS_BAZARR_READONLY` | drops Bazarr's five writes | [docs/tools/BAZARR.md](tools/BAZARR.md#configuration) |
| `NAUTILUS_HTTP_ADDR` + `NAUTILUS_HTTP_TOKEN` | **required** — the address it listens on and the token it demands | [below](#running-it) |
| `DOCKER_GID` | **required in Docker** — the host's `docker` group, which the server's container joins to reach the socket | [docs/INSTALL.md](INSTALL.md#the-env) |
| `TELEGRAM_BOT_TOKEN` + `TELEGRAM_ALLOWED_USER_IDS` | the Telegram bot, a separate binary — read by it, not by the server | [docs/modules/telegram.md](modules/telegram.md#2-the-env) |
| `TELEGRAM_ADMIN_USER_IDS` | the bot's users who may also remove, stop, restart and reconfigure — the rest read and add | [docs/modules/telegram.md](modules/telegram.md#admins) |
| `TELEGRAM_DAILY_BUDGET_USD` | the most a day of the bot's natural language may cost before it pauses until midnight | [docs/modules/telegram.md](modules/telegram.md#spending-cost-and-the-daily-budget) |
| `TELEGRAM_DIGEST_AT` | when the bot sends the admins its daily digest — `17:00` unless set, `off` for never | [docs/modules/telegram.md](modules/telegram.md#the-daily-digest) |

### A `.env` file

The simplest way to configure a server, and the only one that works the same on every machine
with a clone: a `.env` at the root of the repo, read on startup.

```sh
# .env — chmod 600, and already in .gitignore
SERVER_URL=http://localhost
RADARR_API_KEY=your-radarr-key
SONARR_API_KEY=your-sonarr-key
JELLYFIN_API_KEY=your-jellyfin-key
BAZARR_API_KEY=your-bazarr-key
PROWLARR_API_KEY=your-prowlarr-key
NAUTILUS_ALLOW_CONTAINER_NAMES=jellyfin,sonarr,radarr
```

One `SERVER_URL` serves every service because each fills in its own port — Radarr's 7878,
Sonarr's 8989, Prowlarr's 9696, Jellyfin's 8096, Bazarr's 6767 — so keep it a bare host. Written with a port
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

The server states what it registered on startup, on stderr — `journalctl -u nautilus-mcp` for a
systemd install:

```
[nautilus-mcp] radarr at http://localhost:7878: read and write
[nautilus-mcp] sonarr at http://localhost:8989: read and write
[nautilus-mcp] jellyfin at http://localhost:8096: read and write
[nautilus-mcp] bazarr at http://localhost:6767: read and write
[nautilus-mcp] prowlarr at http://localhost:9696: read and write
[nautilus-mcp] MCP server running on transport streamable http at http://100.101.102.103:3000/mcp
```

A service missing from those lines was not configured, and its tools were never registered. If
the assistant says it has no way to act on something, that is why: it is telling the truth.

The `.env` itself is read silently. Only a failure to read one is logged, and it is not fatal:

```
[nautilus-mcp] env file: /home/ubuntu/repos/Homelab-MCP/.env: unexpected character "\n" in variable name near "BROKEN LINE" — continuing without it
```

So a file that seems to have had no effect is either off the search path, or holds a variable
that was already set in the environment and lost to it. Neither case is reported — check the
service lines above against the `.env` you expected to be read.

## Approving actions

Thirty-four tools change something, and none of them act on the first call. They describe the
operation, wait for a decision, and bind the approval to that exact operation with a
fingerprint — so approving `ls /config` cannot execute `rm -rf /config`, approving one
film cannot add another, and approving a search of season 3 cannot search all nine seasons.

The server asks, not the client. The request reaches the user through MCP elicitation, showing
the exact operation, per command: the [Telegram bot](#on-telegram) turns it into a message with
**Aprovar** / **Recusar** buttons, and Claude Code (since 2.1.76) shows it in the terminal.
Setting a tool to always-allow in a client does not bypass it.

A client that cannot show that request is refused, and no setting changes that: a client's own
approval prompt is per-tool rather than per-command, and one set to always-allow stops asking.
The Docker allowlist holds in every case.

The mechanism, and what the fingerprint does and does not protect:
[docs/ARCHITECTURE.md §3](ARCHITECTURE.md#3-waiting-for-a-user-response).

## On Telegram

`bin/telegram-bot` is a second binary that puts the server in a Telegram chat: `/status`,
`/disk`, `/queue`, `/logs jellyfin`, `/search dune` and the rest call their tool directly, and
anything else — *"why hasn't Dune downloaded?"* — goes to Claude with every tool the server
registered. It is an MCP client like
any other, so approvals still come from the server, per command: they arrive as a message with
**Aprovar** / **Recusar** buttons. Long polling, so it needs no inbound port.

```sh
# added to the same .env
TELEGRAM_BOT_TOKEN=123456:ABC...      # from @BotFather
TELEGRAM_ALLOWED_USER_IDS=11111111    # numeric ids; everyone else is ignored
TELEGRAM_ADMIN_USER_IDS=11111111      # only these may remove, restart or reconfigure
ANTHROPIC_API_KEY=sk-ant-...          # optional: without it, commands only
```

Setup, systemd unit, and how the approval buttons are bound to the person who asked:
[docs/modules/telegram.md](modules/telegram.md).

## Running it

The server speaks MCP over **Streamable HTTP** and logs to **stderr** with the `[nautilus-mcp]`
prefix. It is a long-lived process: one of it serves every client that can reach the address,
so it is started by a service manager, never by a client.

Two variables are required, and it refuses to start without either:

```sh
# .env, on the machine being monitored
NAUTILUS_HTTP_ADDR=100.101.102.103:3000   # this host's tailscale address
NAUTILUS_HTTP_TOKEN=<openssl rand -hex 32>
```

```sh
./bin/server
# [nautilus-mcp] MCP server running on transport streamable http at http://100.101.102.103:3000/mcp
```

**Write the host part explicitly.** A bare `:3000` binds every interface the machine has,
including the one facing your LAN; the server says so at startup when you do.

Every request must carry `Authorization: Bearer <token>`, and anything else gets a 401 without
reaching a tool. The token is not optional and there is no flag that makes it optional. A
private bind address is not authentication: it is a bet that nothing hostile is on that
network, and a tailnet holds every device of every user your ACLs admit, plus whatever runs on
this host.

It shuts down cleanly on `Ctrl+C` (SIGINT) and on SIGTERM, so it behaves under systemd.

### In Docker

One image holds the server and the bot, and `compose.yml` runs it twice — once as each — from the
same `.env`, plus `DOCKER_GID`:

```sh
cp .env.example .env && chmod 600 .env   # or the .env you already have
make up                                  # docker compose up -d --build
make logs-mcp                            # make logs-bot for the bot
```

The server's container is given the host — its network and processes, its filesystems read-only,
the Docker socket and the system bus — so every tool answers about the machine, not the
container, and the `.env` reads as it does outside Docker. What each mount is for, the image on
Docker Hub and moving from the units below: [docs/INSTALL.md](INSTALL.md).

### Under systemd

Nothing execs this binary, so something has to keep it alive across logout and reboot. Run it
in a terminal and it dies with your SSH session:

```ini
# /etc/systemd/system/nautilus-mcp.service
[Unit]
Description=Nautilus MCP server
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
sudo systemctl daemon-reload && sudo systemctl enable --now nautilus-mcp
journalctl -u nautilus-mcp -f
```

No `EnvironmentFile` is needed: the binary finds the `.env` itself, next to the executable and
one directory above it. That also avoids the differences between systemd's parser and
[this one](#a-env-file). The `User=` must be in the `docker` group for the container tools.

Binding a tailscale address races with `tailscaled` on boot, and the bind fails with
`can't assign requested address` until the interface exists. `Restart=always` rides it out — a
failed start or two right after a reboot is expected. Binding `127.0.0.1` behind
[`tailscale serve`](#behind-tailscale) avoids the race entirely.

To update: `git pull && make build && sudo systemctl restart nautilus-mcp`. Clients need no
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
cannot do is the [thirty-four writes](#approving-actions): it declares its capabilities once, in
`initialize`, and a sessionless server keeps nothing from that — so the server cannot tell
whether it could be shown a confirmation, and it does not act without one. It refuses with
`this client ("") cannot show a confirmation coming from the server`, where the empty name is
itself the symptom.

Note also that there is no connect-time line in the log to consult: it belonged to the old
handshake, which a sessionless server never performs. The refusal above is the whole signal.

#### Behind Tailscale

Binding to the tailnet address, as above, is already enough: the port exists only inside your
tailnet, and nothing on your LAN or the internet can route to it. Restrict it further with a
Tailscale ACL — device authentication is not user authorisation, which is the other reason the
bearer token is required.

For a real TLS certificate and a name instead of an IP, bind loopback and put `tailscale serve`
in front:

```sh
# NAUTILUS_HTTP_ADDR=127.0.0.1:8080
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

The [Telegram bot](#on-telegram) is the client this runs with: it reads the same `.env` and finds
the server from `NAUTILUS_HTTP_ADDR` on its own. **Claude Code**, or any other client that
speaks HTTP natively, can be pointed at it too:

```sh
claude mcp add --transport http nautilus https://homelab.your-tailnet.ts.net/mcp \
  --header "Authorization: Bearer $NAUTILUS_HTTP_TOKEN"
```

## Documentation

```
docs/tools/            the specification — what each tool, prompt and resource takes and answers
docs/modules/          the design — how each integration is built, and why
docs/ARCHITECTURE.md   the shape both of those sit in
```

| | |
| --- | --- |
| [docs/tools/README.md](tools/README.md) | the tool index, the overview tool, and the conventions every tool shares |
| [docs/tools/PROMPTS.md](tools/PROMPTS.md) · [docs/tools/RESOURCES.md](tools/RESOURCES.md) | the two surfaces that are not tools |
| [docs/tools/SYSTEM.md](tools/SYSTEM.md) · [docs/tools/DOCKER.md](tools/DOCKER.md) · [docs/tools/RADARR.md](tools/RADARR.md) · [docs/tools/SONARR.md](tools/SONARR.md) · [docs/tools/JELLYFIN.md](tools/JELLYFIN.md) · [docs/tools/BAZARR.md](tools/BAZARR.md) · [docs/tools/PROWLARR.md](tools/PROWLARR.md) | per-family reference |
| [docs/ARCHITECTURE.md](ARCHITECTURE.md) | layering, tool registration, the confirmation round trip, the fingerprint |
| [docs/INSTALL.md](INSTALL.md) | running both in Docker: the image, what the server's container is given and why, health |
| [docs/modules/](modules/) | one page per integration: [system](modules/system.md), [docker](modules/docker.md), [radarr](modules/radarr.md), [sonarr](modules/sonarr.md), [jellyfin](modules/jellyfin.md), [bazarr](modules/bazarr.md), [prowlarr](modules/prowlarr.md), [telegram](modules/telegram.md) |

## Project layout

```
cmd/server/          entrypoint: .env loading, signal handling, serving
cmd/telegram-bot/    entrypoint for the Telegram bot
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
internal/bazarr/     bazarr, over its HTTP API
internal/prowlarr/   prowlarr, over its v1 HTTP API
internal/telegram/   the Telegram bot — an MCP client of the server, with Claude for plain words
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
against a whole series. The interactive search, manual import and editor tools
are covered on both: Radarr's and Sonarr's own release order kept, a guid of any length handed
back byte for byte behind an 8-character id, an import that sends back the quality and languages
the *arr parsed, an edit that sends only the named fields, and the 400 that testall answers with
read as the result it is.

The Jellyfin client is covered the same way, against a mock Jellyfin: the authorization header
it sends — which is the one thing it cannot borrow from the `*arr` clients, and a regression
there would look exactly like an expired key — the tick and timestamp conversions, the
four-way split of what a stream actually costs, a stale session told apart from a paused one,
and a health call that degrades into warnings rather than failing when the key turns out not
to be an administrator key. Its writes are covered against a second mock that records every
request: a user's preferences and policy sent back whole with only the named fields changed,
every spelling of Brazilian Portuguese landing on `por`, the pre-10.9 routes tried when the
current ones 404, and a stale stream stopped by ending its transcode rather than by a command
its absent app would never receive.

The Bazarr client has its own mock Bazarr: every spelling of Brazilian Portuguese landing on
Bazarr's invented `pb` rather than the European `pt`, a throttled provider told apart from an
unused `*arr`, a search that has not landed yet reported as pending rather than found or
failed, a manual-search token of kilobytes handed back byte for byte behind an 8-character
id, and the shift syntax Bazarr's own UI sends.

The Prowlarr client is covered against a mock Prowlarr: a backoff that has run out told
apart from a failing indexer, Prowlarr's tag rule for which app receives what, the 400 that
`testall` answers with read as the result it is, an edit that sends only the fields named, a
search that never returns the download link carrying Prowlarr's key, and an add whose
credentials are masked in the confirmation and sent byte for byte.

It also covers the surfaces added around those: the overview, including that a service being
down does not withhold the checks that worked, and — over the SDK's in-memory transport, so
what is asserted is what a client sees — that the prompts and resources appear only where the
tools they name were registered.

## Built with

- [modelcontextprotocol/go-sdk](https://github.com/modelcontextprotocol/go-sdk) — MCP protocol
- [shirou/gopsutil](https://github.com/shirou/gopsutil) — system metrics

## Contributions
Feel free to reach me out or open a PR!
