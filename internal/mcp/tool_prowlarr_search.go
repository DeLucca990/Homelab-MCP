package mcp

import (
	"context"
	"fmt"
	"slices"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DeLucca990/homelab-mcp/internal/prowlarr"
)

// PROWLARR SEARCH TOOL
//
// A search straight across the indexers: does any release of this exist at
// all? It grabs nothing — see the package notes for why.

type prowlarrSearchInput struct {
	Query    string   `json:"query" jsonschema:"the title as a release would name it, e.g. 'Dune 2021' or 'Severance S02'"`
	Kind     string   `json:"kind,omitempty" jsonschema:"'movie' or 'tv' to limit to those categories; 'any' (the default) searches everything"`
	Indexers []string `json:"indexers,omitempty" jsonschema:"only these indexers, by id or name; omitted, every enabled one"`
	Limit    int      `json:"limit,omitempty" jsonschema:"how many releases to list, most seeded first; default 25, maximum 100"`
}

func handleProwlarrSearch(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in prowlarrSearchInput,
) (*sdk.CallToolResult, prowlarr.SearchResult, error) {
	out, err := prowlarr.Search(ctx, prowlarr.SearchRequest{
		Query:    in.Query,
		Kind:     in.Kind,
		Indexers: in.Indexers,
		Limit:    in.Limit,
	})
	if err != nil {
		return nil, prowlarr.SearchResult{}, err
	}
	return &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.TextContent{Text: renderProwlarrSearch(out)},
		},
	}, out, nil
}

func renderProwlarrSearch(r prowlarr.SearchResult) string {
	var b strings.Builder

	fmt.Fprintf(&b, "%d release(s) for %q", r.TotalCount, r.Query)
	if r.Kind != "any" {
		fmt.Fprintf(&b, " (%s)", r.Kind)
	}
	if len(r.Indexers) > 0 {
		fmt.Fprintf(&b, " on %s", strings.Join(r.Indexers, ", "))
	}
	b.WriteString("\n")

	if len(r.Releases) > 0 {
		b.WriteString("\n")
		cols := []column{
			{"TITLE", alignLeft},
			{"SIZE", alignRight},
			{"SEED", alignRight},
			{"AGE", alignRight},
			{"INDEXER", alignLeft},
		}
		rows := make([][]string, 0, len(r.Releases))
		for _, rel := range r.Releases {
			seed := "-"
			if rel.Seeders != nil {
				seed = fmt.Sprintf("%d", *rel.Seeders)
			}
			title := rel.Title
			if len(title) > 80 {
				title = title[:80] + "…"
			}
			rows = append(rows, []string{
				title,
				sizeCell(rel.SizeBytes),
				seed,
				compactDuration(uint64(rel.AgeHours) * 3600),
				rel.Indexer,
			})
		}
		b.WriteString(table(cols, rows))
		if r.ShownCount < r.TotalCount {
			fmt.Fprintf(&b, "\n%d shown of %d\n", r.ShownCount, r.TotalCount)
		}
	}

	if len(r.ByIndexer) > 0 {
		parts := make([]string, 0, len(r.ByIndexer))
		for name, n := range r.ByIndexer {
			parts = append(parts, fmt.Sprintf("%s %d", name, n))
		}
		slices.Sort(parts)
		fmt.Fprintf(&b, "\nper indexer: %s\n", strings.Join(parts, ", "))
	}

	fmt.Fprintf(&b, "\nnote: %s\n", r.Note)

	for _, w := range r.Warnings {
		fmt.Fprintf(&b, "\nwarning: %s\n", w)
	}

	return b.String()
}
