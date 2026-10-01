package mcp

import (
	"context"
	"fmt"
	"slices"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DeLucca990/homelab-mcp/internal/bazarr"
	"github.com/DeLucca990/homelab-mcp/internal/containers"
	"github.com/DeLucca990/homelab-mcp/internal/jellyfin"
	"github.com/DeLucca990/homelab-mcp/internal/prowlarr"
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
	resourceConfiguration     = "homelab://server/configuration"
	resourceRadarrProfiles    = "homelab://radarr/quality-profiles"
	resourceRadarrFolders     = "homelab://radarr/root-folders"
	resourceSonarrProfiles    = "homelab://sonarr/quality-profiles"
	resourceSonarrFolders     = "homelab://sonarr/root-folders"
	resourceBazarrProfiles    = "homelab://bazarr/language-profiles"
	resourceProwlarrSync      = "homelab://prowlarr/sync-profiles"
	resourceJellyfinLibraries = "homelab://jellyfin/libraries"
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

	if bazarr.Configured() {
		s.AddResource(&sdk.Resource{
			URI:      resourceBazarrProfiles,
			Name:     "bazarr-language-profiles",
			Title:    "Bazarr language profiles",
			MIMEType: "text/markdown",
			Description: "The language profiles bazarr_language_profile_set accepts, each with the " +
				"subtitle languages it asks for, and the languages enabled on this Bazarr with the " +
				"codes the subtitle tools take. Read this before choosing a profile or a language " +
				"code — Bazarr's codes are not all ISO: Brazilian Portuguese is 'pb'.",
		}, handleBazarrProfilesResource)
	}

	if jellyfin.Configured() {
		s.AddResource(&sdk.Resource{
			URI:      resourceJellyfinLibraries,
			Name:     "jellyfin-libraries",
			Title:    "Jellyfin libraries",
			MIMEType: "text/markdown",
			Description: "Jellyfin's libraries by name, with their type and the folders on disk each " +
				"one reads. The names are what jellyfin_library_scan and jellyfin_user_access_set take; " +
				"the paths are how to tell which library a file Radarr or Sonarr imported lands in.",
		}, handleJellyfinLibrariesResource)
	}

	if prowlarr.Configured() {
		s.AddResource(&sdk.Resource{
			URI:      resourceProwlarrSync,
			Name:     "prowlarr-sync-profiles",
			Title:    "Prowlarr sync profiles and tags",
			MIMEType: "text/markdown",
			Description: "The sync profiles and tags prowlarr_indexer_update and prowlarr_indexer_add " +
				"accept, and which app each tag routes indexers to. Read this before choosing a " +
				"'sync_profile' or 'tags': a tag decides which apps receive an indexer at all.",
		}, handleProwlarrSyncResource)
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
		ReadTools:    "library, queue, lookup, health, releases, import_candidates, history and calendar",
		WriteTools:   "movie_add, movie_search, movie_remove, queue_remove, release_grab, import, movie_edit",
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
		ReadTools:    "library, missing episodes, queue, lookup, health, releases, import_candidates, history and calendar",
		WriteTools:   "series_add, series_search, season_monitor, series_remove, queue_remove, release_grab, import, series_edit, episode_monitor",
		ProfilesURI:  resourceSonarrProfiles,
		FoldersURI:   resourceSonarrFolders,
		DefaultQuota: sonarr.DefaultQualityProfile,
	})

	writeJellyfinConfiguration(&b)
	writeBazarrConfiguration(&b)
	writeProwlarrConfiguration(&b)

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

// Jellyfin does not use the arrConfig shape: it has no quality profiles and no
// root folders. What it does have that the *arrs do not is a second way to be
// half-configured, because most of what it reads and every write is admin-only.
func writeJellyfinConfiguration(b *strings.Builder) {
	b.WriteString("## Jellyfin\n\n")

	if !jellyfin.Configured() {
		fmt.Fprintf(b, "**Not registered.** Set `%s` and `%s` on the machine running this "+
			"server — %s expects a bare host, because each service fills in its own port "+
			"(Jellyfin's is 8096).\n\n",
			jellyfin.BaseURLEnv, jellyfin.APIKeyEnv, jellyfin.BaseURLEnv)
		return
	}

	const reads = "`jellyfin_active_sessions`, `jellyfin_system_health`, `jellyfin_users`, " +
		"`jellyfin_find_item` and `jellyfin_activity_log`"
	const writes = "`jellyfin_library_scan`, `jellyfin_session_stop`, `jellyfin_session_message`, " +
		"`jellyfin_user_preferences_set`, `jellyfin_user_access_set`, `jellyfin_transcoding_set` " +
		"and `jellyfin_mark_played`"

	fmt.Fprintf(b, "Registered against %s. Read-only tools: %s.\n\n", arrBaseURL(jellyfin.BaseURL), reads)

	if jellyfin.ReadOnly() {
		fmt.Fprintf(b, "The writes (%s) are **not** registered: `%s` is set. Unset it to "+
			"restore them.\n\n", writes, jellyfin.ReadOnlyEnv)
	} else {
		fmt.Fprintf(b, "Writes registered, each asking before it acts: %s. The libraries, with "+
			"their paths, are at `%s`.\n\n", writes, resourceJellyfinLibraries)
	}

	fmt.Fprintf(b, "Most of this is administrator-only. A key issued from Jellyfin's Dashboard "+
		"→ API Keys has those rights; one taken from a user session does not, and the tools then "+
		"say which request was refused. `%s` is that key.\n\n", jellyfin.APIKeyEnv)
}

// Bazarr has language profiles where the *arrs have quality profiles, and no
// root folders — it writes next to whatever Radarr and Sonarr downloaded — so
// it gets its own paragraph rather than the arrConfig shape.
func writeBazarrConfiguration(b *strings.Builder) {
	b.WriteString("## Bazarr\n\n")

	if !bazarr.Configured() {
		fmt.Fprintf(b, "**Not registered.** Set `%s` and `%s` on the machine running this "+
			"server — %s expects a bare host, because each service fills in its own port "+
			"(Bazarr's is 6767).\n\n",
			bazarr.BaseURLEnv, bazarr.APIKeyEnv, bazarr.BaseURLEnv)
		return
	}

	const reads = "`bazarr_system_health`, `bazarr_subtitle_status`, `bazarr_wanted_subtitles` " +
		"and `bazarr_subtitle_candidates`"
	const writes = "`bazarr_subtitle_search`, `bazarr_subtitle_download`, `bazarr_subtitle_sync`, " +
		"`bazarr_language_profile_set` and `bazarr_providers_reset`"

	fmt.Fprintf(b, "Registered against %s. Read-only tools: %s.\n\n", arrBaseURL(bazarr.BaseURL), reads)

	if bazarr.ReadOnly() {
		fmt.Fprintf(b, "The writes (%s) are **not** registered: `%s` is set. Unset it to "+
			"restore them.\n\n", writes, bazarr.ReadOnlyEnv)
		return
	}

	fmt.Fprintf(b, "Writes registered, each asking before it acts: %s. The language profiles and "+
		"language codes this instance has are at `%s`.\n\n", writes, resourceBazarrProfiles)
}

// Prowlarr has sync profiles and tags where the *arrs have quality profiles
// and root folders, so it gets its own paragraph.
func writeProwlarrConfiguration(b *strings.Builder) {
	b.WriteString("## Prowlarr\n\n")

	if !prowlarr.Configured() {
		fmt.Fprintf(b, "**Not registered.** Set `%s` and `%s` on the machine running this "+
			"server — %s expects a bare host, because each service fills in its own port "+
			"(Prowlarr's is 9696).\n\n",
			prowlarr.BaseURLEnv, prowlarr.APIKeyEnv, prowlarr.BaseURLEnv)
		return
	}

	const reads = "`prowlarr_system_health`, `prowlarr_indexer_status`, `prowlarr_indexer_test`, " +
		"`prowlarr_applications`, `prowlarr_search` and `prowlarr_indexer_definitions`"
	const writes = "`prowlarr_indexer_update`, `prowlarr_indexer_add`, `prowlarr_indexer_remove` " +
		"and `prowlarr_apps_sync`"

	fmt.Fprintf(b, "Registered against %s. Read-only tools: %s.\n\n", arrBaseURL(prowlarr.BaseURL), reads)

	if prowlarr.ReadOnly() {
		fmt.Fprintf(b, "The writes (%s) are **not** registered: `%s` is set. Unset it to "+
			"restore them.\n\n", writes, prowlarr.ReadOnlyEnv)
		return
	}

	fmt.Fprintf(b, "Writes registered, each asking before it acts: %s. Indexers are managed "+
		"here and pushed into Radarr and Sonarr by Prowlarr — an indexer edited directly in an "+
		"*arr is overwritten on the next Full Sync. The sync profiles and tags are at `%s`.\n\n",
		writes, resourceProwlarrSync)
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

// --- bazarr language profiles ----------------------------------------------

func handleBazarrProfilesResource(ctx context.Context, req *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
	profiles, err := bazarr.GetProfiles(ctx)
	if err != nil {
		return nil, err
	}
	languages, err := bazarr.GetLanguages(ctx)
	if err != nil {
		return nil, err
	}
	return markdownResource(req.Params.URI, renderBazarrProfiles(profiles, languages)), nil
}

func renderBazarrProfiles(profiles []bazarr.Profile, languages []bazarr.Language) string {
	var b strings.Builder

	b.WriteString("# Bazarr language profiles\n\n")

	if len(profiles) == 0 {
		b.WriteString("Bazarr has no language profile at all, so it wants no subtitle for " +
			"anything and `bazarr_language_profile_set` has nothing to assign. One has to be " +
			"created in Bazarr → Settings → Languages.\n\n")
	} else {
		b.WriteString("| Name | id | Languages | Cutoff | Auto-assigned by tag |\n| --- | --- | --- | --- | --- |\n")
		for _, p := range profiles {
			fmt.Fprintf(&b, "| %s | %d | %s | %s | %s |\n",
				p.Name, p.ID, p.Describe(), blankAs(p.Cutoff, "-"), blankAs(p.Tag, "-"))
		}
		b.WriteString("\nPass a name or id as `profile` to `bazarr_language_profile_set`, or `none` " +
			"to stop Bazarr wanting subtitles for something. The cutoff is the language that, once " +
			"present, stops Bazarr looking for the others.\n\n")
	}

	b.WriteString("## Languages enabled\n\n")
	var enabled []bazarr.Language
	for _, l := range languages {
		if l.Enabled {
			enabled = append(enabled, l)
		}
	}
	if len(enabled) == 0 {
		b.WriteString("No language is enabled in Bazarr → Settings → Languages, so no profile can " +
			"ask for one.\n")
		return b.String()
	}
	b.WriteString("| Code | Name |\n| --- | --- |\n")
	for _, l := range enabled {
		fmt.Fprintf(&b, "| `%s` | %s |\n", l.Code2, l.Name)
	}
	b.WriteString("\nThe subtitle tools take these codes. They are Bazarr's own, and not all ISO " +
		"639-1: `pb` is Brazilian Portuguese where `pt` is European, `zt` is Traditional Chinese, " +
		"`ea` is Latin American Spanish. A language outside this list can still be passed to " +
		"`bazarr_subtitle_search`, but some providers will not be asked for it.\n")

	return b.String()
}

// --- prowlarr sync profiles and tags ------------------------------------------

func handleProwlarrSyncResource(ctx context.Context, req *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
	apps, err := prowlarr.GetApplications(ctx, false)
	if err != nil {
		return nil, err
	}
	tags, err := prowlarr.GetTags(ctx)
	if err != nil {
		return nil, err
	}
	return markdownResource(req.Params.URI, renderProwlarrSync(apps, tags)), nil
}

func renderProwlarrSync(a prowlarr.Applications, tags []prowlarr.Tag) string {
	var b strings.Builder

	b.WriteString("# Prowlarr sync profiles and tags\n\n")

	b.WriteString("## Sync profiles\n\n")
	if len(a.Profiles) == 0 {
		b.WriteString("None — every indexer needs one, so none can be added until one exists " +
			"(Settings → Apps → Sync Profiles).\n\n")
	} else {
		b.WriteString("| Name | id | The apps use the indexer for | Minimum seeders |\n| --- | --- | --- | --- |\n")
		for _, p := range a.Profiles {
			fmt.Fprintf(&b, "| %s | %d | %s | %d |\n", p.Name, p.ID, p.Describe(), p.MinimumSeeders)
		}
		b.WriteString("\nPass a name or id as `sync_profile`. A profile with every search off " +
			"still syncs the indexer, and the app never uses it.\n\n")
	}

	b.WriteString("## Tags\n\n")
	if len(tags) == 0 {
		b.WriteString("No tags, so every indexer reaches every app that is syncing.\n\n")
	} else {
		b.WriteString("| Tag | Apps that only take indexers with it |\n| --- | --- |\n")
		for _, t := range tags {
			var apps []string
			for _, app := range a.Applications {
				if slices.Contains(app.Tags, t.Label) {
					apps = append(apps, app.Name)
				}
			}
			fmt.Fprintf(&b, "| %s | %s |\n", t.Label, blankAs(strings.Join(apps, ", "), "-"))
		}
		b.WriteString("\nAn app with no tags takes every indexer; an app with tags takes only " +
			"indexers sharing one. A FlareSolverr proxy likewise only serves indexers sharing its " +
			"tag. Tags are created in Prowlarr, not by these tools.\n\n")
	}

	b.WriteString("## Apps\n\n")
	if len(a.Applications) == 0 {
		b.WriteString("None — Prowlarr syncs to nothing.\n")
	} else {
		b.WriteString("| App | Sync level | Tags |\n| --- | --- | --- |\n")
		for _, app := range a.Applications {
			fmt.Fprintf(&b, "| %s | %s | %s |\n", app.Name, app.SyncLevel,
				blankAs(strings.Join(app.Tags, ", "), "-"))
		}
		b.WriteString("\n`fullSync` apps take every change; `addOnly` apps only take new indexers.\n")
	}

	return b.String()
}

// --- jellyfin libraries ----------------------------------------------------------

func handleJellyfinLibrariesResource(ctx context.Context, req *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
	libs, err := jellyfin.GetLibraries(ctx)
	if err != nil {
		return nil, err
	}

	var b strings.Builder
	b.WriteString("# Jellyfin libraries\n\n")
	if len(libs) == 0 {
		b.WriteString("Jellyfin has no library, so nothing on disk is visible in it.\n")
		return markdownResource(req.Params.URI, b.String()), nil
	}
	b.WriteString("| Name | Type | Folders | Scanning |\n| --- | --- | --- | --- |\n")
	for _, l := range libs {
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", l.Name, blankAs(l.Type, "mixed"),
			blankAs(strings.Join(l.Paths, "<br>"), "-"), yesNo(l.Refreshing))
	}
	b.WriteString("\nA file only appears in Jellyfin if it is under one of these folders **as " +
		"Jellyfin's container sees it** — Radarr and Sonarr may mount the same disk at a different " +
		"path, and that mismatch is invisible from either side.\n")
	return markdownResource(req.Params.URI, b.String()), nil
}
