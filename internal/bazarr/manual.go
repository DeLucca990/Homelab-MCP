package bazarr

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// A manual search is the one that shows its work: every subtitle every
// provider returned, scored, including the ones below the minimum score an
// automatic search would have thrown away. It is the answer to "the automatic
// search found nothing" and to "the subtitle it found is out of sync, get a
// different one".
//
// Choosing one of them is where the API gets awkward. Bazarr identifies a
// result by an opaque token — on older versions a serialized copy of the whole
// subtitle object, kilobytes of base64 — and downloading it means handing that
// token back. A model asked to copy kilobytes of base64 from one call into the
// next will get one character wrong, and the failure is a confusing one. So
// the token never leaves this server: each result is given a short id, the
// token is kept here against it, and the download takes the short id.
//
// The cost is that a candidate id is only good on the process that listed it,
// and only for a while. Bazarr keeps its own copy of the results for a limited
// time too, so a stale id would have failed there anyway.

// How long a listed candidate stays downloadable, and how many are kept.
const (
	candidateTTL = 30 * time.Minute
	maxCached    = 2000
)

// Default and ceiling for how many candidates a listing returns.
const (
	defaultCandidateLimit = 15
	maxCandidateLimit     = 50
)

// Candidate is one subtitle a provider offered.
type Candidate struct {
	ID string `json:"id" jsonschema:"what bazarr_subtitle_download takes to fetch this one"`

	Provider string `json:"provider"`
	SubtitleLanguage

	Score       int      `json:"score" jsonschema:"Bazarr's match score as a percentage of the best possible; the automatic search only takes one at or above the configured minimum"`
	Matches     []string `json:"matches,omitempty" jsonschema:"what this subtitle matched about the file — hash is the only one that guarantees sync; release_group, source and resolution make it likely"`
	DontMatches []string `json:"dont_matches,omitempty"`
	ReleaseInfo []string `json:"release_info,omitempty" jsonschema:"the release names the subtitle was made for, as the provider lists them"`
	Uploader    string   `json:"uploader,omitempty"`
	URL         string   `json:"url,omitempty"`
}

// Candidates is the result of one manual search.
type Candidates struct {
	Kind      string `json:"kind" jsonschema:"movie or episode"`
	Title     string `json:"title"`
	RadarrID  int    `json:"radarr_id,omitempty"`
	SeriesID  int    `json:"series_id,omitempty"`
	EpisodeID int    `json:"episode_id,omitempty"`

	Profile string `json:"profile" jsonschema:"the language profile the search was limited to"`

	Candidates []Candidate `json:"candidates"`

	TotalCount int `json:"total_count" jsonschema:"results before the language filter and the limit"`
	ShownCount int `json:"shown_count"`

	Warnings []string `json:"warnings,omitempty"`
}

// cachedCandidate is everything needed to download a candidate later.
type cachedCandidate struct {
	Candidate

	kind      string
	title     string
	radarrID  int
	seriesID  int
	episodeID int

	token          string
	forced         string
	hi             string
	originalFormat string

	listed time.Time
}

var (
	cacheMu sync.Mutex
	cache   = map[string]cachedCandidate{}
)

func remember(cc cachedCandidate) {
	cacheMu.Lock()
	defer cacheMu.Unlock()

	now := time.Now()
	for id, old := range cache {
		if now.Sub(old.listed) > candidateTTL {
			delete(cache, id)
		}
	}
	if len(cache) >= maxCached {
		// Oldest first: they are the ones least likely to be chosen.
		ids := make([]string, 0, len(cache))
		for id := range cache {
			ids = append(ids, id)
		}
		slices.SortFunc(ids, func(a, b string) int { return cache[a].listed.Compare(cache[b].listed) })
		for _, id := range ids[:len(ids)-maxCached+1] {
			delete(cache, id)
		}
	}

	cc.listed = now
	cache[cc.ID] = cc
}

func recall(id string) (cachedCandidate, bool) {
	cacheMu.Lock()
	defer cacheMu.Unlock()

	cc, ok := cache[strings.ToLower(strings.TrimSpace(id))]
	if !ok || time.Since(cc.listed) > candidateTTL {
		return cachedCandidate{}, false
	}
	return cc, true
}

// candidateID is short enough to copy without error and derived from the
// result itself, so listing the same search twice gives the same ids.
func candidateID(scope, provider, token string) string {
	h := sha256.Sum256([]byte(scope + "\x00" + provider + "\x00" + token))
	return hex.EncodeToString(h[:])[:8]
}

// GetCandidates runs a manual search for one movie or one episode.
func GetCandidates(ctx context.Context, t Target, language string, limit int) (Candidates, error) {
	if err := t.Validate(); err != nil {
		return Candidates{}, err
	}
	if t.SeriesID > 0 {
		return Candidates{}, fmt.Errorf("a manual search is for one movie or one episode — pass " +
			"'episode_id'; bazarr_subtitle_status with the series_id lists them")
	}

	c, err := newClient()
	if err != nil {
		return Candidates{}, err
	}

	switch {
	case limit <= 0:
		limit = defaultCandidateLimit
	case limit > maxCandidateLimit:
		limit = maxCandidateLimit
	}

	known, err := c.languages(ctx)
	if err != nil {
		return Candidates{}, err
	}
	var only *Language
	if language != "" {
		l, err := resolveLanguage(known, language)
		if err != nil {
			return Candidates{}, err
		}
		only = &l
	}

	profiles, _ := c.profiles(ctx)

	var (
		out        Candidates
		path       string
		query      url.Values
		profile    Profile
		hasProfile bool
	)

	if t.RadarrID > 0 {
		m, err := c.movie(ctx, t.RadarrID)
		if err != nil {
			return Candidates{}, err
		}
		out = Candidates{Kind: "movie", Title: movieLabel(m), RadarrID: m.RadarrID}
		profile, hasProfile = profileByID(profiles, m.ProfileID)
		path, query = "/providers/movies", url.Values{"radarrid": {strconv.Itoa(m.RadarrID)}}
	} else {
		e, err := c.episode(ctx, t.EpisodeID)
		if err != nil {
			return Candidates{}, err
		}
		s, err := c.series(ctx, e.SeriesID)
		if err != nil {
			return Candidates{}, err
		}
		out = Candidates{Kind: "episode", Title: s.Title + " " + e.Label(),
			SeriesID: e.SeriesID, EpisodeID: e.EpisodeID}
		profile, hasProfile = profileByID(profiles, s.ProfileID)
		path, query = "/providers/episodes", url.Values{"episodeid": {strconv.Itoa(e.EpisodeID)}}
	}

	// Bazarr searches the languages of the item's profile and nothing else, so
	// with no profile the search is empty by construction. Saying so beats a
	// three-minute wait for an empty list.
	if !hasProfile {
		return Candidates{}, fmt.Errorf("%s has no language profile, and a manual search only "+
			"looks for the languages a profile names — assign one with "+
			"bazarr_language_profile_set first, or use bazarr_subtitle_search with a 'language', "+
			"which works without one", out.Title)
	}
	out.Profile = fmt.Sprintf("%s (%s)", profile.Name, profile.Describe())
	if only != nil && !slices.ContainsFunc(profile.Languages, func(l ProfileLanguage) bool {
		return l.Code2 == only.Code2
	}) {
		return Candidates{}, fmt.Errorf("%s is not in the profile of %s (%s), and a manual search "+
			"only looks for the profile's languages — bazarr_subtitle_search with that 'language' "+
			"works regardless, or add it to the profile", only.Name, out.Title, out.Profile)
	}

	var raw struct {
		Data []candidateJSON `json:"data"`
	}
	if err := c.do(ctx, "GET", path, query, nil, &raw, providerTimeout); err != nil {
		return Candidates{}, err
	}

	out.TotalCount = len(raw.Data)
	scope := fmt.Sprintf("%s:%d:%d", out.Kind, out.RadarrID, out.EpisodeID)

	for _, r := range raw.Data {
		lang := SubtitleLanguage{Code2: r.Language, Name: r.Language,
			Forced: bool(r.Forced), HI: bool(r.HearingImpaired)}
		// Providers report a language the way the subtitle library spells it
		// ("pt-BR"), which is not always Bazarr's own code ("pb").
		if l, err := resolveLanguage(known, r.Language); err == nil {
			lang.Code2, lang.Name = l.Code2, l.Name
		}
		if only != nil && lang.Code2 != only.Code2 {
			continue
		}
		if len(out.Candidates) >= limit {
			continue
		}

		cand := Candidate{
			ID:               candidateID(scope, r.Provider, r.Subtitle),
			Provider:         r.Provider,
			SubtitleLanguage: lang,
			Score:            r.Score,
			Matches:          r.Matches,
			DontMatches:      r.DontMatches,
			ReleaseInfo:      r.ReleaseInfo,
			Uploader:         r.Uploader,
			URL:              r.URL,
		}
		remember(cachedCandidate{
			Candidate: cand,
			kind:      out.Kind, title: out.Title,
			radarrID: out.RadarrID, seriesID: out.SeriesID, episodeID: out.EpisodeID,
			token:          r.Subtitle,
			forced:         pyBool(bool(r.Forced)),
			hi:             pyBool(bool(r.HearingImpaired)),
			originalFormat: pyBool(bool(r.OriginalFormat)),
		})
		out.Candidates = append(out.Candidates, cand)
	}
	out.ShownCount = len(out.Candidates)

	switch {
	case out.TotalCount == 0:
		out.Warnings = append(out.Warnings, "no provider returned anything for this — "+
			"bazarr_system_health says whether the providers are throttled, and a file with no "+
			"release name recorded is matched on title alone")
	case out.ShownCount == 0 && only != nil:
		out.Warnings = append(out.Warnings, fmt.Sprintf(
			"the providers returned %d subtitles, none of them in %s", out.TotalCount, only.Name))
	case out.ShownCount > 0 && !slices.ContainsFunc(out.Candidates, func(c Candidate) bool {
		return slices.Contains(c.Matches, "hash")
	}):
		out.Warnings = append(out.Warnings, "no candidate matches the file by hash, so none is "+
			"guaranteed to be in sync — prefer one whose matches include release_group")
	}
	if out.ShownCount > 0 {
		out.Warnings = append(out.Warnings, fmt.Sprintf(
			"these ids stay valid for %d minutes on this server; bazarr_subtitle_download takes one",
			int(candidateTTL.Minutes())))
	}

	return out, nil
}

// DownloadPlan is a candidate resolved from its id.
type DownloadPlan struct {
	Candidate

	Kind      string `json:"kind"`
	Title     string `json:"title"`
	RadarrID  int    `json:"radarr_id,omitempty"`
	SeriesID  int    `json:"series_id,omitempty"`
	EpisodeID int    `json:"episode_id,omitempty"`

	cached cachedCandidate
}

// Token is part of the fingerprint: two candidates with the same provider and
// language are different subtitles.
func (p DownloadPlan) Token() string { return p.cached.token }

// PlanDownload resolves a candidate id from an earlier listing.
func PlanDownload(id string) (DownloadPlan, error) {
	if strings.TrimSpace(id) == "" {
		return DownloadPlan{}, fmt.Errorf("a candidate 'id' is required — " +
			"bazarr_subtitle_candidates lists them")
	}
	cc, ok := recall(id)
	if !ok {
		return DownloadPlan{}, fmt.Errorf("no candidate %q is known — candidate ids last %d minutes "+
			"and only on the server process that listed them; run bazarr_subtitle_candidates "+
			"again and pick from the fresh list", id, int(candidateTTL.Minutes()))
	}
	return DownloadPlan{
		Candidate: cc.Candidate,
		Kind:      cc.kind, Title: cc.title,
		RadarrID: cc.radarrID, SeriesID: cc.seriesID, EpisodeID: cc.episodeID,
		cached: cc,
	}, nil
}

// DownloadResult is what came of downloading a candidate.
type DownloadResult struct {
	Plan DownloadPlan `json:"plan"`

	OnDisk bool   `json:"on_disk" jsonschema:"whether a subtitle in this language is on disk after the download"`
	Path   string `json:"path,omitempty"`

	Warnings []string `json:"warnings,omitempty"`
}

// Download fetches a planned candidate and reads the item back.
func Download(ctx context.Context, p DownloadPlan) (DownloadResult, error) {
	c, err := newClient()
	if err != nil {
		return DownloadResult{}, err
	}

	res := DownloadResult{Plan: p}
	cc := p.cached

	form := url.Values{
		"hi":              {cc.hi},
		"forced":          {cc.forced},
		"original_format": {cc.originalFormat},
		"provider":        {cc.Provider},
		"subtitle":        {cc.token},
	}

	var have []Subtitle
	if p.Kind == "movie" {
		form.Set("radarrid", strconv.Itoa(p.RadarrID))
		if err := c.send(ctx, "POST", "/providers/movies", form, providerTimeout); err != nil {
			return res, downloadError(err)
		}
		m, err := c.movie(ctx, p.RadarrID)
		if err != nil {
			res.Warnings = append(res.Warnings, "downloaded, but the movie could not be read back: "+err.Error())
			return res, nil
		}
		have = m.Subtitles
	} else {
		form.Set("seriesid", strconv.Itoa(p.SeriesID))
		form.Set("episodeid", strconv.Itoa(p.EpisodeID))
		if err := c.send(ctx, "POST", "/providers/episodes", form, providerTimeout); err != nil {
			return res, downloadError(err)
		}
		e, err := c.episode(ctx, p.EpisodeID)
		if err != nil {
			res.Warnings = append(res.Warnings, "downloaded, but the episode could not be read back: "+err.Error())
			return res, nil
		}
		have = e.Subtitles
	}

	for _, s := range have {
		if !s.Embedded && s.same(p.SubtitleLanguage) {
			res.OnDisk = true
			res.Path = s.Path
			break
		}
	}
	if !res.OnDisk {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"no %s subtitle is on disk yet — recent Bazarr downloads in a background job, so "+
				"bazarr_subtitle_status in a moment will say whether it landed", p.SubtitleLanguage))
	}
	if !slices.Contains(p.Matches, "hash") {
		res.Warnings = append(res.Warnings, "this subtitle was not matched by hash; if it turns "+
			"out to be out of sync, bazarr_subtitle_sync aligns it to the audio")
	}

	return res, nil
}

// Bazarr's own message for a result it no longer holds is the actionable one;
// it only needs the tool name added.
func downloadError(err error) error {
	if strings.Contains(strings.ToLower(err.Error()), "search again") {
		return fmt.Errorf("%w — run bazarr_subtitle_candidates again and pick from the fresh list", err)
	}
	return err
}

// --- wire types -----------------------------------------------------------

type candidateJSON struct {
	Provider        string   `json:"provider"`
	Language        string   `json:"language"`
	Forced          flexBool `json:"forced"`
	HearingImpaired flexBool `json:"hearing_impaired"`
	OriginalFormat  flexBool `json:"original_format"`
	Score           int      `json:"score"`
	Subtitle        string   `json:"subtitle"`
	URL             string   `json:"url"`
	Uploader        string   `json:"uploader"`
	Matches         []string `json:"matches"`
	DontMatches     []string `json:"dont_matches"`
	ReleaseInfo     []string `json:"release_info"`
}
