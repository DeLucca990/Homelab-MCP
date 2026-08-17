package overview

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/DeLucca990/homelab-mcp/internal/containers"
	"github.com/DeLucca990/homelab-mcp/internal/jellyfin"
	"github.com/DeLucca990/homelab-mcp/internal/radarr"
	"github.com/DeLucca990/homelab-mcp/internal/services"
	"github.com/DeLucca990/homelab-mcp/internal/sonarr"
	"github.com/DeLucca990/homelab-mcp/internal/system"
)

// One answer to "is anything wrong with this server", assembled from the checks
// the other collectors already do. It exists because that question otherwise
// costs six round trips and the model has to know which six — and because the
// answer is almost always "no", which should be one cheap call rather than a
// tour of the whole machine.
//
// It is a composition, not a new source of truth: every warning here is the
// warning its own collector produced, unchanged. The two exceptions are stated
// where they are computed, and exist because a full disk and exhausted memory
// are conditions no existing collector calls a warning.
//
// Deliberately absent: CPU. It is the one reading that costs half a second, and
// a pinned core is not a fault — a media server transcoding looks exactly like
// one in trouble. system_cpu_cores is one call away when the question is
// actually about load.
//
// Where Jellyfin is configured, the jellyfin section is the closest thing to an
// answer for that: it does not measure the CPU, but it says how many streams
// are being re-encoded on it, which is what the load usually turns out to be.

const (
	StatusAttention = "attention"
	StatusFailed    = "failed"
	StatusOK        = "ok"
	StatusAbsent    = "absent"
)

const (
	diskFullPercent   = 90.0
	memoryFullPercent = 90.0
	swapUsedPercent   = 50.0
)

type Section struct {
	Name   string `json:"name"`
	Status string `json:"status" jsonschema:"attention (something is wrong), ok, failed (the check itself could not run) or absent (this host has no such thing)"`

	Headline string `json:"headline" jsonschema:"the numbers that summarise this area, whether or not anything is wrong"`

	Warnings []string `json:"warnings,omitempty" jsonschema:"exactly what the area's own tool would have reported"`
	Error    string   `json:"error,omitempty" jsonschema:"why this check could not run; the other sections still answered"`

	Tool string `json:"tool" jsonschema:"the tool to call next for the detail behind this line"`
}

type Report struct {
	Sections []Section `json:"sections" jsonschema:"one per area checked, worst first"`

	CheckedCount   int `json:"checked_count"`
	AttentionCount int `json:"attention_count" jsonschema:"sections reporting something wrong"`
	FailedCount    int `json:"failed_count,omitempty" jsonschema:"sections whose check could not run"`

	ElapsedMs int `json:"elapsed_ms"`
}

// Get runs every check at once and returns them worst first. It has no error
// return: a check that fails is a section that says so, not an answer withheld
// from the five that worked.
func Get(ctx context.Context) Report {
	start := time.Now()

	checks := []func(context.Context) Section{checkDisk, checkMemory, checkServices, checkDocker}

	if radarr.Configured() {
		checks = append(checks, checkRadarr)
	}
	if sonarr.Configured() {
		checks = append(checks, checkSonarr)
	}
	if jellyfin.Configured() {
		checks = append(checks, checkJellyfin)
	}

	sections := make([]Section, len(checks))
	var wg sync.WaitGroup
	for i, check := range checks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sections[i] = check(ctx)
		}()
	}
	wg.Wait()

	report := Report{Sections: sections, CheckedCount: len(sections)}
	for _, s := range sections {
		switch s.Status {
		case StatusAttention:
			report.AttentionCount++
		case StatusFailed:
			report.FailedCount++
		}
	}

	slices.SortStableFunc(report.Sections, func(a, b Section) int {
		return severity(a.Status) - severity(b.Status)
	})

	report.ElapsedMs = int(time.Since(start).Milliseconds())

	return report
}

func severity(status string) int {
	switch status {
	case StatusAttention:
		return 0
	case StatusFailed:
		return 1
	case StatusOK:
		return 2
	default:
		return 3
	}
}

func checkDisk(ctx context.Context) Section {
	s := Section{Name: "disk", Tool: "system_disk_usage"}

	stats, err := system.GetDiskStats(ctx, false)
	if err != nil {
		return s.failed(err)
	}
	if len(stats.Filesystems) == 0 {
		s.Status = StatusOK
		s.Headline = "no filesystem to report"
		return s
	}

	fullest := stats.Filesystems[0]
	for _, fs := range stats.Filesystems {
		if !fs.ReadOnly && fs.Error == "" {
			fullest = fs
			break
		}
	}
	s.Headline = fmt.Sprintf("%s at %.0f%% (%s free)",
		fullest.Mountpoint, fullest.UsedPercent, system.CompactBytes(fullest.FreeBytes))

	s.Warnings = stats.Warnings
	if fullest.UsedPercent >= diskFullPercent {
		s.Warnings = append([]string{fmt.Sprintf(
			"%s is %.0f%% full, %s left",
			fullest.Mountpoint, fullest.UsedPercent, system.CompactBytes(fullest.FreeBytes))},
			s.Warnings...)
	}

	return s.settled()
}

func checkMemory(ctx context.Context) Section {
	s := Section{Name: "memory", Tool: "system_memory_stats"}

	stats, err := system.GetMemoryStats(ctx)
	if err != nil {
		return s.failed(err)
	}

	s.Headline = fmt.Sprintf("%s of %s used, %s available",
		system.IECBytes(stats.UsedBytes), system.IECBytes(stats.TotalBytes),
		system.IECBytes(stats.AvailableBytes))

	s.Warnings = stats.Warnings
	if stats.UsedPercent >= memoryFullPercent {
		s.Warnings = append(s.Warnings, fmt.Sprintf(
			"memory is %.0f%% used with only %s available to a new process",
			stats.UsedPercent, system.IECBytes(stats.AvailableBytes)))
	}
	if stats.Swap.Configured && stats.Swap.UsedPercent >= swapUsedPercent {
		s.Warnings = append(s.Warnings, fmt.Sprintf(
			"swap is %.0f%% used (%s) — the machine has been paging, which it only does "+
				"once RAM has run out", stats.Swap.UsedPercent, system.IECBytes(stats.Swap.UsedBytes)))
	}

	return s.settled()
}

func checkServices(ctx context.Context) Section {
	s := Section{Name: "services", Tool: "system_service_status"}

	status, err := services.GetServiceStatus(ctx, nil, false)
	if err != nil {
		if errors.Is(err, services.ErrUnavailable) {
			return s.absent("this host does not run systemd")
		}
		return s.failed(err)
	}

	s.Headline = fmt.Sprintf("%d units, %d active, %d failed",
		status.TotalCount, status.ActiveCount, status.FailedCount)
	s.Warnings = status.Warnings

	return s.settled()
}

func checkDocker(ctx context.Context) Section {
	s := Section{Name: "docker", Tool: "docker_container_status"}

	status, err := containers.GetContainerStatus(ctx, nil, false)
	if err != nil {
		if errors.Is(err, containers.ErrUnavailable) {
			return s.absent("there is no docker daemon on this host")
		}
		return s.failed(err)
	}

	s.Headline = fmt.Sprintf("%d containers, %d running",
		status.TotalCount, status.RunningCount)
	s.Warnings = status.Warnings

	return s.settled()
}

func checkRadarr(ctx context.Context) Section {
	s := Section{Name: "radarr", Tool: "radarr_queue_status"}

	health, healthErr := radarr.GetHealth(ctx)
	queue, queueErr := radarr.GetQueue(ctx)
	if healthErr != nil && queueErr != nil {
		return s.failed(healthErr)
	}

	s.Headline = arrHeadline(health.Version, len(health.Issues),
		queue.TotalCount, queue.DownloadingCount, queue.StalledCount, queue.BlockedCount)

	if healthErr == nil {
		s.Warnings = append(s.Warnings, health.Warnings...)
	} else {
		s.Warnings = append(s.Warnings, "could not read Radarr's health: "+healthErr.Error())
	}
	if queueErr == nil {
		s.Warnings = append(s.Warnings, queue.Warnings...)
	} else {
		s.Warnings = append(s.Warnings, "could not read Radarr's queue: "+queueErr.Error())
	}

	return s.settled()
}

func checkSonarr(ctx context.Context) Section {
	s := Section{Name: "sonarr", Tool: "sonarr_queue_status"}

	health, healthErr := sonarr.GetHealth(ctx)
	queue, queueErr := sonarr.GetQueue(ctx)
	if healthErr != nil && queueErr != nil {
		return s.failed(healthErr)
	}

	s.Headline = arrHeadline(health.Version, len(health.Issues),
		queue.TotalCount, queue.DownloadingCount, queue.StalledCount, queue.BlockedCount)

	if healthErr == nil {
		s.Warnings = append(s.Warnings, health.Warnings...)
	} else {
		s.Warnings = append(s.Warnings, "could not read Sonarr's health: "+healthErr.Error())
	}
	if queueErr == nil {
		s.Warnings = append(s.Warnings, queue.Warnings...)
	} else {
		s.Warnings = append(s.Warnings, "could not read Sonarr's queue: "+queueErr.Error())
	}

	return s.settled()
}

func checkJellyfin(ctx context.Context) Section {
	s := Section{Name: "jellyfin"}

	health, healthErr := jellyfin.GetHealth(ctx)
	sessions, sessionsErr := jellyfin.GetSessions(ctx, jellyfin.SessionOptions{})
	if healthErr != nil && sessionsErr != nil {
		s.Tool = "jellyfin_system_health"
		return s.failed(healthErr)
	}

	s.Headline = jellyfinHeadline(health, sessions, healthErr, sessionsErr)

	var fromSessions, fromHealth []string

	if sessionsErr == nil {
		fromSessions = sessions.Warnings
	} else {
		fromSessions = []string{"could not read Jellyfin's sessions: " + sessionsErr.Error()}
	}

	if healthErr == nil {
		fromHealth = slices.DeleteFunc(slices.Clone(health.Warnings), func(w string) bool {
			return slices.Contains(health.StandingWarnings, w)
		})
	} else {
		fromHealth = []string{"could not read Jellyfin's health: " + healthErr.Error()}
	}

	s.Tool = "jellyfin_active_sessions"
	if len(fromSessions) == 0 && len(fromHealth) > 0 {
		s.Tool = "jellyfin_system_health"
	}

	s.Warnings = append(fromSessions, fromHealth...)

	return s.settled()
}

// jellyfinHeadline answers the same three things the *arr line does — is it up,
// is it working, is anything stuck — for a server whose work is streams rather
// than downloads.
func jellyfinHeadline(
	h jellyfin.Health,
	s jellyfin.Sessions,
	healthErr, sessionsErr error,
) string {
	head := "v" + h.Version
	switch {
	case healthErr != nil:
		head = "health unreadable"
	case h.Version == "":
		head = "version unknown"
	}

	switch {
	case sessionsErr != nil:
		head += ", sessions unreadable"

	case s.PlayingCount == 0:
		head += ", nothing playing"

	default:
		head += fmt.Sprintf(", %d playing", s.PlayingCount)
		if transcodes := s.SoftwareCount + s.HardwareCount; transcodes > 0 {
			head += fmt.Sprintf(" (%d transcoding", transcodes)
			if s.SoftwareCount > 0 {
				head += fmt.Sprintf(", %d on the CPU", s.SoftwareCount)
			}
			head += ")"
		}
	}

	if healthErr == nil {
		if h.FailedTaskCount > 0 {
			head += fmt.Sprintf(", %d failing task(s)", h.FailedTaskCount)
		}
		if h.PendingRestart {
			head += ", restart pending"
		}
	}

	return head
}

// arrHeadline is one line for either service, since the question is the same
// for both: is it up, is it working, and is anything stuck.
func arrHeadline(version string, issues, queued, downloading, stalled, blocked int) string {
	head := "v" + version
	if version == "" {
		head = "version unknown"
	}

	head += fmt.Sprintf(", %d in the queue", queued)
	if downloading > 0 {
		head += fmt.Sprintf(" (%d downloading)", downloading)
	}
	if stalled > 0 {
		head += fmt.Sprintf(", %d stalled", stalled)
	}
	if blocked > 0 {
		head += fmt.Sprintf(", %d stuck on import", blocked)
	}
	if issues > 0 {
		head += fmt.Sprintf(", %d failing health check(s)", issues)
	}
	return head
}

// settled decides a section's status from what its check produced. Every check
// ends here, so "warnings present" and "needs attention" cannot drift apart.
func (s Section) settled() Section {
	if len(s.Warnings) > 0 {
		s.Status = StatusAttention
	} else {
		s.Status = StatusOK
	}
	return s
}

func (s Section) failed(err error) Section {
	s.Status = StatusFailed
	s.Error = err.Error()
	s.Headline = "could not be checked"
	return s
}

func (s Section) absent(why string) Section {
	s.Status = StatusAbsent
	s.Headline = why
	return s
}
