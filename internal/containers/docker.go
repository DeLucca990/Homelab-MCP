package containers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/DeLucca990/homelab-mcp/internal/system"
)

// ErrUnavailable means there is no daemon to talk to. Distinct from a
// permission problem, which says so explicitly.
var ErrUnavailable = errors.New("docker is not available on this host")

const (
	defaultSocket  = "/var/run/docker.sock"
	requestTimeout = 5 * time.Second

	// Above this restart count we warn even while the container is up: one
	// restarting every few seconds shows "Up 4 seconds" and reads as healthy.
	restartWarnThreshold = 5
)

// Port is one published or exposed mapping, with the IPv4/IPv6 duplicate
// collapsed: `0.0.0.0:8989->8989/tcp, :::8989->8989/tcp` is one fact, not two.
type Port struct {
	ContainerPort int    `json:"container_port"`
	HostPort      int    `json:"host_port,omitempty" jsonschema:"port on the host that reaches this container; absent means the port is exposed but not published, so it is unreachable from outside the docker network"`
	Protocol      string `json:"protocol"`
}

type Container struct {
	Name  string `json:"name"`
	Image string `json:"image"`
	ID    string `json:"id" jsonschema:"short container id"`

	Command string `json:"command,omitempty" jsonschema:"the container's entrypoint command"`
	Ports   []Port `json:"ports,omitempty"`

	State  string `json:"state" jsonschema:"running, exited, restarting, paused, dead or created"`
	Status string `json:"status" jsonschema:"the human-readable status docker itself reports"`

	Health        string `json:"health,omitempty" jsonschema:"healthcheck result: healthy, unhealthy or starting; empty when the image defines no healthcheck"`
	FailingStreak int    `json:"failing_streak,omitempty" jsonschema:"consecutive healthcheck failures"`

	RestartCount int  `json:"restart_count" jsonschema:"times docker has restarted this container; a climbing value is the signature of a crash loop"`
	ExitCode     int  `json:"exit_code,omitempty" jsonschema:"exit status of the last run; 137 is SIGKILL (usually the OOM killer) and 143 is SIGTERM"`
	OOMKilled    bool `json:"oom_killed,omitempty" jsonschema:"true when the kernel killed this container for exceeding its memory limit"`

	StateForSeconds  uint64 `json:"state_for_seconds,omitempty" jsonschema:"how long the container has been in its current state; short uptime with a high restart count means it is flapping right now"`
	AgeSeconds       uint64 `json:"age_seconds,omitempty" jsonschema:"seconds since the container was created; a large gap between this and state_for_seconds means the container was restarted long after it was deployed"`
	MemoryLimitBytes uint64 `json:"memory_limit_bytes,omitempty" jsonschema:"configured memory ceiling; absent means unlimited"`

	RestartPolicy string `json:"restart_policy,omitempty"`

	// Set when inspecting THIS container failed, without invalidating the rest.
	Error string `json:"error,omitempty"`
}

type ContainerStatus struct {
	Containers []Container `json:"containers"`

	TotalCount   int `json:"total_count" jsonschema:"containers on the host, before filtering"`
	RunningCount int `json:"running_count"`

	SkippedCount int      `json:"skipped_count" jsonschema:"cleanly-stopped containers omitted from the list; use include_all to see them"`
	Warnings     []string `json:"warnings,omitempty"`
}

// GetContainerStatus reports Docker containers. With no names, running
// containers plus anything needing attention are returned unless includeAll is
// set; naming containers returns exactly those, whatever their state.
func GetContainerStatus(ctx context.Context, names []string, includeAll bool) (ContainerStatus, error) {
	client, err := newClient()
	if err != nil {
		return ContainerStatus{}, err
	}

	summaries, err := listContainers(ctx, client)
	if err != nil {
		return ContainerStatus{}, err
	}

	// Filtering against the listing rather than querying per name means every
	// id interpolated into a URL came from Docker itself, so no caller-supplied
	// string ever reaches a request path.
	explicit := len(names) > 0
	if explicit {
		summaries = matchNames(summaries, names)
	}

	if len(summaries) == 0 {
		return ContainerStatus{Containers: []Container{}}, nil
	}

	// One goroutine per container, each writing to its own index: the inspect
	// calls are independent and their latency adds up serially.
	all := make([]Container, len(summaries))
	var wg sync.WaitGroup
	for i, s := range summaries {
		wg.Add(1)
		go func() {
			defer wg.Done()
			all[i] = inspect(ctx, client, s)
		}()
	}
	wg.Wait()

	status := ContainerStatus{TotalCount: len(all), Containers: make([]Container, 0, len(all))}
	for _, c := range all {
		if c.State == "running" {
			status.RunningCount++
		}
	}

	for _, c := range all {
		if explicit || includeAll || isInteresting(c) {
			status.Containers = append(status.Containers, c)
			continue
		}
		status.SkippedCount++
	}

	// Worst first, so whatever is broken is read first.
	slices.SortFunc(status.Containers, func(a, b Container) int {
		if d := severity(a) - severity(b); d != 0 {
			return d
		}
		if d := b.RestartCount - a.RestartCount; d != 0 {
			return d
		}
		return strings.Compare(a.Name, b.Name)
	})

	status.Warnings = buildWarnings(status.Containers)

	return status, nil
}

// severity ranks containers for display order. Lower sorts first.
func severity(c Container) int {
	switch {
	case c.OOMKilled:
		return 0
	case c.State == "restarting", c.State == "dead":
		return 1
	case c.State == "running" && c.Health == "unhealthy":
		return 2
	case c.State == "exited" && c.ExitCode != 0:
		return 3
	case c.State == "paused":
		return 4
	case c.RestartCount > 0:
		return 5
	case c.State == "running":
		return 6
	default:
		return 7
	}
}

// isInteresting keeps running containers plus anything broken, and drops the
// cleanly-exited ones a homelab accumulates from one-shot runs.
func isInteresting(c Container) bool {
	return severity(c) <= 6
}

// Built here rather than in the renderer so a client reading only
// structuredContent sees them too.
func buildWarnings(cs []Container) []string {
	var out []string
	for _, c := range cs {
		if c.Error != "" {
			out = append(out, fmt.Sprintf("%s: %s", c.Name, c.Error))
			continue
		}

		switch {
		case c.OOMKilled:
			msg := fmt.Sprintf("%s was killed for exceeding its memory limit", c.Name)
			if c.MemoryLimitBytes > 0 {
				msg += fmt.Sprintf(" (%s)", system.CompactBytes(c.MemoryLimitBytes))
			}
			out = append(out, msg+" — raise the limit or fix the leak")

		case c.State == "restarting":
			out = append(out, fmt.Sprintf(
				"%s is restarting right now (%d restarts so far) — it is in a crash loop",
				c.Name, c.RestartCount))

		case c.State == "dead":
			out = append(out, fmt.Sprintf(
				"%s is in the dead state — docker could not remove it cleanly", c.Name))

		case c.State == "running" && c.Health == "unhealthy":
			out = append(out, fmt.Sprintf(
				"%s is running but its healthcheck is failing (%d consecutive failures) — "+
					"the process is up while the service behind it is not",
				c.Name, c.FailingStreak))

		case c.State == "exited" && c.ExitCode != 0:
			out = append(out, fmt.Sprintf(
				"%s exited with code %d%s", c.Name, c.ExitCode, exitCodeHint(c.ExitCode)))
		}

		// The one every listing hides: up right now, so `docker ps` calls it
		// healthy, while it has been dying all along.
		if c.State == "running" && c.RestartCount >= restartWarnThreshold {
			out = append(out, fmt.Sprintf(
				"%s reads as running but has restarted %d times and has only been up "+
					"for %ds — it is crash-looping, not running",
				c.Name, c.RestartCount, c.StateForSeconds))
		}
	}
	return out
}

// The two exit codes routinely misread as application errors.
func exitCodeHint(code int) string {
	switch code {
	case 137:
		return " (SIGKILL — usually the OOM killer, or a stop that timed out)"
	case 143:
		return " (SIGTERM — a normal stop request)"
	default:
		return ""
	}
}

// --- Docker API over the unix socket -------------------------------------
//
// The daemon speaks plain HTTP, so a stock net/http client with a custom dialer
// reaches it — no need to pull the Docker SDK's dependency tree into a module
// with two direct dependencies.

type summary struct {
	ID      string   `json:"Id"`
	Names   []string `json:"Names"`
	Image   string   `json:"Image"`
	State   string   `json:"State"`
	Command string   `json:"Command"`
	Created int64    `json:"Created"`
	Ports   []struct {
		IP          string `json:"IP"`
		PrivatePort int    `json:"PrivatePort"`
		PublicPort  int    `json:"PublicPort"`
		Type        string `json:"Type"`
	} `json:"Ports"`
}

type inspectResult struct {
	Name         string `json:"Name"`
	RestartCount int    `json:"RestartCount"`
	State        struct {
		Status     string `json:"Status"`
		OOMKilled  bool   `json:"OOMKilled"`
		ExitCode   int    `json:"ExitCode"`
		Error      string `json:"Error"`
		StartedAt  string `json:"StartedAt"`
		FinishedAt string `json:"FinishedAt"`
		Health     *struct {
			Status        string `json:"Status"`
			FailingStreak int    `json:"FailingStreak"`
		} `json:"Health"`
	} `json:"State"`
	Config struct {
		Image string `json:"Image"`
		Tty   bool   `json:"Tty"`
	} `json:"Config"`
	HostConfig struct {
		Memory        uint64 `json:"Memory"`
		RestartPolicy struct {
			Name string `json:"Name"`
		} `json:"RestartPolicy"`
	} `json:"HostConfig"`
}

func newClient() (*http.Client, error) {
	path := socketPath()

	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: no socket at %s", ErrUnavailable, path)
		}
		if os.IsPermission(err) {
			return nil, fmt.Errorf(
				"cannot access the docker socket at %s: permission denied — "+
					"the user running this server must be in the 'docker' group", path)
		}
		return nil, fmt.Errorf("checking docker socket %s: %w", path, err)
	}

	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", path)
			},
		},
	}, nil
}

// DOCKER_HOST is honoured so rootless installs, which put the socket under
// $XDG_RUNTIME_DIR, work without configuration.
func socketPath() string {
	if h := os.Getenv("DOCKER_HOST"); strings.HasPrefix(h, "unix://") {
		return strings.TrimPrefix(h, "unix://")
	}
	return defaultSocket
}

func get(ctx context.Context, client *http.Client, path string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	// The host is ignored (the dialer always goes to the socket) but net/http
	// still requires a syntactically valid one.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker"+path, nil)
	if err != nil {
		return err
	}

	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, os.ErrPermission) || strings.Contains(err.Error(), "permission denied") {
			return fmt.Errorf("docker socket refused the connection: permission denied — " +
				"the user running this server must be in the 'docker' group")
		}
		if ctx.Err() != nil {
			return fmt.Errorf("docker API timed out after %s (daemon unresponsive?)", requestTimeout)
		}
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("docker API %s returned %s", path, resp.Status)
	}

	return json.NewDecoder(resp.Body).Decode(out)
}

func getStream(ctx context.Context, client *http.Client, path string) (io.ReadCloser, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker"+path, nil)
	if err != nil {
		cancel()
		return nil, err
	}

	resp, err := client.Do(req)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		cancel()
		return nil, fmt.Errorf("docker API %s returned %s", path, resp.Status)
	}

	return cancelOnClose{ReadCloser: resp.Body, cancel: cancel}, nil
}

type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c cancelOnClose) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}

func listContainers(ctx context.Context, client *http.Client) ([]summary, error) {
	var out []summary
	if err := get(ctx, client, "/containers/json?all=true", &out); err != nil {
		return nil, err
	}
	return out, nil
}

func inspect(ctx context.Context, client *http.Client, s summary) Container {
	c := Container{
		ID:      shortID(s.ID),
		Name:    displayName(s.Names),
		Image:   s.Image,
		State:   s.State,
		Command: s.Command,
		Ports:   collectPorts(s),
	}
	if s.Created > 0 {
		if age := time.Since(time.Unix(s.Created, 0)); age > 0 {
			c.AgeSeconds = uint64(age.Seconds())
		}
	}

	var r inspectResult
	if err := get(ctx, client, "/containers/"+s.ID+"/json", &r); err != nil {
		c.Error = err.Error()
		return c
	}

	c.Name = strings.TrimPrefix(r.Name, "/")
	c.State = r.State.Status
	c.RestartCount = r.RestartCount
	c.ExitCode = r.State.ExitCode
	c.OOMKilled = r.State.OOMKilled
	c.MemoryLimitBytes = r.HostConfig.Memory
	c.RestartPolicy = r.HostConfig.RestartPolicy.Name

	if r.Config.Image != "" {
		c.Image = r.Config.Image
	}
	if r.State.Health != nil && r.State.Status == "running" {
		c.Health = r.State.Health.Status
		c.FailingStreak = r.State.Health.FailingStreak
	}
	if r.State.Error != "" {
		c.Error = r.State.Error
	}

	stamp := r.State.StartedAt
	if r.State.Status != "running" && r.State.Status != "restarting" {
		stamp = r.State.FinishedAt
	}
	c.StateForSeconds = secondsSince(stamp)

	c.Status = describe(c)

	return c
}

// Drops the duplicate every published mapping gets from being bound on both
// 0.0.0.0 and ::.
func collectPorts(s summary) []Port {
	seen := make(map[Port]bool, len(s.Ports))
	out := make([]Port, 0, len(s.Ports))

	for _, p := range s.Ports {
		port := Port{ContainerPort: p.PrivatePort, HostPort: p.PublicPort, Protocol: p.Type}
		if seen[port] {
			continue
		}
		seen[port] = true
		out = append(out, port)
	}

	// Published first: what is reachable from outside is what is looked for.
	slices.SortFunc(out, func(a, b Port) int {
		if (a.HostPort == 0) != (b.HostPort == 0) {
			if a.HostPort == 0 {
				return 1
			}
			return -1
		}
		if d := a.HostPort - b.HostPort; d != 0 {
			return d
		}
		if d := a.ContainerPort - b.ContainerPort; d != 0 {
			return d
		}
		return strings.Compare(a.Protocol, b.Protocol)
	})

	if len(out) == 0 {
		return nil
	}
	return out
}

// Docker writes the zero time for events that never happened, which parses
// fine and must be discarded.
func secondsSince(stamp string) uint64 {
	t, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil || t.IsZero() || t.Year() <= 1 {
		return 0
	}
	d := time.Since(t)
	if d < 0 {
		return 0
	}
	return uint64(d.Seconds())
}

// describe rebuilds `docker ps`'s one-line status from the structured fields,
// rather than parsing Docker's own prose.
func describe(c Container) string {
	var b strings.Builder
	switch c.State {
	case "running":
		b.WriteString("Up")
	case "exited":
		fmt.Fprintf(&b, "Exited (%d)", c.ExitCode)
	default:
		b.WriteString(strings.ToUpper(c.State[:1]))
		b.WriteString(c.State[1:])
	}
	if c.Health != "" {
		fmt.Fprintf(&b, " (%s)", c.Health)
	}
	return b.String()
}

func matchNames(all []summary, want []string) []summary {
	var out []summary
	for _, s := range all {
		name := displayName(s.Names)
		for _, w := range want {
			if strings.EqualFold(name, strings.TrimPrefix(w, "/")) {
				out = append(out, s)
				break
			}
		}
	}
	return out
}

// Docker reports names as a slice with a leading slash; the first is the one
// people use.
func displayName(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return strings.TrimPrefix(names[0], "/")
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
