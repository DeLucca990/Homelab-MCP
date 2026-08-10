package mcp

import (
	"context"
	"fmt"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DeLucca990/homelab-mcp/internal/radarr"
)

// RADARR LOOKUP TOOL
//
// Searching TMDB through Radarr. Read-only, and the required first step of
// adding anything: radarr_movie_add takes a tmdb_id and nothing else
// identifies a film unambiguously — "Dune" is two films, and so is "The
// Thing".

type lookupInput struct {
	Term  string `json:"term" jsonschema:"what to search for: a title, optionally with a year, e.g. 'dune 2021'"`
	Limit int    `json:"limit,omitempty" jsonschema:"how many candidates to return; default 10, maximum 25"`
}

type lookupOutput struct {
	Term    string                `json:"term"`
	Results []radarr.LookupResult `json:"results"`
}

func handleRadarrLookup(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in lookupInput,
) (*sdk.CallToolResult, lookupOutput, error) {
	results, err := radarr.Lookup(ctx, in.Term, in.Limit)
	if err != nil {
		return nil, lookupOutput{}, err
	}

	out := lookupOutput{Term: in.Term, Results: results}

	return &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.TextContent{Text: renderLookup(out)},
		},
	}, out, nil
}

func renderLookup(out lookupOutput) string {
	var b strings.Builder

	if len(out.Results) == 0 {
		fmt.Fprintf(&b, "nothing found for %q\n", out.Term)
		return b.String()
	}

	cols := []column{
		{"TMDB", alignRight},
		{"TITLE", alignLeft},
		{"YEAR", alignRight},
		{"STATUS", alignLeft},
		{"STUDIO", alignLeft},
		{"IN LIBRARY", alignLeft},
	}

	rows := make([][]string, 0, len(out.Results))
	for _, r := range out.Results {
		inLibrary := "no"
		switch {
		case r.InLibrary && r.HasFile:
			inLibrary = "yes, downloaded"
		case r.InLibrary:
			inLibrary = "yes, no file"
		}

		rows = append(rows, []string{
			fmt.Sprintf("%d", r.TmdbID),
			r.Title,
			yearCell(r.Year),
			blank(r.Status),
			blank(r.Studio),
			inLibrary,
		})
	}

	b.WriteString(table(cols, rows))
	b.WriteString("\npass the TMDB id of the right one to radarr_movie_add\n")

	for _, r := range out.Results {
		switch {
		case r.Overview == "" && r.PosterURL == "":
			continue
		case r.Overview == "":
			fmt.Fprintf(&b, "\n%d\n", r.TmdbID)
		default:
			fmt.Fprintf(&b, "\n%d — %s\n", r.TmdbID, r.Overview)
		}
		if r.PosterURL != "" {
			fmt.Fprintf(&b, "     cover: %s\n", r.PosterURL)
		}
	}

	return b.String()
}
