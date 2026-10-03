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
		if p, ok := priceOf(model); !ok || p.input != want {
			t.Errorf("%s: got %+v, %v", model, p, ok)
		}
	}
	if _, ok := priceOf("claude-unknown-9"); ok {
		t.Error("an unknown model was priced")
	}
}

func TestSpendOfOneAttempt(t *testing.T) {
	s := spendOf(parseMessage(t, `{"model":"claude-sonnet-5-5","usage":{
		"input_tokens":1000,"output_tokens":500,
		"cache_read_input_tokens":10000,"cache_creation_input_tokens":2000}}`))

	// 1000×$2 + 500×$10 + 10000×$0.20 + 2000×$2×1.25, per million
	if !near(s.usd, 0.014) {
		t.Errorf("usd: %v", s.usd)
	}
	if s.input != 1000 || s.output != 500 || s.cacheRead != 10000 || s.cacheWrite != 2000 {
		t.Errorf("tokens: %+v", s)
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
