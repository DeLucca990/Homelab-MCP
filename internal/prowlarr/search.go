package prowlarr

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// A search straight across the indexers answers the question that sits under
// "why has this not downloaded": does any release exist at all? If Prowlarr
// finds nothing, no amount of searching from Radarr will either; if it finds
// plenty and Radarr grabbed nothing, the reason is on the Radarr side —
// quality profile, a language rule, a release not yet past availability.
//
// It never grabs. A release grabbed from here goes to the download client
// without Radarr or Sonarr knowing, so it would never be imported; the *arr
// search tools are the ones that should act on what this finds.

const (
	defaultReleaseLimit = 25
	maxReleaseLimit     = 100
)

// Newznab's top-level categories, which every indexer maps onto.
const (
	categoryMovies = 2000
	categoryTV     = 5000
)

// A standing fact rather than a warning: it is true of every answer.
const searchNote = "nothing was grabbed — a release grabbed from Prowlarr bypasses Radarr and " +
	"Sonarr and is never imported; radarr_movie_search or sonarr_series_search is the way to " +
	"have them pick one"

type SearchRequest struct {
	Query    string
	Kind     string   // "any", "movie" or "tv"
	Indexers []string // ids or names; empty means every enabled indexer
	Limit    int
}

// Release is one result.
type Release struct {
	Title     string `json:"title"`
	Indexer   string `json:"indexer"`
	IndexerID int    `json:"indexer_id"`
	Protocol  string `json:"protocol"`

	SizeBytes uint64 `json:"size_bytes,omitempty"`
	Seeders   *int   `json:"seeders,omitempty" jsonschema:"torrents only; a torrent with none will never finish"`
	Leechers  *int   `json:"leechers,omitempty"`
	Grabs     *int   `json:"grabs,omitempty"`
	AgeHours  int    `json:"age_hours"`

	Categories []string `json:"categories,omitempty"`
	InfoURL    string   `json:"info_url,omitempty" jsonschema:"the release's page on the indexer"`
}

type SearchResult struct {
	Query    string   `json:"query"`
	Kind     string   `json:"kind"`
	Indexers []string `json:"indexers,omitempty" jsonschema:"the indexers asked; empty means every enabled one"`

	Releases   []Release `json:"releases" jsonschema:"most seeded first"`
	TotalCount int       `json:"total_count"`
	ShownCount int       `json:"shown_count"`

	ByIndexer map[string]int `json:"by_indexer,omitempty" jsonschema:"how many results each indexer returned"`

	Note string `json:"note"`

	Warnings []string `json:"warnings,omitempty"`
}

// Search asks the indexers directly.
func Search(ctx context.Context, req SearchRequest) (SearchResult, error) {
	query := strings.TrimSpace(req.Query)
	if query == "" {
		return SearchResult{}, fmt.Errorf("a 'query' is required — a title, as a release would " +
			"be named, e.g. 'Dune 2021' or 'Severance S02'")
	}

	c, err := newClient()
	if err != nil {
		return SearchResult{}, err
	}

	switch {
	case req.Limit <= 0:
		req.Limit = defaultReleaseLimit
	case req.Limit > maxReleaseLimit:
		req.Limit = maxReleaseLimit
	}

	q := url.Values{"query": {query}, "type": {"search"}, "limit": {strconv.Itoa(maxReleaseLimit)}}
	kind := strings.ToLower(strings.TrimSpace(req.Kind))
	switch kind {
	case "", "any":
		kind = "any"
	case "movie", "movies", "film":
		kind = "movie"
		q.Add("categories", strconv.Itoa(categoryMovies))
	case "tv", "series", "show", "episode":
		kind = "tv"
		q.Add("categories", strconv.Itoa(categoryTV))
	default:
		return SearchResult{}, fmt.Errorf("'kind' is 'any', 'movie' or 'tv', not %q", req.Kind)
	}

	res := SearchResult{Query: query, Kind: kind, ByIndexer: map[string]int{}, Note: searchNote}

	if len(req.Indexers) > 0 {
		var raw []indexerJSON
		if err := c.get(ctx, "/indexer", nil, &raw); err != nil {
			return SearchResult{}, err
		}
		all := make([]Indexer, 0, len(raw))
		for _, r := range raw {
			all = append(all, r.toIndexer(nil, nil))
		}
		for _, in := range req.Indexers {
			ix, err := resolveIndexer(all, in)
			if err != nil {
				return SearchResult{}, err
			}
			q.Add("indexerIds", strconv.Itoa(ix.ID))
			res.Indexers = append(res.Indexers, ix.Name)
			if !ix.Enabled {
				res.Warnings = append(res.Warnings, fmt.Sprintf(
					"%s is disabled, so Prowlarr may skip it", ix.Name))
			}
		}
	}

	var raw []releaseJSON
	if err := c.do(ctx, http.MethodGet, "/search", q, nil, &raw, indexerTimeout); err != nil {
		return SearchResult{}, err
	}

	for _, r := range raw {
		rel := r.toRelease()
		res.ByIndexer[rel.Indexer]++
		res.Releases = append(res.Releases, rel)
	}
	res.TotalCount = len(res.Releases)

	slices.SortStableFunc(res.Releases, func(a, b Release) int {
		if d := seeders(b) - seeders(a); d != 0 {
			return d
		}
		return a.AgeHours - b.AgeHours
	})
	if len(res.Releases) > req.Limit {
		res.Releases = res.Releases[:req.Limit]
	}
	res.ShownCount = len(res.Releases)

	switch {
	case res.TotalCount == 0:
		res.Warnings = append(res.Warnings, fmt.Sprintf("no indexer returned anything for %q. "+
			"Indexers that are failing are skipped without a word — prowlarr_indexer_status shows "+
			"which; otherwise try the title the way a release would name it", query))
	default:
		if dead := deadTorrents(res.Releases); dead > 0 {
			res.Warnings = append(res.Warnings, fmt.Sprintf(
				"%d of the torrents shown have no seeders and would never finish", dead))
		}
	}

	return res, nil
}

func seeders(r Release) int {
	if r.Seeders == nil {
		return -1
	}
	return *r.Seeders
}

func deadTorrents(rs []Release) int {
	n := 0
	for _, r := range rs {
		if r.Protocol == "torrent" && r.Seeders != nil && *r.Seeders == 0 {
			n++
		}
	}
	return n
}

// --- wire types -----------------------------------------------------------

type releaseJSON struct {
	Title      string  `json:"title"`
	Indexer    string  `json:"indexer"`
	IndexerID  int     `json:"indexerId"`
	Protocol   string  `json:"protocol"`
	Size       int64   `json:"size"`
	Seeders    *int    `json:"seeders"`
	Leechers   *int    `json:"leechers"`
	Grabs      *int    `json:"grabs"`
	AgeHours   float64 `json:"ageHours"`
	InfoURL    string  `json:"infoUrl"`
	Categories []struct {
		Name string `json:"name"`
	} `json:"categories"`
}

// The download and magnet links are deliberately not carried: the download
// link embeds Prowlarr's own API key, and neither is any use to a model that
// is not going to grab anything.
func (r releaseJSON) toRelease() Release {
	rel := Release{
		Title:     r.Title,
		Indexer:   r.Indexer,
		IndexerID: r.IndexerID,
		Protocol:  r.Protocol,
		Seeders:   r.Seeders,
		Leechers:  r.Leechers,
		Grabs:     r.Grabs,
		AgeHours:  int(r.AgeHours),
		InfoURL:   r.InfoURL,
	}
	if r.Size > 0 {
		rel.SizeBytes = uint64(r.Size)
	}
	for _, cat := range r.Categories {
		if cat.Name != "" && !slices.Contains(rel.Categories, cat.Name) {
			rel.Categories = append(rel.Categories, cat.Name)
		}
	}
	return rel
}
