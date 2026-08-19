package mcp

import (
	"context"
	"fmt"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DeLucca990/homelab-mcp/internal/sonarr"
)

// SONARR MISSING EPISODES TOOL
//
// The tool with no Radarr counterpart, because a movie has nothing below it. A
// series is missing three episodes out of ninety and the series view stops
// there; this is which three, when they aired, and whether anything has ever
// gone looking for them. Read-only.

type sonarrMissingInput struct {
	SeriesID int `json:"series_id,omitempty" jsonschema:"restrict to one series, from sonarr_library_status. Omit it to ask about the whole library, which answers with the most recently aired missing episodes first"`
	Limit    int `json:"limit,omitempty" jsonschema:"how many episodes to return; default 25, maximum 200. The total is always reported, whatever this is set to"`
}

func handleSonarrMissing(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in sonarrMissingInput,
) (*sdk.CallToolResult, sonarr.Missing, error) {
	missing, err := sonarr.GetMissing(ctx, sonarr.MissingFilter{
		SeriesID: in.SeriesID,
		Limit:    in.Limit,
	})
	if err != nil {
		return nil, sonarr.Missing{}, err
	}
	return &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.TextContent{Text: renderSonarrMissing(missing)},
		},
	}, missing, nil
}

func renderSonarrMissing(m sonarr.Missing) string {
	var b strings.Builder

	if m.TotalCount == 0 {
		fmt.Fprintf(&b, "nothing is missing in %s — every monitored episode that has aired "+
			"is on disk\n", m.Scope)
		return b.String()
	}

	cols := []column{
		{"SERIES", alignLeft},
		{"EPISODE", alignLeft},
		{"TITLE", alignLeft},
		{"AIRED", alignRight},
		{"LAST SEARCH", alignRight},
		{"EPISODE ID", alignRight},
	}

	rows := make([][]string, 0, len(m.Episodes))
	for _, e := range m.Episodes {
		rows = append(rows, []string{
			blank(e.Series),
			e.Code,
			blank(e.Title),
			compactDuration(e.AiredSecondsAgo) + " ago",
			searchAgeCell(e.LastSearchSecondsAgo),
			fmt.Sprintf("%d", e.ID),
		})
	}
	b.WriteString(table(cols, rows))

	fmt.Fprintf(&b, "\n%d missing in %s", m.TotalCount, m.Scope)
	if m.SeriesCount > 1 {
		fmt.Fprintf(&b, ", across %d series", m.SeriesCount)
	}
	if m.ShownCount < m.TotalCount {
		fmt.Fprintf(&b, " (%d shown)", m.ShownCount)
	}
	b.WriteString("\n")

	b.WriteString("\nsonarr_series_search starts a search for one of these — by series, " +
		"by season, or by the episode ids above\n")

	for _, w := range m.Warnings {
		fmt.Fprintf(&b, "\nwarning: %s\n", w)
	}

	return b.String()
}

func searchAgeCell(seconds uint64) string {
	if seconds == 0 {
		return "never"
	}
	return compactDuration(seconds) + " ago"
}
