package mcp

import (
	"context"
	"fmt"
	"strings"
	"time"

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
		// An empty list is a result, not a failure — say so with the counts, so
		// "nothing is broken" never reads as "the tool returned nothing".
		fmt.Fprintf(&b, "no services needing attention (%d units, %d active, %d failed)\n",
			status.TotalCount, status.ActiveCount, status.FailedCount)
		return b.String()
	}

	type row struct{ unit, load, active, sub, restarts, since string }

	head := row{"UNIT", "LOAD", "ACTIVE", "SUB", "RESTARTS", "FOR"}

	rows := make([]row, 0, len(status.Units))
	for _, u := range status.Units {
		rows = append(rows, row{
			unit:     u.Name,
			load:     u.LoadState,
			active:   u.ActiveState,
			sub:      u.SubState,
			restarts: fmt.Sprintf("%d", u.Restarts),
			since:    compactDuration(u.StateForSeconds),
		})
	}

	// Same two-pass measure-then-write as the disk table: widths are only
	// knowable after seeing every row.
	w := [5]int{len(head.unit), len(head.load), len(head.active), len(head.sub), len(head.restarts)}
	for _, r := range rows {
		w[0] = max(w[0], len(r.unit))
		w[1] = max(w[1], len(r.load))
		w[2] = max(w[2], len(r.active))
		w[3] = max(w[3], len(r.sub))
		w[4] = max(w[4], len(r.restarts))
	}

	write := func(r row) {
		fmt.Fprintf(&b, "%-*s  %-*s  %-*s  %-*s  %*s  %s\n",
			w[0], r.unit,
			w[1], r.load,
			w[2], r.active,
			w[3], r.sub,
			w[4], r.restarts,
			r.since)
	}

	write(head)
	for _, r := range rows {
		write(r)
	}

	// Footer: what a plain status check would not tell you. Computed in the
	// services package, so structuredContent carries these too.
	for _, warn := range status.Warnings {
		fmt.Fprintf(&b, "\nwarning: %s\n", warn)
	}
	if status.SkippedCount > 0 {
		fmt.Fprintf(&b, "\n(%d healthy units omitted; use include_all to see them)\n",
			status.SkippedCount)
	}

	return b.String()
}

// compactDuration keeps the age column to a few characters — a table is no
// place for "52h4m28.5s", which is what time.Duration would render.
func compactDuration(seconds uint64) string {
	if seconds == 0 {
		return "-"
	}
	d := time.Duration(seconds) * time.Second
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
	}
}
