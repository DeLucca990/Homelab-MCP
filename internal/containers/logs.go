package containers

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	// Logs are capped like every other payload here. Because the default is to
	// read from the TAIL, what gets dropped on a very chatty container is the
	// oldest of the requested lines — the opposite end from where the useful
	// part of a log usually is.
	maxLogBytes = 16384

	defaultTailLines = 100
	maxTailLines     = 2000
)

type LogsResult struct {
	Container string `json:"container"`

	Lines     string `json:"lines" jsonschema:"stdout and stderr combined, oldest first, in the order docker recorded them. Docker reads the two streams over separate pipes, so lines written in the same instant may appear in a different order than the application wrote them; do not infer causality between an stdout and an stderr line from their position alone"`
	LineCount int    `json:"line_count"`

	Tail         int `json:"tail" jsonschema:"how many lines from the end were requested"`
	SinceSeconds int `json:"since_seconds,omitempty" jsonschema:"only lines newer than this many seconds ago were returned"`

	Truncated bool `json:"truncated,omitempty" jsonschema:"true when the output hit the size limit; the OLDEST lines were dropped, so what is shown is the most recent"`

	Warnings []string `json:"warnings,omitempty"`
}

// GetLogs returns what the container has written to stdout and stderr.
//
// This is deliberately not reachable through exec: most images log to stdout,
// which the daemon captures and which therefore exists nowhere in the
// container's own filesystem. `tail` inside the container would find nothing.
func GetLogs(ctx context.Context, name string, tail, sinceSeconds int, timestamps bool) (LogsResult, error) {
	res := LogsResult{Container: name}

	switch {
	case tail <= 0:
		tail = defaultTailLines
	case tail > maxTailLines:
		tail = maxTailLines
	}
	res.Tail = tail
	res.SinceSeconds = sinceSeconds

	client, err := newClient()
	if err != nil {
		return res, err
	}

	// Resolve the name against Docker's own listing so no caller-supplied
	// string reaches a request path.
	summaries, err := listContainers(ctx, client)
	if err != nil {
		return res, err
	}
	matched := matchNames(summaries, []string{name})
	if len(matched) == 0 {
		return res, fmt.Errorf("no container named %q", name)
	}
	id := matched[0].ID

	// Whether the stream is framed depends on how the container was created,
	// not on this request: with a TTY docker sends raw bytes instead.
	var info inspectResult
	if err := get(ctx, client, "/containers/"+id+"/json", &info); err != nil {
		return res, err
	}

	path := fmt.Sprintf("/containers/%s/logs?stdout=1&stderr=1&tail=%d", id, tail)
	if timestamps {
		path += "&timestamps=1"
	}
	if sinceSeconds > 0 {
		path += fmt.Sprintf("&since=%d", time.Now().Add(-time.Duration(sinceSeconds)*time.Second).Unix())
	}

	body, err := getStream(ctx, client, path)
	if err != nil {
		return res, err
	}
	defer body.Close()

	var buf bytes.Buffer
	if info.Config.Tty {
		// Raw stream: no framing to strip.
		n, err := io.Copy(&buf, io.LimitReader(body, maxLogBytes+1))
		if err != nil {
			return res, fmt.Errorf("reading logs: %w", err)
		}
		if n > maxLogBytes {
			buf.Truncate(maxLogBytes)
			res.Truncated = true
		}
	} else {
		// Framed stream: keep stdout and stderr interleaved, because for a log
		// the order between them is the diagnosis.
		res.Truncated, err = readFrames(body, maxLogBytes, func(_ byte, chunk []byte) {
			buf.Write(chunk)
		})
		if err != nil {
			return res, fmt.Errorf("reading logs: %w", err)
		}
	}

	res.Lines = strings.TrimRight(buf.String(), "\n")
	if res.Lines != "" {
		res.LineCount = strings.Count(res.Lines, "\n") + 1
	}

	if res.Lines == "" {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"%s has written nothing to stdout or stderr%s. Applications that log to a "+
				"file inside the container do not appear here — read that file with the "+
				"exec tool instead",
			name, sinceClause(sinceSeconds)))
	}
	if res.Truncated {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"output hit the size limit; the oldest of the %d requested lines were dropped. "+
				"Lower 'tail' for a cleaner window", tail))
	}

	return res, nil
}

func sinceClause(sinceSeconds int) string {
	if sinceSeconds <= 0 {
		return ""
	}
	return fmt.Sprintf(" in the last %s", time.Duration(sinceSeconds)*time.Second)
}
