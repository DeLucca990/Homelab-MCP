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

func renderMemoryTable(s system.MemoryStats) string {
	var b strings.Builder

	fmt.Fprintf(&b, "%-6s %11s %11s %11s %11s %11s %11s\n",
		"", "total", "used", "free", "shared", "buff/cache", "available")

	fmt.Fprintf(&b, "%-6s %11s %11s %11s %11s %11s %11s\n",
		"Mem:",
		system.IECBytes(s.TotalBytes),
		system.IECBytes(s.UsedBytes),
		system.IECBytes(s.FreeBytes),
		system.IECBytes(s.SharedBytes),
		system.IECBytes(s.BuffCacheBytes),
		system.IECBytes(s.AvailableBytes),
	)

	if s.Swap.Configured {
		fmt.Fprintf(&b, "%-6s %11s %11s %11s\n",
			"Swap:",
			system.IECBytes(s.Swap.TotalBytes),
			system.IECBytes(s.Swap.UsedBytes),
			system.IECBytes(s.Swap.FreeBytes),
		)
	} else {
		fmt.Fprintf(&b, "%-6s %11s\n", "Swap:", "n/a")
	}

	for _, w := range s.Warnings {
		fmt.Fprintf(&b, "\nwarning: %s\n", w)
	}

	return b.String()
}
