package radarr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// Adding a movie is split in two on purpose: Plan resolves everything and
// changes nothing, Add performs the one request that does.
//
// The split exists for the confirmation round trip. The handler plans on the
// first pass to build the message a human reads, and plans again on the retry
// to recompute the fingerprint. Both passes see the same resolved plan — the
// profile, the folder, the title — so what runs is what was shown, and a
// resolution that shifted in between (a profile renamed, the movie added by
// someone else) changes the fingerprint and stops the add rather than
// completing it against different values.

var minimumAvailabilityValues = []string{"tba", "announced", "inCinemas", "released"}

const DefaultQualityProfile = "HD-1080p"

const defaultMinimumAvailability = "released"

type LookupResult struct {
	TmdbID int    `json:"tmdb_id" jsonschema:"pass this to radarr_movie_add"`
	Title  string `json:"title"`
	Year   int    `json:"year,omitempty"`

	InLibrary bool `json:"in_library" jsonschema:"true when this movie has already been added to Radarr, in which case adding it again is refused"`
	MovieID   int  `json:"movie_id,omitempty" jsonschema:"Radarr's id for it, when it is already in the library"`
	HasFile   bool `json:"has_file,omitempty" jsonschema:"true when it is already downloaded"`

	Status    string `json:"status,omitempty" jsonschema:"tba, announced, inCinemas or released"`
	Runtime   int    `json:"runtime_minutes,omitempty"`
	Studio    string `json:"studio,omitempty"`
	ImdbID    string `json:"imdb_id,omitempty"`
	PosterURL string `json:"poster_url,omitempty" jsonschema:"cover art hosted by the metadata provider, ready to display"`
	Overview  string `json:"overview,omitempty"`
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

	var records []lookupJSON
	err = c.do(ctx, "GET", "/movie/lookup", url.Values{"term": {term}}, nil, &records, lookupTimeout)
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

// AddRequest is what the caller asked for, before anything is resolved.
type AddRequest struct {
	TmdbID int

	QualityProfile string // name or id
	RootFolder     string // path

	Monitored           bool
	SearchOnAdd         bool
	MinimumAvailability string
}

// AddPlan is the resolved request: exactly what would be sent, in the words a
// human reads before approving it.
type AddPlan struct {
	TmdbID int    `json:"tmdb_id"`
	Title  string `json:"title"`
	Year   int    `json:"year,omitempty"`

	PosterURL string `json:"poster_url,omitempty" jsonschema:"the movie's cover art, hosted by the metadata provider"`

	QualityProfileID   int    `json:"quality_profile_id"`
	QualityProfileName string `json:"quality_profile"`
	RootFolderPath     string `json:"root_folder"`

	Monitored           bool   `json:"monitored"`
	SearchOnAdd         bool   `json:"search_on_add"`
	MinimumAvailability string `json:"minimum_availability"`

	// The lookup resource with the fields above overlaid. Radarr's POST /movie
	// takes a whole movie resource and uses parts of it (titleSlug, images) that
	// nothing here has any business synthesising, so what it returned is sent
	// back rather than rebuilt.
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
		strconv.Itoa(p.TmdbID),
		p.Title,
		strconv.Itoa(p.Year),
		strconv.Itoa(p.QualityProfileID),
		p.RootFolderPath,
		strconv.FormatBool(p.Monitored),
		strconv.FormatBool(p.SearchOnAdd),
		p.MinimumAvailability,
	}
}

// Plan resolves an add request against the live Radarr and changes nothing. It
// fails on anything ambiguous rather than picking for the user.
func Plan(ctx context.Context, req AddRequest) (AddPlan, error) {
	if req.TmdbID <= 0 {
		return AddPlan{}, fmt.Errorf(
			"a tmdb_id is required — run radarr_movie_lookup first and take it from the result")
	}

	availability := strings.TrimSpace(req.MinimumAvailability)
	if availability == "" {
		availability = defaultMinimumAvailability
	}
	if !slices.ContainsFunc(minimumAvailabilityValues, func(v string) bool {
		return strings.EqualFold(v, availability)
	}) {
		return AddPlan{}, fmt.Errorf("minimum_availability must be one of %s, got %q",
			strings.Join(minimumAvailabilityValues, ", "), req.MinimumAvailability)
	}

	c, err := newClient()
	if err != nil {
		return AddPlan{}, err
	}

	// The lookup resource is both the identity check and the request body, so it
	// is kept twice: as a map to post back untouched, and typed to read from.
	// Decoding the same bytes into both keeps one set of field names.
	var raw json.RawMessage
	err = c.do(ctx, "GET", "/movie/lookup/tmdb",
		url.Values{"tmdbId": {strconv.Itoa(req.TmdbID)}}, nil, &raw, lookupTimeout)
	if err != nil {
		return AddPlan{}, err
	}

	var found map[string]any
	if err := json.Unmarshal(raw, &found); err != nil || len(found) == 0 {
		return AddPlan{}, fmt.Errorf("TMDB has no movie with id %d", req.TmdbID)
	}
	var movie lookupJSON
	if err := json.Unmarshal(raw, &movie); err != nil {
		return AddPlan{}, fmt.Errorf("radarr returned a movie that could not be read: %w", err)
	}

	if movie.Title == "" {
		return AddPlan{}, fmt.Errorf("radarr returned no title for tmdb id %d", req.TmdbID)
	}

	// A non-zero id on a lookup result means Radarr already has it. Adding it
	// again is rejected by Radarr anyway; saying so here names the movie.
	// Adding is not the way to re-download something. Radarr answers this with
	// "This movie has already been added", which is correct and unhelpful: the
	// caller wanted a file, not a library entry. Name the tool that does that.
	if movie.ID != 0 {
		return AddPlan{}, fmt.Errorf(
			"%s (%d) is already in your library (movie id %d) — to look for a release "+
				"of it now, use radarr_movie_search with movie_id %d; "+
				"radarr_library_status shows what state it is in",
			movie.Title, movie.Year, movie.ID, movie.ID)
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
		TmdbID:              req.TmdbID,
		Title:               movie.Title,
		Year:                movie.Year,
		PosterURL:           movie.poster(),
		QualityProfileID:    profile.ID,
		QualityProfileName:  profile.Name,
		RootFolderPath:      folder.Path,
		Monitored:           req.Monitored,
		SearchOnAdd:         req.SearchOnAdd,
		MinimumAvailability: availability,
		body:                found,
	}

	monitor := "movieOnly"
	if !req.Monitored {
		monitor = "none"
	}

	plan.body["qualityProfileId"] = profile.ID
	plan.body["rootFolderPath"] = folder.Path
	plan.body["monitored"] = req.Monitored
	plan.body["minimumAvailability"] = availability
	plan.body["addOptions"] = map[string]any{
		"monitor":        monitor,
		"searchForMovie": req.SearchOnAdd,
	}

	return plan, nil
}

type AddResult struct {
	MovieID int    `json:"movie_id"`
	TmdbID  int    `json:"tmdb_id"`
	Title   string `json:"title"`
	Year    int    `json:"year,omitempty"`

	Path           string `json:"path,omitempty" jsonschema:"the folder Radarr created for it"`
	QualityProfile string `json:"quality_profile"`

	Monitored     bool `json:"monitored"`
	SearchStarted bool `json:"search_started" jsonschema:"true when Radarr began looking for a release immediately; false means it will wait for its next scheduled search"`

	Warnings []string `json:"warnings,omitempty"`
}

// Add performs the one request that changes something. It takes a plan rather
// than a request, so it cannot resolve anything the user was not shown.
func Add(ctx context.Context, plan AddPlan) (AddResult, error) {
	c, err := newClient()
	if err != nil {
		return AddResult{}, err
	}

	var created movieJSON
	if err := c.post(ctx, "/movie", plan.body, &created); err != nil {
		return AddResult{}, err
	}

	res := AddResult{
		MovieID:        created.ID,
		TmdbID:         plan.TmdbID,
		Title:          plan.Title,
		Year:           plan.Year,
		Path:           created.Path,
		QualityProfile: plan.QualityProfileName,
		Monitored:      created.Monitored,
		SearchStarted:  plan.SearchOnAdd,
	}

	if !res.Monitored {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"%s was added unmonitored — Radarr will not search for it or grab it", plan.Title))
	}
	if res.Monitored && !plan.SearchOnAdd {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"no search was started — %s will be picked up by Radarr's next scheduled search, "+
				"which can be hours away", plan.Title))
	}
	if plan.MinimumAvailability != "released" {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"minimum availability is %q, so Radarr may grab a pre-release copy",
			plan.MinimumAvailability))
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
		return QualityProfile{}, fmt.Errorf("radarr has no quality profiles configured")
	}

	want = strings.TrimSpace(want)
	if want == "" {
		// 1080p is the resolution a homelab wants unless it says otherwise, and
		// Radarr ships a profile by this name, so most installs land here.
		if p, ok := profileNamed(profiles, DefaultQualityProfile); ok {
			return p, nil
		}
		// Renamed or deleted: fall back to the only profile there is, and
		// refuse rather than guess when there is a choice to make.
		if len(profiles) == 1 {
			return profiles[0], nil
		}
		return QualityProfile{}, fmt.Errorf(
			"quality_profile is required: radarr has %d of them (%s) and none is called %q, "+
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

	return QualityProfile{}, fmt.Errorf("no quality profile called %q — radarr has %s",
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
			"radarr has no root folder configured — add one under Settings → Media Management")
	}

	want = strings.TrimSpace(want)
	chosen := RootFolder{}

	switch {
	case want == "" && len(folders) == 1:
		chosen = folders[0]
	case want == "":
		return RootFolder{}, fmt.Errorf(
			"root_folder is required: radarr has %d of them (%s), and picking one decides "+
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
			return RootFolder{}, fmt.Errorf("no root folder at %q — radarr has %s",
				want, joinFolders(folders))
		}
	}

	if !chosen.Accessible {
		return RootFolder{}, fmt.Errorf(
			"radarr cannot access the root folder %s — a movie added there would download "+
				"and then fail to import", chosen.Path)
	}

	return chosen, nil
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
	ID       int    `json:"id"`
	TmdbID   int    `json:"tmdbId"`
	ImdbID   string `json:"imdbId"`
	Title    string `json:"title"`
	Year     int    `json:"year"`
	Status   string `json:"status"`
	Runtime  int    `json:"runtime"`
	Studio   string `json:"studio"`
	Overview string `json:"overview"`
	HasFile  bool   `json:"hasFile"`

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

func (r lookupJSON) toResult() LookupResult {
	return LookupResult{
		TmdbID:    r.TmdbID,
		Title:     r.Title,
		Year:      r.Year,
		InLibrary: r.ID != 0,
		MovieID:   r.ID,
		HasFile:   r.HasFile,
		Status:    r.Status,
		Runtime:   r.Runtime,
		Studio:    r.Studio,
		ImdbID:    r.ImdbID,
		PosterURL: r.poster(),
		Overview:  truncate(r.Overview, 220),
	}
}

// The overview is there to tell two films of the same name apart, which the
// first couple of sentences already do.
func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	cut := strings.LastIndex(s[:n], " ")
	if cut < n/2 {
		cut = n
	}
	return s[:cut] + "…"
}
