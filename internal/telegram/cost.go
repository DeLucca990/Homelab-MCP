package telegram

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
)

// USD per million tokens, the Claude API's list prices as of 2026-10-17
// (platform.claude.com/docs/en/about-claude/pricing). Cache writes cost 1.25x
// input (5-minute TTL) or 2x (1 hour) on every model. Cache reads do not share
// one multiplier — 0.1x on most, 0.05x on Opus 5.5 and Sonnet 5.5, 0.025x on
// Fable and Mythos 5.1 — so each row states its own.
type price struct {
	input, output, cacheRead float64
}

var prices = map[string]price{
	"claude-fable-5-1":  {10, 50, 0.25},
	"claude-mythos-5-1": {10, 50, 0.25},
	"claude-fable-5":    {10, 50, 1.00},
	"claude-mythos-5":   {10, 50, 1.00},
	"claude-opus-5-5":   {4, 20, 0.20},
	"claude-opus-5":     {5, 25, 0.50},
	"claude-opus-4-8":   {5, 25, 0.50},
	"claude-opus-4-7":   {5, 25, 0.50},
	"claude-opus-4-6":   {5, 25, 0.50},
	"claude-opus-4-5":   {5, 25, 0.50},
	"claude-sonnet-5-5": {2, 10, 0.10},
	"claude-sonnet-5":   {2, 10, 0.20},
	"claude-sonnet-4-6": {3, 15, 0.30},
	"claude-sonnet-4-5": {3, 15, 0.30},
	"claude-haiku-5-5":  {0.10, 0.50, 0.01},
	"claude-haiku-4-5":  {1, 5, 0.10},
}

var longPrompts = map[string]struct {
	over  int64
	price price
}{
	"claude-haiku-5-5": {100_000, price{0.50, 2.50, 0.05}},
}

// Longest prefix, so "claude-opus-5-5" is not read as "claude-opus-5".
func priceOf(model string, prompt int64) (price, bool) {
	best := ""
	for id := range prices {
		if strings.HasPrefix(model, id) && len(id) > len(best) {
			best = id
		}
	}
	p, ok := prices[best]
	if long, has := longPrompts[best]; has && prompt > long.over {
		p = long.price
	}
	return p, ok
}

type spend struct {
	input, output, cacheRead, cacheWrite int64
	usd                                  float64
	steps                                int      // responses
	models                               []string // that answered
	unpriced                             []string // models the table has no price for
}

func (s *spend) add(o spend) {
	s.input += o.input
	s.output += o.output
	s.cacheRead += o.cacheRead
	s.cacheWrite += o.cacheWrite
	s.usd += o.usd
	s.steps += o.steps
	s.models = union(s.models, o.models)
	s.unpriced = union(s.unpriced, o.unpriced)
}

func union(a, b []string) []string {
	for _, v := range b {
		if !contains(a, v) {
			a = append(a, v)
		}
	}
	return a
}

// With a fallback, top-level usage covers only the answering attempt; each
// attempt in usage.iterations is priced at its own model's rates.
func spendOf(resp *anthropic.BetaMessage) spend {
	total := spend{steps: 1, models: []string{string(resp.Model)}}
	if len(resp.Usage.Iterations) == 0 {
		u := resp.Usage
		total.add(priced(string(resp.Model), u.InputTokens, u.OutputTokens, u.CacheReadInputTokens,
			u.CacheCreationInputTokens, u.CacheCreation.Ephemeral5mInputTokens, u.CacheCreation.Ephemeral1hInputTokens))
		return total
	}

	for _, it := range resp.Usage.Iterations {
		model := string(it.Model)
		if model == "" {
			model = string(resp.Model)
		}
		total.add(priced(model, it.InputTokens, it.OutputTokens, it.CacheReadInputTokens,
			it.CacheCreationInputTokens, it.CacheCreation.Ephemeral5mInputTokens, it.CacheCreation.Ephemeral1hInputTokens))
	}
	return total
}

func usageLine(s spend, label string) string {
	categories := []struct {
		tokens int64
		name   string
	}{
		{s.input, "entrada"},
		{s.cacheRead, "cache lido"},
		{s.cacheWrite, "cache gravado"},
		{s.output, "saída"},
	}

	var total int64
	var parts []string
	for _, c := range categories {
		if c.tokens > 0 {
			total += c.tokens
			parts = append(parts, thousands(c.tokens)+" "+c.name)
		}
	}

	line := "≈ US$ " + strings.Replace(fmt.Sprintf("%.4f", s.usd), ".", ",", 1)
	if len(s.unpriced) > 0 {
		line += " (sem preço para " + strings.Join(s.unpriced, ", ") + ")"
	}
	if total > 0 {
		line += " · " + thousands(total) + " tokens: " + strings.Join(parts, ", ")
	}
	if label != "" {
		line = label + ": " + line
	}
	return "<i>" + escape(line) + "</i>"
}

// thousands writes n the Brazilian way, 12.345.
func thousands(n int64) string {
	digits := strconv.FormatInt(n, 10)
	var b strings.Builder
	for i, d := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(d)
	}
	return b.String()
}

func priced(model string, input, output, cacheRead, cacheWrite, write5m, write1h int64) spend {
	s := spend{input: input, output: output, cacheRead: cacheRead, cacheWrite: cacheWrite}

	p, ok := priceOf(model, input+cacheRead+cacheWrite)
	if !ok {
		s.unpriced = []string{model}
		return s
	}

	if write5m+write1h == 0 {
		write5m = cacheWrite
	}

	s.usd = (float64(input)*p.input +
		float64(output)*p.output +
		float64(cacheRead)*p.cacheRead +
		float64(write5m)*p.input*1.25 +
		float64(write1h)*p.input*2) / 1e6
	return s
}
