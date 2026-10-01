package bazarr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const BaseURLEnv = "SERVER_URL"

const APIKeyEnv = "BAZARR_API_KEY"

const ReadOnlyEnv = "HOMELAB_MCP_BAZARR_READONLY"

const (
	defaultPort = "6767"

	apiPrefix = "/api"

	// Everything but a provider search is answered from Bazarr's own database.
	requestTimeout = 15 * time.Second

	// A provider search fans out to every enabled subtitle site over the
	// internet, one after another where a site throttles, and Bazarr answers
	// only once all of them have. A minute is ordinary; the budget is for the
	// slow day, not the typical one.
	providerTimeout = 3 * time.Minute
)

// ErrNotConfigured means the environment names no Bazarr. Distinct from a
// Bazarr that is configured and unreachable, which says so explicitly.
var ErrNotConfigured = errors.New("bazarr is not configured on this server")

// ErrUnreachable means the host answered nothing at all — down, wrong port, or
// a firewall in between.
var ErrUnreachable = errors.New("bazarr is not reachable")

// Configured reports whether both variables are set. The tools are registered
// on this alone: a Bazarr that is configured but down should produce a tool
// call that fails with a reason, not a server that silently has no tools.
func Configured() bool {
	return os.Getenv(BaseURLEnv) != "" && os.Getenv(APIKeyEnv) != ""
}

// ReadOnly reports whether the operator asked for monitoring without actions.
func ReadOnly() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(ReadOnlyEnv))) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// BaseURL is the normalized address, for the log line and the tool
// descriptions. It carries no credential, so it is safe to print.
func BaseURL() (string, error) { return normalizeBaseURL(os.Getenv(BaseURLEnv)) }

// normalizeBaseURL applies the same rule as the *arr modules: a bare http host
// means a Bazarr reached directly, so its own 6767 is filled in. A URL that
// names a port, carries a path or uses https is left exactly as written.
func normalizeBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("%w: %s is not set", ErrNotConfigured, BaseURLEnv)
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	raw = strings.TrimRight(raw, "/")

	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("%s is not a valid URL: %w", BaseURLEnv, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("%s must be an http or https URL, got %q", BaseURLEnv, u.Scheme)
	}
	if u.Host == "" {
		return "", fmt.Errorf("%s names no host: %q", BaseURLEnv, raw)
	}

	if u.Scheme == "http" && u.Port() == "" && u.Path == "" {
		u.Host = net.JoinHostPort(u.Hostname(), defaultPort)
	}

	return u.String(), nil
}

type client struct {
	http *http.Client
	base string
	key  string
}

var sharedTransport = &http.Client{}

func newClient() (*client, error) {
	base, err := normalizeBaseURL(os.Getenv(BaseURLEnv))
	if err != nil {
		return nil, err
	}

	key := strings.TrimSpace(os.Getenv(APIKeyEnv))
	if key == "" {
		return nil, fmt.Errorf(
			"%w: %s is not set in the environment of this server process "+
				"(Bazarr → Settings → General → Security → API Key)",
			ErrNotConfigured, APIKeyEnv)
	}

	return &client{http: sharedTransport, base: base, key: key}, nil
}

func (c *client) get(ctx context.Context, path string, query url.Values, out any) error {
	return c.do(ctx, http.MethodGet, path, query, nil, out, requestTimeout)
}

// Bazarr's writes take form fields, not JSON — its API is a thin layer over
// the forms its own web UI submits, and that is the shape it is sent here too.
func (c *client) send(
	ctx context.Context,
	method, path string,
	form url.Values,
	timeout time.Duration,
) error {
	return c.do(ctx, method, path, nil, form, nil, timeout)
}

func (c *client) do(
	ctx context.Context,
	method, path string,
	query, form url.Values,
	out any,
	timeout time.Duration,
) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	target := c.base + apiPrefix + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}

	var payload io.Reader
	if form != nil {
		payload = strings.NewReader(form.Encode())
	}

	req, err := http.NewRequestWithContext(ctx, method, target, payload)
	if err != nil {
		return err
	}

	// Header rather than the apikey query parameter Bazarr also accepts: a URL
	// ends up in error messages and proxy logs, a header does not.
	req.Header.Set("X-API-KEY", c.key)
	req.Header.Set("Accept", "application/json")
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("bazarr at %s did not answer within %s", c.base, timeout)
		}
		return fmt.Errorf("%w at %s: %v", ErrUnreachable, c.base, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return c.apiError(resp, method, path)
	}
	if out == nil {
		return nil
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("bazarr answered %s %s with something that is not JSON — "+
			"is %s really a Bazarr? (%w)", method, path, c.base, err)
	}
	return nil
}

// apiError turns a failure into something an operator can act on. Bazarr's
// own refusals are a sentence — "Movie file not found. Path mapping issue?" —
// sent as a JSON string, and that sentence is the whole answer.
func (c *client) apiError(resp *http.Response, method, path string) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))

	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("bazarr rejected the API key (%s) — check %s against "+
			"Settings → General → Security", resp.Status, APIKeyEnv)
	case http.StatusNotFound:
		if strings.Contains(resp.Header.Get("Content-Type"), "html") {
			return fmt.Errorf("no Bazarr API at %s%s — the host answered with a web page, "+
				"so %s is probably pointing at the wrong port or is missing the url base",
				c.base, apiPrefix, BaseURLEnv)
		}
	}

	if msg := bazarrMessage(raw); msg != "" {
		return fmt.Errorf("bazarr refused %s %s (%s): %s", method, path, resp.Status, msg)
	}
	return fmt.Errorf("bazarr returned %s for %s %s%s", resp.Status, method, path, snippet(raw))
}

// Bazarr answers a refusal either with a bare JSON string — the handler's own
// message — or, for anything the framework rejected first, an object with a
// "message" field.
func bazarrMessage(raw []byte) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strings.TrimSpace(s)
	}
	var obj struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil {
		return strings.TrimSpace(obj.Message)
	}
	return ""
}

func snippet(raw []byte) string {
	s := strings.TrimSpace(string(raw))
	if s == "" {
		return ""
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return ": " + s
}

// --- shared decoding helpers ---------------------------------------------

// Bazarr's start time is a Python time.time() — seconds since the epoch, as a
// float — where the *arrs send RFC3339.
func secondsSinceEpoch(epoch float64) uint64 {
	if epoch <= 0 || math.IsNaN(epoch) {
		return 0
	}
	d := time.Since(time.Unix(int64(epoch), 0))
	if d < 0 {
		return 0
	}
	return uint64(d.Seconds())
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// Bazarr's forms read booleans as the strings Python prints them as.
func pyBool(v bool) string {
	if v {
		return "True"
	}
	return "False"
}
