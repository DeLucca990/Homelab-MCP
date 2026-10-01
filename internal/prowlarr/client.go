package prowlarr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const BaseURLEnv = "SERVER_URL"

const APIKeyEnv = "PROWLARR_API_KEY"

const ReadOnlyEnv = "HOMELAB_MCP_PROWLARR_READONLY"

const (
	defaultPort = "9696"

	apiPrefix = "/api/v1"

	// Everything but a search or a test is answered from Prowlarr's own
	// database.
	requestTimeout = 15 * time.Second

	// A search or a test goes out to the indexers themselves — over the
	// internet, some of them behind Cloudflare and a FlareSolverr that takes
	// its time. Prowlarr has its own per-indexer timeouts; this is the budget
	// for the slowest of them, not the typical one.
	indexerTimeout = 2 * time.Minute
)

// ErrNotConfigured means the environment names no Prowlarr. Distinct from a
// Prowlarr that is configured and unreachable, which says so explicitly.
var ErrNotConfigured = errors.New("prowlarr is not configured on this server")

// ErrUnreachable means the host answered nothing at all — down, wrong port, or
// a firewall in between.
var ErrUnreachable = errors.New("prowlarr is not reachable")

// Configured reports whether both variables are set. The tools are registered
// on this alone: a Prowlarr that is configured but down should produce a tool
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

// normalizeBaseURL applies the rule every service module shares: a bare http
// host means a Prowlarr reached directly, so its own 9696 is filled in. A URL
// that names a port, carries a path or uses https is left exactly as written.
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
				"(Prowlarr → Settings → General → Security → API Key)",
			ErrNotConfigured, APIKeyEnv)
	}

	return &client{http: sharedTransport, base: base, key: key}, nil
}

func (c *client) get(ctx context.Context, path string, query url.Values, out any) error {
	return c.do(ctx, http.MethodGet, path, query, nil, out, requestTimeout)
}

func (c *client) do(
	ctx context.Context,
	method, path string,
	query url.Values,
	body, out any,
	timeout time.Duration,
) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	target := c.base + apiPrefix + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}

	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, target, payload)
	if err != nil {
		return err
	}

	// Header rather than the apikey query parameter: a URL ends up in error
	// messages and proxy logs, a header does not.
	req.Header.Set("X-Api-Key", c.key)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("prowlarr at %s did not answer within %s", c.base, timeout)
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
		return fmt.Errorf("prowlarr answered %s %s with something that is not JSON — "+
			"is %s really a Prowlarr? (%w)", method, path, c.base, err)
	}
	return nil
}

// statusError keeps the body of a refusal, for the one endpoint whose failure
// carries the answer: testall reports every indexer's result with a 400 as
// soon as any of them failed.
type statusError struct {
	status int
	body   []byte
	err    error
}

func (e *statusError) Error() string { return e.err.Error() }
func (e *statusError) Unwrap() error { return e.err }

// apiError turns a failure into something an operator can act on. Prowlarr's
// rejections are the Servarr shape — an array of validation failures — and
// their messages ("Unable to connect to indexer, check the log above the
// ValidationFailure for more details") are the answer.
func (c *client) apiError(resp *http.Response, method, path string) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	wrap := func(err error) error { return &statusError{status: resp.StatusCode, body: raw, err: err} }

	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return wrap(fmt.Errorf("prowlarr rejected the API key (%s) — check %s against "+
			"Settings → General → Security", resp.Status, APIKeyEnv))
	case http.StatusNotFound:
		if strings.Contains(resp.Header.Get("Content-Type"), "html") {
			return wrap(fmt.Errorf("no Prowlarr API at %s%s — the host answered with a web page, "+
				"so %s is probably pointing at the wrong port or is missing the url base",
				c.base, apiPrefix, BaseURLEnv))
		}
	}

	if msgs := validationMessages(raw); len(msgs) > 0 {
		return wrap(fmt.Errorf("prowlarr refused %s %s: %s", method, path, strings.Join(msgs, "; ")))
	}
	return wrap(fmt.Errorf("prowlarr returned %s for %s %s%s", resp.Status, method, path, snippet(raw)))
}

// Prowlarr reports failures in two shapes: an array of per-field validation
// errors, and a single object for everything else.
func validationMessages(raw []byte) []string {
	var failures []validationFailureJSON
	if err := json.Unmarshal(raw, &failures); err == nil {
		var out []string
		for _, f := range failures {
			if m := f.String(); m != "" {
				out = append(out, m)
			}
		}
		return out
	}

	var single struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &single); err == nil && single.Message != "" {
		return []string{single.Message}
	}
	return nil
}

type validationFailureJSON struct {
	PropertyName string `json:"propertyName"`
	ErrorMessage string `json:"errorMessage"`
	IsWarning    bool   `json:"isWarning"`
}

func (f validationFailureJSON) String() string {
	switch {
	case f.ErrorMessage == "":
		return ""
	case f.PropertyName == "":
		return f.ErrorMessage
	default:
		return fmt.Sprintf("%s: %s", f.PropertyName, f.ErrorMessage)
	}
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

// secondsSince ignores the zero timestamps Servarr writes for events that
// never happened, which parse fine and would render as decades of age.
func secondsSince(stamp string) uint64 {
	t, ok := parseTime(stamp)
	if !ok {
		return 0
	}
	d := time.Since(t)
	if d < 0 {
		return 0
	}
	return uint64(d.Seconds())
}

// secondsUntil is the same in the other direction, for a backoff that ends in
// the future and means nothing once it is in the past.
func secondsUntil(stamp string) uint64 {
	t, ok := parseTime(stamp)
	if !ok {
		return 0
	}
	d := time.Until(t)
	if d < 0 {
		return 0
	}
	return uint64(d.Seconds())
}

func parseTime(stamp string) (time.Time, bool) {
	stamp = strings.TrimSpace(stamp)
	if stamp == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		// .NET DateTime with no zone: UTC in practice.
		t, err = time.ParseInLocation("2006-01-02T15:04:05", strings.SplitN(stamp, ".", 2)[0], time.UTC)
	}
	if err != nil || t.IsZero() || t.Year() <= 1 {
		return time.Time{}, false
	}
	return t, true
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
