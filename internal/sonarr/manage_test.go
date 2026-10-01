package sonarr

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type manageMock struct {
	*httptest.Server
	mu     sync.Mutex
	routes map[string]any
	writes map[string][]any
	query  map[string]string
}

func newManageMock(t *testing.T) *manageMock {
	t.Helper()
	m := &manageMock{routes: map[string]any{}, writes: map[string][]any{}, query: map[string]string{}}
	series := map[string]any{"id": 7, "title": "Severance", "year": 2022, "tvdbId": 371980, "monitored": true,
		"qualityProfileId": 1, "seriesType": "standard", "tags": []int{},
		"statistics": map[string]any{"episodeFileCount": 12, "episodeCount": 19, "totalEpisodeCount": 19}}
	m.routes["GET /api/v3/series/7"] = series
	m.routes["GET /api/v3/series"] = []map[string]any{series}
	m.routes["GET /api/v3/qualityprofile"] = []map[string]any{{"id": 1, "name": "HD-1080p"}, {"id": 2, "name": "Ultra-HD"}}
	m.routes["GET /api/v3/tag"] = []map[string]any{{"id": 1, "label": "anime"}}
	m.routes["GET /api/v3/episode/101"] = map[string]any{"id": 101, "seriesId": 7, "seasonNumber": 2, "episodeNumber": 1, "monitored": false}
	m.routes["GET /api/v3/episode/102"] = map[string]any{"id": 102, "seriesId": 7, "seasonNumber": 2, "episodeNumber": 2, "monitored": true}
	m.routes["GET /api/v3/episode/900"] = map[string]any{"id": 900, "seriesId": 8, "seasonNumber": 1, "episodeNumber": 1}

	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		key := r.Method + " " + r.URL.Path
		m.query[key] = r.URL.RawQuery
		if r.Method != http.MethodGet {
			raw, _ := io.ReadAll(r.Body)
			var body any
			json.Unmarshal(raw, &body)
			m.writes[key] = append(m.writes[key], body)
		}
		if v, ok := m.routes[key]; ok {
			json.NewEncoder(w).Encode(v)
			return
		}
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"message":"NotFound"}`))
			return
		}
		w.Write([]byte(`{"id":601,"status":"queued"}`))
	}))
	t.Cleanup(m.Close)
	t.Setenv(BaseURLEnv, m.URL)
	t.Setenv(APIKeyEnv, "k")
	return m
}

func (m *manageMock) sent(key string) []any {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.writes[key]
}

func TestSeasonReleasesAndGrab(t *testing.T) {
	m := newManageMock(t)
	m.routes["GET /api/v3/release"] = []map[string]any{
		{"guid": "pack", "title": "Severance.S02.1080p.WEB-DL", "indexerId": 2, "indexer": "Nyaa",
			"protocol": "torrent", "fullSeason": true, "seasonNumber": 2, "approved": true, "seeders": 12},
		{"guid": "e1", "title": "Severance.S02E01.720p", "indexerId": 2, "indexer": "Nyaa",
			"seasonNumber": 2, "episodeNumbers": []int{1}, "approved": false,
			"rejections": []string{"720p is not wanted in profile"}},
	}

	out, err := GetReleases(context.Background(), 7, 2, 0, 0)
	if err != nil {
		t.Fatalf("GetReleases: %v", err)
	}
	if !strings.Contains(m.query["GET /api/v3/release"], "seasonNumber=2") {
		t.Errorf("a season search asks for the season: %s", m.query["GET /api/v3/release"])
	}
	if out.Releases[0].Episodes != "S02 (full season)" || out.Releases[1].Episodes != "S02E01" {
		t.Errorf("what each release covers: %+v", out.Releases)
	}

	plan, err := PlanGrab(out.Releases[0].ID)
	if err != nil {
		t.Fatalf("PlanGrab: %v", err)
	}
	if !containsSubstring(plan.Warnings, "season pack") {
		t.Errorf("a pack should say it covers the season: %v", plan.Warnings)
	}
	if _, err := Grab(context.Background(), plan); err != nil {
		t.Fatalf("Grab: %v", err)
	}
	body := m.sent("POST /api/v3/release")[0].(map[string]any)
	if body["guid"] != "pack" || body["seriesId"] != 7.0 || body["episodeId"] != nil {
		t.Errorf("grab body = %v", body)
	}
}

func TestImportNeedsAnEpisodeMatch(t *testing.T) {
	m := newManageMock(t)
	m.routes["GET /api/v3/manualimport"] = []map[string]any{
		{"path": "/dl/Severance.S02E01.mkv", "size": 2e9, "series": map[string]any{"id": 7, "title": "Severance"},
			"episodes": []map[string]any{{"id": 101, "seasonNumber": 2, "episodeNumber": 1}},
			"quality":  map[string]any{"quality": map[string]any{"name": "WEBDL-1080p"}}, "releaseType": "singleEpisode"},
		{"path": "/dl/extras.mkv", "size": 1e8, "series": map[string]any{"id": 7, "title": "Severance"}},
	}

	out, err := GetImportCandidates(context.Background(), 0, "/dl")
	if err != nil {
		t.Fatalf("GetImportCandidates: %v", err)
	}
	if out.Candidates[0].Episodes != "S02E01" || !containsSubstring(out.Warnings, "could not be matched") {
		t.Errorf("candidates = %+v, warnings = %v", out.Candidates, out.Warnings)
	}
	if _, err := PlanImport([]string{out.Candidates[1].ID}); err == nil {
		t.Error("a file with no episode cannot be imported from here")
	}
	plan, _ := PlanImport([]string{out.Candidates[0].ID})
	if _, err := Import(context.Background(), plan); err != nil {
		t.Fatalf("Import: %v", err)
	}
	f := m.sent("POST /api/v3/command")[0].(map[string]any)["files"].([]any)[0].(map[string]any)
	ids := f["episodeIds"].([]any)
	if f["seriesId"] != 7.0 || len(ids) != 1 || ids[0] != 101.0 || f["releaseType"] != "singleEpisode" {
		t.Errorf("file = %v", f)
	}
}

func TestSeriesEditAndEpisodeMonitor(t *testing.T) {
	m := newManageMock(t)

	plan, err := PlanEdit(context.Background(), EditRequest{SeriesID: 7, SeriesType: "anime", AddTags: []string{"Anime"}})
	if err != nil {
		t.Fatalf("PlanEdit: %v", err)
	}
	if !containsSubstring(plan.Warnings, "absolute numbers") {
		t.Errorf("changing the series type should say what it changes: %v", plan.Warnings)
	}
	if _, err := Edit(context.Background(), plan); err != nil {
		t.Fatalf("Edit: %v", err)
	}
	bodies := m.sent("PUT /api/v3/series/editor")
	if len(bodies) != 2 || bodies[0].(map[string]any)["seriesType"] != "anime" ||
		bodies[1].(map[string]any)["applyTags"] != "add" {
		t.Errorf("editor bodies = %v", bodies)
	}

	mp, err := PlanEpisodeMonitor(context.Background(), []int{101, 102}, true)
	if err != nil {
		t.Fatalf("PlanEpisodeMonitor: %v", err)
	}
	if len(mp.Episodes) != 1 || mp.Unchanged != 1 {
		t.Errorf("102 is already monitored: %+v", mp)
	}
	if _, err := SetEpisodesMonitored(context.Background(), mp); err != nil {
		t.Fatalf("SetEpisodesMonitored: %v", err)
	}
	body := m.sent("PUT /api/v3/episode/monitor")[0].(map[string]any)
	if body["monitored"] != true || len(body["episodeIds"].([]any)) != 1 {
		t.Errorf("monitor body = %v", body)
	}
	if _, err := PlanEpisodeMonitor(context.Background(), []int{101, 900}, true); err == nil {
		t.Error("episodes of two series should be refused")
	}
}

func TestSeriesHistoryFiltersTheBlocklist(t *testing.T) {
	m := newManageMock(t)
	m.routes["GET /api/v3/history/series"] = []map[string]any{
		{"eventType": "grabbed", "sourceTitle": "a", "date": "2026-01-01T00:00:00Z",
			"episode": map[string]any{"seasonNumber": 2, "episodeNumber": 1}},
		{"eventType": "downloadFolderImported", "sourceTitle": "b", "date": "2026-02-01T00:00:00Z"},
	}
	m.routes["GET /api/v3/blocklist"] = map[string]any{"records": []map[string]any{
		{"seriesId": 7, "sourceTitle": "fake.S02"}, {"seriesId": 8, "sourceTitle": "other show"}}}

	h, err := GetHistory(context.Background(), 7, nil, 0)
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if h.Events[0].Event != "imported" || h.Events[1].Episode != "S02E01" {
		t.Errorf("newest first: %+v", h.Events)
	}
	if len(h.Blocklist) != 1 || h.Blocklist[0].Release != "fake.S02" {
		t.Errorf("another series' blocklist leaked in: %+v", h.Blocklist)
	}
}
