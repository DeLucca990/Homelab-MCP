package mcp

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DeLucca990/homelab-mcp/internal/radarr"
)

// RADARR SEARCH TOOL
//
// The Search button of Radarr's own UI. It is a write because it ends in a
// grab: whatever the indexers return is downloaded onto a disk, so it goes
// through the same confirmation as adding.
//
// It exists because the library otherwise has a one-way door. Removing a
// download from the queue leaves the movie monitored and missing, and there is
// no path back: radarr_movie_add is refused with "This movie has already been
// added", correctly, because adding is not what is being asked for.

type searchInput struct {
	MovieID int `json:"movie_id" jsonschema:"the movie to search for, from radarr_library_status. Radarr's own 'id' field is what this expects; a TMDB id is accepted too and resolved, and the confirmation says which film it landed on"`
}

func handleRadarrSearch(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in searchInput,
) (*sdk.CallToolResult, radarr.SearchResult, error) {
	if in.MovieID <= 0 {
		return nil, radarr.SearchResult{}, fmt.Errorf(
			"'movie_id' is required — radarr_library_status lists the movies with their ids")
	}

	// Resolved on both passes, so the user approves a film rather than a number
	// and the fingerprint covers what that number currently means.
	movie, err := radarr.GetMovie(ctx, in.MovieID)
	if err != nil {
		return nil, radarr.SearchResult{}, err
	}

	approved, pending, err := requireApproval(req, approval{
		message: searchConfirmationMessage(movie, in.MovieID),
		fingerprint: fingerprint(
			"radarr_movie_search",
			strconv.Itoa(movie.ID),
			movie.Title,
			strconv.Itoa(movie.Year),
			strconv.FormatBool(movie.HasFile),
		),
		refusal: "no search was started",
		subject: fmt.Sprintf("search radarr for %s (%d)", movie.Title, movie.Year),
	})
	if !approved {
		return pending, radarr.SearchResult{}, err
	}

	out, err := radarr.Search(ctx, movie)
	if err != nil {
		return nil, radarr.SearchResult{}, err
	}

	return &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.TextContent{Text: renderSearchResult(out)},
		},
	}, out, nil
}

func searchConfirmationMessage(m radarr.Movie, requestedID int) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Search for a release of this movie now?\n\n")
	fmt.Fprintf(&b, "    %s (%d)   [movie %d]\n", m.Title, m.Year, m.ID)
	if requestedID != m.ID {
		fmt.Fprintf(&b, "    (you gave %d, which is its TMDB id — Radarr's id for it is %d)\n",
			requestedID, m.ID)
	}
	fmt.Fprintf(&b, "    state: %s\n", movieStateCell(m))
	fmt.Fprintf(&b, "    monitored: %s\n", yesNo(m.Monitored))

	if m.HasFile {
		fmt.Fprintf(&b, "\nThis movie is ALREADY downloaded (%s). The search looks for an "+
			"upgrade, and a better release would replace the file on disk.\n", blank(m.Quality))
	} else {
		b.WriteString("\nAnything the indexers return will be grabbed and downloaded.\n")
	}

	return b.String()
}

func renderSearchResult(r radarr.SearchResult) string {
	var b strings.Builder

	fmt.Fprintf(&b, "radarr is searching for %s (%d)\n", r.Title, r.Year)
	fmt.Fprintf(&b, "command %d: %s\n", r.CommandID, blank(r.CommandStatus))

	b.WriteString("\nthe search runs inside radarr; radarr_queue_status shows whether it " +
		"grabbed anything, and an empty queue a minute from now means the indexers " +
		"returned nothing\n")

	for _, w := range r.Warnings {
		fmt.Fprintf(&b, "\nwarning: %s\n", w)
	}

	return b.String()
}
