# Tools

The reference for every tool this server exposes, one page per family. For *why*
they are built the way they are, see [`docs/`](../docs/); this directory is the
specification.

| Family | Tools | Page |
| --- | --- | --- |
| Overview | 1, read-only, spans every family | [below](#homelab_overview) |
| System | 5, all read-only | [SYSTEM.md](SYSTEM.md) |
| Docker | 4 — 2 read-only, 2 opt-in actions | [DOCKER.md](DOCKER.md) |
| Radarr | 8 — 4 read-only, 4 writes | [RADARR.md](RADARR.md) |
| Sonarr | 10 — 5 read-only, 5 writes | [SONARR.md](SONARR.md) |
| Jellyfin | 2, both read-only | [JELLYFIN.md](JELLYFIN.md) |

**30 tools in total, but never all at once.** A default install registers
**8**: the overview, the five system tools and the two read-only Docker ones.
The rest appear only when the environment says so — the Docker actions need an
allowlist, and the Radarr, Sonarr and Jellyfin families each need a URL and an
API key. A tool that is not registered does not appear in `tools/list`, so it
cannot be called by mistake.

Tools are not the only thing a client sees. Two more surfaces are documented
next to this one:

| | |
| --- | --- |
| [PROMPTS.md](PROMPTS.md) | the procedures — which tool to call in which order, and the traps at each step |
| [RESOURCES.md](RESOURCES.md) | the reference data — the values the add tools accept, and what this install has registered |

---

## `homelab_overview`

No parameters. The answer to *"is anything wrong?"*, which is the question
people actually ask and the only one that spans every family.

```
3 of 7 checks need attention (118ms)

   AREA      SUMMARY
!  jellyfin  v10.10.3, 4 playing (2 transcoding, 1 on the CPU)
!  sonarr    v4.0.10, 14 in the queue (12 downloading), 2 stalled
!  disk      /srv/media at 94% (52G free)
.  memory    16Gi of 24Gi used, 7.7Gi available
.  services  41 units, 11 active, 0 failed
.  docker    9 containers, 9 running
.  radarr    v5.2.6, 1 in the queue (1 downloading)

warning: jellyfin: pedro on macbook is watching Dune (2021) as a software transcode
(VideoCodecNotSupported) — the video is being re-encoded on the CPU, which is roughly one
saturated core per stream and is what a high load average on a media server usually is

warning: sonarr: 2 downloads are stalled: nothing has arrived for The Expanse S04 in 6h

warning: disk: /srv/media is 94% full, 52G left

for the detail behind those lines: jellyfin_active_sessions, sonarr_queue_status, system_disk_usage
```

`!` needs attention, `.` is fine, `?` could not be checked and `-` does not
exist on this host. On a healthy server the whole answer is the first line:

```
nothing needs attention (6 checks in 61ms)
```

- **One round trip, not seven.** Every check runs at once, so the call costs the
  slowest of them rather than their sum.
- **It composes; it does not duplicate.** Every warning is the sentence that
  area's own tool would have produced, unchanged, and each line names the tool
  to call for the detail behind it. Two thresholds are its own, because no
  collector calls them warnings: a writable filesystem over 90%, and memory over
  90% or swap over 50%.
- **It leaves out standing configuration.** Jellyfin's health tool reports that
  no hardware acceleration is configured; the overview does not, because that is
  true every second of the server's life and a permanent `!` is one nobody reads.
  The moment it costs something, it arrives here anyway — as a software transcode
  in the session list.
- **Fullest is not filling up.** The disk line is about the fullest filesystem
  something can still write to. A read-only mount at 100% is an ISO or a
  squashfs image, which is what full looks like when it is working.
- **Absent is not broken.** A host with no Docker and a host whose Docker is
  failing are different answers, and a check that fails does not withhold the
  ones that worked.
- **No CPU — but the reason for it.** Measuring the cores costs half a second,
  and a pinned one is not a fault: a media server transcoding looks exactly like
  one in trouble. Where Jellyfin is configured, its line answers that directly by
  saying how many streams are being re-encoded on the CPU, which is what the load
  usually turns out to be. `system_cpu_cores` is one call away when the question
  is about the cores themselves.

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
