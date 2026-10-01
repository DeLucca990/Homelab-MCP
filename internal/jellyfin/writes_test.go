package jellyfin

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

// writeMock is a Jellyfin that records every write, for the tools that
// change something. Routes can be removed to play an older server.
type writeMock struct {
	*httptest.Server

	mu       sync.Mutex
	users    []map[string]any
	sessions []map[string]any
	encoding map[string]any
	activity []map[string]any
	missing  map[string]bool // routes answered with 404, as a pre-10.9 server would

	writes []recorded
}

type recorded struct {
	method, path, query string
	body                map[string]any
}

func newWriteMock(t *testing.T) *writeMock {
	t.Helper()
	m := &writeMock{
		users: []map[string]any{
			{"Id": "aaaa1111", "Name": "Pedro", "HasPassword": true,
				"Configuration": map[string]any{"SubtitleMode": "Default", "GroupedFolders": []any{"keep-me"},
					"AudioLanguagePreference": "eng"},
				"Policy": map[string]any{"IsAdministrator": true, "EnableAllFolders": true,
					"EnableVideoPlaybackTranscoding": true, "PasswordResetProviderId": "keep-me"}},
			{"Id": "bbbb2222", "Name": "Ana",
				"Configuration": map[string]any{"SubtitleMode": "Default"},
				"Policy": map[string]any{"IsAdministrator": false, "EnableAllFolders": true,
					"EnableVideoPlaybackTranscoding": true, "PasswordResetProviderId": "keep-me"}},
		},
		encoding: map[string]any{"HardwareAccelerationType": "none", "HardwareDecodingCodecs": []any{"h264", "vc1"},
			"EnableHardwareEncoding": true, "TranscodingTempPath": "/cache/transcodes"},
		missing: map[string]bool{},
	}

	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()

		if m.missing[r.URL.Path] {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method != http.MethodGet {
			raw, _ := io.ReadAll(r.Body)
			var body map[string]any
			json.Unmarshal(raw, &body)
			m.writes = append(m.writes, recorded{r.Method, r.URL.Path, r.URL.RawQuery, body})
			if r.URL.Path == "/Users/bbbb2222/Policy" && body["IsDisabled"] == true && m.users[1]["Policy"].(map[string]any)["IsAdministrator"] == true {
				w.WriteHeader(http.StatusForbidden)
				w.Write([]byte(`"Administrators cannot be disabled."`))
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}

		switch {
		case r.URL.Path == "/Users":
			writeJSONResponse(w, m.users)
		case strings.HasPrefix(r.URL.Path, "/Users/"):
			id := strings.TrimPrefix(r.URL.Path, "/Users/")
			for _, u := range m.users {
				if u["Id"] == id {
					writeJSONResponse(w, u)
					return
				}
			}
			w.WriteHeader(http.StatusNotFound)
		case r.URL.Path == "/Library/VirtualFolders":
			writeJSONResponse(w, []map[string]any{
				{"Name": "Movies", "ItemId": "f1f1", "CollectionType": "movies", "Locations": []string{"/data/movies"}},
				{"Name": "Shows", "ItemId": "f2f2", "CollectionType": "tvshows", "Locations": []string{"/data/tv"}},
			})
		case r.URL.Path == "/Localization/Cultures":
			writeJSONResponse(w, []map[string]any{
				{"Name": "Portuguese", "DisplayName": "Portuguese", "TwoLetterISOLanguageName": "pt",
					"ThreeLetterISOLanguageName": "por", "ThreeLetterISOLanguageNames": []string{"por"}},
				{"Name": "English", "DisplayName": "English", "TwoLetterISOLanguageName": "en",
					"ThreeLetterISOLanguageName": "eng", "ThreeLetterISOLanguageNames": []string{"eng"}},
			})
		case r.URL.Path == "/Sessions":
			writeJSONResponse(w, m.sessions)
		case r.URL.Path == "/System/Configuration/encoding":
			writeJSONResponse(w, m.encoding)
		case r.URL.Path == "/System/ActivityLog/Entries":
			writeJSONResponse(w, map[string]any{"Items": m.activity, "TotalRecordCount": len(m.activity)})
		case strings.HasPrefix(r.URL.Path, "/Items/"):
			writeJSONResponse(w, map[string]any{"Id": "it1", "Name": "Severance", "Type": "Series",
				"Path": "/data/tv/Severance", "UserData": map[string]any{"Played": false}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(m.Close)
	t.Setenv(BaseURLEnv, m.URL)
	t.Setenv(APIKeyEnv, "test-key")
	return m
}

func writeJSONResponse(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func (m *writeMock) last() recorded {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.writes) == 0 {
		return recorded{}
	}
	return m.writes[len(m.writes)-1]
}

func (m *writeMock) all() []recorded {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]recorded(nil), m.writes...)
}

func ptrTo[T any](v T) *T { return &v }

// --- preferences -------------------------------------------------------------------

// Bazarr calls Brazilian Portuguese "pb" and the file "pt-BR"; Jellyfin calls
// both "por". Every spelling has to land there, and the rest of the user's
// preferences have to go back exactly as they were.
func TestPreferencesResolveThePortugueseSpellingsAndKeepTheRest(t *testing.T) {
	m := newWriteMock(t)

	for _, in := range []string{"pt-BR", "pb", "Portuguese", "por"} {
		plan, err := PlanPreferences(context.Background(), PreferencesRequest{
			User: "ana", SubtitleLanguage: ptrTo(in), SubtitleMode: "smart",
		})
		if err != nil {
			t.Fatalf("PlanPreferences(%q): %v", in, err)
		}
		if plan.config["SubtitleLanguagePreference"] != "por" || plan.config["SubtitleMode"] != "Smart" {
			t.Errorf("%q → %v", in, plan.config)
		}
	}

	plan, _ := PlanPreferences(context.Background(), PreferencesRequest{
		User: "Pedro", SubtitleLanguage: ptrTo("pt-BR"), SubtitleMode: "Always",
	})
	if _, err := SetPreferences(context.Background(), plan); err != nil {
		t.Fatalf("SetPreferences: %v", err)
	}
	w := m.last()
	if w.path != "/Users/Configuration" || !strings.Contains(w.query, "userId=aaaa1111") {
		t.Errorf("sent to %s?%s", w.path, w.query)
	}
	if w.body["AudioLanguagePreference"] != "eng" || w.body["GroupedFolders"] == nil {
		t.Errorf("fields that were not asked about must go back untouched: %v", w.body)
	}
}

func TestPreferencesFallBackToTheOldRoute(t *testing.T) {
	m := newWriteMock(t)
	m.missing["/Users/Configuration"] = true

	plan, err := PlanPreferences(context.Background(), PreferencesRequest{User: "Ana", SubtitleMode: "None"})
	if err != nil {
		t.Fatalf("PlanPreferences: %v", err)
	}
	if _, err := SetPreferences(context.Background(), plan); err != nil {
		t.Fatalf("SetPreferences: %v", err)
	}
	if m.last().path != "/Users/bbbb2222/Configuration" {
		t.Errorf("a pre-10.9 server should get the old route, got %s", m.last().path)
	}
}

func TestPreferencesSayWhatWillNotWork(t *testing.T) {
	newWriteMock(t)

	plan, err := PlanPreferences(context.Background(), PreferencesRequest{User: "Ana", SubtitleMode: "Always"})
	if err != nil {
		t.Fatalf("PlanPreferences: %v", err)
	}
	if !hasWarningContaining(plan.Warnings, "will load nothing") {
		t.Errorf("Always with no language loads nothing, and must say so: %v", plan.Warnings)
	}
	for name, req := range map[string]PreferencesRequest{
		"nothing":      {User: "Ana"},
		"no change":    {User: "Ana", SubtitleMode: "Default"},
		"bad mode":     {User: "Ana", SubtitleMode: "sometimes"},
		"bad language": {User: "Ana", SubtitleLanguage: ptrTo("klingon")},
		"unknown user": {User: "Zé", SubtitleMode: "None"},
	} {
		if _, err := PlanPreferences(context.Background(), req); err == nil {
			t.Errorf("%s: should have been refused", name)
		}
	}
}

// --- access ------------------------------------------------------------------------

func TestAccessMapsLibrariesAndKeepsThePolicyWhole(t *testing.T) {
	m := newWriteMock(t)

	plan, err := PlanAccess(context.Background(), AccessRequest{
		User: "Ana", Libraries: []string{"movies"}, RemoteBitrateMbps: ptrTo(4.0),
	})
	if err != nil {
		t.Fatalf("PlanAccess: %v", err)
	}
	if !hasWarningContaining(plan.Warnings, "transcoded down") {
		t.Errorf("a low cap costs an encode per stream: %v", plan.Warnings)
	}
	if _, err := SetAccess(context.Background(), plan); err != nil {
		t.Fatalf("SetAccess: %v", err)
	}
	w := m.last()
	if w.path != "/Users/bbbb2222/Policy" {
		t.Fatalf("sent to %s", w.path)
	}
	folders, _ := w.body["EnabledFolders"].([]any)
	if w.body["EnableAllFolders"] != false || len(folders) != 1 || folders[0] != "f1f1" {
		t.Errorf("libraries = %v / %v", w.body["EnableAllFolders"], w.body["EnabledFolders"])
	}
	if w.body["RemoteClientBitrateLimit"] != 4e6 || w.body["PasswordResetProviderId"] != "keep-me" {
		t.Errorf("the policy must go back whole with only the named fields changed: %v", w.body)
	}
}

func TestAccessRefusesToDisableAnAdministrator(t *testing.T) {
	newWriteMock(t)
	_, err := PlanAccess(context.Background(), AccessRequest{User: "Pedro", Disabled: ptrTo(true)})
	if err == nil || !strings.Contains(err.Error(), "administrator") {
		t.Errorf("got: %v", err)
	}
}

// Jellyfin's own refusals carry the rule that refused them, and that sentence
// is the answer — not "needs an administrator key".
func TestForbiddenWithAReasonIsTheReason(t *testing.T) {
	m := newWriteMock(t)
	m.users[1]["Policy"].(map[string]any)["IsAdministrator"] = true

	c, _ := newClient()
	err := c.send(context.Background(), http.MethodPost, "/Users/bbbb2222/Policy", nil,
		map[string]any{"IsDisabled": true})
	if err == nil || !strings.Contains(err.Error(), "Administrators cannot be disabled") {
		t.Errorf("got: %v", err)
	}
}

// --- sessions ----------------------------------------------------------------------

func streaming(remote bool, transcoding map[string]any, checkIn time.Duration) map[string]any {
	s := map[string]any{
		"Id": "sess1", "UserName": "Ana", "DeviceName": "Living Room TV", "DeviceId": "dev-9",
		"SupportsRemoteControl": remote,
		"LastPlaybackCheckIn":   time.Now().Add(-checkIn).UTC().Format(time.RFC3339),
		"NowPlayingItem":        map[string]any{"Name": "Dune", "Type": "Movie", "ProductionYear": 2021},
		"PlayState":             map[string]any{"PlayMethod": "Transcode", "PlaySessionId": "ps-7"},
	}
	if transcoding != nil {
		s["TranscodingInfo"] = transcoding
	}
	return s
}

// A ghost session's app is gone, so a stop command reaches nobody. Ending the
// transcode by device and play session is what frees the CPU.
func TestStoppingAGhostEndsTheTranscode(t *testing.T) {
	m := newWriteMock(t)
	m.sessions = []map[string]any{streaming(false, map[string]any{"IsVideoDirect": false}, 20*time.Minute)}

	plan, err := PlanStop(context.Background(), "sess1", "bye")
	if err != nil {
		t.Fatalf("PlanStop: %v", err)
	}
	if plan.SendsStop || !plan.KillsTranscode || plan.Message != "" {
		t.Errorf("plan = %+v", plan)
	}
	if _, err := Stop(context.Background(), plan); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	writes := m.all()
	if len(writes) != 1 || writes[0].method != http.MethodDelete || writes[0].path != "/Videos/ActiveEncodings" ||
		!strings.Contains(writes[0].query, "deviceId=dev-9") || !strings.Contains(writes[0].query, "playSessionId=ps-7") {
		t.Errorf("writes = %+v", writes)
	}
}

func TestStoppingALiveSessionSendsTheMessageThenTheStop(t *testing.T) {
	m := newWriteMock(t)
	m.sessions = []map[string]any{streaming(true, nil, 5*time.Second)}

	plan, err := PlanStop(context.Background(), "sess1", "server restarting")
	if err != nil {
		t.Fatalf("PlanStop: %v", err)
	}
	if _, err := Stop(context.Background(), plan); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	writes := m.all()
	if len(writes) != 2 || writes[0].path != "/Sessions/sess1/Message" || writes[1].path != "/Sessions/sess1/Playing/Stop" {
		t.Errorf("writes = %+v", writes)
	}
	if writes[0].body["Text"] != "server restarting" {
		t.Errorf("message body = %v", writes[0].body)
	}
}

func TestNothingToStopIsSaid(t *testing.T) {
	m := newWriteMock(t)
	m.sessions = []map[string]any{streaming(false, nil, 5*time.Second)}

	if _, err := PlanStop(context.Background(), "sess1", ""); err == nil ||
		!strings.Contains(err.Error(), "nothing the server can stop") {
		t.Errorf("a direct play with no remote control cannot be stopped: %v", err)
	}
	if _, err := PlanStop(context.Background(), "gone", ""); err == nil {
		t.Error("an unknown session should be refused")
	}
}

// --- library -------------------------------------------------------------------------

func TestScanNamesItsScope(t *testing.T) {
	m := newWriteMock(t)

	plan, err := PlanScan(context.Background(), "shows", "")
	if err != nil {
		t.Fatalf("PlanScan: %v", err)
	}
	if _, err := Scan(context.Background(), plan); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if w := m.last(); w.path != "/Items/f2f2/Refresh" || !strings.Contains(w.query, "replaceAllMetadata=false") {
		t.Errorf("a library scan refreshes the library folder: %+v", w)
	}

	plan, _ = PlanScan(context.Background(), "", "")
	if !hasWarningContaining(plan.Warnings, "full scan") {
		t.Errorf("a full scan should say what it costs: %v", plan.Warnings)
	}
	Scan(context.Background(), plan)
	if m.last().path != "/Library/Refresh" {
		t.Errorf("got %s", m.last().path)
	}

	if _, err := PlanScan(context.Background(), "Anime", ""); err == nil {
		t.Error("an unknown library should be refused")
	}
}

func TestMarkPlayedFallsBackToTheOldRoute(t *testing.T) {
	m := newWriteMock(t)
	m.missing["/UserPlayedItems/it1"] = true

	plan, err := PlanPlayed(context.Background(), "Ana", "it1", true)
	if err != nil {
		t.Fatalf("PlanPlayed: %v", err)
	}
	if !hasWarningContaining(plan.Warnings, "every episode") {
		t.Errorf("a series marks every episode: %v", plan.Warnings)
	}
	if _, err := SetPlayed(context.Background(), plan); err != nil {
		t.Fatalf("SetPlayed: %v", err)
	}
	if w := m.last(); w.method != http.MethodPost || w.path != "/Users/bbbb2222/PlayedItems/it1" {
		t.Errorf("got %+v", w)
	}
}

// --- transcoding ------------------------------------------------------------------

func TestTranscodingChangeCarriesItsWayBack(t *testing.T) {
	m := newWriteMock(t)

	plan, err := PlanEncoding(context.Background(), EncodingRequest{
		Acceleration: "vaapi", Device: "/dev/dri/renderD128", DecodeCodecs: []string{"h264", "h265"},
	})
	if err != nil {
		t.Fatalf("PlanEncoding: %v", err)
	}
	var revertAccel string
	for _, r := range plan.Revert {
		if r.Field == "hardware acceleration" {
			revertAccel = r.To
		}
	}
	if revertAccel != "none" {
		t.Errorf("the revert should restore 'none': %+v", plan.Revert)
	}
	if !hasWarningContaining(plan.Warnings, "/dev/dri") {
		t.Errorf("the container needs the GPU, and must be told so: %v", plan.Warnings)
	}
	if _, err := SetEncoding(context.Background(), plan); err != nil {
		t.Fatalf("SetEncoding: %v", err)
	}
	w := m.last()
	codecs, _ := w.body["HardwareDecodingCodecs"].([]any)
	if w.body["HardwareAccelerationType"] != "vaapi" || w.body["VaapiDevice"] != "/dev/dri/renderD128" ||
		len(codecs) != 2 || codecs[1] != "hevc" || w.body["TranscodingTempPath"] != "/cache/transcodes" {
		t.Errorf("body = %v", w.body)
	}

	for name, req := range map[string]EncodingRequest{
		"bad backend":   {Acceleration: "cuda"},
		"bad device":    {Acceleration: "vaapi", Device: "renderD128"},
		"device on nv":  {Acceleration: "nvenc", Device: "/dev/dri/renderD128"},
		"bad codec":     {DecodeCodecs: []string{"divx"}},
		"nothing asked": {},
	} {
		if _, err := PlanEncoding(context.Background(), req); err == nil {
			t.Errorf("%s: should have been refused", name)
		}
	}
}

// --- activity ------------------------------------------------------------------------

func TestActivityCallsOutABurstOfFailedSignIns(t *testing.T) {
	m := newWriteMock(t)
	for range 6 {
		m.activity = append(m.activity, map[string]any{"Name": "Failed login from 203.0.113.9",
			"Type": "AuthenticationFailed", "Severity": "Error", "Date": nowStamp()})
	}
	m.activity = append(m.activity, map[string]any{"Name": "Ana is playing Dune",
		"Type": "VideoPlayback", "Severity": "Information", "Date": nowStamp()})

	out, err := GetActivity(context.Background(), 0, 0, true)
	if err != nil {
		t.Fatalf("GetActivity: %v", err)
	}
	if out.FailedLogins != 6 || len(out.Entries) != 6 {
		t.Errorf("only_problems should keep the six failures: %+v", out)
	}
	if !hasWarningContaining(out.Warnings, "guessing passwords") {
		t.Errorf("warnings = %v", out.Warnings)
	}
}
