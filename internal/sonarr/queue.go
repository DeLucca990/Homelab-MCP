package sonarr

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// The queue is the one Sonarr view where "it is working on it" and "it has been
// stuck for two days" look identical at a glance: both are a row with a title
// and a progress bar. The difference is whether the download client still
// reports a time remaining, so that is what this file makes explicit.
//
// The Sonarr-specific part is that one download is not one row. A season pack
// arrives as one file and appears in the queue once per episode it contains,
// all sharing a downloadId — so removing "the row" removes every episode of
// that pack. That is stated before a removal rather than discovered after one.

// How many queue rows to ask for. Sonarr pages the queue at 10 by default,
// which would silently hide most of a busy queue — and a single season pack can
// occupy a dozen rows on its own.
const queuePageSize = 200

type QueueItem struct {
	ID       int    `json:"id" jsonschema:"the queue id, which is what sonarr_queue_remove takes; it is assigned per queue refresh, so read it from a fresh sonarr_queue_status rather than from memory"`
	SeriesID int    `json:"series_id,omitempty"`
	Series   string `json:"series,omitempty" jsonschema:"the series this download is for; empty means the download client is holding something Sonarr cannot match to a series"`

	Season       int    `json:"season,omitempty"`
	Episode      int    `json:"episode,omitempty"`
	EpisodeID    int    `json:"episode_id,omitempty"`
	EpisodeTitle string `json:"episode_title,omitempty"`

	Release string `json:"release,omitempty" jsonschema:"the release name the indexer published"`
	Quality string `json:"quality,omitempty"`

	Status        string `json:"status" jsonschema:"queued, downloading, paused, completed, failed, warning, delay or downloadClientUnavailable"`
	TrackedState  string `json:"tracked_state,omitempty" jsonschema:"what Sonarr is doing with the download after it finishes: importing, importPending, importBlocked, imported, failed or ignored. importBlocked and importPending mean the file is on disk and NOT in the library"`
	TrackedStatus string `json:"tracked_status,omitempty" jsonschema:"ok, warning or error"`

	SizeBytes     uint64  `json:"size_bytes,omitempty"`
	SizeLeftBytes uint64  `json:"size_left_bytes,omitempty"`
	Percent       float64 `json:"percent" jsonschema:"how much of the release has arrived, 0-100"`

	TimeLeftSeconds uint64 `json:"time_left_seconds,omitempty" jsonschema:"the download client's own estimate; absent while bytes are still missing means the client is not moving and the download is stalled"`
	AddedSecondsAgo uint64 `json:"added_seconds_ago,omitempty" jsonschema:"how long this item has been in the queue; a large value next to a small percent is a download that is going nowhere"`

	Protocol       string `json:"protocol,omitempty" jsonschema:"usenet or torrent"`
	DownloadClient string `json:"download_client,omitempty"`
	DownloadID     string `json:"download_id,omitempty" jsonschema:"the download client's own id; queue rows that share one are the same file, e.g. every episode of a season pack"`
	Indexer        string `json:"indexer,omitempty"`

	Stalled bool `json:"stalled,omitempty" jsonschema:"true when the item is downloading, bytes are still missing, and the client reports no time remaining"`

	ErrorMessage string   `json:"error_message,omitempty"`
	Messages     []string `json:"messages,omitempty" jsonschema:"what Sonarr recorded about this item, e.g. why an import is blocked"`
}

type Queue struct {
	Items []QueueItem `json:"items"`

	TotalCount       int `json:"total_count" jsonschema:"rows Sonarr reports in the queue; a season pack counts once per episode it holds"`
	DownloadCount    int `json:"download_count" jsonschema:"distinct downloads behind those rows"`
	DownloadingCount int `json:"downloading_count"`
	StalledCount     int `json:"stalled_count" jsonschema:"downloads that are not moving"`
	BlockedCount     int `json:"blocked_count" jsonschema:"downloads that finished but could not be imported — the file is on disk and the episode is still missing from the library"`

	Warnings []string `json:"warnings,omitempty"`
}

// GetQueue returns everything the download queue is working on, worst first.
// Unknown items — what the download client holds that Sonarr cannot match to a
// series — are included: they occupy the client and are exactly what someone
// would want to clear out.
func GetQueue(ctx context.Context) (Queue, error) {
	c, err := newClient()
	if err != nil {
		return Queue{}, err
	}

	var page struct {
		TotalRecords int               `json:"totalRecords"`
		Records      []queueRecordJSON `json:"records"`
	}
	query := url.Values{
		"page":                      {"1"},
		"pageSize":                  {strconv.Itoa(queuePageSize)},
		"includeSeries":             {"true"},
		"includeEpisode":            {"true"},
		"includeUnknownSeriesItems": {"true"},
	}
	if err := c.get(ctx, "/queue", query, &page); err != nil {
		return Queue{}, err
	}

	q := Queue{Items: make([]QueueItem, 0, len(page.Records)), TotalCount: page.TotalRecords}
	downloads := map[string]bool{}

	for _, r := range page.Records {
		item := r.toItem()

		switch {
		case item.TrackedState == "importBlocked" || item.TrackedState == "importPending":
			q.BlockedCount++
		case item.Stalled:
			q.StalledCount++
		case item.Status == "downloading":
			q.DownloadingCount++
		}

		// A season pack is one download and many rows. Counting rows as
		// downloads would report a queue of fourteen things when there is one.
		key := item.DownloadID
		if key == "" {
			key = "row:" + strconv.Itoa(item.ID)
		}
		if !downloads[key] {
			downloads[key] = true
			q.DownloadCount++
		}

		q.Items = append(q.Items, item)
	}

	// Worst first, then closest to finished: whatever needs a human is read
	// before whatever is merely in progress.
	slices.SortFunc(q.Items, func(a, b QueueItem) int {
		if d := queueSeverity(a) - queueSeverity(b); d != 0 {
			return d
		}
		if d := b.Percent - a.Percent; d != 0 {
			if d < 0 {
				return -1
			}
			return 1
		}
		if d := strings.Compare(a.Series, b.Series); d != 0 {
			return d
		}
		if d := a.Season - b.Season; d != 0 {
			return d
		}
		return a.Episode - b.Episode
	})

	q.Warnings = queueWarnings(q.Items)

	return q, nil
}

// queueSeverity ranks items for display order. Lower sorts first.
func queueSeverity(i QueueItem) int {
	switch {
	case i.Status == "failed", i.TrackedState == "failed", i.TrackedStatus == "error":
		return 0
	case i.TrackedState == "importBlocked", i.TrackedState == "importPending":
		return 1
	case i.Status == "downloadClientUnavailable":
		return 2
	case i.Status == "warning", i.TrackedStatus == "warning":
		return 3
	case i.Stalled:
		return 4
	case i.Status == "paused":
		return 5
	case i.Status == "downloading":
		return 6
	case i.Status == "queued", i.Status == "delay":
		return 7
	default:
		return 8
	}
}

// Built here rather than in the renderer so a client reading only
// structuredContent sees them too.
//
// One warning per download rather than per row: a season pack that failed is
// one problem, and repeating it once per episode would bury everything else.
func queueWarnings(items []QueueItem) []string {
	var out []string
	seen := map[string]bool{}

	for _, i := range items {
		key := i.DownloadID + "|" + strconv.Itoa(queueSeverity(i))
		if i.DownloadID != "" {
			if seen[key] {
				continue
			}
			seen[key] = true
		}

		name := i.downloadName()

		switch {
		case i.Status == "failed" || i.TrackedState == "failed":
			out = append(out, fmt.Sprintf(
				"%s failed to download%s — remove it from the queue to let Sonarr try another release",
				name, reason(i)))

		case i.TrackedState == "importBlocked":
			out = append(out, fmt.Sprintf(
				"%s finished downloading but Sonarr could not import it%s — "+
					"the file is on disk and the episode is still missing from the library",
				name, reason(i)))

		case i.TrackedState == "importPending":
			out = append(out, fmt.Sprintf(
				"%s is waiting to be imported%s", name, reason(i)))

		case i.Status == "downloadClientUnavailable":
			out = append(out, fmt.Sprintf(
				"%s cannot be reached in its download client%s — the client is probably down",
				name, reason(i)))

		case i.Stalled:
			out = append(out, fmt.Sprintf(
				"%s is stalled at %.0f%% — it has been queued for %s and the download client "+
					"reports no time remaining, so nothing is arriving",
				name, i.Percent, compactSeconds(i.AddedSecondsAgo)))

		case i.Status == "warning" || i.TrackedStatus == "warning":
			out = append(out, fmt.Sprintf("%s needs attention%s", name, reason(i)))
		}
	}
	return out
}

// reason appends whatever Sonarr recorded, if anything. The status alone says
// something is wrong; the message says what.
func reason(i QueueItem) string {
	switch {
	case i.ErrorMessage != "":
		return ": " + i.ErrorMessage
	case len(i.Messages) > 0:
		return ": " + strings.Join(i.Messages, "; ")
	default:
		return ""
	}
}

// EpisodeCode is the S03E05 a person would say out loud. Season 0 is Sonarr's
// specials, which is where a code with a zero in it is a real answer rather
// than a missing value.
func (i QueueItem) EpisodeCode() string {
	if i.Series == "" || i.Episode == 0 {
		return ""
	}
	return fmt.Sprintf("S%02dE%02d", i.Season, i.Episode)
}

// displayName prefers the episode, because that is what a person asked for. The
// release name is the fallback for items the download client holds and Sonarr
// cannot match.
func (i QueueItem) displayName() string {
	switch {
	case i.Series != "" && i.EpisodeCode() != "":
		name := i.Series + " " + i.EpisodeCode()
		if i.EpisodeTitle != "" {
			name += " — " + i.EpisodeTitle
		}
		return name
	case i.Series != "":
		return i.Series
	case i.Release != "":
		return i.Release
	default:
		return "queue item " + strconv.Itoa(i.ID)
	}
}

// DisplayName is the queue item as a person would name it — used in the
// confirmation a human reads before a removal.
func (i QueueItem) DisplayName() string { return i.displayName() }

// downloadName names the file rather than the episode, for a message that is
// about the whole download: a season pack's rows differ only by episode, and
// "The Expanse S03E05" would be an arbitrary one of fourteen.
func (i QueueItem) downloadName() string {
	if i.Release != "" {
		return i.Release
	}
	return i.displayName()
}

// FindQueueItem resolves a queue id against the queue as it is right now.
//
// It is called before a removal and again after the user approves it, so the
// fingerprint is computed from live state on both passes: a queue that changed
// in between produces a different fingerprint and the removal is refused rather
// than applied to whatever now holds that id.
func FindQueueItem(ctx context.Context, id int) (QueueItem, []QueueItem, error) {
	q, err := GetQueue(ctx)
	if err != nil {
		return QueueItem{}, nil, err
	}

	for _, i := range q.Items {
		if i.ID != id {
			continue
		}

		// Every other row of the same download. Removing one removes them all,
		// which is a fact about the operation, not a detail of the display.
		var siblings []QueueItem
		if i.DownloadID != "" {
			for _, other := range q.Items {
				if other.ID != id && other.DownloadID == i.DownloadID {
					siblings = append(siblings, other)
				}
			}
		}
		return i, siblings, nil
	}

	return QueueItem{}, nil, fmt.Errorf(
		"no queue item with id %d — the queue holds %d rows, and ids change every time "+
			"it is refreshed, so read a fresh sonarr_queue_status and use the id from there",
		id, len(q.Items))
}

type RemoveOptions struct {
	QueueID int

	RemoveFromClient bool

	Blocklist bool

	SkipRedownload bool
}

type RemoveResult struct {
	QueueID int    `json:"queue_id"`
	Series  string `json:"series,omitempty"`
	Episode string `json:"episode,omitempty" jsonschema:"the episode the removed row was for, e.g. S03E05"`
	Release string `json:"release,omitempty"`

	RemovedFromClient bool `json:"removed_from_client"`
	Blocklisted       bool `json:"blocklisted" jsonschema:"true when this release was also blocked from being grabbed again"`
	SkipRedownload    bool `json:"skip_redownload"`

	EpisodesAffected int `json:"episodes_affected" jsonschema:"how many queue rows this removal took out — more than one when the download was a season pack"`

	StillQueued bool     `json:"still_queued,omitempty" jsonschema:"true when the item was still in the queue after the removal — it did not come off"`
	Warnings    []string `json:"warnings,omitempty"`
}

// RemoveFromQueue deletes one item from the queue and then checks that it
// actually left, rather than reporting success because the request returned 200.
func RemoveFromQueue(
	ctx context.Context,
	item QueueItem,
	siblings []QueueItem,
	opts RemoveOptions,
) (RemoveResult, error) {
	c, err := newClient()
	if err != nil {
		return RemoveResult{}, err
	}

	res := RemoveResult{
		QueueID:           item.ID,
		Series:            item.Series,
		Episode:           item.EpisodeCode(),
		Release:           item.Release,
		RemovedFromClient: opts.RemoveFromClient,
		Blocklisted:       opts.Blocklist,
		SkipRedownload:    opts.SkipRedownload,
		EpisodesAffected:  1 + len(siblings),
	}

	query := url.Values{
		"removeFromClient": {strconv.FormatBool(opts.RemoveFromClient)},
		"blocklist":        {strconv.FormatBool(opts.Blocklist)},
		"skipRedownload":   {strconv.FormatBool(opts.SkipRedownload)},
	}
	if err := c.delete(ctx, "/queue/"+strconv.Itoa(item.ID), query); err != nil {
		return res, err
	}

	// Verify. Sonarr answers the delete before the download client has
	// necessarily acted on it, and "removed" is the one thing worth being sure of.
	if _, _, err := FindQueueItem(ctx, item.ID); err == nil {
		res.StillQueued = true
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"%s is still in the queue after the removal — check the download client",
			item.displayName()))
	}

	if len(siblings) > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"that download held %d episodes, so all %d queue rows for it are gone",
			res.EpisodesAffected, res.EpisodesAffected))
	}

	switch {
	case opts.Blocklist && !opts.SkipRedownload:
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"%s was blocklisted, so Sonarr will search for a different release of it",
			item.downloadName()))

	// The dead end this leaves behind. A plain removal is not a blocklist, so
	// Sonarr starts nothing: the episodes go back to monitored-and-missing and
	// stay there until a scheduled search comes around, which can be hours.
	case item.SeriesID != 0:
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"nothing is looking for %s now — a removal is not a blocklist, so Sonarr "+
				"started no replacement search. Those episodes are missing again; "+
				"sonarr_series_search with series_id %d starts a search now",
			item.displayName(), item.SeriesID))
	}

	return res, nil
}

// --- wire types -----------------------------------------------------------

type queueRecordJSON struct {
	ID       int  `json:"id"`
	SeriesID *int `json:"seriesId"`
	Series   *struct {
		Title string `json:"title"`
		Year  int    `json:"year"`
	} `json:"series"`

	EpisodeID    *int `json:"episodeId"`
	SeasonNumber *int `json:"seasonNumber"`
	Episode      *struct {
		Title         string `json:"title"`
		SeasonNumber  int    `json:"seasonNumber"`
		EpisodeNumber int    `json:"episodeNumber"`
	} `json:"episode"`

	Title   string `json:"title"`
	Quality *struct {
		Quality struct {
			Name string `json:"name"`
		} `json:"quality"`
	} `json:"quality"`

	Size     float64 `json:"size"`
	SizeLeft float64 `json:"sizeleft"`
	TimeLeft string  `json:"timeleft"`
	Added    string  `json:"added"`

	EstimatedCompletionTime string `json:"estimatedCompletionTime"`

	Status                string `json:"status"`
	TrackedDownloadStatus string `json:"trackedDownloadStatus"`
	TrackedDownloadState  string `json:"trackedDownloadState"`

	Protocol       string `json:"protocol"`
	DownloadClient string `json:"downloadClient"`
	DownloadID     string `json:"downloadId"`
	Indexer        string `json:"indexer"`

	ErrorMessage   string `json:"errorMessage"`
	StatusMessages []struct {
		Title    string   `json:"title"`
		Messages []string `json:"messages"`
	} `json:"statusMessages"`
}

func (r queueRecordJSON) toItem() QueueItem {
	i := QueueItem{
		ID:              r.ID,
		Release:         r.Title,
		Status:          r.Status,
		TrackedState:    r.TrackedDownloadState,
		TrackedStatus:   r.TrackedDownloadStatus,
		Protocol:        r.Protocol,
		DownloadClient:  r.DownloadClient,
		DownloadID:      r.DownloadID,
		Indexer:         r.Indexer,
		ErrorMessage:    r.ErrorMessage,
		SizeBytes:       nonNegative(r.Size),
		SizeLeftBytes:   nonNegative(r.SizeLeft),
		TimeLeftSeconds: parseSpanSeconds(r.TimeLeft),
		AddedSecondsAgo: secondsSince(r.Added),
	}

	if r.SeriesID != nil {
		i.SeriesID = *r.SeriesID
	}
	if r.Series != nil {
		i.Series = r.Series.Title
	}
	if r.EpisodeID != nil {
		i.EpisodeID = *r.EpisodeID
	}
	if r.SeasonNumber != nil {
		i.Season = *r.SeasonNumber
	}
	if r.Episode != nil {
		i.Episode = r.Episode.EpisodeNumber
		i.EpisodeTitle = r.Episode.Title
		if r.SeasonNumber == nil {
			i.Season = r.Episode.SeasonNumber
		}
	}
	if r.Quality != nil {
		i.Quality = r.Quality.Quality.Name
	}

	if i.SizeBytes > 0 {
		done := i.SizeBytes - min(i.SizeLeftBytes, i.SizeBytes)
		i.Percent = round1(float64(done) / float64(i.SizeBytes) * 100)
	}

	if i.Status == "downloading" && i.SizeLeftBytes > 0 &&
		i.TimeLeftSeconds == 0 && secondsUntil(r.EstimatedCompletionTime) == 0 {
		i.Stalled = true
	}

	for _, sm := range r.StatusMessages {
		i.Messages = append(i.Messages, sm.Messages...)
		if len(sm.Messages) == 0 && sm.Title != "" {
			i.Messages = append(i.Messages, sm.Title)
		}
	}

	return i
}
