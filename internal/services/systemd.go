package services

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

var ErrUnavailable = errors.New("systemd is not available on this host")

// Above this restart count we warn even when the unit is active: a service that
// keeps coming back reads as healthy in every listing while never staying up.
const restartWarnThreshold = 5

// systemctl talks to PID 1 over a socket, which can block if systemd is wedged.
const systemctlTimeout = 5 * time.Second

// An explicit set keeps `show` output small; systemd emits over a hundred
// properties per unit otherwise.
var showProperties = []string{
	"Id",
	"Description",
	"LoadState",
	"ActiveState",
	"SubState",
	"NRestarts",
	"Result",
	"ExecMainStatus",
	"MainPID",
	"StateChangeTimestampMonotonic",
	"MemoryCurrent",
}

type Unit struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`

	LoadState   string `json:"load_state" jsonschema:"loaded, not-found, masked or error"`
	ActiveState string `json:"active_state" jsonschema:"active, inactive, failed, activating or deactivating"`
	SubState    string `json:"sub_state" jsonschema:"unit-type-specific detail behind active_state: running, exited, dead, auto-restart, start-pre"`

	Restarts int `json:"restarts" jsonschema:"times systemd has restarted this unit since it was last started manually; a climbing value is the signature of a crash loop"`

	Result   string `json:"result,omitempty" jsonschema:"why the unit last stopped: success, exit-code, signal, oom-kill, timeout or core-dump"`
	ExitCode int    `json:"exit_code,omitempty"`
	MainPID  int    `json:"main_pid,omitempty"`

	StateForSeconds uint64 `json:"state_for_seconds,omitempty" jsonschema:"how long the unit has been in its current state; a short time paired with a high restart count means it is flapping right now"`

	MemoryBytes uint64 `json:"memory_bytes,omitempty"`
}

type ServiceStatus struct {
	Units []Unit `json:"units"`

	TotalCount  int `json:"total_count" jsonschema:"service units on the host, before filtering"`
	ActiveCount int `json:"active_count"`
	FailedCount int `json:"failed_count"`

	SkippedCount int      `json:"skipped_count" jsonschema:"healthy units omitted from the list; use include_all to see them"`
	Warnings     []string `json:"warnings,omitempty"`
}

// GetServiceStatus reports systemd service units. With no names, every service
// unit is scanned and only those needing attention are returned unless
// includeAll is set; naming units returns exactly those, whatever their state.
func GetServiceStatus(ctx context.Context, names []string, includeAll bool) (ServiceStatus, error) {
	explicit := len(names) > 0

	if explicit {
		// Checked before anything is executed; see validUnitName.
		if err := validateUnitNames(names); err != nil {
			return ServiceStatus{}, err
		}
	} else {
		var err error
		names, err = listServiceUnits(ctx)
		if err != nil {
			return ServiceStatus{}, err
		}
	}
	if len(names) == 0 {
		return ServiceStatus{}, nil
	}

	all, err := showUnits(ctx, names)
	if err != nil {
		return ServiceStatus{}, err
	}

	// Non-nil so an all-healthy scan serializes as [] rather than null.
	status := ServiceStatus{TotalCount: len(all), Units: make([]Unit, 0, len(all))}
	for _, u := range all {
		if u.ActiveState == "active" {
			status.ActiveCount++
		}
		if u.ActiveState == "failed" {
			status.FailedCount++
		}
	}

	// Named units are never filtered out: asking about nginx.service and getting
	// nothing back because it is healthy would be a worse answer than saying so.
	for _, u := range all {
		if explicit || includeAll || needsAttention(u) {
			status.Units = append(status.Units, u)
			continue
		}
		status.SkippedCount++
	}

	// Worst first, so whatever is broken is the first thing read.
	slices.SortFunc(status.Units, func(a, b Unit) int {
		if d := severity(a) - severity(b); d != 0 {
			return d
		}
		if d := b.Restarts - a.Restarts; d != 0 {
			return d
		}
		return strings.Compare(a.Name, b.Name)
	})

	status.Warnings = buildWarnings(status.Units)

	return status, nil
}

// severity ranks units for display order. Lower sorts first.
//
// Deliberately absent: load_state "not-found". systemd carries an entry for
// every unit anything merely references, so a stock install lists a dozen
// never-installed ones (ypbind, plymouth) as not-found/dead forever. When a
// missing unit genuinely matters, something tried to start it and it failed.
func severity(u Unit) int {
	switch {
	case u.ActiveState == "failed":
		return 0
	case u.SubState == "auto-restart":
		return 1
	case u.ActiveState == "activating", u.ActiveState == "deactivating":
		return 2
	case u.LoadState == "error":
		return 3
	case u.Restarts > 0:
		return 4
	default:
		return 5
	}
}

func needsAttention(u Unit) bool {
	return severity(u) < 5
}

// Built here rather than in the renderer so a client reading only
// structuredContent sees them too.
func buildWarnings(units []Unit) []string {
	var out []string
	for _, u := range units {
		switch {
		case u.ActiveState == "failed":
			msg := fmt.Sprintf("%s failed — %s", u.Name, failureReason(u))
			// systemd gives up after the start limit, so restarts behind a
			// failed unit mean an abandoned crash loop, not a one-off.
			if u.Restarts > 0 {
				msg += fmt.Sprintf(", after %d restarts (systemd stopped retrying)", u.Restarts)
			}
			out = append(out, msg)

		case u.SubState == "auto-restart":
			out = append(out, fmt.Sprintf(
				"%s is being restarted right now (%d restarts so far) — it is in a crash loop",
				u.Name, u.Restarts))

		case u.ActiveState == "activating":
			out = append(out, fmt.Sprintf(
				"%s has been starting for %ds without reaching active — it may be stuck",
				u.Name, u.StateForSeconds))

		case u.LoadState == "error":
			out = append(out, fmt.Sprintf(
				"%s has a unit file systemd could not parse", u.Name))

		case u.LoadState == "masked":
			out = append(out, fmt.Sprintf(
				"%s is masked and cannot be started until it is unmasked", u.Name))
		}

		// The one a plain status check hides: up right now, so every listing
		// calls it healthy, while it has been dying all along.
		if u.ActiveState == "active" && u.Restarts >= restartWarnThreshold {
			out = append(out, fmt.Sprintf(
				"%s reads as active but has restarted %d times and has only held "+
					"this state for %ds — it is crash-looping, not running",
				u.Name, u.Restarts, u.StateForSeconds))
		}
	}
	return out
}

// Unit names come from the model, the only untrusted value reaching a command
// line here. Shell injection is already impossible (os/exec calls execve, so
// there is no shell), but systemctl parses its own arguments and would read a
// name beginning with '-' as an option — `--host=` makes it dial out over SSH.
var validUnitName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.@:\\-]{0,255}$`)

func validateUnitNames(names []string) error {
	for _, n := range names {
		if !validUnitName.MatchString(n) {
			return fmt.Errorf(
				"invalid unit name %q: expected a systemd unit name such as nginx.service", n)
		}
	}
	return nil
}

// failureReason turns systemd's Result enum into a sentence. oom-kill in
// particular is invisible in `systemctl status` unless you know to look.
func failureReason(u Unit) string {
	switch {
	case u.Result == "exit-code" && u.ExitCode != 0:
		return fmt.Sprintf("exited with code %d", u.ExitCode)
	case u.Result == "oom-kill":
		return "was killed by the OOM killer — it needs more memory, or it leaks"
	case u.Result == "timeout":
		return "timed out during start or stop"
	case u.Result == "signal":
		return "was terminated by a signal"
	case u.Result == "core-dump":
		return "crashed and dumped core"
	case u.Result == "" || u.Result == "success":
		return "stopped without reporting a reason"
	default:
		return "result: " + u.Result
	}
}

// Parsed from the plain table rather than `--output=json`, which older systemd
// releases lack. Only the first column is read; details come from `show`.
func listServiceUnits(ctx context.Context) ([]string, error) {
	out, err := run(ctx, "list-units", "--type=service", "--all", "--plain", "--no-legend", "--no-pager")
	if err != nil {
		return nil, err
	}

	var names []string
	for line := range strings.SplitSeq(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && strings.HasSuffix(fields[0], ".service") {
			names = append(names, fields[0])
		}
	}
	return names, nil
}

// showUnits fetches every property for every unit in ONE systemctl call:
// passing 200 names on a command line is fine, spawning 200 processes is not.
// systemd separates each unit's block with a blank line.
func showUnits(ctx context.Context, names []string) ([]Unit, error) {
	args := append([]string{"show", "--property=" + strings.Join(showProperties, ",")}, names...)

	out, err := run(ctx, args...)
	if err != nil {
		return nil, err
	}

	uptime := hostUptimeSeconds()

	var units []Unit
	for block := range strings.SplitSeq(strings.TrimSpace(out), "\n\n") {
		props := make(map[string]string, len(showProperties))
		for line := range strings.SplitSeq(block, "\n") {
			// Cut on the FIRST '=' only: Description values contain them.
			if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
				props[k] = v
			}
		}
		if props["Id"] == "" {
			continue
		}

		u := Unit{
			Name:        props["Id"],
			Description: props["Description"],
			LoadState:   props["LoadState"],
			ActiveState: props["ActiveState"],
			SubState:    props["SubState"],
			Restarts:    atoi(props["NRestarts"]),
			Result:      props["Result"],
			ExitCode:    atoi(props["ExecMainStatus"]),
			MainPID:     atoi(props["MainPID"]),
			MemoryBytes: atou(props["MemoryCurrent"]),
		}

		// Timestamps are microseconds since boot, so the age of the current
		// state is (uptime - that). The comparison also skips the field when
		// the clock is unusable.
		if changed := atou(props["StateChangeTimestampMonotonic"]) / 1_000_000; changed > 0 && uptime > changed {
			u.StateForSeconds = uptime - changed
		}

		units = append(units, u)
	}

	return units, nil
}

func run(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, systemctlTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "systemctl", args...)
	// C locale: keeps output stable regardless of the host language.
	cmd.Env = append(os.Environ(), "LC_ALL=C")

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "", ErrUnavailable
		}
		if ctx.Err() != nil {
			return "", fmt.Errorf("systemctl %s timed out after %s (systemd unresponsive?)",
				args[0], systemctlTimeout)
		}
		// systemctl exits non-zero merely because a queried unit is failed or
		// missing, while still writing good output. Usable stdout outranks it.
		if stdout.Len() > 0 {
			return stdout.String(), nil
		}
		return "", fmt.Errorf("systemctl %s: %w: %s",
			args[0], err, strings.TrimSpace(stderr.String()))
	}

	return stdout.String(), nil
}

// Returns 0 when unreadable; callers treat that as "state age unknown" rather
// than an error.
func hostUptimeSeconds() uint64 {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	field, _, _ := strings.Cut(string(b), " ")
	v, err := strconv.ParseFloat(field, 64)
	if err != nil || v < 0 {
		return 0
	}
	return uint64(v)
}

// systemd writes "[not set]" for unset numeric properties, so both parsers
// return zero on anything non-numeric.
func atoi(s string) int {
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return v
}

func atou(s string) uint64 {
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0
	}
	return v
}
