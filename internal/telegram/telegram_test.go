package telegram

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func setEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	for _, k := range []string{TokenEnv, AllowedUsersEnv, AllowedChatsEnv, MCPURLEnv, ModelEnv, EffortEnv,
		ApprovalTimeoutEnv, mcpAddrEnv, mcpTokenEnv, anthropicKeyEnv, anthropicTokenEnv} {
		t.Setenv(k, kv[k])
	}
}

func TestConfigDerivesURLFromServerAddress(t *testing.T) {
	setEnv(t, map[string]string{
		TokenEnv:        "123:abc",
		AllowedUsersEnv: "42, 7",
		mcpAddrEnv:      "100.1.2.3:3000",
		mcpTokenEnv:     "tok",
	})
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MCPURL != "http://100.1.2.3:3000/mcp" {
		t.Errorf("url: %q", cfg.MCPURL)
	}
	if !cfg.AllowedUsers[42] || !cfg.AllowedUsers[7] || len(cfg.AllowedUsers) != 2 {
		t.Errorf("users: %v", cfg.AllowedUsers)
	}
	if cfg.LLM || cfg.Model != defaultModel || cfg.Effort != defaultEffort {
		t.Errorf("defaults: %+v", cfg)
	}
}

func TestConfigReportsEveryProblemAtOnce(t *testing.T) {
	setEnv(t, map[string]string{EffortEnv: "huge", ApprovalTimeoutEnv: "soon"})
	_, err := LoadConfig()
	if err == nil {
		t.Fatal("an empty environment was accepted")
	}
	for _, want := range []string{TokenEnv, AllowedUsersEnv, MCPURLEnv, mcpTokenEnv, EffortEnv, ApprovalTimeoutEnv} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %s:\n%v", want, err)
		}
	}
}

func TestConfigRejectsUsernames(t *testing.T) {
	setEnv(t, map[string]string{TokenEnv: "x", AllowedUsersEnv: "@pedro", MCPURLEnv: "http://x/mcp", mcpTokenEnv: "t"})
	if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "numeric") {
		t.Fatalf("a username was accepted as an id: %v", err)
	}
}

func TestChunkKeepsLinesWhole(t *testing.T) {
	text := strings.Repeat("0123456789\n", 10) // 110 units
	chunks := chunk(text, 25)
	for _, c := range chunks {
		if utf16Len(c) > 25 {
			t.Errorf("chunk over budget: %d", utf16Len(c))
		}
		for _, line := range strings.Split(c, "\n") {
			if line != "0123456789" {
				t.Errorf("a line was cut: %q", line)
			}
		}
	}
	if got := strings.Join(chunks, "\n"); got != strings.TrimRight(text, "\n") {
		t.Errorf("text lost in chunking")
	}
}

func TestChunkCutsLongLinesOnRuneBoundaries(t *testing.T) {
	line := strings.Repeat("é🚀", 20) // 🚀 is two UTF-16 units
	chunks := chunk(line, 7)
	if strings.Join(chunks, "") != line {
		t.Fatal("text lost cutting a long line")
	}
	for _, c := range chunks {
		if utf16Len(c) > 7 {
			t.Errorf("chunk over budget: %q", c)
		}
	}
}

func TestPreEscapesOutput(t *testing.T) {
	if got := pre("a <b> & c"); got != "<pre>a &lt;b&gt; &amp; c</pre>" {
		t.Errorf("got %q", got)
	}
}

func render(s string, budget int) []string { return chunkHTML(parseHTML(s), budget) }

func TestHTMLKeepsSupportedTags(t *testing.T) {
	in := `<b>Disco</b> 5 &lt; 10 & <code class="language-go">x</code> <a href="https://x.y">link</a>`
	want := `<b>Disco</b> 5 &lt; 10 &amp; <code class="language-go">x</code> <a href="https://x.y">link</a>`
	if got := render(in, 3000); len(got) != 1 || got[0] != want {
		t.Errorf("got %q", got)
	}
}

func TestHTMLEscapesUnknownMarkup(t *testing.T) {
	in := "<pre>memória 62%</parameter>\n</invoke> fim"
	got := render(in, 3000)
	want := "<pre>memória 62%&lt;/parameter&gt;\n&lt;/invoke&gt; fim</pre>"
	if len(got) != 1 || got[0] != want {
		t.Errorf("got %q", got)
	}
}

func TestHTMLBalancesTags(t *testing.T) {
	for in, want := range map[string]string{
		"<b>aberta":           "<b>aberta</b>",
		"solta</b> aqui":      "solta aqui",
		"<b><i>cruzada</b>":   "<b><i>cruzada</i></b>",
		"<i>a</i></i>":        "<i>a</i>",
		"x <br> y":            "x &lt;br&gt; y",
		"&nbsp; &amp; &#233;": "&amp;nbsp; &amp; é",
	} {
		if got := render(in, 3000); len(got) != 1 || got[0] != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

func TestHTMLChunksReopenTags(t *testing.T) {
	lines := strings.Repeat("linha da tabela\n", 20)
	got := render("<b>Memória</b>\n<pre>"+lines+"</pre>\nfim", 60)
	if len(got) < 3 {
		t.Fatalf("expected several chunks, got %d", len(got))
	}
	for i, c := range got {
		if utf16Len(c) > 60 {
			t.Errorf("chunk %d over budget (%d): %q", i, utf16Len(c), c)
		}
		if strings.HasPrefix(c, "</") {
			t.Errorf("chunk %d starts with a closing tag: %q", i, c)
		}
		if strings.Count(c, "<pre>") != strings.Count(c, "</pre>") {
			t.Errorf("chunk %d is unbalanced: %q", i, c)
		}
	}
	if joined := plainText(parseHTML(strings.Join(got, ""))); strings.Count(joined, "linha da tabela") != 20 {
		t.Errorf("text lost: %q", joined)
	}
}

func TestHTMLCutsLongLines(t *testing.T) {
	got := render("<i>"+strings.Repeat("a&b", 40)+"</i>", 30)
	var all string
	for _, c := range got {
		if !strings.HasPrefix(c, "<i>") || !strings.HasSuffix(c, "</i>") {
			t.Errorf("chunk not wrapped: %q", c)
		}
		all += plainText(parseHTML(c))
	}
	if all != strings.Repeat("a&b", 40) {
		t.Errorf("text changed: %q", all)
	}
}

func TestParseCommand(t *testing.T) {
	for in, want := range map[string][2]string{
		"/status":             {"status", ""},
		"/Status@homelab_bot": {"status", "homelab_bot"},
		"/queue now":          {"queue", ""},
		"is the disk full?":   {"", ""},
		"  /help":             {"help", ""},
	} {
		if name, target := parseCommand(in); name != want[0] || target != want[1] {
			t.Errorf("parseCommand(%q) = %q, %q, want %q, %q", in, name, target, want[0], want[1])
		}
	}
}

func TestAddressed(t *testing.T) {
	const botID = 500
	group := models.Chat{ID: -100123, Type: models.ChatTypeSupergroup}
	fromBot := &models.Message{From: &models.User{ID: botID}}
	fromFriend := &models.Message{From: &models.User{ID: 7}}

	for _, tc := range []struct {
		name  string
		msg   models.Message
		text  string
		match bool
	}{
		{"private is always addressed", models.Message{Chat: models.Chat{Type: models.ChatTypePrivate}, Text: "oi"}, "oi", true},
		{"plain group chatter", models.Message{Chat: group, Text: "bora jogar?"}, "", false},
		{"bare command", models.Message{Chat: group, Text: "/status"}, "/status", true},
		{"command for this bot", models.Message{Chat: group, Text: "/status@Homelab_Bot"}, "/status@Homelab_Bot", true},
		{"command for another bot", models.Message{Chat: group, Text: "/status@other_bot"}, "/status@other_bot", false},
		{"mention, removed from the text", models.Message{Chat: group, Text: "@homelab_bot como está o disco?"}, "como está o disco?", true},
		{"mention mid-sentence", models.Message{Chat: group, Text: "ei @HOMELAB_BOT, reinicia o jellyfin"}, "ei , reinicia o jellyfin", true},
		{"reply to the bot", models.Message{Chat: group, Text: "e a memória?", ReplyToMessage: fromBot}, "e a memória?", true},
		{"reply to someone else", models.Message{Chat: group, Text: "concordo", ReplyToMessage: fromFriend}, "", false},
	} {
		text, ok := addressed(&tc.msg, "homelab_bot", botID)
		if ok != tc.match || (ok && text != tc.text) {
			t.Errorf("%s: got %q, %v; want %q, %v", tc.name, text, ok, tc.text, tc.match)
		}
	}
}

func TestSpeaker(t *testing.T) {
	if got := speaker(&models.Message{From: &models.User{FirstName: "Pedro", LastName: "L"}}); got != "Pedro L" {
		t.Errorf("got %q", got)
	}
	if got := speaker(&models.Message{From: &models.User{Username: "amigo"}}); got != "amigo" {
		t.Errorf("got %q", got)
	}
}

func TestConfigGroupIDs(t *testing.T) {
	base := map[string]string{TokenEnv: "x", AllowedUsersEnv: "42", MCPURLEnv: "http://x/mcp", mcpTokenEnv: "t"}

	base[AllowedChatsEnv] = "-1001234, -55"
	setEnv(t, base)
	cfg, err := LoadConfig()
	if err != nil || !cfg.AllowedChats[-1001234] || !cfg.AllowedChats[-55] {
		t.Fatalf("groups: %v, %v", cfg.AllowedChats, err)
	}

	base[AllowedChatsEnv] = "42"
	setEnv(t, base)
	if _, err := LoadConfig(); err == nil {
		t.Fatal("a user id was accepted as a group id")
	}
}

type fakeTelegram struct {
	mu      sync.Mutex
	sent    []string
	edits   []string
	photos  []string // URLs sent as photos
	noPhoto bool     // SendPhoto fails, as for a cover Telegram cannot fetch

	approvals *Approvals
	press     string // "y", "n" or "" for no answer
	asUser    int64
}

func (f *fakeTelegram) SendMessage(_ context.Context, p *bot.SendMessageParams) (*models.Message, error) {
	f.mu.Lock()
	f.sent = append(f.sent, p.Text)
	f.mu.Unlock()

	if kb, ok := p.ReplyMarkup.(*models.InlineKeyboardMarkup); ok && f.press != "" {
		for _, btn := range kb.InlineKeyboard[0] {
			if strings.HasSuffix(btn.CallbackData, ":"+f.press) {
				data := btn.CallbackData
				go f.approvals.Answer(f.asUser, data)
			}
		}
	}
	return &models.Message{ID: len(f.sent)}, nil
}

func (f *fakeTelegram) SendPhoto(ctx context.Context, p *bot.SendPhotoParams) (*models.Message, error) {
	if f.noPhoto {
		return nil, errors.New("Bad Request: wrong file identifier/HTTP URL specified")
	}
	f.mu.Lock()
	f.photos = append(f.photos, p.Photo.(*models.InputFileString).Data)
	f.mu.Unlock()
	return f.SendMessage(ctx, &bot.SendMessageParams{Text: p.Caption, ReplyMarkup: p.ReplyMarkup})
}

func (f *fakeTelegram) EditMessageCaption(_ context.Context, p *bot.EditMessageCaptionParams) (*models.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.edits = append(f.edits, "caption:"+p.Caption)
	return &models.Message{ID: p.MessageID}, nil
}

func (f *fakeTelegram) EditMessageText(_ context.Context, p *bot.EditMessageTextParams) (*models.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.edits = append(f.edits, p.Text)
	return &models.Message{ID: p.MessageID}, nil
}

func (f *fakeTelegram) lastEdit() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.edits) == 0 {
		return ""
	}
	return f.edits[len(f.edits)-1]
}

func newApprovalHarness(press string, asUser int64, timeout time.Duration) (*Approvals, *fakeTelegram) {
	a := NewApprovals(timeout)
	f := &fakeTelegram{approvals: a, press: press, asUser: asUser}
	a.SetMessenger(f)
	return a, f
}

func elicit(a *Approvals, ctx context.Context) string {
	res, err := a.Elicit(ctx, &sdk.ElicitRequest{Params: &sdk.ElicitParams{Message: "Restart <jellyfin>?"}})
	if err != nil {
		return "error: " + err.Error()
	}
	return res.Action
}

func TestApprovalAccept(t *testing.T) {
	a, f := newApprovalHarness("y", 42, time.Second)
	if got := elicit(a, withRequester(context.Background(), 1, 42)); got != "accept" {
		t.Fatalf("got %s", got)
	}
	if !strings.Contains(f.sent[0], "Restart &lt;jellyfin&gt;?") {
		t.Errorf("question not shown, or not escaped: %q", f.sent[0])
	}
	if !strings.Contains(f.lastEdit(), "Aprovado") {
		t.Errorf("question not updated with the outcome: %q", f.lastEdit())
	}
}

func TestApprovalDecline(t *testing.T) {
	a, _ := newApprovalHarness("n", 42, time.Second)
	if got := elicit(a, withRequester(context.Background(), 1, 42)); got != "decline" {
		t.Fatalf("got %s", got)
	}
}

func TestApprovalTimesOut(t *testing.T) {
	a, f := newApprovalHarness("", 42, 20*time.Millisecond)
	if got := elicit(a, withRequester(context.Background(), 1, 42)); got != "cancel" {
		t.Fatalf("got %s", got)
	}
	if !strings.Contains(f.lastEdit(), "Expirou") {
		t.Errorf("timeout not shown: %q", f.lastEdit())
	}
}

func TestApprovalIgnoresAnotherUser(t *testing.T) {
	a, _ := newApprovalHarness("y", 99, 50*time.Millisecond)
	if got := elicit(a, withRequester(context.Background(), 1, 42)); got != "cancel" {
		t.Fatalf("another user's press decided it: %s", got)
	}
}

func TestApprovalWithoutRequesterIsCancelled(t *testing.T) {
	a, f := newApprovalHarness("y", 42, time.Second)
	if got := elicit(a, context.Background()); got != "cancel" {
		t.Fatalf("got %s", got)
	}
	if len(f.sent) != 0 {
		t.Error("a question was sent with no chat to send it to")
	}
}

func TestApprovalAnswerOnlyOnce(t *testing.T) {
	a := NewApprovals(time.Second)
	a.pending["abc"] = &pendingApproval{userID: 42, decision: make(chan string, 1)}

	if got := a.Answer(42, "ap:abc:y"); got != "Aprovado." {
		t.Fatalf("first press: %s", got)
	}
	if got := a.Answer(42, "ap:abc:y"); !strings.Contains(got, "expirou") {
		t.Fatalf("second press was accepted: %s", got)
	}
	if got := a.Answer(42, "ap:abc:maybe"); got != "Botão inválido." {
		t.Fatalf("malformed data: %s", got)
	}
}

// Verbatim from internal/mcp/tool_radarr_add.go.
const radarrAdd = "Add this movie to Radarr?\n\n" +
	"    Inglourious Basterds (2009)   [tmdb 16869]\n" +
	"    cover: https://image.tmdb.org/t/p/original/poster.jpg\n" +
	"    quality profile: HD-1080p\n" +
	"    root folder:     /movies\n" +
	"    monitored:       yes\n" +
	"    search now:      yes\n" +
	"    minimum availability: released\n" +
	"\nRadarr will start looking for a release straight away, " +
	"and whatever it finds will be downloaded onto that folder."

func TestFormatApprovalRadarrAdd(t *testing.T) {
	got := formatApproval(radarrAdd)
	want := "🔐 <b>Add this movie to Radarr?</b>\n\n" +
		"<b>Inglourious Basterds (2009)</b> · tmdb 16869\n" +
		"• <b>Quality profile:</b> HD-1080p\n" +
		"• <b>Root folder:</b> <code>/movies</code>\n" +
		"• <b>Monitored:</b> yes\n" +
		"• <b>Search now:</b> yes\n" +
		"• <b>Minimum availability:</b> released\n\n" +
		"Radarr will start looking for a release straight away, and whatever it finds will be downloaded onto that folder."
	if got.html != want {
		t.Errorf("got:\n%s\nwant:\n%s", got.html, want)
	}
	if got.poster != "https://image.tmdb.org/t/p/original/poster.jpg" {
		t.Errorf("poster: %q", got.poster)
	}
}

func TestFormatApprovalCommandStaysVerbatim(t *testing.T) {
	got := formatApproval("Run this command inside the container \"sonarr\"?\n\n    sh -c rm -rf /config/<x>\n\nIt runs as the container's default user and may change its state.")
	if !strings.Contains(got.html, "<code>sh -c rm -rf /config/&lt;x&gt;</code>") {
		t.Errorf("command not verbatim: %s", got.html)
	}
	if got.poster != "" {
		t.Errorf("poster: %q", got.poster)
	}
}

func TestFormatApprovalOtherShapes(t *testing.T) {
	for msg, want := range map[string]string{
		"Show this on Ana's screen?\n\n    \"jantar & filme\"\n":                                                          `<code>"jantar &amp; filme"</code>`,
		"Remove this download from the Radarr queue?\n\n    Dune (2021)\n    progress: 40% (2 GB still to come)\n":        "<b>Dune (2021)</b>\n• <b>Progress:</b> 40% (2 GB still to come)",
		"Search for it?\n\n    Dune   [movie 3]\n    (you gave 438631, which is its TMDB id — Radarr's id for it is 3)\n": "<i>(you gave 438631",
		"Add it?\n\n    Dune: Part Two (2024)   [tmdb 693134]\n":                                                          "<b>Dune: Part Two (2024)</b> · tmdb 693134",
	} {
		if got := formatApproval(msg).html; !strings.Contains(got, want) {
			t.Errorf("%q:\ngot  %s\nwant %s", msg, got, want)
		}
		if got := chunkHTML(parseHTML(formatApproval(msg).html), messageLimit); len(got) != 1 {
			t.Errorf("%q does not survive the sanitiser as one message: %q", msg, got)
		}
	}
}

func TestApprovalSendsPoster(t *testing.T) {
	a, f := newApprovalHarness("y", 42, time.Second)
	res, _ := a.Elicit(withRequester(context.Background(), 1, 42),
		&sdk.ElicitRequest{Params: &sdk.ElicitParams{Message: radarrAdd}})
	if res.Action != "accept" {
		t.Fatalf("got %s", res.Action)
	}
	if len(f.photos) != 1 || !strings.HasSuffix(f.photos[0], "poster.jpg") {
		t.Fatalf("poster not sent: %v", f.photos)
	}
	if strings.Contains(f.sent[0], "cover:") {
		t.Errorf("the cover link is still in the caption: %s", f.sent[0])
	}
	if e := f.lastEdit(); !strings.HasPrefix(e, "caption:") || !strings.Contains(e, "Aprovado") {
		t.Errorf("the caption was not updated with the outcome: %q", e)
	}
}

func TestApprovalFallsBackToTextWithoutPoster(t *testing.T) {
	a, f := newApprovalHarness("y", 42, time.Second)
	f.noPhoto = true
	res, _ := a.Elicit(withRequester(context.Background(), 1, 42),
		&sdk.ElicitRequest{Params: &sdk.ElicitParams{Message: radarrAdd}})
	if res.Action != "accept" || len(f.sent) != 1 {
		t.Fatalf("got %s after %d messages", res.Action, len(f.sent))
	}
	if e := f.lastEdit(); strings.HasPrefix(e, "caption:") || !strings.Contains(e, "Aprovado") {
		t.Errorf("text question not updated as text: %q", e)
	}
}
