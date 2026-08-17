package mcp

import (
	"context"
	"fmt"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DeLucca990/homelab-mcp/internal/jellyfin"
)

// JELLYFIN HEALTH TOOL
//
// Jellyfin's own view of itself: version, the encoding settings that decide
// what a transcode costs, free space per folder it writes to, its scheduled
// tasks and any plugin that is not running. Read-only.

func handleJellyfinHealth(
	ctx context.Context,
	req *sdk.CallToolRequest,
	_ emptyInput,
) (*sdk.CallToolResult, jellyfin.Health, error) {
	h, err := jellyfin.GetHealth(ctx)
	if err != nil {
		return nil, jellyfin.Health{}, err
	}
	return &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.TextContent{Text: renderJellyfinHealth(h)},
		},
	}, h, nil
}

func renderJellyfinHealth(h jellyfin.Health) string {
	var b strings.Builder

	fmt.Fprintf(&b, "%s at %s\n", nonEmpty(h.ServerName, "jellyfin"), h.URL)
	if h.Version != "" {
		fmt.Fprintf(&b, "version: %s\n", h.Version)
	}
	if h.OS != "" {
		fmt.Fprintf(&b, "host: %s\n", h.OS)
	}
	if h.PendingRestart {
		b.WriteString("restart: pending\n")
	}

	b.WriteString("transcoding: ")
	if h.HardwareAcceleration == "" {
		b.WriteString("software only, no hardware acceleration configured\n")
	} else {
		fmt.Fprintf(&b, "%s, hardware encoding %s\n",
			h.HardwareAcceleration, onOff(h.HardwareEncoding))
		if len(h.HardwareDecoders) > 0 {
			fmt.Fprintf(&b, "hardware decodes: %s\n", strings.Join(h.HardwareDecoders, ", "))
		}
	}

	if len(h.Folders) > 0 {
		b.WriteString("\n")
		cols := []column{
			{"FOLDER", alignLeft},
			{"PATH", alignLeft},
			{"FREE", alignRight},
			{"USED", alignRight},
		}
		rows := make([][]string, 0, len(h.Folders))
		for _, f := range h.Folders {
			rows = append(rows, []string{
				f.Name,
				f.Path,
				sizeCell(f.FreeBytes),
				sizeCell(f.UsedBytes),
			})
		}
		b.WriteString(table(cols, rows))
		b.WriteString("free space is the device's, so folders on one disk repeat the number\n")
	}

	if len(h.Tasks) > 0 {
		b.WriteString("\n")
		cols := []column{
			{"TASK", alignLeft},
			{"STATE", alignLeft},
			{"LAST RUN", alignRight},
			{"RESULT", alignLeft},
		}
		rows := make([][]string, 0, len(h.Tasks))
		for _, t := range h.Tasks {
			rows = append(rows, []string{
				t.Name,
				jellyfinTaskStateCell(t),
				jellyfinLastRunCell(t),
				jellyfinTaskResultCell(t),
			})
		}
		b.WriteString(table(cols, rows))
		fmt.Fprintf(&b, "%d scheduled task%s in total; the rest last completed cleanly\n",
			h.TaskCount, plural(h.TaskCount))
	}

	switch {
	case len(h.Plugins) > 0:
		b.WriteString("\n")
		cols := []column{
			{"PLUGIN", alignLeft},
			{"VERSION", alignLeft},
			{"STATUS", alignLeft},
		}
		rows := make([][]string, 0, len(h.Plugins))
		for _, p := range h.Plugins {
			rows = append(rows, []string{p.Name, blank(p.Version), p.Status})
		}
		b.WriteString(table(cols, rows))
		fmt.Fprintf(&b, "%d plugin%s installed; the rest are active\n",
			h.PluginCount, plural(h.PluginCount))
	case h.PluginCount > 0:
		fmt.Fprintf(&b, "\nall %d installed plugin%s are active\n",
			h.PluginCount, plural(h.PluginCount))
	}

	for _, w := range h.Warnings {
		fmt.Fprintf(&b, "\nwarning: %s\n", w)
	}

	return b.String()
}

func jellyfinTaskStateCell(t jellyfin.Task) string {
	if t.State == "Running" {
		return fmt.Sprintf("running %.0f%%", t.ProgressPercent)
	}
	return strings.ToLower(t.State)
}

func jellyfinLastRunCell(t jellyfin.Task) string {
	if t.LastRunSecondsAgo == 0 {
		return "-"
	}
	return compactDuration(t.LastRunSecondsAgo) + " ago"
}

func jellyfinTaskResultCell(t jellyfin.Task) string {
	if t.LastStatus == "" {
		if t.State == "Running" || t.State == "Cancelling" {
			return "first run"
		}
		return "never run"
	}
	if t.ErrorMessage != "" {
		return t.LastStatus + ": " + t.ErrorMessage
	}
	return t.LastStatus
}

func onOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}
