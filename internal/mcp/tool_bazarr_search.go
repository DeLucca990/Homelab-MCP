package mcp

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DeLucca990/homelab-mcp/internal/bazarr"
)

// BAZARR SUBTITLE SEARCH TOOL
//
// The Search button of Bazarr's own UI. It is a write because it ends in a
// file: whatever the best match is gets written next to the video, replacing
// one already there in the same language. And it has a scale the id does not
// show — a series is every episode lacking something — so the confirmation
// states it.

type bazarrSearchInput struct {
	RadarrID  int `json:"radarr_id,omitempty" jsonschema:"a movie, by Radarr's own id — the 'id' radarr_library_status returns, not the TMDB id"`
	SeriesID  int `json:"series_id,omitempty" jsonschema:"a whole series, by Sonarr's own id: searches every episode for whatever its profile says it is missing. Cannot be combined with 'language'"`
	EpisodeID int `json:"episode_id,omitempty" jsonschema:"one episode, by Sonarr's episode id"`

	Language string `json:"language,omitempty" jsonschema:"search for this one language — a code ('en', 'pb' for Brazilian Portuguese; 'pt-BR' is understood) or a name. Works even when the language profile does not ask for it. Omitted, searches everything the profile says is missing"`
	Forced   bool   `json:"forced,omitempty" jsonschema:"the forced subtitle of that language — only the lines for foreign dialogue, signs and songs"`
	HI       bool   `json:"hi,omitempty" jsonschema:"the hearing-impaired subtitle of that language, with sound descriptions"`
}

func handleBazarrSearch(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in bazarrSearchInput,
) (*sdk.CallToolResult, bazarr.SearchResult, error) {
	plan, err := bazarr.PlanSearch(ctx, bazarr.SearchRequest{
		Target:   bazarr.Target{RadarrID: in.RadarrID, SeriesID: in.SeriesID, EpisodeID: in.EpisodeID},
		Language: in.Language,
		Forced:   in.Forced,
		HI:       in.HI,
	})
	if err != nil {
		return nil, bazarr.SearchResult{}, err
	}

	approved, pending, err := requireApproval(req, approval{
		message: bazarrSearchConfirmation(plan),
		fingerprint: fingerprint(
			"bazarr_subtitle_search",
			plan.Kind,
			strconv.Itoa(plan.RadarrID),
			strconv.Itoa(plan.SeriesID),
			strconv.Itoa(plan.EpisodeID),
			plan.Title,
			languageCodes(plan.Languages),
			languageCodes(plan.Replaces),
			strconv.Itoa(plan.EpisodesMissing),
		),
		refusal: "no subtitle search was started",
		subject: "search bazarr for subtitles of " + plan.Title,
	})
	if !approved {
		return pending, bazarr.SearchResult{}, err
	}

	out, err := bazarr.Search(ctx, plan)
	if err != nil {
		return nil, bazarr.SearchResult{}, err
	}

	return &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.TextContent{Text: renderBazarrSearchResult(out)},
		},
	}, out, nil
}

func bazarrSearchConfirmation(p bazarr.SearchPlan) string {
	var b strings.Builder

	switch p.Kind {
	case "series":
		fmt.Fprintf(&b, "Search for the missing subtitles of a whole series?\n\n")
		fmt.Fprintf(&b, "    %s   [series %d]\n", p.Title, p.SeriesID)
		fmt.Fprintf(&b, "    %d episode(s) lacking a subtitle their profile asks for\n", p.EpisodesMissing)
		b.WriteString("\nEach of those episodes is searched for its own missing languages, and every " +
			"match found is downloaded.\n")
		return b.String()
	case "movie":
		fmt.Fprintf(&b, "Search for subtitles for this movie now?\n\n")
		fmt.Fprintf(&b, "    %s   [radarr_id %d]\n", p.Title, p.RadarrID)
	default:
		fmt.Fprintf(&b, "Search for subtitles for this episode now?\n\n")
		fmt.Fprintf(&b, "    %s   [episode %d]\n", p.Title, p.EpisodeID)
	}

	fmt.Fprintf(&b, "    looking for: %s\n", languageNames(p.Languages))
	if len(p.Replaces) > 0 {
		fmt.Fprintf(&b, "\nIt ALREADY has %s. A better match would replace that file.\n",
			languageNames(p.Replaces))
	} else {
		b.WriteString("\nThe best match above Bazarr's minimum score is downloaded next to the video.\n")
	}
	for _, w := range p.Warnings {
		fmt.Fprintf(&b, "\nNote: %s\n", w)
	}

	return b.String()
}

func renderBazarrSearchResult(r bazarr.SearchResult) string {
	var b strings.Builder

	p := r.Plan
	fmt.Fprintf(&b, "bazarr searched for subtitles of %s\n", p.Title)

	if p.Kind == "series" {
		fmt.Fprintf(&b, "episodes lacking a subtitle: %d before, %d now\n",
			p.EpisodesMissing, r.EpisodesMissingAfter)
	} else {
		fmt.Fprintf(&b, "on disk now: %s\n", blank(languageCodes(r.Found)))
		if len(r.Pending) > 0 {
			fmt.Fprintf(&b, "not yet: %s\n", languageCodes(r.Pending))
		}
	}

	for _, w := range r.Warnings {
		fmt.Fprintf(&b, "\nwarning: %s\n", w)
	}

	return b.String()
}
