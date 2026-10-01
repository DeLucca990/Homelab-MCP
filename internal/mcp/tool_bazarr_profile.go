package mcp

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DeLucca990/homelab-mcp/internal/bazarr"
)

// BAZARR LANGUAGE PROFILE TOOL
//
// Points a movie or a whole series at a language profile — the setting that
// decides which subtitles Bazarr keeps looking for. It is how "from now on I
// want Portuguese and English for this show" becomes true, where a one-off
// search only fetches something once. It downloads and deletes nothing: it
// changes what Bazarr wants, and the confirmation says so.

type bazarrProfileInput struct {
	RadarrID int `json:"radarr_id,omitempty" jsonschema:"a movie, by Radarr's own id"`
	SeriesID int `json:"series_id,omitempty" jsonschema:"a whole series, by Sonarr's own id — the profile applies to every episode"`

	Profile string `json:"profile" jsonschema:"the language profile, by name or id — homelab://bazarr/language-profiles lists them with their languages. 'none' stops Bazarr wanting any subtitle for this"`
}

func handleBazarrProfile(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in bazarrProfileInput,
) (*sdk.CallToolResult, bazarr.ProfileChange, error) {
	t := bazarr.Target{RadarrID: in.RadarrID, SeriesID: in.SeriesID}
	if err := t.Validate(); err != nil {
		return nil, bazarr.ProfileChange{}, fmt.Errorf(
			"a profile belongs to a movie ('radarr_id') or a whole series ('series_id'): %w", err)
	}

	profile, none, err := bazarr.ResolveProfile(ctx, in.Profile)
	if err != nil {
		return nil, bazarr.ProfileChange{}, err
	}

	// Resolved on both passes, so the user approves a title rather than a
	// number and the fingerprint covers what the item has now.
	var (
		kind, title, current string
		id                   int
		movie                bazarr.Movie
		series               bazarr.Series
	)
	if in.RadarrID > 0 {
		movie, err = bazarr.GetMovie(ctx, in.RadarrID)
		kind, id, title, current = "movie", movie.RadarrID, movieTitle(movie.Title, movie.Year), movie.ProfileName
	} else {
		series, err = bazarr.GetSeries(ctx, in.SeriesID)
		kind, id, title, current = "series", series.SeriesID, movieTitle(series.Title, series.Year), series.ProfileName
	}
	if err != nil {
		return nil, bazarr.ProfileChange{}, err
	}

	target := "none"
	if !none {
		target = profile.Name
	}
	if strings.EqualFold(blankAs(current, "none"), target) {
		return nil, bazarr.ProfileChange{}, fmt.Errorf(
			"%s already has the %q language profile — nothing to change", title, target)
	}

	approved, pending, err := requireApproval(req, approval{
		message: bazarrProfileConfirmation(kind, title, current, profile, none, series),
		fingerprint: fingerprint(
			"bazarr_language_profile_set",
			kind,
			strconv.Itoa(id),
			title,
			blankAs(current, "none"),
			target,
			strconv.Itoa(profile.ID),
		),
		refusal: "no language profile was changed",
		subject: fmt.Sprintf("set the bazarr language profile of %s to %s", title, target),
	})
	if !approved {
		return pending, bazarr.ProfileChange{}, err
	}

	var out bazarr.ProfileChange
	if kind == "movie" {
		out, err = bazarr.SetMovieProfile(ctx, movie, profile, none)
	} else {
		out, err = bazarr.SetSeriesProfile(ctx, series, profile, none)
	}
	if err != nil {
		return nil, bazarr.ProfileChange{}, err
	}
	out.Title = title

	return &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.TextContent{Text: renderBazarrProfileChange(out)},
		},
	}, out, nil
}

func bazarrProfileConfirmation(
	kind, title, current string,
	p bazarr.Profile,
	none bool,
	s bazarr.Series,
) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Change which subtitles Bazarr wants for this %s?\n\n", kind)
	fmt.Fprintf(&b, "    %s\n", title)
	fmt.Fprintf(&b, "    from: %s\n", blankAs(current, "none"))
	if none {
		b.WriteString("    to:   none\n")
		b.WriteString("\nBazarr will stop looking for subtitles for it. Subtitles already on disk stay.\n")
		return b.String()
	}
	fmt.Fprintf(&b, "    to:   %s (%s)\n", p.Name, p.Describe())

	if kind == "series" {
		fmt.Fprintf(&b, "\nThis applies to every episode — %d episode file(s) on disk now.",
			s.EpisodeFileCount)
	}
	b.WriteString("\nNothing is downloaded or deleted by this; it changes what Bazarr searches " +
		"for from now on.\n")
	return b.String()
}

func renderBazarrProfileChange(c bazarr.ProfileChange) string {
	var b strings.Builder

	fmt.Fprintf(&b, "%s now has language profile %s (was %s)\n", c.Title, c.To, c.From)
	if c.Kind == "movie" {
		fmt.Fprintf(&b, "missing against the new profile: %s\n", languageCodes(c.MissingAfter))
	}

	for _, w := range c.Warnings {
		fmt.Fprintf(&b, "\nwarning: %s\n", w)
	}

	return b.String()
}
