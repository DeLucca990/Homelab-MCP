package sonarr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Manual import is the fix for a download that finished and was not imported:
// the files are on disk, the episodes are still missing, and Sonarr's reason
// is in the queue row. This is Sonarr's own Manual Import dialog — list what
// Sonarr makes of each file, import the ones wanted, sending back exactly the
// quality, languages and release group Sonarr parsed.
//
// A season pack is one download and many files, so the listing is per file
// and each file names the episodes Sonarr matched it to. A file matched to no
// episode cannot be imported from here: which episode it is, is exactly the
// judgement Sonarr could not make, and guessing it would put the wrong file
// behind an episode.

const importTTL = 30 * time.Minute

type ImportCandidate struct {
	ID string `json:"id" jsonschema:"what sonarr_import takes"`

	Path         string   `json:"path"`
	SizeBytes    uint64   `json:"size_bytes,omitempty"`
	SeriesID     int      `json:"series_id,omitempty"`
	Series       string   `json:"series,omitempty"`
	Episodes     string   `json:"episodes,omitempty" jsonschema:"the episodes Sonarr matched the file to, e.g. S02E03"`
	Quality      string   `json:"quality,omitempty"`
	Languages    []string `json:"languages,omitempty"`
	ReleaseGroup string   `json:"release_group,omitempty"`

	Rejections []string `json:"rejections,omitempty" jsonschema:"why Sonarr would not import it on its own"`
}

type ImportCandidates struct {
	Source     string            `json:"source"`
	Candidates []ImportCandidate `json:"candidates"`
	Warnings   []string          `json:"warnings,omitempty"`
}

type cachedImport struct {
	ImportCandidate
	episodeIDs []int
	file       map[string]any
	listed     time.Time
}

var (
	importMu    sync.Mutex
	importCache = map[string]cachedImport{}
)

// GetImportCandidates reads what Sonarr makes of a finished download, by
// queue id, or of any folder it can see.
func GetImportCandidates(ctx context.Context, queueID int, folder string) (ImportCandidates, error) {
	c, err := newClient()
	if err != nil {
		return ImportCandidates{}, err
	}

	q := url.Values{"filterExistingFiles": {"true"}}
	var out ImportCandidates
	switch {
	case queueID > 0 && strings.TrimSpace(folder) != "":
		return ImportCandidates{}, fmt.Errorf("pass 'queue_id' or 'folder', not both")
	case queueID > 0:
		item, _, err := FindQueueItem(ctx, queueID)
		if err != nil {
			return ImportCandidates{}, err
		}
		if item.DownloadID == "" {
			return ImportCandidates{}, fmt.Errorf("queue item %d has no download id — pass the "+
				"'folder' it is in", queueID)
		}
		q.Set("downloadId", item.DownloadID)
		out.Source = "queue item " + strconv.Itoa(queueID) + ": " + item.displayName()
	case strings.TrimSpace(folder) != "":
		q.Set("folder", strings.TrimSpace(folder))
		out.Source = strings.TrimSpace(folder)
	default:
		return ImportCandidates{}, fmt.Errorf("pass the 'queue_id' of a download stuck on import " +
			"(sonarr_queue_status lists them), or a 'folder' as Sonarr sees it")
	}

	var raw []json.RawMessage
	if err := c.do(ctx, http.MethodGet, "/manualimport", q, nil, &raw, releaseTimeout); err != nil {
		return ImportCandidates{}, err
	}

	unmatched := 0
	for _, r := range raw {
		var m manualImportJSON
		if err := json.Unmarshal(r, &m); err != nil {
			continue
		}
		var full map[string]any
		json.Unmarshal(r, &full)

		cand, episodeIDs := m.toCandidate()
		cand.ID = shortID("import", m.Path, strconv.FormatInt(m.Size, 10))
		if len(episodeIDs) == 0 {
			unmatched++
		}
		importMu.Lock()
		for id, old := range importCache {
			if time.Since(old.listed) > importTTL {
				delete(importCache, id)
			}
		}
		importCache[cand.ID] = cachedImport{ImportCandidate: cand, episodeIDs: episodeIDs, file: full, listed: time.Now()}
		importMu.Unlock()
		out.Candidates = append(out.Candidates, cand)
	}

	switch {
	case len(out.Candidates) == 0:
		out.Warnings = append(out.Warnings, "Sonarr found no video file there that is not already "+
			"in the library — the download may hold only an archive still to be extracted, or "+
			"the path is not one Sonarr's container can see")
	default:
		if unmatched > 0 {
			out.Warnings = append(out.Warnings, fmt.Sprintf("%d file(s) could not be matched to an "+
				"episode and cannot be imported from here — Sonarr's Manual Import dialog lets a "+
				"person pick the episode", unmatched))
		}
		out.Warnings = append(out.Warnings, "these ids stay valid for 30 minutes; sonarr_import takes them")
	}
	return out, nil
}

type ImportPlan struct {
	Files    []ImportCandidate `json:"files"`
	Warnings []string          `json:"warnings,omitempty"`

	body []map[string]any
}

// Key is what the fingerprint covers: every path and the episodes it goes to.
func (p ImportPlan) Key() string {
	parts := make([]string, 0, len(p.Files))
	for _, f := range p.Files {
		parts = append(parts, f.Path+"→"+f.Episodes)
	}
	return strings.Join(parts, "|")
}

type ImportResult struct {
	Plan          ImportPlan `json:"plan"`
	CommandID     int        `json:"command_id"`
	CommandStatus string     `json:"command_status,omitempty"`
	Warnings      []string   `json:"warnings,omitempty"`
}

// PlanImport resolves candidate ids.
func PlanImport(ids []string) (ImportPlan, error) {
	if len(ids) == 0 {
		return ImportPlan{}, fmt.Errorf("'ids' is required — sonarr_import_candidates lists them")
	}
	var p ImportPlan
	for _, id := range ids {
		importMu.Lock()
		c, ok := importCache[strings.ToLower(strings.TrimSpace(id))]
		importMu.Unlock()
		if !ok || time.Since(c.listed) > importTTL {
			return ImportPlan{}, fmt.Errorf("no import candidate %q is known — ids last 30 "+
				"minutes; run sonarr_import_candidates again", id)
		}
		if c.SeriesID == 0 || len(c.episodeIDs) == 0 {
			return ImportPlan{}, fmt.Errorf("%s was not matched to a series and episode, so it "+
				"cannot be imported from here", c.Path)
		}
		for _, why := range c.Rejections {
			p.Warnings = append(p.Warnings, fmt.Sprintf("%s: Sonarr's objection was %q — "+
				"importing overrides it", baseName(c.Path), why))
		}
		p.Files = append(p.Files, c.ImportCandidate)

		f := map[string]any{"path": c.file["path"], "seriesId": c.SeriesID, "episodeIds": c.episodeIDs}
		for _, k := range []string{"folderName", "quality", "languages", "releaseGroup", "downloadId",
			"indexerFlags", "releaseType"} {
			if v, ok := c.file[k]; ok && v != nil {
				f[k] = v
			}
		}
		p.body = append(p.body, f)
	}
	return p, nil
}

// Import runs Sonarr's ManualImport command for the planned files.
func Import(ctx context.Context, p ImportPlan) (ImportResult, error) {
	c, err := newClient()
	if err != nil {
		return ImportResult{}, err
	}
	var command struct {
		ID     int    `json:"id"`
		Status string `json:"status"`
	}
	body := map[string]any{"name": "ManualImport", "files": p.body, "importMode": "auto"}
	if err := c.post(ctx, "/command", body, &command); err != nil {
		return ImportResult{}, err
	}
	return ImportResult{Plan: p, CommandID: command.ID, CommandStatus: command.Status,
		Warnings: []string{"the import runs inside Sonarr — importMode auto moves a usenet " +
			"download and hardlinks or copies a torrent so it keeps seeding; " +
			"sonarr_library_status shows the episodes once it is done"}}, nil
}

func baseName(path string) string {
	if i := strings.LastIndexAny(path, `/\`); i >= 0 {
		return path[i+1:]
	}
	return path
}

// --- wire types -----------------------------------------------------------

type manualImportJSON struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	Series *struct {
		ID    int    `json:"id"`
		Title string `json:"title"`
	} `json:"series"`
	Episodes []struct {
		ID            int `json:"id"`
		SeasonNumber  int `json:"seasonNumber"`
		EpisodeNumber int `json:"episodeNumber"`
	} `json:"episodes"`
	Quality *struct {
		Quality struct {
			Name string `json:"name"`
		} `json:"quality"`
	} `json:"quality"`
	Languages []struct {
		Name string `json:"name"`
	} `json:"languages"`
	ReleaseGroup string `json:"releaseGroup"`
	Rejections   []struct {
		Reason string `json:"reason"`
	} `json:"rejections"`
}

func (m manualImportJSON) toCandidate() (ImportCandidate, []int) {
	c := ImportCandidate{Path: m.Path, ReleaseGroup: m.ReleaseGroup}
	if m.Size > 0 {
		c.SizeBytes = uint64(m.Size)
	}
	if m.Series != nil && m.Series.ID > 0 {
		c.SeriesID, c.Series = m.Series.ID, m.Series.Title
	}
	var ids []int
	var codes []string
	for _, e := range m.Episodes {
		ids = append(ids, e.ID)
		codes = append(codes, EpisodeCode(e.SeasonNumber, e.EpisodeNumber))
	}
	c.Episodes = strings.Join(codes, ", ")
	if m.Quality != nil {
		c.Quality = m.Quality.Quality.Name
	}
	for _, l := range m.Languages {
		c.Languages = append(c.Languages, l.Name)
	}
	for _, r := range m.Rejections {
		if r.Reason != "" {
			c.Rejections = append(c.Rejections, r.Reason)
		}
	}
	return c, ids
}
