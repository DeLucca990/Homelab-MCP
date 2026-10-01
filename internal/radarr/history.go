package radarr

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

// History answers "why did it pick that one" and "what happened to it": which
// release was grabbed from which indexer, whether it imported, whether it
// failed and why, whether a better file replaced it. The blocklist is the
// other half — releases Radarr will never grab again — and a movie that
// "cannot be found" is sometimes a movie whose only releases are on it.
//
// Also here: the calendar, and the download client test that the health tool
// runs on request.

const (
	defaultHistoryLimit = 25
	maxHistoryLimit     = 200
)

// HistoryEvent is one thing that happened to a movie.
type HistoryEvent struct {
	Event      string `json:"event" jsonschema:"grabbed, imported, failed, deleted, renamed or ignored"`
	Movie      string `json:"movie,omitempty"`
	MovieID    int    `json:"movie_id,omitempty"`
	Release    string `json:"release"`
	Quality    string `json:"quality,omitempty"`
	Indexer    string `json:"indexer,omitempty"`
	Client     string `json:"download_client,omitempty"`
	Detail     string `json:"detail,omitempty" jsonschema:"the reason Radarr recorded — why a download failed, why a file was deleted"`
	SecondsAgo uint64 `json:"seconds_ago"`
}

// BlocklistEntry is a release Radarr will not grab again.
type BlocklistEntry struct {
	Release    string `json:"release"`
	Indexer    string `json:"indexer,omitempty"`
	Reason     string `json:"reason,omitempty"`
	SecondsAgo uint64 `json:"seconds_ago"`
}

type History struct {
	MovieID   int              `json:"movie_id,omitempty"`
	Movie     string           `json:"movie,omitempty"`
	Events    []HistoryEvent   `json:"events" jsonschema:"most recent first"`
	Blocklist []BlocklistEntry `json:"blocklist,omitempty" jsonschema:"for one movie: releases Radarr will not grab again"`
	Warnings  []string         `json:"warnings,omitempty"`
}

// GetHistory reads one movie's history and blocklist, or the most recent
// events across the library when movieID is 0.
func GetHistory(ctx context.Context, movieID, limit int) (History, error) {
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

	if movieID > 0 {
		m, err := GetMovie(ctx, movieID)
		if err != nil {
			return History{}, err
		}
		out.MovieID, out.Movie = m.ID, m.Title
		q := url.Values{"movieId": {strconv.Itoa(m.ID)}}
		if err := c.get(ctx, "/history/movie", q, &raw); err != nil {
			return History{}, err
		}
		var block []blocklistJSON
		if err := c.get(ctx, "/blocklist/movie", q, &block); err == nil {
			for _, b := range block {
				out.Blocklist = append(out.Blocklist, BlocklistEntry{Release: b.SourceTitle,
					Indexer: b.Indexer, Reason: b.Message, SecondsAgo: secondsSince(b.Date)})
			}
		}
	} else {
		var page struct {
			Records []historyJSON `json:"records"`
		}
		q := url.Values{"page": {"1"}, "pageSize": {strconv.Itoa(limit)}, "sortKey": {"date"},
			"sortDirection": {"descending"}, "includeMovie": {"true"}}
		if err := c.get(ctx, "/history", q, &page); err != nil {
			return History{}, err
		}
		raw = page.Records
	}

	for _, r := range raw {
		out.Events = append(out.Events, r.toEvent())
	}
	// /history/movie comes oldest first; everything here reads newest first.
	if movieID > 0 {
		for i, j := 0, len(out.Events)-1; i < j; i, j = i+1, j-1 {
			out.Events[i], out.Events[j] = out.Events[j], out.Events[i]
		}
	}
	if len(out.Events) > limit {
		out.Events = out.Events[:limit]
	}

	failed := 0
	for _, e := range out.Events {
		if e.Event == "failed" {
			failed++
		}
	}
	if failed >= 3 && movieID > 0 {
		out.Warnings = append(out.Warnings, fmt.Sprintf("%d downloads of %s failed — the "+
			"details say why; a run of failures from one indexer usually means that indexer's "+
			"releases are fakes or passworded", failed, out.Movie))
	}
	if len(out.Blocklist) > 0 {
		out.Warnings = append(out.Warnings, fmt.Sprintf("%d release(s) are blocklisted for %s "+
			"and will not be grabbed again", len(out.Blocklist), out.Movie))
	}
	if len(out.Events) == 0 && movieID > 0 {
		out.Warnings = append(out.Warnings, fmt.Sprintf("nothing has ever been grabbed for %s", out.Movie))
	}
	return out, nil
}

// --- calendar ----------------------------------------------------------------

type CalendarEntry struct {
	MovieID   int    `json:"movie_id"`
	Movie     string `json:"movie"`
	Release   string `json:"release" jsonschema:"inCinemas, digital or physical"`
	Date      string `json:"date" jsonschema:"YYYY-MM-DD"`
	HasFile   bool   `json:"has_file"`
	Monitored bool   `json:"monitored"`
}

type Calendar struct {
	From    string          `json:"from"`
	To      string          `json:"to"`
	Entries []CalendarEntry `json:"entries" jsonschema:"in date order"`
}

// GetCalendar lists the release dates of monitored movies in a window.
func GetCalendar(ctx context.Context, pastDays, days int) (Calendar, error) {
	c, err := newClient()
	if err != nil {
		return Calendar{}, err
	}
	if days <= 0 {
		days = 14
	}
	days, pastDays = min(days, 90), min(max(pastDays, 0), 90)
	start := time.Now().AddDate(0, 0, -pastDays)
	end := time.Now().AddDate(0, 0, days)

	var raw []struct {
		ID              int    `json:"id"`
		Title           string `json:"title"`
		Year            int    `json:"year"`
		HasFile         bool   `json:"hasFile"`
		Monitored       bool   `json:"monitored"`
		InCinemas       string `json:"inCinemas"`
		DigitalRelease  string `json:"digitalRelease"`
		PhysicalRelease string `json:"physicalRelease"`
	}
	q := url.Values{"start": {start.UTC().Format(time.RFC3339)}, "end": {end.UTC().Format(time.RFC3339)}}
	if err := c.get(ctx, "/calendar", q, &raw); err != nil {
		return Calendar{}, err
	}

	out := Calendar{From: start.Format("2006-01-02"), To: end.Format("2006-01-02")}
	for _, r := range raw {
		for _, d := range []struct{ kind, stamp string }{
			{"inCinemas", r.InCinemas}, {"digital", r.DigitalRelease}, {"physical", r.PhysicalRelease},
		} {
			t, err := time.Parse(time.RFC3339, d.stamp)
			if err != nil || t.Before(start) || t.After(end) {
				continue
			}
			out.Entries = append(out.Entries, CalendarEntry{MovieID: r.ID,
				Movie: fmt.Sprintf("%s (%d)", r.Title, r.Year), Release: d.kind,
				Date: t.Format("2006-01-02"), HasFile: r.HasFile, Monitored: r.Monitored})
		}
	}
	slices.SortStableFunc(out.Entries, func(a, b CalendarEntry) int { return strings.Compare(a.Date, b.Date) })
	return out, nil
}

// --- download clients ----------------------------------------------------------

// ClientTest is one download client's test result.
type ClientTest struct {
	Name   string   `json:"name"`
	Passed bool     `json:"passed"`
	Errors []string `json:"errors,omitempty"`
}

// TestDownloadClients has Radarr test every enabled download client. It
// changes nothing; it is how "Radarr sends grabs nowhere" becomes a reason.
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
		return nil, fmt.Errorf("radarr returned %d testing its download clients%s", status, snippet(body))
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
		return nil, fmt.Errorf("radarr's download client test answered something unreadable: %w", err)
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
	MovieID     int    `json:"movieId"`
	SourceTitle string `json:"sourceTitle"`
	EventType   string `json:"eventType"`
	Date        string `json:"date"`
	Quality     *struct {
		Quality struct {
			Name string `json:"name"`
		} `json:"quality"`
	} `json:"quality"`
	Data  map[string]string `json:"data"`
	Movie *struct {
		Title string `json:"title"`
		Year  int    `json:"year"`
	} `json:"movie"`
}

func (r historyJSON) toEvent() HistoryEvent {
	e := HistoryEvent{
		Event:      historyEvent(r.EventType),
		MovieID:    r.MovieID,
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
	if r.Movie != nil {
		e.Movie = fmt.Sprintf("%s (%d)", r.Movie.Title, r.Movie.Year)
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
	case "downloadFolderImported", "movieFolderImported":
		return "imported"
	case "downloadFailed":
		return "failed"
	case "movieFileDeleted":
		return "deleted"
	case "movieFileRenamed":
		return "renamed"
	case "downloadIgnored":
		return "ignored"
	}
	return t
}

type blocklistJSON struct {
	SourceTitle string `json:"sourceTitle"`
	Indexer     string `json:"indexer"`
	Message     string `json:"message"`
	Date        string `json:"date"`
}
