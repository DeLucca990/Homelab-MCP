package mcp

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DeLucca990/homelab-mcp/internal/prowlarr"
)

// PROWLARR WRITE TOOLS
//
// Every change to Prowlarr's indexers travels: Prowlarr pushes it into Radarr
// and Sonarr on its own. So each confirmation names the apps it will reach,
// and — the part nobody expects — the Add Only apps it will not.

// --- update ------------------------------------------------------------------------

type prowlarrUpdateInput struct {
	Indexer     string `json:"indexer" jsonschema:"the indexer to change, by id or name — prowlarr_indexer_status lists them"`
	Enabled     *bool  `json:"enabled,omitempty" jsonschema:"false to stop using it everywhere, true to use it again"`
	Priority    *int   `json:"priority,omitempty" jsonschema:"1 to 50, lower preferred: when two indexers return the same release the *arrs take it from the lower number"`
	SyncProfile string `json:"sync_profile,omitempty" jsonschema:"the sync profile, by name or id — decides whether the *arrs use it for RSS, automatic search and interactive search. prowlarr_applications lists them"`
}

func handleProwlarrUpdate(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in prowlarrUpdateInput,
) (*sdk.CallToolResult, prowlarr.UpdateResult, error) {
	plan, err := prowlarr.PlanUpdate(ctx, prowlarr.UpdateRequest{
		Indexer:    in.Indexer,
		Enable:     in.Enabled,
		Priority:   in.Priority,
		AppProfile: in.SyncProfile,
	})
	if err != nil {
		return nil, prowlarr.UpdateResult{}, err
	}

	approved, pending, err := requireApproval(req, approval{
		message: prowlarrUpdateConfirmation(plan),
		fingerprint: fingerprint(
			"prowlarr_indexer_update",
			strconv.Itoa(plan.Indexer.ID),
			plan.Indexer.Name,
			plan.Summary(),
		),
		refusal: "no indexer was changed",
		subject: fmt.Sprintf("change prowlarr indexer %s (%s)", plan.Indexer.Name, plan.Summary()),
	})
	if !approved {
		return pending, prowlarr.UpdateResult{}, err
	}

	out, err := prowlarr.Update(ctx, plan)
	if err != nil {
		return nil, prowlarr.UpdateResult{}, err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "changed %s: %s\n", out.Indexer.Name, plan.Summary())
	fmt.Fprintf(&b, "now: %s, priority %d, profile %s\n",
		indexerStateCell(out.Indexer), out.Indexer.Priority, blank(out.Indexer.AppProfile))
	writeWarnings(&b, out.Warnings)

	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: b.String()}}}, out, nil
}

func prowlarrUpdateConfirmation(p prowlarr.UpdatePlan) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Change this Prowlarr indexer?\n\n")
	fmt.Fprintf(&b, "    %s   [indexer %d, %s/%s]\n", p.Indexer.Name, p.Indexer.ID, p.Indexer.Protocol, p.Indexer.Privacy)
	for _, c := range p.Changes {
		fmt.Fprintf(&b, "    %s: %s → %s\n", c.Field, c.From, c.To)
	}
	writeReach(&b, p.FullSyncApps, p.AddOnlyApps)
	writeNotes(&b, p.Warnings)
	return b.String()
}

// --- add ---------------------------------------------------------------------------

type prowlarrAddInput struct {
	Definition  string            `json:"definition" jsonschema:"the site definition, from prowlarr_indexer_definitions — call that first, and do not guess"`
	Name        string            `json:"name,omitempty" jsonschema:"what to call it; defaults to the site's name"`
	SyncProfile string            `json:"sync_profile,omitempty" jsonschema:"the sync profile by name or id; may be omitted only when Prowlarr has exactly one"`
	Priority    int               `json:"priority,omitempty" jsonschema:"1 to 50, lower preferred; default 25"`
	Tags        []string          `json:"tags,omitempty" jsonschema:"existing Prowlarr tags, by label. An app with tags only receives indexers sharing one, and a FlareSolverr proxy only serves indexers sharing its tag"`
	Settings    map[string]string `json:"settings,omitempty" jsonschema:"the definition's settings by name, as prowlarr_indexer_definitions lists them — credentials for a private site. A select takes the option's name"`
	Disabled    bool              `json:"disabled,omitempty" jsonschema:"add it switched off; Prowlarr then saves it without testing the login"`
}

func handleProwlarrAdd(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in prowlarrAddInput,
) (*sdk.CallToolResult, prowlarr.AddResult, error) {
	plan, err := prowlarr.PlanAdd(ctx, prowlarr.AddRequest{
		Definition: in.Definition,
		Name:       in.Name,
		AppProfile: in.SyncProfile,
		Priority:   in.Priority,
		Tags:       in.Tags,
		Settings:   in.Settings,
		Disabled:   in.Disabled,
	})
	if err != nil {
		return nil, prowlarr.AddResult{}, err
	}

	approved, pending, err := requireApproval(req, approval{
		message:     prowlarrAddConfirmation(plan),
		fingerprint: fingerprint("prowlarr_indexer_add", plan.Definition.Definition, plan.Key()),
		refusal:     "no indexer was added",
		subject:     "add prowlarr indexer " + plan.Name,
	})
	if !approved {
		return pending, prowlarr.AddResult{}, err
	}

	out, err := prowlarr.Add(ctx, plan)
	if err != nil {
		return nil, prowlarr.AddResult{}, err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "added %s   [indexer %d]\n", out.Indexer.Name, out.Indexer.ID)
	if out.Indexer.Protocol != "" {
		fmt.Fprintf(&b, "%s, priority %d, profile %s\n",
			indexerStateCell(out.Indexer), out.Indexer.Priority, blank(out.Indexer.AppProfile))
	}
	writeWarnings(&b, out.Warnings)

	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: b.String()}}}, out, nil
}

func prowlarrAddConfirmation(p prowlarr.AddPlan) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Add this indexer to Prowlarr?\n\n")
	fmt.Fprintf(&b, "    %s   [%s, %s/%s]\n", p.Name, p.Definition.Definition, p.Definition.Protocol, p.Definition.Privacy)
	fmt.Fprintf(&b, "    enabled: %s\n", yesNo(p.Enabled))
	fmt.Fprintf(&b, "    sync profile: %s\n", p.AppProfile)
	fmt.Fprintf(&b, "    priority: %d\n", p.Priority)
	if len(p.Tags) > 0 {
		fmt.Fprintf(&b, "    tags: %s\n", strings.Join(p.Tags, ", "))
	}
	for _, s := range p.Settings {
		fmt.Fprintf(&b, "    %s: %s\n", s.Name, s.Value)
	}
	if len(p.ReachesApps) > 0 {
		fmt.Fprintf(&b, "\nProwlarr will push it to %s.\n", strings.Join(p.ReachesApps, " and "))
	}
	if p.Enabled {
		b.WriteString("\nProwlarr tests the site before saving; a failed test means nothing is added.\n")
	}
	writeNotes(&b, p.Warnings)
	return b.String()
}

// --- remove ------------------------------------------------------------------------

type prowlarrRemoveInput struct {
	Indexer string `json:"indexer" jsonschema:"the indexer to delete, by id or name"`
}

func handleProwlarrRemove(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in prowlarrRemoveInput,
) (*sdk.CallToolResult, prowlarr.RemoveResult, error) {
	plan, err := prowlarr.PlanRemove(ctx, in.Indexer)
	if err != nil {
		return nil, prowlarr.RemoveResult{}, err
	}

	approved, pending, err := requireApproval(req, approval{
		message: prowlarrRemoveConfirmation(plan),
		fingerprint: fingerprint(
			"prowlarr_indexer_remove",
			strconv.Itoa(plan.Indexer.ID),
			plan.Indexer.Name,
			strings.Join(plan.RemovedFrom, ","),
		),
		refusal: "no indexer was removed",
		subject: "remove prowlarr indexer " + plan.Indexer.Name,
	})
	if !approved {
		return pending, prowlarr.RemoveResult{}, err
	}

	out, err := prowlarr.Remove(ctx, plan)
	if err != nil {
		return nil, prowlarr.RemoveResult{}, err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "removed %s from prowlarr; %d enabled indexer(s) remain\n", out.Removed.Name, out.Remaining)
	writeWarnings(&b, out.Warnings)

	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: b.String()}}}, out, nil
}

func prowlarrRemoveConfirmation(p prowlarr.RemovePlan) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Delete this indexer from Prowlarr?\n\n")
	fmt.Fprintf(&b, "    %s   [indexer %d, %s/%s, %s]\n", p.Indexer.Name, p.Indexer.ID,
		p.Indexer.Protocol, p.Indexer.Privacy, indexerStateCell(p.Indexer))
	if len(p.RemovedFrom) > 0 {
		fmt.Fprintf(&b, "\nIt is removed from %s too — every synced app, Add Only included.\n",
			strings.Join(p.RemovedFrom, " and "))
	}
	b.WriteString("\nIts settings, credentials included, are deleted with it.\n")
	writeNotes(&b, p.Warnings)
	return b.String()
}

// --- sync --------------------------------------------------------------------------

type prowlarrSyncInput struct {
	Force bool `json:"force,omitempty" jsonschema:"rewrite every synced indexer in the apps even when Prowlarr thinks it is unchanged — overwrites anything edited in Radarr or Sonarr by hand"`
}

func handleProwlarrSync(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in prowlarrSyncInput,
) (*sdk.CallToolResult, prowlarr.SyncResult, error) {
	plan, err := prowlarr.PlanSync(ctx, in.Force)
	if err != nil {
		return nil, prowlarr.SyncResult{}, err
	}

	approved, pending, err := requireApproval(req, approval{
		message: prowlarrSyncConfirmation(plan),
		fingerprint: fingerprint(
			"prowlarr_apps_sync",
			strconv.FormatBool(plan.Force),
			strings.Join(plan.FullSyncApps, ","),
			strings.Join(plan.AddOnlyApps, ","),
			strconv.Itoa(plan.EnabledIndexers),
		),
		refusal: "no sync was started",
		subject: "sync prowlarr indexers to the apps",
	})
	if !approved {
		return pending, prowlarr.SyncResult{}, err
	}

	out, err := prowlarr.Sync(ctx, plan)
	if err != nil {
		return nil, prowlarr.SyncResult{}, err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "prowlarr is syncing %d enabled indexer(s) to the apps\n", plan.EnabledIndexers)
	fmt.Fprintf(&b, "command %d: %s\n", out.CommandID, blank(out.CommandStatus))
	writeWarnings(&b, out.Warnings)

	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: b.String()}}}, out, nil
}

func prowlarrSyncConfirmation(p prowlarr.SyncPlan) string {
	var b strings.Builder

	if p.Force {
		b.WriteString("Force-sync every Prowlarr indexer to the apps now?\n\n")
	} else {
		b.WriteString("Sync Prowlarr's indexers to the apps now?\n\n")
	}
	fmt.Fprintf(&b, "    enabled indexers: %d\n", p.EnabledIndexers)
	if len(p.FullSyncApps) > 0 {
		fmt.Fprintf(&b, "    full sync: %s\n", strings.Join(p.FullSyncApps, ", "))
	}
	if len(p.AddOnlyApps) > 0 {
		fmt.Fprintf(&b, "    add only: %s\n", strings.Join(p.AddOnlyApps, ", "))
	}
	if len(p.Disabled) > 0 {
		fmt.Fprintf(&b, "    not synced: %s\n", strings.Join(p.Disabled, ", "))
	}
	writeNotes(&b, p.Warnings)
	return b.String()
}

// --- shared --------------------------------------------------------------------------

func writeReach(b *strings.Builder, full, addOnly []string) {
	if len(full) > 0 {
		fmt.Fprintf(b, "\nProwlarr pushes this to %s straight away.\n", strings.Join(full, " and "))
	}
	if len(addOnly) > 0 {
		fmt.Fprintf(b, "\n%s will NOT receive it (Add Only).\n", strings.Join(addOnly, " and "))
	}
	if len(full) == 0 && len(addOnly) == 0 {
		b.WriteString("\nNo app receives this indexer, so the change stays in Prowlarr.\n")
	}
}

func writeNotes(b *strings.Builder, notes []string) {
	for _, n := range notes {
		fmt.Fprintf(b, "\nNote: %s\n", n)
	}
}

func writeWarnings(b *strings.Builder, warnings []string) {
	for _, w := range warnings {
		fmt.Fprintf(b, "\nwarning: %s\n", w)
	}
}
