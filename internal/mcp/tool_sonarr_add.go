package mcp

import (
	"context"
	"fmt"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DeLucca990/homelab-mcp/internal/sonarr"
)

// SONARR ADD TOOL
//
// Adding a series writes to the library, spends disk and usually starts a
// download, so it goes through the same confirmation as everything else that
// changes state:
//
//  1. Human confirmation — the resolved plan (which show, which profile, which
//     folder, how much of it) is what the user reads, not the tvdb id they would
//     have to look up to check.
//  2. Fingerprint — the plan is resolved again on the retry and re-hashed, so an
//     approval cannot be carried over to a different show, a different quality
//     profile or a different disk.
//
// The one that has no Radarr equivalent is 'monitor'. A film is one file and
// adding it is one download; a series with monitor=all is every episode of every
// season, which on a long-running show is hundreds of grabs and hundreds of
// gigabytes. That is why the season and episode counts are in the confirmation:
// the size of the operation is not visible in its arguments.

type sonarrAddInput struct {
	TvdbID int `json:"tvdb_id" jsonschema:"TVDB id of the series, from sonarr_series_lookup. Look it up first — a title alone does not identify a show, and this is what the user will be asked to approve"`

	QualityProfile string `json:"quality_profile,omitempty" jsonschema:"quality profile by name or id. Omit it to get HD-1080p, which is the default resolution; name one only when the user asked for a different quality. If the install has no HD-1080p profile and more than one to choose from, the add is refused and the options are listed"`
	RootFolder     string `json:"root_folder,omitempty" jsonschema:"root folder path to store the series in; may be omitted only when Sonarr has exactly one, since this decides which disk fills up"`

	Monitor string `json:"monitor,omitempty" jsonschema:"which episodes Sonarr should try to get: all, future, missing, existing, recent, pilot, firstSeason, lastSeason, latestSeason, monitorSpecials, unmonitorSpecials or none. Defaults to all, which on a long-running show means downloading the entire back catalogue — pass 'future' for a show the user only wants from now on, or 'firstSeason' to try one season first"`

	SearchOnAdd  *bool  `json:"search_on_add,omitempty" jsonschema:"whether to start searching for the monitored episodes immediately; defaults to true. False means it waits for Sonarr's next scheduled search, which can be hours away"`
	SeasonFolder *bool  `json:"season_folder,omitempty" jsonschema:"whether to store each season in its own subfolder; defaults to true, which is Sonarr's own default and what media servers expect"`
	SeriesType   string `json:"series_type,omitempty" jsonschema:"standard, daily or anime — it decides how Sonarr parses release names. Defaults to standard; anime matters for shows numbered by absolute episode, daily for news and talk shows dated rather than numbered"`
}

func handleSonarrAdd(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in sonarrAddInput,
) (*sdk.CallToolResult, sonarr.AddResult, error) {
	if in.TvdbID <= 0 {
		return nil, sonarr.AddResult{}, fmt.Errorf(
			"'tvdb_id' is required — run sonarr_series_lookup first and take it from the result")
	}

	// Resolved before the confirmation, and again on the retry: the user
	// approves a show and a destination, not a number.
	plan, err := sonarr.Plan(ctx, sonarr.AddRequest{
		TvdbID:         in.TvdbID,
		QualityProfile: in.QualityProfile,
		RootFolder:     in.RootFolder,
		Monitor:        in.Monitor,
		SearchOnAdd:    boolOr(in.SearchOnAdd, true),
		SeasonFolder:   boolOr(in.SeasonFolder, true),
		SeriesType:     in.SeriesType,
	})
	if err != nil {
		return nil, sonarr.AddResult{}, err
	}

	approved, pending, err := requireApproval(req, approval{
		message:     sonarrAddConfirmationMessage(plan),
		fingerprint: fingerprint(append([]string{"sonarr_series_add"}, plan.Fingerprint()...)...),
		refusal:     "series not added",
		subject:     fmt.Sprintf("add %s (%d) to sonarr", plan.Title, plan.Year),
	})
	if !approved {
		return pending, sonarr.AddResult{}, err
	}

	out, err := sonarr.Add(ctx, plan)
	if err != nil {
		return nil, sonarr.AddResult{}, err
	}

	return &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.TextContent{Text: renderSonarrAddResult(out)},
		},
	}, out, nil
}

// Names the show, the destination and the size of what is about to happen. This
// is the last point at which a person can notice that nine seasons of the wrong
// Office are about to be downloaded onto the wrong disk.
func sonarrAddConfirmationMessage(p sonarr.AddPlan) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Add this series to Sonarr?\n\n")
	fmt.Fprintf(&b, "    %s (%d)   [tvdb %d]\n", p.Title, p.Year, p.TvdbID)
	if p.Network != "" || p.Status != "" {
		fmt.Fprintf(&b, "    %s\n", strings.TrimSpace(strings.Join(
			nonEmptyOf(p.Network, p.Status), ", ")))
	}
	if p.PosterURL != "" {
		fmt.Fprintf(&b, "    cover: %s\n", p.PosterURL)
	}
	if p.SeasonCount > 0 {
		fmt.Fprintf(&b, "    size: %d seasons", p.SeasonCount)
		if p.EpisodeCount > 0 {
			fmt.Fprintf(&b, ", about %d episodes", p.EpisodeCount)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "    quality profile: %s\n", p.QualityProfileName)
	fmt.Fprintf(&b, "    root folder:     %s\n", p.RootFolderPath)
	fmt.Fprintf(&b, "    monitor:         %s (%s)\n", p.Monitor, monitorMeaning(p.Monitor))
	fmt.Fprintf(&b, "    search now:      %s\n", yesNo(p.SearchOnAdd))
	fmt.Fprintf(&b, "    season folders:  %s\n", yesNo(p.SeasonFolder))
	fmt.Fprintf(&b, "    series type:     %s\n", p.SeriesType)

	if p.SearchOnAdd && p.Monitor != "none" {
		b.WriteString("\nSonarr will start looking for releases straight away, and whatever it " +
			"finds will be downloaded onto that folder.")
		if p.Monitor == "all" && p.EpisodeCount > 50 {
			fmt.Fprintf(&b, " That is the whole back catalogue — about %d episodes.",
				p.EpisodeCount)
		}
		b.WriteString("\n")
	}

	return b.String()
}

// The enum value alone does not say what it does, and the difference between
// two of them is hundreds of downloads.
func monitorMeaning(monitor string) string {
	switch monitor {
	case "all":
		return "every episode, including the whole back catalogue"
	case "future":
		return "only episodes that have not aired yet"
	case "missing":
		return "episodes that have aired and are not on disk"
	case "existing":
		return "only episodes already on disk"
	case "recent":
		return "the most recent episode and anything from now on"
	case "pilot":
		return "the first episode only"
	case "firstSeason":
		return "season 1 only"
	case "lastSeason", "latestSeason":
		return "the most recent season only"
	case "monitorSpecials":
		return "specials as well"
	case "unmonitorSpecials":
		return "everything except specials"
	case "none":
		return "nothing — the series is added and then ignored"
	default:
		return monitor
	}
}

func renderSonarrAddResult(r sonarr.AddResult) string {
	var b strings.Builder

	fmt.Fprintf(&b, "added %s (%d) to sonarr as series %d\n", r.Title, r.Year, r.SeriesID)
	fmt.Fprintf(&b, "quality profile: %s   monitor: %s\n", r.QualityProfile, r.Monitor)
	if r.Path != "" {
		fmt.Fprintf(&b, "folder: %s\n", r.Path)
	}
	fmt.Fprintf(&b, "monitored: %s   search started: %s\n",
		yesNo(r.Monitored), yesNo(r.SearchStarted))

	if r.SearchStarted {
		b.WriteString("\nsonarr_queue_status shows what was grabbed; a search that finds " +
			"nothing leaves the queue empty, and sonarr_missing_episodes with series_id ")
		b.WriteString(fmt.Sprintf("%d", r.SeriesID))
		b.WriteString(" shows what is still outstanding.\n")
	}

	for _, w := range r.Warnings {
		fmt.Fprintf(&b, "\nwarning: %s\n", w)
	}

	return b.String()
}

// nonEmptyOf drops the values that were not reported, so a line built from two
// optional facts does not render as ", " when both are absent.
func nonEmptyOf(values ...string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			out = append(out, v)
		}
	}
	return out
}
