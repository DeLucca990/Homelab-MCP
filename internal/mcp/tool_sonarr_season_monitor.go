package mcp

import (
	"context"
	"fmt"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DeLucca990/homelab-mcp/internal/sonarr"
)

// SONARR SEASON MONITORING TOOL
//
// The switch every other Sonarr tool reads. A search of an unmonitored season
// finds nothing — Sonarr filters those episodes out before asking an indexer —
// so without this, "download only season 3" of a show added with monitor=none
// had no path at all: the add options are presets (firstSeason, lastSeason,
// latestSeason) and none of them names an arbitrary season.
//
// It is a write and it goes through the confirmation, because it has a scale
// like the rest of this family: switching a season on tells Sonarr to go and get
// every episode in it, which for a 24-episode season is 24 downloads waiting for
// the next search. Switching one off is the quieter direction, and the
// confirmation says which way it is going rather than assuming the interesting
// one.

type sonarrSeasonMonitorInput struct {
	SeriesID int `json:"series_id" jsonschema:"the series to change, from sonarr_library_status. Sonarr's own 'id' field is what this expects; a TVDB id is accepted too and resolved, and the confirmation says which show it landed on"`

	Season *int `json:"season" jsonschema:"the season number to change. Season 0 is Sonarr's bucket for specials. sonarr_library_status with a 'term' that selects this one series lists the seasons it has"`

	Monitored *bool `json:"monitored,omitempty" jsonschema:"true to have Sonarr start trying to get this season, false to have it stop; defaults to true. The flag cascades to every episode of the season, including ones that have not aired yet"`
}

func handleSonarrSeasonMonitor(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in sonarrSeasonMonitorInput,
) (*sdk.CallToolResult, sonarr.SeasonMonitorResult, error) {
	if in.SeriesID <= 0 {
		return nil, sonarr.SeasonMonitorResult{}, fmt.Errorf(
			"'series_id' is required — sonarr_library_status lists the series with their ids")
	}
	if in.Season == nil {
		return nil, sonarr.SeasonMonitorResult{}, fmt.Errorf(
			"'season' is required — pass the season number to change, or use " +
				"sonarr_library_status with a 'term' naming this series to see which seasons it has")
	}

	plan, err := sonarr.PlanSeasonMonitor(ctx, sonarr.SeasonMonitorRequest{
		SeriesID:  in.SeriesID,
		Season:    *in.Season,
		Monitored: boolOr(in.Monitored, true),
	})
	if err != nil {
		return nil, sonarr.SeasonMonitorResult{}, err
	}

	if !plan.AlreadySet {
		approved, pending, err := requireApproval(req, approval{
			message: seasonMonitorMessage(plan, in.SeriesID),
			fingerprint: fingerprint(
				append([]string{"sonarr_season_monitor"}, plan.Fingerprint()...)...),
			refusal: "monitoring not changed",
			subject: fmt.Sprintf("set season %d of %s (%d) to %s in sonarr",
				plan.Season, plan.Title, plan.Year, monitoredWord(plan.Monitored)),
		})
		if !approved {
			return pending, sonarr.SeasonMonitorResult{}, err
		}
	}

	out, err := sonarr.SetSeasonMonitored(ctx, plan)
	if err != nil {
		return nil, sonarr.SeasonMonitorResult{}, err
	}

	return &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.TextContent{Text: renderSeasonMonitorResult(out)},
		},
	}, out, nil
}

func seasonMonitorMessage(p sonarr.SeasonMonitorPlan, requestedID int) string {
	var b strings.Builder

	if p.Monitored {
		fmt.Fprintf(&b, "Start monitoring a season?\n\n")
	} else {
		fmt.Fprintf(&b, "Stop monitoring a season?\n\n")
	}

	fmt.Fprintf(&b, "    %s (%d)   [series %d]\n", p.Title, p.Year, p.SeriesID)
	if requestedID != p.SeriesID {
		fmt.Fprintf(&b, "    (you gave %d, which is its TVDB id — Sonarr's id for it is %d)\n",
			requestedID, p.SeriesID)
	}
	fmt.Fprintf(&b, "    season %d: %d episodes, %d aired, %d on disk\n",
		p.Season, p.EpisodesTotal, p.EpisodesAired, p.EpisodesOnDisk)
	fmt.Fprintf(&b, "    monitored: %s → %s\n",
		monitoredWord(p.WasMonitored), monitoredWord(p.Monitored))

	unaired := p.EpisodesTotal - p.EpisodesAired

	if p.Monitored {
		switch {
		case p.EpisodesMissing > 0:
			fmt.Fprintf(&b, "\nSonarr will go and get %d episode%s that %s aired and %s not on "+
				"disk.", p.EpisodesMissing, plural(p.EpisodesMissing), have(p.EpisodesMissing),
				are(p.EpisodesMissing))
		case unaired > 0:
			b.WriteString("\nNothing of that season is missing right now.")
		default:
			b.WriteString("\nEvery episode of that season is already on disk, so this only " +
				"affects future searches for upgrades.")
		}
		if unaired > 0 {
			fmt.Fprintf(&b, " The flag also covers the %d episode%s that %s not aired yet, "+
				"so Sonarr will pick %s up as they air.",
				unaired, plural(unaired), have(unaired), them(unaired))
		}
		b.WriteString("\n")

		if !p.SeriesMonitored {
			fmt.Fprintf(&b, "\nNote that %s itself is NOT monitored, so this alone will not "+
				"make Sonarr grab anything — that switch is in Sonarr's own UI.\n", p.Title)
		}
		b.WriteString("\nThis does not start a search; it decides what a search would look for.\n")
	} else {
		fmt.Fprintf(&b, "\nSonarr will stop looking for all %d episode%s of that season, "+
			"including the %d it has not got yet. Nothing on disk is deleted.\n",
			p.EpisodesTotal, plural(p.EpisodesTotal), p.EpisodesMissing)
	}

	return b.String()
}

func renderSeasonMonitorResult(r sonarr.SeasonMonitorResult) string {
	var b strings.Builder

	if !r.Changed {
		fmt.Fprintf(&b, "season %d of %s (%d) was already %s — nothing was changed\n",
			r.Season, r.Title, r.Year, monitoredWord(r.Monitored))
	} else {
		fmt.Fprintf(&b, "season %d of %s (%d) is now %s\n",
			r.Season, r.Title, r.Year, monitoredWord(r.Monitored))
		fmt.Fprintf(&b, "%d episodes of that season follow the change\n", r.EpisodesAffected)
	}

	if r.NotApplied {
		b.WriteString("\nsonarr still reports the old value\n")
	}

	for _, w := range r.Warnings {
		fmt.Fprintf(&b, "\nwarning: %s\n", w)
	}

	return b.String()
}

func monitoredWord(monitored bool) string {
	if monitored {
		return "monitored"
	}
	return "unmonitored"
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func have(n int) string {
	if n == 1 {
		return "has"
	}
	return "have"
}

func are(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}

func them(n int) string {
	if n == 1 {
		return "it"
	}
	return "them"
}
