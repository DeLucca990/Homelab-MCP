package radarr

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
)

// Removing a movie is the only operation here that destroys something a person
// cannot get back by asking Radarr again. Everything else is recoverable: an add
// can be undone by a removal, a search finds another release, a queue item comes
// back on the next grab. A deleted file is gone.
//
// So the two flags are kept separate and both default to off. Radarr's own API
// does the same, and the distinction is real: forgetting a movie and erasing it
// from the disk are different requests that happen to share an endpoint.

type DeleteOptions struct {
	MovieID            int
	DeleteFiles        bool
	AddImportExclusion bool
}

type DeleteResult struct {
	MovieID int    `json:"movie_id"`
	TmdbID  int    `json:"tmdb_id,omitempty"`
	Title   string `json:"title"`
	Year    int    `json:"year,omitempty"`

	FilesDeleted bool   `json:"files_deleted"`
	FreedBytes   uint64 `json:"freed_bytes,omitempty" jsonschema:"how much disk the deleted files had been using; absent when no files were deleted"`
	Path         string `json:"path,omitempty" jsonschema:"the folder the movie occupied"`

	ImportExclusionAdded bool `json:"import_exclusion_added"`

	StillPresent bool     `json:"still_present,omitempty" jsonschema:"true when the movie was still in the library after the delete — it did not come out"`
	Warnings     []string `json:"warnings,omitempty"`
}

// Delete removes a movie from the library and then checks that it actually
// left, rather than reporting success because the request returned 200.
func Delete(ctx context.Context, movie Movie, opts DeleteOptions) (DeleteResult, error) {
	c, err := newClient()
	if err != nil {
		return DeleteResult{}, err
	}

	res := DeleteResult{
		MovieID:              movie.ID,
		TmdbID:               movie.TmdbID,
		Title:                movie.Title,
		Year:                 movie.Year,
		Path:                 movie.Path,
		FilesDeleted:         opts.DeleteFiles,
		ImportExclusionAdded: opts.AddImportExclusion,
	}
	if opts.DeleteFiles {
		res.FreedBytes = movie.SizeBytes
	}

	query := url.Values{
		"deleteFiles":        {strconv.FormatBool(opts.DeleteFiles)},
		"addImportExclusion": {strconv.FormatBool(opts.AddImportExclusion)},
	}
	if err := c.delete(ctx, "/movie/"+strconv.Itoa(movie.ID), query); err != nil {
		return res, err
	}

	// Verify. "Deleted" is the one thing worth being sure of, and the API
	// answers before anything has necessarily been unlinked.
	if _, err := GetMovie(ctx, movie.ID); err == nil {
		res.StillPresent = true
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"%s is still in the library after the delete", movie.Title))
	}

	// Files left behind are not an error, but they are a surprise later: Radarr
	// no longer knows about them, and adding the movie again re-imports them.
	if !opts.DeleteFiles && movie.HasFile {
		msg := fmt.Sprintf(
			"the files for %s are still on disk", movie.Title)
		if movie.Path != "" {
			msg += " at " + movie.Path
		}
		if movie.SizeBytes > 0 {
			msg += fmt.Sprintf(" (%d bytes)", movie.SizeBytes)
		}
		res.Warnings = append(res.Warnings,
			msg+" — Radarr no longer tracks them, and adding the movie back would import them again")
	}

	if opts.AddImportExclusion {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"%s was added to the import exclusion list, so no import list will bring it back; "+
				"adding it by hand still works", movie.Title))
	}

	return res, nil
}
