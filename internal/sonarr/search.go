package sonarr

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// Searching a series already in the library is its own operation, and without it
// the library has a one-way door: a download that is removed from the queue
// leaves the episode monitored and missing, and the only way back is to wait for
// Sonarr's next scheduled search. Adding the series again is not the answer —
// Sonarr rejects that with "This series has already been added", which is the
// correct response to the wrong request.
//
// Sonarr searches at three scales and they are three different commands. The
// scale matters here in a way it never does in Radarr: "search this series" on a
// nine-season show is several hundred grabs, and "search S03E05" is one.

const (
	seriesSearchCommand  = "SeriesSearch"
	seasonSearchCommand  = "SeasonSearch"
	episodeSearchCommand = "EpisodeSearch"
)

// SearchScope is the resolved answer to "how much of this show". It is built
// before the confirmation and hashed into the fingerprint, so an approval for
// one season cannot execute against the whole series.
type SearchScope struct {
	Series Series

	Season *int

	Episodes []Episode

	MissingEpisodes int

	SeasonMonitored bool
}

// Describe names the scope the way the confirmation and the result both need it.
func (s SearchScope) Describe() string {
	switch {
	case len(s.Episodes) > 0:
		return fmt.Sprintf("%d %s of %s (%s)",
			len(s.Episodes), plural(len(s.Episodes), "episode", "episodes"),
			s.Series.Title, JoinEpisodeCodes(s.Episodes, 8))
	case s.Season != nil:
		return fmt.Sprintf("season %d of %s", *s.Season, s.Series.Title)
	default:
		return fmt.Sprintf("every monitored episode of %s", s.Series.Title)
	}
}

// Fingerprint reduces the scope to what decides the operation.
func (s SearchScope) Fingerprint() []string {
	parts := []string{
		fmt.Sprint(s.Series.ID),
		s.Series.Title,
		fmt.Sprint(s.Series.Year),
	}
	if s.Season != nil {
		parts = append(parts, "season", fmt.Sprint(*s.Season))
	} else {
		parts = append(parts, "season", "-")
	}

	ids := make([]string, 0, len(s.Episodes))
	for _, e := range s.Episodes {
		ids = append(ids, fmt.Sprint(e.ID))
	}
	slices.Sort(ids)
	parts = append(parts, "episodes", strings.Join(ids, ","))

	return parts
}

// SearchRequest is what the caller asked for, before anything is resolved.
type SearchRequest struct {
	SeriesID   int
	Season     *int
	EpisodeIDs []int
}

// ResolveSearch turns a request into a scope, and changes nothing. It refuses
// anything it cannot name: a season the series does not have, or an episode id
// belonging to a different show.
func ResolveSearch(ctx context.Context, req SearchRequest) (SearchScope, error) {
	series, err := GetSeries(ctx, req.SeriesID)
	if err != nil {
		return SearchScope{}, err
	}

	scope := SearchScope{
		Series:          series,
		MissingEpisodes: series.EpisodesMissing,
		SeasonMonitored: true,
	}

	if len(req.EpisodeIDs) > 0 {
		episodes, err := GetEpisodes(ctx, series.ID)
		if err != nil {
			return SearchScope{}, err
		}

		byID := make(map[int]Episode, len(episodes))
		for _, e := range episodes {
			byID[e.ID] = e
		}

		// Duplicates in the request would search the same episode twice and,
		// worse, produce a different fingerprint from the same intent.
		seen := map[int]bool{}
		for _, id := range req.EpisodeIDs {
			if seen[id] {
				continue
			}
			seen[id] = true

			e, ok := byID[id]
			if !ok {
				return SearchScope{}, fmt.Errorf(
					"episode id %d does not belong to %s — sonarr_missing_episodes with "+
						"series_id %d lists the episode ids of this series",
					id, series.Title, series.ID)
			}
			scope.Episodes = append(scope.Episodes, e)
		}

		slices.SortFunc(scope.Episodes, func(a, b Episode) int {
			if d := a.Season - b.Season; d != 0 {
				return d
			}
			return a.Number - b.Number
		})

		scope.MissingEpisodes = 0
		for _, e := range scope.Episodes {
			if e.Missing {
				scope.MissingEpisodes++
			}
		}
		return scope, nil
	}

	if req.Season != nil {
		want := *req.Season
		found := false
		for _, s := range series.Seasons {
			if s.Number != want {
				continue
			}
			found = true
			scope.Season = &want
			scope.SeasonMonitored = s.Monitored
			scope.MissingEpisodes = s.EpisodesMissing
			break
		}
		if !found {
			return SearchScope{}, fmt.Errorf(
				"%s has no season %d — it has %s",
				series.Title, want, joinSeasonNumbers(series.Seasons))
		}
	}

	return scope, nil
}

type SearchResult struct {
	SeriesID int    `json:"series_id"`
	Title    string `json:"title"`
	Year     int    `json:"year,omitempty"`

	Scope           string `json:"scope" jsonschema:"what was searched for: the whole series, one season, or named episodes"`
	Season          *int   `json:"season,omitempty"`
	EpisodeIDs      []int  `json:"episode_ids,omitempty"`
	MissingEpisodes int    `json:"missing_episodes" jsonschema:"episodes in scope that Sonarr was missing when the search started"`

	CommandName   string `json:"command_name" jsonschema:"SeriesSearch, SeasonSearch or EpisodeSearch"`
	CommandID     int    `json:"command_id" jsonschema:"Sonarr's id for the search job it started"`
	CommandStatus string `json:"command_status,omitempty" jsonschema:"queued, started, completed or failed — a search that has only been queued has not found anything yet"`

	Monitored bool `json:"monitored"`

	Warnings []string `json:"warnings,omitempty"`
}

// Search asks Sonarr to look for releases now. It returns as soon as the job is
// queued: the search itself runs in Sonarr and whatever it grabs shows up in the
// download queue, which is where the outcome actually is.
func Search(ctx context.Context, scope SearchScope) (SearchResult, error) {
	c, err := newClient()
	if err != nil {
		return SearchResult{}, err
	}

	res := SearchResult{
		SeriesID:        scope.Series.ID,
		Title:           scope.Series.Title,
		Year:            scope.Series.Year,
		Scope:           scope.Describe(),
		Season:          scope.Season,
		MissingEpisodes: scope.MissingEpisodes,
		Monitored:       scope.Series.Monitored,
	}

	body := map[string]any{}
	switch {
	case len(scope.Episodes) > 0:
		ids := make([]int, 0, len(scope.Episodes))
		for _, e := range scope.Episodes {
			ids = append(ids, e.ID)
		}
		res.CommandName = episodeSearchCommand
		res.EpisodeIDs = ids
		body["name"] = episodeSearchCommand
		body["episodeIds"] = ids

	case scope.Season != nil:
		res.CommandName = seasonSearchCommand
		body["name"] = seasonSearchCommand
		body["seriesId"] = scope.Series.ID
		body["seasonNumber"] = *scope.Season

	default:
		res.CommandName = seriesSearchCommand
		body["name"] = seriesSearchCommand
		body["seriesId"] = scope.Series.ID
	}

	var command struct {
		ID     int    `json:"id"`
		Status string `json:"status"`
	}
	if err := c.post(ctx, "/command", body, &command); err != nil {
		return res, err
	}

	res.CommandID = command.ID
	res.CommandStatus = command.Status
	res.Warnings = searchWarnings(scope)

	return res, nil
}

func searchWarnings(scope SearchScope) []string {
	var out []string

	if !scope.Series.Monitored {
		out = append(out, fmt.Sprintf(
			"%s is not monitored — Sonarr may decline to grab anything for it; "+
				"monitor it first if the search finds nothing", scope.Series.Title))
	}
	if scope.Season != nil && !scope.SeasonMonitored {
		out = append(out, fmt.Sprintf(
			"season %d of %s is not monitored, so this search will probably find nothing",
			*scope.Season, scope.Series.Title))
	}

	var unmonitored, unaired int
	for _, e := range scope.Episodes {
		if !e.Monitored {
			unmonitored++
		}
		if !e.Aired {
			unaired++
		}
	}
	if unmonitored > 0 {
		out = append(out, fmt.Sprintf(
			"%d of the episodes named %s not monitored", unmonitored,
			plural(unmonitored, "is", "are")))
	}
	if unaired > 0 {
		out = append(out, fmt.Sprintf(
			"%d of the episodes named %s not aired yet, so no legitimate release exists",
			unaired, plural(unaired, "has", "have")))
	}

	if scope.MissingEpisodes == 0 {
		out = append(out, fmt.Sprintf(
			"nothing in %s is missing, so this searches for upgrades — "+
				"nothing will be grabbed unless a release beats what is on disk",
			scope.Describe()))
	}

	return out
}

func joinSeasonNumbers(seasons []Season) string {
	if len(seasons) == 0 {
		return "no seasons at all"
	}
	numbers := make([]string, 0, len(seasons))
	for _, s := range seasons {
		numbers = append(numbers, fmt.Sprint(s.Number))
	}
	return "seasons " + strings.Join(numbers, ", ")
}
