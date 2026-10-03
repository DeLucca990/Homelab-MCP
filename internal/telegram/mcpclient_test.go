package telegram

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeServer gates write_thing the way internal/mcp/confirm.go gates every write.
type fakeServer struct {
	*httptest.Server
	writes atomic.Int32
}

const testToken = "s3cret"

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	f := &fakeServer{}

	server := sdk.NewServer(&sdk.Implementation{Name: "fake", Version: "1"}, nil)

	sdk.AddTool(server, &sdk.Tool{Name: "read_thing"},
		func(ctx context.Context, req *sdk.CallToolRequest, _ struct{}) (*sdk.CallToolResult, any, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "all good"}}}, nil, nil
		})

	sdk.AddTool(server, &sdk.Tool{Name: "write_thing"},
		func(ctx context.Context, req *sdk.CallToolRequest, in struct {
			Target string `json:"target"`
		}) (*sdk.CallToolResult, any, error) {
			params := req.Session.InitializeParams()
			canConfirm := params != nil && params.Capabilities != nil && params.Capabilities.Elicitation != nil

			if len(req.Params.InputResponses) == 0 {
				if !canConfirm {
					return nil, nil, errors.New("this client cannot show a confirmation")
				}
				return &sdk.CallToolResult{
					InputRequests: sdk.InputRequestMap{
						"confirm": &sdk.ElicitParams{
							Message:         "Restart " + in.Target + "?",
							RequestedSchema: map[string]any{"type": "object", "properties": map[string]any{}},
						},
					},
					RequestState: "fp-" + in.Target,
				}, nil, nil
			}

			res, _ := req.Params.InputResponses["confirm"].(*sdk.ElicitResult)
			if res == nil || res.Action != "accept" {
				return nil, nil, errors.New("the user declined it")
			}
			if req.Params.RequestState != "fp-"+in.Target {
				return nil, nil, errors.New("the approved operation does not match")
			}
			f.writes.Add(1)
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "restarted " + in.Target}}}, nil, nil
		})

	server.AddPrompt(&sdk.Prompt{Name: "triage"},
		func(context.Context, *sdk.GetPromptRequest) (*sdk.GetPromptResult, error) {
			return &sdk.GetPromptResult{Messages: []*sdk.PromptMessage{
				{Role: "user", Content: &sdk.TextContent{Text: "Start with read_thing."}},
			}}, nil
		})

	handler := sdk.NewStreamableHTTPHandler(
		func(*http.Request) *sdk.Server { return server },
		&sdk.StreamableHTTPOptions{Stateless: true, DisableLocalhostProtection: true},
	)

	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(f.Close)
	return f
}

func TestMCPCallsReadTool(t *testing.T) {
	f := newFakeServer(t)
	m := NewMCP(f.URL, testToken, func(context.Context, *sdk.ElicitRequest) (*sdk.ElicitResult, error) {
		t.Fatal("a read tool must not ask for anything")
		return nil, nil
	})
	defer m.Close()

	text, isErr, err := m.Call(context.Background(), "read_thing", nil)
	if err != nil || isErr || text != "all good" {
		t.Fatalf("got %q, isError=%v, err=%v", text, isErr, err)
	}

	tools, err := m.Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 2 || tools[0].Name != "read_thing" || tools[1].Name != "write_thing" {
		t.Fatalf("tools not listed in name order: %v", tools)
	}
}

func TestMCPWrongTokenIsRefused(t *testing.T) {
	f := newFakeServer(t)
	m := NewMCP(f.URL, "wrong", nil)
	defer m.Close()

	if _, _, err := m.Call(context.Background(), "read_thing", nil); err == nil {
		t.Fatal("a wrong token reached the tools")
	}
}

func TestMCPApprovalRoundTrip(t *testing.T) {
	f := newFakeServer(t)

	var asked string
	m := NewMCP(f.URL, testToken, func(_ context.Context, req *sdk.ElicitRequest) (*sdk.ElicitResult, error) {
		asked = req.Params.Message
		return &sdk.ElicitResult{Action: "accept"}, nil
	})
	defer m.Close()

	text, isErr, err := m.Call(context.Background(), "write_thing", map[string]any{"target": "jellyfin"})
	if err != nil || isErr {
		t.Fatalf("approved write failed: %q, isError=%v, err=%v", text, isErr, err)
	}
	if asked != "Restart jellyfin?" {
		t.Fatalf("the server's message did not reach the handler: %q", asked)
	}
	if text != "restarted jellyfin" || f.writes.Load() != 1 {
		t.Fatalf("got %q after %d writes", text, f.writes.Load())
	}
}

func TestMCPDeclineDoesNotAct(t *testing.T) {
	f := newFakeServer(t)
	m := NewMCP(f.URL, testToken, func(context.Context, *sdk.ElicitRequest) (*sdk.ElicitResult, error) {
		return &sdk.ElicitResult{Action: "decline"}, nil
	})
	defer m.Close()

	text, isErr, err := m.Call(context.Background(), "write_thing", map[string]any{"target": "jellyfin"})
	if err != nil {
		t.Fatal(err)
	}
	if !isErr || f.writes.Load() != 0 {
		t.Fatalf("a declined write ran: %q, isError=%v, writes=%d", text, isErr, f.writes.Load())
	}
}

func TestMCPWithoutElicitationIsRefused(t *testing.T) {
	f := newFakeServer(t)
	m := NewMCP(f.URL, testToken, nil)
	defer m.Close()

	_, isErr, err := m.Call(context.Background(), "write_thing", map[string]any{"target": "jellyfin"})
	if err != nil {
		t.Fatal(err)
	}
	if !isErr || f.writes.Load() != 0 {
		t.Fatalf("a client without elicitation could write: isError=%v, writes=%d", isErr, f.writes.Load())
	}
}

func TestMCPRecoversAfterFailedCall(t *testing.T) {
	f := newFakeServer(t)
	m := NewMCP(f.URL, testToken, nil)
	defer m.Close()

	if _, _, err := m.Call(context.Background(), "no_such_tool", nil); err == nil {
		t.Fatal("an unknown tool succeeded")
	}
	if _, err := m.Prompt(context.Background(), "no_such_prompt"); err == nil {
		t.Fatal("an unknown prompt succeeded")
	}

	text, isErr, err := m.Call(context.Background(), "read_thing", nil)
	if err != nil || isErr || text != "all good" {
		t.Fatalf("the session did not recover: %q, isError=%v, err=%v", text, isErr, err)
	}
	if got, err := m.Prompt(context.Background(), "triage"); err != nil || got != "Start with read_thing." {
		t.Fatalf("prompt after recovery: %q, %v", got, err)
	}
}
