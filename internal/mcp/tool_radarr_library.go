package mcp

import (
	"context"
	"fmt"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DeLucca990/homelab-mcp/internal/radarr"
	"github.com/DeLucca990/homelab-mcp/internal/system"
)

// RADARR LIBRARY TOOL
//
// What Radarr is monitoring and what it has actually got. Read-only.

type libraryInput struct {
	Term          string `json:"term,omitempty" jsonschema:"case-insensitive substring of the title, to ask about one film rather than the whole library"`
	OnlyMissing   bool   `json:"only_missing,omitempty" jsonschema:"if true, returns only movies that are monitored and released but have no file — what Radarr owes you"`
	OnlyMonitored bool   `json:"only_monitored,omitempty" jsonschema:"if true, hides movies Radarr is not tracking"`
	Limit         int    `json:"limit,omitempty" jsonschema:"how many movies to return; default 25, maximum 200. The counts always describe the whole library, whatever this is set to"`
}

func handleRadarrLibrary(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in libraryInput,
) (*sdk.CallToolResult, radarr.Library, error) {
	lib, err := radarr.GetLibrary(ctx, radarr.MovieFilter{
		Term:          in.Term,
		OnlyMissing:   in.OnlyMissing,
		OnlyMonitored: in.OnlyMonitored,
		Limit:         in.Limit,
	})
	if err != nil {
		return nil, radarr.Library{}, err
	}
	return &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.TextContent{Text: renderLibrary(lib)},
		},
	}, lib, nil
}

func renderLibrary(lib radarr.Library) string {
	var b strings.Builder

	if lib.TotalCount == 0 {
		return "no movies in the radarr library yet\n"
	}

	if len(lib.Movies) > 0 {
		cols := []column{
			{"MOVIE", alignLeft},
			{"YEAR", alignRight},
			{"STATE", alignLeft},
			{"MONITORED", alignLeft},
			{"QUALITY", alignLeft},
			{"SIZE", alignRight},
			{"TMDB", alignRight},
		}

		rows := make([][]string, 0, len(lib.Movies))
		for _, m := range lib.Movies {
			rows = append(rows, []string{
				m.Title,
				yearCell(m.Year),
				movieStateCell(m),
				yesNo(m.Monitored),
				blank(m.Quality),
				sizeCell(m.SizeBytes),
				fmt.Sprintf("%d", m.TmdbID),
			})
		}
		b.WriteString(table(cols, rows))
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, "%d movies: %d downloaded (%s), %d monitored, %d missing, %d not released yet\n",
		lib.TotalCount, lib.DownloadedCount, system.CompactBytes(lib.SizeOnDiskBytes),
		lib.MonitoredCount, lib.MissingCount, lib.UnreleasedCount)

	if lib.MatchedCount != lib.TotalCount {
		fmt.Fprintf(&b, "(%d matched the filter, %d shown)\n", lib.MatchedCount, lib.ShownCount)
	}

	for _, w := range lib.Warnings {
		fmt.Fprintf(&b, "\nwarning: %s\n", w)
	}

	return b.String()
}

func movieStateCell(m radarr.Movie) string {
	switch {
	case m.HasFile:
		return "downloaded"
	case m.Missing:
		return "MISSING"
	case m.Monitored:
		return "not out yet"
	default:
		return "not tracked"
	}
}

func yearCell(y int) string {
	if y == 0 {
		return "-"
	}
	return fmt.Sprintf("%d", y)
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}
