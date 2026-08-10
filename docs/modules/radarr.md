# The Radarr module

`internal/radarr/`, over Radarr's v3 HTTP API. Tool reference:
[tools/RADARR.md](../../tools/RADARR.md).

The first module whose operations are not self-describing: what `movie_id: 42`
means depends on Radarr, and Radarr changes underneath. Most of what follows
falls out of that.

---

## Configuration and reachability

Gated on two variables rather than an allowlist:

```go
if radarr.Configured() { ... }   // SERVER_URL and RADARR_API_KEY both set
if radarr.ReadOnly() { return }  // stops before the four write tools
```

A configured-but-unreachable Radarr **still registers** its tools: a call that
fails with a reason is more use than tools that silently are not there. An
address that cannot be parsed is different — nothing built on it could ever work
— so it is logged once and skipped.

### Normalising the URL

`SERVER_URL` is deliberately not named for Radarr. It says where the home server
is, and each integration resolves its own service against it, which is what lets
a future Sonarr or qBittorrent module share the same variable.

`normalizeBaseURL` applies exactly one judgement call: a bare `http` host with no
port and no path gets `:7878`. Anything that names a port, carries a path (a
reverse-proxy subfolder install) or uses `https` is left exactly as written,
because all of those reach Radarr through something else and appending a port
would break them.

| Input | Result |
| --- | --- |
| `http://localhost` | `http://localhost:7878` |
| `localhost` | `http://localhost:7878` |
| `http://localhost:8310` | unchanged |
| `https://radarr.example.com` | unchanged |
| `http://nas/radarr` | unchanged |

### The key

Sent as the `X-Api-Key` header, never as the `apikey` query parameter Radarr also
accepts. A URL ends up in error messages, logs and proxy access logs; a header
does not. Nothing in the package prints the key, and the startup log names
variables rather than values.

### Radarr's own rejections are the answer

A refused add comes back as a JSON array of validation failures:

```json
[{"propertyName":"Path","errorMessage":"Folder is not writable"}]
```

Those messages *are* the answer. `apiError` parses both that shape and the
single-object one, and reports "radarr refused POST /movie: Path: Folder is not
writable" rather than "returned 400". A 401 names `RADARR_API_KEY`; a 404 with an
HTML body says the URL is probably pointing at a reverse proxy or the wrong port.

---

## Facts the API reports but does not distinguish

Both of the module's most useful signals are computed in the collector, because
Radarr returns the raw material and no verdict.

**Missing versus not out yet.** Radarr shows a movie as monitored with no file
whether it is released and failing to download, or simply not out. Those call for
opposite responses, so `Movie.Missing` is `monitored && !hasFile && isAvailable`
and the counts are kept apart. `isAvailable` is Radarr's own reading of the
minimum availability the movie was added with, so the line moves per movie.

**Stalled versus downloading.** A torrent with no seeds and a healthy download
are both a row with a percentage. The difference is that the download client
publishes a time remaining for anything that is moving, so:

```go
i.Stalled = i.Status == "downloading" && i.SizeLeftBytes > 0 &&
    i.TimeLeftSeconds == 0 && secondsUntil(r.EstimatedCompletionTime) == 0
```

**Import blocked.** `trackedDownloadState` is what matters once the bytes have
arrived: `completed` alone reads as done, while `importBlocked` means the file is
on disk and the movie is still missing from the library.

Radarr sends timespans as `[d.]hh:mm:ss[.fff]`, which Go's duration parser does
not read; `parseSpanSeconds` turns them into integers so the renderer can format
them and `structuredContent` carries a number.

---

## Two ids, one number space

A Radarr movie carries `id` (Radarr's own) and `tmdbId`. Both are plain integers,
nothing about a value says which it is, and `GET /movie/{tmdbId}` answers 404.

`GetMovie` therefore tries the Radarr id, then falls back to resolving the number
as a TMDB id through `GET /movie?tmdbId=`. Telling the caller off instead would
leave a correct request unfulfilled over a naming detail.

Which one matched needs no extra return value: `movie.ID` differs from what was
asked for exactly when the fallback resolved it, and the confirmation says so —

```
(you gave 438631, which is its TMDB id — Radarr's id for it is 42)
```

— because in the pathological case where one movie's `id` equals another's
`tmdbId`, the title in the confirmation is what separates them. Radarr's id is
what reaches the request path either way.

---

## Adding: resolve first, act second

`radarr.Plan` resolves the request and changes nothing; `radarr.Add` performs the
one request that does. The split exists for the confirmation round trip: the
handler plans on the first pass to build the message, and plans again on the
retry to recompute the fingerprint.

So the human approves **"Dune (2021) at UHD-2160p into /movies"**, not
`tmdb_id: 438631`. A retry naming a different profile re-resolves to a different
plan, fails the fingerprint check, and is refused. Verified end to end.

`Plan` fills in the one default that has a right answer and refuses the rest
rather than choosing:

- **quality profile** — defaults to `HD-1080p` (`DefaultQualityProfile`),
  matched by name and case-insensitively. Profile ids are not stable across
  installs, so the name is the only portable handle, and `HD-1080p` is what
  Radarr ships with. An install that renamed or deleted it falls back to the
  only profile there is; with a real choice and no default present, it refuses
  and lists them. This is the one place the module guesses, and it guesses
  because "1080p" is what a request with no quality in it means.
- more than one root folder and none named — it lists them. No name suggests
  which disk should fill up, so there is nothing to default to.
- a root folder Radarr cannot reach — a movie added there would download and then
  fail to import, hours later, for no visible reason
- a movie already in the library — pointing at `radarr_movie_search`

The lookup resource is kept twice: as `map[string]any` to post back untouched,
and typed to read from. Radarr's `POST /movie` takes a whole movie resource and
uses parts of it (`titleSlug`, `images`) that nothing here has any business
synthesising, so what it returned is sent back rather than rebuilt.

The poster URL is carried into the plan and shown in the confirmation —
recognising a cover takes no reading — but is **excluded from the fingerprint**.
It changes nothing about what gets added, and the provider rotates those paths;
hashing it would refuse genuine approvals over an unrelated image change, which
trains people to re-approve without reading. Only `http`/`https` URLs are passed
on, since the text may be turned into a link.

---

## Removing from the queue: fingerprinting live state

A Radarr queue id is assigned per queue refresh, so the same number can point at a
different download a minute later.

The item is looked up in the live queue on **both** passes and its title is
hashed alongside the id. A queue that moved in between produces a different
fingerprint, so the retry is refused rather than applied to whatever now holds
that id. No extra bookkeeping — the general mechanism does it.

Removals verify: after the delete, the queue is re-read and a still-present item
is reported rather than assumed gone.

### The dead end a removal leaves

**A removal is not a blocklist.** With `blocklist` false, Radarr starts nothing to
replace what was taken out: the movie returns to monitored-and-missing and stays
there until a scheduled search. Meanwhile `radarr_movie_add` is refused with
"This movie has already been added", correctly, because a library entry is not
what was wanted.

Without a search tool that is a one-way door. `radarr_movie_search` is the way
back, and every message that can leave someone in that state — the add refusal,
the removal result, the library warning — names it with the `movie_id` to use.

---

## Bounds

| Bound | Value | Why |
| --- | --- | --- |
| API timeout | 10s | answered from Radarr's local database |
| Lookup timeout | 30s | proxied to TMDB over the internet, inherits that latency |
| Queue page | 200 | the API pages at 10, which would hide most of a busy queue |
| Library listing | 25 default, 200 max | hundreds of titles would bury the answer; the counts still describe the whole library |

`GetHealth` fans out four independent requests and tolerates partial failure: "the
version is unknown" must not hide "your indexer is down". Only when all four fail
is it reported as a connection problem.
