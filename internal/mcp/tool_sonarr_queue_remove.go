package mcp

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DeLucca990/homelab-mcp/internal/sonarr"
	"github.com/DeLucca990/homelab-mcp/internal/system"
)

// SONARR QUEUE REMOVAL TOOL
//
// Removing a queue item throws away a partial download, and with blocklist set
// it also decides that a release is never to be grabbed again. Both go through
// the confirmation flow.
//
// The fingerprint is doing real work here. A Sonarr queue id is assigned per
// refresh, so the same number can point at a different download a minute later.
// The item is therefore resolved from the live queue on both passes and the
// title goes into the hash: if the queue moved between the confirmation and the
// retry, the fingerprints differ and the removal is refused instead of landing
// on whatever now holds that id.
//
// The Sonarr-specific part is the blast radius. One download can be a season
// pack occupying a dozen queue rows, and removing any one row removes the file
// behind all of them — so the confirmation counts the episodes that go with it,
// and that count is part of the fingerprint too.

type sonarrQueueRemoveInput struct {
	QueueID int `json:"queue_id" jsonschema:"id of the queue item to remove, from a fresh sonarr_queue_status. These ids are reassigned when the queue refreshes, so do not reuse one from earlier in the conversation"`

	RemoveFromClient *bool `json:"remove_from_client,omitempty" jsonschema:"also delete the download from the download client; defaults to true. False removes the row from Sonarr while the client keeps downloading the file"`
	Blocklist        *bool `json:"blocklist,omitempty" jsonschema:"also block this release so Sonarr never grabs it again; defaults to false. Use it for a release that is broken or keeps failing to import, not for one you simply do not want right now"`
	SkipRedownload   *bool `json:"skip_redownload,omitempty" jsonschema:"stop Sonarr from immediately searching for a replacement; defaults to false"`
}

func handleSonarrQueueRemove(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in sonarrQueueRemoveInput,
) (*sdk.CallToolResult, sonarr.RemoveResult, error) {
	if in.QueueID <= 0 {
		return nil, sonarr.RemoveResult{}, fmt.Errorf(
			"'queue_id' is required — take it from sonarr_queue_status")
	}

	opts := sonarr.RemoveOptions{
		QueueID:          in.QueueID,
		RemoveFromClient: boolOr(in.RemoveFromClient, true),
		Blocklist:        boolOr(in.Blocklist, false),
		SkipRedownload:   boolOr(in.SkipRedownload, false),
	}

	item, siblings, err := sonarr.FindQueueItem(ctx, in.QueueID)
	if err != nil {
		return nil, sonarr.RemoveResult{}, err
	}

	approved, pending, err := requireApproval(req, approval{
		message: sonarrQueueRemoveMessage(item, siblings, opts),
		fingerprint: fingerprint(
			"sonarr_queue_remove",
			strconv.Itoa(item.ID),
			item.DisplayName(),
			item.Release,
			strconv.Itoa(len(siblings)),
			strconv.FormatBool(opts.RemoveFromClient),
			strconv.FormatBool(opts.Blocklist),
			strconv.FormatBool(opts.SkipRedownload),
		),
		refusal: "queue item not removed",
		subject: fmt.Sprintf("remove queue item %d (%s) from sonarr", item.ID, item.DisplayName()),
	})
	if !approved {
		return pending, sonarr.RemoveResult{}, err
	}

	out, err := sonarr.RemoveFromQueue(ctx, item, siblings, opts)
	if err != nil {
		return nil, sonarr.RemoveResult{}, err
	}

	return &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.TextContent{Text: renderSonarrQueueRemoveResult(out)},
		},
	}, out, nil
}

// States what is being thrown away, how far along it was, and how many episodes
// go with it: 4% of a download and 96% of one are the same request and very
// different losses, and one episode and a fourteen-episode season pack are the
// same row.
func sonarrQueueRemoveMessage(
	i sonarr.QueueItem,
	siblings []sonarr.QueueItem,
	opts sonarr.RemoveOptions,
) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Remove this download from the Sonarr queue?\n\n")
	fmt.Fprintf(&b, "    %s\n", i.DisplayName())
	if i.Release != "" && i.Release != i.DisplayName() {
		fmt.Fprintf(&b, "    release: %s\n", i.Release)
	}
	fmt.Fprintf(&b, "    progress: %.0f%%", i.Percent)
	if i.SizeLeftBytes > 0 {
		fmt.Fprintf(&b, " (%s still to come)", system.CompactBytes(i.SizeLeftBytes))
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "    status: %s\n", sonarrQueueStatusCell(i))

	// The row is one episode; the file may be a season.
	if len(siblings) > 0 {
		fmt.Fprintf(&b, "\nTHIS DOWNLOAD HOLDS %d EPISODES — one file, %d queue rows, and "+
			"removing this row removes all of them: %s\n",
			len(siblings)+1, len(siblings)+1, siblingCodes(i, siblings))
	}

	if opts.RemoveFromClient {
		fmt.Fprintf(&b, "\nThe partial download will be deleted from %s. "+
			"Anything already downloaded is lost.\n", nonEmpty(i.DownloadClient, "the download client"))
	} else {
		b.WriteString("\nThe download client will keep downloading this; " +
			"only Sonarr's queue entry goes away.\n")
	}

	if opts.Blocklist {
		b.WriteString("\nThis release will also be BLOCKLISTED — Sonarr will never grab it again.\n")
	}
	if opts.SkipRedownload {
		b.WriteString("\nNo replacement will be searched for.\n")
	}

	return b.String()
}

// siblingCodes lists the episodes riding on the same file, capped so a
// forty-episode pack does not fill the confirmation. Rows the download client
// holds that Sonarr cannot match to an episode have no code, and are counted
// rather than rendered as a run of commas.
func siblingCodes(item sonarr.QueueItem, siblings []sonarr.QueueItem) string {
	var codes []string
	unnamed := 0

	for _, i := range append([]sonarr.QueueItem{item}, siblings...) {
		switch code := i.EpisodeCode(); {
		case code == "":
			unnamed++
		case len(codes) == 10:
			unnamed++
		default:
			codes = append(codes, code)
		}
	}

	if unnamed > 0 {
		codes = append(codes, fmt.Sprintf("and %d more", unnamed))
	}
	return strings.Join(codes, ", ")
}

func renderSonarrQueueRemoveResult(r sonarr.RemoveResult) string {
	var b strings.Builder

	name := strings.TrimSpace(r.Series + " " + r.Episode)
	if name == "" {
		name = r.Release
	}

	fmt.Fprintf(&b, "removed queue item %d (%s)\n", r.QueueID, name)
	if r.EpisodesAffected > 1 {
		fmt.Fprintf(&b, "that download held %d episodes, and all of their queue rows are gone\n",
			r.EpisodesAffected)
	}
	fmt.Fprintf(&b, "deleted from the download client: %s   blocklisted: %s   replacement search skipped: %s\n",
		yesNo(r.RemovedFromClient), yesNo(r.Blocklisted), yesNo(r.SkipRedownload))

	if r.StillQueued {
		b.WriteString("\nit is still showing in the queue\n")
	}

	for _, w := range r.Warnings {
		fmt.Fprintf(&b, "\nwarning: %s\n", w)
	}

	return b.String()
}
