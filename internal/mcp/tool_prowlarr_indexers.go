package mcp

import (
	"context"
	"fmt"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DeLucca990/homelab-mcp/internal/prowlarr"
)

// PROWLARR INDEXER STATUS AND TEST TOOLS
//
// Which indexers work, by their numbers for the last week, and — when that is
// not enough — a live test that says why one does not. Neither changes
// anything in Prowlarr.

type prowlarrIndexersInput struct {
	Term string `json:"term,omitempty" jsonschema:"case-insensitive part of an indexer's name, to ask about one; the counts always cover all of them"`
}

func handleProwlarrIndexers(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in prowlarrIndexersInput,
) (*sdk.CallToolResult, prowlarr.Indexers, error) {
	out, err := prowlarr.GetIndexers(ctx, in.Term)
	if err != nil {
		return nil, prowlarr.Indexers{}, err
	}
	return &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.TextContent{Text: renderProwlarrIndexers(out)},
		},
	}, out, nil
}

func renderProwlarrIndexers(out prowlarr.Indexers) string {
	var b strings.Builder

	if len(out.Indexers) > 0 {
		cols := []column{
			{"ID", alignRight},
			{"INDEXER", alignLeft},
			{"STATE", alignLeft},
			{"PRIO", alignRight},
			{"TYPE", alignLeft},
			{"PROFILE", alignLeft},
			{"QUERIES", alignRight},
			{"FAILED", alignRight},
			{"GRABS", alignRight},
			{"AVG", alignRight},
			{"TAGS", alignLeft},
		}
		rows := make([][]string, 0, len(out.Indexers))
		for _, ix := range out.Indexers {
			avg := "-"
			if ix.AvgResponseMs > 0 {
				avg = fmt.Sprintf("%dms", ix.AvgResponseMs)
			}
			rows = append(rows, []string{
				fmt.Sprintf("%d", ix.ID),
				ix.Name,
				indexerStateCell(ix),
				fmt.Sprintf("%d", ix.Priority),
				ix.Protocol + "/" + ix.Privacy,
				blank(ix.AppProfile),
				fmt.Sprintf("%d", ix.Queries),
				fmt.Sprintf("%d", ix.FailedQueries),
				fmt.Sprintf("%d", ix.Grabs),
				avg,
				blank(strings.Join(ix.Tags, ",")),
			})
		}
		b.WriteString(table(cols, rows))
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, "%d indexers: %d enabled, %d failing, %d torrent, %d usenet",
		out.TotalCount, out.EnabledCount, out.FailingCount, out.TorrentCount, out.UsenetCount)
	if out.StatsReadable {
		fmt.Fprintf(&b, " — numbers cover the last %d days", out.StatsDays)
	}
	b.WriteString("\n")

	for _, w := range out.Warnings {
		fmt.Fprintf(&b, "\nwarning: %s\n", w)
	}

	return b.String()
}

func indexerStateCell(ix prowlarr.Indexer) string {
	switch {
	case !ix.Enabled:
		return "disabled"
	case ix.Failing:
		return "FAILING " + compactDuration(ix.DisabledForSeconds)
	default:
		return "ok"
	}
}

type prowlarrTestInput struct {
	Indexer string `json:"indexer,omitempty" jsonschema:"one indexer, by id or name; omitted, every enabled indexer is tested at once"`
}

func handleProwlarrTest(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in prowlarrTestInput,
) (*sdk.CallToolResult, prowlarr.TestReport, error) {
	rep, err := prowlarr.TestIndexers(ctx, in.Indexer)
	if err != nil {
		return nil, prowlarr.TestReport{}, err
	}
	return &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.TextContent{Text: renderProwlarrTest(rep)},
		},
	}, rep, nil
}

func renderProwlarrTest(rep prowlarr.TestReport) string {
	var b strings.Builder

	if len(rep.Results) > 0 {
		cols := []column{
			{"ID", alignRight},
			{"INDEXER", alignLeft},
			{"RESULT", alignLeft},
		}
		rows := make([][]string, 0, len(rep.Results))
		for _, r := range rep.Results {
			result := "passed"
			if !r.Passed {
				result = "FAILED"
			}
			rows = append(rows, []string{fmt.Sprintf("%d", r.ID), r.Name, result})
		}
		b.WriteString(table(cols, rows))
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, "%d tested, %d failed", rep.TestedCount, rep.FailedCount)
	if rep.SkippedCount > 0 {
		fmt.Fprintf(&b, ", %d disabled and not tested", rep.SkippedCount)
	}
	b.WriteString("\n")

	for _, w := range rep.Warnings {
		fmt.Fprintf(&b, "\nwarning: %s\n", w)
	}

	return b.String()
}
