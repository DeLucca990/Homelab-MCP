package telegram

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func entryJSON(t *testing.T, e ledgerEntry) string {
	t.Helper()
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestLedgerKeepsWhatItCanReadAndDropsWhatExpired(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.Local)
	path := filepath.Join(t.TempDir(), "spend.jsonl")
	writeLines(t, path,
		entryJSON(t, ledgerEntry{At: now.AddDate(0, 0, -100), Kind: kindQuestion, USD: 9}),
		`{"at": not json`,
		entryJSON(t, ledgerEntry{At: now.Add(-time.Hour), Kind: kindQuestion, User: 42, USD: 0.5}),
	)

	l, err := OpenLedger(path, now)
	if err != nil {
		t.Fatal(err)
	}
	if got := l.total(now.AddDate(-1, 0, 0)); got.Count != 1 || got.USD != 0.5 {
		t.Fatalf("read %+v, want only the recent entry", got)
	}
	if raw, _ := os.ReadFile(path); strings.Count(string(raw), "\n") != 1 {
		t.Errorf("the expired and unreadable lines were not dropped from the file:\n%s", raw)
	}

	if err := l.Record(ledgerEntry{At: now, Kind: kindHeadline, USD: 0.25}); err != nil {
		t.Fatal(err)
	}
	again, err := OpenLedger(path, now)
	if err != nil {
		t.Fatal(err)
	}
	if got := again.total(startOfDay(now)); got.Count != 2 || got.USD != 0.75 {
		t.Errorf("after a restart: %+v, want both of today's entries", got)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Errorf("ledger mode %v", info.Mode().Perm())
	}
}

func TestLedgerFileThatDoesNotExistYet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "spend.jsonl")
	l, err := OpenLedger(path, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Record(ledgerEntry{At: time.Now(), Kind: kindQuestion, USD: 0.1}); err != nil {
		t.Fatalf("the first record did not create the file and its directory: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Error(err)
	}

	var none *Ledger
	if err := none.Record(ledgerEntry{At: time.Now()}); err != nil || none.total(time.Time{}).Count != 0 {
		t.Error("a nil ledger should record nothing and have spent nothing")
	}
}

func TestStateDirMustBeWritable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if err := writableDir(dir); err != nil {
		t.Fatalf("a directory that does not exist yet should be made: %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("the check left %d file(s) behind", len(entries))
	}

	if os.Geteuid() == 0 {
		t.Skip("root writes anywhere")
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	if writableDir(dir) == nil {
		t.Error("a directory the bot cannot write in passed the check")
	}
}

func TestCostReport(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.Local)
	l := &Ledger{}
	for _, e := range []ledgerEntry{
		{At: now.AddDate(0, 0, -20), Kind: kindQuestion, User: 7, Name: "Ana", USD: 2, Input: 1_500_000},
		{At: now.AddDate(0, 0, -3), Kind: kindQuestion, User: 42, Name: "Pedro", USD: 0.30, Input: 20_000},
		{At: now.AddDate(0, 0, -1), Kind: kindHeadline, USD: 0.0004, Input: 1_000},
		{At: now.Add(-2 * time.Hour), Kind: kindQuestion, User: 42, Name: "Pedro", USD: 0.25, Input: 10_000, Output: 2_000},
		{At: now.Add(-time.Hour), Kind: kindQuestion, User: 42, Name: "Pedro", Unpriced: []string{"claude-future-9"}, Input: 100},
	} {
		l.Record(e)
	}

	admin := costReport(l, now, 42, true, 1)
	for _, want := range []string{
		"Hoje: US$ 0,2500 · 2 respostas · 12,1 mil tokens",
		"7 dias: US$ 0,5504 · 4 respostas · 33,1 mil tokens",
		"30 dias: US$ 2,55 · 5 respostas · 1,5 mi tokens",
		"<b>Por pessoa, 30 dias</b>\n• Ana: US$ 2,00 · 1\n• Pedro: US$ 0,5500 · 3\n• resumo diário: US$ 0,0004 · 1",
		"1 resposta de modelos sem preço na tabela ficaram fora da soma",
		"Teto diário: US$ 1,00 — 25% usado hoje",
	} {
		if !strings.Contains(admin, want) {
			t.Errorf("admin report misses %q:\n%s", want, admin)
		}
	}

	user := costReport(l, now, 7, false, 0)
	if strings.Contains(user, "Por pessoa") || !strings.Contains(user, "Você, em 30 dias: US$ 2,00 · 1 resposta") {
		t.Errorf("a user's report should show their own share, not everyone's:\n%s", user)
	}
	if !strings.Contains(user, "Sem teto diário") {
		t.Errorf("no budget line:\n%s", user)
	}

	if empty := costReport(&Ledger{}, now, 42, true, 1); !strings.Contains(empty, "Nada gasto nos últimos 30 dias.") ||
		!strings.Contains(empty, "0% usado hoje") {
		t.Errorf("empty ledger:\n%s", empty)
	}
}

func TestAskRecordsItsCost(t *testing.T) {
	claude := newFakeClaude(t, message("end_turn", `[{"type":"text","text":"Tudo certo."}]`))
	b, _ := newAskBot(t, claude)
	b.ledger = &Ledger{}

	b.ask(context.Background(), 1, 42, "Pedro", "como está?")

	month := b.ledger.since(time.Now().AddDate(0, 0, -1))
	if len(month) != 1 {
		t.Fatalf("%d entries, want the question's", len(month))
	}
	e := month[0]
	if e.Kind != kindQuestion || e.User != 42 || e.Chat != 1 || e.Name != "Pedro" || e.Failed ||
		e.Input != 1 || e.Output != 1 || !near(e.USD, (4+20)/1e6) {
		t.Errorf("entry: %+v", e)
	}
}

func TestAskRefusedOnceTheBudgetIsSpent(t *testing.T) {
	claude := newFakeClaude(t, message("end_turn", `[{"type":"text","text":"Tudo certo."}]`))
	b, api := newAskBot(t, claude)
	b.ledger = &Ledger{}
	b.ledger.Record(ledgerEntry{At: time.Now(), Kind: kindQuestion, USD: 1.2})
	b.ledger.Record(ledgerEntry{At: time.Now().AddDate(0, 0, -1), Kind: kindQuestion, USD: 50}) // yesterday's does not count
	b.cfg.DailyBudget = 1

	b.ask(context.Background(), 1, 42, "Pedro", "como está?")

	if len(claude.requests) != 0 {
		t.Fatal("Claude was asked past the budget")
	}
	if got := api.texts(); !strings.Contains(got, "O teto diário de US$ 1,00 já foi gasto (US$ 1,20 hoje)") {
		t.Errorf("refusal: %q", got)
	}

	b.cfg.DailyBudget = 2
	b.ask(context.Background(), 1, 42, "Pedro", "como está?")
	if len(claude.requests) != 1 {
		t.Error("under the budget the question was refused")
	}
}

func TestDigestHeadlineRespectsTheBudget(t *testing.T) {
	claude := newFakeClaude(t, message("end_turn", `[{"type":"text","text":"Dia tranquilo."}]`))
	b, api := newDigestBot(t, newDigestServer(t, fullDigest()), claude)
	b.ledger = &Ledger{}

	b.sendDigest(context.Background(), []int64{42}, false)
	if got := b.ledger.since(startOfDay(time.Now())); len(got) != 1 || got[0].Kind != kindHeadline {
		t.Fatalf("the headline was not recorded: %+v", got)
	}

	b.cfg.DailyBudget = 0.0000001 // spent by that headline
	b.sendDigest(context.Background(), []int64{42}, false)
	if len(claude.requests) != 1 {
		t.Errorf("a headline was asked for past the budget: %d requests", len(claude.requests))
	}
	if got := api.texts(); strings.Count(got, "📋 <b>Resumo diário</b>") != 2 {
		t.Errorf("the budget held the digest itself back:\n%s", got)
	}
}

func TestConfigBudgetAndLedger(t *testing.T) {
	base := map[string]string{TokenEnv: "x", AllowedUsersEnv: "42", MCPURLEnv: "http://x/mcp", mcpTokenEnv: "t",
		"STATE_DIRECTORY": "/var/lib/nautilus-bot"}

	setEnv(t, base)
	cfg, err := LoadConfig()
	if err != nil || cfg.DailyBudget != 0 || cfg.LedgerPath != "/var/lib/nautilus-bot/spend.jsonl" {
		t.Fatalf("defaults: budget %v, ledger %q, %v", cfg.DailyBudget, cfg.LedgerPath, err)
	}

	base[DailyBudgetEnv], base[LedgerPathEnv] = "1,50", "/tmp/spend.jsonl"
	setEnv(t, base)
	if cfg, err = LoadConfig(); err != nil || cfg.DailyBudget != 1.5 || cfg.LedgerPath != "/tmp/spend.jsonl" {
		t.Errorf("explicit: budget %v, ledger %q, %v", cfg.DailyBudget, cfg.LedgerPath, err)
	}

	for _, bad := range []string{"0", "-2", "lots", "NaN", "Inf"} {
		base[DailyBudgetEnv] = bad
		setEnv(t, base)
		if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), DailyBudgetEnv) {
			t.Errorf("%s=%q was accepted", DailyBudgetEnv, bad)
		}
	}
}
