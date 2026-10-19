package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/go-telegram/bot"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	// Test-only: the bot itself reaches the digest over MCP and imports
	// nothing from the server. This holds its mirror to the server's JSON.
	"github.com/DeLucca990/homelab-mcp/internal/digest"
)

func ptr[T any](v T) *T { return &v }

// fullDigest sets every field, so a tag the bot spells differently from the
// server shows up as a key one side has and the other does not.
func fullDigest() digest.Digest {
	return digest.Digest{
		Hours:              24,
		GeneratedAt:        "2026-10-17T17:00:00-03:00",
		Attention:          []digest.Finding{{Area: "disk", Warning: "/srv/media is 94% full, 52G left"}},
		RebootedSecondsAgo: 5 * 3600,
		Containers:         []digest.ContainerEvent{{Container: "sonarr", Event: "restarted", SecondsAgo: 7200, OOMKilled: true, ExitCode: 137}},
		Imported: []digest.Download{
			{Service: "radarr", Title: "Dune: Part Two (2024)", Quality: "Bluray-2160p", Detail: "upgrade", SecondsAgo: 3600},
			{Service: "sonarr", Title: "Severance", Episode: "S02E04", Quality: "WEBDL-1080p", SecondsAgo: 7200},
			{Service: "sonarr", Title: "Severance", Episode: "S02E05", Quality: "WEBDL-1080p", SecondsAgo: 7300},
		},
		Failed: []digest.Download{{Service: "sonarr", Title: "The Bear", Episode: "S03E01", Quality: "WEBDL-1080p", Detail: "sample <file>", SecondsAgo: 9000}},
		Late: []digest.Release{
			{Service: "sonarr", Title: "Andor", Episode: "S02E09", Due: "2026-10-16 22:00"},
			{Service: "radarr", Title: "Nosferatu (2024)", Kind: "physical", Due: "2026-10-16"},
		},
		Upcoming: []digest.Release{
			{Service: "sonarr", Title: "The Last of Us", Episode: "S02E03", Due: "2026-10-17 22:00"},
			{Service: "radarr", Title: "Wicked (2024)", Kind: "digital", Due: "2026-10-18"},
		},
		Playback:  &digest.Playback{Plays: 2, Watched: []string{"ana is playing Dune"}, FailedLogins: 3, FailedLoginEntries: []string{"Failed login from admin — IP address: 203.0.113.5"}, Problems: []string{"Scan failed"}},
		Truncated: []string{"jellyfin: 300 entries in the window and the newest 200 were read"},
		Errors:    []string{"prowlarr: connection refused"},
		Totals: digest.Totals{
			Disks:                  []digest.Disk{{Mountpoint: "/srv/media", UsedBytes: 900 << 30, TotalBytes: 1000 << 30, UsedPercent: 90}},
			ContainerRestarts:      map[string]int{"sonarr": 4},
			WantedMovieSubtitles:   ptr(4),
			WantedEpisodeSubtitles: ptr(8),
			FailingIndexers:        ptr(1),
		},
		ElapsedMs: 320,
	}
}

func TestDigestMirrorMatchesServer(t *testing.T) {
	raw, _ := json.Marshal(fullDigest())
	var server map[string]any
	json.Unmarshal(raw, &server)

	var mirror serverDigest
	if err := json.Unmarshal(raw, &mirror); err != nil {
		t.Fatal(err)
	}
	back, _ := json.Marshal(mirror)
	var bot map[string]any
	json.Unmarshal(back, &bot)

	// The server leaves empty fields out, so a field one item lacks has to
	// be sent by another: what matters is that every field the bot reads is
	// one the server sends somewhere, and that the values it sends arrive.
	sent := map[string]bool{}
	var collect func(path string, v any)
	collect = func(path string, v any) {
		sent[path] = true
		switch v := v.(type) {
		case map[string]any:
			for k, e := range v {
				collect(path+"."+k, e)
			}
		case []any:
			for _, e := range v {
				collect(path+"[]", e)
			}
		}
	}
	collect("digest", server)

	var check func(path, shape string, bot, server any)
	check = func(path, shape string, bot, server any) {
		switch b := bot.(type) {
		case map[string]any:
			s, _ := server.(map[string]any)
			for k, v := range b {
				if !sent[shape+"."+k] {
					t.Errorf("%s.%s: the bot reads a field the server never sends", path, k)
					continue
				}
				if sv, ok := s[k]; ok {
					check(path+"."+k, shape+"."+k, v, sv)
				}
			}
		case []any:
			s, _ := server.([]any)
			if len(s) != len(b) {
				t.Errorf("%s: %d items read, %d sent", path, len(b), len(s))
				return
			}
			for i := range b {
				check(fmt.Sprintf("%s[%d]", path, i), shape+"[]", b[i], s[i])
			}
		default:
			if fmt.Sprint(bot) != fmt.Sprint(server) {
				t.Errorf("%s: read %v, sent %v", path, bot, server)
			}
		}
	}
	check("digest", "digest", bot, server)
}

func TestUntilDigest(t *testing.T) {
	at := DailyAt{Hour: 17}
	day := func(d, h, m int) time.Time { return time.Date(2026, 10, d, h, m, 0, 0, time.Local) }
	yesterday := day(16, 17, 0)

	for _, tc := range []struct {
		name string
		now  time.Time
		last time.Time
		want time.Duration
	}{
		{"before today's", day(17, 9, 0), yesterday, 8 * time.Hour},
		{"missed today's, back within the day", day(17, 19, 30), yesterday, 0},
		{"missed today's long ago", day(17, 23, 30), yesterday, 17*time.Hour + 30*time.Minute},
		{"already sent today", day(17, 17, 1), day(17, 17, 0), 24*time.Hour - time.Minute},
		{"first start, after the hour", day(17, 18, 0), time.Time{}, 23 * time.Hour},
	} {
		if got := untilDigest(tc.now, at, tc.last); got != tc.want {
			t.Errorf("%s: wait %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestDigestStateKeepsAMonthAndComparesWithYesterday(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "digest.json")
	if s, err := loadDigestState(path); err != nil || len(s.Snapshots) != 0 {
		t.Fatalf("a missing file is an empty state: %+v, %v", s, err)
	}

	start := time.Date(2026, 9, 1, 17, 0, 0, 0, time.Local)
	var s digestState
	for i := range 40 {
		s.record(start.AddDate(0, 0, i), digestTotals{FailingIndexers: ptr(i)})
	}
	if err := s.save(path); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Errorf("state file mode %v", info.Mode().Perm())
	}

	loaded, err := loadDigestState(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Snapshots) != keepSnapshots || *loaded.Snapshots[0].Totals.FailingIndexers != 10 {
		t.Fatalf("kept %d snapshots, the oldest from day %d", len(loaded.Snapshots), *loaded.Snapshots[0].Totals.FailingIndexers)
	}

	last := start.AddDate(0, 0, 39)
	if p := loaded.previous(last.Add(time.Hour)); p == nil || *p.Totals.FailingIndexers != 38 {
		t.Errorf("an hour after the last digest, the comparison is with the one before it: %+v", p)
	}
	if p := loaded.previous(last.Add(24 * time.Hour)); p == nil || *p.Totals.FailingIndexers != 39 {
		t.Errorf("a day after the last digest, the comparison is with it: %+v", p)
	}
}

func TestDaysToFull(t *testing.T) {
	now := time.Date(2026, 10, 17, 17, 0, 0, 0, time.Local)
	const G = 1 << 30
	disk := digestDisk{Mountpoint: "/srv/media", UsedBytes: 900 * G, TotalBytes: 1000 * G}
	state := digestState{Snapshots: []digestSnapshot{
		{At: now.AddDate(0, 0, -30), Totals: digestTotals{Disks: []digestDisk{{Mountpoint: "/srv/media", UsedBytes: 100 * G}}}},
		{At: now.AddDate(0, 0, -5), Totals: digestTotals{Disks: []digestDisk{{Mountpoint: "/srv/media", UsedBytes: 850 * G}}}},
	}}

	// 50G in 5 days is 10G a day, and 100G are left.
	if days, ok := daysToFull(disk, state, now); !ok || days != 10 {
		t.Errorf("got %d days (%v), want 10", days, ok)
	}

	recent := digestState{Snapshots: []digestSnapshot{
		{At: now.Add(-3 * time.Hour), Totals: digestTotals{Disks: []digestDisk{{Mountpoint: "/srv/media", UsedBytes: 800 * G}}}},
	}}
	if _, ok := daysToFull(disk, recent, now); ok {
		t.Error("a trend drawn from three hours of data")
	}

	slow := digestState{Snapshots: []digestSnapshot{
		{At: now.AddDate(0, 0, -7), Totals: digestTotals{Disks: []digestDisk{{Mountpoint: "/srv/media", UsedBytes: 899 * G}}}},
	}}
	if _, ok := daysToFull(disk, slow, now); ok {
		t.Error("a filesystem filling in years was reported")
	}
}

func mirrorOf(t *testing.T, d digest.Digest) serverDigest {
	t.Helper()
	raw, _ := json.Marshal(d)
	var m serverDigest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestRenderDigest(t *testing.T) {
	now := time.Date(2026, 10, 17, 17, 0, 0, 0, time.Local)
	const G = 1 << 30
	state := digestState{Snapshots: []digestSnapshot{{
		At: now.Add(-24 * time.Hour),
		Totals: digestTotals{
			Disks:                  []digestDisk{{Mountpoint: "/srv/media", UsedBytes: 886 * G}},
			WantedMovieSubtitles:   ptr(6),
			WantedEpisodeSubtitles: ptr(9),
			FailingIndexers:        ptr(0),
		},
	}}}

	got := renderDigest(mirrorOf(t, fullDigest()), state, now)
	for _, want := range []string{
		"⚠️ <b>Precisa de atenção</b>\n• disk: /srv/media is 94% full, 52G left",
		"• o servidor reiniciou há 5h",
		"• <code>sonarr</code> reiniciou há 2h — morto por falta de memória",
		"• Dune: Part Two (2024) — Bluray-2160p",
		"• Severance — S02E04, S02E05",
		"• The Bear S03E01 — sample &lt;file&gt;",
		"• Andor S02E09 — saiu ontem 22:00",
		"• Nosferatu (2024) — saiu em mídia física, ontem",
		"• The Last of Us S02E03 — hoje 22:00",
		"• Wicked (2024) — digital, amanhã",
		"🍿 <b>Jellyfin</b>: 2 reproduções",
		"🔐 <b>3 logins falharam no Jellyfin</b>",
		"💬 <b>Legendas</b>: 12 faltando (−3 desde ontem)",
		"🔎 <b>Indexers</b>: 1 com falha (+1 desde ontem)",
		"• <code>/srv/media</code> 90% · +14G desde ontem",
		"• prowlarr: connection refused",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Nada chegou") {
		t.Error("a day with imports was called quiet")
	}
}

func TestRenderQuietDigest(t *testing.T) {
	now := time.Date(2026, 10, 17, 17, 0, 0, 0, time.Local)
	d := digest.Digest{Hours: 24, Totals: digest.Totals{
		Disks:           []digest.Disk{{Mountpoint: "/", UsedBytes: 10 << 30, TotalBytes: 100 << 30, UsedPercent: 10}},
		FailingIndexers: ptr(0),
	}}

	got := renderDigest(mirrorOf(t, d), digestState{}, now)
	want := "✅ Nada precisa de atenção agora\n\n" +
		"<i>Nada chegou, falhou ou reiniciou nas últimas 24h.</i>\n\n" +
		"💾 <b>Disco</b>\n• <code>/</code> 10%"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// --- end to end: the server's tool, Claude for the headline, Telegram ------------

func newDigestServer(t *testing.T, d digest.Digest) string {
	t.Helper()
	server := sdk.NewServer(&sdk.Implementation{Name: "fake", Version: "1"}, nil)
	sdk.AddTool(server, &sdk.Tool{Name: digestTool, Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true}},
		func(context.Context, *sdk.CallToolRequest, struct {
			Hours int `json:"hours"`
		}) (*sdk.CallToolResult, digest.Digest, error) {
			return nil, d, nil
		})
	handler := sdk.NewStreamableHTTPHandler(
		func(*http.Request) *sdk.Server { return server },
		&sdk.StreamableHTTPOptions{Stateless: true, DisableLocalhostProtection: true},
	)
	s := httptest.NewServer(handler)
	t.Cleanup(s.Close)
	return s.URL
}

func newDigestBot(t *testing.T, mcpURL string, claude *fakeClaude) (*Bot, *fakeBotAPI) {
	t.Helper()
	api := newFakeBotAPI(t)
	tg, err := bot.New("123:test", bot.WithServerURL(api.URL), bot.WithSkipGetMe())
	if err != nil {
		t.Fatal(err)
	}
	m := NewMCP(mcpURL, testToken, NewApprovals(time.Second).Elicit)
	t.Cleanup(m.Close)

	b := &Bot{
		cfg: Config{DigestStatePath: filepath.Join(t.TempDir(), "digest.json")},
		mcp: m,
		tg:  tg,
	}
	if claude != nil {
		b.headliner = newHeadliner(defaultDigestModel,
			option.WithBaseURL(claude.URL), option.WithAPIKey("test"), option.WithMaxRetries(0))
	}
	return b, api
}

func TestScheduledDigestSendsAndRecords(t *testing.T) {
	claude := newFakeClaude(t,
		message("end_turn", `[{"type":"thinking","thinking":"","signature":"s"},`+
			`{"type":"text","text":"O disco de mídia\nestá em 94% e o sonarr reiniciou."}]`),
	)
	b, api := newDigestBot(t, newDigestServer(t, fullDigest()), claude)

	b.sendDigest(context.Background(), []int64{42, 7}, true)

	got := api.texts()
	if strings.Count(got, "📋 <b>Resumo diário</b>") != 2 {
		t.Fatalf("not sent to both chats:\n%s", got)
	}
	if !strings.Contains(got, "<i>O disco de mídia está em 94% e o sonarr reiniciou.</i>") {
		t.Errorf("no headline, or it kept its line break:\n%s", got)
	}
	if !strings.Contains(got, "• Severance — S02E04, S02E05") {
		t.Errorf("no body:\n%s", got)
	}
	if !strings.Contains(got, "<i>manchete: ≈ US$ 0,0000 · 2 tokens: 1 entrada, 1 saída</i>") {
		t.Errorf("the headline's cost is not at the foot of the digest:\n%s", got)
	}

	req := claude.requests[0]
	if req["model"] != defaultDigestModel || req["fallbacks"] != nil {
		t.Errorf("headline request: model %v, fallbacks %v", req["model"], req["fallbacks"])
	}
	if oc, _ := req["output_config"].(map[string]any); oc["effort"] != "low" {
		t.Errorf("headline effort: %v", req["output_config"])
	}
	var asked struct {
		Messages []struct {
			Content []struct{ Text string }
		}
	}
	raw, _ := json.Marshal(req)
	json.Unmarshal(raw, &asked)
	if report := asked.Messages[0].Content[0].Text; !strings.Contains(report, "sample <file>") ||
		strings.Contains(report, "&lt;") || strings.Contains(report, "<b>") {
		t.Errorf("the headline was not asked from the report as plain text: %s", report)
	}

	state, err := loadDigestState(b.cfg.DigestStatePath)
	if err != nil {
		t.Fatal(err)
	}
	if state.LastScheduled.IsZero() || len(state.Snapshots) != 1 ||
		state.Snapshots[0].Totals.Disks[0].UsedBytes != 900<<30 {
		t.Errorf("the scheduled digest was not recorded: %+v", state)
	}
}

func TestDigestOnRequestIsNotRecorded(t *testing.T) {
	b, api := newDigestBot(t, newDigestServer(t, fullDigest()), nil)

	b.sendDigest(context.Background(), []int64{42}, false)

	got := api.texts()
	if !strings.Contains(got, "📋 <b>Resumo diário</b>") || strings.Contains(got, "<i>O ") {
		t.Errorf("without Claude the digest goes, headless:\n%s", got)
	}
	if _, err := os.Stat(b.cfg.DigestStatePath); !os.IsNotExist(err) {
		t.Errorf("a /digest wrote the state: %v", err)
	}
}

func TestDigestGoesWithoutAFailedHeadline(t *testing.T) {
	claude := newFakeClaude(t) // nothing scripted: the request fails
	b, api := newDigestBot(t, newDigestServer(t, fullDigest()), claude)

	b.sendDigest(context.Background(), []int64{42}, true)

	got := api.texts()
	if !strings.Contains(got, "• Severance — S02E04, S02E05") {
		t.Errorf("a failed headline held the digest back:\n%s", got)
	}
	if strings.Contains(got, "manchete") {
		t.Errorf("a request that never answered was given a cost:\n%s", got)
	}
}

func TestRefusedHeadlineStillShowsItsCost(t *testing.T) {
	claude := newFakeClaude(t, `{"id":"msg","type":"message","role":"assistant","model":"claude-haiku-5-5",`+
		`"content":[],"stop_reason":"refusal","stop_details":{"type":"refusal","category":"cyber"},`+
		`"stop_sequence":null,"usage":{"input_tokens":1000,"output_tokens":10}}`)
	b, api := newDigestBot(t, newDigestServer(t, fullDigest()), claude)

	b.sendDigest(context.Background(), []int64{42}, false)

	got := api.texts()
	if strings.Contains(got, "<i>O ") || !strings.Contains(got,
		"<i>manchete que falhou: ≈ US$ 0,0001 · 1.010 tokens: 1.000 entrada, 10 saída</i>") {
		t.Errorf("a refused headline should go unwritten but billed:\n%s", got)
	}
}

func TestDigestReportsAServerWithoutTheTool(t *testing.T) {
	url, _ := newCommandServer(t) // a server without homelab_daily_digest
	b, api := newDigestBot(t, url, nil)

	b.sendDigest(context.Background(), []int64{42}, true)

	if got := api.texts(); !strings.Contains(got, "Não consegui montar o resumo diário") ||
		!strings.Contains(got, "precisa ser atualizado") {
		t.Errorf("the failure was not reported to the chat:\n%s", got)
	}
	if state, _ := loadDigestState(b.cfg.DigestStatePath); state.LastScheduled.IsZero() || len(state.Snapshots) != 0 {
		t.Errorf("a failed digest should count as sent, with nothing to compare: %+v", state)
	}
}

func TestConfigDigest(t *testing.T) {
	base := map[string]string{TokenEnv: "x", AllowedUsersEnv: "42, 7", AdminUsersEnv: "42",
		AllowedChatsEnv: "-100", MCPURLEnv: "http://x/mcp", mcpTokenEnv: "t", "XDG_STATE_HOME": "/var/state"}

	setEnv(t, base)
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DigestAt == nil || cfg.DigestAt.String() != "17:00" || fmt.Sprint(cfg.DigestChats) != "[42]" ||
		cfg.DigestModel != "claude-haiku-5-5" || cfg.DigestStatePath != "/var/state/homelab-telegram-bot/digest.json" {
		t.Errorf("defaults: at %v, chats %v, model %s, state %s", cfg.DigestAt, cfg.DigestChats, cfg.DigestModel, cfg.DigestStatePath)
	}

	base[DigestAtEnv], base[DigestChatsEnv], base[DigestStatePathEnv] = "off", "7, -100", "/tmp/d.json"
	setEnv(t, base)
	if cfg, err = LoadConfig(); err != nil || cfg.DigestAt != nil || fmt.Sprint(cfg.DigestChats) != "[7 -100]" || cfg.DigestStatePath != "/tmp/d.json" {
		t.Errorf("explicit: at %v, chats %v, state %s, %v", cfg.DigestAt, cfg.DigestChats, cfg.DigestStatePath, err)
	}

	base[DigestAtEnv], base[DigestChatsEnv] = "5pm", "99, -200"
	setEnv(t, base)
	_, err = LoadConfig()
	for _, want := range []string{"TELEGRAM_DIGEST_AT=\"5pm\"", "lists 99", "lists -200"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q not reported: %v", want, err)
		}
	}
}
