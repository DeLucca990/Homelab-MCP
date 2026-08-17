package mcp

import (
	"log"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DeLucca990/homelab-mcp/internal/containers"
	"github.com/DeLucca990/homelab-mcp/internal/jellyfin"
	"github.com/DeLucca990/homelab-mcp/internal/radarr"
	"github.com/DeLucca990/homelab-mcp/internal/sonarr"
)

func registerTools(s *sdk.Server) {
	// overview tool
	sdk.AddTool(s, &sdk.Tool{
		Name: "homelab_overview",
		Annotations: &sdk.ToolAnnotations{
			Title:        "Homelab Overview",
			ReadOnlyHint: true,
		},
		Description: "Answers 'is anything wrong with this server' in ONE call. Runs every " +
			"cheap check at once — disk, memory, systemd units, Docker containers, the " +
			"Radarr and Sonarr queues and health, and what Jellyfin is streaming, where each " +
			"is configured — and reports only what needs attention, naming the tool to call " +
			"for the detail behind each line. Prefer this over calling the individual read " +
			"tools one by one for any general question about the server's state: it is one " +
			"round trip instead of seven, every warning is the one that area's own tool would " +
			"have given, and on a healthy machine the entire answer is a single line. A check " +
			"that cannot run says so without withholding the others. It deliberately leaves " +
			"out per-core CPU, which costs half a second and where a pinned core is not a " +
			"fault — but where Jellyfin is configured the jellyfin line says how many streams " +
			"are being re-encoded on the CPU, which is what that load usually is. Use " +
			"system_cpu_cores when the question is about the cores themselves.",
	}, handleOverview)

	// system host tool
	sdk.AddTool(s, &sdk.Tool{
		Name: "system_host_info",
		Annotations: &sdk.ToolAnnotations{
			Title:        "System Host Info",
			ReadOnlyHint: true,
		},
		Description: "Returns general server information: hostname, operating system, kernel version, architecture and uptime.",
	}, handleHostInfo)

	// system cpu cores
	sdk.AddTool(s, &sdk.Tool{
		Name: "system_cpu_cores",
		Annotations: &sdk.ToolAnnotations{
			Title:        "System CPU Cores",
			ReadOnlyHint: true,
		},
		Description: "Returns the detailed usage of each CPU core individually, " +
			"broken down into user, kernel, nice, interrupt and I/O wait time — " +
			"the same breakdown htop shows per core. Takes about 500ms.",
	}, handleCoreUsage)

	// system memory tool
	sdk.AddTool(s, &sdk.Tool{
		Name: "system_memory_stats",
		Annotations: &sdk.ToolAnnotations{
			Title:        "System Memory Stats",
			ReadOnlyHint: true,
		},
		Description: "Returns the server's RAM and swap usage. " +
			"To assess memory pressure use 'available_bytes' and 'used_percent', " +
			"never 'free_bytes' — Linux keeps idle RAM occupied with disk cache, " +
			"so a low 'free_bytes' is normal and does not indicate a problem. Immediate response.",
	}, handleMemoryStats)

	// system disk tool
	sdk.AddTool(s, &sdk.Tool{
		Name: "system_disk_usage",
		Annotations: &sdk.ToolAnnotations{
			Title:        "System Disk Usage",
			ReadOnlyHint: true,
		},
		Description: "Returns disk space usage per mountpoint, sorted from " +
			"fullest to emptiest. By default it filters out pseudo-filesystems, snap packages " +
			"and container layers, which show up as 100% full without that indicating a problem. " +
			"Also includes inode usage: a disk can become unusable from inode exhaustion " +
			"even with plenty of free bytes.",
	}, handleDiskStats)

	// systemd services tool
	sdk.AddTool(s, &sdk.Tool{
		Name: "system_service_status",
		Annotations: &sdk.ToolAnnotations{
			Title:        "System Service Status",
			ReadOnlyHint: true,
		},
		Description: "Returns the state of systemd service units — whether the services on " +
			"this server are running. By default it scans every unit and reports only those " +
			"needing attention (failed, stuck starting, or restarting), worst first; pass " +
			"'units' to ask about specific ones by name. Reports the restart count, which is " +
			"what distinguishes a service that is genuinely running from one that is " +
			"crash-looping — the latter reads as active in any point-in-time check. " +
			"Linux only; errors on hosts without systemd.",
	}, handleServiceStatus)

	// docker containers tool
	sdk.AddTool(s, &sdk.Tool{
		Name: "docker_container_status",
		Annotations: &sdk.ToolAnnotations{
			Title:        "Docker Containe Status",
			ReadOnlyHint: true,
		},
		Description: "Returns the state of Docker containers, worst first. By default it " +
			"reports running containers plus anything broken, and hides containers that " +
			"stopped cleanly; pass 'names' to ask about specific ones. Beyond what " +
			"'docker ps' shows, it reports healthcheck results, restart counts, exit codes, " +
			"and whether a container was killed by the OOM killer for exceeding its memory " +
			"limit — the usual cause of a container that keeps dying for no visible reason. " +
			"Requires access to the docker socket.",
	}, handleContainerStatus)

	// docker logs tool
	sdk.AddTool(s, &sdk.Tool{
		Name: "docker_container_logs",
		Annotations: &sdk.ToolAnnotations{
			Title:        "Docker Container Logs",
			ReadOnlyHint: true,
		},
		Description: "Returns what a container has written to stdout and stderr, interleaved " +
			"in order, most recent lines by default. This is the follow-up to any finding from " +
			"docker_container_status — an OOM kill, a failing healthcheck or a restart loop " +
			"tells you a container is broken, and the logs tell you why. Note that most images " +
			"log to stdout, which the daemon captures and which therefore exists nowhere in the " +
			"container's own filesystem: reading it with a shell command would find nothing. " +
			"Read-only.",
	}, handleLogs)

	// docker exec + restart tools
	if allowed := containers.ActionAllowlist(); len(allowed) > 0 { // initialization statment condition - if <statement>; <condition> {}
		sdk.AddTool(s, &sdk.Tool{
			Name: "docker_container_exec",
			Annotations: &sdk.ToolAnnotations{
				Title:           "Run a command inside a container",
				ReadOnlyHint:    false,
				DestructiveHint: ptr(true),
				OpenWorldHint:   ptr(false),
			},
			Description: "Runs a command inside one of the containers this server is permitted " +
				"to reach (" + strings.Join(allowed, ", ") + ") and returns its stdout, stderr " +
				"and exit code. ...",
		}, handleExec)

		sdk.AddTool(s, &sdk.Tool{
			Name: "docker_container_restart",
			Annotations: &sdk.ToolAnnotations{
				Title:           "Restart a container",
				ReadOnlyHint:    false,
				DestructiveHint: ptr(true),
				OpenWorldHint:   ptr(false),
				IdempotentHint:  true,
			},
			Description: "Restarts one of the containers this server is permitted to restart (" +
				strings.Join(allowed, ", ") + "), then waits and reports whether it actually came " +
				"back up. ...",
		}, handleRestart)
	}

	registerRadarrTools(s)
	registerSonarrTools(s)
	registerJellyfinTools(s)
}

// RADARR tools
func registerRadarrTools(s *sdk.Server) {
	if !radarr.Configured() {
		return
	}

	base, err := radarr.BaseURL()
	if err != nil {
		log.Printf("radarr tools not registered: %v", err)
		return
	}

	mode := "read and write"
	if radarr.ReadOnly() {
		mode = "read-only (" + radarr.ReadOnlyEnv + " is set)"
	}
	log.Printf("radarr at %s: %s", base, mode)

	// radarr lookup tool
	sdk.AddTool(s, &sdk.Tool{
		Name: "radarr_movie_lookup",
		Annotations: &sdk.ToolAnnotations{
			Title:        "Radarr Movie Lookup",
			ReadOnlyHint: true,
		},
		Description: "Searches TMDB through Radarr and returns candidate movies with their " +
			"TMDB ids, and whether each is already in the library. This is the first step of " +
			"adding anything: radarr_movie_add takes a tmdb_id, because a title on its own does " +
			"not identify a film — several films share one. Changes nothing.",
	}, handleRadarrLookup)

	// radarr library tool
	sdk.AddTool(s, &sdk.Tool{
		Name: "radarr_library_status",
		Annotations: &sdk.ToolAnnotations{
			Title:        "Radarr Library Status",
			ReadOnlyHint: true,
		},
		Description: "Returns what Radarr is monitoring and what it has actually downloaded, " +
			"missing first. Pass 'term' to ask about one film. Beyond Radarr's own list it " +
			"separates the two ways a movie can be absent: 'missing' means it has been released, " +
			"is monitored, and still has no file — Radarr owes you that one — while a film that " +
			"is simply not out yet is counted apart and is not a problem.",
	}, handleRadarrLibrary)

	// radarr queue tool
	sdk.AddTool(s, &sdk.Tool{
		Name: "radarr_queue_status",
		Annotations: &sdk.ToolAnnotations{
			Title:        "Radarr Download Queue",
			ReadOnlyHint: true,
		},
		Description: "Returns Radarr's download queue with the progress of each item, worst " +
			"first. Beyond the percentage it reports the two states a progress bar hides: a " +
			"download that is stalled — still incomplete, with the client reporting no time " +
			"remaining, so nothing is arriving — and one that finished but could not be " +
			"imported, where the file is on disk and the movie is still missing from the " +
			"library. This is what answers 'is my movie downloading' and 'why has it not " +
			"appeared yet'.",
	}, handleRadarrQueue)

	// radarr health tool
	sdk.AddTool(s, &sdk.Tool{
		Name: "radarr_system_health",
		Annotations: &sdk.ToolAnnotations{
			Title:        "Radarr System Health",
			ReadOnlyHint: true,
		},
		Description: "Returns Radarr's version, uptime, root folders and its own failing health " +
			"checks. This is the answer to 'nothing is downloading and everything looks fine': " +
			"the container can be up and healthy while every indexer it has is refusing to " +
			"answer or its download client is unreachable, and Radarr records exactly that here.",
	}, handleRadarrHealth)

	if radarr.ReadOnly() {
		return
	}

	// radarr add + queue removal tools
	sdk.AddTool(s, &sdk.Tool{
		Name: "radarr_movie_add",
		Annotations: &sdk.ToolAnnotations{
			Title:           "Add a movie to Radarr",
			ReadOnlyHint:    false,
			DestructiveHint: ptr(false),
			OpenWorldHint:   ptr(true),
			IdempotentHint:  true,
		},
		Description: "Adds a movie to Radarr and, by default, starts searching for a release " +
			"immediately. Takes the tmdb_id from radarr_movie_lookup — call that first, and do " +
			"not guess an id. Quality defaults to the HD-1080p profile, so pass " +
			"'quality_profile' only when the user asked for a different resolution. The root " +
			"folder may be omitted only when Radarr has exactly one, because it decides which " +
			"disk fills up. Asks the user before adding anything, showing the film and the " +
			"destination it resolved.",
	}, handleRadarrAdd)

	sdk.AddTool(s, &sdk.Tool{
		Name: "radarr_movie_search",
		Annotations: &sdk.ToolAnnotations{
			Title:           "Search for a release of a movie in the library",
			ReadOnlyHint:    false,
			DestructiveHint: ptr(false),
			OpenWorldHint:   ptr(true),
			IdempotentHint:  true,
		},
		Description: "Asks Radarr to search its indexers right now for a movie that is ALREADY " +
			"in the library, and grab what it finds — the Search button of Radarr's own UI. " +
			"This is what to use when a movie is monitored and missing, including after a " +
			"download was removed from the queue: radarr_movie_add would be refused with 'This " +
			"movie has already been added', because adding is not what is being asked for. " +
			"Takes Radarr's movie_id from radarr_library_status, which is NOT the TMDB id. Not " +
			"to be confused with radarr_movie_lookup, which searches TMDB for a film to add. " +
			"Asks the user first.",
	}, handleRadarrSearch)

	sdk.AddTool(s, &sdk.Tool{
		Name: "radarr_movie_remove",
		Annotations: &sdk.ToolAnnotations{
			Title:           "Remove a movie from the Radarr library",
			ReadOnlyHint:    false,
			DestructiveHint: ptr(true),
			OpenWorldHint:   ptr(false),
		},
		Description: "Removes a movie from Radarr's library and, by default, deletes the " +
			"downloaded files from disk — 'delete_files' defaults to TRUE, so this frees the " +
			"space. Pass delete_files=false when the user wants the movie out of the library " +
			"but the files kept; file deletion cannot be undone. Takes the movie's id from " +
			"radarr_library_status — Radarr's own 'id' field, though a TMDB id is accepted and " +
			"resolved. Asks the user first, naming the film, the folder and how much disk is " +
			"about to be freed.",
	}, handleRadarrMovieRemove)

	sdk.AddTool(s, &sdk.Tool{
		Name: "radarr_queue_remove",
		Annotations: &sdk.ToolAnnotations{
			Title:           "Remove a download from the Radarr queue",
			ReadOnlyHint:    false,
			DestructiveHint: ptr(true),
			OpenWorldHint:   ptr(false),
		},
		Description: "Removes one item from Radarr's download queue, by default deleting the " +
			"partial download from the download client too. Use it for a download that has " +
			"failed, stalled, or cannot be imported. The queue_id must come from a fresh " +
			"radarr_queue_status: Radarr reassigns those ids whenever the queue refreshes. Asks " +
			"the user before removing anything, naming the film and how far the download had got.",
	}, handleRadarrQueueRemove)
}

// SONARR tools
func registerSonarrTools(s *sdk.Server) {
	if !sonarr.Configured() {
		return
	}

	base, err := sonarr.BaseURL()
	if err != nil {
		log.Printf("sonarr tools not registered: %v", err)
		return
	}

	mode := "read and write"
	if sonarr.ReadOnly() {
		mode = "read-only (" + sonarr.ReadOnlyEnv + " is set)"
	}
	log.Printf("sonarr at %s: %s", base, mode)

	// sonarr lookup tool
	sdk.AddTool(s, &sdk.Tool{
		Name: "sonarr_series_lookup",
		Annotations: &sdk.ToolAnnotations{
			Title:        "Sonarr Series Lookup",
			ReadOnlyHint: true,
		},
		Description: "Searches TheTVDB through Sonarr and returns candidate series with their " +
			"TVDB ids, season counts and whether each is already in the library. This is the " +
			"first step of adding anything: sonarr_series_add takes a tvdb_id, because a title " +
			"on its own does not identify a show — 'The Office' is four of them. Changes nothing.",
	}, handleSonarrLookup)

	// sonarr library tool
	sdk.AddTool(s, &sdk.Tool{
		Name: "sonarr_library_status",
		Annotations: &sdk.ToolAnnotations{
			Title:        "Sonarr Library Status",
			ReadOnlyHint: true,
		},
		Description: "Returns what Sonarr is monitoring and how complete each series is — " +
			"episodes on disk out of episodes it owes you — least complete first. Pass 'term' " +
			"to ask about one show, which also returns a per-season breakdown showing which " +
			"season is short. Unlike a film, a series is almost never simply present or " +
			"absent, so 'monitored' answers nothing on its own and the counts are the answer.",
	}, handleSonarrLibrary)

	// sonarr missing episodes tool
	sdk.AddTool(s, &sdk.Tool{
		Name: "sonarr_missing_episodes",
		Annotations: &sdk.ToolAnnotations{
			Title:        "Sonarr Missing Episodes",
			ReadOnlyHint: true,
		},
		Description: "Returns the individual episodes Sonarr is monitoring, has seen air, and " +
			"has not downloaded — its own Wanted list, most recently aired first. This is the " +
			"level below sonarr_library_status: that one says a series is short three episodes, " +
			"this one says which three, when they aired and whether anything has ever searched " +
			"for them. Pass 'series_id' for one show. The episode ids it returns are what " +
			"sonarr_series_search takes to grab a single episode.",
	}, handleSonarrMissing)

	// sonarr queue tool
	sdk.AddTool(s, &sdk.Tool{
		Name: "sonarr_queue_status",
		Annotations: &sdk.ToolAnnotations{
			Title:        "Sonarr Download Queue",
			ReadOnlyHint: true,
		},
		Description: "Returns Sonarr's download queue with the progress of each item, worst " +
			"first. Beyond the percentage it reports the two states a progress bar hides: a " +
			"download that is stalled — still incomplete, with the client reporting no time " +
			"remaining, so nothing is arriving — and one that finished but could not be " +
			"imported, where the file is on disk and the episode is still missing from the " +
			"library. Note that one download is not one row: a season pack appears once per " +
			"episode it holds, all sharing a download id.",
	}, handleSonarrQueue)

	// sonarr health tool
	sdk.AddTool(s, &sdk.Tool{
		Name: "sonarr_system_health",
		Annotations: &sdk.ToolAnnotations{
			Title:        "Sonarr System Health",
			ReadOnlyHint: true,
		},
		Description: "Returns Sonarr's version, uptime, root folders and its own failing health " +
			"checks. This is the answer to 'no episode has arrived all week and everything " +
			"looks fine': the container can be up and healthy while every indexer it has is " +
			"refusing to answer or its download client is unreachable, and Sonarr records " +
			"exactly that here.",
	}, handleSonarrHealth)

	if sonarr.ReadOnly() {
		return
	}

	// sonarr add + search + removal tools
	sdk.AddTool(s, &sdk.Tool{
		Name: "sonarr_series_add",
		Annotations: &sdk.ToolAnnotations{
			Title:           "Add a series to Sonarr",
			ReadOnlyHint:    false,
			DestructiveHint: ptr(false),
			OpenWorldHint:   ptr(true),
			IdempotentHint:  true,
		},
		Description: "Adds a series to Sonarr and, by default, starts searching for every " +
			"monitored episode immediately. Takes the tvdb_id from sonarr_series_lookup — call " +
			"that first, and do not guess an id. The parameter that decides the size of this is " +
			"'monitor': it defaults to 'all', which on a long-running show means downloading the " +
			"entire back catalogue — pass 'future' for a show wanted only from now on, or " +
			"'firstSeason' to try one season first. Quality defaults to the HD-1080p profile. " +
			"The root folder may be omitted only when Sonarr has exactly one, because it decides " +
			"which disk fills up. Asks the user before adding anything, showing the show, how " +
			"many seasons it has and the destination it resolved.",
	}, handleSonarrAdd)

	sdk.AddTool(s, &sdk.Tool{
		Name: "sonarr_series_search",
		Annotations: &sdk.ToolAnnotations{
			Title:           "Search for releases of a series in the library",
			ReadOnlyHint:    false,
			DestructiveHint: ptr(false),
			OpenWorldHint:   ptr(true),
			IdempotentHint:  true,
		},
		Description: "Asks Sonarr to search its indexers right now for a series that is ALREADY " +
			"in the library, and grab what it finds — the Search button of Sonarr's own UI. " +
			"This is what to use when episodes are monitored and missing, including after a " +
			"download was removed from the queue: sonarr_series_add would be refused with 'This " +
			"series has already been added', because adding is not what is being asked for. " +
			"Searches the whole series by default; pass 'season' for one season, or " +
			"'episode_ids' from sonarr_missing_episodes for specific episodes — the whole-series " +
			"form on a long-running show is hundreds of grabs at once. Takes Sonarr's series_id " +
			"from sonarr_library_status, which is NOT the TVDB id. Asks the user first.",
	}, handleSonarrSearch)

	sdk.AddTool(s, &sdk.Tool{
		Name: "sonarr_season_monitor",
		Annotations: &sdk.ToolAnnotations{
			Title:           "Monitor or unmonitor one season",
			ReadOnlyHint:    false,
			DestructiveHint: ptr(false),
			OpenWorldHint:   ptr(false),
			IdempotentHint:  true,
		},
		Description: "Turns Sonarr's monitoring on or off for ONE season of a series, cascading " +
			"to every episode of it. This is the switch every other Sonarr tool reads: a search " +
			"of an unmonitored season finds nothing, because Sonarr filters those episodes out " +
			"before asking an indexer. It is therefore the missing step in 'download only " +
			"season 3' — sonarr_series_add can only monitor presets (all, firstSeason, " +
			"lastSeason, latestSeason, none), so an arbitrary season is added unmonitored and " +
			"switched on here, then searched with sonarr_series_search. Monitoring does NOT " +
			"start a search by itself. Defaults to monitoring; pass monitored=false to stop " +
			"following a season. Deletes nothing. Asks the user first, naming the show, the " +
			"season and how many episodes the flag covers.",
	}, handleSonarrSeasonMonitor)

	sdk.AddTool(s, &sdk.Tool{
		Name: "sonarr_series_remove",
		Annotations: &sdk.ToolAnnotations{
			Title:           "Remove a series from the Sonarr library",
			ReadOnlyHint:    false,
			DestructiveHint: ptr(true),
			OpenWorldHint:   ptr(false),
		},
		Description: "Removes a series from Sonarr's library and, by default, deletes every " +
			"downloaded episode from disk — 'delete_files' defaults to TRUE, so this frees the " +
			"space, and for a series that is every episode of every season. Pass " +
			"delete_files=false when the user wants the show out of the library but the files " +
			"kept; file deletion cannot be undone. Takes the series' id from " +
			"sonarr_library_status — Sonarr's own 'id' field, though a TVDB id is accepted and " +
			"resolved. Asks the user first, naming the show, the folder, how many episode files " +
			"are about to go and how much disk that frees.",
	}, handleSonarrSeriesRemove)

	sdk.AddTool(s, &sdk.Tool{
		Name: "sonarr_queue_remove",
		Annotations: &sdk.ToolAnnotations{
			Title:           "Remove a download from the Sonarr queue",
			ReadOnlyHint:    false,
			DestructiveHint: ptr(true),
			OpenWorldHint:   ptr(false),
		},
		Description: "Removes one item from Sonarr's download queue, by default deleting the " +
			"partial download from the download client too. Use it for a download that has " +
			"failed, stalled, or cannot be imported. The queue_id must come from a fresh " +
			"sonarr_queue_status: Sonarr reassigns those ids whenever the queue refreshes. Be " +
			"aware that one download can be a season pack occupying several queue rows — " +
			"removing any one of them removes the file behind all of them, and the confirmation " +
			"says how many episodes that is. Asks the user before removing anything.",
	}, handleSonarrQueueRemove)
}

// JELLYFIN tools
func registerJellyfinTools(s *sdk.Server) {
	if !jellyfin.Configured() {
		return
	}

	base, err := jellyfin.BaseURL()
	if err != nil {
		log.Printf("jellyfin tools not registered: %v", err)
		return
	}

	log.Printf("jellyfin at %s: read-only", base)

	sdk.AddTool(s, &sdk.Tool{
		Name: "jellyfin_active_sessions",
		Annotations: &sdk.ToolAnnotations{
			Title:        "Jellyfin Active Sessions",
			ReadOnlyHint: true,
		},
		Description: "Returns who is watching what on Jellyfin right now and what each stream " +
			"costs the server, most expensive first. This is the tool that answers 'why is the " +
			"CPU at 100%' on a media server, which homelab_overview deliberately will not guess " +
			"at: a pinned core is either a transcode or a fault, and only this can tell them " +
			"apart. It goes finer than Jellyfin's own label — 'Transcode' covers both a remux " +
			"that costs nothing and a full re-encode that saturates a core, so the 'work' field " +
			"separates direct, remux, hardware transcode and software transcode, and reports the " +
			"reasons Jellyfin would not send the file untouched. It also flags a session that " +
			"claims to be playing but stopped reporting progress: nobody is watching it and the " +
			"transcode is still running. Idle sessions are hidden unless include_idle is set.",
	}, handleJellyfinSessions)

	sdk.AddTool(s, &sdk.Tool{
		Name: "jellyfin_system_health",
		Annotations: &sdk.ToolAnnotations{
			Title:        "Jellyfin System Health",
			ReadOnlyHint: true,
		},
		Description: "Returns Jellyfin's version, its encoding configuration, free space on every " +
			"folder it writes to, its scheduled tasks and any plugin that is not running. Three " +
			"of those are invisible from outside the application: a server with no hardware " +
			"acceleration configured looks perfectly healthy until the first stream that needs " +
			"it; the transcode temp directory is usually not the disk the media is on, and a " +
			"stream that fills it stops playing rather than reporting an error; and a library " +
			"scan that has been failing means files the *arrs imported are on disk and absent " +
			"from Jellyfin. Most of what it reads is administrator-only — a key without those " +
			"rights loses those sections and says so rather than failing the call.",
	}, handleJellyfinHealth)
}
