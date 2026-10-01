package mcp

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DeLucca990/homelab-mcp/internal/bazarr"
)

// BAZARR SUBTITLE SYNC TOOL
//
// Fixes a subtitle that is out of time with the video, either by aligning it
// to the audio track or by shifting every line a fixed amount. Both rewrite
// the file in place, so it is a write and asks first.

type bazarrSyncInput struct {
	RadarrID  int `json:"radarr_id,omitempty" jsonschema:"a movie, by Radarr's own id"`
	EpisodeID int `json:"episode_id,omitempty" jsonschema:"an episode, by Sonarr's episode id"`

	Language string `json:"language" jsonschema:"the language of the subtitle to fix — a code ('en', 'pb' for Brazilian Portuguese; 'pt-BR' is understood) or a name"`
	Forced   bool   `json:"forced,omitempty" jsonschema:"the forced subtitle of that language rather than the full one"`
	HI       bool   `json:"hi,omitempty" jsonschema:"the hearing-impaired subtitle of that language"`

	ShiftMs int `json:"shift_ms,omitempty" jsonschema:"move every line by this many milliseconds instead of syncing to the audio: positive when the subtitles come too EARLY (delays them), negative when they come too LATE. Use it when the offset is the same all the way through; omit it to align to the audio, which is for a subtitle that drifts"`
}

func handleBazarrSync(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in bazarrSyncInput,
) (*sdk.CallToolResult, bazarr.SyncResult, error) {
	plan, err := bazarr.PlanSync(ctx, bazarr.SyncRequest{
		Target:   bazarr.Target{RadarrID: in.RadarrID, EpisodeID: in.EpisodeID},
		Language: in.Language,
		Forced:   in.Forced,
		HI:       in.HI,
		ShiftMs:  in.ShiftMs,
	})
	if err != nil {
		return nil, bazarr.SyncResult{}, err
	}

	approved, pending, err := requireApproval(req, approval{
		message: bazarrSyncConfirmation(plan),
		fingerprint: fingerprint(
			"bazarr_subtitle_sync",
			plan.Kind,
			strconv.Itoa(plan.RadarrID),
			strconv.Itoa(plan.EpisodeID),
			plan.Subtitle.Path,
			plan.Mode,
			strconv.Itoa(plan.ShiftMs),
		),
		refusal: "no subtitle was changed",
		subject: fmt.Sprintf("%s the %s subtitle of %s", plan.Mode, plan.Subtitle.Code2, plan.Title),
	})
	if !approved {
		return pending, bazarr.SyncResult{}, err
	}

	out, err := bazarr.Sync(ctx, plan)
	if err != nil {
		return nil, bazarr.SyncResult{}, err
	}

	return &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.TextContent{Text: renderBazarrSyncResult(out)},
		},
	}, out, nil
}

func bazarrSyncConfirmation(p bazarr.SyncPlan) string {
	var b strings.Builder

	if p.Mode == "sync" {
		fmt.Fprintf(&b, "Align this subtitle to the audio track?\n\n")
	} else {
		fmt.Fprintf(&b, "Shift every line of this subtitle by %s?\n\n", shiftWords(p.ShiftMs))
	}
	fmt.Fprintf(&b, "    for:  %s\n", p.Title)
	fmt.Fprintf(&b, "    file: %s (%s)\n", p.Subtitle.Path, p.Subtitle.SubtitleLanguage)

	b.WriteString("\nThe file is rewritten in place.")
	if p.Mode == "sync" {
		b.WriteString(" Syncing decodes the whole audio track and can take a minute or two.")
	}
	b.WriteString("\n")
	return b.String()
}

func renderBazarrSyncResult(r bazarr.SyncResult) string {
	var b strings.Builder

	p := r.Plan
	if p.Mode == "sync" {
		fmt.Fprintf(&b, "bazarr aligned the %s subtitle of %s to the audio\n", p.Subtitle.SubtitleLanguage, p.Title)
	} else {
		fmt.Fprintf(&b, "bazarr shifted the %s subtitle of %s by %s\n",
			p.Subtitle.SubtitleLanguage, p.Title, shiftWords(p.ShiftMs))
	}
	fmt.Fprintf(&b, "file: %s\n", p.Subtitle.Path)

	for _, w := range r.Warnings {
		fmt.Fprintf(&b, "\nwarning: %s\n", w)
	}

	return b.String()
}

func shiftWords(ms int) string {
	d := time.Duration(ms) * time.Millisecond
	if ms > 0 {
		return fmt.Sprintf("+%s (later)", d)
	}
	return fmt.Sprintf("%s (earlier)", d)
}
