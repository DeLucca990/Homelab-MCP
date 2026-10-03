package telegram

import (
	"html"
	"regexp"
	"strings"
)

// Telegram rejects a whole message over one unknown or unclosed tag, so replies
// are rebuilt from supported tags; any other markup becomes visible text.

type htmlToken struct {
	text string // raw, unescaped; set when the token is text
	open string // a validated opening tag, verbatim
	name string // the tag's name, for opening and closing tags
	shut bool   // a closing tag
}

var (
	openTag  = regexp.MustCompile(`^<(b|strong|i|em|u|ins|s|strike|del|pre|blockquote|tg-spoiler)>|^<(code)(?: class="language-[\w+#-]+")?>|^<(a) href="[^"<>]*">`)
	closeTag = regexp.MustCompile(`^</(b|strong|i|em|u|ins|s|strike|del|pre|blockquote|tg-spoiler|code|a)>`)
	entity   = regexp.MustCompile(`^&(?:lt|gt|amp|quot|#\d{1,7}|#x[0-9a-fA-F]{1,6});`)
)

func parseHTML(s string) []htmlToken {
	var (
		tokens []htmlToken
		stack  []string
		text   strings.Builder
	)
	flushText := func() {
		if text.Len() > 0 {
			tokens = append(tokens, htmlToken{text: text.String()})
			text.Reset()
		}
	}
	closeTo := func(i int) {
		for len(stack) > i {
			tokens = append(tokens, htmlToken{name: stack[len(stack)-1], shut: true})
			stack = stack[:len(stack)-1]
		}
	}

	for i := 0; i < len(s); {
		rest := s[i:]
		switch rest[0] {
		case '<':
			if m := openTag.FindStringSubmatch(rest); m != nil {
				flushText()
				name := m[1] + m[2] + m[3]
				tokens = append(tokens, htmlToken{open: m[0], name: name})
				stack = append(stack, name)
				i += len(m[0])
				continue
			}
			if m := closeTag.FindStringSubmatch(rest); m != nil {
				flushText()
				for j := len(stack) - 1; j >= 0; j-- {
					if stack[j] == m[1] {
						closeTo(j)
						break
					}
				}
				i += len(m[0])
				continue
			}
		case '&':
			if m := entity.FindString(rest); m != "" {
				text.WriteString(html.UnescapeString(m))
				i += len(m)
				continue
			}
		}
		text.WriteByte(rest[0])
		i++
	}
	flushText()
	closeTo(0)
	return tokens
}

func plainText(tokens []htmlToken) string {
	var b strings.Builder
	for _, t := range tokens {
		b.WriteString(t.text)
	}
	return b.String()
}

// Tags open at a cut are closed and reopened in the next chunk.
func chunkHTML(tokens []htmlToken, budget int) []string {
	var (
		chunks []string
		cur    strings.Builder
		curLen int
		hasTxt bool
		stack  []htmlToken
	)
	write := func(s string) {
		cur.WriteString(s)
		curLen += utf16Len(s)
	}
	closers := func() string {
		var b strings.Builder
		for i := len(stack) - 1; i >= 0; i-- {
			b.WriteString("</" + stack[i].name + ">")
		}
		return b.String()
	}
	flush := func() {
		if hasTxt {
			chunks = append(chunks, strings.TrimSpace(cur.String()+closers()))
		}
		cur.Reset()
		curLen, hasTxt = 0, false
		for _, t := range stack {
			write(t.open)
		}
	}

	for _, t := range tokens {
		switch {
		case t.shut:
			write("</" + t.name + ">")
			stack = stack[:len(stack)-1]
		case t.open != "":
			write(t.open)
			stack = append(stack, t)
		default:
			text := t.text
			for text != "" {
				room := budget - curLen - utf16Len(closers())
				head, tail := cutUTF16(text, max(room, 0))
				if tail != "" {
					if nl := strings.LastIndexByte(head, '\n'); nl >= 0 {
						head, tail = text[:nl+1], text[nl+1:]
					} else if hasTxt {
						flush()
						continue
					}
				}
				if head == "" {
					flush()
					head, tail = cutUTF16(text, max(budget-curLen-utf16Len(closers()), 1))
				}
				write(escapeText(head))
				hasTxt = hasTxt || strings.TrimSpace(head) != ""
				text = tail
				if text != "" {
					flush()
				}
			}
		}
	}
	flush()
	return chunks
}

// html.EscapeString also escapes quotes, which Telegram does not need.
func escapeText(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}
