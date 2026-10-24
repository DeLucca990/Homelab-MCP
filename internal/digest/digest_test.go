package digest

import (
	"fmt"
	"testing"
	"time"

	"github.com/DeLucca990/nautilus/internal/containers"
	"github.com/DeLucca990/nautilus/internal/jellyfin"
)

func TestContainerEvents(t *testing.T) {
	const hour = 3600
	cs := []containers.Container{
		{Name: "steady", State: "running", StateForSeconds: 30 * 24 * hour, AgeSeconds: 60 * 24 * hour},
		{Name: "restarted", State: "running", StateForSeconds: 2 * hour, AgeSeconds: 90 * 24 * hour},
		{Name: "oom", State: "running", StateForSeconds: 3 * hour, AgeSeconds: 90 * 24 * hour, OOMKilled: true},
		{Name: "deployed", State: "running", StateForSeconds: 5 * hour, AgeSeconds: 5*hour + 10},
		{Name: "died", State: "exited", StateForSeconds: 1 * hour, AgeSeconds: 9 * 24 * hour, ExitCode: 137},
		{Name: "unknown", State: "running"},
	}

	got := containerEvents(cs, 24*time.Hour, 0, false)
	want := "[{died stopped 3600 false 137} {restarted restarted 7200 false 0} {oom restarted 10800 true 0} {deployed recreated 18000 false 0}]"
	if fmt.Sprint(got) != want {
		t.Errorf("got  %v\nwant %s", got, want)
	}
}

func TestContainerEventsLeaveOutTheBoot(t *testing.T) {
	const hour = 3600
	uptime := 6 * time.Hour
	cs := []containers.Container{
		{Name: "with-the-boot", State: "running", StateForSeconds: 6*hour - 40, AgeSeconds: 90 * 24 * hour},
		{Name: "after-the-boot", State: "running", StateForSeconds: 2 * hour, AgeSeconds: 90 * 24 * hour},
	}

	got := containerEvents(cs, 24*time.Hour, uptime, true)
	if len(got) != 1 || got[0].Container != "after-the-boot" {
		t.Errorf("got %v, want only the restart after the boot", got)
	}
}

func TestLateWindowJudgesEachReleaseOnce(t *testing.T) {
	window := 24 * time.Hour
	today := time.Date(2026, 10, 17, 17, 0, 0, 0, time.Local)
	yesterday := today.Add(-window)

	from, to := lateWindow(today, window)
	prevFrom, prevTo := lateWindow(yesterday, window)
	if !prevTo.Equal(from) {
		t.Errorf("a gap or an overlap between runs: yesterday's window ends %v, today's starts %v", prevTo, from)
	}
	if to.Sub(from) != window || prevTo.Sub(prevFrom) != window {
		t.Errorf("the late window is not as long as the digest window")
	}
	if !to.Equal(today.Add(-lateGrace)) {
		t.Errorf("the late window ends %v, want the grace before now", to)
	}

	// The calendar read has to reach back past the start of the late window.
	if back := today.AddDate(0, 0, -calendarPastDays(window)); back.After(from) {
		t.Errorf("the calendar starts %v, after the late window does (%v)", back, from)
	}
}

func TestSummarizeActivity(t *testing.T) {
	p := summarizeActivity([]jellyfin.ActivityEntry{
		{Name: "pedro is playing Dune", Type: "VideoPlayback", Severity: "Information"},
		{Name: "pedro has finished playing Dune", Type: "VideoPlaybackStopped", Severity: "Information"},
		{Name: "Failed login attempt from admin", Overview: "IP address: 203.0.113.5", Type: "AuthenticationFailed", Severity: "Error"},
		{Name: "Scan Media Library failed", Type: "TaskCompleted", Severity: "Error"},
		{Name: "ana is playing a song", Type: "AudioPlayback", Severity: "Information"},
	})

	if p.Plays != 2 || len(p.Watched) != 2 || p.Watched[1] != "ana is playing a song" {
		t.Errorf("plays: %d %v", p.Plays, p.Watched)
	}
	if p.FailedLogins != 1 || p.FailedLoginEntries[0] != "Failed login attempt from admin — IP address: 203.0.113.5" {
		t.Errorf("failed logins: %d %v", p.FailedLogins, p.FailedLoginEntries)
	}
	if len(p.Problems) != 1 || p.Problems[0] != "Scan Media Library failed" {
		t.Errorf("a failed sign-in counted as a problem too, or the task failure was lost: %v", p.Problems)
	}
}
