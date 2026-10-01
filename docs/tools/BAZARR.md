# Bazarr tools

Nine tools over Bazarr's HTTP API — four that read, five that write. None of
them exist until the server is told where the machine is and given an API key.
Design notes: [docs/modules/bazarr.md](../modules/bazarr.md).

| Tool | Input | What it answers | Writes |
| --- | --- | --- | --- |
| `bazarr_system_health` | — | version, link to Sonarr and Radarr, every provider and whether it is throttled, failing health checks | – |
| `bazarr_subtitle_status` | `radarr_id` / `series_id` / `episode_id` / `term`, `limit` | which subtitles something has, which its profile says are missing, and which profile that is | – |
| `bazarr_wanted_subtitles` | `kind`, `limit` | Bazarr's Wanted list | – |
| `bazarr_subtitle_candidates` | `radarr_id` / `episode_id`, `language`, `limit` | a manual search: every subtitle every provider offered, scored | – |
| `bazarr_subtitle_search` | `radarr_id` / `series_id` / `episode_id`, `language`, `forced`, `hi` | searches the providers now and downloads the best match | **yes** |
| `bazarr_subtitle_download` | `id` | downloads one candidate from a manual search | **yes** |
| `bazarr_subtitle_sync` | `radarr_id` / `episode_id`, `language`, `forced`, `hi`, `shift_ms` | fixes a subtitle's timing, by the audio or by a fixed shift | **yes** |
| `bazarr_language_profile_set` | `radarr_id` / `series_id`, `profile` | changes which subtitles Bazarr wants for a movie or series | **yes** |
| `bazarr_providers_reset` | — | clears every provider throttle | **yes** |

## Configuration

| Variable | Meaning |
| --- | --- |
| `SERVER_URL` | the server the services run on: `http://localhost` when this binary runs on that same machine, otherwise `http://10.0.0.4` or `https://media.example.com/bazarr` |
| `BAZARR_API_KEY` | Bazarr → Settings → General → Security → API Key |
| `HOMELAB_MCP_BAZARR_READONLY` | set to `1` to drop the five writes, leaving the four reads |

Without the first two, **none of these tools are registered**. `SERVER_URL` is
the same variable the other services read, and each fills in its own port —
Bazarr's is **6767**, so `http://localhost` and `http://localhost:6767` are the
same thing here. A URL that names a port, carries a path or uses `https` is used
exactly as written.

## The ids are the *arrs'

Bazarr keeps no library of its own. It mirrors Radarr and Sonarr, and uses
their ids:

- **`radarr_id`** — Radarr's own movie id, the `id` `radarr_library_status`
  returns. **Not** the TMDB id.
- **`series_id`** — Sonarr's own series id, from `sonarr_library_status`.
- **`episode_id`** — Sonarr's episode id. `bazarr_subtitle_status` with a
  `series_id` lists them for the episodes lacking a subtitle, and
  `sonarr_missing_episodes` has them too.

Two consequences. Bazarr copies those libraries on a schedule, so something
added a minute ago may not be here yet. And Bazarr only knows **episodes that
have a file** — one that has not downloaded has no subtitle to want.

When no id is at hand, `bazarr_subtitle_status` with `term` finds movies and
series by title and returns them.

## Language codes are Bazarr's own

Every tool takes a `language`, and Bazarr's two-letter codes are not all ISO
639-1. Where the standard has no code for a variant, Bazarr made one up:

| You mean | Bazarr's code | Not |
| --- | --- | --- |
| Brazilian Portuguese | `pb` | `pt`, which is European Portuguese |
| Traditional Chinese | `zt` | `zh` |
| Latin American Spanish | `ea` | `es` |

Passing `pt` for a Brazilian subtitle is not an error — it is a request for the
European one, which Bazarr fetches without complaint. So a language is never
passed through as typed: it is resolved against the list the instance has, by
code, three-letter code or name, and the usual spellings of the invented codes
(`pt-BR`, `pt_br`, `pob`, `es-419`, `zh-TW`) are mapped onto them. Anything that
does not resolve is refused with the list of enabled languages, rather than
guessed at. `homelab://bazarr/language-profiles` lists them.

---

## `bazarr_system_health`

No parameters.

```
bazarr at http://localhost:6767
version: 1.6.2
up for: 9d7h
sonarr: v4.0.10, live
radarr: configured but UNREACHABLE
language profiles: 1
wanted: 4 movie subtitle(s), 30 episode subtitle(s)

PROVIDER          STATUS           RETRY
opensubtitlescom  TooManyRequests  in 3 hours
podnapisi         Good             -

bazarr reports no failing health checks

warning: 1 of 2 providers is throttled and being skipped: opensubtitlescom:
TooManyRequests, retrying in 3 hours

warning: bazarr cannot reach Radarr, so it is working from a stale copy of the
movie library and will not see new films
```

**Three states where Bazarr is up and finding nothing:**

- **Throttled providers.** The subtitle sites rate-limit hard, and a throttled
  provider is skipped silently until its timer runs out. With all of them
  throttled Bazarr is healthy and searching nothing.
- **A lost `*arr`.** Bazarr reports the Sonarr and Radarr versions it reads.
  Empty means the service is not used; `unknown` means it is configured and
  unreachable, and Bazarr is working from a frozen copy of that library.
- **No live feed.** Without the connection to an `*arr`'s event feed, new
  imports reach Bazarr only on its scheduled sync, so their subtitles arrive
  late. Only reported for a service Bazarr actually uses.

---

## `bazarr_subtitle_status`

| Parameter | Type | Meaning |
| --- | --- | --- |
| `radarr_id` | integer | a movie |
| `series_id` | integer | a whole series |
| `episode_id` | integer | one episode |
| `term` | string | find movies and series by title instead |
| `limit` | integer | episodes or matches to list; default `25`, max `200` |

Exactly one of the three ids, or `term`.

```
Dune (2021)   [radarr_id 42]
language profile: PT+EN
audio: English
release: Dune.2021.1080p.WEB-DL-GROUP

SUBTITLE  CODE  WHERE
English   en    /movies/Dune.en.srt
Spanish   es    embedded in the video

missing: Portuguese (Brazil) [pb]
```

The first call for any subtitle question, because **"why has this no Portuguese
subtitles" is as often "nothing asked for Portuguese" as "nothing found it."** A
movie or series with no language profile wants nothing: Bazarr considers
nothing missing and will never search for it on its own. That state is
invisible from the Wanted list, and is said here out loud.

A `series_id` answers with the series' counts and its episodes that lack a
subtitle, most recent first, each with its `episode_id`. A missing release name
is reported too: providers match on it, and without one only the title and year
are left.

---

## `bazarr_wanted_subtitles`

| Parameter | Type | Default | Meaning |
| --- | --- | --- | --- |
| `kind` | string | `all` | `movies`, `episodes` or `all` |
| `limit` | integer | `25` | per kind, max `200`; the totals always cover the whole list |

```
wanted: 4 movie(s) and 12 episode(s) missing a subtitle

MOVIE  RADARR_ID  MISSING
Dune          42  pb

SERIES   EP    EPISODE_ID  MISSING
Severance  2x03      9120  pb en
```

It only covers what a profile asks for, and says so in a `note` on every answer
rather than a warning — it is true every time, and a warning that is always
there is one nobody reads.

---

## `bazarr_subtitle_candidates`

| Parameter | Type | Meaning |
| --- | --- | --- |
| `radarr_id` | integer | a movie |
| `episode_id` | integer | an episode — a manual search is always for one file |
| `language` | string | only show this language; must be in the item's profile |
| `limit` | integer | default `15`, max `50` |

Bazarr's manual search: every subtitle every provider offered, scored best
first — including the ones below the minimum score an automatic search throws
away.

```
subtitles offered for Dune (2021)
searched for the languages of profile PT+EN (Portuguese (Brazil), English)

ID        SCORE  LANG  PROVIDER          MATCHES              RELEASE
3f9c01ab    93%  pb    opensubtitlescom  release_group,source  Dune.2021.1080p.WEB-DL-GROUP
b71e22d0    71%  pb    podnapisi         source                Dune.2021.720p.BluRay (+4)

2 offered, 2 shown
```

- **It searches only the languages in the item's profile.** That is how Bazarr
  does it, so with no profile the search is empty by construction — it is
  refused up front instead of waiting a minute for an empty list, and
  `bazarr_subtitle_search` with a `language` is named as the way round.
- **`hash` is the only match that guarantees sync.** Without it, prefer a
  candidate whose matches include `release_group`.
- **It is not free.** It queries every provider, and they count those queries
  against their rate limits. It can take a minute or more.
- **The `id` is this server's, not Bazarr's.** See
  [the module notes](../modules/bazarr.md#a-manual-search-result-never-leaves-the-server).
  It lasts 30 minutes.

---

## `bazarr_subtitle_search`

| Parameter | Type | Meaning |
| --- | --- | --- |
| `radarr_id` / `series_id` / `episode_id` | integer | exactly one |
| `language` | string | one language; omitted, everything the profile says is missing |
| `forced` | boolean | the forced subtitle of that language |
| `hi` | boolean | the hearing-impaired subtitle of that language |

The Search button of Bazarr's own UI. **Asks before searching.**

Two shapes, because the two questions are different:

- **One language** — *"get Portuguese subtitles for Dune"*. Sent as that
  language whether or not the profile asks for it; when it does not, the
  confirmation says Bazarr will fetch it this once and never upgrade it, and
  names `bazarr_language_profile_set` as the way to make it permanent.
- **Everything missing** — whatever the profile wants and the disk lacks. A
  `series_id` is always this form: every episode lacking something, searched for
  its own missing languages, and the confirmation says how many episodes that is.

If the language is already on disk, the confirmation says a better match would
**replace** that file.

The result reads the item back and says what is on disk **now**:

```
bazarr searched for subtitles of Dune (2021)
on disk now: pb
```

Recent Bazarr runs the search as a background job and answers before it has
run, so "not yet" is not "not found" — the result says so, and that
`bazarr_subtitle_status` a minute later is the answer. If it is still missing
then, no provider had a match above the minimum score, and
`bazarr_subtitle_candidates` shows what they did have.

---

## `bazarr_subtitle_download`

| Parameter | Type | Meaning |
| --- | --- | --- |
| `id` | string | required, from `bazarr_subtitle_candidates` in the last 30 minutes |

Downloads the candidate the user picked. **Asks first**, naming the provider,
the score, the release it was made for and what it matched. The file is written
next to the video, replacing any subtitle already there in that language.

An id this server does not know — too old, or listed before a restart — is
refused with the instruction to list again. Bazarr keeps its own copy of the
results for a limited time too; when that has expired, its own message ("please
search again") is passed on.

---

## `bazarr_subtitle_sync`

| Parameter | Type | Meaning |
| --- | --- | --- |
| `radarr_id` / `episode_id` | integer | exactly one |
| `language` | string | required, the subtitle to fix |
| `forced`, `hi` | boolean | which subtitle of that language |
| `shift_ms` | integer | shift every line by this much instead of syncing to the audio |

Rewrites a subtitle file in place, so it **asks first**, naming the file.

"Out of sync" has two fixes:

| Symptom | Fix |
| --- | --- |
| off by the same amount the whole way through | `shift_ms` — exact and instant. **Positive delays** the lines (they came too early), negative brings them forward |
| drifts, or is off by different amounts in different places | omit `shift_ms` — aligns to the audio track, takes a minute or two, and can guess wrong on a quiet film |
| off by minutes | neither — it was made for a different release; `bazarr_subtitle_candidates` finds another |

Shifts beyond ten minutes are refused for that reason. A track **embedded in the
video** cannot be changed at all — it is part of the container, not a file
Bazarr owns — and the refusal says to download an external one first.

---

## `bazarr_language_profile_set`

| Parameter | Type | Meaning |
| --- | --- | --- |
| `radarr_id` / `series_id` | integer | exactly one — a profile belongs to a movie or a whole series |
| `profile` | string | a profile name or id, or `none` |

The setting behind *"from now on I want Portuguese and English for this show"*
— where `bazarr_subtitle_search` only fetches something once, a profile is what
Bazarr keeps searching for and upgrading. **Asks first**, showing the profile
it has now and the languages of the one it is getting.

It downloads and deletes nothing, and **does not start a search**: the result
says so, and what the movie is now missing against its new profile.
`none` stops Bazarr wanting anything; the subtitles on disk stay.

---

## `bazarr_providers_reset`

No parameters. The Reset button on Bazarr's Providers page: clears every
throttle. **Asks first**, listing each throttle and when it would have lifted on
its own.

A reset is not always a fix. `TooManyRequests` and `DownloadLimitExceeded` are
the site asking Bazarr to back off, and `AuthenticationError` means the login is
wrong — both come straight back on the next query. The result names the ones it
just cleared that will, so the reset is not mistaken for a repair. With nothing
throttled, it is refused.
