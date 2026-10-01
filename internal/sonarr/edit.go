package sonarr

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Editing a series goes through Sonarr's series editor — ids and only the
// fields to change — so nothing else about the series is sent back. Episode
// monitoring has its own endpoint, and is the finer switch the season monitor
// cannot give: "only get the finale", "stop grabbing the recap episode".

type EditRequest struct {
	SeriesID       int
	Monitored      *bool
	QualityProfile string
	SeriesType     string
	AddTags        []string
	RemoveTags     []string
}

// Change is one field, before and after.
type Change struct {
	Field string `json:"field"`
	From  string `json:"from"`
	To    string `json:"to"`
}

type EditPlan struct {
	Series  Series   `json:"series"`
	Changes []Change `json:"changes"`

	Warnings []string `json:"warnings,omitempty"`

	bodies []map[string]any
}

// Summary is the changes as one line, for the fingerprint and the log.
func (p EditPlan) Summary() string { return summarize(p.Changes) }

func summarize(changes []Change) string {
	parts := make([]string, 0, len(changes))
	for _, c := range changes {
		parts = append(parts, fmt.Sprintf("%s: %s → %s", c.Field, c.From, c.To))
	}
	return strings.Join(parts, "; ")
}

type EditResult struct {
	Series   Series   `json:"series" jsonschema:"the series as Sonarr has it after the edit"`
	Changes  []Change `json:"changes"`
	Warnings []string `json:"warnings,omitempty"`
}

// PlanEdit resolves a series edit without making it.
func PlanEdit(ctx context.Context, req EditRequest) (EditPlan, error) {
	if req.Monitored == nil && req.QualityProfile == "" && req.SeriesType == "" &&
		len(req.AddTags) == 0 && len(req.RemoveTags) == 0 {
		return EditPlan{}, fmt.Errorf("nothing to change — pass 'monitored', 'quality_profile', " +
			"'series_type', 'add_tags' or 'remove_tags'")
	}

	s, err := GetSeries(ctx, req.SeriesID)
	if err != nil {
		return EditPlan{}, err
	}
	c, err := newClient()
	if err != nil {
		return EditPlan{}, err
	}

	p := EditPlan{Series: s}
	body := map[string]any{"seriesIds": []int{s.ID}}

	if req.Monitored != nil && *req.Monitored != s.Monitored {
		p.Changes = append(p.Changes, Change{"monitored", yesNo(s.Monitored), yesNo(*req.Monitored)})
		body["monitored"] = *req.Monitored
		if !*req.Monitored {
			p.Warnings = append(p.Warnings, "an unmonitored series is ignored entirely — no new "+
				"episode is grabbed, whatever each season's own flag says; the files stay")
		} else {
			p.Warnings = append(p.Warnings, "the series flag switches the series back on; each "+
				"season and episode keeps its own flag, which sonarr_season_monitor and "+
				"sonarr_episode_monitor change")
		}
	}

	if req.QualityProfile != "" {
		prof, err := resolveQualityProfile(ctx, c, req.QualityProfile)
		if err != nil {
			return EditPlan{}, err
		}
		if prof.ID != s.QualityProfileID {
			from := "#" + strconv.Itoa(s.QualityProfileID)
			if profiles, err := GetQualityProfiles(ctx); err == nil {
				for _, q := range profiles {
					if q.ID == s.QualityProfileID {
						from = q.Name
					}
				}
			}
			p.Changes = append(p.Changes, Change{"quality profile", from, prof.Name})
			body["qualityProfileId"] = prof.ID
			if s.EpisodesOnDisk > 0 {
				p.Warnings = append(p.Warnings, fmt.Sprintf("%d episode files are on disk; any "+
					"below the new profile's cutoff will be upgraded and replaced as better "+
					"releases appear", s.EpisodesOnDisk))
			}
		}
	}

	if req.SeriesType != "" {
		want, err := matchValue(seriesTypeValues, req.SeriesType)
		if err != nil {
			return EditPlan{}, fmt.Errorf("'series_type' is one of %s", strings.Join(seriesTypeValues, ", "))
		}
		if want != s.SeriesType {
			p.Changes = append(p.Changes, Change{"series type", s.SeriesType, want})
			body["seriesType"] = want
			p.Warnings = append(p.Warnings, "the series type decides how release names are "+
				"read — anime uses absolute numbers, daily uses air dates — so it changes which "+
				"releases match from the next search on")
		}
	}

	if len(p.Changes) > 0 {
		p.bodies = append(p.bodies, body)
	}

	tags, err := c.tags(ctx)
	if err != nil && (len(req.AddTags) > 0 || len(req.RemoveTags) > 0) {
		return EditPlan{}, err
	}
	current := labelsOf(s.tagIDs, tags)
	for _, set := range []struct {
		names []string
		apply string
	}{{req.AddTags, "add"}, {req.RemoveTags, "remove"}} {
		if len(set.names) == 0 {
			continue
		}
		ids, labels, err := resolveTags(tags, set.names)
		if err != nil {
			return EditPlan{}, err
		}
		var effective []string
		for i, l := range labels {
			has := slices.Contains(s.tagIDs, ids[i])
			if (set.apply == "add" && !has) || (set.apply == "remove" && has) {
				effective = append(effective, l)
			}
		}
		if len(effective) == 0 {
			continue
		}
		p.Changes = append(p.Changes, Change{set.apply + " tags",
			orNone(strings.Join(current, ", ")), strings.Join(effective, ", ")})
		p.bodies = append(p.bodies, map[string]any{"seriesIds": []int{s.ID}, "tags": ids, "applyTags": set.apply})
	}

	if len(p.Changes) == 0 {
		return EditPlan{}, fmt.Errorf("%s already has those settings — nothing to change", s.Title)
	}
	return p, nil
}

// Edit applies a planned edit and reads the series back.
func Edit(ctx context.Context, p EditPlan) (EditResult, error) {
	c, err := newClient()
	if err != nil {
		return EditResult{}, err
	}
	for _, body := range p.bodies {
		if err := c.put(ctx, "/series/editor", body, nil); err != nil {
			return EditResult{}, err
		}
	}
	res := EditResult{Series: p.Series, Changes: p.Changes, Warnings: p.Warnings}
	if after, err := GetSeries(ctx, p.Series.ID); err == nil {
		res.Series = after
	}
	return res, nil
}

// --- episode monitoring --------------------------------------------------------

type EpisodeMonitorPlan struct {
	Series    string    `json:"series"`
	Episodes  []Episode `json:"episodes" jsonschema:"the episodes whose flag changes"`
	Monitored bool      `json:"monitored"`
	Unchanged int       `json:"unchanged,omitempty" jsonschema:"episodes asked about that already had this flag"`

	Warnings []string `json:"warnings,omitempty"`
}

// Codes is the episodes as one line, for the fingerprint and the log.
func (p EpisodeMonitorPlan) Codes() string { return JoinEpisodeCodes(p.Episodes, 50) }

// PlanEpisodeMonitor resolves an episode monitor change without making it.
// The episodes must all belong to one series: a change spanning shows is a
// mistake more often than an intention.
func PlanEpisodeMonitor(ctx context.Context, episodeIDs []int, monitored bool) (EpisodeMonitorPlan, error) {
	if len(episodeIDs) == 0 {
		return EpisodeMonitorPlan{}, fmt.Errorf("'episode_ids' is required — sonarr_missing_episodes " +
			"lists them, and sonarr_library_status with a 'term' shows a show's seasons")
	}
	c, err := newClient()
	if err != nil {
		return EpisodeMonitorPlan{}, err
	}

	p := EpisodeMonitorPlan{Monitored: monitored}
	seriesID := 0
	for _, id := range episodeIDs {
		var e episodeJSON
		if err := c.get(ctx, "/episode/"+strconv.Itoa(id), nil, &e); err != nil {
			return EpisodeMonitorPlan{}, fmt.Errorf("no episode %d in Sonarr: %w", id, err)
		}
		if seriesID != 0 && e.SeriesID != seriesID {
			return EpisodeMonitorPlan{}, fmt.Errorf("the episodes belong to more than one series — " +
				"change one series at a time")
		}
		seriesID = e.SeriesID
		if e.Monitored == monitored {
			p.Unchanged++
			continue
		}
		p.Episodes = append(p.Episodes, e.toEpisode())
	}
	if s, err := GetSeries(ctx, seriesID); err == nil {
		p.Series = s.Title
		if monitored && !s.Monitored {
			p.Warnings = append(p.Warnings, fmt.Sprintf("%s itself is unmonitored, so Sonarr "+
				"ignores these episodes anyway — sonarr_series_edit with monitored=true switches "+
				"the series on", s.Title))
		}
	}
	if len(p.Episodes) == 0 {
		return EpisodeMonitorPlan{}, fmt.Errorf("every episode given is already %s", monitoredWord(monitored))
	}
	if monitored {
		p.Warnings = append(p.Warnings, "monitoring does not search by itself — "+
			"sonarr_series_search with these episode_ids does")
	}
	return p, nil
}

// SetEpisodesMonitored applies a planned episode monitor change.
func SetEpisodesMonitored(ctx context.Context, p EpisodeMonitorPlan) (EpisodeMonitorPlan, error) {
	c, err := newClient()
	if err != nil {
		return p, err
	}
	ids := make([]int, 0, len(p.Episodes))
	for _, e := range p.Episodes {
		ids = append(ids, e.ID)
	}
	return p, c.put(ctx, "/episode/monitor", map[string]any{"episodeIds": ids, "monitored": p.Monitored}, nil)
}

// --- tags --------------------------------------------------------------------

type tagJSON struct {
	ID    int    `json:"id"`
	Label string `json:"label"`
}

func (c *client) tags(ctx context.Context) ([]tagJSON, error) {
	var out []tagJSON
	return out, c.get(ctx, "/tag", nil, &out)
}

// resolveTags finds existing tags by label. Tags are never created here.
func resolveTags(all []tagJSON, want []string) ([]int, []string, error) {
	var ids []int
	var labels []string
	for _, w := range want {
		found := false
		for _, t := range all {
			if strings.EqualFold(t.Label, strings.TrimSpace(w)) {
				ids, labels, found = append(ids, t.ID), append(labels, t.Label), true
				break
			}
		}
		if !found {
			existing := make([]string, 0, len(all))
			for _, t := range all {
				existing = append(existing, t.Label)
			}
			return nil, nil, fmt.Errorf("sonarr has no tag %q — it has %s. Tags are created in "+
				"Sonarr's settings, not here", w, orNone(strings.Join(existing, ", ")))
		}
	}
	return ids, labels, nil
}

func labelsOf(ids []int, all []tagJSON) []string {
	var out []string
	for _, id := range ids {
		for _, t := range all {
			if t.ID == id {
				out = append(out, t.Label)
			}
		}
	}
	return out
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "none"
	}
	return s
}
