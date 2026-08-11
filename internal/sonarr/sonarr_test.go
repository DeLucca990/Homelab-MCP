package sonarr

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func TestNormalizeBaseURL(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		// The documented case: a bare http host means a direct Sonarr, and
		// Sonarr's port is 8989 rather than Radarr's 7878.
		{"http://localhost", "http://localhost:8989"},
		{"localhost", "http://localhost:8989"},
		{"http://10.0.0.4/", "http://10.0.0.4:8989"},

		// Anything that reaches Sonarr through something else is left alone.
		{"http://localhost:8310", "http://localhost:8310"},
		{"https://sonarr.example.com", "https://sonarr.example.com"},
		{"http://nas/sonarr", "http://nas/sonarr"},
	}

	for _, c := range cases {
		got, err := normalizeBaseURL(c.in)
		if err != nil {
			t.Errorf("normalizeBaseURL(%q) errored: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("normalizeBaseURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}

	for _, bad := range []string{"", "   ", "ftp://nas"} {
		if _, err := normalizeBaseURL(bad); err == nil {
			t.Errorf("normalizeBaseURL(%q) should have failed", bad)
		}
	}
}

func TestParseSpanSeconds(t *testing.T) {
	cases := map[string]uint64{
		"":                 0,
		"00:00:45":         45,
		"00:12:34":         754,
		"01:00:00":         3600,
		"1.02:03:04":       93784,
		"00:00:30.5000000": 30,
		"not a timespan":   0,
	}
	for in, want := range cases {
		if got := parseSpanSeconds(in); got != want {
			t.Errorf("parseSpanSeconds(%q) = %d, want %d", in, got, want)
		}
	}
}

// mockSonarr serves canned responses and records what was asked of it.
type mockSonarr struct {
	*httptest.Server

	queueRecords []map[string]any
	series       []map[string]any
	profiles     []map[string]any
	rootFolders  []map[string]any
	lookupTerm   []map[string]any
	seriesByID   map[string]any
	episodes     []map[string]any
	missing      []map[string]any

	postedBody    map[string]any
	postedCommand map[string]any
	putBody       map[string]any
	deletedURL    string
	sawAPIKey     string
	lookupTerms   []string
}

func newMockSonarr(t *testing.T) *mockSonarr {
	t.Helper()
	m := &mockSonarr{}

	mux := http.NewServeMux()

	mux.HandleFunc("/api/v3/queue", func(w http.ResponseWriter, r *http.Request) {
		m.sawAPIKey = r.Header.Get("X-Api-Key")
		writeJSON(w, map[string]any{
			"totalRecords": len(m.queueRecords),
			"records":      m.queueRecords,
		})
	})
	mux.HandleFunc("/api/v3/queue/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
			return
		}
		m.deletedURL = r.URL.String()
		// The removal took: drop everything from the queue.
		m.queueRecords = nil
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/api/v3/series", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(body, &m.postedBody); err != nil {
				t.Errorf("posted body is not JSON: %v", err)
			}
			writeJSON(w, map[string]any{
				"id": 77, "title": m.postedBody["title"], "monitored": m.postedBody["monitored"],
				"path": "/tv/Whatever (2015)",
			})
			return
		}
		// Sonarr filters its own listing by tvdbId, which is how a TVDB id is
		// resolved back to a series.
		if tvdb := r.URL.Query().Get("tvdbId"); tvdb != "" {
			var hits []map[string]any
			for _, s := range append(m.series, nonNil(m.seriesByID)...) {
				if fmt.Sprint(s["tvdbId"]) == tvdb {
					hits = append(hits, s)
				}
			}
			writeJSON(w, hits)
			return
		}
		writeJSON(w, m.series)
	})
	// Registered before /api/v3/series/{id} so it is not swallowed by it.
	mux.HandleFunc("/api/v3/series/lookup", func(w http.ResponseWriter, r *http.Request) {
		m.lookupTerms = append(m.lookupTerms, r.URL.Query().Get("term"))
		writeJSON(w, m.lookupTerm)
	})
	mux.HandleFunc("/api/v3/series/{id}", func(w http.ResponseWriter, r *http.Request) {
		if m.seriesByID == nil || fmt.Sprint(m.seriesByID["id"]) != r.PathValue("id") {
			http.Error(w, `{"message":"NotFound"}`, http.StatusNotFound)
			return
		}
		switch r.Method {
		case http.MethodDelete:
			m.deletedURL = r.URL.String()
			m.seriesByID = nil // it left the library
			w.WriteHeader(http.StatusOK)
			return
		case http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(body, &m.putBody); err != nil {
				t.Errorf("put body is not JSON: %v", err)
			}
			// Sonarr stores what it was sent, so a later read sees the change.
			m.seriesByID = m.putBody
			writeJSON(w, m.putBody)
			return
		}
		writeJSON(w, m.seriesByID)
	})
	mux.HandleFunc("/api/v3/command", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &m.postedCommand); err != nil {
			t.Errorf("command body is not JSON: %v", err)
		}
		writeJSON(w, map[string]any{"id": 1234, "name": m.postedCommand["name"], "status": "queued"})
	})
	mux.HandleFunc("/api/v3/episode", func(w http.ResponseWriter, r *http.Request) {
		seriesID := r.URL.Query().Get("seriesId")
		var hits []map[string]any
		for _, e := range m.episodes {
			if seriesID == "" || fmt.Sprint(e["seriesId"]) == seriesID {
				hits = append(hits, e)
			}
		}
		writeJSON(w, hits)
	})
	mux.HandleFunc("/api/v3/wanted/missing", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"totalRecords": len(m.missing),
			"records":      m.missing,
		})
	})
	mux.HandleFunc("/api/v3/qualityprofile", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, m.profiles)
	})
	mux.HandleFunc("/api/v3/rootfolder", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, m.rootFolders)
	})

	m.Server = httptest.NewServer(mux)
	t.Cleanup(m.Close)

	t.Setenv(BaseURLEnv, m.URL)
	t.Setenv(APIKeyEnv, "test-key")

	return m
}

func nonNil(m map[string]any) []map[string]any {
	if m == nil {
		return nil
	}
	return []map[string]any{m}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// Readable arguments for episodesFor, so a fixture says what it means at the
// call site rather than trailing three bare booleans.
const (
	aired   = true
	unaired = false
	onDisk  = true
	noFile  = false
)

// episodesFor builds a season's worth of episodes. The air date is what decides
// "aired", and it is deliberately independent of any monitoring flag — which is
// the whole reason the monitor path counts episodes rather than statistics.
func episodesFor(seriesID, season, count int, hasAired, hasFile bool) []map[string]any {
	airDate := "2018-01-01T00:00:00Z"
	if !hasAired {
		airDate = "2999-01-01T00:00:00Z"
	}

	out := make([]map[string]any, 0, count)
	for n := 1; n <= count; n++ {
		out = append(out, map[string]any{
			"id":            seriesID*10000 + season*100 + n,
			"seriesId":      seriesID,
			"seasonNumber":  season,
			"episodeNumber": n,
			"title":         fmt.Sprintf("S%02dE%02d", season, n),
			"monitored":     false,
			"hasFile":       hasFile,
			"airDateUtc":    airDate,
		})
	}
	return out
}

func stats(fileCount, episodeCount, totalCount int, size int64) map[string]any {
	return map[string]any{
		"episodeFileCount":  fileCount,
		"episodeCount":      episodeCount,
		"totalEpisodeCount": totalCount,
		"sizeOnDisk":        size,
	}
}

func TestGetQueueClassifiesAndSorts(t *testing.T) {
	m := newMockSonarr(t)
	m.queueRecords = []map[string]any{
		{
			"id": 1, "title": "Healthy.Show.S01E01.1080p", "status": "downloading",
			"size": 1000.0, "sizeleft": 400.0, "timeleft": "00:10:00",
			"seriesId": 5, "downloadId": "aaa",
			"series":         map[string]any{"title": "Healthy Show", "year": 2021},
			"episode":        map[string]any{"seasonNumber": 1, "episodeNumber": 1, "title": "Pilot"},
			"downloadClient": "sab",
		},
		{
			"id": 2, "title": "Stuck.Show.S02E03.1080p", "status": "downloading",
			"size": 1000.0, "sizeleft": 950.0, "timeleft": "",
			"seriesId": 6, "downloadId": "bbb",
			"series":  map[string]any{"title": "Stuck Show", "year": 2019},
			"episode": map[string]any{"seasonNumber": 2, "episodeNumber": 3, "title": "Delta-V"},
		},
		{
			"id": 3, "title": "Blocked.Show.S03E05.1080p", "status": "completed",
			"trackedDownloadState": "importBlocked", "trackedDownloadStatus": "warning",
			"size": 1000.0, "sizeleft": 0.0, "seriesId": 7, "downloadId": "ccc",
			"series":         map[string]any{"title": "Blocked Show", "year": 2020},
			"episode":        map[string]any{"seasonNumber": 3, "episodeNumber": 5, "title": "Home"},
			"statusMessages": []map[string]any{{"title": "x", "messages": []string{"no files found"}}},
		},
	}

	q, err := GetQueue(context.Background())
	if err != nil {
		t.Fatalf("GetQueue: %v", err)
	}

	if m.sawAPIKey != "test-key" {
		t.Errorf("api key header = %q, want the configured key", m.sawAPIKey)
	}

	// Worst first: the blocked import needs a human, the stall is next, and the
	// download that is simply working comes last.
	gotOrder := []int{q.Items[0].ID, q.Items[1].ID, q.Items[2].ID}
	if !slices.Equal(gotOrder, []int{3, 2, 1}) {
		t.Errorf("order = %v, want [3 2 1]", gotOrder)
	}

	if q.BlockedCount != 1 || q.StalledCount != 1 || q.DownloadingCount != 1 {
		t.Errorf("counts: blocked=%d stalled=%d downloading=%d, want 1/1/1",
			q.BlockedCount, q.StalledCount, q.DownloadingCount)
	}

	byID := map[int]QueueItem{}
	for _, i := range q.Items {
		byID[i.ID] = i
	}

	if got := byID[1].Percent; got != 60 {
		t.Errorf("healthy item percent = %v, want 60", got)
	}
	if got := byID[2].DisplayName(); got != "Stuck Show S02E03 — Delta-V" {
		t.Errorf("display name = %q, want the episode named the way a person says it", got)
	}
	if byID[1].Stalled {
		t.Error("an item with a time remaining was called stalled")
	}
	if !byID[2].Stalled {
		t.Error("an incomplete download with no time remaining should be stalled")
	}
	if byID[3].Stalled {
		t.Error("a finished download waiting to import is not stalled")
	}

	// The message Sonarr recorded is why the import is blocked, so it must
	// survive into the warning rather than being replaced by the status.
	joined := strings.Join(q.Warnings, "\n")
	for _, want := range []string{"Blocked.Show", "no files found", "Stuck.Show", "stalled"} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "Healthy.Show") {
		t.Errorf("a working download produced a warning:\n%s", joined)
	}
}

// A season pack is one file and many rows. Counting rows as downloads, or
// warning once per row, would report fourteen problems where there is one.
func TestGetQueueGroupsASeasonPack(t *testing.T) {
	m := newMockSonarr(t)
	for episode := 1; episode <= 4; episode++ {
		m.queueRecords = append(m.queueRecords, map[string]any{
			"id": episode, "title": "Show.S02.COMPLETE.1080p", "status": "downloading",
			"size": 1000.0, "sizeleft": 900.0, "timeleft": "",
			"seriesId": 5, "downloadId": "pack-1",
			"series":  map[string]any{"title": "Show", "year": 2019},
			"episode": map[string]any{"seasonNumber": 2, "episodeNumber": episode},
		})
	}

	q, err := GetQueue(context.Background())
	if err != nil {
		t.Fatalf("GetQueue: %v", err)
	}

	if q.TotalCount != 4 || q.DownloadCount != 1 {
		t.Errorf("rows=%d downloads=%d, want 4 rows over 1 download",
			q.TotalCount, q.DownloadCount)
	}
	if len(q.Warnings) != 1 {
		t.Errorf("one stalled pack should warn once, got %d:\n%s",
			len(q.Warnings), strings.Join(q.Warnings, "\n"))
	}

	// And the removal has to say so before it happens, not after.
	item, siblings, err := FindQueueItem(context.Background(), 2)
	if err != nil {
		t.Fatalf("FindQueueItem: %v", err)
	}
	if len(siblings) != 3 {
		t.Errorf("found %d siblings, want the other 3 rows of the pack", len(siblings))
	}

	res, err := RemoveFromQueue(context.Background(), item, siblings, RemoveOptions{
		QueueID: 2, RemoveFromClient: true,
	})
	if err != nil {
		t.Fatalf("RemoveFromQueue: %v", err)
	}
	if res.EpisodesAffected != 4 {
		t.Errorf("EpisodesAffected = %d, want 4", res.EpisodesAffected)
	}
	if !containsSubstring(res.Warnings, "4 episodes") {
		t.Errorf("the result should say how many episodes went with it: %v", res.Warnings)
	}
}

// The heart of the module: a series is never simply present or absent, and
// "monitored" says nothing without the counts behind it.
func TestGetLibraryCountsEpisodesRatherThanSeries(t *testing.T) {
	m := newMockSonarr(t)
	m.series = []map[string]any{
		{"id": 1, "title": "Complete Show", "year": 2001, "monitored": true, "status": "ended",
			"statistics": stats(50, 50, 50, 100)},
		{"id": 2, "title": "Owed To You", "year": 2011, "monitored": true, "status": "continuing",
			"statistics": stats(20, 23, 30, 200)},
		{"id": 3, "title": "Not Started", "year": 2027, "monitored": true, "status": "upcoming",
			"statistics": stats(0, 0, 8, 0)},
		{"id": 4, "title": "Ignored", "year": 1999, "monitored": false, "status": "ended",
			"statistics": stats(0, 10, 10, 0)},
	}

	lib, err := GetLibrary(context.Background(), SeriesFilter{})
	if err != nil {
		t.Fatalf("GetLibrary: %v", err)
	}

	if lib.TotalCount != 4 || lib.MonitoredCount != 3 || lib.ContinuingCount != 1 {
		t.Errorf("total=%d monitored=%d continuing=%d, want 4/3/1",
			lib.TotalCount, lib.MonitoredCount, lib.ContinuingCount)
	}
	// Only the monitored, aired-and-short one counts as missing: an unmonitored
	// series is not owed to anybody, and an unaired one is a calendar entry.
	if lib.MissingCount != 1 || lib.EpisodesMissing != 3 {
		t.Errorf("missing series=%d episodes=%d, want 1 series / 3 episodes",
			lib.MissingCount, lib.EpisodesMissing)
	}
	if lib.EpisodesOnDisk != 70 {
		t.Errorf("episodes on disk = %d, want 70", lib.EpisodesOnDisk)
	}
	if lib.Series[0].Title != "Owed To You" {
		t.Errorf("first series = %q, want the incomplete one first", lib.Series[0].Title)
	}

	// An unmonitored series with no files must never be counted as missing.
	filtered, err := GetLibrary(context.Background(), SeriesFilter{OnlyMissing: true})
	if err != nil {
		t.Fatalf("GetLibrary(only_missing): %v", err)
	}
	if len(filtered.Series) != 1 || filtered.Series[0].ID != 2 {
		t.Errorf("only_missing returned %d series, want just 'Owed To You'", len(filtered.Series))
	}
	if filtered.TotalCount != 4 {
		t.Errorf("counts must describe the whole library even under a filter, got total=%d",
			filtered.TotalCount)
	}
}

// A per-season breakdown is the answer to "which season is short", and only
// makes sense once the question is about one show.
func TestGetLibraryCarriesSeasonsForASingleSeries(t *testing.T) {
	m := newMockSonarr(t)
	m.series = []map[string]any{
		{"id": 1, "title": "The Expanse", "year": 2015, "monitored": true,
			"statistics": stats(20, 23, 30, 200),
			"seasons": []map[string]any{
				{"seasonNumber": 0, "monitored": false, "statistics": stats(0, 0, 2, 0)},
				{"seasonNumber": 1, "monitored": true, "statistics": stats(10, 10, 10, 100)},
				{"seasonNumber": 2, "monitored": true, "statistics": stats(10, 13, 13, 100)},
			}},
		{"id": 2, "title": "Something Else", "year": 2019, "monitored": true,
			"statistics": stats(5, 5, 5, 50)},
	}

	lib, err := GetLibrary(context.Background(), SeriesFilter{Term: "expanse"})
	if err != nil {
		t.Fatalf("GetLibrary: %v", err)
	}
	if len(lib.Series) != 1 {
		t.Fatalf("the term selected %d series, want 1", len(lib.Series))
	}
	if len(lib.Series[0].Seasons) != 3 {
		t.Fatalf("seasons = %d, want the per-season breakdown", len(lib.Series[0].Seasons))
	}
	if got := lib.Series[0].Seasons[2]; got.Number != 2 || got.EpisodesMissing != 3 {
		t.Errorf("season 2 = %+v, want 3 missing", got)
	}

	// The whole library must not carry it: hundreds of shows would be thousands
	// of rows nobody asked for.
	all, err := GetLibrary(context.Background(), SeriesFilter{})
	if err != nil {
		t.Fatalf("GetLibrary: %v", err)
	}
	for _, s := range all.Series {
		if len(s.Seasons) > 0 {
			t.Errorf("%s carried seasons in a whole-library listing", s.Title)
		}
	}
}

func TestPlanRefusesWhatItCannotDecide(t *testing.T) {
	m := newMockSonarr(t)
	m.lookupTerm = []map[string]any{
		{"id": 0, "title": "The Expanse", "year": 2015, "tvdbId": 280619},
	}
	m.rootFolders = []map[string]any{{"id": 1, "path": "/tv", "accessible": true}}
	m.profiles = []map[string]any{
		{"id": 4, "name": "HD-1080p"},
		{"id": 6, "name": "Ultra-HD"},
	}

	// Named by name, it resolves.
	plan, err := Plan(context.Background(), AddRequest{TvdbID: 280619, QualityProfile: "Ultra-HD"})
	if err != nil {
		t.Fatalf("Plan with a named profile: %v", err)
	}
	if plan.QualityProfileID != 6 || plan.RootFolderPath != "/tv" {
		t.Errorf("resolved to profile %d in %q, want 6 in /tv",
			plan.QualityProfileID, plan.RootFolderPath)
	}
	if plan.Monitor != "all" || plan.SeriesType != "standard" {
		t.Errorf("defaults = monitor %q / type %q, want all / standard",
			plan.Monitor, plan.SeriesType)
	}

	// The id goes to Sonarr as a tvdb: term, since there is no by-id endpoint.
	if last := m.lookupTerms[len(m.lookupTerms)-1]; last != "tvdb:280619" {
		t.Errorf("lookup term = %q, want tvdb:280619", last)
	}

	// An unreachable root folder means a download that imports nowhere.
	m.rootFolders = []map[string]any{{"id": 1, "path": "/tv", "accessible": false}}
	if _, err := Plan(context.Background(), AddRequest{TvdbID: 280619, QualityProfile: "4"}); err == nil {
		t.Error("Plan should refuse an inaccessible root folder")
	}
}

// monitor is the parameter with no Radarr equivalent and the one that decides
// how big the operation is, so a value that is not an option must not reach
// Sonarr as one.
func TestPlanValidatesMonitorAndSeriesType(t *testing.T) {
	m := newMockSonarr(t)
	m.lookupTerm = []map[string]any{
		{"id": 0, "title": "The Expanse", "year": 2015, "tvdbId": 280619},
	}
	m.rootFolders = []map[string]any{{"id": 1, "path": "/tv", "accessible": true}}
	m.profiles = []map[string]any{{"id": 4, "name": "HD-1080p"}}

	// Sonarr's own spelling is camelCase and it rejects anything else, so what
	// a caller types is matched case-insensitively and normalised.
	plan, err := Plan(context.Background(), AddRequest{TvdbID: 280619, Monitor: "FIRSTSEASON"})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Monitor != "firstSeason" {
		t.Errorf("monitor = %q, want it normalised to firstSeason", plan.Monitor)
	}

	if _, err := Plan(context.Background(), AddRequest{TvdbID: 280619, Monitor: "everything"}); err == nil {
		t.Error("an invalid monitor value should be refused rather than sent")
	}
	if _, err := Plan(context.Background(), AddRequest{TvdbID: 280619, SeriesType: "cartoon"}); err == nil {
		t.Error("an invalid series_type should be refused rather than sent")
	}
}

// Sonarr answers a `tvdb:` term with a list, and a list is not an identity:
// taking the first element would add whatever it ranked highest for an id it
// did not recognise.
func TestPlanTakesTheMatchingResultNotTheFirst(t *testing.T) {
	m := newMockSonarr(t)
	m.rootFolders = []map[string]any{{"id": 1, "path": "/tv", "accessible": true}}
	m.profiles = []map[string]any{{"id": 4, "name": "HD-1080p"}}
	m.lookupTerm = []map[string]any{
		{"id": 0, "title": "Some Other Show", "year": 2001, "tvdbId": 111},
		{"id": 0, "title": "The Expanse", "year": 2015, "tvdbId": 280619},
	}

	plan, err := Plan(context.Background(), AddRequest{TvdbID: 280619})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Title != "The Expanse" {
		t.Errorf("resolved to %q, want the result whose tvdbId was asked for", plan.Title)
	}

	m.lookupTerm = []map[string]any{{"id": 0, "title": "Some Other Show", "tvdbId": 111}}
	if _, err := Plan(context.Background(), AddRequest{TvdbID: 280619}); err == nil {
		t.Error("a lookup that returned no matching id should fail rather than add something else")
	}
}

func TestPlanDefaultsToHD1080p(t *testing.T) {
	m := newMockSonarr(t)
	m.lookupTerm = []map[string]any{
		{"id": 0, "title": "The Expanse", "year": 2015, "tvdbId": 280619},
	}
	m.rootFolders = []map[string]any{{"id": 1, "path": "/tv", "accessible": true}}

	t.Run("picked out of several", func(t *testing.T) {
		m.profiles = []map[string]any{
			{"id": 6, "name": "Ultra-HD"},
			{"id": 4, "name": "HD-1080p"},
			{"id": 2, "name": "SD"},
		}

		plan, err := Plan(context.Background(), AddRequest{TvdbID: 280619})
		if err != nil {
			t.Fatalf("Plan with no profile named: %v", err)
		}
		if plan.QualityProfileID != 4 {
			t.Errorf("defaulted to %q (id %d), want HD-1080p",
				plan.QualityProfileName, plan.QualityProfileID)
		}
	})

	// Renamed or deleted: the only profile there is beats refusing.
	t.Run("falls back to the only profile", func(t *testing.T) {
		m.profiles = []map[string]any{{"id": 7, "name": "Anything Goes"}}

		plan, err := Plan(context.Background(), AddRequest{TvdbID: 280619})
		if err != nil {
			t.Fatalf("Plan with one non-default profile: %v", err)
		}
		if plan.QualityProfileID != 7 {
			t.Errorf("resolved to id %d, want the only profile", plan.QualityProfileID)
		}
	})

	// No default and a real choice to make: guessing here decides what gets
	// downloaded from now on, so it must refuse and name the options.
	t.Run("refuses with no default and a choice", func(t *testing.T) {
		m.profiles = []map[string]any{
			{"id": 6, "name": "Ultra-HD"},
			{"id": 2, "name": "SD"},
		}

		_, err := Plan(context.Background(), AddRequest{TvdbID: 280619})
		if err == nil {
			t.Fatal("Plan should refuse when there is no default and no choice made")
		}
		for _, want := range []string{"Ultra-HD", "SD", DefaultQualityProfile} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal should mention %q, got: %v", want, err)
			}
		}
	})
}

// The cover is what a person recognises the show by, and the poster path
// rotates — so it belongs in the confirmation and not in the fingerprint.
func TestPlanCarriesThePosterButDoesNotHashIt(t *testing.T) {
	m := newMockSonarr(t)
	m.profiles = []map[string]any{{"id": 4, "name": "HD-1080p"}}
	m.rootFolders = []map[string]any{{"id": 1, "path": "/tv", "accessible": true}}

	base := map[string]any{"id": 0, "title": "The Expanse", "year": 2015, "tvdbId": 280619}

	m.lookupTerm = []map[string]any{merge(base, map[string]any{
		"images": []map[string]any{
			{"coverType": "fanart", "remoteUrl": "https://artworks.thetvdb.com/fanart.jpg"},
			{"coverType": "poster", "url": "/MediaCover/0/poster.jpg",
				"remoteUrl": "https://artworks.thetvdb.com/poster.jpg"},
		},
		"remotePoster": "https://artworks.thetvdb.com/fallback.jpg",
	})}

	first, err := Plan(context.Background(), AddRequest{TvdbID: 280619})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if first.PosterURL != "https://artworks.thetvdb.com/poster.jpg" {
		t.Errorf("PosterURL = %q, want the poster's remoteUrl", first.PosterURL)
	}

	// A scheme that does something when clicked is not a cover.
	m.lookupTerm = []map[string]any{merge(base, map[string]any{
		"images":       []map[string]any{{"coverType": "poster", "remoteUrl": "javascript:alert(1)"}},
		"remotePoster": "data:text/html;base64,PHNjcmlwdD4=",
	})}
	unsafe, err := Plan(context.Background(), AddRequest{TvdbID: 280619})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if unsafe.PosterURL != "" {
		t.Errorf("PosterURL = %q, only http(s) should be passed on", unsafe.PosterURL)
	}

	if !slices.Equal(first.Fingerprint(), unsafe.Fingerprint()) {
		t.Errorf("a changed cover changed the fingerprint:\n%v\n%v",
			first.Fingerprint(), unsafe.Fingerprint())
	}
}

func merge(base, extra map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func TestPlanRefusesASeriesAlreadyAdded(t *testing.T) {
	m := newMockSonarr(t)
	m.lookupTerm = []map[string]any{
		{"id": 12, "title": "The Expanse", "year": 2015, "tvdbId": 280619},
	}
	m.profiles = []map[string]any{{"id": 4, "name": "HD-1080p"}}
	m.rootFolders = []map[string]any{{"id": 1, "path": "/tv", "accessible": true}}

	_, err := Plan(context.Background(), AddRequest{TvdbID: 280619})
	if err == nil || !strings.Contains(err.Error(), "already in your library") {
		t.Fatalf("adding a series Sonarr already has should be refused by name, got: %v", err)
	}
	// And it must point at the tool that does what was actually wanted.
	if !strings.Contains(err.Error(), "sonarr_series_search") {
		t.Errorf("the refusal should name the way to get the missing episodes: %v", err)
	}
}

func TestAddSendsTheResolvedPlan(t *testing.T) {
	m := newMockSonarr(t)
	m.lookupTerm = []map[string]any{{
		"id": 0, "title": "The Expanse", "year": 2015, "tvdbId": 280619,
		// Fields Sonarr fills in and expects back untouched.
		"titleSlug": "the-expanse", "images": []any{map[string]any{"coverType": "poster"}},
		"seasons": []map[string]any{{"seasonNumber": 1}, {"seasonNumber": 2}},
	}}
	m.profiles = []map[string]any{{"id": 4, "name": "HD-1080p"}}
	m.rootFolders = []map[string]any{{"id": 1, "path": "/tv", "accessible": true}}

	plan, err := Plan(context.Background(), AddRequest{
		TvdbID: 280619, SearchOnAdd: true, SeasonFolder: true, Monitor: "future",
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.SeasonCount != 2 {
		t.Errorf("season count = %d, want 2 — it is what tells a human how big this is",
			plan.SeasonCount)
	}

	res, err := Add(context.Background(), plan)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if res.SeriesID != 77 || res.Title != "The Expanse" {
		t.Errorf("result = %+v, want series 77 'The Expanse'", res)
	}

	body := m.postedBody
	if body["qualityProfileId"] != 4.0 {
		t.Errorf("qualityProfileId = %v, want 4", body["qualityProfileId"])
	}
	if body["rootFolderPath"] != "/tv" {
		t.Errorf("rootFolderPath = %v, want /tv", body["rootFolderPath"])
	}
	if body["seriesType"] != "standard" || body["seasonFolder"] != true {
		t.Errorf("seriesType/seasonFolder = %v/%v", body["seriesType"], body["seasonFolder"])
	}
	// The lookup resource is posted back whole: Sonarr uses these and nothing
	// here should be synthesising them.
	if body["titleSlug"] != "the-expanse" || body["images"] == nil || body["seasons"] == nil {
		t.Errorf("the lookup resource was not sent back intact: %v", body)
	}
	opts, ok := body["addOptions"].(map[string]any)
	if !ok || opts["searchForMissingEpisodes"] != true || opts["monitor"] != "future" {
		t.Errorf("addOptions = %v, want a monitored search with the chosen scope",
			body["addOptions"])
	}
}

func TestAddWarnsWhenNothingWillHappen(t *testing.T) {
	m := newMockSonarr(t)
	m.lookupTerm = []map[string]any{
		{"id": 0, "title": "The Expanse", "year": 2015, "tvdbId": 280619},
	}
	m.profiles = []map[string]any{{"id": 4, "name": "HD-1080p"}}
	m.rootFolders = []map[string]any{{"id": 1, "path": "/tv", "accessible": true}}

	plan, err := Plan(context.Background(), AddRequest{TvdbID: 280619, Monitor: "none"})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	res, err := Add(context.Background(), plan)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if !containsSubstring(res.Warnings, "nothing monitored") {
		t.Errorf("adding a series with monitor=none does nothing and should say so: %v",
			res.Warnings)
	}
}

func TestRemoveFromQueuePassesTheFlagsAndVerifies(t *testing.T) {
	m := newMockSonarr(t)
	m.queueRecords = []map[string]any{{
		"id": 9, "title": "Stuck.Show.S02E03", "status": "downloading",
		"size": 1000.0, "sizeleft": 900.0, "seriesId": 42, "downloadId": "abc",
		"series":  map[string]any{"title": "Stuck Show", "year": 2019},
		"episode": map[string]any{"seasonNumber": 2, "episodeNumber": 3},
	}}

	item, siblings, err := FindQueueItem(context.Background(), 9)
	if err != nil {
		t.Fatalf("FindQueueItem: %v", err)
	}
	if len(siblings) != 0 {
		t.Errorf("a single-episode download has no siblings, got %d", len(siblings))
	}

	res, err := RemoveFromQueue(context.Background(), item, siblings, RemoveOptions{
		QueueID: 9, RemoveFromClient: true, Blocklist: true,
	})
	if err != nil {
		t.Fatalf("RemoveFromQueue: %v", err)
	}

	for _, want := range []string{"/api/v3/queue/9", "removeFromClient=true", "blocklist=true", "skipRedownload=false"} {
		if !strings.Contains(m.deletedURL, want) {
			t.Errorf("delete URL %q is missing %q", m.deletedURL, want)
		}
	}
	if res.StillQueued {
		t.Error("the item left the queue but was reported as still there")
	}
	// Blocklisting without skipping the redownload means Sonarr goes looking
	// again, which is a consequence worth stating.
	if len(res.Warnings) == 0 {
		t.Error("a blocklist that triggers a fresh search should warn")
	}

	// A stale id must not resolve to whatever now holds that number.
	if _, _, err := FindQueueItem(context.Background(), 9); err == nil {
		t.Error("FindQueueItem should fail once the item is gone")
	}
}

// The user's actual sequence: remove the download, then wonder why nothing
// happens. Removing is not blocklisting, so Sonarr starts no replacement.
func TestRemovalSaysNothingIsLookingAnymore(t *testing.T) {
	m := newMockSonarr(t)
	m.queueRecords = []map[string]any{{
		"id": 9, "title": "Stuck.Show.S02E03", "status": "downloading",
		"seriesId": 42, "size": 1000.0, "sizeleft": 900.0,
		"series":  map[string]any{"title": "Stuck Show", "year": 2019},
		"episode": map[string]any{"seasonNumber": 2, "episodeNumber": 3},
	}}

	item, siblings, err := FindQueueItem(context.Background(), 9)
	if err != nil {
		t.Fatalf("FindQueueItem: %v", err)
	}
	res, err := RemoveFromQueue(context.Background(), item, siblings, RemoveOptions{
		QueueID: 9, RemoveFromClient: true,
	})
	if err != nil {
		t.Fatalf("RemoveFromQueue: %v", err)
	}

	if !containsSubstring(res.Warnings, "sonarr_series_search") {
		t.Errorf("a plain removal should point at the way back: %v", res.Warnings)
	}
	if !containsSubstring(res.Warnings, "series_id 42") {
		t.Errorf("the warning should carry the id to search with: %v", res.Warnings)
	}
}

// The scale is the whole difference between Sonarr's search and Radarr's: one
// tool, three commands, and an approval for one of them must not run another.
func TestSearchPicksTheCommandForTheScope(t *testing.T) {
	m := newMockSonarr(t)
	m.seriesByID = map[string]any{
		"id": 42, "title": "The Expanse", "year": 2015, "tvdbId": 280619,
		"monitored": true, "statistics": stats(20, 23, 30, 100),
		"seasons": []map[string]any{
			{"seasonNumber": 1, "monitored": true, "statistics": stats(10, 10, 10, 50)},
			{"seasonNumber": 2, "monitored": true, "statistics": stats(10, 13, 13, 50)},
		},
	}
	m.episodes = []map[string]any{
		{"id": 501, "seriesId": 42, "seasonNumber": 2, "episodeNumber": 3, "title": "Delta-V",
			"monitored": true, "hasFile": false, "airDateUtc": "2018-01-01T00:00:00Z"},
		{"id": 502, "seriesId": 42, "seasonNumber": 2, "episodeNumber": 4, "title": "Reload",
			"monitored": true, "hasFile": false, "airDateUtc": "2018-01-08T00:00:00Z"},
		{"id": 900, "seriesId": 99, "seasonNumber": 1, "episodeNumber": 1, "title": "Elsewhere",
			"monitored": true, "hasFile": false, "airDateUtc": "2018-01-08T00:00:00Z"},
	}

	t.Run("whole series", func(t *testing.T) {
		scope, err := ResolveSearch(context.Background(), SearchRequest{SeriesID: 42})
		if err != nil {
			t.Fatalf("ResolveSearch: %v", err)
		}
		res, err := Search(context.Background(), scope)
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if m.postedCommand["name"] != "SeriesSearch" {
			t.Errorf("command = %v, want SeriesSearch", m.postedCommand["name"])
		}
		if m.postedCommand["seriesId"] != 42.0 {
			t.Errorf("seriesId = %v, want 42", m.postedCommand["seriesId"])
		}
		if res.MissingEpisodes != 3 {
			t.Errorf("missing in scope = %d, want the series' 3", res.MissingEpisodes)
		}
	})

	t.Run("one season", func(t *testing.T) {
		season := 2
		scope, err := ResolveSearch(context.Background(), SearchRequest{SeriesID: 42, Season: &season})
		if err != nil {
			t.Fatalf("ResolveSearch: %v", err)
		}
		if _, err := Search(context.Background(), scope); err != nil {
			t.Fatalf("Search: %v", err)
		}
		if m.postedCommand["name"] != "SeasonSearch" || m.postedCommand["seasonNumber"] != 2.0 {
			t.Errorf("command = %v, want SeasonSearch on season 2", m.postedCommand)
		}
		if scope.MissingEpisodes != 3 {
			t.Errorf("missing in season 2 = %d, want 3", scope.MissingEpisodes)
		}
	})

	t.Run("named episodes", func(t *testing.T) {
		scope, err := ResolveSearch(context.Background(), SearchRequest{
			SeriesID: 42, EpisodeIDs: []int{502, 501, 501},
		})
		if err != nil {
			t.Fatalf("ResolveSearch: %v", err)
		}
		// Duplicates would search twice and, worse, produce a different
		// fingerprint from the same intent.
		if len(scope.Episodes) != 2 {
			t.Fatalf("resolved %d episodes, want 2 after de-duplication", len(scope.Episodes))
		}
		if scope.Episodes[0].Code != "S02E03" {
			t.Errorf("first episode = %q, want them in airing order", scope.Episodes[0].Code)
		}
		if _, err := Search(context.Background(), scope); err != nil {
			t.Fatalf("Search: %v", err)
		}
		if m.postedCommand["name"] != "EpisodeSearch" {
			t.Errorf("command = %v, want EpisodeSearch", m.postedCommand["name"])
		}
		ids, _ := m.postedCommand["episodeIds"].([]any)
		if len(ids) != 2 {
			t.Errorf("episodeIds = %v, want two", m.postedCommand["episodeIds"])
		}
	})

	// An id from another series would otherwise be sent to Sonarr, which would
	// happily search a show nobody approved.
	t.Run("an episode of another series is refused", func(t *testing.T) {
		_, err := ResolveSearch(context.Background(), SearchRequest{
			SeriesID: 42, EpisodeIDs: []int{900},
		})
		if err == nil || !strings.Contains(err.Error(), "does not belong") {
			t.Fatalf("an episode id from another series should be refused, got: %v", err)
		}
	})

	t.Run("a season the series does not have is refused", func(t *testing.T) {
		season := 9
		_, err := ResolveSearch(context.Background(), SearchRequest{SeriesID: 42, Season: &season})
		if err == nil || !strings.Contains(err.Error(), "no season 9") {
			t.Fatalf("a season that does not exist should be refused, got: %v", err)
		}
	})
}

// Two scopes that would search different things must not share a fingerprint,
// or an approval for one authorises the other.
func TestSearchScopesFingerprintApart(t *testing.T) {
	newMockSonarrWithExpanse(t)

	season := 2
	whole, err := ResolveSearch(context.Background(), SearchRequest{SeriesID: 42})
	if err != nil {
		t.Fatalf("ResolveSearch: %v", err)
	}
	oneSeason, err := ResolveSearch(context.Background(), SearchRequest{SeriesID: 42, Season: &season})
	if err != nil {
		t.Fatalf("ResolveSearch: %v", err)
	}
	oneEpisode, err := ResolveSearch(context.Background(), SearchRequest{
		SeriesID: 42, EpisodeIDs: []int{501},
	})
	if err != nil {
		t.Fatalf("ResolveSearch: %v", err)
	}

	all := [][]string{whole.Fingerprint(), oneSeason.Fingerprint(), oneEpisode.Fingerprint()}
	for i := range all {
		for j := i + 1; j < len(all); j++ {
			if slices.Equal(all[i], all[j]) {
				t.Errorf("scopes %d and %d hash the same:\n%v", i, j, all[i])
			}
		}
	}

	// The same request twice must be stable, or every approval would be refused.
	again, err := ResolveSearch(context.Background(), SearchRequest{
		SeriesID: 42, EpisodeIDs: []int{501},
	})
	if err != nil {
		t.Fatalf("ResolveSearch: %v", err)
	}
	if !slices.Equal(oneEpisode.Fingerprint(), again.Fingerprint()) {
		t.Error("the same scope resolved twice produced different fingerprints")
	}
}

func TestSearchWarnsWhenItWillProbablyDoNothing(t *testing.T) {
	m := newMockSonarr(t)

	t.Run("unmonitored series", func(t *testing.T) {
		m.seriesByID = map[string]any{"id": 42, "title": "Ignored", "monitored": false,
			"statistics": stats(0, 10, 10, 0)}
		scope, err := ResolveSearch(context.Background(), SearchRequest{SeriesID: 42})
		if err != nil {
			t.Fatalf("ResolveSearch: %v", err)
		}
		res, err := Search(context.Background(), scope)
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if !containsSubstring(res.Warnings, "not monitored") {
			t.Errorf("warnings = %v", res.Warnings)
		}
	})

	t.Run("nothing missing", func(t *testing.T) {
		m.seriesByID = map[string]any{"id": 42, "title": "Complete", "monitored": true,
			"statistics": stats(10, 10, 10, 100)}
		scope, err := ResolveSearch(context.Background(), SearchRequest{SeriesID: 42})
		if err != nil {
			t.Fatalf("ResolveSearch: %v", err)
		}
		res, err := Search(context.Background(), scope)
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if !containsSubstring(res.Warnings, "upgrades") {
			t.Errorf("warnings = %v", res.Warnings)
		}
	})
}

// Sonarr's own id and the TVDB id are different numbers for the same show, both
// plain integers, so the wrong one gets passed and /series/{id} answers 404.
// Resolving it is what keeps a correct request from failing over a naming
// detail.
func TestGetSeriesAcceptsEitherID(t *testing.T) {
	m := newMockSonarr(t)
	m.seriesByID = map[string]any{
		"id": 42, "title": "The Expanse", "year": 2015, "tvdbId": 280619,
		"monitored": true, "statistics": stats(20, 23, 30, 100),
	}

	t.Run("sonarr id", func(t *testing.T) {
		series, err := GetSeries(context.Background(), 42)
		if err != nil {
			t.Fatalf("GetSeries(42): %v", err)
		}
		if series.ID != 42 || series.EpisodesMissing != 3 {
			t.Errorf("resolved to %+v, want series 42 with 3 missing", series)
		}
	})

	t.Run("tvdb id", func(t *testing.T) {
		series, err := GetSeries(context.Background(), 280619)
		if err != nil {
			t.Fatalf("GetSeries(280619) should have fallen back to the TVDB id: %v", err)
		}
		// The caller tells the two apart by comparing: what came back is not the
		// number that went in, which is what the confirmation reports.
		if series.ID != 42 || series.Title != "The Expanse" {
			t.Errorf("resolved to %+v, want series 42", series)
		}
	})

	t.Run("neither", func(t *testing.T) {
		_, err := GetSeries(context.Background(), 999999)
		if err == nil {
			t.Fatal("an id in neither space should fail")
		}
		// Naming only one of them sends the reader looking in the wrong place.
		for _, want := range []string{"Sonarr id 999999", "TVDB id 999999"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the error should mention %q, got: %v", want, err)
			}
		}
	})
}

// Forgetting a series and erasing every episode of it are different requests
// that share an endpoint, so the flag has to reach Sonarr exactly as it was
// approved — under the name Sonarr uses, which is not Radarr's.
func TestDeletePassesTheFlagsAndVerifies(t *testing.T) {
	t.Run("keeps the files by default", func(t *testing.T) {
		m := newMockSonarr(t)
		m.seriesByID = map[string]any{
			"id": 42, "title": "The Expanse", "year": 2015, "tvdbId": 280619,
			"monitored": true, "path": "/tv/The Expanse",
			"statistics": stats(20, 23, 30, 8_000_000_000),
		}

		series, err := GetSeries(context.Background(), 42)
		if err != nil {
			t.Fatalf("GetSeries: %v", err)
		}

		res, err := Delete(context.Background(), series, DeleteOptions{SeriesID: 42})
		if err != nil {
			t.Fatalf("Delete: %v", err)
		}

		for _, want := range []string{"/api/v3/series/42", "deleteFiles=false", "addImportListExclusion=false"} {
			if !strings.Contains(m.deletedURL, want) {
				t.Errorf("delete URL %q is missing %q", m.deletedURL, want)
			}
		}
		if res.FreedBytes != 0 {
			t.Errorf("FreedBytes = %d, nothing was deleted", res.FreedBytes)
		}
		if res.StillPresent {
			t.Error("the series left the library but was reported as still there")
		}
		// Files nobody tracks anymore are a surprise later, not an error now.
		if !containsSubstring(res.Warnings, "still on disk") {
			t.Errorf("warnings = %v", res.Warnings)
		}
		if !containsSubstring(res.Warnings, "/tv/The Expanse") {
			t.Errorf("the warning should name the folder: %v", res.Warnings)
		}
	})

	t.Run("erases when asked", func(t *testing.T) {
		m := newMockSonarr(t)
		m.seriesByID = map[string]any{
			"id": 42, "title": "The Expanse", "year": 2015,
			"statistics": stats(20, 23, 30, 8_000_000_000),
		}

		series, _ := GetSeries(context.Background(), 42)
		res, err := Delete(context.Background(), series, DeleteOptions{
			SeriesID: 42, DeleteFiles: true, AddImportExclusion: true,
		})
		if err != nil {
			t.Fatalf("Delete: %v", err)
		}

		for _, want := range []string{"deleteFiles=true", "addImportListExclusion=true"} {
			if !strings.Contains(m.deletedURL, want) {
				t.Errorf("delete URL %q is missing %q", m.deletedURL, want)
			}
		}
		if res.FreedBytes != 8_000_000_000 || res.EpisodesOnDisk != 20 {
			t.Errorf("result = %+v, want the freed size and file count reported", res)
		}
		if containsSubstring(res.Warnings, "still on disk") {
			t.Errorf("nothing is still on disk: %v", res.Warnings)
		}
		if !containsSubstring(res.Warnings, "import list exclusions") {
			t.Errorf("an exclusion outlives the series and should be stated: %v", res.Warnings)
		}
	})
}

// The 404 as reported for Radarr, in Sonarr's number space: a delete driven by
// the TVDB id must land on the series, and must address it by Sonarr's id on the
// wire.
func TestDeleteWorksThroughATvdbID(t *testing.T) {
	m := newMockSonarr(t)
	m.seriesByID = map[string]any{
		"id": 42, "title": "The Expanse", "year": 2015, "tvdbId": 280619,
		"statistics": stats(20, 20, 20, 100),
	}

	series, err := GetSeries(context.Background(), 280619)
	if err != nil {
		t.Fatalf("GetSeries: %v", err)
	}
	if _, err := Delete(context.Background(), series, DeleteOptions{DeleteFiles: true}); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if !strings.Contains(m.deletedURL, "/api/v3/series/42") {
		t.Errorf("delete URL = %q, want it addressed by Sonarr's id 42", m.deletedURL)
	}
	if strings.Contains(m.deletedURL, "280619") {
		t.Errorf("the TVDB id reached the request path: %q", m.deletedURL)
	}
}

func TestMissingEpisodes(t *testing.T) {
	m := newMockSonarr(t)
	m.seriesByID = map[string]any{
		"id": 42, "title": "The Expanse", "year": 2015, "tvdbId": 280619, "monitored": true,
		"statistics": stats(1, 3, 5, 100),
	}
	m.episodes = []map[string]any{
		{"id": 501, "seriesId": 42, "seasonNumber": 2, "episodeNumber": 4, "title": "Reload",
			"monitored": true, "hasFile": false, "airDateUtc": "2018-01-08T00:00:00Z"},
		{"id": 502, "seriesId": 42, "seasonNumber": 2, "episodeNumber": 3, "title": "Delta-V",
			"monitored": true, "hasFile": false, "airDateUtc": "2018-01-01T00:00:00Z",
			"lastSearchTime": "2018-02-01T00:00:00Z"},
		{"id": 503, "seriesId": 42, "seasonNumber": 2, "episodeNumber": 5, "title": "Downloaded",
			"monitored": true, "hasFile": true, "airDateUtc": "2018-01-15T00:00:00Z"},
		{"id": 504, "seriesId": 42, "seasonNumber": 2, "episodeNumber": 6, "title": "Ignored",
			"monitored": false, "hasFile": false, "airDateUtc": "2018-01-22T00:00:00Z"},
		{"id": 505, "seriesId": 42, "seasonNumber": 9, "episodeNumber": 1, "title": "Next Year",
			"monitored": true, "hasFile": false, "airDateUtc": "2999-01-01T00:00:00Z"},
	}

	missing, err := GetMissing(context.Background(), MissingFilter{SeriesID: 42})
	if err != nil {
		t.Fatalf("GetMissing: %v", err)
	}

	// Downloaded, unmonitored and unaired are all excluded, and for different
	// reasons: only "monitored, aired, no file" is something Sonarr owes you.
	if missing.TotalCount != 2 {
		t.Fatalf("missing = %d, want 2:\n%+v", missing.TotalCount, missing.Episodes)
	}
	if missing.Episodes[0].Code != "S02E03" {
		t.Errorf("first = %q, want airing order", missing.Episodes[0].Code)
	}
	if missing.Scope != "The Expanse" {
		t.Errorf("scope = %q, want the series name", missing.Scope)
	}
	// One of the two has never been searched for, which is a different problem
	// from one searched for and never found.
	if !containsSubstring(missing.Warnings, "never been searched") {
		t.Errorf("warnings = %v", missing.Warnings)
	}

	t.Run("whole library", func(t *testing.T) {
		m.missing = []map[string]any{
			{"id": 501, "seriesId": 42, "seasonNumber": 2, "episodeNumber": 4, "title": "Reload",
				"monitored": true, "hasFile": false, "airDateUtc": "2018-01-08T00:00:00Z",
				"series": map[string]any{"title": "The Expanse"}},
			{"id": 900, "seriesId": 99, "seasonNumber": 1, "episodeNumber": 1, "title": "Elsewhere",
				"monitored": true, "hasFile": false, "airDateUtc": "2020-01-08T00:00:00Z",
				"series": map[string]any{"title": "Another Show"}},
		}

		all, err := GetMissing(context.Background(), MissingFilter{})
		if err != nil {
			t.Fatalf("GetMissing: %v", err)
		}
		if all.TotalCount != 2 || all.SeriesCount != 2 {
			t.Errorf("total=%d series=%d, want 2/2", all.TotalCount, all.SeriesCount)
		}
		if all.Episodes[0].Series != "The Expanse" {
			t.Errorf("the series name did not survive: %+v", all.Episodes[0])
		}
	})
}

// Monitoring is the switch a search reads, so this is the step that makes
// "download only season 3" possible at all. The season flag has to reach Sonarr
// inside a resource that is otherwise untouched — a PUT is a whole-resource
// write, and anything this package failed to send would be written as absent.
func TestSetSeasonMonitoredEditsOneSeasonInPlace(t *testing.T) {
	m := newMockSonarr(t)
	m.seriesByID = map[string]any{
		"id": 42, "title": "The Expanse", "year": 2015, "tvdbId": 280619,
		"monitored": true, "statistics": stats(10, 13, 30, 100),
		// Fields this package does not model. A rebuilt body would drop them.
		"titleSlug": "the-expanse", "path": "/tv/The Expanse", "qualityProfileId": 4,
		"tags": []any{1.0, 2.0},
		"seasons": []map[string]any{
			{"seasonNumber": 1, "monitored": true, "statistics": stats(10, 10, 10, 50)},
			// Sonarr's episodeCount is "(monitored AND aired) OR has a file", so
			// an unmonitored season reports zero of everything but the total.
			{"seasonNumber": 2, "monitored": false, "statistics": stats(0, 0, 13, 0)},
			{"seasonNumber": 3, "monitored": false, "statistics": stats(0, 0, 7, 0)},
		},
	}
	// 13 episodes in season 2: 9 have aired with no file, 4 have not aired.
	m.episodes = append(m.episodes, episodesFor(42, 2, 9, aired, noFile)...)
	m.episodes = append(m.episodes, episodesFor(42, 2, 4, unaired, noFile)...)
	m.episodes = append(m.episodes, episodesFor(42, 1, 10, aired, onDisk)...)

	plan, err := PlanSeasonMonitor(context.Background(), SeasonMonitorRequest{
		SeriesID: 42, Season: 2, Monitored: true,
	})
	if err != nil {
		t.Fatalf("PlanSeasonMonitor: %v", err)
	}
	if plan.AlreadySet || plan.WasMonitored {
		t.Errorf("plan = %+v, want a season that is currently unmonitored", plan)
	}
	if plan.EpisodesTotal != 13 {
		t.Errorf("EpisodesTotal = %d, want 13 — it is the size of the operation and part "+
			"of the fingerprint", plan.EpisodesTotal)
	}
	// The number the confirmation exists to state. Read off the season
	// statistics it would be 0, because those are derived from the very flag
	// being changed — which is the one case where the user must not be told
	// "nothing will be fetched".
	if plan.EpisodesMissing != 9 || plan.EpisodesAired != 9 {
		t.Errorf("aired=%d missing=%d, want 9/9 counted from the episodes rather than "+
			"from statistics that assume the current monitoring flag",
			plan.EpisodesAired, plan.EpisodesMissing)
	}

	res, err := SetSeasonMonitored(context.Background(), plan)
	if err != nil {
		t.Fatalf("SetSeasonMonitored: %v", err)
	}
	if !res.Changed || res.NotApplied {
		t.Errorf("result = %+v, want a change that verified", res)
	}

	// Only season 2 moved, and everything else came back untouched.
	seasons, _ := m.putBody["seasons"].([]any)
	if len(seasons) != 3 {
		t.Fatalf("put body carried %d seasons, want all 3", len(seasons))
	}
	want := map[int]bool{1: true, 2: true, 3: false}
	for _, entry := range seasons {
		s := entry.(map[string]any)
		n := int(s["seasonNumber"].(float64))
		if got := s["monitored"].(bool); got != want[n] {
			t.Errorf("season %d monitored = %v, want %v", n, got, want[n])
		}
	}
	for _, field := range []string{"titleSlug", "path", "qualityProfileId", "tags"} {
		if m.putBody[field] == nil {
			t.Errorf("the stored resource lost %q — a PUT writes the whole series", field)
		}
	}
}

func TestSeasonMonitorRefusesAndReportsWhatItCannotDo(t *testing.T) {
	m := newMockSonarr(t)
	m.seriesByID = map[string]any{
		"id": 42, "title": "The Expanse", "year": 2015, "monitored": false,
		"statistics": stats(0, 5, 20, 0),
		"seasons": []map[string]any{
			{"seasonNumber": 1, "monitored": true, "statistics": stats(0, 5, 10, 0)},
			{"seasonNumber": 2, "monitored": false, "statistics": stats(0, 0, 10, 0)},
		},
	}

	// Asking for the state it already has is not an error and not a write.
	t.Run("already in that state", func(t *testing.T) {
		plan, err := PlanSeasonMonitor(context.Background(), SeasonMonitorRequest{
			SeriesID: 42, Season: 1, Monitored: true,
		})
		if err != nil {
			t.Fatalf("PlanSeasonMonitor: %v", err)
		}
		if !plan.AlreadySet {
			t.Fatal("season 1 is already monitored and should be reported as such")
		}

		m.putBody = nil
		res, err := SetSeasonMonitored(context.Background(), plan)
		if err != nil {
			t.Fatalf("SetSeasonMonitored: %v", err)
		}
		if res.Changed || m.putBody != nil {
			t.Error("a no-op must not write anything to sonarr")
		}
		if !containsSubstring(res.Warnings, "already") {
			t.Errorf("warnings = %v", res.Warnings)
		}
	})

	// The switch that outranks this one: an unmonitored series will not grab
	// anything however the season is set, and stopping short of saying so leaves
	// someone waiting for downloads that cannot start.
	t.Run("series itself unmonitored", func(t *testing.T) {
		plan, err := PlanSeasonMonitor(context.Background(), SeasonMonitorRequest{
			SeriesID: 42, Season: 2, Monitored: true,
		})
		if err != nil {
			t.Fatalf("PlanSeasonMonitor: %v", err)
		}
		res, err := SetSeasonMonitored(context.Background(), plan)
		if err != nil {
			t.Fatalf("SetSeasonMonitored: %v", err)
		}
		if !containsSubstring(res.Warnings, "itself is not monitored") {
			t.Errorf("warnings = %v", res.Warnings)
		}
	})

	t.Run("a season the series does not have", func(t *testing.T) {
		_, err := PlanSeasonMonitor(context.Background(), SeasonMonitorRequest{
			SeriesID: 42, Season: 9, Monitored: true,
		})
		if err == nil || !strings.Contains(err.Error(), "no season 9") {
			t.Fatalf("a season that does not exist should be refused, got: %v", err)
		}
	})
}

// Monitoring decides what a search looks for; it does not search. A result that
// did not say so would read as "the episodes are on their way".
func TestSeasonMonitorSaysItStartedNoSearch(t *testing.T) {
	m := newMockSonarr(t)
	m.seriesByID = map[string]any{
		"id": 42, "title": "The Expanse", "year": 2015, "monitored": true,
		"statistics": stats(0, 0, 20, 0),
		"seasons": []map[string]any{
			{"seasonNumber": 3, "monitored": false, "statistics": stats(0, 7, 7, 0)},
		},
	}
	m.episodes = episodesFor(42, 3, 7, aired, noFile)

	plan, err := PlanSeasonMonitor(context.Background(), SeasonMonitorRequest{
		SeriesID: 42, Season: 3, Monitored: true,
	})
	if err != nil {
		t.Fatalf("PlanSeasonMonitor: %v", err)
	}
	res, err := SetSeasonMonitored(context.Background(), plan)
	if err != nil {
		t.Fatalf("SetSeasonMonitored: %v", err)
	}

	if !containsSubstring(res.Warnings, "does not start a search") {
		t.Errorf("warnings = %v", res.Warnings)
	}
	// And it names the way to start one, with the arguments to use.
	if !containsSubstring(res.Warnings, "series_id 42 and season 3") {
		t.Errorf("the warning should carry the search arguments: %v", res.Warnings)
	}
}

// Turning monitoring off is the quiet direction and must not borrow the loud
// direction's wording.
func TestSeasonUnmonitorSaysWhatItDoesNotDo(t *testing.T) {
	newMockSonarr(t).seriesByID = map[string]any{
		"id": 42, "title": "The Expanse", "year": 2015, "monitored": true,
		"statistics": stats(10, 10, 20, 100),
		"seasons": []map[string]any{
			{"seasonNumber": 1, "monitored": true, "statistics": stats(10, 10, 10, 100)},
		},
	}

	plan, err := PlanSeasonMonitor(context.Background(), SeasonMonitorRequest{
		SeriesID: 42, Season: 1, Monitored: false,
	})
	if err != nil {
		t.Fatalf("PlanSeasonMonitor: %v", err)
	}
	res, err := SetSeasonMonitored(context.Background(), plan)
	if err != nil {
		t.Fatalf("SetSeasonMonitored: %v", err)
	}

	if !res.Changed || res.Monitored {
		t.Errorf("result = %+v, want the season switched off", res)
	}
	if !containsSubstring(res.Warnings, "Nothing on disk") &&
		!containsSubstring(res.Warnings, "stays in the queue") {
		t.Errorf("unmonitoring should say what it leaves alone: %v", res.Warnings)
	}
	if containsSubstring(res.Warnings, "does not start a search") {
		t.Errorf("that warning belongs to the other direction: %v", res.Warnings)
	}
}

func containsSubstring(all []string, want string) bool {
	for _, s := range all {
		if strings.Contains(s, want) {
			return true
		}
	}
	return false
}

func newMockSonarrWithExpanse(t *testing.T) *mockSonarr {
	t.Helper()
	m := newMockSonarr(t)
	m.seriesByID = map[string]any{
		"id": 42, "title": "The Expanse", "year": 2015, "tvdbId": 280619,
		"monitored": true, "statistics": stats(20, 23, 30, 100),
		"seasons": []map[string]any{
			{"seasonNumber": 1, "monitored": true, "statistics": stats(10, 10, 10, 50)},
			{"seasonNumber": 2, "monitored": true, "statistics": stats(10, 13, 13, 50)},
		},
	}
	m.episodes = []map[string]any{
		{"id": 501, "seriesId": 42, "seasonNumber": 2, "episodeNumber": 3, "title": "Delta-V",
			"monitored": true, "hasFile": false, "airDateUtc": "2018-01-01T00:00:00Z"},
	}
	return m
}

func TestBadAPIKeyIsNamed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	t.Setenv(BaseURLEnv, srv.URL)
	t.Setenv(APIKeyEnv, "wrong")

	_, err := GetQueue(context.Background())
	if err == nil || !strings.Contains(err.Error(), APIKeyEnv) {
		t.Fatalf("a 401 should point at the API key variable, got: %v", err)
	}
}

// Sonarr's own rejections carry the reason; losing them behind "400" would make
// every failed add unexplainable.
func TestValidationMessagesSurvive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`[{"propertyName":"Path","errorMessage":"Folder is not writable"}]`))
	}))
	defer srv.Close()

	t.Setenv(BaseURLEnv, srv.URL)
	t.Setenv(APIKeyEnv, "k")

	_, err := GetQueue(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Folder is not writable") {
		t.Fatalf("sonarr's own message should reach the caller, got: %v", err)
	}
}
