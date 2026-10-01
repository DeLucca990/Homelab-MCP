package jellyfin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// The library side of "Radarr says it imported it and I cannot find it": is
// the item in Jellyfin at all, and if not, make Jellyfin look.
//
// A scan is not free — a full one walks every file of every library and can
// run for an hour on a large collection, with the disk busy throughout — so
// the scan names its scope, and the narrowest one that answers the question
// is the one to use: one item, then one library, then everything.

const (
	defaultItemLimit = 20
	maxItemLimit     = 100
)

// Item is one thing in the library.
type Item struct {
	ID     string `json:"id" jsonschema:"what jellyfin_library_scan and jellyfin_mark_played take"`
	Name   string `json:"name" jsonschema:"as a person would say it: a film with its year, an episode with its code"`
	Type   string `json:"type" jsonschema:"Movie, Series, Season or Episode"`
	Path   string `json:"path,omitempty" jsonschema:"the file or folder on disk, as Jellyfin sees it"`
	Year   int    `json:"year,omitempty"`
	Played *bool  `json:"played,omitempty" jsonschema:"for the user asked about, when one was"`

	AddedSecondsAgo uint64 `json:"added_seconds_ago,omitempty"`
}

type ItemSearch struct {
	Term       string `json:"term"`
	User       string `json:"user,omitempty"`
	Items      []Item `json:"items"`
	TotalCount int    `json:"total_count"`

	Warnings []string `json:"warnings,omitempty"`
}

// SearchItems finds movies, series and episodes by name.
func SearchItems(ctx context.Context, term, user string, limit int) (ItemSearch, error) {
	term = strings.TrimSpace(term)
	if term == "" {
		return ItemSearch{}, fmt.Errorf("a 'term' is required — part of the title")
	}
	switch {
	case limit <= 0:
		limit = defaultItemLimit
	case limit > maxItemLimit:
		limit = maxItemLimit
	}

	c, err := newClient()
	if err != nil {
		return ItemSearch{}, err
	}

	q := url.Values{
		"searchTerm":             {term},
		"recursive":              {"true"},
		"includeItemTypes":       {"Movie,Series,Episode"},
		"fields":                 {"Path,DateCreated"},
		"limit":                  {strconv.Itoa(limit)},
		"enableTotalRecordCount": {"true"},
	}
	out := ItemSearch{Term: term}
	if strings.TrimSpace(user) != "" {
		u, _, err := c.resolveUser(ctx, user)
		if err != nil {
			return ItemSearch{}, err
		}
		q.Set("userId", u.ID)
		out.User = u.Name
	}

	var raw struct {
		Items            []itemJSON `json:"Items"`
		TotalRecordCount int        `json:"TotalRecordCount"`
	}
	if err := c.get(ctx, "/Items", q, &raw); err != nil {
		return ItemSearch{}, err
	}
	for _, r := range raw.Items {
		out.Items = append(out.Items, r.toItem(out.User != ""))
	}
	out.TotalCount = raw.TotalRecordCount

	switch {
	case len(out.Items) == 0:
		out.Warnings = append(out.Warnings, fmt.Sprintf("nothing in Jellyfin matches %q. If Radarr "+
			"or Sonarr imported it, the file is on disk and the library has not caught up — "+
			"jellyfin_library_scan on its library makes Jellyfin look", term))
	case out.TotalCount > len(out.Items):
		out.Warnings = append(out.Warnings, fmt.Sprintf("%d match and %d are shown — narrow 'term'",
			out.TotalCount, len(out.Items)))
	}
	return out, nil
}

// --- scanning -------------------------------------------------------------------

type ScanPlan struct {
	Scope   string   `json:"scope" jsonschema:"all, library or item"`
	Library *Library `json:"library,omitempty"`
	Item    *Item    `json:"item,omitempty"`

	AlreadyRunning bool `json:"already_running,omitempty"`

	Warnings []string `json:"warnings,omitempty"`
}

// Target names what is being scanned, for the confirmation and the log.
func (p ScanPlan) Target() string {
	switch {
	case p.Item != nil:
		return p.Item.Name
	case p.Library != nil:
		return "the " + p.Library.Name + " library"
	default:
		return "every library"
	}
}

// PlanScan resolves what a scan covers without starting it.
func PlanScan(ctx context.Context, library, itemID string) (ScanPlan, error) {
	if strings.TrimSpace(library) != "" && strings.TrimSpace(itemID) != "" {
		return ScanPlan{}, fmt.Errorf("pass 'library' or 'item_id', not both")
	}
	c, err := newClient()
	if err != nil {
		return ScanPlan{}, err
	}

	libs, err := c.libraries(ctx)
	if err != nil {
		return ScanPlan{}, err
	}

	switch {
	case strings.TrimSpace(itemID) != "":
		var r itemJSON
		q := url.Values{"fields": {"Path,DateCreated"}}
		if err := c.get(ctx, "/Items/"+url.PathEscape(strings.TrimSpace(itemID)), q, &r); err != nil {
			if errors.Is(err, ErrNotFound) || strings.Contains(err.Error(), "400") {
				return ScanPlan{}, fmt.Errorf("jellyfin has no item %q — jellyfin_find_item finds one", itemID)
			}
			return ScanPlan{}, err
		}
		item := r.toItem(false)
		return ScanPlan{Scope: "item", Item: &item}, nil

	case strings.TrimSpace(library) != "":
		l, err := resolveLibrary(libs, library)
		if err != nil {
			return ScanPlan{}, err
		}
		p := ScanPlan{Scope: "library", Library: &l, AlreadyRunning: l.Refreshing}
		if l.Refreshing {
			p.Warnings = append(p.Warnings, "a scan of this library is already running")
		}
		return p, nil
	}

	p := ScanPlan{Scope: "all"}
	for _, l := range libs {
		if l.Refreshing {
			p.AlreadyRunning = true
		}
	}
	if p.AlreadyRunning {
		p.Warnings = append(p.Warnings, "a library scan is already running; another is queued behind it")
	}
	p.Warnings = append(p.Warnings, fmt.Sprintf("a full scan walks every file of %d libraries "+
		"and can take a long time on a large collection — a single library or item is faster "+
		"when the question is about one", len(libs)))
	return p, nil
}

// Scan starts a planned scan. It returns as soon as Jellyfin has queued it.
func Scan(ctx context.Context, p ScanPlan) (ScanPlan, error) {
	c, err := newClient()
	if err != nil {
		return p, err
	}

	// The same parameters Jellyfin's own "Scan library" and "Refresh metadata"
	// buttons send: look for new and changed files and fill in what is missing,
	// without replacing metadata or images someone has edited.
	refresh := url.Values{
		"Recursive":           {"true"},
		"metadataRefreshMode": {"Default"},
		"imageRefreshMode":    {"Default"},
		"replaceAllMetadata":  {"false"},
		"replaceAllImages":    {"false"},
	}

	switch p.Scope {
	case "item":
		err = c.send(ctx, http.MethodPost, "/Items/"+url.PathEscape(p.Item.ID)+"/Refresh", refresh, nil)
	case "library":
		err = c.send(ctx, http.MethodPost, "/Items/"+url.PathEscape(p.Library.ID)+"/Refresh", refresh, nil)
	default:
		err = c.send(ctx, http.MethodPost, "/Library/Refresh", nil, nil)
	}
	if err != nil {
		return p, err
	}
	p.Warnings = []string{"the scan runs in the background; jellyfin_find_item shows new items " +
		"as they arrive, and jellyfin_system_health reports the scan task if it fails"}
	return p, nil
}

// --- played state ------------------------------------------------------------------

type PlayedPlan struct {
	User   string `json:"user"`
	UserID string `json:"user_id"`
	Item   Item   `json:"item"`
	Played bool   `json:"played"`

	Warnings []string `json:"warnings,omitempty"`
}

// PlanPlayed resolves a watched/unwatched change without making it.
func PlanPlayed(ctx context.Context, user, itemID string, played bool) (PlayedPlan, error) {
	c, err := newClient()
	if err != nil {
		return PlayedPlan{}, err
	}
	u, _, err := c.resolveUser(ctx, user)
	if err != nil {
		return PlayedPlan{}, err
	}
	if strings.TrimSpace(itemID) == "" {
		return PlayedPlan{}, fmt.Errorf("an 'item_id' is required — jellyfin_find_item finds one")
	}

	var r itemJSON
	q := url.Values{"userId": {u.ID}, "fields": {"Path"}}
	if err := c.get(ctx, "/Items/"+url.PathEscape(strings.TrimSpace(itemID)), q, &r); err != nil {
		return PlayedPlan{}, fmt.Errorf("jellyfin has no item %q for %s: %w", itemID, u.Name, err)
	}
	item := r.toItem(true)
	if item.Played != nil && *item.Played == played {
		return PlayedPlan{}, fmt.Errorf("%s is already marked %s for %s", item.Name, playedWord(played), u.Name)
	}

	p := PlayedPlan{User: u.Name, UserID: u.ID, Item: item, Played: played}
	switch item.Type {
	case "Series", "Season":
		p.Warnings = append(p.Warnings, fmt.Sprintf("this marks every episode of %s %s for %s",
			item.Name, playedWord(played), u.Name))
	}
	if !played {
		p.Warnings = append(p.Warnings, "marking unwatched also clears the resume position")
	}
	return p, nil
}

// SetPlayed applies a planned watched/unwatched change.
func SetPlayed(ctx context.Context, p PlayedPlan) (PlayedPlan, error) {
	c, err := newClient()
	if err != nil {
		return p, err
	}
	method := http.MethodPost
	if !p.Played {
		method = http.MethodDelete
	}
	id := url.PathEscape(p.Item.ID)

	err = c.send(ctx, method, "/UserPlayedItems/"+id, url.Values{"userId": {p.UserID}}, nil)
	if errors.Is(err, ErrNotFound) {
		// Servers before 10.9 only have the route with the user in the path.
		err = c.send(ctx, method, "/Users/"+p.UserID+"/PlayedItems/"+id, nil, nil)
	}
	return p, err
}

func playedWord(played bool) string {
	if played {
		return "watched"
	}
	return "unwatched"
}

// --- wire types -----------------------------------------------------------

type itemJSON struct {
	ID                string `json:"Id"`
	Name              string `json:"Name"`
	Type              string `json:"Type"`
	Path              string `json:"Path"`
	SeriesName        string `json:"SeriesName"`
	IndexNumber       *int   `json:"IndexNumber"`
	ParentIndexNumber *int   `json:"ParentIndexNumber"`
	ProductionYear    *int   `json:"ProductionYear"`
	DateCreated       string `json:"DateCreated"`

	UserData *struct {
		Played bool `json:"Played"`
	} `json:"UserData"`
}

func (r itemJSON) toItem(withUser bool) Item {
	it := Item{
		ID:              r.ID,
		Name:            itemName(r.SeriesName, r.Name, r.ParentIndexNumber, r.IndexNumber, r.ProductionYear),
		Type:            r.Type,
		Path:            r.Path,
		AddedSecondsAgo: secondsSince(r.DateCreated),
	}
	if r.ProductionYear != nil {
		it.Year = *r.ProductionYear
	}
	if withUser && r.UserData != nil {
		played := r.UserData.Played
		it.Played = &played
	}
	return it
}
