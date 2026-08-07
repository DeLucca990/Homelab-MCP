package mcp

import (
	"context"
	"fmt"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DeLucca990/homelab-mcp/internal/system"
)

// MEMORY TOOL
func handleMemoryStats(
	ctx context.Context,
	req *sdk.CallToolRequest,
	_ emptyInput,
) (*sdk.CallToolResult, system.MemoryStats, error) {
	stats, err := system.GetMemoryStats(ctx)
	if err != nil {
		return nil, system.MemoryStats{}, err
	}
	return &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.TextContent{Text: renderMemoryTable(stats)},
		},
	}, stats, nil
}

func humanCompact(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%dB", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	v := float64(b) / float64(div)
	if v >= 10 {
		return fmt.Sprintf("%.0f%ci", v, "KMGTPE"[exp])
	}
	return fmt.Sprintf("%.1f%ci", v, "KMGTPE"[exp])
}

func renderMemoryTable(s system.MemoryStats) string {
	var b strings.Builder

	fmt.Fprintf(&b, "%-6s %11s %11s %11s %11s %11s %11s\n",
		"", "total", "used", "free", "shared", "buff/cache", "available")

	fmt.Fprintf(&b, "%-6s %11s %11s %11s %11s %11s %11s\n",
		"Mem:",
		humanCompact(s.Total.Bytes),
		humanCompact(s.Used.Bytes),
		humanCompact(s.Free.Bytes),
		humanCompact(s.Shared.Bytes),
		humanCompact(s.BuffCache.Bytes),
		humanCompact(s.Available.Bytes),
	)

	if s.Swap.Configured {
		fmt.Fprintf(&b, "%-6s %11s %11s %11s\n",
			"Swap:",
			humanCompact(s.Swap.Total.Bytes),
			humanCompact(s.Swap.Used.Bytes),
			humanCompact(s.Swap.Free.Bytes),
		)
	} else {
		fmt.Fprintf(&b, "%-6s %11s\n", "Swap:", "n/a")
	}

	for _, w := range s.Warnings {
		fmt.Fprintf(&b, "\naviso: %s\n", w)
	}

	return b.String()
}
