# Sonarr tools

Ten tools over Sonarr's v3 HTTP API — five that read, five that write. None of
them exist until the server is told where the machine is and given an API key.
Design notes: [docs/modules/sonarr.md](../modules/sonarr.md).

| Tool | Input | What it answers | Writes |
| --- | --- | --- | --- |
| `sonarr_library_status` | `term`, `only_missing`, `only_monitored`, `limit` | what Sonarr is monitoring and how complete each series is | – |
| `sonarr_missing_episodes` | `series_id`, `limit` | which individual episodes are missing, when they aired, whether anything searched | – |
| `sonarr_queue_status` | — | the download queue with per-item progress, stalls and blocked imports | – |
| `sonarr_series_lookup` | `term`, `limit` | searches TheTVDB through Sonarr, returning candidates with their TVDB ids | – |
| `sonarr_system_health` | — | Sonarr's version, uptime, root folders and its own failing health checks | – |
| `sonarr_series_add` | `tvdb_id`, … | adds a series and starts searching for it | **yes** |
| `sonarr_season_monitor` | `series_id`, `season`, `monitored` | turns monitoring on or off for one season | **yes** |
| `sonarr_series_search` | `series_id`, `season`, `episode_ids` | searches the indexers now for a series already in the library | **yes** |
| `sonarr_series_remove` | `series_id`, … | removes a series from the library, by default deleting every episode file | **yes** |
| `sonarr_queue_remove` | `queue_id`, … | removes one download from the queue | **yes** |

## Downloading one season

The most common request this family gets, and it takes a different path
depending on whether the show is already there:

| Situation | What to do |
| --- | --- |
| Series in the library, season monitored | `sonarr_series_search` with `season` — one call |
| Series in the library, season **not** monitored | `sonarr_season_monitor` first, then the search |
| Series not in the library | `sonarr_series_add` with `monitor: none`, then `sonarr_season_monitor`, then the search |

The middle row is the one that surprises people: a search of an unmonitored
season completes successfully and grabs nothing. Sonarr dispatches
`SeasonSearch` with `monitoredOnly`, so it filters those episodes out before it
asks an indexer anything. Monitoring is the switch; the search is the trigger;
neither does the other's job.

The third row exists because `monitor` on the add only offers Sonarr's presets —
`all`, `firstSeason`, `lastSeason`, `latestSeason`, `pilot` and so on. None of
them names an arbitrary season, so season 3 of a new show means adding it
monitoring nothing and switching that one season on.

## What Sonarr is not

This family mirrors [Radarr's](RADARR.md) tool for tool, and then adds two that
have no counterpart there — `sonarr_missing_episodes` and
`sonarr_season_monitor`. Both exist for the same reason: two things about a
series do not carry over from a film.

**A series is never simply present or absent.** A movie is one file: Radarr has
it or owes it to you. A series is 62 files, of which 59 are there, and the
answer to "do you have The Expanse" is a fraction. So `monitored` says nothing on
its own, `MISSING` is not a state but a count, and everything in this family is
built around *episodes on disk out of episodes owed*.

**Every write has a scale.** `radarr_movie_search` searches for one film.
`sonarr_series_search` with no other argument searches for every monitored
episode of a nine-season show — several hundred grabs. That is why the search
tool takes a season and an episode list, why `sonarr_series_add` takes `monitor`,
why there is a tool for monitoring a single season, and why every confirmation
here states the size of what is about to happen rather than just its name.

## Configuration

| Variable | Meaning |
| --- | --- |
| `SERVER_URL` | the server the services run on: `http://localhost` when this binary runs on that same machine, otherwise `http://10.0.0.4` |
| `SONARR_API_KEY` | Sonarr → Settings → General → Security → API Key |
| `HOMELAB_MCP_SONARR_READONLY` | set to `1` to drop the five writes, leaving monitoring only |

Without both of the first two, **none of these tools are registered** — a server
with no Sonarr configured cannot be asked to reach one. Both belong on the
server side, in the environment of the process that runs the binary; the
[README](../../README.md#configuration) covers how to get them there.

**`SERVER_URL` is the same variable Radarr uses**, and that is the point: it says
*where the home server is*, and each integration resolves its own service against
it by filling in its own port. A bare `http://` host with no port and no path
gets `:8989` here and `:7878` there, so one line configures both. A URL that
names a port, carries a path (a reverse-proxy subfolder install) or uses `https`
is used exactly as written.

The corollary: **keep it a bare host.** A `SERVER_URL` that already names a port
can only ever address one service, and there is no per-service override — an
install that reaches the two through different addresses needs a reverse proxy
path, or a variable this server does not have yet.

A Sonarr that is configured but unreachable still registers its tools — a call
that fails with a reason is more use than tools that silently are not there.

## The two ids, and the third

Sonarr series carry two integers and they are not interchangeable:

- **`tvdb_id`** — TheTVDB's id. Comes from `sonarr_series_lookup`. This is what
  `sonarr_series_add` takes, because a title does not identify a show: *The
  Office* is at least four of them.
- **`series_id`** — Sonarr's own id, only meaningful once the show is in the
  library. Comes from `sonarr_library_status`. This is what
  `sonarr_series_search` and `sonarr_series_remove` take.

Nothing about a number says which one it is, so the tools that take a
`series_id` accept a TVDB id too and resolve it — and the confirmation says
which show it landed on.

The third is the **episode id**, which `sonarr_missing_episodes` returns and
`sonarr_series_search` accepts in `episode_ids`. It is not an episode *number*:
`S03E05` is a number, and the id is a database key that says nothing about which
season it belongs to. Episode ids that do not belong to the series being
searched are refused rather than passed on.

---

## `sonarr_library_status`

| Parameter | Type | Default | Meaning |
| --- | --- | --- | --- |
| `term` | string | — | case-insensitive substring of the title |
| `only_missing` | boolean | `false` | only series monitored and short of an aired episode |
| `only_monitored` | boolean | `false` | hide series Sonarr is not tracking |
| `limit` | integer | `25` | max `200` |

```
SERIES         YEAR  STATE       EPISODES  MISSING  MONITORED  SIZE  ID
The Expanse    2015  INCOMPLETE     20/23        3  yes        200G  42
Complete Show  2001  complete         6/6        -  yes         10G  43

2 series: 2 monitored, 1 continuing, 1 complete, 3 missing episodes
26 episodes on disk (210G)
```

`EPISODES` is on disk out of **owed**, and the second number is the one worth
understanding: it counts episodes that have aired *and* are monitored, plus
anything already downloaded. Everything beyond it — next season, an unmonitored
special — is not missing, it is simply not due.

**When `term` selects exactly one series, a per-season breakdown comes with it**,
because "which season is short" is the next question and the answer decides
whether to search a season or the whole show:

```
The Expanse, by season:
SEASON        EPISODES  MISSING  MONITORED  SIZE
0 (specials)       0/0        -  no         -
1                10/10        -  yes        100G
2                10/13        3  yes        100G
```

Season 0 is Sonarr's bucket for specials, and it is normal for it to be empty
and unmonitored. A whole-library listing never carries seasons: hundreds of
shows would be thousands of rows nobody asked for.

The counts always describe the whole library, whatever the filter is.

---

## `sonarr_missing_episodes`

| Parameter | Type | Default | Meaning |
| --- | --- | --- | --- |
| `series_id` | integer | — | one series; omit for the whole library |
| `limit` | integer | `25` | max `200` |

**The tool with no Radarr counterpart**, because a movie has nothing below it.
`sonarr_library_status` says a series is short three episodes; this says which
three.

```
SERIES       EPISODE  TITLE    AIRED    LAST SEARCH  EPISODE ID
The Expanse  S02E03   Delta-V  6d ago   4h ago       501
The Expanse  S02E04   Reload   13d ago  never        502

2 missing in The Expanse

sonarr_series_search starts a search for one of these — by series, by season,
or by the episode ids above

warning: 1 of the episodes listed have never been searched for
```

`LAST SEARCH` is the column that separates two very different failures. An
episode searched for nightly and never found is an indexer problem — check
`sonarr_system_health`. An episode **never searched for** is almost always
monitoring switched on after the fact, and one `sonarr_series_search` fixes it.

Without `series_id` this is Sonarr's own Wanted page, most recently aired first,
and the API does the filtering: monitored episodes of monitored series that have
aired and have no file. With `series_id` the same line is drawn over one show's
episode list, which is the only way to ask, since Sonarr's wanted endpoint takes
no series parameter.

---

## `sonarr_queue_status`

No parameters. The download queue, worst first.

```
ID  SERIES       EPISODE  STATUS       PROGRESS  LEFT  ETA   CLIENT       QUALITY
 9  Stuck Show   S02E03   stalled            5%  7.1G  none  qbittorrent  WEBDL-1080p
10  Healthy Show S01E01   downloading       80%  3.7G  14m   sabnzbd      Bluray-1080p

2 rows over 2 downloads: 1 downloading, 1 stalled, 0 waiting to import

warning: Stuck.Show.S02E03.1080p is stalled at 5% — it has been queued for 2d
and the download client reports no time remaining, so nothing is arriving
```

**Two states a progress bar hides**, the same two as Radarr's:

- **Stalled** — bytes still missing and the download client reports no time
  remaining. Rendered as `none`, never as `-`.
- **Import blocked / pending** — the download finished but Sonarr could not
  import it. The file is on disk and the episode is still missing from the
  library.

**And one that is Sonarr's alone: a download is not a row.** A season pack is one
file that appears in the queue once per episode it contains, all sharing a
download id. That is why the summary line counts rows *and* downloads, why a
stalled pack warns once rather than fourteen times, and why removing any one of
those rows removes all of them.

Items the download client holds that Sonarr cannot match to a series are
included too. **Queue ids are reassigned on every refresh** — read them from a
fresh call rather than from earlier in a conversation.

---

## `sonarr_series_lookup`

| Parameter | Type | Default | Meaning |
| --- | --- | --- | --- |
| `term` | string | required | a title, optionally with a year |
| `limit` | integer | `10` | max `25` |

Searches TheTVDB through Sonarr. Changes nothing, and it is the required first
step of adding anything.

```
  TVDB  TITLE        YEAR  STATUS      SEASONS  NETWORK  IN LIBRARY
280619  The Expanse  2015  ended             6  Syfy     no

pass the TVDB id of the right one to sonarr_series_add

280619 — Two hundred years in the future, a hardened detective and a rogue ship's…
     cover: https://artworks.thetvdb.com/banners/posters/280619-1.jpg
```

`SEASONS` is in the table rather than below it because it is the size of what an
add would download, and it is the fastest way to notice that the wrong *Office*
has 9 seasons where the one that was meant has 12.

---

## `sonarr_system_health`

No parameters. Sonarr's version, uptime, root folders and its own failing health
checks.

```
Sonarr at http://localhost:8989
version: 4.0.10.2544 (main)
host: ubuntu 24.04, in docker
up for: 1d5h
queue: 2 item(s)

ROOT FOLDER  FREE  REACHABLE  UNMAPPED
/tv          838G  yes        0

TYPE     CHECK               MESSAGE
warning  IndexerStatusCheck  Indexers unavailable due to failures: NZBgeek
```

This is the answer to *"no episode has arrived all week and everything looks
fine"*. Free space per root folder comes from Sonarr, which reports no total, so
there is no percentage here — `system_disk_usage` is the tool that knows that.

---

## `sonarr_series_add`

| Parameter | Type | Default | Meaning |
| --- | --- | --- | --- |
| `tvdb_id` | integer | required | from `sonarr_series_lookup` |
| `monitor` | string | `all` | how much of the show to monitor |
| `quality_profile` | string | `HD-1080p` | name or id |
| `root_folder` | string | the only one | path |
| `search_on_add` | boolean | `true` | start searching immediately |
| `season_folder` | boolean | `true` | one subfolder per season |
| `series_type` | string | `standard` | `standard`, `daily` or `anime` |

**`monitor` is the parameter with no Radarr equivalent, and it decides the size
of the operation.** With the default `all` on a long-running show, adding it
means downloading the entire back catalogue — hundreds of grabs and hundreds of
gigabytes, started by one tool call.

| Value | Monitors |
| --- | --- |
| `all` | every episode, including the whole back catalogue |
| `future` | only episodes that have not aired yet |
| `missing` | episodes that have aired and are not on disk |
| `existing` | only episodes already on disk |
| `recent` | the most recent episode and anything from now on |
| `pilot` | the first episode only |
| `firstSeason` | season 1 only |
| `lastSeason` / `latestSeason` | the most recent season only |
| `monitorSpecials` / `unmonitorSpecials` | specials as well / everything except specials |
| `none` | nothing — the series is added and then ignored, which is the starting point for "only season 3" with `sonarr_season_monitor` |

**Asks before adding**, showing the *resolved* plan, the size of the show, and
what the monitor value actually means:

```
Add this series to Sonarr?

    The Expanse (2015)   [tvdb 280619]
    Syfy, ended
    cover: https://artworks.thetvdb.com/banners/posters/280619-1.jpg
    size: 6 seasons, about 62 episodes
    quality profile: HD-1080p
    root folder:     /tv
    monitor:         all (every episode, including the whole back catalogue)
    search now:      yes
    season folders:  yes
    series type:     standard

Sonarr will start looking for releases straight away, and whatever it finds will
be downloaded onto that folder. That is the whole back catalogue — about 62
episodes.
```

Everything else works as in Radarr: quality defaults to `HD-1080p` matched by
name and case-insensitively, falling back to the only profile there is and
refusing rather than guessing when there is a real choice; `root_folder` may be
omitted only when Sonarr has exactly one; a series Sonarr already has is refused,
pointing at `sonarr_series_search` with the `series_id` to use.

`series_type` matters more than it looks: `anime` changes how Sonarr parses
release names (absolute episode numbering), and `daily` covers shows dated rather
than numbered. Getting it wrong means grabs that never match.

---

## `sonarr_season_monitor`

| Parameter | Type | Default | Meaning |
| --- | --- | --- | --- |
| `series_id` | integer | required | from `sonarr_library_status` |
| `season` | integer | required | the season number; `0` is Sonarr's specials |
| `monitored` | boolean | `true` | `false` stops Sonarr following that season |

Monitoring is the switch every other Sonarr tool reads, and this is the only way
to flip it from here. It cascades: Sonarr compares the season's flag against the
stored one and applies the change to **every episode of that season**, including
ones that have not aired yet — which is why it is done through the season rather
than through a list of episode ids.

```
Start monitoring a season?

    The Expanse (2015)   [series 42]
    season 2: 13 episodes, 9 aired, 0 on disk
    monitored: unmonitored → monitored

Sonarr will go and get 9 episodes that have aired and are not on disk. The flag
also covers the 4 episodes that have not aired yet, so Sonarr will pick them up
as they air.

This does not start a search; it decides what a search would look for.
```

**Those counts come from the episodes, not from Sonarr's season statistics**, and
the difference matters here more than anywhere else in the family. Sonarr's own
`episodeCount` is *(monitored AND aired) OR has a file*, so for a season that is
currently unmonitored it is structurally zero — reading "missing" off it would
tell you *0 episodes will be fetched* in exactly the case where the answer is
nine. The extra episode request the plan makes is what buys that number.

Asking for the state a season is already in writes nothing, asks nothing, and
says so.

Two things it deliberately does not do:

- **It does not start a search.** The result says so and names
  `sonarr_series_search` with the arguments to use. Monitoring decides what a
  search would look for; Sonarr's next scheduled pass is otherwise when anything
  happens.
- **It does not touch the series-level switch.** Sonarr refuses to grab for an
  episode of an unmonitored series whatever the season says, so if the series
  itself is off, both the confirmation and the result say that this alone changes
  nothing and that the series switch is in Sonarr's own UI.

Unmonitoring is the quiet direction: nothing on disk is deleted, and anything
already downloading for that season stays in the queue and still imports.

---

## `sonarr_series_search`

| Parameter | Type | Meaning |
| --- | --- | --- |
| `series_id` | integer | required, from `sonarr_library_status` |
| `season` | integer | one season only |
| `episode_ids` | integer[] | specific episodes, from `sonarr_missing_episodes`; takes precedence over `season` |

Sonarr's own Search button: asks the indexers now for a series already in the
library, and grabs what it finds. **Asks before searching.**

This is what to use when episodes are monitored and missing — including right
after a download was removed from the queue. `sonarr_series_add` would be
refused with *"This series has already been added"*, correctly, because a
library entry is not what was being asked for.

**Three scales, three Sonarr commands**, and the scope is resolved and named
before anything is approved:

| Arguments | Command | Searches |
| --- | --- | --- |
| `series_id` alone | `SeriesSearch` | every monitored episode of the show |
| `+ season` | `SeasonSearch` | that season, which may arrive as one pack |
| `+ episode_ids` | `EpisodeSearch` | exactly those episodes |

```
Search for releases now?

    The Expanse (2015)   [series 42]
    searching:  season 2 of The Expanse
    on disk:    20 of 23 episodes
    missing in scope: 3
    monitored:  yes

Anything the indexers return will be grabbed and downloaded.
```

A whole-series search with more than twenty episodes missing says so and
suggests a season instead. A scope with nothing missing says the search is for
**upgrades**, and that a better release would replace what is on disk.

The result reports a *queued command*, not found episodes:

```
sonarr is searching for season 2 of The Expanse
SeasonSearch command 1234: queued
3 episode(s) in scope were missing when it started

the search runs inside sonarr; sonarr_queue_status shows whether it grabbed
anything, and an empty queue a minute from now means the indexers returned nothing
```

Warns when the search will probably do nothing: the series is unmonitored, the
named season is unmonitored, or a named episode has not aired.

---

## `sonarr_series_remove`

| Parameter | Type | Default | Meaning |
| --- | --- | --- | --- |
| `series_id` | integer | required | from `sonarr_library_status` |
| `delete_files` | boolean | `true` | erase every downloaded episode from disk |
| `add_import_exclusion` | boolean | `false` | stop import lists adding it back |

**The one operation here that destroys something no amount of asking Sonarr
again brings back**, and the largest in the server: not one file but every
episode of every season. `delete_files` defaults to `true` — removing a series
from a homelab library normally means reclaiming the disk — so the confirmation
states the file count and the size in capitals:

```
Remove this series from the Sonarr library?

    The Expanse (2015)   [series 42]
    state: INCOMPLETE, 20 of 23 episodes on disk
    folder: /tv/The Expanse

ALL 20 EPISODE FILES WILL BE DELETED — 200G freed.
This cannot be undone, and getting the show back means downloading it again.
```

Pass `delete_files: false` to remove the library entry and keep the files. The
result then warns that Sonarr no longer tracks them and that adding the series
back would import them again.

---

## `sonarr_queue_remove`

| Parameter | Type | Default | Meaning |
| --- | --- | --- | --- |
| `queue_id` | integer | required | from a **fresh** `sonarr_queue_status` |
| `remove_from_client` | boolean | `true` | also delete the download from the download client |
| `blocklist` | boolean | `false` | block this release so Sonarr never grabs it again |
| `skip_redownload` | boolean | `false` | stop Sonarr searching for a replacement |

**Asks before removing**, naming the episode, how far the download had got, and
— the part that has no Radarr equivalent — how many other episodes ride on the
same file:

```
Remove this download from the Sonarr queue?

    The Expanse S02E03 — Delta-V
    release: The.Expanse.S02.COMPLETE.1080p
    progress: 5% (7.1G still to come)
    status: stalled

THIS DOWNLOAD HOLDS 13 EPISODES — one file, 13 queue rows, and removing this row
removes all of them: S02E01, S02E02, S02E03, S02E04, …

The partial download will be deleted from qbittorrent. Anything already
downloaded is lost.
```

That count is part of the approval fingerprint, so a row that was one episode
when it was shown and a season pack by the time it was approved is refused
rather than removed.

**A removal is not a blocklist.** With `blocklist` false Sonarr starts nothing to
replace what you took out, so the episodes go back to monitored-and-missing and
stay there until a scheduled search. The result says so, with the `series_id` to
pass to `sonarr_series_search`.
