package radarr

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// An interactive search is the answer to "what did it find, and why did it not
// take any of it". radarr_movie_search asks Radarr to pick, and when Radarr
// picks nothing it says nothing; this lists every release the indexers
// returned, in the order Radarr would prefer them, with the reason Radarr
// rejected each one — wrong quality for the profile, a language rule, too
// small, already blocklisted. That list is usually the whole diagnosis, and
// grabbing one of them is the override.
//
// Radarr keeps the results for 30 minutes and grabs one by its guid and
// indexer. A guid is often a full URL, so the release is handed to the model
// under an 8-character id and the guid stays here — the same arrangement the
// Bazarr module uses for subtitle candidates.

const (
	releaseTTL     = 30 * time.Minute
	releaseTimeout = 3 * time.Minute

	defaultReleaseLimit = 20
	maxReleaseLimit     = 100
)

// Release is one release an indexer offered for a movie.
type Release struct {
	ID   string `json:"id" jsonschema:"what radarr_release_grab takes"`
	Rank int    `json:"rank" jsonschema:"Radarr's own order of preference, 1 first"`

	Title        string   `json:"title"`
	Quality      string   `json:"quality"`
	SizeBytes    uint64   `json:"size_bytes,omitempty"`
	Languages    []string `json:"languages,omitempty"`
	ReleaseGroup string   `json:"release_group,omitempty"`
	Indexer      string   `json:"indexer"`
	Protocol     string   `json:"protocol"`
	Seeders      *int     `json:"seeders,omitempty"`
	AgeHours     int      `json:"age_hours"`

	CustomFormatScore int `json:"custom_format_score,omitempty" jsonschema:"how the profile's custom formats score it; higher is preferred"`

	Approved            bool     `json:"approved" jsonschema:"Radarr would grab this one on its own"`
	TemporarilyRejected bool     `json:"temporarily_rejected,omitempty" jsonschema:"rejected only for now — a delay profile, or not yet available"`
	Rejections          []string `json:"rejections,omitempty" jsonschema:"why Radarr would not grab it, in its own words"`
}

type Releases struct {
	MovieID int    `json:"movie_id"`
	Movie   string `json:"movie"`

	Releases      []Release `json:"releases"`
	TotalCount    int       `json:"total_count"`
	ApprovedCount int       `json:"approved_count"`

	TopRejections map[string]int `json:"top_rejections,omitempty" jsonschema:"how many releases each rejection reason turned away"`

	Warnings []string `json:"warnings,omitempty"`
}

type cachedRelease struct {
	Release
	guid      string
	indexerID int
	movieID   int
	movie     string
	listed    time.Time
}

var (
	releaseMu    sync.Mutex
	releaseCache = map[string]cachedRelease{}
)

func rememberRelease(r cachedRelease) {
	releaseMu.Lock()
	defer releaseMu.Unlock()
	for id, old := range releaseCache {
		if time.Since(old.listed) > releaseTTL {
			delete(releaseCache, id)
		}
	}
	r.listed = time.Now()
	releaseCache[r.ID] = r
}

func recallRelease(id string) (cachedRelease, bool) {
	releaseMu.Lock()
	defer releaseMu.Unlock()
	r, ok := releaseCache[strings.ToLower(strings.TrimSpace(id))]
	if !ok || time.Since(r.listed) > releaseTTL {
		return cachedRelease{}, false
	}
	return r, true
}

func shortID(parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(h[:])[:8]
}

// GetReleases runs an interactive search for one movie.
func GetReleases(ctx context.Context, movie Movie, limit int) (Releases, error) {
	c, err := newClient()
	if err != nil {
		return Releases{}, err
	}
	switch {
	case limit <= 0:
		limit = defaultReleaseLimit
	case limit > maxReleaseLimit:
		limit = maxReleaseLimit
	}

	var raw []releaseJSON
	q := url.Values{"movieId": {strconv.Itoa(movie.ID)}}
	if err := c.do(ctx, http.MethodGet, "/release", q, nil, &raw, releaseTimeout); err != nil {
		return Releases{}, err
	}

	label := movie.Title
	if movie.Year > 0 {
		label = fmt.Sprintf("%s (%d)", movie.Title, movie.Year)
	}
	out := Releases{MovieID: movie.ID, Movie: label, TotalCount: len(raw), TopRejections: map[string]int{}}

	for i, r := range raw {
		rel := r.toRelease(i + 1)
		rel.ID = shortID(strconv.Itoa(movie.ID), strconv.Itoa(r.IndexerID), r.GUID)
		if rel.Approved {
			out.ApprovedCount++
		}
		for _, why := range rel.Rejections {
			out.TopRejections[rejectionKind(why)]++
		}
		if len(out.Releases) < limit {
			rememberRelease(cachedRelease{Release: rel, guid: r.GUID, indexerID: r.IndexerID,
				movieID: movie.ID, movie: label})
			out.Releases = append(out.Releases, rel)
		}
	}

	switch {
	case out.TotalCount == 0:
		out.Warnings = append(out.Warnings, fmt.Sprintf("the indexers returned nothing for %s — "+
			"prowlarr_indexer_status says whether they are answering, and prowlarr_search "+
			"whether any release exists under another name", label))
	case out.ApprovedCount == 0:
		out.Warnings = append(out.Warnings, fmt.Sprintf("%d releases were found and Radarr "+
			"rejected every one — the rejections are why nothing was grabbed. Grabbing one "+
			"anyway with radarr_release_grab overrides them", out.TotalCount))
	}
	if movie.HasFile {
		out.Warnings = append(out.Warnings, fmt.Sprintf("%s already has a file (%s); a grab "+
			"replaces it once imported", label, blank(movie.Quality)))
	}
	if out.TotalCount > len(out.Releases) {
		out.Warnings = append(out.Warnings, fmt.Sprintf("%d releases, %d shown — raise 'limit' "+
			"to see more", out.TotalCount, len(out.Releases)))
	}
	if len(out.Releases) > 0 {
		out.Warnings = append(out.Warnings, "these ids stay valid for 30 minutes; "+
			"radarr_release_grab takes one")
	}
	return out, nil
}

// rejectionKind folds rejections that differ only in their numbers ("Size
// 1.2 GB is smaller than minimum allowed 1.5 GB") into one reason.
func rejectionKind(why string) string {
	for _, cut := range []string{" is ", " was ", ":", " ("} {
		if i := strings.Index(why, cut); i > 0 && i < 60 {
			return why[:i]
		}
	}
	if len(why) > 60 {
		return why[:60] + "…"
	}
	return why
}

// --- grabbing ----------------------------------------------------------------

type GrabPlan struct {
	Release
	MovieID int    `json:"movie_id"`
	Movie   string `json:"movie"`

	Warnings []string `json:"warnings,omitempty"`

	guid      string
	indexerID int
}

// GUID is part of the fingerprint: two releases with the same title from two
// indexers are different grabs.
func (p GrabPlan) GUID() string { return p.guid }

type GrabResult struct {
	Plan     GrabPlan `json:"plan"`
	Warnings []string `json:"warnings,omitempty"`
}

// PlanGrab resolves a release id from an earlier interactive search.
func PlanGrab(id string) (GrabPlan, error) {
	if strings.TrimSpace(id) == "" {
		return GrabPlan{}, fmt.Errorf("a release 'id' is required — radarr_releases lists them")
	}
	r, ok := recallRelease(id)
	if !ok {
		return GrabPlan{}, fmt.Errorf("no release %q is known — release ids last 30 minutes and "+
			"only on the server process that listed them; run radarr_releases again", id)
	}
	p := GrabPlan{Release: r.Release, MovieID: r.movieID, Movie: r.movie, guid: r.guid, indexerID: r.indexerID}
	if len(r.Rejections) > 0 {
		p.Warnings = append(p.Warnings, "Radarr rejected this release ("+
			strings.Join(r.Rejections, "; ")+") — grabbing it overrides that")
	}
	if r.Protocol == "torrent" && r.Seeders != nil && *r.Seeders == 0 {
		p.Warnings = append(p.Warnings, "it has no seeders, so it will sit in the queue and never finish")
	}
	return p, nil
}

// Grab sends a planned release to the download client through Radarr, so it
// is tracked and imported like any other.
func Grab(ctx context.Context, p GrabPlan) (GrabResult, error) {
	c, err := newClient()
	if err != nil {
		return GrabResult{}, err
	}
	body := map[string]any{"guid": p.guid, "indexerId": p.indexerID, "movieId": p.MovieID}
	if err := c.do(ctx, http.MethodPost, "/release", nil, body, nil, releaseTimeout); err != nil {
		if strings.Contains(err.Error(), "try searching again") {
			return GrabResult{}, fmt.Errorf("%w — Radarr's copy of the search expired; run radarr_releases again", err)
		}
		return GrabResult{}, err
	}
	return GrabResult{Plan: p, Warnings: []string{"sent to the download client; radarr_queue_status " +
		"shows it arriving, and Radarr imports it when it finishes"}}, nil
}

// --- wire types -----------------------------------------------------------

type releaseJSON struct {
	GUID      string  `json:"guid"`
	Title     string  `json:"title"`
	IndexerID int     `json:"indexerId"`
	Indexer   string  `json:"indexer"`
	Protocol  string  `json:"protocol"`
	Size      int64   `json:"size"`
	Seeders   *int    `json:"seeders"`
	AgeHours  float64 `json:"ageHours"`

	ReleaseGroup      string `json:"releaseGroup"`
	CustomFormatScore int    `json:"customFormatScore"`

	Quality *struct {
		Quality struct {
			Name string `json:"name"`
		} `json:"quality"`
	} `json:"quality"`
	Languages []struct {
		Name string `json:"name"`
	} `json:"languages"`

	Approved            bool     `json:"approved"`
	TemporarilyRejected bool     `json:"temporarilyRejected"`
	Rejections          []string `json:"rejections"`
}

func (r releaseJSON) toRelease(rank int) Release {
	rel := Release{
		Rank:                rank,
		Title:               r.Title,
		Indexer:             r.Indexer,
		Protocol:            r.Protocol,
		Seeders:             r.Seeders,
		AgeHours:            int(r.AgeHours),
		ReleaseGroup:        r.ReleaseGroup,
		CustomFormatScore:   r.CustomFormatScore,
		Approved:            r.Approved,
		TemporarilyRejected: r.TemporarilyRejected,
		Rejections:          r.Rejections,
	}
	if r.Size > 0 {
		rel.SizeBytes = uint64(r.Size)
	}
	if r.Quality != nil {
		rel.Quality = r.Quality.Quality.Name
	}
	for _, l := range r.Languages {
		if l.Name != "" {
			rel.Languages = append(rel.Languages, l.Name)
		}
	}
	return rel
}

func blank(s string) string {
	if s == "" {
		return "unknown quality"
	}
	return s
}
