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
	"os"
	"strings"
	"time"
)

// AllowlistEnv names the environment variable that enables execution at all.
// It holds a comma-separated list of container names.
//
// Unset or empty means the exec tool is not registered: the model cannot call
// what does not exist, and a default install stays entirely read-only. This is
// the only layer of this feature that cannot be bypassed by a confused client,
// a client that auto-approves everything, or a model under prompt injection —
// so it is the one that decides what is reachable at all.
const AllowlistEnv = "HOMELAB_MCP_EXEC_ALLOW_CONTAINER_NAMES"

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

// ExecAllowlist returns the container names execution is permitted for.
func ExecAllowlist() []string { return parseAllowlist(os.Getenv(AllowlistEnv)) }

// ExecEnabled reports whether any container may be executed in at all.
func ExecEnabled() bool { return len(ExecAllowlist()) > 0 }

func execAllowed(name string) bool {
	for _, allowed := range ExecAllowlist() {
		if strings.EqualFold(allowed, name) {
			return true
		}
	}
	return false
}

// Fingerprint identifies a specific (container, command) pair. It is carried
// across the confirmation round trip so that what gets executed is provably the
// same thing the user approved — approving `ls` and running `rm` would
// otherwise be a matter of the retry carrying different arguments.
func Fingerprint(container string, command []string) string {
	h := sha256.New()
	h.Write([]byte(container))
	for _, arg := range command {
		binary.Write(h, binary.BigEndian, uint32(len(arg)))
		h.Write([]byte(arg))
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// Exec runs a command inside a container and returns its output.
//
// The command is an argv vector, never a shell string: Docker execs it
// directly, so shell metacharacters have no meaning unless the caller
// explicitly asks for a shell (["sh","-c",...]) — which then shows up verbatim
// in the confirmation the user sees.
func Exec(ctx context.Context, container string, command []string, timeout time.Duration) (ExecResult, error) {
	res := ExecResult{Container: container, Command: command}

	if len(command) == 0 {
		return res, errors.New("command is empty")
	}
	if !execAllowed(container) {
		return res, fmt.Errorf("%w: commands may not be run in %q. Allowed: %s",
			ErrNotAllowed, container, strings.Join(ExecAllowlist(), ", "))
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

	// Resolve the name against the daemon's own listing rather than putting a
	// caller-supplied string in a request path.
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

// readFrames walks Docker's multiplexed stream. When a container runs without
// a TTY, each frame is an 8-byte header — stream type, three padding bytes,
// then a big-endian payload length — followed by that many bytes.
//
// The per-frame callback is what lets exec split the streams apart while the
// log reader keeps them interleaved: both need the same framing, for opposite
// reasons.
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

// demux splits an exec stream into its two channels. Stream type 2 is stderr;
// 1 (and anything else) is stdout.
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

// getJSON is the GET counterpart used by exec; the read-only collector has its
// own helper with different error framing.
func getJSON(ctx context.Context, client *http.Client, path string, out any) error {
	return get(ctx, client, path, out)
}
