package mcp

import (
	"context"
	"fmt"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DeLucca990/homelab-mcp/internal/sonarr"
	"github.com/DeLucca990/homelab-mcp/internal/system"
)

// SONARR HEALTH TOOL
func handleSonarrHealth(
	ctx context.Context,
	req *sdk.CallToolRequest,
	_ emptyInput,
) (*sdk.CallToolResult, sonarr.Health, error) {
	h, err := sonarr.GetHealth(ctx)
	if err != nil {
		return nil, sonarr.Health{}, err
	}
	return &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.TextContent{Text: renderSonarrHealth(h)},
		},
	}, h, nil
}

func renderSonarrHealth(h sonarr.Health) string {
	var b strings.Builder

	fmt.Fprintf(&b, "%s at %s\n", nonEmpty(h.AppName, "sonarr"), h.URL)
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
		b.WriteString("\nsonarr reports no failing health checks\n")
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
