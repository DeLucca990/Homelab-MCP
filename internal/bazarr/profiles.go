package bazarr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// A language profile is Bazarr's answer to "which subtitles should this have".
// Every movie and series points at one — or at none, in which case Bazarr
// considers nothing missing and will never search for it on its own. That is
// the state most "why has this no subtitles" questions end in, and it is
// invisible from the wanted list, which only knows about what a profile asks
// for.

// ProfileLanguage is one entry of a profile.
type ProfileLanguage struct {
	Code2  string `json:"code2"`
	Name   string `json:"name,omitempty"`
	Forced bool   `json:"forced,omitempty" jsonschema:"only the forced subtitles — the lines for foreign dialogue, signs and songs — not the full track"`
	HI     bool   `json:"hi,omitempty" jsonschema:"hearing-impaired subtitles, with sound descriptions"`
}

// Profile is a language profile, with its languages spelled out.
type Profile struct {
	ID        int               `json:"id"`
	Name      string            `json:"name"`
	Languages []ProfileLanguage `json:"languages"`
	Cutoff    string            `json:"cutoff,omitempty" jsonschema:"the language that, once present, stops Bazarr searching for the rest"`
	Tag       string            `json:"tag,omitempty" jsonschema:"items carrying this *arr tag get this profile automatically when Bazarr first syncs them"`
}

// Describe is the one-line form a confirmation or a table shows.
func (p Profile) Describe() string {
	parts := make([]string, 0, len(p.Languages))
	for _, l := range p.Languages {
		s := l.Code2
		if l.Name != "" && l.Name != l.Code2 {
			s = l.Name
		}
		switch {
		case l.Forced:
			s += " (forced)"
		case l.HI:
			s += " (HI)"
		}
		parts = append(parts, s)
	}
	if len(parts) == 0 {
		return "no languages"
	}
	return strings.Join(parts, ", ")
}

// GetProfiles lists the language profiles, with language names resolved.
func GetProfiles(ctx context.Context) ([]Profile, error) {
	c, err := newClient()
	if err != nil {
		return nil, err
	}
	return c.profiles(ctx)
}

func (c *client) profiles(ctx context.Context) ([]Profile, error) {
	var raw []profileJSON
	if err := c.get(ctx, "/system/languages/profiles", nil, &raw); err != nil {
		return nil, err
	}

	// Names are a nicety: a profile that cannot be named is still a profile.
	known, _ := c.languages(ctx)

	out := make([]Profile, 0, len(raw))
	for _, r := range raw {
		out = append(out, r.toProfile(known))
	}
	return out, nil
}

// ResolveProfile finds a profile by name or id. "none" is a valid answer and
// comes back as the zero Profile with none=true: it is how a movie is told to
// stop wanting subtitles at all.
func ResolveProfile(ctx context.Context, input string) (p Profile, none bool, err error) {
	profiles, err := GetProfiles(ctx)
	if err != nil {
		return Profile{}, false, err
	}
	return resolveProfile(profiles, input)
}

func resolveProfile(profiles []Profile, input string) (Profile, bool, error) {
	want := strings.TrimSpace(input)
	switch strings.ToLower(want) {
	case "":
		return Profile{}, false, fmt.Errorf("a language profile is required — by name, by id, " +
			"or 'none' to stop Bazarr wanting subtitles for this at all")
	case "none", "null":
		return Profile{}, true, nil
	}

	if id, err := strconv.Atoi(want); err == nil {
		for _, p := range profiles {
			if p.ID == id {
				return p, false, nil
			}
		}
	}
	for _, p := range profiles {
		if strings.EqualFold(p.Name, want) {
			return p, false, nil
		}
	}

	if len(profiles) == 0 {
		return Profile{}, false, fmt.Errorf("bazarr has no language profile at all — one has to be " +
			"created in Bazarr → Settings → Languages before anything can be assigned")
	}
	names := make([]string, 0, len(profiles))
	for _, p := range profiles {
		names = append(names, fmt.Sprintf("%q (id %d: %s)", p.Name, p.ID, p.Describe()))
	}
	return Profile{}, false, fmt.Errorf("no language profile named %q — this Bazarr has %s",
		input, strings.Join(names, ", "))
}

// profileByID is the name a movie's profileId stands for.
func profileByID(profiles []Profile, id int) (Profile, bool) {
	if id == 0 {
		return Profile{}, false
	}
	for _, p := range profiles {
		if p.ID == id {
			return p, true
		}
	}
	return Profile{}, false
}

// --- assigning --------------------------------------------------------------

// ProfileChange is the outcome of pointing a movie or series at a profile.
type ProfileChange struct {
	Kind  string `json:"kind" jsonschema:"movie or series"`
	ID    int    `json:"id" jsonschema:"the Radarr movie id or Sonarr series id"`
	Title string `json:"title"`

	From string `json:"from" jsonschema:"the profile it had, or 'none'"`
	To   string `json:"to" jsonschema:"the profile it has now, or 'none'"`

	MissingAfter []SubtitleLanguage `json:"missing_after,omitempty" jsonschema:"what Bazarr now considers missing, recomputed against the new profile. For a series this is not filled in: it is per episode"`

	Warnings []string `json:"warnings,omitempty"`
}

// SetMovieProfile points one movie at a profile, or at none. Bazarr recomputes
// what is missing as part of the same request, so the answer can say what the
// change made it want.
func SetMovieProfile(ctx context.Context, m Movie, p Profile, none bool) (ProfileChange, error) {
	c, err := newClient()
	if err != nil {
		return ProfileChange{}, err
	}

	res := ProfileChange{Kind: "movie", ID: m.RadarrID, Title: m.Title,
		From: nonEmpty(m.ProfileName, "none"), To: profileLabel(p, none)}

	if err := c.send(ctx, "POST", "/movies", profileForm("radarrid", m.RadarrID, p, none), requestTimeout); err != nil {
		return res, err
	}

	after, err := c.movie(ctx, m.RadarrID)
	if err != nil {
		res.Warnings = append(res.Warnings, "the profile was set but the movie could not be "+
			"read back: "+err.Error())
		return res, nil
	}
	res.MissingAfter = after.Missing
	res.Warnings = append(res.Warnings, profileWarnings(none, len(after.Missing))...)
	return res, nil
}

// SetSeriesProfile does the same for a whole series — every episode of it.
func SetSeriesProfile(ctx context.Context, s Series, p Profile, none bool) (ProfileChange, error) {
	c, err := newClient()
	if err != nil {
		return ProfileChange{}, err
	}

	res := ProfileChange{Kind: "series", ID: s.SeriesID, Title: s.Title,
		From: nonEmpty(s.ProfileName, "none"), To: profileLabel(p, none)}

	if err := c.send(ctx, "POST", "/series", profileForm("seriesid", s.SeriesID, p, none), requestTimeout); err != nil {
		return res, err
	}
	res.Warnings = append(res.Warnings, profileWarnings(none, -1)...)
	return res, nil
}

func profileWarnings(none bool, missing int) []string {
	switch {
	case none:
		return []string{"with no profile Bazarr considers nothing missing, so it will not " +
			"search for subtitles here again on its own; the subtitles already on disk stay"}
	case missing > 0:
		return []string{"this changes what Bazarr wants but does not search by itself — its " +
			"scheduled search will get to it, or bazarr_subtitle_search does it now"}
	case missing < 0:
		return []string{"this changes what Bazarr wants for every episode but does not search " +
			"by itself — its scheduled search will get to them, or bazarr_subtitle_search " +
			"with the series_id does it now"}
	}
	return nil
}

func profileLabel(p Profile, none bool) string {
	if none {
		return "none"
	}
	return p.Name
}

// The form both assignment endpoints take: parallel lists of ids and profile
// ids, of which this only ever sends one pair.
func profileForm(idField string, id int, p Profile, none bool) url.Values {
	profile := "none"
	if !none {
		profile = strconv.Itoa(p.ID)
	}
	return url.Values{
		idField:     {strconv.Itoa(id)},
		"profileid": {profile},
	}
}

func nonEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

// --- wire types -----------------------------------------------------------

type profileJSON struct {
	ProfileID int    `json:"profileId"`
	Name      string `json:"name"`
	Cutoff    *int   `json:"cutoff"`
	Tag       string `json:"tag"`
	Items     []struct {
		ID       int      `json:"id"`
		Language string   `json:"language"`
		Forced   flexBool `json:"forced"`
		HI       flexBool `json:"hi"`
	} `json:"items"`
}

func (r profileJSON) toProfile(known []Language) Profile {
	p := Profile{ID: r.ProfileID, Name: r.Name, Tag: r.Tag}
	for _, it := range r.Items {
		l := ProfileLanguage{
			Code2:  it.Language,
			Name:   languageName(known, it.Language),
			Forced: bool(it.Forced),
			HI:     bool(it.HI),
		}
		p.Languages = append(p.Languages, l)

		if r.Cutoff != nil && *r.Cutoff == it.ID {
			p.Cutoff = l.Name
		}
	}
	// 65535 is Bazarr's "any of them": the first language found is enough.
	if r.Cutoff != nil && *r.Cutoff == 65535 {
		p.Cutoff = "any"
	}
	return p
}

// flexBool reads a boolean that Bazarr sends as true, "True" or "true"
// depending on which table it came from.
type flexBool bool

func (b *flexBool) UnmarshalJSON(raw []byte) error {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	switch t := v.(type) {
	case bool:
		*b = flexBool(t)
	case string:
		*b = flexBool(strings.EqualFold(t, "true"))
	case float64:
		*b = flexBool(t != 0)
	default:
		*b = false
	}
	return nil
}
