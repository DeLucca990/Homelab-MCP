package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/DeLucca990/homelab-mcp/internal/system"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type diskInput struct {
	IncludeAll bool `json:"include_all,omitempty" jsonschema:"se true, inclui pseudo-filesystems, snaps e montagens duplicadas que normalmente são filtrados"`
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

// Above this inode percentage we warn explicitly — it is the kind of
// problem that `df -h` hides completely.
const inodeWarnThreshold = 80.0

func renderDiskTable(stats system.DiskStats) string {
	if len(stats.Filesystems) == 0 {
		return "nenhum sistema de arquivos encontrado\n"
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
			size:  system.CompactBytes(f.Total.Bytes),
			used:  system.CompactBytes(f.Used.Bytes),
			avail: system.CompactBytes(f.Free.Bytes),
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

	// Footer: what df -h would not tell you.
	for _, f := range stats.Filesystems {
		if f.InodesUsedPercent >= inodeWarnThreshold {
			fmt.Fprintf(&b, "\naviso: %s está com %.0f%% dos inodes em uso "+
				"(%d de %d) — pode falhar com \"no space left on device\" "+
				"mesmo com espaço livre\n",
				f.Mountpoint, f.InodesUsedPercent, f.InodesUsed, f.InodesTotal)
		}
	}
	for _, warn := range stats.Warnings {
		fmt.Fprintf(&b, "\naviso: %s\n", warn)
	}
	if stats.SkippedCount > 0 {
		fmt.Fprintf(&b, "\n(%d montagens filtradas; use include_all para vê-las)\n",
			stats.SkippedCount)
	}

	return b.String()
}
