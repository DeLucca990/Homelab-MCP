package bazarr

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNormalizeBaseURL(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"http://localhost", "http://localhost:6767"},
		{"localhost", "http://localhost:6767"},
		{"http://10.0.0.4/", "http://10.0.0.4:6767"},

		{"http://localhost:8310", "http://localhost:8310"},
		{"https://bazarr.example.com", "https://bazarr.example.com"},
		{"http://nas/bazarr", "http://nas/bazarr"},
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

var testLanguages = []Language{
	{Code2: "en", Code3: "eng", Name: "English", Enabled: true},
	{Code2: "pt", Code3: "por", Name: "Portuguese", Enabled: false},
	{Code2: "pb", Code3: "pob", Name: "Portuguese (Brazil)", Enabled: true},
	{Code2: "es", Code3: "spa", Name: "Spanish", Enabled: false},
	{Code2: "ea", Code3: "spl", Name: "Spanish (Latino)", Enabled: false},
}

// Bazarr's code for Brazilian Portuguese is its own invention. Every spelling
// a person would use for it has to land on "pb" — "pt" is a request for the
// European one, which Bazarr would fetch without complaint.
func TestResolveLanguageFindsBazarrsOwnCodes(t *testing.T) {
	cases := map[string]string{
		"pb":                  "pb",
		"pt-BR":               "pb",
		"pt_br":               "pb",
		"pob":                 "pb",
		"Portuguese (Brazil)": "pb",
		"brazil":              "pb",
		"pt":                  "pt",
		"Portuguese":          "pt",
		"en":                  "en",
		"eng":                 "en",
		"english":             "en",
		"es-419":              "ea",
	}
	for in, want := range cases {
		got, err := resolveLanguage(testLanguages, in)
		if err != nil {
			t.Errorf("resolveLanguage(%q): %v", in, err)
			continue
		}
		if got.Code2 != want {
			t.Errorf("resolveLanguage(%q) = %s, want %s", in, got.Code2, want)
		}
	}
}

func TestResolveLanguageRefusesRatherThanGuesses(t *testing.T) {
	_, err := resolveLanguage(testLanguages, "klingon")
	if err == nil || !strings.Contains(err.Error(), "pb (Portuguese (Brazil))") {
		t.Errorf("an unknown language should list the enabled ones, got: %v", err)
	}

	// "spanish" names one language exactly; "span" names two and is refused.
	if l, err := resolveLanguage(testLanguages, "spanish"); err != nil || l.Code2 != "es" {
		t.Errorf("resolveLanguage(spanish) = %v, %v", l, err)
	}
	if _, err := resolveLanguage(testLanguages, "span"); err == nil ||
		!strings.Contains(err.Error(), "more than one") {
		t.Errorf("an ambiguous name should be refused, got: %v", err)
	}
}

// --- the mock ------------------------------------------------------------------

type mockBazarr struct {
	*httptest.Server

	mu sync.Mutex

	status    map[string]any
	health    []map[string]any
	providers []map[string]any
	badges    map[string]any
	profiles  []map[string]any
	languages []Language

	movies   map[int]map[string]any
	series   map[int]map[string]any
	episodes map[int]map[string]any

	candidates []map[string]any

	// what was sent
	calls []string
	forms map[string]url.Values

	// onPatchMovieSubtitles simulates a download landing.
	onPatchMovieSubtitles func(form url.Values)
}

func newMockBazarr(t *testing.T) *mockBazarr {
	t.Helper()
	m := &mockBazarr{
		status:    map[string]any{"bazarr_version": "1.6.2", "sonarr_version": "4.0.10", "radarr_version": "5.2.6", "start_time": 1.7e9},
		badges:    map[string]any{"movies": 3, "episodes": 12, "sonarr_signalr": "LIVE", "radarr_signalr": "LIVE"},
		providers: []map[string]any{{"name": "opensubtitlescom", "status": "Good", "retry": "-"}},
		profiles: []map[string]any{{
			"profileId": 1, "name": "PT+EN", "cutoff": nil, "tag": nil,
			"items": []map[string]any{
				{"id": 1, "language": "pb", "forced": "False", "hi": "False"},
				{"id": 2, "language": "en", "forced": "False", "hi": "False"},
			},
		}},
		languages: testLanguages,
		movies:    map[int]map[string]any{},
		series:    map[int]map[string]any{},
		episodes:  map[int]map[string]any{},
		forms:     map[string]url.Values{},
	}

	mux := http.NewServeMux()
	handle := func(pattern string, fn func(w http.ResponseWriter, r *http.Request)) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-API-KEY") != "test-key" {
				http.Error(w, `{"message":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
			m.mu.Lock()
			defer m.mu.Unlock()
			if r.Method != http.MethodGet {
				body, _ := io.ReadAll(r.Body)
				form, _ := url.ParseQuery(string(body))
				key := r.Method + " " + r.URL.Path
				m.calls = append(m.calls, key)
				m.forms[key] = form
			}
			fn(w, r)
		})
	}

	handle("/api/system/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"data": m.status})
	})
	handle("/api/system/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"data": nonNilList(m.health)})
	})
	handle("/api/providers", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			for _, p := range m.providers {
				p["status"], p["retry"] = "Good", "-"
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(w, map[string]any{"data": m.providers})
	})
	handle("/api/badges", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, m.badges)
	})
	handle("/api/system/languages/profiles", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, m.profiles)
	})
	handle("/api/system/languages", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, m.languages)
	})
	handle("/api/movies", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			form := m.forms["POST /api/movies"]
			id := atoi(form.Get("radarrid"))
			if mv, ok := m.movies[id]; ok {
				if p := form.Get("profileid"); p == "none" {
					mv["profileId"] = nil
				} else {
					mv["profileId"] = atoi(p)
				}
			}
			w.WriteHeader(http.StatusNoContent)
			return
		case http.MethodPatch:
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(w, map[string]any{"data": pick(m.movies, r.URL.Query()["radarrid[]"])})
	})
	handle("/api/movies/subtitles", func(w http.ResponseWriter, r *http.Request) {
		if m.onPatchMovieSubtitles != nil {
			m.onPatchMovieSubtitles(m.forms["PATCH /api/movies/subtitles"])
		}
		w.WriteHeader(http.StatusNoContent)
	})
	handle("/api/series", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(w, map[string]any{"data": pick(m.series, r.URL.Query()["seriesid[]"])})
	})
	handle("/api/episodes", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if ids := q["episodeid[]"]; len(ids) > 0 {
			writeJSON(w, map[string]any{"data": pick(m.episodes, ids)})
			return
		}
		var out []map[string]any
		for _, e := range m.episodes {
			if fmt.Sprint(e["sonarrSeriesId"]) == q.Get("seriesid[]") {
				out = append(out, e)
			}
		}
		writeJSON(w, map[string]any{"data": nonNilList(out)})
	})
	handle("/api/episodes/subtitles", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	handle("/api/providers/movies", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(w, map[string]any{"data": nonNilList(m.candidates)})
	})
	handle("/api/subtitles", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	m.Server = httptest.NewServer(mux)
	t.Cleanup(m.Close)

	t.Setenv(BaseURLEnv, m.URL)
	t.Setenv(APIKeyEnv, "test-key")

	return m
}

func (m *mockBazarr) sent(key string) (url.Values, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.forms[key]
	return f, ok
}

func pick(all map[int]map[string]any, ids []string) []map[string]any {
	out := []map[string]any{}
	if len(ids) == 0 {
		for _, v := range all {
			out = append(out, v)
		}
		return out
	}
	for _, id := range ids {
		if v, ok := all[atoi(id)]; ok {
			out = append(out, v)
		}
	}
	return out
}

func atoi(s string) int {
	var n int
	fmt.Sscanf(s, "%d", &n)
	return n
}

func nonNilList(l []map[string]any) []map[string]any {
	if l == nil {
		return []map[string]any{}
	}
	return l
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func dune(profileID any) map[string]any {
	return map[string]any{
		"radarrId": 42, "title": "Dune", "year": "2021", "monitored": true,
		"profileId": profileID, "path": "/movies/Dune (2021)/Dune.mkv",
		"sceneName":         "Dune.2021.1080p.WEB-DL.DDP5.1.Atmos-GROUP",
		"audio_language":    []map[string]any{{"name": "English", "code2": "en", "code3": "eng"}},
		"subtitles":         []map[string]any{{"name": "English", "code2": "en", "path": "/movies/Dune (2021)/Dune.en.srt", "forced": false, "hi": false}},
		"missing_subtitles": []map[string]any{{"name": "Portuguese (Brazil)", "code2": "pb", "forced": false, "hi": false}},
	}
}

// --- health ----------------------------------------------------------------------

func TestHealthSaysWhenEveryProviderIsThrottled(t *testing.T) {
	m := newMockBazarr(t)
	m.providers = []map[string]any{
		{"name": "opensubtitlescom", "status": "TooManyRequests", "retry": "in 2 hours"},
		{"name": "podnapisi", "status": "Good", "retry": "-"},
	}

	h, err := GetHealth(context.Background())
	if err != nil {
		t.Fatalf("GetHealth: %v", err)
	}
	if h.ThrottledCount != 1 || !h.Providers[0].Throttled {
		t.Errorf("the throttled provider should be counted and listed first: %+v", h.Providers)
	}
	if !containsSubstring(h.Warnings, "1 of 2 providers is throttled") {
		t.Errorf("warnings = %v", h.Warnings)
	}

	m.providers[1]["status"] = "AuthenticationError"
	h, _ = GetHealth(context.Background())
	if !containsSubstring(h.Warnings, "every provider is throttled") {
		t.Errorf("all throttled should be its own warning: %v", h.Warnings)
	}
}

func TestHealthTellsNotUsedFromUnreachable(t *testing.T) {
	m := newMockBazarr(t)
	m.status["sonarr_version"] = "unknown"
	m.status["radarr_version"] = ""
	m.badges["radarr_signalr"] = "DOWN"

	h, err := GetHealth(context.Background())
	if err != nil {
		t.Fatalf("GetHealth: %v", err)
	}
	if !containsSubstring(h.Warnings, "cannot reach Sonarr") {
		t.Errorf("'unknown' means configured and unreachable: %v", h.Warnings)
	}
	// Radarr is not used, so its live feed being down is not a finding.
	if containsSubstring(h.Warnings, "Radarr") {
		t.Errorf("an unused Radarr produced a warning: %v", h.Warnings)
	}
}

func TestHealthWithNoProviderSaysNothingCanArrive(t *testing.T) {
	m := newMockBazarr(t)
	m.providers = nil

	h, _ := GetHealth(context.Background())
	if !containsSubstring(h.Warnings, "no subtitle provider is enabled") {
		t.Errorf("warnings = %v", h.Warnings)
	}
}

// --- status ------------------------------------------------------------------------

func TestStatusOfAMovieWithNoProfileSaysWhy(t *testing.T) {
	m := newMockBazarr(t)
	m.movies[42] = dune(nil)

	st, err := GetStatus(context.Background(), Target{RadarrID: 42}, "", 0)
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if st.Movie == nil || st.Movie.Title != "Dune" {
		t.Fatalf("movie = %+v", st.Movie)
	}
	if len(st.Movie.Missing) != 1 || st.Movie.Missing[0].Code2 != "pb" {
		t.Errorf("missing = %+v", st.Movie.Missing)
	}
	if !containsSubstring(st.Warnings, "no language profile") {
		t.Errorf("a movie with no profile wants nothing, and that must be said: %v", st.Warnings)
	}
}

func TestStatusNamesTheProfile(t *testing.T) {
	m := newMockBazarr(t)
	m.movies[42] = dune(1)

	st, err := GetStatus(context.Background(), Target{RadarrID: 42}, "", 0)
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if st.Movie.ProfileName != "PT+EN" {
		t.Errorf("profile = %q, want PT+EN", st.Movie.ProfileName)
	}
	if len(st.Warnings) != 0 {
		t.Errorf("unexpected warnings: %v", st.Warnings)
	}
}

func TestStatusByTermSearchesBothLibraries(t *testing.T) {
	m := newMockBazarr(t)
	m.movies[42] = dune(1)
	m.series[7] = map[string]any{"sonarrSeriesId": 7, "title": "Dune: Prophecy", "profileId": 1,
		"episodeFileCount": 6, "episodeMissingCount": 2}

	st, err := GetStatus(context.Background(), Target{}, "dune", 0)
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if len(st.MatchedMovies) != 1 || len(st.MatchedSeries) != 1 {
		t.Errorf("matched %d movies and %d series, want 1 and 1", len(st.MatchedMovies), len(st.MatchedSeries))
	}
}

func TestUnknownMovieSaysWhatTheNumberProbablyWas(t *testing.T) {
	newMockBazarr(t)
	_, err := GetMovie(context.Background(), 438631)
	if err == nil || !strings.Contains(err.Error(), "TMDB") {
		t.Errorf("an unknown id should mention the TMDB mix-up, got: %v", err)
	}
}

// --- search ------------------------------------------------------------------------

func TestSearchOneLanguageSendsBazarrsCodeAndReportsWhatArrived(t *testing.T) {
	m := newMockBazarr(t)
	m.movies[42] = dune(1)
	m.onPatchMovieSubtitles = func(form url.Values) {
		subs := m.movies[42]["subtitles"].([]map[string]any)
		m.movies[42]["subtitles"] = append(subs, map[string]any{
			"name": "Portuguese (Brazil)", "code2": form.Get("language"),
			"path": "/movies/Dune (2021)/Dune.pb.srt", "forced": false, "hi": false,
		})
		m.movies[42]["missing_subtitles"] = []map[string]any{}
	}

	plan, err := PlanSearch(context.Background(), SearchRequest{
		Target: Target{RadarrID: 42}, Language: "pt-BR",
	})
	if err != nil {
		t.Fatalf("PlanSearch: %v", err)
	}
	if plan.OutsideProfile || len(plan.Replaces) != 0 {
		t.Errorf("pb is in the profile and not on disk: %+v", plan)
	}

	res, err := Search(context.Background(), plan)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	form, ok := m.sent("PATCH /api/movies/subtitles")
	if !ok {
		t.Fatal("no PATCH /api/movies/subtitles was sent")
	}
	if form.Get("language") != "pb" || form.Get("radarrid") != "42" ||
		form.Get("forced") != "False" || form.Get("hi") != "False" {
		t.Errorf("form = %v", form)
	}
	if len(res.Found) != 1 || res.Found[0].Code2 != "pb" || len(res.Pending) != 0 {
		t.Errorf("found = %v, pending = %v", res.Found, res.Pending)
	}
}

func TestSearchThatFindsNothingYetSaysSo(t *testing.T) {
	m := newMockBazarr(t)
	m.movies[42] = dune(1)

	plan, _ := PlanSearch(context.Background(), SearchRequest{Target: Target{RadarrID: 42}})
	res, err := Search(context.Background(), plan)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if _, ok := m.sent("PATCH /api/movies"); !ok {
		t.Error("everything-missing should use the search-missing action")
	}
	if len(res.Found) != 0 || len(res.Pending) != 1 {
		t.Errorf("found = %v, pending = %v", res.Found, res.Pending)
	}
	if !containsSubstring(res.Warnings, "background job") {
		t.Errorf("a pending search must not read as a failure or a success: %v", res.Warnings)
	}
}

func TestSearchOutsideTheProfileAndOverAnExistingFileWarns(t *testing.T) {
	m := newMockBazarr(t)
	m.movies[42] = dune(1)

	plan, err := PlanSearch(context.Background(), SearchRequest{
		Target: Target{RadarrID: 42}, Language: "en",
	})
	if err != nil {
		t.Fatalf("PlanSearch: %v", err)
	}
	if len(plan.Replaces) != 1 {
		t.Errorf("an existing English file should be named as replaced: %+v", plan.Replaces)
	}

	plan, err = PlanSearch(context.Background(), SearchRequest{
		Target: Target{RadarrID: 42}, Language: "Portuguese",
	})
	if err != nil {
		t.Fatalf("PlanSearch: %v", err)
	}
	if !plan.OutsideProfile || !containsSubstring(plan.Warnings, "not enabled") {
		t.Errorf("European Portuguese is outside the profile and disabled: %+v", plan)
	}
}

func TestSearchRefusesWhatItCannotDo(t *testing.T) {
	m := newMockBazarr(t)
	m.movies[42] = dune(1)
	m.movies[42]["missing_subtitles"] = []map[string]any{}
	m.series[7] = map[string]any{"sonarrSeriesId": 7, "title": "Show", "profileId": 1,
		"episodeFileCount": 6, "episodeMissingCount": 2}

	cases := map[string]SearchRequest{
		"no target":              {},
		"two targets":            {Target: Target{RadarrID: 42, SeriesID: 7}},
		"language on a series":   {Target: Target{SeriesID: 7}, Language: "en"},
		"nothing missing":        {Target: Target{RadarrID: 42}},
		"forced without a lang":  {Target: Target{RadarrID: 42}, Forced: true},
		"forced and hi together": {Target: Target{RadarrID: 42}, Language: "en", Forced: true, HI: true},
	}
	for name, req := range cases {
		if _, err := PlanSearch(context.Background(), req); err == nil {
			t.Errorf("%s: should have been refused", name)
		}
	}
}

// --- manual search -----------------------------------------------------------------

func TestCandidatesKeepBazarrsTokenAndDownloadSendsItBack(t *testing.T) {
	m := newMockBazarr(t)
	m.movies[42] = dune(1)
	token := strings.Repeat("gASVbase64pickle", 200) // what old Bazarr sends: kilobytes
	m.candidates = []map[string]any{
		{"provider": "opensubtitlescom", "language": "pt-BR", "forced": "False",
			"hearing_impaired": "False", "original_format": "False", "score": 93,
			"subtitle": token, "matches": []string{"release_group", "source"},
			"release_info": []string{"Dune.2021.1080p.WEB-DL-GROUP"}},
		{"provider": "podnapisi", "language": "en", "forced": "False",
			"hearing_impaired": "True", "original_format": "False", "score": 71,
			"subtitle": "other-token"},
	}

	list, err := GetCandidates(context.Background(), Target{RadarrID: 42}, "pb", 0)
	if err != nil {
		t.Fatalf("GetCandidates: %v", err)
	}
	if list.TotalCount != 2 || list.ShownCount != 1 {
		t.Fatalf("the language filter should keep only the pt-BR one: %+v", list)
	}
	cand := list.Candidates[0]
	if cand.Code2 != "pb" {
		t.Errorf("a provider's pt-BR should be Bazarr's pb, got %q", cand.Code2)
	}
	if len(cand.ID) != 8 || strings.Contains(cand.ID, "gASV") {
		t.Errorf("the id handed to the model should be short, got %q", cand.ID)
	}

	plan, err := PlanDownload(cand.ID)
	if err != nil {
		t.Fatalf("PlanDownload: %v", err)
	}
	if _, err := Download(context.Background(), plan); err != nil {
		t.Fatalf("Download: %v", err)
	}
	form, ok := m.sent("POST /api/providers/movies")
	if !ok {
		t.Fatal("nothing was posted to /api/providers/movies")
	}
	if form.Get("subtitle") != token || form.Get("provider") != "opensubtitlescom" ||
		form.Get("radarrid") != "42" || form.Get("hi") != "False" {
		t.Errorf("the download must hand back exactly what Bazarr listed: %v", form)
	}
}

func TestCandidatesNeedAProfile(t *testing.T) {
	m := newMockBazarr(t)
	m.movies[42] = dune(nil)

	_, err := GetCandidates(context.Background(), Target{RadarrID: 42}, "", 0)
	if err == nil || !strings.Contains(err.Error(), "no language profile") {
		t.Errorf("a manual search with no profile finds nothing by construction: %v", err)
	}
}

func TestUnknownCandidateSaysToSearchAgain(t *testing.T) {
	_, err := PlanDownload("deadbeef")
	if err == nil || !strings.Contains(err.Error(), "bazarr_subtitle_candidates") {
		t.Errorf("got: %v", err)
	}
}

// --- sync ----------------------------------------------------------------------------

func TestShiftActionIsBazarrsModSyntax(t *testing.T) {
	cases := map[int]string{
		2500:    "shift_offset(h=0,m=0,s=2,ms=500)",
		-2500:   "shift_offset(h=0,m=0,s=-2,ms=-500)",
		61_001:  "shift_offset(h=0,m=1,s=1,ms=1)",
		-90_000: "shift_offset(h=0,m=-1,s=-30,ms=0)",
	}
	for ms, want := range cases {
		if got := shiftAction(msDuration(ms)); got != want {
			t.Errorf("shiftAction(%dms) = %s, want %s", ms, got, want)
		}
	}
}

func TestSyncRewritesTheExternalFile(t *testing.T) {
	m := newMockBazarr(t)
	m.movies[42] = dune(1)

	plan, err := PlanSync(context.Background(), SyncRequest{
		Target: Target{RadarrID: 42}, Language: "english", ShiftMs: -1500,
	})
	if err != nil {
		t.Fatalf("PlanSync: %v", err)
	}
	if _, err := Sync(context.Background(), plan); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	form, _ := m.sent("PATCH /api/subtitles")
	if form.Get("action") != "shift_offset(h=0,m=0,s=-1,ms=-500)" ||
		form.Get("path") != "/movies/Dune (2021)/Dune.en.srt" ||
		form.Get("type") != "movie" || form.Get("id") != "42" {
		t.Errorf("form = %v", form)
	}
}

func TestSyncRefusesAnEmbeddedTrack(t *testing.T) {
	m := newMockBazarr(t)
	m.movies[42] = dune(1)
	m.movies[42]["subtitles"] = []map[string]any{{"name": "English", "code2": "en", "path": nil}}

	_, err := PlanSync(context.Background(), SyncRequest{Target: Target{RadarrID: 42}, Language: "en"})
	if err == nil || !strings.Contains(err.Error(), "inside the video file") {
		t.Errorf("got: %v", err)
	}
}

// --- profiles --------------------------------------------------------------------------

func TestSetMovieProfileSendsTheFormAndRecomputes(t *testing.T) {
	m := newMockBazarr(t)
	m.movies[42] = dune(nil)

	movie, _ := GetMovie(context.Background(), 42)
	p, none, err := ResolveProfile(context.Background(), "pt+en")
	if err != nil || none {
		t.Fatalf("ResolveProfile: %v, none=%v", err, none)
	}

	res, err := SetMovieProfile(context.Background(), movie, p, none)
	if err != nil {
		t.Fatalf("SetMovieProfile: %v", err)
	}
	form, _ := m.sent("POST /api/movies")
	if form.Get("radarrid") != "42" || form.Get("profileid") != "1" {
		t.Errorf("form = %v", form)
	}
	if res.From != "none" || res.To != "PT+EN" {
		t.Errorf("change = %+v", res)
	}
	if !containsSubstring(res.Warnings, "does not search by itself") {
		t.Errorf("setting a profile starts no search, and must say so: %v", res.Warnings)
	}
}

func TestResolveProfileListsWhatExists(t *testing.T) {
	newMockBazarr(t)

	if _, none, err := ResolveProfile(context.Background(), "none"); err != nil || !none {
		t.Errorf("'none' is a valid profile: %v, %v", none, err)
	}
	if p, _, err := ResolveProfile(context.Background(), "1"); err != nil || p.Name != "PT+EN" {
		t.Errorf("by id: %+v, %v", p, err)
	}
	_, _, err := ResolveProfile(context.Background(), "Klingon")
	if err == nil || !strings.Contains(err.Error(), "PT+EN") {
		t.Errorf("an unknown profile should name the ones that exist: %v", err)
	}
}

// --- providers ---------------------------------------------------------------------------

func TestResetSaysWhichThrottlesWillComeBack(t *testing.T) {
	m := newMockBazarr(t)
	m.providers = []map[string]any{
		{"name": "opensubtitlescom", "status": "AuthenticationError", "retry": "in 12 hours"},
		{"name": "podnapisi", "status": "Good", "retry": "-"},
	}

	before, _ := GetProviders(context.Background())
	res, err := ResetProviders(context.Background(), before)
	if err != nil {
		t.Fatalf("ResetProviders: %v", err)
	}
	if res.ClearedCount != 1 {
		t.Errorf("cleared = %d, want 1", res.ClearedCount)
	}
	if !containsSubstring(res.Warnings, "credential") {
		t.Errorf("an auth throttle comes straight back and must say so: %v", res.Warnings)
	}
	if form, _ := m.sent("POST /api/providers"); form.Get("action") != "reset" {
		t.Errorf("form = %v", form)
	}
}

// --- errors --------------------------------------------------------------------------------

func TestBadAPIKeyIsNamed(t *testing.T) {
	newMockBazarr(t)
	t.Setenv(APIKeyEnv, "wrong")

	_, err := GetHealth(context.Background())
	if err == nil || !strings.Contains(err.Error(), APIKeyEnv) {
		t.Fatalf("a 401 should point at the API key variable, got: %v", err)
	}
}

// Bazarr's refusals are a sentence sent as a JSON string, and that sentence —
// a path mapping problem, usually — is the whole answer.
func TestBazarrsOwnMessageSurvives(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`"Movie file not found. Path mapping issue?"` + "\n"))
	}))
	defer srv.Close()

	t.Setenv(BaseURLEnv, srv.URL)
	t.Setenv(APIKeyEnv, "k")

	_, err := GetMovie(context.Background(), 42)
	if err == nil || !strings.Contains(err.Error(), "Path mapping issue?") {
		t.Fatalf("bazarr's own message should reach the caller, got: %v", err)
	}
}

// --- helpers ---------------------------------------------------------------------------------

func msDuration(ms int) time.Duration { return time.Duration(ms) * time.Millisecond }

func containsSubstring(list []string, sub string) bool {
	for _, s := range list {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
