# Resources

Five of them, all `text/markdown`, all read-only. Where a tool measures the
machine, a resource holds **reference data**: small, stable, and the answer to a
question rather than a reading.

| URI | Holds | Registered |
| --- | --- | --- |
| `homelab://server/configuration` | what this install registered, and the variable that would enable what it did not | always |
| `homelab://radarr/quality-profiles` | the profile names `radarr_movie_add` accepts | with Radarr |
| `homelab://radarr/root-folders` | where it can put a film, and the free space on each | with Radarr |
| `homelab://sonarr/quality-profiles` | the profile names `sonarr_series_add` accepts | with Sonarr |
| `homelab://sonarr/root-folders` | where it can put a show, and the free space on each | with Sonarr |

A client reads them with `resources/read`; most also let you attach one to a
conversation directly.

## Why these are not tools

**The profiles and folders are the values the add tools accept.** Today the only
way to discover them is to guess a name and read the refusal, which lists them —
a round trip whose successful case is an error. They change roughly never, so a
resource is the right shape: attach it once and every add afterwards has the
names in front of it.

**The configuration is the reason a tool is missing.** A server with no Radarr
key registers no Radarr tools, and the only record of that is a line on stderr
that the model will never see. So the assistant can say *"I have no way to do
that"* and be telling the truth without knowing why, and the person reading it
cannot tell a missing feature from a missing variable.

---

## `homelab://server/configuration`

Every family, whether it is registered, and — where it is not — the environment
variable that would register it. It also states the Docker allowlist verbatim,
which is the list of containers the action tools may ever touch.

```markdown
## Docker

`docker_container_status` and `docker_container_logs` are always registered, and
need access to the docker socket to answer.

`docker_container_exec` and `docker_container_restart` are **not** registered:
`HOMELAB_MCP_ALLOW_CONTAINER_NAMES` is unset. It takes a comma-separated list of
container names, and those are the only ones those tools will ever reach.

## Radarr

**Not registered.** Set `SERVER_URL` and `RADARR_API_KEY` on the machine running
this server — SERVER_URL expects a bare host, because each service fills in its
own port.
```

**No API key appears in this document, and none ever will.** The base URLs do:
they are LAN addresses, and the server already logs them at startup.

---

## `homelab://radarr/quality-profiles`, `homelab://sonarr/quality-profiles`

```markdown
# Radarr quality profiles

| Name | id |
| --- | --- |
| HD-1080p | 4 |
| Ultra-HD | 7 |
| SD | 3 |

Pass one of these names as `quality_profile` to `radarr_movie_add`. Omitted, it
uses `HD-1080p`.
```

The names are whatever that instance was configured with, not a fixed list —
which is the reason to read them rather than assume. If the profile the add tool
defaults to is **not** among them, the document says so: every add that omits
the parameter on that instance is refused, and nothing else would tell you
before it happened.

---

## `homelab://radarr/root-folders`, `homelab://sonarr/root-folders`

```markdown
# Sonarr root folders

| Path | Free | Accessible |
| --- | --- | --- |
| /srv/media/tv | 52G | yes |
| /mnt/archive/tv | unknown | **no** |

There is more than one, so `sonarr_series_add` requires `root_folder`: this is
the parameter that decides which disk fills up.

`/mnt/archive/tv` is not accessible to Sonarr right now — usually a mount that is
no longer there. Adding to it will not fail loudly; nothing will simply ever
arrive.
```

Two things this states that the add tool's refusal would not:

- **Whether the parameter is optional.** With exactly one root folder it may be
  omitted; with more than one it is required, because it decides which disk
  fills up.
- **An unreachable folder.** A mount that went away leaves the folder configured
  in Radarr or Sonarr. An add against it succeeds, and nothing ever arrives.
