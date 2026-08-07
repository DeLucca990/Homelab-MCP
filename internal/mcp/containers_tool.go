package mcp

import (
	"context"
	"fmt"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DeLucca990/homelab-mcp/internal/containers"
)

// DOCKER CONTAINERS TOOL
type containersInput struct {
	Names      []string `json:"names,omitempty" jsonschema:"specific container names to inspect; these are always reported whatever their state. When empty, running containers plus anything needing attention are returned"`
	IncludeAll bool     `json:"include_all,omitempty" jsonschema:"if true, also returns containers that stopped cleanly"`
}

func handleContainerStatus(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in containersInput,
) (*sdk.CallToolResult, containers.ContainerStatus, error) {
	status, err := containers.GetContainerStatus(ctx, in.Names, in.IncludeAll)
	if err != nil {
		return nil, containers.ContainerStatus{}, err
	}
	return &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.TextContent{Text: renderContainerTable(status)},
		},
	}, status, nil
}

func renderContainerTable(status containers.ContainerStatus) string {
	var b strings.Builder

	if len(status.Containers) == 0 {
		// An empty list is a result, not a failure.
		if status.TotalCount == 0 {
			return "no containers on this host\n"
		}
		fmt.Fprintf(&b, "no containers needing attention (%d total, %d running)\n",
			status.TotalCount, status.RunningCount)
		return b.String()
	}

	type row struct{ name, image, state, health, restarts, since string }

	head := row{"NAME", "IMAGE", "STATE", "HEALTH", "RESTARTS", "FOR"}

	rows := make([]row, 0, len(status.Containers))
	for _, c := range status.Containers {
		health := c.Health
		if health == "" {
			health = "-"
		}
		state := c.State
		if c.OOMKilled {
			// The single most load-bearing fact about this container, and the
			// one no listing shows — it does not survive being a column alone.
			state += " (OOM)"
		} else if c.State == "exited" && c.ExitCode != 0 {
			state = fmt.Sprintf("exited (%d)", c.ExitCode)
		}

		rows = append(rows, row{
			name:     c.Name,
			image:    c.Image,
			state:    state,
			health:   health,
			restarts: fmt.Sprintf("%d", c.RestartCount),
			since:    compactDuration(c.StateForSeconds),
		})
	}

	// Same two-pass measure-then-write as the disk and service tables.
	w := [5]int{len(head.name), len(head.image), len(head.state), len(head.health), len(head.restarts)}
	for _, r := range rows {
		w[0] = max(w[0], len(r.name))
		w[1] = max(w[1], len(r.image))
		w[2] = max(w[2], len(r.state))
		w[3] = max(w[3], len(r.health))
		w[4] = max(w[4], len(r.restarts))
	}

	write := func(r row) {
		fmt.Fprintf(&b, "%-*s  %-*s  %-*s  %-*s  %*s  %s\n",
			w[0], r.name,
			w[1], r.image,
			w[2], r.state,
			w[3], r.health,
			w[4], r.restarts,
			r.since)
	}

	write(head)
	for _, r := range rows {
		write(r)
	}

	// Footer: what `docker ps` would not tell you. Computed in the containers
	// package, so structuredContent carries these too.
	for _, warn := range status.Warnings {
		fmt.Fprintf(&b, "\nwarning: %s\n", warn)
	}
	if status.SkippedCount > 0 {
		fmt.Fprintf(&b, "\n(%d cleanly-stopped containers omitted; use include_all to see them)\n",
			status.SkippedCount)
	}

	return b.String()
}
