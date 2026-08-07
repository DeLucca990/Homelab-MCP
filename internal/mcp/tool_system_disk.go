package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/DeLucca990/homelab-mcp/internal/system"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type diskInput struct {
	IncludeAll bool `json:"include_all,omitempty" jsonschema:"if true, includes pseudo-filesystems, snaps and duplicate mounts that are normally filtered out"`
}

func handleDiskStats(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in diskInput,
) (*sdk.CallToolResult, system.DiskStats, error) {
	stats, err := system.GetDiskStats(ctx, in.IncludeAll)
	if err != nil {
		return nil, system.DiskStats{}, err
	}
	return &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.TextContent{Text: renderDiskTable(stats)},
		},
	}, stats, nil
}

func renderDiskTable(stats system.DiskStats) string {
	if len(stats.Filesystems) == 0 {
		return "no filesystem found\n"
	}

	// Text on the left, numbers on the right — same as df.
	cols := []column{
		{"Filesystem", alignLeft},
		{"Size", alignRight},
		{"Used", alignRight},
		{"Avail", alignRight},
		{"Use%", alignRight},
		{"Mounted on", alignLeft},
	}

	rows := make([][]string, 0, len(stats.Filesystems))
	for _, f := range stats.Filesystems {
		if f.Error != "" {
			// Unreachable mount: keep the row, flag the numbers.
			rows = append(rows, []string{f.Device, "-", "-", "-", "-", f.Mountpoint})
			continue
		}
		rows = append(rows, []string{
			f.Device,
			system.CompactBytes(f.TotalBytes),
			system.CompactBytes(f.UsedBytes),
			system.CompactBytes(f.FreeBytes),
			fmt.Sprintf("%.0f%%", f.UsedPercent),
			f.Mountpoint,
		})
	}

	var b strings.Builder
	b.WriteString(table(cols, rows))

	// Footer: what `df -h` would not tell you.
	for _, warn := range stats.Warnings {
		fmt.Fprintf(&b, "\nwarning: %s\n", warn)
	}
	if stats.SkippedCount > 0 {
		fmt.Fprintf(&b, "\n(%d mounts filtered out; use include_all to see them)\n",
			stats.SkippedCount)
	}

	return b.String()
}
