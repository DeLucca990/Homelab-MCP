package sonarr

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
)

// Removing a series is the only operation here that destroys something a person
// cannot get back by asking Sonarr again. Everything else is recoverable: an add
// can be undone by a removal, a search finds another release, a queue item comes
// back on the next grab. Deleted episode files are gone.
//
// And the loss is of a different size than Radarr's. A movie is one file; a
// series is every episode of every season, which for a long-running show is the
// largest single thing on the disk. So the two flags are kept separate, the
// count of files goes into the confirmation next to the size, and neither is
// inferred from the other.

type DeleteOptions struct {
	SeriesID           int
	DeleteFiles        bool
	AddImportExclusion bool
}

type DeleteResult struct {
	SeriesID int    `json:"series_id"`
	TvdbID   int    `json:"tvdb_id,omitempty"`
	Title    string `json:"title"`
	Year     int    `json:"year,omitempty"`

	FilesDeleted   bool   `json:"files_deleted"`
	EpisodesOnDisk int    `json:"episodes_on_disk" jsonschema:"how many episode files the series had"`
	FreedBytes     uint64 `json:"freed_bytes,omitempty" jsonschema:"how much disk the deleted files had been using; absent when no files were deleted"`
	Path           string `json:"path,omitempty" jsonschema:"the folder the series occupied"`

	ImportExclusionAdded bool `json:"import_exclusion_added"`

	StillPresent bool     `json:"still_present,omitempty" jsonschema:"true when the series was still in the library after the delete — it did not come out"`
	Warnings     []string `json:"warnings,omitempty"`
}

// Delete removes a series from the library and then checks that it actually
// left, rather than reporting success because the request returned 200.
func Delete(ctx context.Context, series Series, opts DeleteOptions) (DeleteResult, error) {
	c, err := newClient()
	if err != nil {
		return DeleteResult{}, err
	}

	res := DeleteResult{
		SeriesID:             series.ID,
		TvdbID:               series.TvdbID,
		Title:                series.Title,
		Year:                 series.Year,
		Path:                 series.Path,
		EpisodesOnDisk:       series.EpisodesOnDisk,
		FilesDeleted:         opts.DeleteFiles,
		ImportExclusionAdded: opts.AddImportExclusion,
	}
	if opts.DeleteFiles {
		res.FreedBytes = series.SizeBytes
	}

	// Sonarr spells this one addImportListExclusion, where Radarr spells it
	// addImportExclusion. The same word in the tool, a different word on the
	// wire.
	query := url.Values{
		"deleteFiles":            {strconv.FormatBool(opts.DeleteFiles)},
		"addImportListExclusion": {strconv.FormatBool(opts.AddImportExclusion)},
	}
	if err := c.delete(ctx, "/series/"+strconv.Itoa(series.ID), query); err != nil {
		return res, err
	}

	// Verify. "Deleted" is the one thing worth being sure of, and the API
	// answers before anything has necessarily been unlinked.
	if _, err := GetSeries(ctx, series.ID); err == nil {
		res.StillPresent = true
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"%s is still in the library after the delete", series.Title))
	}

	// Files left behind are not an error, but they are a surprise later: Sonarr
	// no longer knows about them, and adding the series again re-imports them.
	if !opts.DeleteFiles && series.EpisodesOnDisk > 0 {
		msg := fmt.Sprintf("the %d episode %s of %s %s still on disk",
			series.EpisodesOnDisk, plural(series.EpisodesOnDisk, "file", "files"),
			series.Title, plural(series.EpisodesOnDisk, "is", "are"))
		if series.Path != "" {
			msg += " at " + series.Path
		}
		if series.SizeBytes > 0 {
			msg += fmt.Sprintf(" (%d bytes)", series.SizeBytes)
		}
		res.Warnings = append(res.Warnings,
			msg+" — Sonarr no longer tracks them, and adding the series back would import them again")
	}

	if opts.AddImportExclusion {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"%s was added to the import list exclusions, so no import list will bring it back; "+
				"adding it by hand still works", series.Title))
	}

	return res, nil
}
