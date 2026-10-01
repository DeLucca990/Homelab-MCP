# The Bazarr module

`internal/bazarr/`, over Bazarr's HTTP API. Tool reference:
[tools/BAZARR.md](../tools/BAZARR.md).

Bazarr is the one service here with no library of its own: it mirrors Radarr's
movies and Sonarr's series and writes subtitle files next to what they
downloaded. Most of what follows is about the places where that shows.

---

## Configuration and reachability

Gated like the `*arr` modules:

```go
if bazarr.Configured() { ... }   // SERVER_URL and BAZARR_API_KEY both set
if bazarr.ReadOnly() { return }  // stops before the five write tools
```

A configured-but-unreachable Bazarr still registers its tools, and `SERVER_URL`
stays a bare host: `normalizeBaseURL` fills in **6767** the same way the `*arr`
clients fill in their own ports, and leaves alone anything with a port, a path
or `https`.

The key goes in the `X-API-KEY` header — never the `apikey` query parameter
Bazarr also accepts, because a URL ends up in error messages and proxy logs.

### Writes are forms, not JSON

Bazarr's API is a thin layer over the forms its own web UI submits. Every write
here is sent as `application/x-www-form-urlencoded` with the same field names
the UI uses, and booleans as the strings Python prints — `"True"`, `"False"` —
because that is what the handlers compare against.

### Its refusals are the answer

A failed request comes back as a sentence encoded as a JSON string:

```json
"Movie file not found. Path mapping issue?"
```

That sentence is the diagnosis — a path mapping problem is the most common
reason Bazarr can see a movie and not touch it — so `apiError` decodes it and
reports it rather than "returned 500".

---

## The ids are the *arrs'

A movie is identified by its **Radarr** id and an episode by its **Sonarr** id,
so the numbers `radarr_library_status` and the Sonarr tools return are the ones
these tools take — nothing is translated between families. Two things follow,
and both are said in the errors rather than left to be discovered:

- **Bazarr copies those libraries on a schedule.** A film added a minute ago
  may not be here yet.
- **Bazarr only knows episodes with a file.** An episode that has not
  downloaded has nothing to subtitle.

An unknown `radarr_id` also says the number was probably a TMDB id — the same
mix-up [the Radarr module](radarr.md#two-ids-one-number-space) resolves, and one
Bazarr gives no way to resolve here, since it does not index by TMDB.

---

## Languages: Bazarr's codes are not ISO

A subtitle language is a two-letter code, and Bazarr invented the ones the
standard lacks: `pb` for Brazilian Portuguese, `zt` for Traditional Chinese,
`ea` for Latin American Spanish. The trap is that the wrong code is **not an
error**: `pt` is a valid request for European Portuguese, which Bazarr will
fetch, and a Brazilian user finds out when they press play.

So `resolveLanguage` never passes input through. It matches against the list
the instance has (`/system/languages`) by code, three-letter code or name, maps
the usual spellings of the invented codes (`pt-BR`, `pob`, `es-419`, `zh-TW`)
first, accepts a partial name only when it names exactly one language, and
otherwise refuses with the enabled languages listed. The same resolution is
applied to what providers return, which spell Brazilian Portuguese `pt-BR`.

---

## A profile decides what is "missing"

Every movie and series points at a language profile, or at none. With none,
Bazarr considers nothing missing: it never searches, the Wanted list never shows
it, and nothing looks wrong. That is the usual end of "why has this no
subtitles", so the status tool warns about it explicitly, and the Wanted tool
carries a standing `note` that it only covers what a profile asks for.

The profile also bounds the **manual** search: Bazarr searches the profile's
languages and nothing else. A manual search on an item with no profile is
therefore empty by construction, and is refused before the minute-long provider
round trip rather than after.

---

## Searching: "not yet" is not "not found"

Recent Bazarr queues a subtitle search as a background job and answers `204`
before it has run; older versions searched inside the request. The two are
indistinguishable from the response, so the result never trusts it: after the
request, the item is **read back**, and a language is reported as found only if
an external subtitle in it is on disk. Anything else is `pending`, with the
sentence that says it may still be running and what to call in a minute.

A language that was already on disk is excluded from both lists: its presence
proves nothing about whether the search replaced it, and the result says that
instead of claiming success.

---

## A manual search result never leaves the server

Downloading a manual search result means handing Bazarr back the token it
returned for it. On older versions that token is a serialized copy of the whole
subtitle object — kilobytes of base64. A model asked to copy that from one call
into the next will eventually change a character, and the failure it gets back
does not explain itself.

So the token stays here. `GetCandidates` keeps each result in an in-process
cache under an 8-character id derived from the item, provider and token, and
`bazarr_subtitle_download` takes that id. The costs are deliberate:

- **An id lasts 30 minutes**, and at most 2 000 are kept, oldest evicted first.
- **An id is only good on the process that listed it.** The HTTP transport is
  stateless, but the process is one long-lived one, so this holds across
  requests; a restart forgets them, and the refusal says to list again.
- Bazarr's own cache of results expires too. When it has, its message ("please
  search again") is passed on with the tool name added.

The confirmation fingerprint covers the token, not only the id, so an id that
somehow came to mean a different subtitle cannot ride an earlier approval.

---

## Sync and shift

Both rewrite the subtitle file in place, through the same `PATCH /subtitles`
the UI uses: `action=sync` for an audio alignment, and Bazarr's own mod string
for a shift — `shift_offset(h=0,m=0,s=-2,ms=-500)`, every component negated for
a negative shift, exactly as the web UI builds it.

An embedded track cannot be synced: it is inside the video container, not a
file Bazarr owns. Shifts over ten minutes are refused, because a subtitle that
far out was made for a different release and no amount of shifting fixes it.

---

## Provider throttles

`/providers` lists every **enabled** provider with its throttle reason or
`Good`. The health tool puts throttled ones first and warns when all of them
are, which is the state where Bazarr is up and searching nothing.

A reset clears every throttle at once — Bazarr has no per-provider reset. The
result flags the throttles that will come straight back: `TooManyRequests` and
`DownloadLimitExceeded` are the site asking for a pause, and an authentication
or configuration error is a login that is still wrong.

---

## In the overview

`homelab_overview` gets a `bazarr` section wherever the module is configured,
from the health collector alone:

```
!  bazarr  v1.6.2, 2 provider(s) (1 throttled), 4 movie and 30 episode subtitle(s) wanted
.  bazarr  v1.6.2, 3 provider(s), 0 movie and 0 episode subtitle(s) wanted
```

The wanted counts are in the headline and **not** the warnings. A library
always wants some subtitle no provider has, and a permanent `!` is one nobody
reads. What is a warning is the state where nothing can be found at all.

---

## What this module deliberately does not do

- **Settings writes** (`POST /system/settings`) — providers, credentials,
  scores and enabled languages. The endpoint takes the whole settings form, and
  a partial write there can disable things nobody asked to touch. It belongs in
  the generic, diffed configuration tool planned for the `*arr` families, not a
  one-off here.
- **Creating or editing language profiles** — the same endpoint.
- **Uploading a subtitle file** — needs a file the model does not have.
- **Deleting subtitles** — rarely wanted, and a better download replaces the
  file anyway.
- **Translation** — it calls out to a translation service and produces a
  machine translation; worth a deliberate decision before it is a tool.
