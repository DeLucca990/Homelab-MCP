package sonarr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// History answers "what happened to this episode": which release was grabbed
// from which indexer, whether it imported, whether it failed and why, whether
// a better file replaced it. The blocklist is the other half — releases Sonarr
// will never grab again. Also here: the calendar, and the download client
// test the health tool runs on request.

const (
	defaultHistoryLimit = 25
	maxHistoryLimit     = 200
)

type HistoryEvent struct {
	Event      string `json:"event" jsonschema:"grabbed, imported, failed, deleted, renamed or ignored"`
	Series     string `json:"series,omitempty"`
	Episode    string `json:"episode,omitempty" jsonschema:"e.g. S02E03"`
	Release    string `json:"release"`
	Quality    string `json:"quality,omitempty"`
	Indexer    string `json:"indexer,omitempty"`
	Client     string `json:"download_client,omitempty"`
	Detail     string `json:"detail,omitempty" jsonschema:"the reason Sonarr recorded"`
	SecondsAgo uint64 `json:"seconds_ago"`
}

type BlocklistEntry struct {
	Release    string `json:"release"`
	Indexer    string `json:"indexer,omitempty"`
	Reason     string `json:"reason,omitempty"`
	SecondsAgo uint64 `json:"seconds_ago"`
}

type History struct {
	SeriesID  int              `json:"series_id,omitempty"`
	Series    string           `json:"series,omitempty"`
	Season    *int             `json:"season,omitempty"`
	Events    []HistoryEvent   `json:"events" jsonschema:"most recent first"`
	Blocklist []BlocklistEntry `json:"blocklist,omitempty"`
	Warnings  []string         `json:"warnings,omitempty"`
}

// GetHistory reads one series' history (optionally one season) and its
// blocklist, or the most recent events across the library when seriesID is 0.
func GetHistory(ctx context.Context, seriesID int, season *int, limit int) (History, error) {
	c, err := newClient()
	if err != nil {
		return History{}, err
	}
	switch {
	case limit <= 0:
		limit = defaultHistoryLimit
	case limit > maxHistoryLimit:
		limit = maxHistoryLimit
	}

	var out History
	var raw []historyJSON

	if seriesID > 0 {
		s, err := GetSeries(ctx, seriesID)
		if err != nil {
			return History{}, err
		}
		out.SeriesID, out.Series, out.Season = s.ID, s.Title, season
		q := url.Values{"seriesId": {strconv.Itoa(s.ID)}, "includeEpisode": {"true"}}
		if season != nil {
			q.Set("seasonNumber", strconv.Itoa(*season))
		}
		if err := c.get(ctx, "/history/series", q, &raw); err != nil {
			return History{}, err
		}
		slices.SortStableFunc(raw, func(a, b historyJSON) int { return strings.Compare(b.Date, a.Date) })

		var block struct {
			Records []blocklistJSON `json:"records"`
		}
		bq := url.Values{"page": {"1"}, "pageSize": {"200"}, "seriesIds": {strconv.Itoa(s.ID)}}
		if err := c.get(ctx, "/blocklist", bq, &block); err == nil {
			for _, b := range block.Records {
				// Older Sonarr ignores the filter, so it is applied here too.
				if b.SeriesID != s.ID {
					continue
				}
				out.Blocklist = append(out.Blocklist, BlocklistEntry{Release: b.SourceTitle,
					Indexer: b.Indexer, Reason: b.Message, SecondsAgo: secondsSince(b.Date)})
			}
		}
	} else {
		var page struct {
			Records []historyJSON `json:"records"`
		}
		q := url.Values{"page": {"1"}, "pageSize": {strconv.Itoa(limit)}, "sortKey": {"date"},
			"sortDirection": {"descending"}, "includeSeries": {"true"}, "includeEpisode": {"true"}}
		if err := c.get(ctx, "/history", q, &page); err != nil {
			return History{}, err
		}
		raw = page.Records
	}

	for _, r := range raw {
		out.Events = append(out.Events, r.toEvent())
		if len(out.Events) == limit {
			break
		}
	}

	failed := 0
	for _, e := range out.Events {
		if e.Event == "failed" {
			failed++
		}
	}
	if failed >= 3 && seriesID > 0 {
		out.Warnings = append(out.Warnings, fmt.Sprintf("%d downloads of %s failed — the details "+
			"say why; a run of failures from one indexer usually means fake or passworded releases",
			failed, out.Series))
	}
	if len(out.Blocklist) > 0 {
		out.Warnings = append(out.Warnings, fmt.Sprintf("%d release(s) of %s are blocklisted and "+
			"will not be grabbed again", len(out.Blocklist), out.Series))
	}
	return out, nil
}

// --- calendar ----------------------------------------------------------------

type CalendarEntry struct {
	SeriesID  int    `json:"series_id"`
	Series    string `json:"series"`
	EpisodeID int    `json:"episode_id"`
	Episode   string `json:"episode"`
	Title     string `json:"title,omitempty"`
	AirDate   string `json:"air_date" jsonschema:"YYYY-MM-DD HH:MM, local to this server"`
	HasFile   bool   `json:"has_file"`
	Monitored bool   `json:"monitored"`
}

type Calendar struct {
	From    string          `json:"from"`
	To      string          `json:"to"`
	Entries []CalendarEntry `json:"entries" jsonschema:"in air order"`
}

// GetCalendar lists the monitored episodes airing in a window.
func GetCalendar(ctx context.Context, pastDays, days int) (Calendar, error) {
	c, err := newClient()
	if err != nil {
		return Calendar{}, err
	}
	if days <= 0 {
		days = 7
	}
	days, pastDays = min(days, 90), min(max(pastDays, 0), 90)
	start := time.Now().AddDate(0, 0, -pastDays)
	end := time.Now().AddDate(0, 0, days)

	var raw []struct {
		episodeJSON
		Series *struct {
			Title string `json:"title"`
		} `json:"series"`
	}
	q := url.Values{"start": {start.UTC().Format(time.RFC3339)}, "end": {end.UTC().Format(time.RFC3339)},
		"includeSeries": {"true"}}
	if err := c.get(ctx, "/calendar", q, &raw); err != nil {
		return Calendar{}, err
	}

	out := Calendar{From: start.Format("2006-01-02"), To: end.Format("2006-01-02")}
	for _, r := range raw {
		e := CalendarEntry{SeriesID: r.SeriesID, EpisodeID: r.ID,
			Episode: EpisodeCode(r.SeasonNumber, r.EpisodeNumber), Title: r.Title,
			HasFile: r.HasFile, Monitored: r.Monitored}
		if r.Series != nil {
			e.Series = r.Series.Title
		}
		if t, err := time.Parse(time.RFC3339, r.AirDateUtc); err == nil {
			e.AirDate = t.Local().Format("2006-01-02 15:04")
		}
		out.Entries = append(out.Entries, e)
	}
	slices.SortStableFunc(out.Entries, func(a, b CalendarEntry) int { return strings.Compare(a.AirDate, b.AirDate) })
	return out, nil
}

// --- download clients ----------------------------------------------------------

type ClientTest struct {
	Name   string   `json:"name"`
	Passed bool     `json:"passed"`
	Errors []string `json:"errors,omitempty"`
}

// TestDownloadClients has Sonarr test every enabled download client. It
// changes nothing.
func TestDownloadClients(ctx context.Context) ([]ClientTest, error) {
	c, err := newClient()
	if err != nil {
		return nil, err
	}
	var clients []struct {
		ID     int    `json:"id"`
		Name   string `json:"name"`
		Enable bool   `json:"enable"`
	}
	if err := c.get(ctx, "/downloadclient", nil, &clients); err != nil {
		return nil, err
	}

	status, body, err := c.postRaw(ctx, "/downloadclient/testall", releaseTimeout)
	if err != nil {
		return nil, err
	}
	if status >= 300 && status != http.StatusBadRequest {
		return nil, fmt.Errorf("sonarr returned %d testing its download clients%s", status, snippet(body))
	}
	var results []struct {
		ID                 int  `json:"id"`
		IsValid            bool `json:"isValid"`
		ValidationFailures []struct {
			ErrorMessage string `json:"errorMessage"`
			IsWarning    bool   `json:"isWarning"`
		} `json:"validationFailures"`
	}
	if err := json.Unmarshal(body, &results); err != nil {
		return nil, fmt.Errorf("sonarr's download client test answered something unreadable: %w", err)
	}

	var out []ClientTest
	for _, dc := range clients {
		if !dc.Enable {
			continue
		}
		t := ClientTest{Name: dc.Name, Passed: true}
		for _, r := range results {
			if r.ID != dc.ID {
				continue
			}
			for _, f := range r.ValidationFailures {
				if !f.IsWarning && f.ErrorMessage != "" {
					t.Errors = append(t.Errors, f.ErrorMessage)
				}
			}
			t.Passed = r.IsValid && len(t.Errors) == 0
		}
		out = append(out, t)
	}
	return out, nil
}

// --- wire types -----------------------------------------------------------

type historyJSON struct {
	SourceTitle string `json:"sourceTitle"`
	EventType   string `json:"eventType"`
	Date        string `json:"date"`
	Quality     *struct {
		Quality struct {
			Name string `json:"name"`
		} `json:"quality"`
	} `json:"quality"`
	Data    map[string]string `json:"data"`
	Episode *struct {
		SeasonNumber  int `json:"seasonNumber"`
		EpisodeNumber int `json:"episodeNumber"`
	} `json:"episode"`
	Series *struct {
		Title string `json:"title"`
	} `json:"series"`
}

func (r historyJSON) toEvent() HistoryEvent {
	e := HistoryEvent{
		Event:      historyEvent(r.EventType),
		Release:    r.SourceTitle,
		Indexer:    r.Data["indexer"],
		Client:     r.Data["downloadClientName"],
		SecondsAgo: secondsSince(r.Date),
	}
	if e.Client == "" {
		e.Client = r.Data["downloadClient"]
	}
	if r.Quality != nil {
		e.Quality = r.Quality.Quality.Name
	}
	if r.Episode != nil {
		e.Episode = EpisodeCode(r.Episode.SeasonNumber, r.Episode.EpisodeNumber)
	}
	if r.Series != nil {
		e.Series = r.Series.Title
	}
	for _, k := range []string{"message", "reason"} {
		if v := strings.TrimSpace(r.Data[k]); v != "" {
			e.Detail = v
			break
		}
	}
	return e
}

func historyEvent(t string) string {
	switch t {
	case "grabbed":
		return "grabbed"
	case "downloadFolderImported", "seriesFolderImported":
		return "imported"
	case "downloadFailed":
		return "failed"
	case "episodeFileDeleted":
		return "deleted"
	case "episodeFileRenamed":
		return "renamed"
	case "downloadIgnored":
		return "ignored"
	}
	return t
}

type blocklistJSON struct {
	SeriesID    int    `json:"seriesId"`
	SourceTitle string `json:"sourceTitle"`
	Indexer     string `json:"indexer"`
	Message     string `json:"message"`
	Date        string `json:"date"`
}
