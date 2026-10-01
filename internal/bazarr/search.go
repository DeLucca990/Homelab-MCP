package bazarr

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// An automatic search is the Search button of Bazarr's own UI: ask every
// enabled provider, score what comes back, and keep the best subtitle that
// clears the minimum score. It either finds one or it finds nothing it trusts
// — Bazarr never downloads a poor match just to have something.
//
// Two shapes, because the two questions people ask are different:
//
//   - one language ("get Portuguese subtitles for Dune") — sent as that
//     language, whether or not the profile asks for it;
//   - everything missing ("get the subtitles this is missing") — whatever the
//     language profile wants and the disk does not have.
//
// Recent Bazarr versions queue the search as a background job and answer
// before it has run; older ones search inside the request. The result reads
// the item back afterwards either way, so "found" is only ever said when the
// subtitle is on disk.

// SearchRequest is what the caller asked for.
type SearchRequest struct {
	Target
	Language string // optional; empty means every missing language
	Forced   bool
	HI       bool
}

// SearchPlan is the resolved operation — what the confirmation shows and what
// the fingerprint covers.
type SearchPlan struct {
	Kind     string `json:"kind" jsonschema:"movie, episode or series"`
	RadarrID int    `json:"radarr_id,omitempty"`
	SeriesID int    `json:"series_id,omitempty"`

	EpisodeID int    `json:"episode_id,omitempty"`
	Title     string `json:"title"`

	Languages []SubtitleLanguage `json:"languages,omitempty" jsonschema:"what will be searched for; empty for a whole series, where it is each episode's own missing languages"`
	Replaces  []SubtitleLanguage `json:"replaces,omitempty" jsonschema:"requested languages that already have a subtitle on disk, which a better match would replace"`

	EpisodesMissing int `json:"episodes_missing,omitempty" jsonschema:"for a series: episodes lacking something their profile asks for"`

	OutsideProfile bool `json:"outside_profile,omitempty" jsonschema:"the language is not in this item's profile; Bazarr will fetch it, but will not keep it up to date"`

	Warnings []string `json:"warnings,omitempty"`

	// one-language form, as Bazarr's form fields want it
	oneLanguage bool
}

// SearchResult is what came of it.
type SearchResult struct {
	Plan SearchPlan `json:"plan"`

	Found   []SubtitleLanguage `json:"found,omitempty" jsonschema:"requested languages that now have a subtitle on disk"`
	Pending []SubtitleLanguage `json:"pending,omitempty" jsonschema:"requested languages still without one — either the search is still running in Bazarr's job queue or it found nothing above the minimum score"`

	EpisodesMissingAfter int `json:"episodes_missing_after,omitempty"`

	Warnings []string `json:"warnings,omitempty"`
}

// PlanSearch resolves a search without starting it.
func PlanSearch(ctx context.Context, req SearchRequest) (SearchPlan, error) {
	if err := req.Validate(); err != nil {
		return SearchPlan{}, err
	}

	c, err := newClient()
	if err != nil {
		return SearchPlan{}, err
	}

	var lang *Language
	if req.Language != "" {
		known, err := c.languages(ctx)
		if err != nil {
			return SearchPlan{}, err
		}
		l, err := resolveLanguage(known, req.Language)
		if err != nil {
			return SearchPlan{}, err
		}
		lang = &l
	}
	if req.Forced && req.HI {
		return SearchPlan{}, fmt.Errorf("a subtitle is either forced or hearing-impaired, not both")
	}
	if (req.Forced || req.HI) && lang == nil {
		return SearchPlan{}, fmt.Errorf("'forced' and 'hi' qualify a 'language' — name the language too")
	}

	switch {
	case req.SeriesID > 0:
		if lang != nil {
			return SearchPlan{}, fmt.Errorf("a whole series is searched for what each episode is " +
				"missing, not for one language — for one language pass the 'episode_id' of each " +
				"episode, or give the series a language profile that asks for it " +
				"(bazarr_language_profile_set) and search again")
		}
		s, err := c.series(ctx, req.SeriesID)
		if err != nil {
			return SearchPlan{}, err
		}
		c.nameProfiles(ctx, nil, &s)
		p := SearchPlan{Kind: "series", SeriesID: s.SeriesID, Title: s.Title,
			EpisodesMissing: s.EpisodesMissingSubtitles}
		if s.EpisodesMissingSubtitles == 0 {
			return SearchPlan{}, fmt.Errorf("no episode of %s is missing a subtitle its profile "+
				"(%s) asks for, so there is nothing to search for", s.Title, nonEmpty(s.ProfileName, "none"))
		}
		return p, nil

	case req.RadarrID > 0:
		m, err := c.movie(ctx, req.RadarrID)
		if err != nil {
			return SearchPlan{}, err
		}
		c.nameProfiles(ctx, &m, nil)
		p := SearchPlan{Kind: "movie", RadarrID: m.RadarrID, Title: movieLabel(m)}
		return p.withLanguages(lang, req, m.Missing, m.Subtitles, m.ProfileName)

	default:
		e, err := c.episode(ctx, req.EpisodeID)
		if err != nil {
			return SearchPlan{}, err
		}
		title := e.Label()
		profileName := ""
		if s, err := c.series(ctx, e.SeriesID); err == nil {
			c.nameProfiles(ctx, nil, &s)
			title = s.Title + " " + e.Label()
			profileName = s.ProfileName
		}
		p := SearchPlan{Kind: "episode", SeriesID: e.SeriesID, EpisodeID: e.EpisodeID, Title: title}
		return p.withLanguages(lang, req, e.Missing, e.Subtitles, profileName)
	}
}

func (p SearchPlan) withLanguages(
	lang *Language,
	req SearchRequest,
	missing []SubtitleLanguage,
	have []Subtitle,
	profileName string,
) (SearchPlan, error) {
	if lang == nil {
		if len(missing) == 0 {
			return SearchPlan{}, fmt.Errorf("%s is missing nothing its language profile (%s) asks "+
				"for — pass 'language' to fetch a specific one anyway", p.Title, nonEmpty(profileName, "none"))
		}
		p.Languages = missing
		return p, nil
	}

	want := SubtitleLanguage{Code2: lang.Code2, Name: lang.Name, Forced: req.Forced, HI: req.HI}
	p.Languages = []SubtitleLanguage{want}
	p.oneLanguage = true

	for _, s := range have {
		if want.same(s.SubtitleLanguage) {
			p.Replaces = append(p.Replaces, s.SubtitleLanguage)
			if s.Embedded {
				p.Warnings = append(p.Warnings, fmt.Sprintf(
					"%s already has %s as a track inside the video file; a downloaded one is added "+
						"next to it and players usually prefer the external file", p.Title, want))
			} else {
				p.Warnings = append(p.Warnings, fmt.Sprintf(
					"%s already has a %s subtitle on disk; a better match replaces it", p.Title, want))
			}
			break
		}
	}

	wanted := slices.ContainsFunc(missing, func(m SubtitleLanguage) bool { return m.Code2 == want.Code2 })
	if !wanted && len(p.Replaces) == 0 {
		p.OutsideProfile = true
		p.Warnings = append(p.Warnings, fmt.Sprintf(
			"%s is not in the language profile of %s (%s) — Bazarr will fetch it this once, but "+
				"will not look for it again or upgrade it; bazarr_language_profile_set is how to "+
				"make it permanent", want, p.Title, nonEmpty(profileName, "none")))
	}
	if !lang.Enabled {
		p.Warnings = append(p.Warnings, fmt.Sprintf(
			"%s is not enabled in Bazarr's language settings, so some providers may not be "+
				"asked for it", lang.Name))
	}

	return p, nil
}

// Search runs a planned search, then reads the item back to say what arrived.
func Search(ctx context.Context, p SearchPlan) (SearchResult, error) {
	c, err := newClient()
	if err != nil {
		return SearchResult{}, err
	}

	res := SearchResult{Plan: p}

	switch p.Kind {
	case "series":
		form := url.Values{"seriesid": {strconv.Itoa(p.SeriesID)}, "action": {"search-missing"}}
		if err := c.send(ctx, "PATCH", "/series", form, providerTimeout); err != nil {
			return res, err
		}
		if s, err := c.series(ctx, p.SeriesID); err == nil {
			res.EpisodesMissingAfter = s.EpisodesMissingSubtitles
		}
		res.Warnings = append(res.Warnings, searchedSeriesNote(p, res.EpisodesMissingAfter))
		return res, nil

	case "movie":
		if p.oneLanguage {
			l := p.Languages[0]
			form := url.Values{
				"radarrid": {strconv.Itoa(p.RadarrID)},
				"language": {l.Code2}, "forced": {pyBool(l.Forced)}, "hi": {pyBool(l.HI)},
			}
			if err := c.send(ctx, "PATCH", "/movies/subtitles", form, providerTimeout); err != nil {
				return res, err
			}
		} else {
			form := url.Values{"radarrid": {strconv.Itoa(p.RadarrID)}, "action": {"search-missing"}}
			if err := c.send(ctx, "PATCH", "/movies", form, providerTimeout); err != nil {
				return res, err
			}
		}
		after, err := c.movie(ctx, p.RadarrID)
		if err != nil {
			res.Warnings = append(res.Warnings, "the search was started but the movie could not "+
				"be read back: "+err.Error())
			return res, nil
		}
		res.Found, res.Pending = arrived(p, after.Subtitles)

	default:
		// Bazarr has no "everything missing" action for a single episode, so
		// that form is one request per language, in the order they are owed.
		for _, l := range p.Languages {
			form := url.Values{
				"seriesid": {strconv.Itoa(p.SeriesID)}, "episodeid": {strconv.Itoa(p.EpisodeID)},
				"language": {l.Code2}, "forced": {pyBool(l.Forced)}, "hi": {pyBool(l.HI)},
			}
			if err := c.send(ctx, "PATCH", "/episodes/subtitles", form, providerTimeout); err != nil {
				return res, err
			}
		}
		after, err := c.episode(ctx, p.EpisodeID)
		if err != nil {
			res.Warnings = append(res.Warnings, "the search was started but the episode could "+
				"not be read back: "+err.Error())
			return res, nil
		}
		res.Found, res.Pending = arrived(p, after.Subtitles)
	}

	if len(p.Replaces) > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"%s already had %s, so whether a better match replaced it cannot be told from here — "+
				"bazarr_subtitle_status shows the file Bazarr has now", p.Title, languageList(p.Replaces)))
	}
	if len(res.Pending) > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"no %s subtitle is on disk yet. Recent Bazarr runs the search as a background job, so "+
				"it may still be going — bazarr_subtitle_status in a minute will say. If it is still "+
				"missing then, no provider had a match above the minimum score: "+
				"bazarr_subtitle_candidates lists everything that was found, however poor",
			languageList(res.Pending)))
	}

	return res, nil
}

// arrived splits the requested languages by whether a subtitle is now there.
// A language the plan replaces was already there before, so it proves nothing
// and is not counted as found.
func arrived(p SearchPlan, have []Subtitle) (found, pending []SubtitleLanguage) {
	for _, want := range p.Languages {
		replaced := slices.ContainsFunc(p.Replaces, want.same)
		present := slices.ContainsFunc(have, func(s Subtitle) bool {
			return !s.Embedded && want.same(s.SubtitleLanguage)
		})
		if present && !replaced {
			found = append(found, want)
		} else if !present {
			pending = append(pending, want)
		}
	}
	return found, pending
}

func searchedSeriesNote(p SearchPlan, after int) string {
	if after < p.EpisodesMissing {
		return fmt.Sprintf("%d of %d episodes lacking a subtitle are now covered; the rest are "+
			"still being searched in the background or had no match above the minimum score",
			p.EpisodesMissing-after, p.EpisodesMissing)
	}
	return fmt.Sprintf("the search is running in Bazarr's job queue for %d episodes — "+
		"bazarr_subtitle_status with the series_id shows what is still missing as it goes",
		p.EpisodesMissing)
}

func movieLabel(m Movie) string {
	if m.Year != "" && m.Year != "0" {
		return fmt.Sprintf("%s (%s)", m.Title, m.Year)
	}
	return m.Title
}

func languageList(ls []SubtitleLanguage) string {
	names := make([]string, 0, len(ls))
	for _, l := range ls {
		names = append(names, l.String())
	}
	return strings.Join(names, ", ")
}
