# Jellyfin tools

Twelve tools over Jellyfin's HTTP API — five that read, seven that write. None
of them exist until the server is told where the machine is and given an API
key. Design notes:
[docs/modules/jellyfin.md](../modules/jellyfin.md).

| Tool | Input | What it answers | Writes |
| --- | --- | --- | --- |
| `jellyfin_active_sessions` | `include_idle` | who is watching what, and what each stream costs the machine | – |
| `jellyfin_system_health` | — | Jellyfin's version, its encoding settings, free space per folder, its scheduled tasks and any plugin that is not running | – |
| `jellyfin_users` | — | every user's audio and subtitle preferences, libraries, bitrate cap and transcoding rights | – |
| `jellyfin_find_item` | `term`, `user`, `limit` | whether a film, series or episode is in the library, with its id and path | – |
| `jellyfin_activity_log` | `hours`, `limit`, `only_problems` | Jellyfin's own record: sign-ins, failed sign-ins, playback, failed tasks | – |
| `jellyfin_library_scan` | `library` / `item_id` | makes Jellyfin look for new and changed files | **yes** |
| `jellyfin_session_stop` | `session_id`, `message` | stops a stream — and its transcode, for a viewer who is gone | **yes** |
| `jellyfin_session_message` | `session_id`, `text` | shows a message on someone's screen | **yes** |
| `jellyfin_user_preferences_set` | `user`, `audio_language`, `subtitle_language`, `subtitle_mode`, … | which tracks the player picks for a user | **yes** |
| `jellyfin_user_access_set` | `user`, `libraries`, `remote_bitrate_mbps`, `video_transcoding`, … | what a user can see and how they may stream | **yes** |
| `jellyfin_transcoding_set` | `acceleration`, `device`, `decode_codecs`, … | hardware acceleration for every transcode | **yes** |
| `jellyfin_mark_played` | `user`, `item_id`, `played` | marks watched or unwatched | **yes** |

## Configuration

| Variable | Meaning |
| --- | --- |
| `SERVER_URL` | the server the services run on: `http://localhost` when this binary runs on that same machine, otherwise `http://10.0.0.4` or `https://media.example.com/jellyfin` |
| `JELLYFIN_API_KEY` | Jellyfin → Dashboard → API Keys → **+** |
| `HOMELAB_MCP_JELLYFIN_READONLY` | set to `1` to drop the seven writes, leaving the five reads |

Without the first two, **none of these tools are registered**. `SERVER_URL` is the same variable
Radarr and Sonarr read, and each fills in its own port — Jellyfin's is **8096**,
so `http://localhost` and `http://localhost:8096` are the same thing here. Write
it with a port and it can only address one of the three services.

**Use a key from the dashboard, not one lifted from a browser session.** Three
of the five requests behind `jellyfin_system_health` are administrator-only, and
a key without those rights loses those sections — as do the users, the activity
log and every write but the session ones. It does not fail the call — it
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

---

## `jellyfin_users`

No parameters. Administrator key.

```
USER   ROLE   AUDIO  SUBS  SUB MODE  LIBRARIES  REMOTE   TRANSCODE  SEEN
Ana    user   -      por   Smart     Movies     8 Mbps   no         2h3m
Pedro  admin  eng    -     Default   all        no cap   yes        4m
```

The first call for two questions that sound like faults and are settings:

- **"It always starts with the wrong subtitles"** — the subtitle language and
  mode. `Default` follows the file's own flags; `Smart` loads the preferred
  language only when the audio is in another one.
- **"It fails for her and not for me"** — very often video transcoding switched
  off for that user, so a file her TV cannot play directly fails instead of
  being converted. That is a warning.

Languages are Jellyfin's three-letter codes.

---

## `jellyfin_find_item`

| Parameter | Type | Meaning |
| --- | --- | --- |
| `term` | string | required, part of the title |
| `user` | string | also show whether this user has watched each item |
| `limit` | integer | default `20`, max `100` |

The Jellyfin half of *"Radarr imported it and I cannot find it"*. Absent here
while the file is on disk means the library has not caught up —
`jellyfin_library_scan` fixes that. Returns each item's id, which the scan and
`jellyfin_mark_played` take, its path and when it was added.

---

## `jellyfin_activity_log`

| Parameter | Type | Default | Meaning |
| --- | --- | --- | --- |
| `hours` | integer | `24` | max `720` |
| `limit` | integer | `50` | max `200` |
| `only_problems` | boolean | `false` | warnings, errors and failed sign-ins only |

Jellyfin's own record, newest first: who signed in and from where, who failed
to, what was played, which task failed, which plugin changed. Five or more
failed sign-ins in the window is a warning — on a server reachable from the
internet, that is someone guessing passwords. Administrator key.

---

## `jellyfin_library_scan`

| Parameter | Type | Meaning |
| --- | --- | --- |
| `library` | string | one library, by name (`homelab://jellyfin/libraries`) |
| `item_id` | string | one item, from `jellyfin_find_item` |

Neither: every library. **Asks first**, naming the scope and its folders. The
same request Jellyfin's own *Scan library* button sends — new and changed files
are picked up, metadata and images someone edited are kept. A full scan warns
what it costs; the narrowest scope that answers the question is the one to use.
It returns once queued.

---

## `jellyfin_session_stop`

| Parameter | Type | Meaning |
| --- | --- | --- |
| `session_id` | string | required, from a fresh `jellyfin_active_sessions` |
| `message` | string | shown on their screen first |

**Asks first**, naming who is watching what and how it will be stopped. Two
different operations, sent as they apply:

- **Stop the app** — a command to the client. Only arrives if the app is still
  connected and accepts remote control.
- **End the transcode** — kills the ffmpeg process on the server, by device and
  play session. This is what frees the CPU for the stale session
  `jellyfin_active_sessions` flags: its viewer closed the lid, the app will never
  receive a stop, and the transcode kept running.

A direct play on an app without remote control is refused: nothing on the
server is doing work for it, and it ends when the client lets go.

---

## `jellyfin_session_message`

| Parameter | Type | Meaning |
| --- | --- | --- |
| `session_id` | string | required |
| `text` | string | required; shown for ten seconds |

**Asks first**, with the exact text. Only apps that accept remote control can
show one; the others are refused rather than silently ignored.

---

## `jellyfin_user_preferences_set`

| Parameter | Type | Meaning |
| --- | --- | --- |
| `user` | string | required, by name |
| `audio_language` | string | `por`, `eng`, `pt-BR`, a name — `none` clears it |
| `subtitle_language` | string | same forms |
| `subtitle_mode` | string | `Default`, `Always`, `Smart`, `OnlyForced`, `None` |
| `remember_subtitle_selections` | boolean | whether a hand-picked track wins next time |

The other half of *"I want Portuguese subtitles"*: Bazarr puts the file on disk,
this makes the player pick it. **Asks first**, with each value before and after
and what the new mode does.

**Portuguese is `por`, whichever Portuguese.** Jellyfin matches on the language
tag in the file, and Brazil and Portugal share it — so `pt-BR`, `pb` and
`Portuguese` all land on `por`, and a `pt-BR` subtitle from Bazarr matches it.
`Always` or `Smart` with no subtitle language set is warned about: they pick by
language, and would pick nothing.

Only the fields passed change; the rest of the user's preferences go back
exactly as they were. Applies from the next playback; some third-party apps
apply their own track rules instead.

---

## `jellyfin_user_access_set`

| Parameter | Type | Meaning |
| --- | --- | --- |
| `user` | string | required, by name |
| `libraries` | string[] | replaces the list; `["all"]` for every library |
| `remote_bitrate_mbps` | number | cap away from home; `0` removes it |
| `video_transcoding` | boolean | whether video may be re-encoded for them |
| `remuxing` | boolean | |
| `disabled` | boolean | signs them out everywhere and blocks sign-in |

**Asks first**, with each value before and after. Covers what a household
changes, and deliberately not administrator rights or passwords. Administrators
cannot be disabled — Jellyfin refuses, and so does this, before asking. A cap
under 8 Mbps is warned about: most 1080p files are then transcoded down for
that user away from home, an encode per stream.

---

## `jellyfin_transcoding_set`

| Parameter | Type | Meaning |
| --- | --- | --- |
| `acceleration` | string | `none`, `qsv`, `vaapi`, `nvenc`, `amf`, `videotoolbox`, `rkmpp`, `v4l2m2m` |
| `device` | string | for `vaapi`/`qsv`, e.g. `/dev/dri/renderD128` |
| `decode_codecs` | string[] | replaces the list: `h264`, `hevc`, `mpeg2video`, `mpeg4`, `vc1`, `vp8`, `vp9`, `av1` |
| `hardware_encoding` | boolean | encode on the GPU too |
| `tonemapping` | boolean | HDR to SDR |

The setting with the widest reach: every transcode goes through it, and a
backend the machine — or the container — cannot use makes every one of them
fail. **Asks first**, and both the confirmation and the result carry the values
that undo it. In Docker, the GPU has to be passed through (`/dev/dri` for
VA-API and QSV, the NVIDIA runtime for NVENC), which the confirmation says.
HEVC missing from the decode list is warned about: 4K still decodes on the CPU.

To prove it worked: play something that needs a transcode and check
`jellyfin_active_sessions` says `hardware transcode`.

---

## `jellyfin_mark_played`

| Parameter | Type | Default | Meaning |
| --- | --- | --- | --- |
| `user` | string | — | required |
| `item_id` | string | — | required, from `jellyfin_find_item` |
| `played` | boolean | `true` | `false` for unwatched |

**Asks first.** A series or season marks every episode in it; unwatched also
clears the resume position.
