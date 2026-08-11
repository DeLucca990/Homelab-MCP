package sonarr

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// Sonarr keeps its own health checks — indexers that stopped answering, a
// download client that is unreachable, a root folder that vanished. They are
// the reason a queue can be empty and a library can be missing half its
// episodes while nothing looks broken from the outside.

type HealthIssue struct {
	Type    string `json:"type" jsonschema:"ok, notice, warning or error"`
	Source  string `json:"source,omitempty" jsonschema:"the check that reported it"`
	Message string `json:"message"`
	WikiURL string `json:"wiki_url,omitempty"`
}

// Sonarr reports free space per root folder but not the size of the disk behind
// it, so there is no percentage to give here — system_disk_usage is the tool
// that knows that.
type RootFolderSpace struct {
	Path            string `json:"path"`
	Accessible      bool   `json:"accessible"`
	FreeBytes       uint64 `json:"free_bytes,omitempty"`
	UnmappedFolders int    `json:"unmapped_folders,omitempty" jsonschema:"folders inside this root that Sonarr has no series for"`
}

type Health struct {
	URL string `json:"url" jsonschema:"the address this server is talking to"`

	AppName  string `json:"app_name,omitempty"`
	Instance string `json:"instance,omitempty"`
	Version  string `json:"version,omitempty"`
	Branch   string `json:"branch,omitempty"`
	OS       string `json:"os,omitempty"`
	IsDocker bool   `json:"is_docker,omitempty"`

	UptimeSeconds uint64 `json:"uptime_seconds,omitempty"`

	Issues []HealthIssue `json:"issues,omitempty" jsonschema:"Sonarr's own health checks, only the ones currently failing"`

	QueueCount        int  `json:"queue_count"`
	QueueUnknownCount int  `json:"queue_unknown_count,omitempty" jsonschema:"queue items the download client holds that Sonarr cannot match to a series"`
	QueueErrors       bool `json:"queue_errors,omitempty"`
	QueueWarnings     bool `json:"queue_warnings,omitempty"`

	RootFolders []RootFolderSpace `json:"root_folders,omitempty"`

	Warnings []string `json:"warnings,omitempty"`
}

// GetHealth reports whether Sonarr itself is in a state to do its job.
func GetHealth(ctx context.Context) (Health, error) {
	c, err := newClient()
	if err != nil {
		return Health{}, err
	}

	h := Health{URL: c.base}

	var (
		status  systemStatusJSON
		issues  []healthJSON
		queue   queueStatusJSON
		folders []rootFolderJSON

		statusErr, issuesErr, queueErr, foldersErr error

		wg sync.WaitGroup
	)

	wg.Add(4)
	go func() { defer wg.Done(); statusErr = c.get(ctx, "/system/status", nil, &status) }()
	go func() { defer wg.Done(); issuesErr = c.get(ctx, "/health", nil, &issues) }()
	go func() { defer wg.Done(); queueErr = c.get(ctx, "/queue/status", nil, &queue) }()
	go func() { defer wg.Done(); foldersErr = c.get(ctx, "/rootfolder", nil, &folders) }()
	wg.Wait()

	// Nothing answered: this is a connection problem, not a health report.
	if statusErr != nil && issuesErr != nil && queueErr != nil && foldersErr != nil {
		return Health{}, statusErr
	}

	if statusErr == nil {
		h.AppName = status.AppName
		h.Instance = status.InstanceName
		h.Version = status.Version
		h.Branch = status.Branch
		h.IsDocker = status.IsDocker
		h.OS = strings.TrimSpace(status.OsName + " " + status.OsVersion)
		h.UptimeSeconds = secondsSince(status.StartTime)
	} else {
		h.Warnings = append(h.Warnings, "could not read Sonarr's version: "+statusErr.Error())
	}

	if issuesErr == nil {
		for _, i := range issues {
			h.Issues = append(h.Issues, HealthIssue{
				Type:    i.Type,
				Source:  i.Source,
				Message: i.Message,
				WikiURL: i.WikiURL,
			})
		}
	} else {
		h.Warnings = append(h.Warnings, "could not read Sonarr's health checks: "+issuesErr.Error())
	}

	if queueErr == nil {
		h.QueueCount = queue.TotalCount
		h.QueueUnknownCount = queue.UnknownCount
		h.QueueErrors = queue.Errors || queue.UnknownErrors
		h.QueueWarnings = queue.Warnings || queue.UnknownWarnings
	}

	if foldersErr == nil {
		for _, f := range folders {
			space := RootFolderSpace{
				Path:            f.Path,
				Accessible:      f.Accessible,
				UnmappedFolders: len(f.UnmappedFolders),
			}
			if f.FreeSpace > 0 {
				space.FreeBytes = uint64(f.FreeSpace)
			}
			h.RootFolders = append(h.RootFolders, space)
		}
	}

	h.Warnings = append(h.Warnings, healthWarnings(h)...)

	return h, nil
}

func healthWarnings(h Health) []string {
	var out []string

	for _, i := range h.Issues {
		switch strings.ToLower(i.Type) {
		case "error", "warning":
			out = append(out, fmt.Sprintf("%s: %s", i.Source, i.Message))
		}
	}

	for _, f := range h.RootFolders {
		if !f.Accessible {
			out = append(out, fmt.Sprintf(
				"root folder %s is not accessible — anything downloaded into it will fail to import",
				f.Path))
		}
	}

	if h.QueueErrors {
		out = append(out, "the download queue has items in error — sonarr_queue_status shows which")
	}
	if h.QueueUnknownCount > 0 {
		out = append(out, fmt.Sprintf(
			"%d queue %s held by the download client that Sonarr cannot match to a series",
			h.QueueUnknownCount, plural(h.QueueUnknownCount, "item is", "items are")))
	}

	return out
}

// --- wire types -----------------------------------------------------------

type systemStatusJSON struct {
	AppName      string `json:"appName"`
	InstanceName string `json:"instanceName"`
	Version      string `json:"version"`
	Branch       string `json:"branch"`
	OsName       string `json:"osName"`
	OsVersion    string `json:"osVersion"`
	IsDocker     bool   `json:"isDocker"`
	StartTime    string `json:"startTime"`
}

type healthJSON struct {
	Source  string `json:"source"`
	Type    string `json:"type"`
	Message string `json:"message"`
	WikiURL string `json:"wikiUrl"`
}

type queueStatusJSON struct {
	TotalCount      int  `json:"totalCount"`
	Count           int  `json:"count"`
	UnknownCount    int  `json:"unknownCount"`
	Errors          bool `json:"errors"`
	Warnings        bool `json:"warnings"`
	UnknownErrors   bool `json:"unknownErrors"`
	UnknownWarnings bool `json:"unknownWarnings"`
}

type rootFolderJSON struct {
	Path       string `json:"path"`
	Accessible bool   `json:"accessible"`
	FreeSpace  int64  `json:"freeSpace"`

	UnmappedFolders []struct {
		Name string `json:"name"`
	} `json:"unmappedFolders"`
}
