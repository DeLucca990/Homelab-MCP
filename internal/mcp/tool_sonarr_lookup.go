package mcp

import (
	"context"
	"fmt"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DeLucca990/homelab-mcp/internal/sonarr"
)

// SONARR LOOKUP TOOL
//
// Searching TheTVDB through Sonarr. Read-only, and the required first step of
// adding anything: sonarr_series_add takes a tvdb_id and nothing else
// identifies a show unambiguously — "The Office" is at least four series, and
// remakes share titles with the originals they remade.

type sonarrLookupInput struct {
	Term  string `json:"term" jsonschema:"what to search for: a series title, optionally with a year, e.g. 'the office 2005'"`
	Limit int    `json:"limit,omitempty" jsonschema:"how many candidates to return; default 10, maximum 25"`
}

type sonarrLookupOutput struct {
	Term    string                `json:"term"`
	Results []sonarr.LookupResult `json:"results"`
}

func handleSonarrLookup(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in sonarrLookupInput,
) (*sdk.CallToolResult, sonarrLookupOutput, error) {
	results, err := sonarr.Lookup(ctx, in.Term, in.Limit)
	if err != nil {
		return nil, sonarrLookupOutput{}, err
	}

	out := sonarrLookupOutput{Term: in.Term, Results: results}

	return &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.TextContent{Text: renderSonarrLookup(out)},
		},
	}, out, nil
}

func renderSonarrLookup(out sonarrLookupOutput) string {
	var b strings.Builder

	if len(out.Results) == 0 {
		fmt.Fprintf(&b, "nothing found for %q\n", out.Term)
		return b.String()
	}

	cols := []column{
		{"TVDB", alignRight},
		{"TITLE", alignLeft},
		{"YEAR", alignRight},
		{"STATUS", alignLeft},
		{"SEASONS", alignRight},
		{"NETWORK", alignLeft},
		{"IN LIBRARY", alignLeft},
	}

	rows := make([][]string, 0, len(out.Results))
	for _, r := range out.Results {
		inLibrary := "no"
		if r.InLibrary {
			inLibrary = fmt.Sprintf("yes, series %d", r.SeriesID)
		}

		seasons := "-"
		if r.SeasonCount > 0 {
			seasons = fmt.Sprintf("%d", r.SeasonCount)
		}

		rows = append(rows, []string{
			fmt.Sprintf("%d", r.TvdbID),
			r.Title,
			yearCell(r.Year),
			blank(r.Status),
			seasons,
			blank(r.Network),
			inLibrary,
		})
	}

	b.WriteString(table(cols, rows))
	b.WriteString("\npass the TVDB id of the right one to sonarr_series_add\n")

	for _, r := range out.Results {
		switch {
		case r.Overview == "" && r.PosterURL == "":
			continue
		case r.Overview == "":
			fmt.Fprintf(&b, "\n%d\n", r.TvdbID)
		default:
			fmt.Fprintf(&b, "\n%d — %s\n", r.TvdbID, r.Overview)
		}
		if r.PosterURL != "" {
			fmt.Fprintf(&b, "     cover: %s\n", r.PosterURL)
		}
	}

	return b.String()
}
