package mcp

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DeLucca990/homelab-mcp/internal/radarr"
	"github.com/DeLucca990/homelab-mcp/internal/system"
)

// RADARR LIBRARY REMOVAL TOOL
//
// The one operation in this server that destroys something no amount of asking
// Radarr again brings back. It gets the same confirmation as the rest, but the
// message is written to be read rather than skimmed: it names the file size
// about to disappear, and it states which of the two things is happening —
// forgetting the movie, or erasing it.
//
// delete_files defaults to TRUE here, unlike Radarr's own API: removing a movie
// from a homelab library normally means reclaiming the disk, and an entry that
// disappears while tens of gigabytes stay behind is its own kind of surprise.
// The cost is that the destructive reading is the default one, which is why the
// confirmation states the size in capitals and why passing false is offered in
// the same breath.

type movieRemoveInput struct {
	MovieID int `json:"movie_id" jsonschema:"the movie to remove, from radarr_library_status. Radarr's own 'id' field is what this expects; a TMDB id is accepted too and resolved, and the confirmation says which film it landed on"`

	DeleteFiles        *bool `json:"delete_files,omitempty" jsonschema:"also erase the downloaded files from disk; defaults to TRUE. Pass false to remove the movie from Radarr's library while leaving the files where they are — deletion cannot be undone"`
	AddImportExclusion *bool `json:"add_import_exclusion,omitempty" jsonschema:"also stop any import list from adding this movie back automatically; defaults to false"`
}

func handleRadarrMovieRemove(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in movieRemoveInput,
) (*sdk.CallToolResult, radarr.DeleteResult, error) {
	if in.MovieID <= 0 {
		return nil, radarr.DeleteResult{}, fmt.Errorf(
			"'movie_id' is required — radarr_library_status lists the movies with their ids")
	}

	opts := radarr.DeleteOptions{
		MovieID:            in.MovieID,
		DeleteFiles:        boolOr(in.DeleteFiles, true),
		AddImportExclusion: boolOr(in.AddImportExclusion, false),
	}

	movie, err := radarr.GetMovie(ctx, in.MovieID)
	if err != nil {
		return nil, radarr.DeleteResult{}, err
	}

	approved, pending, err := requireApproval(req, approval{
		message: movieRemoveMessage(movie, opts, in.MovieID),
		fingerprint: fingerprint(
			"radarr_movie_remove",
			strconv.Itoa(movie.ID),
			movie.Title,
			strconv.Itoa(movie.Year),
			movie.Path,
			strconv.FormatBool(opts.DeleteFiles),
			strconv.FormatBool(opts.AddImportExclusion),
		),
		refusal: "movie not removed",
		subject: fmt.Sprintf("remove %s (%d) from the radarr library, delete_files=%t",
			movie.Title, movie.Year, opts.DeleteFiles),
	})
	if !approved {
		return pending, radarr.DeleteResult{}, err
	}

	out, err := radarr.Delete(ctx, movie, opts)
	if err != nil {
		return nil, radarr.DeleteResult{}, err
	}

	return &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.TextContent{Text: renderMovieRemoveResult(out)},
		},
	}, out, nil
}

func movieRemoveMessage(m radarr.Movie, opts radarr.DeleteOptions, requestedID int) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Remove this movie from the Radarr library?\n\n")
	fmt.Fprintf(&b, "    %s (%d)   [movie %d]\n", m.Title, m.Year, m.ID)
	if requestedID != m.ID {
		fmt.Fprintf(&b, "    (you gave %d, which is its TMDB id — Radarr's id for it is %d)\n",
			requestedID, m.ID)
	}
	fmt.Fprintf(&b, "    state: %s\n", movieStateCell(m))
	if m.Path != "" {
		fmt.Fprintf(&b, "    folder: %s\n", m.Path)
	}

	switch {
	case opts.DeleteFiles && m.HasFile:
		fmt.Fprintf(&b, "\nTHE DOWNLOADED FILES WILL BE DELETED — %s of %s freed. "+
			"This cannot be undone, and getting the movie back means downloading it again.\n",
			system.CompactBytes(m.SizeBytes), blank(m.Quality))
	case opts.DeleteFiles:
		b.WriteString("\nFile deletion was requested, but this movie has no files on disk, " +
			"so only the library entry goes away.\n")
	case m.HasFile:
		fmt.Fprintf(&b, "\nThe files stay on disk (%s in the folder above) and Radarr stops "+
			"tracking them. Only the library entry goes away.\n", system.CompactBytes(m.SizeBytes))
	default:
		b.WriteString("\nThe movie has no files, so only the library entry goes away.\n")
	}

	if opts.AddImportExclusion {
		b.WriteString("\nIt will also be EXCLUDED from import lists, so nothing adds it back " +
			"automatically.\n")
	}

	return b.String()
}

func renderMovieRemoveResult(r radarr.DeleteResult) string {
	var b strings.Builder

	fmt.Fprintf(&b, "removed %s (%d) from the radarr library\n", r.Title, r.Year)
	fmt.Fprintf(&b, "files deleted: %s", yesNo(r.FilesDeleted))
	if r.FilesDeleted && r.FreedBytes > 0 {
		fmt.Fprintf(&b, " (%s freed)", system.CompactBytes(r.FreedBytes))
	}
	fmt.Fprintf(&b, "   import exclusion: %s\n", yesNo(r.ImportExclusionAdded))

	if r.StillPresent {
		b.WriteString("\nit is still showing in the library\n")
	}

	for _, w := range r.Warnings {
		fmt.Fprintf(&b, "\nwarning: %s\n", w)
	}

	return b.String()
}
