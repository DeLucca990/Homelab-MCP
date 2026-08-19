package mcp

import (
	"context"
	"fmt"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DeLucca990/homelab-mcp/internal/radarr"
)

// RADARR ADD TOOL
//
// Adding a movie writes to the library, spends disk and usually starts a
// download, so it goes through the same confirmation as the container actions:
//
//  1. Human confirmation — the resolved plan (which film, which profile, which
//     folder) is what the user reads, not the tmdb id they would have to look
//     up to check.
//  2. Fingerprint — the plan is resolved again on the retry and re-hashed, so
//     an approval cannot be carried over to a different film, a different
//     quality profile or a different disk.
//
// There is no allowlist layer here, and there is nothing for one to protect:
// the reachable set is "movies", the operation is reversible from Radarr's own
// UI, and the API key is already the grant that decides whether any of this is
// possible at all.

type addInput struct {
	TmdbID int `json:"tmdb_id" jsonschema:"TMDB id of the movie, from radarr_movie_lookup. Look it up first — a title alone does not identify a film, and this is what the user will be asked to approve"`

	QualityProfile string `json:"quality_profile,omitempty" jsonschema:"quality profile by name or id. Omit it to get HD-1080p, which is the default resolution; name one only when the user asked for a different quality. If the install has no HD-1080p profile and more than one to choose from, the add is refused and the options are listed"`
	RootFolder     string `json:"root_folder,omitempty" jsonschema:"root folder path to store the movie in; may be omitted only when Radarr has exactly one, since this decides which disk fills up"`

	Monitored   *bool `json:"monitored,omitempty" jsonschema:"whether Radarr should try to get this movie; defaults to true. An unmonitored movie is added and then ignored"`
	SearchOnAdd *bool `json:"search_on_add,omitempty" jsonschema:"whether to start searching for a release immediately; defaults to true. False means it waits for Radarr's next scheduled search, which can be hours away"`

	MinimumAvailability string `json:"minimum_availability,omitempty" jsonschema:"how far into its release cycle a film must be before Radarr searches for it: tba, announced, inCinemas or released. Defaults to released, which is what avoids grabbing cam rips"`
}

func handleRadarrAdd(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in addInput,
) (*sdk.CallToolResult, radarr.AddResult, error) {
	if in.TmdbID <= 0 {
		return nil, radarr.AddResult{}, fmt.Errorf(
			"'tmdb_id' is required — run radarr_movie_lookup first and take it from the result")
	}

	plan, err := radarr.Plan(ctx, radarr.AddRequest{
		TmdbID:              in.TmdbID,
		QualityProfile:      in.QualityProfile,
		RootFolder:          in.RootFolder,
		Monitored:           boolOr(in.Monitored, true),
		SearchOnAdd:         boolOr(in.SearchOnAdd, true),
		MinimumAvailability: in.MinimumAvailability,
	})
	if err != nil {
		return nil, radarr.AddResult{}, err
	}

	approved, pending, err := requireApproval(req, approval{
		message:     addConfirmationMessage(plan),
		fingerprint: fingerprint(append([]string{"radarr_movie_add"}, plan.Fingerprint()...)...),
		refusal:     "movie not added",
		subject:     fmt.Sprintf("add %s (%d) to radarr", plan.Title, plan.Year),
	})
	if !approved {
		return pending, radarr.AddResult{}, err
	}

	out, err := radarr.Add(ctx, plan)
	if err != nil {
		return nil, radarr.AddResult{}, err
	}

	return &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.TextContent{Text: renderAddResult(out)},
		},
	}, out, nil
}

func addConfirmationMessage(p radarr.AddPlan) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Add this movie to Radarr?\n\n")
	fmt.Fprintf(&b, "    %s (%d)   [tmdb %d]\n", p.Title, p.Year, p.TmdbID)
	if p.PosterURL != "" {
		fmt.Fprintf(&b, "    cover: %s\n", p.PosterURL)
	}
	fmt.Fprintf(&b, "    quality profile: %s\n", p.QualityProfileName)
	fmt.Fprintf(&b, "    root folder:     %s\n", p.RootFolderPath)
	fmt.Fprintf(&b, "    monitored:       %s\n", yesNo(p.Monitored))
	fmt.Fprintf(&b, "    search now:      %s\n", yesNo(p.SearchOnAdd))
	fmt.Fprintf(&b, "    minimum availability: %s\n", p.MinimumAvailability)

	if p.SearchOnAdd {
		b.WriteString("\nRadarr will start looking for a release straight away, " +
			"and whatever it finds will be downloaded onto that folder.")
	}

	return b.String()
}

func renderAddResult(r radarr.AddResult) string {
	var b strings.Builder

	fmt.Fprintf(&b, "added %s (%d) to radarr as movie %d\n", r.Title, r.Year, r.MovieID)
	fmt.Fprintf(&b, "quality profile: %s\n", r.QualityProfile)
	if r.Path != "" {
		fmt.Fprintf(&b, "folder: %s\n", r.Path)
	}
	fmt.Fprintf(&b, "monitored: %s   search started: %s\n",
		yesNo(r.Monitored), yesNo(r.SearchStarted))

	if r.SearchStarted {
		b.WriteString("\nradarr_queue_status shows whether a release was grabbed; " +
			"a search that finds nothing leaves the queue empty.\n")
	}

	for _, w := range r.Warnings {
		fmt.Fprintf(&b, "\nwarning: %s\n", w)
	}

	return b.String()
}

func boolOr(v *bool, fallback bool) bool {
	if v == nil {
		return fallback
	}
	return *v
}
