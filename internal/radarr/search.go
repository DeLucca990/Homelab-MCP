package radarr

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
)

// Searching a movie already in the library is its own operation, and without it
// the library has a one-way door: a download that is removed from the queue
// leaves the movie monitored and missing, and the only way back is to wait for
// Radarr's next scheduled search. Adding it again is not the answer — Radarr
// rejects that with "This movie has already been added", which is the correct
// response to the wrong request.

// The command Radarr runs for the Search button in its own UI.
const searchCommand = "MoviesSearch"

type SearchResult struct {
	MovieID int    `json:"movie_id"`
	Title   string `json:"title"`
	Year    int    `json:"year,omitempty"`

	CommandID     int    `json:"command_id" jsonschema:"Radarr's id for the search job it started"`
	CommandStatus string `json:"command_status,omitempty" jsonschema:"queued, started, completed or failed — a search that has only been queued has not found anything yet"`

	HadFile   bool `json:"had_file" jsonschema:"true when the movie was already downloaded, so this search is looking for an upgrade rather than for a missing film"`
	Monitored bool `json:"monitored"`

	Warnings []string `json:"warnings,omitempty"`
}

// GetMovie resolves one movie so an operation names the film it is about rather
// than a number — in the confirmation and in the result.
//
// It takes Radarr's own id, and falls back to reading the number as a TMDB id.
// The fallback is not politeness: a movie carries both, they are both plain
// integers, and nothing about the value says which space it belongs to, so the
// wrong one gets passed and the endpoint answers 404. Telling the caller off
// leaves a correct request unfulfilled over a naming detail.
//
// Which one matched is visible to the caller without another return value:
// movie.ID differs from what was asked for exactly when the fallback resolved
// it. The confirmation says so, because on a destructive operation the film is
// what the human should be checking, not the id.
func GetMovie(ctx context.Context, movieID int) (Movie, error) {
	if movieID <= 0 {
		return Movie{}, fmt.Errorf("a movie_id is required — radarr_library_status lists them")
	}

	c, err := newClient()
	if err != nil {
		return Movie{}, err
	}

	var record movieJSON
	byID := c.get(ctx, "/movie/"+strconv.Itoa(movieID), nil, &record)
	if byID == nil && record.ID != 0 {
		return record.toMovie(), nil
	}

	// Radarr filters its own listing by tmdbId, so this stays one request.
	var byTmdb []movieJSON
	err = c.get(ctx, "/movie", url.Values{"tmdbId": {strconv.Itoa(movieID)}}, &byTmdb)
	if err == nil && len(byTmdb) == 1 {
		return byTmdb[0].toMovie(), nil
	}

	return Movie{}, fmt.Errorf(
		"no movie in the library with Radarr id %d, and none with TMDB id %d either — "+
			"radarr_library_status lists what is there, with both numbers",
		movieID, movieID)
}

// Search asks Radarr to look for a release now. It returns as soon as the job
// is queued: the search itself runs in Radarr and whatever it grabs shows up in
// the download queue, which is where the outcome actually is.
func Search(ctx context.Context, movie Movie) (SearchResult, error) {
	c, err := newClient()
	if err != nil {
		return SearchResult{}, err
	}

	res := SearchResult{
		MovieID:   movie.ID,
		Title:     movie.Title,
		Year:      movie.Year,
		HadFile:   movie.HasFile,
		Monitored: movie.Monitored,
	}

	var command struct {
		ID     int    `json:"id"`
		Status string `json:"status"`
	}
	err = c.post(ctx, "/command", map[string]any{
		"name":     searchCommand,
		"movieIds": []int{movie.ID},
	}, &command)
	if err != nil {
		return res, err
	}

	res.CommandID = command.ID
	res.CommandStatus = command.Status

	if !movie.Monitored {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"%s is not monitored — Radarr may decline to grab anything for it; "+
				"monitor it first if the search finds nothing", movie.Title))
	}
	if movie.HasFile {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"%s is already downloaded, so this searches for an upgrade — "+
				"nothing will be grabbed unless a release beats the current file",
			movie.Title))
	}
	if !movie.IsAvailable {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"%s has not reached its minimum availability (%s) yet, so a search is "+
				"unlikely to find a legitimate release",
			movie.Title, movie.MinimumAvailability))
	}

	return res, nil
}
