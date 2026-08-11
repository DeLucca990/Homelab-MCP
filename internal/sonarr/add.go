package sonarr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// Adding a series is split in two on purpose: Plan resolves everything and
// changes nothing, Add performs the one request that does.
//
// The split exists for the confirmation round trip. The handler plans on the
// first pass to build the message a human reads, and plans again on the retry
// to recompute the fingerprint. Both passes see the same resolved plan — the
// profile, the folder, the title, how much of the show is about to be monitored
// — so what runs is what was shown, and a resolution that shifted in between (a
// profile renamed, the series added by someone else) changes the fingerprint and
// stops the add rather than completing it against different values.

// What Sonarr will monitor once the series is added. This is the parameter with
// no equivalent in Radarr and the one that decides the size of the operation:
// "all" on a nine-season show is several hundred episodes.
var monitorValues = []string{
	"all", "future", "missing", "existing", "recent",
	"pilot", "firstSeason", "lastSeason", "latestSeason",
	"monitorSpecials", "unmonitorSpecials", "none",
}

var seriesTypeValues = []string{"standard", "daily", "anime"}

const DefaultQualityProfile = "HD-1080p"

const (
	defaultMonitor    = "all"
	defaultSeriesType = "standard"
)

type LookupResult struct {
	TvdbID int    `json:"tvdb_id" jsonschema:"pass this to sonarr_series_add"`
	Title  string `json:"title"`
	Year   int    `json:"year,omitempty"`

	InLibrary bool `json:"in_library" jsonschema:"true when this series has already been added to Sonarr, in which case adding it again is refused"`
	SeriesID  int  `json:"series_id,omitempty" jsonschema:"Sonarr's id for it, when it is already in the library"`

	Status      string `json:"status,omitempty" jsonschema:"continuing, ended or upcoming"`
	Network     string `json:"network,omitempty"`
	SeasonCount int    `json:"season_count,omitempty" jsonschema:"seasons excluding specials — how big the show is, which is how big the download would be"`
	Runtime     int    `json:"runtime_minutes,omitempty"`
	ImdbID      string `json:"imdb_id,omitempty"`
	PosterURL   string `json:"poster_url,omitempty" jsonschema:"cover art hosted by the metadata provider, ready to display"`
	Overview    string `json:"overview,omitempty"`
}

func Lookup(ctx context.Context, term string, limit int) ([]LookupResult, error) {
	term = strings.TrimSpace(term)
	if term == "" {
		return nil, fmt.Errorf("a search term is required")
	}

	c, err := newClient()
	if err != nil {
		return nil, err
	}

	records, err := lookupSeries(ctx, c, term)
	if err != nil {
		return nil, err
	}

	if limit <= 0 || limit > 25 {
		limit = 10
	}
	if len(records) > limit {
		records = records[:limit]
	}

	out := make([]LookupResult, 0, len(records))
	for _, r := range records {
		out = append(out, r.toResult())
	}
	return out, nil
}

// lookupSeries is the one endpoint Sonarr offers for finding a show that is not
// in the library yet. Unlike Radarr it has no by-id variant — an id is expressed
// as a `tvdb:` prefixed term, which is why AddRequest resolution goes through
// the same call.
func lookupSeries(ctx context.Context, c *client, term string) ([]lookupJSON, error) {
	var records []lookupJSON
	err := c.do(ctx, "GET", "/series/lookup", url.Values{"term": {term}}, nil, &records, lookupTimeout)
	return records, err
}

// AddRequest is what the caller asked for, before anything is resolved.
type AddRequest struct {
	TvdbID int

	QualityProfile string // name or id
	RootFolder     string

	Monitor      string
	SearchOnAdd  bool
	SeasonFolder bool
	SeriesType   string
}

// AddPlan is the resolved request: exactly what would be sent, in the words a
// human reads before approving it.
type AddPlan struct {
	TvdbID int    `json:"tvdb_id"`
	Title  string `json:"title"`
	Year   int    `json:"year,omitempty"`

	PosterURL string `json:"poster_url,omitempty" jsonschema:"the show's cover art, hosted by the metadata provider"`
	Network   string `json:"network,omitempty"`
	Status    string `json:"status,omitempty"`

	SeasonCount  int `json:"season_count,omitempty" jsonschema:"seasons excluding specials"`
	EpisodeCount int `json:"episode_count,omitempty" jsonschema:"episodes the metadata provider knows of, when it says — the size of what is about to be searched for"`

	QualityProfileID   int    `json:"quality_profile_id"`
	QualityProfileName string `json:"quality_profile"`
	RootFolderPath     string `json:"root_folder"`

	Monitor      string `json:"monitor" jsonschema:"which episodes Sonarr will try to get"`
	SearchOnAdd  bool   `json:"search_on_add"`
	SeasonFolder bool   `json:"season_folder"`
	SeriesType   string `json:"series_type"`

	body map[string]any
}

// Fingerprint is the plan reduced to the values that decide what happens, in a
// fixed order. Two plans that would produce different libraries produce
// different fingerprints.
//
// PosterURL is deliberately absent. It changes nothing about what gets added,
// and the metadata provider rotates those paths — including it would turn an
// unrelated image change between the confirmation and the retry into a refused
// approval, which trains people to re-approve without reading.
func (p AddPlan) Fingerprint() []string {
	return []string{
		strconv.Itoa(p.TvdbID),
		p.Title,
		strconv.Itoa(p.Year),
		strconv.Itoa(p.QualityProfileID),
		p.RootFolderPath,
		p.Monitor,
		strconv.FormatBool(p.SearchOnAdd),
		strconv.FormatBool(p.SeasonFolder),
		p.SeriesType,
	}
}

// Plan resolves an add request against the live Sonarr and changes nothing. It
// fails on anything ambiguous rather than picking for the user.
func Plan(ctx context.Context, req AddRequest) (AddPlan, error) {
	if req.TvdbID <= 0 {
		return AddPlan{}, fmt.Errorf(
			"a tvdb_id is required — run sonarr_series_lookup first and take it from the result")
	}

	monitor := strings.TrimSpace(req.Monitor)
	if monitor == "" {
		monitor = defaultMonitor
	}
	monitor, err := matchValue(monitorValues, monitor)
	if err != nil {
		return AddPlan{}, fmt.Errorf("monitor must be one of %s, got %q",
			strings.Join(monitorValues, ", "), req.Monitor)
	}

	seriesType := strings.TrimSpace(req.SeriesType)
	if seriesType == "" {
		seriesType = defaultSeriesType
	}
	seriesType, err = matchValue(seriesTypeValues, seriesType)
	if err != nil {
		return AddPlan{}, fmt.Errorf("series_type must be one of %s, got %q",
			strings.Join(seriesTypeValues, ", "), req.SeriesType)
	}

	c, err := newClient()
	if err != nil {
		return AddPlan{}, err
	}

	var raw []json.RawMessage
	err = c.do(ctx, "GET", "/series/lookup",
		url.Values{"term": {"tvdb:" + strconv.Itoa(req.TvdbID)}}, nil, &raw, lookupTimeout)
	if err != nil {
		return AddPlan{}, err
	}

	found, series, err := pickByTvdbID(raw, req.TvdbID)
	if err != nil {
		return AddPlan{}, err
	}

	if series.ID != 0 {
		return AddPlan{}, fmt.Errorf(
			"%s (%d) is already in your library (series id %d) — to look for the episodes "+
				"it is missing, use sonarr_series_search with series_id %d; "+
				"sonarr_library_status shows how complete it is",
			series.Title, series.Year, series.ID, series.ID)
	}

	profile, err := resolveQualityProfile(ctx, c, req.QualityProfile)
	if err != nil {
		return AddPlan{}, err
	}
	folder, err := resolveRootFolder(ctx, c, req.RootFolder)
	if err != nil {
		return AddPlan{}, err
	}

	plan := AddPlan{
		TvdbID:             req.TvdbID,
		Title:              series.Title,
		Year:               series.Year,
		PosterURL:          series.poster(),
		Network:            series.Network,
		Status:             series.Status,
		SeasonCount:        series.seasonCount(),
		EpisodeCount:       series.episodeCount(),
		QualityProfileID:   profile.ID,
		QualityProfileName: profile.Name,
		RootFolderPath:     folder.Path,
		Monitor:            monitor,
		SearchOnAdd:        req.SearchOnAdd,
		SeasonFolder:       req.SeasonFolder,
		SeriesType:         seriesType,
		body:               found,
	}

	plan.body["qualityProfileId"] = profile.ID
	plan.body["rootFolderPath"] = folder.Path
	plan.body["seasonFolder"] = req.SeasonFolder
	plan.body["seriesType"] = seriesType
	plan.body["monitored"] = monitor != "none"
	plan.body["addOptions"] = map[string]any{
		"monitor":                      monitor,
		"searchForMissingEpisodes":     req.SearchOnAdd,
		"searchForCutoffUnmetEpisodes": false,
	}

	return plan, nil
}

// pickByTvdbID selects the one result that actually is the show that was asked
// for. Sonarr answers a `tvdb:` term with a list, and a list is not an identity
// — taking the first element would add whatever the metadata service decided to
// rank highest for an id it did not recognise.
func pickByTvdbID(raw []json.RawMessage, tvdbID int) (map[string]any, lookupJSON, error) {
	for _, entry := range raw {
		var series lookupJSON
		if err := json.Unmarshal(entry, &series); err != nil {
			continue
		}
		if series.TvdbID != tvdbID {
			continue
		}

		var body map[string]any
		if err := json.Unmarshal(entry, &body); err != nil || len(body) == 0 {
			return nil, lookupJSON{}, fmt.Errorf(
				"sonarr returned a series that could not be read: %w", err)
		}
		if series.Title == "" {
			return nil, lookupJSON{}, fmt.Errorf("sonarr returned no title for tvdb id %d", tvdbID)
		}
		return body, series, nil
	}

	return nil, lookupJSON{}, fmt.Errorf(
		"no series with TVDB id %d — sonarr_series_lookup finds shows by title and "+
			"returns the id to use", tvdbID)
}

type AddResult struct {
	SeriesID int    `json:"series_id"`
	TvdbID   int    `json:"tvdb_id"`
	Title    string `json:"title"`
	Year     int    `json:"year,omitempty"`

	Path           string `json:"path,omitempty" jsonschema:"the folder Sonarr created for it"`
	QualityProfile string `json:"quality_profile"`

	Monitored     bool   `json:"monitored"`
	Monitor       string `json:"monitor"`
	SearchStarted bool   `json:"search_started" jsonschema:"true when Sonarr began looking for releases immediately; false means it will wait for its next scheduled search"`
	SeasonCount   int    `json:"season_count,omitempty"`

	Warnings []string `json:"warnings,omitempty"`
}

// Add performs the one request that changes something. It takes a plan rather
// than a request, so it cannot resolve anything the user was not shown.
func Add(ctx context.Context, plan AddPlan) (AddResult, error) {
	c, err := newClient()
	if err != nil {
		return AddResult{}, err
	}

	var created seriesJSON
	if err := c.post(ctx, "/series", plan.body, &created); err != nil {
		return AddResult{}, err
	}

	res := AddResult{
		SeriesID:       created.ID,
		TvdbID:         plan.TvdbID,
		Title:          plan.Title,
		Year:           plan.Year,
		Path:           created.Path,
		QualityProfile: plan.QualityProfileName,
		Monitored:      created.Monitored,
		Monitor:        plan.Monitor,
		SearchStarted:  plan.SearchOnAdd,
		SeasonCount:    plan.SeasonCount,
	}

	if !res.Monitored || plan.Monitor == "none" {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"%s was added with nothing monitored — Sonarr will not search for any episode of it",
			plan.Title))
	}
	if res.Monitored && !plan.SearchOnAdd {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"no search was started — %s will be picked up by Sonarr's next scheduled search, "+
				"which can be hours away; sonarr_series_search starts one now", plan.Title))
	}
	if plan.SearchOnAdd && plan.Monitor == "all" && plan.EpisodeCount > 50 {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"%s has about %d episodes and all of them were monitored, so Sonarr is now "+
				"searching for the whole back catalogue — expect the queue and the disk to fill",
			plan.Title, plan.EpisodeCount))
	}

	return res, nil
}

// --- resolution -----------------------------------------------------------

type QualityProfile struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type RootFolder struct {
	ID         int    `json:"id"`
	Path       string `json:"path"`
	Accessible bool   `json:"accessible"`
	FreeSpace  int64  `json:"freeSpace"`
}

// GetQualityProfiles and GetRootFolders exist so a failed resolution can be
// answered with the actual choices rather than "not found".
func GetQualityProfiles(ctx context.Context) ([]QualityProfile, error) {
	c, err := newClient()
	if err != nil {
		return nil, err
	}
	var out []QualityProfile
	return out, c.get(ctx, "/qualityprofile", nil, &out)
}

func GetRootFolders(ctx context.Context) ([]RootFolder, error) {
	c, err := newClient()
	if err != nil {
		return nil, err
	}
	var out []RootFolder
	return out, c.get(ctx, "/rootfolder", nil, &out)
}

func resolveQualityProfile(ctx context.Context, c *client, want string) (QualityProfile, error) {
	var profiles []QualityProfile
	if err := c.get(ctx, "/qualityprofile", nil, &profiles); err != nil {
		return QualityProfile{}, err
	}
	if len(profiles) == 0 {
		return QualityProfile{}, fmt.Errorf("sonarr has no quality profiles configured")
	}

	want = strings.TrimSpace(want)
	if want == "" {
		if p, ok := profileNamed(profiles, DefaultQualityProfile); ok {
			return p, nil
		}
		if len(profiles) == 1 {
			return profiles[0], nil
		}
		return QualityProfile{}, fmt.Errorf(
			"quality_profile is required: sonarr has %d of them (%s) and none is called %q, "+
				"so there is no default to fall back on — picking one decides what gets downloaded",
			len(profiles), joinProfiles(profiles), DefaultQualityProfile)
	}

	if id, err := strconv.Atoi(want); err == nil {
		for _, p := range profiles {
			if p.ID == id {
				return p, nil
			}
		}
	}
	if p, ok := profileNamed(profiles, want); ok {
		return p, nil
	}

	return QualityProfile{}, fmt.Errorf("no quality profile called %q — sonarr has %s",
		want, joinProfiles(profiles))
}

func profileNamed(profiles []QualityProfile, name string) (QualityProfile, bool) {
	for _, p := range profiles {
		if strings.EqualFold(p.Name, name) {
			return p, true
		}
	}
	return QualityProfile{}, false
}

func resolveRootFolder(ctx context.Context, c *client, want string) (RootFolder, error) {
	var folders []RootFolder
	if err := c.get(ctx, "/rootfolder", nil, &folders); err != nil {
		return RootFolder{}, err
	}
	if len(folders) == 0 {
		return RootFolder{}, fmt.Errorf(
			"sonarr has no root folder configured — add one under Settings → Media Management")
	}

	want = strings.TrimSpace(want)
	chosen := RootFolder{}

	switch {
	case want == "" && len(folders) == 1:
		chosen = folders[0]
	case want == "":
		return RootFolder{}, fmt.Errorf(
			"root_folder is required: sonarr has %d of them (%s), and picking one decides "+
				"which disk fills up",
			len(folders), joinFolders(folders))
	default:
		for _, f := range folders {
			if strings.EqualFold(strings.TrimRight(f.Path, "/"), strings.TrimRight(want, "/")) {
				chosen = f
				break
			}
		}
		if chosen.Path == "" {
			return RootFolder{}, fmt.Errorf("no root folder at %q — sonarr has %s",
				want, joinFolders(folders))
		}
	}

	if !chosen.Accessible {
		return RootFolder{}, fmt.Errorf(
			"sonarr cannot access the root folder %s — a series added there would download "+
				"and then fail to import", chosen.Path)
	}

	return chosen, nil
}

// matchValue accepts the enum value in whatever case it was typed and returns
// it in the spelling Sonarr expects, which is camelCase and would otherwise be
// rejected for a capital letter.
func matchValue(allowed []string, want string) (string, error) {
	i := slices.IndexFunc(allowed, func(v string) bool { return strings.EqualFold(v, want) })
	if i < 0 {
		return "", fmt.Errorf("not one of %s", strings.Join(allowed, ", "))
	}
	return allowed[i], nil
}

func joinProfiles(ps []QualityProfile) string {
	names := make([]string, 0, len(ps))
	for _, p := range ps {
		names = append(names, fmt.Sprintf("%q (id %d)", p.Name, p.ID))
	}
	return strings.Join(names, ", ")
}

func joinFolders(fs []RootFolder) string {
	paths := make([]string, 0, len(fs))
	for _, f := range fs {
		paths = append(paths, f.Path)
	}
	return strings.Join(paths, ", ")
}

// --- wire types -----------------------------------------------------------

type lookupJSON struct {
	ID      int    `json:"id"`
	TvdbID  int    `json:"tvdbId"`
	ImdbID  string `json:"imdbId"`
	Title   string `json:"title"`
	Year    int    `json:"year"`
	Status  string `json:"status"`
	Network string `json:"network"`
	Runtime int    `json:"runtime"`

	Overview string `json:"overview"`
	Ended    bool   `json:"ended"`

	Seasons    []seasonJSON          `json:"seasons"`
	Statistics *seriesStatisticsJSON `json:"statistics"`

	RemotePoster string       `json:"remotePoster"`
	Images       []mediaCover `json:"images"`
}

type mediaCover struct {
	CoverType string `json:"coverType"`
	URL       string `json:"url"`
	RemoteURL string `json:"remoteUrl"`
}

// poster returns a URL the cover can actually be fetched from.
func (r lookupJSON) poster() string {
	isFetchable := func(raw string) bool {
		u, err := url.Parse(strings.TrimSpace(raw))
		return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
	}

	for _, img := range r.Images {
		if strings.EqualFold(img.CoverType, "poster") && isFetchable(img.RemoteURL) {
			return img.RemoteURL
		}
	}
	if isFetchable(r.RemotePoster) {
		return r.RemotePoster
	}
	return ""
}

// seasonCount excludes season 0, which is Sonarr's bucket for specials and is
// not what anyone means by "how many seasons".
func (r lookupJSON) seasonCount() int {
	n := 0
	for _, s := range r.Seasons {
		if s.SeasonNumber > 0 {
			n++
		}
	}
	return n
}

// episodeCount is what the metadata provider claims the show has. It is the
// size of what an add with monitor=all is about to go looking for, and it is
// absent often enough that the confirmation has to survive without it.
func (r lookupJSON) episodeCount() int {
	if r.Statistics != nil && r.Statistics.TotalEpisodeCount > 0 {
		return r.Statistics.TotalEpisodeCount
	}
	n := 0
	for _, s := range r.Seasons {
		if s.Statistics != nil {
			n += s.Statistics.TotalEpisodeCount
		}
	}
	return n
}

func (r lookupJSON) toResult() LookupResult {
	return LookupResult{
		TvdbID:      r.TvdbID,
		Title:       r.Title,
		Year:        r.Year,
		InLibrary:   r.ID != 0,
		SeriesID:    r.ID,
		Status:      r.Status,
		Network:     r.Network,
		SeasonCount: r.seasonCount(),
		Runtime:     r.Runtime,
		ImdbID:      r.ImdbID,
		PosterURL:   r.poster(),
		Overview:    truncate(r.Overview, 220),
	}
}
