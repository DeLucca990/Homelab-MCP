package mcp

import (
	"context"
	"fmt"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DeLucca990/homelab-mcp/internal/sonarr"
)

// SONARR QUEUE TOOL
//
// The download queue, which is where "I asked for this show" turns into either
// files or a problem. Read-only.

func handleSonarrQueue(
	ctx context.Context,
	req *sdk.CallToolRequest,
	_ emptyInput,
) (*sdk.CallToolResult, sonarr.Queue, error) {
	queue, err := sonarr.GetQueue(ctx)
	if err != nil {
		return nil, sonarr.Queue{}, err
	}
	return &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.TextContent{Text: renderSonarrQueue(queue)},
		},
	}, queue, nil
}

func renderSonarrQueue(q sonarr.Queue) string {
	var b strings.Builder

	if len(q.Items) == 0 {
		return "the download queue is empty\n"
	}

	cols := []column{
		{"ID", alignRight},
		{"SERIES", alignLeft},
		{"EPISODE", alignLeft},
		{"STATUS", alignLeft},
		{"PROGRESS", alignRight},
		{"LEFT", alignRight},
		{"ETA", alignLeft},
		{"CLIENT", alignLeft},
		{"QUALITY", alignLeft},
	}

	rows := make([][]string, 0, len(q.Items))
	for _, i := range q.Items {
		series := i.Series
		if series == "" {
			series = i.Release
		}
		rows = append(rows, []string{
			fmt.Sprintf("%d", i.ID),
			series,
			blank(i.EpisodeCode()),
			sonarrQueueStatusCell(i),
			fmt.Sprintf("%.0f%%", i.Percent),
			sizeCell(i.SizeLeftBytes),
			sonarrEtaCell(i),
			blank(i.DownloadClient),
			blank(i.Quality),
		})
	}

	b.WriteString(table(cols, rows))

	fmt.Fprintf(&b, "\n%d rows over %d downloads: %d downloading, %d stalled, %d waiting to import\n",
		q.TotalCount, q.DownloadCount, q.DownloadingCount, q.StalledCount, q.BlockedCount)

	if q.DownloadCount < q.TotalCount {
		b.WriteString("rows sharing a download are the same file — a season pack occupies " +
			"one row per episode, and removing any of them removes all of them\n")
	}

	for _, w := range q.Warnings {
		fmt.Fprintf(&b, "\nwarning: %s\n", w)
	}

	return b.String()
}

func sonarrQueueStatusCell(i sonarr.QueueItem) string {
	switch {
	case i.TrackedState == "importBlocked":
		return "import blocked"
	case i.TrackedState == "importPending":
		return "import pending"
	case i.TrackedState == "importing":
		return "importing"
	case i.Stalled:
		return "stalled"
	default:
		return i.Status
	}
}

func sonarrEtaCell(i sonarr.QueueItem) string {
	if i.TimeLeftSeconds > 0 {
		return compactDuration(i.TimeLeftSeconds)
	}
	if i.SizeLeftBytes == 0 {
		return "done"
	}
	return "none"
}
