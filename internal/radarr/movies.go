package radarr

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"
)

// The library view answers "is it there, and if not, is that a problem?".
//
// Radarr's own list shows a movie as monitored with no file whether it comes
// out next year or came out in 2011 and has been failing to download ever
// since. Those are opposite situations, so they are counted separately here:
// unreleased is normal, missing is Radarr not doing its job.

// Default and ceiling for how many movies come back. A homelab library runs to
// hundreds of titles, and a full dump would bury the answer.
const (
	defaultMovieLimit = 25
	maxMovieLimit     = 200
)

type Movie struct {
	ID     int    `json:"id" jsonschema:"Radarr's own movie id"`
	TmdbID int    `json:"tmdb_id,omitempty"`
	Title  string `json:"title"`
	Year   int    `json:"year,omitempty"`

	Monitored bool `json:"monitored" jsonschema:"whether Radarr is trying to get or upgrade this movie; an unmonitored movie is ignored entirely"`
	HasFile   bool `json:"has_file" jsonschema:"whether the movie is downloaded and in the library"`

	Status      string `json:"status,omitempty" jsonschema:"tba, announced, inCinemas, released or deleted — where the film is in its release cycle"`
	IsAvailable bool   `json:"is_available" jsonschema:"whether the film has passed the minimum availability this movie was added with, which is when Radarr starts searching for it"`

	Missing bool `json:"missing,omitempty" jsonschema:"monitored, available and not downloaded — Radarr should have this and does not"`

	Quality   string `json:"quality,omitempty" jsonschema:"quality of the file on disk"`
	SizeBytes uint64 `json:"size_bytes,omitempty"`
	Path      string `json:"path,omitempty"`

	QualityProfileID    int    `json:"quality_profile_id,omitempty"`
	MinimumAvailability string `json:"minimum_availability,omitempty"`

	AddedSecondsAgo          uint64 `json:"added_seconds_ago,omitempty"`
	LastSearchSecondsAgo     uint64 `json:"last_search_seconds_ago,omitempty" jsonschema:"when Radarr last looked for this movie; absent means it never has"`
	DigitalReleaseSecondsAgo uint64 `json:"digital_release_seconds_ago,omitempty"`
}

type Library struct {
	Movies []Movie `json:"movies"`

	TotalCount      int `json:"total_count" jsonschema:"movies in the library, before any filter"`
	MonitoredCount  int `json:"monitored_count"`
	DownloadedCount int `json:"downloaded_count"`
	MissingCount    int `json:"missing_count" jsonschema:"monitored and released but not downloaded — what Radarr owes you"`
	UnreleasedCount int `json:"unreleased_count" jsonschema:"monitored, not downloaded, and not out yet — normal, not a problem"`

	SizeOnDiskBytes uint64 `json:"size_on_disk_bytes,omitempty"`

	MatchedCount int `json:"matched_count" jsonschema:"movies the filter selected, before the limit"`
	ShownCount   int `json:"shown_count"`

	Warnings []string `json:"warnings,omitempty"`
}

// MovieFilter narrows what comes back. The counts in Library always describe
// the whole library, whatever the filter is: "3 of your 412 movies match" is
// two facts and both matter.
type MovieFilter struct {
	Term          string // substring of the title, case-insensitive
	OnlyMissing   bool
	OnlyMonitored bool
	Limit         int
}

// GetLibrary reports the movies Radarr knows about and how they are doing.
func GetLibrary(ctx context.Context, f MovieFilter) (Library, error) {
	c, err := newClient()
	if err != nil {
		return Library{}, err
	}

	// excludeLocalCovers drops the per-movie cover paths, which are payload and
	// not answer. Everything else is filtered here: Radarr's /movie takes no
	// search parameter beyond tmdbId.
	var records []movieJSON
	if err := c.get(ctx, "/movie", url.Values{"excludeLocalCovers": {"true"}}, &records); err != nil {
		return Library{}, err
	}

	switch {
	case f.Limit <= 0:
		f.Limit = defaultMovieLimit
	case f.Limit > maxMovieLimit:
		f.Limit = maxMovieLimit
	}
	term := strings.ToLower(strings.TrimSpace(f.Term))

	lib := Library{TotalCount: len(records)}
	matched := make([]Movie, 0, len(records))

	for _, r := range records {
		m := r.toMovie()

		if m.Monitored {
			lib.MonitoredCount++
		}
		if m.HasFile {
			lib.DownloadedCount++
			lib.SizeOnDiskBytes += m.SizeBytes
		}
		switch {
		case m.Missing:
			lib.MissingCount++
		case m.Monitored && !m.HasFile:
			lib.UnreleasedCount++
		}

		if term != "" && !strings.Contains(strings.ToLower(m.Title), term) {
			continue
		}
		if f.OnlyMonitored && !m.Monitored {
			continue
		}
		if f.OnlyMissing && !m.Missing {
			continue
		}
		matched = append(matched, m)
	}

	lib.MatchedCount = len(matched)

	// Missing first: a listing is read from the top, and what is absent is why
	// someone is looking.
	slices.SortFunc(matched, func(a, b Movie) int {
		if d := movieSeverity(a) - movieSeverity(b); d != 0 {
			return d
		}
		return strings.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title))
	})

	if len(matched) > f.Limit {
		matched = matched[:f.Limit]
	}
	lib.Movies = matched
	lib.ShownCount = len(matched)

	lib.Warnings = libraryWarnings(lib, f)

	return lib, nil
}

func movieSeverity(m Movie) int {
	switch {
	case m.Missing:
		return 0
	case m.Monitored && !m.HasFile:
		return 1 // wanted, but not out yet
	case !m.HasFile:
		return 2
	default:
		return 3
	}
}

func libraryWarnings(lib Library, f MovieFilter) []string {
	var out []string

	if lib.MissingCount > 0 {
		out = append(out, fmt.Sprintf(
			"%d monitored %s released but not downloaded — Radarr is not finding them, "+
				"or the grabs are failing before import. radarr_movie_search starts a search "+
				"for one now; radarr_system_health says whether the indexers are answering "+
				"at all",
			lib.MissingCount, plural(lib.MissingCount, "movie is", "movies are")))
	}

	// A movie in the library that Radarr has never searched for is usually one
	// added with monitoring off by accident.
	var neverSearched int
	for _, m := range lib.Movies {
		if m.Missing && m.LastSearchSecondsAgo == 0 {
			neverSearched++
		}
	}
	if neverSearched > 0 {
		out = append(out, fmt.Sprintf(
			"%d of the movies listed have never been searched for", neverSearched))
	}

	if lib.MatchedCount > lib.ShownCount {
		out = append(out, fmt.Sprintf(
			"%d movies matched and %d are shown — raise 'limit' or narrow 'term' to see the rest",
			lib.MatchedCount, lib.ShownCount))
	}
	if lib.MatchedCount == 0 && f.Term != "" {
		out = append(out, fmt.Sprintf(
			"no movie in the library matches %q — it may not be added yet, "+
				"in which case radarr_movie_lookup finds it and radarr_movie_add adds it",
			f.Term))
	}

	return out
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// --- wire types -----------------------------------------------------------

type movieJSON struct {
	ID     int    `json:"id"`
	TmdbID int    `json:"tmdbId"`
	Title  string `json:"title"`
	Year   int    `json:"year"`

	Monitored bool   `json:"monitored"`
	HasFile   bool   `json:"hasFile"`
	Status    string `json:"status"`

	IsAvailable         bool   `json:"isAvailable"`
	MinimumAvailability string `json:"minimumAvailability"`
	QualityProfileID    int    `json:"qualityProfileId"`

	Path           string `json:"path"`
	SizeOnDisk     int64  `json:"sizeOnDisk"`
	Added          string `json:"added"`
	LastSearchTime string `json:"lastSearchTime"`
	DigitalRelease string `json:"digitalRelease"`

	MovieFile *struct {
		Quality *struct {
			Quality struct {
				Name string `json:"name"`
			} `json:"quality"`
		} `json:"quality"`
	} `json:"movieFile"`
}

func (r movieJSON) toMovie() Movie {
	m := Movie{
		ID:                       r.ID,
		TmdbID:                   r.TmdbID,
		Title:                    r.Title,
		Year:                     r.Year,
		Monitored:                r.Monitored,
		HasFile:                  r.HasFile,
		Status:                   r.Status,
		IsAvailable:              r.IsAvailable,
		MinimumAvailability:      r.MinimumAvailability,
		QualityProfileID:         r.QualityProfileID,
		Path:                     r.Path,
		AddedSecondsAgo:          secondsSince(r.Added),
		LastSearchSecondsAgo:     secondsSince(r.LastSearchTime),
		DigitalReleaseSecondsAgo: secondsSince(r.DigitalRelease),
	}
	if r.SizeOnDisk > 0 {
		m.SizeBytes = uint64(r.SizeOnDisk)
	}
	if r.MovieFile != nil && r.MovieFile.Quality != nil {
		m.Quality = r.MovieFile.Quality.Quality.Name
	}

	// The distinction the plain listing hides: Radarr only searches once a film
	// passes the minimum availability it was added with, so "monitored, no
	// file" is a problem only on the far side of that line.
	m.Missing = m.Monitored && !m.HasFile && m.IsAvailable

	return m
}
