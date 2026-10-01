package prowlarr

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNormalizeBaseURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"http://localhost", "http://localhost:9696"},
		{"localhost", "http://localhost:9696"},
		{"http://10.0.0.4/", "http://10.0.0.4:9696"},
		{"http://localhost:8310", "http://localhost:8310"},
		{"https://prowlarr.example.com", "https://prowlarr.example.com"},
		{"http://nas/prowlarr", "http://nas/prowlarr"},
	}
	for _, c := range cases {
		got, err := normalizeBaseURL(c.in)
		if err != nil || got != c.want {
			t.Errorf("normalizeBaseURL(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	for _, bad := range []string{"", "ftp://nas"} {
		if _, err := normalizeBaseURL(bad); err == nil {
			t.Errorf("normalizeBaseURL(%q) should have failed", bad)
		}
	}
}

// --- the mock --------------------------------------------------------------------

type mockProwlarr struct {
	*httptest.Server

	mu sync.Mutex

	indexers []map[string]any
	stats    []map[string]any
	apps     []map[string]any
	profiles []map[string]any
	tags     []map[string]any
	health   []map[string]any
	schema   []map[string]any
	releases []map[string]any

	testAllStatus int
	testAll       []map[string]any
	testOne       func(body []byte) (int, string)

	searchQuery string
	bodies      map[string][]byte
}

func newMockProwlarr(t *testing.T) *mockProwlarr {
	t.Helper()
	m := &mockProwlarr{
		profiles: []map[string]any{{"id": 1, "name": "Standard", "enableRss": true,
			"enableAutomaticSearch": true, "enableInteractiveSearch": true, "minimumSeeders": 1}},
		tags:          []map[string]any{{"id": 1, "label": "anime"}},
		testAllStatus: http.StatusOK,
		bodies:        map[string][]byte{},
	}

	mux := http.NewServeMux()
	handle := func(pattern string, fn func(w http.ResponseWriter, r *http.Request)) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Api-Key") != "test-key" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			m.mu.Lock()
			defer m.mu.Unlock()
			if r.Method != http.MethodGet {
				body, _ := io.ReadAll(r.Body)
				m.bodies[r.Method+" "+r.URL.Path] = body
			}
			fn(w, r)
		})
	}

	handle("GET /api/v1/system/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"version": "2.6.5", "branch": "master", "startTime": "2026-09-01T00:00:00Z"})
	})
	handle("GET /api/v1/health", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, nonNil(m.health)) })
	handle("GET /api/v1/indexer", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, nonNil(m.indexers)) })
	handle("GET /api/v1/indexer/{id}", func(w http.ResponseWriter, r *http.Request) {
		for _, ix := range m.indexers {
			if jsonNum(ix["id"]) == r.PathValue("id") {
				writeJSON(w, ix)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
	})
	handle("DELETE /api/v1/indexer/{id}", func(w http.ResponseWriter, r *http.Request) {
		kept := m.indexers[:0]
		for _, ix := range m.indexers {
			if jsonNum(ix["id"]) != r.PathValue("id") {
				kept = append(kept, ix)
			}
		}
		m.indexers = kept
		writeJSON(w, map[string]any{})
	})
	handle("GET /api/v1/indexer/schema", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, nonNil(m.schema)) })
	handle("POST /api/v1/indexer", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.Unmarshal(m.bodies["POST /api/v1/indexer"], &body)
		body["id"] = 99
		m.indexers = append(m.indexers, body)
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, body)
	})
	handle("PUT /api/v1/indexer/bulk", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.Unmarshal(m.bodies["PUT /api/v1/indexer/bulk"], &body)
		for _, ix := range m.indexers {
			for _, id := range body["ids"].([]any) {
				if jsonNum(ix["id"]) == jsonNum(id) {
					for _, k := range []string{"enable", "priority", "appProfileId"} {
						if v, ok := body[k]; ok {
							ix[k] = v
						}
					}
				}
			}
		}
		w.WriteHeader(http.StatusAccepted)
		writeJSON(w, m.indexers)
	})
	handle("POST /api/v1/indexer/test", func(w http.ResponseWriter, r *http.Request) {
		status, body := http.StatusOK, "{}"
		if m.testOne != nil {
			status, body = m.testOne(m.bodies["POST /api/v1/indexer/test"])
		}
		w.WriteHeader(status)
		w.Write([]byte(body))
	})
	handle("POST /api/v1/indexer/testall", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(m.testAllStatus)
		json.NewEncoder(w).Encode(nonNil(m.testAll))
	})
	handle("GET /api/v1/indexerstats", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"indexers": nonNil(m.stats)})
	})
	handle("GET /api/v1/indexerproxy", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, []any{}) })
	handle("GET /api/v1/applications", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, nonNil(m.apps)) })
	handle("GET /api/v1/appprofile", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, m.profiles) })
	handle("GET /api/v1/tag", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, m.tags) })
	handle("GET /api/v1/search", func(w http.ResponseWriter, r *http.Request) {
		m.searchQuery = r.URL.RawQuery
		writeJSON(w, nonNil(m.releases))
	})
	handle("POST /api/v1/command", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"id": 77, "status": "queued"})
	})

	m.Server = httptest.NewServer(mux)
	t.Cleanup(m.Close)
	t.Setenv(BaseURLEnv, m.URL)
	t.Setenv(APIKeyEnv, "test-key")
	return m
}

func (m *mockProwlarr) body(key string) map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out map[string]any
	json.Unmarshal(m.bodies[key], &out)
	return out
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func nonNil(l []map[string]any) []map[string]any {
	if l == nil {
		return []map[string]any{}
	}
	return l
}

func jsonNum(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func indexer(id int, name string, enable bool, tags ...int) map[string]any {
	if tags == nil {
		tags = []int{}
	}
	return map[string]any{
		"id": id, "name": name, "definitionName": strings.ToLower(name), "implementation": "Cardigann",
		"enable": enable, "protocol": "torrent", "privacy": "public", "priority": 25,
		"appProfileId": 1, "tags": tags, "supportsRss": true, "supportsSearch": true,
		"fields": []map[string]any{{"name": "username", "value": "secret-user", "privacy": "userName"}},
	}
}

func app(id int, name, level string, tags ...int) map[string]any {
	if tags == nil {
		tags = []int{}
	}
	return map[string]any{
		"id": id, "name": name, "implementation": name, "syncLevel": level, "tags": tags,
		"fields": []map[string]any{
			{"name": "baseUrl", "value": "http://localhost:7878", "privacy": "normal"},
			{"name": "apiKey", "value": "radarr-secret-key", "privacy": "apiKey"},
		},
	}
}

func containsSubstring(list []string, sub string) bool {
	for _, s := range list {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// --- indexers --------------------------------------------------------------------

func TestIndexersPutTheFailingOneFirstWithItsNumbers(t *testing.T) {
	m := newMockProwlarr(t)
	failing := indexer(2, "Zeta", true)
	failing["status"] = map[string]any{
		"disabledTill":   time.Now().Add(3 * time.Hour).UTC().Format(time.RFC3339),
		"initialFailure": time.Now().Add(-26 * time.Hour).UTC().Format(time.RFC3339),
	}
	stale := indexer(3, "Beta", true)
	stale["status"] = map[string]any{"disabledTill": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)}
	m.indexers = []map[string]any{indexer(1, "Alpha", true), failing, stale, indexer(4, "Off", false)}
	m.stats = []map[string]any{{"indexerId": 1, "numberOfQueries": 40, "numberOfRssQueries": 10,
		"numberOfFailedQueries": 2, "numberOfGrabs": 5, "averageResponseTime": 800}}

	out, err := GetIndexers(context.Background(), "")
	if err != nil {
		t.Fatalf("GetIndexers: %v", err)
	}
	if out.Indexers[0].Name != "Zeta" || !out.Indexers[0].Failing {
		t.Errorf("the failing indexer should be first: %+v", out.Indexers[0])
	}
	if out.Indexers[len(out.Indexers)-1].Name != "Off" {
		t.Errorf("the disabled indexer should be last")
	}
	for _, ix := range out.Indexers {
		if ix.Name == "Beta" && ix.Failing {
			t.Error("a backoff that has run out is not a failure")
		}
		if ix.Name == "Alpha" && (ix.Queries != 50 || ix.Grabs != 5 || ix.AvgResponseMs != 800) {
			t.Errorf("stats not joined: %+v", ix)
		}
	}
	if out.FailingCount != 1 || out.EnabledCount != 3 {
		t.Errorf("counts = %+v", out)
	}
	if !containsSubstring(out.Warnings, "Zeta is failing") {
		t.Errorf("warnings = %v", out.Warnings)
	}
}

func TestResolveIndexerRefusesAmbiguity(t *testing.T) {
	all := []Indexer{{ID: 1, Name: "TorrentLeech"}, {ID: 2, Name: "TorrentDay"}, {ID: 3, Name: "Nyaa"}}
	if ix, err := resolveIndexer(all, "nyaa"); err != nil || ix.ID != 3 {
		t.Errorf("resolveIndexer(nyaa) = %+v, %v", ix, err)
	}
	if _, err := resolveIndexer(all, "torrent"); err == nil || !strings.Contains(err.Error(), "more than one") {
		t.Errorf("an ambiguous name should be refused: %v", err)
	}
	if ix, err := resolveIndexer(all, "2"); err != nil || ix.Name != "TorrentDay" {
		t.Errorf("by id: %+v, %v", ix, err)
	}
}

// --- applications ------------------------------------------------------------------

func TestApplicationsFollowProwlarrsTagRule(t *testing.T) {
	m := newMockProwlarr(t)
	m.indexers = []map[string]any{indexer(1, "Public", true), indexer(2, "AnimeOnly", true, 1)}
	m.apps = []map[string]any{app(1, "Radarr", "fullSync"), app(2, "Sonarr", "addOnly", 1)}

	out, err := GetApplications(context.Background(), false)
	if err != nil {
		t.Fatalf("GetApplications: %v", err)
	}
	radarr, sonarr := out.Applications[0], out.Applications[1]
	if len(radarr.Receives) != 2 {
		t.Errorf("an app with no tags takes every indexer: %v", radarr.Receives)
	}
	if len(sonarr.Receives) != 1 || sonarr.Receives[0] != "AnimeOnly" {
		t.Errorf("a tagged app takes only indexers sharing a tag: %v", sonarr.Receives)
	}
	if !containsSubstring(out.Warnings, "Add Only") {
		t.Errorf("Add Only must be explained: %v", out.Warnings)
	}
	if radarr.BaseURL != "http://localhost:7878" {
		t.Errorf("base url = %q", radarr.BaseURL)
	}
}

func TestSecretsNeverLeave(t *testing.T) {
	m := newMockProwlarr(t)
	m.indexers = []map[string]any{indexer(1, "Private", true)}
	m.apps = []map[string]any{app(1, "Radarr", "fullSync")}

	apps, _ := GetApplications(context.Background(), false)
	ixs, _ := GetIndexers(context.Background(), "")
	for _, v := range []any{apps, ixs} {
		b, _ := json.Marshal(v)
		if strings.Contains(string(b), "radarr-secret-key") || strings.Contains(string(b), "secret-user") {
			t.Errorf("a credential leaked into a result: %s", b)
		}
	}
}

// --- tests -------------------------------------------------------------------------

// Prowlarr answers testall with 400 as soon as one indexer fails, and the body
// is the full result list. Treating the 400 as an error would lose the answer.
func TestTestAllReadsTheBodyOfA400(t *testing.T) {
	m := newMockProwlarr(t)
	m.indexers = []map[string]any{indexer(1, "Good", true), indexer(2, "Bad", true), indexer(3, "Off", false)}
	m.testAllStatus = http.StatusBadRequest
	m.testAll = []map[string]any{
		{"id": 1, "isValid": true, "validationFailures": []any{}},
		{"id": 2, "isValid": false, "validationFailures": []map[string]any{
			{"propertyName": "", "errorMessage": "Unable to access site, blocked by CloudFlare Protection."}}},
	}

	rep, err := TestIndexers(context.Background(), "")
	if err != nil {
		t.Fatalf("TestIndexers: %v", err)
	}
	if rep.TestedCount != 2 || rep.FailedCount != 1 || rep.SkippedCount != 1 {
		t.Errorf("report = %+v", rep)
	}
	if rep.Results[0].Name != "Bad" {
		t.Errorf("failures first: %+v", rep.Results)
	}
	if !containsSubstring(rep.Warnings, "FlareSolverr") {
		t.Errorf("a Cloudflare failure should name the fix: %v", rep.Warnings)
	}
}

func TestTestOneSendsTheStoredIndexerBack(t *testing.T) {
	m := newMockProwlarr(t)
	m.indexers = []map[string]any{indexer(1, "Tracker", true)}
	var sent map[string]any
	m.testOne = func(body []byte) (int, string) {
		json.Unmarshal(body, &sent)
		return http.StatusBadRequest, `[{"propertyName":"Cookie","errorMessage":"Cookie has expired"}]`
	}

	rep, err := TestIndexers(context.Background(), "tracker")
	if err != nil {
		t.Fatalf("TestIndexers: %v", err)
	}
	if sent["name"] != "Tracker" || sent["fields"] == nil {
		t.Errorf("the test should send the indexer exactly as stored: %v", sent)
	}
	if rep.FailedCount != 1 || !strings.Contains(rep.Results[0].Errors[0], "Cookie has expired") {
		t.Errorf("report = %+v", rep)
	}
}

// --- update ------------------------------------------------------------------------

func TestUpdateSendsOnlyTheNamedFields(t *testing.T) {
	m := newMockProwlarr(t)
	m.indexers = []map[string]any{indexer(1, "Tracker", true)}
	m.apps = []map[string]any{app(1, "Radarr", "fullSync"), app(2, "Sonarr", "addOnly")}

	off := false
	plan, err := PlanUpdate(context.Background(), UpdateRequest{Indexer: "Tracker", Enable: &off})
	if err != nil {
		t.Fatalf("PlanUpdate: %v", err)
	}
	if len(plan.FullSyncApps) != 1 || len(plan.AddOnlyApps) != 1 {
		t.Errorf("reach = %v / %v", plan.FullSyncApps, plan.AddOnlyApps)
	}
	if !containsSubstring(plan.Warnings, "Sonarr is on Add Only") {
		t.Errorf("warnings = %v", plan.Warnings)
	}

	res, err := Update(context.Background(), plan)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	body := m.body("PUT /api/v1/indexer/bulk")
	if body["enable"] != false || body["priority"] != nil || body["fields"] != nil {
		t.Errorf("the bulk edit should carry only what changed: %v", body)
	}
	if res.Indexer.Enabled {
		t.Error("the indexer should read back disabled")
	}
}

func TestUpdateRefusesWhatItCannotDo(t *testing.T) {
	m := newMockProwlarr(t)
	m.indexers = []map[string]any{indexer(1, "Tracker", true)}
	on, prio, bad := true, 25, 99

	cases := map[string]UpdateRequest{
		"nothing":        {Indexer: "Tracker"},
		"already on":     {Indexer: "Tracker", Enable: &on},
		"same priority":  {Indexer: "Tracker", Priority: &prio},
		"priority range": {Indexer: "Tracker", Priority: &bad},
		"unknown":        {Indexer: "Nope", Enable: &on},
		"bad profile":    {Indexer: "Tracker", AppProfile: "Klingon"},
	}
	for name, req := range cases {
		if _, err := PlanUpdate(context.Background(), req); err == nil {
			t.Errorf("%s: should have been refused", name)
		}
	}
}

// --- search ------------------------------------------------------------------------

func TestSearchSortsBySeedersAndDropsDownloadLinks(t *testing.T) {
	m := newMockProwlarr(t)
	m.releases = []map[string]any{
		{"title": "Dune.2021.720p", "indexer": "A", "protocol": "torrent", "seeders": 3, "size": 1e9,
			"downloadUrl": "http://prowlarr/1/download?apikey=secret", "ageHours": 10},
		{"title": "Dune.2021.2160p", "indexer": "B", "protocol": "torrent", "seeders": 90, "size": 2e10,
			"downloadUrl": "http://prowlarr/2/download?apikey=secret", "ageHours": 5},
		{"title": "Dune.2021.dead", "indexer": "A", "protocol": "torrent", "seeders": 0, "ageHours": 1},
	}

	res, err := Search(context.Background(), SearchRequest{Query: "Dune 2021", Kind: "movie"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if !strings.Contains(m.searchQuery, "categories=2000") {
		t.Errorf("a movie search should limit to 2000: %s", m.searchQuery)
	}
	if res.Releases[0].Title != "Dune.2021.2160p" {
		t.Errorf("most seeded first: %+v", res.Releases)
	}
	if b, _ := json.Marshal(res); strings.Contains(string(b), "apikey=secret") {
		t.Error("the download link carries Prowlarr's API key and must not be returned")
	}
	if !containsSubstring(res.Warnings, "no seeders") || res.ByIndexer["A"] != 2 {
		t.Errorf("result = %+v", res)
	}
}

// --- add ---------------------------------------------------------------------------

func TestAddFromADefinitionMasksCredentials(t *testing.T) {
	m := newMockProwlarr(t)
	m.apps = []map[string]any{app(1, "Radarr", "fullSync")}
	m.schema = []map[string]any{{
		"name": "PrivateHD", "definitionName": "privatehd", "implementation": "Cardigann",
		"protocol": "torrent", "privacy": "private", "enable": false, "presets": []any{map[string]any{"x": 1}},
		"fields": []map[string]any{
			{"name": "username", "label": "Username", "type": "textbox", "privacy": "userName"},
			{"name": "password", "label": "Password", "type": "password", "privacy": "password"},
			{"name": "sort", "label": "Sort", "type": "select", "value": 0,
				"selectOptions": []map[string]any{{"value": 0, "name": "created"}, {"value": 1, "name": "seeders"}}},
			{"name": "info_flaresolverr", "type": "info"},
		},
	}}

	plan, err := PlanAdd(context.Background(), AddRequest{
		Definition: "privatehd",
		Settings:   map[string]string{"username": "me", "password": "hunter2", "Sort": "seeders"},
	})
	if err != nil {
		t.Fatalf("PlanAdd: %v", err)
	}
	for _, s := range plan.Settings {
		if s.Value == "hunter2" || s.Value == "me" {
			t.Errorf("a credential is shown in the confirmation: %+v", plan.Settings)
		}
	}
	if !plan.Definition.NeedsFlareSolverr || !containsSubstring(plan.Warnings, "Cloudflare") {
		t.Errorf("the FlareSolverr need should be said: %+v", plan)
	}
	if plan.AppProfile == "" || plan.Priority != 25 || len(plan.ReachesApps) != 1 {
		t.Errorf("plan = %+v", plan)
	}

	if _, err := Add(context.Background(), plan); err != nil {
		t.Fatalf("Add: %v", err)
	}
	body := m.body("POST /api/v1/indexer")
	if body["presets"] != nil || body["enable"] != true || body["appProfileId"] != 1.0 {
		t.Errorf("body = %v", body)
	}
	values := map[string]any{}
	for _, f := range body["fields"].([]any) {
		fm := f.(map[string]any)
		values[fm["name"].(string)] = fm["value"]
	}
	if values["password"] != "hunter2" || values["sort"] != 1.0 {
		t.Errorf("the settings should be sent as Prowlarr stores them: %v", values)
	}
}

func TestAddRefusesWhatItCannotResolve(t *testing.T) {
	m := newMockProwlarr(t)
	m.indexers = []map[string]any{indexer(1, "Nyaa", true)}
	m.schema = []map[string]any{
		{"name": "Nyaa", "definitionName": "nyaa", "protocol": "torrent", "privacy": "public"},
		{"name": "1337x", "definitionName": "1337x", "protocol": "torrent", "privacy": "public",
			"fields": []map[string]any{{"name": "sort", "type": "select",
				"selectOptions": []map[string]any{{"value": 0, "name": "created"}}}}},
	}

	cases := map[string]AddRequest{
		"unknown definition": {Definition: "nope"},
		"already added":      {Definition: "nyaa"},
		"unknown setting":    {Definition: "1337x", Settings: map[string]string{"cookie": "x"}},
		"bad select":         {Definition: "1337x", Settings: map[string]string{"sort": "size"}},
		"unknown tag":        {Definition: "1337x", Tags: []string{"movies"}},
	}
	for name, req := range cases {
		if _, err := PlanAdd(context.Background(), req); err == nil {
			t.Errorf("%s: should have been refused", name)
		}
	}
}

// --- remove and sync ---------------------------------------------------------------

func TestRemoveReachesAddOnlyAppsToo(t *testing.T) {
	m := newMockProwlarr(t)
	m.indexers = []map[string]any{indexer(1, "Tracker", true), indexer(2, "Other", true)}
	m.apps = []map[string]any{app(1, "Radarr", "fullSync"), app(2, "Sonarr", "addOnly"), app(3, "Lidarr", "disabled")}

	plan, err := PlanRemove(context.Background(), "Tracker")
	if err != nil {
		t.Fatalf("PlanRemove: %v", err)
	}
	if strings.Join(plan.RemovedFrom, ",") != "Radarr,Sonarr" {
		t.Errorf("removed from = %v", plan.RemovedFrom)
	}
	res, err := Remove(context.Background(), plan)
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if res.Remaining != 1 {
		t.Errorf("remaining = %d", res.Remaining)
	}
}

func TestSyncSendsProwlarrsOwnCommand(t *testing.T) {
	m := newMockProwlarr(t)
	m.indexers = []map[string]any{indexer(1, "Tracker", true)}
	m.apps = []map[string]any{app(1, "Radarr", "fullSync")}

	plan, err := PlanSync(context.Background(), true)
	if err != nil {
		t.Fatalf("PlanSync: %v", err)
	}
	if !containsSubstring(plan.Warnings, "overwriting") {
		t.Errorf("a forced sync must say it overwrites: %v", plan.Warnings)
	}
	res, err := Sync(context.Background(), plan)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	body := m.body("POST /api/v1/command")
	if body["name"] != "ApplicationIndexerSync" || body["forceSync"] != true || res.CommandID != 77 {
		t.Errorf("body = %v, result = %+v", body, res)
	}

	m.apps = []map[string]any{app(1, "Radarr", "disabled")}
	if _, err := PlanSync(context.Background(), false); err == nil {
		t.Error("a sync with nowhere to go should be refused")
	}
}

// --- health and errors ---------------------------------------------------------------

func TestHealthPassesProwlarrsChecksThrough(t *testing.T) {
	m := newMockProwlarr(t)
	m.indexers = []map[string]any{indexer(1, "Tracker", true)}
	m.health = []map[string]any{{"source": "IndexerStatusCheck", "type": "warning",
		"message": "Indexers unavailable due to failures: Tracker"}}

	h, err := GetHealth(context.Background())
	if err != nil {
		t.Fatalf("GetHealth: %v", err)
	}
	if !containsSubstring(h.Warnings, "Indexers unavailable due to failures") {
		t.Errorf("warnings = %v", h.Warnings)
	}
	if !containsSubstring(h.Warnings, "syncs to no application") {
		t.Errorf("no apps should be a warning: %v", h.Warnings)
	}
}

// A list that could not be read is not an empty list.
func TestHealthDoesNotReportUnreadAppsAsNone(t *testing.T) {
	m := newMockProwlarr(t)
	m.indexers = []map[string]any{indexer(1, "Tracker", true)}
	m.Server.Config.Handler = brokenRoute(m.Server.Config.Handler, "/api/v1/applications")

	h, err := GetHealth(context.Background())
	if err != nil {
		t.Fatalf("GetHealth: %v", err)
	}
	if h.SyncLevels != nil || !containsSubstring(h.Warnings, "could not read the applications") {
		t.Errorf("an unread app list must be said, not shown as zero: %+v", h)
	}
	if containsSubstring(h.Warnings, "syncs to no application") {
		t.Errorf("'no application' is a claim about apps that were never read: %v", h.Warnings)
	}
}

func brokenRoute(next http.Handler, path string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == path {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func TestBadAPIKeyIsNamed(t *testing.T) {
	newMockProwlarr(t)
	t.Setenv(APIKeyEnv, "wrong")
	_, err := GetHealth(context.Background())
	if err == nil || !strings.Contains(err.Error(), APIKeyEnv) {
		t.Fatalf("a 401 should point at the API key variable, got: %v", err)
	}
}
