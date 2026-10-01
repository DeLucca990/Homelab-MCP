package sonarr

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

// An interactive search lists every release the indexers returned for one
// episode or one season, in the order Sonarr would prefer them, with the
// reason Sonarr rejected each one. When sonarr_series_search grabs nothing and
// says nothing, this is the list that explains it — and grabbing one of them
// is the override.
//
// Sonarr grabs a listed release by its guid and indexer, and keeps the results
// for 30 minutes. A guid is often a whole URL, so the model gets an
// 8-character id and the guid stays on this server.

const (
	releaseTTL     = 30 * time.Minute
	releaseTimeout = 3 * time.Minute

	defaultReleaseLimit = 20
	maxReleaseLimit     = 100
)

type Release struct {
	ID   string `json:"id" jsonschema:"what sonarr_release_grab takes"`
	Rank int    `json:"rank" jsonschema:"Sonarr's own order of preference, 1 first"`

	Title        string   `json:"title"`
	Episodes     string   `json:"episodes" jsonschema:"what the release covers, e.g. S02E03 or S02 (full season)"`
	Quality      string   `json:"quality"`
	SizeBytes    uint64   `json:"size_bytes,omitempty"`
	Languages    []string `json:"languages,omitempty"`
	ReleaseGroup string   `json:"release_group,omitempty"`
	Indexer      string   `json:"indexer"`
	Protocol     string   `json:"protocol"`
	Seeders      *int     `json:"seeders,omitempty"`
	AgeHours     int      `json:"age_hours"`

	CustomFormatScore int `json:"custom_format_score,omitempty"`

	Approved            bool     `json:"approved" jsonschema:"Sonarr would grab this one on its own"`
	TemporarilyRejected bool     `json:"temporarily_rejected,omitempty"`
	Rejections          []string `json:"rejections,omitempty" jsonschema:"why Sonarr would not grab it, in its own words"`
}

type Releases struct {
	SeriesID int    `json:"series_id"`
	Target   string `json:"target" jsonschema:"the episode or season searched"`

	Releases      []Release      `json:"releases"`
	TotalCount    int            `json:"total_count"`
	ApprovedCount int            `json:"approved_count"`
	TopRejections map[string]int `json:"top_rejections,omitempty"`

	Warnings []string `json:"warnings,omitempty"`
}

type cachedRelease struct {
	Release
	guid      string
	indexerID int
	seriesID  int
	episodeID int
	target    string
	listed    time.Time
}

var (
	releaseMu    sync.Mutex
	releaseCache = map[string]cachedRelease{}
)

func shortID(parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(h[:])[:8]
}

// GetReleases runs an interactive search for one episode, or one season when
// episodeID is 0.
func GetReleases(ctx context.Context, seriesID, season, episodeID, limit int) (Releases, error) {
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

	q := url.Values{}
	var out Releases
	switch {
	case episodeID > 0:
		var e episodeJSON
		if err := c.get(ctx, "/episode/"+strconv.Itoa(episodeID), nil, &e); err != nil {
			return Releases{}, fmt.Errorf("no episode %d in Sonarr — sonarr_missing_episodes lists "+
				"episode ids: %w", episodeID, err)
		}
		s, err := GetSeries(ctx, e.SeriesID)
		if err != nil {
			return Releases{}, err
		}
		q.Set("episodeId", strconv.Itoa(episodeID))
		out.SeriesID, out.Target = s.ID, s.Title+" "+EpisodeCode(e.SeasonNumber, e.EpisodeNumber)
	case seriesID > 0:
		s, err := GetSeries(ctx, seriesID)
		if err != nil {
			return Releases{}, err
		}
		q.Set("seriesId", strconv.Itoa(s.ID))
		q.Set("seasonNumber", strconv.Itoa(season))
		out.SeriesID, out.Target = s.ID, fmt.Sprintf("%s season %d", s.Title, season)
	default:
		return Releases{}, fmt.Errorf("pass an 'episode_id', or a 'series_id' with a 'season' — " +
			"an interactive search is for one episode or one season")
	}

	var raw []releaseJSON
	if err := c.do(ctx, http.MethodGet, "/release", q, nil, &raw, releaseTimeout); err != nil {
		return Releases{}, err
	}

	out.TotalCount = len(raw)
	out.TopRejections = map[string]int{}
	for i, r := range raw {
		rel := r.toRelease(i + 1)
		rel.ID = shortID(out.Target, strconv.Itoa(r.IndexerID), r.GUID)
		if rel.Approved {
			out.ApprovedCount++
		}
		for _, why := range rel.Rejections {
			out.TopRejections[rejectionKind(why)]++
		}
		if len(out.Releases) < limit {
			releaseMu.Lock()
			for id, old := range releaseCache {
				if time.Since(old.listed) > releaseTTL {
					delete(releaseCache, id)
				}
			}
			releaseCache[rel.ID] = cachedRelease{Release: rel, guid: r.GUID, indexerID: r.IndexerID,
				seriesID: out.SeriesID, episodeID: episodeID, target: out.Target, listed: time.Now()}
			releaseMu.Unlock()
			out.Releases = append(out.Releases, rel)
		}
	}

	switch {
	case out.TotalCount == 0:
		out.Warnings = append(out.Warnings, fmt.Sprintf("the indexers returned nothing for %s — "+
			"prowlarr_indexer_status says whether they are answering; an anime or daily show "+
			"set to the wrong series type is also searched under the wrong numbering", out.Target))
	case out.ApprovedCount == 0:
		out.Warnings = append(out.Warnings, fmt.Sprintf("%d releases were found and Sonarr "+
			"rejected every one — the rejections are why nothing was grabbed. Grabbing one "+
			"anyway with sonarr_release_grab overrides them", out.TotalCount))
	}
	if out.TotalCount > len(out.Releases) {
		out.Warnings = append(out.Warnings, fmt.Sprintf("%d releases, %d shown — raise 'limit'",
			out.TotalCount, len(out.Releases)))
	}
	if len(out.Releases) > 0 {
		out.Warnings = append(out.Warnings, "these ids stay valid for 30 minutes; sonarr_release_grab takes one")
	}
	return out, nil
}

func rejectionKind(why string) string {
	for _, cut := range []string{" is ", " was ", ":", " ("} {
		if i := strings.Index(why, cut); i > 0 && i < 60 {
			return why[:i]
		}
	}
	return truncate(why, 60)
}

// --- grabbing ----------------------------------------------------------------

type GrabPlan struct {
	Release
	SeriesID int    `json:"series_id"`
	Target   string `json:"target"`

	Warnings []string `json:"warnings,omitempty"`

	guid      string
	indexerID int
	episodeID int
}

// GUID is part of the fingerprint.
func (p GrabPlan) GUID() string { return p.guid }

type GrabResult struct {
	Plan     GrabPlan `json:"plan"`
	Warnings []string `json:"warnings,omitempty"`
}

// PlanGrab resolves a release id from an earlier interactive search.
func PlanGrab(id string) (GrabPlan, error) {
	if strings.TrimSpace(id) == "" {
		return GrabPlan{}, fmt.Errorf("a release 'id' is required — sonarr_releases lists them")
	}
	releaseMu.Lock()
	r, ok := releaseCache[strings.ToLower(strings.TrimSpace(id))]
	releaseMu.Unlock()
	if !ok || time.Since(r.listed) > releaseTTL {
		return GrabPlan{}, fmt.Errorf("no release %q is known — release ids last 30 minutes and "+
			"only on the server process that listed them; run sonarr_releases again", id)
	}
	p := GrabPlan{Release: r.Release, SeriesID: r.seriesID, Target: r.target,
		guid: r.guid, indexerID: r.indexerID, episodeID: r.episodeID}
	if len(r.Rejections) > 0 {
		p.Warnings = append(p.Warnings, "Sonarr rejected this release ("+
			strings.Join(r.Rejections, "; ")+") — grabbing it overrides that")
	}
	if r.Protocol == "torrent" && r.Seeders != nil && *r.Seeders == 0 {
		p.Warnings = append(p.Warnings, "it has no seeders, so it will sit in the queue and never finish")
	}
	if strings.Contains(r.Episodes, "full season") {
		p.Warnings = append(p.Warnings, "this is a season pack: every episode in it is imported, "+
			"replacing files already on disk where it is better")
	}
	return p, nil
}

// Grab sends a planned release to the download client through Sonarr.
func Grab(ctx context.Context, p GrabPlan) (GrabResult, error) {
	c, err := newClient()
	if err != nil {
		return GrabResult{}, err
	}
	body := map[string]any{"guid": p.guid, "indexerId": p.indexerID, "seriesId": p.SeriesID}
	if p.episodeID > 0 {
		body["episodeId"] = p.episodeID
	}
	if err := c.do(ctx, http.MethodPost, "/release", nil, body, nil, releaseTimeout); err != nil {
		if strings.Contains(err.Error(), "try searching again") {
			return GrabResult{}, fmt.Errorf("%w — Sonarr's copy of the search expired; run sonarr_releases again", err)
		}
		return GrabResult{}, err
	}
	return GrabResult{Plan: p, Warnings: []string{"sent to the download client; sonarr_queue_status " +
		"shows it arriving, and Sonarr imports it when it finishes"}}, nil
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

	FullSeason     bool  `json:"fullSeason"`
	SeasonNumber   int   `json:"seasonNumber"`
	EpisodeNumbers []int `json:"episodeNumbers"`

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
	switch {
	case r.FullSeason:
		rel.Episodes = fmt.Sprintf("S%02d (full season)", r.SeasonNumber)
	case len(r.EpisodeNumbers) == 1:
		rel.Episodes = EpisodeCode(r.SeasonNumber, r.EpisodeNumbers[0])
	case len(r.EpisodeNumbers) > 1:
		rel.Episodes = fmt.Sprintf("%s-E%02d", EpisodeCode(r.SeasonNumber, r.EpisodeNumbers[0]),
			r.EpisodeNumbers[len(r.EpisodeNumbers)-1])
	}
	return rel
}
