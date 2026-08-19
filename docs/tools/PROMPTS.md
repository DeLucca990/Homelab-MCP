# Prompts

Two of them. Where a tool is a fact, a prompt is a **procedure**: the order to
check things in, and the trap waiting at each step.

| Prompt | Arguments | Registered |
| --- | --- | --- |
| `triage` | `symptom` (optional) | always |
| `why-no-download` | `title` (optional) | when Radarr or Sonarr is configured |

Clients surface these differently — Claude Code lists them as slash commands,
others as a picker. Either way the client fetches the text and sends it as the
user's message; nothing here runs on its own.

## Why they exist

The knowledge is already in this server, and that is the problem. It is spread
across twenty-eight tool descriptions, where a model meets it **one tool at a
time** and has to reassemble the sequence itself — so the same question gets a
different route depending on which description the model happened to read first.
A prompt states the route once.

They also carry what no single tool description can: the order in which steps
rule each other out. `sonarr_series_search` will happily search an unmonitored
season and find nothing, and the tool that explains that is
`sonarr_season_monitor` — a tool the model has no reason to read while it is
looking at the search one.

## They follow registration

A procedure whose third step is a tool this server never created is worse than
no procedure, because it reads as authoritative. So the prompts are conditional
in the same way the tools are:

- `why-no-download` is not registered at all without Radarr or Sonarr.
- Both expand the tool names they mention to the ones that exist here. With only
  Sonarr configured, step 2 says `sonarr_queue_status` and never mentions
  Radarr; with both, it names both.

---

## `triage`

| Argument | Required | Meaning |
| --- | --- | --- |
| `symptom` | no | what made you look — *"jellyfin keeps buffering"*, *"the whole box is slow"* |

A read-only sweep of the machine, worst first. Given a symptom it works out
whether the server explains it, rather than only listing what is broken.

The procedure starts at `homelab_overview` and goes down only into the areas
that flag, carrying the reading that each area needs and that its numbers do not
state on their own:

- disk — inode exhaustion fails with `no space left on device` while `df` still
  shows free bytes
- memory — read `available_bytes`, never `free_bytes`; Linux keeps idle RAM as
  disk cache
- services — a unit reading `active` with a climbing restart count is
  crash-looping, not running
- docker — exit code 137 is the OOM killer, and most images log to stdout, so
  there is no log file inside the container to go looking for
- radarr / sonarr — health before queue: every indexer refusing to answer looks
  exactly like there being nothing to download

It ends by telling the model to change nothing, and to report in one line if
nothing is wrong instead of narrating every check that passed.

---

## `why-no-download`

| Argument | Required | Meaning |
| --- | --- | --- |
| `title` | no | the film or series in question; without it the queue is examined as a whole, which is the right shape for *"nothing has arrived all week"* |

```
Work out why The Expanse season 4 has not downloaded.

The order matters, because each step rules out a different cause and the last
two are indistinguishable from the outside:

1. The library first (radarr_library_status / sonarr_library_status, with 'term' set) — is it
   even there, and is it monitored? A film that has not been released yet is not
   late, and it is counted apart from one that is genuinely missing. A series
   reading "monitored" can still have the season in question unmonitored, and
   Sonarr will not search for an episode it is not monitoring — the per-season
   breakdown is what shows that.

2. The queue (radarr_queue_status / sonarr_queue_status) — if it is downloading, the answer
   is here, and a progress bar hides both of the states that matter: stalled,
   where nothing is arriving at all, and finished-but-not-imported, where the
   file is on disk and the library still says missing. They need different
   fixes. One Sonarr season pack occupies one queue row per episode it holds.

3. Health (radarr_system_health / sonarr_system_health) — if the queue is empty. This is the
   answer to "nothing was found": the service can be up, healthy and idle while
   every indexer it has is refusing to answer or its download client is
   unreachable, and it records exactly that here.

4. For a series, sonarr_missing_episodes — which episodes are actually owed,
   when they aired, and whether anything has ever searched for them.
```

Step 4 appears only where Sonarr is configured.

It closes by asking for the diagnosis before the fix. Every tool that would act
here asks for confirmation anyway; the point is to not spend that approval on a
search fired at a problem that was never about searching.
