package mcp

import (
	"context"
	"fmt"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DeLucca990/homelab-mcp/internal/containers"
)

// DOCKER LOGS TOOL
//
// Read-only, so no allowlist and no confirmation: it changes nothing. It exists
// because the diagnostic tools stop one question short — knowing a container
// was OOM-killed or is unhealthy is only useful if you can then read what it
// said on the way down.
type logsInput struct {
	Container    string `json:"container" jsonschema:"name of the container to read logs from"`
	Tail         int    `json:"tail,omitempty" jsonschema:"how many lines from the END of the log to return; default 100, maximum 2000"`
	SinceSeconds int    `json:"since_seconds,omitempty" jsonschema:"only return lines newer than this many seconds ago; useful for isolating what happened around a restart"`
	Timestamps   bool   `json:"timestamps,omitempty" jsonschema:"prefix each line with the time docker recorded it. Enable this when you need to tell a live problem from an old one — without it there is no way to know whether an error is from a minute ago or a month ago"`
}

func handleLogs(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in logsInput,
) (*sdk.CallToolResult, containers.LogsResult, error) {
	if in.Container == "" {
		return nil, containers.LogsResult{}, fmt.Errorf("'container' is required")
	}

	out, err := containers.GetLogs(ctx, in.Container, in.Tail, in.SinceSeconds, in.Timestamps)
	if err != nil {
		return nil, containers.LogsResult{}, err
	}

	return &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.TextContent{Text: renderLogs(out)},
		},
	}, out, nil
}

func renderLogs(r containers.LogsResult) string {
	var b strings.Builder

	fmt.Fprintf(&b, "%s — last %d lines", r.Container, r.LineCount)
	if r.SinceSeconds > 0 {
		fmt.Fprintf(&b, " from the last %ds", r.SinceSeconds)
	}
	b.WriteString("\n")

	if r.Lines != "" {
		b.WriteString("\n")
		for line := range strings.SplitSeq(r.Lines, "\n") {
			b.WriteString(compactTimestamp(line))
			b.WriteString("\n")
		}
	}

	for _, w := range r.Warnings {
		fmt.Fprintf(&b, "\nwarning: %s\n", w)
	}

	return b.String()
}

// compactTimestamp trims the nanoseconds off docker's RFC3339 log prefix.
// `2026-08-07T20:41:32.123456789Z` becomes `2026-08-07T20:41:32Z` — the same
// information for a reader, at two thirds the width, on every single line.
func compactTimestamp(line string) string {
	stamp, rest, found := strings.Cut(line, " ")
	if !found || len(stamp) < 20 || stamp[4] != '-' || stamp[10] != 'T' {
		return line
	}
	dot := strings.IndexByte(stamp, '.')
	if dot < 0 {
		return line
	}
	return stamp[:dot] + "Z " + rest
}
