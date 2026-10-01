package prowlarr

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// Prowlarr's own view of itself. It keeps health checks for exactly the
// failures that matter downstream — indexers backed off after failures, an app
// it cannot push to, a FlareSolverr proxy that stopped answering — and those
// are the reason Radarr and Sonarr can find nothing while every container is
// up and healthy.

type HealthIssue struct {
	Type    string `json:"type" jsonschema:"ok, notice, warning or error"`
	Source  string `json:"source,omitempty" jsonschema:"the check that reported it"`
	Message string `json:"message"`
	WikiURL string `json:"wiki_url,omitempty"`
}

type Health struct {
	URL string `json:"url" jsonschema:"the address this server is talking to"`

	Version       string `json:"version,omitempty"`
	Branch        string `json:"branch,omitempty"`
	OS            string `json:"os,omitempty"`
	IsDocker      bool   `json:"is_docker,omitempty"`
	UptimeSeconds uint64 `json:"uptime_seconds,omitempty"`

	Issues []HealthIssue `json:"issues,omitempty" jsonschema:"Prowlarr's own health checks, only the ones currently failing"`

	IndexerCount        int `json:"indexer_count"`
	EnabledIndexerCount int `json:"enabled_indexer_count"`
	FailingIndexerCount int `json:"failing_indexer_count" jsonschema:"enabled indexers backed off after failures"`

	ApplicationCount int            `json:"application_count"`
	SyncLevels       map[string]int `json:"sync_levels,omitempty" jsonschema:"applications per sync level; absent when the applications could not be read"`

	ProxyCount int `json:"proxy_count" jsonschema:"indexer proxies, FlareSolverr among them"`

	Warnings []string `json:"warnings,omitempty"`
}

// GetHealth reports whether Prowlarr is in a state to feed the *arrs.
func GetHealth(ctx context.Context) (Health, error) {
	c, err := newClient()
	if err != nil {
		return Health{}, err
	}

	h := Health{URL: c.base}

	var (
		status   statusJSON
		issues   []healthJSON
		indexers []indexerJSON
		apps     []applicationJSON
		proxies  []struct {
			ID int `json:"id"`
		}

		statusErr, issuesErr, indexersErr, appsErr, proxiesErr error

		wg sync.WaitGroup
	)

	wg.Add(5)
	go func() { defer wg.Done(); statusErr = c.get(ctx, "/system/status", nil, &status) }()
	go func() { defer wg.Done(); issuesErr = c.get(ctx, "/health", nil, &issues) }()
	go func() { defer wg.Done(); indexersErr = c.get(ctx, "/indexer", nil, &indexers) }()
	go func() { defer wg.Done(); appsErr = c.get(ctx, "/applications", nil, &apps) }()
	go func() { defer wg.Done(); proxiesErr = c.get(ctx, "/indexerproxy", nil, &proxies) }()
	wg.Wait()

	// Nothing answered: this is a connection problem, not a health report.
	if statusErr != nil && issuesErr != nil && indexersErr != nil && appsErr != nil {
		return Health{}, statusErr
	}

	if statusErr == nil {
		h.Version = status.Version
		h.Branch = status.Branch
		h.IsDocker = status.IsDocker
		h.OS = strings.TrimSpace(status.OsName + " " + status.OsVersion)
		h.UptimeSeconds = secondsSince(status.StartTime)
	} else {
		h.Warnings = append(h.Warnings, "could not read Prowlarr's version: "+statusErr.Error())
	}

	if issuesErr == nil {
		for _, i := range issues {
			h.Issues = append(h.Issues, HealthIssue(i))
		}
	} else {
		h.Warnings = append(h.Warnings, "could not read Prowlarr's health checks: "+issuesErr.Error())
	}

	if indexersErr == nil {
		h.IndexerCount = len(indexers)
		for _, r := range indexers {
			if !r.Enable {
				continue
			}
			h.EnabledIndexerCount++
			if r.toIndexer(nil, nil).Failing {
				h.FailingIndexerCount++
			}
		}
	} else {
		h.Warnings = append(h.Warnings, "could not read the indexers: "+indexersErr.Error())
	}

	// An unread list is not an empty one: "0 apps" from a request that timed
	// out would read as Prowlarr syncing to nothing.
	if appsErr == nil {
		h.ApplicationCount = len(apps)
		h.SyncLevels = map[string]int{}
		for _, a := range apps {
			h.SyncLevels[a.SyncLevel]++
		}
	} else {
		h.Warnings = append(h.Warnings, "could not read the applications: "+appsErr.Error())
	}

	if proxiesErr == nil {
		h.ProxyCount = len(proxies)
	} else {
		h.Warnings = append(h.Warnings, "could not read the indexer proxies: "+proxiesErr.Error())
	}

	h.Warnings = append(h.Warnings, healthWarnings(h, indexersErr == nil, appsErr == nil)...)

	return h, nil
}

func healthWarnings(h Health, indexersRead, appsRead bool) []string {
	var out []string

	for _, i := range h.Issues {
		switch strings.ToLower(i.Type) {
		case "error", "warning":
			out = append(out, fmt.Sprintf("%s: %s", i.Source, i.Message))
		}
	}

	if indexersRead {
		switch {
		case h.IndexerCount == 0:
			out = append(out, "prowlarr has no indexer at all, so Radarr and Sonarr have nothing "+
				"to search")
		case h.EnabledIndexerCount == 0:
			out = append(out, "every indexer is disabled, so Radarr and Sonarr have nothing to search")
		case h.FailingIndexerCount == h.EnabledIndexerCount:
			out = append(out, "every enabled indexer is failing and backed off — Radarr and "+
				"Sonarr are searching nothing until one comes back")
		}
	}

	if appsRead && h.ApplicationCount == 0 {
		out = append(out, "prowlarr syncs to no application, so none of its indexers reach "+
			"Radarr or Sonarr")
	}

	return out
}

// --- wire types -----------------------------------------------------------

type statusJSON struct {
	Version   string `json:"version"`
	Branch    string `json:"branch"`
	OsName    string `json:"osName"`
	OsVersion string `json:"osVersion"`
	IsDocker  bool   `json:"isDocker"`
	StartTime string `json:"startTime"`
}

type healthJSON struct {
	Type    string `json:"type"`
	Source  string `json:"source"`
	Message string `json:"message"`
	WikiURL string `json:"wikiUrl"`
}
