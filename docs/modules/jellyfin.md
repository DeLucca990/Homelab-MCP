# The Jellyfin module

`internal/jellyfin/`, over Jellyfin's HTTP API. Tool reference:
[tools/JELLYFIN.md](../../tools/JELLYFIN.md).

The first module that is read-only end to end, and the first whose value is a
*judgement* rather than a relay: almost everything here exists to turn a number
Jellyfin reports into the question someone actually has, which is "is this
machine in trouble".

---

## Configuration and reachability

Gated on two variables, the same shape as the two `*arr` families:

```go
if jellyfin.Configured() { ... }   // SERVER_URL and JELLYFIN_API_KEY both set
```

There is no `ReadOnly()` predicate. Both tools are reads, so it would gate
nothing — and a switch that turns nothing off is worse than an absent one,
because an operator who sets it believes something happened. It arrives with the
first write.

A configured-but-unreachable Jellyfin **still registers** its tools, for the
same reason as Radarr: a call that fails with a reason is more use than tools
that silently are not there.

### Normalising the URL

Identical to the `*arr` clients but for the port: a bare `http` host with no
port and no path gets `:8096`. This is what lets one `SERVER_URL` serve three
services, and it is why that variable has to stay a bare host — written with a
port it can only address one of them.

| Input | Result |
| --- | --- |
| `http://localhost` | `http://localhost:8096` |
| `localhost` | `http://localhost:8096` |
| `http://localhost:8920` | unchanged |
| `https://jellyfin.example.com` | unchanged |
| `http://nas/jellyfin` | unchanged |

### The key is not sent the way the *arr keys are

This is the one place the module cannot copy `internal/radarr/client.go`, and
the failure mode if it did is silent: Jellyfin reads **neither `X-Api-Key` nor
the deprecated `X-Emby-Token`**. A key sent either way is an unauthenticated
request that happens to carry a header. Jellyfin wants its own scheme, with
named values:

```
Authorization: MediaBrowser Client="Homelab MCP", Device="Homelab MCP",
               DeviceId="homelab-mcp", Version="1.0", Token="<key>"
```

Only `Token` is strictly required, but the client fields are sent because
Jellyfin records them against the session — a request from here is identifiable
in its dashboard rather than appearing as an anonymous token. The key never goes
in the query string, which Jellyfin also accepts: a URL ends up in error
messages, logs and proxy access logs; a header does not.

There is a test asserting the header shape, because nothing else would catch a
regression to the `*arr` form — a wrong header is a 401, and a 401 is what an
expired key looks like too.

### Admin rights are a second axis of "configured"

The `*arr` families have one: the key works or it does not. Jellyfin has two,
because most of what the health tool reads is administrator-only:

| Endpoint | Rights |
| --- | --- |
| `/Sessions` | any authenticated key |
| `/System/Info` | any authenticated key |
| `/System/Configuration/encoding` | any authenticated key |
| `/System/Info/Storage` | **administrator** |
| `/ScheduledTasks` | **administrator** |
| `/Plugins` | **administrator** |

A key issued from Dashboard → API Keys has those rights; one lifted from a user
session does not. That failure arrives per request as a 403, so `ErrForbidden`
is a distinct sentinel and `GetHealth` degrades on it — the sections that could
be read are returned, and each one that could not becomes a warning naming the
cause. A refusal that reads "this API key is not an administrator key" is a
fixable state; four missing tables are not.

---

## Sessions: what a stream costs is not what Jellyfin calls it

`homelab_overview` deliberately reports no CPU, because a media server
transcoding looks exactly like one in trouble. This module exists to close that
gap, and doing so needs one distinction Jellyfin's own vocabulary does not make.

`PlayMethod` has three values, and `Transcode` covers two operations whose costs
differ by two orders of magnitude:

- rewriting the **container** or the **audio** while every video frame passes
  through untouched — a remux, costing almost nothing
- re-encoding **every frame** — which is either a GPU engine or an entire CPU
  core, per stream

The field that separates them is `TranscodingInfo.IsVideoDirect`, and the field
that separates the expensive case in two is `HardwareAccelerationType`. So
`classifyWork` produces four values instead of three:

```
TranscodingInfo == nil          → direct  (or remux, if PlayMethod is DirectStream)
IsVideoDirect                   → remux
HardwareAccelerationType != ""  → hardware transcode
otherwise                       → software transcode
```

`"none"` is how Jellyfin spells no acceleration, in both the session and the
configuration. It is normalised to empty at the boundary, because a field
containing the string "none" reads as a backend called none to anything that
does not know better — including a model.

### Stale is not paused

A session reports playback progress every few seconds. One that has not done so
in five minutes while still claiming to play is a viewer who closed a lid or
lost a network — and the transcode behind them is still running. Jellyfin
reports that session as playing, so it is invisible in its own dashboard as
anything else.

Two guards keep this from crying wolf:

- **A paused session is excluded.** Someone is there and stopped it deliberately.
- **A missing timestamp is excluded.** `secondsSince` returns 0 for an absent or
  zero date, and the check is `> staleCheckInSeconds`, so a client that never
  reports progress at all is not the same as one that stopped.

### The window, and why idle sessions are counted but not listed

`/Sessions` is asked with `activeWithinSeconds=900`. Anything actually playing
checks in constantly, so no stream can fall outside that; what it excludes is
the long tail of devices that connected earlier and have done nothing since.

Sessions with no `NowPlayingItem` are dropped from the listing unless
`include_idle` is set — but they are counted first, so the summary line can say
how many were left out. A filter that hides its own effect is how a reader
concludes nobody is connected.

---

## Health: three states where everything else looks fine

Five requests, in parallel, on the pattern `radarr.GetHealth` established. All
failing is a connection problem and returns an error; any one failing is a
warning. What is worth saying about the three that are not obvious:

**The encoding configuration** is read from `/System/Configuration/encoding`
rather than from `/System/Info`, whose `EncoderLocation` — along with
`HasUpdateAvailable` and `SystemArchitecture` — is **deprecated** in the current
API and not worth building a warning on. The configuration endpoint also needs
no elevation, which the storage one does. This is the only place that answers
"will a transcode on this server melt it" *before* a transcode happens, so it is
warned on unconditionally: no acceleration configured is a warning even when
nothing is playing, and acceleration configured for decode with
`EnableHardwareEncoding` off gets its own, because the encode half is most of
the cost.

**Free space is per device, not per folder.** `FolderStorageDto.FreeSpace` is
the free space of the underlying storage device, so several of Jellyfin's paths
on one disk report the same number. Warnings are therefore deduplicated by
`DeviceId` — one full disk is one problem, not four — falling back to the path
when Jellyfin reports no device id.

The transcode temp folder is listed first because it is the one whose filling up
breaks playback while every other reading still looks healthy: it is usually not
the disk the media is on, so `system_disk_usage` shows terabytes free, and
Jellyfin buffers ahead of the viewer, so a stream that runs out of room there
stops playing rather than reporting a disk error.

**Two of the warnings are marked as *standing*.** `Health.StandingWarnings` is
the subset of `Warnings` describing how the server is configured rather than
what is wrong with it now — today, the two encoding findings. They are repeated
there rather than held out, so the health tool reports everything and neither
output channel is short a fact.

The distinction exists for `homelab_overview`, which asks a different question:
*is anything wrong right now*. A machine with no GPU has no hardware
acceleration every second of its life, and an overview that opens with that on
every glance is one nobody reads twice — so the jellyfin section drops them and
stays `ok`. The two surfaces do not contradict each other, because the health
tool was asked how the server is set up and answers in full. And the condition
stops being standing the moment something is paying for it, which reaches the
overview anyway as a software-transcode warning out of the session list.

Anything added to this category later has to pass the same test: **would it
still be true on a server nobody is using?** A failing library scan would not —
it is a fault that started at a particular moment. No hardware acceleration
would.

**The library scan is reported even when it succeeded.** Every other task is
filtered to running-or-failed, because fifteen idle rows are noise. The scan is
the exception because "when did Jellyfin last look at the disk" has an answer
here and nowhere else — and a failing scan is exactly the state where Radarr and
Sonarr report everything imported while the library looks untouched. It is
matched on `Key == "RefreshLibrary"` rather than on its name, which Jellyfin
translates into the server's configured language.

---

## Two decoding traps

**Ticks.** Every media timeline value — `RunTimeTicks`, `PositionTicks` — is in
.NET ticks of 100 nanoseconds, so a two-hour film is `72000000000`. Read as
seconds it would be 2,283 years. Every duration this package exposes is seconds,
converted once at the boundary in `ticksToSeconds`.

**Timestamps arrive in two shapes.** Most carry a zone; some — task execution
times among them — come from a .NET `DateTime` with an unspecified kind and
carry none at all. Those are UTC in practice, so `secondsSince` falls back to
parsing them *as UTC*: read as local time, a task that ran a minute ago lands
hours in the future and reports an age of zero, which renders as "never".

---

## In the overview

`homelab_overview` gets a `jellyfin` section wherever the module is configured,
composed from both collectors — six requests, in parallel with every other
check, so it costs the slowest of them rather than their sum.

```
!  jellyfin  v10.10.3, 5 playing (3 transcoding, 1 on the CPU), 1 failing task(s), restart pending
.  jellyfin  v10.10.3, nothing playing
```

Two things about that section are worth knowing:

**It is the closest the overview comes to answering "why is the CPU busy".** The
overview measures no CPU on purpose — it costs half a second and a pinned core
is not a fault. This line does not measure it either; it says how many streams
are being re-encoded on it, which is what the load usually turns out to be.

**The tool it names depends on what it found.** A section carries one
`Tool`, and health findings are not chased in a session list. It names
`jellyfin_active_sessions` by default, because that is the follow-up to the
headline — and `jellyfin_system_health` when the only warnings came from there.

---

## What this module deliberately does not do

Jellyfin's API is 294 endpoints. Most of them are the wrong shape for this
server, and two are the right shape for a tool that already exists:

- **`POST /System/Restart` and `/System/Shutdown`** — `docker_container_restart`
  already restarts Jellyfin, behind the allowlist and a confirmation. A second
  path to the same action with a weaker gate weakens the gate.
- **`DELETE /Items`** — deletes from disk, and `radarr_movie_remove` /
  `sonarr_series_remove` are the right way to do that: they keep the `*arr` in
  step, where deleting through Jellyfin leaves Radarr believing the file is
  still there.
- **User and policy writes** (`/Users/New`, `/Users/{id}/Policy`) — creating
  accounts and changing permissions has no defence proportional to it here.
- **Image, video, audio and subtitle streaming** — binary payloads, useless to a
  model.
- **LiveTv, SyncPlay, Playlists, UserData** — client behaviour, not the state of
  a machine.
