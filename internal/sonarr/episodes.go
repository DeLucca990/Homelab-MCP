package sonarr

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// Episodes are the unit Sonarr actually works in, and the one Radarr has no
// equivalent of. "The Expanse is missing 3 episodes" is where the series view
// stops; which three, and when they aired, is this file.
//
// There are two ways to ask, because Sonarr offers two and they do not answer
// the same question:
//
//   - /wanted/missing is Sonarr's own Wanted page: every monitored episode of
//     every monitored series that has aired and has no file, paged. It is the
//     library-wide answer and the API applies the filter, so what comes back is
//     already the truth.
//   - /episode?seriesId= is everything about one show, filtered here. It is the
//     only way to ask about one series, since the wanted endpoint takes no
//     series parameter.

const (
	defaultEpisodeLimit = 25
	maxEpisodeLimit     = 200
)

type Episode struct {
	ID       int    `json:"id" jsonschema:"Sonarr's episode id, which is what sonarr_series_search takes in 'episode_ids'"`
	SeriesID int    `json:"series_id"`
	Series   string `json:"series,omitempty"`

	Season int    `json:"season"`
	Number int    `json:"episode"`
	Code   string `json:"code" jsonschema:"the episode as a person says it, e.g. S03E05; season 0 is Sonarr's bucket for specials"`
	Title  string `json:"title,omitempty"`

	Monitored bool `json:"monitored"`
	HasFile   bool `json:"has_file"`
	Aired     bool `json:"aired"`
	Missing   bool `json:"missing,omitempty" jsonschema:"monitored, aired and not downloaded — Sonarr should have this and does not"`

	AiredSecondsAgo      uint64 `json:"aired_seconds_ago,omitempty"`
	AirsInSeconds        uint64 `json:"airs_in_seconds,omitempty" jsonschema:"how long until it airs, for an episode that has not"`
	LastSearchSecondsAgo uint64 `json:"last_search_seconds_ago,omitempty" jsonschema:"when Sonarr last looked for this episode; absent means it never has"`
}

type Missing struct {
	Episodes []Episode `json:"episodes"`

	Scope string `json:"scope" jsonschema:"what was asked about: the whole library, or one series by name"`

	TotalCount  int `json:"total_count" jsonschema:"missing episodes in scope, before the limit"`
	ShownCount  int `json:"shown_count"`
	SeriesCount int `json:"series_count,omitempty" jsonschema:"distinct series the shown episodes belong to"`

	Warnings []string `json:"warnings,omitempty"`
}

type MissingFilter struct {
	SeriesID int
	Limit    int
}

func GetMissing(ctx context.Context, f MissingFilter) (Missing, error) {
	c, err := newClient()
	if err != nil {
		return Missing{}, err
	}

	switch {
	case f.Limit <= 0:
		f.Limit = defaultEpisodeLimit
	case f.Limit > maxEpisodeLimit:
		f.Limit = maxEpisodeLimit
	}

	if f.SeriesID > 0 {
		return missingForSeries(ctx, c, f)
	}
	return missingForLibrary(ctx, c, f)
}

// missingForLibrary reads Sonarr's own Wanted page. The filtering and the paging
// are the API's, so totalRecords is the real count rather than the size of what
// happened to be fetched.
func missingForLibrary(ctx context.Context, c *client, f MissingFilter) (Missing, error) {
	var page struct {
		TotalRecords int           `json:"totalRecords"`
		Records      []episodeJSON `json:"records"`
	}
	query := url.Values{
		"page":          {"1"},
		"pageSize":      {strconv.Itoa(f.Limit)},
		"includeSeries": {"true"},
		"monitored":     {"true"},
		"sortKey":       {"episodes.airDateUtc"},
		"sortDirection": {"descending"},
	}
	if err := c.get(ctx, "/wanted/missing", query, &page); err != nil {
		return Missing{}, err
	}

	out := Missing{
		Scope:      "the whole library",
		TotalCount: page.TotalRecords,
	}
	for _, r := range page.Records {
		e := r.toEpisode()
		e.Missing = true
		out.Episodes = append(out.Episodes, e)
	}
	out.ShownCount = len(out.Episodes)
	out.SeriesCount = distinctSeries(out.Episodes)
	out.Warnings = missingWarnings(out)

	return out, nil
}

// missingForSeries asks about one show, which the wanted endpoint cannot do: it
// takes no series parameter, so the whole episode list is fetched and filtered
// here.
func missingForSeries(ctx context.Context, c *client, f MissingFilter) (Missing, error) {
	series, err := GetSeries(ctx, f.SeriesID)
	if err != nil {
		return Missing{}, err
	}

	var records []episodeJSON
	query := url.Values{"seriesId": {strconv.Itoa(series.ID)}}
	if err := c.get(ctx, "/episode", query, &records); err != nil {
		return Missing{}, err
	}

	out := Missing{Scope: series.Title}
	for _, r := range records {
		e := r.toEpisode()
		if e.Series == "" {
			e.Series = series.Title
		}
		if !e.Missing {
			continue
		}
		out.Episodes = append(out.Episodes, e)
	}
	out.TotalCount = len(out.Episodes)

	// In airing order, because a season is read forwards.
	slices.SortFunc(out.Episodes, func(a, b Episode) int {
		if d := a.Season - b.Season; d != 0 {
			return d
		}
		return a.Number - b.Number
	})

	if len(out.Episodes) > f.Limit {
		out.Episodes = out.Episodes[:f.Limit]
	}
	out.ShownCount = len(out.Episodes)
	out.SeriesCount = distinctSeries(out.Episodes)

	if !series.Monitored && out.TotalCount > 0 {
		out.Warnings = append(out.Warnings, fmt.Sprintf(
			"%s is not monitored, so Sonarr is not looking for any of these", series.Title))
	}
	out.Warnings = append(out.Warnings, missingWarnings(out)...)

	return out, nil
}

func missingWarnings(m Missing) []string {
	var out []string

	if m.TotalCount == 0 {
		return nil
	}

	var neverSearched int
	for _, e := range m.Episodes {
		if e.LastSearchSecondsAgo == 0 {
			neverSearched++
		}
	}
	if neverSearched > 0 {
		out = append(out, fmt.Sprintf(
			"%d of the episodes listed have never been searched for — "+
				"sonarr_series_search starts a search now",
			neverSearched))
	}

	if m.TotalCount > m.ShownCount {
		out = append(out, fmt.Sprintf(
			"%d episodes are missing and %d are shown — raise 'limit', or pass 'series_id' "+
				"to ask about one show", m.TotalCount, m.ShownCount))
	}

	return out
}

func distinctSeries(episodes []Episode) int {
	seen := map[int]bool{}
	for _, e := range episodes {
		seen[e.SeriesID] = true
	}
	return len(seen)
}

// GetEpisodes returns every episode of one series. It is what turns a list of
// episode ids into the codes and titles a human reads in a confirmation, and
// what proves those ids belong to the series being acted on.
func GetEpisodes(ctx context.Context, seriesID int) ([]Episode, error) {
	c, err := newClient()
	if err != nil {
		return nil, err
	}

	var records []episodeJSON
	query := url.Values{"seriesId": {strconv.Itoa(seriesID)}}
	if err := c.get(ctx, "/episode", query, &records); err != nil {
		return nil, err
	}

	out := make([]Episode, 0, len(records))
	for _, r := range records {
		out = append(out, r.toEpisode())
	}
	return out, nil
}

// EpisodeCode formats a season and episode number the way everyone writes them.
func EpisodeCode(season, episode int) string {
	return fmt.Sprintf("S%02dE%02d", season, episode)
}

// JoinEpisodeCodes names a set of episodes in a confirmation without letting a
// selection of two hundred run off the screen.
func JoinEpisodeCodes(episodes []Episode, max int) string {
	codes := make([]string, 0, len(episodes))
	for i, e := range episodes {
		if i == max {
			codes = append(codes, fmt.Sprintf("and %d more", len(episodes)-max))
			break
		}
		codes = append(codes, e.Code)
	}
	return strings.Join(codes, ", ")
}

// --- wire types -----------------------------------------------------------

type episodeJSON struct {
	ID       int `json:"id"`
	SeriesID int `json:"seriesId"`
	Series   *struct {
		Title string `json:"title"`
	} `json:"series"`

	SeasonNumber  int    `json:"seasonNumber"`
	EpisodeNumber int    `json:"episodeNumber"`
	Title         string `json:"title"`

	Monitored bool `json:"monitored"`
	HasFile   bool `json:"hasFile"`

	AirDateUtc     string `json:"airDateUtc"`
	LastSearchTime string `json:"lastSearchTime"`
}

func (r episodeJSON) toEpisode() Episode {
	e := Episode{
		ID:                   r.ID,
		SeriesID:             r.SeriesID,
		Season:               r.SeasonNumber,
		Number:               r.EpisodeNumber,
		Code:                 EpisodeCode(r.SeasonNumber, r.EpisodeNumber),
		Title:                r.Title,
		Monitored:            r.Monitored,
		HasFile:              r.HasFile,
		AiredSecondsAgo:      secondsSince(r.AirDateUtc),
		AirsInSeconds:        secondsUntil(r.AirDateUtc),
		LastSearchSecondsAgo: secondsSince(r.LastSearchTime),
	}
	if r.Series != nil {
		e.Series = r.Series.Title
	}

	e.Aired = e.AiredSecondsAgo > 0

	e.Missing = e.Monitored && !e.HasFile && e.Aired

	return e
}
