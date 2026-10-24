package digest

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/DeLucca990/nautilus/internal/bazarr"
	"github.com/DeLucca990/nautilus/internal/containers"
	"github.com/DeLucca990/nautilus/internal/jellyfin"
	"github.com/DeLucca990/nautilus/internal/overview"
	"github.com/DeLucca990/nautilus/internal/prowlarr"
	"github.com/DeLucca990/nautilus/internal/radarr"
	"github.com/DeLucca990/nautilus/internal/sonarr"
	"github.com/DeLucca990/nautilus/internal/system"
)

const (
	DefaultHours = 24
	MaxHours     = 24 * 7

	lateGrace = 6 * time.Hour

	upcomingWindow = 24 * time.Hour

	historyLimit = 200

	bootSlack = 10 * time.Minute

	recreateSlack = time.Minute

	maxWatched      = 10
	maxFailedLogins = 5
	maxProblems     = 5
)

type Digest struct {
	Hours       int    `json:"hours"`
	GeneratedAt string `json:"generated_at" jsonschema:"RFC 3339, in this server's local time"`

	Attention []Finding `json:"attention,omitempty" jsonschema:"what nautilus_overview reports as needing attention right now, one warning per entry"`

	RebootedSecondsAgo uint64           `json:"rebooted_seconds_ago,omitempty" jsonschema:"set when the host booted within the window"`
	Containers         []ContainerEvent `json:"containers,omitempty" jsonschema:"containers that restarted, were recreated or stopped within the window, apart from those that came up with a reboot"`

	Imported []Download `json:"imported,omitempty" jsonschema:"files Radarr and Sonarr imported within the window, newest first"`
	Failed   []Download `json:"failed,omitempty" jsonschema:"downloads that failed within the window, newest first, with the reason recorded"`
	Late     []Release  `json:"late,omitempty" jsonschema:"monitored releases due within the window shifted back 6 hours, still without a file — late, as opposed to not out yet"`
	Upcoming []Release  `json:"upcoming,omitempty" jsonschema:"monitored releases due in the next 24 hours"`

	Playback *Playback `json:"playback,omitempty" jsonschema:"Jellyfin's activity log for the window; absent where Jellyfin is not configured or its log could not be read"`

	Totals Totals `json:"totals" jsonschema:"numbers worth comparing from one digest to the next"`

	Truncated []string `json:"truncated,omitempty" jsonschema:"lists that hold only part of the window, and why"`
	Errors    []string `json:"errors,omitempty" jsonschema:"parts that could not be read; the rest still answered"`

	ElapsedMs int `json:"elapsed_ms"`
}

type Finding struct {
	Area    string `json:"area"`
	Warning string `json:"warning"`
}

type ContainerEvent struct {
	Container  string `json:"container"`
	Event      string `json:"event" jsonschema:"restarted, recreated (deployed again, usually a new image) or stopped"`
	SecondsAgo uint64 `json:"seconds_ago"`
	OOMKilled  bool   `json:"oom_killed,omitempty"`
	ExitCode   int    `json:"exit_code,omitempty" jsonschema:"for a stopped container"`
}

type Download struct {
	Service    string `json:"service" jsonschema:"radarr or sonarr"`
	Title      string `json:"title" jsonschema:"the movie, or the series"`
	Episode    string `json:"episode,omitempty" jsonschema:"for Sonarr, e.g. S02E03"`
	Quality    string `json:"quality,omitempty"`
	Detail     string `json:"detail,omitempty" jsonschema:"for a failure, the reason the service recorded"`
	SecondsAgo uint64 `json:"seconds_ago"`
}

type Release struct {
	Service string `json:"service" jsonschema:"radarr or sonarr"`
	Title   string `json:"title" jsonschema:"the movie, or the series"`
	Episode string `json:"episode,omitempty" jsonschema:"for Sonarr, e.g. S02E03"`
	Kind    string `json:"kind,omitempty" jsonschema:"for Radarr: digital or physical"`
	Due     string `json:"due" jsonschema:"for Sonarr YYYY-MM-DD HH:MM, local; for Radarr YYYY-MM-DD, a whole day"`
}

type Playback struct {
	Plays   int      `json:"plays" jsonschema:"playbacks started within the window"`
	Watched []string `json:"watched,omitempty" jsonschema:"Jellyfin's own line for each playback started, newest first, at most 10"`

	FailedLogins       int      `json:"failed_logins"`
	FailedLoginEntries []string `json:"failed_login_entries,omitempty" jsonschema:"who and from where, newest first, at most 5"`

	Problems []string `json:"problems,omitempty" jsonschema:"warnings and errors other than sign-ins — a scheduled task that failed, a plugin that would not load — newest first, at most 5"`
}

type Totals struct {
	Disks []Disk `json:"disks,omitempty" jsonschema:"every writable filesystem, fullest first"`

	ContainerRestarts map[string]int `json:"container_restarts,omitempty" jsonschema:"Docker's restart count per container. It resets when a container is recreated"`

	WantedMovieSubtitles   *int `json:"wanted_movie_subtitles,omitempty" jsonschema:"absent where Bazarr is not configured"`
	WantedEpisodeSubtitles *int `json:"wanted_episode_subtitles,omitempty"`
	FailingIndexers        *int `json:"failing_indexers,omitempty" jsonschema:"absent where Prowlarr is not configured"`
}

type Disk struct {
	Mountpoint  string  `json:"mountpoint"`
	UsedBytes   uint64  `json:"used_bytes"`
	TotalBytes  uint64  `json:"total_bytes"`
	UsedPercent float64 `json:"used_percent"`
}

func Get(ctx context.Context, hours int) Digest {
	switch {
	case hours <= 0:
		hours = DefaultHours
	case hours > MaxHours:
		hours = MaxHours
	}
	start := time.Now()
	window := time.Duration(hours) * time.Hour

	d := Digest{Hours: hours, GeneratedAt: start.Format(time.RFC3339)}
	var mu sync.Mutex
	report := func(err error, format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		d.Errors = append(d.Errors, fmt.Sprintf(format, args...)+": "+err.Error())
	}
	truncated := func(what string) {
		mu.Lock()
		defer mu.Unlock()
		d.Truncated = append(d.Truncated, what)
	}

	parts := []func(){
		func() { d.Attention = attention(ctx, report) },
		func() { d.RebootedSecondsAgo, d.Containers, d.Totals.ContainerRestarts = machine(ctx, window, report) },
		func() { d.Totals.Disks = disks(ctx, report) },
	}

	var radarrDone, sonarrDone history
	if radarr.Configured() {
		parts = append(parts, func() { radarrDone = radarrHistory(ctx, start, window, report, truncated) })
	}
	if sonarr.Configured() {
		parts = append(parts, func() { sonarrDone = sonarrHistory(ctx, start, window, report, truncated) })
	}
	if jellyfin.Configured() {
		parts = append(parts, func() { d.Playback = playback(ctx, hours, report, truncated) })
	}
	if bazarr.Configured() {
		parts = append(parts, func() {
			h, err := bazarr.GetHealth(ctx)
			if err != nil {
				report(err, "bazarr")
				return
			}
			d.Totals.WantedMovieSubtitles = &h.WantedMovieCount
			d.Totals.WantedEpisodeSubtitles = &h.WantedEpisodeCount
		})
	}
	if prowlarr.Configured() {
		parts = append(parts, func() {
			h, err := prowlarr.GetHealth(ctx)
			if err != nil {
				report(err, "prowlarr")
				return
			}
			d.Totals.FailingIndexers = &h.FailingIndexerCount
		})
	}

	var wg sync.WaitGroup
	for _, part := range parts {
		wg.Go(part)
	}
	wg.Wait()

	for _, h := range []history{radarrDone, sonarrDone} {
		d.Imported = append(d.Imported, h.imported...)
		d.Failed = append(d.Failed, h.failed...)
		d.Late = append(d.Late, h.late...)
		d.Upcoming = append(d.Upcoming, h.upcoming...)
	}
	newestFirst := func(a, b Download) int { return int(a.SecondsAgo) - int(b.SecondsAgo) }
	slices.SortStableFunc(d.Imported, newestFirst)
	slices.SortStableFunc(d.Failed, newestFirst)
	byDue := func(a, b Release) int { return strings.Compare(a.Due, b.Due) }
	slices.SortStableFunc(d.Late, byDue)
	slices.SortStableFunc(d.Upcoming, byDue)
	slices.Sort(d.Errors)

	d.ElapsedMs = int(time.Since(start).Milliseconds())
	return d
}

type reportFunc func(err error, format string, args ...any)

func attention(ctx context.Context, report reportFunc) []Finding {
	var out []Finding
	for _, s := range overview.Get(ctx).Sections {
		for _, w := range s.Warnings {
			out = append(out, Finding{Area: s.Name, Warning: w})
		}
		if s.Status == overview.StatusFailed {
			report(errors.New(s.Error), "%s could not be checked", s.Name)
		}
	}
	return out
}

func machine(ctx context.Context, window time.Duration, report reportFunc) (rebooted uint64, events []ContainerEvent, restarts map[string]int) {
	var uptime time.Duration
	if h, err := system.GetHostInfo(ctx); err != nil {
		report(err, "host")
	} else {
		uptime = time.Duration(h.UptimeSeconds) * time.Second
		if uptime > 0 && uptime <= window {
			rebooted = h.UptimeSeconds
		}
	}

	status, err := containers.GetContainerStatus(ctx, nil, true)
	switch {
	case errors.Is(err, containers.ErrUnavailable):
		return rebooted, nil, nil
	case err != nil:
		report(err, "docker")
		return rebooted, nil, nil
	}
	return rebooted, containerEvents(status.Containers, window, uptime, rebooted > 0), restartCounts(status.Containers)
}

func containerEvents(cs []containers.Container, window, uptime time.Duration, rebooted bool) []ContainerEvent {
	var out []ContainerEvent
	for _, c := range cs {
		since := time.Duration(c.StateForSeconds) * time.Second
		if c.StateForSeconds == 0 || since > window {
			continue
		}
		if rebooted && since >= uptime-bootSlack {
			continue
		}

		e := ContainerEvent{Container: c.Name, SecondsAgo: c.StateForSeconds, OOMKilled: c.OOMKilled}
		switch {
		case c.State != "running":
			e.Event, e.ExitCode = "stopped", c.ExitCode
		case c.AgeSeconds > 0 && time.Duration(c.AgeSeconds-c.StateForSeconds)*time.Second < recreateSlack:
			e.Event = "recreated"
		default:
			e.Event = "restarted"
		}
		out = append(out, e)
	}
	slices.SortStableFunc(out, func(a, b ContainerEvent) int { return int(a.SecondsAgo) - int(b.SecondsAgo) })
	return out
}

func restartCounts(cs []containers.Container) map[string]int {
	out := map[string]int{}
	for _, c := range cs {
		out[c.Name] = c.RestartCount
	}
	return out
}

func disks(ctx context.Context, report reportFunc) []Disk {
	stats, err := system.GetDiskStats(ctx, false)
	if err != nil {
		report(err, "disk")
		return nil
	}
	var out []Disk
	for _, fs := range stats.Filesystems {
		if fs.ReadOnly || fs.Error != "" || fs.TotalBytes == 0 {
			continue
		}
		out = append(out, Disk{Mountpoint: fs.Mountpoint, UsedBytes: fs.UsedBytes,
			TotalBytes: fs.TotalBytes, UsedPercent: math.Round(fs.UsedPercent*10) / 10})
	}
	slices.SortStableFunc(out, func(a, b Disk) int {
		switch {
		case a.UsedPercent > b.UsedPercent:
			return -1
		case a.UsedPercent < b.UsedPercent:
			return 1
		}
		return 0
	})
	return out
}

type history struct {
	imported, failed []Download
	late, upcoming   []Release
}

func lateWindow(now time.Time, window time.Duration) (from, to time.Time) {
	return now.Add(-window - lateGrace), now.Add(-lateGrace)
}

func calendarPastDays(window time.Duration) int {
	return int(math.Ceil((window+lateGrace).Hours()/24)) + 1
}

func within(event uint64, window time.Duration) bool {
	return time.Duration(event)*time.Second <= window
}

func radarrHistory(ctx context.Context, now time.Time, window time.Duration, report reportFunc, truncated func(string)) history {
	var out history

	h, err := radarr.GetHistory(ctx, 0, historyLimit)
	if err != nil {
		report(err, "radarr history")
	} else {
		for _, e := range h.Events {
			if !within(e.SecondsAgo, window) {
				continue
			}
			dl := Download{Service: "radarr", Title: e.Movie, Quality: e.Quality, SecondsAgo: e.SecondsAgo}
			switch e.Event {
			case "imported":
				out.imported = append(out.imported, dl)
			case "failed":
				dl.Detail = e.Detail
				out.failed = append(out.failed, dl)
			}
		}
		if len(h.Events) == historyLimit && within(h.Events[len(h.Events)-1].SecondsAgo, window) {
			truncated(fmt.Sprintf("radarr: the newest %d events are all within the window, so older ones were not read", historyLimit))
		}
	}

	cal, err := radarr.GetCalendar(ctx, calendarPastDays(window), 2)
	if err != nil {
		report(err, "radarr calendar")
		return out
	}
	lateFrom, lateTo := lateWindow(now, window)
	for _, e := range cal.Entries {
		if !e.Monitored || e.HasFile || e.Release == "inCinemas" {
			continue
		}
		due, err := time.Parse("2006-01-02", e.Date)
		if err != nil {
			continue
		}
		r := Release{Service: "radarr", Title: e.Movie, Kind: e.Release, Due: e.Date}
		switch {
		case !due.Before(lateFrom) && !due.After(lateTo):
			out.late = append(out.late, r)
		case due.After(now) && !due.After(now.Add(upcomingWindow)):
			out.upcoming = append(out.upcoming, r)
		}
	}
	return out
}

func sonarrHistory(ctx context.Context, now time.Time, window time.Duration, report reportFunc, truncated func(string)) history {
	var out history

	h, err := sonarr.GetHistory(ctx, 0, nil, historyLimit)
	if err != nil {
		report(err, "sonarr history")
	} else {
		for _, e := range h.Events {
			if !within(e.SecondsAgo, window) {
				continue
			}
			dl := Download{Service: "sonarr", Title: e.Series, Episode: e.Episode, Quality: e.Quality, SecondsAgo: e.SecondsAgo}
			switch e.Event {
			case "imported":
				out.imported = append(out.imported, dl)
			case "failed":
				dl.Detail = e.Detail
				out.failed = append(out.failed, dl)
			}
		}
		if len(h.Events) == historyLimit && within(h.Events[len(h.Events)-1].SecondsAgo, window) {
			truncated(fmt.Sprintf("sonarr: the newest %d events are all within the window, so older ones were not read", historyLimit))
		}
	}

	cal, err := sonarr.GetCalendar(ctx, calendarPastDays(window), 2)
	if err != nil {
		report(err, "sonarr calendar")
		return out
	}
	lateFrom, lateTo := lateWindow(now, window)
	for _, e := range cal.Entries {
		if !e.Monitored || e.HasFile {
			continue
		}
		due, err := time.ParseInLocation("2006-01-02 15:04", e.AirDate, time.Local)
		if err != nil {
			continue
		}
		r := Release{Service: "sonarr", Title: e.Series, Episode: e.Episode, Due: e.AirDate}
		switch {
		case !due.Before(lateFrom) && !due.After(lateTo):
			out.late = append(out.late, r)
		case due.After(now) && !due.After(now.Add(upcomingWindow)):
			out.upcoming = append(out.upcoming, r)
		}
	}
	return out
}

func playback(ctx context.Context, hours int, report reportFunc, truncated func(string)) *Playback {
	a, err := jellyfin.GetActivity(ctx, hours, 200, false)
	if err != nil {
		report(err, "jellyfin activity")
		return nil
	}
	if a.TotalCount > len(a.Entries) {
		truncated(fmt.Sprintf("jellyfin: %d entries in the window and the newest %d were read", a.TotalCount, len(a.Entries)))
	}
	return summarizeActivity(a.Entries)
}

func summarizeActivity(entries []jellyfin.ActivityEntry) *Playback {
	p := &Playback{}
	for _, e := range entries {
		switch {
		case e.Type == "VideoPlayback" || e.Type == "AudioPlayback":
			p.Plays++
			if len(p.Watched) < maxWatched {
				p.Watched = append(p.Watched, e.Name)
			}
		case strings.Contains(e.Type, "AuthenticationFailed"):
			p.FailedLogins++
			if len(p.FailedLoginEntries) < maxFailedLogins {
				line := e.Name
				if e.Overview != "" {
					line += " — " + e.Overview
				}
				p.FailedLoginEntries = append(p.FailedLoginEntries, line)
			}
		case !strings.EqualFold(e.Severity, "Information"):
			if len(p.Problems) < maxProblems {
				p.Problems = append(p.Problems, e.Name)
			}
		}
	}
	return p
}
