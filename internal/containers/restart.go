package containers

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// parseAllowlist reads a comma-separated list of container names. Whitespace is
// trimmed BEFORE the optional leading slash, because entries are usually
// written with a space after the comma and " /jellyfin" would otherwise keep
// its slash and silently match nothing.
func parseAllowlist(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var out []string
	for part := range strings.SplitSeq(raw, ",") {
		if name := strings.TrimPrefix(strings.TrimSpace(part), "/"); name != "" {
			out = append(out, name)
		}
	}
	return out
}

// RestartAllowlistEnv names the environment variable that permits restarting
// containers. It is deliberately SEPARATE from the exec allowlist: reading logs
// inside a container and taking that service down for everyone who uses it are
// different grants, and an operator may reasonably want the first without the
// second. Set both to the same list if you want both.
const RestartAllowlistEnv = "HOMELAB_MCP_RESTART_ALLOW_CONTAINER_NAMES"

const (
	// Seconds Docker waits for the container to stop gracefully before it
	// sends SIGKILL. Docker's own default is 10.
	defaultStopTimeout = 10
	maxStopTimeout     = 120

	// How long we keep checking that the container actually came back, and how
	// often. Reporting "restarted" without confirming it is up would hide the
	// exact failure an operator most needs to know about.
	settleTimeout  = 15 * time.Second
	settleInterval = 500 * time.Millisecond

	// A container is only called "back" after it has held running for this
	// long. Docker reports "running" the instant the process is spawned, so
	// checking once would pass a container that dies a second later — exactly
	// the failure this tool exists to surface.
	minStableWindow = 3 * time.Second
)

type RestartResult struct {
	Container string `json:"container"`

	PreviousState string `json:"previous_state"`
	State         string `json:"state"`
	Health        string `json:"health,omitempty" jsonschema:"healthcheck result once the container came back; 'starting' is normal immediately after a restart"`

	CameBack   bool  `json:"came_back" jsonschema:"true when the container was observed running again after the restart; false means it did not come back up and needs attention"`
	DurationMS int64 `json:"duration_ms"`

	Warnings []string `json:"warnings,omitempty"`
}

// RestartAllowlist returns the container names that may be restarted.
func RestartAllowlist() []string { return parseAllowlist(os.Getenv(RestartAllowlistEnv)) }

func restartAllowed(name string) bool {
	for _, allowed := range RestartAllowlist() {
		if strings.EqualFold(allowed, name) {
			return true
		}
	}
	return false
}

// Restart stops and starts a container, then waits to confirm it came back.
func Restart(ctx context.Context, name string, stopTimeout int) (RestartResult, error) {
	res := RestartResult{Container: name}

	if !restartAllowed(name) {
		return res, fmt.Errorf("%w: %q may not be restarted. Allowed: %s",
			ErrNotAllowed, name, strings.Join(RestartAllowlist(), ", "))
	}

	switch {
	case stopTimeout <= 0:
		stopTimeout = defaultStopTimeout
	case stopTimeout > maxStopTimeout:
		stopTimeout = maxStopTimeout
	}

	client, err := newClient()
	if err != nil {
		return res, err
	}

	// Resolve the name through Docker's own listing so no caller-supplied
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
	res.PreviousState = matched[0].State

	start := time.Now()

	// Docker returns 204 with no body; post() already treats >= 300 as an error.
	body, err := post(ctx, client,
		fmt.Sprintf("/containers/%s/restart?t=%d", id, stopTimeout), nil)
	if err != nil {
		return res, err
	}
	body.Close()

	res.State, res.Health, res.CameBack = waitUntilSettled(ctx, client, id)
	res.DurationMS = time.Since(start).Milliseconds()

	if !res.CameBack {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"%s did not come back up after the restart (it is %s) — "+
				"check its logs before restarting again",
			name, res.State))
	}
	if res.Health == "unhealthy" {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"%s is running again but its healthcheck is already failing", name))
	}

	return res, nil
}

// waitUntilSettled polls until the container is running, or the settle window
// expires. A container that crashes on boot is back in "restarting" or "exited"
// within a second or two, and that is precisely the outcome worth reporting.
func waitUntilSettled(ctx context.Context, client *http.Client, id string) (state, health string, cameBack bool) {
	deadline := time.Now().Add(settleTimeout)
	var runningSince time.Time

	for {
		var r inspectResult
		if err := get(ctx, client, "/containers/"+id+"/json", &r); err != nil {
			return "unknown", "", false
		}

		state = r.State.Status
		health = ""
		if r.State.Health != nil && state == "running" {
			health = r.State.Health.Status
		}

		switch state {
		case "running":
			if runningSince.IsZero() {
				runningSince = time.Now()
			}
			// "starting" means the healthcheck has not reported yet; wait for a
			// verdict rather than calling the restart good too early.
			if health != "starting" && time.Since(runningSince) >= minStableWindow {
				return state, health, true
			}
		case "exited", "dead":
			// It came up and fell over, or never came up at all.
			return state, health, false
		default:
			// created / restarting / paused — not up yet, so the clock restarts.
			runningSince = time.Time{}
		}

		if time.Now().After(deadline) {
			return state, health, state == "running"
		}

		select {
		case <-ctx.Done():
			return state, health, state == "running"
		case <-time.After(settleInterval):
		}
	}
}
