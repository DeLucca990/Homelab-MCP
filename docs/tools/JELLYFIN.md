# Jellyfin tools

Two tools over Jellyfin's HTTP API, both read-only. Neither exists until the
server is told where the machine is and given an API key. Design notes:
[docs/modules/jellyfin.md](../modules/jellyfin.md).

| Tool | Input | What it answers | Writes |
| --- | --- | --- | --- |
| `jellyfin_active_sessions` | `include_idle` | who is watching what, and what each stream costs the machine | – |
| `jellyfin_system_health` | — | Jellyfin's version, its encoding settings, free space per folder, its scheduled tasks and any plugin that is not running | – |

## Configuration

| Variable | Meaning |
| --- | --- |
| `SERVER_URL` | the server the services run on: `http://localhost` when this binary runs on that same machine, otherwise `http://10.0.0.4` or `https://media.example.com/jellyfin` |
| `JELLYFIN_API_KEY` | Jellyfin → Dashboard → API Keys → **+** |

Without both, **neither tool is registered**. `SERVER_URL` is the same variable
Radarr and Sonarr read, and each fills in its own port — Jellyfin's is **8096**,
so `http://localhost` and `http://localhost:8096` are the same thing here. Write
it with a port and it can only address one of the three services.

There is no `HOMELAB_MCP_JELLYFIN_READONLY`. Both tools are reads, so there
would be nothing for it to drop; it appears when a write does.

**Use a key from the dashboard, not one lifted from a browser session.** Three
of the five requests behind `jellyfin_system_health` are administrator-only, and
a key without those rights loses those sections. It does not fail the call — it
answers with what it could read and a warning naming each section it could not,
which is how you find out that is what happened.

---

## `jellyfin_active_sessions`

| Parameter | Type | Default | Meaning |
| --- | --- | --- | --- |
| `include_idle` | boolean | `false` | also list sessions connected and playing nothing |

```
USER   WATCHING                          WORK              AT  BITRATE  CLIENT        DEVICE
pedro  Dune (2021)                       transcode (cpu)  34%   20.0Mb  Jellyfin Web  macbook
ana    The Expanse S03E05 — Delta-V      transcode (qsv)  12%   12.0Mb  Android TV    shield
sam    Arrival (2016)                    remux            88%    8.0Mb  Jellyfin Web  desktop
lu     Blade Runner 2049 (2017)          direct            5%        -  Infuse        ipad

4 playing: 1 direct, 1 remux, 1 hardware transcode, 1 software transcode (2 idle sessions not listed)
Dune (2021): VideoCodecNotSupported, SubtitleCodecNotSupported
The Expanse S03E05 — Delta-V: VideoCodecNotSupported

warning: pedro on macbook is watching Dune (2021) as a software transcode
(VideoCodecNotSupported, SubtitleCodecNotSupported) — the video is being re-encoded on the
CPU, which is roughly one saturated core per stream and is what a high load average on a
media server usually is

warning: pedro on macbook is re-encoding Dune (2021) only to burn subtitles into the
picture — converting that subtitle track to a text format, or turning it off, would make
this stream cost nothing

warning: 2 streams are being re-encoded at once (1 on the CPU, 1 on hardware) — concurrent
transcodes are the load, and each new viewer adds another
```

**This is the reading `homelab_overview` refuses to guess at.** The overview
leaves CPU out on purpose, because a media server transcoding and a media server
in trouble look identical from a load average. This is what tells them apart.

**"Transcoding" is not one thing, and Jellyfin's own label does not separate
them.** `PlayMethod: Transcode` covers a container rewrite that costs nothing
and a 4K re-encode that saturates a core. The `work` field is the distinction:

| `work` | What is happening | Roughly what it costs |
| --- | --- | --- |
| `direct` | the file is sent as it is | disk and network |
| `remux` | the container or the audio is rewritten; **every video frame passes through untouched** | almost nothing |
| `hardware transcode` | video re-encoded on the GPU | a GPU engine, little CPU |
| `software transcode` | video re-encoded on the CPU | **about one saturated core, per stream** |

The field behind that split is Jellyfin's `IsVideoDirect`, not `PlayMethod`.

**The reasons are the actionable half.** A software transcode is a fact;
`SubtitleCodecNotSupported` is a fix. That reason means an image-based subtitle
track is being burned into the picture, which forces a full re-encode of a file
that would otherwise have been sent untouched — so it gets a warning of its own.
`VideoCodecNotSupported` on an HEVC file played by a browser is the other common
one, and that is a client limitation rather than a library problem.

**A session can be playing to nobody.** `STALE` in the work column means the
client has not reported playback progress in over five minutes while still
claiming to play. Clients check in every few seconds, so that is a viewer who
closed a lid or lost a network — and the transcode behind them is still running,
still holding a core. Those sort to the top. A *paused* session is not stale:
someone is there and stopped it deliberately.

**Idle sessions are hidden by default** and still counted, so the summary line
says how many there are. They cost the server nothing; an open browser tab is
not what the question was about.

Sessions are asked for inside a **15-minute activity window**. Anything actually
playing checks in constantly, so nothing being watched can fall outside it —
what it excludes is the long tail of devices that connected earlier and have
been doing nothing since.

---

## `jellyfin_system_health`

No parameters.

```
media at http://localhost:8096
version: 10.10.3
host: Debian GNU/Linux 12
transcoding: qsv, hardware encoding on
hardware decodes: h264, hevc

FOLDER            PATH                FREE  USED
transcode temp    /cache/transcodes   3.1G   10G
metadata          /var/lib/jellyfin    88G   12G
cache             /cache              3.1G   10G
library: Movies   /media/movies       916G  4.1T
library: TV       /media/tv           916G  4.1T
free space is the device's, so folders on one disk repeat the number

TASK                 STATE        LAST RUN  RESULT
Scan Media Library   idle          2d4h ago  Failed: Access to the path '/media/movies' is denied
Extract Chapter Ima  running 42%          -  Completed
14 scheduled tasks in total; the rest last completed cleanly

all 3 installed plugins are active

warning: transcode temp (/cache/transcodes) has under 5.0G free — Jellyfin buffers a
transcode ahead of the viewer, and a stream that runs out of room there stops playing
rather than reporting a disk error

warning: the Scan Media Library task failed 2d4h ago: Access to the path '/media/movies'
is denied
```

Three things here are invisible from outside the application, and each is a
state where everything else looks fine:

**The encoding settings.** A server with no hardware acceleration configured is
indistinguishable from a healthy one right up until the first person plays
something their client cannot take directly — and then it is indistinguishable
from a server under attack. This is the one line that says so before it happens.
Acceleration set for *decoding* with hardware encoding off is reported too: the
encode half still runs on the CPU, which is most of the cost.

**The transcode temp directory.** It is usually not the disk the media is on —
often `/cache`, a tmpfs, or a small SSD — so `system_disk_usage` can show
terabytes free while this is the thing about to fill. Jellyfin buffers ahead of
the viewer, so a 4K stream can want several gigabytes of it, and the failure
when it runs out is a playback that stops rather than a disk error anyone sees.
Under 5G free is a warning.

**The library scan.** A scan that has been failing means files Radarr and Sonarr
have already imported are on disk and absent from Jellyfin — the library looks
untouched while the `*arr` queue says everything succeeded. The scan task is
reported even when it succeeded, because *when Jellyfin last looked at the disk*
is a question with an answer here and nowhere else. Never having run at all, and
not having run in over a week, each get a warning.

**Free space is the device's, not the folder's.** Jellyfin reports the
underlying device, so several folders on one disk repeat the same number. The
warnings are deduplicated by device — one full disk is one problem, not four.

**Only the tasks and plugins worth reading are listed:** whatever is running,
whatever last failed, the library scan, and any plugin not in the `Active`
state. The totals for both are given, so a short list is not mistaken for a
short install.

**The encoding warnings are marked `standing_warnings`** as well as appearing in
`warnings`, because they describe how the server is configured rather than what
is wrong with it now. That is why `homelab_overview` can report `jellyfin` as
fine on a server this tool warns about: it was asked a different question, and a
machine with no GPU would otherwise need attention every second of its life. The
moment the configuration actually costs something, it shows up in the overview
as a software transcode from `jellyfin_active_sessions`.
