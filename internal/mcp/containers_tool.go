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
		if status.TotalCount == 0 {
			return "no containers on this host\n"
		}
		fmt.Fprintf(&b, "no containers needing attention (%d total, %d running)\n",
			status.TotalCount, status.RunningCount)
		return b.String()
	}

	cols := []column{
		{"NAME", alignLeft},
		{"IMAGE", alignLeft},
		{"STATE", alignLeft},
		{"HEALTH", alignLeft},
		{"RESTARTS", alignRight},
		{"FOR", alignLeft},
		{"PORTS", alignLeft},
	}

	rows := make([][]string, 0, len(status.Containers))
	for _, c := range status.Containers {
		health := c.Health
		if health == "" {
			health = "-"
		}
		state := c.State
		if c.OOMKilled {
			state += " (OOM)"
		} else if c.State == "exited" && c.ExitCode != 0 {
			state = fmt.Sprintf("exited (%d)", c.ExitCode)
		}

		rows = append(rows, []string{
			c.Name,
			c.Image,
			state,
			health,
			fmt.Sprintf("%d", c.RestartCount),
			compactDuration(c.StateForSeconds),
			formatPorts(c.Ports),
		})
	}

	b.WriteString(table(cols, rows))

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

func formatPorts(ports []containers.Port) string {
	parts := make([]string, 0, len(ports))
	for _, p := range ports {
		switch {
		case p.HostPort == 0:
			continue
		case p.HostPort == p.ContainerPort:
			parts = append(parts, fmt.Sprintf("%d/%s", p.HostPort, p.Protocol))
		default:
			parts = append(parts, fmt.Sprintf("%d->%d/%s", p.HostPort, p.ContainerPort, p.Protocol))
		}
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, ",")
}
