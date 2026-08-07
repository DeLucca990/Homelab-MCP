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

	type row struct{ fs, size, used, avail, pct, mount string }

	head := row{"Filesystem", "Size", "Used", "Avail", "Use%", "Mounted on"}

	rows := make([]row, 0, len(stats.Filesystems))
	for _, f := range stats.Filesystems {
		if f.Error != "" {
			// Unreachable mount: keep the row, flag the numbers.
			rows = append(rows, row{f.Device, "-", "-", "-", "-", f.Mountpoint})
			continue
		}
		rows = append(rows, row{
			fs:    f.Device,
			size:  system.CompactBytes(f.TotalBytes),
			used:  system.CompactBytes(f.UsedBytes),
			avail: system.CompactBytes(f.FreeBytes),
			pct:   fmt.Sprintf("%.0f%%", f.UsedPercent),
			mount: f.Mountpoint,
		})
	}

	// FIRST PASS: measure. There is no way to align the columns without
	// first knowing the widest content in each one.
	w := [5]int{
		len(head.fs), len(head.size), len(head.used), len(head.avail), len(head.pct),
	}
	for _, r := range rows {
		w[0] = max(w[0], len(r.fs))
		w[1] = max(w[1], len(r.size))
		w[2] = max(w[2], len(r.used))
		w[3] = max(w[3], len(r.avail))
		w[4] = max(w[4], len(r.pct))
	}

	// SECOND PASS: write.
	var b strings.Builder
	write := func(r row) {
		// The `*` in the verb consumes the width as an argument, which allows
		// widths computed at runtime. `%-*s` aligns left, `%*s` aligns right —
		// same as df: text on the left, numbers on the right.
		fmt.Fprintf(&b, "%-*s  %*s  %*s  %*s  %*s  %s\n",
			w[0], r.fs,
			w[1], r.size,
			w[2], r.used,
			w[3], r.avail,
			w[4], r.pct,
			r.mount)
	}

	write(head)
	for _, r := range rows {
		write(r)
	}

	// Footer: what df -h would not tell you. The warnings themselves are
	// computed in the system package, so structuredContent carries them too.
	for _, warn := range stats.Warnings {
		fmt.Fprintf(&b, "\nwarning: %s\n", warn)
	}
	if stats.SkippedCount > 0 {
		fmt.Fprintf(&b, "\n(%d mounts filtered out; use include_all to see them)\n",
			stats.SkippedCount)
	}

	return b.String()
}
