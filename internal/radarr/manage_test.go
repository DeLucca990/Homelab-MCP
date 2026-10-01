package radarr

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

// manageMock is a Radarr for the release, import, edit and history tools. It
// records every write.
type manageMock struct {
	*httptest.Server
	mu     sync.Mutex
	routes map[string]any
	status map[string]int
	writes map[string]any
	query  map[string]string
}

func newManageMock(t *testing.T) *manageMock {
	t.Helper()
	m := &manageMock{routes: map[string]any{}, status: map[string]int{}, writes: map[string]any{}, query: map[string]string{}}
	m.routes["GET /api/v3/movie/42"] = map[string]any{"id": 42, "title": "Dune", "year": 2021, "tmdbId": 438631,
		"monitored": true, "hasFile": false, "isAvailable": true, "qualityProfileId": 1,
		"minimumAvailability": "released", "tags": []int{1}}
	m.routes["GET /api/v3/qualityprofile"] = []map[string]any{{"id": 1, "name": "HD-1080p"}, {"id": 2, "name": "Ultra-HD"}}
	m.routes["GET /api/v3/tag"] = []map[string]any{{"id": 1, "label": "kids"}, {"id": 2, "label": "4k"}}

	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		key := r.Method + " " + r.URL.Path
		m.query[key] = r.URL.RawQuery
		if r.Method != http.MethodGet {
			raw, _ := io.ReadAll(r.Body)
			var body any
			json.Unmarshal(raw, &body)
			if prev, ok := m.writes[key]; ok {
				m.writes[key] = append(asList(prev), body)
			} else {
				m.writes[key] = body
			}
		}
		if code, ok := m.status[key]; ok {
			w.WriteHeader(code)
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
		w.Write([]byte(`{"id":501,"status":"queued"}`))
	}))
	t.Cleanup(m.Close)
	t.Setenv(BaseURLEnv, m.URL)
	t.Setenv(APIKeyEnv, "k")
	return m
}

func asList(v any) []any {
	if l, ok := v.([]any); ok {
		if len(l) > 0 {
			if _, isMap := l[0].(map[string]any); isMap {
				return l
			}
		}
	}
	return []any{v}
}

func (m *manageMock) sent(key string) any {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.writes[key]
}

// --- interactive search ------------------------------------------------------------

func TestReleasesKeepRadarrsOrderAndGrabByShortID(t *testing.T) {
	m := newManageMock(t)
	longGUID := "https://indexer.example/details/123456?apikey=" + strings.Repeat("x", 64)
	m.routes["GET /api/v3/release"] = []map[string]any{
		{"guid": longGUID, "title": "Dune.2021.1080p.WEB-DL-GRP", "indexerId": 3, "indexer": "Nyaa",
			"protocol": "torrent", "size": 5e9, "seeders": 40, "approved": true,
			"quality": map[string]any{"quality": map[string]any{"name": "WEBDL-1080p"}}},
		{"guid": "g2", "title": "Dune.2021.CAM", "indexerId": 4, "indexer": "1337x", "protocol": "torrent",
			"approved": false, "rejections": []string{"CAM is not wanted in profile"}},
		{"guid": "g3", "title": "Dune.2021.720p.tiny", "indexerId": 4, "indexer": "1337x", "protocol": "torrent",
			"approved": false, "rejections": []string{"Size 300 MB is smaller than minimum allowed 1.5 GB"}},
	}

	movie, _ := GetMovie(context.Background(), 42)
	out, err := GetReleases(context.Background(), movie, 0)
	if err != nil {
		t.Fatalf("GetReleases: %v", err)
	}
	if out.TotalCount != 3 || out.ApprovedCount != 1 || out.Releases[0].Rank != 1 || !out.Releases[0].Approved {
		t.Errorf("Radarr's own order must be kept: %+v", out.Releases)
	}
	if out.TopRejections["Size 300 MB"] != 1 || out.TopRejections["CAM"] != 1 {
		t.Errorf("rejections should be summarised by reason: %v", out.TopRejections)
	}
	if len(out.Releases[0].ID) != 8 {
		t.Errorf("the id handed out should be short, got %q", out.Releases[0].ID)
	}

	plan, err := PlanGrab(out.Releases[1].ID)
	if err != nil {
		t.Fatalf("PlanGrab: %v", err)
	}
	if !containsSubstring(plan.Warnings, "overrides") {
		t.Errorf("grabbing a rejected release should say it overrides: %v", plan.Warnings)
	}
	plan, _ = PlanGrab(out.Releases[0].ID)
	if _, err := Grab(context.Background(), plan); err != nil {
		t.Fatalf("Grab: %v", err)
	}
	body := m.sent("POST /api/v3/release").(map[string]any)
	if body["guid"] != longGUID || body["indexerId"] != 3.0 || body["movieId"] != 42.0 {
		t.Errorf("the grab must hand back Radarr's own guid and indexer: %v", body)
	}
}

func TestReleasesAllRejectedIsTheDiagnosis(t *testing.T) {
	m := newManageMock(t)
	m.routes["GET /api/v3/release"] = []map[string]any{
		{"guid": "g", "title": "x", "indexerId": 1, "approved": false, "rejections": []string{"Not an upgrade"}},
	}
	movie, _ := GetMovie(context.Background(), 42)
	out, _ := GetReleases(context.Background(), movie, 0)
	if !containsSubstring(out.Warnings, "rejected every one") {
		t.Errorf("warnings = %v", out.Warnings)
	}
	if _, err := PlanGrab("nope1234"); err == nil {
		t.Error("an unknown release id should be refused")
	}
}

// --- manual import -------------------------------------------------------------

func TestImportUsesTheQueueDownloadAndSendsRadarrsOwnParse(t *testing.T) {
	m := newManageMock(t)
	m.routes["GET /api/v3/queue"] = map[string]any{"totalRecords": 1, "records": []map[string]any{
		{"id": 9, "movieId": 42, "title": "Dune.2021.1080p", "downloadId": "ABC123", "status": "completed",
			"trackedDownloadState": "importBlocked"}}}
	quality := map[string]any{"quality": map[string]any{"id": 3, "name": "WEBDL-1080p"}, "revision": map[string]any{"version": 1}}
	m.routes["GET /api/v3/manualimport"] = []map[string]any{
		{"path": "/downloads/Dune/Dune.mkv", "size": 5e9, "downloadId": "ABC123", "quality": quality,
			"languages": []map[string]any{{"id": 1, "name": "English"}}, "releaseGroup": "GRP",
			"rejections": []map[string]any{{"reason": "Unable to determine if file is a sample"}}},
		{"path": "/downloads/Dune/sample.mkv", "size": 1e6},
	}

	out, err := GetImportCandidates(context.Background(), 9, "")
	if err != nil {
		t.Fatalf("GetImportCandidates: %v", err)
	}
	if !strings.Contains(m.query["GET /api/v3/manualimport"], "downloadId=ABC123") {
		t.Errorf("the queue item's download id should be used: %s", m.query["GET /api/v3/manualimport"])
	}
	if len(out.Candidates) != 2 || !containsSubstring(out.Warnings, "could not be matched") {
		t.Fatalf("candidates = %+v, warnings = %v", out.Candidates, out.Warnings)
	}

	if _, err := PlanImport(context.Background(), []string{out.Candidates[1].ID}, 0); err == nil {
		t.Error("an unmatched file without movie_id should be refused")
	}

	plan, err := PlanImport(context.Background(), []string{out.Candidates[0].ID}, 42)
	if err != nil {
		t.Fatalf("PlanImport: %v", err)
	}
	if !containsSubstring(plan.Warnings, "sample") {
		t.Errorf("the objection being overridden should be named: %v", plan.Warnings)
	}
	if _, err := Import(context.Background(), plan); err != nil {
		t.Fatalf("Import: %v", err)
	}
	cmd := m.sent("POST /api/v3/command").(map[string]any)
	files := cmd["files"].([]any)
	f := files[0].(map[string]any)
	if cmd["name"] != "ManualImport" || f["movieId"] != 42.0 || f["downloadId"] != "ABC123" ||
		f["quality"].(map[string]any)["revision"] == nil {
		t.Errorf("the command should carry Radarr's own parse: %v", cmd)
	}
}

// --- edit ------------------------------------------------------------------------

func TestEditSendsOnlyWhatChangedThroughTheEditor(t *testing.T) {
	m := newManageMock(t)

	off := false
	plan, err := PlanEdit(context.Background(), EditRequest{MovieID: 42, Monitored: &off,
		QualityProfile: "ultra-hd", AddTags: []string{"4K", "kids"}, RemoveTags: []string{"kids"}})
	if err != nil {
		t.Fatalf("PlanEdit: %v", err)
	}
	if len(plan.Changes) != 4 {
		t.Errorf("monitored, profile, add 4k, remove kids — 'kids' is already there: %+v", plan.Changes)
	}
	if _, err := Edit(context.Background(), plan); err != nil {
		t.Fatalf("Edit: %v", err)
	}
	bodies := asList(m.sent("PUT /api/v3/movie/editor"))
	if len(bodies) != 3 {
		t.Fatalf("expected the field edit and two tag edits, got %v", bodies)
	}
	first := bodies[0].(map[string]any)
	if first["monitored"] != false || first["qualityProfileId"] != 2.0 || first["minimumAvailability"] != nil {
		t.Errorf("only the named fields go to the editor: %v", first)
	}
	if bodies[2].(map[string]any)["applyTags"] != "remove" {
		t.Errorf("tag removal = %v", bodies[2])
	}

	on := true
	for name, req := range map[string]EditRequest{
		"nothing":     {MovieID: 42},
		"no change":   {MovieID: 42, Monitored: &on},
		"bad tag":     {MovieID: 42, AddTags: []string{"anime"}},
		"bad avail":   {MovieID: 42, MinimumAvailability: "someday"},
		"bad profile": {MovieID: 42, QualityProfile: "potato"},
	} {
		if _, err := PlanEdit(context.Background(), req); err == nil {
			t.Errorf("%s: should have been refused", name)
		}
	}
}

// --- history and download clients ------------------------------------------------

func TestHistoryIsNewestFirstWithTheBlocklist(t *testing.T) {
	m := newManageMock(t)
	m.routes["GET /api/v3/history/movie"] = []map[string]any{
		{"eventType": "grabbed", "sourceTitle": "old", "date": "2026-01-01T00:00:00Z", "data": map[string]string{"indexer": "Nyaa"}},
		{"eventType": "downloadFailed", "sourceTitle": "new", "date": "2026-09-01T00:00:00Z",
			"data": map[string]string{"message": "Password protected archive"}},
	}
	m.routes["GET /api/v3/blocklist/movie"] = []map[string]any{{"sourceTitle": "new", "message": "Password protected archive"}}

	h, err := GetHistory(context.Background(), 42, 0)
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if h.Events[0].Event != "failed" || h.Events[0].Detail != "Password protected archive" {
		t.Errorf("newest first, with the reason: %+v", h.Events)
	}
	if len(h.Blocklist) != 1 || !containsSubstring(h.Warnings, "blocklisted") {
		t.Errorf("blocklist = %+v, warnings = %v", h.Blocklist, h.Warnings)
	}
}

func TestDownloadClientTestReadsTheBodyOfA400(t *testing.T) {
	m := newManageMock(t)
	m.routes["GET /api/v3/downloadclient"] = []map[string]any{
		{"id": 1, "name": "qBittorrent", "enable": true}, {"id": 2, "name": "old", "enable": false}}
	m.status["POST /api/v3/downloadclient/testall"] = http.StatusBadRequest
	m.routes["POST /api/v3/downloadclient/testall"] = []map[string]any{
		{"id": 1, "isValid": false, "validationFailures": []map[string]any{{"errorMessage": "Unable to connect to qBittorrent"}}}}

	tests, err := TestDownloadClients(context.Background())
	if err != nil {
		t.Fatalf("TestDownloadClients: %v", err)
	}
	if len(tests) != 1 || tests[0].Passed || !strings.Contains(tests[0].Errors[0], "Unable to connect") {
		t.Errorf("tests = %+v", tests)
	}
}
