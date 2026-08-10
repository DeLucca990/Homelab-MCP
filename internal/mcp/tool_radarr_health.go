package mcp

import (
	"context"
	"fmt"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DeLucca990/homelab-mcp/internal/radarr"
	"github.com/DeLucca990/homelab-mcp/internal/system"
)

// RADARR HEALTH TOOL
//
// Radarr's own view of itself. The container can be up and healthy while every
// indexer it has is refusing to answer, which is the state where nothing is
// downloading and nothing looks wrong. Read-only.

func handleRadarrHealth(
	ctx context.Context,
	req *sdk.CallToolRequest,
	_ emptyInput,
) (*sdk.CallToolResult, radarr.Health, error) {
	h, err := radarr.GetHealth(ctx)
	if err != nil {
		return nil, radarr.Health{}, err
	}
	return &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.TextContent{Text: renderRadarrHealth(h)},
		},
	}, h, nil
}

func renderRadarrHealth(h radarr.Health) string {
	var b strings.Builder

	fmt.Fprintf(&b, "%s at %s\n", nonEmpty(h.AppName, "radarr"), h.URL)
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

	fmt.Fprintf(&b, "queue: %d item(s)\n", h.QueueCount)

	if len(h.RootFolders) > 0 {
		b.WriteString("\n")
		cols := []column{
			{"ROOT FOLDER", alignLeft},
			{"FREE", alignRight},
			{"REACHABLE", alignLeft},
			{"UNMAPPED", alignRight},
		}
		rows := make([][]string, 0, len(h.RootFolders))
		for _, f := range h.RootFolders {
			rows = append(rows, []string{
				f.Path,
				system.CompactBytes(f.FreeBytes),
				yesNo(f.Accessible),
				fmt.Sprintf("%d", f.UnmappedFolders),
			})
		}
		b.WriteString(table(cols, rows))
	}

	if len(h.Issues) == 0 {
		b.WriteString("\nradarr reports no failing health checks\n")
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

func nonEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}
