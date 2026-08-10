package radarr

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// The queue is the one Radarr view where "it is working on it" and "it has been
// stuck for two days" look identical at a glance: both are a row with a title
// and a progress bar. The difference is whether the download client still
// reports a time remaining, so that is what this file makes explicit.

// How many queue rows to ask for. Radarr pages the queue at 10 by default,
// which would silently hide most of a busy queue.
const queuePageSize = 200

type QueueItem struct {
	ID      int    `json:"id" jsonschema:"the queue id, which is what radarr_queue_remove takes; it is assigned per queue refresh, so read it from a fresh radarr_queue_status rather than from memory"`
	MovieID int    `json:"movie_id,omitempty"`
	Movie   string `json:"movie,omitempty" jsonschema:"the movie this download is for; empty means the download client is holding something Radarr cannot match to a movie"`
	Year    int    `json:"year,omitempty"`

	Release string `json:"release,omitempty" jsonschema:"the release name the indexer published"`
	Quality string `json:"quality,omitempty"`

	Status        string `json:"status" jsonschema:"queued, downloading, paused, completed, failed, warning, delay or downloadClientUnavailable"`
	TrackedState  string `json:"tracked_state,omitempty" jsonschema:"what Radarr is doing with the download after it finishes: importing, importPending, importBlocked, imported, failed or ignored. importBlocked and importPending mean the file is on disk and NOT in the library"`
	TrackedStatus string `json:"tracked_status,omitempty" jsonschema:"ok, warning or error"`

	SizeBytes     uint64  `json:"size_bytes,omitempty"`
	SizeLeftBytes uint64  `json:"size_left_bytes,omitempty"`
	Percent       float64 `json:"percent" jsonschema:"how much of the release has arrived, 0-100"`

	TimeLeftSeconds uint64 `json:"time_left_seconds,omitempty" jsonschema:"the download client's own estimate; absent while bytes are still missing means the client is not moving and the download is stalled"`
	AddedSecondsAgo uint64 `json:"added_seconds_ago,omitempty" jsonschema:"how long this item has been in the queue; a large value next to a small percent is a download that is going nowhere"`

	Protocol       string `json:"protocol,omitempty" jsonschema:"usenet or torrent"`
	DownloadClient string `json:"download_client,omitempty"`
	Indexer        string `json:"indexer,omitempty"`

	Stalled bool `json:"stalled,omitempty" jsonschema:"true when the item is downloading, bytes are still missing, and the client reports no time remaining"`

	ErrorMessage string   `json:"error_message,omitempty"`
	Messages     []string `json:"messages,omitempty" jsonschema:"what Radarr recorded about this item, e.g. why an import is blocked"`
}

type Queue struct {
	Items []QueueItem `json:"items"`

	TotalCount       int `json:"total_count" jsonschema:"items Radarr reports in the queue"`
	DownloadingCount int `json:"downloading_count"`
	StalledCount     int `json:"stalled_count" jsonschema:"downloads that are not moving"`
	BlockedCount     int `json:"blocked_count" jsonschema:"downloads that finished but could not be imported — the file is on disk and the movie is still missing from the library"`

	Warnings []string `json:"warnings,omitempty"`
}

// GetQueue returns everything the download queue is working on, worst first.
// Unknown items — what the download client holds that Radarr cannot match to a
// movie — are included: they occupy the client and are exactly what someone
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
		"page":                     {"1"},
		"pageSize":                 {strconv.Itoa(queuePageSize)},
		"includeMovie":             {"true"},
		"includeUnknownMovieItems": {"true"},
	}
	if err := c.get(ctx, "/queue", query, &page); err != nil {
		return Queue{}, err
	}

	q := Queue{Items: make([]QueueItem, 0, len(page.Records)), TotalCount: page.TotalRecords}
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
		return strings.Compare(a.Movie, b.Movie)
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
func queueWarnings(items []QueueItem) []string {
	var out []string
	for _, i := range items {
		name := i.displayName()

		switch {
		case i.Status == "failed" || i.TrackedState == "failed":
			out = append(out, fmt.Sprintf(
				"%s failed to download%s — remove it from the queue to let Radarr try another release",
				name, reason(i)))

		case i.TrackedState == "importBlocked":
			out = append(out, fmt.Sprintf(
				"%s finished downloading but Radarr could not import it%s — "+
					"the file is on disk and the movie is still missing from the library",
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

// reason appends whatever Radarr recorded, if anything. The status alone says
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

// displayName prefers the movie, because that is what a person asked for. The
// release name is the fallback for items the download client holds and Radarr
// cannot match.
func (i QueueItem) displayName() string {
	switch {
	case i.Movie != "" && i.Year > 0:
		return fmt.Sprintf("%s (%d)", i.Movie, i.Year)
	case i.Movie != "":
		return i.Movie
	case i.Release != "":
		return i.Release
	default:
		return fmt.Sprintf("queue item %d", i.ID)
	}
}

// DisplayName is the queue item as a person would name it — used in the
// confirmation a human reads before a removal.
func (i QueueItem) DisplayName() string { return i.displayName() }

// FindQueueItem resolves a queue id against the queue as it is right now.
//
// It is called before a removal and again after the user approves it, so the
// fingerprint is computed from live state on both passes: a queue that changed
// in between produces a different fingerprint and the removal is refused rather
// than applied to whatever now holds that id.
func FindQueueItem(ctx context.Context, id int) (QueueItem, error) {
	q, err := GetQueue(ctx)
	if err != nil {
		return QueueItem{}, err
	}
	for _, i := range q.Items {
		if i.ID == id {
			return i, nil
		}
	}
	return QueueItem{}, fmt.Errorf(
		"no queue item with id %d — the queue holds %d items, and ids change every time "+
			"it is refreshed, so read a fresh radarr_queue_status and use the id from there",
		id, len(q.Items))
}

type RemoveOptions struct {
	QueueID int

	// RemoveFromClient deletes the download from the download client too.
	// Leaving it false removes the row from Radarr's queue while the client
	// keeps downloading, which is almost never what someone means.
	RemoveFromClient bool

	// Blocklist tells Radarr never to grab this release again.
	Blocklist bool

	// SkipRedownload stops Radarr searching for a replacement immediately.
	SkipRedownload bool
}

type RemoveResult struct {
	QueueID int    `json:"queue_id"`
	Movie   string `json:"movie,omitempty"`
	Release string `json:"release,omitempty"`

	RemovedFromClient bool `json:"removed_from_client"`
	Blocklisted       bool `json:"blocklisted" jsonschema:"true when this release was also blocked from being grabbed again"`
	SkipRedownload    bool `json:"skip_redownload"`

	StillQueued bool     `json:"still_queued,omitempty" jsonschema:"true when the item was still in the queue after the removal — it did not come off"`
	Warnings    []string `json:"warnings,omitempty"`
}

// RemoveFromQueue deletes one item from the queue and then checks that it
// actually left, rather than reporting success because the request returned 200.
func RemoveFromQueue(ctx context.Context, item QueueItem, opts RemoveOptions) (RemoveResult, error) {
	c, err := newClient()
	if err != nil {
		return RemoveResult{}, err
	}

	res := RemoveResult{
		QueueID:           item.ID,
		Movie:             item.Movie,
		Release:           item.Release,
		RemovedFromClient: opts.RemoveFromClient,
		Blocklisted:       opts.Blocklist,
		SkipRedownload:    opts.SkipRedownload,
	}

	query := url.Values{
		"removeFromClient": {strconv.FormatBool(opts.RemoveFromClient)},
		"blocklist":        {strconv.FormatBool(opts.Blocklist)},
		"skipRedownload":   {strconv.FormatBool(opts.SkipRedownload)},
	}
	if err := c.delete(ctx, "/queue/"+strconv.Itoa(item.ID), query); err != nil {
		return res, err
	}

	// Verify. Radarr answers the delete before the download client has
	// necessarily acted on it, and "removed" is the one thing worth being sure of.
	if _, err := FindQueueItem(ctx, item.ID); err == nil {
		res.StillQueued = true
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"%s is still in the queue after the removal — check the download client",
			item.displayName()))
	}

	switch {
	case opts.Blocklist && !opts.SkipRedownload:
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"%s was blocklisted, so Radarr will search for a different release of it",
			item.displayName()))

	// The dead end this leaves behind. A plain removal is not a blocklist, so
	// Radarr starts nothing: the movie goes back to monitored-and-missing and
	// stays there until a scheduled search comes around, which can be hours.
	case item.MovieID != 0:
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"nothing is looking for %s now — a removal is not a blocklist, so Radarr "+
				"started no replacement search. If this was its only copy the movie is "+
				"missing again; radarr_movie_search with movie_id %d starts a search now",
			item.displayName(), item.MovieID))
	}

	return res, nil
}

// --- wire types -----------------------------------------------------------

type queueRecordJSON struct {
	ID      int  `json:"id"`
	MovieID *int `json:"movieId"`
	Movie   *struct {
		Title string `json:"title"`
		Year  int    `json:"year"`
	} `json:"movie"`

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
		Indexer:         r.Indexer,
		ErrorMessage:    r.ErrorMessage,
		SizeBytes:       nonNegative(r.Size),
		SizeLeftBytes:   nonNegative(r.SizeLeft),
		TimeLeftSeconds: parseSpanSeconds(r.TimeLeft),
		AddedSecondsAgo: secondsSince(r.Added),
	}

	if r.MovieID != nil {
		i.MovieID = *r.MovieID
	}
	if r.Movie != nil {
		i.Movie, i.Year = r.Movie.Title, r.Movie.Year
	}
	if r.Quality != nil {
		i.Quality = r.Quality.Quality.Name
	}

	if i.SizeBytes > 0 {
		done := i.SizeBytes - min(i.SizeLeftBytes, i.SizeBytes)
		i.Percent = round1(float64(done) / float64(i.SizeBytes) * 100)
	}

	// The download client publishes a time remaining for anything that is
	// moving. Bytes still missing and no estimate is the signature of a torrent
	// with no seeds or a usenet download whose server dropped it.
	if i.Status == "downloading" && i.SizeLeftBytes > 0 &&
		i.TimeLeftSeconds == 0 && secondsUntil(r.EstimatedCompletionTime) == 0 {
		i.Stalled = true
	}

	for _, sm := range r.StatusMessages {
		for _, m := range sm.Messages {
			i.Messages = append(i.Messages, m)
		}
		if len(sm.Messages) == 0 && sm.Title != "" {
			i.Messages = append(i.Messages, sm.Title)
		}
	}

	return i
}

// Radarr sends sizes as JSON doubles; a negative one is meaningless here.
func nonNegative(v float64) uint64 {
	if v <= 0 {
		return 0
	}
	return uint64(v)
}

func round1(v float64) float64 { return float64(int(v*10+0.5)) / 10 }

// compactSeconds keeps a duration inside a warning sentence short.
func compactSeconds(s uint64) string {
	switch {
	case s == 0:
		return "an unknown time"
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 3600:
		return fmt.Sprintf("%dm", s/60)
	case s < 86400:
		return fmt.Sprintf("%dh", s/3600)
	default:
		return fmt.Sprintf("%dd", s/86400)
	}
}
