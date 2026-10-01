package bazarr

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"
)

// "The subtitle is out of sync" has two fixes, and which one applies depends
// on how it is out:
//
//   - drifting, or off by a different amount in different places — the
//     subtitle was made for another cut or frame rate. Syncing aligns it to the
//     audio track, which is what Bazarr's sync does (ffsubsync underneath).
//   - off by the same amount the whole way through — a constant shift fixes
//     it exactly, and costs nothing, where an audio sync takes a minute of CPU
//     and can still guess wrong on a quiet film.
//
// Both rewrite the subtitle file in place. Neither can touch a track embedded
// in the video: that is part of the container, not a file Bazarr owns.

// Sync can run a while on a long film: it decodes the whole audio track.
const syncTimeout = 5 * time.Minute

// The largest shift accepted. A subtitle off by more than this is for a
// different release altogether, and a new subtitle is the fix.
const maxShift = 10 * time.Minute

type SyncRequest struct {
	Target
	Language string
	Forced   bool
	HI       bool
	ShiftMs  int // 0 means align to the audio
}

// SyncPlan is the resolved operation.
type SyncPlan struct {
	Kind      string `json:"kind" jsonschema:"movie or episode"`
	Title     string `json:"title"`
	RadarrID  int    `json:"radarr_id,omitempty"`
	EpisodeID int    `json:"episode_id,omitempty"`

	Subtitle Subtitle `json:"subtitle" jsonschema:"the file that will be rewritten"`

	Mode    string `json:"mode" jsonschema:"'sync' aligns to the audio track; 'shift' moves every line by shift_ms"`
	ShiftMs int    `json:"shift_ms,omitempty" jsonschema:"positive delays the subtitles, negative brings them earlier"`

	action string
}

type SyncResult struct {
	Plan     SyncPlan `json:"plan"`
	Warnings []string `json:"warnings,omitempty"`
}

// PlanSync finds the subtitle file a sync or shift would rewrite.
func PlanSync(ctx context.Context, req SyncRequest) (SyncPlan, error) {
	if err := req.Validate(); err != nil {
		return SyncPlan{}, err
	}
	if req.SeriesID > 0 {
		return SyncPlan{}, fmt.Errorf("a sync rewrites one subtitle file — pass the 'episode_id' " +
			"of the episode whose subtitle is out")
	}
	if req.Forced && req.HI {
		return SyncPlan{}, fmt.Errorf("a subtitle is either forced or hearing-impaired, not both")
	}
	shift := time.Duration(req.ShiftMs) * time.Millisecond
	if shift > maxShift || shift < -maxShift {
		return SyncPlan{}, fmt.Errorf("a shift of %s is not a sync problem — a subtitle that far out "+
			"was made for a different release, and bazarr_subtitle_candidates finds another", shift)
	}

	c, err := newClient()
	if err != nil {
		return SyncPlan{}, err
	}
	known, err := c.languages(ctx)
	if err != nil {
		return SyncPlan{}, err
	}
	lang, err := resolveLanguage(known, req.Language)
	if err != nil {
		return SyncPlan{}, err
	}
	want := SubtitleLanguage{Code2: lang.Code2, Name: lang.Name, Forced: req.Forced, HI: req.HI}

	var (
		p    SyncPlan
		have []Subtitle
	)
	if req.RadarrID > 0 {
		m, err := c.movie(ctx, req.RadarrID)
		if err != nil {
			return SyncPlan{}, err
		}
		p = SyncPlan{Kind: "movie", Title: movieLabel(m), RadarrID: m.RadarrID}
		have = m.Subtitles
	} else {
		e, err := c.episode(ctx, req.EpisodeID)
		if err != nil {
			return SyncPlan{}, err
		}
		p = SyncPlan{Kind: "episode", Title: e.Label(), EpisodeID: e.EpisodeID}
		if s, err := c.series(ctx, e.SeriesID); err == nil {
			p.Title = s.Title + " " + e.Label()
		}
		have = e.Subtitles
	}

	var embedded bool
	for _, s := range have {
		if !s.same(want) {
			continue
		}
		if s.Embedded {
			embedded = true
			continue
		}
		p.Subtitle = s
		break
	}
	if p.Subtitle.Path == "" {
		if embedded {
			return SyncPlan{}, fmt.Errorf("the %s subtitle of %s is a track inside the video file, "+
				"which Bazarr cannot rewrite — download an external one with "+
				"bazarr_subtitle_search or bazarr_subtitle_candidates and sync that", want, p.Title)
		}
		return SyncPlan{}, fmt.Errorf("%s has no %s subtitle file to sync%s", p.Title, want,
			availableSubtitles(have))
	}

	if req.ShiftMs == 0 {
		p.Mode, p.action = "sync", "sync"
	} else {
		p.Mode, p.ShiftMs, p.action = "shift", req.ShiftMs, shiftAction(shift)
	}
	return p, nil
}

// Sync rewrites the planned subtitle file.
func Sync(ctx context.Context, p SyncPlan) (SyncResult, error) {
	c, err := newClient()
	if err != nil {
		return SyncResult{}, err
	}

	id := p.RadarrID
	if p.Kind == "episode" {
		id = p.EpisodeID
	}

	form := url.Values{
		"action":   {p.action},
		"language": {p.Subtitle.Code2},
		"path":     {p.Subtitle.Path},
		"type":     {p.Kind},
		"id":       {strconv.Itoa(id)},
		"forced":   {pyBool(p.Subtitle.Forced)},
		"hi":       {pyBool(p.Subtitle.HI)},
	}

	timeout := requestTimeout
	if p.Mode == "sync" {
		timeout = syncTimeout
	}
	if err := c.send(ctx, "PATCH", "/subtitles", form, timeout); err != nil {
		return SyncResult{}, err
	}

	res := SyncResult{Plan: p}
	if p.Mode == "sync" {
		res.Warnings = append(res.Warnings, "an audio sync is a best guess — on a quiet film or "+
			"one with a long cold open it can land wrong. If it is still off by the same amount "+
			"throughout, a shift fixes that exactly; if it drifts, the subtitle was made for "+
			"another cut and bazarr_subtitle_candidates finds a different one")
	}
	return res, nil
}

// shiftAction is Bazarr's own mod syntax, the same string its web UI sends.
// A negative shift negates every component rather than only the largest.
func shiftAction(d time.Duration) string {
	sign := 1
	if d < 0 {
		sign, d = -1, -d
	}
	h := int(d / time.Hour)
	d -= time.Duration(h) * time.Hour
	m := int(d / time.Minute)
	d -= time.Duration(m) * time.Minute
	s := int(d / time.Second)
	d -= time.Duration(s) * time.Second
	ms := int(d / time.Millisecond)

	return fmt.Sprintf("shift_offset(h=%d,m=%d,s=%d,ms=%d)", sign*h, sign*m, sign*s, sign*ms)
}

func availableSubtitles(have []Subtitle) string {
	var files []SubtitleLanguage
	for _, s := range have {
		if !s.Embedded {
			files = append(files, s.SubtitleLanguage)
		}
	}
	if len(files) == 0 {
		return " — it has no external subtitle file at all"
	}
	return " — the files it has are " + languageList(files)
}
