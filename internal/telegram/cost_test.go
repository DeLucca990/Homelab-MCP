package telegram

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
)

func parseMessage(t *testing.T, raw string) *anthropic.BetaMessage {
	t.Helper()
	var m anthropic.BetaMessage
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	return &m
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestPriceOfPicksTheLongestID(t *testing.T) {
	for model, want := range map[string]float64{
		"claude-opus-5-5":           4,
		"claude-opus-5":             5,
		"claude-sonnet-5-5":         2,
		"claude-haiku-4-5-20251001": 1,
	} {
		if p, ok := priceOf(model, 1000); !ok || p.input != want {
			t.Errorf("%s: got %+v, %v", model, p, ok)
		}
	}
	if _, ok := priceOf("claude-unknown-9", 1000); ok {
		t.Error("an unknown model was priced")
	}
}

func TestSpendOfOneAttempt(t *testing.T) {
	s := spendOf(parseMessage(t, `{"model":"claude-sonnet-5-5","usage":{
		"input_tokens":1000,"output_tokens":500,
		"cache_read_input_tokens":10000,"cache_creation_input_tokens":2000}}`))

	// 1000×$2 + 500×$10 + 10000×$0.10 + 2000×$2×1.25, per million
	if !near(s.usd, 0.013) {
		t.Errorf("usd: %v", s.usd)
	}
	if s.input != 1000 || s.output != 500 || s.cacheRead != 10000 || s.cacheWrite != 2000 {
		t.Errorf("tokens: %+v", s)
	}
}

func TestSpendOfHaikuLongPrompt(t *testing.T) {
	short := spendOf(parseMessage(t, `{"model":"claude-haiku-5-5","usage":{
		"input_tokens":100000,"output_tokens":1000000}}`))
	if !near(short.usd, 0.01+0.50) { // 100K×$0.10 + 1M×$0.50
		t.Errorf("at the threshold: %v", short.usd)
	}

	// 1 input token over the threshold, the rest a cache hit: the whole
	// request pays the long rates, the cached part too.
	long := spendOf(parseMessage(t, `{"model":"claude-haiku-5-5","usage":{
		"input_tokens":1,"output_tokens":1000000,"cache_read_input_tokens":100000}}`))
	if !near(long.usd, 0.0000005+2.50+0.005) { // 1×$0.50 + 1M×$2.50 + 100K×$0.05
		t.Errorf("over the threshold: %v", long.usd)
	}
}

func TestSpendOfHourLongCacheWrite(t *testing.T) {
	s := spendOf(parseMessage(t, `{"model":"claude-opus-5-5","usage":{
		"input_tokens":0,"output_tokens":0,"cache_creation_input_tokens":1000000,
		"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":1000000}}}`))
	if !near(s.usd, 8) { // 1M × $4 × 2
		t.Errorf("usd: %v", s.usd)
	}
}

func TestSpendOfFallbackAttempts(t *testing.T) {
	s := spendOf(parseMessage(t, `{"model":"claude-opus-4-8","usage":{
		"input_tokens":1000000,"output_tokens":0,
		"iterations":[
			{"type":"message","model":"claude-fable-5-1","input_tokens":1000000,"output_tokens":0},
			{"type":"fallback_message","model":"claude-opus-4-8","input_tokens":1000000,"output_tokens":1000000}
		]}}`))
	if !near(s.usd, 10+5+25) {
		t.Errorf("usd: %v", s.usd)
	}
	if s.input != 2000000 {
		t.Errorf("input: %d", s.input)
	}
}

func TestSpendOfUnknownModelIsNamedNotGuessed(t *testing.T) {
	s := spendOf(parseMessage(t, `{"model":"claude-future-9","usage":{"input_tokens":5,"output_tokens":5}}`))
	if s.usd != 0 || len(s.unpriced) != 1 || s.unpriced[0] != "claude-future-9" {
		t.Errorf("got %+v", s)
	}
}

func TestWho(t *testing.T) {
	if got := who(requester{chatID: 42, userID: 42}); got != "user 42" {
		t.Errorf("private: %q", got)
	}
	if got := who(requester{chatID: -5552970492, userID: 42}); got != "user 42 in group -5552970492" {
		t.Errorf("group: %q", got)
	}
}

func TestSpendCountsStepsAndModels(t *testing.T) {
	var turn spend
	turn.add(spendOf(parseMessage(t, `{"model":"claude-sonnet-5-5","usage":{"input_tokens":1,"output_tokens":1}}`)))
	turn.add(spendOf(parseMessage(t, `{"model":"claude-opus-4-8","usage":{"input_tokens":1,"output_tokens":1}}`)))
	if turn.steps != 2 {
		t.Errorf("steps: %d", turn.steps)
	}
	if got := otherThan(turn.models, "claude-sonnet-5-5"); len(got) != 1 || got[0] != "claude-opus-4-8" {
		t.Errorf("other models: %v", got)
	}
}

func TestUsageLine(t *testing.T) {
	s := spend{input: 1020, cacheRead: 12100, output: 2114, usd: 0.01234}
	if got, want := usageLine(s, ""), "<i>≈ US$ 0,0123 · 15.234 tokens: 1.020 entrada, 12.100 cache lido, 2.114 saída</i>"; got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}

	s = spend{input: 5, cacheWrite: 1000000, output: 5, unpriced: []string{"claude-future-9"}}
	if got, want := usageLine(s, "manchete"), "<i>manchete: ≈ US$ 0,0000 (sem preço para claude-future-9) · 1.000.010 tokens: 5 entrada, 1.000.000 cache gravado, 5 saída</i>"; got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}
