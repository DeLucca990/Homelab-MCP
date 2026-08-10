package mcp

import (
	"context"
	"fmt"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DeLucca990/homelab-mcp/internal/radarr"
	"github.com/DeLucca990/homelab-mcp/internal/system"
)

// RADARR QUEUE TOOL
//
// The download queue, which is where "I asked for this film" turns into either
// a file or a problem. Read-only.

func handleRadarrQueue(
	ctx context.Context,
	req *sdk.CallToolRequest,
	_ emptyInput,
) (*sdk.CallToolResult, radarr.Queue, error) {
	queue, err := radarr.GetQueue(ctx)
	if err != nil {
		return nil, radarr.Queue{}, err
	}
	return &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.TextContent{Text: renderQueue(queue)},
		},
	}, queue, nil
}

func renderQueue(q radarr.Queue) string {
	var b strings.Builder

	if len(q.Items) == 0 {
		return "the download queue is empty\n"
	}

	cols := []column{
		{"ID", alignRight},
		{"MOVIE", alignLeft},
		{"STATUS", alignLeft},
		{"PROGRESS", alignRight},
		{"LEFT", alignRight},
		{"ETA", alignLeft},
		{"CLIENT", alignLeft},
		{"QUALITY", alignLeft},
	}

	rows := make([][]string, 0, len(q.Items))
	for _, i := range q.Items {
		rows = append(rows, []string{
			fmt.Sprintf("%d", i.ID),
			i.DisplayName(),
			queueStatusCell(i),
			fmt.Sprintf("%.0f%%", i.Percent),
			sizeCell(i.SizeLeftBytes),
			etaCell(i),
			blank(i.DownloadClient),
			blank(i.Quality),
		})
	}

	b.WriteString(table(cols, rows))

	fmt.Fprintf(&b, "\n%d in the queue: %d downloading, %d stalled, %d waiting to import\n",
		q.TotalCount, q.DownloadingCount, q.StalledCount, q.BlockedCount)

	for _, w := range q.Warnings {
		fmt.Fprintf(&b, "\nwarning: %s\n", w)
	}

	return b.String()
}

// The tracked state is what matters once the bytes have arrived: "completed" on
// its own reads as done, while the file may still be sitting outside the library.
func queueStatusCell(i radarr.QueueItem) string {
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

// An empty ETA is the signal, so it is rendered as one rather than as "-".
func etaCell(i radarr.QueueItem) string {
	if i.TimeLeftSeconds > 0 {
		return compactDuration(i.TimeLeftSeconds)
	}
	if i.SizeLeftBytes == 0 {
		return "done"
	}
	return "none"
}

func sizeCell(b uint64) string {
	if b == 0 {
		return "-"
	}
	return system.CompactBytes(b)
}

func blank(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
