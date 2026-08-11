# Tools

The reference for every tool this server exposes, one page per family. For *why*
they are built the way they are, see [`docs/`](../docs/); this directory is the
specification.

| Family | Tools | Page |
| --- | --- | --- |
| System | 5, all read-only | [SYSTEM.md](SYSTEM.md) |
| Docker | 4 — 2 read-only, 2 opt-in actions | [DOCKER.md](DOCKER.md) |
| Radarr | 8 — 4 read-only, 4 writes | [RADARR.md](RADARR.md) |
| Sonarr | 10 — 5 read-only, 5 writes | [SONARR.md](SONARR.md) |

**27 tools in total, but never all at once.** A default install registers
**7**: the five system tools and the two read-only Docker ones. The rest appear
only when the environment says so — the Docker actions need an allowlist, the
Radarr and Sonarr families each need a URL and an API key. A tool that is not
registered does not appear in `tools/list`, so it cannot be called by mistake.

## What every tool has in common

**Two channels, one set of facts.** Every result carries both a compact text
rendering (`content`) and structured JSON (`structuredContent`). Clients differ
in which they show — some display the text, some drop it entirely when the JSON
is present — so neither channel is allowed to carry a fact the other lacks. In
particular, warnings are computed in the collector, never in the renderer.

**Sizes are plain byte counts.** Every `*_bytes` field in the JSON is an
integer. Human-readable units (`4.2Gi`, `916G`) are rendered once, in the text,
rather than duplicated per value.

**Durations are seconds.** Fields ending `_seconds`, `_seconds_ago` and
`time_left_seconds` are integers, and the text renders them compactly (`14m`,
`92d1h`).

**Truncation is always reported.** Command output, container logs and library
listings are capped. What was cut is stated in the result, because silent
truncation reads as a complete answer.

**Worst first.** Every listing that can contain a problem — services,
containers, the download queue, the movie library, the series library — is
sorted by severity rather than by name, so the reason someone is looking is at
the top.

**Writes ask first.** The eleven tools that change something never act on the
first call. They describe the operation, wait for a human decision, and bind the
approval to the exact operation with a fingerprint. See
[docs/ARCHITECTURE.md §3](../docs/ARCHITECTURE.md#3-waiting-for-a-user-response).

| Tool | Family | Guarded by |
| --- | --- | --- |
| `docker_container_exec` | Docker | allowlist + confirmation |
| `docker_container_restart` | Docker | allowlist + confirmation |
| `radarr_movie_add` | Radarr | confirmation |
| `radarr_movie_search` | Radarr | confirmation |
| `radarr_movie_remove` | Radarr | confirmation |
| `radarr_queue_remove` | Radarr | confirmation |
| `sonarr_series_add` | Sonarr | confirmation |
| `sonarr_season_monitor` | Sonarr | confirmation |
| `sonarr_series_search` | Sonarr | confirmation |
| `sonarr_series_remove` | Sonarr | confirmation |
| `sonarr_queue_remove` | Sonarr | confirmation |

**A confirmation states the size, not just the name.** Where an operation's
arguments hide how much it does — `sonarr_series_add` with `monitor: all` is a
whole back catalogue, one Sonarr queue row can be a fourteen-episode season pack
— the count is in the message the human reads, and in the fingerprint.
