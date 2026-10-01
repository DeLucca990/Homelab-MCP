package prowlarr

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The indexer list answers "which of my indexers actually work".
//
// Prowlarr's own list shows an indexer as enabled whether it has answered
// every query this week or has been failing since Tuesday and is backed off
// until tonight. Those are opposite situations, and the second is invisible
// downstream: Radarr and Sonarr still list it, and simply get nothing from it.
// So each indexer here carries its failure state and its numbers for the last
// week — queries, failures, grabs, response time — and failing ones sort first.
//
// Credentials never leave this package. An indexer's fields hold usernames,
// passwords, cookies and API keys, and none of them are read into anything a
// tool returns.

// How far back the per-indexer numbers go.
const statsWindow = 7 * 24 * time.Hour

// A failure rate worth a warning, once there are enough queries to mean it.
const (
	failureRateWarn   = 0.5
	minQueriesToJudge = 10
)

// Indexer is one indexer as this server reports it.
type Indexer struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	Definition string `json:"definition,omitempty" jsonschema:"the site definition it was created from"`

	Enabled  bool   `json:"enabled"`
	Protocol string `json:"protocol" jsonschema:"torrent or usenet"`
	Privacy  string `json:"privacy" jsonschema:"public, semiPrivate or private"`
	Priority int    `json:"priority" jsonschema:"1 to 50, lower is preferred when two indexers return the same release"`
	Language string `json:"language,omitempty"`

	AppProfileID int      `json:"app_profile_id"`
	AppProfile   string   `json:"app_profile,omitempty" jsonschema:"the sync profile deciding whether the *arrs use it for RSS, automatic search and interactive search"`
	Tags         []string `json:"tags,omitempty" jsonschema:"an application with tags only receives indexers sharing one of them"`

	Failing             bool   `json:"failing" jsonschema:"Prowlarr has backed this indexer off after repeated failures and is not querying it"`
	DisabledForSeconds  uint64 `json:"disabled_for_seconds,omitempty" jsonschema:"how long until Prowlarr tries it again"`
	FailingSinceSeconds uint64 `json:"failing_since_seconds,omitempty" jsonschema:"how long ago the current run of failures began"`

	Queries       int `json:"queries" jsonschema:"queries in the last 7 days, RSS included"`
	FailedQueries int `json:"failed_queries"`
	Grabs         int `json:"grabs"`
	FailedGrabs   int `json:"failed_grabs,omitempty"`
	AvgResponseMs int `json:"avg_response_ms,omitempty"`

	SupportsRSS    bool `json:"supports_rss"`
	SupportsSearch bool `json:"supports_search"`

	// the raw tag ids, for working out which applications it reaches
	tagIDs []int
}

// FailureRate is failed queries over queries, or 0 when there were none.
func (ix Indexer) FailureRate() float64 {
	if ix.Queries == 0 {
		return 0
	}
	return float64(ix.FailedQueries) / float64(ix.Queries)
}

type Indexers struct {
	Indexers []Indexer `json:"indexers"`

	TotalCount    int  `json:"total_count" jsonschema:"indexers configured, before any filter"`
	EnabledCount  int  `json:"enabled_count"`
	FailingCount  int  `json:"failing_count" jsonschema:"enabled indexers Prowlarr has backed off after failures"`
	TorrentCount  int  `json:"torrent_count"`
	UsenetCount   int  `json:"usenet_count"`
	StatsDays     int  `json:"stats_days" jsonschema:"the window the query and grab numbers cover"`
	StatsReadable bool `json:"stats_readable"`

	Warnings []string `json:"warnings,omitempty"`
}

// GetIndexers lists the indexers, worst first. term narrows the list by name;
// the counts always describe all of them.
func GetIndexers(ctx context.Context, term string) (Indexers, error) {
	c, err := newClient()
	if err != nil {
		return Indexers{}, err
	}

	var (
		raw      []indexerJSON
		stats    indexerStatsJSON
		profiles []appProfileJSON
		tags     []tagJSON

		rawErr, statsErr, profilesErr, tagsErr error
		wg                                     sync.WaitGroup
	)

	since := time.Now().Add(-statsWindow).UTC().Format(time.RFC3339)

	wg.Add(4)
	go func() { defer wg.Done(); rawErr = c.get(ctx, "/indexer", nil, &raw) }()
	go func() {
		defer wg.Done()
		statsErr = c.get(ctx, "/indexerstats", url.Values{"startDate": {since}}, &stats)
	}()
	go func() { defer wg.Done(); profilesErr = c.get(ctx, "/appprofile", nil, &profiles) }()
	go func() { defer wg.Done(); tagsErr = c.get(ctx, "/tag", nil, &tags) }()
	wg.Wait()

	if rawErr != nil {
		return Indexers{}, rawErr
	}

	out := Indexers{StatsDays: int(statsWindow.Hours() / 24), StatsReadable: statsErr == nil}

	byID := map[int]indexerStatJSON{}
	for _, s := range stats.Indexers {
		byID[s.IndexerID] = s
	}

	want := strings.ToLower(strings.TrimSpace(term))
	for _, r := range raw {
		ix := r.toIndexer(profileNames(profiles), tagLabels(tags))
		if s, ok := byID[ix.ID]; ok {
			ix.Queries = s.NumberOfQueries + s.NumberOfRssQueries
			ix.FailedQueries = s.NumberOfFailedQueries + s.NumberOfFailedRssQueries
			ix.Grabs = s.NumberOfGrabs
			ix.FailedGrabs = s.NumberOfFailedGrabs
			ix.AvgResponseMs = s.AverageResponseTime
		}

		if ix.Enabled {
			out.EnabledCount++
			if ix.Failing {
				out.FailingCount++
			}
		}
		switch ix.Protocol {
		case "torrent":
			out.TorrentCount++
		case "usenet":
			out.UsenetCount++
		}

		if want != "" && !strings.Contains(strings.ToLower(ix.Name), want) &&
			!strings.Contains(strings.ToLower(ix.Definition), want) {
			continue
		}
		out.Indexers = append(out.Indexers, ix)
	}
	out.TotalCount = len(raw)

	slices.SortFunc(out.Indexers, func(a, b Indexer) int {
		if d := indexerSeverity(a) - indexerSeverity(b); d != 0 {
			return d
		}
		if a.Priority != b.Priority {
			return a.Priority - b.Priority
		}
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})

	out.Warnings = indexerWarnings(out, term)
	if statsErr != nil {
		out.Warnings = append(out.Warnings, "could not read the indexer statistics, so the query "+
			"and grab numbers are missing: "+statsErr.Error())
	}
	if profilesErr != nil {
		out.Warnings = append(out.Warnings, "could not read the sync profiles: "+profilesErr.Error())
	}
	if tagsErr != nil {
		out.Warnings = append(out.Warnings, "could not read the tags: "+tagsErr.Error())
	}

	return out, nil
}

func indexerSeverity(ix Indexer) int {
	switch {
	case ix.Enabled && ix.Failing:
		return 0
	case ix.Enabled && ix.Queries >= minQueriesToJudge && ix.FailureRate() >= failureRateWarn:
		return 1
	case !ix.Enabled:
		return 3
	default:
		return 2
	}
}

func indexerWarnings(out Indexers, term string) []string {
	var w []string

	switch {
	case out.TotalCount == 0:
		w = append(w, "prowlarr has no indexer at all, so Radarr and Sonarr have nothing to "+
			"search — prowlarr_indexer_definitions finds one to add")
	case out.EnabledCount == 0:
		w = append(w, "every indexer is disabled, so Radarr and Sonarr have nothing to search")
	case out.FailingCount == out.EnabledCount:
		w = append(w, "every enabled indexer is failing and backed off — Radarr and Sonarr are "+
			"searching nothing until one of them comes back")
	}

	for _, ix := range out.Indexers {
		switch {
		case ix.Enabled && ix.Failing:
			msg := fmt.Sprintf("%s is failing and Prowlarr has stopped querying it", ix.Name)
			if ix.DisabledForSeconds > 0 {
				msg += fmt.Sprintf(" for another %s", compact(ix.DisabledForSeconds))
			}
			if ix.FailingSinceSeconds > 0 {
				msg += fmt.Sprintf("; it started failing %s ago", compact(ix.FailingSinceSeconds))
			}
			w = append(w, msg+" — prowlarr_indexer_test says why")
		case ix.Enabled && ix.Queries >= minQueriesToJudge && ix.FailureRate() >= failureRateWarn:
			w = append(w, fmt.Sprintf("%s failed %d of its %d queries in the last %d days",
				ix.Name, ix.FailedQueries, ix.Queries, out.StatsDays))
		}
	}

	if len(out.Indexers) == 0 && term != "" && out.TotalCount > 0 {
		w = append(w, fmt.Sprintf("no indexer is named like %q", term))
	}
	return w
}

// --- resolving one ------------------------------------------------------------

// ResolveIndexer finds one indexer by id or by name. A name matches exactly,
// or as a substring when that names exactly one indexer.
func ResolveIndexer(ctx context.Context, input string) (Indexer, error) {
	c, err := newClient()
	if err != nil {
		return Indexer{}, err
	}
	var (
		raw      []indexerJSON
		profiles []appProfileJSON
		tags     []tagJSON
	)
	if err := c.get(ctx, "/indexer", nil, &raw); err != nil {
		return Indexer{}, err
	}
	_ = c.get(ctx, "/appprofile", nil, &profiles)
	_ = c.get(ctx, "/tag", nil, &tags)

	all := make([]Indexer, 0, len(raw))
	for _, r := range raw {
		all = append(all, r.toIndexer(profileNames(profiles), tagLabels(tags)))
	}
	return resolveIndexer(all, input)
}

func resolveIndexer(all []Indexer, input string) (Indexer, error) {
	want := strings.TrimSpace(input)
	if want == "" {
		return Indexer{}, fmt.Errorf("an indexer is required, by id or name — " +
			"prowlarr_indexer_status lists them")
	}
	if id, err := strconv.Atoi(want); err == nil {
		for _, ix := range all {
			if ix.ID == id {
				return ix, nil
			}
		}
		return Indexer{}, fmt.Errorf("prowlarr has no indexer with id %d — prowlarr_indexer_status "+
			"lists them", id)
	}

	for _, ix := range all {
		if strings.EqualFold(ix.Name, want) {
			return ix, nil
		}
	}
	var partial []Indexer
	for _, ix := range all {
		if strings.Contains(strings.ToLower(ix.Name), strings.ToLower(want)) {
			partial = append(partial, ix)
		}
	}
	switch len(partial) {
	case 1:
		return partial[0], nil
	case 0:
		names := make([]string, 0, len(all))
		for _, ix := range all {
			names = append(names, fmt.Sprintf("%s (%d)", ix.Name, ix.ID))
		}
		return Indexer{}, fmt.Errorf("no indexer named like %q — this Prowlarr has %s",
			input, strings.Join(names, ", "))
	default:
		names := make([]string, 0, len(partial))
		for _, ix := range partial {
			names = append(names, fmt.Sprintf("%s (%d)", ix.Name, ix.ID))
		}
		return Indexer{}, fmt.Errorf("%q matches more than one indexer: %s — pass the id",
			input, strings.Join(names, ", "))
	}
}

// --- sync profiles and tags ---------------------------------------------------

// AppProfile is a sync profile: which kinds of search the *arrs may use an
// indexer for.
type AppProfile struct {
	ID                      int    `json:"id"`
	Name                    string `json:"name"`
	EnableRSS               bool   `json:"enable_rss"`
	EnableAutomaticSearch   bool   `json:"enable_automatic_search"`
	EnableInteractiveSearch bool   `json:"enable_interactive_search"`
	MinimumSeeders          int    `json:"minimum_seeders"`
}

// Describe is the one-line form a confirmation shows.
func (p AppProfile) Describe() string {
	var on []string
	if p.EnableRSS {
		on = append(on, "RSS")
	}
	if p.EnableAutomaticSearch {
		on = append(on, "automatic search")
	}
	if p.EnableInteractiveSearch {
		on = append(on, "interactive search")
	}
	if len(on) == 0 {
		return "nothing — the *arrs receive the indexer and never use it"
	}
	return strings.Join(on, ", ")
}

// GetAppProfiles lists the sync profiles.
func GetAppProfiles(ctx context.Context) ([]AppProfile, error) {
	c, err := newClient()
	if err != nil {
		return nil, err
	}
	return c.appProfiles(ctx)
}

func (c *client) appProfiles(ctx context.Context) ([]AppProfile, error) {
	var raw []appProfileJSON
	if err := c.get(ctx, "/appprofile", nil, &raw); err != nil {
		return nil, err
	}
	out := make([]AppProfile, 0, len(raw))
	for _, r := range raw {
		out = append(out, AppProfile{
			ID:                      r.ID,
			Name:                    r.Name,
			EnableRSS:               r.EnableRss,
			EnableAutomaticSearch:   r.EnableAutomaticSearch,
			EnableInteractiveSearch: r.EnableInteractiveSearch,
			MinimumSeeders:          r.MinimumSeeders,
		})
	}
	return out, nil
}

// resolveAppProfile finds a sync profile by name or id.
func resolveAppProfile(profiles []AppProfile, input string) (AppProfile, error) {
	want := strings.TrimSpace(input)
	if id, err := strconv.Atoi(want); err == nil {
		for _, p := range profiles {
			if p.ID == id {
				return p, nil
			}
		}
	}
	for _, p := range profiles {
		if strings.EqualFold(p.Name, want) {
			return p, nil
		}
	}
	names := make([]string, 0, len(profiles))
	for _, p := range profiles {
		names = append(names, fmt.Sprintf("%q (id %d: %s)", p.Name, p.ID, p.Describe()))
	}
	return AppProfile{}, fmt.Errorf("no sync profile named %q — this Prowlarr has %s",
		input, strings.Join(names, ", "))
}

// Tag is a Prowlarr tag.
type Tag struct {
	ID    int    `json:"id"`
	Label string `json:"label"`
}

// GetTags lists the tags.
func GetTags(ctx context.Context) ([]Tag, error) {
	c, err := newClient()
	if err != nil {
		return nil, err
	}
	var raw []tagJSON
	if err := c.get(ctx, "/tag", nil, &raw); err != nil {
		return nil, err
	}
	out := make([]Tag, 0, len(raw))
	for _, r := range raw {
		out = append(out, Tag(r))
	}
	return out, nil
}

func profileNames(ps []appProfileJSON) map[int]string {
	m := make(map[int]string, len(ps))
	for _, p := range ps {
		m[p.ID] = p.Name
	}
	return m
}

func tagLabels(ts []tagJSON) map[int]string {
	m := make(map[int]string, len(ts))
	for _, t := range ts {
		m[t.ID] = t.Label
	}
	return m
}

func labelsFor(ids []int, labels map[int]string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if l, ok := labels[id]; ok {
			out = append(out, l)
		} else {
			out = append(out, "#"+strconv.Itoa(id))
		}
	}
	slices.Sort(out)
	return out
}

func compact(s uint64) string {
	d := time.Duration(s) * time.Second
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
	}
}

// --- wire types -----------------------------------------------------------

type indexerJSON struct {
	ID             int    `json:"id"`
	Name           string `json:"name"`
	DefinitionName string `json:"definitionName"`
	Implementation string `json:"implementation"`
	Enable         bool   `json:"enable"`
	Protocol       string `json:"protocol"`
	Privacy        string `json:"privacy"`
	Priority       int    `json:"priority"`
	Language       string `json:"language"`
	AppProfileID   int    `json:"appProfileId"`
	Tags           []int  `json:"tags"`
	SupportsRss    bool   `json:"supportsRss"`
	SupportsSearch bool   `json:"supportsSearch"`

	Status *struct {
		DisabledTill      string `json:"disabledTill"`
		MostRecentFailure string `json:"mostRecentFailure"`
		InitialFailure    string `json:"initialFailure"`
	} `json:"status"`
}

func (r indexerJSON) toIndexer(profiles, tags map[int]string) Indexer {
	ix := Indexer{
		ID:             r.ID,
		Name:           r.Name,
		Definition:     r.DefinitionName,
		Enabled:        r.Enable,
		Protocol:       r.Protocol,
		Privacy:        r.Privacy,
		Priority:       r.Priority,
		Language:       r.Language,
		AppProfileID:   r.AppProfileID,
		AppProfile:     profiles[r.AppProfileID],
		Tags:           labelsFor(r.Tags, tags),
		tagIDs:         r.Tags,
		SupportsRSS:    r.SupportsRss,
		SupportsSearch: r.SupportsSearch,
	}
	if ix.Definition == "" {
		ix.Definition = r.Implementation
	}
	if r.Status != nil {
		// A backoff that has already run out is history, not a failure: the
		// next query decides whether it is still broken.
		ix.DisabledForSeconds = secondsUntil(r.Status.DisabledTill)
		ix.Failing = ix.DisabledForSeconds > 0
		if ix.Failing {
			ix.FailingSinceSeconds = secondsSince(r.Status.InitialFailure)
		}
	}
	return ix
}

type indexerStatsJSON struct {
	Indexers []indexerStatJSON `json:"indexers"`
}

type indexerStatJSON struct {
	IndexerID                int `json:"indexerId"`
	AverageResponseTime      int `json:"averageResponseTime"`
	NumberOfQueries          int `json:"numberOfQueries"`
	NumberOfGrabs            int `json:"numberOfGrabs"`
	NumberOfRssQueries       int `json:"numberOfRssQueries"`
	NumberOfFailedQueries    int `json:"numberOfFailedQueries"`
	NumberOfFailedGrabs      int `json:"numberOfFailedGrabs"`
	NumberOfFailedRssQueries int `json:"numberOfFailedRssQueries"`
}

type appProfileJSON struct {
	ID                      int    `json:"id"`
	Name                    string `json:"name"`
	EnableRss               bool   `json:"enableRss"`
	EnableAutomaticSearch   bool   `json:"enableAutomaticSearch"`
	EnableInteractiveSearch bool   `json:"enableInteractiveSearch"`
	MinimumSeeders          int    `json:"minimumSeeders"`
}

type tagJSON struct {
	ID    int    `json:"id"`
	Label string `json:"label"`
}
