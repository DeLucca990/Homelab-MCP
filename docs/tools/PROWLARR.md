# Prowlarr tools

Ten tools over Prowlarr's v1 HTTP API — six that read, four that write. None of
them exist until the server is told where the machine is and given an API key.
Design notes: [docs/modules/prowlarr.md](../modules/prowlarr.md).

| Tool | Input | What it answers | Writes |
| --- | --- | --- | --- |
| `prowlarr_system_health` | — | version, Prowlarr's own failing health checks, indexer and app counts | – |
| `prowlarr_indexer_status` | `term` | every indexer, failing first, with its backoff and its numbers for the last 7 days | – |
| `prowlarr_indexer_test` | `indexer` | tests one indexer or every enabled one, and says why each failure failed | – |
| `prowlarr_applications` | `test` | the apps Prowlarr syncs into, their sync level and tags, and which indexers reach each | – |
| `prowlarr_search` | `query`, `kind`, `indexers`, `limit` | searches the indexers directly — does any release exist at all? | – |
| `prowlarr_indexer_definitions` | `term`, `protocol`, `privacy`, `limit` | the sites Prowlarr can add, with the settings each takes | – |
| `prowlarr_indexer_update` | `indexer`, `enabled`, `priority`, `sync_profile` | enables, disables, re-prioritises or re-profiles an indexer | **yes** |
| `prowlarr_indexer_add` | `definition`, `settings`, `sync_profile`, `tags`, … | adds an indexer from a site definition | **yes** |
| `prowlarr_indexer_remove` | `indexer` | deletes an indexer, from Prowlarr and every synced app | **yes** |
| `prowlarr_apps_sync` | `force` | pushes every indexer to the apps now | **yes** |

## Configuration

| Variable | Meaning |
| --- | --- |
| `SERVER_URL` | the server the services run on: `http://localhost` when this binary runs on that same machine, otherwise `http://10.0.0.4` or `https://media.example.com/prowlarr` |
| `PROWLARR_API_KEY` | Prowlarr → Settings → General → Security → API Key |
| `HOMELAB_MCP_PROWLARR_READONLY` | set to `1` to drop the four writes, leaving the six reads |

Without the first two, **none of these tools are registered**. `SERVER_URL` is
the same variable the other services read, and each fills in its own port —
Prowlarr's is **9696**, so `http://localhost` and `http://localhost:9696` are
the same thing here.

## Indexers live here, not in the *arrs

With Prowlarr in front of them, Radarr's and Sonarr's indexers are **copies**
Prowlarr pushes in. On a Full Sync app an indexer edited there by hand is
overwritten on the next sync, which is why no tool here or in the Radarr and
Sonarr families edits indexers on the *arr side.

Whether a change made here arrives depends on each app:

| Sync level | New indexer | Change to an existing one | Removed indexer |
| --- | --- | --- | --- |
| `fullSync` | pushed | pushed | removed |
| `addOnly` | pushed | **never** | removed |
| `disabled` | – | – | – |

And an app **with tags** only receives indexers that share one of them. The
write confirmations name the apps a change reaches and the ones it does not.

**No credential ever leaves Prowlarr through these tools.** An indexer's and an
app's settings hold usernames, passwords, cookies and API keys; none of them is
read into a result, and the search results drop the download links, which carry
Prowlarr's own API key.

---

## `prowlarr_system_health`

No parameters.

```
prowlarr at http://localhost:9696
version: 2.6.5.5623 (master)
up for: 10d22h
indexers: 3, 2 enabled, 1 failing
applications: 2 (1 addOnly, 1 fullSync)
proxies: 0

TYPE     CHECK               MESSAGE
warning  IndexerStatusCheck  Indexers unavailable due to failures: 1337x
```

Prowlarr keeps health checks for exactly what breaks things downstream —
indexers backed off after failures, apps it cannot push to, a FlareSolverr proxy
that stopped answering — and they are passed through as warnings. On top of
those: no indexer, every indexer disabled, every enabled one failing, and no app
to sync to.

---

## `prowlarr_indexer_status`

| Parameter | Type | Meaning |
| --- | --- | --- |
| `term` | string | part of an indexer's name; the counts always cover all of them |

```
ID  INDEXER        STATE          PRIO  TYPE            PROFILE   QUERIES  FAILED  GRABS     AVG  TAGS
 2  1337x          FAILING 1h59m    25  torrent/public  Standard       80      61      0  9000ms  -
 1  Nyaa           ok               10  torrent/public  Standard      420       0     14   640ms  anime
 3  TorrentGalaxy  disabled         30  torrent/public  Standard        0       0      0       -  -
```

**Enabled is not working.** An indexer can be enabled and have failed every
query this week; Radarr and Sonarr still list it and simply get nothing. So:

- **`FAILING`** — Prowlarr has backed it off after repeated failures and is not
  querying it; the time is how long until it tries again. A backoff that has
  already run out is history, not a failure.
- **The numbers** — queries (RSS included), failures, grabs and average response
  time for the last 7 days. More than half of 10+ queries failing is a warning
  even without a backoff.

Sorted failing first, then by priority. The `ID` is what every other tool here
takes; a name works too, when it names exactly one indexer.

---

## `prowlarr_indexer_test`

| Parameter | Type | Meaning |
| --- | --- | --- |
| `indexer` | string | one indexer, by id or name; omitted, every enabled one |

Has Prowlarr test right now and report why each failure failed, in its own
words — with the fix appended where the failure has a known one: Cloudflare
(needs FlareSolverr and a shared tag), an expired cookie, refused credentials,
rate limiting, a domain that no longer resolves.

Changes nothing in Prowlarr, but it does query the sites — a private tracker
may count those requests, so it is not something to loop. Testing all at once is
one request; Prowlarr answers it with a 400 as soon as any indexer fails, and
the body of that 400 is read as the result rather than treated as an error.

---

## `prowlarr_applications`

| Parameter | Type | Default | Meaning |
| --- | --- | --- | --- |
| `test` | boolean | `false` | also have Prowlarr test its connection to every app |

```
APP     TYPE    SYNC      TAGS   TEST  RECEIVES
Radarr  Radarr  fullSync  -      -     2: 1337x, Nyaa
Sonarr  Sonarr  addOnly   anime  -     1: Nyaa

sync profiles:
  Standard (id 1): RSS, automatic search, interactive search

warning: Sonarr is on Add Only: new indexers reach it, but disabling, re-prioritising
or re-profiling one in Prowlarr never does — its copy keeps the settings it was added with
```

Where *"I added it in Prowlarr and Radarr does not have it"* is answered.
`RECEIVES` is worked out with Prowlarr's own rule — enabled, and either the app
has no tags or the two share one — so an indexer that reaches nobody, or an app
that receives nothing, shows as exactly that.

---

## `prowlarr_search`

| Parameter | Type | Default | Meaning |
| --- | --- | --- | --- |
| `query` | string | — | required, the title as a release would name it |
| `kind` | string | `any` | `movie` or `tv` to limit to those categories |
| `indexers` | string[] | all enabled | only these, by id or name |
| `limit` | integer | `25` | max `100`, most seeded first |

The question under *"why has this not downloaded"*: does any release exist?
Nothing here means no search from Radarr or Sonarr will find anything either;
plenty here and nothing grabbed means the cause is on their side — the quality
profile, a language rule, availability.

**It grabs nothing, on purpose.** A release grabbed through Prowlarr goes to the
download client without Radarr or Sonarr knowing, and is never imported.
`radarr_movie_search` and `sonarr_series_search` are how to act on what it
finds; every answer carries that as a `note`. Torrents with no seeders are
counted in a warning — they would never finish.

---

## `prowlarr_indexer_definitions`

| Parameter | Type | Meaning |
| --- | --- | --- |
| `term` | string | part of the site's name |
| `protocol` | string | `torrent` or `usenet` |
| `privacy` | string | `public`, `semiPrivate` or `private` |
| `limit` | integer | default `15`, max `50` |

```
1337x   [definition: 1337x]
  torrent, public, en-US, needs FlareSolverr
  1337X is a Public torrent site that offers verified torrent downloads
  settings: downloadlink (magnet|iTorrents.org), sort (created|seeders|size|title)

PrivateHD   [definition: privatehd]
  torrent, private, en-US
  settings: username (secret), password (secret), freeleech (true|false)
```

The first step of adding an indexer. Each definition says whether it needs an
account, whether it is behind Cloudflare, whether it is **already added**, and
the settings it takes — the names `prowlarr_indexer_add` expects in `settings`,
with a select's options by name and credentials marked secret. The catalogue is
several megabytes and is read once an hour.

---

## `prowlarr_indexer_update`

| Parameter | Type | Meaning |
| --- | --- | --- |
| `indexer` | string | required, by id or name |
| `enabled` | boolean | switch it off or back on |
| `priority` | integer | 1–50, lower preferred when two indexers return the same release |
| `sync_profile` | string | by name or id — whether the apps use it for RSS, automatic and interactive search |

**Asks first.** Only the fields passed change: it goes through Prowlarr's bulk
editor, which touches nothing else — credentials included — and does not
re-test the site, so a site that happens to be down does not block turning it
off. The confirmation shows each field before and after, which apps receive the
change at once (Full Sync) and which **will not** (Add Only).

A disabled indexer stays in the Full Sync apps with RSS and both searches off;
it is not removed. Enabling one that is failing is allowed, and says it will
not make it work.

---

## `prowlarr_indexer_add`

| Parameter | Type | Default | Meaning |
| --- | --- | --- | --- |
| `definition` | string | — | required, from `prowlarr_indexer_definitions` |
| `name` | string | the site's name | |
| `sync_profile` | string | the only one | required when there is more than one |
| `priority` | integer | `25` | 1–50 |
| `tags` | string[] | — | existing tags, by label; they decide which apps receive it |
| `settings` | object | — | the definition's settings by name; a select takes the option's name |
| `disabled` | boolean | `false` | add it switched off |

**Asks first**, showing every setting that will be sent with **credentials
masked**, and the apps it will reach — or that it reaches none, because its
tags match no app. Warns up front when a private site is given no credentials,
and when the site needs FlareSolverr.

Prowlarr tests an enabled indexer before saving it, so a refusal is the site's
own answer — passed on with the known fix appended. Unknown definitions,
settings, select options and tags are refused before anything is sent; tags are
never created here. A definition already in use is refused and pointed at
`prowlarr_indexer_update`.

---

## `prowlarr_indexer_remove`

| Parameter | Type | Meaning |
| --- | --- | --- |
| `indexer` | string | required, by id or name |

**Asks first.** Deletes the indexer and its settings, credentials included, and
removes it from **every** app that syncs — Add Only too, unlike every other
change. The confirmation names those apps, and says so when this is the last
enabled indexer, when it is a private tracker whose login would have to be
entered again, and when it is working — disabling keeps it for later.

---

## `prowlarr_apps_sync`

| Parameter | Type | Default | Meaning |
| --- | --- | --- | --- |
| `force` | boolean | `false` | rewrite every synced indexer even when unchanged |

Prowlarr's *Sync App Indexers* button. **Asks first**, listing the apps by sync
level. The fix for an app that has drifted — an indexer deleted there by hand, a
new app that came up empty.

Not harmless: on Full Sync apps it also removes indexers Prowlarr no longer
handles, and `force` overwrites every edit made to a synced indexer in the apps.
The result is a queued command — an app Prowlarr cannot reach is skipped without
failing it, and `prowlarr_applications` with `test=true` shows which.
