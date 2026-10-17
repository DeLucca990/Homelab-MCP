package telegram

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-telegram/bot"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestCommandArgs(t *testing.T) {
	for _, tc := range []struct {
		args func(string) (map[string]any, error)
		rest string
		want string // fmt of the arguments, or "error"
	}{
		{logsArgs, "jellyfin", "map[container:jellyfin]"},
		{logsArgs, "jellyfin 50", "map[container:jellyfin tail:50]"},
		{logsArgs, "", "error"},
		{logsArgs, "jellyfin lots", "error"},
		{logsArgs, "jellyfin -5", "error"},
		{logsArgs, "jellyfin sonarr 50", "error"},
		{containerArgs, "sonarr", "map[container:sonarr]"},
		{containerArgs, "", "error"},
		{containerArgs, "sonarr radarr", "error"},
		{termArgs, "Duna 2021", "map[term:Duna 2021]"},
		{termArgs, "", "error"},
	} {
		got, err := tc.args(tc.rest)
		s := fmt.Sprint(got)
		if err != nil {
			s = "error"
		}
		if s != tc.want {
			t.Errorf("%q: got %s (%v), want %s", tc.rest, s, err, tc.want)
		}
	}
}

func TestCommandsHaveValidNames(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range append(toolCommands, otherCommands...) {
		for _, r := range c.name {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
				t.Errorf("/%s: Telegram takes lowercase letters, digits and underscores only", c.name)
			}
		}
		if seen[c.name] {
			t.Errorf("/%s is defined twice", c.name)
		}
		seen[c.name] = true
		if (c.usage == "") != (c.args == nil) {
			t.Errorf("/%s: a usage without arguments, or arguments without a usage", c.name)
		}
		if d := c.menuDescription(); len(d) < 3 || len(d) > 256 {
			t.Errorf("/%s: Telegram takes a description of 3 to 256 characters, got %d", c.name, len(d))
		}
	}
}

// fakeBotAPI answers the Bot API methods the bot sends with, and keeps what it sent.
type fakeBotAPI struct {
	*httptest.Server

	mu   sync.Mutex
	sent []string // message texts, and "file:<name>" for documents
}

func newFakeBotAPI(t *testing.T) *fakeBotAPI {
	t.Helper()
	f := &fakeBotAPI{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseMultipartForm(1 << 20)
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]

		w.Header().Set("Content-Type", "application/json")
		switch method {
		case "sendMessage", "sendDocument":
			f.mu.Lock()
			if method == "sendDocument" {
				f.sent = append(f.sent, "file:"+r.FormValue("caption"))
			} else {
				f.sent = append(f.sent, r.FormValue("text"))
			}
			f.mu.Unlock()
			fmt.Fprint(w, `{"ok":true,"result":{"message_id":1,"date":0,"chat":{"id":1,"type":"private"}}}`)
		default:
			fmt.Fprint(w, `{"ok":true,"result":true}`)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeBotAPI) texts() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.sent, "\n---\n")
}

// newCommandServer registers tools by the names the commands call, echoing the
// arguments each one got. sonarr_series_lookup is left out, as on a server
// without Sonarr.
func newCommandServer(t *testing.T) (url string, restarts *atomic.Int32) {
	t.Helper()
	restarts = &atomic.Int32{}
	server := sdk.NewServer(&sdk.Implementation{Name: "fake", Version: "1"}, nil)

	type args struct {
		Container string `json:"container,omitempty"`
		Tail      int    `json:"tail,omitempty"`
		Term      string `json:"term,omitempty"`
	}
	echo := func(_ context.Context, req *sdk.CallToolRequest, in args) (*sdk.CallToolResult, any, error) {
		text := fmt.Sprintf("%s container=%s tail=%d term=%s", req.Params.Name, in.Container, in.Tail, in.Term)
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: text}}}, nil, nil
	}
	read := &sdk.ToolAnnotations{ReadOnlyHint: true}
	sdk.AddTool(server, &sdk.Tool{Name: "docker_container_logs", Annotations: read}, echo)
	sdk.AddTool(server, &sdk.Tool{Name: "radarr_movie_lookup", Annotations: read}, echo)
	sdk.AddTool(server, &sdk.Tool{Name: "docker_container_restart", Annotations: &sdk.ToolAnnotations{DestructiveHint: new(true)}},
		func(_ context.Context, req *sdk.CallToolRequest, in args) (*sdk.CallToolResult, any, error) {
			if pending, err := confirmed(req, "Restart the container "+in.Container+"?", "fp-"+in.Container); pending != nil || err != nil {
				return pending, nil, err
			}
			restarts.Add(1)
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: in.Container + " is running again"}}}, nil, nil
		})

	handler := sdk.NewStreamableHTTPHandler(
		func(*http.Request) *sdk.Server { return server },
		&sdk.StreamableHTTPOptions{Stateless: true, DisableLocalhostProtection: true},
	)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(s.Close)
	return s.URL, restarts
}

func newCommandBot(t *testing.T, press string, admins ...int64) (*Bot, *fakeBotAPI, *fakeTelegram, *atomic.Int32) {
	t.Helper()
	url, restarts := newCommandServer(t)
	api := newFakeBotAPI(t)
	approvals, asked := newApprovalHarness(press, 42, time.Second)

	tg, err := bot.New("123:test", bot.WithServerURL(api.URL), bot.WithSkipGetMe())
	if err != nil {
		t.Fatal(err)
	}
	m := NewMCP(url, testToken, approvals.Elicit)
	t.Cleanup(m.Close)

	cfg := Config{AdminUsers: map[int64]bool{}}
	for _, id := range admins {
		cfg.AdminUsers[id] = true
	}
	return &Bot{cfg: cfg, mcp: m, approvals: approvals, tg: tg}, api, asked, restarts
}

func runCommandText(b *Bot, userID int64, text string) {
	name, _, rest := parseCommand(text)
	cmd, _ := findCommand(name)
	b.runCommand(context.Background(), 1, userID, cmd, rest)
}

func TestCommandPassesItsArgument(t *testing.T) {
	b, api, _, _ := newCommandBot(t, "")

	runCommandText(b, 42, "/logs jellyfin 50")
	if got := api.texts(); !strings.Contains(got, "docker_container_logs container=jellyfin tail=50") {
		t.Errorf("the argument did not reach the tool: %s", got)
	}
}

func TestCommandSkipsUnregisteredTool(t *testing.T) {
	b, api, _, _ := newCommandBot(t, "")

	runCommandText(b, 42, "/search Duna 2021")
	got := api.texts()
	if !strings.Contains(got, "radarr_movie_lookup container= tail=0 term=Duna 2021") {
		t.Errorf("the lookup did not get the whole title: %s", got)
	}
	if strings.Contains(got, "não tem") || strings.Count(got, "---") != 0 {
		t.Errorf("a tool the server lacks was reported instead of skipped: %s", got)
	}
}

func TestCommandWithoutArgumentShowsUsage(t *testing.T) {
	b, api, _, _ := newCommandBot(t, "")

	runCommandText(b, 42, "/logs")
	if got := api.texts(); !strings.Contains(got, "Uso: <code>/logs &lt;container&gt; [linhas]</code>") {
		t.Errorf("no usage line: %s", got)
	}
}

func TestRestartCommandRefusedForUser(t *testing.T) {
	b, api, asked, restarts := newCommandBot(t, "y")

	runCommandText(b, 42, "/restart jellyfin")
	if restarts.Load() != 0 || len(asked.sent) != 0 {
		t.Fatalf("a user's restart got through: %d restarts, approvals %v", restarts.Load(), asked.sent)
	}
	if got := api.texts(); !strings.Contains(got, "/restart é só para os administradores") {
		t.Errorf("the refusal was not sent: %s", got)
	}
}

func TestRestartCommandAsksAdmin(t *testing.T) {
	b, api, asked, restarts := newCommandBot(t, "y", 42)

	runCommandText(b, 42, "/restart jellyfin")
	if restarts.Load() != 1 {
		t.Fatalf("an admin's approved restart did not run: %d restarts", restarts.Load())
	}
	if len(asked.sent) != 1 || !strings.Contains(asked.sent[0], "Restart the container jellyfin?") {
		t.Errorf("approval question: %v", asked.sent)
	}
	if got := api.texts(); !strings.Contains(got, "jellyfin is running again") {
		t.Errorf("the result was not sent: %s", got)
	}
}

func TestRestartCommandDeclined(t *testing.T) {
	b, api, _, restarts := newCommandBot(t, "n", 42)

	runCommandText(b, 42, "/restart jellyfin")
	if restarts.Load() != 0 {
		t.Fatalf("a declined restart ran")
	}
	if got := api.texts(); !strings.Contains(got, "declined") {
		t.Errorf("the decline was not reported: %s", got)
	}
}
