package mcp

import (
	"context"
	"fmt"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DeLucca990/homelab-mcp/internal/overview"
)

// OVERVIEW TOOL
//
// The one call behind "is everything alright?". Every other read tool answers
// one area; this answers the question people actually ask, which spans all of
// them and is usually answered "yes".
func handleOverview(
	ctx context.Context,
	req *sdk.CallToolRequest,
	_ emptyInput,
) (*sdk.CallToolResult, overview.Report, error) {
	report := overview.Get(ctx)

	return &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.TextContent{Text: renderOverview(report)},
		},
	}, report, nil
}

func renderOverview(r overview.Report) string {
	var b strings.Builder

	switch {
	case r.AttentionCount == 0 && r.FailedCount == 0:
		fmt.Fprintf(&b, "nothing needs attention (%d checks in %dms)\n\n",
			r.CheckedCount, r.ElapsedMs)
	case r.AttentionCount == 0:
		fmt.Fprintf(&b, "nothing needs attention, but %d of %d checks could not run (%dms)\n\n",
			r.FailedCount, r.CheckedCount, r.ElapsedMs)
	default:
		fmt.Fprintf(&b, "%d of %d checks need attention (%dms)\n\n",
			r.AttentionCount, r.CheckedCount, r.ElapsedMs)
	}

	cols := []column{
		{"", alignLeft},
		{"AREA", alignLeft},
		{"SUMMARY", alignLeft},
	}

	rows := make([][]string, 0, len(r.Sections))
	for _, s := range r.Sections {
		rows = append(rows, []string{statusMark(s.Status), s.Name, s.Headline})
	}
	b.WriteString(table(cols, rows))

	// Each section's own warnings, verbatim and attributed — the whole point of
	// the tool is that these are the same sentences the area's own tool gives,
	// so a reader never has to wonder whether the summary softened something.
	for _, s := range r.Sections {
		for _, w := range s.Warnings {
			fmt.Fprintf(&b, "\nwarning: %s: %s\n", s.Name, w)
		}
		if s.Error != "" {
			fmt.Fprintf(&b, "\nwarning: %s could not be checked: %s\n", s.Name, s.Error)
		}
	}

	// Named rather than implied: the summary is deliberately one line per area,
	// and the next call is the point of it.
	if next := nextSteps(r); next != "" {
		fmt.Fprintf(&b, "\nfor the detail behind those lines: %s\n", next)
	}

	return b.String()
}

// A glyph rather than the word, so the status column costs one character on
// every row of a table whose usual answer is "everything is fine".
func statusMark(status string) string {
	switch status {
	case overview.StatusAttention:
		return "!"
	case overview.StatusFailed:
		return "?"
	case overview.StatusAbsent:
		return "-"
	default:
		return "."
	}
}

func nextSteps(r overview.Report) string {
	var tools []string
	for _, s := range r.Sections {
		if s.Status == overview.StatusAttention && s.Tool != "" {
			tools = append(tools, s.Tool)
		}
	}
	return strings.Join(tools, ", ")
}
