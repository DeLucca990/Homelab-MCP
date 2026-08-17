package jellyfin

import (
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

const APIKeyEnv = "JELLYFIN_API_KEY"

const (
	defaultPort    = "8096"
	requestTimeout = 10 * time.Second
	clientName     = "Homelab MCP"
	clientVersion  = "1.0"
)

var ErrNotConfigured = errors.New("jellyfin is not configured on this server")

var ErrUnreachable = errors.New("jellyfin is not reachable")

var ErrForbidden = errors.New("jellyfin refused the API key for an admin-only endpoint")

func Configured() bool {
	return os.Getenv(BaseURLEnv) != "" && os.Getenv(APIKeyEnv) != ""
}

func BaseURL() (string, error) { return normalizeBaseURL(os.Getenv(BaseURLEnv)) }

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
				"(Jellyfin → Dashboard → API Keys)",
			ErrNotConfigured, APIKeyEnv)
	}

	return &client{http: sharedTransport, base: base, key: key}, nil
}

func (c *client) get(ctx context.Context, path string, query url.Values, out any) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	target := c.base + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}

	req.Header.Set("Authorization", fmt.Sprintf(
		`MediaBrowser Client=%q, Device=%q, DeviceId=%q, Version=%q, Token=%q`,
		clientName, clientName, "homelab-mcp", clientVersion, c.key))
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("jellyfin at %s did not answer within %s", c.base, requestTimeout)
		}
		return fmt.Errorf("%w at %s: %v", ErrUnreachable, c.base, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return c.apiError(resp, path)
	}
	if out == nil {
		return nil
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("jellyfin answered GET %s with something that is not JSON — "+
			"is %s really a Jellyfin? (%w)", path, c.base, err)
	}
	return nil
}

func (c *client) apiError(resp *http.Response, path string) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))

	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return fmt.Errorf("jellyfin rejected the API key (%s) — check %s against "+
			"Dashboard → API Keys", resp.Status, APIKeyEnv)

	case http.StatusForbidden:
		return fmt.Errorf("%w: GET %s needs an administrator key, and this one was "+
			"accepted but not allowed. Keys issued from Dashboard → API Keys have those "+
			"rights; a key taken from a user session does not", ErrForbidden, path)

	case http.StatusNotFound:
		if strings.Contains(resp.Header.Get("Content-Type"), "html") {
			return fmt.Errorf("no Jellyfin API at %s — the host answered with a web page, "+
				"so %s is probably pointing at the wrong port or is missing the base url",
				c.base+path, BaseURLEnv)
		}
	}

	body := strings.TrimSpace(string(raw))
	if body != "" {
		if len(body) > 200 {
			body = body[:200] + "…"
		}
		body = ": " + body
	}
	return fmt.Errorf("jellyfin returned %s for GET %s%s", resp.Status, path, body)
}

// --- shared decoding helpers ---------------------------------------------
// ticksToSeconds, secondsSince, round1 and compactSeconds are unexported but
// used across this package's other clients (the *arr health/queue tools), so
// they stay as standalone functions rather than being folded into a caller.

// Jellyfin measures anything to do with a media timeline in .NET ticks of 100
// nanoseconds, so a two-hour film is 72000000000. Every duration this package
// exposes is seconds, converted here.
const ticksPerSecond = 10_000_000

func ticksToSeconds(ticks int64) uint64 {
	if ticks <= 0 {
		return 0
	}
	return uint64(ticks / ticksPerSecond)
}

// secondsSince ignores the zero timestamps Jellyfin writes for events that
// never happened, which parse fine and would render as decades of age.
//
// Two layouts, because Jellyfin is inconsistent about the zone: most timestamps
// arrive as RFC3339 with a Z, and some — task execution times among them — come
// from a .NET DateTime with an unspecified kind and carry no zone at all. Those
// are UTC in practice, and reading them as local time would put a task that ran
// a minute ago hours into the future.
func secondsSince(stamp string) uint64 {
	stamp = strings.TrimSpace(stamp)
	if stamp == "" {
		return 0
	}

	t, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		fallback := stamp
		if dot := strings.IndexByte(fallback, '.'); dot >= 0 {
			fallback = fallback[:dot]
		}
		t, err = time.ParseInLocation("2006-01-02T15:04:05", fallback, time.UTC)
	}
	if err != nil || t.IsZero() || t.Year() <= 1 {
		return 0
	}

	d := time.Since(t)
	if d < 0 {
		return 0
	}
	return uint64(d.Seconds())
}

func round1(v float64) float64 { return float64(int(v*10+0.5)) / 10 }

func compactSeconds(s uint64) string {
	d := time.Duration(s) * time.Second
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
