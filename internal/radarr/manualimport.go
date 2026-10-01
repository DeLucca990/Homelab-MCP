package radarr

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

// Manual import is the fix for the queue state this module already calls out
// and until now could not act on: a download that finished and was not
// imported. The file is on disk, the movie is still missing, and Radarr's
// reason is in the queue row — "Unable to determine if file is a sample",
// "Not an upgrade for existing movie file", "Movie title mismatch". Looking at
// what Radarr makes of the files and importing them anyway is what its own
// Manual Import dialog does, and this is that dialog.
//
// Each file is listed with its own short id. The import sends back exactly
// what Radarr parsed for it — quality, languages, release group — so the
// movie gets the same metadata the dialog would have given it.

const importTTL = 30 * time.Minute

// ImportCandidate is one file Radarr could import.
type ImportCandidate struct {
	ID string `json:"id" jsonschema:"what radarr_import takes"`

	Path         string   `json:"path"`
	SizeBytes    uint64   `json:"size_bytes,omitempty"`
	MovieID      int      `json:"movie_id,omitempty" jsonschema:"the movie Radarr matched the file to; absent means it could not tell, and radarr_import needs a movie_id"`
	Movie        string   `json:"movie,omitempty"`
	Quality      string   `json:"quality,omitempty"`
	Languages    []string `json:"languages,omitempty"`
	ReleaseGroup string   `json:"release_group,omitempty"`

	Rejections []string `json:"rejections,omitempty" jsonschema:"why Radarr would not import it on its own"`
}

type ImportCandidates struct {
	Source     string            `json:"source" jsonschema:"the queue item or folder that was read"`
	Candidates []ImportCandidate `json:"candidates"`
	Warnings   []string          `json:"warnings,omitempty"`
}

type cachedImport struct {
	ImportCandidate
	file   map[string]any
	listed time.Time
}

var (
	importMu    sync.Mutex
	importCache = map[string]cachedImport{}
)

// GetImportCandidates reads what Radarr makes of a finished download, by queue
// id, or of any folder it can see.
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
		item, err := FindQueueItem(ctx, queueID)
		if err != nil {
			return ImportCandidates{}, err
		}
		if item.DownloadID == "" {
			return ImportCandidates{}, fmt.Errorf("queue item %d has no download id — it is not "+
				"a download Radarr is tracking, so pass the 'folder' it is in", queueID)
		}
		q.Set("downloadId", item.DownloadID)
		out.Source = "queue item " + strconv.Itoa(queueID) + ": " + item.displayName()
	case strings.TrimSpace(folder) != "":
		q.Set("folder", strings.TrimSpace(folder))
		out.Source = strings.TrimSpace(folder)
	default:
		return ImportCandidates{}, fmt.Errorf("pass the 'queue_id' of a download stuck on import " +
			"(radarr_queue_status lists them), or a 'folder' as Radarr sees it")
	}

	var raw []json.RawMessage
	if err := c.do(ctx, http.MethodGet, "/manualimport", q, nil, &raw, releaseTimeout); err != nil {
		return ImportCandidates{}, err
	}

	for _, r := range raw {
		var m manualImportJSON
		if err := json.Unmarshal(r, &m); err != nil {
			continue
		}
		var full map[string]any
		json.Unmarshal(r, &full)

		cand := m.toCandidate()
		cand.ID = shortID("import", m.Path, strconv.FormatInt(m.Size, 10))
		importMu.Lock()
		for id, old := range importCache {
			if time.Since(old.listed) > importTTL {
				delete(importCache, id)
			}
		}
		importCache[cand.ID] = cachedImport{ImportCandidate: cand, file: full, listed: time.Now()}
		importMu.Unlock()
		out.Candidates = append(out.Candidates, cand)
	}

	switch {
	case len(out.Candidates) == 0:
		out.Warnings = append(out.Warnings, "Radarr found no video file there that is not already "+
			"in the library — the download may hold only an archive still to be extracted, or "+
			"the path is not one Radarr's container can see")
	default:
		for _, cand := range out.Candidates {
			if cand.MovieID == 0 {
				out.Warnings = append(out.Warnings, fmt.Sprintf("%s could not be matched to a "+
					"movie — radarr_import needs a 'movie_id' for it", cand.Path))
			}
		}
		out.Warnings = append(out.Warnings, "these ids stay valid for 30 minutes; radarr_import takes them")
	}
	return out, nil
}

type ImportPlan struct {
	Files    []ImportCandidate `json:"files"`
	Warnings []string          `json:"warnings,omitempty"`

	body []map[string]any
}

// Key is what the fingerprint covers: every path and the movie each goes to.
func (p ImportPlan) Key() string {
	parts := make([]string, 0, len(p.Files))
	for _, f := range p.Files {
		parts = append(parts, f.Path+"→"+strconv.Itoa(f.MovieID))
	}
	return strings.Join(parts, "|")
}

type ImportResult struct {
	Plan          ImportPlan `json:"plan"`
	CommandID     int        `json:"command_id"`
	CommandStatus string     `json:"command_status,omitempty"`
	Warnings      []string   `json:"warnings,omitempty"`
}

// PlanImport resolves candidate ids, optionally pointing them all at one movie.
func PlanImport(ctx context.Context, ids []string, movieID int) (ImportPlan, error) {
	if len(ids) == 0 {
		return ImportPlan{}, fmt.Errorf("'ids' is required — radarr_import_candidates lists them")
	}

	var movie *Movie
	if movieID > 0 {
		m, err := GetMovie(ctx, movieID)
		if err != nil {
			return ImportPlan{}, err
		}
		movie = &m
	}

	var p ImportPlan
	for _, id := range ids {
		importMu.Lock()
		c, ok := importCache[strings.ToLower(strings.TrimSpace(id))]
		importMu.Unlock()
		if !ok || time.Since(c.listed) > importTTL {
			return ImportPlan{}, fmt.Errorf("no import candidate %q is known — ids last 30 "+
				"minutes; run radarr_import_candidates again", id)
		}
		f := c.ImportCandidate
		if movie != nil {
			f.MovieID, f.Movie = movie.ID, movie.Title
		}
		if f.MovieID == 0 {
			return ImportPlan{}, fmt.Errorf("%s was not matched to a movie — pass 'movie_id'", f.Path)
		}
		for _, why := range f.Rejections {
			p.Warnings = append(p.Warnings, fmt.Sprintf("%s: Radarr's objection was %q — "+
				"importing overrides it", baseName(f.Path), why))
		}
		p.Files = append(p.Files, f)
		p.body = append(p.body, importFile(c.file, f.MovieID))
	}
	if movie != nil && movie.HasFile {
		p.Warnings = append(p.Warnings, fmt.Sprintf("%s already has a file (%s); importing "+
			"replaces it", movie.Title, blank(movie.Quality)))
	}
	return p, nil
}

// Import runs Radarr's ManualImport command for the planned files.
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
		Warnings: []string{"the import runs inside Radarr — importMode auto moves a usenet " +
			"download and hardlinks or copies a torrent so it keeps seeding; radarr_library_status " +
			"shows the movie with its file once it is done"}}, nil
}

// importFile builds the command's file entry from what Radarr itself parsed,
// so quality, languages and release group are Radarr's own reading.
func importFile(parsed map[string]any, movieID int) map[string]any {
	f := map[string]any{"path": parsed["path"], "movieId": movieID}
	for _, k := range []string{"folderName", "quality", "languages", "releaseGroup", "downloadId", "indexerFlags"} {
		if v, ok := parsed[k]; ok && v != nil {
			f[k] = v
		}
	}
	return f
}

func baseName(path string) string {
	if i := strings.LastIndexAny(path, `/\`); i >= 0 {
		return path[i+1:]
	}
	return path
}

// --- wire types -----------------------------------------------------------

type manualImportJSON struct {
	Path  string `json:"path"`
	Size  int64  `json:"size"`
	Movie *struct {
		ID    int    `json:"id"`
		Title string `json:"title"`
		Year  int    `json:"year"`
	} `json:"movie"`
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

func (m manualImportJSON) toCandidate() ImportCandidate {
	c := ImportCandidate{Path: m.Path, ReleaseGroup: m.ReleaseGroup}
	if m.Size > 0 {
		c.SizeBytes = uint64(m.Size)
	}
	if m.Movie != nil && m.Movie.ID > 0 {
		c.MovieID = m.Movie.ID
		c.Movie = m.Movie.Title
		if m.Movie.Year > 0 {
			c.Movie = fmt.Sprintf("%s (%d)", m.Movie.Title, m.Movie.Year)
		}
	}
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
	return c
}
