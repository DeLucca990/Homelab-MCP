package prowlarr

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// Changing an indexer goes through Prowlarr's bulk editor rather than a PUT of
// the whole indexer. The PUT sends every setting back — credentials included —
// and re-tests the indexer before saving, so a site that happens to be down
// refuses the change; the bulk editor touches only the fields named, which is
// exactly the operation being approved.
//
// What the change does downstream depends on each application's sync level,
// and that is the part worth stating before it happens: Full Sync apps are
// updated straight away, Add Only apps never hear about it.

type UpdateRequest struct {
	Indexer    string
	Enable     *bool
	Priority   *int
	AppProfile string
}

// Change is one field of an indexer, before and after.
type Change struct {
	Field string `json:"field"`
	From  string `json:"from"`
	To    string `json:"to"`
}

type UpdatePlan struct {
	Indexer Indexer  `json:"indexer"`
	Changes []Change `json:"changes"`

	FullSyncApps []string `json:"full_sync_apps,omitempty" jsonschema:"apps that will receive this change straight away"`
	AddOnlyApps  []string `json:"add_only_apps,omitempty" jsonschema:"apps that will keep their old copy of this indexer"`

	Warnings []string `json:"warnings,omitempty"`

	body map[string]any
}

// Summary is the changes as one line, for the fingerprint and the log.
func (p UpdatePlan) Summary() string {
	parts := make([]string, 0, len(p.Changes))
	for _, c := range p.Changes {
		parts = append(parts, fmt.Sprintf("%s: %s → %s", c.Field, c.From, c.To))
	}
	return strings.Join(parts, ", ")
}

type UpdateResult struct {
	Indexer  Indexer  `json:"indexer" jsonschema:"the indexer as Prowlarr has it after the change"`
	Changes  []Change `json:"changes"`
	Warnings []string `json:"warnings,omitempty"`
}

// PlanUpdate resolves an indexer change without making it.
func PlanUpdate(ctx context.Context, req UpdateRequest) (UpdatePlan, error) {
	if req.Enable == nil && req.Priority == nil && strings.TrimSpace(req.AppProfile) == "" {
		return UpdatePlan{}, fmt.Errorf("nothing to change — pass 'enabled', 'priority' or " +
			"'sync_profile'")
	}
	if req.Priority != nil && (*req.Priority < 1 || *req.Priority > 50) {
		return UpdatePlan{}, fmt.Errorf("'priority' runs from 1 to 50, lower preferred; got %d", *req.Priority)
	}

	ix, err := ResolveIndexer(ctx, req.Indexer)
	if err != nil {
		return UpdatePlan{}, err
	}

	c, err := newClient()
	if err != nil {
		return UpdatePlan{}, err
	}

	p := UpdatePlan{Indexer: ix, body: map[string]any{"ids": []int{ix.ID}}}

	if req.Enable != nil && *req.Enable != ix.Enabled {
		p.Changes = append(p.Changes, Change{"enabled", yesNo(ix.Enabled), yesNo(*req.Enable)})
		p.body["enable"] = *req.Enable
	}
	if req.Priority != nil && *req.Priority != ix.Priority {
		p.Changes = append(p.Changes, Change{"priority", strconv.Itoa(ix.Priority), strconv.Itoa(*req.Priority)})
		p.body["priority"] = *req.Priority
	}
	if strings.TrimSpace(req.AppProfile) != "" {
		profiles, err := c.appProfiles(ctx)
		if err != nil {
			return UpdatePlan{}, err
		}
		prof, err := resolveAppProfile(profiles, req.AppProfile)
		if err != nil {
			return UpdatePlan{}, err
		}
		if prof.ID != ix.AppProfileID {
			p.Changes = append(p.Changes, Change{"sync profile",
				nonEmpty(ix.AppProfile, "#"+strconv.Itoa(ix.AppProfileID)),
				fmt.Sprintf("%s (%s)", prof.Name, prof.Describe())})
			p.body["appProfileId"] = prof.ID
		}
	}

	if len(p.Changes) == 0 {
		return UpdatePlan{}, fmt.Errorf("%s already has those settings — nothing to change", ix.Name)
	}

	full, addOnly, err := c.syncTargets(ctx, ix.tagIDs)
	if err == nil {
		p.FullSyncApps, p.AddOnlyApps = full, addOnly
	} else {
		p.Warnings = append(p.Warnings, "could not read the applications, so where this change "+
			"lands is unknown: "+err.Error())
	}

	if len(p.AddOnlyApps) > 0 {
		p.Warnings = append(p.Warnings, fmt.Sprintf(
			"%s %s on Add Only and will keep the old settings for this indexer — the change "+
				"has to be made there by hand, or the app switched to Full Sync",
			strings.Join(p.AddOnlyApps, " and "), plural(len(p.AddOnlyApps), "is", "are")))
	}
	if req.Enable != nil && *req.Enable && ix.Failing {
		p.Warnings = append(p.Warnings, fmt.Sprintf(
			"%s is currently failing — enabling it does not make it work; prowlarr_indexer_test "+
				"says what is wrong", ix.Name))
	}
	if req.Enable != nil && !*req.Enable {
		p.Warnings = append(p.Warnings, "a disabled indexer stays in the Full Sync apps with "+
			"RSS and both searches switched off; it is not removed")
	}

	return p, nil
}

// Update applies a planned change and reads the indexer back.
func Update(ctx context.Context, p UpdatePlan) (UpdateResult, error) {
	c, err := newClient()
	if err != nil {
		return UpdateResult{}, err
	}

	if err := c.do(ctx, http.MethodPut, "/indexer/bulk", nil, p.body, nil, requestTimeout); err != nil {
		return UpdateResult{}, err
	}

	res := UpdateResult{Indexer: p.Indexer, Changes: p.Changes}
	after, err := ResolveIndexer(ctx, strconv.Itoa(p.Indexer.ID))
	if err != nil {
		res.Warnings = append(res.Warnings, "changed, but the indexer could not be read back: "+err.Error())
		return res, nil
	}
	res.Indexer = after

	if len(p.FullSyncApps) > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"prowlarr pushes this to %s in the background; prowlarr_applications shows whether "+
				"it can reach them", strings.Join(p.FullSyncApps, " and ")))
	}
	res.Warnings = append(res.Warnings, p.Warnings...)
	return res, nil
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func nonEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}
