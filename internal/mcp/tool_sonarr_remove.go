package mcp

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DeLucca990/homelab-mcp/internal/sonarr"
	"github.com/DeLucca990/homelab-mcp/internal/system"
)

// SONARR LIBRARY REMOVAL TOOL
//
// The one operation in this family that destroys something no amount of asking
// Sonarr again brings back — and the largest one in the server, because a series
// is not one file but every episode of every season. It gets the same
// confirmation as the rest, with a message written to be read rather than
// skimmed: it names how many files are about to disappear and how much disk they
// occupy, and it states which of the two things is happening — forgetting the
// show, or erasing it.
//
// delete_files defaults to TRUE here, unlike Sonarr's own API: removing a series
// from a homelab library normally means reclaiming the disk, and an entry that
// disappears while 400 GB stay behind is its own kind of surprise. The cost is
// that the destructive reading is the default one, which is why the confirmation
// states the size in capitals and why passing false is offered in the same
// breath.

type sonarrRemoveInput struct {
	SeriesID int `json:"series_id" jsonschema:"the series to remove, from sonarr_library_status. Sonarr's own 'id' field is what this expects; a TVDB id is accepted too and resolved, and the confirmation says which show it landed on"`

	DeleteFiles        *bool `json:"delete_files,omitempty" jsonschema:"also erase every downloaded episode from disk; defaults to TRUE. Pass false to remove the series from Sonarr's library while leaving the files where they are — deletion cannot be undone"`
	AddImportExclusion *bool `json:"add_import_exclusion,omitempty" jsonschema:"also stop any import list from adding this series back automatically; defaults to false"`
}

func handleSonarrSeriesRemove(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in sonarrRemoveInput,
) (*sdk.CallToolResult, sonarr.DeleteResult, error) {
	if in.SeriesID <= 0 {
		return nil, sonarr.DeleteResult{}, fmt.Errorf(
			"'series_id' is required — sonarr_library_status lists the series with their ids")
	}

	opts := sonarr.DeleteOptions{
		SeriesID:           in.SeriesID,
		DeleteFiles:        boolOr(in.DeleteFiles, true),
		AddImportExclusion: boolOr(in.AddImportExclusion, false),
	}

	series, err := sonarr.GetSeries(ctx, in.SeriesID)
	if err != nil {
		return nil, sonarr.DeleteResult{}, err
	}

	approved, pending, err := requireApproval(req, approval{
		message: sonarrRemoveMessage(series, opts, in.SeriesID),
		fingerprint: fingerprint(
			"sonarr_series_remove",
			strconv.Itoa(series.ID),
			series.Title,
			strconv.Itoa(series.Year),
			series.Path,
			strconv.Itoa(series.EpisodesOnDisk),
			strconv.FormatBool(opts.DeleteFiles),
			strconv.FormatBool(opts.AddImportExclusion),
		),
		refusal: "series not removed",
		subject: fmt.Sprintf("remove %s (%d) from the sonarr library, delete_files=%t",
			series.Title, series.Year, opts.DeleteFiles),
	})
	if !approved {
		return pending, sonarr.DeleteResult{}, err
	}

	out, err := sonarr.Delete(ctx, series, opts)
	if err != nil {
		return nil, sonarr.DeleteResult{}, err
	}

	return &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.TextContent{Text: renderSonarrRemoveResult(out)},
		},
	}, out, nil
}

func sonarrRemoveMessage(s sonarr.Series, opts sonarr.DeleteOptions, requestedID int) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Remove this series from the Sonarr library?\n\n")
	fmt.Fprintf(&b, "    %s (%d)   [series %d]\n", s.Title, s.Year, s.ID)
	if requestedID != s.ID {
		fmt.Fprintf(&b, "    (you gave %d, which is its TVDB id — Sonarr's id for it is %d)\n",
			requestedID, s.ID)
	}
	fmt.Fprintf(&b, "    state: %s, %d of %d episodes on disk\n",
		seriesStateCell(s), s.EpisodesOnDisk, s.EpisodesWanted)
	if s.Path != "" {
		fmt.Fprintf(&b, "    folder: %s\n", s.Path)
	}

	switch {
	case opts.DeleteFiles && s.EpisodesOnDisk > 0:
		fmt.Fprintf(&b, "\nALL %d EPISODE FILES WILL BE DELETED — %s freed. "+
			"This cannot be undone, and getting the show back means downloading it again.\n",
			s.EpisodesOnDisk, system.CompactBytes(s.SizeBytes))
	case opts.DeleteFiles:
		b.WriteString("\nFile deletion was requested, but this series has no files on disk, " +
			"so only the library entry goes away.\n")
	case s.EpisodesOnDisk > 0:
		fmt.Fprintf(&b, "\nThe %d episode files stay on disk (%s in the folder above) and Sonarr "+
			"stops tracking them. Only the library entry goes away.\n",
			s.EpisodesOnDisk, system.CompactBytes(s.SizeBytes))
	default:
		b.WriteString("\nThe series has no files, so only the library entry goes away.\n")
	}

	if opts.AddImportExclusion {
		b.WriteString("\nIt will also be EXCLUDED from import lists, so nothing adds it back " +
			"automatically.\n")
	}

	return b.String()
}

func renderSonarrRemoveResult(r sonarr.DeleteResult) string {
	var b strings.Builder

	fmt.Fprintf(&b, "removed %s (%d) from the sonarr library\n", r.Title, r.Year)
	fmt.Fprintf(&b, "files deleted: %s", yesNo(r.FilesDeleted))
	if r.FilesDeleted && r.EpisodesOnDisk > 0 {
		fmt.Fprintf(&b, " (%d episodes, %s freed)", r.EpisodesOnDisk,
			system.CompactBytes(r.FreedBytes))
	}
	fmt.Fprintf(&b, "   import exclusion: %s\n", yesNo(r.ImportExclusionAdded))

	if r.StillPresent {
		b.WriteString("\nit is still showing in the library\n")
	}

	for _, w := range r.Warnings {
		fmt.Fprintf(&b, "\nwarning: %s\n", w)
	}

	return b.String()
}
