package mcp

import (
	"context"
	"fmt"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DeLucca990/homelab-mcp/internal/bazarr"
)

// BAZARR SUBTITLE STATUS TOOL
//
// Which subtitles one movie, episode or series has, which its language profile
// says it should have, and which profile that is. It is the first call of
// every subtitle question, because the answer to "why has this no Portuguese
// subtitles" is as often "nothing asked for Portuguese" as "nothing found it".
// Read-only.

type bazarrStatusInput struct {
	RadarrID  int    `json:"radarr_id,omitempty" jsonschema:"a movie, by Radarr's own id — the 'id' radarr_library_status returns, not the TMDB id"`
	SeriesID  int    `json:"series_id,omitempty" jsonschema:"a whole series, by Sonarr's own id — lists its episodes that lack a subtitle, with their episode ids"`
	EpisodeID int    `json:"episode_id,omitempty" jsonschema:"one episode, by Sonarr's episode id"`
	Term      string `json:"term,omitempty" jsonschema:"find movies and series by title instead, when no id is known; returns their ids and profiles"`
	Limit     int    `json:"limit,omitempty" jsonschema:"how many episodes or matches to list; default 25, maximum 200"`
}

func handleBazarrStatus(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in bazarrStatusInput,
) (*sdk.CallToolResult, bazarr.Status, error) {
	st, err := bazarr.GetStatus(ctx, bazarr.Target{
		RadarrID:  in.RadarrID,
		SeriesID:  in.SeriesID,
		EpisodeID: in.EpisodeID,
	}, in.Term, in.Limit)
	if err != nil {
		return nil, bazarr.Status{}, err
	}
	return &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.TextContent{Text: renderBazarrStatus(st)},
		},
	}, st, nil
}

func renderBazarrStatus(st bazarr.Status) string {
	var b strings.Builder

	switch {
	case st.Movie != nil:
		m := st.Movie
		fmt.Fprintf(&b, "%s   [radarr_id %d]\n", movieTitle(m.Title, m.Year), m.RadarrID)
		fmt.Fprintf(&b, "language profile: %s\n", blankAs(m.ProfileName, profileIDCell(m.ProfileID)))
		writeSubtitleBlock(&b, m.AudioLanguages, m.Subtitles, m.Missing, m.SceneName)

	case st.Episode != nil:
		e := st.Episode
		title := e.Label()
		profile := "unknown"
		if st.Series != nil {
			title = st.Series.Title + " " + title
			profile = blankAs(st.Series.ProfileName, profileIDCell(st.Series.ProfileID))
		}
		if e.Title != "" {
			title += " — " + e.Title
		}
		fmt.Fprintf(&b, "%s   [episode_id %d, series_id %d]\n", title, e.EpisodeID, e.SeriesID)
		fmt.Fprintf(&b, "language profile (the series'): %s\n", profile)
		writeSubtitleBlock(&b, e.AudioLanguages, e.Subtitles, e.Missing, e.SceneName)

	case st.Series != nil:
		s := st.Series
		fmt.Fprintf(&b, "%s   [series_id %d]\n", movieTitle(s.Title, s.Year), s.SeriesID)
		fmt.Fprintf(&b, "language profile: %s\n", blankAs(s.ProfileName, profileIDCell(s.ProfileID)))
		fmt.Fprintf(&b, "%d episode file(s), %d lacking a subtitle the profile asks for\n",
			s.EpisodeFileCount, s.EpisodesMissingSubtitles)

		if len(st.Episodes) > 0 {
			b.WriteString("\n")
			cols := []column{
				{"EPISODE", alignLeft},
				{"ID", alignRight},
				{"HAS", alignLeft},
				{"MISSING", alignLeft},
			}
			rows := make([][]string, 0, len(st.Episodes))
			for _, e := range st.Episodes {
				rows = append(rows, []string{
					e.Label(),
					fmt.Sprintf("%d", e.EpisodeID),
					subtitleCodes(e.Subtitles),
					languageCodes(e.Missing),
				})
			}
			b.WriteString(table(cols, rows))
		}

	default:
		writeMatches(&b, st)
	}

	for _, w := range st.Warnings {
		fmt.Fprintf(&b, "\nwarning: %s\n", w)
	}

	return b.String()
}

func writeSubtitleBlock(
	b *strings.Builder,
	audio []string,
	subs []bazarr.Subtitle,
	missing []bazarr.SubtitleLanguage,
	sceneName string,
) {
	if len(audio) > 0 {
		fmt.Fprintf(b, "audio: %s\n", strings.Join(audio, ", "))
	}
	if sceneName != "" {
		fmt.Fprintf(b, "release: %s\n", sceneName)
	}

	if len(subs) == 0 {
		b.WriteString("\nno subtitles at all\n")
	} else {
		b.WriteString("\n")
		cols := []column{
			{"SUBTITLE", alignLeft},
			{"CODE", alignLeft},
			{"WHERE", alignLeft},
		}
		rows := make([][]string, 0, len(subs))
		for _, s := range subs {
			where := s.Path
			if s.Embedded {
				where = "embedded in the video"
			}
			rows = append(rows, []string{s.String(), s.Code2, where})
		}
		b.WriteString(table(cols, rows))
	}

	if len(missing) == 0 {
		b.WriteString("\nmissing: nothing the profile asks for\n")
	} else {
		fmt.Fprintf(b, "\nmissing: %s\n", languageNames(missing))
	}
}

func writeMatches(b *strings.Builder, st bazarr.Status) {
	if len(st.MatchedMovies) > 0 {
		cols := []column{
			{"MOVIE", alignLeft},
			{"RADARR_ID", alignRight},
			{"PROFILE", alignLeft},
			{"HAS", alignLeft},
			{"MISSING", alignLeft},
		}
		rows := make([][]string, 0, len(st.MatchedMovies))
		for _, m := range st.MatchedMovies {
			rows = append(rows, []string{
				movieTitle(m.Title, m.Year),
				fmt.Sprintf("%d", m.RadarrID),
				blankAs(m.ProfileName, profileIDCell(m.ProfileID)),
				subtitleCodes(m.Subtitles),
				languageCodes(m.Missing),
			})
		}
		b.WriteString(table(cols, rows))
	}

	if len(st.MatchedSeries) > 0 {
		if len(st.MatchedMovies) > 0 {
			b.WriteString("\n")
		}
		cols := []column{
			{"SERIES", alignLeft},
			{"SERIES_ID", alignRight},
			{"PROFILE", alignLeft},
			{"FILES", alignRight},
			{"LACKING", alignRight},
		}
		rows := make([][]string, 0, len(st.MatchedSeries))
		for _, s := range st.MatchedSeries {
			rows = append(rows, []string{
				movieTitle(s.Title, s.Year),
				fmt.Sprintf("%d", s.SeriesID),
				blankAs(s.ProfileName, profileIDCell(s.ProfileID)),
				fmt.Sprintf("%d", s.EpisodeFileCount),
				fmt.Sprintf("%d", s.EpisodesMissingSubtitles),
			})
		}
		b.WriteString(table(cols, rows))
		b.WriteString("\nLACKING counts episodes missing a subtitle the profile asks for\n")
	}
}

// --- cells shared by the bazarr tools -----------------------------------------

func movieTitle(title, year string) string {
	if year == "" || year == "0" {
		return title
	}
	return fmt.Sprintf("%s (%s)", title, year)
}

// A profile id with no name is either "none" or a profile that could not be
// read; the second is rare enough to show as its number.
func profileIDCell(id int) string {
	if id == 0 {
		return "NONE"
	}
	return fmt.Sprintf("#%d", id)
}

func blankAs(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func languageCodes(ls []bazarr.SubtitleLanguage) string {
	if len(ls) == 0 {
		return "-"
	}
	codes := make([]string, 0, len(ls))
	for _, l := range ls {
		codes = append(codes, languageCode(l))
	}
	return strings.Join(codes, " ")
}

func subtitleCodes(subs []bazarr.Subtitle) string {
	if len(subs) == 0 {
		return "-"
	}
	codes := make([]string, 0, len(subs))
	for _, s := range subs {
		c := languageCode(s.SubtitleLanguage)
		if s.Embedded {
			c += "*"
		}
		codes = append(codes, c)
	}
	return strings.Join(codes, " ")
}

func languageCode(l bazarr.SubtitleLanguage) string {
	switch {
	case l.Forced:
		return l.Code2 + ":forced"
	case l.HI:
		return l.Code2 + ":hi"
	}
	return l.Code2
}

func languageNames(ls []bazarr.SubtitleLanguage) string {
	names := make([]string, 0, len(ls))
	for _, l := range ls {
		names = append(names, fmt.Sprintf("%s [%s]", l.String(), l.Code2))
	}
	return strings.Join(names, ", ")
}
