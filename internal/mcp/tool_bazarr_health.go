package mcp

import (
	"context"
	"fmt"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DeLucca990/homelab-mcp/internal/bazarr"
)

// BAZARR HEALTH TOOL
//
// Bazarr's own view of itself. It can be up and answering while every provider
// is throttled, while it has lost Sonarr, or while it has no language profile
// — each of which means no subtitle will ever arrive and nothing looks broken.
// Read-only.

func handleBazarrHealth(
	ctx context.Context,
	req *sdk.CallToolRequest,
	_ emptyInput,
) (*sdk.CallToolResult, bazarr.Health, error) {
	h, err := bazarr.GetHealth(ctx)
	if err != nil {
		return nil, bazarr.Health{}, err
	}
	return &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.TextContent{Text: renderBazarrHealth(h)},
		},
	}, h, nil
}

func renderBazarrHealth(h bazarr.Health) string {
	var b strings.Builder

	fmt.Fprintf(&b, "bazarr at %s\n", h.URL)
	if h.Version != "" {
		fmt.Fprintf(&b, "version: %s\n", h.Version)
	}
	if h.OS != "" {
		fmt.Fprintf(&b, "host: %s\n", h.OS)
	}
	if h.UptimeSeconds > 0 {
		fmt.Fprintf(&b, "up for: %s\n", compactDuration(h.UptimeSeconds))
	}

	fmt.Fprintf(&b, "sonarr: %s\n", arrLink(h.SonarrVersion, h.SonarrLive))
	fmt.Fprintf(&b, "radarr: %s\n", arrLink(h.RadarrVersion, h.RadarrLive))

	fmt.Fprintf(&b, "language profiles: %d\n", h.LanguageProfiles)
	fmt.Fprintf(&b, "wanted: %d movie subtitle(s), %d episode subtitle(s)\n",
		h.WantedMovieCount, h.WantedEpisodeCount)

	if len(h.Providers) > 0 {
		b.WriteString("\n")
		cols := []column{
			{"PROVIDER", alignLeft},
			{"STATUS", alignLeft},
			{"RETRY", alignLeft},
		}
		rows := make([][]string, 0, len(h.Providers))
		for _, p := range h.Providers {
			rows = append(rows, []string{p.Name, p.Status, blank(p.Retry)})
		}
		b.WriteString(table(cols, rows))
	}

	if len(h.Issues) == 0 {
		b.WriteString("\nbazarr reports no failing health checks\n")
	} else {
		b.WriteString("\n")
		cols := []column{
			{"OBJECT", alignLeft},
			{"ISSUE", alignLeft},
		}
		rows := make([][]string, 0, len(h.Issues))
		for _, i := range h.Issues {
			rows = append(rows, []string{i.Object, i.Issue})
		}
		b.WriteString(table(cols, rows))
	}

	for _, w := range h.Warnings {
		fmt.Fprintf(&b, "\nwarning: %s\n", w)
	}

	return b.String()
}

// arrLink says how Bazarr is connected to one of the *arrs it mirrors.
func arrLink(version string, live bool) string {
	switch version {
	case "":
		return "not used"
	case "unknown":
		return "configured but UNREACHABLE"
	}
	if live {
		return "v" + version + ", live"
	}
	return "v" + version + ", no live feed"
}
