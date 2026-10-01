package bazarr

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"sync"
)

// Bazarr's Wanted list: what the language profiles ask for and is not on disk.
//
// It is only as complete as the profiles are. A movie with no profile wants
// nothing, so it is never here however bare it is — which is why the counts
// below come with a reminder of that rather than reading as "everything else
// is fine".

type WantedMovie struct {
	RadarrID int                `json:"radarr_id"`
	Title    string             `json:"title"`
	Missing  []SubtitleLanguage `json:"missing"`
}

type WantedEpisode struct {
	SeriesID     int                `json:"series_id"`
	EpisodeID    int                `json:"episode_id"`
	SeriesTitle  string             `json:"series_title"`
	Number       string             `json:"number" jsonschema:"season and episode, as 3x07"`
	EpisodeTitle string             `json:"episode_title,omitempty"`
	Missing      []SubtitleLanguage `json:"missing"`
}

type Wanted struct {
	Movies   []WantedMovie   `json:"movies,omitempty"`
	Episodes []WantedEpisode `json:"episodes,omitempty"`

	MovieTotal   int `json:"movie_total" jsonschema:"movies missing at least one wanted subtitle, before the limit"`
	EpisodeTotal int `json:"episode_total" jsonschema:"episodes missing at least one wanted subtitle, before the limit"`

	Note string `json:"note" jsonschema:"what the list cannot show"`

	Warnings []string `json:"warnings,omitempty"`
}

// A standing fact rather than a warning: it is true of every answer, and a
// warning that is always there is one nobody reads.
const wantedNote = "this list only covers what a language profile asks for — a movie or " +
	"series with no profile wants nothing and never appears here, however few subtitles it has"

// WantedKinds selects which half of the list to read.
const (
	WantedAll      = "all"
	WantedMovies   = "movies"
	WantedEpisodes = "episodes"
)

// GetWanted reads the Wanted list, most recently added first.
func GetWanted(ctx context.Context, kind string, limit int) (Wanted, error) {
	c, err := newClient()
	if err != nil {
		return Wanted{}, err
	}

	switch kind {
	case "", WantedAll, WantedMovies, WantedEpisodes:
	default:
		return Wanted{}, fmt.Errorf("'kind' is one of %q, %q or %q, not %q",
			WantedAll, WantedMovies, WantedEpisodes, kind)
	}
	switch {
	case limit <= 0:
		limit = defaultListLimit
	case limit > maxListLimit:
		limit = maxListLimit
	}

	q := url.Values{"start": {"0"}, "length": {strconv.Itoa(limit)}}

	var (
		movies   wantedPage[wantedMovieJSON]
		episodes wantedPage[wantedEpisodeJSON]

		moviesErr, episodesErr error
		wg                     sync.WaitGroup
	)
	if kind != WantedEpisodes {
		wg.Add(1)
		go func() { defer wg.Done(); moviesErr = c.get(ctx, "/movies/wanted", q, &movies) }()
	}
	if kind != WantedMovies {
		wg.Add(1)
		go func() { defer wg.Done(); episodesErr = c.get(ctx, "/episodes/wanted", q, &episodes) }()
	}
	wg.Wait()

	if kind != WantedEpisodes && kind != WantedMovies && moviesErr != nil && episodesErr != nil {
		return Wanted{}, moviesErr
	}
	if kind == WantedMovies && moviesErr != nil {
		return Wanted{}, moviesErr
	}
	if kind == WantedEpisodes && episodesErr != nil {
		return Wanted{}, episodesErr
	}

	w := Wanted{Note: wantedNote}

	if moviesErr != nil {
		w.Warnings = append(w.Warnings, "could not read the movies Bazarr wants: "+moviesErr.Error())
	}
	if episodesErr != nil {
		w.Warnings = append(w.Warnings, "could not read the episodes Bazarr wants: "+episodesErr.Error())
	}

	w.MovieTotal = movies.Total
	for _, r := range movies.Data {
		w.Movies = append(w.Movies, WantedMovie{
			RadarrID: r.RadarrID,
			Title:    r.Title,
			Missing:  missingLanguages(r.MissingSubtitles),
		})
	}

	w.EpisodeTotal = episodes.Total
	for _, r := range episodes.Data {
		w.Episodes = append(w.Episodes, WantedEpisode{
			SeriesID:     r.SonarrSeriesID,
			EpisodeID:    r.SonarrEpisodeID,
			SeriesTitle:  r.SeriesTitle,
			Number:       r.EpisodeNumber,
			EpisodeTitle: r.EpisodeTitle,
			Missing:      missingLanguages(r.MissingSubtitles),
		})
	}

	if len(w.Movies) < w.MovieTotal || len(w.Episodes) < w.EpisodeTotal {
		w.Warnings = append(w.Warnings, fmt.Sprintf(
			"%d movies and %d episodes are wanted in total; %d and %d are shown — raise 'limit' "+
				"to see more", w.MovieTotal, w.EpisodeTotal, len(w.Movies), len(w.Episodes)))
	}
	if w.MovieTotal+w.EpisodeTotal > 0 {
		w.Warnings = append(w.Warnings, "bazarr_subtitle_search starts a search now for one movie, "+
			"episode or series; otherwise Bazarr's own scheduled search keeps retrying these")
	}
	return w, nil
}

// --- wire types -----------------------------------------------------------

type wantedPage[T any] struct {
	Data  []T `json:"data"`
	Total int `json:"total"`
}

type wantedMovieJSON struct {
	RadarrID         int            `json:"radarrId"`
	Title            string         `json:"title"`
	MissingSubtitles []languageJSON `json:"missing_subtitles"`
}

type wantedEpisodeJSON struct {
	SonarrSeriesID   int            `json:"sonarrSeriesId"`
	SonarrEpisodeID  int            `json:"sonarrEpisodeId"`
	SeriesTitle      string         `json:"seriesTitle"`
	EpisodeNumber    string         `json:"episode_number"`
	EpisodeTitle     string         `json:"episodeTitle"`
	MissingSubtitles []languageJSON `json:"missing_subtitles"`
}
