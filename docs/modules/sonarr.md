# The Sonarr module

`internal/sonarr/`, over Sonarr's v3 HTTP API. Tool reference:
[tools/SONARR.md](../../tools/SONARR.md).

Built as a deliberate parallel to [the Radarr module](radarr.md): same gating,
same URL normalisation, same resolve-then-act split, same fingerprinted
confirmations. This page covers only what is **different**, because the
similarities are documented once next door and the differences are where the
work went.

Three things do not carry over from a module about films:

1. A series is never simply present or absent.
2. Every write has a scale.
3. One download is not one queue row.

---

## Configuration

Gated on the same two facts as Radarr:

```go
if sonarr.Configured() { ... }   // SERVER_URL and SONARR_API_KEY both set
if sonarr.ReadOnly() { return }  // stops before the four write tools
```

`normalizeBaseURL` is Radarr's with one constant changed — a bare `http` host
with no port and no path gets `:8989` — and the same reasoning: a URL that names
a port, carries a path or uses `https` reaches Sonarr through something else and
appending a port would break it.

**`SERVER_URL` is shared deliberately**, and this module is what it was written
for: kept as a bare host, `http://nas` becomes `http://nas:7878` for Radarr and
`http://nas:8989` here, and one line configures both.

The limit is real and left open on purpose: a `SERVER_URL` that already names a
port can address only one of the two. A per-service override was considered and
dropped — it would solve a problem no install here has, and it would undercut the
one variable whose whole design is that each module resolves its own service
against it. If that case ever shows up, the fix belongs in both modules at once,
not bolted onto this one.

Everything else — the `X-Api-Key` header rather than the query parameter,
validation messages surfaced as the answer, a 401 naming the key variable, a
404 with an HTML body naming the URL variable — is [as described for
Radarr](radarr.md#the-key).

---

## Counting, not checking

Radarr's central computed fact is a boolean: `Missing = monitored && !hasFile &&
isAvailable`. Sonarr's is arithmetic, and it comes from three numbers Sonarr
publishes per series and per season:

| Field | Sonarr's own definition |
| --- | --- |
| `episodeFileCount` | episodes with a file |
| `episodeCount` | `(monitored AND aired) OR has a file` |
| `totalEpisodeCount` | every episode, aired or not |

That middle row is the whole module. `episodeCount` is already *"what Sonarr owes
you, plus what it has"* — the aired-and-monitored line is drawn inside Sonarr's
own statistics query — so:

```go
EpisodesMissing = episodeCount - episodeFileCount
Missing         = monitored && EpisodesMissing > 0
```

and the remainder, `totalEpisodeCount - episodeCount`, is episodes that are
unaired or deliberately unmonitored. Not a problem, and not counted as one.

The subtraction is clamped at zero. It cannot go negative in practice, because a
downloaded episode is counted in both terms — but it is a subtraction of two
numbers from a remote database, and a negative "missing" would render as a fact.

**Library totals only count monitored series.** An unmonitored show is short of
episodes by arithmetic and by nobody's expectation; including it would inflate
"what Sonarr owes you" with what Sonarr was told to ignore, and the number would
not match Sonarr's own Wanted page.

### Seasons ride along only when they are the answer

The per-season breakdown is attached when the filter selected **exactly one
series**. It is the answer to "which season is short" — which decides whether to
search a season or a whole show — and at any other scale it is thousands of rows
nobody asked for. No parameter for it: asking about one show is already the
request.

---

## Episodes: two endpoints, two questions

`sonarr_missing_episodes` has no Radarr counterpart because a movie has nothing
below it. It reads from whichever of two endpoints actually answers the question
asked:

| Scope | Endpoint | Who filters |
| --- | --- | --- |
| whole library | `/wanted/missing?monitored=true` | Sonarr |
| one series | `/episode?seriesId=` | this module |

The first is Sonarr's own Wanted page: it filters to monitored episodes of
monitored series that have aired and have no file, and it pages, so
`totalRecords` is the real count rather than the size of what came back. It is
sorted `episodes.airDateUtc` descending — one of only three sort keys the
endpoint accepts — because an episode that aired last night is what someone is
asking about, while one missing since 2014 is a decision.

The second exists because **the wanted endpoint takes no series parameter**. The
same line is drawn here instead, over one show's episode list:

```go
e.Aired   = e.AiredSecondsAgo > 0        // undated and future both read as not aired
e.Missing = e.Monitored && !e.HasFile && e.Aired
```

`lastSearchTime` is carried into the listing because it separates two failures
that look identical: an episode searched for nightly and never found is an
indexer problem, and an episode **never searched for** is monitoring switched on
after the fact — one search away from fixed.

---

## Scale is part of the operation

The Radarr write tools take one film. Every Sonarr write takes a show, and a show
is a range:

| Tool | The scale nobody sees in the arguments |
| --- | --- |
| `sonarr_series_add` | `monitor: all` on a nine-season show is the entire back catalogue |
| `sonarr_season_monitor` | switching a season on is every episode of it, aired and unaired |
| `sonarr_series_search` | with no season, several hundred grabs at once |
| `sonarr_series_remove` | not one file but every episode of every season |
| `sonarr_queue_remove` | one row can be a season pack of fourteen episodes |

So every confirmation states the size rather than the name alone: the add shows
seasons and episode counts and spells out what the `monitor` value means, the
search shows how many episodes are missing *in the chosen scope*, the removal
shows the file count next to the bytes, and the queue removal counts the
episodes riding on the same download.

### `monitor` is validated, not forwarded

Sonarr's enum is camelCase and it rejects anything else, so a value is matched
case-insensitively against the allowed set and returned in Sonarr's spelling —
`FIRSTSEASON` becomes `firstSeason`. A value that is not in the set is refused
here rather than sent, because the failure would otherwise arrive as a validation
error about a field the caller never typed.

### Monitoring is a write, and the counts for it cannot come from statistics

`sonarr_season_monitor` exists because monitoring is the switch every other tool
reads, and nothing else here could flip it. A `SeasonSearch` is dispatched with
`monitoredOnly`, so it completes successfully and grabs nothing for a season that
is off — which made "download only season 3" unreachable, since the add options
are presets (`firstSeason`, `lastSeason`, `latestSeason`) and none names an
arbitrary season.

It edits the **season**, not the episodes. Sonarr's `UpdateSeries` compares each
season's flag against the stored one and calls `SetEpisodeMonitoredBySeason` for
the ones that changed, so a single `PUT /series/{id}` cascades to every episode —
including episodes that do not exist yet, which a list of episode ids could never
cover. The body sent is the resource that was read, with one boolean flipped:
`PUT` is a whole-resource write, so a body rebuilt from this package's structs
would write every unmodelled field (tags, path, quality profile) back as absent.
That is why `getSeriesRaw` keeps the bytes alongside the typed view.

**And the counts in the confirmation are read from the episode list, not from the
season statistics.** This is the one place in the module where Sonarr's own
`episodeCount` is actively wrong for the question being asked: it is
*(monitored AND aired) OR has a file*, so a season that is currently unmonitored
reports zero, and the confirmation would have said

```
Sonarr will then try to get the 0 that are missing.
```

for exactly the season worth switching on. The plan therefore pays one extra
`GET /episode?seriesId=` and counts aired-and-fileless itself, ignoring the
monitored flag it is about to change. For the same reason the post-write
verification re-reads only the flag and keeps the counts the plan measured —
re-reading the statistics would rewrite the number the user just approved.

### The search scope is resolved before it is approved

`ResolveSearch` turns `(series_id, season?, episode_ids?)` into a `SearchScope`
and changes nothing; `Search` sends the one command that does. Same split as
`radarr.Plan`/`radarr.Add`, and for the same reason — but here it is also doing
authorisation work, because the three forms are three different Sonarr commands:

| Scope | Command |
| --- | --- |
| series | `SeriesSearch {seriesId}` |
| season | `SeasonSearch {seriesId, seasonNumber}` |
| episodes | `EpisodeSearch {episodeIds}` |

`SearchScope.Fingerprint` covers the series, the season and the sorted episode
ids, so **an approval for season 3 cannot execute against the whole series** —
the two hash differently. Resolution refuses what it cannot name: a season the
series does not have, and, importantly, an episode id belonging to a different
show. That last one matters because `EpisodeSearch` takes episode ids and nothing
else — an id from another series would be sent happily, and Sonarr would search
a show nobody approved. Duplicate ids are dropped, so the same request twice
produces the same fingerprint.

---

## One download is not one row

A season pack arrives as one file and appears in the queue **once per episode it
contains**, all sharing a `downloadId`. Nothing in a single row says so, and
three things go wrong if it is ignored:

- The queue reports fourteen things where there is one. `Queue.DownloadCount`
  counts distinct download ids next to `TotalCount`.
- A stalled pack produces fourteen identical warnings, burying everything else.
  Warnings are emitted once per `(downloadId, severity)`.
- **Removing "the row" removes the file behind all of them.** `FindQueueItem`
  returns the item *and its siblings*, the confirmation names them
  (`S02E01, S02E02, …`), and the sibling count goes into the fingerprint — so a
  row that was one episode when it was shown and a pack by the time it was
  approved is refused rather than removed.

Removals verify: after the delete the queue is re-read and a still-present item
is reported rather than assumed gone. And a removal is not a blocklist, so the
result names `sonarr_series_search` with the `series_id` — the same dead end
[described for Radarr](radarr.md#the-dead-end-a-removal-leaves), one level down.

---

## Adding: what Sonarr spells differently

The shape is Radarr's — post the lookup resource back with the resolved fields
overlaid, rather than synthesising `titleSlug`, `images` and `seasons` — with
three differences worth knowing:

**There is no by-id lookup endpoint.** Radarr has `/movie/lookup/tmdb?tmdbId=`;
Sonarr expresses an id as a term, `GET /series/lookup?term=tvdb:280619`, and
answers with a **list**. A list is not an identity, so the result whose `tvdbId`
matches is selected rather than the first element — otherwise an id the metadata
service did not recognise would add whatever it ranked highest.

**The delete flag has another name.** Sonarr's is `addImportListExclusion` where
Radarr's is `addImportExclusion`. The tool parameter is the same word in both
families; only the wire differs.

**`languageProfileId` is not sent.** Sonarr v3 required it and v4 removed it;
this module targets v4 (which still serves `/api/v3`). A v3 install would refuse
the add with its own validation message, which is at least legible.

---

## Bounds

| Bound | Value | Why |
| --- | --- | --- |
| API timeout | 10s | answered from Sonarr's local database |
| Lookup timeout | 30s | proxied to the metadata service over the internet |
| Queue page | 200 | the API pages at 10, and one season pack can occupy a dozen rows |
| Series listing | 25 default, 200 max | hundreds of shows would bury the answer |
| Episode listing | 25 default, 200 max | a library-wide Wanted list runs to thousands |

`GetHealth` fans out four independent requests and tolerates partial failure, as
Radarr's does: "the version is unknown" must not hide "your indexer is down".

---

## The duplication, stated plainly

`internal/sonarr/client.go` is `internal/radarr/client.go` with the port, the
key variable and the word "radarr" changed — roughly 300 lines that exist twice,
along with the timespan parser and the size helpers. That was a deliberate
choice: the modules stay independent, which is the layering this repo already
has, and the Radarr package is tested and shipped.

It is also the obvious thing to extract when a third `*arr` shows up. The
natural shape is an `internal/arr` holding the client, the error mapping and the
decoding helpers, parameterised by service name, port and key variable — leaving
each module with only what is actually about films or episodes. Two copies is a
fair price; three is not.
