package mcp

import (
	"context"
	"fmt"
	"slices"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DeLucca990/homelab-mcp/internal/prowlarr"
)

// PROWLARR HEALTH TOOL
//
// Prowlarr's own view of itself: the health checks it keeps for indexers that
// are backed off, apps it cannot push to and proxies that stopped answering.
// Read-only.

func handleProwlarrHealth(
	ctx context.Context,
	req *sdk.CallToolRequest,
	_ emptyInput,
) (*sdk.CallToolResult, prowlarr.Health, error) {
	h, err := prowlarr.GetHealth(ctx)
	if err != nil {
		return nil, prowlarr.Health{}, err
	}
	return &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.TextContent{Text: renderProwlarrHealth(h)},
		},
	}, h, nil
}

func renderProwlarrHealth(h prowlarr.Health) string {
	var b strings.Builder

	fmt.Fprintf(&b, "prowlarr at %s\n", h.URL)
	if h.Version != "" {
		fmt.Fprintf(&b, "version: %s", h.Version)
		if h.Branch != "" {
			fmt.Fprintf(&b, " (%s)", h.Branch)
		}
		b.WriteString("\n")
	}
	if h.OS != "" {
		fmt.Fprintf(&b, "host: %s", h.OS)
		if h.IsDocker {
			b.WriteString(", in docker")
		}
		b.WriteString("\n")
	}
	if h.UptimeSeconds > 0 {
		fmt.Fprintf(&b, "up for: %s\n", compactDuration(h.UptimeSeconds))
	}

	fmt.Fprintf(&b, "indexers: %d, %d enabled, %d failing\n",
		h.IndexerCount, h.EnabledIndexerCount, h.FailingIndexerCount)
	fmt.Fprintf(&b, "applications: %d%s\n", h.ApplicationCount, syncLevelSummary(h.SyncLevels))
	fmt.Fprintf(&b, "proxies: %d\n", h.ProxyCount)

	if len(h.Issues) == 0 {
		b.WriteString("\nprowlarr reports no failing health checks\n")
	} else {
		b.WriteString("\n")
		cols := []column{
			{"TYPE", alignLeft},
			{"CHECK", alignLeft},
			{"MESSAGE", alignLeft},
		}
		rows := make([][]string, 0, len(h.Issues))
		for _, i := range h.Issues {
			rows = append(rows, []string{i.Type, i.Source, i.Message})
		}
		b.WriteString(table(cols, rows))
	}

	for _, w := range h.Warnings {
		fmt.Fprintf(&b, "\nwarning: %s\n", w)
	}

	return b.String()
}

func syncLevelSummary(levels map[string]int) string {
	if len(levels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(levels))
	for k := range levels {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%d %s", levels[k], k))
	}
	return " (" + strings.Join(parts, ", ") + ")"
}
