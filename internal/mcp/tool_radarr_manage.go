package mcp

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DeLucca990/homelab-mcp/internal/radarr"
)

// RADARR RELEASES, IMPORT, EDIT, HISTORY AND CALENDAR TOOLS
//
// The tools that act on what the others only diagnose: a search that found
// nothing it liked (the releases and why each was rejected, and a grab to
// override it), a download stuck on import (the files and an import to force
// it), a movie with the wrong settings (an edit), and "what happened to it"
// (its history and blocklist).

// --- interactive search --------------------------------------------------------

type radarrReleasesInput struct {
	MovieID int `json:"movie_id" jsonschema:"the movie, from radarr_library_status — Radarr's own id; a TMDB id is resolved too"`
	Limit   int `json:"limit,omitempty" jsonschema:"how many releases to list, in Radarr's order of preference; default 20, maximum 100"`
}

func handleRadarrReleases(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in radarrReleasesInput,
) (*sdk.CallToolResult, radarr.Releases, error) {
	movie, err := radarr.GetMovie(ctx, in.MovieID)
	if err != nil {
		return nil, radarr.Releases{}, err
	}
	out, err := radarr.GetReleases(ctx, movie, in.Limit)
	if err != nil {
		return nil, radarr.Releases{}, err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "releases for %s: %d found, %d Radarr would take\n", out.Movie, out.TotalCount, out.ApprovedCount)
	if len(out.Releases) > 0 {
		b.WriteString("\n")
		b.WriteString(table(releaseColumns(), releaseRows(out.Releases)))
	}
	writeRejectionSummary(&b, out.TopRejections)
	writeWarnings(&b, out.Warnings)
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: b.String()}}}, out, nil
}

func releaseColumns() []column {
	return []column{
		{"ID", alignLeft}, {"#", alignRight}, {"OK", alignLeft}, {"QUALITY", alignLeft},
		{"SIZE", alignRight}, {"SEED", alignRight}, {"AGE", alignRight}, {"INDEXER", alignLeft},
		{"TITLE / WHY NOT", alignLeft},
	}
}

func releaseRows(rels []radarr.Release) [][]string {
	rows := make([][]string, 0, len(rels))
	for _, r := range rels {
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
		rows = append(rows, []string{r.ID, strconv.Itoa(r.Rank), ok, r.Quality, sizeCell(r.SizeBytes),
			seed, compactDuration(uint64(r.AgeHours) * 3600), r.Indexer, title})
	}
	return rows
}

func writeRejectionSummary(b *strings.Builder, top map[string]int) {
	if len(top) == 0 {
		return
	}
	parts := make([]string, 0, len(top))
	for why, n := range top {
		parts = append(parts, fmt.Sprintf("%dx %s", n, why))
	}
	slices.SortFunc(parts, func(a, b string) int { return strings.Compare(b, a) })
	fmt.Fprintf(b, "\nrejected because: %s\n", strings.Join(parts, "; "))
}

func truncateCell(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

type radarrGrabInput struct {
	ID string `json:"id" jsonschema:"the release, from a radarr_releases listing made in the last 30 minutes"`
}

func handleRadarrGrab(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in radarrGrabInput,
) (*sdk.CallToolResult, radarr.GrabResult, error) {
	plan, err := radarr.PlanGrab(in.ID)
	if err != nil {
		return nil, radarr.GrabResult{}, err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Download this release of %s?\n\n", plan.Movie)
	fmt.Fprintf(&b, "    %s\n", plan.Title)
	fmt.Fprintf(&b, "    %s, %s, from %s", blank(plan.Quality), sizeCell(plan.SizeBytes), plan.Indexer)
	if plan.Seeders != nil {
		fmt.Fprintf(&b, ", %d seeders", *plan.Seeders)
	}
	b.WriteString("\n")
	writeNotes(&b, plan.Warnings)

	approved, pending, err := requireApproval(req, approval{
		message:     b.String(),
		fingerprint: fingerprint("radarr_release_grab", strconv.Itoa(plan.MovieID), plan.GUID(), plan.Title),
		refusal:     "nothing was grabbed",
		subject:     "grab " + plan.Title,
	})
	if !approved {
		return pending, radarr.GrabResult{}, err
	}

	out, err := radarr.Grab(ctx, plan)
	if err != nil {
		return nil, radarr.GrabResult{}, err
	}
	var r strings.Builder
	fmt.Fprintf(&r, "grabbed %s for %s\n", plan.Title, plan.Movie)
	writeWarnings(&r, out.Warnings)
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: r.String()}}}, out, nil
}

// --- manual import --------------------------------------------------------------

type radarrImportCandidatesInput struct {
	QueueID int    `json:"queue_id,omitempty" jsonschema:"a download stuck on import, from a fresh radarr_queue_status"`
	Folder  string `json:"folder,omitempty" jsonschema:"or any folder, as Radarr's container sees it"`
}

func handleRadarrImportCandidates(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in radarrImportCandidatesInput,
) (*sdk.CallToolResult, radarr.ImportCandidates, error) {
	out, err := radarr.GetImportCandidates(ctx, in.QueueID, in.Folder)
	if err != nil {
		return nil, radarr.ImportCandidates{}, err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "files in %s\n", out.Source)
	if len(out.Candidates) > 0 {
		b.WriteString("\n")
		cols := []column{{"ID", alignLeft}, {"FILE", alignLeft}, {"SIZE", alignRight},
			{"MOVIE", alignLeft}, {"QUALITY", alignLeft}, {"OBJECTION", alignLeft}}
		rows := make([][]string, 0, len(out.Candidates))
		for _, c := range out.Candidates {
			rows = append(rows, []string{c.ID, truncateCell(baseOf(c.Path), 60), sizeCell(c.SizeBytes),
				blankAs(c.Movie, "UNMATCHED"), blank(c.Quality), blank(strings.Join(c.Rejections, "; "))})
		}
		b.WriteString(table(cols, rows))
	}
	writeWarnings(&b, out.Warnings)
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: b.String()}}}, out, nil
}

func baseOf(path string) string {
	if i := strings.LastIndexAny(path, `/\`); i >= 0 {
		return path[i+1:]
	}
	return path
}

type radarrImportInput struct {
	IDs     []string `json:"ids" jsonschema:"the files to import, from radarr_import_candidates"`
	MovieID int      `json:"movie_id,omitempty" jsonschema:"the movie to import them as — needed when Radarr could not match a file, and overrides its match otherwise"`
}

func handleRadarrImport(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in radarrImportInput,
) (*sdk.CallToolResult, radarr.ImportResult, error) {
	plan, err := radarr.PlanImport(ctx, in.IDs, in.MovieID)
	if err != nil {
		return nil, radarr.ImportResult{}, err
	}

	var b strings.Builder
	b.WriteString("Import these files into the Radarr library?\n\n")
	for _, f := range plan.Files {
		fmt.Fprintf(&b, "    %s → %s (%s)\n", baseOf(f.Path), f.Movie, blank(f.Quality))
	}
	writeNotes(&b, plan.Warnings)

	approved, pending, err := requireApproval(req, approval{
		message:     b.String(),
		fingerprint: fingerprint("radarr_import", plan.Key()),
		refusal:     "nothing was imported",
		subject:     fmt.Sprintf("import %d file(s) into radarr", len(plan.Files)),
	})
	if !approved {
		return pending, radarr.ImportResult{}, err
	}

	out, err := radarr.Import(ctx, plan)
	if err != nil {
		return nil, radarr.ImportResult{}, err
	}
	var r strings.Builder
	fmt.Fprintf(&r, "radarr is importing %d file(s) — command %d: %s\n", len(plan.Files), out.CommandID, blank(out.CommandStatus))
	writeWarnings(&r, out.Warnings)
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: r.String()}}}, out, nil
}

// --- edit -----------------------------------------------------------------------

type radarrEditInput struct {
	MovieID             int      `json:"movie_id" jsonschema:"the movie, from radarr_library_status"`
	Monitored           *bool    `json:"monitored,omitempty" jsonschema:"false stops Radarr searching for or upgrading it; the file stays"`
	QualityProfile      string   `json:"quality_profile,omitempty" jsonschema:"by name — homelab://radarr/quality-profiles lists them"`
	MinimumAvailability string   `json:"minimum_availability,omitempty" jsonschema:"announced, inCinemas or released — when Radarr starts searching"`
	AddTags             []string `json:"add_tags,omitempty" jsonschema:"existing Radarr tags, by label"`
	RemoveTags          []string `json:"remove_tags,omitempty"`
}

func handleRadarrEdit(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in radarrEditInput,
) (*sdk.CallToolResult, radarr.EditResult, error) {
	plan, err := radarr.PlanEdit(ctx, radarr.EditRequest{
		MovieID:             in.MovieID,
		Monitored:           in.Monitored,
		QualityProfile:      in.QualityProfile,
		MinimumAvailability: in.MinimumAvailability,
		AddTags:             in.AddTags,
		RemoveTags:          in.RemoveTags,
	})
	if err != nil {
		return nil, radarr.EditResult{}, err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Change %s (%d)?\n\n", plan.Movie.Title, plan.Movie.Year)
	for _, c := range plan.Changes {
		fmt.Fprintf(&b, "    %s: %s → %s\n", c.Field, c.From, c.To)
	}
	b.WriteString("\nNothing is downloaded or deleted by this.\n")
	writeNotes(&b, plan.Warnings)

	approved, pending, err := requireApproval(req, approval{
		message:     b.String(),
		fingerprint: fingerprint("radarr_movie_edit", strconv.Itoa(plan.Movie.ID), plan.Summary()),
		refusal:     "the movie was not changed",
		subject:     fmt.Sprintf("edit %s (%s)", plan.Movie.Title, plan.Summary()),
	})
	if !approved {
		return pending, radarr.EditResult{}, err
	}

	out, err := radarr.Edit(ctx, plan)
	if err != nil {
		return nil, radarr.EditResult{}, err
	}
	var r strings.Builder
	fmt.Fprintf(&r, "changed %s:\n", out.Movie.Title)
	for _, c := range out.Changes {
		fmt.Fprintf(&r, "    %s: %s → %s\n", c.Field, c.From, c.To)
	}
	writeWarnings(&r, out.Warnings)
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: r.String()}}}, out, nil
}

// --- history and calendar ---------------------------------------------------------

type radarrHistoryInput struct {
	MovieID int `json:"movie_id,omitempty" jsonschema:"one movie, with its blocklist; omitted, the most recent events across the library"`
	Limit   int `json:"limit,omitempty" jsonschema:"default 25, maximum 200"`
}

func handleRadarrHistory(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in radarrHistoryInput,
) (*sdk.CallToolResult, radarr.History, error) {
	out, err := radarr.GetHistory(ctx, in.MovieID, in.Limit)
	if err != nil {
		return nil, radarr.History{}, err
	}

	var b strings.Builder
	if out.Movie != "" {
		fmt.Fprintf(&b, "history of %s\n\n", out.Movie)
	}
	if len(out.Events) > 0 {
		cols := []column{{"AGO", alignRight}, {"EVENT", alignLeft}}
		if out.Movie == "" {
			cols = append(cols, column{"MOVIE", alignLeft})
		}
		cols = append(cols, column{"QUALITY", alignLeft}, column{"INDEXER", alignLeft}, column{"RELEASE / DETAIL", alignLeft})
		rows := make([][]string, 0, len(out.Events))
		for _, e := range out.Events {
			row := []string{compactDuration(e.SecondsAgo), e.Event}
			if out.Movie == "" {
				row = append(row, e.Movie)
			}
			rel := truncateCell(e.Release, 70)
			if e.Detail != "" {
				rel += " — " + truncateCell(e.Detail, 90)
			}
			rows = append(rows, append(row, blank(e.Quality), blank(e.Indexer), rel))
		}
		b.WriteString(table(cols, rows))
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

type arrCalendarInput struct {
	Days     int `json:"days,omitempty" jsonschema:"how far ahead to look; maximum 90"`
	PastDays int `json:"past_days,omitempty" jsonschema:"also include the last N days — what should have arrived by now"`
}

func handleRadarrCalendar(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in arrCalendarInput,
) (*sdk.CallToolResult, radarr.Calendar, error) {
	out, err := radarr.GetCalendar(ctx, in.PastDays, in.Days)
	if err != nil {
		return nil, radarr.Calendar{}, err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "radarr release dates, %s to %s\n", out.From, out.To)
	if len(out.Entries) == 0 {
		b.WriteString("\nnothing in the library is released in that window\n")
	} else {
		b.WriteString("\n")
		cols := []column{{"DATE", alignLeft}, {"RELEASE", alignLeft}, {"MOVIE", alignLeft}, {"HAVE", alignLeft}}
		rows := make([][]string, 0, len(out.Entries))
		for _, e := range out.Entries {
			rows = append(rows, []string{e.Date, e.Release, e.Movie, yesNo(e.HasFile)})
		}
		b.WriteString(table(cols, rows))
	}
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: b.String()}}}, out, nil
}
