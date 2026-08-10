package mcp

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DeLucca990/homelab-mcp/internal/radarr"
	"github.com/DeLucca990/homelab-mcp/internal/system"
)

// RADARR QUEUE REMOVAL TOOL
//
// Removing a queue item throws away a partial download, and with blocklist set
// it also decides that a release is never to be grabbed again. Both go through
// the confirmation flow.
//
// The fingerprint is doing real work here. A Radarr queue id is assigned per
// refresh, so the same number can point at a different download a minute later.
// The item is therefore resolved from the live queue on both passes and the
// title goes into the hash: if the queue moved between the confirmation and the
// retry, the fingerprints differ and the removal is refused instead of landing
// on whatever now holds that id.

type queueRemoveInput struct {
	QueueID int `json:"queue_id" jsonschema:"id of the queue item to remove, from a fresh radarr_queue_status. These ids are reassigned when the queue refreshes, so do not reuse one from earlier in the conversation"`

	RemoveFromClient *bool `json:"remove_from_client,omitempty" jsonschema:"also delete the download from the download client; defaults to true. False removes the row from Radarr while the client keeps downloading the file"`
	Blocklist        *bool `json:"blocklist,omitempty" jsonschema:"also block this release so Radarr never grabs it again; defaults to false. Use it for a release that is broken or keeps failing to import, not for one you simply do not want right now"`
	SkipRedownload   *bool `json:"skip_redownload,omitempty" jsonschema:"stop Radarr from immediately searching for a replacement; defaults to false"`
}

func handleRadarrQueueRemove(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in queueRemoveInput,
) (*sdk.CallToolResult, radarr.RemoveResult, error) {
	if in.QueueID <= 0 {
		return nil, radarr.RemoveResult{}, fmt.Errorf(
			"'queue_id' is required — take it from radarr_queue_status")
	}

	opts := radarr.RemoveOptions{
		QueueID:          in.QueueID,
		RemoveFromClient: boolOr(in.RemoveFromClient, true),
		Blocklist:        boolOr(in.Blocklist, false),
		SkipRedownload:   boolOr(in.SkipRedownload, false),
	}

	item, err := radarr.FindQueueItem(ctx, in.QueueID)
	if err != nil {
		return nil, radarr.RemoveResult{}, err
	}

	approved, pending, err := requireApproval(req, approval{
		message: queueRemoveMessage(item, opts),
		fingerprint: fingerprint(
			"radarr_queue_remove",
			strconv.Itoa(item.ID),
			item.DisplayName(),
			item.Release,
			strconv.FormatBool(opts.RemoveFromClient),
			strconv.FormatBool(opts.Blocklist),
			strconv.FormatBool(opts.SkipRedownload),
		),
		refusal: "queue item not removed",
		subject: fmt.Sprintf("remove queue item %d (%s) from radarr", item.ID, item.DisplayName()),
	})
	if !approved {
		return pending, radarr.RemoveResult{}, err
	}

	out, err := radarr.RemoveFromQueue(ctx, item, opts)
	if err != nil {
		return nil, radarr.RemoveResult{}, err
	}

	return &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.TextContent{Text: renderQueueRemoveResult(out)},
		},
	}, out, nil
}

// States what is being thrown away and how far along it was: 4% of a download
// and 96% of one are the same request and very different losses.
func queueRemoveMessage(i radarr.QueueItem, opts radarr.RemoveOptions) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Remove this download from the Radarr queue?\n\n")
	fmt.Fprintf(&b, "    %s\n", i.DisplayName())
	if i.Release != "" && i.Release != i.DisplayName() {
		fmt.Fprintf(&b, "    release: %s\n", i.Release)
	}
	fmt.Fprintf(&b, "    progress: %.0f%%", i.Percent)
	if i.SizeLeftBytes > 0 {
		fmt.Fprintf(&b, " (%s still to come)", system.CompactBytes(i.SizeLeftBytes))
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "    status: %s\n", queueStatusCell(i))

	if opts.RemoveFromClient {
		fmt.Fprintf(&b, "\nThe partial download will be deleted from %s. "+
			"Anything already downloaded is lost.\n", nonEmpty(i.DownloadClient, "the download client"))
	} else {
		b.WriteString("\nThe download client will keep downloading this; " +
			"only Radarr's queue entry goes away.\n")
	}

	if opts.Blocklist {
		b.WriteString("\nThis release will also be BLOCKLISTED — Radarr will never grab it again.\n")
	}
	if opts.SkipRedownload {
		b.WriteString("\nNo replacement will be searched for.\n")
	}

	return b.String()
}

func renderQueueRemoveResult(r radarr.RemoveResult) string {
	var b strings.Builder

	name := r.Movie
	if name == "" {
		name = r.Release
	}

	fmt.Fprintf(&b, "removed queue item %d (%s)\n", r.QueueID, name)
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
