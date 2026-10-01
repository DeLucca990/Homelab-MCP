package bazarr

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// Bazarr keeps no library of its own: it mirrors Radarr's movies and Sonarr's
// series, and uses their ids. A movie here is identified by its Radarr id, an
// episode by its Sonarr episode id — the same numbers radarr_library_status
// and the Sonarr tools hand out, so nothing has to be translated between
// families. The one catch is timing: Bazarr copies those libraries on a
// schedule, so something added to Radarr a minute ago may not be here yet.

// Default and ceiling for how many rows a listing returns.
const (
	defaultListLimit = 25
	maxListLimit     = 200
)

// SubtitleLanguage is a language a movie or episode is missing.
type SubtitleLanguage struct {
	Code2  string `json:"code2"`
	Name   string `json:"name,omitempty"`
	Forced bool   `json:"forced,omitempty"`
	HI     bool   `json:"hi,omitempty"`
}

// same compares what makes two subtitles the same slot on disk — the name is
// presentation and differs between Bazarr's endpoints.
func (l SubtitleLanguage) same(o SubtitleLanguage) bool {
	return l.Code2 == o.Code2 && l.Forced == o.Forced && l.HI == o.HI
}

func (l SubtitleLanguage) String() string {
	s := nonEmpty(l.Name, l.Code2)
	switch {
	case l.Forced:
		s += " (forced)"
	case l.HI:
		s += " (HI)"
	}
	return s
}

// Subtitle is a subtitle a movie or episode has.
type Subtitle struct {
	SubtitleLanguage

	Path      string `json:"path,omitempty" jsonschema:"the subtitle file as Bazarr sees it; absent for a track embedded in the video file"`
	Embedded  bool   `json:"embedded,omitempty" jsonschema:"a track inside the video container rather than a file next to it. Bazarr cannot sync or shift those"`
	SizeBytes uint64 `json:"size_bytes,omitempty"`
}

// Movie is one film as Bazarr sees it.
type Movie struct {
	RadarrID int    `json:"radarr_id" jsonschema:"Radarr's own movie id, which is also Bazarr's"`
	Title    string `json:"title"`
	Year     string `json:"year,omitempty"`

	Monitored bool `json:"monitored"`

	ProfileID   int    `json:"profile_id,omitempty"`
	ProfileName string `json:"profile,omitempty" jsonschema:"the language profile deciding which subtitles this should have; absent means none, so Bazarr wants nothing for it"`

	AudioLanguages []string           `json:"audio_languages,omitempty"`
	Subtitles      []Subtitle         `json:"subtitles,omitempty"`
	Missing        []SubtitleLanguage `json:"missing,omitempty" jsonschema:"languages the profile asks for that are not there"`

	Path      string `json:"path,omitempty"`
	SceneName string `json:"scene_name,omitempty" jsonschema:"the release name the file was downloaded as; providers match subtitles against it, and without one only the title and year are left"`
}

// Series is one show as Bazarr sees it.
type Series struct {
	SeriesID int    `json:"series_id" jsonschema:"Sonarr's own series id, which is also Bazarr's"`
	Title    string `json:"title"`
	Year     string `json:"year,omitempty"`

	Monitored bool `json:"monitored"`

	ProfileID   int    `json:"profile_id,omitempty"`
	ProfileName string `json:"profile,omitempty" jsonschema:"the language profile every episode inherits; absent means none"`

	EpisodeFileCount         int `json:"episode_file_count"`
	EpisodesMissingSubtitles int `json:"episodes_missing_subtitles" jsonschema:"episodes lacking at least one language the profile asks for"`
}

// Episode is one episode as Bazarr sees it.
type Episode struct {
	EpisodeID int    `json:"episode_id" jsonschema:"Sonarr's own episode id, which is also Bazarr's"`
	SeriesID  int    `json:"series_id"`
	Season    int    `json:"season"`
	Episode   int    `json:"episode"`
	Title     string `json:"title,omitempty"`

	Monitored bool `json:"monitored"`

	AudioLanguages []string           `json:"audio_languages,omitempty"`
	Subtitles      []Subtitle         `json:"subtitles,omitempty"`
	Missing        []SubtitleLanguage `json:"missing,omitempty"`

	Path      string `json:"path,omitempty"`
	SceneName string `json:"scene_name,omitempty"`
}

// Label is how an episode is named in a sentence.
func (e Episode) Label() string {
	return fmt.Sprintf("S%02dE%02d", e.Season, e.Episode)
}

// --- reading one ------------------------------------------------------------

// GetMovie reads one movie by its Radarr id.
func GetMovie(ctx context.Context, radarrID int) (Movie, error) {
	c, err := newClient()
	if err != nil {
		return Movie{}, err
	}
	m, err := c.movie(ctx, radarrID)
	if err != nil {
		return Movie{}, err
	}
	c.nameProfiles(ctx, &m, nil)
	return m, nil
}

func (c *client) movie(ctx context.Context, radarrID int) (Movie, error) {
	if radarrID <= 0 {
		return Movie{}, fmt.Errorf("a radarr_id is required — radarr_library_status lists them, " +
			"or bazarr_subtitle_status with 'term' finds one by title")
	}

	var page struct {
		Data []movieJSON `json:"data"`
	}
	q := url.Values{"radarrid[]": {strconv.Itoa(radarrID)}}
	if err := c.get(ctx, "/movies", q, &page); err != nil {
		return Movie{}, err
	}
	for _, r := range page.Data {
		if r.RadarrID == radarrID {
			return r.toMovie(), nil
		}
	}
	return Movie{}, fmt.Errorf("bazarr has no movie with Radarr id %d. Bazarr copies Radarr's "+
		"library on a schedule, so a film added in the last few minutes may not have arrived "+
		"yet; otherwise the number is probably a TMDB id, which is not what Bazarr uses", radarrID)
}

// GetSeries reads one series by its Sonarr id.
func GetSeries(ctx context.Context, seriesID int) (Series, error) {
	c, err := newClient()
	if err != nil {
		return Series{}, err
	}
	s, err := c.series(ctx, seriesID)
	if err != nil {
		return Series{}, err
	}
	c.nameProfiles(ctx, nil, &s)
	return s, nil
}

func (c *client) series(ctx context.Context, seriesID int) (Series, error) {
	if seriesID <= 0 {
		return Series{}, fmt.Errorf("a series_id is required — sonarr_library_status lists them, " +
			"or bazarr_subtitle_status with 'term' finds one by title")
	}

	var page struct {
		Data []seriesJSON `json:"data"`
	}
	q := url.Values{"seriesid[]": {strconv.Itoa(seriesID)}}
	if err := c.get(ctx, "/series", q, &page); err != nil {
		return Series{}, err
	}
	for _, r := range page.Data {
		if r.SonarrSeriesID == seriesID {
			return r.toSeries(), nil
		}
	}
	return Series{}, fmt.Errorf("bazarr has no series with Sonarr id %d. Bazarr copies Sonarr's "+
		"library on a schedule, so a show added in the last few minutes may not have arrived "+
		"yet; otherwise the number is probably a TVDB id, which is not what Bazarr uses", seriesID)
}

// GetEpisode reads one episode by its Sonarr episode id.
func GetEpisode(ctx context.Context, episodeID int) (Episode, error) {
	c, err := newClient()
	if err != nil {
		return Episode{}, err
	}
	return c.episode(ctx, episodeID)
}

func (c *client) episode(ctx context.Context, episodeID int) (Episode, error) {
	if episodeID <= 0 {
		return Episode{}, fmt.Errorf("an episode_id is required — bazarr_subtitle_status with a " +
			"series_id lists them, as does sonarr_missing_episodes")
	}

	var page struct {
		Data []episodeJSON `json:"data"`
	}
	q := url.Values{"episodeid[]": {strconv.Itoa(episodeID)}}
	if err := c.get(ctx, "/episodes", q, &page); err != nil {
		return Episode{}, err
	}
	for _, r := range page.Data {
		if r.SonarrEpisodeID == episodeID {
			return r.toEpisode(), nil
		}
	}
	return Episode{}, fmt.Errorf("bazarr has no episode with Sonarr id %d — Bazarr only knows "+
		"episodes that have a file on disk, and copies Sonarr on a schedule", episodeID)
}

func (c *client) seriesEpisodes(ctx context.Context, seriesID int) ([]Episode, error) {
	var page struct {
		Data []episodeJSON `json:"data"`
	}
	q := url.Values{"seriesid[]": {strconv.Itoa(seriesID)}}
	if err := c.get(ctx, "/episodes", q, &page); err != nil {
		return nil, err
	}
	out := make([]Episode, 0, len(page.Data))
	for _, r := range page.Data {
		out = append(out, r.toEpisode())
	}
	return out, nil
}

// nameProfiles fills in profile names, which the media endpoints only give as
// ids. Best effort: a profile that cannot be named keeps its id.
func (c *client) nameProfiles(ctx context.Context, m *Movie, s *Series) {
	profiles, err := c.profiles(ctx)
	if err != nil {
		return
	}
	if m != nil {
		if p, ok := profileByID(profiles, m.ProfileID); ok {
			m.ProfileName = p.Name
		}
	}
	if s != nil {
		if p, ok := profileByID(profiles, s.ProfileID); ok {
			s.ProfileName = p.Name
		}
	}
}

// --- the status view ----------------------------------------------------------

// Target names what a subtitle operation is about. Exactly one of the three
// ids is set.
type Target struct {
	RadarrID  int
	SeriesID  int
	EpisodeID int
}

func (t Target) Validate() error {
	n := 0
	for _, id := range []int{t.RadarrID, t.SeriesID, t.EpisodeID} {
		if id < 0 {
			return fmt.Errorf("ids are positive numbers")
		}
		if id > 0 {
			n++
		}
	}
	switch n {
	case 0:
		return fmt.Errorf("name what this is about: 'radarr_id' for a movie, 'series_id' for a " +
			"whole series, or 'episode_id' for one episode")
	case 1:
		return nil
	default:
		return fmt.Errorf("pass exactly one of 'radarr_id', 'series_id' and 'episode_id'")
	}
}

// Status is what bazarr_subtitle_status answers: one movie, one episode, one
// series with its episodes, or the titles a search term matched.
type Status struct {
	Movie   *Movie   `json:"movie,omitempty"`
	Series  *Series  `json:"series,omitempty"`
	Episode *Episode `json:"episode,omitempty"`

	Episodes           []Episode `json:"episodes,omitempty" jsonschema:"for a series: its episodes lacking a subtitle, most recent first"`
	EpisodesShownCount int       `json:"episodes_shown_count,omitempty"`

	MatchedMovies []Movie  `json:"matched_movies,omitempty" jsonschema:"for a search term: the movies whose title matched"`
	MatchedSeries []Series `json:"matched_series,omitempty" jsonschema:"for a search term: the series whose title matched"`

	Warnings []string `json:"warnings,omitempty"`
}

// GetStatus reads the subtitles of one thing, or finds things by title.
func GetStatus(ctx context.Context, t Target, term string, limit int) (Status, error) {
	c, err := newClient()
	if err != nil {
		return Status{}, err
	}

	switch {
	case limit <= 0:
		limit = defaultListLimit
	case limit > maxListLimit:
		limit = maxListLimit
	}

	if strings.TrimSpace(term) != "" && t == (Target{}) {
		return c.findByTitle(ctx, term, limit)
	}
	if err := t.Validate(); err != nil {
		return Status{}, fmt.Errorf("%w, or 'term' to find one by title", err)
	}

	var st Status
	switch {
	case t.RadarrID > 0:
		m, err := c.movie(ctx, t.RadarrID)
		if err != nil {
			return Status{}, err
		}
		c.nameProfiles(ctx, &m, nil)
		st.Movie = &m
		st.Warnings = mediaWarnings(m.Title, m.ProfileID, m.Missing, m.Subtitles, m.SceneName)

	case t.EpisodeID > 0:
		e, err := c.episode(ctx, t.EpisodeID)
		if err != nil {
			return Status{}, err
		}
		st.Episode = &e
		if s, err := c.series(ctx, e.SeriesID); err == nil {
			c.nameProfiles(ctx, nil, &s)
			st.Series = &s
			st.Warnings = mediaWarnings(s.Title+" "+e.Label(), s.ProfileID, e.Missing,
				e.Subtitles, e.SceneName)
		}

	default:
		s, err := c.series(ctx, t.SeriesID)
		if err != nil {
			return Status{}, err
		}
		c.nameProfiles(ctx, nil, &s)
		st.Series = &s

		episodes, err := c.seriesEpisodes(ctx, s.SeriesID)
		if err != nil {
			return Status{}, err
		}
		lacking := slices.DeleteFunc(episodes, func(e Episode) bool { return len(e.Missing) == 0 })
		slices.SortFunc(lacking, func(a, b Episode) int {
			if a.Season != b.Season {
				return b.Season - a.Season
			}
			return b.Episode - a.Episode
		})
		if len(lacking) > limit {
			st.Warnings = append(st.Warnings, fmt.Sprintf(
				"%d episodes lack a subtitle and %d are shown — raise 'limit' to see the rest",
				len(lacking), limit))
			lacking = lacking[:limit]
		}
		st.Episodes = lacking
		st.EpisodesShownCount = len(lacking)

		if s.ProfileID == 0 {
			st.Warnings = append(st.Warnings, fmt.Sprintf(
				"%s has no language profile, so Bazarr wants no subtitles for any episode of it "+
					"and will never search on its own — bazarr_language_profile_set fixes that",
				s.Title))
		}
	}

	return st, nil
}

// findByTitle searches both libraries, because "subtitles for The Office" does
// not say which one it is in.
func (c *client) findByTitle(ctx context.Context, term string, limit int) (Status, error) {
	want := strings.ToLower(strings.TrimSpace(term))

	var movies struct {
		Data []movieJSON `json:"data"`
	}
	var series struct {
		Data []seriesJSON `json:"data"`
	}
	moviesErr := c.get(ctx, "/movies", nil, &movies)
	seriesErr := c.get(ctx, "/series", nil, &series)
	if moviesErr != nil && seriesErr != nil {
		return Status{}, moviesErr
	}

	profiles, _ := c.profiles(ctx)

	var st Status
	for _, r := range movies.Data {
		if strings.Contains(strings.ToLower(r.Title), want) {
			m := r.toMovie()
			if p, ok := profileByID(profiles, m.ProfileID); ok {
				m.ProfileName = p.Name
			}
			st.MatchedMovies = append(st.MatchedMovies, m)
		}
	}
	for _, r := range series.Data {
		if strings.Contains(strings.ToLower(r.Title), want) {
			s := r.toSeries()
			if p, ok := profileByID(profiles, s.ProfileID); ok {
				s.ProfileName = p.Name
			}
			st.MatchedSeries = append(st.MatchedSeries, s)
		}
	}

	if moviesErr != nil {
		st.Warnings = append(st.Warnings, "could not read Bazarr's movies: "+moviesErr.Error())
	}
	if seriesErr != nil {
		st.Warnings = append(st.Warnings, "could not read Bazarr's series: "+seriesErr.Error())
	}

	total := len(st.MatchedMovies) + len(st.MatchedSeries)
	if len(st.MatchedMovies) > limit {
		st.MatchedMovies = st.MatchedMovies[:limit]
	}
	if len(st.MatchedSeries) > limit {
		st.MatchedSeries = st.MatchedSeries[:limit]
	}
	if shown := len(st.MatchedMovies) + len(st.MatchedSeries); shown < total {
		st.Warnings = append(st.Warnings, fmt.Sprintf(
			"%d titles matched %q and %d are shown — narrow 'term'", total, term, shown))
	}
	if total == 0 {
		st.Warnings = append(st.Warnings, fmt.Sprintf(
			"nothing in Bazarr matches %q. Bazarr only knows what Radarr and Sonarr have — and, "+
				"for a series, only episodes with a file on disk", term))
	}

	return st, nil
}

func mediaWarnings(label string, profileID int, missing []SubtitleLanguage, subs []Subtitle, sceneName string) []string {
	var out []string
	if profileID == 0 {
		out = append(out, fmt.Sprintf(
			"%s has no language profile, so Bazarr considers nothing missing and will never "+
				"search for it on its own — bazarr_language_profile_set fixes that, and "+
				"bazarr_subtitle_search with a 'language' works regardless", label))
	}
	if len(missing) > 0 && strings.TrimSpace(sceneName) == "" {
		out = append(out, fmt.Sprintf(
			"%s has no release name recorded, so providers can only match subtitles on title "+
				"and year — expect a lower score and more subtitles that are out of sync", label))
	}
	return out
}

// --- wire types -----------------------------------------------------------

type languageJSON struct {
	Name   string   `json:"name"`
	Code2  string   `json:"code2"`
	Code3  string   `json:"code3"`
	Forced flexBool `json:"forced"`
	HI     flexBool `json:"hi"`
}

func (l languageJSON) toLanguage() SubtitleLanguage {
	return SubtitleLanguage{Code2: l.Code2, Name: l.Name, Forced: bool(l.Forced), HI: bool(l.HI)}
}

type subtitleJSON struct {
	languageJSON
	Path     string `json:"path"`
	FileSize int64  `json:"file_size"`
}

func (s subtitleJSON) toSubtitle() Subtitle {
	out := Subtitle{SubtitleLanguage: s.toLanguage(), Path: s.Path, Embedded: s.Path == ""}
	if s.FileSize > 0 {
		out.SizeBytes = uint64(s.FileSize)
	}
	return out
}

type audioJSON struct {
	Name string `json:"name"`
}

func audioNames(in []audioJSON) []string {
	out := make([]string, 0, len(in))
	for _, a := range in {
		if a.Name != "" {
			out = append(out, a.Name)
		}
	}
	return out
}

func subtitles(in []subtitleJSON) []Subtitle {
	out := make([]Subtitle, 0, len(in))
	for _, s := range in {
		out = append(out, s.toSubtitle())
	}
	return out
}

func missingLanguages(in []languageJSON) []SubtitleLanguage {
	out := make([]SubtitleLanguage, 0, len(in))
	for _, l := range in {
		out = append(out, l.toLanguage())
	}
	return out
}

type movieJSON struct {
	RadarrID  int    `json:"radarrId"`
	Title     string `json:"title"`
	Year      string `json:"year"`
	Monitored bool   `json:"monitored"`
	ProfileID *int   `json:"profileId"`
	Path      string `json:"path"`
	SceneName string `json:"sceneName"`

	AudioLanguage    []audioJSON    `json:"audio_language"`
	Subtitles        []subtitleJSON `json:"subtitles"`
	MissingSubtitles []languageJSON `json:"missing_subtitles"`
}

func (r movieJSON) toMovie() Movie {
	m := Movie{
		RadarrID:       r.RadarrID,
		Title:          r.Title,
		Year:           r.Year,
		Monitored:      r.Monitored,
		Path:           r.Path,
		SceneName:      r.SceneName,
		AudioLanguages: audioNames(r.AudioLanguage),
		Subtitles:      subtitles(r.Subtitles),
		Missing:        missingLanguages(r.MissingSubtitles),
	}
	if r.ProfileID != nil {
		m.ProfileID = *r.ProfileID
	}
	return m
}

type seriesJSON struct {
	SonarrSeriesID      int    `json:"sonarrSeriesId"`
	Title               string `json:"title"`
	Year                string `json:"year"`
	Monitored           bool   `json:"monitored"`
	ProfileID           *int   `json:"profileId"`
	EpisodeFileCount    int    `json:"episodeFileCount"`
	EpisodeMissingCount int    `json:"episodeMissingCount"`
}

func (r seriesJSON) toSeries() Series {
	s := Series{
		SeriesID:                 r.SonarrSeriesID,
		Title:                    r.Title,
		Year:                     r.Year,
		Monitored:                r.Monitored,
		EpisodeFileCount:         r.EpisodeFileCount,
		EpisodesMissingSubtitles: r.EpisodeMissingCount,
	}
	if r.ProfileID != nil {
		s.ProfileID = *r.ProfileID
	}
	return s
}

type episodeJSON struct {
	SonarrEpisodeID int    `json:"sonarrEpisodeId"`
	SonarrSeriesID  int    `json:"sonarrSeriesId"`
	Season          int    `json:"season"`
	Episode         int    `json:"episode"`
	Title           string `json:"title"`
	Monitored       bool   `json:"monitored"`
	Path            string `json:"path"`
	SceneName       string `json:"sceneName"`

	AudioLanguage    []audioJSON    `json:"audio_language"`
	Subtitles        []subtitleJSON `json:"subtitles"`
	MissingSubtitles []languageJSON `json:"missing_subtitles"`
}

func (r episodeJSON) toEpisode() Episode {
	return Episode{
		EpisodeID:      r.SonarrEpisodeID,
		SeriesID:       r.SonarrSeriesID,
		Season:         r.Season,
		Episode:        r.Episode,
		Title:          r.Title,
		Monitored:      r.Monitored,
		Path:           r.Path,
		SceneName:      r.SceneName,
		AudioLanguages: audioNames(r.AudioLanguage),
		Subtitles:      subtitles(r.Subtitles),
		Missing:        missingLanguages(r.MissingSubtitles),
	}
}
