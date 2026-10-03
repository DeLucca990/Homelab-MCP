package telegram

import (
	"html"
	"strings"
	"unicode/utf16"
)

// Telegram counts message length in UTF-16 code units.
const messageLimit = 4096

const chunkBudget = 3000

// Longer output is sent as a file.
const maxChunks = 4

func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}

func chunk(text string, budget int) []string {
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return nil
	}

	var (
		chunks []string
		cur    strings.Builder
		curLen int
	)
	flush := func() {
		if cur.Len() > 0 {
			chunks = append(chunks, strings.TrimRight(cur.String(), "\n"))
			cur.Reset()
			curLen = 0
		}
	}

	for _, line := range strings.SplitAfter(text, "\n") {
		n := utf16Len(line)
		if curLen+n > budget {
			flush()
		}
		for n > budget {
			head, tail := cutUTF16(line, budget)
			chunks = append(chunks, head)
			line, n = tail, utf16Len(tail)
		}
		cur.WriteString(line)
		curLen += n
	}
	flush()
	return chunks
}

func cutUTF16(s string, n int) (string, string) {
	used := 0
	for i, r := range s {
		w := utf16.RuneLen(r)
		if used+w > n {
			return s[:i], s[i:]
		}
		used += w
	}
	return s, ""
}

func pre(text string) string {
	return "<pre>" + escape(text) + "</pre>"
}

func escape(s string) string { return html.EscapeString(s) }
