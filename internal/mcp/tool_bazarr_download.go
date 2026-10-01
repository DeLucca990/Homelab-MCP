package mcp

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DeLucca990/homelab-mcp/internal/bazarr"
)

// BAZARR SUBTITLE DOWNLOAD TOOL
//
// The second half of a manual search: download the one candidate the user
// picked. It takes the short id bazarr_subtitle_candidates gave it, never
// Bazarr's own token for the result — that stays on this server — so the
// fingerprint covers the token and an id reused for a different subtitle
// cannot ride an earlier approval.

type bazarrDownloadInput struct {
	ID string `json:"id" jsonschema:"the candidate to download, from a bazarr_subtitle_candidates listing made in the last 30 minutes"`
}

func handleBazarrDownload(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in bazarrDownloadInput,
) (*sdk.CallToolResult, bazarr.DownloadResult, error) {
	plan, err := bazarr.PlanDownload(in.ID)
	if err != nil {
		return nil, bazarr.DownloadResult{}, err
	}

	approved, pending, err := requireApproval(req, approval{
		message: bazarrDownloadConfirmation(plan),
		fingerprint: fingerprint(
			"bazarr_subtitle_download",
			plan.Kind,
			strconv.Itoa(plan.RadarrID),
			strconv.Itoa(plan.EpisodeID),
			plan.Provider,
			languageCode(plan.SubtitleLanguage),
			plan.Token(),
		),
		refusal: "no subtitle was downloaded",
		subject: fmt.Sprintf("download %s subtitle from %s for %s",
			plan.Code2, plan.Provider, plan.Title),
	})
	if !approved {
		return pending, bazarr.DownloadResult{}, err
	}

	out, err := bazarr.Download(ctx, plan)
	if err != nil {
		return nil, bazarr.DownloadResult{}, err
	}

	return &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.TextContent{Text: renderBazarrDownloadResult(out)},
		},
	}, out, nil
}

func bazarrDownloadConfirmation(p bazarr.DownloadPlan) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Download this subtitle?\n\n")
	fmt.Fprintf(&b, "    for:      %s\n", p.Title)
	fmt.Fprintf(&b, "    language: %s\n", p.SubtitleLanguage)
	fmt.Fprintf(&b, "    from:     %s, score %d%%\n", p.Provider, p.Score)
	if len(p.ReleaseInfo) > 0 {
		fmt.Fprintf(&b, "    made for: %s\n", firstRelease(p.ReleaseInfo))
	}
	if len(p.Matches) > 0 {
		fmt.Fprintf(&b, "    matches:  %s\n", strings.Join(p.Matches, ", "))
	}

	b.WriteString("\nIt is written next to the video, replacing any subtitle already there in " +
		"this language.\n")
	return b.String()
}

func renderBazarrDownloadResult(r bazarr.DownloadResult) string {
	var b strings.Builder

	p := r.Plan
	fmt.Fprintf(&b, "downloaded the %s subtitle from %s for %s\n", p.SubtitleLanguage, p.Provider, p.Title)
	if r.OnDisk {
		fmt.Fprintf(&b, "on disk: %s\n", blank(r.Path))
	}

	for _, w := range r.Warnings {
		fmt.Fprintf(&b, "\nwarning: %s\n", w)
	}

	return b.String()
}
