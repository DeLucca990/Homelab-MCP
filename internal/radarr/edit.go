package radarr

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Editing a movie goes through Radarr's movie editor, the endpoint behind its
// own mass-edit bar: it takes the ids and only the fields to change, where a
// PUT of the movie would send every field back and a stale copy could undo an
// edit made meanwhile. Moving the files to another root folder is deliberately
// not offered — it is a disk operation, not a setting.

var minimumAvailabilities = []string{"announced", "inCinemas", "released"}

type EditRequest struct {
	MovieID             int
	Monitored           *bool
	QualityProfile      string
	MinimumAvailability string
	AddTags             []string
	RemoveTags          []string
}

// Change is one field, before and after.
type Change struct {
	Field string `json:"field"`
	From  string `json:"from"`
	To    string `json:"to"`
}

type EditPlan struct {
	Movie   Movie    `json:"movie"`
	Changes []Change `json:"changes"`

	Warnings []string `json:"warnings,omitempty"`

	bodies []map[string]any
}

// Summary is the changes as one line, for the fingerprint and the log.
func (p EditPlan) Summary() string {
	parts := make([]string, 0, len(p.Changes))
	for _, c := range p.Changes {
		parts = append(parts, fmt.Sprintf("%s: %s → %s", c.Field, c.From, c.To))
	}
	return strings.Join(parts, "; ")
}

type EditResult struct {
	Movie    Movie    `json:"movie" jsonschema:"the movie as Radarr has it after the edit"`
	Changes  []Change `json:"changes"`
	Warnings []string `json:"warnings,omitempty"`
}

// PlanEdit resolves a movie edit without making it.
func PlanEdit(ctx context.Context, req EditRequest) (EditPlan, error) {
	if req.Monitored == nil && req.QualityProfile == "" && req.MinimumAvailability == "" &&
		len(req.AddTags) == 0 && len(req.RemoveTags) == 0 {
		return EditPlan{}, fmt.Errorf("nothing to change — pass 'monitored', 'quality_profile', " +
			"'minimum_availability', 'add_tags' or 'remove_tags'")
	}

	m, err := GetMovie(ctx, req.MovieID)
	if err != nil {
		return EditPlan{}, err
	}
	c, err := newClient()
	if err != nil {
		return EditPlan{}, err
	}

	p := EditPlan{Movie: m}
	body := map[string]any{"movieIds": []int{m.ID}}

	if req.Monitored != nil && *req.Monitored != m.Monitored {
		p.Changes = append(p.Changes, Change{"monitored", yesNo(m.Monitored), yesNo(*req.Monitored)})
		body["monitored"] = *req.Monitored
		if !*req.Monitored {
			p.Warnings = append(p.Warnings, "an unmonitored movie is never searched for or upgraded; "+
				"its file, if any, stays")
		}
	}

	if req.QualityProfile != "" {
		prof, err := resolveQualityProfile(ctx, c, req.QualityProfile)
		if err != nil {
			return EditPlan{}, err
		}
		if prof.ID != m.QualityProfileID {
			from := "#" + strconv.Itoa(m.QualityProfileID)
			if profiles, err := GetQualityProfiles(ctx); err == nil {
				for _, q := range profiles {
					if q.ID == m.QualityProfileID {
						from = q.Name
					}
				}
			}
			p.Changes = append(p.Changes, Change{"quality profile", from, prof.Name})
			body["qualityProfileId"] = prof.ID
			if m.HasFile {
				p.Warnings = append(p.Warnings, fmt.Sprintf("%s has a %s file; if it does not "+
					"meet the new profile's cutoff, Radarr looks for an upgrade and replaces it",
					m.Title, blank(m.Quality)))
			}
		}
	}

	if req.MinimumAvailability != "" {
		want := ""
		for _, v := range minimumAvailabilities {
			if strings.EqualFold(v, strings.TrimSpace(req.MinimumAvailability)) {
				want = v
			}
		}
		if want == "" {
			return EditPlan{}, fmt.Errorf("'minimum_availability' is one of %s",
				strings.Join(minimumAvailabilities, ", "))
		}
		if want != m.MinimumAvailability {
			p.Changes = append(p.Changes, Change{"minimum availability", m.MinimumAvailability, want})
			body["minimumAvailability"] = want
			if want != "released" && !m.HasFile {
				p.Warnings = append(p.Warnings, "before a film is released the only copies are "+
					"usually cam and telesync recordings, which a quality profile may or may not reject")
			}
		}
	}

	if len(p.Changes) > 0 {
		p.bodies = append(p.bodies, body)
	}

	tags, err := c.tags(ctx)
	if err != nil && (len(req.AddTags) > 0 || len(req.RemoveTags) > 0) {
		return EditPlan{}, err
	}
	current := labelsOf(m.tagIDs, tags)
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
			has := slices.Contains(m.tagIDs, ids[i])
			if (set.apply == "add" && !has) || (set.apply == "remove" && has) {
				effective = append(effective, l)
			}
		}
		if len(effective) == 0 {
			continue
		}
		p.Changes = append(p.Changes, Change{set.apply + " tags",
			nonEmptyStr(strings.Join(current, ", "), "none"), strings.Join(effective, ", ")})
		p.bodies = append(p.bodies, map[string]any{"movieIds": []int{m.ID}, "tags": ids, "applyTags": set.apply})
	}

	if len(p.Changes) == 0 {
		return EditPlan{}, fmt.Errorf("%s already has those settings — nothing to change", m.Title)
	}
	return p, nil
}

// Edit applies a planned edit and reads the movie back.
func Edit(ctx context.Context, p EditPlan) (EditResult, error) {
	c, err := newClient()
	if err != nil {
		return EditResult{}, err
	}
	for _, body := range p.bodies {
		if err := c.put(ctx, "/movie/editor", body, nil); err != nil {
			return EditResult{}, err
		}
	}
	res := EditResult{Movie: p.Movie, Changes: p.Changes, Warnings: p.Warnings}
	if after, err := GetMovie(ctx, p.Movie.ID); err == nil {
		res.Movie = after
	}
	return res, nil
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

// resolveTags finds existing tags by label. Tags are never created here: a
// typo would otherwise become a new tag nothing else uses.
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
			return nil, nil, fmt.Errorf("radarr has no tag %q — it has %s. Tags are created in "+
				"Radarr's settings, not here", w, nonEmptyStr(strings.Join(existing, ", "), "none"))
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

func nonEmptyStr(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}
