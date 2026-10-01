package bazarr

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync"
)

// Bazarr's own view of itself. A Bazarr that is up and answering can still be
// finding nothing, for three reasons none of which show from outside:
//
//   - every provider it has is throttled — the subtitle sites rate-limit
//     aggressively, and a throttled provider is skipped silently until its
//     timer runs out;
//   - it has lost Sonarr or Radarr, so the library it is working from is
//     frozen at whatever it last copied;
//   - it has no language profile, or the items have none, so it wants nothing.

// Provider is one enabled subtitle provider.
type Provider struct {
	Name      string `json:"name"`
	Status    string `json:"status" jsonschema:"'Good', or the reason the provider is throttled — TooManyRequests, AuthenticationError, DownloadLimitExceeded and the like"`
	Retry     string `json:"retry,omitempty" jsonschema:"when Bazarr will try a throttled provider again, as Bazarr phrases it"`
	Throttled bool   `json:"throttled"`
}

// HealthIssue is one of Bazarr's own health checks that is failing.
type HealthIssue struct {
	Object string `json:"object" jsonschema:"what the issue is about — a root folder path or a profile name"`
	Issue  string `json:"issue"`
}

type Health struct {
	URL string `json:"url" jsonschema:"the address this server is talking to"`

	Version       string `json:"version,omitempty"`
	OS            string `json:"os,omitempty"`
	UptimeSeconds uint64 `json:"uptime_seconds,omitempty"`

	SonarrVersion string `json:"sonarr_version,omitempty" jsonschema:"what Bazarr reads from Sonarr; 'unknown' means Bazarr is configured for Sonarr and cannot reach it, absent means Sonarr is not used"`
	RadarrVersion string `json:"radarr_version,omitempty" jsonschema:"what Bazarr reads from Radarr; 'unknown' means Bazarr is configured for Radarr and cannot reach it, absent means Radarr is not used"`

	SonarrLive bool `json:"sonarr_live" jsonschema:"whether Bazarr is connected to Sonarr's live event feed; without it new episodes reach Bazarr only on its scheduled sync"`
	RadarrLive bool `json:"radarr_live" jsonschema:"the same for Radarr"`

	Issues []HealthIssue `json:"issues,omitempty" jsonschema:"Bazarr's own health checks, only the ones currently failing"`

	Providers          []Provider `json:"providers,omitempty" jsonschema:"every enabled provider, throttled first"`
	ThrottledCount     int        `json:"throttled_count"`
	LanguageProfiles   int        `json:"language_profiles"`
	WantedMovieCount   int        `json:"wanted_movie_subtitles" jsonschema:"subtitles missing across all movies — one movie missing two languages counts twice"`
	WantedEpisodeCount int        `json:"wanted_episode_subtitles" jsonschema:"subtitles missing across all episodes, counted the same way"`

	Warnings []string `json:"warnings,omitempty"`
}

// GetHealth reports whether Bazarr is in a state to find anything.
func GetHealth(ctx context.Context) (Health, error) {
	c, err := newClient()
	if err != nil {
		return Health{}, err
	}

	h := Health{URL: c.base}

	var (
		status struct {
			Data statusJSON `json:"data"`
		}
		issues struct {
			Data []HealthIssue `json:"data"`
		}
		providers struct {
			Data []providerJSON `json:"data"`
		}
		badges   badgesJSON
		profiles []profileJSON

		statusErr, issuesErr, providersErr, badgesErr, profilesErr error

		wg sync.WaitGroup
	)

	wg.Add(5)
	go func() { defer wg.Done(); statusErr = c.get(ctx, "/system/status", nil, &status) }()
	go func() { defer wg.Done(); issuesErr = c.get(ctx, "/system/health", nil, &issues) }()
	go func() { defer wg.Done(); providersErr = c.get(ctx, "/providers", nil, &providers) }()
	go func() { defer wg.Done(); badgesErr = c.get(ctx, "/badges", nil, &badges) }()
	go func() {
		defer wg.Done()
		profilesErr = c.get(ctx, "/system/languages/profiles", nil, &profiles)
	}()
	wg.Wait()

	// Nothing answered: this is a connection problem, not a health report.
	if statusErr != nil && issuesErr != nil && providersErr != nil && badgesErr != nil {
		return Health{}, statusErr
	}

	if statusErr == nil {
		s := status.Data
		h.Version = s.BazarrVersion
		h.OS = s.OperatingSystem
		h.UptimeSeconds = secondsSinceEpoch(s.StartTime)
		h.SonarrVersion = s.SonarrVersion
		h.RadarrVersion = s.RadarrVersion
	} else {
		h.Warnings = append(h.Warnings, "could not read Bazarr's version: "+statusErr.Error())
	}

	if issuesErr == nil {
		h.Issues = issues.Data
	} else {
		h.Warnings = append(h.Warnings, "could not read Bazarr's health checks: "+issuesErr.Error())
	}

	if providersErr == nil {
		h.Providers = toProviders(providers.Data)
		for _, p := range h.Providers {
			if p.Throttled {
				h.ThrottledCount++
			}
		}
	} else {
		h.Warnings = append(h.Warnings, "could not read Bazarr's providers: "+providersErr.Error())
	}

	if badgesErr == nil {
		h.WantedMovieCount = badges.Movies
		h.WantedEpisodeCount = badges.Episodes
		h.SonarrLive = badges.SonarrSignalr != "DOWN"
		h.RadarrLive = badges.RadarrSignalr != "DOWN"
	}

	if profilesErr == nil {
		h.LanguageProfiles = len(profiles)
	}

	h.Warnings = append(h.Warnings, healthWarnings(h, providersErr == nil, badgesErr == nil)...)

	return h, nil
}

func healthWarnings(h Health, providersRead, badgesRead bool) []string {
	var out []string

	for _, i := range h.Issues {
		out = append(out, fmt.Sprintf("%s: %s", i.Object, i.Issue))
	}

	if providersRead {
		switch {
		case len(h.Providers) == 0:
			out = append(out, "no subtitle provider is enabled, so Bazarr has nowhere to look "+
				"and will never download anything — add one in Settings → Providers")
		case h.ThrottledCount == len(h.Providers):
			out = append(out, fmt.Sprintf(
				"every provider is throttled (%s) — Bazarr is searching nothing until one of them "+
					"comes back; bazarr_providers_reset clears the throttles now",
				throttledSummary(h.Providers)))
		case h.ThrottledCount > 0:
			out = append(out, fmt.Sprintf(
				"%d of %d providers %s throttled and being skipped: %s",
				h.ThrottledCount, len(h.Providers), plural(h.ThrottledCount, "is", "are"),
				throttledSummary(h.Providers)))
		}
	}

	switch h.SonarrVersion {
	case "unknown":
		out = append(out, "bazarr cannot reach Sonarr, so it is working from a stale copy of "+
			"the series library and will not see new episodes")
	case "":
	default:
		if badgesRead && !h.SonarrLive {
			out = append(out, "bazarr is not connected to Sonarr's live feed — new episodes "+
				"reach it only on the scheduled sync, so their subtitles arrive late")
		}
	}
	switch h.RadarrVersion {
	case "unknown":
		out = append(out, "bazarr cannot reach Radarr, so it is working from a stale copy of "+
			"the movie library and will not see new films")
	case "":
	default:
		if badgesRead && !h.RadarrLive {
			out = append(out, "bazarr is not connected to Radarr's live feed — new films reach "+
				"it only on the scheduled sync, so their subtitles arrive late")
		}
	}

	return out
}

func throttledSummary(providers []Provider) string {
	var parts []string
	for _, p := range providers {
		if !p.Throttled {
			continue
		}
		s := fmt.Sprintf("%s: %s", p.Name, p.Status)
		if p.Retry != "" {
			s += ", retrying " + p.Retry
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, "; ")
}

// --- resetting the throttles --------------------------------------------------

// ProviderReset is the outcome of clearing every provider throttle.
type ProviderReset struct {
	ClearedCount int        `json:"cleared_count" jsonschema:"providers that were throttled before the reset"`
	Providers    []Provider `json:"providers" jsonschema:"every enabled provider, as Bazarr reports it after the reset"`
	Warnings     []string   `json:"warnings,omitempty"`
}

// GetProviders reads the enabled providers, throttled first.
func GetProviders(ctx context.Context) ([]Provider, error) {
	c, err := newClient()
	if err != nil {
		return nil, err
	}
	return c.providers(ctx)
}

func (c *client) providers(ctx context.Context) ([]Provider, error) {
	var raw struct {
		Data []providerJSON `json:"data"`
	}
	if err := c.get(ctx, "/providers", nil, &raw); err != nil {
		return nil, err
	}
	return toProviders(raw.Data), nil
}

// toProviders decodes the list throttled first: they are why anyone is looking.
func toProviders(raw []providerJSON) []Provider {
	out := make([]Provider, 0, len(raw))
	for _, p := range raw {
		prov := Provider{Name: p.Name, Status: p.Status, Retry: p.Retry}
		prov.Throttled = p.Status != "" && !strings.EqualFold(p.Status, "Good")
		if prov.Retry == "-" {
			prov.Retry = ""
		}
		out = append(out, prov)
	}
	slices.SortStableFunc(out, func(a, b Provider) int {
		switch {
		case a.Throttled == b.Throttled:
			return 0
		case a.Throttled:
			return -1
		default:
			return 1
		}
	})
	return out
}

// ResetProviders clears every throttle, then reads the providers back.
//
// A throttle is not always a nuisance to clear. TooManyRequests and
// DownloadLimitExceeded are the site saying "not now", and clearing them sends
// the next search straight back into the same wall; AuthenticationError means
// the credentials are wrong and will be again on the first request. The result
// says which of those it just cleared, so the reset is not mistaken for a fix.
func ResetProviders(ctx context.Context, before []Provider) (ProviderReset, error) {
	c, err := newClient()
	if err != nil {
		return ProviderReset{}, err
	}

	var res ProviderReset
	for _, p := range before {
		if p.Throttled {
			res.ClearedCount++
			res.Warnings = append(res.Warnings, throttleAdvice(p)...)
		}
	}

	if err := c.send(ctx, "POST", "/providers", url.Values{"action": {"reset"}},
		requestTimeout); err != nil {
		return res, err
	}

	after, err := c.providers(ctx)
	if err != nil {
		res.Warnings = append(res.Warnings, "the throttles were reset but the providers could "+
			"not be read back: "+err.Error())
		return res, nil
	}
	res.Providers = after
	return res, nil
}

func throttleAdvice(p Provider) []string {
	reason := strings.ToLower(p.Status)
	switch {
	case strings.Contains(reason, "auth"), strings.Contains(reason, "configuration"),
		strings.Contains(reason, "payment"):
		return []string{fmt.Sprintf("%s was throttled for %s — that is a credential or account "+
			"problem, and it will be throttled again on its next request until Settings → "+
			"Providers is fixed", p.Name, p.Status)}
	case strings.Contains(reason, "toomany"), strings.Contains(reason, "limit"):
		return []string{fmt.Sprintf("%s was throttled for %s — the site asked Bazarr to back "+
			"off, and searching hard right after the reset is the quickest way back into it",
			p.Name, p.Status)}
	}
	return nil
}

// --- wire types -----------------------------------------------------------

type statusJSON struct {
	BazarrVersion   string  `json:"bazarr_version"`
	SonarrVersion   string  `json:"sonarr_version"`
	RadarrVersion   string  `json:"radarr_version"`
	OperatingSystem string  `json:"operating_system"`
	StartTime       float64 `json:"start_time"`
}

type providerJSON struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Retry  string `json:"retry"`
}

type badgesJSON struct {
	Episodes      int    `json:"episodes"`
	Movies        int    `json:"movies"`
	Providers     int    `json:"providers"`
	Status        int    `json:"status"`
	SonarrSignalr string `json:"sonarr_signalr"`
	RadarrSignalr string `json:"radarr_signalr"`
}
