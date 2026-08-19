package mcp

import (
	"context"
	"fmt"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DeLucca990/homelab-mcp/internal/services"
)

// SYSTEMD SERVICES TOOL
type servicesInput struct {
	Units      []string `json:"units,omitempty" jsonschema:"specific unit names to inspect, e.g. nginx.service; these are always reported whatever their state. When empty, every service unit is scanned and only those needing attention are returned"`
	IncludeAll bool     `json:"include_all,omitempty" jsonschema:"if true, returns every service unit instead of only the ones needing attention"`
}

func handleServiceStatus(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in servicesInput,
) (*sdk.CallToolResult, services.ServiceStatus, error) {
	status, err := services.GetServiceStatus(ctx, in.Units, in.IncludeAll)
	if err != nil {
		return nil, services.ServiceStatus{}, err
	}
	return &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.TextContent{Text: renderServiceTable(status)},
		},
	}, status, nil
}

func renderServiceTable(status services.ServiceStatus) string {
	var b strings.Builder

	if len(status.Units) == 0 {
		fmt.Fprintf(&b, "no services needing attention (%d units, %d active, %d failed)\n",
			status.TotalCount, status.ActiveCount, status.FailedCount)
		return b.String()
	}

	cols := []column{
		{"UNIT", alignLeft},
		{"LOAD", alignLeft},
		{"ACTIVE", alignLeft},
		{"SUB", alignLeft},
		{"RESTARTS", alignRight},
		{"FOR", alignLeft},
	}

	rows := make([][]string, 0, len(status.Units))
	for _, u := range status.Units {
		rows = append(rows, []string{
			u.Name,
			u.LoadState,
			u.ActiveState,
			u.SubState,
			fmt.Sprintf("%d", u.Restarts),
			compactDuration(u.StateForSeconds),
		})
	}

	b.WriteString(table(cols, rows))

	for _, warn := range status.Warnings {
		fmt.Fprintf(&b, "\nwarning: %s\n", warn)
	}
	if status.SkippedCount > 0 {
		fmt.Fprintf(&b, "\n(%d healthy units omitted; use include_all to see them)\n",
			status.SkippedCount)
	}

	return b.String()
}
