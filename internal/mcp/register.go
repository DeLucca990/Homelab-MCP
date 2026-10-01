package mcp

import (
	"log"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DeLucca990/homelab-mcp/internal/bazarr"
	"github.com/DeLucca990/homelab-mcp/internal/containers"
	"github.com/DeLucca990/homelab-mcp/internal/jellyfin"
	"github.com/DeLucca990/homelab-mcp/internal/prowlarr"
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
			"Radarr and Sonarr queues and health, what Jellyfin is streaming, whether Prowlarr's " +
			"indexers are answering and whether Bazarr can reach its subtitle providers, where each " +
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
	registerBazarrTools(s)
	registerProwlarrTools(s)
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
			"answer or its download client is unreachable, and Radarr records exactly that here. Pass test_download_clients=true to also test the connection to each download client.",
	}, handleRadarrHealth)

	sdk.AddTool(s, &sdk.Tool{
		Name: "radarr_releases",
		Annotations: &sdk.ToolAnnotations{
			Title:         "Radarr Interactive Search",
			ReadOnlyHint:  true,
			OpenWorldHint: ptr(true),
		},
		Description: "Asks the indexers for one movie ('movie_id') and lists every release they returned, in " +
			"the order Radarr would prefer them, with the reason Radarr rejected each one — wrong quality " +
			"for the profile, too small, a language or custom format rule, already blocklisted. When " +
			"radarr_movie_search grabs nothing, this is the list that says why. Each release has a short " +
			"'id' that radarr_release_grab takes. Grabs nothing, but queries every indexer and can take a minute.",
	}, handleRadarrReleases)

	sdk.AddTool(s, &sdk.Tool{
		Name: "radarr_import_candidates",
		Annotations: &sdk.ToolAnnotations{
			Title:        "Radarr Manual Import Candidates",
			ReadOnlyHint: true,
		},
		Description: "Lists what Radarr makes of the files in a finished download — by the 'queue_id' " +
			"of a download stuck on import, or any 'folder' — with the movie each file matched and " +
			"Radarr's objection to importing it. This is the diagnosis for the queue's 'import blocked' " +
			"state, where the file is on disk and the movie is still missing; radarr_import acts on it " +
			"with the short 'id' of each file. Changes nothing.",
	}, handleRadarrImportCandidates)

	sdk.AddTool(s, &sdk.Tool{
		Name: "radarr_history",
		Annotations: &sdk.ToolAnnotations{
			Title:        "Radarr History",
			ReadOnlyHint: true,
		},
		Description: "Returns what happened to a movie or the whole library, newest first: which release was grabbed from " +
			"which indexer, whether it imported, whether a download failed and why, whether a file was " +
			"deleted or upgraded. With 'movie_id' it also lists that movie's blocklist — releases Radarr " +
			"will never grab again. Without it, the most recent events across the whole library.",
	}, handleRadarrHistory)

	sdk.AddTool(s, &sdk.Tool{
		Name: "radarr_calendar",
		Annotations: &sdk.ToolAnnotations{
			Title:        "Radarr Calendar",
			ReadOnlyHint: true,
		},
		Description: "Returns the cinema, digital and physical release dates of the movies in the library over the next 'days' (and, " +
			"with 'past_days', the recent past), with whether each is already downloaded. Answers " +
			"'what comes out this week' and 'what should have arrived by now'.",
	}, handleRadarrCalendar)

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

	sdk.AddTool(s, &sdk.Tool{
		Name: "radarr_release_grab",
		Annotations: &sdk.ToolAnnotations{
			Title:           "Grab a chosen release",
			ReadOnlyHint:    false,
			DestructiveHint: ptr(false),
			OpenWorldHint:   ptr(true),
		},
		Description: "Sends one release picked from a radarr_releases listing to the download client " +
			"through Radarr, so it is tracked and imported like any other — including one Radarr " +
			"rejected, which is the point: this is the override. Takes the short 'id' from that " +
			"listing; ids last 30 minutes. Asks the user first, naming the release, its quality, size " +
			"and indexer, and any rejection it overrides.",
	}, handleRadarrGrab)

	sdk.AddTool(s, &sdk.Tool{
		Name: "radarr_import",
		Annotations: &sdk.ToolAnnotations{
			Title:           "Import files into Radarr",
			ReadOnlyHint:    false,
			DestructiveHint: ptr(false),
			OpenWorldHint:   ptr(false),
		},
		Description: "Imports files listed by radarr_import_candidates into the library — Radarr's " +
			"Manual Import — using the quality and languages Radarr parsed for each. Pass 'movie_id' " +
			"for a file Radarr could not match, or to override its match. Overrides Radarr's objections, " +
			"and replaces an existing file. Moves a usenet download; hardlinks or copies a torrent so it " +
			"keeps seeding. Asks the user first, file by file.",
	}, handleRadarrImport)

	sdk.AddTool(s, &sdk.Tool{
		Name: "radarr_movie_edit",
		Annotations: &sdk.ToolAnnotations{
			Title:           "Change a movie's settings",
			ReadOnlyHint:    false,
			DestructiveHint: ptr(false),
			OpenWorldHint:   ptr(false),
			IdempotentHint:  true,
		},
		Description: "Changes one movie's settings through Radarr's movie editor: 'monitored' (false " +
			"stops all searching and upgrading), 'quality_profile' (e.g. to ask for 4K, or to stop " +
			"upgrading), 'minimum_availability' (when searching starts), and tags. Only the fields " +
			"passed change; nothing is downloaded or deleted. Asks the user first, with each value " +
			"before and after.",
	}, handleRadarrEdit)
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
			"exactly that here. Pass test_download_clients=true to also test the connection to each download client.",
	}, handleSonarrHealth)

	sdk.AddTool(s, &sdk.Tool{
		Name: "sonarr_releases",
		Annotations: &sdk.ToolAnnotations{
			Title:         "Sonarr Interactive Search",
			ReadOnlyHint:  true,
			OpenWorldHint: ptr(true),
		},
		Description: "Asks the indexers for one episode ('episode_id') or one season ('series_id' and 'season') and lists every release they returned, in " +
			"the order Sonarr would prefer them, with the reason Sonarr rejected each one — wrong quality " +
			"for the profile, too small, a language or custom format rule, already blocklisted. When " +
			"sonarr_series_search grabs nothing, this is the list that says why. Each release has a short " +
			"'id' that sonarr_release_grab takes. Grabs nothing, but queries every indexer and can take a minute.",
	}, handleSonarrReleases)

	sdk.AddTool(s, &sdk.Tool{
		Name: "sonarr_import_candidates",
		Annotations: &sdk.ToolAnnotations{
			Title:        "Sonarr Manual Import Candidates",
			ReadOnlyHint: true,
		},
		Description: "Lists what Sonarr makes of the files in a finished download — by the 'queue_id' " +
			"of a download stuck on import, or any 'folder' — with the episode each file matched (a season pack is many files) and " +
			"Sonarr's objection to importing it. This is the diagnosis for the queue's 'import blocked' " +
			"state, where the file is on disk and the episode is still missing; sonarr_import acts on it " +
			"with the short 'id' of each file. Changes nothing.",
	}, handleSonarrImportCandidates)

	sdk.AddTool(s, &sdk.Tool{
		Name: "sonarr_history",
		Annotations: &sdk.ToolAnnotations{
			Title:        "Sonarr History",
			ReadOnlyHint: true,
		},
		Description: "Returns what happened to a series or the whole library, newest first: which release was grabbed from " +
			"which indexer, whether it imported, whether a download failed and why, whether a file was " +
			"deleted or upgraded. With 'series_id' it also lists that episode's blocklist — releases Sonarr " +
			"will never grab again. Without it, the most recent events across the whole library.",
	}, handleSonarrHistory)

	sdk.AddTool(s, &sdk.Tool{
		Name: "sonarr_calendar",
		Annotations: &sdk.ToolAnnotations{
			Title:        "Sonarr Calendar",
			ReadOnlyHint: true,
		},
		Description: "Returns the episodes airing in the library over the next 'days' (and, " +
			"with 'past_days', the recent past), with whether each is already downloaded. Answers " +
			"'what comes out this week' and 'what should have arrived by now'.",
	}, handleSonarrCalendar)

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

	sdk.AddTool(s, &sdk.Tool{
		Name: "sonarr_release_grab",
		Annotations: &sdk.ToolAnnotations{
			Title:           "Grab a chosen release",
			ReadOnlyHint:    false,
			DestructiveHint: ptr(false),
			OpenWorldHint:   ptr(true),
		},
		Description: "Sends one release picked from a sonarr_releases listing to the download client " +
			"through Sonarr, so it is tracked and imported like any other — including one Sonarr " +
			"rejected, which is the point: this is the override. A season pack imports every episode " +
			"in it. Takes the short 'id' from that listing; ids last 30 minutes. Asks the user first.",
	}, handleSonarrGrab)

	sdk.AddTool(s, &sdk.Tool{
		Name: "sonarr_import",
		Annotations: &sdk.ToolAnnotations{
			Title:           "Import files into Sonarr",
			ReadOnlyHint:    false,
			DestructiveHint: ptr(false),
			OpenWorldHint:   ptr(false),
		},
		Description: "Imports files listed by sonarr_import_candidates into the library — Sonarr's " +
			"Manual Import — as the episodes Sonarr matched them to, with the quality and languages it " +
			"parsed. A file matched to no episode cannot be imported from here. Overrides Sonarr's " +
			"objections and replaces existing files. Moves a usenet download; hardlinks or copies a " +
			"torrent. Asks the user first, file by file.",
	}, handleSonarrImport)

	sdk.AddTool(s, &sdk.Tool{
		Name: "sonarr_series_edit",
		Annotations: &sdk.ToolAnnotations{
			Title:           "Change a series' settings",
			ReadOnlyHint:    false,
			DestructiveHint: ptr(false),
			OpenWorldHint:   ptr(false),
			IdempotentHint:  true,
		},
		Description: "Changes one series' settings through Sonarr's series editor: 'monitored' (false " +
			"ignores the whole series), 'quality_profile', 'series_type' (standard, daily or anime — how " +
			"release names are numbered; the fix for an anime found under the wrong episode numbers), " +
			"and tags. Only the fields passed change; nothing is downloaded or deleted. Asks the user first.",
	}, handleSonarrEdit)

	sdk.AddTool(s, &sdk.Tool{
		Name: "sonarr_episode_monitor",
		Annotations: &sdk.ToolAnnotations{
			Title:           "Monitor or unmonitor episodes",
			ReadOnlyHint:    false,
			DestructiveHint: ptr(false),
			OpenWorldHint:   ptr(false),
			IdempotentHint:  true,
		},
		Description: "Turns monitoring on or off for individual episodes of one series — the finer " +
			"switch below sonarr_season_monitor: 'only get the finale', 'skip the recap episode'. Takes " +
			"'episode_ids' from sonarr_missing_episodes or sonarr_calendar. Does not search by itself. " +
			"Asks the user first, listing the episodes.",
	}, handleSonarrEpisodeMonitor)
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

	mode := "read and write"
	if jellyfin.ReadOnly() {
		mode = "read-only (" + jellyfin.ReadOnlyEnv + " is set)"
	}
	log.Printf("jellyfin at %s: %s", base, mode)

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

	sdk.AddTool(s, &sdk.Tool{
		Name: "jellyfin_users",
		Annotations: &sdk.ToolAnnotations{
			Title:        "Jellyfin Users",
			ReadOnlyHint: true,
		},
		Description: "Returns every Jellyfin user with their playback preferences — audio and " +
			"subtitle language, subtitle mode — and their access: which libraries, the bitrate cap " +
			"away from home, whether video may be transcoded for them, and when they were last " +
			"seen. The first call for 'why does it always start with the wrong subtitles' (a " +
			"preference) and 'why does it fail for her and not for me' (often transcoding switched " +
			"off, so a file her TV cannot play simply fails). Administrator key required.",
	}, handleJellyfinUsers)

	sdk.AddTool(s, &sdk.Tool{
		Name: "jellyfin_find_item",
		Annotations: &sdk.ToolAnnotations{
			Title:        "Find in the Jellyfin library",
			ReadOnlyHint: true,
		},
		Description: "Searches Jellyfin's library for movies, series and episodes by title and " +
			"returns each with its id, file path and when it was added — and, with 'user', whether " +
			"they have watched it. This is the Jellyfin half of 'Radarr imported it and I cannot " +
			"find it': absent here means the library has not caught up, which jellyfin_library_scan " +
			"fixes. The ids are what jellyfin_library_scan and jellyfin_mark_played take.",
	}, handleJellyfinFind)

	sdk.AddTool(s, &sdk.Tool{
		Name: "jellyfin_activity_log",
		Annotations: &sdk.ToolAnnotations{
			Title:        "Jellyfin Activity Log",
			ReadOnlyHint: true,
		},
		Description: "Returns Jellyfin's own record of what happened, most recent first: sign-ins " +
			"and failed sign-ins with their address, playback started and stopped, failed scheduled " +
			"tasks, plugin updates. Answers 'who was watching last night' and 'is someone guessing " +
			"passwords' — a burst of failed sign-ins is called out. Pass only_problems for warnings, " +
			"errors and failed sign-ins alone. Administrator key required.",
	}, handleJellyfinActivity)

	if jellyfin.ReadOnly() {
		return
	}

	sdk.AddTool(s, &sdk.Tool{
		Name: "jellyfin_library_scan",
		Annotations: &sdk.ToolAnnotations{
			Title:           "Scan a Jellyfin library",
			ReadOnlyHint:    false,
			DestructiveHint: ptr(false),
			OpenWorldHint:   ptr(false),
			IdempotentHint:  true,
		},
		Description: "Makes Jellyfin look for new and changed files: one item ('item_id' from " +
			"jellyfin_find_item), one library ('library' by name), or every library when neither is " +
			"given. The fix for a film Radarr imported that Jellyfin does not show. Keeps edited " +
			"metadata and images. A full scan can run a long time on a large collection, so the " +
			"narrowest scope that answers the question is the one to use. Asks the user first.",
	}, handleJellyfinScan)

	sdk.AddTool(s, &sdk.Tool{
		Name: "jellyfin_session_stop",
		Annotations: &sdk.ToolAnnotations{
			Title:           "Stop a Jellyfin stream",
			ReadOnlyHint:    false,
			DestructiveHint: ptr(true),
			OpenWorldHint:   ptr(false),
		},
		Description: "Stops what one session is playing, by 'session_id' from " +
			"jellyfin_active_sessions. It tells the app to stop when the app accepts remote control, " +
			"and ends the server-side transcode when there is one — which is what actually frees " +
			"the CPU for a stale session whose viewer has gone and whose app will never receive a " +
			"stop command. An optional 'message' is shown on their screen first. Asks the user " +
			"first, naming who is watching what.",
	}, handleJellyfinStop)

	sdk.AddTool(s, &sdk.Tool{
		Name: "jellyfin_session_message",
		Annotations: &sdk.ToolAnnotations{
			Title:           "Show a message on a Jellyfin screen",
			ReadOnlyHint:    false,
			DestructiveHint: ptr(false),
			OpenWorldHint:   ptr(false),
		},
		Description: "Shows a short message on one session's screen for ten seconds — 'the server " +
			"restarts in five minutes'. Only works on apps that accept remote control. Asks the " +
			"user first, with the exact text.",
	}, handleJellyfinMessage)

	sdk.AddTool(s, &sdk.Tool{
		Name: "jellyfin_user_preferences_set",
		Annotations: &sdk.ToolAnnotations{
			Title:           "Set a user's audio and subtitle preferences",
			ReadOnlyHint:    false,
			DestructiveHint: ptr(false),
			OpenWorldHint:   ptr(false),
			IdempotentHint:  true,
		},
		Description: "Sets which audio and subtitle tracks Jellyfin picks for one user when " +
			"playback starts: 'subtitle_language' and 'audio_language' ('por', 'eng', 'pt-BR' or a " +
			"name — Jellyfin stores Portuguese as 'por' for both Brazil and Portugal), and " +
			"'subtitle_mode' (Default, Always, Smart, OnlyForced, None). This is the other half of " +
			"'I want Portuguese subtitles': Bazarr puts the file on disk, this makes the player " +
			"choose it. Only the fields passed change. Asks the user first.",
	}, handleJellyfinPreferences)

	sdk.AddTool(s, &sdk.Tool{
		Name: "jellyfin_user_access_set",
		Annotations: &sdk.ToolAnnotations{
			Title:           "Change what a Jellyfin user can do",
			ReadOnlyHint:    false,
			DestructiveHint: ptr(true),
			OpenWorldHint:   ptr(false),
			IdempotentHint:  true,
		},
		Description: "Changes one user's access: which libraries they see ('libraries' replaces the " +
			"list; ['all'] for every one), their bitrate cap away from home, whether video may be " +
			"transcoded or remuxed for them, and whether the account is disabled. Never touches " +
			"administrator rights or passwords. Only the fields passed change. Asks the user first, " +
			"showing each value before and after.",
	}, handleJellyfinAccess)

	sdk.AddTool(s, &sdk.Tool{
		Name: "jellyfin_transcoding_set",
		Annotations: &sdk.ToolAnnotations{
			Title:           "Set Jellyfin hardware acceleration",
			ReadOnlyHint:    false,
			DestructiveHint: ptr(true),
			OpenWorldHint:   ptr(false),
			IdempotentHint:  true,
		},
		Description: "Sets how Jellyfin transcodes: the hardware acceleration backend (qsv, vaapi, " +
			"nvenc…), its device, which codecs the GPU decodes, and whether it encodes too. " +
			"jellyfin_system_health shows the current values. It applies to every stream at once, " +
			"and a backend the machine or container cannot use makes every transcode fail — the " +
			"confirmation and the result both carry the values to set it back. Asks the user first.",
	}, handleJellyfinTranscoding)

	sdk.AddTool(s, &sdk.Tool{
		Name: "jellyfin_mark_played",
		Annotations: &sdk.ToolAnnotations{
			Title:           "Mark watched or unwatched",
			ReadOnlyHint:    false,
			DestructiveHint: ptr(false),
			OpenWorldHint:   ptr(false),
			IdempotentHint:  true,
		},
		Description: "Marks an item watched (the default) or unwatched for one user. A series or " +
			"season marks every episode in it; unwatched also clears the resume position. Takes the " +
			"'item_id' from jellyfin_find_item. Asks the user first.",
	}, handleJellyfinPlayed)
}

// BAZARR tools
func registerBazarrTools(s *sdk.Server) {
	if !bazarr.Configured() {
		return
	}

	base, err := bazarr.BaseURL()
	if err != nil {
		log.Printf("bazarr tools not registered: %v", err)
		return
	}

	mode := "read and write"
	if bazarr.ReadOnly() {
		mode = "read-only (" + bazarr.ReadOnlyEnv + " is set)"
	}
	log.Printf("bazarr at %s: %s", base, mode)

	sdk.AddTool(s, &sdk.Tool{
		Name: "bazarr_system_health",
		Annotations: &sdk.ToolAnnotations{
			Title:        "Bazarr System Health",
			ReadOnlyHint: true,
		},
		Description: "Returns Bazarr's version, its link to Sonarr and Radarr, every enabled subtitle " +
			"provider with whether it is throttled, its own failing health checks and how many " +
			"subtitles are wanted. This is the answer to 'no subtitles are arriving and everything " +
			"looks fine': the subtitle sites rate-limit hard, and a throttled provider is skipped " +
			"silently until its timer runs out — with all of them throttled Bazarr searches nothing. " +
			"It also catches a Bazarr that has lost Sonarr or Radarr and is working from a frozen " +
			"copy of their libraries.",
	}, handleBazarrHealth)

	sdk.AddTool(s, &sdk.Tool{
		Name: "bazarr_subtitle_status",
		Annotations: &sdk.ToolAnnotations{
			Title:        "Bazarr Subtitle Status",
			ReadOnlyHint: true,
		},
		Description: "Returns which subtitles a movie, episode or series has on disk or embedded, which " +
			"its language profile says are missing, and which profile that is. The first call for " +
			"any subtitle question — 'why has this no Portuguese subtitles' is as often 'no profile " +
			"asks for Portuguese' as 'nothing was found'. Bazarr uses the *arr ids: 'radarr_id' is " +
			"Radarr's movie id from radarr_library_status (not TMDB), 'series_id' and 'episode_id' " +
			"are Sonarr's. A series_id lists the episodes lacking a subtitle with their episode ids. " +
			"Without an id, 'term' finds movies and series by title. Language codes are Bazarr's " +
			"own: Brazilian Portuguese is 'pb', not 'pt'.",
	}, handleBazarrStatus)

	sdk.AddTool(s, &sdk.Tool{
		Name: "bazarr_wanted_subtitles",
		Annotations: &sdk.ToolAnnotations{
			Title:        "Bazarr Wanted Subtitles",
			ReadOnlyHint: true,
		},
		Description: "Returns Bazarr's Wanted list: every movie and episode missing a subtitle its " +
			"language profile asks for, with the languages missing. It only covers what a profile " +
			"asks for — an item with no profile never appears however bare it is, and " +
			"bazarr_subtitle_status is what shows that.",
	}, handleBazarrWanted)

	sdk.AddTool(s, &sdk.Tool{
		Name: "bazarr_subtitle_candidates",
		Annotations: &sdk.ToolAnnotations{
			Title:         "Bazarr Manual Subtitle Search",
			ReadOnlyHint:  true,
			OpenWorldHint: ptr(true),
		},
		Description: "Bazarr's manual search: asks every provider for one movie or one episode and " +
			"lists everything offered, scored, best first — including matches below the minimum " +
			"score an automatic search would have rejected. Use it when bazarr_subtitle_search found " +
			"nothing, or when the subtitle it found is out of sync and a different one is wanted. " +
			"Only searches the languages in the item's language profile. Each candidate has a short " +
			"'id' that bazarr_subtitle_download takes. Downloads nothing, but queries every provider, " +
			"which counts against their rate limits, and can take a minute or more.",
	}, handleBazarrCandidates)

	if bazarr.ReadOnly() {
		return
	}

	sdk.AddTool(s, &sdk.Tool{
		Name: "bazarr_subtitle_search",
		Annotations: &sdk.ToolAnnotations{
			Title:           "Search and download subtitles",
			ReadOnlyHint:    false,
			DestructiveHint: ptr(false),
			OpenWorldHint:   ptr(true),
			IdempotentHint:  true,
		},
		Description: "Asks Bazarr to search its providers now and download the best subtitle above " +
			"its minimum score — the Search button of Bazarr's own UI. Pass 'language' for one " +
			"language ('pb' for Brazilian Portuguese; 'pt-BR' and names are understood), which works " +
			"even if the language profile does not ask for it; omit it to fetch everything the " +
			"profile says is missing. Targets a movie ('radarr_id', Radarr's id), an episode " +
			"('episode_id') or a whole series ('series_id', every episode lacking something — no " +
			"'language' there). Recent Bazarr runs the search as a background job, so the result " +
			"says what is on disk right after and what is still pending. Asks the user first.",
	}, handleBazarrSearch)

	sdk.AddTool(s, &sdk.Tool{
		Name: "bazarr_subtitle_download",
		Annotations: &sdk.ToolAnnotations{
			Title:           "Download a chosen subtitle",
			ReadOnlyHint:    false,
			DestructiveHint: ptr(false),
			OpenWorldHint:   ptr(true),
			IdempotentHint:  true,
		},
		Description: "Downloads one subtitle picked from a bazarr_subtitle_candidates listing, by the " +
			"short 'id' that listing gave it. Ids last 30 minutes. The file is written next to the " +
			"video, replacing any subtitle already there in that language. Asks the user first, " +
			"naming the provider, the score and the release it was made for.",
	}, handleBazarrDownload)

	sdk.AddTool(s, &sdk.Tool{
		Name: "bazarr_subtitle_sync",
		Annotations: &sdk.ToolAnnotations{
			Title:           "Fix a subtitle's timing",
			ReadOnlyHint:    false,
			DestructiveHint: ptr(true),
			OpenWorldHint:   ptr(false),
		},
		Description: "Fixes a subtitle that is out of time with the video, rewriting the file in " +
			"place. By default it aligns the subtitle to the audio track — right for one that " +
			"drifts. Pass 'shift_ms' instead when it is off by the same amount throughout: positive " +
			"delays the lines (they came too early), negative brings them forward (they came too " +
			"late). Only external subtitle files can be changed, not tracks embedded in the video. " +
			"Takes a movie ('radarr_id') or an episode ('episode_id') and the subtitle's 'language'. " +
			"Asks the user first, naming the file.",
	}, handleBazarrSync)

	sdk.AddTool(s, &sdk.Tool{
		Name: "bazarr_language_profile_set",
		Annotations: &sdk.ToolAnnotations{
			Title:           "Set which subtitles Bazarr wants",
			ReadOnlyHint:    false,
			DestructiveHint: ptr(false),
			OpenWorldHint:   ptr(false),
			IdempotentHint:  true,
		},
		Description: "Points a movie ('radarr_id') or a whole series ('series_id') at a language " +
			"profile — the setting that decides which subtitles Bazarr keeps searching for and " +
			"upgrading. This is how 'from now on I want Portuguese and English subtitles for this' " +
			"becomes permanent, where bazarr_subtitle_search only fetches something once. The " +
			"profiles, with their languages, are at homelab://bazarr/language-profiles; 'none' stops " +
			"Bazarr wanting anything. Downloads and deletes nothing, and does not start a search. " +
			"Asks the user first.",
	}, handleBazarrProfile)

	sdk.AddTool(s, &sdk.Tool{
		Name: "bazarr_providers_reset",
		Annotations: &sdk.ToolAnnotations{
			Title:           "Clear subtitle provider throttles",
			ReadOnlyHint:    false,
			DestructiveHint: ptr(false),
			OpenWorldHint:   ptr(false),
			IdempotentHint:  true,
		},
		Description: "Clears every subtitle provider throttle — the Reset button on Bazarr's " +
			"Providers page. Use it when bazarr_system_health shows providers throttled and the " +
			"cause has gone away. A provider throttled for too many requests or a bad login will " +
			"usually be throttled again on its next query, and the result says which ones those " +
			"are. Asks the user first, listing each throttle.",
	}, handleBazarrProvidersReset)
}

// PROWLARR tools
func registerProwlarrTools(s *sdk.Server) {
	if !prowlarr.Configured() {
		return
	}

	base, err := prowlarr.BaseURL()
	if err != nil {
		log.Printf("prowlarr tools not registered: %v", err)
		return
	}

	mode := "read and write"
	if prowlarr.ReadOnly() {
		mode = "read-only (" + prowlarr.ReadOnlyEnv + " is set)"
	}
	log.Printf("prowlarr at %s: %s", base, mode)

	sdk.AddTool(s, &sdk.Tool{
		Name: "prowlarr_system_health",
		Annotations: &sdk.ToolAnnotations{
			Title:        "Prowlarr System Health",
			ReadOnlyHint: true,
		},
		Description: "Returns Prowlarr's version, uptime, its own failing health checks and how many " +
			"indexers are enabled and failing. Prowlarr is where Radarr's and Sonarr's indexers come " +
			"from, so this is the answer to 'the *arrs find nothing and everything looks fine': it " +
			"records indexers backed off after failures, apps it cannot push to and a FlareSolverr " +
			"proxy that stopped answering.",
	}, handleProwlarrHealth)

	sdk.AddTool(s, &sdk.Tool{
		Name: "prowlarr_indexer_status",
		Annotations: &sdk.ToolAnnotations{
			Title:        "Prowlarr Indexer Status",
			ReadOnlyHint: true,
		},
		Description: "Returns every indexer, failing first: enabled or not, whether Prowlarr has " +
			"backed it off after failures and for how long, its priority, sync profile and tags, and " +
			"its queries, failures, grabs and response time over the last 7 days. An indexer can be " +
			"enabled and have failed every query this week — Radarr and Sonarr still list it and just " +
			"get nothing — and the numbers are what show that. Pass 'term' for one indexer. The ids " +
			"are what the other prowlarr tools take.",
	}, handleProwlarrIndexers)

	sdk.AddTool(s, &sdk.Tool{
		Name: "prowlarr_indexer_test",
		Annotations: &sdk.ToolAnnotations{
			Title:         "Test Prowlarr indexers",
			ReadOnlyHint:  true,
			OpenWorldHint: ptr(true),
		},
		Description: "Has Prowlarr test one indexer (by id or name) or every enabled one right now " +
			"and says why each failure failed, in Prowlarr's words — an expired cookie, a Cloudflare " +
			"challenge, refused credentials, a site that moved. Changes nothing in Prowlarr, but it " +
			"does query the sites, so do not loop it.",
	}, handleProwlarrTest)

	sdk.AddTool(s, &sdk.Tool{
		Name: "prowlarr_applications",
		Annotations: &sdk.ToolAnnotations{
			Title:        "Prowlarr Applications",
			ReadOnlyHint: true,
		},
		Description: "Returns the apps Prowlarr pushes indexers into (Radarr, Sonarr…), each with its " +
			"sync level and tags and the indexers that actually reach it under Prowlarr's rules, plus " +
			"the sync profiles. This is where 'I added the indexer in Prowlarr and Radarr does not have " +
			"it' is answered: an app on Add Only never receives changes, and an app with tags only " +
			"receives indexers sharing one. Pass test=true to also check Prowlarr can reach each app.",
	}, handleProwlarrApps)

	sdk.AddTool(s, &sdk.Tool{
		Name: "prowlarr_search",
		Annotations: &sdk.ToolAnnotations{
			Title:         "Search the indexers",
			ReadOnlyHint:  true,
			OpenWorldHint: ptr(true),
		},
		Description: "Searches the indexers directly through Prowlarr and lists what exists, most " +
			"seeded first. It answers whether any release exists at all — if Prowlarr finds nothing, " +
			"no search from Radarr or Sonarr will either; if it finds plenty and they grabbed nothing, " +
			"the cause is on their side (quality profile, availability). Grabs nothing: a release " +
			"grabbed through Prowlarr is never imported, so radarr_movie_search / sonarr_series_search " +
			"are how to act on it. Pass kind='movie' or 'tv' to narrow categories.",
	}, handleProwlarrSearch)

	sdk.AddTool(s, &sdk.Tool{
		Name: "prowlarr_indexer_definitions",
		Annotations: &sdk.ToolAnnotations{
			Title:        "Find an indexer to add",
			ReadOnlyHint: true,
		},
		Description: "Searches the catalogue of sites Prowlarr can add, by name, and returns each " +
			"with its protocol, whether it needs an account, whether it sits behind Cloudflare, " +
			"whether it is already added, and the settings it takes — credentials among them for a " +
			"private site. The first step of prowlarr_indexer_add, which takes the 'definition' and " +
			"the setting names shown here.",
	}, handleProwlarrDefinitions)

	if prowlarr.ReadOnly() {
		return
	}

	sdk.AddTool(s, &sdk.Tool{
		Name: "prowlarr_indexer_update",
		Annotations: &sdk.ToolAnnotations{
			Title:           "Change a Prowlarr indexer",
			ReadOnlyHint:    false,
			DestructiveHint: ptr(false),
			OpenWorldHint:   ptr(false),
			IdempotentHint:  true,
		},
		Description: "Enables or disables an indexer, changes its priority (1-50, lower preferred) or " +
			"its sync profile — the setting that decides whether Radarr and Sonarr use it for RSS, " +
			"automatic search and interactive search. Only the fields passed change; credentials are " +
			"never touched. Prowlarr pushes the change to Full Sync apps at once; Add Only apps keep " +
			"their old copy, and the confirmation names which is which. Asks the user first.",
	}, handleProwlarrUpdate)

	sdk.AddTool(s, &sdk.Tool{
		Name: "prowlarr_indexer_add",
		Annotations: &sdk.ToolAnnotations{
			Title:           "Add an indexer to Prowlarr",
			ReadOnlyHint:    false,
			DestructiveHint: ptr(false),
			OpenWorldHint:   ptr(true),
		},
		Description: "Adds an indexer from one of Prowlarr's site definitions and lets Prowlarr push " +
			"it to the apps. Takes the 'definition' from prowlarr_indexer_definitions — call that " +
			"first, and do not guess — and, for a private site, its credentials in 'settings' by the " +
			"names that tool lists. Prowlarr tests the site before saving, so a refusal is the site's " +
			"own answer. Credentials are masked in the confirmation. Asks the user first.",
	}, handleProwlarrAdd)

	sdk.AddTool(s, &sdk.Tool{
		Name: "prowlarr_indexer_remove",
		Annotations: &sdk.ToolAnnotations{
			Title:           "Delete a Prowlarr indexer",
			ReadOnlyHint:    false,
			DestructiveHint: ptr(true),
			OpenWorldHint:   ptr(false),
		},
		Description: "Deletes an indexer from Prowlarr, its settings and credentials with it, and " +
			"from every app Prowlarr syncs it to — Add Only apps included. To stop using one while " +
			"keeping it, disable it with prowlarr_indexer_update instead. Asks the user first, " +
			"naming the apps it leaves.",
	}, handleProwlarrRemove)

	sdk.AddTool(s, &sdk.Tool{
		Name: "prowlarr_apps_sync",
		Annotations: &sdk.ToolAnnotations{
			Title:           "Sync indexers to the apps",
			ReadOnlyHint:    false,
			DestructiveHint: ptr(true),
			OpenWorldHint:   ptr(false),
			IdempotentHint:  true,
		},
		Description: "Prowlarr's Sync App Indexers button: pushes every indexer to Radarr, Sonarr and " +
			"the other apps now. The fix for an app that has drifted — an indexer deleted there by " +
			"hand, a new app that came up empty. On Full Sync apps it also removes indexers Prowlarr " +
			"no longer handles; force=true rewrites every synced indexer, overwriting edits made in " +
			"the apps. Asks the user first.",
	}, handleProwlarrSync)
}
