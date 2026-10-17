package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go/option"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type fakeClaude struct {
	*httptest.Server

	mu       sync.Mutex
	script   []string
	requests []map[string]any
}

func newFakeClaude(t *testing.T, script ...string) *fakeClaude {
	t.Helper()
	f := &fakeClaude{script: script}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		json.Unmarshal(body, &req)

		f.mu.Lock()
		f.requests = append(f.requests, req)
		if len(f.script) == 0 {
			f.mu.Unlock()
			http.Error(w, `{"type":"error","error":{"type":"api_error","message":"script exhausted"}}`, 500)
			return
		}
		resp := f.script[0]
		f.script = f.script[1:]
		f.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, resp)
	}))
	t.Cleanup(f.Close)
	return f
}

func message(stop string, content string) string {
	return `{"id":"msg","type":"message","role":"assistant","model":"claude-opus-5-5",` +
		`"content":` + content + `,"stop_reason":"` + stop + `","stop_sequence":null,` +
		`"usage":{"input_tokens":1,"output_tokens":1}}`
}

func newTestAgent(t *testing.T, claude *fakeClaude, mcpServer *fakeServer, a *Approvals) *Agent {
	t.Helper()
	m := NewMCP(mcpServer.URL, testToken, a.Elicit)
	t.Cleanup(m.Close)
	return NewAgent(Config{Model: defaultModel, Effort: defaultEffort}, m,
		option.WithBaseURL(claude.URL), option.WithAPIKey("test"), option.WithMaxRetries(0))
}

func TestAgentRunsApprovedWrite(t *testing.T) {
	mcpServer := newFakeServer(t)
	claude := newFakeClaude(t,
		message("tool_use", `[{"type":"tool_use","id":"tu_1","name":"write_thing","input":{"target":"jellyfin"}}]`),
		message("end_turn", `[{"type":"text","text":"Reiniciei o <b>jellyfin</b>."}]`),
	)
	approvals, tg := newApprovalHarness("y", 42, time.Second)
	agent := newTestAgent(t, claude, mcpServer, approvals)

	answer, err := agent.Ask(context.Background(), 1, 42, "reinicia o jellyfin")
	if err != nil {
		t.Fatal(err)
	}
	if answer != "Reiniciei o <b>jellyfin</b>." {
		t.Errorf("answer: %q", answer)
	}
	if mcpServer.writes.Load() != 1 {
		t.Errorf("writes: %d", mcpServer.writes.Load())
	}
	if len(tg.sent) != 1 || !strings.Contains(tg.sent[0], "Restart jellyfin?") {
		t.Errorf("approval question: %v", tg.sent)
	}

	second, _ := json.Marshal(claude.requests[1]["messages"])
	if !strings.Contains(string(second), "restarted jellyfin") {
		t.Errorf("tool result missing from the second request: %s", second)
	}

	first := claude.requests[0]
	tools, _ := first["tools"].([]any)
	if len(tools) != 4 || tools[0].(map[string]any)["name"] != "read_thing" ||
		tools[3].(map[string]any)["name"] != resourceTool {
		t.Errorf("tools: %v", first["tools"])
	}
	for _, tool := range tools {
		tool := tool.(map[string]any)
		marked := strings.HasPrefix(fmt.Sprint(tool["description"]), adminMark)
		if marked != (tool["name"] == "remove_thing") {
			t.Errorf("%s: admin mark %v", tool["name"], marked)
		}
	}
	if msgs, _ := json.Marshal(first["messages"]); !strings.Contains(string(msgs), userNote) {
		t.Errorf("Claude was not told the user is not an admin: %s", msgs)
	}
	if first["model"] != defaultModel || first["cache_control"] == nil || first["fallbacks"] != "default" {
		t.Errorf("request settings: model=%v cache_control=%v fallbacks=%v",
			first["model"], first["cache_control"], first["fallbacks"])
	}
	system, _ := json.Marshal(first["system"])
	if !strings.Contains(string(system), "Start with read_thing.") ||
		!strings.Contains(string(system), "The library first.") {
		t.Errorf("the server's procedures are not in the system prompt: %s", system)
	}
	if oc, _ := first["output_config"].(map[string]any); oc["effort"] != defaultEffort {
		t.Errorf("effort: %v", first["output_config"])
	}
}

func TestAgentReportsDeclinedWriteToClaude(t *testing.T) {
	mcpServer := newFakeServer(t)
	claude := newFakeClaude(t,
		message("tool_use", `[{"type":"tool_use","id":"tu_1","name":"write_thing","input":{"target":"jellyfin"}}]`),
		message("end_turn", `[{"type":"text","text":"Ok, não reiniciei."}]`),
	)
	approvals, _ := newApprovalHarness("n", 42, time.Second)
	agent := newTestAgent(t, claude, mcpServer, approvals)

	if _, err := agent.Ask(context.Background(), 1, 42, "reinicia o jellyfin"); err != nil {
		t.Fatal(err)
	}
	if mcpServer.writes.Load() != 0 {
		t.Fatal("a declined write ran")
	}
	second, _ := json.Marshal(claude.requests[1]["messages"])
	if !strings.Contains(string(second), `"is_error":true`) || !strings.Contains(string(second), "declined") {
		t.Errorf("the decline did not reach Claude as an error: %s", second)
	}
}

func TestAgentRefusesAdminToolForUser(t *testing.T) {
	mcpServer := newFakeServer(t)
	claude := newFakeClaude(t,
		message("tool_use", `[{"type":"tool_use","id":"tu_1","name":"read_thing","input":{}},`+
			`{"type":"tool_use","id":"tu_2","name":"remove_thing","input":{"target":"dune"}}]`),
		message("end_turn", `[{"type":"text","text":"Remover precisa de um admin."}]`),
	)
	approvals, tg := newApprovalHarness("y", 42, time.Second)
	agent := newTestAgent(t, claude, mcpServer, approvals)

	if _, err := agent.Ask(context.Background(), 1, 42, "remove o dune"); err != nil {
		t.Fatal(err)
	}
	if mcpServer.writes.Load() != 0 || len(tg.sent) != 0 {
		t.Fatalf("a user's removal got through: %d writes, approvals %v", mcpServer.writes.Load(), tg.sent)
	}

	second, _ := json.Marshal(claude.requests[1]["messages"])
	if !strings.Contains(string(second), "all good") {
		t.Errorf("the read was refused too: %s", second)
	}
	if !strings.Contains(string(second), "remove_thing was not called") || !strings.Contains(string(second), `"is_error":true`) {
		t.Errorf("the refusal did not reach Claude as an error: %s", second)
	}
}

func TestAgentAdminRunsAdminTool(t *testing.T) {
	mcpServer := newFakeServer(t)
	claude := newFakeClaude(t,
		message("tool_use", `[{"type":"tool_use","id":"tu_1","name":"remove_thing","input":{"target":"dune"}}]`),
		message("end_turn", `[{"type":"text","text":"Removi o Dune."}]`),
	)
	approvals, tg := newApprovalHarness("y", 42, time.Second)
	agent := newTestAgent(t, claude, mcpServer, approvals)
	agent.isAdmin = Config{AdminUsers: map[int64]bool{42: true}}.IsAdmin

	if _, err := agent.Ask(context.Background(), 1, 42, "remove o dune"); err != nil {
		t.Fatal(err)
	}
	if mcpServer.writes.Load() != 1 || len(tg.sent) != 1 {
		t.Fatalf("an admin's removal did not run: %d writes, approvals %v", mcpServer.writes.Load(), tg.sent)
	}
	if msgs, _ := json.Marshal(claude.requests[0]["messages"]); !strings.Contains(string(msgs), adminNote) {
		t.Errorf("Claude was not told the user is an admin: %s", msgs)
	}
}

func TestAdminOnly(t *testing.T) {
	for _, tc := range []struct {
		tool  sdk.Tool
		admin bool
	}{
		{sdk.Tool{Name: "radarr_queue_status", Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true}}, false},
		{sdk.Tool{Name: "radarr_movie_add", Annotations: &sdk.ToolAnnotations{DestructiveHint: new(false)}}, false},
		{sdk.Tool{Name: "radarr_movie_remove", Annotations: &sdk.ToolAnnotations{DestructiveHint: new(true)}}, true},
		{sdk.Tool{Name: "unsaid_write", Annotations: &sdk.ToolAnnotations{}}, true},
		{sdk.Tool{Name: "unannotated"}, true},
		{sdk.Tool{Name: "prowlarr_indexer_add", Annotations: &sdk.ToolAnnotations{DestructiveHint: new(false)}}, true},
	} {
		if got := adminOnly(&tc.tool); got != tc.admin {
			t.Errorf("%s: admin only %v, want %v", tc.tool.Name, got, tc.admin)
		}
	}
}

func TestAgentReadsResource(t *testing.T) {
	mcpServer := newFakeServer(t)
	claude := newFakeClaude(t,
		message("tool_use", `[{"type":"tool_use","id":"tu_1","name":"`+resourceTool+`","input":{"uri":"homelab://thing/profiles"}}]`),
		message("end_turn", `[{"type":"text","text":"Use HD-1080p."}]`),
	)
	approvals, _ := newApprovalHarness("", 42, time.Second)
	agent := newTestAgent(t, claude, mcpServer, approvals)

	if _, err := agent.Ask(context.Background(), 1, 42, "quais perfis existem?"); err != nil {
		t.Fatal(err)
	}

	tools, _ := json.Marshal(claude.requests[0]["tools"])
	if !strings.Contains(string(tools), "homelab://thing/profiles — The profiles write_thing accepts.") {
		t.Errorf("the resource is not described in the tool: %s", tools)
	}
	second, _ := json.Marshal(claude.requests[1]["messages"])
	if !strings.Contains(string(second), "| HD-1080p |") || strings.Contains(string(second), `"is_error":true`) {
		t.Errorf("the resource did not reach Claude: %s", second)
	}
}

func TestAgentRollsBackFailedTurn(t *testing.T) {
	mcpServer := newFakeServer(t)
	claude := newFakeClaude(t,
		message("tool_use", `[{"type":"tool_use","id":"tu_1","name":"read_thing","input":{}}]`),
		// the script runs out: the second request fails
	)
	approvals, _ := newApprovalHarness("", 42, time.Second)
	agent := newTestAgent(t, claude, mcpServer, approvals)

	if _, err := agent.Ask(context.Background(), 1, 42, "como está?"); err == nil {
		t.Fatal("expected the turn to fail")
	}
	if n := len(agent.conversation(1).messages); n != 0 {
		t.Fatalf("%d messages left behind by a failed turn", n)
	}
}

func TestAgentKeepsHistoryAcrossTurns(t *testing.T) {
	mcpServer := newFakeServer(t)
	claude := newFakeClaude(t,
		message("end_turn", `[{"type":"text","text":"um"}]`),
		message("end_turn", `[{"type":"text","text":"dois"}]`),
	)
	approvals, _ := newApprovalHarness("", 42, time.Second)
	agent := newTestAgent(t, claude, mcpServer, approvals)

	agent.Ask(context.Background(), 1, 42, "primeira")
	agent.Ask(context.Background(), 1, 42, "segunda")

	msgs, _ := claude.requests[1]["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("second request carried %d messages, want 3", len(msgs))
	}

	if agent.spent.output != 2 || !near(agent.spent.usd, 2*(4+20)/1e6) {
		t.Errorf("spend since start: %+v", agent.spent)
	}

	agent.Reset(1)
	if len(agent.conversation(1).messages) != 0 {
		t.Fatal("reset kept the history")
	}
}

func TestAgentBusyWhileTurnInProgress(t *testing.T) {
	agent := &Agent{convs: map[int64]*conversation{}}
	conv := agent.conversation(1)
	conv.mu.Lock()
	defer conv.mu.Unlock()

	if _, err := agent.Ask(context.Background(), 1, 42, "oi"); err != errBusy {
		t.Fatalf("got %v, want errBusy", err)
	}
}

func TestClaudeToolKeepsSchema(t *testing.T) {
	tool := claudeTool(&sdk.Tool{
		Name:        "docker_container_logs",
		Description: "Reads logs.",
		InputSchema: map[string]any{
			"type":                 "object",
			"properties":           map[string]any{"name": map[string]any{"type": "string"}},
			"required":             []any{"name"},
			"additionalProperties": false,
		},
	})

	raw, _ := json.Marshal(tool)
	var got map[string]any
	json.Unmarshal(raw, &got)

	schema := got["input_schema"].(map[string]any)
	if got["name"] != "docker_container_logs" || got["description"] != "Reads logs." {
		t.Errorf("tool: %s", raw)
	}
	if schema["type"] != "object" || schema["additionalProperties"] != false {
		t.Errorf("schema: %v", schema)
	}
	if req, _ := schema["required"].([]any); len(req) != 1 || req[0] != "name" {
		t.Errorf("required: %v", schema["required"])
	}
}
