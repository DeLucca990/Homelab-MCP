package jellyfin

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
)

// Users are where two of the most common "it plays wrong" questions end:
// "why does it always start with the wrong subtitles" is a per-user playback
// preference, and "why does it buffer for her and not for me" is often a
// per-user policy — a remote bitrate cap, or video transcoding switched off so
// every file her TV cannot play simply fails.
//
// Both are whole objects in Jellyfin's API: the preferences and the policy are
// each replaced wholesale on every write. So every change here reads the user's
// current object, changes the named fields and sends the rest back untouched —
// the operation approved is the operation performed, and nothing else moves.

// Subtitle modes, as Jellyfin names them.
var subtitleModes = map[string]string{
	"default":    "Default",
	"always":     "Always",
	"onlyforced": "OnlyForced",
	"forced":     "OnlyForced",
	"none":       "None",
	"off":        "None",
	"smart":      "Smart",
}

// SubtitleModeMeaning says what a mode does, in the words Jellyfin's own
// settings page uses.
func SubtitleModeMeaning(mode string) string {
	switch mode {
	case "Default":
		return "follows the default and forced flags in the file; the language preference only breaks ties"
	case "Always":
		return "loads a subtitle in the preferred language whatever the audio is"
	case "OnlyForced":
		return "loads only forced subtitles — the lines for foreign dialogue, signs and songs"
	case "None":
		return "loads no subtitle; it can still be switched on during playback"
	case "Smart":
		return "loads a subtitle in the preferred language only when the audio is in another language"
	}
	return mode
}

type User struct {
	ID   string `json:"id"`
	Name string `json:"name"`

	Administrator bool `json:"administrator"`
	Disabled      bool `json:"disabled,omitempty"`
	HasPassword   bool `json:"has_password"`

	LastActivitySecondsAgo uint64 `json:"last_activity_seconds_ago,omitempty"`

	AudioLanguage     string `json:"audio_language,omitempty" jsonschema:"preferred audio track language, three-letter; empty means whatever the file marks as default"`
	SubtitleLanguage  string `json:"subtitle_language,omitempty" jsonschema:"preferred subtitle language, three-letter"`
	SubtitleMode      string `json:"subtitle_mode" jsonschema:"Default, Always, OnlyForced, None or Smart"`
	RememberAudio     bool   `json:"remember_audio_selections"`
	RememberSubtitles bool   `json:"remember_subtitle_selections" jsonschema:"a track picked by hand during playback wins over the preference the next time"`

	AllLibraries bool     `json:"all_libraries"`
	Libraries    []string `json:"libraries,omitempty" jsonschema:"the libraries this user can see, when not all of them"`

	RemoteBitrateMbps float64 `json:"remote_bitrate_mbps,omitempty" jsonschema:"cap on streams outside the LAN; absent means no cap"`
	VideoTranscoding  bool    `json:"video_transcoding" jsonschema:"whether Jellyfin may re-encode video for this user; off means a file their client cannot play directly fails instead"`
	AudioTranscoding  bool    `json:"audio_transcoding"`
	Remuxing          bool    `json:"remuxing"`
}

type Users struct {
	Users    []User   `json:"users"`
	Warnings []string `json:"warnings,omitempty"`
}

// GetUsers lists every user with their playback preferences and access.
// Administrator-only.
func GetUsers(ctx context.Context) (Users, error) {
	c, err := newClient()
	if err != nil {
		return Users{}, err
	}

	var (
		raw  []userJSON
		libs []Library

		rawErr, libsErr error
		wg              sync.WaitGroup
	)
	wg.Add(2)
	go func() { defer wg.Done(); rawErr = c.get(ctx, "/Users", nil, &raw) }()
	go func() { defer wg.Done(); libs, libsErr = c.libraries(ctx) }()
	wg.Wait()

	if rawErr != nil {
		return Users{}, rawErr
	}

	out := Users{}
	for _, r := range raw {
		out.Users = append(out.Users, r.toUser(libs))
	}
	slices.SortFunc(out.Users, func(a, b User) int {
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})

	if libsErr != nil {
		out.Warnings = append(out.Warnings, "could not read the libraries, so per-user access "+
			"shows ids rather than names: "+libsErr.Error())
	}
	for _, u := range out.Users {
		if u.Disabled {
			continue
		}
		if !u.VideoTranscoding {
			out.Warnings = append(out.Warnings, fmt.Sprintf("%s may not have video transcoded: a "+
				"file their client cannot play directly will fail rather than play", u.Name))
		}
		if !u.AllLibraries && len(u.Libraries) == 0 {
			out.Warnings = append(out.Warnings, fmt.Sprintf("%s has access to no library at all", u.Name))
		}
	}
	return out, nil
}

// resolveUser finds a user by name or id.
func (c *client) resolveUser(ctx context.Context, input string) (userJSON, map[string]any, error) {
	want := strings.TrimSpace(input)
	if want == "" {
		return userJSON{}, nil, fmt.Errorf("a user is required, by name — jellyfin_users lists them")
	}

	var all []userJSON
	if err := c.get(ctx, "/Users", nil, &all); err != nil {
		return userJSON{}, nil, err
	}
	var hit *userJSON
	for i, u := range all {
		if strings.EqualFold(u.Name, want) || sameGUID(u.ID, want) {
			hit = &all[i]
			break
		}
	}
	if hit == nil {
		names := make([]string, 0, len(all))
		for _, u := range all {
			names = append(names, u.Name)
		}
		return userJSON{}, nil, fmt.Errorf("jellyfin has no user %q — it has %s", input,
			strings.Join(names, ", "))
	}

	// The whole object again, untyped, so a write can send back every field
	// this package does not know about exactly as it was.
	var raw map[string]any
	if err := c.get(ctx, "/Users/"+hit.ID, nil, &raw); err != nil {
		return userJSON{}, nil, err
	}
	return *hit, raw, nil
}

// --- libraries ------------------------------------------------------------------

// Library is one of Jellyfin's libraries ("virtual folders").
type Library struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Type       string   `json:"type,omitempty" jsonschema:"movies, tvshows, music, mixed…"`
	Paths      []string `json:"paths,omitempty"`
	Refreshing bool     `json:"refreshing,omitempty" jsonschema:"a scan of this library is running right now"`
}

// GetLibraries lists the libraries. Administrator-only.
func GetLibraries(ctx context.Context) ([]Library, error) {
	c, err := newClient()
	if err != nil {
		return nil, err
	}
	return c.libraries(ctx)
}

func (c *client) libraries(ctx context.Context) ([]Library, error) {
	var raw []struct {
		Name           string   `json:"Name"`
		ItemID         string   `json:"ItemId"`
		CollectionType string   `json:"CollectionType"`
		Locations      []string `json:"Locations"`
		RefreshStatus  string   `json:"RefreshStatus"`
	}
	if err := c.get(ctx, "/Library/VirtualFolders", nil, &raw); err != nil {
		return nil, err
	}
	out := make([]Library, 0, len(raw))
	for _, r := range raw {
		out = append(out, Library{
			ID:         r.ItemID,
			Name:       r.Name,
			Type:       r.CollectionType,
			Paths:      r.Locations,
			Refreshing: strings.EqualFold(r.RefreshStatus, "Active"),
		})
	}
	return out, nil
}

func resolveLibrary(libs []Library, input string) (Library, error) {
	want := strings.TrimSpace(input)
	for _, l := range libs {
		if strings.EqualFold(l.Name, want) || sameGUID(l.ID, want) {
			return l, nil
		}
	}
	names := make([]string, 0, len(libs))
	for _, l := range libs {
		names = append(names, l.Name)
	}
	return Library{}, fmt.Errorf("jellyfin has no library %q — it has %s", input, strings.Join(names, ", "))
}

// sameGUID compares Jellyfin ids, which it writes with and without dashes
// depending on the endpoint.
func sameGUID(a, b string) bool {
	norm := func(s string) string { return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), "-", "")) }
	return a != "" && norm(a) == norm(b)
}

// --- languages -----------------------------------------------------------------

// resolveCulture turns what someone typed into the three-letter code Jellyfin
// stores a language preference as, checked against the server's own list.
//
// Jellyfin does not tell Brazilian from European Portuguese: both are "por",
// because that is what the language tag inside a video file says. So "pt-BR"
// and Bazarr's "pb" both land on "por", and the result says so rather than
// pretending to a distinction the player will not make.
func (c *client) resolveCulture(ctx context.Context, input string) (code, name string, err error) {
	want := strings.ToLower(strings.TrimSpace(input))
	want = strings.ReplaceAll(want, "_", "-")
	switch want {
	case "":
		return "", "", fmt.Errorf("a language is required")
	case "any", "none", "default", "clear":
		return "", "no preference", nil
	case "pb", "pob", "pt-br", "brazilian", "brazilian portuguese", "portuguese (brazil)":
		want = "por"
	}

	var cultures []struct {
		Name                        string   `json:"Name"`
		DisplayName                 string   `json:"DisplayName"`
		TwoLetterISOLanguageName    string   `json:"TwoLetterISOLanguageName"`
		ThreeLetterISOLanguageName  string   `json:"ThreeLetterISOLanguageName"`
		ThreeLetterISOLanguageNames []string `json:"ThreeLetterISOLanguageNames"`
	}
	if err := c.get(ctx, "/Localization/Cultures", nil, &cultures); err != nil {
		return "", "", err
	}

	for _, cu := range cultures {
		if strings.EqualFold(cu.ThreeLetterISOLanguageName, want) ||
			strings.EqualFold(cu.TwoLetterISOLanguageName, want) ||
			strings.EqualFold(cu.Name, want) || strings.EqualFold(cu.DisplayName, want) ||
			slices.ContainsFunc(cu.ThreeLetterISOLanguageNames, func(s string) bool { return strings.EqualFold(s, want) }) {
			return cu.ThreeLetterISOLanguageName, cu.DisplayName, nil
		}
	}
	return "", "", fmt.Errorf("jellyfin knows no language %q — use a code such as 'eng' or "+
		"'por', a two-letter code such as 'en', or the language's English name", input)
}

// --- wire types -----------------------------------------------------------

type userJSON struct {
	ID               string `json:"Id"`
	Name             string `json:"Name"`
	HasPassword      bool   `json:"HasPassword"`
	LastActivityDate string `json:"LastActivityDate"`

	Configuration struct {
		AudioLanguagePreference    string `json:"AudioLanguagePreference"`
		SubtitleLanguagePreference string `json:"SubtitleLanguagePreference"`
		SubtitleMode               string `json:"SubtitleMode"`
		RememberAudioSelections    bool   `json:"RememberAudioSelections"`
		RememberSubtitleSelections bool   `json:"RememberSubtitleSelections"`
	} `json:"Configuration"`

	Policy struct {
		IsAdministrator                bool     `json:"IsAdministrator"`
		IsDisabled                     bool     `json:"IsDisabled"`
		EnableAllFolders               bool     `json:"EnableAllFolders"`
		EnabledFolders                 []string `json:"EnabledFolders"`
		RemoteClientBitrateLimit       int64    `json:"RemoteClientBitrateLimit"`
		EnableVideoPlaybackTranscoding bool     `json:"EnableVideoPlaybackTranscoding"`
		EnableAudioPlaybackTranscoding bool     `json:"EnableAudioPlaybackTranscoding"`
		EnablePlaybackRemuxing         bool     `json:"EnablePlaybackRemuxing"`
	} `json:"Policy"`
}

func (r userJSON) toUser(libs []Library) User {
	u := User{
		ID:                     r.ID,
		Name:                   r.Name,
		Administrator:          r.Policy.IsAdministrator,
		Disabled:               r.Policy.IsDisabled,
		HasPassword:            r.HasPassword,
		LastActivitySecondsAgo: secondsSince(r.LastActivityDate),
		AudioLanguage:          r.Configuration.AudioLanguagePreference,
		SubtitleLanguage:       r.Configuration.SubtitleLanguagePreference,
		SubtitleMode:           nonEmpty(r.Configuration.SubtitleMode, "Default"),
		RememberAudio:          r.Configuration.RememberAudioSelections,
		RememberSubtitles:      r.Configuration.RememberSubtitleSelections,
		AllLibraries:           r.Policy.EnableAllFolders,
		VideoTranscoding:       r.Policy.EnableVideoPlaybackTranscoding,
		AudioTranscoding:       r.Policy.EnableAudioPlaybackTranscoding,
		Remuxing:               r.Policy.EnablePlaybackRemuxing,
	}
	if r.Policy.RemoteClientBitrateLimit > 0 {
		u.RemoteBitrateMbps = round1(float64(r.Policy.RemoteClientBitrateLimit) / 1e6)
	}
	if !u.AllLibraries {
		for _, id := range r.Policy.EnabledFolders {
			name := id
			for _, l := range libs {
				if sameGUID(l.ID, id) {
					name = l.Name
					break
				}
			}
			u.Libraries = append(u.Libraries, name)
		}
		slices.Sort(u.Libraries)
	}
	return u
}
