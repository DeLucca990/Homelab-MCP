package telegram

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// What the bot has spent on Claude, kept across restarts: one JSON line per
// response a model wrote — a question, answered or failed, or a digest's
// headline. /cost reads it, and so does the daily budget, which would be no
// budget at all if a restart forgot what the day had already cost.
//
// The prices are the estimate of cost.go, not the bill.

const ledgerRetention = 90 * 24 * time.Hour

const (
	kindQuestion = "question"
	kindHeadline = "headline"
)

type ledgerEntry struct {
	At     time.Time `json:"at"`
	Kind   string    `json:"kind"`
	Chat   int64     `json:"chat,omitempty"`
	User   int64     `json:"user,omitempty"`
	Name   string    `json:"name,omitempty"`
	Failed bool      `json:"failed,omitempty"`

	USD        float64  `json:"usd"`
	Models     []string `json:"models,omitempty"`
	Unpriced   []string `json:"unpriced,omitempty"`
	Input      int64    `json:"input,omitempty"`
	Output     int64    `json:"output,omitempty"`
	CacheRead  int64    `json:"cache_read,omitempty"`
	CacheWrite int64    `json:"cache_write,omitempty"`
}

func entryOf(at time.Time, kind string, chat, user int64, name string, s spend, failed bool) ledgerEntry {
	return ledgerEntry{At: at, Kind: kind, Chat: chat, User: user, Name: name, Failed: failed,
		USD: s.usd, Models: s.models, Unpriced: s.unpriced,
		Input: s.input, Output: s.output, CacheRead: s.cacheRead, CacheWrite: s.cacheWrite}
}

func (e ledgerEntry) tokens() int64 { return e.Input + e.Output + e.CacheRead + e.CacheWrite }

type Ledger struct {
	path string

	mu      sync.Mutex
	entries []ledgerEntry
}

func OpenLedger(path string, now time.Time) (*Ledger, error) {
	l := &Ledger{path: path}
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return l, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	expired, unreadable := 0, 0
	cutoff := now.Add(-ledgerRetention)
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		line := strings.TrimSpace(scan.Text())
		if line == "" {
			continue
		}
		var e ledgerEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil || e.At.IsZero() {
			unreadable++
			continue
		}
		if e.At.Before(cutoff) {
			expired++
			continue
		}
		l.entries = append(l.entries, e)
	}
	if err := scan.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	slices.SortStableFunc(l.entries, func(a, b ledgerEntry) int { return a.At.Compare(b.At) })

	if unreadable > 0 {
		log.Printf("spending ledger: %d unreadable line(s) in %s left out", unreadable, path)
	}
	if expired+unreadable > 0 {
		if err := l.rewrite(); err != nil {
			log.Printf("spending ledger: could not drop %d old line(s): %v", expired+unreadable, err)
		}
	}
	return l, nil
}

func (l *Ledger) rewrite() error {
	var b strings.Builder
	for _, e := range l.entries {
		raw, err := json.Marshal(e)
		if err != nil {
			return err
		}
		b.Write(raw)
		b.WriteByte('\n')
	}
	tmp, err := os.CreateTemp(filepath.Dir(l.path), ".spend-*.jsonl")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(b.String()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), l.path)
}

func (l *Ledger) Record(e ledgerEntry) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	i, _ := slices.BinarySearchFunc(l.entries, e.At, func(x ledgerEntry, t time.Time) int {
		if x.At.After(t) {
			return 1
		}
		return -1
	})
	l.entries = slices.Insert(l.entries, i, e)
	cutoff := l.entries[len(l.entries)-1].At.Add(-ledgerRetention)
	if i := slices.IndexFunc(l.entries, func(x ledgerEntry) bool { return !x.At.Before(cutoff) }); i > 0 {
		l.entries = slices.Delete(l.entries, 0, i)
	}

	if l.path == "" {
		return nil
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(raw, '\n')); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

type ledgerTotal struct {
	USD    float64
	Count  int
	Tokens int64
}

func (t *ledgerTotal) add(e ledgerEntry) {
	t.USD += e.USD
	t.Count++
	t.Tokens += e.tokens()
}

func (l *Ledger) since(from time.Time) []ledgerEntry {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	i, _ := slices.BinarySearchFunc(l.entries, from, func(e ledgerEntry, t time.Time) int { return e.At.Compare(t) })
	return slices.Clone(l.entries[i:])
}

func (l *Ledger) total(from time.Time) ledgerTotal {
	var t ledgerTotal
	for _, e := range l.since(from) {
		t.add(e)
	}
	return t
}

func startOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

func (b *Bot) budgetSpent(now time.Time) (spent float64, over bool) {
	if b.cfg.DailyBudget <= 0 {
		return 0, false
	}
	spent = b.ledger.total(startOfDay(now)).USD
	return spent, spent >= b.cfg.DailyBudget
}

func (b *Bot) record(e ledgerEntry) {
	if err := b.ledger.Record(e); err != nil {
		log.Printf("spending ledger: kept in memory only, a restart will forget it: %v", err)
	}
}

func budgetMessage(budget, spent float64) string {
	return fmt.Sprintf("💸 O teto diário de %s já foi gasto (%s hoje). Perguntas em linguagem natural "+
		"voltam à meia-noite; os comandos continuam funcionando.", money(budget), money(spent))
}

// --- /cost -------------------------------------------------------------------------

const headlineName = "resumo diário"

func costReport(l *Ledger, now time.Time, viewer int64, admin bool, budget float64) string {
	today := startOfDay(now)
	periods := []struct {
		label string
		from  time.Time
	}{
		{"Hoje", today},
		{"7 dias", today.AddDate(0, 0, -6)},
		{"30 dias", today.AddDate(0, 0, -29)},
	}

	var b strings.Builder
	b.WriteString("💲 <b>Gastos com o Claude</b> <i>(estimativa a preço de tabela)</i>\n\n")

	month := l.since(periods[2].from)
	if len(month) == 0 {
		b.WriteString("Nada gasto nos últimos 30 dias.\n")
	} else {
		for _, p := range periods {
			var t ledgerTotal
			for _, e := range month {
				if !e.At.Before(p.from) {
					t.add(e)
				}
			}
			fmt.Fprintf(&b, "%s: %s · %d %s · %s tokens\n", p.label, money(t.USD), t.Count,
				plural(t.Count, "resposta", "respostas"), compactTokens(t.Tokens))
		}

		b.WriteString("\n")
		if admin {
			b.WriteString(byPerson(month))
		} else {
			var mine ledgerTotal
			for _, e := range month {
				if e.User == viewer {
					mine.add(e)
				}
			}
			fmt.Fprintf(&b, "Você, em 30 dias: %s · %d %s\n", money(mine.USD), mine.Count,
				plural(mine.Count, "resposta", "respostas"))
		}
	}

	var unpriced int
	for _, e := range month {
		if len(e.Unpriced) > 0 {
			unpriced++
		}
	}
	if unpriced > 0 {
		fmt.Fprintf(&b, "\n<i>%d %s de modelos sem preço na tabela ficaram fora da soma.</i>\n",
			unpriced, plural(unpriced, "resposta", "respostas"))
	}

	b.WriteString("\n")
	if budget > 0 {
		spent := 0.0
		for _, e := range month {
			if !e.At.Before(today) {
				spent += e.USD
			}
		}
		fmt.Fprintf(&b, "Teto diário: %s — %.0f%% usado hoje", money(budget), 100*spent/budget)
		if spent >= budget {
			b.WriteString(", linguagem natural pausada até a meia-noite")
		}
	} else {
		b.WriteString("<i>Sem teto diário (" + DailyBudgetEnv + ").</i>")
	}
	return b.String()
}

func byPerson(entries []ledgerEntry) string {
	type person struct {
		name  string
		total ledgerTotal
	}
	people := map[string]*person{}
	var order []string
	for _, e := range entries {
		key, name := fmt.Sprint(e.User), e.Name
		if e.Kind == kindHeadline {
			key, name = kindHeadline, headlineName
		}
		p, ok := people[key]
		if !ok {
			p = &person{name: key}
			people[key] = p
			order = append(order, key)
		}
		if name != "" {
			p.name = name
		}
		p.total.add(e)
	}
	slices.SortStableFunc(order, func(a, b string) int {
		switch x, y := people[a].total.USD, people[b].total.USD; {
		case x > y:
			return -1
		case x < y:
			return 1
		}
		return 0
	})

	var b strings.Builder
	b.WriteString("<b>Por pessoa, 30 dias</b>\n")
	for _, key := range order {
		p := people[key]
		fmt.Fprintf(&b, "• %s: %s · %d\n", escape(p.name), money(p.total.USD), p.total.Count)
	}
	return b.String()
}

func money(usd float64) string {
	format := "%.2f"
	if usd < 1 {
		format = "%.4f"
	}
	return "US$ " + strings.Replace(fmt.Sprintf(format, usd), ".", ",", 1)
}

func compactTokens(n int64) string {
	short := func(v float64, unit string) string {
		s := strings.Replace(fmt.Sprintf("%.1f", v), ".", ",", 1)
		return strings.TrimSuffix(s, ",0") + " " + unit
	}
	switch {
	case n < 1000:
		return fmt.Sprint(n)
	case n < 1_000_000:
		return short(float64(n)/1e3, "mil")
	default:
		return short(float64(n)/1e6, "mi")
	}
}
