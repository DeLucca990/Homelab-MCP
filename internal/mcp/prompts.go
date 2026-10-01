package mcp

import (
	"context"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DeLucca990/homelab-mcp/internal/bazarr"
	"github.com/DeLucca990/homelab-mcp/internal/jellyfin"
	"github.com/DeLucca990/homelab-mcp/internal/prowlarr"
	"github.com/DeLucca990/homelab-mcp/internal/radarr"
	"github.com/DeLucca990/homelab-mcp/internal/sonarr"
)

// Prompts are the procedures, where tools are the facts.
//
// The order to check things in, and the traps at each step, are knowledge this
// server already has — spread across twenty-nine tool descriptions, where a
// model meets it one tool at a time and has to reassemble the sequence itself.
// A prompt is where that sequence can be stated once, so the same question gets
// the same route every time rather than whichever tool the description happened
// to remind the model of.
//
// They are registered on the same conditions the tools are: a procedure whose
// third step is a tool this server never created is worse than no procedure,
// because it reads as authoritative.

func registerPrompts(s *sdk.Server) {
	s.AddPrompt(&sdk.Prompt{
		Name:        "triage",
		Title:       "Triage this server",
		Description: "Find out what, if anything, is wrong with this machine — the whole tour, worst first, in the order that rules causes out fastest.",
		Arguments: []*sdk.PromptArgument{{
			Name:        "symptom",
			Title:       "What you are seeing",
			Description: "Optional. What made you look — 'jellyfin keeps buffering', 'the whole box is slow'. The triage then works out whether the server explains it, rather than only listing what is broken.",
		}},
	}, handleTriagePrompt)

	if radarr.Configured() || sonarr.Configured() {
		s.AddPrompt(&sdk.Prompt{
			Name:        "why-no-download",
			Title:       "Why has this not downloaded?",
			Description: "Work out why something has not arrived: not released, not monitored, stalled, stuck on import, or an indexer that is quietly refusing to answer.",
			Arguments: []*sdk.PromptArgument{{
				Name:        "title",
				Title:       "The film or series",
				Description: "Optional. The title in question. Without it the queue is examined as a whole, which is the right shape for 'nothing has downloaded all week'.",
			}},
		}, handleWhyNoDownloadPrompt)
	}
}

func handleTriagePrompt(ctx context.Context, req *sdk.GetPromptRequest) (*sdk.GetPromptResult, error) {
	var b strings.Builder

	if symptom := strings.TrimSpace(req.Params.Arguments["symptom"]); symptom != "" {
		b.WriteString("I am seeing this on my home server: ")
		b.WriteString(symptom)
		b.WriteString("\n\n")
		b.WriteString("Work out whether the machine explains it, and tell me what to do about it.\n\n")
	} else {
		b.WriteString("Check my home server and tell me what, if anything, needs attention.\n\n")
	}

	b.WriteString(`Start with homelab_overview: one call, every area at once, and on a healthy
server it is the entire answer. Go down into an area only where it flags one,
using the tool it names.

What to look for in each, beyond what the tool says on its own:

- disk (system_disk_usage) — a mount over 90% is the finding. Inode exhaustion
  is the one to say out loud if it appears: that filesystem will fail with "no
  space left on device" while df still shows free bytes.
- memory (system_memory_stats) — read available_bytes and used_percent, never
  free_bytes. Linux keeps idle RAM occupied with disk cache, so a low free is
  normal and says nothing. Swap in use at all means the machine has already run
  out once.
- services (system_service_status) — read the restart count next to the state. A
  unit that reads active with a climbing restart count is crash-looping, not
  running, and no point-in-time check will ever say so.
- docker (docker_container_status, then docker_container_logs on whatever it
  flags) — exit code 137 is the OOM killer rather than an application error, and
  a container restarting every few seconds shows "Up 4 seconds". Most images log
  to stdout, so there is no log file inside the container to go looking for.`)

	if radarr.Configured() || sonarr.Configured() {
		b.WriteString("\n- ")
		b.WriteString(arrNames())
		b.WriteString(" — check ")
		b.WriteString(arrTools("_system_health"))
		b.WriteString(` first and the queue second. The container can be up and healthy while every
  indexer it has is refusing to answer, which looks exactly like there being
  nothing to download.`)
	}

	if prowlarr.Configured() {
		b.WriteString(`
- prowlarr (prowlarr_indexer_status) — an indexer can be enabled and have failed
  every query this week; Radarr and Sonarr still list it and simply get nothing.
  The failing column and the 7-day numbers are the finding, not the enabled flag.`)
	}

	if bazarr.Configured() {
		b.WriteString(`
- bazarr (bazarr_system_health) — the provider table is the finding. A throttled
  provider is skipped silently until its timer runs out, so with all of them
  throttled Bazarr is up, healthy and searching nothing. An "unknown" Sonarr or
  Radarr version means it has lost that service and is working from a stale
  copy of its library.`)
	}

	b.WriteString(`

Report worst first, and only what is wrong. If nothing is, say so in one line
with the numbers behind it rather than narrating every check that passed.

Change nothing while you look. Every tool named here is read-only; anything that
would alter this server asks me first, and I would rather decide once you know
the cause.`)

	return &sdk.GetPromptResult{
		Description: "A read-only sweep of the whole server, worst first.",
		Messages: []*sdk.PromptMessage{{
			Role:    "user",
			Content: &sdk.TextContent{Text: b.String()},
		}},
	}, nil
}

func handleWhyNoDownloadPrompt(ctx context.Context, req *sdk.GetPromptRequest) (*sdk.GetPromptResult, error) {
	var b strings.Builder

	if title := strings.TrimSpace(req.Params.Arguments["title"]); title != "" {
		b.WriteString("Work out why ")
		b.WriteString(title)
		b.WriteString(" has not downloaded.")
	} else {
		b.WriteString("Work out why nothing is arriving from ")
		b.WriteString(arrNames())
		b.WriteString(".")
	}

	b.WriteString(`

The order matters, because each step rules out a different cause and the last
two are indistinguishable from the outside:

1. The library first (`)
	b.WriteString(arrTools("_library_status"))
	b.WriteString(`, with 'term' set) — is it
   even there, and is it monitored? A film that has not been released yet is not
   late, and it is counted apart from one that is genuinely missing. A series
   reading "monitored" can still have the season in question unmonitored, and
   Sonarr will not search for an episode it is not monitoring — the per-season
   breakdown is what shows that.

2. The queue (`)
	b.WriteString(arrTools("_queue_status"))
	b.WriteString(`) — if it is downloading, the answer
   is here, and a progress bar hides both of the states that matter: stalled,
   where nothing is arriving at all, and finished-but-not-imported, where the
   file is on disk and the library still says missing. They need different
   fixes. One Sonarr season pack occupies one queue row per episode it holds.

3. Health (`)
	b.WriteString(arrTools("_system_health"))
	b.WriteString(`) — if the queue is empty. This is the
   answer to "nothing was found": the service can be up, healthy and idle while
   every indexer it has is refusing to answer or its download client is
   unreachable, and it records exactly that here. test_download_clients=true
   checks the client too.

   If health is clean, ask the indexers yourself (`)
	b.WriteString(arrTools("_releases"))
	b.WriteString(`):
   every release they return is listed with the reason it was rejected, which
   is usually the whole answer — a quality profile that wants nothing on offer,
   a size limit, a language rule. A download stuck on import is the other
   case: `)
	b.WriteString(arrTools("_import_candidates"))
	b.WriteString(` shows the files and the objection.`)

	if prowlarr.Configured() {
		b.WriteString(`

   The indexers behind them live in Prowlarr: prowlarr_indexer_status says
   which are failing, and prowlarr_search asks them directly whether any
   release exists at all — nothing there means no search from the *arr will
   find anything either. prowlarr_applications explains an indexer that works
   in Prowlarr and is missing from the *arr (Add Only, or a tag mismatch).`)
	}

	if jellyfin.Configured() {
		b.WriteString(`

   If the library already says downloaded and it still cannot be watched, the
   problem is on the Jellyfin side: jellyfin_find_item says whether Jellyfin
   has it at all, and homelab://jellyfin/libraries whether the folder it was
   imported to is one Jellyfin reads. A scan (jellyfin_library_scan) fixes the
   first; only a path change fixes the second.`)
	}

	if sonarr.Configured() {
		b.WriteString(`

4. For a series, sonarr_missing_episodes — which episodes are actually owed,
   when they aired, and whether anything has ever searched for them.`)
	}

	b.WriteString(`

Tell me what you found before acting on it. If the fix is a search, a monitor
change or clearing a stuck queue item, name which and why — those tools ask for
confirmation anyway, and I would rather approve something once you know the
cause than watch a search get fired at a problem that was never about searching.`)

	return &sdk.GetPromptResult{
		Description: "Diagnose a download that has not arrived, cause by cause.",
		Messages: []*sdk.PromptMessage{{
			Role:    "user",
			Content: &sdk.TextContent{Text: b.String()},
		}},
	}, nil
}

// arrNames names the services that actually exist on this install, so a
// procedure never sends the model at a tool family that was never registered.
func arrNames() string {
	switch {
	case radarr.Configured() && sonarr.Configured():
		return "Radarr and Sonarr"
	case radarr.Configured():
		return "Radarr"
	case sonarr.Configured():
		return "Sonarr"
	default:
		return "the download services"
	}
}

// arrTools expands a tool suffix into the names that exist here:
// "_queue_status" becomes "radarr_queue_status / sonarr_queue_status".
func arrTools(suffix string) string {
	var names []string
	if radarr.Configured() {
		names = append(names, "radarr"+suffix)
	}
	if sonarr.Configured() {
		names = append(names, "sonarr"+suffix)
	}
	return strings.Join(names, " / ")
}
