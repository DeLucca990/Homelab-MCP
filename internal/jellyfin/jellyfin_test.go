package jellyfin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestNormalizeBaseURL(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		// The documented case: a bare http host means a direct Jellyfin, and
		// Jellyfin's port is 8096 rather than Radarr's 7878 or Sonarr's 8989.
		{"http://localhost", "http://localhost:8096"},
		{"localhost", "http://localhost:8096"},
		{"http://10.0.0.4/", "http://10.0.0.4:8096"},

		// Anything that reaches Jellyfin through something else is left alone.
		{"http://localhost:8920", "http://localhost:8920"},
		{"https://jellyfin.example.com", "https://jellyfin.example.com"},
		{"http://nas/jellyfin", "http://nas/jellyfin"},
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

func TestTicksToSeconds(t *testing.T) {
	cases := map[int64]uint64{
		0:              0,
		-1:             0,
		10_000_000:     1,
		72_000_000_000: 7200, // a two-hour film
		45_000_000:     4,    // truncates rather than rounds
	}
	for in, want := range cases {
		if got := ticksToSeconds(in); got != want {
			t.Errorf("ticksToSeconds(%d) = %d, want %d", in, got, want)
		}
	}
}

// Jellyfin writes some timestamps with a zone and some without. Read as local
// time, a zoneless one would land in the future and report an age of zero.
func TestSecondsSinceAcceptsBothLayouts(t *testing.T) {
	ago := time.Now().UTC().Add(-2 * time.Hour)

	for _, stamp := range []string{
		ago.Format(time.RFC3339),
		ago.Format("2006-01-02T15:04:05.0000000Z"),
		ago.Format("2006-01-02T15:04:05.0000000"), // no zone at all
		ago.Format("2006-01-02T15:04:05"),
	} {
		got := secondsSince(stamp)
		if got < 7100 || got > 7300 {
			t.Errorf("secondsSince(%q) = %d, want about 7200", stamp, got)
		}
	}

	for _, empty := range []string{"", "   ", "0001-01-01T00:00:00Z", "not a date"} {
		if got := secondsSince(empty); got != 0 {
			t.Errorf("secondsSince(%q) = %d, want 0", empty, got)
		}
	}
}

// --- the mock ---------------------------------------------------------------

// mockJellyfin serves canned responses and records what was asked of it.
type mockJellyfin struct {
	*httptest.Server

	sessions []map[string]any
	info     map[string]any
	storage  map[string]any
	tasks    []map[string]any
	plugins  []map[string]any
	encoding map[string]any

	// forbid names paths that answer 403, standing in for an API key that is
	// not an administrator key.
	forbid map[string]bool

	gotAuth  string
	gotQuery map[string]string
}

func newMockJellyfin(t *testing.T) *mockJellyfin {
	t.Helper()

	m := &mockJellyfin{
		info:     map[string]any{},
		storage:  map[string]any{},
		encoding: map[string]any{},
		forbid:   map[string]bool{},
		gotQuery: map[string]string{},
	}

	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.gotAuth = r.Header.Get("Authorization")
		for k := range r.URL.Query() {
			m.gotQuery[k] = r.URL.Query().Get(k)
		}

		if m.forbid[r.URL.Path] {
			w.WriteHeader(http.StatusForbidden)
			return
		}

		var body any
		switch r.URL.Path {
		case "/Sessions":
			body = m.sessions
		case "/System/Info":
			body = m.info
		case "/System/Info/Storage":
			body = m.storage
		case "/ScheduledTasks":
			body = m.tasks
		case "/Plugins":
			body = m.plugins
		case "/System/Configuration/encoding":
			body = m.encoding
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}))

	t.Cleanup(m.Close)

	t.Setenv(BaseURLEnv, m.URL)
	t.Setenv(APIKeyEnv, "test-key")

	return m
}

// The one thing this module cannot copy from the *arr clients: Jellyfin reads
// neither X-Api-Key nor the deprecated X-Emby-Token, so a key sent that way is
// an unauthenticated request that happens to carry a header.
func TestAuthorizationHeaderIsJellyfinsScheme(t *testing.T) {
	m := newMockJellyfin(t)

	if _, err := GetSessions(context.Background(), SessionOptions{}); err != nil {
		t.Fatalf("GetSessions: %v", err)
	}

	if !strings.HasPrefix(m.gotAuth, "MediaBrowser ") {
		t.Errorf("Authorization = %q, want the MediaBrowser scheme", m.gotAuth)
	}
	if !strings.Contains(m.gotAuth, `Token="test-key"`) {
		t.Errorf("Authorization = %q, want it to carry Token=\"test-key\"", m.gotAuth)
	}
}

func TestSessionsAsksForTheActivityWindow(t *testing.T) {
	m := newMockJellyfin(t)

	if _, err := GetSessions(context.Background(), SessionOptions{}); err != nil {
		t.Fatalf("GetSessions: %v", err)
	}

	if got := m.gotQuery["activeWithinSeconds"]; got != "900" {
		t.Errorf("activeWithinSeconds = %q, want 900", got)
	}
}

// --- what a stream costs ----------------------------------------------------

func nowStamp() string { return time.Now().UTC().Format(time.RFC3339) }

func playing(user, method string, transcoding map[string]any) map[string]any {
	s := map[string]any{
		"Id":                  "sess-" + user,
		"UserName":            user,
		"Client":              "Jellyfin Web",
		"DeviceName":          user + "-laptop",
		"LastActivityDate":    nowStamp(),
		"LastPlaybackCheckIn": nowStamp(),
		"NowPlayingItem": map[string]any{
			"Name":           "Dune",
			"Type":           "Movie",
			"Container":      "mkv",
			"RunTimeTicks":   72_000_000_000,
			"ProductionYear": 2021,
		},
		"PlayState": map[string]any{
			"PositionTicks": 36_000_000_000,
			"IsPaused":      false,
			"PlayMethod":    method,
		},
	}
	if transcoding != nil {
		s["TranscodingInfo"] = transcoding
	}
	return s
}

// The judgement this module exists for: Jellyfin's own "Transcode" label covers
// a remux costing nothing and a re-encode saturating a core, and the field that
// separates them is IsVideoDirect — not PlayMethod.
func TestWorkSeparatesRemuxFromReEncode(t *testing.T) {
	m := newMockJellyfin(t)
	m.sessions = []map[string]any{
		playing("direct", "DirectPlay", nil),

		// PlayMethod says Transcode, but the video stream is passing through:
		// only the container or the audio is being rewritten.
		playing("remux", "Transcode", map[string]any{
			"IsVideoDirect":            true,
			"IsAudioDirect":            false,
			"Bitrate":                  8_000_000,
			"HardwareAccelerationType": "none",
			"TranscodeReasons":         []string{"AudioCodecNotSupported"},
		}),

		playing("gpu", "Transcode", map[string]any{
			"IsVideoDirect":            false,
			"Bitrate":                  12_000_000,
			"HardwareAccelerationType": "qsv",
			"TranscodeReasons":         []string{"VideoCodecNotSupported"},
		}),

		playing("cpu", "Transcode", map[string]any{
			"IsVideoDirect":            false,
			"Bitrate":                  20_000_000,
			"HardwareAccelerationType": "none",
			"TranscodeReasons":         []string{"VideoCodecNotSupported"},
		}),
	}

	s, err := GetSessions(context.Background(), SessionOptions{})
	if err != nil {
		t.Fatalf("GetSessions: %v", err)
	}

	want := map[string]string{
		"direct": WorkDirect,
		"remux":  WorkRemux,
		"gpu":    WorkHardware,
		"cpu":    WorkSoftware,
	}
	for _, i := range s.Items {
		if got := i.Work; got != want[i.User] {
			t.Errorf("%s: work = %q, want %q", i.User, got, want[i.User])
		}
	}

	if s.DirectCount != 1 || s.RemuxCount != 1 || s.HardwareCount != 1 || s.SoftwareCount != 1 {
		t.Errorf("counts = direct %d, remux %d, hw %d, sw %d; want one of each",
			s.DirectCount, s.RemuxCount, s.HardwareCount, s.SoftwareCount)
	}
	if s.PlayingCount != 4 {
		t.Errorf("playing_count = %d, want 4", s.PlayingCount)
	}

	// Worst first: the CPU re-encode is what someone is looking for.
	if s.Items[0].User != "cpu" {
		t.Errorf("first row is %q, want the software transcode first", s.Items[0].User)
	}

	// "none" is how Jellyfin spells no acceleration; reporting it as a backend
	// name would read as one.
	for _, i := range s.Items {
		if i.User == "cpu" && i.HardwareAccel != "" {
			t.Errorf("cpu session reports hardware_acceleration %q, want empty", i.HardwareAccel)
		}
		if i.User == "gpu" && i.HardwareAccel != "qsv" {
			t.Errorf("gpu session reports hardware_acceleration %q, want qsv", i.HardwareAccel)
		}
	}
}

func TestIdleSessionsAreHiddenUnlessAskedFor(t *testing.T) {
	m := newMockJellyfin(t)
	m.sessions = []map[string]any{
		{"Id": "idle-1", "UserName": "someone", "LastActivityDate": nowStamp()},
		playing("watcher", "DirectPlay", nil),
	}

	s, err := GetSessions(context.Background(), SessionOptions{})
	if err != nil {
		t.Fatalf("GetSessions: %v", err)
	}
	if len(s.Items) != 1 || s.Items[0].User != "watcher" {
		t.Errorf("default listing = %d items, want only the one that is playing", len(s.Items))
	}
	// Counted even though it is not listed, so the summary line can say so.
	if s.IdleCount != 1 {
		t.Errorf("idle_count = %d, want 1", s.IdleCount)
	}

	s, err = GetSessions(context.Background(), SessionOptions{IncludeIdle: true})
	if err != nil {
		t.Fatalf("GetSessions: %v", err)
	}
	if len(s.Items) != 2 {
		t.Errorf("include_idle listing = %d items, want 2", len(s.Items))
	}
}

// A session that stopped reporting progress but is still "playing" is a ghost:
// the viewer is gone and the transcode is still running.
func TestStaleSessionIsFlaggedAndSortedFirst(t *testing.T) {
	m := newMockJellyfin(t)

	ghost := playing("ghost", "Transcode", map[string]any{
		"IsVideoDirect":            false,
		"HardwareAccelerationType": "qsv",
	})
	ghost["LastPlaybackCheckIn"] = time.Now().UTC().Add(-30 * time.Minute).Format(time.RFC3339)

	m.sessions = []map[string]any{
		playing("cpu", "Transcode", map[string]any{
			"IsVideoDirect":            false,
			"HardwareAccelerationType": "none",
		}),
		ghost,
	}

	s, err := GetSessions(context.Background(), SessionOptions{})
	if err != nil {
		t.Fatalf("GetSessions: %v", err)
	}

	if s.Items[0].User != "ghost" || !s.Items[0].Stale {
		t.Fatalf("first row = %+v, want the stale session first and flagged", s.Items[0])
	}
	if !hasWarningContaining(s.Warnings, "has not reported playback progress") {
		t.Errorf("warnings = %v, want one about the stale session", s.Warnings)
	}

	// A paused session is not a ghost: someone is there and stopped it.
	paused := playing("paused", "DirectPlay", nil)
	paused["LastPlaybackCheckIn"] = time.Now().UTC().Add(-30 * time.Minute).Format(time.RFC3339)
	paused["PlayState"].(map[string]any)["IsPaused"] = true
	m.sessions = []map[string]any{paused}

	s, err = GetSessions(context.Background(), SessionOptions{})
	if err != nil {
		t.Fatalf("GetSessions: %v", err)
	}
	if s.Items[0].Stale {
		t.Error("a paused session was reported as stale")
	}
}

func TestSessionWarnings(t *testing.T) {
	m := newMockJellyfin(t)
	m.sessions = []map[string]any{
		playing("burner", "Transcode", map[string]any{
			"IsVideoDirect":            false,
			"HardwareAccelerationType": "none",
			"TranscodeReasons":         []string{"SubtitleCodecNotSupported"},
		}),
		playing("second", "Transcode", map[string]any{
			"IsVideoDirect":            false,
			"HardwareAccelerationType": "qsv",
			"TranscodeReasons":         []string{"VideoCodecNotSupported"},
		}),
	}

	s, err := GetSessions(context.Background(), SessionOptions{})
	if err != nil {
		t.Fatalf("GetSessions: %v", err)
	}

	for _, want := range []string{
		"software transcode", // the CPU one
		"burn subtitles",     // the avoidable cause
		"re-encoded at once", // two concurrent transcodes
	} {
		if !hasWarningContaining(s.Warnings, want) {
			t.Errorf("warnings = %v, want one containing %q", s.Warnings, want)
		}
	}

	// The reasons ride along, because "it is transcoding" leaves nothing to act on.
	if !slices.Contains(s.Items[0].TranscodeReasons, "SubtitleCodecNotSupported") {
		t.Errorf("transcode reasons were dropped: %+v", s.Items[0])
	}
}

// --- health -----------------------------------------------------------------

func (m *mockJellyfin) withHealthDefaults() *mockJellyfin {
	m.info = map[string]any{
		"ServerName":                 "media",
		"Version":                    "10.10.3",
		"OperatingSystemDisplayName": "Debian GNU/Linux 12",
		"HasPendingRestart":          false,
	}
	m.encoding = map[string]any{
		"HardwareAccelerationType": "qsv",
		"EnableHardwareEncoding":   true,
		"HardwareDecodingCodecs":   []string{"h264", "hevc"},
		"EncoderAppPathDisplay":    "/usr/lib/jellyfin-ffmpeg/ffmpeg",
	}
	m.storage = map[string]any{
		"TranscodingTempFolder": map[string]any{
			"Path": "/cache/transcodes", "FreeSpace": 200 << 30, "UsedSpace": 10 << 30,
			"DeviceId": "sda1",
		},
		"CacheFolder": map[string]any{
			"Path": "/cache", "FreeSpace": 200 << 30, "UsedSpace": 10 << 30, "DeviceId": "sda1",
		},
		"Libraries": []map[string]any{{
			"Name": "Movies",
			"Folders": []map[string]any{{
				"Path": "/media/movies", "FreeSpace": 900 << 30, "UsedSpace": 4 << 40,
				"DeviceId": "sdb1",
			}},
		}},
	}
	m.tasks = []map[string]any{{
		"Name": "Scan Media Library", "Key": libraryScanKey, "State": "Idle",
		"LastExecutionResult": map[string]any{
			"Status":       "Completed",
			"StartTimeUtc": time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339),
			"EndTimeUtc":   time.Now().UTC().Add(-2 * time.Hour).Add(time.Minute).Format(time.RFC3339),
		},
	}}
	m.plugins = []map[string]any{
		{"Name": "Playback Reporting", "Version": "13.0.0.0", "Status": "Active"},
	}
	return m
}

func TestHealthHappyPath(t *testing.T) {
	newMockJellyfin(t).withHealthDefaults()

	h, err := GetHealth(context.Background())
	if err != nil {
		t.Fatalf("GetHealth: %v", err)
	}

	if h.Version != "10.10.3" || h.ServerName != "media" {
		t.Errorf("identity = %+v, want the mock's", h)
	}
	if h.HardwareAcceleration != "qsv" || !h.HardwareEncoding {
		t.Errorf("acceleration = %q/%v, want qsv/true", h.HardwareAcceleration, h.HardwareEncoding)
	}

	// The transcode temp is first: it is the one whose filling up breaks
	// playback while every other reading still looks healthy.
	if len(h.Folders) == 0 || h.Folders[0].Name != "transcode temp" {
		t.Fatalf("folders = %+v, want the transcode temp first", h.Folders)
	}
	if !slices.ContainsFunc(h.Folders, func(f FolderSpace) bool { return f.Name == "library: Movies" }) {
		t.Errorf("folders = %+v, want the library folder flattened in", h.Folders)
	}

	// The library scan is kept even though it succeeded, because "when did
	// Jellyfin last look at the disk" has an answer here and nowhere else.
	if len(h.Tasks) != 1 || h.Tasks[0].Key != libraryScanKey {
		t.Errorf("tasks = %+v, want the library scan reported", h.Tasks)
	}
	if h.Tasks[0].LastDuration != 60 {
		t.Errorf("scan duration = %ds, want 60", h.Tasks[0].LastDuration)
	}

	if len(h.Plugins) != 0 {
		t.Errorf("plugins = %+v, want only the ones that are not Active", h.Plugins)
	}
	if h.PluginCount != 1 {
		t.Errorf("plugin_count = %d, want 1", h.PluginCount)
	}
	if len(h.Warnings) != 0 {
		t.Errorf("warnings = %v, want none on a healthy server", h.Warnings)
	}
}

func TestHealthWarnsAboutSoftwareOnlyTranscoding(t *testing.T) {
	m := newMockJellyfin(t).withHealthDefaults()
	m.encoding["HardwareAccelerationType"] = "none"

	h, err := GetHealth(context.Background())
	if err != nil {
		t.Fatalf("GetHealth: %v", err)
	}
	if h.HardwareAcceleration != "" {
		t.Errorf("acceleration = %q, want empty for none", h.HardwareAcceleration)
	}
	if !hasWarningContaining(h.Warnings, "no hardware acceleration is configured") {
		t.Errorf("warnings = %v, want one about software-only transcoding", h.Warnings)
	}

	// Configured for decode but not encode still leaves the encode on the CPU.
	m.encoding["HardwareAccelerationType"] = "vaapi"
	m.encoding["EnableHardwareEncoding"] = false

	h, err = GetHealth(context.Background())
	if err != nil {
		t.Fatalf("GetHealth: %v", err)
	}
	if !hasWarningContaining(h.Warnings, "hardware encoding is off") {
		t.Errorf("warnings = %v, want one about decode-only acceleration", h.Warnings)
	}
}

// The encoding findings are true every second of the server's life, so they are
// marked as standing — repeated inside Warnings so the health tool still reports
// everything, and identifiable so the overview can leave them out of "is
// anything wrong right now".
func TestStandingWarningsAreMarkedButNotWithheld(t *testing.T) {
	m := newMockJellyfin(t).withHealthDefaults()
	m.encoding["HardwareAccelerationType"] = "none"
	m.tasks = []map[string]any{{
		"Name": "Scan Media Library", "Key": libraryScanKey, "State": "Idle",
		"LastExecutionResult": map[string]any{
			"Status": "Failed", "ErrorMessage": "boom",
			"EndTimeUtc": time.Now().UTC().Add(-time.Hour).Format(time.RFC3339),
		},
	}}

	h, err := GetHealth(context.Background())
	if err != nil {
		t.Fatalf("GetHealth: %v", err)
	}

	if len(h.StandingWarnings) != 1 {
		t.Fatalf("standing warnings = %v, want just the acceleration one", h.StandingWarnings)
	}
	if !strings.Contains(h.StandingWarnings[0], "no hardware acceleration") {
		t.Errorf("standing warning = %q, want the acceleration one", h.StandingWarnings[0])
	}

	// Repeated, not moved: the health tool must still report it.
	if !slices.Contains(h.Warnings, h.StandingWarnings[0]) {
		t.Error("the standing warning was held out of Warnings")
	}

	// A failed task is a fault happening now, not a configuration.
	if !hasWarningContaining(h.Warnings, "boom") {
		t.Fatalf("warnings = %v, want the failed task", h.Warnings)
	}
	for _, w := range h.StandingWarnings {
		if strings.Contains(w, "boom") {
			t.Error("a failed task was classed as a standing warning")
		}
	}

	// What the overview is left with once the standing ones are removed.
	current := slices.DeleteFunc(slices.Clone(h.Warnings), func(w string) bool {
		return slices.Contains(h.StandingWarnings, w)
	})
	if len(current) != 1 || !strings.Contains(current[0], "boom") {
		t.Errorf("current warnings = %v, want only the failed task", current)
	}
}

// Jellyfin reports free space of the device, not of the folder, so one full
// disk holding several of these paths must not produce several warnings.
func TestHealthLowSpaceWarnsOncePerDevice(t *testing.T) {
	m := newMockJellyfin(t).withHealthDefaults()
	m.storage["TranscodingTempFolder"] = map[string]any{
		"Path": "/cache/transcodes", "FreeSpace": 1 << 30, "DeviceId": "sda1",
	}
	m.storage["CacheFolder"] = map[string]any{
		"Path": "/cache", "FreeSpace": 1 << 30, "DeviceId": "sda1",
	}

	h, err := GetHealth(context.Background())
	if err != nil {
		t.Fatalf("GetHealth: %v", err)
	}

	var n int
	for _, w := range h.Warnings {
		if strings.Contains(w, "free") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("got %d low-space warnings for one device, want 1: %v", n, h.Warnings)
	}
}

func TestHealthTaskWarnings(t *testing.T) {
	m := newMockJellyfin(t).withHealthDefaults()
	m.tasks = []map[string]any{
		{
			"Name": "Scan Media Library", "Key": libraryScanKey, "State": "Idle",
			"LastExecutionResult": map[string]any{
				"Status":       "Failed",
				"ErrorMessage": "Access to the path '/media/movies' is denied",
				"EndTimeUtc":   time.Now().UTC().Add(-time.Hour).Format(time.RFC3339),
			},
		},
		{"Name": "Extract Chapter Images", "Key": "ChapterImages", "State": "Running",
			"CurrentProgressPercentage": 42.5},
		{"Name": "Clean Cache", "Key": "CleanCache", "State": "Idle",
			"LastExecutionResult": map[string]any{"Status": "Completed"}},
	}

	h, err := GetHealth(context.Background())
	if err != nil {
		t.Fatalf("GetHealth: %v", err)
	}

	if h.TaskCount != 3 {
		t.Errorf("task_count = %d, want 3", h.TaskCount)
	}
	if h.RunningTaskCount != 1 || h.FailedTaskCount != 1 {
		t.Errorf("running/failed = %d/%d, want 1/1", h.RunningTaskCount, h.FailedTaskCount)
	}
	// A task that ran cleanly and is idle is noise.
	if len(h.Tasks) != 2 {
		t.Errorf("tasks = %+v, want only the failed one and the running one", h.Tasks)
	}
	// Worst first.
	if h.Tasks[0].LastStatus != "Failed" {
		t.Errorf("first task = %+v, want the failed one", h.Tasks[0])
	}
	if !hasWarningContaining(h.Warnings, "Access to the path") {
		t.Errorf("warnings = %v, want Jellyfin's own error message carried through", h.Warnings)
	}
}

func TestHealthWarnsWhenTheLibraryWasNeverScanned(t *testing.T) {
	m := newMockJellyfin(t).withHealthDefaults()
	m.tasks = []map[string]any{
		{"Name": "Scan Media Library", "Key": libraryScanKey, "State": "Idle"},
	}

	h, err := GetHealth(context.Background())
	if err != nil {
		t.Fatalf("GetHealth: %v", err)
	}
	if !hasWarningContaining(h.Warnings, "never run") {
		t.Errorf("warnings = %v, want one about a library that was never scanned", h.Warnings)
	}
}

func TestHealthWarnsAboutPluginsThatAreNotRunning(t *testing.T) {
	m := newMockJellyfin(t).withHealthDefaults()
	m.plugins = []map[string]any{
		{"Name": "Playback Reporting", "Version": "13.0.0.0", "Status": "Active"},
		{"Name": "Trakt", "Version": "12.0.0.0", "Status": "Malfunctioned"},
		{"Name": "Intro Skipper", "Version": "1.0.0.0", "Status": "NotSupported"},
	}

	h, err := GetHealth(context.Background())
	if err != nil {
		t.Fatalf("GetHealth: %v", err)
	}
	if len(h.Plugins) != 2 {
		t.Errorf("plugins = %+v, want only the two that are not Active", h.Plugins)
	}
	if !hasWarningContaining(h.Warnings, "Trakt plugin has malfunctioned") {
		t.Errorf("warnings = %v, want one naming the malfunctioning plugin", h.Warnings)
	}
	if !hasWarningContaining(h.Warnings, "does not support this Jellyfin version") {
		t.Errorf("warnings = %v, want one about the unsupported plugin", h.Warnings)
	}
}

// A key that is not an administrator key fails four of the five reads. Each
// failure becomes a warning; the call still answers with what it could read.
func TestHealthDegradesWithoutAdminRights(t *testing.T) {
	m := newMockJellyfin(t).withHealthDefaults()
	m.forbid["/System/Info/Storage"] = true
	m.forbid["/ScheduledTasks"] = true
	m.forbid["/Plugins"] = true

	h, err := GetHealth(context.Background())
	if err != nil {
		t.Fatalf("GetHealth should degrade rather than fail: %v", err)
	}

	if h.Version != "10.10.3" {
		t.Errorf("version = %q, want the section that was readable to survive", h.Version)
	}
	if len(h.Folders) != 0 || len(h.Tasks) != 0 {
		t.Error("sections that were forbidden came back with data")
	}

	var n int
	for _, w := range h.Warnings {
		if strings.Contains(w, "not an administrator key") {
			n++
		}
	}
	if n != 3 {
		t.Errorf("got %d admin-rights warnings, want one per forbidden read: %v", n, h.Warnings)
	}
}

// Everything failing is a connection problem, not a health report.
func TestHealthFailsWhenNothingAnswers(t *testing.T) {
	m := newMockJellyfin(t)
	m.Close()

	if _, err := GetHealth(context.Background()); err == nil {
		t.Fatal("GetHealth should have failed against a closed server")
	}
}

func TestNotConfigured(t *testing.T) {
	t.Setenv(BaseURLEnv, "")
	t.Setenv(APIKeyEnv, "")

	if Configured() {
		t.Error("Configured() is true with neither variable set")
	}

	t.Setenv(BaseURLEnv, "http://localhost")
	if Configured() {
		t.Error("Configured() is true with no API key")
	}
}

func hasWarningContaining(warnings []string, substr string) bool {
	return slices.ContainsFunc(warnings, func(w string) bool {
		return strings.Contains(w, substr)
	})
}
