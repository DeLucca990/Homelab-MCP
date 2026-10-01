package mcp

import (
	"context"
	"fmt"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DeLucca990/homelab-mcp/internal/bazarr"
)

// BAZARR WANTED TOOL
//
// Bazarr's Wanted list: every subtitle a language profile asks for and the
// disk does not have. Read-only.

type bazarrWantedInput struct {
	Kind  string `json:"kind,omitempty" jsonschema:"'movies', 'episodes' or 'all' (the default)"`
	Limit int    `json:"limit,omitempty" jsonschema:"how many of each to list, most recently added first; default 25, maximum 200. The totals always cover the whole list"`
}

func handleBazarrWanted(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in bazarrWantedInput,
) (*sdk.CallToolResult, bazarr.Wanted, error) {
	w, err := bazarr.GetWanted(ctx, strings.ToLower(strings.TrimSpace(in.Kind)), in.Limit)
	if err != nil {
		return nil, bazarr.Wanted{}, err
	}
	return &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.TextContent{Text: renderBazarrWanted(w)},
		},
	}, w, nil
}

func renderBazarrWanted(w bazarr.Wanted) string {
	var b strings.Builder

	fmt.Fprintf(&b, "wanted: %d movie(s) and %d episode(s) missing a subtitle\n",
		w.MovieTotal, w.EpisodeTotal)

	if len(w.Movies) > 0 {
		b.WriteString("\n")
		cols := []column{
			{"MOVIE", alignLeft},
			{"RADARR_ID", alignRight},
			{"MISSING", alignLeft},
		}
		rows := make([][]string, 0, len(w.Movies))
		for _, m := range w.Movies {
			rows = append(rows, []string{m.Title, fmt.Sprintf("%d", m.RadarrID), languageCodes(m.Missing)})
		}
		b.WriteString(table(cols, rows))
	}

	if len(w.Episodes) > 0 {
		b.WriteString("\n")
		cols := []column{
			{"SERIES", alignLeft},
			{"EP", alignLeft},
			{"EPISODE_ID", alignRight},
			{"MISSING", alignLeft},
		}
		rows := make([][]string, 0, len(w.Episodes))
		for _, e := range w.Episodes {
			rows = append(rows, []string{
				e.SeriesTitle, e.Number, fmt.Sprintf("%d", e.EpisodeID), languageCodes(e.Missing),
			})
		}
		b.WriteString(table(cols, rows))
	}

	fmt.Fprintf(&b, "\nnote: %s\n", w.Note)

	for _, warn := range w.Warnings {
		fmt.Fprintf(&b, "\nwarning: %s\n", warn)
	}

	return b.String()
}
