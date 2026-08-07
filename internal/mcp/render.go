package mcp

import (
	"fmt"
	"strings"
	"time"
)

// Shared presentation helpers.
//
// Every tool here renders a fixed-width table, and each one used to carry its
// own copy of the measure-then-write pass. Adding a column meant remembering to
// widen an array literal in one file and not the others.

// emptyInput is the parameter type for tools that take no arguments.
type emptyInput struct{}

// ptr is needed for the optional-bool fields in sdk.ToolAnnotations, whose
// defaults are true — a plain bool could not express "explicitly false".
func ptr[T any](v T) *T { return new(v) }

type alignment int

const (
	alignLeft alignment = iota
	alignRight
)

type column struct {
	head  string
	align alignment
}

// table renders rows under head, padding every column to its widest cell.
//
// The final column is never padded: trailing whitespace is invisible to a
// reader and pure cost to a model, and the widest column is usually last
// (a mountpoint, a port list) precisely because it does not need aligning.
// Cells missing from a short row render blank rather than panicking.
func table(cols []column, rows [][]string) string {
	if len(cols) == 0 {
		return ""
	}

	// FIRST PASS: measure. There is no way to align the columns without first
	// knowing the widest content in each one.
	width := make([]int, len(cols))
	for i, c := range cols {
		width[i] = len(c.head)
	}
	for _, r := range rows {
		for i := range cols {
			if i < len(r) {
				width[i] = max(width[i], len(r[i]))
			}
		}
	}

	// SECOND PASS: write.
	var b strings.Builder
	write := func(cells []string) {
		for i, c := range cols {
			cell := ""
			if i < len(cells) {
				cell = cells[i]
			}
			if i == len(cols)-1 {
				b.WriteString(cell)
				break
			}
			if c.align == alignRight {
				fmt.Fprintf(&b, "%*s", width[i], cell)
			} else {
				fmt.Fprintf(&b, "%-*s", width[i], cell)
			}
			b.WriteString("  ")
		}
		b.WriteString("\n")
	}

	heads := make([]string, len(cols))
	for i, c := range cols {
		heads[i] = c.head
	}

	write(heads)
	for _, r := range rows {
		write(r)
	}

	return b.String()
}

// compactDuration keeps an age column to a few characters — a table is no
// place for "52h4m28.5s", which is what time.Duration would render.
func compactDuration(seconds uint64) string {
	if seconds == 0 {
		return "-"
	}
	d := time.Duration(seconds) * time.Second
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
	}
}
