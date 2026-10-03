package telegram

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Reformats the server's approval text (question, indented details, paragraphs)
// for a chat. The wording is never changed: what is approved is what the server wrote.

const captionLimit = 1024

var (
	// Lower-case labels only, so a title like "Dune: Part Two" is not taken for one.
	detailLabel = regexp.MustCompile(`^([a-z][a-z ]{0,24}):\s+(.*)$`)

	idSuffix = regexp.MustCompile(`^(.*?)\s{2,}\[([^\]]+)\]$`)

	posterLabel = regexp.MustCompile(`^(?:cover|poster):\s+(https?://\S+)$`)
)

type formattedApproval struct {
	html   string
	poster string // the cover the server linked, when it did
}

func formatApproval(message string) formattedApproval {
	var (
		out    formattedApproval
		blocks = strings.Split(strings.ReplaceAll(strings.TrimSpace(message), "\r\n", "\n"), "\n\n")
		parts  []string
	)

	question, rest, _ := strings.Cut(blocks[0], "\n")
	parts = append(parts, "🔐 <b>"+escapeText(strings.TrimSpace(question))+"</b>")
	if strings.TrimSpace(rest) != "" {
		blocks[0] = rest
	} else {
		blocks = blocks[1:]
	}

	// Commands stay monospace and verbatim: reading exactly what will run is the point.
	verbatim := strings.Contains(strings.ToLower(question), "command")

	for _, block := range blocks {
		if strings.TrimSpace(block) == "" {
			continue
		}
		if isDetailBlock(block) {
			if s := formatDetails(block, verbatim, &out.poster); s != "" {
				parts = append(parts, s)
			}
			continue
		}
		parts = append(parts, escapeText(strings.TrimSpace(block)))
	}

	out.html = strings.Join(parts, "\n\n")
	return out
}

func isDetailBlock(block string) bool {
	for _, line := range strings.Split(block, "\n") {
		if strings.TrimSpace(line) != "" && !strings.HasPrefix(line, "    ") && !strings.HasPrefix(line, "\t") {
			return false
		}
	}
	return true
}

func formatDetails(block string, verbatim bool, poster *string) string {
	var lines []string
	first := true

	for _, raw := range strings.Split(block, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}

		if m := posterLabel.FindStringSubmatch(line); m != nil {
			*poster = m[1]
			continue
		}

		switch m := detailLabel.FindStringSubmatch(line); {
		case m != nil && !verbatim:
			lines = append(lines, "• <b>"+escapeText(capitalize(m[1]))+":</b> "+formatValue(m[2]))
		case verbatim || strings.HasPrefix(line, `"`):
			lines = append(lines, "<code>"+escapeText(line)+"</code>")
		case strings.HasPrefix(line, "("):
			lines = append(lines, "<i>"+escapeText(line)+"</i>")
		case first:
			lines = append(lines, formatTitle(line))
		default:
			lines = append(lines, escapeText(line))
		}
		first = false
	}
	return strings.Join(lines, "\n")
}

func formatTitle(line string) string {
	if m := idSuffix.FindStringSubmatch(line); m != nil {
		return "<b>" + escapeText(m[1]) + "</b> · " + escapeText(m[2])
	}
	return "<b>" + escapeText(line) + "</b>"
}

func formatValue(v string) string {
	if strings.HasPrefix(v, "/") && !strings.ContainsAny(v, " ") {
		return "<code>" + escapeText(v) + "</code>"
	}
	return escapeText(v)
}

func capitalize(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[size:]
}
