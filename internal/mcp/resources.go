package mcp

import (
	"context"
	"fmt"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DeLucca990/homelab-mcp/internal/containers"
	"github.com/DeLucca990/homelab-mcp/internal/radarr"
	"github.com/DeLucca990/homelab-mcp/internal/sonarr"
	"github.com/DeLucca990/homelab-mcp/internal/system"
)

// Resources are the reference data — small, stable, and the answer to a
// question rather than a measurement of the machine.
//
// Two of them exist because a tool call is the wrong shape for what they hold.
// The quality profiles and root folders are the values the add tools accept:
// today the only way to discover them is to guess one and read the refusal,
// which names them. And the configuration resource is the reason a tool is
// missing — a server that has no Radarr key registers no Radarr tools, and the
// only record of that is a line on stderr the model will never see, so the
// assistant can only say "I have no way to do that" without ever knowing why.

const (
	resourceConfiguration  = "homelab://server/configuration"
	resourceRadarrProfiles = "homelab://radarr/quality-profiles"
	resourceRadarrFolders  = "homelab://radarr/root-folders"
	resourceSonarrProfiles = "homelab://sonarr/quality-profiles"
	resourceSonarrFolders  = "homelab://sonarr/root-folders"
)

func registerResources(s *sdk.Server) {
	s.AddResource(&sdk.Resource{
		URI:      resourceConfiguration,
		Name:     "server-configuration",
		Title:    "What this server has registered",
		MIMEType: "text/markdown",
		Description: "Which tool families this install has, which it does not, and the " +
			"environment variable that would enable each missing one. Read it when a tool " +
			"you expected is not in the list: it was never registered, and this says what " +
			"is missing rather than leaving 'I cannot do that' unexplained. Holds no secrets.",
	}, handleConfigurationResource)

	if radarr.Configured() {
		s.AddResource(&sdk.Resource{
			URI:      resourceRadarrProfiles,
			Name:     "radarr-quality-profiles",
			Title:    "Radarr quality profiles",
			MIMEType: "text/markdown",
			Description: "The quality profiles radarr_movie_add accepts, by name. Read this " +
				"before passing 'quality_profile' — the names are whatever this Radarr was " +
				"configured with, not a fixed list.",
		}, handleRadarrProfilesResource)

		s.AddResource(&sdk.Resource{
			URI:      resourceRadarrFolders,
			Name:     "radarr-root-folders",
			Title:    "Radarr root folders",
			MIMEType: "text/markdown",
			Description: "The root folders radarr_movie_add can put a film in, with the free " +
				"space on each. This is the parameter that decides which disk fills up.",
		}, handleRadarrFoldersResource)
	}

	if sonarr.Configured() {
		s.AddResource(&sdk.Resource{
			URI:      resourceSonarrProfiles,
			Name:     "sonarr-quality-profiles",
			Title:    "Sonarr quality profiles",
			MIMEType: "text/markdown",
			Description: "The quality profiles sonarr_series_add accepts, by name. Read this " +
				"before passing 'quality_profile' — the names are whatever this Sonarr was " +
				"configured with, not a fixed list.",
		}, handleSonarrProfilesResource)

		s.AddResource(&sdk.Resource{
			URI:      resourceSonarrFolders,
			Name:     "sonarr-root-folders",
			Title:    "Sonarr root folders",
			MIMEType: "text/markdown",
			Description: "The root folders sonarr_series_add can put a show in, with the free " +
				"space on each. This is the parameter that decides which disk fills up — and a " +
				"series is every episode of every season.",
		}, handleSonarrFoldersResource)
	}
}

// --- the configuration ------------------------------------------------------

func handleConfigurationResource(ctx context.Context, req *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
	var b strings.Builder

	b.WriteString("# What this server has registered\n\n" +
		"A tool that is not listed here was never created, so it cannot be called. " +
		"Each line says what would enable it.\n\n")

	b.WriteString("## Overview\n\n`homelab_overview` is always registered and covers every " +
		"area below in one call. It is the one to reach for first.\n\n")

	b.WriteString("## System (5 tools)\n\nAlways registered, all read-only: host info, CPU, " +
		"memory, disk, systemd units. The systemd one is Linux-only and errors elsewhere.\n\n")

	b.WriteString("## Docker\n\n")
	b.WriteString("`docker_container_status` and `docker_container_logs` are always registered, " +
		"and need access to the docker socket to answer.\n\n")
	if allowed := containers.ActionAllowlist(); len(allowed) > 0 {
		b.WriteString("`docker_container_exec` and `docker_container_restart` are registered, and " +
			"may touch **only** these containers: ")
		b.WriteString(strings.Join(allowed, ", "))
		b.WriteString(". A container outside that list is refused whatever anyone approves.\n\n")
	} else {
		b.WriteString("`docker_container_exec` and `docker_container_restart` are **not** " +
			"registered: `" + containers.AllowlistEnv + "` is unset. It takes a comma-separated " +
			"list of container names, and those are the only ones those tools will ever reach.\n\n")
	}

	writeArrConfiguration(&b, arrConfig{
		Title:        "Radarr",
		Configured:   radarr.Configured(),
		ReadOnly:     radarr.ReadOnly(),
		BaseURL:      arrBaseURL(radarr.BaseURL),
		APIKeyEnv:    radarr.APIKeyEnv,
		BaseURLEnv:   radarr.BaseURLEnv,
		ReadOnlyEnv:  radarr.ReadOnlyEnv,
		ReadTools:    "library, queue, lookup and health",
		WriteTools:   "movie_add, movie_search, movie_remove, queue_remove",
		ProfilesURI:  resourceRadarrProfiles,
		FoldersURI:   resourceRadarrFolders,
		DefaultQuota: radarr.DefaultQualityProfile,
	})

	writeArrConfiguration(&b, arrConfig{
		Title:        "Sonarr",
		Configured:   sonarr.Configured(),
		ReadOnly:     sonarr.ReadOnly(),
		BaseURL:      arrBaseURL(sonarr.BaseURL),
		APIKeyEnv:    sonarr.APIKeyEnv,
		BaseURLEnv:   sonarr.BaseURLEnv,
		ReadOnlyEnv:  sonarr.ReadOnlyEnv,
		ReadTools:    "library, missing episodes, queue, lookup and health",
		WriteTools:   "series_add, series_search, season_monitor, series_remove, queue_remove",
		ProfilesURI:  resourceSonarrProfiles,
		FoldersURI:   resourceSonarrFolders,
		DefaultQuota: sonarr.DefaultQualityProfile,
	})

	b.WriteString("## Approving an action\n\n")
	if trustClientConfirmation() {
		b.WriteString("`" + trustClientEnv + "` is set: this server accepts the approval prompt " +
			"the client shows before calling a tool, instead of asking for one itself. That " +
			"prompt is per-tool where the server's is per-command.\n\n")
	} else {
		b.WriteString("Every tool that changes something asks the user through the client, " +
			"per command, and refuses to act if the client cannot show that request. " +
			"Set `" + trustClientEnv + "=1` only to accept a client's own approval prompt " +
			"instead.\n\n")
	}

	b.WriteString("_No API key appears in this document, and none ever will._\n")

	return markdownResource(req.Params.URI, b.String()), nil
}

type arrConfig struct {
	Title      string
	Configured bool
	ReadOnly   bool
	BaseURL    string

	APIKeyEnv   string
	BaseURLEnv  string
	ReadOnlyEnv string

	ReadTools  string
	WriteTools string

	ProfilesURI  string
	FoldersURI   string
	DefaultQuota string
}

func writeArrConfiguration(b *strings.Builder, c arrConfig) {
	fmt.Fprintf(b, "## %s\n\n", c.Title)

	if !c.Configured {
		fmt.Fprintf(b, "**Not registered.** Set `%s` and `%s` on the machine running this "+
			"server — %s expects a bare host, because each service fills in its own port.\n\n",
			c.BaseURLEnv, c.APIKeyEnv, c.BaseURLEnv)
		return
	}

	fmt.Fprintf(b, "Registered against %s. Read-only tools: %s.\n\n", c.BaseURL, c.ReadTools)

	if c.ReadOnly {
		fmt.Fprintf(b, "The writes (%s) are **not** registered: `%s` is set. Unset it to "+
			"restore them.\n\n", c.WriteTools, c.ReadOnlyEnv)
		return
	}

	fmt.Fprintf(b, "Writes registered, each asking before it acts: %s. Quality defaults to "+
		"`%s`; the profiles and folders this instance actually has are at `%s` and `%s`.\n\n",
		c.WriteTools, c.DefaultQuota, c.ProfilesURI, c.FoldersURI)
}

// arrBaseURL reports the address a service is configured against, or why it
// could not be worked out. Not a secret — it is a LAN address, and the server
// already logs it at startup.
func arrBaseURL(fn func() (string, error)) string {
	url, err := fn()
	if err != nil {
		return "an address that could not be parsed (" + err.Error() + ")"
	}
	return "`" + url + "`"
}

// --- quality profiles and root folders --------------------------------------

func handleRadarrProfilesResource(ctx context.Context, req *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
	profiles, err := radarr.GetQualityProfiles(ctx)
	if err != nil {
		return nil, err
	}
	rows := make([]profileRow, 0, len(profiles))
	for _, p := range profiles {
		rows = append(rows, profileRow{ID: p.ID, Name: p.Name})
	}
	return markdownResource(req.Params.URI,
		renderProfiles("Radarr", "radarr_movie_add", radarr.DefaultQualityProfile, rows)), nil
}

func handleSonarrProfilesResource(ctx context.Context, req *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
	profiles, err := sonarr.GetQualityProfiles(ctx)
	if err != nil {
		return nil, err
	}
	rows := make([]profileRow, 0, len(profiles))
	for _, p := range profiles {
		rows = append(rows, profileRow{ID: p.ID, Name: p.Name})
	}
	return markdownResource(req.Params.URI,
		renderProfiles("Sonarr", "sonarr_series_add", sonarr.DefaultQualityProfile, rows)), nil
}

func handleRadarrFoldersResource(ctx context.Context, req *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
	folders, err := radarr.GetRootFolders(ctx)
	if err != nil {
		return nil, err
	}
	rows := make([]folderRow, 0, len(folders))
	for _, f := range folders {
		rows = append(rows, folderRow{Path: f.Path, Accessible: f.Accessible, FreeSpace: f.FreeSpace})
	}
	return markdownResource(req.Params.URI,
		renderFolders("Radarr", "radarr_movie_add", rows)), nil
}

func handleSonarrFoldersResource(ctx context.Context, req *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
	folders, err := sonarr.GetRootFolders(ctx)
	if err != nil {
		return nil, err
	}
	rows := make([]folderRow, 0, len(folders))
	for _, f := range folders {
		rows = append(rows, folderRow{Path: f.Path, Accessible: f.Accessible, FreeSpace: f.FreeSpace})
	}
	return markdownResource(req.Params.URI,
		renderFolders("Sonarr", "sonarr_series_add", rows)), nil
}

// The two services return the same shapes through different types, so the
// rendering is written once against these.
type profileRow struct {
	ID   int
	Name string
}

type folderRow struct {
	Path       string
	Accessible bool
	FreeSpace  int64
}

func renderProfiles(service, addTool, fallback string, rows []profileRow) string {
	var b strings.Builder

	fmt.Fprintf(&b, "# %s quality profiles\n\n", service)

	if len(rows) == 0 {
		fmt.Fprintf(&b, "%s reports no quality profile at all, which means %s cannot resolve "+
			"one and will refuse to add anything until this instance has one.\n", service, addTool)
		return b.String()
	}

	b.WriteString("| Name | id |\n| --- | --- |\n")
	for _, p := range rows {
		fmt.Fprintf(&b, "| %s | %d |\n", p.Name, p.ID)
	}

	fmt.Fprintf(&b, "\nPass one of these names as `quality_profile` to `%s`. Omitted, it uses "+
		"`%s`", addTool, fallback)
	if !hasProfile(rows, fallback) {
		fmt.Fprintf(&b, " — which this instance does **not** have, so `quality_profile` has to "+
			"be given explicitly here")
	}
	b.WriteString(".\n")

	return b.String()
}

func renderFolders(service, addTool string, rows []folderRow) string {
	var b strings.Builder

	fmt.Fprintf(&b, "# %s root folders\n\n", service)

	if len(rows) == 0 {
		fmt.Fprintf(&b, "%s has no root folder configured, so it has nowhere to put anything "+
			"and %s will refuse to add.\n", service, addTool)
		return b.String()
	}

	b.WriteString("| Path | Free | Accessible |\n| --- | --- | --- |\n")
	for _, f := range rows {
		free := "unknown"
		if f.FreeSpace > 0 {
			free = system.CompactBytes(uint64(f.FreeSpace))
		}
		reachable := "yes"
		if !f.Accessible {
			reachable = "**no**"
		}
		fmt.Fprintf(&b, "| %s | %s | %s |\n", f.Path, free, reachable)
	}

	if len(rows) == 1 {
		fmt.Fprintf(&b, "\nThere is exactly one, so `%s` may leave `root_folder` out.\n", addTool)
	} else {
		fmt.Fprintf(&b, "\nThere is more than one, so `%s` requires `root_folder`: this is the "+
			"parameter that decides which disk fills up.\n", addTool)
	}

	for _, f := range rows {
		if !f.Accessible {
			fmt.Fprintf(&b, "\n`%s` is not accessible to %s right now — usually a mount that "+
				"is no longer there. Adding to it will not fail loudly; nothing will simply "+
				"ever arrive.\n", f.Path, service)
		}
	}

	return b.String()
}

func hasProfile(rows []profileRow, name string) bool {
	for _, p := range rows {
		if strings.EqualFold(p.Name, name) {
			return true
		}
	}
	return false
}

func markdownResource(uri, text string) *sdk.ReadResourceResult {
	return &sdk.ReadResourceResult{
		Contents: []*sdk.ResourceContents{{
			URI:      uri,
			MIMEType: "text/markdown",
			Text:     text,
		}},
	}
}
