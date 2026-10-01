package mcp

import (
	"context"
	"fmt"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DeLucca990/homelab-mcp/internal/prowlarr"
)

// PROWLARR APPLICATIONS TOOL
//
// Where Prowlarr's indexers actually end up: each app, its sync level, its
// tags, and the indexers that reach it under Prowlarr's own rules. Read-only;
// the optional test only checks that Prowlarr can reach each app.

type prowlarrAppsInput struct {
	Test bool `json:"test,omitempty" jsonschema:"also have Prowlarr test its connection to every app — the answer to 'Prowlarr changed it but Radarr never got it'"`
}

func handleProwlarrApps(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in prowlarrAppsInput,
) (*sdk.CallToolResult, prowlarr.Applications, error) {
	out, err := prowlarr.GetApplications(ctx, in.Test)
	if err != nil {
		return nil, prowlarr.Applications{}, err
	}
	return &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.TextContent{Text: renderProwlarrApps(out)},
		},
	}, out, nil
}

func renderProwlarrApps(a prowlarr.Applications) string {
	var b strings.Builder

	if len(a.Applications) > 0 {
		cols := []column{
			{"APP", alignLeft},
			{"TYPE", alignLeft},
			{"SYNC", alignLeft},
			{"TAGS", alignLeft},
			{"TEST", alignLeft},
			{"RECEIVES", alignLeft},
		}
		rows := make([][]string, 0, len(a.Applications))
		for _, app := range a.Applications {
			test := "-"
			if app.Tested {
				test = "passed"
				if !app.TestPassed {
					test = "FAILED"
				}
			}
			rows = append(rows, []string{
				app.Name,
				app.Implementation,
				app.SyncLevel,
				blank(strings.Join(app.Tags, ",")),
				test,
				fmt.Sprintf("%d: %s", len(app.Receives), blank(strings.Join(app.Receives, ", "))),
			})
		}
		b.WriteString(table(cols, rows))
	}

	if len(a.Profiles) > 0 {
		b.WriteString("\nsync profiles:\n")
		for _, p := range a.Profiles {
			fmt.Fprintf(&b, "  %s (id %d): %s\n", p.Name, p.ID, p.Describe())
		}
	}

	for _, w := range a.Warnings {
		fmt.Fprintf(&b, "\nwarning: %s\n", w)
	}

	return b.String()
}
