package containers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	maxOutputBytes = 16384

	defaultExecTimeout = 30 * time.Second
	maxExecTimeout     = 120 * time.Second
)

// ErrNotAllowed is shared by every state-changing operation, so the message
// stays generic and each caller names the operation it was refused for.
var ErrNotAllowed = errors.New("not permitted by this server's allowlist")

type ExecResult struct {
	Container string   `json:"container"`
	Command   []string `json:"command"`

	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout,omitempty"`
	Stderr   string `json:"stderr,omitempty"`

	Truncated  bool  `json:"truncated,omitempty" jsonschema:"true when output was cut at the size limit; what is shown is the beginning of the output"`
	DurationMS int64 `json:"duration_ms"`
	TimedOut   bool  `json:"timed_out,omitempty"`
}

// Fingerprint identifies a (container, command) pair across the confirmation
// round trip, so what runs is provably what the user approved — otherwise
// approving `ls` and running `rm` is just a matter of the retry carrying
// different arguments.
func Fingerprint(container string, command []string) string {
	h := sha256.New()
	h.Write([]byte(container))
	for _, arg := range command {
		binary.Write(h, binary.BigEndian, uint32(len(arg)))
		h.Write([]byte(arg))
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// Exec runs a command inside a container and returns its output. The command is
// an argv vector, never a shell string: Docker execs it directly, so shell
// metacharacters mean nothing unless the caller asks for a shell explicitly
// (["sh","-c",...]) — which then shows up verbatim in the confirmation.
func Exec(ctx context.Context, container string, command []string, timeout time.Duration) (ExecResult, error) {
	res := ExecResult{Container: container, Command: command}

	if len(command) == 0 {
		return res, errors.New("command is empty")
	}
	if !actionAllowed(container) {
		return res, fmt.Errorf("%w: commands may not be run in %q. Allowed: %s",
			ErrNotAllowed, container, strings.Join(ActionAllowlist(), ", "))
	}

	switch {
	case timeout <= 0:
		timeout = defaultExecTimeout
	case timeout > maxExecTimeout:
		timeout = maxExecTimeout
	}

	client, err := newClient()
	if err != nil {
		return res, err
	}

	// Resolved against the daemon's own listing, so no caller-supplied string
	// reaches a request path.
	summaries, err := listContainers(ctx, client)
	if err != nil {
		return res, err
	}
	matched := matchNames(summaries, []string{container})
	if len(matched) == 0 {
		return res, fmt.Errorf("no container named %q", container)
	}
	if matched[0].State != "running" {
		return res, fmt.Errorf("container %q is %s — it must be running to exec into",
			container, matched[0].State)
	}
	id := matched[0].ID

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()

	var created struct {
		ID string `json:"Id"`
	}
	err = postJSON(ctx, client, "/containers/"+id+"/exec", map[string]any{
		"Cmd":          command,
		"AttachStdout": true,
		"AttachStderr": true,
		"Tty":          false,
	}, &created)
	if err != nil {
		return res, err
	}

	body, err := postStream(ctx, client, "/exec/"+created.ID+"/start", map[string]any{
		"Detach": false,
		"Tty":    false,
	})
	if err != nil {
		return res, err
	}
	defer body.Close()

	stdout, stderr, truncated, err := demux(body, maxOutputBytes)
	res.Stdout, res.Stderr, res.Truncated = stdout, stderr, truncated
	res.DurationMS = time.Since(start).Milliseconds()

	if err != nil && ctx.Err() != nil {
		res.TimedOut = true
		res.ExitCode = -1
		return res, nil
	}
	if err != nil {
		return res, fmt.Errorf("reading command output: %w", err)
	}

	// Fetch the exit code separately; the stream carries no status.
	var inspectExec struct {
		ExitCode int  `json:"ExitCode"`
		Running  bool `json:"Running"`
	}
	if err := getJSON(ctx, client, "/exec/"+created.ID+"/json", &inspectExec); err != nil {
		return res, nil // output is already useful; the code is not worth failing over
	}
	res.ExitCode = inspectExec.ExitCode

	return res, nil
}

// readFrames walks Docker's multiplexed stream: without a TTY, each frame is an
// 8-byte header — stream type, three padding bytes, big-endian payload length —
// followed by that many bytes. The callback is what lets exec split the streams
// apart while the log reader keeps them interleaved.
func readFrames(r io.Reader, limit int, fn func(stream byte, chunk []byte)) (truncated bool, err error) {
	header := make([]byte, 8)
	total := 0

	for {
		if _, err := io.ReadFull(r, header); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return truncated, nil
			}
			return truncated, err
		}

		size := int(binary.BigEndian.Uint32(header[4:8]))
		if size == 0 {
			continue
		}

		remaining := limit - total
		if remaining <= 0 {
			return true, nil
		}

		read := min(size, remaining)
		chunk := make([]byte, read)
		if _, err := io.ReadFull(r, chunk); err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
				return truncated, err
			}
		}
		total += read
		if read < size {
			truncated = true
		}

		fn(header[0], chunk)

		if truncated {
			return true, nil
		}
	}
}

// Stream type 2 is stderr; 1 (and anything else) is stdout.
func demux(r io.Reader, limit int) (stdout, stderr string, truncated bool, err error) {
	var out, errOut bytes.Buffer

	truncated, err = readFrames(r, limit, func(stream byte, chunk []byte) {
		if stream == 2 {
			errOut.Write(chunk)
		} else {
			out.Write(chunk)
		}
	})

	return out.String(), errOut.String(), truncated, err
}

func postJSON(ctx context.Context, client *http.Client, path string, body, out any) error {
	resp, err := post(ctx, client, path, body)
	if err != nil {
		return err
	}
	defer resp.Close()
	return json.NewDecoder(resp).Decode(out)
}

func postStream(ctx context.Context, client *http.Client, path string, body any) (io.ReadCloser, error) {
	return post(ctx, client, path, body)
}

func post(ctx context.Context, client *http.Client, path string, body any) (io.ReadCloser, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://docker"+path, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("docker API timed out")
		}
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("docker API %s returned %s: %s",
			path, resp.Status, strings.TrimSpace(string(msg)))
	}

	return resp.Body, nil
}

func getJSON(ctx context.Context, client *http.Client, path string, out any) error {
	return get(ctx, client, path, out)
}
