package mcp

import (
	"context"
	"fmt"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DeLucca990/homelab-mcp/internal/sonarr"
	"github.com/DeLucca990/homelab-mcp/internal/system"
)

// SONARR LIBRARY TOOL
//
// What Sonarr is monitoring and how much of each show it has actually got.
// Read-only.

type sonarrLibraryInput struct {
	Term          string `json:"term,omitempty" jsonschema:"case-insensitive substring of the series title, to ask about one show rather than the whole library. When it selects exactly one series, the answer carries a per-season breakdown"`
	OnlyMissing   bool   `json:"only_missing,omitempty" jsonschema:"if true, returns only series that are monitored and short of at least one aired episode — what Sonarr owes you"`
	OnlyMonitored bool   `json:"only_monitored,omitempty" jsonschema:"if true, hides series Sonarr is not tracking"`
	Limit         int    `json:"limit,omitempty" jsonschema:"how many series to return; default 25, maximum 200. The counts always describe the whole library, whatever this is set to"`
}

func handleSonarrLibrary(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in sonarrLibraryInput,
) (*sdk.CallToolResult, sonarr.Library, error) {
	lib, err := sonarr.GetLibrary(ctx, sonarr.SeriesFilter{
		Term:          in.Term,
		OnlyMissing:   in.OnlyMissing,
		OnlyMonitored: in.OnlyMonitored,
		Limit:         in.Limit,
	})
	if err != nil {
		return nil, sonarr.Library{}, err
	}
	return &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.TextContent{Text: renderSonarrLibrary(lib)},
		},
	}, lib, nil
}

func renderSonarrLibrary(lib sonarr.Library) string {
	var b strings.Builder

	if lib.TotalCount == 0 {
		return "no series in the sonarr library yet\n"
	}

	if len(lib.Series) > 0 {
		cols := []column{
			{"SERIES", alignLeft},
			{"YEAR", alignRight},
			{"STATE", alignLeft},
			{"EPISODES", alignRight},
			{"MISSING", alignRight},
			{"MONITORED", alignLeft},
			{"SIZE", alignRight},
			{"ID", alignRight},
		}

		rows := make([][]string, 0, len(lib.Series))
		for _, s := range lib.Series {
			rows = append(rows, []string{
				s.Title,
				yearCell(s.Year),
				seriesStateCell(s),
				fmt.Sprintf("%d/%d", s.EpisodesOnDisk, s.EpisodesWanted),
				missingCell(s.EpisodesMissing),
				yesNo(s.Monitored),
				sizeCell(s.SizeBytes),
				fmt.Sprintf("%d", s.ID),
			})
		}
		b.WriteString(table(cols, rows))
		b.WriteString("\n")
	}

	if len(lib.Series) == 1 && len(lib.Series[0].Seasons) > 0 {
		b.WriteString(renderSeasons(lib.Series[0]))
	}

	fmt.Fprintf(&b, "%d series: %d monitored, %d continuing, %d complete, %d missing episodes\n",
		lib.TotalCount, lib.MonitoredCount, lib.ContinuingCount, lib.CompleteCount, lib.EpisodesMissing)
	fmt.Fprintf(&b, "%d episodes on disk (%s)\n",
		lib.EpisodesOnDisk, system.CompactBytes(lib.SizeOnDiskBytes))

	if lib.MatchedCount != lib.TotalCount {
		fmt.Fprintf(&b, "(%d matched the filter, %d shown)\n", lib.MatchedCount, lib.ShownCount)
	}

	for _, w := range lib.Warnings {
		fmt.Fprintf(&b, "\nwarning: %s\n", w)
	}

	return b.String()
}

func renderSeasons(s sonarr.Series) string {
	var b strings.Builder

	cols := []column{
		{"SEASON", alignLeft},
		{"EPISODES", alignRight},
		{"MISSING", alignRight},
		{"MONITORED", alignLeft},
		{"SIZE", alignRight},
	}

	rows := make([][]string, 0, len(s.Seasons))
	for _, season := range s.Seasons {
		name := fmt.Sprintf("%d", season.Number)
		if season.Number == 0 {
			name = "0 (specials)"
		}
		rows = append(rows, []string{
			name,
			fmt.Sprintf("%d/%d", season.EpisodesOnDisk, season.EpisodesWanted),
			missingCell(season.EpisodesMissing),
			yesNo(season.Monitored),
			sizeCell(season.SizeBytes),
		})
	}

	fmt.Fprintf(&b, "%s, by season:\n", s.Title)
	b.WriteString(table(cols, rows))
	b.WriteString("\n")

	return b.String()
}

func seriesStateCell(s sonarr.Series) string {
	switch {
	case s.Missing && s.EpisodesOnDisk == 0:
		return "NOTHING YET"
	case s.Missing:
		return "INCOMPLETE"
	case !s.Monitored:
		return "not tracked"
	case s.EpisodesWanted == 0:
		return "not out yet"
	case s.EpisodesTotal > s.EpisodesWanted:
		return "up to date"
	default:
		return "complete"
	}
}

func missingCell(n int) string {
	if n == 0 {
		return "-"
	}
	return fmt.Sprintf("%d", n)
}
