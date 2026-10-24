# Running it in Docker

One image, [`pedrodl/nautilus`](https://hub.docker.com/r/pedrodl/nautilus), for amd64 and arm64,
holds both binaries: `/server`, which it runs unless told otherwise, and `/telegram-bot`, which
the [bot](modules/telegram.md)'s service names as its `command`. It is Debian slim because the
server runs `systemctl`.

The server watches the machine it runs on, so its container is given that machine — its
network, its processes, its filesystems read-only, the Docker socket and the system bus. Each is
[explained below](#what-the-server-is-given). It is for a **Linux host running systemd**; on Docker
Desktop it would report the desktop's VM.

## The `.env`

One file for both containers, the same variables as [outside Docker](DETAILS.md#configuration),
plus one:

```sh
curl -fsSL -o .env https://raw.githubusercontent.com/DeLucca990/Homelab-MCP/main/.env.example
chmod 600 .env
getent group docker | cut -d: -f3   # → DOCKER_GID
```

| Variable | |
| --- | --- |
| `HOMELAB_MCP_HTTP_ADDR` | **required**, as outside Docker: the host's tailscale address and a port, e.g. `100.101.102.103:3000` |
| `HOMELAB_MCP_HTTP_TOKEN` | **required**: `openssl rand -hex 32` |
| `DOCKER_GID` | **required**: the id of the host's `docker` group, which the server's container joins to reach the socket |
| `SERVER_URL` and the API keys | the families to register — `http://localhost` reaches the ports the `*arr`s publish |
| `TELEGRAM_BOT_TOKEN`, `TELEGRAM_ALLOWED_USER_IDS` | the bot; without them, run only the server (`docker compose up -d nautilus-mcp`) |

The image sets `TZ=America/Sao_Paulo`, for the digest's hour and the times in answers; a `TZ` in
the `.env` replaces it.

## From a clone

```sh
git clone https://github.com/DeLucca990/Homelab-MCP.git && cd Homelab-MCP
cp .env.example .env && chmod 600 .env   # then fill it in
make up                                  # docker compose up -d --build
make logs-mcp                            # the server; make logs-bot for the bot
# [homelab-mcp] radarr at http://localhost:7878: read and write
# [homelab-mcp] MCP server running on transport streamable http at http://100.101.102.103:3000/mcp
```

To update: `git pull && make up`.

## From Docker Hub

No source needed: the `.env` above, and a `compose.yml` beside it — on its own, or these two
services added to a compose you already have:

```yaml
services:
  nautilus-mcp:
    image: pedrodl/nautilus:latest
    container_name: nautilus-mcp
    env_file: .env
    network_mode: host
    pid: host
    user: "1000:1000"
    group_add: ["${DOCKER_GID}"]
    environment:
      - HOST_ROOT=/host
      - HOST_ETC=/host/etc
    volumes:
      - /:/host:ro,rslave
      - /var/run/docker.sock:/var/run/docker.sock
      - /run/dbus:/run/dbus:ro
      - /run/systemd/system:/run/systemd/system:ro
    read_only: true
    tmpfs: [/tmp]
    restart: unless-stopped

  nautilus-bot:
    image: pedrodl/nautilus:latest
    container_name: nautilus-bot
    command: ["/telegram-bot"]
    healthcheck:
      test: ["CMD", "/telegram-bot", "healthcheck"]
      start_period: 1m
    env_file: .env
    network_mode: host
    volumes:
      - nautilus-bot-data:/data
    read_only: true
    tmpfs: [/tmp]
    restart: unless-stopped

volumes:
  nautilus-bot-data:
    name: nautilus-bot-data
```

```sh
docker compose up -d                          # start
docker compose pull && docker compose up -d   # update
```

| Tag | |
| --- | --- |
| `latest` | the newest release |
| `1` | every 1.x release, never a 2.0 that may break a `.env` that works |
| `1.2.3` | that release, for good |
| `edge` | the newest commit on `main`, not yet released |

## What the server is given

Nothing in the container is privileged: it adds no capabilities, its filesystem is read-only, and
it runs as an ordinary user. What it can see of the host is what these lines hand it:

| In `compose.yml` | For | Without it |
| --- | --- | --- |
| `network_mode: host` | the `.env` meaning what it does outside Docker: `SERVER_URL=http://localhost` reaches the `*arr`s, `HOMELAB_MCP_HTTP_ADDR` binds the tailscale address, and the hostname is the host's | `localhost` is the container; the `*arr`s need the host's address and the server a published port |
| `pid: host` | `/proc/1/mountinfo` being the host's mounts, and the process count the host's | the disks listed are the container's own |
| `/:/host:ro,rslave`, `HOST_ROOT=/host` | each of those mountpoints statted at `/host/<mountpoint>`; `rslave` lets a disk mounted later appear | every disk row is an error |
| `HOST_ETC=/host/etc` | the OS in `system_host_info` read from the host's `os-release` | it names the image's Debian |
| `/var/run/docker.sock`, `group_add: DOCKER_GID` | the Docker tools | they report that there is no Docker daemon |
| `/run/dbus`, `/run/systemd/system` | `system_service_status`: `systemctl` reaches the host's systemd over the system bus, as an unprivileged user — `list-units` and `show`, the two verbs it ever runs, are open to anyone there | services read as absent: *"in a container, mount the host's /run/systemd/system and /run/dbus"* |
| `user: "1000:1000"` | statting a mountpoint under a home directory, which only its owner can reach — as the systemd unit's `User=` did | that mountpoint's row says *permission denied* |

The Docker socket is the one that matters: whoever can talk to it is root on the host, in a
container or out of one. It is why the [action tools](tools/DOCKER.md#turning-them-on) are off
until `HOMELAB_MCP_ALLOW_CONTAINER_NAMES` names something, and why the bearer token is not
optional.

The bot is given none of this: the network, to reach the server at `HOMELAB_MCP_HTTP_ADDR`
whichever interface that is, and a volume.

## The bot's files

The digest's totals (`digest.json`) and the [spending ledger](modules/telegram.md#spending-cost-and-the-daily-budget)
(`spend.jsonl`) are kept in the `nautilus-bot-data` volume, at `/data` — the image sets
`STATE_DIRECTORY=/data`, which is where the bot looks for its directory under systemd too. A
volume rather than a folder because the image runs as 65532 and ships `/data` owned by it, so
nothing on the host needs `chown`ing.

## Health

Both have a healthcheck, and `docker ps` shows `healthy` within a couple of minutes. The image's
is the server's; the bot's service replaces it with its own, as it replaces the command — without
that, the bot's container would report on the server.

- **The server**: `server healthcheck` sends a `GET` to its own `/mcp` with the token. The
  endpoint answers `405` past the token check without running anything, so anything else — no
  answer, a `401`, a `5xx` — is unhealthy.
- **The bot**: every 30 seconds it asks Telegram who it is and, when Telegram answers, touches a
  file in `/tmp`; `telegram-bot healthcheck` is unhealthy once that file is two minutes old. The
  server being down does not count against it: saying so is part of the bot's job.

Docker reports an unhealthy container; it does not restart it. A crash is what `restart:
unless-stopped` handles — including the failed binds right after a reboot, before `tailscaled`
has brought its address up.

## Moving from systemd

The units and the containers cannot run side by side: two servers on one address fail with
`address already in use`, two bots on one token with `409 Conflict`. Stop the units, bring the
bot's files into the volume, and start:

```sh
sudo systemctl disable --now homelab-telegram-bot homelab-mcp
docker compose create
docker run --rm -v nautilus-bot-data:/data -v /var/lib/homelab-telegram-bot:/from:ro alpine \
  sh -c 'cp -a /from/. /data/ && chown -R 65532:65532 /data'
docker compose up -d
```

The `.env` carries over as it is, plus `DOCKER_GID`.

## When it does not work

| Symptom | Cause |
| --- | --- |
| `required variable DOCKER_GID is missing a value` | the `.env` has no `DOCKER_GID` |
| `cannot access the docker socket … permission denied` | `DOCKER_GID` is not the socket's group: `stat -c %g /var/run/docker.sock` |
| `listening on …: bind: address already in use` | the systemd unit, or something else, still holds that port |
| `listening on …: bind: can't assign requested address` | `HOMELAB_MCP_HTTP_ADDR` is not one of this host's addresses — or, right after a reboot, not yet: it retries |
| `path / is mounted on / but it is not a shared or slave mount` | a host whose `/` is not shared (systemd's default is shared): drop `,rslave` |
| a disk row saying *permission denied* | `user:` cannot reach that mountpoint: make it the owner's `id -u`:`id -g` |
| `telegram: … 409 Conflict` | another process polls the same bot token — the systemd unit, or another install |
