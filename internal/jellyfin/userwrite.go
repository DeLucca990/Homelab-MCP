package jellyfin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// Change is one field, before and after.
type Change struct {
	Field string `json:"field"`
	From  string `json:"from"`
	To    string `json:"to"`
}

func summarize(changes []Change) string {
	parts := make([]string, 0, len(changes))
	for _, c := range changes {
		parts = append(parts, fmt.Sprintf("%s: %s → %s", c.Field, c.From, c.To))
	}
	return strings.Join(parts, "; ")
}

// --- playback preferences ------------------------------------------------------

type PreferencesRequest struct {
	User              string
	AudioLanguage     *string
	SubtitleLanguage  *string
	SubtitleMode      string
	RememberSubtitles *bool
}

type PreferencesPlan struct {
	User    string   `json:"user"`
	UserID  string   `json:"user_id"`
	Changes []Change `json:"changes"`

	Warnings []string `json:"warnings,omitempty"`

	config map[string]any
}

// Summary is the changes as one line, for the fingerprint and the log.
func (p PreferencesPlan) Summary() string { return summarize(p.Changes) }

type UserChangeResult struct {
	User     User     `json:"user" jsonschema:"the user as Jellyfin has them after the change"`
	Changes  []Change `json:"changes"`
	Warnings []string `json:"warnings,omitempty"`
}

// PlanPreferences resolves a preference change without making it.
func PlanPreferences(ctx context.Context, req PreferencesRequest) (PreferencesPlan, error) {
	if req.AudioLanguage == nil && req.SubtitleLanguage == nil && req.SubtitleMode == "" &&
		req.RememberSubtitles == nil {
		return PreferencesPlan{}, fmt.Errorf("nothing to change — pass 'audio_language', " +
			"'subtitle_language', 'subtitle_mode' or 'remember_subtitle_selections'")
	}

	c, err := newClient()
	if err != nil {
		return PreferencesPlan{}, err
	}
	u, raw, err := c.resolveUser(ctx, req.User)
	if err != nil {
		return PreferencesPlan{}, err
	}
	config, _ := raw["Configuration"].(map[string]any)
	if config == nil {
		return PreferencesPlan{}, fmt.Errorf("jellyfin returned %s without their preferences", u.Name)
	}

	p := PreferencesPlan{User: u.Name, UserID: u.ID, config: config}
	cur := u.Configuration

	setLanguage := func(field, key, current string, input *string) error {
		if input == nil {
			return nil
		}
		code, name, err := c.resolveCulture(ctx, *input)
		if err != nil {
			return err
		}
		if strings.EqualFold(code, current) {
			return nil
		}
		p.Changes = append(p.Changes, Change{field, nonEmpty(current, "none"), nonEmpty(code, "none") + nameSuffix(name, code)})
		if code == "" {
			config[key] = nil
		} else {
			config[key] = code
		}
		if code == "por" {
			p.Warnings = append(p.Warnings, "Jellyfin does not tell Brazilian from European "+
				"Portuguese: both are 'por', because that is what the language tag in the file "+
				"says. A pt-BR subtitle from Bazarr matches this")
		}
		return nil
	}
	if err := setLanguage("audio language", "AudioLanguagePreference", cur.AudioLanguagePreference, req.AudioLanguage); err != nil {
		return PreferencesPlan{}, err
	}
	if err := setLanguage("subtitle language", "SubtitleLanguagePreference", cur.SubtitleLanguagePreference, req.SubtitleLanguage); err != nil {
		return PreferencesPlan{}, err
	}

	mode := nonEmpty(cur.SubtitleMode, "Default")
	if req.SubtitleMode != "" {
		key := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(req.SubtitleMode), "_", ""))
		want, ok := subtitleModes[key]
		if !ok {
			return PreferencesPlan{}, fmt.Errorf("'subtitle_mode' is one of Default, Always, " +
				"OnlyForced, None or Smart")
		}
		if want != mode {
			p.Changes = append(p.Changes, Change{"subtitle mode", mode, want})
			config["SubtitleMode"] = want
			mode = want
		}
	}

	if req.RememberSubtitles != nil && *req.RememberSubtitles != cur.RememberSubtitleSelections {
		p.Changes = append(p.Changes, Change{"remember subtitle selections",
			yesNo(cur.RememberSubtitleSelections), yesNo(*req.RememberSubtitles)})
		config["RememberSubtitleSelections"] = *req.RememberSubtitles
	}

	if len(p.Changes) == 0 {
		return PreferencesPlan{}, fmt.Errorf("%s already has those preferences — nothing to change", u.Name)
	}

	subLang, _ := config["SubtitleLanguagePreference"].(string)
	if (mode == "Always" || mode == "Smart") && subLang == "" {
		p.Warnings = append(p.Warnings, fmt.Sprintf("subtitle mode %s picks a subtitle by "+
			"language, and %s has no subtitle language set — it will load nothing", mode, u.Name))
	}
	if remember, _ := config["RememberSubtitleSelections"].(bool); remember {
		p.Warnings = append(p.Warnings, "remembered selections are on: a subtitle picked by hand "+
			"in a series is used again for its next episodes, ahead of this preference")
	}
	return p, nil
}

// SetPreferences applies a planned preference change and reads the user back.
func SetPreferences(ctx context.Context, p PreferencesPlan) (UserChangeResult, error) {
	c, err := newClient()
	if err != nil {
		return UserChangeResult{}, err
	}

	// The current route takes the user as a query parameter; servers before
	// 10.9 only have the old one with it in the path.
	err = c.send(ctx, http.MethodPost, "/Users/Configuration", url.Values{"userId": {p.UserID}}, p.config)
	if errors.Is(err, ErrNotFound) {
		err = c.send(ctx, http.MethodPost, "/Users/"+p.UserID+"/Configuration", nil, p.config)
	}
	if err != nil {
		return UserChangeResult{}, err
	}
	return c.userAfter(ctx, p.UserID, p.Changes, append(p.Warnings,
		"the preference applies when playback starts, so anything already playing keeps its "+
			"tracks; some third-party apps (Kodi, Infuse) apply their own track rules instead"))
}

// --- access -------------------------------------------------------------------

type AccessRequest struct {
	User              string
	Libraries         []string // nil leaves them alone; ["all"] grants every library
	RemoteBitrateMbps *float64 // 0 removes the cap
	VideoTranscoding  *bool
	Remuxing          *bool
	Disabled          *bool
}

type AccessPlan struct {
	User          string   `json:"user"`
	UserID        string   `json:"user_id"`
	Administrator bool     `json:"administrator"`
	Changes       []Change `json:"changes"`

	Warnings []string `json:"warnings,omitempty"`

	policy map[string]any
}

// Summary is the changes as one line, for the fingerprint and the log.
func (p AccessPlan) Summary() string { return summarize(p.Changes) }

// PlanAccess resolves an access change without making it. It covers what a
// household actually changes — which libraries, how much bandwidth away from
// home, whether video may be transcoded, whether the account works at all — and
// deliberately not administrator rights or passwords.
func PlanAccess(ctx context.Context, req AccessRequest) (AccessPlan, error) {
	if req.Libraries == nil && req.RemoteBitrateMbps == nil && req.VideoTranscoding == nil &&
		req.Remuxing == nil && req.Disabled == nil {
		return AccessPlan{}, fmt.Errorf("nothing to change — pass 'libraries', " +
			"'remote_bitrate_mbps', 'video_transcoding', 'remuxing' or 'disabled'")
	}

	c, err := newClient()
	if err != nil {
		return AccessPlan{}, err
	}
	u, raw, err := c.resolveUser(ctx, req.User)
	if err != nil {
		return AccessPlan{}, err
	}
	policy, _ := raw["Policy"].(map[string]any)
	if policy == nil {
		return AccessPlan{}, fmt.Errorf("jellyfin returned %s without their policy", u.Name)
	}

	libs, err := c.libraries(ctx)
	if err != nil {
		return AccessPlan{}, err
	}
	before := u.toUser(libs)

	p := AccessPlan{User: u.Name, UserID: u.ID, Administrator: before.Administrator, policy: policy}

	if req.Libraries != nil {
		all := len(req.Libraries) == 1 && strings.EqualFold(strings.TrimSpace(req.Libraries[0]), "all")
		from := "all"
		if !before.AllLibraries {
			from = nonEmpty(strings.Join(before.Libraries, ", "), "none")
		}
		if all {
			if !before.AllLibraries {
				p.Changes = append(p.Changes, Change{"libraries", from, "all"})
				policy["EnableAllFolders"] = true
			}
		} else {
			ids := []string{}
			names := []string{}
			for _, in := range req.Libraries {
				l, err := resolveLibrary(libs, in)
				if err != nil {
					return AccessPlan{}, err
				}
				if !slices.Contains(ids, l.ID) {
					ids = append(ids, l.ID)
					names = append(names, l.Name)
				}
			}
			slices.Sort(names)
			to := nonEmpty(strings.Join(names, ", "), "none")
			if to != from {
				p.Changes = append(p.Changes, Change{"libraries", from, to})
				policy["EnableAllFolders"] = false
				policy["EnabledFolders"] = ids
				if len(ids) == 0 {
					p.Warnings = append(p.Warnings, u.Name+" will see no library at all")
				}
			}
		}
	}

	if req.RemoteBitrateMbps != nil {
		mbps := *req.RemoteBitrateMbps
		if mbps < 0 || mbps > 1000 {
			return AccessPlan{}, fmt.Errorf("'remote_bitrate_mbps' runs from 0 (no cap) to 1000, got %g", mbps)
		}
		bps := int64(mbps * 1e6)
		if bps != int64(before.RemoteBitrateMbps*1e6) {
			p.Changes = append(p.Changes, Change{"remote bitrate", mbpsCell(before.RemoteBitrateMbps), mbpsCell(mbps)})
			policy["RemoteClientBitrateLimit"] = bps
			if mbps > 0 && mbps < 8 {
				p.Warnings = append(p.Warnings, fmt.Sprintf("at %g Mbps most 1080p files are "+
					"transcoded down for %s away from home — that is a CPU or GPU encode per stream",
					mbps, u.Name))
			}
		}
	}

	setBool := func(field, key string, current bool, want *bool) {
		if want != nil && *want != current {
			p.Changes = append(p.Changes, Change{field, yesNo(current), yesNo(*want)})
			policy[key] = *want
		}
	}
	setBool("video transcoding", "EnableVideoPlaybackTranscoding", before.VideoTranscoding, req.VideoTranscoding)
	setBool("remuxing", "EnablePlaybackRemuxing", before.Remuxing, req.Remuxing)

	if req.Disabled != nil && *req.Disabled != before.Disabled {
		if *req.Disabled && before.Administrator {
			return AccessPlan{}, fmt.Errorf("%s is an administrator, and Jellyfin does not allow "+
				"disabling one", u.Name)
		}
		setBool("disabled", "IsDisabled", before.Disabled, req.Disabled)
		if *req.Disabled {
			p.Warnings = append(p.Warnings, fmt.Sprintf("disabling signs %s out of every device "+
				"and stops anything they are watching", u.Name))
		}
	}

	if len(p.Changes) == 0 {
		return AccessPlan{}, fmt.Errorf("%s already has that access — nothing to change", u.Name)
	}
	if req.VideoTranscoding != nil && !*req.VideoTranscoding {
		p.Warnings = append(p.Warnings, fmt.Sprintf("with video transcoding off, a file %s's "+
			"client cannot play directly fails instead of playing", u.Name))
	}
	return p, nil
}

// SetAccess applies a planned access change and reads the user back.
func SetAccess(ctx context.Context, p AccessPlan) (UserChangeResult, error) {
	c, err := newClient()
	if err != nil {
		return UserChangeResult{}, err
	}
	if err := c.send(ctx, http.MethodPost, "/Users/"+p.UserID+"/Policy", nil, p.policy); err != nil {
		return UserChangeResult{}, err
	}
	return c.userAfter(ctx, p.UserID, p.Changes, p.Warnings)
}

func (c *client) userAfter(ctx context.Context, id string, changes []Change, warnings []string) (UserChangeResult, error) {
	res := UserChangeResult{Changes: changes, Warnings: warnings}
	var after userJSON
	if err := c.get(ctx, "/Users/"+id, nil, &after); err != nil {
		res.Warnings = append(res.Warnings, "changed, but the user could not be read back: "+err.Error())
		return res, nil
	}
	libs, _ := c.libraries(ctx)
	res.User = after.toUser(libs)
	return res, nil
}

func mbpsCell(v float64) string {
	if v <= 0 {
		return "no cap"
	}
	return strconv.FormatFloat(v, 'f', -1, 64) + " Mbps"
}

func nameSuffix(name, code string) string {
	if name == "" || strings.EqualFold(name, code) || code == "" {
		return ""
	}
	return " (" + name + ")"
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}
