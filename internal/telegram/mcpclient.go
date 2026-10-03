package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type ElicitFunc func(context.Context, *sdk.ElicitRequest) (*sdk.ElicitResult, error)

type MCP struct {
	client    *sdk.Client
	transport *sdk.StreamableClientTransport

	mu      sync.Mutex
	session *sdk.ClientSession
}

func NewMCP(url, token string, elicit ElicitFunc) *MCP {
	return &MCP{
		// The server refuses writes to a client that does not declare elicitation.
		client: sdk.NewClient(
			&sdk.Implementation{Name: "homelab-telegram-bot", Version: "1.0.0"},
			&sdk.ClientOptions{ElicitationHandler: elicit},
		),
		transport: &sdk.StreamableClientTransport{
			Endpoint: url,
			HTTPClient: &http.Client{
				Transport: bearer{token: token, next: http.DefaultTransport},
			},
			DisableStandaloneSSE: true,
		},
	}
}

type bearer struct {
	token string
	next  http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.next.RoundTrip(r)
}

// Over HTTP the SDK closes the session on any error status, so a failed call
// drops it and the next one reconnects.
func (m *MCP) connect(ctx context.Context) (*sdk.ClientSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.session != nil {
		return m.session, nil
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	session, err := m.client.Connect(ctx, m.transport, nil)
	if err != nil {
		return nil, fmt.Errorf("connecting to the MCP server at %s: %w", m.transport.Endpoint, err)
	}
	m.session = session
	return session, nil
}

func (m *MCP) drop(session *sdk.ClientSession) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.session == session {
		m.session.Close()
		m.session = nil
	}
}

// retry repeats reads only: a failed tool call may already have acted.
func retry[T any](ctx context.Context, m *MCP, read func(*sdk.ClientSession) (T, error)) (T, error) {
	var (
		res T
		err error
	)
	for attempt := 0; attempt < 2; attempt++ {
		var session *sdk.ClientSession
		if session, err = m.connect(ctx); err != nil {
			return res, err
		}
		if res, err = read(session); err == nil {
			return res, nil
		}
		m.drop(session)
	}
	return res, err
}

func (m *MCP) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.session != nil {
		m.session.Close()
		m.session = nil
	}
}

// Sorted so the tool list, which starts every prompt, stays byte-identical for the cache.
func (m *MCP) Tools(ctx context.Context) ([]*sdk.Tool, error) {
	return retry(ctx, m, func(session *sdk.ClientSession) ([]*sdk.Tool, error) {
		var tools []*sdk.Tool
		for tool, err := range session.Tools(ctx, nil) {
			if err != nil {
				return nil, fmt.Errorf("listing tools: %w", err)
			}
			tools = append(tools, tool)
		}
		sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
		return tools, nil
	})
}

func (m *MCP) HasTool(ctx context.Context, name string) (bool, error) {
	tools, err := m.Tools(ctx)
	if err != nil {
		return false, err
	}
	for _, t := range tools {
		if t.Name == name {
			return true, nil
		}
	}
	return false, nil
}

func (m *MCP) Call(ctx context.Context, name string, args map[string]any) (text string, isError bool, err error) {
	session, err := m.connect(ctx)
	if err != nil {
		return "", false, err
	}

	if args == nil {
		args = map[string]any{}
	}

	res, err := session.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		m.drop(session)
		return "", false, fmt.Errorf("%s: %w", name, err)
	}
	return resultText(res), res.IsError, nil
}

func (m *MCP) Prompt(ctx context.Context, name string) (string, error) {
	res, err := retry(ctx, m, func(session *sdk.ClientSession) (*sdk.GetPromptResult, error) {
		return session.GetPrompt(ctx, &sdk.GetPromptParams{Name: name})
	})
	if err != nil {
		return "", fmt.Errorf("prompt %s: %w", name, err)
	}

	var b strings.Builder
	for _, msg := range res.Messages {
		if t, ok := msg.Content.(*sdk.TextContent); ok {
			b.WriteString(t.Text)
		}
	}
	return b.String(), nil
}

func resultText(res *sdk.CallToolResult) string {
	var parts []string
	for _, c := range res.Content {
		if t, ok := c.(*sdk.TextContent); ok && t.Text != "" {
			parts = append(parts, t.Text)
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, "\n\n")
	}

	if res.StructuredContent != nil {
		if raw, err := json.Marshal(res.StructuredContent); err == nil {
			return string(raw)
		}
	}
	return "(no output)"
}
