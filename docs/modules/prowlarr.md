# The Prowlarr module

`internal/prowlarr/`, over Prowlarr's v1 HTTP API. Tool reference:
[tools/PROWLARR.md](../tools/PROWLARR.md).

Prowlarr is where Radarr's and Sonarr's indexers come from. Nearly everything it
does is felt somewhere else — an indexer that fails here is an *arr that finds
nothing — so most of this module is about saying, before and after each change,
where it will land.

---

## Configuration and reachability

Gated like the other service modules:

```go
if prowlarr.Configured() { ... }   // SERVER_URL and PROWLARR_API_KEY both set
if prowlarr.ReadOnly() { return }  // stops before the four write tools
```

`SERVER_URL` stays a bare host and **9696** is filled in. The key goes in the
`X-Api-Key` header. Refusals are the Servarr shape — an array of validation
failures — and their messages are reported rather than the status code.

---

## Credentials never leave

An indexer's settings hold usernames, passwords, cookies, passkeys and API
keys; an application's hold the *arr's API key. This module never reads a
setting into anything it returns except by an explicit allowlist — an app's
`baseUrl` — and `isSecret` refuses even that for any field Prowlarr marks as a
password, API key or user name, or whose name looks like a credential.

Two places need more than that:

- **Search results** drop `downloadUrl` and `magnetUrl`. The download link is a
  Prowlarr proxy URL with Prowlarr's own API key in it.
- **Adding an indexer** takes credentials as input. The confirmation masks every
  secret setting; the fingerprint covers the real values, hashed, so an approval
  cannot be replayed with a different password.

---

## Enabled is not working

`GET /indexer` carries each indexer's failure status — `disabledTill`,
`initialFailure` — and `/indexerstats` its queries, failures, grabs and response
time. Joined, they separate the states Prowlarr's own list shows identically:

| State | Rule |
| --- | --- |
| failing | enabled, and `disabledTill` is in the future — Prowlarr is not querying it |
| unreliable | enabled, 10+ queries in 7 days, half or more failed |
| ok | enabled, neither of the above |
| disabled | switched off |

A backoff whose `disabledTill` has passed is **not** failing: the next query
decides whether it still is, and reporting it would be reporting history.

---

## Where a change lands

Prowlarr pushes indexers into apps on its own, and three things decide whether a
given change arrives — the module reproduces Prowlarr's rule
(`ApplicationService.ShouldHandleIndexer`) rather than guessing:

- **Tags.** An app with no tags takes every indexer; an app with tags takes only
  indexers sharing one. `tagsIntersect` is that rule, and it is why
  `prowlarr_applications` can list what reaches each app and which enabled
  indexers reach nobody.
- **Sync level.** On an edit, Prowlarr syncs only to Full Sync apps. Add Only
  apps keep whatever they were first given — so disabling an indexer in Prowlarr
  leaves it active in an Add Only Sonarr. Every update confirmation names those
  apps.
- **Deletion is the exception.** A deleted indexer is removed from every
  sync-enabled app, Add Only included, because Prowlarr cleans up what it no
  longer defines. The removal confirmation says so.

Disabling does not remove: a Full Sync app keeps the indexer with RSS and both
searches switched off.

---

## Edits go through the bulk editor

`PUT /indexer/{id}` takes the whole indexer back — every setting, credentials
included — and re-tests the site before saving, so a site that is down at that
moment blocks even switching it off. `PUT /indexer/bulk` takes the ids and only
the fields to change, does not test, and is what Prowlarr's own mass editor
uses. The update tool always uses the bulk form, which also means the request
it sends is exactly the change the user approved.

---

## testall answers with a 400

`POST /indexer/testall` and `/applications/testall` return `400 Bad Request` as
soon as any one provider fails — with the full list of results as the body,
passes included. `statusError` keeps the body of a refusal so `testAll` can read
it; treating the 400 as a failure would throw away the answer the call was made
for.

A single-indexer test sends the indexer back exactly as `GET /indexer/{id}`
returned it — what the Test button in Prowlarr's UI does — and reads a 400 as
the site's own reason for failing. The few reasons with a known fix (Cloudflare,
an expired cookie, refused credentials, rate limiting, a dead domain) get it
appended.

---

## Adding from a definition

`GET /indexer/schema` is the catalogue — several hundred site definitions, each
a template, several megabytes in one response. It is read once and kept for an
hour. An add takes one template verbatim, sets the name, enable flag, sync
profile, priority and tags, fills the named settings, drops `presets`, and
posts it — so whatever the template carries that this module does not
understand is still sent as Prowlarr expects it.

Settings are resolved before anything is sent: an unknown setting names the ones
that exist, a select takes the **option's name** and sends the stored value,
checkboxes and numbers are parsed, and an unknown tag is refused (tags are never
created here). What cannot be checked in advance — whether the credentials work
— is Prowlarr's test on save, and its refusal is the answer.

---

## Searching never grabs

`GET /search` is exposed; `POST /search` (grab) is not. A release grabbed from
Prowlarr goes to the download client behind Radarr's and Sonarr's backs, is
never matched to a movie or episode, and is never imported. The search answers
whether a release exists; the *arr search tools are what act on it.

---

## In the overview

```
!  prowlarr  v2.6.5, 2 of 3 indexer(s) enabled (1 failing), 2 app(s)
.  prowlarr  v2.6.5, 12 of 12 indexer(s) enabled, 2 app(s)
```

Warnings are Prowlarr's own health checks plus the three states where the *arrs
have nothing to search. It names `prowlarr_indexer_status` when an indexer is
failing, and `prowlarr_system_health` otherwise.

---

## What this module deliberately does not do

- **Grabbing** — see above.
- **Editing indexers in Radarr or Sonarr** — on a Full Sync app the next sync
  undoes it.
- **Creating tags, sync profiles, apps or proxies** — each is a one-time setup
  step with its own credentials (an app's API key, a proxy's URL), better done
  once in Prowlarr's UI than through a model.
- **Download client settings** — per-indexer download clients are rare, and the
  *arrs own the download client for everything they grab.
