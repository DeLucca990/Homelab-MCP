package radarr

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
	"strconv"
	"strings"
	"time"
)

const BaseURLEnv = "SERVER_URL"

const APIKeyEnv = "RADARR_API_KEY"

const ReadOnlyEnv = "HOMELAB_MCP_RADARR_READONLY"

const (
	defaultPort = "7878"

	apiPrefix = "/api/v3"

	// Everything but a lookup is answered from Radarr's local database.
	requestTimeout = 10 * time.Second

	// A lookup is proxied to TMDB over the internet, so it inherits that
	// latency and needs a budget of its own.
	lookupTimeout = 30 * time.Second
)

// ErrNotConfigured means the environment names no Radarr. Distinct from a
// Radarr that is configured and unreachable, which says so explicitly.
var ErrNotConfigured = errors.New("radarr is not configured on this server")

// ErrUnreachable means the host answered nothing at all — down, wrong port, or
// a firewall in between.
var ErrUnreachable = errors.New("radarr is not reachable")

// Configured reports whether both variables are set. The tools are registered
// on this alone: a Radarr that is configured but down should produce a tool
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

// normalizeBaseURL turns what an operator would type into something requests
// can be built on.
//
// The port default is the one judgement call here: a bare http host means a
// server reached directly, so Radarr's own 7878 is filled in and SERVER_URL can
// stay service-agnostic. A URL that names a port, carries a path (a
// reverse-proxy subfolder install) or uses https is left exactly as written —
// those all reach Radarr through something else, and appending 7878 would break
// them.
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
				"(Radarr → Settings → General → Security)",
			ErrNotConfigured, APIKeyEnv)
	}

	return &client{http: sharedTransport, base: base, key: key}, nil
}

func (c *client) get(ctx context.Context, path string, query url.Values, out any) error {
	return c.do(ctx, http.MethodGet, path, query, nil, out, requestTimeout)
}

func (c *client) post(ctx context.Context, path string, body, out any) error {
	return c.do(ctx, http.MethodPost, path, nil, body, out, requestTimeout)
}

func (c *client) delete(ctx context.Context, path string, query url.Values) error {
	return c.do(ctx, http.MethodDelete, path, query, nil, nil, requestTimeout)
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

	req.Header.Set("X-Api-Key", c.key)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("radarr at %s did not answer within %s", c.base, timeout)
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
		return fmt.Errorf("radarr answered %s %s with something that is not JSON — "+
			"is %s really a Radarr? (%w)", method, path, c.base, err)
	}
	return nil
}

// apiError turns a failure into something an operator can act on. Radarr's own
// rejections arrive as a JSON array of validation failures, and those messages
// ("This movie has already been added", "Folder is not writable") are the whole
// answer — surfacing "returned 400" instead would throw them away.
func (c *client) apiError(resp *http.Response, method, path string) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))

	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("radarr rejected the API key (%s) — check %s against "+
			"Settings → General → Security", resp.Status, APIKeyEnv)
	case http.StatusNotFound:
		if strings.Contains(resp.Header.Get("Content-Type"), "html") {
			return fmt.Errorf("no Radarr API at %s%s — the host answered with a web page, "+
				"so %s is probably pointing at the wrong port or is missing the url base",
				c.base, apiPrefix, BaseURLEnv)
		}
	}

	if msgs := validationMessages(raw); len(msgs) > 0 {
		return fmt.Errorf("radarr refused %s %s: %s", method, path, strings.Join(msgs, "; "))
	}

	return fmt.Errorf("radarr returned %s for %s %s%s",
		resp.Status, method, path, snippet(raw))
}

// Radarr reports failures in two shapes: an array of per-field validation
// errors, and a single object for everything else.
func validationMessages(raw []byte) []string {
	var failures []struct {
		PropertyName string `json:"propertyName"`
		ErrorMessage string `json:"errorMessage"`
	}
	if err := json.Unmarshal(raw, &failures); err == nil {
		var out []string
		for _, f := range failures {
			switch {
			case f.ErrorMessage == "":
				continue
			case f.PropertyName == "":
				out = append(out, f.ErrorMessage)
			default:
				out = append(out, fmt.Sprintf("%s: %s", f.PropertyName, f.ErrorMessage))
			}
		}
		return out
	}

	var single struct {
		Message     string `json:"message"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(raw, &single); err == nil && single.Message != "" {
		return []string{single.Message}
	}
	return nil
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

// Radarr sends timespans as [d.]hh:mm:ss[.fffffff] — Go's own duration parser
// does not read that shape, and the value ("how long until this download
// finishes") is worth having as a number rather than a string.
func parseSpanSeconds(s string) uint64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}

	days := 0
	if dot := strings.Index(s, "."); dot > 0 && !strings.Contains(s[:dot], ":") {
		d, err := strconv.Atoi(s[:dot])
		if err != nil {
			return 0
		}
		days, s = d, s[dot+1:]
	}
	if frac := strings.Index(s, "."); frac > 0 {
		s = s[:frac]
	}

	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return 0
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	sec, err3 := strconv.Atoi(parts[2])
	if err1 != nil || err2 != nil || err3 != nil || h < 0 || m < 0 || sec < 0 {
		return 0
	}

	return uint64(days*86400 + h*3600 + m*60 + sec)
}

// secondsSince ignores the zero timestamps Radarr writes for events that never
// happened, which parse fine and would render as decades of age.
func secondsSince(stamp string) uint64 {
	if stamp == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339, stamp)
	if err != nil || t.IsZero() || t.Year() <= 1 {
		return 0
	}
	d := time.Since(t)
	if d < 0 {
		return 0
	}
	return uint64(d.Seconds())
}

// secondsUntil is the same in the other direction, for estimates that lie in
// the future and go stale the moment they are in the past.
func secondsUntil(stamp string) uint64 {
	if stamp == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339, stamp)
	if err != nil || t.IsZero() || t.Year() <= 1 {
		return 0
	}
	d := time.Until(t)
	if d < 0 {
		return 0
	}
	return uint64(d.Seconds())
}
