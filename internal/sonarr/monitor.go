package sonarr

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
)

// Monitoring is the switch every other Sonarr operation reads.
//
// A search finds nothing for an unmonitored season, and that is not a bug: the
// search command is dispatched with monitoredOnly, so Sonarr filters those
// episodes out before it asks an indexer anything. Without a way to flip the
// switch, two ordinary requests had no path at all — "download only season 3"
// of a show added with monitor=none, and "start following this season again" —
// because the add options Sonarr offers are presets (firstSeason, lastSeason,
// latestSeason) and none of them names an arbitrary season.
//
// It is done through the series resource rather than per episode. Sonarr's own
// UpdateSeries compares each season's flag against the stored one and calls
// SetEpisodeMonitoredBySeason for the ones that changed, so a single PUT
// cascades to every episode of that season — including episodes that do not
// exist yet, which a list of episode ids could not cover.

type SeasonMonitorRequest struct {
	SeriesID  int
	Season    int
	Monitored bool
}

// SeasonMonitorPlan is the resolved request: which show, which season, what it
// is now and what it would become. Built before the confirmation and again on
// the retry, like every other resolution in this package.
type SeasonMonitorPlan struct {
	SeriesID int    `json:"series_id"`
	Title    string `json:"title"`
	Year     int    `json:"year,omitempty"`

	SeriesMonitored bool `json:"series_monitored" jsonschema:"whether the series itself is monitored; Sonarr will not grab anything for a season of an unmonitored series, whatever the season's own flag says"`

	Season       int  `json:"season"`
	Monitored    bool `json:"monitored" jsonschema:"what the season's flag would become"`
	WasMonitored bool `json:"was_monitored" jsonschema:"what it is now"`

	AlreadySet bool `json:"already_set"`

	EpisodesTotal  int `json:"episodes_total" jsonschema:"episodes in the season, including ones that have not aired — all of them follow the season's flag"`
	EpisodesOnDisk int `json:"episodes_on_disk"`
	EpisodesAired  int `json:"episodes_aired"`

	EpisodesMissing int `json:"episodes_missing" jsonschema:"episodes of the season that have aired and are not on disk, whatever the current monitoring flag says — switching monitoring on is what makes Sonarr go and get these"`

	// The stored series resource with this one season's flag flipped. Sonarr's
	// PUT /series takes a whole resource back and uses fields nothing here
	// models, so what it returned is what gets sent.
	body map[string]any
}

// Fingerprint reduces the plan to the values that decide what happens. The
// episode count is in it because it is the size of the operation: switching on
// a season that grew from 8 episodes to 24 between the confirmation and the
// retry is not the change that was approved.
func (p SeasonMonitorPlan) Fingerprint() []string {
	return []string{
		strconv.Itoa(p.SeriesID),
		p.Title,
		strconv.Itoa(p.Season),
		strconv.FormatBool(p.Monitored),
		strconv.FormatBool(p.WasMonitored),
		strconv.Itoa(p.EpisodesTotal),
	}
}

// PlanSeasonMonitor resolves the request against the live Sonarr and changes
// nothing.
func PlanSeasonMonitor(ctx context.Context, req SeasonMonitorRequest) (SeasonMonitorPlan, error) {
	if req.SeriesID <= 0 {
		return SeasonMonitorPlan{}, fmt.Errorf(
			"a series_id is required — sonarr_library_status lists them")
	}

	c, err := newClient()
	if err != nil {
		return SeasonMonitorPlan{}, err
	}

	raw, record, err := getSeriesRaw(ctx, c, req.SeriesID)
	if err != nil {
		return SeasonMonitorPlan{}, err
	}

	series := record.toSeries()
	seasons := record.toSeasons()

	var season Season
	found := false
	for _, s := range seasons {
		if s.Number == req.Season {
			season, found = s, true
			break
		}
	}
	if !found {
		return SeasonMonitorPlan{}, fmt.Errorf("%s has no season %d — it has %s",
			series.Title, req.Season, joinSeasonNumbers(seasons))
	}

	// Counted from the episodes themselves rather than from the season
	// statistics, because the statistics are derived from the very flag this
	// operation changes. A confirmation is only worth as much as the numbers in
	// it, so a plan that cannot count is an error rather than a plan with zeros.
	counts, err := countSeason(ctx, series.ID, req.Season)
	if err != nil {
		return SeasonMonitorPlan{}, err
	}

	plan := SeasonMonitorPlan{
		SeriesID:        series.ID,
		Title:           series.Title,
		Year:            series.Year,
		SeriesMonitored: series.Monitored,
		Season:          req.Season,
		Monitored:       req.Monitored,
		WasMonitored:    season.Monitored,
		AlreadySet:      season.Monitored == req.Monitored,
		EpisodesTotal:   counts.total,
		EpisodesOnDisk:  counts.onDisk,
		EpisodesAired:   counts.aired,
		EpisodesMissing: counts.missing,
	}

	// Nothing to send when nothing would change, and nothing to build a body
	// from either — a caller that asked for the state it already has gets told
	// so rather than a write and a confirmation for a no-op.
	if plan.AlreadySet {
		return plan, nil
	}

	body, err := seriesBodyWithSeason(raw, req.Season, req.Monitored)
	if err != nil {
		return SeasonMonitorPlan{}, err
	}
	plan.body = body

	return plan, nil
}

type seasonCounts struct {
	total, onDisk, aired, missing int
}

// countSeason answers "how big is this season and how much of it is actually
// outstanding" from the episode list, which says nothing about monitoring in
// its air dates or its file ids and is therefore still true across the change.
func countSeason(ctx context.Context, seriesID, season int) (seasonCounts, error) {
	episodes, err := GetEpisodes(ctx, seriesID)
	if err != nil {
		return seasonCounts{}, err
	}

	var c seasonCounts
	for _, e := range episodes {
		if e.Season != season {
			continue
		}
		c.total++
		if e.HasFile {
			c.onDisk++
		}
		if e.Aired {
			c.aired++
			if !e.HasFile {
				c.missing++
			}
		}
	}
	return c, nil
}

// seriesBodyWithSeason flips one season's flag inside the stored resource and
// leaves every other byte of it alone. Rebuilding the resource from this
// package's own structs would drop everything they do not model — tags, the
// quality profile, the path — and PUT would write those absences back.
func seriesBodyWithSeason(raw json.RawMessage, season int, monitored bool) (map[string]any, error) {
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil || len(body) == 0 {
		return nil, fmt.Errorf("sonarr returned a series that could not be read back: %w", err)
	}

	list, ok := body["seasons"].([]any)
	if !ok {
		return nil, fmt.Errorf("sonarr returned a series with no seasons to change")
	}

	for _, entry := range list {
		s, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		// JSON numbers decode as float64, and a season number is small enough
		// that the conversion is exact.
		n, ok := s["seasonNumber"].(float64)
		if !ok || int(n) != season {
			continue
		}
		s["monitored"] = monitored
		return body, nil
	}

	return nil, fmt.Errorf("season %d disappeared from the series between reading it and changing it",
		season)
}

type SeasonMonitorResult struct {
	SeriesID int    `json:"series_id"`
	Title    string `json:"title"`
	Year     int    `json:"year,omitempty"`

	Season    int  `json:"season"`
	Monitored bool `json:"monitored" jsonschema:"the season's flag after the change"`
	Changed   bool `json:"changed" jsonschema:"false when the season was already in the requested state and nothing was written"`

	EpisodesAffected int `json:"episodes_affected" jsonschema:"episodes of the season that now follow this flag; Sonarr cascades it to all of them, including ones that have not aired"`
	EpisodesMissing  int `json:"episodes_missing" jsonschema:"episodes of the season that have aired and are not on disk — when monitoring was switched on, this is what Sonarr will go and get once something searches"`

	NotApplied bool     `json:"not_applied,omitempty" jsonschema:"true when the season did not actually come back in the requested state"`
	Warnings   []string `json:"warnings,omitempty"`
}

// SetSeasonMonitored performs the one request that changes something, then
// re-reads the season to check that it took — the API answers before the
// cascade to the episodes has necessarily finished.
func SetSeasonMonitored(ctx context.Context, plan SeasonMonitorPlan) (SeasonMonitorResult, error) {
	res := SeasonMonitorResult{
		SeriesID:         plan.SeriesID,
		Title:            plan.Title,
		Year:             plan.Year,
		Season:           plan.Season,
		Monitored:        plan.Monitored,
		EpisodesAffected: plan.EpisodesTotal,
		EpisodesMissing:  plan.EpisodesMissing,
	}

	if plan.AlreadySet {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"season %d of %s was already %s — nothing was changed",
			plan.Season, plan.Title, monitoredWord(plan.Monitored)))
		return res, nil
	}

	c, err := newClient()
	if err != nil {
		return SeasonMonitorResult{}, err
	}

	if err := c.put(ctx, "/series/"+strconv.Itoa(plan.SeriesID), plan.body, nil); err != nil {
		return res, err
	}
	res.Changed = true

	// Verify the flag, and only the flag. The counts are deliberately kept as
	// the plan measured them: the season statistics Sonarr reports back are
	// derived from this very flag, so re-reading them would rewrite "13 episodes
	// to fetch" into whatever the new monitoring state implies — which is how
	// the number the user just approved would stop matching the result.
	if series, err := GetSeries(ctx, plan.SeriesID); err == nil {
		for _, s := range series.Seasons {
			if s.Number != plan.Season {
				continue
			}
			if s.Monitored != plan.Monitored {
				res.NotApplied = true
				res.Warnings = append(res.Warnings, fmt.Sprintf(
					"season %d of %s still reads as %s after the change",
					plan.Season, plan.Title, monitoredWord(s.Monitored)))
			}
			break
		}
	}

	res.Warnings = append(res.Warnings, seasonMonitorWarnings(plan, res)...)

	return res, nil
}

func seasonMonitorWarnings(plan SeasonMonitorPlan, res SeasonMonitorResult) []string {
	var out []string

	if !plan.Monitored {
		out = append(out, fmt.Sprintf(
			"nothing will be searched for season %d from now on; anything already downloading "+
				"for it stays in the queue and still imports",
			plan.Season))
		return out
	}

	// The switch that outranks this one. Sonarr rejects a grab for an episode of
	// an unmonitored series whatever the season says, so switching the season on
	// and stopping there leaves someone waiting for downloads that cannot start.
	if !plan.SeriesMonitored {
		out = append(out, fmt.Sprintf(
			"%s itself is not monitored, so Sonarr will still not grab anything for this "+
				"season — the series-level switch has to be turned on in Sonarr's own UI",
			plan.Title))
	}

	// Monitoring is not a search. Without this, the natural reading of a
	// successful result is that episodes are on their way.
	if res.EpisodesMissing > 0 {
		out = append(out, fmt.Sprintf(
			"%d aired %s of season %d %s still missing, and monitoring does not start a "+
				"search — sonarr_series_search with series_id %d and season %d starts one now, "+
				"otherwise Sonarr waits for its next scheduled pass",
			res.EpisodesMissing, plural(res.EpisodesMissing, "episode", "episodes"),
			plan.Season, plural(res.EpisodesMissing, "is", "are"),
			plan.SeriesID, plan.Season))
	}

	return out
}

func monitoredWord(monitored bool) string {
	if monitored {
		return "monitored"
	}
	return "unmonitored"
}
