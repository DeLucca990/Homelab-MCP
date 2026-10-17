package mcp

import (
	"context"
	"fmt"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DeLucca990/homelab-mcp/internal/digest"
	"github.com/DeLucca990/homelab-mcp/internal/system"
)

// DIGEST TOOL
//
// The overview over a window of time: what happened since the last look. A
// daily report is built on it, but so is any "how was the night?".

type digestInput struct {
	Hours int `json:"hours,omitempty" jsonschema:"how far back to look; default 24, maximum 168"`
}

func handleDigest(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in digestInput,
) (*sdk.CallToolResult, digest.Digest, error) {
	d := digest.Get(ctx, in.Hours)

	return &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.TextContent{Text: renderDigest(d)},
		},
	}, d, nil
}

func renderDigest(d digest.Digest) string {
	var b strings.Builder

	fmt.Fprintf(&b, "the last %dh, read in %dms\n\n", d.Hours, d.ElapsedMs)

	if len(d.Attention) == 0 {
		b.WriteString("nothing needs attention right now\n")
	} else {
		b.WriteString("needs attention now:\n")
		for _, f := range d.Attention {
			fmt.Fprintf(&b, "  %s: %s\n", f.Area, f.Warning)
		}
	}

	if d.RebootedSecondsAgo > 0 {
		fmt.Fprintf(&b, "\nthe host booted %s ago\n", compactDuration(d.RebootedSecondsAgo))
	}
	if len(d.Containers) > 0 {
		b.WriteString("\ncontainers:\n")
		for _, c := range d.Containers {
			fmt.Fprintf(&b, "  %s %s %s ago", c.Container, c.Event, compactDuration(c.SecondsAgo))
			switch {
			case c.OOMKilled:
				b.WriteString(" (killed for its memory limit)")
			case c.Event == "stopped":
				fmt.Fprintf(&b, " (exit %d)", c.ExitCode)
			}
			b.WriteString("\n")
		}
	}

	writeDownloads(&b, "imported", d.Imported)
	writeDownloads(&b, "failed", d.Failed)
	writeReleases(&b, "late — due, monitored, still no file", d.Late)
	writeReleases(&b, "due in the next 24h", d.Upcoming)

	if p := d.Playback; p != nil {
		fmt.Fprintf(&b, "\njellyfin: %d playback(s), %d failed sign-in(s)\n", p.Plays, p.FailedLogins)
		for _, w := range p.Watched {
			fmt.Fprintf(&b, "  %s\n", w)
		}
		for _, f := range p.FailedLoginEntries {
			fmt.Fprintf(&b, "  failed sign-in: %s\n", f)
		}
		for _, pr := range p.Problems {
			fmt.Fprintf(&b, "  problem: %s\n", pr)
		}
	}

	t := d.Totals
	if t.WantedMovieSubtitles != nil {
		fmt.Fprintf(&b, "\nsubtitles wanted: %d for movies, %d for episodes\n",
			*t.WantedMovieSubtitles, *t.WantedEpisodeSubtitles)
	}
	if t.FailingIndexers != nil {
		fmt.Fprintf(&b, "\nfailing indexers: %d\n", *t.FailingIndexers)
	}
	if len(t.Disks) > 0 {
		b.WriteString("\nwritable filesystems:\n")
		for _, disk := range t.Disks {
			fmt.Fprintf(&b, "  %s %.0f%% (%s of %s)\n", disk.Mountpoint, disk.UsedPercent,
				system.CompactBytes(disk.UsedBytes), system.CompactBytes(disk.TotalBytes))
		}
	}

	for _, t := range d.Truncated {
		fmt.Fprintf(&b, "\nwarning: incomplete: %s\n", t)
	}
	for _, e := range d.Errors {
		fmt.Fprintf(&b, "\nwarning: could not read %s\n", e)
	}

	return b.String()
}

func writeDownloads(b *strings.Builder, label string, ds []digest.Download) {
	if len(ds) == 0 {
		return
	}
	fmt.Fprintf(b, "\n%s (%d):\n", label, len(ds))
	for _, d := range ds {
		title := d.Title
		if d.Episode != "" {
			title += " " + d.Episode
		}
		fmt.Fprintf(b, "  %s  %s", d.Service, title)
		if d.Quality != "" {
			fmt.Fprintf(b, "  %s", d.Quality)
		}
		if d.Detail != "" {
			fmt.Fprintf(b, "  %q", d.Detail)
		}
		fmt.Fprintf(b, "  %s ago\n", compactDuration(d.SecondsAgo))
	}
}

func writeReleases(b *strings.Builder, label string, rs []digest.Release) {
	if len(rs) == 0 {
		return
	}
	fmt.Fprintf(b, "\n%s (%d):\n", label, len(rs))
	for _, r := range rs {
		title := r.Title
		if r.Episode != "" {
			title += " " + r.Episode
		}
		if r.Kind != "" {
			title += " (" + r.Kind + ")"
		}
		fmt.Fprintf(b, "  %s  %s  %s\n", r.Service, title, r.Due)
	}
}
