package radarr

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
		// The documented case: a bare http host means a direct Radarr.
		{"http://localhost", "http://localhost:7878"},
		{"localhost", "http://localhost:7878"},
		{"http://10.0.0.4/", "http://10.0.0.4:7878"},

		// Anything that reaches Radarr through something else is left alone.
		{"http://localhost:8310", "http://localhost:8310"},
		{"https://radarr.example.com", "https://radarr.example.com"},
		{"http://nas/radarr", "http://nas/radarr"},
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

// mockRadarr serves canned responses and records what was asked of it.
type mockRadarr struct {
	*httptest.Server

	queueRecords []map[string]any
	movies       []map[string]any
	profiles     []map[string]any
	rootFolders  []map[string]any
	lookupTmdb   map[string]any
	lookupTerm   []map[string]any
	movieByID    map[string]any

	postedBody    map[string]any
	postedCommand map[string]any
	deletedURL    string
	sawAPIKey     string
}

func newMockRadarr(t *testing.T) *mockRadarr {
	t.Helper()
	m := &mockRadarr{}

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
	mux.HandleFunc("/api/v3/movie", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(body, &m.postedBody); err != nil {
				t.Errorf("posted body is not JSON: %v", err)
			}
			writeJSON(w, map[string]any{
				"id": 77, "title": m.postedBody["title"], "monitored": m.postedBody["monitored"],
				"path": "/movies/Whatever (2021)",
			})
			return
		}
		// Radarr filters its own listing by tmdbId, which is how a TMDB id is
		// resolved back to a movie.
		if tmdb := r.URL.Query().Get("tmdbId"); tmdb != "" {
			var hits []map[string]any
			for _, mv := range append(m.movies, nonNil(m.movieByID)...) {
				if fmt.Sprint(mv["tmdbId"]) == tmdb {
					hits = append(hits, mv)
				}
			}
			writeJSON(w, hits)
			return
		}
		writeJSON(w, m.movies)
	})
	// Registered before /api/v3/movie/ so it is not swallowed by it.
	mux.HandleFunc("/api/v3/command", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &m.postedCommand); err != nil {
			t.Errorf("command body is not JSON: %v", err)
		}
		writeJSON(w, map[string]any{"id": 1234, "name": m.postedCommand["name"], "status": "queued"})
	})
	mux.HandleFunc("/api/v3/movie/{id}", func(w http.ResponseWriter, r *http.Request) {
		if m.movieByID == nil || fmt.Sprint(m.movieByID["id"]) != r.PathValue("id") {
			http.Error(w, `{"message":"NotFound"}`, http.StatusNotFound)
			return
		}
		if r.Method == http.MethodDelete {
			m.deletedURL = r.URL.String()
			m.movieByID = nil // it left the library
			w.WriteHeader(http.StatusOK)
			return
		}
		writeJSON(w, m.movieByID)
	})
	mux.HandleFunc("/api/v3/movie/lookup/tmdb", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, m.lookupTmdb)
	})
	mux.HandleFunc("/api/v3/movie/lookup", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, m.lookupTerm)
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

func TestGetQueueClassifiesAndSorts(t *testing.T) {
	m := newMockRadarr(t)
	m.queueRecords = []map[string]any{
		{
			"id": 1, "title": "Healthy.Movie.2021.1080p", "status": "downloading",
			"size": 1000.0, "sizeleft": 400.0, "timeleft": "00:10:00",
			"movie":          map[string]any{"title": "Healthy Movie", "year": 2021},
			"downloadClient": "sab",
		},
		{
			"id": 2, "title": "Stuck.Movie.2019.1080p", "status": "downloading",
			"size": 1000.0, "sizeleft": 950.0, "timeleft": "",
			"movie": map[string]any{"title": "Stuck Movie", "year": 2019},
		},
		{
			"id": 3, "title": "Blocked.Movie.2020.1080p", "status": "completed",
			"trackedDownloadState": "importBlocked", "trackedDownloadStatus": "warning",
			"size": 1000.0, "sizeleft": 0.0,
			"movie":          map[string]any{"title": "Blocked Movie", "year": 2020},
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
	if byID[1].Stalled {
		t.Error("an item with a time remaining was called stalled")
	}
	if !byID[2].Stalled {
		t.Error("an incomplete download with no time remaining should be stalled")
	}
	if byID[3].Stalled {
		t.Error("a finished download waiting to import is not stalled")
	}

	// The message Radarr recorded is why the import is blocked, so it must
	// survive into the warning rather than being replaced by the status.
	joined := strings.Join(q.Warnings, "\n")
	for _, want := range []string{"Blocked Movie", "no files found", "Stuck Movie", "stalled"} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "Healthy Movie") {
		t.Errorf("a working download produced a warning:\n%s", joined)
	}
}

func TestGetLibrarySeparatesMissingFromUnreleased(t *testing.T) {
	m := newMockRadarr(t)
	m.movies = []map[string]any{
		{"id": 1, "title": "Downloaded", "year": 2001, "monitored": true, "hasFile": true,
			"isAvailable": true, "sizeOnDisk": 100, "status": "released"},
		{"id": 2, "title": "Owed To You", "year": 2011, "monitored": true, "hasFile": false,
			"isAvailable": true, "status": "released"},
		{"id": 3, "title": "Next Year", "year": 2027, "monitored": true, "hasFile": false,
			"isAvailable": false, "status": "announced"},
		{"id": 4, "title": "Ignored", "year": 1999, "monitored": false, "hasFile": false,
			"isAvailable": true, "status": "released"},
	}

	lib, err := GetLibrary(context.Background(), MovieFilter{})
	if err != nil {
		t.Fatalf("GetLibrary: %v", err)
	}

	if lib.TotalCount != 4 || lib.MonitoredCount != 3 || lib.DownloadedCount != 1 {
		t.Errorf("total=%d monitored=%d downloaded=%d, want 4/3/1",
			lib.TotalCount, lib.MonitoredCount, lib.DownloadedCount)
	}
	// The whole point: one is Radarr failing, the other is a film that is not out.
	if lib.MissingCount != 1 || lib.UnreleasedCount != 1 {
		t.Errorf("missing=%d unreleased=%d, want 1/1", lib.MissingCount, lib.UnreleasedCount)
	}
	if lib.Movies[0].Title != "Owed To You" {
		t.Errorf("first movie = %q, want the missing one first", lib.Movies[0].Title)
	}

	// An unmonitored movie with no file must never be counted as missing.
	filtered, err := GetLibrary(context.Background(), MovieFilter{OnlyMissing: true})
	if err != nil {
		t.Fatalf("GetLibrary(only_missing): %v", err)
	}
	if len(filtered.Movies) != 1 || filtered.Movies[0].ID != 2 {
		t.Errorf("only_missing returned %d movies, want just 'Owed To You'", len(filtered.Movies))
	}
	if filtered.TotalCount != 4 {
		t.Errorf("counts must describe the whole library even under a filter, got total=%d",
			filtered.TotalCount)
	}
}

func TestPlanRefusesWhatItCannotDecide(t *testing.T) {
	m := newMockRadarr(t)
	m.lookupTmdb = map[string]any{"id": 0, "title": "Dune", "year": 2021, "tmdbId": 438631}
	m.rootFolders = []map[string]any{{"id": 1, "path": "/movies", "accessible": true}}
	m.profiles = []map[string]any{
		{"id": 4, "name": "HD-1080p"},
		{"id": 6, "name": "Ultra-HD"},
	}

	// Named by name, it resolves.
	plan, err := Plan(context.Background(), AddRequest{TmdbID: 438631, QualityProfile: "Ultra-HD"})
	if err != nil {
		t.Fatalf("Plan with a named profile: %v", err)
	}
	if plan.QualityProfileID != 6 || plan.RootFolderPath != "/movies" {
		t.Errorf("resolved to profile %d in %q, want 6 in /movies",
			plan.QualityProfileID, plan.RootFolderPath)
	}
	if plan.MinimumAvailability != "released" {
		t.Errorf("minimum availability defaulted to %q, want released", plan.MinimumAvailability)
	}

	// By id, too.
	if plan, err := Plan(context.Background(), AddRequest{TmdbID: 438631, QualityProfile: "4"}); err != nil {
		t.Errorf("Plan by profile id: %v", err)
	} else if plan.QualityProfileID != 4 {
		t.Errorf("profile id 4 resolved to %d", plan.QualityProfileID)
	}

	// An unreachable root folder means a download that imports nowhere.
	m.rootFolders = []map[string]any{{"id": 1, "path": "/movies", "accessible": false}}
	if _, err := Plan(context.Background(), AddRequest{TmdbID: 438631, QualityProfile: "4"}); err == nil {
		t.Error("Plan should refuse an inaccessible root folder")
	}
}

// Naming no profile means 1080p, which is what someone asking for a film
// without saying more wants.
func TestPlanDefaultsToHD1080p(t *testing.T) {
	m := newMockRadarr(t)
	m.lookupTmdb = map[string]any{"id": 0, "title": "Dune", "year": 2021, "tmdbId": 438631}
	m.rootFolders = []map[string]any{{"id": 1, "path": "/movies", "accessible": true}}

	t.Run("picked out of several", func(t *testing.T) {
		m.profiles = []map[string]any{
			{"id": 6, "name": "Ultra-HD"},
			{"id": 4, "name": "HD-1080p"},
			{"id": 2, "name": "SD"},
		}

		plan, err := Plan(context.Background(), AddRequest{TmdbID: 438631})
		if err != nil {
			t.Fatalf("Plan with no profile named: %v", err)
		}
		if plan.QualityProfileID != 4 || plan.QualityProfileName != "HD-1080p" {
			t.Errorf("defaulted to %q (id %d), want HD-1080p",
				plan.QualityProfileName, plan.QualityProfileID)
		}
	})

	// Profile names are user-defined, so the match is by name and forgiving of
	// case — but nothing else is guessed from it.
	t.Run("matched case-insensitively", func(t *testing.T) {
		m.profiles = []map[string]any{
			{"id": 6, "name": "Ultra-HD"},
			{"id": 9, "name": "hd-1080P"},
		}

		plan, err := Plan(context.Background(), AddRequest{TmdbID: 438631})
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		if plan.QualityProfileID != 9 {
			t.Errorf("resolved to id %d, want 9", plan.QualityProfileID)
		}
	})

	// An explicit choice still wins over the default.
	t.Run("overridden by the caller", func(t *testing.T) {
		m.profiles = []map[string]any{
			{"id": 4, "name": "HD-1080p"},
			{"id": 6, "name": "Ultra-HD"},
		}

		plan, err := Plan(context.Background(), AddRequest{
			TmdbID: 438631, QualityProfile: "Ultra-HD",
		})
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		if plan.QualityProfileID != 6 {
			t.Errorf("resolved to id %d, want the profile that was asked for", plan.QualityProfileID)
		}
	})

	// Renamed or deleted: the only profile there is beats refusing.
	t.Run("falls back to the only profile", func(t *testing.T) {
		m.profiles = []map[string]any{{"id": 7, "name": "Anything Goes"}}

		plan, err := Plan(context.Background(), AddRequest{TmdbID: 438631})
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

		_, err := Plan(context.Background(), AddRequest{TmdbID: 438631})
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

// The cover is what a person recognises the film by, so it has to survive into
// the plan the confirmation is built from.
func TestPlanCarriesThePoster(t *testing.T) {
	m := newMockRadarr(t)
	m.profiles = []map[string]any{{"id": 4, "name": "HD-1080p"}}
	m.rootFolders = []map[string]any{{"id": 1, "path": "/movies", "accessible": true}}

	base := map[string]any{"id": 0, "title": "Dune", "year": 2021, "tmdbId": 438631}

	t.Run("prefers the poster's remoteUrl", func(t *testing.T) {
		m.lookupTmdb = merge(base, map[string]any{
			// `url` is Radarr's own /MediaCover path, which holds nothing for a
			// movie that is not in the library yet.
			"images": []map[string]any{
				{"coverType": "fanart", "remoteUrl": "https://image.tmdb.org/fanart.jpg"},
				{"coverType": "poster", "url": "/MediaCover/0/poster.jpg",
					"remoteUrl": "https://image.tmdb.org/poster.jpg"},
			},
			"remotePoster": "https://image.tmdb.org/fallback.jpg",
		})

		plan, err := Plan(context.Background(), AddRequest{TmdbID: 438631})
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		if plan.PosterURL != "https://image.tmdb.org/poster.jpg" {
			t.Errorf("PosterURL = %q, want the poster's remoteUrl", plan.PosterURL)
		}
	})

	t.Run("falls back to remotePoster", func(t *testing.T) {
		m.lookupTmdb = merge(base, map[string]any{
			"remotePoster": "https://image.tmdb.org/fallback.jpg",
		})

		plan, err := Plan(context.Background(), AddRequest{TmdbID: 438631})
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		if plan.PosterURL != "https://image.tmdb.org/fallback.jpg" {
			t.Errorf("PosterURL = %q, want the remotePoster fallback", plan.PosterURL)
		}
	})

	t.Run("drops a scheme that does something when clicked", func(t *testing.T) {
		m.lookupTmdb = merge(base, map[string]any{
			"images": []map[string]any{
				{"coverType": "poster", "remoteUrl": "javascript:alert(1)"},
			},
			"remotePoster": "data:text/html;base64,PHNjcmlwdD4=",
		})

		plan, err := Plan(context.Background(), AddRequest{TmdbID: 438631})
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		if plan.PosterURL != "" {
			t.Errorf("PosterURL = %q, only http(s) should be passed on", plan.PosterURL)
		}
	})

	// The image has nothing to do with what gets added, and the provider rotates
	// those paths: a cover that changed between the confirmation and the retry
	// must not refuse an approval the user genuinely gave.
	t.Run("is not part of the fingerprint", func(t *testing.T) {
		m.lookupTmdb = merge(base, map[string]any{"remotePoster": "https://image.tmdb.org/a.jpg"})
		first, err := Plan(context.Background(), AddRequest{TmdbID: 438631})
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}

		m.lookupTmdb = merge(base, map[string]any{"remotePoster": "https://image.tmdb.org/b.jpg"})
		second, err := Plan(context.Background(), AddRequest{TmdbID: 438631})
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}

		if first.PosterURL == second.PosterURL {
			t.Fatal("the test did not actually change the poster")
		}
		if !slices.Equal(first.Fingerprint(), second.Fingerprint()) {
			t.Errorf("a changed cover changed the fingerprint:\n%v\n%v",
				first.Fingerprint(), second.Fingerprint())
		}
	})
}

func TestLookupCarriesThePoster(t *testing.T) {
	m := newMockRadarr(t)
	m.lookupTerm = []map[string]any{{
		"id": 0, "title": "Dune", "year": 2021, "tmdbId": 438631,
		"images": []map[string]any{
			{"coverType": "poster", "remoteUrl": "https://image.tmdb.org/poster.jpg"},
		},
	}}

	results, err := Lookup(context.Background(), "dune", 0)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(results) != 1 || results[0].PosterURL != "https://image.tmdb.org/poster.jpg" {
		t.Errorf("lookup did not return the cover: %+v", results)
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

func TestPlanRefusesAMovieAlreadyAdded(t *testing.T) {
	m := newMockRadarr(t)
	m.lookupTmdb = map[string]any{"id": 12, "title": "Dune", "year": 2021, "tmdbId": 438631}
	m.profiles = []map[string]any{{"id": 4, "name": "HD-1080p"}}
	m.rootFolders = []map[string]any{{"id": 1, "path": "/movies", "accessible": true}}

	_, err := Plan(context.Background(), AddRequest{TmdbID: 438631})
	if err == nil || !strings.Contains(err.Error(), "already in your library") {
		t.Fatalf("adding a movie Radarr already has should be refused by name, got: %v", err)
	}
}

func TestAddSendsTheResolvedPlan(t *testing.T) {
	m := newMockRadarr(t)
	m.lookupTmdb = map[string]any{
		"id": 0, "title": "Dune", "year": 2021, "tmdbId": 438631,
		// Fields Radarr fills in and expects back untouched.
		"titleSlug": "dune-438631", "images": []any{map[string]any{"coverType": "poster"}},
	}
	m.profiles = []map[string]any{{"id": 4, "name": "HD-1080p"}}
	m.rootFolders = []map[string]any{{"id": 1, "path": "/movies", "accessible": true}}

	plan, err := Plan(context.Background(), AddRequest{
		TmdbID: 438631, Monitored: true, SearchOnAdd: true,
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	res, err := Add(context.Background(), plan)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if res.MovieID != 77 || res.Title != "Dune" {
		t.Errorf("result = %+v, want movie 77 'Dune'", res)
	}

	body := m.postedBody
	if body["qualityProfileId"] != 4.0 {
		t.Errorf("qualityProfileId = %v, want 4", body["qualityProfileId"])
	}
	if body["rootFolderPath"] != "/movies" {
		t.Errorf("rootFolderPath = %v, want /movies", body["rootFolderPath"])
	}
	if body["minimumAvailability"] != "released" {
		t.Errorf("minimumAvailability = %v, want released", body["minimumAvailability"])
	}
	// The lookup resource is posted back whole: Radarr uses these and nothing
	// here should be synthesising them.
	if body["titleSlug"] != "dune-438631" || body["images"] == nil {
		t.Errorf("the lookup resource was not sent back intact: %v", body)
	}
	opts, ok := body["addOptions"].(map[string]any)
	if !ok || opts["searchForMovie"] != true || opts["monitor"] != "movieOnly" {
		t.Errorf("addOptions = %v, want a monitored search", body["addOptions"])
	}
}

func TestAddWarnsWhenNothingWillHappen(t *testing.T) {
	m := newMockRadarr(t)
	m.lookupTmdb = map[string]any{"id": 0, "title": "Dune", "year": 2021, "tmdbId": 438631}
	m.profiles = []map[string]any{{"id": 4, "name": "HD-1080p"}}
	m.rootFolders = []map[string]any{{"id": 1, "path": "/movies", "accessible": true}}

	plan, err := Plan(context.Background(), AddRequest{TmdbID: 438631, Monitored: false})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	res, err := Add(context.Background(), plan)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if len(res.Warnings) == 0 {
		t.Error("adding an unmonitored movie does nothing and should say so")
	}
}

func TestRemoveFromQueuePassesTheFlagsAndVerifies(t *testing.T) {
	m := newMockRadarr(t)
	m.queueRecords = []map[string]any{{
		"id": 9, "title": "Stuck.Movie.2019", "status": "downloading",
		"size": 1000.0, "sizeleft": 900.0,
		"movie": map[string]any{"title": "Stuck Movie", "year": 2019},
	}}

	item, err := FindQueueItem(context.Background(), 9)
	if err != nil {
		t.Fatalf("FindQueueItem: %v", err)
	}
	if item.DisplayName() != "Stuck Movie (2019)" {
		t.Errorf("display name = %q", item.DisplayName())
	}

	res, err := RemoveFromQueue(context.Background(), item, RemoveOptions{
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
	// Blocklisting without skipping the redownload means Radarr goes looking
	// again, which is a consequence worth stating.
	if len(res.Warnings) == 0 {
		t.Error("a blocklist that triggers a fresh search should warn")
	}

	// A stale id must not resolve to whatever now holds that number.
	if _, err := FindQueueItem(context.Background(), 9); err == nil {
		t.Error("FindQueueItem should fail once the item is gone")
	}
}

// The gap this closes: a download removed from the queue leaves the movie
// monitored and missing, and re-adding it is refused by Radarr. Searching is
// the operation that was missing.
func TestSearchStartsTheRadarrCommand(t *testing.T) {
	m := newMockRadarr(t)
	m.movieByID = map[string]any{
		"id": 42, "title": "Owed To You", "year": 2011, "tmdbId": 999,
		"monitored": true, "hasFile": false, "isAvailable": true, "status": "released",
	}

	movie, err := GetMovie(context.Background(), 42)
	if err != nil {
		t.Fatalf("GetMovie: %v", err)
	}
	if !movie.Missing {
		t.Error("a monitored, available movie with no file should read as missing")
	}

	res, err := Search(context.Background(), movie)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if m.postedCommand["name"] != "MoviesSearch" {
		t.Errorf("command name = %v, want MoviesSearch", m.postedCommand["name"])
	}
	ids, _ := m.postedCommand["movieIds"].([]any)
	if len(ids) != 1 || ids[0] != 42.0 {
		t.Errorf("movieIds = %v, want [42]", m.postedCommand["movieIds"])
	}
	if res.CommandID != 1234 || res.CommandStatus != "queued" {
		t.Errorf("result = %+v, want the queued command", res)
	}
	// Queued is not found. Nothing here may read as "downloaded".
	if res.Warnings != nil {
		t.Errorf("a monitored, missing, available movie needs no warning: %v", res.Warnings)
	}
}

func TestSearchWarnsWhenItWillProbablyDoNothing(t *testing.T) {
	m := newMockRadarr(t)

	t.Run("unmonitored", func(t *testing.T) {
		m.movieByID = map[string]any{"id": 42, "title": "Ignored", "monitored": false,
			"hasFile": false, "isAvailable": true}
		movie, _ := GetMovie(context.Background(), 42)
		res, err := Search(context.Background(), movie)
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if !containsSubstring(res.Warnings, "not monitored") {
			t.Errorf("warnings = %v", res.Warnings)
		}
	})

	t.Run("already downloaded", func(t *testing.T) {
		m.movieByID = map[string]any{"id": 42, "title": "Have It", "monitored": true,
			"hasFile": true, "isAvailable": true}
		movie, _ := GetMovie(context.Background(), 42)
		res, err := Search(context.Background(), movie)
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if !containsSubstring(res.Warnings, "upgrade") {
			t.Errorf("warnings = %v", res.Warnings)
		}
	})
}

// Radarr's own id and the TMDB id are different numbers for the same film, both
// plain integers, so the wrong one gets passed and /movie/{id} answers 404.
// Resolving it is what keeps a correct request from failing over a naming
// detail.
func TestGetMovieAcceptsEitherID(t *testing.T) {
	m := newMockRadarr(t)
	m.movieByID = map[string]any{
		"id": 42, "title": "Owed To You", "year": 2011, "tmdbId": 438631,
		"monitored": true, "hasFile": true, "isAvailable": true,
	}

	t.Run("radarr id", func(t *testing.T) {
		movie, err := GetMovie(context.Background(), 42)
		if err != nil {
			t.Fatalf("GetMovie(42): %v", err)
		}
		if movie.ID != 42 {
			t.Errorf("resolved to movie %d, want 42", movie.ID)
		}
	})

	t.Run("tmdb id", func(t *testing.T) {
		movie, err := GetMovie(context.Background(), 438631)
		if err != nil {
			t.Fatalf("GetMovie(438631) should have fallen back to the TMDB id: %v", err)
		}
		if movie.ID != 42 || movie.Title != "Owed To You" {
			t.Errorf("resolved to %+v, want movie 42", movie)
		}
		// The caller tells the two apart by comparing: what came back is not the
		// number that went in, which is what the confirmation reports.
		if movie.ID == 438631 {
			t.Error("the fallback must return Radarr's id, not the number asked for")
		}
	})

	t.Run("neither", func(t *testing.T) {
		_, err := GetMovie(context.Background(), 999999)
		if err == nil {
			t.Fatal("an id in neither space should fail")
		}
		// Naming only one of them sends the reader looking in the wrong place.
		for _, want := range []string{"Radarr id 999999", "TMDB id 999999"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the error should mention %q, got: %v", want, err)
			}
		}
	})
}

// The 404 as reported: a delete driven by the TMDB id must land on the movie,
// and must address it by Radarr's id on the wire.
func TestDeleteWorksThroughATmdbID(t *testing.T) {
	m := newMockRadarr(t)
	m.movieByID = map[string]any{
		"id": 42, "title": "Owed To You", "year": 2011, "tmdbId": 438631, "hasFile": true,
	}

	movie, err := GetMovie(context.Background(), 438631)
	if err != nil {
		t.Fatalf("GetMovie: %v", err)
	}
	if _, err := Delete(context.Background(), movie, DeleteOptions{DeleteFiles: true}); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if !strings.Contains(m.deletedURL, "/api/v3/movie/42") {
		t.Errorf("delete URL = %q, want it addressed by Radarr's id 42", m.deletedURL)
	}
	if strings.Contains(m.deletedURL, "438631") {
		t.Errorf("the TMDB id reached the request path: %q", m.deletedURL)
	}
}

// The user's actual sequence: remove the download, then wonder why nothing
// happens. Removing is not blocklisting, so Radarr starts no replacement.
func TestRemovalSaysNothingIsLookingAnymore(t *testing.T) {
	m := newMockRadarr(t)
	m.queueRecords = []map[string]any{{
		"id": 9, "title": "Stuck.Movie.2019", "status": "downloading",
		"movieId": 42, "size": 1000.0, "sizeleft": 900.0,
		"movie": map[string]any{"title": "Stuck Movie", "year": 2019},
	}}

	item, err := FindQueueItem(context.Background(), 9)
	if err != nil {
		t.Fatalf("FindQueueItem: %v", err)
	}
	res, err := RemoveFromQueue(context.Background(), item, RemoveOptions{
		QueueID: 9, RemoveFromClient: true,
	})
	if err != nil {
		t.Fatalf("RemoveFromQueue: %v", err)
	}

	if !containsSubstring(res.Warnings, "radarr_movie_search") {
		t.Errorf("a plain removal should point at the way back: %v", res.Warnings)
	}
	if !containsSubstring(res.Warnings, "movie_id 42") {
		t.Errorf("the warning should carry the id to search with: %v", res.Warnings)
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

// Forgetting a movie and erasing it are different requests that share an
// endpoint, so the flag has to reach Radarr exactly as it was approved.
func TestDeletePassesTheFlagsAndVerifies(t *testing.T) {
	t.Run("keeps the files by default", func(t *testing.T) {
		m := newMockRadarr(t)
		m.movieByID = map[string]any{
			"id": 42, "title": "Owed To You", "year": 2011, "tmdbId": 999,
			"monitored": true, "hasFile": true, "isAvailable": true,
			"sizeOnDisk": 8_000_000_000, "path": "/movies/Owed To You (2011)",
		}

		movie, err := GetMovie(context.Background(), 42)
		if err != nil {
			t.Fatalf("GetMovie: %v", err)
		}

		res, err := Delete(context.Background(), movie, DeleteOptions{MovieID: 42})
		if err != nil {
			t.Fatalf("Delete: %v", err)
		}

		for _, want := range []string{"/api/v3/movie/42", "deleteFiles=false", "addImportExclusion=false"} {
			if !strings.Contains(m.deletedURL, want) {
				t.Errorf("delete URL %q is missing %q", m.deletedURL, want)
			}
		}
		if res.FreedBytes != 0 {
			t.Errorf("FreedBytes = %d, nothing was deleted", res.FreedBytes)
		}
		if res.StillPresent {
			t.Error("the movie left the library but was reported as still there")
		}
		// Files nobody tracks anymore are a surprise later, not an error now.
		if !containsSubstring(res.Warnings, "still on disk") {
			t.Errorf("warnings = %v", res.Warnings)
		}
		if !containsSubstring(res.Warnings, "/movies/Owed To You (2011)") {
			t.Errorf("the warning should name the folder: %v", res.Warnings)
		}
	})

	t.Run("erases when asked", func(t *testing.T) {
		m := newMockRadarr(t)
		m.movieByID = map[string]any{
			"id": 42, "title": "Owed To You", "year": 2011,
			"hasFile": true, "sizeOnDisk": 8_000_000_000,
		}

		movie, _ := GetMovie(context.Background(), 42)
		res, err := Delete(context.Background(), movie, DeleteOptions{
			MovieID: 42, DeleteFiles: true, AddImportExclusion: true,
		})
		if err != nil {
			t.Fatalf("Delete: %v", err)
		}

		for _, want := range []string{"deleteFiles=true", "addImportExclusion=true"} {
			if !strings.Contains(m.deletedURL, want) {
				t.Errorf("delete URL %q is missing %q", m.deletedURL, want)
			}
		}
		if res.FreedBytes != 8_000_000_000 || !res.FilesDeleted {
			t.Errorf("result = %+v, want the freed size reported", res)
		}
		if containsSubstring(res.Warnings, "still on disk") {
			t.Errorf("nothing is still on disk: %v", res.Warnings)
		}
		if !containsSubstring(res.Warnings, "import exclusion") {
			t.Errorf("an exclusion outlives the movie and should be stated: %v", res.Warnings)
		}
	})
}

func TestDeleteRejectsAnUnknownMovie(t *testing.T) {
	newMockRadarr(t)

	if _, err := GetMovie(context.Background(), 12345); err == nil {
		t.Fatal("deleting an unknown movie id should fail before anything is sent")
	}
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

// Radarr's own rejections carry the reason; losing them behind "400" would make
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
		t.Fatalf("radarr's own message should reach the caller, got: %v", err)
	}
}
