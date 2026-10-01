# Radarr tools

Fifteen tools over Radarr's v3 HTTP API — eight that read, seven that write. None of
them exist until the server is told where the machine is and given an API key.
Design notes: [docs/modules/radarr.md](../modules/radarr.md).

| Tool | Input | What it answers | Writes |
| --- | --- | --- | --- |
| `radarr_library_status` | `term`, `only_missing`, `only_monitored`, `limit` | what Radarr is monitoring and what it actually has | – |
| `radarr_queue_status` | — | the download queue with per-item progress, stalls and blocked imports | – |
| `radarr_movie_lookup` | `term`, `limit` | searches TMDB through Radarr, returning candidates with their TMDB ids | – |
| `radarr_system_health` | `test_download_clients` | Radarr's version, uptime, root folders, its own failing health checks and, on request, a download client test | – |
| `radarr_releases` | `movie_id`, `limit` | an interactive search: every release found, in Radarr's order, with why each was rejected | – |
| `radarr_import_candidates` | `queue_id` / `folder` | what Radarr makes of the files in a finished download, and its objection to each | – |
| `radarr_history` | `movie_id`, `limit` | what was grabbed, imported, failed or deleted — and the blocklist | – |
| `radarr_calendar` | `days`, `past_days` | cinema, digital and physical release dates | – |
| `radarr_movie_add` | `tmdb_id`, … | adds a movie and starts searching for it | **yes** |
| `radarr_movie_search` | `movie_id` | searches the indexers now for a movie already in the library | **yes** |
| `radarr_movie_remove` | `movie_id`, … | removes a movie from the library, by default deleting its files | **yes** |
| `radarr_queue_remove` | `queue_id`, … | removes one download from the queue | **yes** |
| `radarr_release_grab` | `id` | grabs a release picked from `radarr_releases`, rejected or not | **yes** |
| `radarr_import` | `ids`, `movie_id` | imports files picked from `radarr_import_candidates` | **yes** |
| `radarr_movie_edit` | `movie_id`, `monitored`, `quality_profile`, `minimum_availability`, tags | changes a movie's settings | **yes** |

## Configuration

| Variable | Meaning |
| --- | --- |
| `SERVER_URL` | the server the services run on: `http://localhost` when this binary runs on that same machine, otherwise `http://10.0.0.4` or `https://media.example.com/radarr` |
| `RADARR_API_KEY` | Radarr → Settings → General → Security → API Key |
| `HOMELAB_MCP_RADARR_READONLY` | set to `1` to drop the seven writes, leaving monitoring only |

Without the first two, **none of these tools are registered** — a server with no
Radarr configured cannot be asked to reach one. Both belong on the server side,
in the environment of the process that runs the binary; the
[README](../../README.md#configuration) covers how to get them there.

`SERVER_URL` is not named for Radarr on purpose: it says *where the home server
is*, and each integration resolves its own service against it — this one by
filling in Radarr's port. A bare `http://` host with no port and no path gets
`:7878`, so `http://localhost` and `http://localhost:7878` are the same thing
here. A URL that names a port, carries a path (a reverse-proxy subfolder
install) or uses `https` is used exactly as written.

A Radarr that is configured but unreachable still registers its tools — a call
that fails with a reason is more use than tools that silently are not there.

## The two ids

Radarr movies carry two integers and they are not interchangeable:

- **`tmdb_id`** — TMDB's id. Comes from `radarr_movie_lookup`. This is what
  `radarr_movie_add` takes, because a title does not identify a film.
- **`movie_id`** — Radarr's own id, only meaningful once the film is in the
  library. Comes from `radarr_library_status`. This is what `radarr_movie_search`
  and `radarr_movie_remove` take.

Nothing about a number says which one it is, so the tools that take a `movie_id`
accept a TMDB id too and resolve it — and the confirmation says which film it
landed on.

---

## `radarr_library_status`

| Parameter | Type | Default | Meaning |
| --- | --- | --- | --- |
| `term` | string | — | case-insensitive substring of the title |
| `only_missing` | boolean | `false` | only movies monitored, released and without a file |
| `only_monitored` | boolean | `false` | hide movies Radarr is not tracking |
| `limit` | integer | `25` | max `200` |

```
MOVIE               YEAR  STATE        MONITORED  QUALITY       SIZE  TMDB
Owed To You         2011  MISSING      yes        -                -  999
Next Year           2027  not out yet  yes        -                -  1234
Downloaded          2001  downloaded   yes        Bluray-1080p  7.5G  4321

3 movies: 1 downloaded (7.5G), 3 monitored, 1 missing, 1 not released yet
```

**The distinction Radarr's own list hides.** A movie monitored with no file
looks identical whether it comes out next year or came out in 2011 and has been
failing to download ever since. They are counted apart:

- **`missing`** — monitored, past its minimum availability, still no file.
  Radarr owes you this one.
- **not out yet** — monitored, no file, not yet available. Normal.

The counts always describe the whole library, whatever the filter is: *3 of your
412 movies match* is two facts and both matter.

---

## `radarr_queue_status`

No parameters. The download queue, worst first.

```
ID  MOVIE               STATUS       PROGRESS  LEFT  ETA   CLIENT       QUALITY
 9  Stuck Movie (2019)  stalled            5%  7.1G  none  qbittorrent  WEBDL-1080p
10  Fine Movie (2021)   downloading       80%  3.7G  14m   sabnzbd      Bluray-2160p

2 in the queue: 1 downloading, 1 stalled, 0 waiting to import

warning: Stuck Movie (2019) is stalled at 5% — it has been queued for 2d and the
download client reports no time remaining, so nothing is arriving
```

**Two states a progress bar hides:**

- **Stalled** — bytes still missing and the download client reports no time
  remaining. A torrent with no seeds and a healthy one are both a row with a
  percentage; only the ETA tells them apart. Rendered as `none`, never as `-`.
- **Import blocked / pending** — the download finished but Radarr could not
  import it. The file is on disk and the movie is still missing from the
  library, which is the state most easily mistaken for success.

Items the download client holds that Radarr cannot match to a movie are included
too — they occupy the client and are exactly what someone would want to clear.

**Queue ids are reassigned on every refresh.** Read them from a fresh call
rather than from earlier in a conversation.

---

## `radarr_movie_lookup`

| Parameter | Type | Default | Meaning |
| --- | --- | --- | --- |
| `term` | string | required | a title, optionally with a year |
| `limit` | integer | `10` | max `25` |

Searches TMDB through Radarr. Changes nothing, and it is the required first step
of adding anything.

```
  TMDB  TITLE  YEAR  STATUS    STUDIO              IN LIBRARY
438631  Dune   2021  released  Legendary Pictures  no

pass the TMDB id of the right one to radarr_movie_add

438631 — Paul Atreides, a brilliant and gifted young man born into a great destiny…
     cover: https://image.tmdb.org/t/p/original/d5NXSklXo0qyIYkgV94XAgMIckC.jpg
```

`IN LIBRARY` distinguishes not added / added without a file / added and
downloaded. The overview and cover URL are below the table rather than in it:
they are what tells two films of the same name apart, and both are far too wide
for a column.

---

## `radarr_system_health`

No parameters. Radarr's version, uptime, root folders and its own failing health
checks.

```
Radarr at http://localhost:7878
version: 5.14.0.9383 (master)
host: ubuntu 24.04, in docker
up for: 1d5h
queue: 2 item(s)

ROOT FOLDER  FREE  REACHABLE  UNMAPPED
/movies      838G  yes        0

TYPE     CHECK               MESSAGE
warning  IndexerStatusCheck  Indexers unavailable due to failures: NZBgeek
```

This is the answer to *"nothing is downloading and everything looks fine"*. The
container can be up and healthy while every indexer it has is refusing to answer
or its download client is unreachable — Radarr records exactly that, and nothing
else surfaces it.

Free space per root folder comes from Radarr, which reports no total, so there
is no percentage here — `system_disk_usage` is the tool that knows that.

---

## `radarr_movie_add`

| Parameter | Type | Default | Meaning |
| --- | --- | --- | --- |
| `tmdb_id` | integer | required | from `radarr_movie_lookup` |
| `quality_profile` | string | `HD-1080p` | name or id |
| `root_folder` | string | the only one | path |
| `monitored` | boolean | `true` | |
| `search_on_add` | boolean | `true` | start searching immediately |
| `minimum_availability` | string | `released` | `tba`, `announced`, `inCinemas` or `released` |

**Asks before adding**, showing the *resolved* plan rather than the arguments:

```
Add this movie to Radarr?

    Dune (2021)   [tmdb 438631]
    cover: https://image.tmdb.org/t/p/original/d5NXSklXo0qyIYkgV94XAgMIckC.jpg
    quality profile: UHD-2160p
    root folder:     /movies
    monitored:       yes
    search now:      yes
    minimum availability: released

Radarr will start looking for a release straight away, and whatever it finds will
be downloaded onto that folder.
```

The cover is there because recognising a poster takes no reading — it is the
fastest way to catch the wrong film before it downloads.

**Quality defaults to `HD-1080p`** — 1080p is what someone asking for a film
without saying more wants. The profile is matched by name, case-insensitively,
because profile ids are not stable across installs, and `HD-1080p` is what
Radarr ships with. Name a profile explicitly only for a different resolution.

An install that renamed or deleted that profile falls back to the only profile
there is; with several to choose from and no `HD-1080p` among them, the add is
refused and the options are listed rather than guessed at.

`root_folder` has no such default and may be omitted **only when Radarr has
exactly one** — it decides which disk fills up, and no name suggests the right
answer.

Either way, the profile that was resolved is named in the confirmation, so a
default is something you approve rather than something that happens to you.

`minimum_availability` defaults to `released`, not Radarr's own `announced` —
that is what stops it grabbing a cam rip of a film still in cinemas.

Adding a movie Radarr already has is refused, pointing at `radarr_movie_search`
with the `movie_id` to use.

---

## `radarr_movie_search`

| Parameter | Type | Meaning |
| --- | --- | --- |
| `movie_id` | integer | required, from `radarr_library_status` |

Radarr's own Search button: asks the indexers now for a release of a movie
already in the library, and grabs what it finds. **Asks before searching.**

This is what to use when a movie is monitored and missing — including right
after a download was removed from the queue. `radarr_movie_add` would be refused
with `TmdbId: This movie has already been added`, correctly, because a library
entry is not what was being asked for.

The result reports a *queued command*, not a found film:

```
radarr is searching for Stuck Movie (2019)
command 1234: queued

the search runs inside radarr; radarr_queue_status shows whether it grabbed
anything, and an empty queue a minute from now means the indexers returned nothing
```

Warns when the search will probably do nothing: the movie is unmonitored, or has
not reached its minimum availability. If it already has a file, the confirmation
says the search is for an **upgrade** and that a better release would replace
what is on disk.

---

## `radarr_movie_remove`

| Parameter | Type | Default | Meaning |
| --- | --- | --- | --- |
| `movie_id` | integer | required | from `radarr_library_status` |
| `delete_files` | boolean | `true` | erase the downloaded files from disk |
| `add_import_exclusion` | boolean | `false` | stop import lists adding it back |

**The one operation here that destroys something no amount of asking Radarr
again brings back.** `delete_files` defaults to `true` — removing a movie from a
homelab library normally means reclaiming the disk — so the confirmation states
the size in capitals:

```
Remove this movie from the Radarr library?

    Owed To You (2011)   [movie 42]
    state: downloaded
    folder: /movies/Owed To You (2011)

THE DOWNLOADED FILES WILL BE DELETED — 7.5G of Bluray-1080p freed.
This cannot be undone, and getting the movie back means downloading it again.
```

Pass `delete_files: false` to remove the library entry and keep the files. The
result then warns that Radarr no longer tracks them and that adding the movie
back would import them again.

`add_import_exclusion` outlives the movie, so it is stated separately.

---

## `radarr_queue_remove`

| Parameter | Type | Default | Meaning |
| --- | --- | --- | --- |
| `queue_id` | integer | required | from a **fresh** `radarr_queue_status` |
| `remove_from_client` | boolean | `true` | also delete the download from the download client |
| `blocklist` | boolean | `false` | block this release so Radarr never grabs it again |
| `skip_redownload` | boolean | `false` | stop Radarr searching for a replacement |

**Asks before removing**, naming the film and how far the download had got — 4%
and 96% are the same request and very different losses:

```
Remove this download from the Radarr queue?

    Stuck Movie (2019)
    release: Stuck.Movie.2019.1080p.WEB
    progress: 5% (7.1G still to come)
    status: stalled

The partial download will be deleted from qbittorrent. Anything already
downloaded is lost.
```

**A removal is not a blocklist.** With `blocklist` false Radarr starts nothing to
replace what you took out, so the movie goes back to monitored-and-missing and
stays there until a scheduled search. The result says so, with the `movie_id` to
pass to `radarr_movie_search`.

Use `blocklist` for a release that is broken or keeps failing to import, not for
one you simply do not want right now.

---

## `radarr_releases`

| Parameter | Type | Default | Meaning |
| --- | --- | --- | --- |
| `movie_id` | integer | — | required |
| `limit` | integer | `20` | max `100` |

Radarr's interactive search. Where `radarr_movie_search` asks Radarr to pick and
says nothing when it picks nothing, this lists **every** release the indexers
returned, in the order Radarr would prefer them, with Radarr's reason for
rejecting each one:

```
releases for Dune (2021): 14 found, 0 Radarr would take

ID        #  OK  QUALITY      SIZE  SEED   AGE  INDEXER  TITLE / WHY NOT
3f9c01ab  1  no  Bluray-2160p  62G    40   3d  Nyaa     Dune.2021.2160p.UHD… — Not an upgrade for existing movie file
b71e22d0  2  no  WEBDL-720p   2.1G    12   9d  1337x    Dune.2021.720p… — WEBDL-720p is not wanted in profile

rejected because: 9x WEBDL-720p; 5x Not an upgrade for existing movie file
```

Every release rejected is the diagnosis — the quality profile wants nothing on
offer, a size limit, a language rule — and the rejections are summarised by
reason. It grabs nothing, but queries every indexer and can take a minute. The
`id` is this server's handle for Radarr's guid, valid for 30 minutes.

---

## `radarr_release_grab`

| Parameter | Type | Meaning |
| --- | --- | --- |
| `id` | string | required, from `radarr_releases` |

**Asks first.** Sends the release to the download client through Radarr, so it
is tracked and imported like any other — including one Radarr rejected, which is
the point: this is the override. The confirmation names the rejection it
overrides, and a torrent with no seeders. If Radarr's own 30-minute copy of the
search expired, the refusal says to search again.

---

## `radarr_import_candidates`

| Parameter | Type | Meaning |
| --- | --- | --- |
| `queue_id` | integer | a download stuck on import, from `radarr_queue_status` |
| `folder` | string | or any folder, as Radarr's container sees it |

The diagnosis for the queue's **import blocked** state, where the file is on
disk and the movie is still missing. Lists what Radarr makes of each file — the
movie it matched, quality, release group — and its objection: *Unable to
determine if file is a sample*, *Not an upgrade*, *Movie title mismatch*. A file
matched to no movie is flagged. `radarr_queue_status` points here for every
blocked import.

---

## `radarr_import`

| Parameter | Type | Meaning |
| --- | --- | --- |
| `ids` | string[] | required, from `radarr_import_candidates` |
| `movie_id` | integer | the movie to import them as — required for an unmatched file, overrides the match otherwise |

Radarr's Manual Import. **Asks first**, file by file, naming the objection each
import overrides and any existing file it replaces. It sends back exactly what
Radarr parsed for the file — quality, languages, release group — so the movie
gets the same metadata the dialog would give it. Import mode is Radarr's `auto`:
a usenet download is moved, a torrent hardlinked or copied so it keeps seeding.

---

## `radarr_movie_edit`

| Parameter | Type | Meaning |
| --- | --- | --- |
| `movie_id` | integer | required |
| `monitored` | boolean | `false` stops all searching and upgrading; the file stays |
| `quality_profile` | string | by name — e.g. to ask for 4K, or to stop upgrading |
| `minimum_availability` | string | `announced`, `inCinemas` or `released` |
| `add_tags`, `remove_tags` | string[] | existing tags, by label |

**Asks first**, with each value before and after. Goes through Radarr's movie
editor, which takes only the fields to change — nothing else about the movie is
sent back. A new profile on a movie with a file warns that an upgrade may
replace it; an availability before release warns about cam copies. Tags are
never created here. Moving files to another root folder is not offered.

---

## `radarr_history`

| Parameter | Type | Default | Meaning |
| --- | --- | --- | --- |
| `movie_id` | integer | — | one movie, with its blocklist; omitted, recent events across the library |
| `limit` | integer | `25` | max `200` |

Newest first: grabbed (from which indexer), imported, failed (with Radarr's
reason — *Password protected archive*, *Sample*), deleted (upgrade, missing from
disk), renamed. For one movie it adds the **blocklist** — releases Radarr will
never grab again — and warns about a run of failures.

---

## `radarr_calendar`

| Parameter | Type | Default | Meaning |
| --- | --- | --- | --- |
| `days` | integer | `14` | how far ahead, max `90` |
| `past_days` | integer | `0` | also the recent past — what should have arrived |

Cinema, digital and physical release dates of the movies in the library, each
with whether it is already downloaded.

---

## Download client test

`radarr_system_health` with `test_download_clients=true` has Radarr test every
enabled download client — the answer to *"it grabs things and nothing ever
downloads"*. Radarr answers the test with a 400 as soon as one client fails,
and the body of that 400 is read as the result rather than treated as an error.
