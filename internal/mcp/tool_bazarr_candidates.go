package mcp

import (
	"context"
	"fmt"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DeLucca990/homelab-mcp/internal/bazarr"
)

// BAZARR SUBTITLE CANDIDATES TOOL
//
// Bazarr's manual search: every subtitle every provider offered, scored,
// including the ones an automatic search would have rejected. It changes
// nothing — the download is a separate, confirmed step — but it is not free
// either: it queries every provider, and they count those queries against
// their rate limits.

type bazarrCandidatesInput struct {
	RadarrID  int    `json:"radarr_id,omitempty" jsonschema:"a movie, by Radarr's own id"`
	EpisodeID int    `json:"episode_id,omitempty" jsonschema:"an episode, by Sonarr's episode id — a manual search is always for one file"`
	Language  string `json:"language,omitempty" jsonschema:"only show candidates in this language — a code ('en', 'pb' for Brazilian Portuguese, 'pt-BR' is understood) or a name. It must be in the item's language profile"`
	Limit     int    `json:"limit,omitempty" jsonschema:"how many candidates to list, best first; default 15, maximum 50"`
}

func handleBazarrCandidates(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in bazarrCandidatesInput,
) (*sdk.CallToolResult, bazarr.Candidates, error) {
	out, err := bazarr.GetCandidates(ctx, bazarr.Target{
		RadarrID:  in.RadarrID,
		EpisodeID: in.EpisodeID,
	}, in.Language, in.Limit)
	if err != nil {
		return nil, bazarr.Candidates{}, err
	}
	return &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.TextContent{Text: renderBazarrCandidates(out)},
		},
	}, out, nil
}

func renderBazarrCandidates(c bazarr.Candidates) string {
	var b strings.Builder

	fmt.Fprintf(&b, "subtitles offered for %s\n", c.Title)
	fmt.Fprintf(&b, "searched for the languages of profile %s\n", c.Profile)

	if len(c.Candidates) > 0 {
		b.WriteString("\n")
		cols := []column{
			{"ID", alignLeft},
			{"SCORE", alignRight},
			{"LANG", alignLeft},
			{"PROVIDER", alignLeft},
			{"MATCHES", alignLeft},
			{"RELEASE", alignLeft},
		}
		rows := make([][]string, 0, len(c.Candidates))
		for _, cand := range c.Candidates {
			rows = append(rows, []string{
				cand.ID,
				fmt.Sprintf("%d%%", cand.Score),
				languageCode(cand.SubtitleLanguage),
				cand.Provider,
				blank(strings.Join(cand.Matches, ",")),
				blank(firstRelease(cand.ReleaseInfo)),
			})
		}
		b.WriteString(table(cols, rows))
	}

	fmt.Fprintf(&b, "\n%d offered, %d shown\n", c.TotalCount, c.ShownCount)

	for _, w := range c.Warnings {
		fmt.Fprintf(&b, "\nwarning: %s\n", w)
	}

	return b.String()
}

// Providers list every release a subtitle fits, sometimes dozens; the first is
// the one the uploader named it for, and the full list is in the JSON.
func firstRelease(releases []string) string {
	if len(releases) == 0 {
		return ""
	}
	r := releases[0]
	if len(r) > 60 {
		r = r[:60] + "…"
	}
	if len(releases) > 1 {
		r += fmt.Sprintf(" (+%d)", len(releases)-1)
	}
	return r
}
