package prowlarr

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// A sync is Prowlarr's "Sync App Indexers" button: push every indexer to every
// application now instead of waiting for the next change. It is the fix for an
// *arr that has drifted — an indexer deleted there by hand, a new app that came
// up empty.
//
// It is not harmless, which is why it asks. On a Full Sync app it also removes
// indexers Prowlarr no longer handles, and a forced sync rewrites every synced
// indexer in the app with Prowlarr's settings, overwriting anything edited in
// the app by hand.

// The command Prowlarr runs for its own Sync App Indexers button.
const syncCommand = "ApplicationIndexerSync"

type SyncPlan struct {
	Force bool `json:"force" jsonschema:"rewrite every synced indexer even when Prowlarr thinks it is unchanged"`

	FullSyncApps []string `json:"full_sync_apps,omitempty"`
	AddOnlyApps  []string `json:"add_only_apps,omitempty"`
	Disabled     []string `json:"disabled_apps,omitempty"`

	EnabledIndexers int `json:"enabled_indexers"`

	Warnings []string `json:"warnings,omitempty"`
}

type SyncResult struct {
	Plan          SyncPlan `json:"plan"`
	CommandID     int      `json:"command_id"`
	CommandStatus string   `json:"command_status,omitempty" jsonschema:"queued, started, completed or failed — a queued sync has not pushed anything yet"`
	Warnings      []string `json:"warnings,omitempty"`
}

// PlanSync reads what a sync would reach.
func PlanSync(ctx context.Context, force bool) (SyncPlan, error) {
	c, err := newClient()
	if err != nil {
		return SyncPlan{}, err
	}

	var (
		apps []applicationJSON
		raw  []indexerJSON
	)
	if err := c.get(ctx, "/applications", nil, &apps); err != nil {
		return SyncPlan{}, err
	}
	if err := c.get(ctx, "/indexer", nil, &raw); err != nil {
		return SyncPlan{}, err
	}

	p := SyncPlan{Force: force}
	for _, a := range apps {
		switch a.SyncLevel {
		case "fullSync":
			p.FullSyncApps = append(p.FullSyncApps, a.Name)
		case "addOnly":
			p.AddOnlyApps = append(p.AddOnlyApps, a.Name)
		default:
			p.Disabled = append(p.Disabled, a.Name)
		}
	}
	for _, r := range raw {
		if r.Enable {
			p.EnabledIndexers++
		}
	}

	if len(p.FullSyncApps)+len(p.AddOnlyApps) == 0 {
		return SyncPlan{}, fmt.Errorf("prowlarr has no application to sync to — every one is " +
			"missing or has sync disabled (Settings → Apps)")
	}

	if len(p.FullSyncApps) > 0 {
		p.Warnings = append(p.Warnings, fmt.Sprintf("in %s, indexers Prowlarr no longer handles "+
			"are removed", strings.Join(p.FullSyncApps, " and ")))
	}
	if len(p.AddOnlyApps) > 0 {
		p.Warnings = append(p.Warnings, fmt.Sprintf("%s only receives indexers it does not have "+
			"yet; existing ones there keep their settings", strings.Join(p.AddOnlyApps, " and ")))
	}
	if force {
		p.Warnings = append(p.Warnings, "forced: every synced indexer is rewritten with "+
			"Prowlarr's settings, overwriting anything changed in the apps by hand")
	}
	return p, nil
}

// Sync starts the sync. It returns as soon as the command is queued: the push
// itself runs inside Prowlarr.
func Sync(ctx context.Context, p SyncPlan) (SyncResult, error) {
	c, err := newClient()
	if err != nil {
		return SyncResult{}, err
	}

	var command struct {
		ID     int    `json:"id"`
		Status string `json:"status"`
	}
	body := map[string]any{"name": syncCommand, "forceSync": p.Force}
	if err := c.do(ctx, http.MethodPost, "/command", nil, body, &command, requestTimeout); err != nil {
		return SyncResult{}, err
	}

	return SyncResult{
		Plan:          p,
		CommandID:     command.ID,
		CommandStatus: command.Status,
		Warnings: []string{"the sync runs inside Prowlarr; an app it cannot reach is skipped " +
			"without failing the command — prowlarr_applications with test=true shows which " +
			"can be reached, and radarr_system_health / sonarr_system_health report any indexer " +
			"problem on the app's side"},
	}, nil
}
