package mcp

import (
	"context"
	"fmt"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DeLucca990/homelab-mcp/internal/jellyfin"
)

// JELLYFIN SESSIONS TOOL
//
// What the server is serving right now, and what each stream costs it. This is
// the reading homelab_overview leaves out on purpose: a pinned core on a media
// server is either a transcode or a fault, and nothing else here can say which.
// Read-only.

type jellyfinSessionsInput struct {
	IncludeIdle bool `json:"include_idle,omitempty" jsonschema:"if true, also lists sessions that are connected but playing nothing — an open app or a paused browser tab. They cost the server nothing, so they are hidden by default"`
}

func handleJellyfinSessions(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in jellyfinSessionsInput,
) (*sdk.CallToolResult, jellyfin.Sessions, error) {
	s, err := jellyfin.GetSessions(ctx, jellyfin.SessionOptions{IncludeIdle: in.IncludeIdle})
	if err != nil {
		return nil, jellyfin.Sessions{}, err
	}
	return &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.TextContent{Text: renderJellyfinSessions(s)},
		},
	}, s, nil
}

func renderJellyfinSessions(s jellyfin.Sessions) string {
	var b strings.Builder

	if len(s.Items) == 0 {
		if s.IdleCount > 0 {
			fmt.Fprintf(&b, "nothing is playing — %d session%s connected and idle "+
				"(include_idle lists them)\n", s.IdleCount, plural(s.IdleCount))
			return b.String()
		}
		return "nothing is playing and no client is connected\n"
	}

	cols := []column{
		{"USER", alignLeft},
		{"WATCHING", alignLeft},
		{"WORK", alignLeft},
		{"AT", alignRight},
		{"BITRATE", alignRight},
		{"CLIENT", alignLeft},
		{"DEVICE", alignLeft},
	}

	rows := make([][]string, 0, len(s.Items))
	for _, i := range s.Items {
		rows = append(rows, []string{
			blank(i.User),
			blank(i.NowPlaying),
			jellyfinWorkCell(i),
			jellyfinPositionCell(i),
			jellyfinBitrateCell(i),
			blank(i.Client),
			blank(i.Device),
		})
	}

	b.WriteString(table(cols, rows))

	fmt.Fprintf(&b, "\n%d playing: %d direct, %d remux, %d hardware transcode, "+
		"%d software transcode",
		s.PlayingCount, s.DirectCount, s.RemuxCount, s.HardwareCount, s.SoftwareCount)
	if s.IdleCount > 0 {
		fmt.Fprintf(&b, " (%d idle session%s not listed)", s.IdleCount, plural(s.IdleCount))
	}
	b.WriteString("\n")

	for _, i := range s.Items {
		if len(i.TranscodeReasons) == 0 {
			continue
		}
		fmt.Fprintf(&b, "%s: %s\n", blank(i.NowPlaying), strings.Join(i.TranscodeReasons, ", "))
	}

	for _, w := range s.Warnings {
		fmt.Fprintf(&b, "\nwarning: %s\n", w)
	}

	return b.String()
}

func jellyfinWorkCell(s jellyfin.Session) string {
	cell := s.Work
	switch {
	case s.Work == jellyfin.WorkHardware && s.HardwareAccel != "":
		cell = "transcode (" + s.HardwareAccel + ")"
	case s.Work == jellyfin.WorkSoftware:
		cell = "transcode (cpu)"
	}
	if s.Stale {
		cell += " STALE"
	} else if s.Paused {
		cell += ", paused"
	}
	return cell
}

func jellyfinPositionCell(s jellyfin.Session) string {
	if s.RuntimeSeconds == 0 {
		return "-"
	}
	return fmt.Sprintf("%.0f%%", s.Percent)
}

func jellyfinBitrateCell(s jellyfin.Session) string {
	if s.TranscodeBitrate == 0 {
		return "-"
	}
	return fmt.Sprintf("%.1fMb", float64(s.TranscodeBitrate)/1_000_000)
}
