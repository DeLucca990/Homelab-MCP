package sonarr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// The library view answers "is it there, and if not, how much of it is
// missing?".
//
// This is where Sonarr differs from Radarr in kind rather than in vocabulary. A
// movie is one file and is either there or not; a series is a hundred files and
// is almost never either. "The Expanse: monitored" says nothing — the question
// is always how many episodes are on disk out of how many Sonarr owes you, and
// that is what this file computes.

// Default and ceiling for how many series come back. A homelab library runs to
// hundreds of shows, and a full dump would bury the answer.
const (
	defaultSeriesLimit = 25
	maxSeriesLimit     = 200
)

type Season struct {
	Number    int  `json:"season"`
	Monitored bool `json:"monitored"`

	EpisodesWanted  int `json:"episodes_wanted" jsonschema:"episodes of this season that have aired and are monitored, plus any already downloaded"`
	EpisodesOnDisk  int `json:"episodes_on_disk"`
	EpisodesMissing int `json:"episodes_missing" jsonschema:"aired, monitored and not downloaded — what Sonarr owes you for this season"`
	EpisodesTotal   int `json:"episodes_total" jsonschema:"every episode of the season, including ones that have not aired"`

	SizeBytes uint64 `json:"size_bytes,omitempty"`
}

type Series struct {
	ID     int    `json:"id" jsonschema:"Sonarr's own series id, which is what sonarr_series_search and sonarr_series_remove take"`
	TvdbID int    `json:"tvdb_id,omitempty" jsonschema:"TheTVDB's id — a different number from 'id', and what sonarr_series_add takes"`
	Title  string `json:"title"`
	Year   int    `json:"year,omitempty"`

	Monitored bool   `json:"monitored" jsonschema:"whether Sonarr is trying to get episodes of this series at all; an unmonitored series is ignored entirely"`
	Status    string `json:"status,omitempty" jsonschema:"continuing, ended, upcoming or deleted — where the show is in its life, not whether you have it"`
	Network   string `json:"network,omitempty"`

	SeasonCount     int     `json:"season_count,omitempty"`
	EpisodesTotal   int     `json:"episodes_total" jsonschema:"every episode the series has, including ones that have not aired"`
	EpisodesWanted  int     `json:"episodes_wanted" jsonschema:"episodes that have aired and are monitored, plus any already downloaded — the denominator of the progress bar"`
	EpisodesOnDisk  int     `json:"episodes_on_disk"`
	EpisodesMissing int     `json:"episodes_missing" jsonschema:"aired, monitored and not downloaded — what Sonarr owes you"`
	PercentComplete float64 `json:"percent_complete" jsonschema:"episodes_on_disk out of episodes_wanted, 0-100"`

	Missing bool `json:"missing,omitempty" jsonschema:"monitored and short of at least one aired episode — Sonarr should have more of this than it does"`

	SizeBytes uint64 `json:"size_bytes,omitempty"`
	Path      string `json:"path,omitempty"`

	QualityProfileID int    `json:"quality_profile_id,omitempty"`
	SeriesType       string `json:"series_type,omitempty" jsonschema:"standard, daily or anime — it decides how Sonarr parses release names"`

	AddedSecondsAgo         uint64 `json:"added_seconds_ago,omitempty"`
	NextAiringInSeconds     uint64 `json:"next_airing_in_seconds,omitempty" jsonschema:"how long until the next episode airs; absent means nothing is scheduled, which for a continuing show means the network has announced no date"`
	PreviousAiredSecondsAgo uint64 `json:"previous_aired_seconds_ago,omitempty"`

	Seasons []Season `json:"seasons,omitempty" jsonschema:"per-season breakdown, included only when the filter selects one series"`
}

type Library struct {
	Series []Series `json:"series"`

	TotalCount      int `json:"total_count" jsonschema:"series in the library, before any filter"`
	MonitoredCount  int `json:"monitored_count"`
	ContinuingCount int `json:"continuing_count" jsonschema:"series still airing, which are the ones that will keep needing episodes"`
	MissingCount    int `json:"missing_count" jsonschema:"series short of at least one aired episode"`
	CompleteCount   int `json:"complete_count" jsonschema:"monitored series with every aired episode on disk"`

	EpisodesOnDisk  int `json:"episodes_on_disk"`
	EpisodesMissing int `json:"episodes_missing" jsonschema:"aired, monitored episodes with no file, across the whole library — sonarr_missing_episodes lists them one by one"`

	SizeOnDiskBytes uint64 `json:"size_on_disk_bytes,omitempty"`

	MatchedCount int `json:"matched_count" jsonschema:"series the filter selected, before the limit"`
	ShownCount   int `json:"shown_count"`

	Warnings []string `json:"warnings,omitempty"`
}

// SeriesFilter narrows what comes back. The counts in Library always describe
// the whole library, whatever the filter is: "3 of your 118 shows match" is two
// facts and both matter.
type SeriesFilter struct {
	Term          string
	OnlyMissing   bool
	OnlyMonitored bool
	Limit         int
}

// GetLibrary reports the series Sonarr knows about and how complete each is.
func GetLibrary(ctx context.Context, f SeriesFilter) (Library, error) {
	c, err := newClient()
	if err != nil {
		return Library{}, err
	}

	var records []seriesJSON
	if err := c.get(ctx, "/series", nil, &records); err != nil {
		return Library{}, err
	}

	switch {
	case f.Limit <= 0:
		f.Limit = defaultSeriesLimit
	case f.Limit > maxSeriesLimit:
		f.Limit = maxSeriesLimit
	}
	term := strings.ToLower(strings.TrimSpace(f.Term))

	lib := Library{TotalCount: len(records)}
	matched := make([]Series, 0, len(records))

	for _, r := range records {
		s := r.toSeries()

		if s.Monitored {
			lib.MonitoredCount++
		}
		if strings.EqualFold(s.Status, "continuing") {
			lib.ContinuingCount++
		}
		lib.EpisodesOnDisk += s.EpisodesOnDisk
		lib.SizeOnDiskBytes += s.SizeBytes
		if s.Monitored {
			lib.EpisodesMissing += s.EpisodesMissing
		}
		switch {
		case s.Missing:
			lib.MissingCount++
		case s.Monitored:
			lib.CompleteCount++
		}

		if term != "" && !strings.Contains(strings.ToLower(s.Title), term) {
			continue
		}
		if f.OnlyMonitored && !s.Monitored {
			continue
		}
		if f.OnlyMissing && !s.Missing {
			continue
		}
		matched = append(matched, s)
	}

	lib.MatchedCount = len(matched)

	// Incomplete first, and among those the emptiest: a listing is read from the
	// top, and what is absent is why someone is looking.
	slices.SortFunc(matched, func(a, b Series) int {
		if d := seriesSeverity(a) - seriesSeverity(b); d != 0 {
			return d
		}
		if d := b.EpisodesMissing - a.EpisodesMissing; d != 0 {
			return d
		}
		return strings.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title))
	})

	if len(matched) > f.Limit {
		matched = matched[:f.Limit]
	}
	lib.Series = matched
	lib.ShownCount = len(matched)

	// The per-season breakdown is the answer to "which season is incomplete",
	// and only makes sense once the question is about one show.
	if len(lib.Series) == 1 {
		for _, r := range records {
			if r.ID == lib.Series[0].ID {
				lib.Series[0].Seasons = r.toSeasons()
				break
			}
		}
	}

	lib.Warnings = libraryWarnings(lib, f)

	return lib, nil
}

func seriesSeverity(s Series) int {
	switch {
	case s.Missing:
		return 0
	case s.Monitored && s.EpisodesTotal > s.EpisodesWanted && s.EpisodesOnDisk == 0:
		return 1
	case !s.Monitored && s.EpisodesOnDisk < s.EpisodesWanted:
		return 2
	default:
		return 3
	}
}

func libraryWarnings(lib Library, f SeriesFilter) []string {
	var out []string

	if lib.EpisodesMissing > 0 {
		out = append(out, fmt.Sprintf(
			"%d aired %s missing across %d %s — sonarr_missing_episodes lists them, "+
				"sonarr_series_search starts a search for one show or season, and "+
				"sonarr_system_health says whether the indexers are answering at all",
			lib.EpisodesMissing, plural(lib.EpisodesMissing, "episode is", "episodes are"),
			lib.MissingCount, plural(lib.MissingCount, "series", "series")))
	}

	// A series with nothing on disk at all is a different problem from one that
	// is a few episodes short: usually an add that never found anything.
	var empty []string
	for _, s := range lib.Series {
		if s.Missing && s.EpisodesOnDisk == 0 {
			empty = append(empty, s.Title)
		}
	}
	if len(empty) > 0 {
		out = append(out, fmt.Sprintf(
			"nothing at all has been downloaded for %s", strings.Join(empty, ", ")))
	}

	if lib.MatchedCount > lib.ShownCount {
		out = append(out, fmt.Sprintf(
			"%d series matched and %d are shown — raise 'limit' or narrow 'term' to see the rest",
			lib.MatchedCount, lib.ShownCount))
	}
	if lib.MatchedCount == 0 && f.Term != "" {
		out = append(out, fmt.Sprintf(
			"no series in the library matches %q — it may not be added yet, "+
				"in which case sonarr_series_lookup finds it and sonarr_series_add adds it",
			f.Term))
	}

	return out
}

// GetSeries resolves one series so an operation names the show it is about
// rather than a number — in the confirmation and in the result.
//
// It takes Sonarr's own id, and falls back to reading the number as a TVDB id.
// The fallback is not politeness: a series carries both, they are both plain
// integers, and nothing about the value says which space it belongs to, so the
// wrong one gets passed and the endpoint answers 404. Telling the caller off
// leaves a correct request unfulfilled over a naming detail.
//
// Which one matched is visible to the caller without another return value:
// series.ID differs from what was asked for exactly when the fallback resolved
// it. The confirmation says so, because on a destructive operation the show is
// what the human should be checking, not the id.
func GetSeries(ctx context.Context, seriesID int) (Series, error) {
	if seriesID <= 0 {
		return Series{}, fmt.Errorf("a series_id is required — sonarr_library_status lists them")
	}

	c, err := newClient()
	if err != nil {
		return Series{}, err
	}

	record, err := getSeriesRecord(ctx, c, seriesID)
	if err != nil {
		return Series{}, err
	}

	s := record.toSeries()
	s.Seasons = record.toSeasons()
	return s, nil
}

// getSeriesRecord is the raw resolution, kept separate because the add path
// needs the record itself rather than the reduced view.
func getSeriesRecord(ctx context.Context, c *client, seriesID int) (seriesJSON, error) {
	_, record, err := getSeriesRaw(ctx, c, seriesID)
	return record, err
}

// getSeriesRaw is the same resolution, keeping the bytes as well as the reduced
// view. Sonarr's PUT /series takes a whole series resource back, and a resource
// this code rebuilt from its own struct would silently drop every field the
// struct does not model — so what came off the wire is what goes back on it.
//
// Decoding the same bytes twice, rather than returning one and re-fetching for
// the other, is what keeps the two views describing the same instant.
func getSeriesRaw(ctx context.Context, c *client, seriesID int) (json.RawMessage, seriesJSON, error) {
	decode := func(raw json.RawMessage) (json.RawMessage, seriesJSON, error) {
		var record seriesJSON
		if err := json.Unmarshal(raw, &record); err != nil {
			return nil, seriesJSON{}, fmt.Errorf(
				"sonarr returned a series that could not be read: %w", err)
		}
		return raw, record, nil
	}

	var byID json.RawMessage
	if err := c.get(ctx, "/series/"+strconv.Itoa(seriesID), nil, &byID); err == nil {
		if raw, record, err := decode(byID); err == nil && record.ID != 0 {
			return raw, record, nil
		}
	}

	// Sonarr filters its own listing by tvdbId, so this stays one request.
	var byTvdb []json.RawMessage
	err := c.get(ctx, "/series", url.Values{"tvdbId": {strconv.Itoa(seriesID)}}, &byTvdb)
	if err == nil && len(byTvdb) == 1 {
		return decode(byTvdb[0])
	}

	return nil, seriesJSON{}, fmt.Errorf(
		"no series in the library with Sonarr id %d, and none with TVDB id %d either — "+
			"sonarr_library_status lists what is there, with both numbers",
		seriesID, seriesID)
}

// --- wire types -----------------------------------------------------------

type seriesStatisticsJSON struct {
	SeasonCount       int     `json:"seasonCount"`
	EpisodeFileCount  int     `json:"episodeFileCount"`
	EpisodeCount      int     `json:"episodeCount"`
	TotalEpisodeCount int     `json:"totalEpisodeCount"`
	SizeOnDisk        int64   `json:"sizeOnDisk"`
	PercentOfEpisodes float64 `json:"percentOfEpisodes"`
}

type seasonJSON struct {
	SeasonNumber int                   `json:"seasonNumber"`
	Monitored    bool                  `json:"monitored"`
	Statistics   *seriesStatisticsJSON `json:"statistics"`
}

type seriesJSON struct {
	ID     int    `json:"id"`
	TvdbID int    `json:"tvdbId"`
	Title  string `json:"title"`
	Year   int    `json:"year"`

	Monitored bool   `json:"monitored"`
	Status    string `json:"status"`
	Ended     bool   `json:"ended"`
	Network   string `json:"network"`

	Path             string `json:"path"`
	QualityProfileID int    `json:"qualityProfileId"`
	SeasonFolder     bool   `json:"seasonFolder"`
	SeriesType       string `json:"seriesType"`

	Added         string `json:"added"`
	NextAiring    string `json:"nextAiring"`
	PreviousAired string `json:"previousAiring"`

	Seasons    []seasonJSON          `json:"seasons"`
	Statistics *seriesStatisticsJSON `json:"statistics"`
}

func (r seriesJSON) toSeries() Series {
	s := Series{
		ID:                      r.ID,
		TvdbID:                  r.TvdbID,
		Title:                   r.Title,
		Year:                    r.Year,
		Monitored:               r.Monitored,
		Status:                  r.Status,
		Network:                 r.Network,
		Path:                    r.Path,
		QualityProfileID:        r.QualityProfileID,
		SeriesType:              r.SeriesType,
		AddedSecondsAgo:         secondsSince(r.Added),
		NextAiringInSeconds:     secondsUntil(r.NextAiring),
		PreviousAiredSecondsAgo: secondsSince(r.PreviousAired),
	}

	if st := r.Statistics; st != nil {
		s.SeasonCount = st.SeasonCount
		s.EpisodesTotal = st.TotalEpisodeCount
		s.EpisodesWanted = st.EpisodeCount
		s.EpisodesOnDisk = st.EpisodeFileCount
		if st.SizeOnDisk > 0 {
			s.SizeBytes = uint64(st.SizeOnDisk)
		}
	}
	if s.SeasonCount == 0 {
		s.SeasonCount = len(r.Seasons)
	}

	s.EpisodesMissing = missingOf(s.EpisodesWanted, s.EpisodesOnDisk)
	s.PercentComplete = percentOf(s.EpisodesOnDisk, s.EpisodesWanted)
	s.Missing = s.Monitored && s.EpisodesMissing > 0

	return s
}

func (r seriesJSON) toSeasons() []Season {
	out := make([]Season, 0, len(r.Seasons))
	for _, sn := range r.Seasons {
		season := Season{Number: sn.SeasonNumber, Monitored: sn.Monitored}
		if st := sn.Statistics; st != nil {
			season.EpisodesWanted = st.EpisodeCount
			season.EpisodesOnDisk = st.EpisodeFileCount
			season.EpisodesTotal = st.TotalEpisodeCount
			if st.SizeOnDisk > 0 {
				season.SizeBytes = uint64(st.SizeOnDisk)
			}
		}
		season.EpisodesMissing = missingOf(season.EpisodesWanted, season.EpisodesOnDisk)
		out = append(out, season)
	}
	return out
}

// Sonarr counts a downloaded episode as wanted even when it is unmonitored, so
// the difference cannot go negative in practice — but it is a subtraction of two
// numbers from a remote database, and a negative "missing" would read as a fact.
func missingOf(wanted, onDisk int) int {
	if wanted <= onDisk {
		return 0
	}
	return wanted - onDisk
}

func percentOf(onDisk, wanted int) float64 {
	if wanted <= 0 {
		return 0
	}
	return round1(float64(onDisk) / float64(wanted) * 100)
}
