package jellyfin

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Jellyfin's activity log is its own account of what happened: who signed in
// and from where, who failed to, what was played, which scheduled task failed,
// which plugin was updated. It is the answer to "who was watching last night"
// and — the reason it earns a tool — "is someone trying passwords on this".

const (
	defaultActivityHours = 24
	maxActivityHours     = 24 * 30
	defaultActivityLimit = 50
	maxActivityLimit     = 200
)

// A burst of failed logins worth a warning.
const failedLoginWarn = 5

type ActivityEntry struct {
	Name       string `json:"name"`
	Overview   string `json:"overview,omitempty"`
	Type       string `json:"type"`
	Severity   string `json:"severity" jsonschema:"Information, Warning or Error"`
	SecondsAgo uint64 `json:"seconds_ago"`
}

type Activity struct {
	Hours   int             `json:"hours"`
	Entries []ActivityEntry `json:"entries" jsonschema:"most recent first"`

	TotalCount     int `json:"total_count" jsonschema:"entries in the window, before the limit"`
	FailedLogins   int `json:"failed_logins" jsonschema:"among the entries read"`
	ProblemEntries int `json:"problem_entries" jsonschema:"warnings and errors among the entries read"`

	Warnings []string `json:"warnings,omitempty"`
}

// GetActivity reads the activity log. Administrator-only.
func GetActivity(ctx context.Context, hours, limit int, onlyProblems bool) (Activity, error) {
	switch {
	case hours <= 0:
		hours = defaultActivityHours
	case hours > maxActivityHours:
		hours = maxActivityHours
	}
	switch {
	case limit <= 0:
		limit = defaultActivityLimit
	case limit > maxActivityLimit:
		limit = maxActivityLimit
	}

	c, err := newClient()
	if err != nil {
		return Activity{}, err
	}

	since := time.Now().Add(-time.Duration(hours) * time.Hour).UTC().Format(time.RFC3339)
	q := url.Values{"minDate": {since}, "startIndex": {"0"}, "limit": {strconv.Itoa(maxActivityLimit)}}

	var raw struct {
		Items []struct {
			Name          string `json:"Name"`
			Overview      string `json:"Overview"`
			ShortOverview string `json:"ShortOverview"`
			Type          string `json:"Type"`
			Date          string `json:"Date"`
			Severity      string `json:"Severity"`
		} `json:"Items"`
		TotalRecordCount int `json:"TotalRecordCount"`
	}
	if err := c.get(ctx, "/System/ActivityLog/Entries", q, &raw); err != nil {
		return Activity{}, err
	}

	out := Activity{Hours: hours, TotalCount: raw.TotalRecordCount}
	for _, r := range raw.Items {
		problem := !strings.EqualFold(r.Severity, "Information")
		if strings.Contains(r.Type, "AuthenticationFailed") {
			out.FailedLogins++
			problem = true
		}
		if problem {
			out.ProblemEntries++
		}
		if onlyProblems && !problem {
			continue
		}
		if len(out.Entries) < limit {
			out.Entries = append(out.Entries, ActivityEntry{
				Name:       r.Name,
				Overview:   nonEmpty(r.ShortOverview, r.Overview),
				Type:       r.Type,
				Severity:   r.Severity,
				SecondsAgo: secondsSince(r.Date),
			})
		}
	}

	if out.FailedLogins >= failedLoginWarn {
		out.Warnings = append(out.Warnings, fmt.Sprintf("%d failed sign-ins in the last %d hours — "+
			"if this server is reachable from the internet, someone may be guessing passwords; "+
			"the entries name the user and address", out.FailedLogins, hours))
	}
	if out.TotalCount > len(raw.Items) {
		out.Warnings = append(out.Warnings, fmt.Sprintf("the window holds %d entries and the "+
			"newest %d were read — shorten 'hours' to see all of a busy period",
			out.TotalCount, len(raw.Items)))
	}
	return out, nil
}
