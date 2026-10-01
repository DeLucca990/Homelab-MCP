package mcp

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DeLucca990/homelab-mcp/internal/sonarr"
)

// SONARR RELEASES, IMPORT, EDIT, EPISODE MONITOR, HISTORY AND CALENDAR TOOLS
//
// The Sonarr side of the same set as tool_radarr_manage.go, plus the one thing
// a series has that a film does not: per-episode monitoring.

// --- interactive search --------------------------------------------------------

type sonarrReleasesInput struct {
	EpisodeID int  `json:"episode_id,omitempty" jsonschema:"one episode, from sonarr_missing_episodes"`
	SeriesID  int  `json:"series_id,omitempty" jsonschema:"or a series, with 'season' — searches for that season, packs included"`
	Season    *int `json:"season,omitempty"`
	Limit     int  `json:"limit,omitempty" jsonschema:"default 20, maximum 100"`
}

func handleSonarrReleases(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in sonarrReleasesInput,
) (*sdk.CallToolResult, sonarr.Releases, error) {
	season := -1
	if in.Season != nil {
		season = *in.Season
	}
	if in.EpisodeID == 0 && (in.SeriesID == 0 || season < 0) {
		return nil, sonarr.Releases{}, fmt.Errorf("pass an 'episode_id', or a 'series_id' with a " +
			"'season' — an interactive search is for one episode or one season")
	}
	out, err := sonarr.GetReleases(ctx, in.SeriesID, season, in.EpisodeID, in.Limit)
	if err != nil {
		return nil, sonarr.Releases{}, err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "releases for %s: %d found, %d Sonarr would take\n", out.Target, out.TotalCount, out.ApprovedCount)
	if len(out.Releases) > 0 {
		b.WriteString("\n")
		cols := []column{
			{"ID", alignLeft}, {"#", alignRight}, {"OK", alignLeft}, {"COVERS", alignLeft},
			{"QUALITY", alignLeft}, {"SIZE", alignRight}, {"SEED", alignRight}, {"INDEXER", alignLeft},
			{"TITLE / WHY NOT", alignLeft},
		}
		rows := make([][]string, 0, len(out.Releases))
		for _, r := range out.Releases {
			ok := "yes"
			switch {
			case r.TemporarilyRejected:
				ok = "later"
			case !r.Approved:
				ok = "no"
			}
			seed := "-"
			if r.Seeders != nil {
				seed = strconv.Itoa(*r.Seeders)
			}
			title := truncateCell(r.Title, 70)
			if len(r.Rejections) > 0 {
				title += " — " + truncateCell(strings.Join(r.Rejections, "; "), 90)
			}
			rows = append(rows, []string{r.ID, strconv.Itoa(r.Rank), ok, blank(r.Episodes), r.Quality,
				sizeCell(r.SizeBytes), seed, r.Indexer, title})
		}
		b.WriteString(table(cols, rows))
	}
	writeRejectionSummary(&b, out.TopRejections)
	writeWarnings(&b, out.Warnings)
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: b.String()}}}, out, nil
}

type sonarrGrabInput struct {
	ID string `json:"id" jsonschema:"the release, from a sonarr_releases listing made in the last 30 minutes"`
}

func handleSonarrGrab(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in sonarrGrabInput,
) (*sdk.CallToolResult, sonarr.GrabResult, error) {
	plan, err := sonarr.PlanGrab(in.ID)
	if err != nil {
		return nil, sonarr.GrabResult{}, err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Download this release for %s?\n\n", plan.Target)
	fmt.Fprintf(&b, "    %s\n", plan.Title)
	fmt.Fprintf(&b, "    %s, %s, %s, from %s", blank(plan.Episodes), blank(plan.Quality), sizeCell(plan.SizeBytes), plan.Indexer)
	if plan.Seeders != nil {
		fmt.Fprintf(&b, ", %d seeders", *plan.Seeders)
	}
	b.WriteString("\n")
	writeNotes(&b, plan.Warnings)

	approved, pending, err := requireApproval(req, approval{
		message:     b.String(),
		fingerprint: fingerprint("sonarr_release_grab", strconv.Itoa(plan.SeriesID), plan.GUID(), plan.Title),
		refusal:     "nothing was grabbed",
		subject:     "grab " + plan.Title,
	})
	if !approved {
		return pending, sonarr.GrabResult{}, err
	}

	out, err := sonarr.Grab(ctx, plan)
	if err != nil {
		return nil, sonarr.GrabResult{}, err
	}
	var r strings.Builder
	fmt.Fprintf(&r, "grabbed %s for %s\n", plan.Title, plan.Target)
	writeWarnings(&r, out.Warnings)
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: r.String()}}}, out, nil
}

// --- manual import --------------------------------------------------------------

type sonarrImportCandidatesInput struct {
	QueueID int    `json:"queue_id,omitempty" jsonschema:"a download stuck on import, from a fresh sonarr_queue_status"`
	Folder  string `json:"folder,omitempty" jsonschema:"or any folder, as Sonarr's container sees it"`
}

func handleSonarrImportCandidates(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in sonarrImportCandidatesInput,
) (*sdk.CallToolResult, sonarr.ImportCandidates, error) {
	out, err := sonarr.GetImportCandidates(ctx, in.QueueID, in.Folder)
	if err != nil {
		return nil, sonarr.ImportCandidates{}, err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "files in %s\n", out.Source)
	if len(out.Candidates) > 0 {
		b.WriteString("\n")
		cols := []column{{"ID", alignLeft}, {"FILE", alignLeft}, {"SIZE", alignRight},
			{"EPISODES", alignLeft}, {"QUALITY", alignLeft}, {"OBJECTION", alignLeft}}
		rows := make([][]string, 0, len(out.Candidates))
		for _, c := range out.Candidates {
			rows = append(rows, []string{c.ID, truncateCell(baseOf(c.Path), 60), sizeCell(c.SizeBytes),
				blankAs(c.Episodes, "UNMATCHED"), blank(c.Quality), blank(strings.Join(c.Rejections, "; "))})
		}
		b.WriteString(table(cols, rows))
	}
	writeWarnings(&b, out.Warnings)
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: b.String()}}}, out, nil
}

type sonarrImportInput struct {
	IDs []string `json:"ids" jsonschema:"the files to import, from sonarr_import_candidates; each must have been matched to an episode"`
}

func handleSonarrImport(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in sonarrImportInput,
) (*sdk.CallToolResult, sonarr.ImportResult, error) {
	plan, err := sonarr.PlanImport(in.IDs)
	if err != nil {
		return nil, sonarr.ImportResult{}, err
	}

	var b strings.Builder
	b.WriteString("Import these files into the Sonarr library?\n\n")
	for _, f := range plan.Files {
		fmt.Fprintf(&b, "    %s → %s %s (%s)\n", baseOf(f.Path), f.Series, f.Episodes, blank(f.Quality))
	}
	writeNotes(&b, plan.Warnings)

	approved, pending, err := requireApproval(req, approval{
		message:     b.String(),
		fingerprint: fingerprint("sonarr_import", plan.Key()),
		refusal:     "nothing was imported",
		subject:     fmt.Sprintf("import %d file(s) into sonarr", len(plan.Files)),
	})
	if !approved {
		return pending, sonarr.ImportResult{}, err
	}

	out, err := sonarr.Import(ctx, plan)
	if err != nil {
		return nil, sonarr.ImportResult{}, err
	}
	var r strings.Builder
	fmt.Fprintf(&r, "sonarr is importing %d file(s) — command %d: %s\n", len(plan.Files), out.CommandID, blank(out.CommandStatus))
	writeWarnings(&r, out.Warnings)
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: r.String()}}}, out, nil
}

// --- edit and episode monitoring ------------------------------------------------------

type sonarrEditInput struct {
	SeriesID       int      `json:"series_id" jsonschema:"the series, from sonarr_library_status"`
	Monitored      *bool    `json:"monitored,omitempty" jsonschema:"false makes Sonarr ignore the whole series; the files stay"`
	QualityProfile string   `json:"quality_profile,omitempty" jsonschema:"by name — homelab://sonarr/quality-profiles lists them"`
	SeriesType     string   `json:"series_type,omitempty" jsonschema:"standard, daily or anime — how release names are numbered"`
	AddTags        []string `json:"add_tags,omitempty" jsonschema:"existing Sonarr tags, by label"`
	RemoveTags     []string `json:"remove_tags,omitempty"`
}

func handleSonarrEdit(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in sonarrEditInput,
) (*sdk.CallToolResult, sonarr.EditResult, error) {
	plan, err := sonarr.PlanEdit(ctx, sonarr.EditRequest{
		SeriesID:       in.SeriesID,
		Monitored:      in.Monitored,
		QualityProfile: in.QualityProfile,
		SeriesType:     in.SeriesType,
		AddTags:        in.AddTags,
		RemoveTags:     in.RemoveTags,
	})
	if err != nil {
		return nil, sonarr.EditResult{}, err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Change %s?\n\n", plan.Series.Title)
	for _, c := range plan.Changes {
		fmt.Fprintf(&b, "    %s: %s → %s\n", c.Field, c.From, c.To)
	}
	b.WriteString("\nNothing is downloaded or deleted by this.\n")
	writeNotes(&b, plan.Warnings)

	approved, pending, err := requireApproval(req, approval{
		message:     b.String(),
		fingerprint: fingerprint("sonarr_series_edit", strconv.Itoa(plan.Series.ID), plan.Summary()),
		refusal:     "the series was not changed",
		subject:     fmt.Sprintf("edit %s (%s)", plan.Series.Title, plan.Summary()),
	})
	if !approved {
		return pending, sonarr.EditResult{}, err
	}

	out, err := sonarr.Edit(ctx, plan)
	if err != nil {
		return nil, sonarr.EditResult{}, err
	}
	var r strings.Builder
	fmt.Fprintf(&r, "changed %s:\n", out.Series.Title)
	for _, c := range out.Changes {
		fmt.Fprintf(&r, "    %s: %s → %s\n", c.Field, c.From, c.To)
	}
	writeWarnings(&r, out.Warnings)
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: r.String()}}}, out, nil
}

type sonarrEpisodeMonitorInput struct {
	EpisodeIDs []int `json:"episode_ids" jsonschema:"episodes of one series, from sonarr_missing_episodes or sonarr_calendar"`
	Monitored  *bool `json:"monitored,omitempty" jsonschema:"true (the default) to have Sonarr get them, false to have it skip them"`
}

func handleSonarrEpisodeMonitor(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in sonarrEpisodeMonitorInput,
) (*sdk.CallToolResult, sonarr.EpisodeMonitorPlan, error) {
	monitored := boolOr(in.Monitored, true)
	plan, err := sonarr.PlanEpisodeMonitor(ctx, in.EpisodeIDs, monitored)
	if err != nil {
		return nil, sonarr.EpisodeMonitorPlan{}, err
	}
	word := "Monitor"
	if !monitored {
		word = "Stop monitoring"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s these episodes of %s?\n\n    %s\n", word, plan.Series, plan.Codes())
	if plan.Unchanged > 0 {
		fmt.Fprintf(&b, "\n%d other episode(s) given already have that flag.\n", plan.Unchanged)
	}
	writeNotes(&b, plan.Warnings)

	approved, pending, err := requireApproval(req, approval{
		message:     b.String(),
		fingerprint: fingerprint("sonarr_episode_monitor", plan.Series, plan.Codes(), strconv.FormatBool(monitored)),
		refusal:     "no episode was changed",
		subject:     fmt.Sprintf("%s %s %s", strings.ToLower(word), plan.Series, plan.Codes()),
	})
	if !approved {
		return pending, sonarr.EpisodeMonitorPlan{}, err
	}

	out, err := sonarr.SetEpisodesMonitored(ctx, plan)
	if err != nil {
		return nil, sonarr.EpisodeMonitorPlan{}, err
	}
	var r strings.Builder
	fmt.Fprintf(&r, "%s %s: %s\n", monitoredLabel(monitored), out.Series, out.Codes())
	writeWarnings(&r, out.Warnings)
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: r.String()}}}, out, nil
}

func monitoredLabel(v bool) string {
	if v {
		return "now monitored in"
	}
	return "no longer monitored in"
}

// --- history and calendar ---------------------------------------------------------

type sonarrHistoryInput struct {
	SeriesID int  `json:"series_id,omitempty" jsonschema:"one series, with its blocklist; omitted, the most recent events across the library"`
	Season   *int `json:"season,omitempty" jsonschema:"with series_id, only this season"`
	Limit    int  `json:"limit,omitempty" jsonschema:"default 25, maximum 200"`
}

func handleSonarrHistory(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in sonarrHistoryInput,
) (*sdk.CallToolResult, sonarr.History, error) {
	out, err := sonarr.GetHistory(ctx, in.SeriesID, in.Season, in.Limit)
	if err != nil {
		return nil, sonarr.History{}, err
	}

	var b strings.Builder
	if out.Series != "" {
		fmt.Fprintf(&b, "history of %s\n\n", out.Series)
	}
	if len(out.Events) > 0 {
		cols := []column{{"AGO", alignRight}, {"EVENT", alignLeft}}
		if out.Series == "" {
			cols = append(cols, column{"SERIES", alignLeft})
		}
		cols = append(cols, column{"EP", alignLeft}, column{"QUALITY", alignLeft},
			column{"INDEXER", alignLeft}, column{"RELEASE / DETAIL", alignLeft})
		rows := make([][]string, 0, len(out.Events))
		for _, e := range out.Events {
			row := []string{compactDuration(e.SecondsAgo), e.Event}
			if out.Series == "" {
				row = append(row, e.Series)
			}
			rel := truncateCell(e.Release, 70)
			if e.Detail != "" {
				rel += " — " + truncateCell(e.Detail, 90)
			}
			rows = append(rows, append(row, blank(e.Episode), blank(e.Quality), blank(e.Indexer), rel))
		}
		b.WriteString(table(cols, rows))
	} else {
		b.WriteString("no history\n")
	}
	if len(out.Blocklist) > 0 {
		b.WriteString("\nblocklisted:\n")
		for _, e := range out.Blocklist {
			fmt.Fprintf(&b, "    %s (%s) — %s\n", e.Release, blank(e.Indexer), blank(e.Reason))
		}
	}
	writeWarnings(&b, out.Warnings)
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: b.String()}}}, out, nil
}

func handleSonarrCalendar(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in arrCalendarInput,
) (*sdk.CallToolResult, sonarr.Calendar, error) {
	out, err := sonarr.GetCalendar(ctx, in.PastDays, in.Days)
	if err != nil {
		return nil, sonarr.Calendar{}, err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "episodes airing %s to %s\n", out.From, out.To)
	if len(out.Entries) == 0 {
		b.WriteString("\nnothing monitored airs in that window\n")
	} else {
		b.WriteString("\n")
		cols := []column{{"AIRS", alignLeft}, {"SERIES", alignLeft}, {"EP", alignLeft},
			{"HAVE", alignLeft}, {"EPISODE_ID", alignRight}, {"TITLE", alignLeft}}
		rows := make([][]string, 0, len(out.Entries))
		for _, e := range out.Entries {
			rows = append(rows, []string{e.AirDate, e.Series, e.Episode, yesNo(e.HasFile),
				strconv.Itoa(e.EpisodeID), e.Title})
		}
		b.WriteString(table(cols, rows))
	}
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: b.String()}}}, out, nil
}
