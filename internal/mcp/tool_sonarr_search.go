package mcp

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DeLucca990/homelab-mcp/internal/sonarr"
)

// SONARR SEARCH TOOL
//
// Sonarr's own Search button. It is a write because it ends in a grab: whatever
// the indexers return is downloaded onto a disk, so it goes through the same
// confirmation as adding.
//
// It exists because the library otherwise has a one-way door. Removing a
// download from the queue leaves the episode monitored and missing, and there is
// no path back: sonarr_series_add is refused with "This series has already been
// added", correctly, because adding is not what is being asked for.
//
// Unlike Radarr's, this one has a scale. The same tool searches one episode, one
// season or nine seasons, and those are three very different operations — so the
// scope is resolved before the confirmation, named in it, and hashed into the
// fingerprint. An approval for season 3 cannot execute against the whole series.

type sonarrSearchInput struct {
	SeriesID int `json:"series_id" jsonschema:"the series to search for, from sonarr_library_status. Sonarr's own 'id' field is what this expects; a TVDB id is accepted too and resolved, and the confirmation says which show it landed on"`

	Season *int `json:"season,omitempty" jsonschema:"restrict the search to one season. Omit it to search every monitored episode of the series, which on a long-running show is hundreds of grabs. Season 0 is Sonarr's specials"`

	EpisodeIDs []int `json:"episode_ids,omitempty" jsonschema:"restrict the search to specific episodes, using the episode ids from sonarr_missing_episodes. These are episode ids, not episode numbers, and they must belong to this series. Takes precedence over 'season'"`
}

func handleSonarrSearch(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in sonarrSearchInput,
) (*sdk.CallToolResult, sonarr.SearchResult, error) {
	if in.SeriesID <= 0 {
		return nil, sonarr.SearchResult{}, fmt.Errorf(
			"'series_id' is required — sonarr_library_status lists the series with their ids")
	}

	scope, err := sonarr.ResolveSearch(ctx, sonarr.SearchRequest{
		SeriesID:   in.SeriesID,
		Season:     in.Season,
		EpisodeIDs: in.EpisodeIDs,
	})
	if err != nil {
		return nil, sonarr.SearchResult{}, err
	}

	approved, pending, err := requireApproval(req, approval{
		message: sonarrSearchConfirmationMessage(scope, in.SeriesID),
		fingerprint: fingerprint(
			append([]string{"sonarr_series_search"}, scope.Fingerprint()...)...),
		refusal: "no search was started",
		subject: fmt.Sprintf("search sonarr for %s", scope.Describe()),
	})
	if !approved {
		return pending, sonarr.SearchResult{}, err
	}

	out, err := sonarr.Search(ctx, scope)
	if err != nil {
		return nil, sonarr.SearchResult{}, err
	}

	return &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.TextContent{Text: renderSonarrSearchResult(out)},
		},
	}, out, nil
}

func sonarrSearchConfirmationMessage(scope sonarr.SearchScope, requestedID int) string {
	var b strings.Builder
	s := scope.Series

	fmt.Fprintf(&b, "Search for releases now?\n\n")
	fmt.Fprintf(&b, "    %s (%d)   [series %d]\n", s.Title, s.Year, s.ID)
	// The number asked for was a TVDB id. Say so: it is the difference between
	// the show the caller meant and one that merely shares a number.
	if requestedID != s.ID {
		fmt.Fprintf(&b, "    (you gave %d, which is its TVDB id — Sonarr's id for it is %d)\n",
			requestedID, s.ID)
	}
	fmt.Fprintf(&b, "    searching:  %s\n", scope.Describe())
	fmt.Fprintf(&b, "    on disk:    %d of %d episodes\n", s.EpisodesOnDisk, s.EpisodesWanted)
	fmt.Fprintf(&b, "    missing in scope: %d\n", scope.MissingEpisodes)
	fmt.Fprintf(&b, "    monitored:  %s\n", yesNo(s.Monitored))

	switch {
	case scope.MissingEpisodes == 0:
		b.WriteString("\nNothing in that scope is missing, so this searches for UPGRADES — " +
			"a better release would replace what is on disk.\n")
	case len(scope.Episodes) == 0 && scope.Season == nil && scope.MissingEpisodes > 20:
		fmt.Fprintf(&b, "\nThis covers the whole series: %d episodes could be grabbed at once. "+
			"Pass 'season' to try one season first.\n", scope.MissingEpisodes)
	default:
		b.WriteString("\nAnything the indexers return will be grabbed and downloaded.\n")
	}

	return b.String()
}

func renderSonarrSearchResult(r sonarr.SearchResult) string {
	var b strings.Builder

	fmt.Fprintf(&b, "sonarr is searching for %s\n", r.Scope)
	fmt.Fprintf(&b, "%s command %d: %s\n", r.CommandName, r.CommandID, blank(r.CommandStatus))
	if r.MissingEpisodes > 0 {
		fmt.Fprintf(&b, "%d episode(s) in scope were missing when it started\n", r.MissingEpisodes)
	}
	if len(r.EpisodeIDs) > 0 {
		ids := make([]string, 0, len(r.EpisodeIDs))
		for _, id := range r.EpisodeIDs {
			ids = append(ids, strconv.Itoa(id))
		}
		fmt.Fprintf(&b, "episode ids: %s\n", strings.Join(ids, ", "))
	}

	// The command being queued is not the episodes being found. Anything else
	// would read as "downloaded".
	b.WriteString("\nthe search runs inside sonarr; sonarr_queue_status shows whether it " +
		"grabbed anything, and an empty queue a minute from now means the indexers " +
		"returned nothing\n")

	for _, w := range r.Warnings {
		fmt.Fprintf(&b, "\nwarning: %s\n", w)
	}

	return b.String()
}
